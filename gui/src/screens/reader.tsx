import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore, type ReactNode } from "react"
import * as Dialog from "@radix-ui/react-dialog"
import { getCurrentWindow } from "@tauri-apps/api/window"
import { BookOpen, Check, ChevronLeft, ChevronRight, Globe, HardDrive, List, Loader2, Maximize, Minimize, Search, Settings2, X } from "lucide-react"
import { toast } from "sonner"

import { ReaderPageView } from "@/components/reader-page"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { humanBytes, titleFromSlug } from "@/lib/api"
import { isTauri } from "@/lib/cli"
import { clearPageCache, loadReaderChapter, type ReaderReport } from "@/lib/reader"
import { getProgress, getProgressSnapshot, progressKey, resetPrefs, setPrefs, setProgress, subscribeProgress, useReaderPrefs, type ReaderBackground, type ReaderFit, type ReadingMode } from "@/lib/reader-prefs"
import { clampPage, turnPage, visiblePageNumbers } from "@/lib/reader-state"
import { cn } from "@/lib/utils"

const modes: { id: ReadingMode; label: string }[] = [
  { id: "single", label: "Single" }, { id: "spread", label: "Spread" }, { id: "strip", label: "Strip" },
]
const fits: { id: ReaderFit; label: string }[] = [
  { id: "width", label: "Width" }, { id: "height", label: "Height" }, { id: "original", label: "Original" },
]
const backgrounds: { id: ReaderBackground; label: string }[] = [
  { id: "dark", label: "Dark" }, { id: "dim", label: "Dim" }, { id: "paper", label: "Paper" },
]
const backgroundClass = { dark: "bg-[#09090b]", dim: "bg-background", paper: "bg-[#e8e2d7]" }

function Segments<T extends string>({ label, options, value, onChange }: {
  label: string; options: { id: T; label: string }[]; value: T; onChange: (value: T) => void
}) {
  return (
    <div role="group" aria-label={label} className="flex w-full gap-1 rounded-[7px] border border-border bg-sidebar p-1">
      {options.map((option) => (
        <button key={option.id} type="button" data-value={option.id} aria-pressed={value === option.id} onClick={() => onChange(option.id)}
          className={cn("h-7 min-w-0 flex-1 rounded-[5px] px-2 text-xs font-medium focus-visible:outline-2 focus-visible:outline-primary", value === option.id ? "bg-muted text-foreground" : "text-muted-foreground hover:bg-muted/60 hover:text-foreground")}>
          {option.label}
        </button>
      ))}
    </div>
  )
}

function IconButton({ label, children, onClick, dataNav }: { label: string; children: ReactNode; onClick: () => void; dataNav?: string }) {
  return <button type="button" aria-label={label} title={label} data-nav={dataNav} onClick={onClick} className="flex size-9 shrink-0 items-center justify-center rounded-md bg-surface text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-2 focus-visible:outline-primary">{children}</button>
}

interface ReaderProps {
  slug: string
  seriesURL: string
  initialChapter?: string
  onBack: () => void
  onOpenSeries: (slug: string, url?: string) => void
}

export function ReaderScreen(props: ReaderProps) {
  return <ReaderSession key={`${props.seriesURL}:${props.initialChapter ?? "latest"}`} {...props} />
}

