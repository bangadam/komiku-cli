import type { ProgressEvent } from "@/lib/api"
import type { DownloadJobState } from "@/lib/download-store"

export interface JobMetrics {
  pagesDone: number
  pagesTotal: number
  pagesFailed: number
  chaptersDone: number
  chaptersFailed: number
  currentChapter: string | null
  percent: number
  /** Bytes received for this job, summed from page_done events. */
  bytesDone: number
  /** Average throughput since the job started, in bytes per second. */
  bytesPerSecond: number
}

export function clampPercent(value: number): number {
  if (!Number.isFinite(value)) return 0
  return Math.max(0, Math.min(100, Math.round(value)))
}

/** Derive live progress from the NDJSON events emitted by `komiku-cli dl --progress`. */
export function metricsFor(job: DownloadJobState): JobMetrics {
  const pagesByChapter = new Map<string, number>()
  let pagesDone = 0
  let pagesFailed = 0
  let bytesDone = 0
  let chaptersDone = 0
  let chaptersFailed = 0
  let currentChapter: string | null = null

  for (const event of job.events) {
    if (event.kind === "chapter_started") {
      currentChapter = event.chapter ?? currentChapter
    } else if (event.kind === "pages_known") {
      if (event.chapter && typeof event.pages === "number") {
        pagesByChapter.set(event.chapter, event.pages)
      }
    } else if (event.kind === "page_done") {
      pagesDone += 1
      bytesDone += event.bytes ?? 0
    } else if (event.kind === "page_failed") {
      pagesFailed += 1
    } else if (event.kind === "chapter_finished") {
      if (event.status === "DONE") chaptersDone += 1
      else chaptersFailed += 1
    }
  }

  let pagesTotal = 0
  for (const pages of pagesByChapter.values()) pagesTotal += pages

  const chapterTotal = job.chapters.length
  const percent =
    pagesTotal > 0
      ? (pagesDone / pagesTotal) * 100
      : chapterTotal > 0
        ? (chaptersDone / chapterTotal) * 100
        : 0

  const elapsedSeconds = Math.max(1, (Date.now() - job.startedAt) / 1000)
  const bytesPerSecond =
    job.status === "running" ? Math.round(bytesDone / elapsedSeconds) : 0

  return {
    pagesDone,
    pagesTotal,
    pagesFailed,
    chaptersDone,
    chaptersFailed,
    currentChapter,
    percent: clampPercent(percent),
    bytesDone,
    bytesPerSecond,
  }
}

/** Compact transfer rate for the downloads table. */
export function formatRate(bytesPerSecond: number): string {
  if (!bytesPerSecond || bytesPerSecond <= 0) return "—"
  const units = ["B/s", "KB/s", "MB/s", "GB/s"]
  let value = bytesPerSecond
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value >= 10 || unit === 0 ? Math.round(value) : value.toFixed(1)} ${units[unit]}`
}

export function eventLevel(event: ProgressEvent): "ok" | "warn" | "info" | "error" {
  if (event.kind === "page_failed") return "warn"
  if (event.kind === "chapter_finished" && event.status !== "DONE") return "error"
  if (event.kind === "batch_finished") return "ok"
  return "info"
}

export function eventDetail(event: ProgressEvent): string {
  switch (event.kind) {
    case "batch_started":
      return `batch started · ${event.requested ?? 0} requested`
    case "chapter_started":
      return `chapter ${event.chapter ?? "?"} started`
    case "pages_known":
      return `chapter ${event.chapter ?? "?"} · ${event.pages ?? 0} pages`
    case "page_done":
      return `${event.chapter ?? "?"} · page ${event.page ?? 0}/${event.pages ?? 0}`
    case "page_failed":
      return `${event.chapter ?? "?"} · page ${event.page ?? 0} failed${
        event.message ? `: ${event.message}` : ""
      }`
    case "chapter_finished":
      return `${event.chapter ?? "?"} ${event.status ?? "?"} (${
        event.success ?? 0
      }/${event.total ?? 0})`
    case "batch_finished":
      return `batch finished · ${event.started ?? 0}/${event.requested ?? 0} started`
    default:
      return event.kind
  }
}
