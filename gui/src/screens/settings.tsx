import { useEffect, useRef, useState, type ReactNode } from "react"
import { open } from "@tauri-apps/plugin-dialog"
import {
  Check,
  Copy,
  ExternalLink,
  FileText,
  Folder,
  Info,
  Settings2,
} from "lucide-react"
import { toast } from "sonner"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { PageHeader } from "@/components/shared"
import {
  BrowserOnlyNotice,
  EmptyState,
  ErrorState,
  LoadingState,
} from "@/components/states"
import {
  getConfig,
  humanBytes,
  saveConfig,
  type ConfigReport,
  type LibraryReport,
} from "@/lib/api"
import { cliBinary, cliVersion, hasCliBridge, openConfigFile } from "@/lib/cli"
import { useAsync, type AsyncState } from "@/lib/use-async"
import { cn } from "@/lib/utils"

type Preset = "raw" | "medium" | "small" | "tiny"

const DEFAULT_DELAY = "200ms"

const presetOptions: { id: Preset; label: string }[] = [
  { id: "raw", label: "Raw" },
  { id: "medium", label: "Medium" },
  { id: "small", label: "Small" },
  { id: "tiny", label: "Tiny" },
]

// Go time.ParseDuration, which the CLI uses for --delay. Accepts "200ms",
// "1s", "1.5s" and composites such as "1m30s".
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

function SettingsCard({
  title,
  description,
  children,
}: {
  title: string
  description: string
  children: ReactNode
}) {
  return (
    <div className="flex flex-col gap-3.5 rounded-[8px] border border-border-subtle bg-surface p-4">
      <div className="flex flex-col gap-0.5">
        <h2 className="text-[13px] font-semibold">{title}</h2>
        <p className="text-[12px] text-dim">{description}</p>
      </div>
      {children}
    </div>
  )
}

function FactRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center gap-3">
      <span className="text-[12px] text-dim">{label}</span>
      <div className="flex-1" />
      <span className="font-mono text-[12px] text-muted-foreground">
        {value}
      </span>
    </div>
  )
}

