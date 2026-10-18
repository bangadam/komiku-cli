import { useState } from "react"
import { RefreshCw } from "lucide-react"

import { cn } from "@/lib/utils"
import { gradientFor, initialsFromTitle } from "@/lib/api"
import { typeLabel } from "@/lib/discovery"

/**
 * Remote cover image with a deterministic gradient fallback. Discovery pages
 * carry absolute thumbnail URLs, so the gradient stays visible while the image
 * loads and remains the fallback when the CDN refuses the request.
 */
export function RemoteCover({
  src,
  title,
  className,
  initials,
  rounded = "rounded-[6px]",
}: {
  src?: string
  title: string
  className?: string
  initials?: string
  rounded?: string
}) {
  const [failed, setFailed] = useState(false)
  const gradient = gradientFor(title)
  const showImage = src && !failed

  return (
    <div
      className={cn(
        "flex shrink-0 items-center justify-center overflow-hidden font-bold text-white/65",
        rounded,
        className
      )}
      style={{
        backgroundImage: `linear-gradient(135deg, ${gradient[0]}, ${gradient[1]})`,
      }}
    >
      {showImage ? (
        <img
          src={src}
          alt=""
          loading="lazy"
          draggable={false}
          onError={() => setFailed(true)}
          className="h-full w-full object-cover"
        />
      ) : (
        (initials ?? initialsFromTitle(title))
      )}
    </div>
  )
}

/** Rank number tile used by the Peringkat board. */
export function RankBadge({
  rank,
  className,
}: {
  rank: number
  className?: string
}) {
  const top = rank <= 3
  return (
    <span
      className={cn(
        "inline-flex size-6 shrink-0 items-center justify-center rounded-[6px] font-mono text-[11px] font-semibold",
        top ? "bg-primary-soft text-primary" : "bg-muted text-muted-foreground",
        className
      )}
    >
      {rank}
    </span>
  )
}

/** Trending delta badge, e.g. "+22%". */
export function DeltaBadge({
  value,
  className,
}: {
  value: string
  className?: string
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full bg-success-soft px-1.5 py-0.5 font-mono text-[10px] font-medium text-success",
        className
      )}
    >
      {value}
    </span>
  )
}

const typeTone: Record<string, string> = {
  manga: "bg-primary-soft text-primary",
  manhwa: "bg-info-soft text-info",
  manhua: "bg-warning-soft text-warning",
}

/** Small type chip: Manga / Manhwa / Manhua. */
export function TypeChip({
  type,
  className,
}: {
  type?: string
  className?: string
}) {
  const label = typeLabel(type)
  if (!label) return null
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded-full px-1.5 py-0.5 text-[10px] font-medium",
        typeTone[label.toLowerCase()] ?? "bg-muted text-muted-foreground",
        className
      )}
    >
      {label}
    </span>
  )
}

/** Letter tile with its title count, used by the A–Z index. */
export function LetterTile({
  letter,
  count,
  active,
  onClick,
}: {
  letter: string
  count: number
  active: boolean
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      data-letter={letter}
      aria-pressed={active}
      className={cn(
        "flex h-11 min-w-[38px] flex-col items-center justify-center gap-0.5 rounded-[6px] border px-1 transition-colors",
        active
          ? "border-primary bg-primary-soft text-primary"
          : "border-border-subtle bg-surface text-muted-foreground hover:border-border hover:text-foreground"
      )}
    >
      <span className="text-[12px] font-semibold leading-none">{letter}</span>
      <span
        className={cn(
          "font-mono text-[9px] leading-none",
          active ? "text-primary/80" : "text-dim"
        )}
      >
        {count.toLocaleString("id-ID")}
      </span>
    </button>
  )
}

/** "BARU" flag for recently added or updated titles. */
export function NewFlag({ className }: { className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded-[3px] bg-primary px-1 py-px text-[9px] font-bold leading-tight tracking-[0.04em] text-primary-foreground",
        className
      )}
    >
      BARU
    </span>
  )
}

