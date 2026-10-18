import { cn } from "@/lib/utils"
import type { SeriesStatus } from "@/lib/types"
import { useCover } from "@/lib/use-cover"

export function SeriesCover({
  initials,
  gradient,
  coverPath,
  size = "md",
  className,
}: {
  initials: string
  gradient: [string, string]
  /** Local cover file reported by the CLI; falls back to the gradient tile. */
  coverPath?: string
  size?: "sm" | "md" | "lg" | "xl" | "fill"
  className?: string
}) {
  const cover = useCover(coverPath)
  const sizes = {
    sm: "size-10 rounded-[5px] text-[13px]",
    md: "h-[72px] w-[52px] rounded-md text-base",
    lg: "h-[258px] w-[184px] rounded-lg text-[40px]",
    xl: "h-[240px] w-[172px] rounded-lg text-[52px]",
    fill: "h-full w-full rounded-lg text-[44px]",
  }

  return (
    <div
      className={cn(
        "flex shrink-0 items-center justify-center overflow-hidden font-bold text-white/65",
        sizes[size],
        className
      )}
      style={{
        backgroundImage: `linear-gradient(135deg, ${gradient[0]}, ${gradient[1]})`,
      }}
    >
      {cover ? (
        <img
          src={cover}
          alt=""
          className="h-full w-full object-cover"
          draggable={false}
        />
      ) : (
        initials
      )}
    </div>
  )
}

const statusStyles: Record<SeriesStatus, string> = {
  complete: "bg-success-soft text-success",
  partial: "bg-muted text-muted-foreground",
  broken: "bg-danger-soft text-danger",
  downloading: "bg-info-soft text-info",
  new: "bg-warning-soft text-warning",
}

export function StatusBadge({
  status,
  label,
}: {
  status: SeriesStatus
  label: string
}) {
  return (
    <span
      className={cn(
        "inline-flex items-center rounded-full px-2 py-0.5 text-[10px] font-medium",
        statusStyles[status]
      )}
    >
      {label}
    </span>
  )
}

export function MetaDot() {
  return <span className="text-[10px] text-dim">·</span>
}

export function PageHeader({
  title,
  subtitle,
  children,
}: {
  title: string
  subtitle?: React.ReactNode
  children?: React.ReactNode
}) {
  return (
    <div
      data-tauri-drag-region="deep"
      className="flex w-full items-start gap-4"
    >
      <div className="flex min-w-0 flex-col gap-1">
        <h1 className="text-[30px] font-semibold leading-[1.1] tracking-[-0.02em]">
          {title}
        </h1>
        {subtitle && (
          <div className="text-[13px] leading-relaxed text-muted-foreground">
            {subtitle}
          </div>
        )}
      </div>
      <div className="flex-1" />
      {children && <div className="flex items-center gap-2">{children}</div>}
    </div>
  )
}

export function SectionTitle({
  title,
  count,
  children,
}: {
  title: string
  count?: string | number
  children?: React.ReactNode
}) {
  return (
    <div className="flex items-center gap-2.5">
      <h2 className="text-[20px] font-semibold tracking-[-0.01em]">{title}</h2>
      {count !== undefined && (
        <span className="rounded-full bg-muted px-1.5 py-0.5 font-mono text-[10px] text-muted-foreground">
          {count}
        </span>
      )}
      <div className="flex-1" />
      {children}
    </div>
  )
}

export function Breadcrumb({
  root,
  current,
  children,
}: {
  root: string
  current?: string
  children?: React.ReactNode
}) {
  return (
    <div
      data-tauri-drag-region="deep"
      className="flex h-[52px] shrink-0 items-center gap-2 border-b border-border-subtle px-7"
    >
      <span className="text-[13px] font-medium text-foreground">{root}</span>
      {current && (
        <>
          <span className="text-[12px] text-dim">/</span>
          <span className="max-w-[320px] truncate text-[12px] text-muted-foreground">
            {current}
          </span>
        </>
      )}
      <div className="flex-1" />
      {children}
    </div>
  )
}
