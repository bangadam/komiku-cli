import { useEffect, useRef, useState } from "react"
import { Loader2, RotateCcw, TriangleAlert } from "lucide-react"

import { acquirePageSource, type ReaderPage } from "@/lib/reader"
import { pageDimensions, type ReaderFit } from "@/lib/reader-state"
import { cn } from "@/lib/utils"

export function ReaderPageView({
  page,
  fit,
  zoom,
  strip = false,
  viewportHeight,
  waiting,
  onRetry,
  onLoaded,
}: {
  page: ReaderPage
  fit: ReaderFit
  zoom: number
  strip?: boolean
  viewportHeight: number
  waiting: boolean
  onRetry: () => void
  onLoaded?: (index: number) => void
}) {
  const container = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(520)
  const [nearby, setNearby] = useState(false)
  const [image, setImage] = useState<{ key: string; source: string; width: number; height: number } | null>(null)
  const [failure, setFailure] = useState<{ key: string; message: string } | null>(null)
  const identity = `${page.local ?? ""}:${page.bytes ?? 0}`
  const current = image?.key === identity ? image : null
  const failureMessage = failure?.key === identity ? failure.message : null
  // Paged mode loads the visible page right away; the strip waits until the
  // page is about to scroll into view.
  const armed = !strip || nearby
  const failed = Boolean(page.error || failureMessage || (!waiting && !page.local))

  useEffect(() => {
    const element = container.current
    if (!element) return
    const observer = new ResizeObserver(([entry]) => setWidth(Math.max(1, entry.contentRect.width)))
    observer.observe(element)
    return () => observer.disconnect()
  }, [])

  useEffect(() => {
    if (!strip) return
    const element = container.current
    if (!element) return
    const observer = new IntersectionObserver(([entry]) => {
      if (entry.isIntersecting) { setNearby(true); observer.disconnect() }
    }, { rootMargin: "1200px" })
    observer.observe(element)
    return () => observer.disconnect()
  }, [strip])

  useEffect(() => {
    if (!armed || !page.local) return
    let cancelled = false
    let release: (() => void) | undefined
    void acquirePageSource(page.local).then(async (result) => {
      if (cancelled) { result.release(); return }
      release = result.release
      const probe = new Image()
      probe.src = result.source
      await probe.decode()
      if (!cancelled) {
        setImage({ key: identity, source: result.source, width: probe.naturalWidth, height: probe.naturalHeight })
      }
    }).catch((error: unknown) => {
      if (!cancelled) setFailure({ key: identity, message: error instanceof Error ? error.message : "Invalid image." })
    })
    return () => { cancelled = true; release?.() }
  }, [armed, identity, page.local])

  const dimensions = current ? pageDimensions(current, { width, height: viewportHeight }, fit, zoom) : null
  const frameHeight = strip ? dimensions?.height ?? Math.round(width * 1.4) : undefined

  return (
    <div
      ref={container}
      data-reader-page={page.index}
      data-page-ready={Boolean(current)}
      className={cn("min-w-0 shrink-0", strip ? "w-full" : "h-full flex-1 overflow-auto")}
      style={frameHeight ? { minHeight: frameHeight } : undefined}
    >
      {current && dimensions ? (
        <div className={cn("flex", !strip && "min-h-full min-w-full")} style={{ width: Math.max(width, dimensions.width), minHeight: strip ? dimensions.height : viewportHeight }}>
          <img
            src={current.source}
            alt={`Page ${page.index}`}
            data-page-image={page.index}
            width={dimensions.width}
            height={dimensions.height}
            style={{ width: dimensions.width, height: dimensions.height, maxWidth: "none" }}
            draggable={false}
            onLoad={() => onLoaded?.(page.index)}
            className="m-auto block shrink-0 select-none"
          />
        </div>
      ) : (
        <div className="flex h-full min-h-[220px] flex-col items-center justify-center gap-3 p-6 text-center" style={strip ? { height: frameHeight } : undefined}>
          {failed ? (
            <>
              <TriangleAlert className="size-5 text-danger" aria-hidden="true" />
              <span className="text-sm text-foreground">Page {page.index} is not available yet</span>
              <span className="max-w-sm break-words text-xs text-muted-foreground">{page.error || failureMessage || "This page is not saved on this device yet."}</span>
              <button type="button" onClick={onRetry} className="inline-flex min-h-9 items-center gap-2 rounded-md border border-border bg-surface px-3 text-xs text-foreground hover:bg-muted focus-visible:outline-2 focus-visible:outline-primary">
                <RotateCcw className="size-3.5" /> Retry page {page.index}
              </button>
            </>
          ) : (
            <>
              <Loader2 className="size-5 animate-spin text-muted-foreground motion-reduce:animate-none" aria-hidden="true" />
              <span className="text-xs text-muted-foreground">Preparing page {page.index}…</span>
            </>
          )}
        </div>
      )}
    </div>
  )
}
