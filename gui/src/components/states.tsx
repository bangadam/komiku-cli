import { AlertTriangle, Loader2, MonitorX } from "lucide-react"

import { cn } from "@/lib/utils"
import { hasCliBridge } from "@/lib/cli"

export function LoadingState({ label = "Loading…" }: { label?: string }) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-2 py-16">
      <Loader2 className="size-5 animate-spin text-dim" strokeWidth={1.8} />
      <span className="text-[11px] text-dim">{label}</span>
    </div>
  )
}

export function ErrorState({
  message,
  onRetry,
}: {
  message: string
  onRetry?: () => void
}) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-2 py-16">
      <AlertTriangle className="size-5 text-danger" strokeWidth={1.8} />
      <span className="max-w-md text-center text-[11px] leading-relaxed text-danger">
        {message}
      </span>
      {onRetry && (
        <button
          onClick={onRetry}
          className="mt-1 rounded-[6px] border border-border bg-card px-2.5 py-1 text-[10px] text-muted-foreground hover:text-foreground"
        >
          Try again
        </button>
      )}
    </div>
  )
}

export function EmptyState({
  title,
  hint,
  icon: Icon,
}: {
  title: string
  hint?: string
  icon?: React.ComponentType<{ className?: string; strokeWidth?: number }>
}) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center gap-2 py-16">
      {Icon && <Icon className="size-6 text-dim" strokeWidth={1.6} />}
      <span className="text-[13px] text-muted-foreground">{title}</span>
      {hint && <span className="text-[11px] text-dim">{hint}</span>}
    </div>
  )
}

export function BrowserOnlyNotice({ className }: { className?: string }) {
  if (hasCliBridge) return null
  return (
    <div
      className={cn(
        "flex items-center gap-2 rounded-[8px] border border-warning/25 bg-warning-soft px-3 py-2",
        className
      )}
    >
      <MonitorX className="size-3.5 shrink-0 text-warning" strokeWidth={1.8} />
      <span className="text-[10px] leading-relaxed text-warning">
        Browser preview has no CLI access. Run{" "}
        <code className="font-mono">pnpm desktop</code> to use the real komiku-cli
        binary.
      </span>
    </div>
  )
}
