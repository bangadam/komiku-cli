import { useMemo, useState, type ReactNode } from "react"
import {
  ChevronDown,
  Folder,
  Package,
  RefreshCw,
  TriangleAlert,
} from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import { Input } from "@/components/ui/input"
import {
  BrowserOnlyNotice,
  EmptyState,
  ErrorState,
  LoadingState,
} from "@/components/states"
import {
  getConfig,
  getSeriesInfo,
  humanBytes,
  packSeries,
  verifySeries,
  titleFromSlug,
  type LibraryReport,
  type LibrarySeries,
  type PackOptions,
  type SeriesInfo,
  type VolumeInfo,
} from "@/lib/api"
import { hasCliBridge } from "@/lib/cli"
import { useAsync, type AsyncState } from "@/lib/use-async"
import { cn } from "@/lib/utils"

type Mode = "volume" | "flat" | "recover"
type Preset = "raw" | "medium" | "small" | "tiny"

const presetOptions: { id: Preset; name: string; desc: string }[] = [
  { id: "raw", name: "Raw", desc: "lossless" },
  { id: "medium", name: "Medium", desc: "1600 · q72" },
  { id: "small", name: "Small", desc: "1400 · q65" },
  { id: "tiny", name: "Tiny", desc: "1200 · q60" },
]

function isPreset(value: string | undefined): value is Preset {
  return (
    value === "raw" ||
    value === "medium" ||
    value === "small" ||
    value === "tiny"
  )
}

function seriesUrl(slug: string): string {
  return `https://komiku.org/manga/${slug}/`
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error)
}

function basename(path: string): string {
  const parts = path.split(/[\\/]/)
  return parts[parts.length - 1] || path
}

/** Compress sorted volume numbers into the CLI's `--vol` expression. */
function compressRanges(numbers: number[]): string {
  if (numbers.length === 0) return ""
  const sorted = [...numbers].sort((a, b) => a - b)
  const parts: string[] = []
  let start = sorted[0]
  let previous = sorted[0]

  for (let index = 1; index < sorted.length; index += 1) {
    const current = sorted[index]
    if (current === previous + 1) {
      previous = current
      continue
    }
    parts.push(start === previous ? String(start) : `${start}-${previous}`)
    start = current
    previous = current
  }
  parts.push(start === previous ? String(start) : `${start}-${previous}`)
  return parts.join(",")
}

/** Compress the mapping into the CLI's `--volume-map` expression. */
function compressMapping(volumes: VolumeInfo[]): string {
  return [...volumes]
    .sort((a, b) => a.volume - b.volume)
    .map((volume) => `${volume.volume}:${volume.start}-${volume.end}`)
    .join(",")
}

/** Split the CLI's single stderr line into the error and its own hint text. */
function splitCliMessage(message: string): { error: string; hint: string } {
  const index = message.indexOf("; ")
  if (index < 0) return { error: message, hint: "" }
  return {
    error: message.slice(0, index),
    hint: message.slice(index + 2).trim(),
  }
}

function wholeChapter(display: string): number {
  const value = Number.parseFloat(display)
  return Number.isFinite(value) ? Math.trunc(value) : 0
}

function Field({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[11px] font-medium text-muted-foreground">
        {label}
      </span>
      {children}
    </div>
  )
}

function ReadonlyBox({ children }: { children: ReactNode }) {
  return (
    <div className="flex min-h-8 items-center rounded-[6px] border border-border bg-card px-2.5 py-1.5 font-mono text-[11px]">
      {children}
    </div>
  )
}

function SourceStat({ value, label }: { value: string; label: string }) {
  return (
    <div className="flex items-baseline gap-1.5">
      <span className="font-mono text-[12px] font-semibold">{value}</span>
      <span className="text-[10px] text-dim">{label}</span>
    </div>
  )
}

function presetLabel(preset: Preset): string {
  return presetOptions.find((option) => option.id === preset)?.name ?? preset
}

