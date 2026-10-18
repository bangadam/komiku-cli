import { invoke } from "@tauri-apps/api/core"
import { listen, type UnlistenFn } from "@tauri-apps/api/event"

export interface CliOutput {
  ok: boolean
  code: number | null
  stdout: string
  stderr: string
}

export interface CliLine {
  run_id: string
  stream: "stdout" | "stderr"
  line: string
}

export const isTauri =
  typeof window !== "undefined" && "__TAURI_INTERNALS__" in window

/**
 * `pnpm dev` serves the same CLI operations from a Vite middleware, so the
 * browser build renders real data instead of a "desktop only" notice. The
 * bridge is development-only and never shipped in a bundle.
 */
export const hasCliBridge = isTauri || import.meta.env.DEV

export class CliError extends Error {
  stderr: string
  code: number | null

  constructor(message: string, stderr = "", code: number | null = null) {
    super(message)
    this.name = "CliError"
    this.stderr = stderr
    this.code = code
  }
}

export interface CliContext {
  binary: string
  cwd: string
}

async function bridgeFetch<T>(path: string, body: unknown, signal?: AbortSignal): Promise<T> {
  const response = await fetch(path, {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify(body),
    signal,
  })
  if (!response.ok) throw new CliError(`CLI bridge failed: ${response.status}`)
  return (await response.json()) as T
}

export async function cliContext(): Promise<CliContext> {
  if (isTauri) return invoke<CliContext>("cli_context")
  if (import.meta.env.DEV) {
    const response = await fetch("/__cli/context")
    if (!response.ok) throw new CliError("CLI bridge is not running")
    return (await response.json()) as CliContext
  }
  return { binary: "komiku-cli", cwd: "." }
}

export async function cliBinary(): Promise<string> {
  const context = await cliContext()
  return context.binary
}

/** The binary's own version, used by the Settings footer. */
export async function cliVersion(): Promise<string> {
  const result = await runCli(["--version"])
  if (!result.ok) return ""
  // Cobra prints "komiku-cli version v1.2.3"; keep just the version token.
  const text = result.stdout.trim()
  const match = /v?\d[^\s]*/.exec(text)
  return match ? match[0] : text
}

export async function runCli(args: string[]): Promise<CliOutput> {
  if (isTauri) return invoke<CliOutput>("run_cli", { args })
  if (import.meta.env.DEV) {
    return bridgeFetch<CliOutput>("/__cli/run", { args })
  }
  throw new CliError(
    "The CLI bridge is not available in this build. Use `pnpm desktop`."
  )
}

export async function runCliJson<T>(args: string[]): Promise<T> {
  const result = await runCli(args)
  if (!result.ok) {
    throw new CliError(
      firstErrorLine(result.stderr) || `komiku-cli ${args[0]} failed`,
      result.stderr,
      result.code
    )
  }
  const text = result.stdout.trim()
  if (!text) {
    throw new CliError(`komiku-cli ${args[0]} returned no output`)
  }
  try {
    return JSON.parse(text) as T
  } catch {
    throw new CliError(`Unexpected output from komiku-cli ${args[0]}: ${text.slice(0, 200)}`)
  }
}

export async function openConfigFile(path: string): Promise<void> {
  if (!isTauri) {
    throw new CliError(
      "Opening the config file only works in the desktop app. Run `pnpm desktop` instead of `pnpm dev`."
    )
  }
  await invoke("open_config_file", { path })
}

/** Reveal a folder in the OS file manager (desktop or the dev bridge). */
export async function openFolder(path: string): Promise<void> {
  if (isTauri) {
    await invoke("open_folder", { path })
    return
  }
  if (import.meta.env.DEV) {
    const response = await fetch("/__open-folder", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ path }),
    })
    if (!response.ok) {
      const payload = (await response.json().catch(() => ({}))) as {
        error?: string
      }
      throw new CliError(payload.error ?? "could not open the folder")
    }
    return
  }
  throw new CliError("Opening a folder only works in the desktop app.")
}

export function firstErrorLine(stderr: string): string {
  const line = stderr
    .split("\n")
    .map((value) => value.trim())
    .find((value) => value.length > 0)
  return line ?? ""
}

