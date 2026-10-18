import type { ProgressEvent } from "@/lib/api"
import type { StreamHandle } from "@/lib/cli"

export interface DownloadJobState {
  id: string
  slug: string
  url: string
  chapters: string[]
  startedAt: number
  status: "running" | "done" | "failed" | "cancelled"
  events: ProgressEvent[]
  message?: string
}

export interface DownloadLogEntry {
  id: number
  jobId: string
  slug: string
  at: string
  event: ProgressEvent
}

const MAX_LOG_ENTRIES = 500

let jobs: DownloadJobState[] = []
let log: DownloadLogEntry[] = []
let jobSeq = 0
let logSeq = 0

const handles = new Map<string, StreamHandle>()
const listeners = new Set<() => void>()

function notify() {
  for (const listener of listeners) listener()
}

export function subscribe(listener: () => void): () => void {
  listeners.add(listener)
  return () => {
    listeners.delete(listener)
  }
}

/** Stable snapshot for useSyncExternalStore — replaced only on mutation. */
export function getJobs(): DownloadJobState[] {
  return jobs
}

/** Stable snapshot of the activity log, newest entries last. */
export function getLog(): DownloadLogEntry[] {
  return log
}

export function startJob(input: {
  slug: string
  url: string
  chapters: string[]
}): string {
  jobSeq += 1
  const id = `job-${Date.now().toString(36)}-${jobSeq}`
  const job: DownloadJobState = {
    id,
    slug: input.slug,
    url: input.url,
    chapters: [...input.chapters],
    startedAt: Date.now(),
    status: "running",
    events: [],
  }
  jobs = [job, ...jobs]
  notify()
  return id
}

export function setHandle(id: string, handle: StreamHandle): void {
  const job = jobs.find((item) => item.id === id)
  if (!job) return
  if (job.status === "cancelled") {
    void handle.cancel().catch(() => undefined)
    return
  }
  handles.set(id, handle)
}

export function appendEvent(id: string, event: ProgressEvent): void {
  const target = jobs.find((job) => job.id === id)
  if (!target) return

  jobs = jobs.map((job) => {
    if (job.id !== id) return job
    const status =
      job.status === "running" && event.kind === "batch_finished"
        ? "done"
        : job.status
    return { ...job, status, events: [...job.events, event] }
  })

  logSeq += 1
  log = [
    ...log,
    {
      id: logSeq,
      jobId: id,
      slug: target.slug,
      at: new Date().toLocaleTimeString(),
      event,
    },
  ].slice(-MAX_LOG_ENTRIES)

  notify()
}

export function finishJob(
  id: string,
  status: "done" | "failed" | "cancelled",
  message?: string
): void {
  let changed = false
  jobs = jobs.map((job) => {
    // A cancelled job keeps its cancelled state even if the stream later fails.
    if (job.id !== id || job.status !== "running") return job
    changed = true
    return { ...job, status, message }
  })
  handles.delete(id)
  if (changed) notify()
}

export async function cancelJob(id: string): Promise<void> {
  const job = jobs.find((item) => item.id === id)
  if (!job || job.status !== "running") return

  jobs = jobs.map((item) =>
    item.id === id
      ? { ...item, status: "cancelled", message: "Cancelled" }
      : item
  )
  notify()

  const handle = handles.get(id)
  handles.delete(id)
  if (!handle) return
  try {
    await handle.cancel()
  } catch {
    // The CLI process may already have exited; the UI state is authoritative.
  }
}

export function clearLog(): void {
  if (log.length === 0) return
  log = []
  notify()
}