export function SettingsScreen({
  library,
}: {
  library: AsyncState<LibraryReport | null>
}) {
  const configState = useAsync(
    () => (hasCliBridge ? getConfig() : Promise.resolve(null)),
    []
  )
  const binaryState = useAsync(
    () => (hasCliBridge ? cliBinary() : Promise.resolve("")),
    []
  )
  const versionState = useAsync(
    () => (hasCliBridge ? cliVersion() : Promise.resolve("")),
    []
  )

  const [root, setRoot] = useState("")
  const [delay, setDelay] = useState(DEFAULT_DELAY)
  const [preset, setPreset] = useState<Preset>("medium")
  const [baseline, setBaseline] = useState<ConfigReport | null>(null)
  const [saved, setSaved] = useState(false)
  const [busy, setBusy] = useState(false)
  const [picking, setPicking] = useState(false)
  const savedTimer = useRef<number | null>(null)

  const config = configState.data

  // Seed the editable fields from the real config, and reseed whenever the CLI
  // reports a new baseline (save, reset, reload).
  useEffect(() => {
    if (!config) return
    setBaseline(config)
    setRoot(config.output_root)
    setDelay(config.image_delay)
    setPreset(isPreset(config.preset) ? config.preset : "medium")
  }, [config])

  useEffect(() => {
    return () => {
      if (savedTimer.current !== null) {
        window.clearTimeout(savedTimer.current)
      }
    }
  }, [])

  function flashSaved() {
    if (savedTimer.current !== null) {
      window.clearTimeout(savedTimer.current)
    }
    setSaved(true)
    savedTimer.current = window.setTimeout(() => {
      setSaved(false)
      savedTimer.current = null
    }, 2000)
  }

  async function pickFolder() {
    if (picking) return
    setPicking(true)
    try {
      const current = root.trim()
      const selected = await open({
        directory: true,
        multiple: false,
        title: "Choose download root",
        defaultPath:
          current && current !== "." && current !== "./" ? current : undefined,
      })
      if (typeof selected === "string") setRoot(selected)
    } catch (error) {
      toast.error("Could not open the folder picker", {
        description: errorText(error),
      })
    } finally {
      setPicking(false)
    }
  }

  function commitDelay() {
    const trimmed = delay.trim()
    if (!delayPattern.test(trimmed)) {
      toast.error("Invalid request delay", {
        description: "Use Go duration syntax such as 200ms, 1s or 1.5s.",
      })
      setDelay(baseline?.image_delay ?? DEFAULT_DELAY)
      return
    }
    setDelay(trimmed)
  }

  async function handleSave() {
    if (busy) return

    const trimmedDelay = delay.trim()
    if (!delayPattern.test(trimmedDelay)) {
      toast.error("Invalid request delay", {
        description: "Use Go duration syntax such as 200ms, 1s or 1.5s.",
      })
      setDelay(baseline?.image_delay ?? DEFAULT_DELAY)
      return
    }

    setBusy(true)
    try {
      const report = await saveConfig({
        out: root.trim() || ".",
        delay: trimmedDelay,
        preset,
      })
      setBaseline(report)
      setRoot(report.output_root)
      setDelay(report.image_delay)
      setPreset(isPreset(report.preset) ? report.preset : "medium")
      configState.setData(report)
      flashSaved()
      toast.success("Settings saved", { description: report.config_path })
    } catch (error) {
      toast.error("Could not save settings", {
        description: errorText(error),
      })
    } finally {
      setBusy(false)
    }
  }

  async function handleReset() {
    if (busy) return

    setBusy(true)
    try {
      const report = await saveConfig({
        out: ".",
        delay: DEFAULT_DELAY,
        preset: "medium",
      })
      setBaseline(report)
      setRoot(report.output_root)
      setDelay(report.image_delay)
      setPreset(isPreset(report.preset) ? report.preset : "medium")
      configState.reload()
      library.reload()
      flashSaved()
      toast.success("Reset to CLI defaults", {
        description: report.config_path,
      })
    } catch (error) {
      toast.error("Could not reset settings", {
        description: errorText(error),
      })
    } finally {
      setBusy(false)
    }
  }

  async function copyConfigPath() {
    const path = configState.data?.config_path
    if (!path) {
      toast.error("Config path is not loaded yet")
      return
    }
    try {
      await navigator.clipboard.writeText(path)
      toast.success("Config path copied", { description: path })
    } catch (error) {
      toast.error("Could not copy the config path", {
        description: errorText(error),
      })
    }
  }

  async function handleOpenConfig() {
    const path = configState.data?.config_path
    if (!path) {
      toast.error("Config path is not loaded yet")
      return
    }
    try {
      await openConfigFile(path)
    } catch (error) {
      toast.error("Could not open the config file", {
        description: errorText(error),
      })
    }
  }

  if (!hasCliBridge) {
    return (
      <div className="flex h-full flex-col gap-5 overflow-hidden px-8 py-7">
        <PageHeader
          title="Settings"
          subtitle="Preferences are stored in the komiku-cli config file"
        />
        <BrowserOnlyNotice />
        <EmptyState
          title="Settings need the desktop app"
          hint="Run pnpm desktop to read and write the real komiku-cli config."
          icon={Settings2}
        />
      </div>
    )
  }

  if (configState.loading) {
    return (
      <div className="flex h-full flex-col gap-5 overflow-hidden px-8 py-7">
        <PageHeader
          title="Settings"
          subtitle="Loading the komiku-cli config…"
        />
        <LoadingState label="Reading komiku-cli config…" />
      </div>
    )
  }

  if (configState.error) {
    return (
      <div className="flex h-full flex-col gap-5 overflow-hidden px-8 py-7">
        <PageHeader title="Settings" subtitle="komiku-cli config --json" />
        <ErrorState
          message={configState.error}
          onRetry={configState.reload}
        />
      </div>
    )
  }

  if (!config) {
    return (
      <div className="flex h-full flex-col gap-5 overflow-hidden px-8 py-7">
        <PageHeader title="Settings" subtitle="komiku-cli config --json" />
        <EmptyState
          title="No config returned"
          hint="komiku-cli config --json returned an empty response."
        />
      </div>
    )
  }

  const configDirty =
    baseline !== null &&
    (root !== baseline.output_root ||
      delay.trim() !== baseline.image_delay ||
      preset !== baseline.preset)

  const libraryFacts: { label: string; value: string }[] = [
    {
      label: "Series",
      value: library.data
        ? String((library.data.series ?? []).length)
        : library.loading
          ? "…"
          : "—",
    },
    {
      label: "Chapters",
      value: library.data
        ? library.data.total_chapters.toLocaleString("en-US")
        : library.loading
          ? "…"
          : "—",
    },
    {
      label: "CBZ archives",
      value: library.data
        ? library.data.total_cbz.toLocaleString("en-US")
        : library.loading
          ? "…"
          : "—",
    },
    {
      label: "On disk",
      value: library.data
        ? humanBytes(library.data.total_bytes)
        : library.loading
          ? "…"
          : "—",
    },
  ]

  return (
    <div className="flex h-full flex-col gap-5 overflow-hidden px-8 py-7">
      <PageHeader
        title="Settings"
        subtitle={
          <>
            Preferences are stored in{" "}
            <span className="font-mono text-[11px]">{config.config_path}</span>
          </>
        }
      >
        {saved ? (
          <span className="text-[11px] text-success">Saved</span>
        ) : (
          configDirty && (
            <span className="text-[11px] text-warning">Unsaved changes</span>
          )
        )}
        <Button
          variant="outline"
          size="sm"
          className="h-9 px-3.5 text-[13px]"
          onClick={() => {
            void handleReset()
          }}
          disabled={busy}
        >
          Reset defaults
        </Button>
        <Button
          variant="default"
          size="sm"
          className="h-9 px-3.5 text-[13px]"
          onClick={() => {
            void handleSave()
          }}
          disabled={busy}
        >
          <Check className="size-[13px]" strokeWidth={1.8} />
          Save changes
        </Button>
      </PageHeader>

      <div className="grid min-h-0 flex-1 grid-cols-2 content-start gap-4 overflow-y-auto">
        {/* Left column */}
        <div className="flex flex-col gap-4">
          <SettingsCard
            title="Library"
            description="Where downloads and CBZ archives live"
          >
            <div className="flex flex-col gap-1.5">
              <span className="text-[12px] font-medium text-muted-foreground">
                Download root
              </span>
              <div className="flex h-9 items-center gap-2.5 rounded-[6px] border border-border bg-card px-2.5 transition-colors focus-within:border-primary">
                <button
                  type="button"
                  onClick={() => {
                    void pickFolder()
                  }}
                  disabled={picking}
                  title="Browse for a folder"
                  aria-label="Browse for a folder"
                  className="shrink-0 text-dim transition-colors hover:text-foreground disabled:opacity-50"
                >
                  <Folder className="size-[13px]" strokeWidth={1.8} />
                </button>
                <Input
                  value={root}
                  onChange={(event) => setRoot(event.target.value)}
                  spellCheck={false}
                  aria-label="Download root"
                  className="h-auto flex-1 border-0 bg-transparent p-0 font-mono text-[12px] focus-visible:ring-0"
                />
                <button
                  type="button"
                  onClick={() => {
                    void pickFolder()
                  }}
                  disabled={picking}
                  className="shrink-0 rounded-[5px] bg-muted px-2 py-1 text-[10px] text-muted-foreground transition-colors hover:text-foreground disabled:opacity-50"
                >
                  {picking ? "Opening…" : "Browse…"}
                </button>
              </div>
              <div className="flex items-center gap-1.5">
                <p className="flex-1 text-[10px] leading-relaxed text-dim">
                  Choose any folder on this machine. Paths are stored as-is.
                </p>
                <button
                  type="button"
                  onClick={() => setRoot(".")}
                  className="shrink-0 text-[10px] text-dim underline decoration-border underline-offset-2 transition-colors hover:text-muted-foreground"
                >
                  Use .
                </button>
              </div>
            </div>

            <div className="flex flex-col gap-1.5">
              <span className="text-[12px] font-medium text-muted-foreground">
                Default CBZ preset
              </span>
              <div className="flex w-full gap-[3px] rounded-[7px] border border-border bg-muted p-[3px]">
                {presetOptions.map((option) => (
                  <button
                    key={option.id}
                    type="button"
                    onClick={() => setPreset(option.id)}
                    className={cn(
                      "flex-1 rounded-[5px] border py-[7px] text-[12px] font-medium transition-colors",
                      preset === option.id
                        ? "border-border bg-card text-primary"
                        : "border-transparent text-dim hover:text-muted-foreground"
                    )}
                  >
                    {option.label}
                  </button>
                ))}
              </div>
            </div>

            <div className="flex items-center justify-between gap-3">
              <div className="flex min-w-0 flex-col gap-0.5">
                <span className="text-[12px] font-medium">Request delay</span>
                <span className="text-[10px] text-dim">
                  Pacing between image requests — raise it if Komiku throttles
                </span>
              </div>
              <input
                type="text"
                spellCheck={false}
                aria-label="Request delay"
                placeholder={DEFAULT_DELAY}
                value={delay}
                onChange={(event) => setDelay(event.target.value)}
                onBlur={commitDelay}
                className="h-[30px] w-[88px] shrink-0 rounded-[6px] border border-border bg-card px-2 text-center font-mono text-[12px] text-foreground outline-none transition-colors focus-visible:border-primary"
              />
            </div>
          </SettingsCard>

          <SettingsCard
            title="Detected library"
            description="Read-only facts reported by komiku-cli library"
          >
            <div className="flex flex-col gap-2">
              {libraryFacts.map((fact) => (
                <FactRow key={fact.label} {...fact} />
              ))}
            </div>

            {library.error && (
              <span className="break-words font-mono text-[10px] leading-relaxed text-danger">
                {library.error}
              </span>
            )}
          </SettingsCard>
        </div>

        {/* Right column */}
        <div className="flex flex-col gap-4">
          <SettingsCard
            title="Config file"
            description="komiku-cli config reads and writes this file"
          >
            <div className="flex h-[34px] items-center gap-2 rounded-[6px] border border-border bg-card px-2.5">
              <FileText
                className="size-3 shrink-0 text-dim"
                strokeWidth={1.8}
              />
              <span
                className="truncate font-mono text-[11px]"
                title={config.config_path}
              >
                {config.config_path}
              </span>
            </div>

            <div className="flex flex-col gap-2.5">
              <div className="flex flex-col gap-1">
                <div className="flex items-center gap-2.5">
                  <span className="font-mono text-[11px] text-foreground">
                    output_root
                  </span>
                  <div className="flex-1" />
                  <span className="shrink-0 rounded-full bg-muted px-1.5 py-0.5 font-mono text-[10px] text-dim">
                    string
                  </span>
                </div>
                <div className="flex h-[30px] items-center gap-2 rounded-[6px] border border-border bg-card px-2 transition-colors focus-within:border-primary">
                  <button
                    type="button"
                    onClick={() => {
                      void pickFolder()
                    }}
                    disabled={picking}
                    title="Browse for a folder"
                    aria-label="Browse for the download root"
                    className="shrink-0 text-dim transition-colors hover:text-foreground disabled:opacity-50"
                  >
                    <Folder className="size-3" strokeWidth={1.8} />
                  </button>
                  <input
                    value={root}
                    onChange={(event) => setRoot(event.target.value)}
                    spellCheck={false}
                    aria-label="output_root"
                    className="h-full min-w-0 flex-1 border-0 bg-transparent font-mono text-[11px] text-foreground outline-none"
                  />
                </div>
              </div>

              <div className="flex flex-col gap-1">
                <div className="flex items-center gap-2.5">
                  <span className="font-mono text-[11px] text-foreground">
                    image_delay
                  </span>
                  <div className="flex-1" />
                  <span className="shrink-0 rounded-full bg-muted px-1.5 py-0.5 font-mono text-[10px] text-dim">
                    duration
                  </span>
                </div>
                <input
                  type="text"
                  spellCheck={false}
                  aria-label="image_delay"
                  placeholder={DEFAULT_DELAY}
                  value={delay}
                  onChange={(event) => setDelay(event.target.value)}
                  onBlur={commitDelay}
                  className="h-[30px] w-full rounded-[6px] border border-border bg-card px-2 font-mono text-[11px] text-foreground outline-none transition-colors focus-visible:border-primary"
                />
              </div>

              <div className="flex flex-col gap-1">
                <div className="flex items-center gap-2.5">
                  <span className="font-mono text-[11px] text-foreground">
                    preset
                  </span>
                  <div className="flex-1" />
                  <span className="shrink-0 rounded-full bg-muted px-1.5 py-0.5 font-mono text-[10px] text-dim">
                    enum
                  </span>
                </div>
                <div className="flex w-full gap-[3px] rounded-[7px] border border-border bg-muted p-[3px]">
                  {presetOptions.map((option) => (
                    <button
                      key={option.id}
                      type="button"
                      onClick={() => setPreset(option.id)}
                      className={cn(
                        "flex-1 rounded-[5px] border py-[5px] font-mono text-[11px] font-medium transition-colors",
                        preset === option.id
                          ? "border-border bg-card text-primary"
                          : "border-transparent text-dim hover:text-muted-foreground"
                      )}
                    >
                      {option.id}
                    </button>
                  ))}
                </div>
              </div>
            </div>

            <div className="flex gap-2">
              <Button
                variant="outline"
                className="h-8 flex-1 text-[12px]"
                onClick={() => {
                  void handleOpenConfig()
                }}
              >
                <ExternalLink className="size-3" strokeWidth={1.8} />
                Open config file
              </Button>
              <Button
                variant="ghost"
                size="icon"
                className="size-8 shrink-0"
                title="Copy config path"
                aria-label="Copy config path"
                onClick={() => {
                  void copyConfigPath()
                }}
              >
                <Copy className="size-3" strokeWidth={1.8} />
              </Button>
            </div>
          </SettingsCard>

          <div className="flex flex-col gap-3 rounded-[8px] border border-border-subtle bg-surface p-4">
            <div className="flex items-center gap-2.5">
              <div className="flex size-7 shrink-0 items-center justify-center rounded-[7px] bg-primary text-[14px] font-bold text-primary-foreground">
                K
              </div>
              <div className="flex flex-col gap-0.5">
                <span className="text-[13px] font-semibold">komiku-cli</span>
                <span className="font-mono text-[10px] text-dim">
                  Tauri desktop shell
                </span>
              </div>
            </div>

            <div className="flex flex-col gap-2">
              <div className="flex items-center">
                <span className="text-[12px] text-dim">Source</span>
                <div className="flex-1" />
                <span className="font-mono text-[11px] text-muted-foreground">
                  komiku.org
                </span>
              </div>
              <div className="flex items-center">
                <span className="text-[12px] text-dim">License</span>
                <div className="flex-1" />
                <span className="font-mono text-[11px] text-muted-foreground">
                  Independent client
                </span>
              </div>
              <div className="flex items-center">
                <span className="text-[12px] text-dim">Build</span>
                <div className="flex-1" />
                {binaryState.error ? (
                  <span
                    className="max-w-[190px] truncate font-mono text-[11px] text-danger"
                    title={binaryState.error}
                  >
                    {binaryState.error}
                  </span>
                ) : (
                  <span
                    className="max-w-[190px] truncate font-mono text-[11px] text-muted-foreground"
                    title={binaryState.data ?? undefined}
                  >
                    {binaryState.data ?? "…"}
                  </span>
                )}
              </div>
            </div>

            <p className="text-[10px] leading-relaxed text-dim">
              Not affiliated with Komiku. Download only content you are
              authorized to access.
            </p>
          </div>
        </div>
      </div>

      <div className="flex shrink-0 items-center gap-2.5 rounded-[7px] bg-muted px-4 py-3">
        <Info className="size-[13px] shrink-0 text-dim" strokeWidth={1.8} />
        <span className="truncate font-mono text-[11px] text-muted-foreground">
          CLI flags override saved settings for a single run: komiku-cli --out …
          --preset raw
        </span>
        <div className="flex-1" />
        <span className="shrink-0 font-mono text-[11px] text-dim">
          {versionState.data || "komiku-cli"}
        </span>
      </div>
    </div>
  )
}