export interface StreamHandle {
  done: Promise<CliOutput>
  cancel: () => Promise<void>
}

async function runCliStreamDev(
  runId: string,
  args: string[],
  onLine: (line: CliLine) => void
): Promise<StreamHandle> {
  const controller = new AbortController()
  const response = await fetch("/__cli/stream", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ runId, args }),
    signal: controller.signal,
  })
  if (!response.ok || !response.body) {
    controller.abort()
    throw new CliError(`CLI stream bridge failed: ${response.status}`)
  }

  const reader = response.body.getReader()
  const decoder = new TextDecoder()
  let buffer = ""
  let settled = false
  let cancelled = false
  let cancelTimer: ReturnType<typeof setTimeout> | undefined
  let stdout = ""
  let stderr = ""

  const done = (async (): Promise<CliOutput> => {
    try {
      for (;;) {
        const { done: finished, value } = await reader.read()
        buffer += finished ? decoder.decode() : decoder.decode(value, { stream: true })
        let boundary = buffer.indexOf("\n\n")
        while (boundary >= 0) {
          const frame = buffer.slice(0, boundary)
          buffer = buffer.slice(boundary + 2)
          boundary = buffer.indexOf("\n\n")
          const lines = frame.split("\n")
          const eventName =
            lines.find((line) => line.startsWith("event:"))?.slice(6).trim() ?? "message"
          const data = lines.filter((line) => line.startsWith("data:"))
            .map((line) => line.slice(5).trim()).join("\n")
          if (!data) continue
          if (eventName === "done") return JSON.parse(data) as CliOutput
          if (eventName === "error") throw new CliError(data)
          const line = JSON.parse(data) as CliLine
          if (line.run_id !== runId) continue
          if (line.stream === "stdout") stdout += `${line.line}\n`
          else stderr += `${line.line}\n`
          onLine(line)
        }
        if (finished) throw new CliError("CLI stream ended without a result")
      }
    } catch (error) {
      if (cancelled) return { ok: false, code: null, stdout, stderr: `${stderr}CLI run cancelled\n` }
      throw error
    } finally {
      settled = true
      if (cancelTimer) clearTimeout(cancelTimer)
      // Disconnect also tells the dev bridge to reap only this request's child.
      controller.abort()
      await reader.cancel().catch(() => undefined)
      reader.releaseLock()
    }
  })()
  // Consumers can attach after this function resolves, even on an instant spawn
  // failure. Mark it handled now without changing the rejecting promise returned.
  void done.catch(() => undefined)

  return {
    done,
    cancel: async () => {
      if (settled || cancelled) return
      cancelled = true
      // Keep the SSE response open to receive final output. Fall back to abort
      // if cancellation/transport is broken so `done` always settles.
      cancelTimer = setTimeout(() => controller.abort(), 3000)
      await bridgeFetch<boolean>("/__cli/cancel", { runId }, controller.signal)
        .catch(() => controller.abort())
    },
  }
}

export async function runCliStream(
  args: string[],
  onLine: (line: CliLine) => void
): Promise<StreamHandle> {
  const runId = `run-${Date.now()}-${Math.random().toString(36).slice(2, 8)}`

  if (import.meta.env.DEV && !isTauri) {
    return runCliStreamDev(runId, args, onLine)
  }

  if (!isTauri) {
    throw new CliError(
      "The CLI bridge is not available in this build. Use `pnpm desktop`."
    )
  }

  // Await directly: a failed subscription must reject, not leave the caller
  // waiting forever, and the CLI must not start without its listener installed.
  let unlisten: UnlistenFn | undefined = await listen<CliLine>("cli://line", (event) => {
    if (event.payload.run_id !== runId) return
    onLine(event.payload)
  })
  let settled = false
  const cleanup = () => {
    const stop = unlisten
    unlisten = undefined
    stop?.()
  }
  const done = (async () => {
    try {
      return await invoke<CliOutput>("run_cli_stream", { runId, args })
    } finally {
      settled = true
      cleanup()
    }
  })()
  void done.catch(() => undefined)

  return {
    done,
    cancel: async () => {
      if (!settled) await invoke<boolean>("cancel_cli", { runId })
    },
  }
}
