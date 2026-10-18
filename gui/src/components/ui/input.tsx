import * as React from "react"

import { cn } from "@/lib/utils"

function Input({ className, type, ...props }: React.ComponentProps<"input">) {
  return (
    <input
      type={type}
      data-slot="input"
      className={cn(
        "flex h-9 w-full min-w-0 rounded-md border border-border bg-card px-3 py-1 font-mono text-[11px] text-foreground transition-colors outline-none",
        "placeholder:font-sans placeholder:text-[12px] placeholder:text-dim",
        "focus-visible:border-primary focus-visible:ring-1 focus-visible:ring-primary/40",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className
      )}
      {...props}
    />
  )
}

export { Input }
