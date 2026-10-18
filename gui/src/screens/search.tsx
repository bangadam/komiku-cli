import {
  useEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from "react"
import { ArrowRight, Link, Search, X } from "lucide-react"

import { Button } from "@/components/ui/button"
import { PageHeader, SeriesCover } from "@/components/shared"
import {
  BrowserOnlyNotice,
  EmptyState,
  ErrorState,
  LoadingState,
} from "@/components/states"
import {
  gradientFor,
  initialsFromTitle,
  searchSeries,
  type LibraryReport,
  type SearchHit,
} from "@/lib/api"
import { hasCliBridge } from "@/lib/cli"
import { useAsync } from "@/lib/use-async"
import { cn } from "@/lib/utils"

type FilterKey = "all" | "library"

const filterOrder: FilterKey[] = ["all", "library"]

const filterLabels: Record<FilterKey, string> = {
  all: "All results",
  library: "In library",
}

const DEBOUNCE_MS = 450

/** Derive a series slug from a pasted URL, or null when it is not a URL. */
function slugFromUrlQuery(value: string): string | null {
  const trimmed = value.trim()
  if (!/^https?:\/\//i.test(trimmed)) return null
  try {
    const parsed = new URL(trimmed)
    const segments = parsed.pathname.split("/").filter(Boolean)
    if (segments.length === 0) return null
    return segments[segments.length - 1]
  } catch {
    return null
  }
}

function urlLabel(value: string): string {
  try {
    const parsed = new URL(value)
    return `${parsed.host}${parsed.pathname}`
  } catch {
    return value
  }
}

export function SearchScreen({
  onOpenSeries,
  library,
}: {
  onOpenSeries: (slug: string, url?: string) => void
  library: LibraryReport | null
}) {
  const [query, setQuery] = useState("")
  const [debounced, setDebounced] = useState("")
  const [filter, setFilter] = useState<FilterKey>("all")
  const inputRef = useRef<HTMLInputElement>(null)
  const debounceRef = useRef<number | null>(null)

  useEffect(() => {
    return () => {
      if (debounceRef.current !== null) {
        window.clearTimeout(debounceRef.current)
      }
    }
  }, [])

  function scheduleSearch(value: string) {
    if (debounceRef.current !== null) {
      window.clearTimeout(debounceRef.current)
      debounceRef.current = null
    }
    const trimmed = value.trim()
    if (!trimmed || slugFromUrlQuery(trimmed)) {
      setDebounced("")
      return
    }
    debounceRef.current = window.setTimeout(
      () => setDebounced(trimmed),
      DEBOUNCE_MS
    )
  }

  function handleQueryChange(value: string) {
    setQuery(value)
    scheduleSearch(value)
  }

  useEffect(() => {
    function handleKeyDown(event: globalThis.KeyboardEvent) {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault()
        inputRef.current?.focus()
        inputRef.current?.select()
      }
    }

    window.addEventListener("keydown", handleKeyDown)
    return () => window.removeEventListener("keydown", handleKeyDown)
  }, [])

  const urlSlug = slugFromUrlQuery(query)

  const results = useAsync<SearchHit[]>(
    () =>
      hasCliBridge && debounced ? searchSeries(debounced) : Promise.resolve([]),
    [debounced]
  )

  const librarySlugs = useMemo(
    () => new Set((library?.series ?? []).map((item) => item.slug)),
    [library]
  )

  const visibleResults = useMemo(
    () =>
      (results.data ?? []).filter((hit) =>
        filter === "library" ? librarySlugs.has(hit.Slug) : true
      ),
    [results.data, filter, librarySlugs]
  )

  const trimmedQuery = query.trim()
  const pending =
    trimmedQuery.length > 0 && trimmedQuery !== debounced && !urlSlug
  const searching = pending || (results.loading && debounced.length > 0)

  function clearQuery() {
    if (debounceRef.current !== null) {
      window.clearTimeout(debounceRef.current)
      debounceRef.current = null
    }
    setQuery("")
    setDebounced("")
    inputRef.current?.focus()
  }

  function handleInputKeyDown(event: KeyboardEvent<HTMLInputElement>) {
    if (event.key !== "Enter") return
    const slug = slugFromUrlQuery(query)
    if (!slug) return
    event.preventDefault()
    onOpenSeries(slug, query.trim())
  }

  return (
    <div className="flex h-full flex-col gap-5 overflow-hidden px-8 py-7">
      <BrowserOnlyNotice />

      <PageHeader
        title="Search"
        subtitle="Find a series by title, or paste a Komiku URL to open it directly"
      >
        <span className="rounded-[6px] bg-muted px-2 py-1 font-mono text-[11px] text-dim">
          ⌘K
        </span>
      </PageHeader>

      {!hasCliBridge ? (
        <EmptyState
          title="Open the desktop app to search komiku.org"
          hint="The browser preview cannot reach the komiku-cli binary."
          icon={Search}
        />
      ) : (
        <>
          <div className="flex h-[52px] shrink-0 items-center gap-3 rounded-[8px] border border-primary bg-surface px-4">
            <Search className="size-[18px] text-primary" strokeWidth={1.8} />
            <input
              ref={inputRef}
              value={query}
              onChange={(event) => handleQueryChange(event.target.value)}
              onKeyDown={handleInputKeyDown}
              placeholder="Search a title or paste a komiku URL"
              spellCheck={false}
              autoFocus
              className="min-w-0 flex-1 bg-transparent text-[15px] text-foreground outline-none placeholder:text-dim"
            />
            <button
              type="button"
              aria-label="Clear search"
              onClick={clearQuery}
              className="flex size-5 shrink-0 items-center justify-center rounded-full bg-muted"
            >
              <X
                className="size-[11px] text-muted-foreground"
                strokeWidth={1.8}
              />
            </button>
          </div>

          <div className="flex shrink-0 items-center gap-2">
            {filterOrder.map((key) => (
              <button
                key={key}
                type="button"
                aria-pressed={filter === key}
                onClick={() => setFilter(key)}
                className={cn(
                  "h-8 rounded-full border px-3 text-[12px] font-medium transition-colors",
                  filter === key
                    ? "border-primary bg-primary-soft text-primary"
                    : "border-border bg-surface text-muted-foreground hover:text-foreground"
                )}
              >
                {filterLabels[key]}
              </button>
            ))}
            <div className="flex-1" />
            <span className="font-mono text-[12px] text-dim">
              {searching
                ? "Searching…"
                : trimmedQuery
                  ? `${visibleResults.length} result${
                      visibleResults.length === 1 ? "" : "s"
                    } for “${trimmedQuery}”`
                  : "Type to search"}
            </span>
          </div>

          <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto">
            {searching ? (
              <LoadingState label="Searching komiku.org…" />
            ) : results.error ? (
              <ErrorState
                message={results.error}
                onRetry={results.reload}
              />
            ) : !trimmedQuery ? (
              <EmptyState
                title="Search a series"
                hint="Type a title or paste a komiku.org/manga/… URL"
                icon={Search}
              />
            ) : urlSlug ? (
              <EmptyState
                title="Press Enter to open this series"
                hint={urlSlug}
                icon={Link}
              />
            ) : visibleResults.length === 0 ? (
              <EmptyState
                title="No series found"
                hint={
                  filter === "all"
                    ? "Try a different title or paste a URL"
                    : "Try another filter or search a different title"
                }
                icon={Search}
              />
            ) : (
              visibleResults.map((hit) => {
                const owned = library?.series?.find(
                  (item) => item.slug === hit.Slug
                )
                return (
                  <div
                    key={hit.Slug}
                    className="flex items-center gap-3.5 rounded-[8px] border border-border-subtle bg-surface p-3"
                  >
                    <SeriesCover
                      initials={initialsFromTitle(hit.Title)}
                      gradient={gradientFor(hit.Slug)}
                      coverPath={owned?.cover}
                      size="md"
                    />
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-[14px] font-medium">
                        {hit.Title}
                      </div>
                      <div className="mt-0.5 truncate font-mono text-[11px] text-dim">
                        {urlLabel(hit.URL)}
                      </div>
                      {owned && (
                        <div className="mt-0.5 truncate text-[11px] text-muted-foreground">
                          You have {owned.done} of {owned.chapters} chapters ·{" "}
                          {owned.cbz} CBZ
                        </div>
                      )}
                    </div>
                    <span
                      className={cn(
                        "shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium",
                        librarySlugs.has(hit.Slug)
                          ? "bg-success-soft text-success"
                          : "bg-muted text-muted-foreground"
                      )}
                    >
                      {librarySlugs.has(hit.Slug)
                        ? "In library"
                        : "Not in library"}
                    </span>
                    <Button
                      variant="secondary"
                      size="sm"
                      className="h-8 px-3 text-[12px]"
                      onClick={() => onOpenSeries(hit.Slug, hit.URL)}
                    >
                      Open
                      <ArrowRight className="size-3" strokeWidth={1.8} />
                    </Button>
                  </div>
                )
              })
            )}
          </div>

          <div className="flex shrink-0 items-center gap-2.5 rounded-[8px] border border-border-subtle bg-muted p-3">
            <Link className="size-3.5 shrink-0 text-dim" strokeWidth={1.8} />
            <span className="text-[12px] text-muted-foreground">
              Tip: paste any komiku.org/manga/… URL here and press Enter to open
              its chapter list.
            </span>
          </div>
        </>
      )}
    </div>
  )
}
