export type ReadingMode = "single" | "spread" | "strip"
export type ReaderBackground = "dark" | "dim" | "paper"
export type ReaderFit = "width" | "height" | "original"

export interface ReaderPrefs {
  mode: ReadingMode
  background: ReaderBackground
  fit: ReaderFit
  gap: number
  zoom: number
  rightToLeft: boolean
  preloadNext: boolean
  autoHideChrome: boolean
}

export const DEFAULT_PREFS: Readonly<ReaderPrefs> = {
  mode: "single",
  background: "dim",
  fit: "height",
  gap: 8,
  zoom: 100,
  rightToLeft: false,
  preloadNext: true,
  autoHideChrome: false,
}

function record(value: unknown): Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? value as Record<string, unknown>
    : {}
}

function bounded(value: unknown, fallback: number, min: number, max: number) {
  return typeof value === "number" && Number.isFinite(value)
    ? Math.min(max, Math.max(min, Math.round(value)))
    : fallback
}

export function normalizePrefs(value: unknown): ReaderPrefs {
  const input = record(value)
  return {
    mode: ["single", "spread", "strip"].includes(String(input.mode))
      ? input.mode as ReadingMode : DEFAULT_PREFS.mode,
    background: ["dark", "dim", "paper"].includes(String(input.background))
      ? input.background as ReaderBackground : DEFAULT_PREFS.background,
    fit: ["width", "height", "original"].includes(String(input.fit))
      ? input.fit as ReaderFit : DEFAULT_PREFS.fit,
    gap: bounded(input.gap, DEFAULT_PREFS.gap, 0, 32),
    zoom: bounded(input.zoom, DEFAULT_PREFS.zoom, 60, 200),
    rightToLeft: typeof input.rightToLeft === "boolean" ? input.rightToLeft : DEFAULT_PREFS.rightToLeft,
    preloadNext: typeof input.preloadNext === "boolean" ? input.preloadNext : DEFAULT_PREFS.preloadNext,
    autoHideChrome: typeof input.autoHideChrome === "boolean" ? input.autoHideChrome : DEFAULT_PREFS.autoHideChrome,
  }
}

export function clampPage(page: number, count: number): number {
  const maximum = Number.isFinite(count) ? Math.max(1, Math.floor(count)) : 1
  return bounded(page, 1, 1, maximum)
}

export function visiblePageNumbers(page: number, count: number, mode: ReadingMode): number[] {
  if (count <= 0) return []
  const current = clampPage(page, count)
  if (mode === "spread") {
    const start = current - (current % 2 === 0 ? 1 : 0)
    return start < count ? [start, start + 1] : [start]
  }
  return [current]
}

export function turnPage(page: number, count: number, mode: ReadingMode, direction: -1 | 1): number {
  const visible = visiblePageNumbers(page, count, mode)
  if (visible.length === 0) return 1
  const current = visible[0]
  return clampPage(current + direction * (mode === "spread" ? 2 : 1), count)
}

export function pageDimensions(
  natural: { width: number; height: number },
  viewport: { width: number; height: number },
  fit: ReaderFit,
  zoom: number
): { width: number; height: number } {
  if (natural.width <= 0 || natural.height <= 0) return { width: 0, height: 0 }
  const widthScale = Math.max(1, viewport.width) / natural.width
  const heightScale = Math.min(widthScale, Math.max(1, viewport.height) / natural.height)
  const scale = (fit === "width" ? widthScale : fit === "height" ? heightScale : 1)
    * bounded(zoom, 100, 60, 200) / 100
  return { width: Math.round(natural.width * scale), height: Math.round(natural.height * scale) }
}

export interface ReadingProgress {
  page: number
  pages: number
  at: number
  completed?: boolean
}

export function normalizeProgress(value: unknown): Record<string, ReadingProgress> {
  const output: Record<string, ReadingProgress> = Object.create(null)
  for (const [key, candidate] of Object.entries(record(value)).slice(-2000)) {
    const row = record(candidate)
    if (typeof row.pages !== "number" || !Number.isFinite(row.pages) || row.pages < 1) continue
    if (typeof row.page !== "number" || !Number.isFinite(row.page) || row.page < 1) continue
    if (typeof row.at !== "number" || !Number.isFinite(row.at)) continue
    output[key] = {
      page: clampPage(row.page, row.pages),
      pages: Math.floor(row.pages),
      at: row.at,
      completed: row.completed === true,
    }
  }
  return output
}
