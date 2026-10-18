import { useMemo, useState } from "react"
import { ArrowDownRight, ArrowUpRight, Trophy } from "lucide-react"

import {
  BarMeta,
  Chip,
  DiscoveryBar,
  RankBadge,
  RefreshButton,
  RemoteCover,
  SegmentedControl,
  TypeChip,
} from "@/components/discovery"
import {
  EmptyState,
  ErrorState,
  LoadingState,
} from "@/components/states"
import {
  formatCount,
  getRanking,
  type DiscoverySeries,
  type RankingPeriod,
} from "@/lib/discovery"
import { useAsync } from "@/lib/use-async"
import { cn } from "@/lib/utils"

const periodOptions: { id: RankingPeriod; label: string }[] = [
  { id: "harian", label: "Harian" },
  { id: "mingguan", label: "Mingguan" },
  { id: "bulanan", label: "Bulanan" },
  { id: "total", label: "Total" },
]

type TypeFilter = "" | "manga" | "manhwa" | "manhua"

const typeOptions: { id: TypeFilter; label: string }[] = [
  { id: "", label: "Semua" },
  { id: "manga", label: "Manga" },
  { id: "manhwa", label: "Manhwa" },
  { id: "manhua", label: "Manhua" },
]

type SortKey = "views" | "title"

const sortOptions: { id: SortKey; label: string }[] = [
  { id: "views", label: "Views" },
  { id: "title", label: "Judul A–Z" },
]

