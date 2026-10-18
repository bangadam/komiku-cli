import { useMemo, useState } from "react"
import {
  AlertTriangle,
  BookOpen,
  Check,
  ChevronLeft,
  ChevronRight,
  Download,
  FolderOpen,
  Loader2,
  Package,
} from "lucide-react"
import { toast } from "sonner"

import { MetaDot, SeriesCover } from "@/components/shared"
import {
  BrowserOnlyNotice,
  EmptyState,
  ErrorState,
  LoadingState,
} from "@/components/states"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Progress } from "@/components/ui/progress"
import { Switch } from "@/components/ui/switch"
import {
  getConfig,
  getSeriesInfo,
  gradientFor,
  initialsFromTitle,
  startDownload,
  titleFromSlug,
  type ChapterInfo,
  type SeriesInfo,
  type VolumeInfo,
} from "@/lib/api"
import { appendEvent, finishJob, setHandle, startJob } from "@/lib/download-store"
import { openFolder as openFolderAction } from "@/lib/cli"
import { useAsync } from "@/lib/use-async"
import { cn } from "@/lib/utils"

type Tab = "chapters" | "volumes" | "files"
type Preset = "raw" | "medium" | "small" | "tiny"
type QuickFilter = "all" | "missing" | "latest" | "range"

const tabOptions: { id: Tab; label: string }[] = [
  { id: "chapters", label: "Chapters" },
  { id: "volumes", label: "Volumes" },
  { id: "files", label: "Files on disk" },
]

const presetOptions: { id: Preset; name: string; desc: string }[] = [
  { id: "raw", name: "Raw", desc: "lossless" },
  { id: "medium", name: "Medium", desc: "1600 · q72" },
  { id: "small", name: "Small", desc: "1400 · q65" },
  { id: "tiny", name: "Tiny", desc: "1200 · q60" },
]

// Stable identities so derived memo values do not recompute on every render
// when the CLI returned a null/empty slice.
const EMPTY_CHAPTERS: ChapterInfo[] = []
const EMPTY_VOLUMES: VolumeInfo[] = []

// Go time.ParseDuration, which the CLI uses for --delay. Accepts composite
// values such as "1m30s" as well as the bare "0".
const durationUnit = "(?:ns|us|µs|ms|s|m|h)"
const durationPart = `\\d+(?:\\.\\d+)?${durationUnit}`
const delayPattern = new RegExp(`^(?:0|${durationPart}(?:${durationPart})*)$`)

