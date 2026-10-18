import { invoke } from "@tauri-apps/api/core"

import { cliContext, firstErrorLine, isTauri, runCliJson, runCliStream } from "./cli"
import { resolvePath } from "./api"

export interface ReaderPage {
  index: number
  url?: string
  local?: string
  cached: boolean
  bytes?: number
  error?: string
}

export interface ReaderChapterRef {
  display: string
  number: number
  url: string
  done?: boolean
}

export interface ReaderReport {
  series: string
  series_url: string
  chapter: ReaderChapterRef
  chapters?: ReaderChapterRef[]
  pages: ReaderPage[]
  page_count: number
  prev?: ReaderChapterRef
  next?: ReaderChapterRef
  source: "library" | "cache" | "network" | "mixed"
  cache_dir?: string
  bytes: number
  chapter_index: number
  chapter_total: number
}

export interface ReaderProgress {
  kind: "page_cached" | "page_failed"
  page: number
  pages: number
  bytes?: number
  local?: string
  message?: string
}

export interface ReaderOptions {
  chapter?: string
  cache?: boolean
  refresh?: boolean
  offline?: boolean
  signal?: AbortSignal
}

function readerArgs(seriesURL: string, options: ReaderOptions, streaming: boolean): string[] {
  const args = ["read", seriesURL, streaming ? "--progress" : "--json", "--ch", options.chapter ?? "latest"]
  args.push(`--cache=${options.cache !== false}`)
  if (options.refresh) args.push("--refresh")
  if (options.offline) args.push("--offline")
  return args
}

function absoluteReport(report: ReaderReport, cwd: string): ReaderReport {
  return {
    ...report,
    chapters: report.chapters ?? [],
    pages: (report.pages ?? []).map((page) => ({
      ...page,
      local: page.local ? resolvePath(cwd, page.local) : undefined,
    })),
    cache_dir: report.cache_dir ? resolvePath(cwd, report.cache_dir) : undefined,
  }
}

export async function getReaderChapter(seriesURL: string, options: ReaderOptions = {}): Promise<ReaderReport> {
  const [report, context] = await Promise.all([
    runCliJson<ReaderReport>(readerArgs(seriesURL, options, false)),
    cliContext(),
  ])
  return absoluteReport(report, context.cwd)
}

export async function loadReaderChapter(
  seriesURL: string,
  options: ReaderOptions,
  onProgress: (event: ReaderProgress) => void,
  onReport?: (report: ReaderReport) => void,
): Promise<ReaderReport> {
  options.signal?.throwIfAborted()
  const context = await cliContext()
  options.signal?.throwIfAborted()
  let report: ReaderReport | null = null
  const handle = await runCliStream(readerArgs(seriesURL, options, true), (line) => {
    if (options.signal?.aborted || line.stream !== "stdout") return
    let event: ReaderProgress | (ReaderReport & { kind: "report" })
    try { event = JSON.parse(line.line) } catch { return }
    if (event.kind === "report") {
      report = absoluteReport(event, context.cwd)
      onReport?.(report)
    } else if (event.kind === "page_cached" || event.kind === "page_failed") {
      onProgress({ ...event, local: event.local ? resolvePath(context.cwd, event.local) : undefined })
    }
  })
  const cancel = () => { void handle.cancel().catch(() => undefined) }
  options.signal?.addEventListener("abort", cancel, { once: true })
  if (options.signal?.aborted) cancel()
  try {
    const result = await handle.done
    options.signal?.throwIfAborted()
    if (!result.ok) throw new Error(firstErrorLine(result.stderr) || "Chapter gagal dimuat.")
    if (!report) throw new Error("komiku-cli read tidak mengembalikan daftar halaman.")
    return report
  } finally {
    options.signal?.removeEventListener("abort", cancel)
  }
}

// Keep at most 24 page sources; visible images hold their own source until unmount.
const sources = new Map<string, { value: string; users: number }>()
const pending = new Map<string, Promise<string>>()
const MAX_SOURCES = 24

function pruneSources() {
  for (const [path, entry] of sources) {
    if (sources.size <= MAX_SOURCES) break
    if (entry.users > 0) continue
    if (entry.value.startsWith("blob:")) URL.revokeObjectURL(entry.value)
    sources.delete(path)
  }
}

export async function acquirePageSource(path: string): Promise<{ source: string; release: () => void }> {
  let entry = sources.get(path)
  if (!entry) {
    let request = pending.get(path)
    if (!request) {
      request = (async () => {
        if (import.meta.env.DEV && !isTauri) {
          const response = await fetch(`/__page?path=${encodeURIComponent(path)}`)
          if (!response.ok) throw new Error("File halaman tidak bisa dibuka.")
          return URL.createObjectURL(await response.blob())
        }
        const source = await invoke<string | null>("read_page", { path })
        if (!source) throw new Error("File halaman tidak bisa dibuka.")
        return source
      })()
      pending.set(path, request)
      void request.finally(() => pending.delete(path)).catch(() => undefined)
    }
    const value = await request
    entry = sources.get(path) ?? { value, users: 0 }
  }
  entry.users += 1
  sources.delete(path)
  sources.set(path, entry)
  pruneSources()
  let released = false
  return {
    source: entry.value,
    release: () => {
      if (released) return
      released = true
      entry.users = Math.max(0, entry.users - 1)
      pruneSources()
    },
  }
}

export function clearPageCache() {
  for (const [path, entry] of sources) {
    if (entry.users > 0) continue
    if (entry.value.startsWith("blob:")) URL.revokeObjectURL(entry.value)
    sources.delete(path)
  }
}
