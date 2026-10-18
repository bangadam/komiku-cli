import { useMemo, useState, useSyncExternalStore } from "react"
import { toast } from "sonner"
import {
  ArrowDown,
  ChevronDown,
  ChevronRight,
  Folder,
  ImageIcon,
  Library,
  RefreshCw,
  Search,
  ShieldCheck,
  TriangleAlert,
} from "lucide-react"

import { Button } from "@/components/ui/button"
import { Progress } from "@/components/ui/progress"
import {
  SectionTitle,
  SeriesCover,
  StatusBadge,
} from "@/components/shared"
import {
  BrowserOnlyNotice,
  EmptyState,
  ErrorState,
  LoadingState,
} from "@/components/states"
import {
  checkUpdates,
  fetchCovers,
  gradientFor,
  humanBytes,
  initialsFromTitle,
  runUpdate,
  startDownload,
  titleFromSlug,
  verifySeries,
  type LibraryReport,
  type LibrarySeries,
  type UpdateReport,
} from "@/lib/api"
import { firstErrorLine, hasCliBridge } from "@/lib/cli"
import { getJobs, subscribe } from "@/lib/download-store"
import { metricsFor } from "@/lib/job-metrics"
import type { LibraryFilter, SeriesStatus } from "@/lib/types"
import type { AsyncState } from "@/lib/use-async"
import { invalidateCover } from "@/lib/use-cover"
import { cn } from "@/lib/utils"

type SortKey = "updated" | "title" | "size"

const sortOrder: SortKey[] = ["updated", "title", "size"]