function isPreset(value: string | undefined): value is Preset {
  return (
    value === "raw" ||
    value === "medium" ||
    value === "small" ||
    value === "tiny"
  )
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function chapterPath(url: string): string {
  try {
    return new URL(url).pathname
  } catch {
    return url
  }
}

function chapterInVolume(chapter: ChapterInfo, volume: VolumeInfo): boolean {
  return chapter.number >= volume.start && chapter.number <= volume.end
}

function ToggleRow({
  label,
  description,
  checked,
  disabled,
  onCheckedChange,
}: {
  label: string
  description: string
  checked: boolean
  disabled?: boolean
  onCheckedChange: (checked: boolean) => void
}) {
  return (
    <div className="flex items-center justify-between gap-3">
      <div className="flex min-w-0 flex-col gap-0.5">
        <span className="text-[11px] font-medium">{label}</span>
        <span className="text-[9px] text-dim">{description}</span>
      </div>
      <Switch
        checked={checked}
        disabled={disabled}
        onCheckedChange={(value) => onCheckedChange(value === true)}
      />
    </div>
  )
}

export function SeriesScreen({
  slug,
  url,
  onBack,
  onOpenPack,
  onRead,
}: {
  slug: string
  url?: string
  onBack: () => void
  onOpenPack: () => void
  onRead: (chapter?: string) => void
}) {
  const seriesUrl = url ?? `https://komiku.org/manga/${slug}/`

  // Remount on URL change so selections and the lazy volume request never leak
  // from one series into another.
  return (
    <SeriesView
      key={seriesUrl}
      slug={slug}
      seriesUrl={seriesUrl}
      onBack={onBack}
      onOpenPack={onOpenPack}
      onRead={onRead}
    />
  )
}

function SeriesView({
  slug,
  seriesUrl,
  onBack,
  onOpenPack,
  onRead,
}: {
  slug: string
  seriesUrl: string
  onBack: () => void
  onOpenPack: () => void
  onRead: (chapter?: string) => void
}) {
  const infoState = useAsync(() => getSeriesInfo(seriesUrl), [seriesUrl])
  const configState = useAsync(() => getConfig(), [])

  const [activeTab, setActiveTab] = useState<Tab>("chapters")
  const [selectedChapters, setSelectedChapters] = useState<Set<string>>(
    () => new Set()
  )
  const [selectedVolumes, setSelectedVolumes] = useState<Set<number>>(
    () => new Set()
  )
  const [volumesRequested, setVolumesRequested] = useState(false)
  const [quickFilter, setQuickFilter] = useState<QuickFilter | null>(null)
  const [latestCount, setLatestCount] = useState(12)
  const [presetOverride, setPresetOverride] = useState<Preset | null>(null)
  const [workers, setWorkers] = useState("3")
  const [delayOverride, setDelayOverride] = useState<string | null>(null)
  const [packAfter, setPackAfter] = useState(false)
  const [flatFolders, setFlatFolders] = useState(false)
  const [starting, setStarting] = useState(false)

  const config = configState.data

  // Config is loaded asynchronously; fall back to CLI defaults until it arrives
  // and let an explicit user choice win over the config value.
  const preset: Preset =
    presetOverride ?? (isPreset(config?.preset) ? config.preset : "medium")
  const delay = delayOverride ?? config?.image_delay ?? "200ms"

  // A different series must not inherit the previous selection: SeriesScreen
  // remounts this view with a keyed seriesUrl.

  // Volume mapping hits the network (Wikipedia on cache miss), so only ask
  // for it once the Volumes tab is opened.
  const volumesState = useAsync<SeriesInfo | null>(
    () =>
      volumesRequested
        ? getSeriesInfo(seriesUrl, { volumes: true, volumeSource: "mangadex" })
        : Promise.resolve(null),
    [seriesUrl, volumesRequested]
  )

  // Guard against the previous series' report lingering for a frame while the
  // new URL loads (useAsync keeps old data during the in-flight request).
  const info =
    infoState.data &&
    (!infoState.data.series_url || infoState.data.series_url === seriesUrl)
      ? infoState.data
      : null
  const volumeInfo =
    volumesState.data &&
    (!volumesState.data.series_url ||
      volumesState.data.series_url === seriesUrl)
      ? volumesState.data
      : null
  const chapters = info?.chapters ?? EMPTY_CHAPTERS
  const volumeRows = volumeInfo?.volumes ?? EMPTY_VOLUMES
  const doneCount = info?.done_count ?? 0
  const totalChapters = chapters.length
  const percent =
    totalChapters > 0 ? Math.round((doneCount / totalChapters) * 100) : 0
  const title = info ? titleFromSlug(info.series) : titleFromSlug(slug)
  const missingCount = chapters.filter((chapter) => !chapter.done).length

  const selectedChapterRows = useMemo(
    () => chapters.filter((chapter) => selectedChapters.has(chapter.url)),
    [chapters, selectedChapters]
  )
  const selectedChapterCount = selectedChapterRows.length
  const selectedMissingCount = selectedChapterRows.filter(
    (chapter) => !chapter.done
  ).length

  const selectedVolumeNumbers = useMemo(
    () =>
      volumeRows
        .filter((volume) => selectedVolumes.has(volume.volume))
        .map((volume) => volume.volume),
    [volumeRows, selectedVolumes]
  )
  const selectedVolumeCount = selectedVolumeNumbers.length

  const selectedVolumeChapters = useMemo(
    () =>
      chapters.filter((chapter) =>
        volumeRows.some(
          (volume) =>
            selectedVolumes.has(volume.volume) && chapterInVolume(chapter, volume)
        )
      ),
    [chapters, volumeRows, selectedVolumes]
  )

  const volumeChapterCounts = useMemo(() => {
    const counts = new Map<number, number>()
    for (const volume of volumeRows) {
      counts.set(
        volume.volume,
        chapters.filter((chapter) => chapterInVolume(chapter, volume)).length
      )
    }
    return counts
  }, [volumeRows, chapters])

  const downloadMode: "chapters" | "volumes" =
    selectedChapterCount > 0 || selectedVolumeCount === 0 ? "chapters" : "volumes"
  const packAllowed = downloadMode === "volumes" && selectedVolumeCount > 0

  const allChaptersSelected =
    chapters.length > 0 && selectedChapterCount === chapters.length
  const allVolumesSelected =
    volumeRows.length > 0 && selectedVolumeCount === volumeRows.length

  const activeSelectedCount =
    activeTab === "volumes"
      ? selectedVolumeCount
      : activeTab === "chapters"
        ? selectedChapterCount
        : 0

  function openTab(tab: Tab) {
    setActiveTab(tab)
    if (tab === "volumes") setVolumesRequested(true)
  }

  function toggleChapter(urlToToggle: string, checked: boolean) {
    setSelectedVolumes(new Set())
    setSelectedChapters((current) => {
      const next = new Set(current)
      if (checked) next.add(urlToToggle)
      else next.delete(urlToToggle)
      return next
    })
    setQuickFilter(null)
  }

  function toggleVolume(number: number, checked: boolean) {
    setSelectedChapters(new Set())
    setSelectedVolumes((current) => {
      const next = new Set(current)
      if (checked) next.add(number)
      else next.delete(number)
      return next
    })
  }

  function toggleAll(checked: boolean) {
    if (activeTab === "volumes") {
      setSelectedChapters(new Set())
      setSelectedVolumes(
        checked ? new Set(volumeRows.map((volume) => volume.volume)) : new Set()
      )
      return
    }
    setSelectedVolumes(new Set())
    setSelectedChapters(
      checked ? new Set(chapters.map((chapter) => chapter.url)) : new Set()
    )
    setQuickFilter(checked ? "all" : null)
  }

  function selectMissing() {
    openTab("chapters")
    setSelectedVolumes(new Set())
    setSelectedChapters(
      new Set(
        chapters.filter((chapter) => !chapter.done).map((chapter) => chapter.url)
      )
    )
    setQuickFilter("missing")
    toast.success(
      `${missingCount} missing chapter${missingCount === 1 ? "" : "s"} selected`
    )
  }

  function selectAllChapters() {
    openTab("chapters")
    setSelectedVolumes(new Set())
    setSelectedChapters(new Set(chapters.map((chapter) => chapter.url)))
    setQuickFilter("all")
    toast.success(`All ${chapters.length} chapters selected`)
  }

  function selectLatest(count: number) {
    openTab("chapters")
    setSelectedVolumes(new Set())
    const latest = chapters.slice(-count)
    setSelectedChapters(new Set(latest.map((chapter) => chapter.url)))
    setLatestCount(count)
    setQuickFilter("latest")
    toast.success(`Latest ${latest.length} chapters selected`)
  }

  function applyQuickFilter(filter: QuickFilter) {
    if (filter === "all") {
      selectAllChapters()
      return
    }
    if (filter === "missing") {
      selectMissing()
      return
    }
    if (filter === "latest") {
      const input = window.prompt(
        "How many latest chapters?",
        String(latestCount)
      )
      if (input === null) return
      const count = Number(input.trim())
      if (!Number.isInteger(count) || count < 1 || count > chapters.length) {
        toast.error(`Enter a number between 1 and ${chapters.length}`)
        return
      }
      selectLatest(count)
      return
    }

    const input = window.prompt("Chapter range, e.g. 180-188")
    if (input === null) return
    const match = /^\s*(\d+(?:\.\d+)?)\s*-\s*(\d+(?:\.\d+)?)\s*$/.exec(input)
    if (!match) {
      toast.error("Invalid range format. Use e.g. 180-188")
      return
    }
    const from = Number(match[1])
    const to = Number(match[2])
    if (from > to) {
      toast.error("Range start must be lower than range end")
      return
    }
    const matches = chapters.filter(
      (chapter) => chapter.number >= from && chapter.number <= to
    )
    if (matches.length === 0) {
      toast.error(`No chapters found in range ${from}–${to}`)
      return
    }
    openTab("chapters")
    setSelectedVolumes(new Set())
    setSelectedChapters(new Set(matches.map((chapter) => chapter.url)))
    setQuickFilter("range")
    toast.success(`Chapters ${from}–${to} selected`)
  }

  function packToCbz() {
    onOpenPack()
  }

  async function handleOpenFolder() {
    const seriesDir = info?.series ?? slug
    const root = config?.output_root
    if (!root) {
      if (configState.error) {
        toast.error(configState.error)
      } else {
        toast.info("Output folder is still loading")
      }
      return
    }
    const folder = `${root.replace(/\/+$/, "")}/${seriesDir}`
    try {
      await openFolderAction(folder)
    } catch (error) {
      toast.error("Could not open the folder", {
        description: errorText(error),
      })
    }
  }

  async function downloadSelected() {
    if (starting) return
    if (downloadMode === "chapters" && selectedChapterCount === 0) return
    if (downloadMode === "volumes" && selectedVolumeCount === 0) return

    const trimmedDelay = delay.trim()
    if (trimmedDelay && !delayPattern.test(trimmedDelay)) {
      toast.error("Delay must be a Go duration such as 200ms or 1s")
      return
    }

    const parsedWorkers = Number(workers)
    if (!Number.isInteger(parsedWorkers) || parsedWorkers < 1 || parsedWorkers > 32) {
      toast.error("Workers must be a whole number between 1 and 32")
      return
    }

    const chapterList =
      downloadMode === "chapters"
        ? selectedChapterRows.map((chapter) => chapter.display)
        : selectedVolumeChapters.map((chapter) => chapter.display)

    setStarting(true)
    const jobId = startJob({ slug, url: seriesUrl, chapters: chapterList })

    try {
      const handle = await startDownload(
        seriesUrl,
        {
          chapters:
            downloadMode === "chapters" ? chapterList.join(",") : undefined,
          volumes:
            downloadMode === "volumes"
              ? selectedVolumeNumbers.join(",")
              : undefined,
          out: config?.output_root || undefined,
          preset,
          workers: parsedWorkers,
          delay: trimmedDelay || undefined,
          flat: flatFolders || undefined,
          pack: downloadMode === "volumes" ? packAfter : undefined,
        },
        (event) => appendEvent(jobId, event)
      )

      setHandle(jobId, handle)

      void handle.done
        .then((result) => {
          const detail = result.ok
            ? undefined
            : result.stderr.trim().split("\n")[0] ||
              `komiku-cli exited with code ${result.code ?? "?"}`
          finishJob(jobId, result.ok ? "done" : "failed", detail)
        })
        .catch((error: unknown) => {
          finishJob(jobId, "failed", errorText(error))
        })

      toast.success("Download started", {
        description:
          downloadMode === "chapters"
            ? `${chapterList.length} chapters · ${preset} preset · follow it in Downloads`
            : `${selectedVolumeCount} volumes · ${preset} preset · follow it in Downloads`,
      })
    } catch (error) {
      finishJob(jobId, "failed", errorText(error))
      toast.error("Could not start the download", {
        description: errorText(error),
      })
    } finally {
      setStarting(false)
    }
  }

  const quickChips: { id: QuickFilter; label: string }[] = [
    { id: "all", label: "All" },
    { id: "missing", label: "Missing" },
    { id: "latest", label: `Latest: ${latestCount}` },
    { id: "range", label: "Range…" },
  ]

  const ctaCount =
    downloadMode === "chapters" ? selectedChapterCount : selectedVolumeCount
  const canDownload = ctaCount > 0
  const ctaLabel =
    downloadMode === "chapters"
      ? `Download ${ctaCount} chapter${ctaCount === 1 ? "" : "s"}`
      : `Download ${ctaCount} volume${ctaCount === 1 ? "" : "s"}`

  return (
    <div className="flex h-full flex-col overflow-hidden bg-background">
      {/* Breadcrumb bar */}
      <div
        data-tauri-drag-region="deep"
        className="flex h-[52px] shrink-0 items-center gap-2 border-b border-border-subtle px-8"
      >
        <button
          type="button"
          onClick={onBack}
          aria-label="Back to library"
          className="flex size-6 items-center justify-center rounded-[5px] bg-muted text-muted-foreground transition-colors hover:text-foreground"
        >
          <ChevronLeft className="size-3.5" strokeWidth={1.8} />
        </button>
        <button
          type="button"
          onClick={onBack}
          className="text-[12px] text-muted-foreground transition-colors hover:text-foreground"
        >
          Library
        </button>
        <ChevronRight className="size-[11px] text-dim" strokeWidth={1.8} />
        <span className="max-w-[320px] truncate text-[12px] text-muted-foreground">
          {title}
        </span>
      </div>

      {infoState.loading && <LoadingState label="Loading series…" />}

      {!infoState.loading && infoState.error && (
        <ErrorState message={infoState.error} onRetry={infoState.reload} />
      )}

      {!infoState.loading && !infoState.error && !info && (
        <EmptyState
          title="No series data"
          hint="komiku-cli info returned an empty response."
        />
      )}

      {!infoState.loading && !infoState.error && info && (
        <>
          <BrowserOnlyNotice className="mx-7 mt-4" />

          {/* Hero */}
          <div className="flex shrink-0 gap-6 px-8 py-6">
            <SeriesCover
              size="lg"
              initials={initialsFromTitle(title)}
              gradient={gradientFor(slug)}
              coverPath={info.cover}
            />

            <div className="flex min-w-0 flex-1 flex-col gap-3.5">
              <h1 className="text-[28px] font-semibold leading-tight tracking-[-0.02em]">
                {title}
              </h1>

              <div className="flex flex-wrap items-center gap-2 text-[12px] text-muted-foreground">
                <span>{totalChapters} chapters</span>
                <MetaDot />
                <span>{doneCount} done</span>
                <MetaDot />
                <span className="font-mono text-dim">{seriesUrl}</span>
              </div>

              <div className="flex max-w-[440px] flex-col gap-1.5">
                <div className="flex items-center">
                  <span className="text-[12px] font-medium text-muted-foreground">
                    Downloaded {doneCount} of {totalChapters} chapters
                  </span>
                  <div className="flex-1" />
                  <span className="font-mono text-[12px] text-foreground">
                    {percent}%
                  </span>
                </div>
                <Progress value={percent} indicatorClassName="bg-success" />
              </div>

              <div className="flex flex-wrap items-center gap-2">
                <Button
                  size="sm"
                  className="h-8 px-3 text-[12px] font-semibold"
                  data-nav="series-read"
                  onClick={() => onRead()}
                >
                  <BookOpen className="size-[13px]" strokeWidth={1.8} />
                  Read latest
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-8 px-3 text-[12px]"
                  onClick={selectMissing}
                  disabled={missingCount === 0}
                >
                  <Download className="size-[13px]" strokeWidth={1.8} />
                  Missing only
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-8 px-3 text-[12px]"
                  onClick={selectAllChapters}
                  disabled={totalChapters === 0}
                >
                  <Download className="size-[13px]" strokeWidth={1.8} />
                  All chapters
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-8 px-3 text-[12px]"
                  onClick={packToCbz}
                >
                  <Package className="size-[13px]" strokeWidth={1.8} />
                  Pack to CBZ
                </Button>
                <Button
                  variant="outline"
                  size="sm"
                  className="h-8 px-3 text-[12px]"
                  onClick={() => {
                    void handleOpenFolder()
                  }}
                >
                  <FolderOpen className="size-[13px]" strokeWidth={1.8} />
                  Open folder
                </Button>
              </div>
            </div>
          </div>

          {/* Content */}
          <div className="flex min-h-0 flex-1 gap-6 px-8 pb-8">
            {/* Left: chapter / volume list */}
            <div className="flex min-w-0 flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
              {/* Tabs header */}
              <div className="flex items-center gap-5 border-b border-border-subtle px-4">
                {tabOptions.map((tab) => {
                  const isActive = activeTab === tab.id
                  return (
                    <button
                      key={tab.id}
                      type="button"
                      onClick={() => openTab(tab.id)}
                      className={cn(
                        "-mb-px flex h-11 items-center border-b-2 transition-colors",
                        isActive
                          ? "border-primary text-[13px] font-semibold text-foreground"
                          : "border-transparent text-[13px] font-medium text-dim hover:text-muted-foreground"
                      )}
                    >
                      {tab.label}
                    </button>
                  )
                })}
                <div className="flex-1" />
                <span className="rounded-full bg-primary-soft px-2 py-0.5 font-mono text-[11px] text-primary">
                  {activeSelectedCount} selected
                </span>
              </div>

              {/* Select bar */}
              {activeTab !== "files" && (
                <div className="flex items-center gap-2.5 border-b border-border-subtle bg-card px-4 py-2.5">
                  <Checkbox
                    checked={
                      activeTab === "volumes"
                        ? allVolumesSelected
                          ? true
                          : selectedVolumeCount > 0
                            ? "indeterminate"
                            : false
                        : allChaptersSelected
                          ? true
                          : selectedChapterCount > 0
                            ? "indeterminate"
                            : false
                    }
                    disabled={
                      activeTab === "volumes"
                        ? volumeRows.length === 0
                        : chapters.length === 0
                    }
                    onCheckedChange={(value) => toggleAll(value === true)}
                  />
                  <span className="text-[12px] font-medium">Select all</span>
                  <div className="flex-1" />
                  {activeTab === "chapters" && (
                    <div className="flex items-center gap-1.5">
                      {quickChips.map((chip) => (
                        <button
                          key={chip.id}
                          type="button"
                          onClick={() => applyQuickFilter(chip.id)}
                          className={cn(
                            "rounded-[5px] px-2 py-1 text-[11px] font-medium transition-colors",
                            quickFilter === chip.id
                              ? "bg-primary-soft text-primary"
                              : "text-dim hover:bg-muted hover:text-muted-foreground"
                          )}
                        >
                          {chip.label}
                        </button>
                      ))}
                    </div>
                  )}
                  {activeTab === "volumes" && (
                    <span className="font-mono text-[11px] text-dim">
                      {volumeRows.length} volumes
                    </span>
                  )}
                </div>
              )}

              {/* Scrollable list */}
              <div className="min-h-0 flex-1 overflow-y-auto">
                {activeTab === "chapters" &&
                  (chapters.length === 0 ? (
                    <EmptyState
                      title="No chapters found"
                      hint="Chapter discovery returned nothing for this series."
                    />
                  ) : (
                    chapters.map((chapter) => {
                      const isSelected = selectedChapters.has(chapter.url)
                      return (
                        <div
                          key={chapter.url}
                          className={cn(
                            "flex items-center gap-3 border-b border-border-subtle px-4 py-2.5",
                            isSelected && "bg-primary-soft"
                          )}
                        >
                          <Checkbox
                            checked={isSelected}
                            aria-label={`Select chapter ${chapter.display}`}
                            data-chapter={chapter.display}
                            onCheckedChange={(value) =>
                              toggleChapter(chapter.url, value === true)
                            }
                          />
                          <span className="w-[46px] shrink-0 font-mono text-[12px] text-muted-foreground">
                            {chapter.display}
                          </span>
                          <span className="min-w-0 flex-1 truncate font-mono text-[11px] text-dim">
                            {chapterPath(chapter.url)}
                          </span>
                          {chapter.done ? (
                            <span className="inline-flex shrink-0 items-center gap-1 rounded-full bg-success-soft px-2 py-0.5 text-[11px] text-success">
                              <Check className="size-[10px]" strokeWidth={1.8} />
                              On disk
                            </span>
                          ) : (
                            <span className="shrink-0 rounded-full bg-warning-soft px-2 py-0.5 text-[11px] text-warning">
                              Missing
                            </span>
                          )}
                          <button
                            type="button"
                            onClick={() => onRead(chapter.display)}
                            aria-label={`Read chapter ${chapter.display}`}
                            data-read-chapter={chapter.display}
                            className="flex size-7 shrink-0 items-center justify-center rounded-[5px] text-dim transition-colors hover:bg-muted hover:text-primary"
                          >
                            <BookOpen className="size-3.5" strokeWidth={1.8} />
                          </button>
                        </div>
                      )
                    })
                  ))}

                {activeTab === "volumes" &&
                  (volumesState.loading ||
                  (!volumeInfo && !volumesState.error) ? (
                    <div className="flex items-center justify-center gap-2 py-10">
                      <Loader2
                        className="size-4 animate-spin text-dim"
                        strokeWidth={1.8}
                      />
                      <span className="text-[11px] text-dim">
                        Loading volume mapping…
                      </span>
                    </div>
                  ) : volumesState.error ? (
                    <div className="m-4 flex flex-col gap-2 rounded-[8px] border border-danger/25 bg-danger-soft p-3">
                      <div className="flex items-start gap-2">
                        <AlertTriangle
                          className="mt-px size-3.5 shrink-0 text-danger"
                          strokeWidth={1.8}
                        />
                        <span className="min-w-0 flex-1 break-words font-mono text-[10px] leading-relaxed text-danger">
                          {volumesState.error}
                        </span>
                      </div>
                      <button
                        type="button"
                        onClick={volumesState.reload}
                        className="self-start rounded-[6px] border border-border bg-card px-2.5 py-1 text-[10px] text-muted-foreground hover:text-foreground"
                      >
                        Try again
                      </button>
                    </div>
                  ) : volumeRows.length === 0 ? (
                    <EmptyState
                      title="No volume mapping"
                      hint="komiku-cli returned no volumes for this series."
                    />
                  ) : (
                    volumeRows.map((volume) => {
                      const isSelected = selectedVolumes.has(volume.volume)
                      return (
                        <div
                          key={volume.volume}
                          className={cn(
                            "flex items-center gap-3 border-b border-border-subtle px-4 py-2.5",
                            isSelected && "bg-primary-soft"
                          )}
                        >
                          <Checkbox
                            checked={isSelected}
                            onCheckedChange={(value) =>
                              toggleVolume(volume.volume, value === true)
                            }
                          />
                          <span className="w-[46px] shrink-0 font-mono text-[12px] text-muted-foreground">
                            v{volume.volume}
                          </span>
                          <span className="min-w-0 flex-1 truncate font-mono text-[12px] text-muted-foreground">
                            {volume.start}–{volume.end}
                          </span>
                          <span className="shrink-0 font-mono text-[11px] text-dim">
                            {volumeChapterCounts.get(volume.volume) ?? 0} ch
                          </span>
                        </div>
                      )
                    })
                  ))}

                {activeTab === "files" && (
                  <EmptyState
                    title="Not available in the CLI yet"
                    hint="Use the library dashboard to inspect chapters on disk."
                    icon={FolderOpen}
                  />
                )}
              </div>
            </div>

            {/* Right: download setup */}
            <aside className="flex w-[330px] shrink-0 flex-col gap-3">
              <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto pr-0.5">
                <div className="flex flex-col gap-3.5 rounded-[10px] border border-border-subtle bg-surface p-4">
                  <h2 className="text-[13px] font-semibold">Download setup</h2>

                  <div className="flex flex-col gap-1.5">
                    <span className="text-[12px] font-medium text-muted-foreground">
                      Chapter selection
                    </span>
                    <div className="flex h-8 items-center rounded-[6px] border border-border bg-card px-2.5 font-mono text-[12px]">
                      {downloadMode === "chapters"
                        ? `${selectedChapterCount} chapters · ${selectedMissingCount} missing`
                        : `${selectedVolumeCount} volumes · ${selectedVolumeChapters.length} chapters`}
                    </div>
                  </div>

                  <div className="flex flex-col gap-1.5">
                    <span className="text-[12px] font-medium text-muted-foreground">
                      CBZ preset
                    </span>
                    <div className="grid grid-cols-2 gap-1.5">
                      {presetOptions.map((option) => {
                        const isActive = preset === option.id
                        return (
                          <button
                            key={option.id}
                            type="button"
                            onClick={() => setPresetOverride(option.id)}
                            className={cn(
                              "flex flex-col gap-0.5 rounded-[7px] border px-2 py-1.5 text-left transition-colors",
                              isActive
                                ? "border-primary bg-primary-soft text-primary"
                                : "border-border bg-card text-foreground hover:bg-muted"
                            )}
                          >
                            <span className="text-[12px] font-medium">
                              {option.name}
                            </span>
                            <span
                              className={cn(
                                "font-mono text-[10px]",
                                isActive ? "text-primary/70" : "text-dim"
                              )}
                            >
                              {option.desc}
                            </span>
                          </button>
                        )
                      })}
                    </div>
                  </div>

                  <div className="flex flex-col gap-2">
                    <ToggleRow
                      label="Pack after download"
                      description={
                        packAllowed
                          ? "Pack complete volumes into CBZ"
                          : "Requires a volume selection"
                      }
                      checked={packAfter && packAllowed}
                      disabled={!packAllowed}
                      onCheckedChange={setPackAfter}
                    />
                    <ToggleRow
                      label="Flat chapter folders"
                      description="Store chapters without volume folders"
                      checked={flatFolders}
                      onCheckedChange={setFlatFolders}
                    />
                  </div>

                  <div className="grid grid-cols-2 gap-2">
                    <div className="flex flex-col gap-1">
                      <span className="text-[11px] text-muted-foreground">
                        Workers
                      </span>
                      <input
                        type="number"
                        min={1}
                        max={32}
                        aria-label="Workers"
                        value={workers}
                        onChange={(event) => setWorkers(event.target.value)}
                        className="h-8 w-full rounded-[6px] border border-border bg-card px-2.5 font-mono text-[12px] text-foreground outline-none transition-colors focus:border-primary/60"
                      />
                    </div>
                    <div className="flex flex-col gap-1">
                      <span className="text-[11px] text-muted-foreground">
                        Delay
                      </span>
                      <input
                        type="text"
                        aria-label="Delay"
                        placeholder="200ms"
                        value={delay}
                        onChange={(event) => setDelayOverride(event.target.value)}
                        className="h-8 w-full rounded-[6px] border border-border bg-card px-2.5 font-mono text-[12px] text-foreground outline-none transition-colors focus:border-primary/60"
                      />
                    </div>
                  </div>

                  <div className="flex flex-col gap-0.5">
                    <span className="text-[11px] text-muted-foreground">
                      Output
                    </span>
                    <span className="truncate font-mono text-[11px] text-muted-foreground">
                      {config?.output_root ?? "CLI default"}
                    </span>
                  </div>

                  {configState.error && (
                    <span className="break-words font-mono text-[10px] leading-relaxed text-danger">
                      Config: {configState.error}
                    </span>
                  )}
                </div>

                <div className="flex flex-col gap-2.5 rounded-[8px] border border-border-subtle bg-surface p-4">
                  <h2 className="text-[13px] font-semibold">Estimate</h2>
                  <div className="flex items-center">
                    <span className="text-[12px] text-muted-foreground">
                      Chapters to fetch
                    </span>
                    <div className="flex-1" />
                    <span className="font-mono text-[12px]">
                      {downloadMode === "chapters"
                        ? selectedMissingCount
                        : selectedVolumeChapters.filter((chapter) => !chapter.done)
                            .length}
                    </span>
                  </div>
                  <div className="flex items-center">
                    <span className="text-[12px] text-muted-foreground">
                      Already on disk
                    </span>
                    <div className="flex-1" />
                    <span className="font-mono text-[12px]">
                      {downloadMode === "chapters"
                        ? selectedChapterCount - selectedMissingCount
                        : selectedVolumeChapters.filter((chapter) => chapter.done)
                            .length}
                    </span>
                  </div>
                  <div className="flex items-center">
                    <span className="text-[12px] text-muted-foreground">
                      Preset
                    </span>
                    <div className="flex-1" />
                    <span className="font-mono text-[12px]">{preset}</span>
                  </div>
                </div>

                <div className="rounded-[7px] bg-muted p-2.5">
                  <p className="text-[11px] leading-relaxed text-dim">
                    Chapters are sent as an explicit list to{" "}
                    <code className="font-mono">komiku-cli dl --ch</code>. The CLI
                    reuses validated pages already on disk, so nothing is fetched
                    twice.
                  </p>
                </div>
              </div>

              <Button
                variant="default"
                className="h-10 w-full shrink-0 text-[14px]"
                disabled={!canDownload || starting}
                onClick={() => {
                  void downloadSelected()
                }}
              >
                <Download className="size-3.5" strokeWidth={1.8} />
                {starting ? "Starting…" : ctaLabel}
              </Button>
            </aside>
          </div>
        </>
      )}
    </div>
  )
}
