import { useSyncExternalStore } from "react"
import {
  AlertTriangle,
  CalendarPlus,
  Clock,
  Download,
  Flame,
  Library,
  List,
  Package,
  Search,
  Settings,
  ShieldCheck,
  Sparkles,
  Tags,
  Trophy,
  type LucideIcon,
} from "lucide-react"

import { humanBytes, type LibraryReport } from "@/lib/api"
import { getJobs, subscribe } from "@/lib/download-store"
import type { LibraryFilter, Screen } from "@/lib/types"
import { cn } from "@/lib/utils"

interface NavItem {
  id: Screen
  label: string
  icon: LucideIcon
  count?: number
}

interface NavGroup {
  id: string
  heading: string
  /** Collection rows use the taller 38px rhythm and a 14px label. */
  variant: "nav" | "collection"
  items: NavItem[]
}

function SidebarRow({
  label,
  icon: Icon,
  active,
  count,
  variant = "nav",
  navId,
  onClick,
}: {
  label: string
  icon: LucideIcon
  active: boolean
  count?: number
  variant?: "nav" | "collection"
  /** Stable hook for the E2E suite; the accessible name includes the count. */
  navId?: string
  onClick: () => void
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      data-nav={navId}
      aria-current={active ? "page" : undefined}
      className={cn(
        "flex w-full items-center gap-2.5 rounded-[6px] px-3 text-left transition-colors",
        variant === "nav" ? "h-[34px]" : "h-[38px]",
        active
          ? "bg-muted text-foreground"
          : "text-muted-foreground hover:bg-muted/60 hover:text-foreground"
      )}
    >
      <Icon
        className={cn("size-[17px]", active ? "text-foreground" : "text-dim")}
        strokeWidth={1.8}
      />
      <span
        className={cn(
          "min-w-0 flex-1 truncate font-medium",
          variant === "nav" ? "text-[13px]" : "text-[14px]"
        )}
      >
        {label}
      </span>
      {count !== undefined && count > 0 && (
        <span className="shrink-0 font-mono text-[10px] text-muted-foreground">
          {count}
        </span>
      )}
    </button>
  )
}

function GroupHeading({ children }: { children: React.ReactNode }) {
  return (
    <span className="px-3 pb-1 text-[10px] font-semibold uppercase tracking-[0.08em] text-dim">
      {children}
    </span>
  )
}

const collectionFilters: LibraryFilter[] = ["all", "attention"]

export function Sidebar({
  active,
  filter,
  library,
  onSelect,
  onCollection,
}: {
  active: Screen
  filter: LibraryFilter
  library: LibraryReport | null
  onSelect: (screen: Screen) => void
  onCollection: (filter: LibraryFilter) => void
}) {
  const jobs = useSyncExternalStore(subscribe, getJobs)
  const running = jobs.filter((job) => job.status === "running").length

  const series = library?.series ?? []
  const problems = library?.total_problems ?? 0
  const storage = library
    ? `${humanBytes(library.total_bytes)} · ${library.total_cbz} CBZ`
    : "Scanning library…"

  const groups: NavGroup[] = [
    {
      id: "library",
      heading: "Library",
      variant: "nav",
      items: [
        { id: "library", label: "Library", icon: Library },
        { id: "search", label: "Search", icon: Search },
      ],
    },
    {
      id: "explore",
      heading: "Jelajahi",
      variant: "nav",
      items: [
        { id: "terbaru", label: "Terbaru", icon: Clock },
        { id: "peringkat", label: "Peringkat", icon: Trophy },
        { id: "trending", label: "Trending", icon: Flame },
        { id: "baru", label: "Baru", icon: Sparkles },
        { id: "daftar", label: "Daftar Komik", icon: List },
        { id: "genre", label: "Genre", icon: Tags },
      ],
    },
    {
      id: "tools",
      heading: "Tools",
      variant: "nav",
      items: [
        { id: "downloads", label: "Downloads", icon: Download, count: running },
        { id: "pack", label: "Pack to CBZ", icon: Package },
        { id: "verify", label: "Verify", icon: ShieldCheck, count: problems },
      ],
    },
    {
      id: "collections",
      heading: "Collections",
      variant: "collection",
      items: [
        { id: "library", label: "Recently added", icon: CalendarPlus },
        { id: "library", label: "Needs attention", icon: AlertTriangle },
      ],
    },
  ]

  return (
    <aside className="flex w-[224px] shrink-0 flex-col border-r border-border-subtle bg-sidebar px-4 py-6">
      <div
        data-tauri-drag-region="deep"
        className="flex h-7 items-center gap-2.5 px-1"
      >
        <div className="flex size-7 items-center justify-center rounded-[7px] bg-primary">
          <span className="text-[15px] font-bold leading-none text-primary-foreground">
            K
          </span>
        </div>
        <span className="text-[18px] font-semibold tracking-[-0.01em]">
          komiku
        </span>
      </div>

      <nav className="mt-6 flex flex-col gap-5">
        {groups.map((group) => (
          <div key={group.id} className="flex flex-col gap-0.5">
            <GroupHeading>{group.heading}</GroupHeading>
            {group.items.map((item, index) => {
              if (group.id === "collections") {
                const itemFilter = collectionFilters[index]
                return (
                  <SidebarRow
                    key={item.label}
                    label={item.label}
                    icon={item.icon}
                    variant="collection"
                    navId={`collection-${itemFilter}`}
                    active={active === "library" && filter === itemFilter}
                    onClick={() => onCollection(itemFilter)}
                  />
                )
              }
              return (
                <SidebarRow
                  key={item.id}
                  label={item.label}
                  icon={item.icon}
                  navId={item.id}
                  active={active === item.id}
                  count={item.count}
                  onClick={() => onSelect(item.id)}
                />
              )
            })}
          </div>
        ))}
      </nav>

      <div className="flex-1" />

      <div className="px-3 pb-1">
        <div className="text-[13px] font-medium">Local library</div>
        <div className="mt-1 font-mono text-[11px] text-muted-foreground">
          {storage}
        </div>
        <div className="mt-1 text-[11px] text-dim">
          {series.length} series tracked
        </div>
      </div>

      <div className="mt-4">
        <SidebarRow
          label="Settings"
          icon={Settings}
          navId="settings"
          variant="collection"
          active={active === "settings"}
          onClick={() => onSelect("settings")}
        />
      </div>
    </aside>
  )
}
