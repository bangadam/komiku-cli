import { useEffect, useMemo, useState } from "react"
import {
  ChevronDown,
  CircleAlert,
  Copy,
  Download,
  File,
  RefreshCw,
  ShieldCheck,
} from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { PageHeader } from "@/components/shared"
import {
  BrowserOnlyNotice,
  EmptyState,
  ErrorState,
  LoadingState,
} from "@/components/states"
import {
  startDownload,
  titleFromSlug,
  verifySeries,
  type LibraryReport,
  type VerifyChapter,
  type VerifyReport,
} from "@/lib/api"
import { firstErrorLine, hasCliBridge } from "@/lib/cli"
import { useAsync, type AsyncState } from "@/lib/use-async"
import { cn } from "@/lib/utils"

type Tone = "default" | "danger" | "success"

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function isBroken(chapter: VerifyChapter): boolean {
  return (
    (chapter.broken_pages?.length ?? 0) > 0 ||
    (chapter.missing_pages?.length ?? 0) > 0
  )
}

function missingLabel(page: number): string {
  return `page ${String(page).padStart(3, "0")}`
}

export function VerifyScreen({
  library,
}: {
  library: AsyncState<LibraryReport | null>
}) {
  const [selectedDir, setSelectedDir] = useState<string | null>(null)
  const [filter, setFilter] = useState<"problems" | "all">("problems")
  const [fixing, setFixing] = useState<string | null>(null)

  const seriesList = useMemo(
    () => library.data?.series ?? [],
    [library.data]
  )

  useEffect(() => {
    if (!hasCliBridge || seriesList.length === 0) return
    if (selectedDir && seriesList.some((item) => item.dir === selectedDir)) {
      return
    }
    const target = seriesList.find((item) => item.problems > 0) ?? seriesList[0]
    setSelectedDir(target.dir)
  }, [seriesList, selectedDir])

  const verify = useAsync<VerifyReport | null>(
    () =>
      hasCliBridge && selectedDir
        ? verifySeries(selectedDir)
        : Promise.resolve(null),
    [selectedDir]
  )

  // useAsync keeps the previous result in flight; never show another series.
  const report =
    verify.data && verify.data.series_dir === selectedDir ? verify.data : null
  const chapters = report?.chapters ?? []
  const visible =
    filter === "problems" ? chapters.filter(isBroken) : chapters
  const notInState = report?.chapters_not_in_state ?? []

  const strays = useMemo(() => {
    const counts = new Map<string, number>()
    for (const chapter of report?.chapters ?? []) {
      for (const file of chapter.stray_files ?? []) {
        const path = `${chapter.dir}/${file}`
        counts.set(path, (counts.get(path) ?? 0) + 1)
      }
    }
    return [...counts.entries()].map(([path, count]) => ({ path, count }))
  }, [report])

  const summaryStats: { value: string; label: string; tone: Tone }[] = report
    ? [
        {
          value: String(chapters.length),
          label: "chapters",
          tone: "default",
        },
        {
          value: String(report.done_count),
          label: "done",
          tone: "default",
        },
        {
          value: String(report.problems),
          label: "problems",
          tone: report.problems > 0 ? "danger" : "success",
        },
        {
          value: report.healthy ? "yes" : "no",
          label: "healthy",
          tone: report.healthy ? "success" : "danger",
        },
      ]
    : []

  function handleSeriesChange(dir: string) {
    setSelectedDir(dir)
    setFilter("problems")
  }

  async function handleCopyPath() {
    if (!report) return
    try {
      await navigator.clipboard.writeText(report.series_dir)
      toast.success("Report path copied", {
        description: report.series_dir,
      })
    } catch (error) {
      toast.error(errorMessage(error))
    }
  }

  function handleDownloadReport() {
    if (!report) return
    const blob = new Blob([JSON.stringify(report, null, 2)], {
      type: "application/json",
    })
    const url = URL.createObjectURL(blob)
    const anchor = document.createElement("a")
    anchor.href = url
    anchor.download = "verify-report.json"
    document.body.appendChild(anchor)
    anchor.click()
    anchor.remove()
    URL.revokeObjectURL(url)
    toast.success("verify-report.json downloaded")
  }

  /** The chapter folder name is what `dl --ch` accepts, e.g. "chapter-107". */
  async function handleRedownload(chapter: VerifyChapter) {
    const slug = report?.series_dir.split("/").filter(Boolean).pop()
    if (!slug || fixing) return
    const number = /chapter-(\d+(?:\.\d+)?)/i.exec(chapter.dir)?.[1]
    if (!number) {
      toast.error(`Cannot read a chapter number from ${chapter.dir}`)
      return
    }

    setFixing(chapter.dir)
    const toastId = toast.loading(`Re-downloading chapter ${number}…`)
    try {
      const handle = await startDownload(
        `https://komiku.org/manga/${slug}/`,
        { chapters: number },
        () => {}
      )
      const result = await handle.done
      if (!result.ok) {
        throw new Error(firstErrorLine(result.stderr) || "download failed")
      }
      toast.success(`Chapter ${number} re-downloaded`, { id: toastId })
      verify.reload()
      library.reload()
    } catch (error) {
      toast.error(errorMessage(error), { id: toastId })
    } finally {
      setFixing(null)
    }
  }

  return (
    <div className="flex h-full flex-col gap-5 overflow-hidden px-8 py-7">
      <BrowserOnlyNotice />

      <PageHeader
        title="Verify"
        subtitle="Offline integrity check · magic bytes, page gaps, stray files"
      >
        <Button
          variant="outline"
          size="sm"
          className="h-9 px-3.5 text-[13px]"
          onClick={verify.reload}
          disabled={verify.loading || !report}
        >
          <RefreshCw
            className={cn("size-3", verify.loading && "animate-spin")}
            strokeWidth={1.8}
          />
          {verify.loading ? "Re-running…" : "Re-run"}
        </Button>
        <Button
          size="sm"
          className="h-9 px-3.5 text-[13px]"
          onClick={() => {
            void handleCopyPath()
          }}
          disabled={!report}
        >
          <Copy className="size-3" strokeWidth={1.8} />
          Copy report path
        </Button>
      </PageHeader>

      {!hasCliBridge ? (
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
          <EmptyState
            title="Open the desktop app to verify"
            hint="The browser preview cannot run komiku-cli verify."
            icon={ShieldCheck}
          />
        </div>
      ) : library.loading ? (
        <LoadingState label="Scanning library…" />
      ) : library.error ? (
        <ErrorState message={library.error} onRetry={library.reload} />
      ) : seriesList.length === 0 ? (
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
          <EmptyState
            title="Nothing to verify yet"
            hint="Download chapters first"
            icon={ShieldCheck}
          />
        </div>
      ) : verify.error ? (
        <ErrorState message={verify.error} onRetry={verify.reload} />
      ) : !report ? (
        <LoadingState label="Verifying chapters…" />
      ) : (
        <>
          <div className="flex shrink-0 items-center gap-6 rounded-[8px] border border-border-subtle bg-surface p-4">
            <div className="flex min-w-0 flex-1 flex-col gap-1">
              <div className="relative w-full max-w-[300px]">
                <select
                  aria-label="Series"
                  value={selectedDir ?? ""}
                  onChange={(event) => handleSeriesChange(event.target.value)}
                  className="h-9 w-full appearance-none rounded-[6px] border border-border bg-card px-2.5 pr-8 text-[13px] text-foreground outline-none transition-colors focus:border-primary/60"
                >
                  {seriesList.map((item) => (
                    <option key={item.dir} value={item.dir}>
                      {titleFromSlug(item.slug)}
                    </option>
                  ))}
                </select>
                <ChevronDown
                  className="pointer-events-none absolute right-2.5 top-1/2 size-3 -translate-y-1/2 text-dim"
                  strokeWidth={1.8}
                />
              </div>
              <span
                className="truncate font-mono text-[11px] text-dim"
                title={report.series_dir}
              >
                {report.series_dir}
              </span>
            </div>

            {summaryStats.map((stat) => (
              <div
                key={stat.label}
                className="flex shrink-0 flex-col gap-0.5 text-right"
              >
                <span
                  className={cn(
                    "font-mono text-[16px] font-semibold leading-none",
                    stat.tone === "danger" && "text-danger",
                    stat.tone === "success" && "text-success"
                  )}
                >
                  {stat.value}
                </span>
                <span className="text-[11px] text-dim">{stat.label}</span>
              </div>
            ))}
          </div>

          <div className="flex min-h-0 flex-1 gap-5">
            {/* Chapter report */}
            <div className="flex min-w-0 flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
              <div className="flex items-center gap-2.5 border-b border-border-subtle px-4 py-3.5">
                <span className="text-[13px] font-semibold">Chapter report</span>
                <span className="font-mono text-[11px] text-dim">
                  {visible.length} of {chapters.length}
                </span>
                <div className="flex-1" />
                <button
                  type="button"
                  onClick={() => setFilter("problems")}
                  className={cn(
                    "rounded-[5px] px-2 py-1 text-[11px] font-medium transition-colors",
                    filter === "problems"
                      ? "bg-danger-soft text-danger"
                      : "bg-muted text-dim hover:text-muted-foreground"
                  )}
                >
                  Problems
                </button>
                <button
                  type="button"
                  onClick={() => setFilter("all")}
                  className={cn(
                    "rounded-[5px] px-2 py-1 text-[11px] font-medium transition-colors",
                    filter === "all"
                      ? "bg-muted text-foreground"
                      : "bg-muted text-dim hover:text-muted-foreground"
                  )}
                >
                  All
                </button>
              </div>

              <div className="flex items-center gap-2.5 border-b border-border-subtle bg-card px-4 py-2.5 text-[11px] font-semibold text-dim">
                <span className="w-[70px] shrink-0">Status</span>
                <span className="flex-1">Chapter</span>
                <span className="w-[76px] shrink-0">Pages</span>
                <span className="w-[200px] shrink-0">Detail</span>
              </div>

              <div className="min-h-0 flex-1 overflow-y-auto">
                {visible.length === 0 ? (
                  <EmptyState
                    title={
                      filter === "problems"
                        ? "No problems found"
                        : "No chapters found"
                    }
                    hint={
                      filter === "problems"
                        ? "Every chapter passed the offline check."
                        : undefined
                    }
                    icon={ShieldCheck}
                  />
                ) : (
                  visible.map((chapter) => {
                    const broken = chapter.broken_pages?.length ?? 0
                    const missing = chapter.missing_pages?.length ?? 0
                    const brokenChapter = isBroken(chapter)
                    const detail = `${broken} broken · ${missing} missing`

                    return (
                      <div
                        key={`${chapter.dir}-${chapter.number}`}
                        className="flex flex-col gap-1.5 border-b border-border-subtle px-4 py-2.5 last:border-b-0"
                      >
                        <div className="flex items-center gap-2.5">
                          <span
                            className={cn(
                              "inline-flex w-[70px] shrink-0 items-center justify-center rounded-full px-1.5 py-0.5 text-[11px] font-medium",
                              brokenChapter
                                ? "bg-danger-soft text-danger"
                                : "bg-success-soft text-success"
                            )}
                          >
                            {brokenChapter ? "BROKEN" : "OK"}
                          </span>
                          <span className="min-w-0 flex-1 truncate font-mono text-[12px]">
                            {chapter.dir}
                          </span>
                          <span
                            className={cn(
                              "w-[76px] shrink-0 font-mono text-[11px]",
                              brokenChapter ? "text-danger" : "text-dim"
                            )}
                          >
                            {chapter.valid_pages}/{chapter.pages}
                          </span>
                          <span
                            className={cn(
                              "w-[200px] shrink-0 text-[11px]",
                              brokenChapter ? "text-warning" : "text-dim"
                            )}
                          >
                            {detail}
                          </span>
                        </div>

                        {brokenChapter && (
                          <div className="flex flex-col gap-0.5 rounded-[6px] bg-danger-soft p-2">
                            {(chapter.broken_pages ?? []).map((page) => (
                              <span
                                key={`broken-${page}`}
                                className="break-all font-mono text-[10px] text-danger"
                              >
                                broken: {page}
                              </span>
                            ))}
                            {(chapter.missing_pages ?? []).map((page) => (
                              <span
                                key={`missing-${page}`}
                                className="font-mono text-[10px] text-danger"
                              >
                                missing: {missingLabel(page)}
                              </span>
                            ))}
                            {(chapter.stray_files ?? []).map((file) => (
                              <span
                                key={`stray-${file}`}
                                className="break-all font-mono text-[10px] text-warning"
                              >
                                stray: {file}
                              </span>
                            ))}
                            <button
                              type="button"
                              onClick={() => {
                                void handleRedownload(chapter)
                              }}
                              disabled={fixing !== null}
                              className="mt-1 inline-flex items-center gap-1 self-start text-[11px] font-medium text-danger transition-opacity hover:opacity-80 disabled:opacity-50"
                            >
                              <Download className="size-3" strokeWidth={1.8} />
                              {fixing === chapter.dir
                                ? "Re-downloading…"
                                : `Re-download ${chapter.dir}`}
                            </button>
                          </div>
                        )}
                      </div>
                    )
                  })
                )}
              </div>
            </div>

            {/* Right sidebar */}
            <aside className="flex w-[330px] shrink-0 flex-col gap-3.5 overflow-y-auto">
              <div className="flex flex-col gap-3 rounded-[8px] border border-border-subtle bg-surface p-4">
                <div className="flex items-center gap-2.5">
                  <CircleAlert
                    className={cn(
                      "size-[13px] shrink-0",
                      notInState.length > 0 ? "text-warning" : "text-dim"
                    )}
                    strokeWidth={1.8}
                  />
                  <span className="text-[12px] font-semibold">
                    Not in .state.json
                  </span>
                  <div className="flex-1" />
                  <span
                    className={cn(
                      "font-mono text-[11px]",
                      notInState.length > 0 ? "text-warning" : "text-dim"
                    )}
                  >
                    {notInState.length}
                  </span>
                </div>
                {notInState.length === 0 ? (
                  <span className="text-[11px] text-dim">None</span>
                ) : (
                  notInState.map((entry) => (
                    <span
                      key={entry}
                      className="break-all font-mono text-[11px] text-dim"
                    >
                      {entry}
                    </span>
                  ))
                )}
              </div>

              <div className="flex flex-col gap-3 rounded-[8px] border border-border-subtle bg-surface p-4">
                <div className="flex items-center gap-2">
                  <span className="text-[12px] font-semibold">Stray files</span>
                  <div className="flex-1" />
                  <span className="font-mono text-[11px] text-dim">
                    {strays.length}
                  </span>
                </div>
                {strays.length === 0 ? (
                  <span className="text-[11px] text-dim">None</span>
                ) : (
                  <div className="flex flex-col gap-2">
                    {strays.map((stray) => (
                      <div
                        key={stray.path}
                        className="flex items-center gap-2"
                      >
                        <File
                          className="size-[11px] shrink-0 text-dim"
                          strokeWidth={1.8}
                        />
                        <span
                          className="min-w-0 flex-1 truncate font-mono text-[10px] text-muted-foreground"
                          title={stray.path}
                        >
                          {stray.path}
                        </span>
                        <span className="shrink-0 font-mono text-[10px] text-dim">
                          {stray.count}
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </div>

              <div className="flex flex-col gap-3 rounded-[8px] border border-border-subtle bg-surface p-4">
                <span className="text-[12px] font-semibold">Last run</span>

                <div className="flex flex-col gap-2">
                  <div className="flex flex-col gap-0.5">
                    <span className="text-[11px] text-dim">Series dir</span>
                    <span
                      className="truncate font-mono text-[11px] text-muted-foreground"
                      title={report.series_dir}
                    >
                      {report.series_dir}
                    </span>
                  </div>
                  <div className="flex items-center">
                    <span className="text-[11px] text-dim">Chapters</span>
                    <div className="flex-1" />
                    <span className="font-mono text-[11px] text-muted-foreground">
                      {chapters.length}
                    </span>
                  </div>
                  <div className="flex items-center">
                    <span className="text-[11px] text-dim">Problems</span>
                    <div className="flex-1" />
                    <span
                      className={cn(
                        "font-mono text-[11px]",
                        report.problems > 0
                          ? "text-danger"
                          : "text-success"
                      )}
                    >
                      {report.problems}
                    </span>
                  </div>
                </div>

                <button
                  type="button"
                  onClick={handleDownloadReport}
                  className="self-start text-[11px] text-primary transition-opacity hover:opacity-80"
                >
                  Download JSON report
                </button>
              </div>
            </aside>
          </div>
        </>
      )}
    </div>
  )
}
