import { useMemo, useState } from "react"
import { ChevronLeft, ChevronRight, List, Search } from "lucide-react"

import {
  BarMeta,
  Chip,
  DiscoveryBar,
  LetterTile,
  RemoteCover,
  TypeChip,
} from "@/components/discovery"
import { EmptyState, ErrorState, LoadingState } from "@/components/states"
import {
  formatCount,
  getCatalog,
  getCatalogTotals,
  type CatalogReport,
  type DiscoverySeries,
} from "@/lib/discovery"
import { useAsync } from "@/lib/use-async"
import { cn } from "@/lib/utils"

type TypeFilter = "" | "manga" | "manhwa" | "manhua"
type StatusFilter = "" | "ongoing" | "tamat"

const typeOptions: { id: TypeFilter; label: string }[] = [
  { id: "", label: "Semua" },
  { id: "manga", label: "Manga" },
  { id: "manhwa", label: "Manhwa" },
  { id: "manhua", label: "Manhua" },
]

const statusOptions: { id: StatusFilter; label: string }[] = [
  { id: "", label: "Semua status" },
  { id: "ongoing", label: "Ongoing" },
  { id: "tamat", label: "Tamat" },
]

/** Compact page window: 1 … n-1 n n+1 … last */
function pageWindow(current: number, total: number): (number | "…")[] {
  if (total <= 7) {
    return Array.from({ length: total }, (_, index) => index + 1)
  }
  const pages = new Set<number>([1, total, current - 1, current, current + 1])
  const sorted = [...pages].filter((page) => page >= 1 && page <= total).sort((a, b) => a - b)
  const output: (number | "…")[] = []
  for (let index = 0; index < sorted.length; index += 1) {
    const page = sorted[index]
    if (index > 0 && page - sorted[index - 1] > 1) output.push("…")
    output.push(page)
  }
  return output
}

