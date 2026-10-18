import { useMemo, useSyncExternalStore } from "react"
import { Check, Download, Loader2, Terminal, X } from "lucide-react"
import { toast } from "sonner"

import { EmptyState } from "@/components/states"
import { Button } from "@/components/ui/button"
import { Progress } from "@/components/ui/progress"
import { titleFromSlug } from "@/lib/api"
import {
  cancelJob,
  clearLog,
  getJobs,
  getLog,
  subscribe,
  type DownloadJobState,
} from "@/lib/download-store"
import {
  clampPercent,
  eventDetail,
  eventLevel,
  formatRate,
  metricsFor,
} from "@/lib/job-metrics"
import { cn } from "@/lib/utils"

const statusStyles: Record<DownloadJobState["status"], string> = {
  running: "bg-info-soft text-info",
  done: "bg-success-soft text-success",
  failed: "bg-danger-soft text-danger",
  cancelled: "bg-muted text-muted-foreground",
}

const statusLabels: Record<DownloadJobState["status"], string> = {
  running: "Downloading",
  done: "Done",
  failed: "Failed",
  cancelled: "Cancelled",
}

const progressStyles: Record<DownloadJobState["status"], string> = {
  running: "bg-primary",
  done: "bg-success",
  failed: "bg-danger",
  cancelled: "bg-muted-foreground/40",
}

const levelStyles: Record<"ok" | "warn" | "info" | "error", string> = {
  ok: "text-success",
  warn: "text-warning",
  info: "text-info",
  error: "text-danger",
}

function JobIcon({ status }: { status: DownloadJobState["status"] }) {
  if (status === "done") {
    return <Check className="size-[13px] text-success" strokeWidth={1.8} />
  }
  if (status === "failed") {
    return <X className="size-[13px] text-danger" strokeWidth={1.8} />
  }
  if (status === "cancelled") {
    return <X className="size-[13px] text-dim" strokeWidth={1.8} />
  }
  return (
    <Loader2 className="size-[13px] animate-spin text-info" strokeWidth={1.8} />
  )
}

