import { useSyncExternalStore } from "react"

import {
  clampPage,
  DEFAULT_PREFS,
  normalizePrefs,
  normalizeProgress,
  type ReaderPrefs,
  type ReadingProgress,
} from "./reader-state"

export { DEFAULT_PREFS } from "./reader-state"
export type { ReadingMode, ReaderBackground, ReaderFit, ReaderPrefs, ReadingProgress } from "./reader-state"

const STORAGE_KEY = "komiku.reader.prefs"
const PROGRESS_KEY = "komiku.reader.progress"
const listeners = new Set<() => void>()
const progressListeners = new Set<() => void>()

function readStorage(key: string): unknown {
  try {
    return JSON.parse(window.localStorage.getItem(key) ?? "null")
  } catch {
    return null
  }
}

function writeStorage(key: string, value: unknown) {
  try {
    window.localStorage.setItem(key, JSON.stringify(value))
  } catch {
    // Reading remains usable when storage is blocked or full.
  }
}

let prefs = normalizePrefs(readStorage(STORAGE_KEY))
let progress = normalizeProgress(readStorage(PROGRESS_KEY))

export function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => { listeners.delete(listener) }
}

export function getPrefs(): ReaderPrefs { return prefs }

export function setPrefs(patch: Partial<ReaderPrefs>) {
  prefs = normalizePrefs({ ...prefs, ...patch })
  writeStorage(STORAGE_KEY, prefs)
  for (const listener of listeners) listener()
}

export function resetPrefs() { setPrefs(DEFAULT_PREFS) }

export function useReaderPrefs(): ReaderPrefs {
  return useSyncExternalStore(subscribe, getPrefs)
}

export function progressKey(series: string, chapter: string): string {
  return `${series}/${chapter}`
}

export function getProgress(series: string, chapter: string): ReadingProgress | undefined {
  return progress[progressKey(series, chapter)]
}

export function getProgressSnapshot() { return progress }

export function getSeriesProgress(series: string): Record<string, ReadingProgress> {
  const prefix = `${series}/`
  return Object.fromEntries(Object.entries(progress)
    .filter(([key]) => key.startsWith(prefix))
    .map(([key, value]) => [key.slice(prefix.length), value]))
}

export function setProgress(series: string, chapter: string, page: number, pages: number, completed = false) {
  if (pages < 1 || !Number.isFinite(pages)) return
  const key = progressKey(series, chapter)
  const next = { page: clampPage(page, pages), pages, at: Date.now(), completed: completed || progress[key]?.completed === true }
  if (progress[key]?.page === next.page && progress[key]?.pages === pages && progress[key]?.completed === next.completed) return
  progress = { ...progress, [key]: next }
  const entries = Object.entries(progress).sort((a, b) => b[1].at - a[1].at).slice(0, 2000)
  progress = Object.fromEntries(entries)
  writeStorage(PROGRESS_KEY, progress)
  for (const listener of progressListeners) listener()
}

export function subscribeProgress(listener: () => void): () => void {
  progressListeners.add(listener)
  return () => { progressListeners.delete(listener) }
}