export function CatalogScreen({
  onOpenSeries,
}: {
  onOpenSeries: (slug: string, url?: string) => void
}) {
  const [letter, setLetter] = useState("All")
  const [type, setType] = useState<TypeFilter>("")
  const [status, setStatus] = useState<StatusFilter>("")
  const [page, setPage] = useState(1)
  const [filter, setFilter] = useState("")

  const catalog = useAsync<CatalogReport>(
    () => getCatalog({ letter, type, status, page }),
    [letter, type, status, page]
  )
  const totals = useAsync(getCatalogTotals, [])

  const report = catalog.data
  const rows = useMemo<DiscoverySeries[]>(() => {
    const list = report?.series ?? []
    const term = filter.trim().toLowerCase()
    return term
      ? list.filter((entry) => entry.title.toLowerCase().includes(term))
      : list
  }, [report, filter])

  const alphabet = report?.alphabet ?? []
  const totalPages = report?.total_pages ?? 0

  function chooseLetter(next: string) {
    setLetter(next)
    setPage(1)
  }

  function chooseType(next: TypeFilter) {
    setType(next)
    setPage(1)
  }

  function chooseStatus(next: StatusFilter) {
    setStatus(next)
    setPage(1)
  }

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <DiscoveryBar
        icon={List}
        label="Daftar Komik"
        crumb={letter === "All" ? "A–Z" : letter}
      >
        <BarMeta icon={List}>
          {formatCount(totals.data?.all)} komik · {totalPages} halaman
        </BarMeta>
      </DiscoveryBar>

      <div className="min-h-0 flex-1 overflow-y-auto px-8 pb-8 pt-1">
        <div className="flex items-start gap-4">
          <div className="flex min-w-0 flex-col gap-1.5">
            <h1 className="text-[30px] font-semibold leading-[1.1] tracking-[-0.02em]">
              Daftar Komik
            </h1>
            <p className="text-[13px] text-muted-foreground">
              Semua judul, diurutkan alfabetis
            </p>
          </div>
          <div className="flex-1" />
          <div className="flex h-[34px] w-[260px] items-center gap-2 rounded-[6px] border border-border bg-surface px-2.5">
            <Search className="size-3.5 shrink-0 text-dim" strokeWidth={1.8} />
            <input
              value={filter}
              onChange={(event) => setFilter(event.target.value)}
              placeholder="Filter dalam daftar…"
              aria-label="Filter dalam daftar"
              spellCheck={false}
              className="min-w-0 flex-1 bg-transparent text-[12px] outline-none placeholder:text-dim"
            />
          </div>
        </div>

        {/* Alphabet index */}
        <div className="mt-5 flex flex-col gap-2">
          <div className="flex items-center gap-2">
            <span className="text-[10px] font-semibold tracking-[0.08em] text-dim">
              INDEKS
            </span>
            <span className="text-[10px] text-dim">
              Angka di bawah huruf = jumlah judul
            </span>
            <div className="h-px flex-1 bg-border-subtle" />
          </div>
          <div className="flex flex-wrap gap-1.5">
            <LetterTile
              letter="All"
              count={totals.data?.all ?? 0}
              active={letter === "All"}
              onClick={() => chooseLetter("All")}
            />
            {alphabet.map((entry) => (
              <LetterTile
                key={entry.letter}
                letter={entry.letter}
                count={entry.count}
                active={letter === entry.letter}
                onClick={() => chooseLetter(entry.letter)}
              />
            ))}
          </div>
        </div>

        <div className="mt-4 flex flex-wrap items-center gap-1.5">
          {typeOptions.map((option) => (
            <Chip
              key={option.label}
              label={option.label}
              active={type === option.id}
              onClick={() => chooseType(option.id)}
            />
          ))}
          <span className="mx-1 h-[18px] w-px bg-border" />
          {statusOptions.map((option) => (
            <Chip
              key={option.label}
              label={option.label}
              active={status === option.id}
              onClick={() => chooseStatus(option.id)}
            />
          ))}
          <div className="flex-1" />
          <span className="font-mono text-[11px] text-dim">
            {filter
              ? `${rows.length} dari ${report?.series?.length ?? 0} judul`
              : `Menampilkan ${letter === "All" ? "semua" : letter} · ${formatCount(
                  report?.total
                )} judul`}
          </span>
        </div>

        {catalog.loading && !catalog.data ? (
          <LoadingState label="Membaca katalog…" />
        ) : catalog.error ? (
          <ErrorState message={catalog.error} onRetry={catalog.reload} />
        ) : rows.length === 0 ? (
          <EmptyState
            title="Tidak ada judul"
            hint="Coba huruf atau filter lain."
            icon={List}
          />
        ) : (
          <div className="mt-4 grid grid-cols-4 gap-3">
            {rows.map((entry) => (
              <button
                key={entry.slug}
                type="button"
                onClick={() => onOpenSeries(entry.slug, entry.url)}
                className="group flex gap-3 rounded-[8px] border border-border-subtle bg-surface p-2.5 text-left transition-colors hover:border-border hover:bg-muted/40"
              >
                <div className="relative shrink-0">
                  <RemoteCover
                    src={entry.cover}
                    title={entry.title}
                    className="h-[86px] w-[62px]"
                    rounded="rounded-[5px]"
                  />
                  <span className="absolute left-1 top-1">
                    <TypeChip type={entry.type} />
                  </span>
                </div>
                <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                  <span className="line-clamp-2 text-[12px] font-medium leading-snug group-hover:text-primary">
                    {entry.title}
                  </span>
                  <span className="truncate text-[10px] text-muted-foreground">
                    {(entry.genres ?? []).slice(0, 2).join(" · ") || "—"}
                  </span>
                  <span
                    className={cn(
                      "mt-auto text-[10px]",
                      entry.status?.toLowerCase() === "tamat"
                        ? "text-success"
                        : "text-dim"
                    )}
                  >
                    {entry.status ?? "—"}
                  </span>
                </span>
              </button>
            ))}
          </div>
        )}

        {totalPages > 1 && (
          <div className="mt-5 flex items-center gap-2">
            <span className="font-mono text-[11px] text-dim">Halaman</span>
            <div className="flex items-center gap-1">
              <button
                type="button"
                onClick={() => setPage((value) => Math.max(1, value - 1))}
                disabled={(report?.page ?? 1) <= 1}
                aria-label="Halaman sebelumnya"
                className="flex size-7 items-center justify-center rounded-[5px] border border-border-subtle text-dim transition-colors hover:text-foreground disabled:opacity-40"
              >
                <ChevronLeft className="size-3.5" strokeWidth={1.8} />
              </button>
              {pageWindow(report?.page ?? 1, totalPages).map((item, index) =>
                item === "…" ? (
                  <span
                    key={`gap-${index}`}
                    className="px-1 font-mono text-[11px] text-dim"
                  >
                    …
                  </span>
                ) : (
                  <button
                    key={item}
                    type="button"
                    onClick={() => setPage(item)}
                    aria-current={item === report?.page ? "page" : undefined}
                    className={cn(
                      "min-w-7 rounded-[5px] px-2 py-1 font-mono text-[11px] transition-colors",
                      item === report?.page
                        ? "bg-primary text-primary-foreground"
                        : "text-muted-foreground hover:bg-muted"
                    )}
                  >
                    {item}
                  </button>
                )
              )}
              <button
                type="button"
                onClick={() =>
                  setPage((value) => Math.min(totalPages, value + 1))
                }
                disabled={(report?.page ?? 1) >= totalPages}
                aria-label="Halaman berikutnya"
                className="flex size-7 items-center justify-center rounded-[5px] border border-border-subtle text-dim transition-colors hover:text-foreground disabled:opacity-40"
              >
                <ChevronRight className="size-3.5" strokeWidth={1.8} />
              </button>
            </div>
            <div className="flex-1" />
            <span className="font-mono text-[10px] text-dim">
              {report?.page ?? 1} / {totalPages}
            </span>
          </div>
        )}
      </div>
    </div>
  )
}
