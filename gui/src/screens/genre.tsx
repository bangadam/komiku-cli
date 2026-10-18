import { useMemo, useState } from "react"
import { Filter, Layers, Plus, Search, Tags, X } from "lucide-react"

import {
  BarMeta,
  DiscoveryBar,
  RemoteCover,
  RefreshButton,
  SegmentedControl,
  TypeChip,
} from "@/components/discovery"
import { EmptyState, ErrorState, LoadingState } from "@/components/states"
import {
  formatCount,
  getFeed,
  getGenreSeries,
  getGenres,
  type DiscoverySeries,
  type Genre,
} from "@/lib/discovery"
import { useAsync } from "@/lib/use-async"
import { cn } from "@/lib/utils"

type SortKey = "popular" | "latest" | "title"

const sortOptions: { id: SortKey; label: string }[] = [
  { id: "popular", label: "Populer" },
  { id: "latest", label: "Terbaru" },
  { id: "title", label: "Judul A–Z" },
]

/** Quick collections must exist in the real genre index to be clickable. */
const quickCollections = [
  { label: "Isekai", slug: "isekai" },
  { label: "Romance", slug: "romance" },
  { label: "Fantasy", slug: "fantasy" },
]

const sortToCli: Record<SortKey, string> = {
  popular: "rating",
  latest: "update",
  title: "date",
}