export function DownloadsScreen() {
  const jobs = useSyncExternalStore(subscribe, getJobs)
  const log = useSyncExternalStore(subscribe, getLog)

  const rows = useMemo(
    () => jobs.map((job) => ({ job, metrics: metricsFor(job) })),
    [jobs]
  )

  const runningCount = jobs.filter((job) => job.status === "running").length
  const doneCount = jobs.filter((job) => job.status === "done").length
  const failedCount = jobs.filter((job) => job.status === "failed").length

  const overall = useMemo(() => {
    let chaptersDone = 0
    let chaptersTotal = 0
    let pagesDone = 0
    let pagesTotal = 0
    let errors = 0
    let bytesPerSecond = 0

    for (const { job, metrics } of rows) {
      if (job.status !== "running") continue
      chaptersDone += metrics.chaptersDone
      chaptersTotal += job.chapters.length
      pagesDone += metrics.pagesDone
      pagesTotal += metrics.pagesTotal
      errors += metrics.pagesFailed + metrics.chaptersFailed
      bytesPerSecond += metrics.bytesPerSecond
    }

    const percent =
      pagesTotal > 0
        ? (pagesDone / pagesTotal) * 100
        : chaptersTotal > 0
          ? (chaptersDone / chaptersTotal) * 100
          : 0

    return {
      chaptersDone,
      chaptersTotal,
      pagesDone,
      pagesTotal,
      errors,
      bytesPerSecond,
      percent: clampPercent(percent),
    }
  }, [rows])

  const visibleLog = log.slice(-50)
  const runningJobs = jobs.filter((job) => job.status === "running")

  async function cancelAll() {
    if (runningJobs.length === 0) return
    await Promise.all(runningJobs.map((job) => cancelJob(job.id)))
    toast.success("Downloads cancelled")
  }

  async function cancelOne(job: DownloadJobState) {
    await cancelJob(job.id)
    toast.info(`${titleFromSlug(job.slug)} cancelled`)
  }

  function handleClearLog() {
    clearLog()
    toast.info("Activity log cleared")
  }

  const stats = [
    {
      label: "Chapters",
      value: `${overall.chaptersDone} / ${overall.chaptersTotal}`,
      danger: false,
    },
    {
      label: "Pages",
      value: `${overall.pagesDone} / ${overall.pagesTotal}`,
      danger: false,
    },
    {
      label: "Speed",
      value: formatRate(overall.bytesPerSecond),
      danger: false,
    },
    { label: "Errors", value: String(overall.errors), danger: overall.errors > 0 },
  ]

  return (
    <div className="flex h-full flex-col gap-5 overflow-hidden px-8 py-7">
      {/* Header */}
      <div data-tauri-drag-region="deep" className="flex items-start gap-4">
        <div className="flex min-w-0 flex-col gap-1.5">
          <h1 className="text-[30px] font-semibold leading-[1.1] tracking-[-0.02em]">
            Downloads
          </h1>
          <div className="flex items-center gap-2">
            <span
              className={cn(
                "size-1.5 rounded-full",
                runningCount > 0 ? "animate-pulse bg-success" : "bg-dim"
              )}
            />
            <span className="text-[12px] text-muted-foreground">
              {runningCount} running · {doneCount} finished · {failedCount} failed
            </span>
          </div>
        </div>
        <div className="flex-1" />
        <div className="flex items-center gap-2 pt-1">
          <Button
            variant="destructive"
            size="sm"
            className="h-9 px-3.5 text-[13px]"
            onClick={() => {
              void cancelAll()
            }}
            disabled={runningCount === 0}
          >
            Cancel all
          </Button>
        </div>
      </div>

      {jobs.length === 0 ? (
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
          <EmptyState
            title="No downloads yet"
            hint="Start one from a series page"
            icon={Download}
          />
        </div>
      ) : (
        <>
          {/* Overall batch */}
          <div className="flex shrink-0 flex-col gap-2.5 rounded-[8px] border border-border-subtle bg-surface p-4">
            <div className="flex items-end gap-4">
              <div className="flex min-w-0 flex-1 flex-col gap-1">
                <span className="text-[14px] font-semibold">Current batch</span>
                <span className="truncate text-[12px] text-muted-foreground">
                  {runningJobs.length > 0
                    ? runningJobs
                        .map((job) => titleFromSlug(job.slug))
                        .join(" · ")
                    : "No active downloads"}
                </span>
              </div>
              <span className="shrink-0 font-mono text-[18px] font-semibold leading-none">
                {overall.percent}%
              </span>
            </div>

            <Progress value={overall.percent} className="h-2" />

            <div className="flex items-center gap-6">
              {stats.map((stat) => (
                <div key={stat.label} className="flex flex-col gap-0.5">
                  <span className="text-[11px] text-muted-foreground">
                    {stat.label}
                  </span>
                  <span
                    className={cn(
                      "font-mono text-[13px]",
                      stat.danger && "text-danger"
                    )}
                  >
                    {stat.value}
                  </span>
                </div>
              ))}
            </div>
          </div>

          {/* Body */}
          <div className="flex min-h-0 flex-1 gap-5">
            {/* Jobs */}
            <div className="flex min-w-0 flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
              <div className="flex items-center gap-2 border-b border-border-subtle px-4 py-3.5">
                <span className="text-[13px] font-semibold">Active</span>
                <span className="rounded-full bg-muted px-1.5 py-0.5 font-mono text-[11px] text-muted-foreground">
                  {runningCount}
                </span>
                <div className="flex-1" />
                <span className="font-mono text-[11px] text-dim">
                  {runningCount} running
                </span>
              </div>

              <div className="min-h-0 flex-1 overflow-y-auto">
                {rows.map(({ job, metrics }) => {
                  const errors = metrics.pagesFailed + metrics.chaptersFailed
                  return (
                    <div
                      key={job.id}
                      className="flex flex-col gap-2.5 border-b border-border-subtle px-4 py-3.5 last:border-b-0"
                    >
                      <div className="flex items-center gap-2.5">
                        <div className="flex size-7 shrink-0 items-center justify-center rounded-[6px] bg-muted">
                          <JobIcon status={job.status} />
                        </div>

                        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
                          <div className="flex min-w-0 items-baseline gap-1.5">
                            <span className="truncate text-[13px] font-medium">
                              {titleFromSlug(job.slug)}
                            </span>
                            <span className="shrink-0 font-mono text-[11px] text-dim">
                              {metrics.currentChapter
                                ? `ch. ${metrics.currentChapter}`
                                : `${job.chapters.length} ch`}
                            </span>
                          </div>
                          <span className="truncate font-mono text-[11px] text-muted-foreground">
                            {metrics.chaptersDone}/{job.chapters.length} chapters ·{" "}
                            {metrics.pagesDone}/{metrics.pagesTotal || "—"} pages
                            {errors > 0 ? ` · ${errors} errors` : ""}
                          </span>
                        </div>

                        <span
                          className={cn(
                            "shrink-0 rounded-full px-2 py-0.5 text-[11px] font-medium",
                            statusStyles[job.status]
                          )}
                        >
                          {statusLabels[job.status]}
                        </span>

                        {job.status === "running" && (
                          <button
                            type="button"
                            aria-label={`Cancel ${titleFromSlug(job.slug)}`}
                            onClick={() => {
                              void cancelOne(job)
                            }}
                            className="shrink-0 text-dim transition-colors hover:text-danger"
                          >
                            <X className="size-[13px]" strokeWidth={1.8} />
                          </button>
                        )}
                      </div>

                      <Progress
                        value={metrics.percent}
                        className="h-[5px]"
                        indicatorClassName={progressStyles[job.status]}
                      />

                      <div className="flex items-center font-mono text-[11px] text-dim">
                        <span>
                          {metrics.pagesDone} / {metrics.pagesTotal || "—"} pages
                        </span>
                        <div className="flex-1" />
                        <span>{formatRate(metrics.bytesPerSecond)}</span>
                        <span className="ml-3">{metrics.percent}%</span>
                      </div>

                      {job.message && (
                        <span
                          className={cn(
                            "truncate font-mono text-[10px]",
                            job.status === "failed" ? "text-danger" : "text-dim"
                          )}
                        >
                          {job.message}
                        </span>
                      )}
                    </div>
                  )
                })}
              </div>
            </div>

            {/* Activity log */}
            <div className="flex w-[340px] shrink-0 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
              <div className="flex items-center gap-2 border-b border-border-subtle px-4 py-3.5">
                <Terminal
                  className="size-[13px] text-muted-foreground"
                  strokeWidth={1.8}
                />
                <span className="text-[13px] font-semibold">Activity log</span>
                <div className="flex-1" />
                <span
                  className={cn(
                    "font-mono text-[10px]",
                    runningCount > 0 ? "text-success" : "text-dim"
                  )}
                >
                  {runningCount > 0 ? "live" : "idle"}
                </span>
                <button
                  type="button"
                  onClick={handleClearLog}
                  disabled={log.length === 0}
                  className="text-[10px] text-dim transition-colors hover:text-foreground disabled:pointer-events-none disabled:opacity-50"
                >
                  Clear
                </button>
              </div>

              <div className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto px-4 py-3">
                {visibleLog.length === 0 && (
                  <span className="font-mono text-[10px] text-dim">
                    No activity yet
                  </span>
                )}

                {visibleLog.map((entry) => {
                  const level = eventLevel(entry.event)
                  return (
                    <div
                      key={entry.id}
                      className="flex items-start gap-2 font-mono text-[10px]"
                    >
                      <span className="shrink-0 text-dim">{entry.at}</span>
                      <span
                        className={cn("w-[26px] shrink-0", levelStyles[level])}
                      >
                        {level}
                      </span>
                      <span className="min-w-0 flex-1 leading-relaxed text-muted-foreground">
                        {titleFromSlug(entry.slug)} ·{" "}
                        {eventDetail(entry.event)}
                      </span>
                    </div>
                  )
                })}
              </div>
            </div>
          </div>
        </>
      )}
    </div>
  )
}
