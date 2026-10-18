import { useEffect, useMemo, useState } from "react"
import {
  ArrowDownToLine,
  Clock,
  Rss,
  Shuffle,
  Timer,
} from "lucide-react"

import {
  BarMeta,
  Chip,
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
  getFeed,
  isFresh,
  relativeBucket,
  type DiscoverySeries,
  type FeedOrder,
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

const tabOptions: { id: FeedOrder; label: string; icon: typeof Rss }[] = [
  { id: "update", label: "Chapter terbaru", icon: Rss },
  { id: "new", label: "Komik terbaru", icon: Clock },
  { id: "random", label: "Acak", icon: Shuffle },
]

const bucketOrder = ["KURANG DARI 3 JAM", "HARI INI", "MINGGU INI", "LAINNYA"]

export function LatestScreen({
  onOpenSeries,
}: {
  onOpenSeries: (slug: string, url?: string) => void
}) {
  const [tab, setTab] = useState<FeedOrder>("update")
  const [type, setType] = useState<TypeFilter>("")
  const [autoRefresh, setAutoRefresh] = useState(false)

  const feed = useAsync(
    () => getFeed({ order: tab, type, page: 1 }),
    [tab, type]
  )

  // Real 60s auto-refresh, only while the user asks for it.
  useEffect(() => {
    if (!autoRefresh) return
    const timer = window.setInterval(() => feed.reload(), 60_000)
    return () => window.clearInterval(timer)
  }, [autoRefresh, feed])

  // Stable identity so the derived group/rhythm memos do not recompute on
  // every render.
  const rows = useMemo(() => feed.data ?? [], [feed.data])

  const groups = useMemo(() => {
    const map = new Map<string, DiscoverySeries[]>()
    for (const entry of rows) {
      const bucket = relativeBucket(entry.updated)
      const list = map.get(bucket) ?? []
      list.push(entry)
      map.set(bucket, list)
    }
    return bucketOrder
      .filter((bucket) => (map.get(bucket)?.length ?? 0) > 0)
      .map((bucket) => ({ bucket, entries: map.get(bucket) ?? [] }))
  }, [rows])

  // The histogram is built from the real "N menit/jam lalu" strings in the
  // loaded page, so it never claims more precision than the feed provides.
  const rhythm = useMemo(() => {
    const buckets = [
      { label: "<3 jam", count: 0 },
      { label: "3–6 jam", count: 0 },
      { label: "6–12 jam", count: 0 },
      { label: "12–24 jam", count: 0 },
      { label: ">24 jam", count: 0 },
    ]
    for (const entry of rows) {
      const value = (entry.updated ?? "").toLowerCase()
      const hours = Number(/(\d+)\s*jam/.exec(value)?.[1] ?? -1)
      const days = Number(/(\d+)\s*hari/.exec(value)?.[1] ?? -1)
      if (value.includes("menit") || (hours >= 0 && hours < 3)) buckets[0].count++
      else if (hours >= 3 && hours < 6) buckets[1].count++
      else if (hours >= 6 && hours < 12) buckets[2].count++
      else if (hours >= 12 && hours < 24) buckets[3].count++
      else if (hours >= 24 || days >= 1) buckets[4].count++
    }
    const peak = Math.max(1, ...buckets.map((bucket) => bucket.count))
    return buckets.map((bucket) => ({
      ...bucket,
      height: Math.round((bucket.count / peak) * 100),
    }))
  }, [rows])

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <DiscoveryBar icon={Clock} label="Terbaru">
        <button
          type="button"
          onClick={() => setAutoRefresh((value) => !value)}
          aria-pressed={autoRefresh}
          className={cn(
            "flex items-center gap-1.5 rounded-full px-2 py-1 text-[10px] font-medium transition-colors",
            autoRefresh
              ? "bg-success-soft text-success"
              : "text-dim hover:text-muted-foreground"
          )}
        >
          <span
            className={cn(
              "size-1.5 rounded-full",
              autoRefresh ? "animate-pulse bg-success" : "bg-dim"
            )}
          />
          Auto-refresh 60s
        </button>
        <span className="mx-1 h-4 w-px bg-border" />
        <BarMeta icon={Rss}>komiku.org/pustaka</BarMeta>
        <RefreshButton busy={feed.loading} onClick={feed.reload} />
      </DiscoveryBar>

      <div className="min-h-0 flex-1 overflow-y-auto px-8 pb-8 pt-1">
        <div className="flex items-start gap-4">
          <div className="flex min-w-0 flex-col gap-1.5">
            <h1 className="text-[30px] font-semibold leading-[1.1] tracking-[-0.02em]">
              Update Terbaru
            </h1>
            <p className="text-[13px] text-muted-foreground">
              {formatCount(rows.length)} judul di halaman ini · sumber komiku.org
            </p>
          </div>
        </div>

        <div className="mt-5 flex flex-wrap items-center gap-3">
          <div className="flex items-center gap-5 border-b border-border-subtle">
            {tabOptions.map((option) => {
              const active = tab === option.id
              return (
                <button
                  key={option.id}
                  type="button"
                  onClick={() => setTab(option.id)}
                  className={cn(
                    "-mb-px flex h-9 items-center gap-1.5 border-b-2 text-[12px] transition-colors",
                    active
                      ? "border-primary font-semibold text-foreground"
                      : "border-transparent font-medium text-dim hover:text-muted-foreground"
                  )}
                >
                  <option.icon className="size-3.5" strokeWidth={1.8} />
                  {option.label}
                </button>
              )
            })}
          </div>
          <div className="flex-1" />
          {typeOptions.map((option) => (
            <Chip
              key={option.label}
              label={option.label}
              active={type === option.id}
              onClick={() => setType(option.id)}
            />
          ))}
        </div>

        <div className="mt-4 flex min-h-0 gap-5">
          <div className="flex min-w-0 flex-1 flex-col gap-4">
            {feed.loading && !feed.data ? (
              <LoadingState label="Membaca update terbaru…" />
            ) : feed.error ? (
              <ErrorState message={feed.error} onRetry={feed.reload} />
            ) : rows.length === 0 ? (
              <EmptyState
                title="Tidak ada update"
                hint="Coba tab atau filter lain."
                icon={Clock}
              />
            ) : (
              groups.map((group) => (
                <div key={group.bucket} className="flex flex-col gap-2">
                  <div className="flex items-center gap-2">
                    <span className="text-[10px] font-semibold tracking-[0.08em] text-dim">
                      {group.bucket}
                    </span>
                    <span className="font-mono text-[10px] text-dim">
                      {group.entries.length} update
                    </span>
                    <div className="h-px flex-1 bg-border-subtle" />
                  </div>

                  <div className="overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
                    {group.entries.map((entry) => (
                      <button
                        key={`${entry.slug}-${entry.chapter ?? entry.title}`}
                        type="button"
                        onClick={() => onOpenSeries(entry.slug, entry.url)}
                        className="flex w-full items-center gap-3 border-b border-border-subtle px-3.5 py-2.5 text-left transition-colors last:border-b-0 hover:bg-muted/50"
                      >
                        <RemoteCover
                          src={entry.cover}
                          title={entry.title}
                          className="h-[52px] w-[38px]"
                          rounded="rounded-[4px]"
                        />
                        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
                          <span className="flex min-w-0 items-center gap-1.5">
                            <span className="truncate text-[13px] font-medium">
                              {entry.title}
                            </span>
                            {isFresh(entry.updated) && <NewFlag />}
                          </span>
                          <span className="flex items-center gap-1.5">
                            <TypeChip type={entry.type} />
                            <span className="truncate text-[10px] text-muted-foreground">
                              {(entry.genres ?? []).slice(0, 2).join(" · ")}
                            </span>
                            {entry.color && (
                              <span className="shrink-0 text-[9px] text-info">
                                Berwarna
                              </span>
                            )}
                          </span>
                        </span>
                        <TimeChip value={entry.updated} />
                        {entry.chapter && (
                          <span className="shrink-0 rounded-full bg-muted px-2 py-0.5 font-mono text-[10px] text-muted-foreground">
                            {entry.chapter}
                          </span>
                        )}
                        <ArrowDownToLine
                          className="size-3.5 shrink-0 text-dim"
                          strokeWidth={1.8}
                        />
                      </button>
                    ))}
                  </div>
                </div>
              ))
            )}
          </div>

          <aside className="flex w-[318px] shrink-0 flex-col gap-3.5 overflow-y-auto">
            <div className="flex flex-col gap-3 rounded-[8px] border border-border-subtle bg-surface p-4">
              <div className="flex items-center gap-2">
                <Timer className="size-3.5 text-dim" strokeWidth={1.8} />
                <span className="text-[12px] font-semibold">Ritme update</span>
                <div className="flex-1" />
                <span className="font-mono text-[10px] text-dim">
                  {rows.length} judul
                </span>
              </div>
              <div className="flex h-16 items-end gap-1.5">
                {rhythm.map((bucket) => (
                  <div
                    key={bucket.label}
                    className="flex flex-1 flex-col items-center gap-1"
                    title={`${bucket.label}: ${bucket.count}`}
                  >
                    <div
                      className={cn(
                        "w-full rounded-[2px]",
                        bucket.count > 0 ? "bg-primary/70" : "bg-muted"
                      )}
                      style={{ height: `${Math.max(4, bucket.height)}%` }}
                    />
                  </div>
                ))}
              </div>
              <div className="flex items-center justify-between font-mono text-[9px] text-dim">
                {rhythm.map((bucket) => (
                  <span key={bucket.label}>{bucket.label}</span>
                ))}
              </div>
              <span className="text-[10px] leading-relaxed text-dim">
                Dihitung dari waktu update pada halaman yang sedang tampil.
              </span>
            </div>

            <div className="rounded-[7px] bg-muted p-2.5">
              <p className="text-[10px] leading-relaxed text-dim">
                Auto-refresh hanya memperbarui daftar. Unduhan tetap berjalan
                lewat layar Downloads.
              </p>
            </div>
          </aside>
        </div>
      </div>
    </div>
  )
}
