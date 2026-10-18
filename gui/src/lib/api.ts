import {
  CliError,
  cliContext,
  runCli,
  runCliJson,
  runCliStream,
  type CliLine,
  type StreamHandle,
} from "./cli"

/* ---------- raw CLI shapes (mirror the Go structs) ---------- */

export interface LibrarySeries {
  slug: string
  dir: string
  /** Source URL recorded by the CLI (pack manifest); absent when unknown. */
  series_url?: string
  chapters: number
  done: number
  cbz: number
  bytes: number
  healthy: boolean
  problems: number
  problems_detail?: string[]
  /** Local cover file, when one has been saved. */
  cover?: string
}

export interface LibraryReport {
  root: string
  series: LibrarySeries[] | null
  total_chapters: number
  total_done: number
  total_cbz: number
  total_bytes: number
  total_problems: number
}

export interface ChapterInfo {
  display: string
  number: number
  url: string
  done: boolean
}

export interface VolumeInfo {
  volume: number
  start: number
  end: number
}

export interface SeriesInfo {
  series: string
  series_url?: string
  chapters: ChapterInfo[]
  done_count: number
  volumes?: VolumeInfo[]
  volume_error?: string
  /** Local cover file, when one has been saved. */
  cover?: string
}

export interface SearchHit {
  Title: string
  Slug: string
  Href: string
  URL: string
}

export interface VerifyChapter {
  dir: string
  number: number
  chapter: string
  pages: number
  valid_pages: number
  broken_pages?: string[]
  stray_files?: string[]
  missing_pages?: number[]
}

export interface VerifyReport {
  series_dir: string
  chapters: VerifyChapter[]
  done_count: number
  chapters_not_in_state?: string[]
  healthy: boolean
  problems: number
}

export interface UpdateSeries {
  slug: string
  series_url: string
  new_chapters?: string[]
  downloaded: number
  skipped: number
  failed: number
  error?: string
}

export interface UpdateReport {
  series: UpdateSeries[] | null
}

export interface ConfigReport {
  output_root: string
  image_delay: string
  preset: string
  config_path: string
}

export interface ProgressEvent {
  kind: string
  index?: number
  chapter?: string
  number?: number
  page?: number
  pages?: number
  bytes?: number
  status?: string
  success?: number
  total?: number
  message?: string
  requested?: number
  started?: number
}

/* ---------- read commands ---------- */

export async function getLibrary(out?: string): Promise<LibraryReport> {
  const args = ["library", "--json"]
  if (out) args.push("--out", out)
  const [report, context] = await Promise.all([
    runCliJson<LibraryReport>(args),
    cliContext(),
  ])
  return {
    ...report,
    root: resolvePath(context.cwd, report.root),
    series: (report.series ?? []).map((item) => ({
      ...item,
      dir: resolvePath(context.cwd, item.dir),
      cover: item.cover ? resolvePath(context.cwd, item.cover) : undefined,
    })),
  }
}

/** Join a possibly-relative CLI path onto the CLI working directory. */
export function resolvePath(cwd: string, value: string): string {
  if (!value) return cwd
  if (value.startsWith("/") || /^[A-Za-z]:[/\\]/.test(value)) return value
  if (value === "." || value === "./") return cwd
  return `${cwd.replace(/\/+$/, "")}/${value.replace(/^\.\//, "")}`
}

export async function getConfig(): Promise<ConfigReport> {
  const [report, context] = await Promise.all([
    runCliJson<ConfigReport>(["config", "--json"]),
    cliContext(),
  ])
  return { ...report, output_root: resolvePath(context.cwd, report.output_root) }
}

export async function saveConfig(values: {
  out?: string
  delay?: string
  preset?: string
}): Promise<ConfigReport> {
  const args = ["config", "--json"]
  if (values.out !== undefined) args.push("--out", values.out)
  if (values.delay !== undefined) args.push("--delay", values.delay)
  if (values.preset !== undefined) args.push("--preset", values.preset)
  return runCliJson<ConfigReport>(args)
}

export async function searchSeries(query: string): Promise<SearchHit[]> {
  const trimmed = query.trim()
  if (!trimmed) return []
  return runCliJson<SearchHit[]>(["search", trimmed, "--json"])
}

export type VolumeSource = "mangadex" | "auto" | "wikipedia" | "fandom"

export async function getSeriesInfo(
  url: string,
  options: {
    out?: string
    volumes?: boolean
    volumeSource?: VolumeSource
    offline?: boolean
  } = {}
): Promise<SeriesInfo> {
  const args = ["info", url, "--json"]
  if (options.out) args.push("--out", options.out)
  if (options.volumes) args.push("--volumes")
  if (options.volumeSource) args.push("--volume-source", options.volumeSource)
  if (options.offline) args.push("--offline")
  const [info, context] = await Promise.all([
    runCliJson<SeriesInfo>(args),
    cliContext(),
  ])
  return {
    ...info,
    cover: info.cover ? resolvePath(context.cwd, info.cover) : undefined,
  }
}

export interface CoverReport {
  slug: string
  cover?: string
  source?: string
  bytes?: number
  skipped?: boolean
  error?: string
}

/**
 * Save cover art locally. Passing no URL runs the library-wide backfill, which
 * is what the GUI uses to fill covers for series downloaded before this
 * feature existed.
 */
export async function fetchCovers(
  url?: string,
  options: { out?: string; force?: boolean } = {}
): Promise<CoverReport[]> {
  const args = ["cover"]
  if (url) args.push(url)
  else args.push("--all")
  if (options.out) args.push("--out", options.out)
  if (options.force) args.push("--force")
  args.push("--json")
  const [reports, context] = await Promise.all([
    runCliJson<CoverReport[]>(args),
    cliContext(),
  ])
  return reports.map((report) => ({
    ...report,
    cover: report.cover ? resolvePath(context.cwd, report.cover) : undefined,
  }))
}