function ReaderSession({ slug, seriesURL, initialChapter, onBack, onOpenSeries }: ReaderProps) {
  const prefs = useReaderPrefs()
  const history = useSyncExternalStore(subscribeProgress, getProgressSnapshot)
  const [chapter, setChapter] = useState(initialChapter ?? "latest")
  const [attempt, setAttempt] = useState(0)
  const [offline, setOffline] = useState(false)
  const [state, setState] = useState<{
    chapter: string
    report: ReaderReport | null
    busy: boolean
    error: string | null
  }>({ chapter, report: null, busy: true, error: null })
  const [page, setPage] = useState(1)
  const [panel, setPanel] = useState<"chapters" | "settings" | null>(null)
  const [chapterQuery, setChapterQuery] = useState("")
  const [chapterFilter, setChapterFilter] = useState<"all" | "unread" | "disk">("all")
  const [fullscreen, setFullscreen] = useState(false)
  const [hidden, setHidden] = useState(false)
  const [viewportHeight, setViewportHeight] = useState(700)
  const [rendered, setRendered] = useState<Set<number>>(new Set())
  const idle = useRef<ReturnType<typeof setTimeout> | null>(null)
  const content = useRef<HTMLDivElement>(null)
  const strip = useRef<HTMLDivElement>(null)
  const pageRef = useRef(page)
  const prefetched = useRef(new Set<string>())
  // A report for a previous chapter must never render under the new chapter.
  const loaded = state.chapter === chapter ? state : null
  const report = loaded?.report ?? null
  const busy = loaded?.busy ?? true
  const failure = loaded?.error ?? null
  const count = report?.page_count ?? 0
  // Progress is keyed by the stable series slug, never the URL (trailing
  // slashes and mirror hosts would otherwise split the same series in two).
  const seriesKey = report?.series ?? slug

  useEffect(() => {
    pageRef.current = page
  }, [page])

  useEffect(() => {
    const controller = new AbortController()
    let initialized = false
    setState({ chapter, report: null, busy: true, error: null })
    setRendered(new Set())
    const publish = (value: ReaderReport) => {
      if (controller.signal.aborted) return
      if (!initialized) {
        initialized = true
        const saved = getProgress(value.series || slug, value.chapter.display)
        setPage(clampPage(saved?.page ?? 1, value.page_count))
      }
      setState((current) => ({ ...current, report: value }))
    }
    void loadReaderChapter(seriesURL, { chapter, offline, signal: controller.signal }, (event) => {
      if (controller.signal.aborted) return
      setState((current) => {
        if (!current.report) return current
        const pages = current.report.pages.map((entry) => entry.index !== event.page ? entry : event.kind === "page_cached"
          ? { ...entry, local: event.local, cached: true, bytes: event.bytes, error: undefined }
          : { ...entry, error: event.message || "Gambar gagal diunduh." })
        return { ...current, report: { ...current.report, pages, bytes: pages.reduce((sum, entry) => sum + (entry.bytes ?? 0), 0) } }
      })
    }, publish).then((value) => {
      if (!controller.signal.aborted) {
        setState({ chapter, report: value, busy: false, error: null })
      }
    }).catch((error: unknown) => {
      if (!controller.signal.aborted) {
        setState({ chapter, report: null, busy: false, error: error instanceof Error ? error.message : String(error) })
      }
    })
    return () => { controller.abort(); clearPageCache() }
  }, [seriesURL, chapter, attempt, offline])

  const nextChapter = report?.next?.display
  useEffect(() => {
    if (!prefs.preloadNext || offline || busy || !nextChapter || prefetched.current.has(nextChapter)) return
    const controller = new AbortController()
    const timer = window.setTimeout(() => {
      prefetched.current.add(nextChapter)
      void loadReaderChapter(seriesURL, { chapter: nextChapter, signal: controller.signal }, () => undefined)
        .catch(() => prefetched.current.delete(nextChapter))
    }, 1200)
    return () => { window.clearTimeout(timer); controller.abort() }
  }, [prefs.preloadNext, offline, busy, nextChapter, seriesURL])

  const visible = useMemo(
    () => visiblePageNumbers(page, count, prefs.mode),
    [page, count, prefs.mode]
  )
  const loadedChapter = report?.chapter.display ?? ""
  const firstVisible = visible[0] ?? 1
  const lastVisible = visible[visible.length - 1] ?? 1
  const knownOnDisk = report?.pages.filter((entry) => entry.local).length ?? 0
  const failed = report?.pages.filter((entry) => entry.error).length ?? 0

  useEffect(() => {
    if (!report || count < 1 || !rendered.has(page)) return
    const complete = lastVisible === count && visible.every((index) => rendered.has(index))
    setProgress(seriesKey, report.chapter.display, page, count, complete)
  }, [seriesKey, report, page, count, lastVisible, visible, rendered])

  useEffect(() => {
    const element = content.current
    if (!element) return
    const resize = new ResizeObserver(([entry]) => setViewportHeight(Math.max(160, entry.contentRect.height - 32)))
    resize.observe(element)
    return () => resize.disconnect()
  }, [])

  const hasReport = Boolean(report)
  const hasFailure = Boolean(failure)
  const reveal = useCallback(() => {
    if (idle.current) clearTimeout(idle.current)
    setHidden(false)
    if (prefs.autoHideChrome && !panel && hasReport && !hasFailure) {
      idle.current = setTimeout(() => setHidden(true), 2500)
    }
  }, [prefs.autoHideChrome, panel, hasReport, hasFailure])

  useEffect(() => { reveal(); return () => { if (idle.current) clearTimeout(idle.current) } }, [reveal])

  const retry = useCallback(() => {
    clearPageCache()
    setOffline(false)
    setAttempt((value) => value + 1)
  }, [])

  function selectChapter(display: string) {
    if (display === report?.chapter.display) { setPanel(null); return }
    setPanel(null)
    setChapter(display)
    setPage(1)
  }

  function goChapter(direction: -1 | 1) {
    const target = direction < 0 ? report?.prev : report?.next
    if (target) selectChapter(target.display)
  }

  function goTo(next: number) {
    setPage(clampPage(next, count))
    if (prefs.mode === "strip") {
      const node = strip.current?.querySelector<HTMLElement>(`[data-reader-page="${clampPage(next, count)}"]`)
      if (node && strip.current) strip.current.scrollTo({ top: node.offsetTop - strip.current.offsetTop, behavior: "instant" })
    }
  }

  function move(direction: -1 | 1) { goTo(turnPage(page, count, prefs.mode, direction)) }

  const toggleFullscreen = useCallback(async () => {
    try {
      if (isTauri) {
        const window = getCurrentWindow()
        await window.setFullscreen(!(await window.isFullscreen()))
        setFullscreen(await window.isFullscreen())
      } else if (document.fullscreenElement) {
        await document.exitFullscreen()
      } else {
        await document.documentElement.requestFullscreen()
      }
    } catch (error) {
      toast.error("Fullscreen tidak tersedia", { description: error instanceof Error ? error.message : String(error) })
    }
  }, [])

  useEffect(() => {
    const update = () => {
      if (isTauri) void getCurrentWindow().isFullscreen().then(setFullscreen).catch(() => undefined)
      else setFullscreen(Boolean(document.fullscreenElement))
    }
    update()
    document.addEventListener("fullscreenchange", update)
    window.addEventListener("resize", update)
    return () => { document.removeEventListener("fullscreenchange", update); window.removeEventListener("resize", update) }
  }, [])

  useEffect(() => {
    const keydown = (event: KeyboardEvent) => {
      const target = event.target as HTMLElement | null
      if (event.defaultPrevented || event.metaKey || event.ctrlKey || event.altKey || target?.isContentEditable || target?.closest("input,textarea,select,[role=slider],[role=switch]")) return
      reveal()
      if (panel) return
      if (event.key === "Escape") {
        if (fullscreen) void toggleFullscreen()
        else onBack()
      } else if (event.key.toLowerCase() === "f") {
        event.preventDefault(); void toggleFullscreen()
      } else if (event.key.toLowerCase() === "j") {
        event.preventDefault(); goChapter(1)
      } else if (event.key.toLowerCase() === "k") {
        event.preventDefault(); goChapter(-1)
      } else if (event.key === "ArrowRight" || event.key === "ArrowLeft") {
        event.preventDefault()
        const forward = event.key === "ArrowRight" ? !prefs.rightToLeft : prefs.rightToLeft
        move(forward ? 1 : -1)
      }
    }
    window.addEventListener("keydown", keydown)
    return () => window.removeEventListener("keydown", keydown)
  })

  useEffect(() => {
    if (prefs.mode !== "strip" || !loadedChapter) return
    const element = strip.current
    if (!element) return
    let scheduled = 0
    let restoring = true
    const restore = () => {
      const target = element.querySelector<HTMLElement>(`[data-reader-page="${pageRef.current}"]`)
      if (target) element.scrollTop = target.offsetTop - element.offsetTop
    }
    const track = () => {
      if (restoring || scheduled) return
      scheduled = requestAnimationFrame(() => {
        scheduled = 0
        const reference = element.getBoundingClientRect().top + Math.min(150, element.clientHeight * 0.2)
        const nodes = [...element.querySelectorAll<HTMLElement>("[data-reader-page]")]
        const current = nodes.find((node) => node.getBoundingClientRect().bottom > reference)
        if (current) setPage(Number(current.dataset.readerPage))
      })
    }
    const frame = requestAnimationFrame(() => { restore(); restoring = false })
    element.addEventListener("scroll", track, { passive: true })
    return () => { cancelAnimationFrame(frame); cancelAnimationFrame(scheduled); element.removeEventListener("scroll", track) }
  }, [prefs.mode, loadedChapter, count])

  const drawerRows = useMemo(() => {
    const rows = report?.chapters?.length ? report.chapters : report ? [report.prev, report.chapter, report.next].filter((row) => Boolean(row)) : []
    const term = chapterQuery.trim().toLowerCase().replace(/^chapter\s+/, "")
    return rows.filter((row): row is NonNullable<typeof row> => Boolean(row))
      .slice().sort((a, b) => b.number - a.number)
      .filter((row) => row.display.toLowerCase().includes(term)
        && (chapterFilter !== "disk" || row.done)
        && (chapterFilter !== "unread" || !history[progressKey(seriesKey, row.display)]))
  }, [report, chapterQuery, chapterFilter, history, seriesKey])

  const onPageLoaded = useCallback((index: number) => setRendered((current) => current.has(index) ? current : new Set([...current, index])), [])
  const modePages = prefs.mode === "strip" ? report?.pages ?? [] : (report?.pages ?? []).filter((entry) => visible.includes(entry.index))
  const leftDirection = prefs.rightToLeft ? 1 : -1
  const rightDirection = prefs.rightToLeft ? -1 : 1
  const labelFor = (direction: number) => direction > 0 ? "Halaman berikutnya" : "Halaman sebelumnya"
  const canMove = (direction: number) => count > 0 && (direction > 0 ? lastVisible < count : firstVisible > 1)
  const source = report?.source === "library" ? "Offline" : report?.source === "cache" ? "Cached" : "Online"
  const statusLabel = busy ? `Menyiapkan cache ${knownOnDisk}/${count}` : failed ? `${failed} halaman perlu dicoba lagi` : ""
  const chromeClass = hidden ? "pointer-events-none opacity-0" : "opacity-100"

  return (
    <div data-reader-root data-reader-background={prefs.background} className={cn("flex h-full flex-col overflow-hidden", backgroundClass[prefs.background])} onPointerMove={reveal} onPointerDown={reveal} onFocusCapture={reveal}>
      <header data-tauri-drag-region="deep" data-reader-chrome="top" inert={hidden} className={cn("flex h-14 shrink-0 items-center gap-2.5 border-b border-border bg-sidebar px-4 text-foreground transition-opacity motion-reduce:transition-none", chromeClass)}>
        <IconButton label="Back to series" dataNav="reader-back" onClick={onBack}><ChevronLeft className="size-4" /></IconButton>
        <button onClick={() => onOpenSeries(slug, seriesURL)} className="max-w-[32vw] truncate text-sm font-medium hover:text-primary focus-visible:outline-2 focus-visible:outline-primary">{titleFromSlug(slug)}</button>
        <span className="text-muted-foreground">/</span>
        <span className="shrink-0 text-xs text-muted-foreground">{report ? `Chapter ${report.chapter.display}` : "Menyiapkan chapter…"}</span>
        {report && <span className="hidden items-center gap-1 rounded-md bg-surface px-2 py-1 text-xs text-info sm:inline-flex">{source === "Offline" ? <HardDrive className="size-3" /> : <Globe className="size-3" />}{source}</span>}
        <div className="flex-1" />
        {busy && report && <Loader2 className="size-3.5 animate-spin text-muted-foreground motion-reduce:animate-none" aria-label="Cache sedang disiapkan" />}
        <span data-reader-counter className="shrink-0 rounded-md bg-surface px-2 py-1 font-mono text-xs">{count ? `${visible.length === 2 ? `${firstVisible}–${lastVisible}` : page} / ${count}` : "— / —"}</span>
        <IconButton label="Chapters" dataNav="reader-chapters" onClick={() => setPanel("chapters")}><List className="size-4" /></IconButton>
        <IconButton label="Reader settings" dataNav="reader-settings" onClick={() => setPanel("settings")}><Settings2 className="size-4" /></IconButton>
        <IconButton label={fullscreen ? "Exit fullscreen" : "Fullscreen"} onClick={() => { void toggleFullscreen() }}>{fullscreen ? <Minimize className="size-4" /> : <Maximize className="size-4" />}</IconButton>
      </header>

      <main ref={content} aria-label="Reader pages" className="relative flex min-h-0 flex-1 flex-col overflow-hidden">
        {failure && report && <div role="alert" className="flex shrink-0 items-center gap-3 border-b border-border bg-surface px-4 py-2 text-xs text-danger"><span className="min-w-0 flex-1 truncate">{failure}</span><button onClick={retry} className="shrink-0 underline">Coba lagi</button></div>}
        {!report ? (
          <div className="flex flex-1 flex-col items-center justify-center gap-4 px-8 text-center">
            {failure ? <><BookOpen className="size-6 text-danger" /><h1 className="text-base text-foreground">Chapter tidak bisa dimuat</h1><p role="alert" className="max-w-lg break-words text-sm text-muted-foreground">{failure}</p><div className="flex gap-2"><Button onClick={retry}>Coba lagi</Button><Button variant="secondary" onClick={() => { setOffline(true); setAttempt((n) => n + 1) }}>Baca cache offline</Button></div></>
              : <><Loader2 className="size-5 animate-spin text-muted-foreground motion-reduce:animate-none" /><span role="status" className="text-sm text-muted-foreground">Menyiapkan chapter…</span><Button variant="secondary" onClick={onBack}>Kembali ke series</Button></>}
          </div>
        ) : !count ? <div className="m-auto text-center text-sm text-muted-foreground">Chapter tidak memiliki halaman. Pilih chapter lain.</div> : (
          <>
            {prefs.mode === "strip" ? (
              <div ref={strip} data-reader-strip tabIndex={0} aria-label="Halaman scroll vertikal" className="relative min-h-0 flex-1 overflow-auto px-6 py-4 focus-visible:outline-2 focus-visible:outline-primary">
                <div className="mx-auto flex w-full max-w-[860px] flex-col" style={{ gap: prefs.gap }}>
                  {modePages.map((entry) => <ReaderPageView key={`${report.chapter.display}:${entry.index}`} page={entry} strip fit={prefs.fit} zoom={prefs.zoom} viewportHeight={viewportHeight} waiting={busy} onRetry={retry} onLoaded={onPageLoaded} />)}
                  <div className="flex justify-center py-8">{report.next ? <Button onClick={() => goChapter(1)}>Lanjut Chapter {report.next.display}</Button> : <span className="rounded-md bg-surface px-4 py-2 text-sm text-muted-foreground">Chapter terakhir</span>}</div>
                </div>
              </div>
            ) : (
              <div data-reader-paged className={cn("flex min-h-0 flex-1 overflow-hidden px-14 py-4", prefs.mode === "spread" && prefs.rightToLeft && "flex-row-reverse")} style={{ gap: prefs.mode === "spread" ? prefs.gap : 0 }}>
                {modePages.map((entry) => <ReaderPageView key={`${report.chapter.display}:${entry.index}`} page={entry} fit={prefs.fit} zoom={prefs.zoom} viewportHeight={viewportHeight} waiting={busy} onRetry={retry} onLoaded={onPageLoaded} />)}
              </div>
            )}
            {prefs.mode !== "strip" && <>
              <button type="button" aria-label={labelFor(leftDirection)} data-page-edge="left" disabled={!canMove(leftDirection)} onClick={() => move(leftDirection)} className="absolute left-3 top-1/2 z-10 flex h-[72px] w-10 -translate-y-1/2 items-center justify-center rounded-lg border border-border bg-surface text-muted-foreground hover:text-foreground focus-visible:outline-2 focus-visible:outline-primary disabled:opacity-30"><ChevronLeft className="size-5" /></button>
              <button type="button" aria-label={labelFor(rightDirection)} data-page-edge="right" disabled={!canMove(rightDirection)} onClick={() => move(rightDirection)} className="absolute right-3 top-1/2 z-10 flex h-[72px] w-10 -translate-y-1/2 items-center justify-center rounded-lg border border-border bg-surface text-muted-foreground hover:text-foreground focus-visible:outline-2 focus-visible:outline-primary disabled:opacity-30"><ChevronRight className="size-5" /></button>
            </>}
            {statusLabel && <div role="status" className="pointer-events-none absolute bottom-3 left-3 rounded-md bg-sidebar px-3 py-1.5 text-xs text-muted-foreground">{statusLabel}</div>}
          </>
        )}
      </main>

      <footer data-reader-chrome="bottom" inert={hidden} className={cn("flex h-16 shrink-0 items-center gap-4 border-t border-border bg-sidebar px-4 text-foreground transition-opacity motion-reduce:transition-none", chromeClass)}>
        <Button variant="secondary" className="shrink-0" data-nav="reader-prev-chapter" disabled={!report?.prev} onClick={() => goChapter(-1)}><ChevronLeft className="size-4" />{report?.prev ? `Ch. ${report.prev.display}` : "Awal"}</Button>
        <div className="flex min-w-0 flex-1 items-center gap-3">
          <input aria-label="Lompat ke halaman" type="range" min={1} max={Math.max(1, count)} value={page} disabled={!count} onChange={(event) => goTo(Number(event.target.value))} className="h-6 min-w-0 max-w-[520px] flex-1 accent-primary" />
          <span className="font-mono text-xs text-muted-foreground">{count ? Math.round(lastVisible / count * 100) : 0}%</span>
        </div>
        <div className="hidden shrink-0 text-[10px] text-muted-foreground xl:block">← → halaman · J / K chapter · F fullscreen</div>
        <div className="w-[186px] shrink-0"><Segments label="Reading mode" options={modes} value={prefs.mode} onChange={(mode) => setPrefs({ mode })} /></div>
        <Button className="shrink-0" data-nav="reader-next-chapter" disabled={!report?.next} onClick={() => goChapter(1)}>{report?.next ? `Ch. ${report.next.display}` : "Akhir"}<ChevronRight className="size-4" /></Button>
      </footer>

      <Dialog.Root open={panel !== null} onOpenChange={(open) => { if (!open) setPanel(null) }}>
        <Dialog.Portal>
          <Dialog.Overlay className="fixed inset-0 z-40 bg-black/65" />
          <Dialog.Content onCloseAutoFocus={(event) => { event.preventDefault(); document.querySelector<HTMLButtonElement>(`[data-nav="reader-${panel === "chapters" ? "chapters" : "settings"}"]`)?.focus() }}
            className={cn("fixed z-50 flex max-w-[calc(100vw-24px)] flex-col bg-surface text-foreground shadow-xl outline-none", panel === "chapters" ? "inset-y-0 left-0 w-[420px] border-r border-border bg-sidebar" : "right-4 top-16 bottom-20 w-[360px] rounded-lg border border-border")}>
            <div className="flex h-14 shrink-0 items-center gap-2 border-b border-border px-5">
              <Dialog.Title className="text-base font-semibold">{panel === "chapters" ? "Chapters" : "Reader settings"}</Dialog.Title>
              {panel === "chapters" && <span className="font-mono text-xs text-muted-foreground">{report?.chapter_total ?? 0} total</span>}
              <Dialog.Close aria-label={panel === "chapters" ? "Close chapters" : "Close settings"} className="ml-auto flex size-8 items-center justify-center rounded-md hover:bg-muted focus-visible:outline-2 focus-visible:outline-primary"><X className="size-4" /></Dialog.Close>
            </div>
            <Dialog.Description className="sr-only">{panel === "chapters" ? "Cari dan pilih chapter. Progres baca terpisah dari unduhan." : "Sesuaikan mode, tampilan halaman, dan navigasi reader. Preferensi disimpan di perangkat ini."}</Dialog.Description>
            {panel === "chapters" ? <>
              <div className="space-y-3 p-5 pb-3">
                <div className="flex gap-1 rounded-md bg-surface p-1" role="group" aria-label="Chapter filter">{([{ id: "all", label: "All" }, { id: "unread", label: "Unread" }, { id: "disk", label: "On disk" }] as const).map((option) => <button key={option.id} data-chapter-filter={option.id} aria-pressed={chapterFilter === option.id} onClick={() => setChapterFilter(option.id)} className={cn("h-8 flex-1 rounded-md text-xs focus-visible:outline-2 focus-visible:outline-primary", chapterFilter === option.id ? "bg-muted" : "text-muted-foreground hover:text-foreground")}>{option.label}</button>)}</div>
                <label className="flex items-center gap-2 rounded-md border border-border bg-surface px-3"><Search className="size-4 text-muted-foreground" /><input aria-label="Cari chapter" placeholder="Cari chapter…" value={chapterQuery} onChange={(event) => setChapterQuery(event.target.value)} className="h-9 min-w-0 flex-1 bg-transparent text-sm outline-none" /></label>
              </div>
              <div className="min-h-0 flex-1 overflow-y-auto px-3 pb-3">
                {drawerRows.length ? drawerRows.map((row) => {
                  const current = row.display === report?.chapter.display
                  const seen = history[progressKey(seriesKey, row.display)]
                  return <button key={`${row.display}:${row.url}`} data-chapter-row={row.display} aria-current={current ? "page" : undefined} onClick={() => selectChapter(row.display)} className={cn("flex min-h-11 w-full items-center gap-3 rounded-md px-3 text-left text-xs focus-visible:outline-2 focus-visible:outline-primary", current ? "bg-muted font-semibold" : "text-muted-foreground hover:bg-muted/60")}>
                    <span className="w-12 shrink-0 font-mono">{row.display}</span><span className="min-w-0 flex-1 truncate">Chapter {row.display}</span>
                    {current ? <span className="text-primary">current</span> : seen ? <span className="text-muted-foreground">{seen.completed ? "selesai" : "dibaca"}</span> : <span className="size-1.5 rounded-full bg-primary" aria-label="Belum dibaca" />}
                    {row.done && <Check className="size-3.5 text-success" aria-label="On disk" />}
                  </button>
                }) : <p className="py-8 text-center text-sm text-muted-foreground">Tidak ada chapter yang cocok.</p>}
              </div>
              <div className="border-t border-border px-5 py-4 text-xs text-muted-foreground">Progres baca disimpan otomatis · Esc tutup</div>
            </> : <div className="min-h-0 flex-1 space-y-5 overflow-y-auto p-5">
              <Setting label="Reading mode"><Segments label="Settings reading mode" options={modes} value={prefs.mode} onChange={(mode) => setPrefs({ mode })} /></Setting>
              <Setting label="Background"><Segments label="Reader background" options={backgrounds} value={prefs.background} onChange={(background) => setPrefs({ background })} /></Setting>
              <Setting label="Fit"><Segments label="Page fit" options={fits} value={prefs.fit} onChange={(fit) => setPrefs({ fit })} /></Setting>
              <Setting label={`Page gap · ${prefs.gap} px`}><input aria-label="Page gap" type="range" min={0} max={32} step={2} value={prefs.gap} onChange={(event) => setPrefs({ gap: Number(event.target.value) })} className="h-6 w-full accent-primary" /></Setting>
              <Setting label={`Zoom · ${prefs.zoom}%`}><input aria-label="Zoom" type="range" min={60} max={200} step={10} value={prefs.zoom} onChange={(event) => setPrefs({ zoom: Number(event.target.value) })} className="h-6 w-full accent-primary" /></Setting>
              <Toggle label="Right-to-left (manga)" hint="Urutan Spread dan tombol panah dibalik" checked={prefs.rightToLeft} onChange={(rightToLeft) => setPrefs({ rightToLeft })} />
              <Toggle label="Preload next chapter" hint="Siapkan cache berikutnya setelah chapter ini" checked={prefs.preloadNext} onChange={(preloadNext) => setPrefs({ preloadNext })} />
              <Toggle label="Auto-hide chrome" hint="Kontrol disembunyikan saat tidak digunakan" checked={prefs.autoHideChrome} onChange={(autoHideChrome) => setPrefs({ autoHideChrome })} />
              <div className="space-y-1 border-t border-border pt-4 text-xs text-muted-foreground"><p className="font-medium">Sumber gambar</p><p>{source === "Offline" ? "Library lokal" : source === "Cached" ? "Cache reader" : "Komiku · online"}</p><p className="font-mono">{knownOnDisk}/{count} halaman · {humanBytes(report?.bytes ?? 0)} di disk</p>{failed > 0 && <p className="text-warning">{failed} halaman belum tersimpan.</p>}</div>
              <Button variant="secondary" className="w-full" onClick={resetPrefs}>Reset reader defaults</Button>
              <Dialog.Close asChild><Button className="w-full">Selesai</Button></Dialog.Close>
            </div>}
          </Dialog.Content>
        </Dialog.Portal>
      </Dialog.Root>
    </div>
  )
}

function Setting({ label, children }: { label: string; children: ReactNode }) {
  return <div className="space-y-2"><div className="text-xs font-medium text-muted-foreground">{label}</div>{children}</div>
}

function Toggle({ label, hint, checked, onChange }: { label: string; hint: string; checked: boolean; onChange: (checked: boolean) => void }) {
  return <label className="flex items-center gap-3"><span className="min-w-0 flex-1"><span className="block text-sm">{label}</span><span className="mt-1 block text-xs leading-relaxed text-muted-foreground">{hint}</span></span><Switch aria-label={label} checked={checked} onCheckedChange={onChange} /></label>
}
