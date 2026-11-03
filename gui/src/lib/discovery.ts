import { runCliJson } from "./cli"

/* ---------- shapes emitted by the Go discovery commands ---------- */

export interface DiscoverySeries {
  title: string
  slug: string
  url: string
  cover?: string
  type?: string
  genres?: string[] | null
  status?: string
  rank?: number
  views?: number
  chapter?: string
  chapter_url?: string
  updated?: string
  color?: boolean
}

export interface RankingReport {
  period: string
  series: DiscoverySeries[] | null
}

export interface TrendingReport {
  period: string
  total: number
  series: DiscoverySeries[] | null
}

export interface FeedReport {
  order: string
  type?: string
  genre?: string
  genre2?: string
  page: number
  series: DiscoverySeries[] | null
}

export interface AlphabetCount {
  letter: string
  count: number
}

export interface CatalogReport {
  letter?: string
  type?: string
  status?: string
  page: number
  total_pages: number
  total: number
  alphabet: AlphabetCount[] | null
  series: DiscoverySeries[] | null
}

export interface Genre {
  name: string
  slug: string
  count: number
}

export interface GenreListReport {
  genres: Genre[] | null
}

export interface GenreSeriesReport {
  genre: string
  page: number
  series: DiscoverySeries[] | null
}

export type RankingPeriod = "daily" | "weekly" | "monthly" | "all"
export type TrendingPeriod = "daily" | "weekly" | "monthly" | "active"
export type FeedOrder = "update" | "new" | "random"
export type DiscoveryType = "" | "manga" | "manhwa" | "manhua"

/* ---------- read commands ---------- */

export async function getRanking(period: RankingPeriod): Promise<DiscoverySeries[]> {
  const report = await runCliJson<RankingReport>([
    "ranking",
    "--period",
    period,
    "--json",
  ])
  return report.series ?? []
}

export async function getTrending(
  period: TrendingPeriod,
  limit = 40
): Promise<TrendingReport> {
  const report = await runCliJson<TrendingReport>([
    "trending",
    "--period",
    period,
    "--limit",
    String(limit),
    "--json",
  ])
  return { ...report, series: report.series ?? [] }
}

export async function getFeed(options: {
  order: FeedOrder
  type?: DiscoveryType
  genre?: string
  genre2?: string
  page?: number
}): Promise<DiscoverySeries[]> {
  const command =
    options.order === "update"
      ? "latest"
      : options.order === "new"
        ? "new"
        : "random"
  const args = [command, "--json"]
  if (options.type) args.push("--type", options.type)
  if (options.genre) args.push("--genre", options.genre)
  if (options.genre2) args.push("--genre2", options.genre2)
  if (options.page && options.page > 1) args.push("--page", String(options.page))
  const report = await runCliJson<FeedReport>(args)
  return report.series ?? []
}

export async function getCatalog(options: {
  letter?: string
  type?: DiscoveryType
  status?: "" | "ongoing" | "completed"
  page?: number
}): Promise<CatalogReport> {
  const args = ["catalog", "--json"]
  if (options.letter && options.letter !== "All") {
    args.push("--letter", options.letter)
  }
  if (options.type) args.push("--type", options.type)
  if (options.status) args.push("--status", options.status)
  if (options.page && options.page > 1) args.push("--page", String(options.page))
  const report = await runCliJson<CatalogReport>(args)
  return {
    ...report,
    alphabet: report.alphabet ?? [],
    series: report.series ?? [],
  }
}

export async function getGenres(): Promise<Genre[]> {
  const report = await runCliJson<GenreListReport>(["genres", "--json"])
  return report.genres ?? []
}

export async function getGenreSeries(
  genre: string,
  options: { sort?: string; type?: DiscoveryType; page?: number } = {}
): Promise<DiscoverySeries[]> {
  const args = ["genre", genre, "--json"]
  if (options.sort) args.push("--sort", options.sort)
  if (options.type) args.push("--type", options.type)
  if (options.page && options.page > 1) args.push("--page", String(options.page))
  const report = await runCliJson<GenreSeriesReport>(args)
  return report.series ?? []
}

/* ---------- presentation helpers ---------- */

const compact = new Intl.NumberFormat("id-ID")

/** 46838 → "46.838", matching the design's Indonesian grouping. */
export function formatCount(value?: number): string {
  if (!value) return "0"
  return compact.format(value)
}

export function typeLabel(type?: string): string {
  if (!type) return ""
  const lower = type.toLowerCase()
  return lower.charAt(0).toUpperCase() + lower.slice(1)
}

export function seriesTitleFromSlug(slug: string): string {
  return slug
    .split(/[-_]+/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ")
}

/** Group a feed into relative time buckets for the Latest screen. */
export function relativeBucket(updated?: string): string {
  if (!updated) return "TODAY"
  const value = updated.toLowerCase()
  const minutes = /(\d+)\s*menit/.exec(value)
  if (minutes) return "LESS THAN 3 HOURS"
  const hours = /(\d+)\s*jam/.exec(value)
  if (hours) return Number(hours[1]) < 3 ? "LESS THAN 3 HOURS" : "TODAY"
  const days = /(\d+)\s*hari/.exec(value)
  if (days) return "THIS WEEK"
  return "TODAY"
}

const relativeUnits: Record<string, string> = {
  menit: "min",
  jam: "h",
  hari: "d",
}

/**
 * The feed reports its update age in the site's language ("5 menit lalu").
 * Render it with English units; anything unrecognised passes through.
 */
export function formatUpdated(value?: string): string | undefined {
  if (!value) return undefined
  const match = /^(\d+)\s*(menit|jam|hari)(?:\s*lalu)?$/i.exec(value.trim())
  if (!match) return value
  const unit = relativeUnits[match[2].toLowerCase()]
  return `${match[1]} ${unit} ago`
}

export function isFresh(updated?: string): boolean {
  if (!updated) return false
  const value = updated.toLowerCase()
  const minutes = /(\d+)\s*menit/.exec(value)
  if (minutes) return true
  const hours = /(\d+)\s*jam/.exec(value)
  if (hours) return Number(hours[1]) <= 24
  return false
}

/* ---------- catalogue totals (cached) ---------- */

export interface CatalogTotals {
  all: number
  manga: number
  manhwa: number
  manhua: number
}

let totalsPromise: Promise<CatalogTotals> | null = null

/**
 * Total title count per type. The catalogue page reports its own total, so
 * these numbers are read from the site rather than estimated. One shared
 * promise keeps the four requests from repeating across screens.
 */
export function getCatalogTotals(): Promise<CatalogTotals> {
  if (totalsPromise) return totalsPromise
  totalsPromise = (async () => {
    const [all, manga, manhwa, manhua] = await Promise.all([
      getCatalog({ page: 1 }),
      getCatalog({ type: "manga", page: 1 }),
      getCatalog({ type: "manhwa", page: 1 }),
      getCatalog({ type: "manhua", page: 1 }),
    ])
    return {
      all: all.total,
      manga: manga.total,
      manhwa: manhwa.total,
      manhua: manhua.total,
    }
  })().catch((error) => {
    totalsPromise = null
    throw error
  })
  return totalsPromise
}
