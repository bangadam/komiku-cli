import * as React from "react"
import * as SwitchPrimitive from "@radix-ui/react-switch"

import { cn } from "@/lib/utils"

function Switch({
  className,
  ...props
}: React.ComponentProps<typeof SwitchPrimitive.Root>) {
  return (
    <SwitchPrimitive.Root
      data-slot="switch"
      className={cn(
        "peer inline-flex h-[18px] w-8 shrink-0 items-center rounded-full border-0 transition-colors outline-none",
        "focus-visible:ring-1 focus-visible:ring-primary/50",
        "data-[state=checked]:bg-primary data-[state=unchecked]:bg-muted",
        "disabled:cursor-not-allowed disabled:opacity-50",
        className
      )}
      {...props}
    >
      <SwitchPrimitive.Thumb
        data-slot="switch-thumb"
        className={cn(
          "pointer-events-none block size-3.5 rounded-full transition-transform",
          "data-[state=checked]:translate-x-[16px] data-[state=checked]:bg-primary-foreground",
          "data-[state=unchecked]:translate-x-0.5 data-[state=unchecked]:bg-dim"
        )}
      />
    </SwitchPrimitive.Root>
  )
}

export { Switch }