export function GenreScreen({
  onOpenSeries,
}: {
  onOpenSeries: (slug: string, url?: string) => void
}) {
  const [genre, setGenre] = useState("action")
  const [secondGenre, setSecondGenre] = useState("")
  const [appliedSecond, setAppliedSecond] = useState("")
  const [sort, setSort] = useState<SortKey>("popular")
  const [search, setSearch] = useState("")
  const [page, setPage] = useState(1)

  const genres = useAsync(getGenres, [])

  const listing = useAsync<DiscoverySeries[]>(() => {
    if (appliedSecond) {
      return getFeed({
        order: "update",
        genre,
        genre2: appliedSecond,
        page,
      })
    }
    return getGenreSeries(genre, { sort: sortToCli[sort], page })
  }, [genre, appliedSecond, sort, page])

  // Stable identities: `?? []` allocates a fresh array on every render, which
  // would invalidate every derived memo below.
  const genreList = useMemo(() => genres.data ?? [], [genres.data])
  const rows = useMemo(() => listing.data ?? [], [listing.data])

  const visibleGenres = useMemo(() => {
    const term = search.trim().toLowerCase()
    return term
      ? genreList.filter((entry) => entry.name.toLowerCase().includes(term))
      : genreList
  }, [genreList, search])

  const popular = useMemo(
    () => [...genreList].sort((a, b) => b.count - a.count).slice(0, 9),
    [genreList]
  )

  const currentGenre: Genre | undefined = genreList.find(
    (entry) => entry.slug === genre
  )
  const second: Genre | undefined = genreList.find(
    (entry) => entry.slug === appliedSecond
  )

  function chooseGenre(slug: string) {
    setGenre(slug)
    setAppliedSecond("")
    setSecondGenre("")
    setPage(1)
  }

  function applyCombo() {
    setAppliedSecond(secondGenre)
    setPage(1)
  }

  function clearCombo() {
    setAppliedSecond("")
    setSecondGenre("")
    setPage(1)
  }

  return (
    <div className="flex h-full flex-col overflow-hidden">
      <DiscoveryBar icon={Tags} label="Genre" crumb={currentGenre?.name ?? genre}>
        <BarMeta icon={Tags}>
          {formatCount(genreList.length)} genre ·{" "}
          {formatCount(currentGenre?.count ?? 0)} judul
        </BarMeta>
      </DiscoveryBar>

      <div className="min-h-0 flex-1 overflow-y-auto px-8 pb-8 pt-1">
        <div className="flex items-start gap-4">
          <div className="flex min-w-0 flex-col gap-1.5">
            <h1 className="text-[30px] font-semibold leading-[1.1] tracking-[-0.02em]">
              Daftar Genre
            </h1>
            <p className="text-[13px] text-muted-foreground">
              Pilih genre untuk melihat judul, atau gabungkan dua genre sekaligus
            </p>
          </div>
          <div className="flex-1" />
          <div className="flex h-[34px] w-[240px] items-center gap-2 rounded-[6px] border border-border bg-surface px-2.5">
            <Search className="size-3.5 shrink-0 text-dim" strokeWidth={1.8} />
            <input
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              placeholder="Cari genre…"
              aria-label="Cari genre"
              spellCheck={false}
              className="min-w-0 flex-1 bg-transparent text-[12px] outline-none placeholder:text-dim"
            />
          </div>
        </div>

        {/* Combine bar */}
        <div className="mt-5 flex flex-wrap items-center gap-2 rounded-[8px] border border-border-subtle bg-surface px-3.5 py-3">
          <Layers className="size-[15px] shrink-0 text-dim" strokeWidth={1.8} />
          <span className="text-[12px] font-medium">Gabungkan</span>

          <select
            aria-label="Genre pertama"
            value={genre}
            onChange={(event) => chooseGenre(event.target.value)}
            className="h-7 rounded-[5px] border border-border bg-card px-2 text-[11px] text-foreground outline-none focus:border-primary/60"
          >
            {visibleGenres.map((entry) => (
              <option key={entry.slug} value={entry.slug}>
                {entry.name}
              </option>
            ))}
          </select>

          <Plus className="size-3 shrink-0 text-dim" strokeWidth={1.8} />

          <select
            aria-label="Genre kedua"
            value={secondGenre}
            onChange={(event) => setSecondGenre(event.target.value)}
            className="h-7 rounded-[5px] border border-border bg-card px-2 text-[11px] text-foreground outline-none focus:border-primary/60"
          >
            <option value="">Genre 2 (opsional)</option>
            {visibleGenres
              .filter((entry) => entry.slug !== genre)
              .map((entry) => (
                <option key={entry.slug} value={entry.slug}>
                  {entry.name}
                </option>
              ))}
          </select>

          <div className="flex-1" />

          <span className="font-mono text-[10px] text-dim">
            {formatCount(rows.length)} judul di halaman ini
          </span>

          <button
            type="button"
            onClick={applyCombo}
            disabled={!secondGenre || secondGenre === genre}
            className="h-7 rounded-[5px] bg-primary px-2.5 text-[11px] font-medium text-primary-foreground transition-opacity hover:opacity-90 disabled:opacity-40"
          >
            Terapkan
          </button>
          {appliedSecond && (
            <button
              type="button"
              onClick={clearCombo}
              aria-label="Hapus kombinasi"
              className="flex size-7 items-center justify-center rounded-[5px] text-dim transition-colors hover:text-foreground"
            >
              <X className="size-3.5" strokeWidth={1.8} />
            </button>
          )}
        </div>

        <div className="mt-4 flex flex-wrap items-center gap-1.5">
          <span className="text-[10px] font-semibold tracking-[0.08em] text-dim">
            URUTKAN
          </span>
          <SegmentedControl
            options={sortOptions}
            value={sort}
            onChange={(value) => {
              setSort(value)
              setPage(1)
            }}
          />
          <div className="flex-1" />
          <span className="font-mono text-[11px] text-dim">
            {appliedSecond
              ? `${currentGenre?.name ?? genre} + ${second?.name ?? appliedSecond}`
              : `${currentGenre?.name ?? genre} · ${formatCount(
                  currentGenre?.count
                )} judul`}
          </span>
        </div>

        <div className="mt-4 flex min-h-0 gap-5">
          <div className="flex min-w-0 flex-1 flex-col">
            {listing.loading && !listing.data ? (
              <div className="flex flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
                <LoadingState label="Membaca judul genre…" />
              </div>
            ) : listing.error ? (
              <div className="flex flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
                <ErrorState message={listing.error} onRetry={listing.reload} />
              </div>
            ) : rows.length === 0 ? (
              <div className="flex flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
                <EmptyState
                  title="Tidak ada judul"
                  hint="Coba genre lain atau hapus kombinasi."
                  icon={Filter}
                />
              </div>
            ) : (
              <div className="overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
                {rows.map((entry) => (
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
                      <span className="truncate text-[13px] font-medium">
                        {entry.title}
                      </span>
                      <span className="flex items-center gap-1.5">
                        <TypeChip type={entry.type} />
                        <span className="truncate text-[10px] text-muted-foreground">
                          {(entry.genres ?? []).join(" · ") || "—"}
                        </span>
                      </span>
                    </span>
                    {entry.updated && (
                      <span className="shrink-0 font-mono text-[10px] text-dim">
                        {entry.updated}
                      </span>
                    )}
                    {entry.chapter && (
                      <span className="shrink-0 rounded-full bg-muted px-2 py-0.5 font-mono text-[10px] text-muted-foreground">
                        {entry.chapter}
                      </span>
                    )}
                  </button>
                ))}
              </div>
            )}
          </div>

          <aside className="flex w-[300px] shrink-0 flex-col gap-3.5 overflow-y-auto">
            <div className="flex flex-col gap-2.5 rounded-[8px] border border-border-subtle bg-surface p-4">
              <div className="flex items-center gap-2">
                <span className="text-[12px] font-semibold">Genre populer</span>
                <div className="flex-1" />
                <span className="font-mono text-[10px] text-dim">
                  {genreList.length} genre
                </span>
              </div>
              <div className="flex flex-col gap-1">
                {popular.map((entry) => (
                  <button
                    key={entry.slug}
                    type="button"
                    onClick={() => chooseGenre(entry.slug)}
                    className={cn(
                      "flex items-center gap-2 rounded-[5px] px-1.5 py-1 text-left transition-colors",
                      entry.slug === genre
                        ? "bg-primary-soft text-primary"
                        : "text-muted-foreground hover:bg-muted hover:text-foreground"
                    )}
                  >
                    <span className="min-w-0 flex-1 truncate text-[11px]">
                      {entry.name}
                    </span>
                    <span className="shrink-0 font-mono text-[10px] text-dim">
                      {formatCount(entry.count)}
                    </span>
                  </button>
                ))}
                {genres.loading && popular.length === 0 && (
                  <span className="text-[11px] text-dim">Memuat genre…</span>
                )}
              </div>
            </div>

            <div className="flex flex-col gap-2 rounded-[8px] border border-border-subtle bg-surface p-4">
              <span className="text-[12px] font-semibold">Koleksi cepat</span>
              {quickCollections.map((entry) => {
                const found = genreList.find((item) => item.slug === entry.slug)
                return (
                  <button
                    key={entry.slug}
                    type="button"
                    onClick={() => chooseGenre(entry.slug)}
                    disabled={!found}
                    className="flex items-center gap-2 rounded-[5px] px-1.5 py-1 text-left transition-colors hover:bg-muted disabled:opacity-40"
                  >
                    <span className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground">
                      {entry.label}
                    </span>
                    <span className="shrink-0 font-mono text-[10px] text-dim">
                      {found ? `${formatCount(found.count)} judul` : "—"}
                    </span>
                  </button>
                )
              })}
            </div>

            <div className="rounded-[7px] bg-muted p-2.5">
              <p className="text-[10px] leading-relaxed text-dim">
                Kombinasi dua genre memakai filter genre ganda dari komiku.org.
                Urutan hasil mengikuti situs.
              </p>
            </div>
          </aside>
        </div>

        <div className="mt-4 flex items-center gap-2">
          <button
            type="button"
            onClick={() => setPage((value) => Math.max(1, value - 1))}
            disabled={page <= 1}
            className="h-7 rounded-[5px] border border-border-subtle px-2.5 text-[11px] text-muted-foreground transition-colors hover:text-foreground disabled:opacity-40"
          >
            Sebelumnya
          </button>
          <span className="font-mono text-[10px] text-dim">halaman {page}</span>
          <button
            type="button"
            onClick={() => setPage((value) => value + 1)}
            disabled={rows.length === 0}
            className="h-7 rounded-[5px] border border-border-subtle px-2.5 text-[11px] text-muted-foreground transition-colors hover:text-foreground disabled:opacity-40"
          >
            Berikutnya
          </button>
          <div className="flex-1" />
          <RefreshButton busy={listing.loading} onClick={listing.reload} />
        </div>
      </div>
    </div>
  )
}