export async function checkUpdates(): Promise<UpdateReport> {
  const report = await runCliJson<UpdateReport>(["update", "--check", "--json"])
  return { series: report.series ?? [] }
}

export async function runUpdate(out?: string): Promise<UpdateReport> {
  const args = ["update", "--json"]
  if (out) args.push("--out", out)
  const report = await runCliJson<UpdateReport>(args)
  return { series: report.series ?? [] }
}

export async function verifySeries(dir: string): Promise<VerifyReport> {
  const context = await cliContext()
  return runCliJson<VerifyReport>(["verify", resolvePath(context.cwd, dir), "--json"])
}

/* ---------- write commands ---------- */

export interface PackOptions {
  volumes?: string
  preset?: string
  flat?: boolean
  series?: string
  recoverWikipedia?: boolean
  wikipediaTitle?: string
  volumeSource?: VolumeSource
  /** Explicit mapping ("1:1-5,2:6-14"); packs offline without a manifest. */
  volumeMap?: string
}

export interface PackResult {
  archives: string[]
  warnings: string[]
  output: string
}

export async function packSeries(
  dir: string,
  options: PackOptions
): Promise<PackResult> {
  const context = await cliContext()
  const args = ["pack", resolvePath(context.cwd, dir)]
  if (options.volumes) args.push("--vol", options.volumes)
  if (options.preset) args.push("--preset", options.preset)
  if (options.flat) args.push("--flat")
  if (options.series) args.push("--series", options.series)
  if (options.recoverWikipedia) args.push("--recover-wikipedia")
  if (options.wikipediaTitle) args.push("--wikipedia-title", options.wikipediaTitle)
  if (options.volumeSource) args.push("--volume-source", options.volumeSource)
  if (options.volumeMap) args.push("--volume-map", options.volumeMap)

  const result = await runCli(args)
  if (!result.ok) {
    throw new CliError(
      result.stderr.trim().split("\n")[0] || "pack failed",
      result.stderr,
      result.code
    )
  }

  const archives: string[] = []
  const warnings: string[] = []
  for (const line of result.stdout.split("\n")) {
    const trimmed = line.trim()
    if (trimmed.startsWith("packed:")) {
      archives.push(
        trimmed.replace(/^packed:\s*/, "").replace(/\s+preset=.*$/, "")
      )
    } else if (trimmed.startsWith("warning:")) {
      warnings.push(trimmed.replace(/^warning:\s*/, ""))
    }
  }
  return { archives, warnings, output: result.stdout.trim() }
}

export interface DownloadOptions {
  chapters?: string
  volumes?: string
  volumeSource?: VolumeSource
  out?: string
  preset?: string
  delay?: string
  workers?: number
  flat?: boolean
  pack?: boolean
}

/**
 * Start a real headless download. The CLI emits NDJSON progress events on
 * stdout when `--progress` is passed, so the GUI can render live per-chapter
 * and per-page state.
 */
export async function startDownload(
  url: string,
  options: DownloadOptions,
  onEvent: (event: ProgressEvent) => void
): Promise<StreamHandle> {
  const args = ["dl", url, "--no-tui", "--progress"]
  if (options.chapters) args.push("--ch", options.chapters)
  if (options.volumes) args.push("--vol", options.volumes)
  if (options.volumeSource) args.push("--volume-source", options.volumeSource)
  if (options.out) args.push("--out", options.out)
  if (options.preset) args.push("--preset", options.preset)
  if (options.delay) args.push("--delay", options.delay)
  if (options.workers) args.push("--workers", String(options.workers))
  if (options.flat) args.push("--flat")
  if (options.pack) args.push("--pack")

  return runCliStream(args, (line: CliLine) => {
    if (line.stream !== "stdout") return
    const trimmed = line.line.trim()
    if (!trimmed.startsWith("{")) return
    try {
      onEvent(JSON.parse(trimmed) as ProgressEvent)
    } catch {
      // Ignore non-event output such as "no missing chapters: ...".
    }
  })
}

/* ---------- presentation helpers ---------- */

export function titleFromSlug(slug: string): string {
  return slug
    .split(/[-_]+/)
    .filter(Boolean)
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ")
}

export function initialsFromTitle(title: string): string {
  const words = title.split(/\s+/).filter(Boolean)
  if (words.length === 0) return "?"
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase()
  return (words[0][0] + words[1][0]).toUpperCase()
}

const gradients: Array<[string, string]> = [
  ["#3B2A1F", "#8A4B2A"],
  ["#0E2A3B", "#1E6A8A"],
  ["#1E2A3B", "#4A6A9A"],
  ["#2B1E3B", "#6A4A9A"],
  ["#3B1E2A", "#9A4A6A"],
  ["#1E3B2A", "#4A8A6A"],
  ["#3B351E", "#8A7A4A"],
  ["#1E2B3B", "#4A6A8A"],
]

export function gradientFor(seed: string): [string, string] {
  let hash = 0
  for (let i = 0; i < seed.length; i += 1) {
    hash = (hash * 31 + seed.charCodeAt(i)) >>> 0
  }
  return gradients[hash % gradients.length]
}

export function humanBytes(bytes: number): string {
  if (bytes <= 0) return "0 B"
  const units = ["B", "KB", "MB", "GB", "TB"]
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  const digits = value >= 100 || unit === 0 ? 0 : 1
  return `${value.toFixed(digits)} ${units[unit]}`
}

export type { StreamHandle, CliLine }
