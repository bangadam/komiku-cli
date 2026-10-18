import { useMemo, useState } from "react"
import { Sparkles } from "lucide-react"

import {
  BarMeta,
  DiscoveryBar,
  NewFlag,
  RefreshButton,
  RemoteCover,
  TimeChip,
  TypeChip,
} from "@/components/discovery"
import { EmptyState, ErrorState, LoadingState } from "@/components/states"
import {
  formatCount,
  getCatalogTotals,
  getFeed,
  isFresh,
} from "@/lib/discovery"
import { useAsync } from "@/lib/use-async"
import { cn } from "@/lib/utils"

type TypeFilter = "" | "manga" | "manhwa" | "manhua"

const typeOptions: { id: TypeFilter; label: string }[] = [
  { id: "", label: "Semua" },
  { id: "manga", label: "Manga" },
  { id: "manhwa", label: "Manhwa" },
  { id: "manhua", label: "Manhua" },
]

export function NewScreen({
  onOpenSeries,
}: {
  onOpenSeries: (slug: string, url?: string) => void
}) {
  const [type, setType] = useState<TypeFilter>("")

  const feed = useAsync(() => getFeed({ order: "new", type, page: 1 }), [type])
  const totals = useAsync(getCatalogTotals, [])

  const rows = feed.data ?? []

  const tabOptions = useMemo(
    () =>
      typeOptions.map((option) => ({
        ...option,
        count: totals.data
          ? option.id === ""
            ? totals.data.all
            : totals.data[option.id]
          : undefined,
      })),
    [totals.data]
  )

  const freshCount = rows.filter((entry) => isFresh(entry.updated)).length

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <DiscoveryBar icon={Sparkles} label="Baru">
        <BarMeta icon={Sparkles}>komiku.org/pustaka?orderby=date</BarMeta>
        <span className="mx-1 h-4 w-px bg-border" />
        <RefreshButton busy={feed.loading} onClick={feed.reload} />
      </DiscoveryBar>

      <div className="min-h-0 flex-1 overflow-y-auto px-8 pb-8 pt-1">
        <div className="flex items-start gap-4">
          <div className="flex min-w-0 flex-col gap-1.5">
            <h1 className="text-[30px] font-semibold leading-[1.1] tracking-[-0.02em]">
              Baru Ditambahkan
            </h1>
            <p className="text-[13px] text-muted-foreground">
              {formatCount(totals.data?.all)} judul di katalog ·{" "}
              {freshCount} update baru di halaman ini
            </p>
          </div>
        </div>

        <div className="mt-5 flex flex-wrap items-center gap-3 border-b border-border-subtle pb-2">
          {tabOptions.map((option) => {
            const active = type === option.id
            return (
              <button
                key={option.label}
                type="button"
                onClick={() => setType(option.id)}
                className={cn(
                  "-mb-2 flex h-9 items-center gap-1.5 border-b-2 px-0.5 text-[12px] transition-colors",
                  active
                    ? "border-primary font-semibold text-foreground"
                    : "border-transparent font-medium text-dim hover:text-muted-foreground"
                )}
              >
                {option.label}
                {option.count !== undefined && (
                  <span className="font-mono text-[10px] text-dim">
                    {formatCount(option.count)}
                  </span>
                )}
              </button>
            )
          })}
          <div className="flex-1" />
          <span className="font-mono text-[10px] text-dim">Minggu ini</span>
        </div>

        {feed.loading && !feed.data ? (
          <LoadingState label="Membaca judul baru…" />
        ) : feed.error ? (
          <ErrorState message={feed.error} onRetry={feed.reload} />
        ) : rows.length === 0 ? (
          <EmptyState
            title="Tidak ada judul baru"
            hint="Coba filter tipe lain."
            icon={Sparkles}
          />
        ) : (
          <div className="mt-4 grid grid-cols-4 gap-4">
            {rows.map((entry) => (
              <div
                key={entry.slug}
                className="flex flex-col gap-2 rounded-[8px] border border-border-subtle bg-surface p-2.5"
              >
                <button
                  type="button"
                  data-series={entry.slug}
                  onClick={() => onOpenSeries(entry.slug, entry.url)}
                  className="group relative flex flex-col gap-2 text-left"
                >
                  <RemoteCover
                    src={entry.cover}
                    title={entry.title}
                    className="aspect-[2/3] w-full"
                    rounded="rounded-[6px]"
                  />
                  {isFresh(entry.updated) && (
                    <NewFlag className="absolute left-1.5 top-1.5" />
                  )}
                  <span className="line-clamp-2 min-h-[32px] text-[12px] font-medium leading-snug group-hover:text-primary">
                    {entry.title}
                  </span>
                </button>
                <div className="flex items-center gap-1.5">
                  <TypeChip type={entry.type} />
                  <span className="truncate text-[10px] text-muted-foreground">
                    {(entry.genres ?? [])[0] ?? "—"}
                  </span>
                </div>
                <div className="flex items-center gap-1.5">
                  {entry.chapter && (
                    <span className="shrink-0 rounded-full bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
                      {entry.chapter}
                    </span>
                  )}
                  <TimeChip value={entry.updated} />
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}
