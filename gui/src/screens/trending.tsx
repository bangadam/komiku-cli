import { useMemo, useState } from "react"
import { Flame, TrendingUp } from "lucide-react"

import { Button } from "@/components/ui/button"
import {
  BarMeta,
  Chip,
  DeltaBadge,
  DiscoveryBar,
  RankBadge,
  RefreshButton,
  RemoteCover,
  SegmentedControl,
  TypeChip,
} from "@/components/discovery"
import { EmptyState, ErrorState, LoadingState } from "@/components/states"
import {
  formatCount,
  getTrending,
  type TrendingPeriod,
} from "@/lib/discovery"
import { useAsync } from "@/lib/use-async"

const periodOptions: { id: TrendingPeriod; label: string }[] = [
  { id: "daily", label: "24 jam" },
  { id: "weekly", label: "7 hari" },
  { id: "monthly", label: "30 hari" },
]

const windowLabel: Record<TrendingPeriod, string> = {
  daily: "24 jam",
  weekly: "7 hari",
  monthly: "30 hari",
  active: "realtime",
}

type TypeFilter = "" | "manga" | "manhwa" | "manhua"

const typeOptions: { id: TypeFilter; label: string }[] = [
  { id: "", label: "Semua" },
  { id: "manga", label: "Manga" },
  { id: "manhwa", label: "Manhwa" },
  { id: "manhua", label: "Manhua" },
]