export function PackScreen({
  library,
}: {
  library: AsyncState<LibraryReport | null>
}) {
  const [selectedDir, setSelectedDir] = useState<string | null>(null)
  const [modePreference, setModePreference] = useState<Mode>("volume")
  // Per-series state carries its slug, so switching series resets it without
  // an effect. `null` volumes mean "every complete volume".
  const [selectionState, setSelectionState] = useState<{
    slug: string
    volumes: Set<number> | null
  } | null>(null)
  const [presetOverride, setPresetOverride] = useState<Preset | null>(null)
  const [seriesNameState, setSeriesNameState] = useState<{
    slug: string
    value: string
  } | null>(null)
  const [wikipediaTitle, setWikipediaTitle] = useState("")
  const [packState, setPackState] = useState<{
    slug: string
    error: string | null
    warnings: string[]
  } | null>(null)
  const [busy, setBusy] = useState(false)

  const config = useAsync(
    () => (hasCliBridge ? getConfig() : Promise.resolve(null)),
    []
  )

  const seriesList = useMemo<LibrarySeries[]>(
    () => library.data?.series ?? [],
    [library.data]
  )
  // Default to the first series without an effect: the explicit choice wins
  // while it still exists in the freshly scanned library.
  const activeDir =
    selectedDir && seriesList.some((item) => item.dir === selectedDir)
      ? selectedDir
      : (seriesList[0]?.dir ?? null)
  const selected = useMemo(
    () => seriesList.find((item) => item.dir === activeDir) ?? null,
    [seriesList, activeDir]
  )

  const selectedSlug = selected?.slug ?? null

  const selection =
    selectionState && selectionState.slug === selectedSlug
      ? selectionState.volumes
      : null
  const seriesNameOverride =
    seriesNameState && seriesNameState.slug === selectedSlug
      ? seriesNameState.value
      : null
  const packError =
    packState && packState.slug === selectedSlug ? packState.error : null
  const warnings =
    packState && packState.slug === selectedSlug ? packState.warnings : []

  // The volume mapping is what the screen shows, and the same mapping is sent
  // to pack. Read it from disk first: a series that was mapped once never
  // needs another lookup. Only a series with no local mapping asks a provider.
  const info = useAsync<SeriesInfo | null>(
    async () => {
      if (!hasCliBridge || !selected) return null
      const url = seriesUrl(selected.slug)
      let local: SeriesInfo | null = null
      try {
        local = await getSeriesInfo(url, { volumes: true, offline: true })
        if ((local.volumes?.length ?? 0) > 0) return local
      } catch {
        // An older CLI without --offline still works through the online path.
        local = null
      }
      if (local && local.chapters.length > 0) {
        // Chapters are known but no mapping is stored. Ask a provider once —
        // MangaDex first because it accepts partial mappings, then the auto
        // chain for series it does not cover. The result is cached, so this
        // happens at most once per series.
        try {
          const online = await getSeriesInfo(url, { volumes: true })
          if ((online.volumes?.length ?? 0) > 0) return online
          const auto = await getSeriesInfo(url, {
            volumes: true,
            volumeSource: "auto",
          })
          if ((auto.volumes?.length ?? 0) > 0) return auto
          return {
            ...online,
            volume_error: online.volume_error ?? auto.volume_error,
          }
        } catch {
          return local
        }
      }
      return getSeriesInfo(url, { volumes: true })
    },
    [selected?.slug]
  )

  // Real page counts per chapter, used by the Pages column. verify is the only
  // command that reports page totals, and it runs offline.
  const pages = useAsync<Map<number, number>>(async () => {
    const map = new Map<number, number>()
    if (!hasCliBridge || !selected) return map
    try {
      const report = await verifySeries(selected.dir)
      for (const chapter of report.chapters ?? []) {
        map.set(chapter.number, chapter.pages)
      }
    } catch {
      // A series that verify cannot read simply shows "—" per volume.
    }
    return map
  }, [selected?.dir])

  const volumeInfo = useMemo(() => {
    if (!info.data) return null
    if (selected && info.data.series !== selected.slug) return null
    return info.data
  }, [info.data, selected])

  const volumes = useMemo(
    () => volumeInfo?.volumes ?? [],
    [volumeInfo]
  )
  const mappingError = volumeInfo?.volume_error ?? null
  const hasMapping = volumes.length > 0
  // While the mapping loads, keep the user's choice. A series without one has
  // only a single thing to pack, so the choice disappears.
  const mode: Mode =
    modePreference === "recover"
      ? "recover"
      : info.loading || hasMapping
        ? modePreference
        : "flat"
  const doneWhole = useMemo(() => {
    const done = new Set<number>()
    for (const chapter of volumeInfo?.chapters ?? []) {
      if (!chapter.done) continue
      const whole = wholeChapter(chapter.display)
      if (whole > 0) done.add(whole)
    }
    return done
  }, [volumeInfo])

  /** A volume can be packed only when every chapter in its range is done. */
  const packable = useMemo(() => {
    const result = new Map<number, boolean>()
    for (const volume of volumes) {
      let complete = true
      for (let chapter = volume.start; chapter <= volume.end; chapter += 1) {
        if (!doneWhole.has(chapter)) {
          complete = false
          break
        }
      }
      result.set(volume.volume, complete)
    }
    return result
  }, [volumes, doneWhole])

  const packableVolumes = useMemo(
    () => volumes.filter((volume) => packable.get(volume.volume)),
    [volumes, packable]
  )

  // `null` selection means "all complete volumes"; the toggle buttons replace
  // it with an explicit set.
  const selectedVolumes = useMemo(() => {
    if (selection) return selection
    return new Set(packableVolumes.map((volume) => volume.volume))
  }, [selection, packableVolumes])

  const preset: Preset =
    presetOverride ??
    (isPreset(config.data?.preset) ? config.data.preset : "medium")

  const selectedMapping = useMemo(
    () => volumes.filter((volume) => selectedVolumes.has(volume.volume)),
    [volumes, selectedVolumes]
  )
  const volumeExpression = useMemo(
    () => compressRanges([...selectedVolumes]),
    [selectedVolumes]
  )
  const volumeMapExpression = useMemo(
    () => compressMapping(selectedMapping),
    [selectedMapping]
  )

  const seriesName =
    seriesNameOverride ?? (selected ? titleFromSlug(selected.slug) : "")

  const localChapters = useMemo(() => {
    const done = (volumeInfo?.chapters ?? [])
      .filter((chapter) => chapter.done)
      .map((chapter) => chapter.display)
    return done
  }, [volumeInfo])

  // `info` needs the series page, so an offline GUI can still count the
  // chapter folders from the library scan and pack them as one file.
  const localChapterCount = volumeInfo
    ? localChapters.length
    : (selected?.chapters ?? 0)

  const flatArchive = useMemo(() => {
    const name = seriesName.trim() || "Series"
    if (localChapters.length === 1) {
      return `${name} Chapter ${localChapters[0]}.cbz`
    }
    if (localChapters.length > 1) {
      return `${name} Chapters ${localChapters[0]}-${
        localChapters[localChapters.length - 1]
      }.cbz`
    }
    return `${name} Chapters {first}-{last}.cbz`
  }, [seriesName, localChapters])

  const targetDir =
    config.data && selected
      ? `${config.data.output_root}/${selected.slug}`
      : "—"

  const infoError = info.error ? splitCliMessage(info.error) : null
  const mappingNote = mappingError
    ? splitCliMessage(mappingError)
    : infoError

  // The CLI names a volume archive "<directory basename> Volume NN.cbz" and a
  // flat run "<series> Chapters X-Y.cbz". Showing that rule keeps the preview
  // truthful instead of offering a naming field the CLI cannot honour.
  const archivePreview = useMemo(() => {
    if (!selected) return "—"
    const base = basename(selected.dir)
    if (mode === "flat") return flatArchive
    const numbers = [...selectedVolumes].sort((a, b) => a - b)
    if (mode === "recover" || numbers.length === 0) {
      return `${base} Volume NN.cbz`
    }
    const pad = (value: number) => String(value).padStart(2, "0")
    if (numbers.length === 1) return `${base} Volume ${pad(numbers[0])}.cbz`
    return `${base} Volume ${pad(numbers[0])}.cbz … ${pad(numbers[numbers.length - 1])}.cbz`
  }, [selected, mode, flatArchive, selectedVolumes])

  /** Sum the real page totals of every chapter the mapping covers. */
  function pagesFor(volume: VolumeInfo): string {
    const known = pages.data
    if (!known || known.size === 0) return "—"
    let total = 0
    let found = 0
    for (let chapter = volume.start; chapter <= volume.end; chapter += 1) {
      const value = known.get(chapter)
      if (value === undefined) continue
      total += value
      found += 1
    }
    return found === 0 ? "—" : String(total)
  }

  const packLabel =
    mode === "flat"
      ? "Pack one file"
      : mode === "recover"
        ? "Recover volumes"
        : `Pack ${selectedVolumes.size} volume${
            selectedVolumes.size === 1 ? "" : "s"
          }`

  const packDisabled =
    busy ||
    !selected ||
    (mode === "volume" && selectedVolumes.size === 0) ||
    (mode === "flat" && localChapterCount === 0) ||
    (mode === "recover" && localChapterCount === 0)

  function toggleVolume(volume: number, checked: boolean) {
    if (!selectedSlug) return
    setSelectionState(() => {
      const base = selection ?? new Set(packableVolumes.map((v) => v.volume))
      const next = new Set(base)
      if (checked) next.add(volume)
      else next.delete(volume)
      return { slug: selectedSlug, volumes: next }
    })
  }

  function toggleAllVolumes(checked: boolean) {
    if (!selectedSlug) return
    setSelectionState({
      slug: selectedSlug,
      volumes: checked
        ? new Set(packableVolumes.map((volume) => volume.volume))
        : new Set(),
    })
  }

  function handleSeriesChange(dir: string) {
    setSelectedDir(dir)
  }

  async function handlePack() {
    if (!selected || !selectedSlug || packDisabled) return

    const target = selected
    const options: PackOptions =
      mode === "volume"
        ? {
            volumes: volumeExpression,
            volumeMap: volumeMapExpression,
            preset,
          }
        : mode === "recover"
          ? {
              recoverWikipedia: true,
              wikipediaTitle: wikipediaTitle.trim() || undefined,
              preset,
            }
          : { flat: true, series: seriesName.trim() || undefined, preset }

    setBusy(true)
    setPackState({ slug: selectedSlug, error: null, warnings: [] })
    const toastId = toast.loading("Packing…")

    try {
      const result = await packSeries(target.dir, options)
      setPackState({
        slug: selectedSlug,
        error: null,
        warnings: result.warnings,
      })
      const archives = result.archives
      toast.success(`${archives.length} CBZ archives created`, {
        id: toastId,
        description:
          archives.length > 0
            ? archives.map(basename).join(" · ")
            : result.output,
      })
      library.reload()
    } catch (error) {
      const message = errorMessage(error)
      setPackState({ slug: selectedSlug, error: message, warnings: [] })
      toast.error(message, { id: toastId })
    } finally {
      setBusy(false)
    }
  }

  const selectedVisibleCount = volumes.filter((volume) =>
    selectedVolumes.has(volume.volume)
  ).length
  const allVolumesSelected =
    packableVolumes.length > 0 &&
    selectedVisibleCount === packableVolumes.length
  const someVolumesSelected =
    selectedVisibleCount > 0 && !allVolumesSelected

  return (
    <div className="flex h-full flex-col gap-5 overflow-hidden px-8 py-7">
      <BrowserOnlyNotice />

      {/* Header */}
      <div data-tauri-drag-region="deep" className="flex items-start gap-4">
        <div className="flex min-w-0 flex-col gap-1.5">
          <h1 className="text-[30px] font-semibold leading-[1.1] tracking-[-0.02em]">
            Pack to CBZ
          </h1>
          <span className="text-[13px] text-muted-foreground">
            Offline packing from local images — no network requests
          </span>
        </div>
      </div>

      {!hasCliBridge ? (
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
          <EmptyState
            title="Open the desktop app to pack series"
            hint="The browser preview cannot run komiku-cli pack."
            icon={Package}
          />
        </div>
      ) : library.loading ? (
        <LoadingState label="Scanning library…" />
      ) : library.error ? (
        <ErrorState message={library.error} onRetry={library.reload} />
      ) : seriesList.length === 0 ? (
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
          <EmptyState
            title="Nothing to pack yet"
            hint="Download chapters first"
            icon={Package}
          />
        </div>
      ) : (
        <div className="flex min-h-0 flex-1 gap-5">
          {/* Source + volume mapping */}
          <div className="flex min-w-0 flex-1 flex-col gap-3">
            <div className="flex items-center gap-1.5 rounded-[6px] border border-border bg-muted p-[3px]">
              {(
                [
                  { id: "volume", label: "By volume" },
                  { id: "flat", label: "Flat run" },
                  { id: "recover", label: "Recover" },
                ] as { id: Mode; label: string }[]
              ).map((option) => (
                <button
                  key={option.id}
                  type="button"
                  data-value={option.id}
                  aria-pressed={mode === option.id}
                  onClick={() => setModePreference(option.id)}
                  className={cn(
                    "flex-1 rounded-[5px] px-2 py-1.5 text-[12px] font-medium transition-colors",
                    mode === option.id
                      ? "bg-card text-foreground"
                      : "text-dim hover:text-muted-foreground"
                  )}
                >
                  {option.label}
                </button>
              ))}
            </div>

            <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-[8px] border border-border-subtle bg-surface">
            <div className="flex flex-col gap-3 border-b border-border-subtle px-4 py-4">
              <div className="flex items-center gap-2">
                <span className="text-[13px] font-semibold">Source folder</span>
                <div className="flex-1" />
                <span className="font-mono text-[11px] text-dim">
                  {seriesList.length} series
                </span>
              </div>

              <div className="relative">
                <Folder
                  className="pointer-events-none absolute left-3 top-1/2 size-3.5 -translate-y-1/2 text-muted-foreground"
                  strokeWidth={1.8}
                />
                <select
                  aria-label="Source series"
                  value={activeDir ?? ""}
                  onChange={(event) => handleSeriesChange(event.target.value)}
                  className="h-[38px] w-full appearance-none rounded-[6px] border border-border bg-card pl-9 pr-9 text-[12px] text-foreground outline-none transition-colors focus:border-primary/60"
                >
                  {seriesList.map((item) => (
                    <option key={item.dir} value={item.dir}>
                      {titleFromSlug(item.slug)}
                    </option>
                  ))}
                </select>
                <ChevronDown
                  className="pointer-events-none absolute right-3 top-1/2 size-[13px] -translate-y-1/2 text-dim"
                  strokeWidth={1.8}
                />
              </div>

              <span className="truncate font-mono text-[11px] text-dim">
                {selected?.dir ?? "—"}
              </span>

              {selected && (
                <div className="flex items-center gap-5">
                  <SourceStat
                    value={String(selected.chapters)}
                    label="chapters"
                  />
                  <SourceStat value={String(selected.done)} label="complete" />
                  <SourceStat
                    value={String(selected.chapters - selected.done)}
                    label="missing"
                  />
                  <SourceStat
                    value={humanBytes(selected.bytes)}
                    label="on disk"
                  />
                </div>
              )}
            </div>

            {mode === "volume" ? (
              <>
                <div className="flex items-center gap-2.5 border-b border-border-subtle bg-card px-4.5 py-2.5">
                  <Checkbox
                    aria-label={
                      allVolumesSelected
                        ? "Clear all volumes"
                        : "Select all complete volumes"
                    }
                    checked={
                      allVolumesSelected
                        ? true
                        : someVolumesSelected
                          ? "indeterminate"
                          : false
                    }
                    disabled={packableVolumes.length === 0}
                    onCheckedChange={(value) => toggleAllVolumes(value === true)}
                  />
                  <span className="w-[70px] shrink-0 text-[10px] font-semibold text-dim">
                    Volume
                  </span>
                  <span className="flex-1 text-[10px] font-semibold text-dim">
                    Chapters
                  </span>
                  <span className="w-[64px] shrink-0 text-right text-[10px] font-semibold text-dim">
                    Pages
                  </span>
                  <span className="w-[82px] shrink-0 text-right text-[10px] font-semibold text-dim">
                    Status
                  </span>
                </div>

                <div className="min-h-0 flex-1 overflow-y-auto">
                  {info.loading ? (
                    <LoadingState label="Loading volume mapping…" />
                  ) : (
                    volumes.map((volume) => {
                      const isSelected = selectedVolumes.has(volume.volume)
                      const isComplete = packable.get(volume.volume) === true
                      return (
                        <div
                          key={volume.volume}
                          onClick={() =>
                            isComplete &&
                            toggleVolume(volume.volume, !isSelected)
                          }
                          className={cn(
                            "flex items-center gap-2.5 border-b border-border-subtle px-4.5 py-[11px]",
                            isComplete ? "cursor-pointer" : "cursor-default",
                            isSelected && "bg-primary-soft"
                          )}
                        >
                          <Checkbox
                            checked={isSelected}
                            disabled={!isComplete}
                            onClick={(event) => event.stopPropagation()}
                            onCheckedChange={(value) =>
                              toggleVolume(volume.volume, value === true)
                            }
                          />
                          <span
                            className={cn(
                              "w-[70px] shrink-0 font-mono text-[11px]",
                              isSelected
                                ? "text-primary"
                                : isComplete
                                  ? "text-foreground"
                                  : "text-dim"
                            )}
                          >
                            v{volume.volume}
                          </span>
                          <span className="flex min-w-0 flex-1 flex-col">
                            <span className="truncate font-mono text-[10px] text-muted-foreground">
                              {volume.start}–{volume.end}
                            </span>
                            <span className="truncate text-[9px] text-dim">
                              {volume.end - volume.start + 1} chapters
                              {isComplete ? " · complete" : " · missing"}
                            </span>
                          </span>
                          <span className="w-[64px] shrink-0 text-right font-mono text-[10px] text-dim">
                            {pagesFor(volume)}
                          </span>
                          <span className="w-[82px] shrink-0 text-right">
                            <span
                              className={cn(
                                "inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium",
                                isComplete
                                  ? "bg-success-soft text-success"
                                  : "bg-warning-soft text-warning"
                              )}
                            >
                              {isComplete ? "Ready" : "Partial"}
                            </span>
                          </span>
                        </div>
                      )
                    })
                  )}
                </div>
              </>
            ) : (
              <div className="flex min-h-0 flex-1 flex-col gap-3 overflow-y-auto px-4 py-4">
                {info.loading ? (
                  <LoadingState label="Reading local chapters…" />
                ) : mode === "recover" ? (
                  <>
                    <div className="rounded-[7px] border border-border-subtle bg-card p-3">
                      <div className="text-[12px] font-medium">
                        Rebuild volume boundaries
                      </div>
                      <p className="mt-1 text-[11px] leading-relaxed text-muted-foreground">
                        Asks English Wikipedia for this series&apos; volume
                        chapters and packs the local chapters into those volumes.
                        Use it for legacy flat runs that predate the stored
                        mapping.
                      </p>
                    </div>

                    <div className="grid grid-cols-2 gap-2">
                      <div className="rounded-[7px] border border-border-subtle bg-card p-3">
                        <div className="font-mono text-[18px] font-semibold">
                          {localChapterCount}
                        </div>
                        <div className="text-[11px] text-dim">
                          chapters on disk
                        </div>
                      </div>
                      <div className="rounded-[7px] border border-border-subtle bg-card p-3">
                        <div className="truncate font-mono text-[18px] font-semibold">
                          {localChapters.length > 0
                            ? `${localChapters[0]}–${
                                localChapters[localChapters.length - 1]
                              }`
                            : "—"}
                        </div>
                        <div className="text-[11px] text-dim">
                          local chapter range
                        </div>
                      </div>
                    </div>

                    <Field label="Wikipedia title (optional)">
                      <Input
                        aria-label="Wikipedia title"
                        placeholder={seriesName}
                        value={wikipediaTitle}
                        onChange={(event) =>
                          setWikipediaTitle(event.target.value)
                        }
                      />
                      <span className="text-[10px] text-dim">
                        Leave empty to use the series name. Only needed when the
                        Wikipedia article is titled differently.
                      </span>
                    </Field>

                    {mappingNote && (
                      <div className="rounded-[7px] border border-warning/25 bg-warning-soft p-2.5">
                        <div className="flex items-center gap-1.5">
                          <TriangleAlert
                            className="size-[13px] shrink-0 text-warning"
                            strokeWidth={1.8}
                          />
                          <span className="text-[11px] font-medium text-warning">
                            Stored mapping unavailable
                          </span>
                        </div>
                        <p className="mt-1 break-words font-mono text-[10px] leading-relaxed text-warning">
                          {mappingNote.error}
                        </p>
                      </div>
                    )}
                  </>
                ) : (
                  <>
                    <div className="grid grid-cols-2 gap-2">
                      <div className="rounded-[7px] border border-border-subtle bg-card p-3">
                        <div className="font-mono text-[18px] font-semibold">
                          {localChapterCount}
                        </div>
                        <div className="text-[11px] text-dim">
                          chapters on disk
                        </div>
                      </div>
                      <div className="rounded-[7px] border border-border-subtle bg-card p-3">
                        <div className="truncate font-mono text-[18px] font-semibold">
                          {localChapters.length > 0
                            ? `${localChapters[0]}–${
                                localChapters[localChapters.length - 1]
                              }`
                            : "—"}
                        </div>
                        <div className="text-[11px] text-dim">
                          local chapter range
                        </div>
                      </div>
                    </div>

                    <Field label="Series name">
                      <Input
                        aria-label="Series name"
                        value={seriesName}
                        onChange={(event) =>
                          setSeriesNameState({
                            slug: selectedSlug ?? "",
                            value: event.target.value,
                          })
                        }
                      />
                    </Field>

                    <Field label="Output archive">
                      <ReadonlyBox>{flatArchive}</ReadonlyBox>
                      <span className="text-[10px] text-dim">
                        Preview only — the CLI derives the final name from the
                        local chapter range. Missing chapters are skipped, not
                        invented.
                      </span>
                    </Field>

                    {mappingNote && (
                      <div className="rounded-[7px] border border-warning/25 bg-warning-soft p-2.5">
                        <div className="flex items-center gap-1.5">
                          <TriangleAlert
                            className="size-[13px] shrink-0 text-warning"
                            strokeWidth={1.8}
                          />
                          <span className="text-[11px] font-medium text-warning">
                            Volume mapping unavailable
                          </span>
                        </div>
                        <p className="mt-1 break-words font-mono text-[10px] leading-relaxed text-warning">
                          {mappingNote.error}
                        </p>
                        {mappingNote.hint && (
                          <p className="mt-1 break-words text-[10px] leading-relaxed text-warning/80">
                            {mappingNote.hint}
                          </p>
                        )}
                        <button
                          type="button"
                          onClick={info.reload}
                          className="mt-1.5 inline-flex items-center gap-1 text-[10px] text-warning transition-opacity hover:opacity-80"
                        >
                          <RefreshCw className="size-3" strokeWidth={1.8} />
                          Retry mapping
                        </button>
                      </div>
                    )}
                  </>
                )}
              </div>
            )}
            </div>
          </div>

          {/* Output setup */}
          <aside className="flex w-[350px] shrink-0 flex-col gap-3.5 overflow-y-auto">
            <div className="flex flex-col gap-3.5 rounded-[8px] border border-border-subtle bg-surface p-4">
              <h2 className="text-[13px] font-semibold">Output</h2>

              <div className="grid grid-cols-2 gap-1.5">
                {presetOptions.map((option) => {
                  const isActive = preset === option.id
                  return (
                    <button
                      key={option.id}
                      type="button"
                      onClick={() => setPresetOverride(option.id)}
                      className={cn(
                        "flex flex-col gap-0.5 rounded-[6px] border p-2 text-left transition-colors",
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

              <div className="flex flex-col gap-1">
                <span className="text-[11px] font-medium text-muted-foreground">
                  Archive naming
                </span>
                <ReadonlyBox>{archivePreview}</ReadonlyBox>
                <span className="text-[10px] leading-relaxed text-dim">
                  komiku-cli derives the filename from the series folder and the
                  volume number.
                </span>
              </div>

              {config.loading ? (
                <span className="text-[10px] text-dim">Loading config…</span>
              ) : config.error ? (
                <div className="flex flex-col gap-1">
                  <span className="break-words text-[10px] leading-relaxed text-danger">
                    {config.error}
                  </span>
                  <button
                    type="button"
                    onClick={config.reload}
                    className="self-start text-[11px] text-primary transition-opacity hover:opacity-80"
                  >
                    Retry config →
                  </button>
                </div>
              ) : (
                <span className="truncate font-mono text-[10px] text-dim">
                  {config.data?.config_path ?? "—"}
                </span>
              )}
            </div>

            <div className="flex flex-col gap-3 rounded-[8px] border border-border-subtle bg-surface p-4">
              <h2 className="text-[13px] font-semibold">Pack preview</h2>

              <div className="flex flex-col gap-2">
                <div className="flex items-center">
                  <span className="text-[12px] text-muted-foreground">
                    {mode === "flat"
                      ? "Chapters"
                      : mode === "recover"
                        ? "Local chapters"
                        : "Selected volumes"}
                  </span>
                  <div className="flex-1" />
                  <span className="font-mono text-[12px]">
                    {mode === "volume"
                      ? `${selectedVolumes.size} of ${packableVolumes.length} complete`
                      : mode === "recover"
                        ? `${localChapterCount} → Wikipedia volumes`
                        : `${localChapterCount} in one file`}
                  </span>
                </div>
                <div className="flex items-center">
                  <span className="text-[12px] text-muted-foreground">
                    Preset
                  </span>
                  <div className="flex-1" />
                  <span className="font-mono text-[12px]">
                    {presetLabel(preset)}
                  </span>
                </div>
                <div className="flex items-center gap-3">
                  <span className="shrink-0 text-[12px] text-muted-foreground">
                    Target directory
                  </span>
                  <div className="flex-1" />
                  <span className="min-w-0 truncate font-mono text-[11px]">
                    {targetDir}
                  </span>
                </div>
              </div>

              {mode === "volume" && selectedVolumes.size > 0 && (
                <div className="rounded-[6px] bg-muted p-2.5">
                  <span className="break-all font-mono text-[10px] leading-relaxed text-dim">
                    --vol {volumeExpression}
                  </span>
                </div>
              )}

              {warnings.length > 0 && (
                <div className="flex flex-col gap-1.5 rounded-[6px] border border-warning/25 bg-warning-soft p-2.5">
                  <div className="flex items-center gap-1.5">
                    <TriangleAlert
                      className="size-[13px] shrink-0 text-warning"
                      strokeWidth={1.8}
                    />
                    <span className="text-[11px] font-medium text-warning">
                      Warnings
                    </span>
                  </div>
                  {warnings.map((warning, index) => (
                    <span
                      key={`${index}-${warning}`}
                      className="break-words font-mono text-[10px] leading-relaxed text-warning"
                    >
                      {warning}
                    </span>
                  ))}
                </div>
              )}
            </div>

            <Button
              className="h-10 w-full"
              onClick={() => {
                void handlePack()
              }}
              disabled={packDisabled}
            >
              <Package className="size-3.5" strokeWidth={1.8} />
              {busy ? "Packing…" : packLabel}
            </Button>

            {packError && (
              <div className="rounded-[7px] border border-danger/25 bg-danger-soft p-2.5">
                <p className="break-words font-mono text-[10px] leading-relaxed text-danger">
                  {packError}
                </p>
              </div>
            )}

            <div className="rounded-[7px] bg-muted p-2.5">
              <p className="text-[11px] leading-relaxed text-dim">
                {mode === "volume"
                  ? "Packing uses the mapping shown here, so no lookup runs again. Volumes with missing chapters stay disabled."
                  : mode === "recover"
                    ? "Recover reads the volume boundaries from English Wikipedia, then packs the local chapters into them. It needs a network request."
                    : "One file packs every local chapter into a single CBZ. Use this when the series has no volume mapping."}
              </p>
            </div>
          </aside>
        </div>
      )}
    </div>
  )
}