export function RankingScreen({
  onOpenSeries,
}: {
  onOpenSeries: (slug: string, url?: string) => void
}) {
  const [period, setPeriod] = useState<RankingPeriod>("harian")
  const [type, setType] = useState<TypeFilter>("")
  const [sort, setSort] = useState<SortKey>("views")

  const ranking = useAsync(() => getRanking(period), [period])
  // The previous window is only used to show how a title moved between
  // boards; both numbers come straight from the site.
  const comparison = useAsync(
    () => getRanking(period === "harian" ? "mingguan" : "harian"),
    [period]
  )

  const rows = useMemo(() => {
    const list = (ranking.data ?? []).filter(
      (entry) => !type || entry.type?.toLowerCase() === type
    )
    if (sort === "title") {
      return [...list].sort((a, b) => a.title.localeCompare(b.title))
    }
    return list
  }, [ranking.data, type, sort])

  const comparisonRanks = useMemo(() => {
    const map = new Map<string, number>()
    for (const entry of comparison.data ?? []) {
      if (entry.slug && entry.rank) map.set(entry.slug, entry.rank)
    }
    return map
  }, [comparison.data])

  const movers = useMemo(() => {
    return rows
      .map((entry) => {
        const other = comparisonRanks.get(entry.slug)
        if (!other || !entry.rank) return null
        return { entry, delta: other - entry.rank }
      })
      .filter((value): value is { entry: DiscoverySeries; delta: number } =>
        value !== null && value.delta !== 0
      )
      .sort((a, b) => Math.abs(b.delta) - Math.abs(a.delta))
      .slice(0, 4)
  }, [rows, comparisonRanks])

  // Shares are computed over the titles whose type is actually known, and the
  // card states that coverage. komiku.org's ranking page does not carry a type,
  // so the rest comes from the analytics feed matched by slug.
  const { typeMix, typedCount } = useMemo(() => {
    const counts = new Map<string, number>()
    let typed = 0
    for (const entry of rows) {
      if (!entry.type) continue
      typed += 1
      counts.set(entry.type, (counts.get(entry.type) ?? 0) + 1)
    }
    const total = typed || 1
    return {
      typedCount: typed,
      typeMix: [...counts.entries()]
        .map(([label, count]) => ({
          label,
          count,
          share: Math.round((count / total) * 100),
        }))
        .sort((a, b) => b.count - a.count),
    }
  }, [rows])

  const topGenres = useMemo(() => {
    const counts = new Map<string, number>()
    for (const entry of rows) {
      for (const genre of entry.genres ?? []) {
        counts.set(genre, (counts.get(genre) ?? 0) + 1)
      }
    }
    return [...counts.entries()]
      .map(([label, count]) => ({ label, count }))
      .sort((a, b) => b.count - a.count)
      .slice(0, 6)
  }, [rows])

  const periodLabel =
    periodOptions.find((option) => option.id === period)?.label ?? "Harian"
  const otherPeriodLabel = period === "harian" ? "mingguan" : "harian"

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <DiscoveryBar icon={Trophy} label="Peringkat">
        <BarMeta icon={Trophy}>komiku.org/p/ranking</BarMeta>
        <span className="mx-1 h-4 w-px bg-border" />
        <RefreshButton busy={ranking.loading} onClick={ranking.reload} />
      </DiscoveryBar>

      <div className="min-h-0 flex-1 overflow-y-auto px-8 pb-8 pt-1">
        <div className="flex items-start gap-4">
          <div className="flex min-w-0 flex-col gap-1.5">
            <h1 className="text-[30px] font-semibold leading-[1.1] tracking-[-0.02em]">
              Peringkat Komiku
            </h1>
            <p className="text-[13px] text-muted-foreground">
              {periodLabel} · {formatCount(rows.length)} judul di halaman ini
            </p>
          </div>
          <div className="flex-1" />
          <SegmentedControl
            options={periodOptions}
            value={period}
            onChange={setPeriod}
          />
        </div>

        <div className="mt-5 flex flex-wrap items-center gap-1.5">
          {typeOptions.map((option) => (
            <Chip
              key={option.label}
              label={option.label}
              active={type === option.id}
              onClick={() => setType(option.id)}
            />
          ))}
          <div className="flex-1" />
          <span className="text-[11px] text-dim">Urutkan</span>
          <SegmentedControl
            options={sortOptions}
            value={sort}
            onChange={setSort}
          />
        </div>

        <div className="mt-4 flex min-h-0 gap-5">
          <div className="flex min-w-0 flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
            <div className="flex items-center gap-3 border-b border-border-subtle bg-card px-4 py-2.5 text-[10px] font-semibold uppercase tracking-[0.06em] text-dim">
              <span className="w-6 shrink-0">#</span>
              <span className="w-[34px] shrink-0" />
              <span className="flex-1">Judul</span>
              <span className="w-[90px] shrink-0">Genre</span>
              <span className="w-[80px] shrink-0 text-right">Views</span>
              <span className="w-[86px] shrink-0 text-right">Terbaru</span>
            </div>

            {ranking.loading && !ranking.data ? (
              <LoadingState label="Membaca peringkat…" />
            ) : ranking.error ? (
              <ErrorState message={ranking.error} onRetry={ranking.reload} />
            ) : rows.length === 0 ? (
              <EmptyState
                title="Tidak ada judul"
                hint="Coba filter tipe lain."
                icon={Trophy}
              />
            ) : (
              <div className="min-h-0 flex-1 overflow-y-auto">
                {rows.map((entry) => (
                  <button
                    key={`${entry.slug}-${entry.rank}`}
                    type="button"
                    onClick={() => onOpenSeries(entry.slug, entry.url)}
                    className="flex w-full items-center gap-3 border-b border-border-subtle px-4 py-2.5 text-left transition-colors last:border-b-0 hover:bg-muted/50"
                  >
                    <RankBadge rank={entry.rank ?? 0} />
                    <RemoteCover
                      src={entry.cover}
                      title={entry.title}
                      className="h-[42px] w-[30px]"
                      rounded="rounded-[4px]"
                    />
                    <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                      <span className="flex min-w-0 items-center gap-1.5">
                        <span className="truncate text-[13px] font-medium">
                          {entry.title}
                        </span>
                        <TypeChip type={entry.type} />
                      </span>
                      <span className="truncate font-mono text-[10px] text-dim">
                        {(entry.genres ?? []).join(" · ") || "—"}
                      </span>
                    </span>
                    <span className="w-[90px] shrink-0 truncate text-[11px] text-muted-foreground">
                      {(entry.genres ?? [])[0] ?? "—"}
                    </span>
                    <span className="w-[80px] shrink-0 text-right font-mono text-[11px] text-muted-foreground">
                      {formatCount(entry.views)}
                    </span>
                    <span className="w-[86px] shrink-0 truncate text-right font-mono text-[10px] text-dim">
                      {entry.chapter ?? "—"}
                    </span>
                  </button>
                ))}
              </div>
            )}
          </div>

          <aside className="flex w-[330px] shrink-0 flex-col gap-3.5 overflow-y-auto">
            <div className="flex flex-col gap-3 rounded-[8px] border border-border-subtle bg-surface p-4">
              <div className="flex items-center gap-2">
                <span className="text-[12px] font-semibold">Pergerakan</span>
                <div className="flex-1" />
                <span className="font-mono text-[10px] text-dim">
                  vs {otherPeriodLabel}
                </span>
              </div>
              {comparison.loading && !comparison.data ? (
                <span className="text-[11px] text-dim">Menghitung…</span>
              ) : movers.length === 0 ? (
                <span className="text-[11px] leading-relaxed text-dim">
                  Tidak ada perbedaan posisi antara dua periode ini.
                </span>
              ) : (
                <div className="flex flex-col gap-2">
                  {movers.map(({ entry, delta }) => (
                    <div key={entry.slug} className="flex items-center gap-2">
                      <span className="min-w-0 flex-1 truncate text-[11px]">
                        {entry.title}
                      </span>
                      <span
                        className={cn(
                          "inline-flex shrink-0 items-center gap-0.5 rounded-full px-1.5 py-0.5 font-mono text-[10px]",
                          delta > 0
                            ? "bg-success-soft text-success"
                            : "bg-danger-soft text-danger"
                        )}
                      >
                        {delta > 0 ? (
                          <ArrowUpRight className="size-2.5" strokeWidth={2} />
                        ) : (
                          <ArrowDownRight className="size-2.5" strokeWidth={2} />
                        )}
                        {Math.abs(delta)}
                      </span>
                    </div>
                  ))}
                </div>
              )}
              <span className="text-[10px] leading-relaxed text-dim">
                Posisi di peringkat {periodLabel.toLowerCase()} dibandingkan
                peringkat {otherPeriodLabel}.
              </span>
            </div>

            <div className="flex flex-col gap-3 rounded-[8px] border border-border-subtle bg-surface p-4">
              <div className="flex items-center gap-2">
                <span className="text-[12px] font-semibold">Sebaran tipe</span>
                <div className="flex-1" />
                <span className="font-mono text-[10px] text-dim">
                  {typedCount}/{rows.length}
                </span>
              </div>
              {typeMix.map((item) => (
                <div key={item.label} className="flex flex-col gap-1">
                  <div className="flex items-center text-[11px]">
                    <span className="text-muted-foreground">{item.label}</span>
                    <div className="flex-1" />
                    <span className="font-mono text-[10px] text-dim">
                      {item.share}%
                    </span>
                  </div>
                  <div className="h-1 overflow-hidden rounded-full bg-muted">
                    <div
                      className="h-full rounded-full bg-primary"
                      style={{ width: `${item.share}%` }}
                    />
                  </div>
                </div>
              ))}
              <span className="text-[10px] leading-relaxed text-dim">
                {typedCount === rows.length
                  ? "Semua judul punya tipe yang diketahui."
                  : `${typedCount} dari ${rows.length} judul punya tipe yang diketahui.`}
              </span>
            </div>

            <div className="flex flex-col gap-2.5 rounded-[8px] border border-border-subtle bg-surface p-4">
              <span className="text-[12px] font-semibold">Genre teratas</span>
              {topGenres.length === 0 ? (
                <span className="text-[11px] text-dim">—</span>
              ) : (
                <div className="flex flex-wrap gap-1.5">
                  {topGenres.map((genre) => (
                    <span
                      key={genre.label}
                      className="inline-flex items-center gap-1 rounded-full bg-muted px-2 py-0.5 text-[10px] text-muted-foreground"
                    >
                      {genre.label}
                      <span className="font-mono text-[9px] text-dim">
                        {genre.count}
                      </span>
                    </span>
                  ))}
                </div>
              )}
            </div>

            <div className="rounded-[7px] bg-muted p-2.5">
              <p className="text-[10px] leading-relaxed text-dim">
                Peringkat diambil langsung dari komiku.org dan tidak disimpan di
                library. Klik judul untuk membuka detail series.
              </p>
            </div>
          </aside>
        </div>
      </div>
    </div>
  )
}