export function TrendingScreen({
  onOpenSeries,
}: {
  onOpenSeries: (slug: string, url?: string) => void
}) {
  const [period, setPeriod] = useState<TrendingPeriod>("daily")
  const [type, setType] = useState<TypeFilter>("")

  const trending = useAsync(() => getTrending(period, 40), [period])

  const rows = useMemo(
    () =>
      (trending.data?.series ?? []).filter(
        (entry) => !type || entry.type?.toLowerCase() === type
      ),
    [trending.data, type]
  )

  const hero = rows[0]
  const rest = rows.slice(1, 5)

  const genreMix = useMemo(() => {
    const counts = new Map<string, number>()
    for (const entry of rows) {
      for (const genre of entry.genres ?? []) {
        counts.set(genre, (counts.get(genre) ?? 0) + 1)
      }
    }
    const total = rows.length || 1
    return [...counts.entries()]
      .map(([label, count]) => ({
        label,
        count,
        share: Math.round((count / total) * 100),
      }))
      .sort((a, b) => b.count - a.count)
      .slice(0, 4)
  }, [rows])

  const topViews = useMemo(
    () =>
      [...rows]
        .filter((entry) => entry.views)
        .sort((a, b) => (b.views ?? 0) - (a.views ?? 0))
        .slice(0, 5),
    [rows]
  )

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <DiscoveryBar icon={TrendingUp} label="Trending">
        <BarMeta icon={Flame}>komiku.org/p/trending</BarMeta>
        <span className="mx-1 h-4 w-px bg-border" />
        <RefreshButton busy={trending.loading} onClick={trending.reload} />
      </DiscoveryBar>

      <div className="min-h-0 flex-1 overflow-y-auto px-8 pb-8 pt-1">
        <div className="flex items-start gap-4">
          <div className="flex min-w-0 flex-col gap-1.5">
            <h1 className="text-[30px] font-semibold leading-[1.1] tracking-[-0.02em]">
              Trending Now
            </h1>
            <p className="text-[13px] text-muted-foreground">
              Judul yang paling banyak dibaca dalam {windowLabel[period]}{" "}
              terakhir
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
          <span className="font-mono text-[11px] text-dim">
            {rows.length} judul
          </span>
        </div>

        <div className="mt-4 flex min-h-0 gap-5">
          <div className="flex min-w-0 flex-1 flex-col gap-4">
            {trending.loading && !trending.data ? (
              <div className="flex flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
                <LoadingState label="Membaca data trending…" />
              </div>
            ) : trending.error ? (
              <div className="flex flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
                <ErrorState message={trending.error} onRetry={trending.reload} />
              </div>
            ) : !hero ? (
              <div className="flex flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
                <EmptyState
                  title="Tidak ada judul trending"
                  hint="Coba filter tipe lain."
                  icon={TrendingUp}
                />
              </div>
            ) : (
              <>
                {/* Hero */}
                <div className="flex gap-5 rounded-[8px] border border-border-subtle bg-surface p-4">
                  <RemoteCover
                    src={hero.cover}
                    title={hero.title}
                    className="h-[190px] w-[136px]"
                    rounded="rounded-[7px]"
                  />
                  <div className="flex min-w-0 flex-1 flex-col gap-2.5">
                    <div className="flex items-center gap-2">
                      <span className="inline-flex items-center gap-1 rounded-full bg-primary-soft px-2 py-0.5 text-[10px] font-semibold text-primary">
                        <Flame className="size-2.5" strokeWidth={2} />
                        #1 Trending
                      </span>
                      <DeltaBadge value={windowLabel[period]} />
                    </div>
                    <button
                      type="button"
                      onClick={() => onOpenSeries(hero.slug, hero.url)}
                      className="text-left text-[22px] font-semibold leading-tight tracking-[-0.01em] transition-colors hover:text-primary"
                    >
                      {hero.title}
                    </button>
                    <div className="flex flex-wrap items-center gap-1.5">
                      <TypeChip type={hero.type} />
                      {(hero.genres ?? []).slice(0, 3).map((genre) => (
                        <span
                          key={genre}
                          className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground"
                        >
                          {genre}
                        </span>
                      ))}
                      {hero.status && (
                        <span className="rounded-full bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
                          {hero.status}
                        </span>
                      )}
                    </div>

                    <div className="mt-1 flex items-center gap-6">
                      <div className="flex flex-col gap-0.5">
                        <span className="font-mono text-[17px] font-semibold leading-none">
                          {formatCount(hero.views)}
                        </span>
                        <span className="text-[10px] text-dim">Views</span>
                      </div>
                      <div className="flex flex-col gap-0.5">
                        <span className="font-mono text-[17px] font-semibold leading-none">
                          #{hero.rank}
                        </span>
                        <span className="text-[10px] text-dim">Peringkat</span>
                      </div>
                    </div>

                    <div className="mt-auto flex items-center gap-2">
                      <Button
                        size="sm"
                        className="h-8 px-3 text-[12px]"
                        onClick={() => onOpenSeries(hero.slug, hero.url)}
                      >
                        Buka series
                      </Button>
                    </div>
                  </div>
                </div>

                {/* Shelf */}
                <div className="flex flex-col gap-2.5">
                  <div className="flex items-center gap-2">
                    <h2 className="text-[16px] font-semibold">
                      Trending berikutnya
                    </h2>
                    <div className="flex-1" />
                    <span className="font-mono text-[10px] text-dim">
                      {rest.length} judul
                    </span>
                  </div>
                  <div className="grid grid-cols-4 gap-3">
                    {rest.map((entry) => (
                      <button
                        key={entry.slug}
                        type="button"
                        onClick={() => onOpenSeries(entry.slug, entry.url)}
                        className="group flex flex-col gap-1.5 text-left"
                      >
                        <div className="relative">
                          <RemoteCover
                            src={entry.cover}
                            title={entry.title}
                            className="aspect-[2/3] w-full"
                            rounded="rounded-[6px]"
                          />
                          <span className="absolute left-1.5 top-1.5">
                            <RankBadge rank={entry.rank ?? 0} />
                          </span>
                          <DeltaBadge
                            value={`${formatCount(entry.views)}`}
                            className="absolute bottom-1.5 right-1.5 bg-background/85 text-foreground backdrop-blur"
                          />
                        </div>
                        <span className="truncate text-[12px] font-medium group-hover:text-primary">
                          {entry.title}
                        </span>
                        <span className="truncate text-[10px] text-muted-foreground">
                          {[entry.type, (entry.genres ?? [])[0]]
                            .filter(Boolean)
                            .join(" · ")}
                        </span>
                      </button>
                    ))}
                  </div>
                </div>
              </>
            )}
          </div>

          <aside className="flex w-[316px] shrink-0 flex-col gap-3.5 overflow-y-auto">
            <div className="flex flex-col gap-3 rounded-[8px] border border-border-subtle bg-surface p-4">
              <div className="flex items-center gap-2">
                <span className="text-[12px] font-semibold">
                  Genre paling panas
                </span>
                <div className="flex-1" />
                <span className="font-mono text-[10px] text-dim">
                  {windowLabel[period]}
                </span>
              </div>
              {genreMix.length === 0 ? (
                <span className="text-[11px] text-dim">—</span>
              ) : (
                genreMix.map((genre) => (
                  <div key={genre.label} className="flex flex-col gap-1">
                    <div className="flex items-center text-[11px]">
                      <span className="text-muted-foreground">{genre.label}</span>
                      <div className="flex-1" />
                      <span className="font-mono text-[10px] text-dim">
                        {genre.share}%
                      </span>
                    </div>
                    <div className="h-1 overflow-hidden rounded-full bg-muted">
                      <div
                        className="h-full rounded-full bg-primary"
                        style={{ width: `${genre.share}%` }}
                      />
                    </div>
                  </div>
                ))
              )}
              <span className="text-[10px] leading-relaxed text-dim">
                Porsi judul trending yang memuat genre tersebut.
              </span>
            </div>

            <div className="flex flex-col gap-2.5 rounded-[8px] border border-border-subtle bg-surface p-4">
              <span className="text-[12px] font-semibold">Views tertinggi</span>
              {topViews.map((entry) => (
                <button
                  key={entry.slug}
                  type="button"
                  onClick={() => onOpenSeries(entry.slug, entry.url)}
                  className="flex items-center gap-2 text-left"
                >
                  <span className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground hover:text-foreground">
                    {entry.title}
                  </span>
                  <span className="shrink-0 font-mono text-[10px] text-dim">
                    {formatCount(entry.views)}
                  </span>
                </button>
              ))}
            </div>

            <div className="rounded-[7px] bg-muted p-2.5">
              <p className="text-[10px] leading-relaxed text-dim">
                Trending diambil dari analytics komiku.org. Buka series untuk
                memilih chapter yang ingin diunduh.
              </p>
            </div>
          </aside>
        </div>
      </div>
    </div>
  )
}