/** Relative time chip ("2 jam lalu"). */
export function TimeChip({
  value,
  className,
}: {
  value?: string
  className?: string
}) {
  if (!value) return null
  return (
    <span
      className={cn(
        "inline-flex shrink-0 items-center rounded-full bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground",
        className
      )}
    >
      {value}
    </span>
  )
}

/** Segmented control used by the period, type and sort switches. */
export function SegmentedControl<T extends string>({
  options,
  value,
  onChange,
  className,
}: {
  options: { id: T; label: string; count?: number }[]
  value: T
  onChange: (value: T) => void
  className?: string
}) {
  return (
    <div
      className={cn(
        "flex items-center gap-1 rounded-[7px] border border-border bg-muted p-[3px]",
        className
      )}
    >
      {options.map((option) => (
        <button
          key={option.id}
          type="button"
          data-value={option.id}
          aria-pressed={value === option.id}
          onClick={() => onChange(option.id)}
          className={cn(
            "rounded-[5px] px-2.5 py-1 text-[11px] font-medium transition-colors",
            value === option.id
              ? "bg-card text-foreground"
              : "text-dim hover:text-muted-foreground"
          )}
        >
          {option.label}
          {option.count !== undefined && (
            <span className="ml-1 font-mono text-[9px] text-dim">
              {option.count.toLocaleString("id-ID")}
            </span>
          )}
        </button>
      ))}
    </div>
  )
}

/** Compact chip used for genre filters. */
export function Chip({
  label,
  active,
  onClick,
  className,
}: {
  label: string
  active?: boolean
  onClick?: () => void
  className?: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      data-chip={label}
      aria-pressed={active}
      className={cn(
        "h-7 shrink-0 rounded-full border px-2.5 text-[11px] font-medium transition-colors",
        active
          ? "border-primary bg-primary-soft text-primary"
          : "border-border bg-surface text-muted-foreground hover:text-foreground",
        className
      )}
    >
      {label}
    </button>
  )
}

/** 60px workspace toolbar shared by every discovery screen. */
export function DiscoveryBar({
  icon: Icon,
  label,
  crumb,
  children,
}: {
  icon: React.ComponentType<{ className?: string; strokeWidth?: number }>
  label: string
  crumb?: string
  children?: React.ReactNode
}) {
  return (
    <div
      data-tauri-drag-region="deep"
      className="flex h-[60px] shrink-0 items-center gap-2.5 px-8"
    >
      <Icon className="size-4 text-dim" strokeWidth={1.8} />
      <span className="text-[13px] font-medium">{label}</span>
      {crumb && (
        <>
          <span className="text-[12px] text-dim">/</span>
          <span className="max-w-[240px] truncate text-[12px] text-muted-foreground">
            {crumb}
          </span>
        </>
      )}
      <div className="flex-1" />
      {children}
    </div>
  )
}

/** Small refresh control used by the discovery toolbars. */
export function RefreshButton({
  busy,
  onClick,
  label = "Refresh",
}: {
  busy?: boolean
  onClick: () => void
  label?: string
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={busy}
      aria-label={label}
      title={label}
      className="flex size-7 items-center justify-center rounded-[5px] text-dim transition-colors hover:bg-muted hover:text-foreground disabled:opacity-50"
    >
      <RefreshCw
        className={cn("size-[15px]", busy && "animate-spin")}
        strokeWidth={1.8}
      />
    </button>
  )
}

/** Text + icon label used on the right side of a discovery toolbar. */
export function BarMeta({
  icon: Icon,
  children,
}: {
  icon: React.ComponentType<{ className?: string; strokeWidth?: number }>
  children: React.ReactNode
}) {
  return (
    <span className="flex items-center gap-1.5 font-mono text-[11px] text-dim">
      <Icon className="size-3.5" strokeWidth={1.8} />
      {children}
    </span>
  )
}