const sortLabels: Record<SortKey, string> = {
  updated: "Library order",
  title: "Title",
  size: "Size",
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

/**
 * Design-system status vocabulary: Downloading / Needs repair / N new /
 * Up to date / Partial / Empty. Every branch is backed by a real CLI field —
 * no status is invented for display.
 */
function seriesStatus(
  series: LibrarySeries,
  options: { running?: boolean; newChapters?: number } = {}
): {
  status: SeriesStatus
  label: string
} {
  if (options.running) return { status: "downloading", label: "Downloading" }
  if (series.problems > 0) return { status: "broken", label: "Needs repair" }
  if ((options.newChapters ?? 0) > 0) {
    const count = options.newChapters ?? 0
    return { status: "new", label: `${count} new` }
  }
  if (series.chapters > 0 && series.done >= series.chapters) {
    return { status: "complete", label: "Up to date" }
  }
  if (series.done > 0) return { status: "new", label: "Partial" }
  return { status: "partial", label: "Empty" }
}

// Prefer the URL the CLI recorded; only fall back to the conventional path for
// series downloaded before a pack manifest existed.
function seriesUrl(item: { slug: string; series_url?: string }): string {
  return item.series_url ?? `https://komiku.org/manga/${item.slug}/`
}

function filterSeries(
  series: LibrarySeries[],
  filter: LibraryFilter
): LibrarySeries[] {
  if (filter === "attention") return series.filter((item) => item.problems > 0)
  return series
}

export function LibraryScreen({
  onOpenSeries,
  onOpenDownloads,
  library,
  filter,
  onFilterChange,
}: {
  onOpenSeries: (slug: string, url?: string) => void
  onOpenDownloads: () => void
  library: AsyncState<LibraryReport | null>
  filter: LibraryFilter
  onFilterChange: (filter: LibraryFilter) => void
}) {
  const [query, setQuery] = useState("")
  const [sort, setSort] = useState<SortKey>("updated")
  const [verifying, setVerifying] = useState(false)
  const [checking, setChecking] = useState(false)
  const [updating, setUpdating] = useState(false)
  const [redownloading, setRedownloading] = useState(false)
  const [fetchingCovers, setFetchingCovers] = useState(false)
  const [updateReport, setUpdateReport] = useState<UpdateReport | null>(null)

  const jobs = useSyncExternalStore(subscribe, getJobs)
  const activeJobs = jobs.filter((job) => job.status === "running")
  const runningSlugs = useMemo(
    () => new Set(activeJobs.map((job) => job.slug)),
    [activeJobs]
  )

  const report = library.data
  const series = useMemo(() => report?.series ?? [], [report])

  const visibleSeries = useMemo(() => {
    const term = query.trim().toLowerCase()
    const filtered = filterSeries(series, filter).filter((item) =>
      term ? titleFromSlug(item.slug).toLowerCase().includes(term) : true
    )
    if (sort === "title") {
      return [...filtered].sort((a, b) =>
        titleFromSlug(a.slug).localeCompare(titleFromSlug(b.slug))
      )
    }
    if (sort === "size") {
      return [...filtered].sort((a, b) => b.bytes - a.bytes)
    }
    // The CLI report has no timestamp; preserve its insertion order.
    return filtered
  }, [series, filter, query, sort])

  const shelf = useMemo(
    () =>
      [...series]
        .filter((item) => item.done > 0)
        .sort((a, b) => b.done / Math.max(b.chapters, 1) - a.done / Math.max(a.chapters, 1))
        .slice(0, 4),
    [series]
  )

  const problemSeries = series.find((item) => item.problems > 0)
  const firstProblemDetail = problemSeries?.problems_detail?.[0]
  const problemChapter =
    firstProblemDetail !== undefined
      ? /chapter-(\d+(?:\.\d+)?)/i.exec(firstProblemDetail)?.[1] ?? null
      : null

  const newChapterCount = (updateReport?.series ?? []).reduce(
    (total, item) => total + (item.new_chapters?.length ?? 0),
    0
  )
  const newBySlug = useMemo(() => {
    const map = new Map<string, number>()
    for (const item of updateReport?.series ?? []) {
      const count = item.new_chapters?.length ?? 0
      if (count > 0) map.set(item.slug, count)
    }
    return map
  }, [updateReport])

  const rootLabel = !hasCliBridge
    ? "CLI unavailable"
    : report?.root ?? "Scanning library…"

  const summary = report
    ? `${series.length} series · ${report.total_chapters.toLocaleString(
        "en-US"
      )} chapters`
    : "Reading your local library…"

  function cycleSort() {
    const index = sortOrder.indexOf(sort)
    setSort(sortOrder[(index + 1) % sortOrder.length])
  }

  async function handleVerify() {
    if (verifying || !report) return
    setVerifying(true)
    let problems = 0
    let checked = 0
    try {
      for (const item of report.series ?? []) {
        const result = await verifySeries(item.dir)
        problems += result.problems
        checked += 1
      }
      if (problems > 0) {
        toast.warning(
          `${problems} problem${problems === 1 ? "" : "s"} across ${checked} series`
        )
      } else {
        toast.success("All chapters verified", {
          description: `${checked} series checked`,
        })
      }
      library.reload()
    } catch (error) {
      toast.error(errorMessage(error))
    } finally {
      setVerifying(false)
    }
  }

  async function handleCheckForNew() {
    if (checking) return
    setChecking(true)
    const toastId = toast.loading("Checking downloaded library…")
    try {
      const result = await checkUpdates()
      setUpdateReport(result)
      const updated = result.series ?? []
      const total = updated.reduce(
        (sum, item) => sum + (item.new_chapters?.length ?? 0),
        0
      )
      if (total > 0) {
        const slugs = updated
          .filter((item) => (item.new_chapters?.length ?? 0) > 0)
          .map((item) => `${item.slug} (${item.new_chapters?.length ?? 0})`)
          .join(" · ")
        toast.success(
          `${total} new chapter${total === 1 ? "" : "s"} found`,
          { id: toastId, description: slugs }
        )
      } else {
        toast.success("Everything is up to date", { id: toastId })
      }
    } catch (error) {
      toast.error(errorMessage(error), { id: toastId })
    } finally {
      setChecking(false)
    }
  }

  async function handleUpdateAll() {
    if (updating) return
    setUpdating(true)
    const toastId = toast.loading("Downloading new chapters…")
    try {
      const result = await runUpdate()
      const updated = result.series ?? []
      const downloaded = updated.reduce(
        (sum, item) => sum + item.downloaded,
        0
      )
      const failed = updated.filter((item) => item.error).length
      toast.success(
        `${downloaded} chapter${downloaded === 1 ? "" : "s"} downloaded`,
        {
          id: toastId,
          description: failed > 0 ? `${failed} series failed` : undefined,
        }
      )
      library.reload()
      setUpdateReport(null)
    } catch (error) {
      toast.error(errorMessage(error), { id: toastId })
    } finally {
      setUpdating(false)
    }
  }

  async function handleFetchCovers() {
    if (fetchingCovers) return
    setFetchingCovers(true)
    const toastId = toast.loading("Fetching cover art…")
    try {
      const reports = await fetchCovers()
      const saved = reports.filter((item) => item.cover && !item.error).length
      const failed = reports.filter((item) => item.error).length
      for (const report of reports) {
        if (report.cover) invalidateCover(report.cover)
      }
      if (saved === 0 && failed > 0) {
        toast.error("No covers could be fetched", {
          id: toastId,
          description: reports.find((item) => item.error)?.error,
        })
      } else {
        toast.success(
          `${saved} cover${saved === 1 ? "" : "s"} saved`,
          {
            id: toastId,
            description: failed > 0 ? `${failed} series failed` : undefined,
          }
        )
      }
      library.reload()
    } catch (error) {
      toast.error(errorMessage(error), { id: toastId })
    } finally {
      setFetchingCovers(false)
    }
  }

  async function handleRedownload() {
    if (!problemSeries || !problemChapter || redownloading) return
    setRedownloading(true)
    const toastId = toast.loading(
      `Re-downloading chapter ${problemChapter}…`
    )
    try {
      const handle = await startDownload(
        seriesUrl(problemSeries),
        { chapters: problemChapter },
        () => {}
      )
      const result = await handle.done
      if (!result.ok) {
        throw new Error(firstErrorLine(result.stderr) || "download failed")
      }
      toast.success(`Chapter ${problemChapter} re-downloaded`, {
        id: toastId,
        description: titleFromSlug(problemSeries.slug),
      })
      library.reload()
    } catch (error) {
      toast.error(errorMessage(error), { id: toastId })
    } finally {
      setRedownloading(false)
    }
  }

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <div
        data-tauri-drag-region="deep"
        className="flex h-[60px] shrink-0 items-center gap-2.5 px-8"
      >
        <Library className="size-4 text-dim" strokeWidth={1.8} />
        <span className="text-[13px] font-medium">Library</span>
        <span className="text-[12px] text-dim">·</span>
        <Folder className="size-3.5 text-dim" strokeWidth={1.8} />
        <span className="max-w-[420px] truncate font-mono text-[11px] text-muted-foreground">
          {rootLabel}
        </span>
        <div className="flex-1" />
        {updateReport && newChapterCount > 0 && (
          <span className="rounded-full bg-warning-soft px-2 py-1 text-[11px] font-medium text-warning">
            {newChapterCount} new chapter
            {newChapterCount === 1 ? "" : "s"}
          </span>
        )}
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-8 pb-8 pt-1">
        <BrowserOnlyNotice className="mb-5" />

        {!hasCliBridge ? (
          <EmptyState
            title="Open the desktop app to load your library"
            hint="The browser preview cannot reach the komiku-cli binary."
            icon={Library}
          />
        ) : library.loading ? (
          <LoadingState label="Scanning library…" />
        ) : library.error ? (
          <ErrorState message={library.error} onRetry={library.reload} />
        ) : !report ? (
          <EmptyState
            title="Open the desktop app to load your library"
            hint="The CLI bridge is not available in this environment."
            icon={Library}
          />
        ) : (
          <>
            <div className="flex items-start gap-4">
              <div className="flex min-w-0 flex-col gap-1.5">
                <h1 className="text-[30px] font-semibold leading-[1.1] tracking-[-0.02em]">
                  Your library
                </h1>
                <p className="text-[13px] text-muted-foreground">{summary}</p>
              </div>
              <div className="flex-1" />
              <div className="flex items-center gap-2 pt-1">
                <Button
                  variant="outline"
                  size="sm"
                  className="h-9 px-3.5 text-[13px]"
                  onClick={handleFetchCovers}
                  disabled={!hasCliBridge || !report || fetchingCovers}
                >
                  <ImageIcon
                    className={cn("size-3.5", fetchingCovers && "animate-pulse")}
                    strokeWidth={1.8}
                  />                  {fetchingCovers ? "Fetching…" : "Fetch covers"}
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  className="h-9 px-3.5 text-[13px]"
                  onClick={handleCheckForNew}
                  disabled={!hasCliBridge || !report || checking}
                >
                  <RefreshCw
                    className={cn("size-3.5", checking && "animate-spin")}
                    strokeWidth={1.8}
                  />
                  {checking ? "Checking…" : "Check for new"}
                </Button>
                <Button
                  size="sm"
                  className="h-9 px-3.5 text-[13px]"
                  onClick={handleUpdateAll}
                  disabled={!hasCliBridge || !report || updating}
                >
                  <RefreshCw
                    className={cn("size-3.5", updating && "animate-spin")}
                    strokeWidth={1.8}
                  />
                  {updating ? "Updating…" : "Update all"}
                </Button>
              </div>
            </div>

            {shelf.length > 0 && (
              <section className="mt-8">
                <div className="flex items-center gap-2.5">
                  <h2 className="text-[20px] font-semibold tracking-[-0.01em]">
                    Continue reading
                  </h2>
                  <div className="flex-1" />
                  <button
                    type="button"
                    onClick={() => {
                      onFilterChange("all")
                      setQuery("")
                    }}
                    className="text-[13px] text-muted-foreground transition-colors hover:text-foreground"
                  >
                    View all
                  </button>
                </div>

                <div className="mt-4 grid grid-cols-4 gap-5">
                  {shelf.map((item) => {
                    const status = seriesStatus(item, {
                      running: runningSlugs.has(item.slug),
                      newChapters: newBySlug.get(item.slug),
                    })
                    return (
                      <button
                        key={item.slug}
                        type="button"
                        onClick={() =>
                          onOpenSeries(item.slug, seriesUrl(item))
                        }
                        className="group flex flex-col gap-2.5 text-left"
                      >
                        <div className="aspect-[273/330] w-full overflow-hidden rounded-[6px] transition-opacity group-hover:opacity-90">
                          <SeriesCover
                            size="fill"
                            initials={initialsFromTitle(
                              titleFromSlug(item.slug)
                            )}
                            gradient={gradientFor(item.slug)}
                            coverPath={item.cover}
                          />
                        </div>
                        <span className="truncate text-[16px] font-semibold">
                          {titleFromSlug(item.slug)}
                        </span>
                        <span className="text-[12px] text-muted-foreground">
                          {item.done} / {item.chapters} chapters
                        </span>
                        <span className="flex">
                          <StatusBadge
                            status={status.status}
                            label={status.label}
                          />
                        </span>
                      </button>
                    )
                  })}
                </div>
              </section>
            )}

            <section className="mt-8">
              <div className="flex items-center gap-3">
                <SectionTitle
                  title="All series"
                  count={visibleSeries.length}
                />
                <div className="flex items-center gap-2">
                  <div className="flex h-9 w-[220px] items-center gap-2 rounded-[6px] border border-border bg-surface px-2.5">
                    <Search
                      className="size-3.5 shrink-0 text-dim"
                      strokeWidth={1.8}
                    />
                    <input
                      value={query}
                      onChange={(event) => setQuery(event.target.value)}
                      placeholder="Search your library…"
                      spellCheck={false}
                      aria-label="Search your library"
                      className="min-w-0 flex-1 bg-transparent text-[12px] text-foreground outline-none placeholder:text-dim"
                    />
                  </div>
                  <button
                    type="button"
                    onClick={cycleSort}
                    className="inline-flex h-9 items-center gap-1.5 rounded-[6px] border border-border bg-surface px-2.5 text-[12px] text-muted-foreground transition-colors hover:text-foreground"
                  >
                    {sortLabels[sort]}
                    <ChevronDown
                      className={cn(
                        "size-3 transition-transform",
                        sort !== "updated" && "rotate-180"
                      )}
                      strokeWidth={1.8}
                    />
                  </button>
                </div>
              </div>

              <div className="mt-3 overflow-hidden rounded-[8px] border border-border-subtle">
                <div className="flex items-center gap-4 border-b border-border-subtle bg-surface px-3 py-2.5 text-[12px] text-muted-foreground">
                  <span className="flex-1">Series</span>
                  <span className="w-[120px] shrink-0">Chapters</span>
                  <span className="w-[150px] shrink-0">Status</span>
                  <span className="w-[100px] shrink-0 text-right">Size</span>
                </div>

                {visibleSeries.length === 0 ? (
                  <div className="bg-surface/40">
                    <EmptyState
                      title={
                        series.length === 0
                          ? "No series downloaded yet"
                          : filter === "attention"
                            ? "No series need repair"
                            : "No matching series"
                      }
                      hint={
                        series.length === 0
                          ? "Download a series to populate your library."
                          : "Clear the filter or search a different title."
                      }
                      icon={Library}
                    />
                  </div>
                ) : (
                  visibleSeries.map((item) => {
                    const status = seriesStatus(item, {
                      running: runningSlugs.has(item.slug),
                      newChapters: newBySlug.get(item.slug),
                    })
                    return (
                      <button
                        key={item.slug}
                        type="button"
                        onClick={() =>
                          onOpenSeries(item.slug, seriesUrl(item))
                        }
                        className="flex h-[58px] w-full items-center gap-4 border-b border-border-subtle px-3 text-left transition-colors last:border-b-0 hover:bg-muted/50"
                      >
                        <span className="flex min-w-0 flex-1 items-center gap-2.5">
                          <span className="truncate text-[14px] font-medium">
                            {titleFromSlug(item.slug)}
                          </span>
                          {item.problems > 0 && (
                            <TriangleAlert
                              className="size-3.5 shrink-0 text-danger"
                              strokeWidth={1.8}
                            />
                          )}
                        </span>
                        <span className="w-[120px] shrink-0 font-mono text-[12px] text-muted-foreground">
                          {item.done} / {item.chapters}
                        </span>
                        <span className="w-[150px] shrink-0">
                          <StatusBadge
                            status={status.status}
                            label={status.label}
                          />
                        </span>
                        <span className="flex w-[100px] shrink-0 items-center justify-end gap-2 font-mono text-[12px] text-muted-foreground">
                          {humanBytes(item.bytes)}
                          <ChevronRight
                            className="size-3.5 text-dim"
                            strokeWidth={1.8}
                          />
                        </span>
                      </button>
                    )
                  })
                )}
              </div>
            </section>

            <section className="mt-8 grid grid-cols-2 gap-4">
              <div className="flex flex-col gap-2.5 rounded-[8px] border border-border-subtle bg-surface p-4">
                <div className="flex items-center gap-2">
                  <RefreshCw
                    className={cn(
                      "size-3.5 text-primary",
                      checking && "animate-spin"
                    )}
                    strokeWidth={1.8}
                  />
                  <span className="text-[13px] font-semibold">Catch-up</span>
                  <div className="flex-1" />
                  {newChapterCount > 0 && (
                    <span className="font-mono text-[11px] text-primary">
                      {newChapterCount}
                    </span>
                  )}
                </div>

                {checking ? (
                  <span className="text-[12px] text-dim">
                    Checking downloaded library…
                  </span>
                ) : !updateReport ? (
                  <>
                    <span className="text-[12px] leading-relaxed text-dim">
                      Check your downloaded library for chapters that are not on disk yet.
                    </span>
                    <Button
                      variant="outline"
                      size="sm"
                      className="mt-1 h-8 w-full text-[12px]"
                      onClick={handleCheckForNew}
                      disabled={checking}
                    >
                      Check now
                    </Button>
                  </>
                ) : newChapterCount === 0 ? (
                  <>
                    <span className="text-[12px] text-muted-foreground">
                      Everything is up to date.
                    </span>
                    <button
                      type="button"
                      onClick={handleCheckForNew}
                      className="self-start text-[12px] font-medium text-primary hover:underline"
                    >
                      Check again →
                    </button>
                  </>
                ) : (
                  <>
                    <div className="flex flex-col gap-1.5">
                      {(updateReport.series ?? [])
                        .filter(
                          (item) => (item.new_chapters?.length ?? 0) > 0
                        )
                        .slice(0, 2)
                        .map((item) => (
                          <div
                            key={item.slug}
                            className="flex items-center gap-2"
                          >
                            <span className="size-1.5 shrink-0 rounded-full bg-primary" />
                            <span className="min-w-0 flex-1 truncate text-[12px] text-muted-foreground">
                              {titleFromSlug(item.slug)}
                            </span>
                            <span className="shrink-0 font-mono text-[11px] text-dim">
                              {item.new_chapters?.length ?? 0} new
                            </span>
                          </div>
                        ))}
                    </div>
                    <Button
                      size="sm"
                      className="mt-1 h-9 w-full text-[12px]"
                      onClick={handleUpdateAll}
                      disabled={updating}
                    >
                      {updating
                        ? "Downloading…"
                        : `Download ${newChapterCount} chapter${
                            newChapterCount === 1 ? "" : "s"
                          }`}
                    </Button>
                  </>
                )}
              </div>

              <div className="flex flex-col gap-2.5 rounded-[8px] border border-border-subtle bg-surface p-4">
                <div className="flex items-center gap-2">
                  <ShieldCheck
                    className={cn(
                      "size-3.5",
                      report.total_problems > 0
                        ? "text-warning"
                        : "text-success"
                    )}
                    strokeWidth={1.8}
                  />
                  <span className="text-[13px] font-semibold">Integrity</span>
                  <div className="flex-1" />
                  <span
                    className={cn(
                      "font-mono text-[11px]",
                      report.total_problems > 0
                        ? "text-warning"
                        : "text-success"
                    )}
                  >
                    {report.total_problems}
                  </span>
                </div>

                {problemSeries ? (
                  <>
                    <div className="rounded-[6px] bg-muted p-2.5">
                      <div className="truncate text-[12px] font-medium">
                        {titleFromSlug(problemSeries.slug)}
                      </div>
                      <div className="mt-0.5 truncate font-mono text-[10px] text-dim">
                        {firstProblemDetail ?? "unknown problem"}
                      </div>
                    </div>
                    <div className="flex items-center gap-2">
                      <button
                        type="button"
                        onClick={handleRedownload}
                        disabled={!problemChapter || redownloading}
                        className={cn(
                          "text-[12px] font-medium",
                          problemChapter
                            ? "text-primary hover:underline"
                            : "cursor-default text-dim"
                        )}
                      >
                        {redownloading
                          ? "Re-downloading…"
                          : "Re-download chapter →"}
                      </button>
                      <div className="flex-1" />
                      <button
                        type="button"
                        onClick={handleVerify}
                        disabled={verifying}
                        className="text-[12px] text-muted-foreground hover:text-foreground"
                      >
                        {verifying ? "Verifying…" : "Verify all"}
                      </button>
                    </div>
                  </>
                ) : (
                  <>
                    <span className="text-[12px] text-dim">
                      No broken chapters detected.
                    </span>
                    <button
                      type="button"
                      onClick={handleVerify}
                      disabled={verifying}
                      className="self-start text-[12px] font-medium text-primary hover:underline"
                    >
                      {verifying ? "Verifying…" : "Verify all →"}
                    </button>
                  </>
                )}
              </div>
            </section>
          </>
        )}
      </div>

      {activeJobs.length > 0 && (
        <button
          type="button"
          onClick={onOpenDownloads}
          className="flex h-[60px] w-full shrink-0 items-center gap-3 border-t border-border bg-surface px-8 text-left"
        >
          <ArrowDown className="size-[18px] text-primary" strokeWidth={1.8} />
          <span className="text-[14px] font-medium">
            {titleFromSlug(activeJobs[0].slug)}
          </span>
          <span className="text-[13px] text-muted-foreground">
            {metricsFor(activeJobs[0]).currentChapter
              ? `Chapter ${metricsFor(activeJobs[0]).currentChapter}`
              : `${activeJobs[0].chapters.length} chapters`}
          </span>
          <div className="flex-1" />
          <Progress
            value={metricsFor(activeJobs[0]).percent}
            className="h-1 w-[170px]"
          />
          <span className="w-9 text-right font-mono text-[12px]">
            {metricsFor(activeJobs[0]).percent}%
          </span>
          <span className="font-mono text-[12px] text-muted-foreground">
            {activeJobs.length > 1
              ? `${activeJobs.length} running`
              : "downloading"}
          </span>
          <ChevronRight className="size-4 text-dim" strokeWidth={1.8} />
        </button>
      )}
    </div>
  )
}
