import { spawn, type ChildProcess } from "node:child_process"
import fs from "node:fs/promises"
import { existsSync, statSync } from "node:fs"
import type { IncomingMessage, ServerResponse } from "node:http"
import { isIP } from "node:net"
import path from "node:path"
import type { Plugin, ViteDevServer } from "vite"

/** Development-only, loopback-only bridge. Never installed in preview/build. */
const MAX_BODY = 128 * 1024
const MAX_OUTPUT = 32 * 1024 * 1024
const MAX_PAGE_BYTES = 24 * 1024 * 1024
const MAX_COVER_BYTES = 8 * 1024 * 1024

interface RunResult {
  ok: boolean
  code: number | null
  stdout: string
  stderr: string
}

class BridgeError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

function safeArgs(value: unknown): string[] {
  if (!Array.isArray(value) || value.length > 128) {
    throw new BridgeError(400, "args must be an array of at most 128 strings")
  }
  let total = 0
  for (const arg of value) {
    if (typeof arg !== "string" || arg.includes("\0") || Buffer.byteLength(arg) > 8192) {
      throw new BridgeError(400, "invalid CLI argument (8 KiB maximum per string)")
    }
    total += Buffer.byteLength(arg)
  }
  if (total > 64 * 1024) throw new BridgeError(400, "CLI arguments exceed 64 KiB")
  return value as string[]
}

function safeRunId(value: unknown): string {
  if (typeof value !== "string" || !/^[A-Za-z0-9_-]{1,128}$/.test(value)) {
    throw new BridgeError(400, "invalid runId")
  }
  return value
}

async function readBody(req: IncomingMessage): Promise<Record<string, unknown>> {
  if (req.headers["content-type"]?.split(";", 1)[0].trim() !== "application/json") {
    throw new BridgeError(415, "expected application/json")
  }
  const chunks: Buffer[] = []
  let size = 0
  for await (const chunk of req) {
    const bytes = Buffer.isBuffer(chunk) ? chunk : Buffer.from(chunk)
    size += bytes.length
    if (size > MAX_BODY) throw new BridgeError(413, "request body too large")
    chunks.push(bytes)
  }
  try {
    const payload: unknown = JSON.parse(Buffer.concat(chunks).toString("utf8"))
    if (typeof payload === "object" && payload !== null && !Array.isArray(payload)) {
      return payload as Record<string, unknown>
    }
  } catch { /* report malformed JSON as a client error */ }
  throw new BridgeError(400, "expected a JSON object")
}

function sendJSON(res: ServerResponse, status: number, value: unknown) {
  if (res.destroyed || res.writableEnded || res.headersSent) return
  res.statusCode = status
  res.setHeader("content-type", "application/json; charset=utf-8")
  res.setHeader("cache-control", "no-store")
  res.setHeader("x-content-type-options", "nosniff")
  res.end(JSON.stringify(value))
}

function isLoopback(host: string): boolean {
  const value = host.replace(/^\[|\]$/g, "").toLowerCase()
  return value === "localhost" || value === "::1" || value === "::ffff:127.0.0.1"
    || (isIP(value) === 4 && value.startsWith("127."))
}

function guardRequest(req: IncomingMessage, https: boolean): void {
  const host = req.headers.host
  if (!host || /[\s/@\\?#]/.test(host)) throw new BridgeError(403, "invalid bridge host")
  const origin = new URL(`${https ? "https" : "http"}://${host}`).origin
  const url = new URL(origin)
  if (!isLoopback(url.hostname)
      || (req.socket.remoteAddress && !isLoopback(req.socket.remoteAddress))
      || (req.socket.localPort && Number(url.port || (https ? 443 : 80)) !== req.socket.localPort)) {
    throw new BridgeError(403, "CLI bridge is loopback-only")
  }
  // Check Origin even for GET. Also reject cross-site no-cors embeds and form
  // submissions, which may omit Origin but still carry Sec-Fetch-Site.
  if ((req.headers.origin !== undefined && req.headers.origin !== origin)
      || (req.headers["sec-fetch-site"] !== undefined
        && !["same-origin", "none"].includes(String(req.headers["sec-fetch-site"])))) {
    throw new BridgeError(403, "cross-origin CLI bridge request denied")
  }
}

function resolveBinary(root: string): string {
  const fromEnv = process.env.KOMIKU_CLI_BIN
  if (fromEnv && existsSync(fromEnv) && statSync(fromEnv).isFile()) return path.resolve(fromEnv)
  for (const candidate of [path.resolve(root, "..", "komiku-cli"), path.resolve(root, "komiku-cli")]) {
    if (existsSync(candidate) && statSync(candidate).isFile()) return candidate
  }
  return "komiku-cli"
}

function resolveCwd(root: string): string {
  const fromEnv = process.env.KOMIKU_CLI_CWD
  if (fromEnv && existsSync(fromEnv) && statSync(fromEnv).isDirectory()) return path.resolve(fromEnv)
  return path.resolve(root, "..")
}

function isWithin(root: string, target: string): boolean {
  const relative = path.relative(root, target)
  return relative !== ".." && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative)
}

async function confinedPath(root: string, cwd: string, value: unknown): Promise<string | null> {
  if (typeof value !== "string" || !value || value.length > 4096 || value.includes("\0")) return null
  const target = await fs.realpath(path.resolve(cwd, value)).catch(() => null)
  return target && isWithin(root, target) ? target : null
}

function allowedImagePath(root: string, target: string, kind: "page" | "cover"): boolean {
  if (!isWithin(root, target)) return false
  const parts = path.relative(root, target).split(path.sep)
  if (!parts[0] || parts[0].startsWith(".")) return false
  const leaf = parts.at(-1) ?? ""
  if (kind === "cover") return parts.length === 2 && /^cover\.(jpe?g|png|webp)$/i.test(leaf)
  if (!/^\d+(?:-[0-9a-f]{8,64})?\.(jpe?g|png|webp)$/i.test(leaf) || /^0+(?:-|\.)/.test(leaf)) return false
  const chapter = /^chapter-\d+(?:[.-]\d+)?(?:(?:-url-[0-9a-fA-F]{8,64})?-raw-[A-Za-z0-9._-]+)?$/
  return (parts.length === 3 && chapter.test(parts[1]))
    || (parts.length === 4 && (parts[1] === ".reader" || /^vol-\d+$/.test(parts[1]))
      && chapter.test(parts[2]))
}

function imageMime(bytes: Buffer): string | null {
  if (bytes.length >= 3 && bytes[0] === 0xff && bytes[1] === 0xd8 && bytes[2] === 0xff) return "image/jpeg"
  if (bytes.subarray(0, 8).equals(Buffer.from("89504e470d0a1a0a", "hex"))) return "image/png"
  if (bytes.length >= 12 && bytes.toString("ascii", 0, 4) === "RIFF"
      && bytes.toString("ascii", 8, 12) === "WEBP") return "image/webp"
  return null
}

async function readImage(root: string, cwd: string, value: unknown, kind: "page" | "cover") {
  const target = await confinedPath(root, cwd, value)
  if (!target || !allowedImagePath(root, target, kind)) return null
  const limit = kind === "page" ? MAX_PAGE_BYTES : MAX_COVER_BYTES
  const before = await fs.stat(target).catch(() => null)
  if (!before?.isFile() || !before.size || before.size > limit) return null
  const file = await fs.open(target, "r")
  try {
    const meta = await file.stat()
    if (!meta.isFile() || !meta.size || meta.size > limit
        || await fs.realpath(target).catch(() => null) !== target) return null
    // Read no more than the stat size plus one byte, even if the file grows.
    const buffer = Buffer.alloc(meta.size + 1)
    let size = 0
    while (size < buffer.length) {
      const { bytesRead } = await file.read(buffer, size, buffer.length - size, null)
      if (!bytesRead) break
      size += bytesRead
    }
    if (size > meta.size || size > limit) return null
    const bytes = buffer.subarray(0, size)
    const mime = imageMime(bytes)
    return mime ? { bytes, mime } : null
  } finally {
    await file.close()
  }
}

interface OwnedRun {
  child: ChildProcess
  done: Promise<RunResult>
  exited: Promise<void>
  cancel: () => boolean
}

interface LibraryConfig {
  output_root: string
  config_path: string
}

async function configStamp(filename: string): Promise<string> {
  const meta = await fs.stat(filename).catch(() => null)
  return meta ? `${meta.mtimeMs}:${meta.ctimeMs}:${meta.size}` : "missing"
}

export function cliBridge(): Plugin {
  let root = process.cwd()
  let closing = false
  const children = new Set<OwnedRun>()
  const running = new Map<string, OwnedRun>()
  let configCache: { binary: string; cwd: string; root: string; filename: string; stamp: string } | undefined
  let configPending: Promise<void> | undefined

  function startProcess(binary: string, cwd: string, args: string[], options: {
    runId?: string
    res?: ServerResponse
    onLine?: (stream: "stdout" | "stderr", line: string) => void
    maxOutput?: number
  } = {}): OwnedRun {
    if (closing || options.res?.destroyed) throw new BridgeError(503, "CLI bridge is closing")
    if (options.runId && running.has(options.runId)) throw new BridgeError(409, "runId is already active")
    const child = spawn(binary, args, { cwd, shell: false, stdio: ["ignore", "pipe", "pipe"] })
    let stdout = ""
    let stderr = ""
    let failure = ""
    let total = 0
    let settled = false
    let closed = false
    let cancelled = false
    let killTimer: ReturnType<typeof setTimeout> | undefined
    let resolveDone!: (result: RunResult) => void
    let resolveExit!: () => void
    const done = new Promise<RunResult>((resolve) => { resolveDone = resolve })
    const exited = new Promise<void>((resolve) => { resolveExit = resolve })
    const terminate = () => {
      if (closed || child.exitCode !== null || child.signalCode !== null || killTimer) return
      child.kill("SIGTERM")
      killTimer = setTimeout(() => {
        if (!closed && child.exitCode === null && child.signalCode === null) child.kill("SIGKILL")
      }, 1000)
      killTimer.unref()
    }
    const run: OwnedRun = {
      child,
      done,
      exited,
      cancel: () => {
        if (settled || cancelled || child.exitCode !== null || child.signalCode !== null) return false
        cancelled = true
        terminate()
        return true
      },
    }
    children.add(run)
    if (options.runId) running.set(options.runId, run)
    const disconnected = () => { if (!settled) run.cancel() }
    options.res?.once("close", disconnected)
    const emit = (stream: "stdout" | "stderr", line: string) => {
      if (!options.res?.destroyed) options.onLine?.(stream, line)
    }
    const pending = { stdout: "", stderr: "" }
    const finish = (code: number | null) => {
      if (settled) return
      settled = true
      options.res?.off("close", disconnected)
      for (const stream of ["stdout", "stderr"] as const) {
        if (pending[stream]) emit(stream, pending[stream])
      }
      const message = failure || (cancelled ? "CLI run cancelled" : "")
      if (message) stderr += `${stderr && !stderr.endsWith("\n") ? "\n" : ""}${message}\n`
      resolveDone({ ok: !message && code === 0, code: message ? null : code, stdout, stderr })
    }
    const fail = (error: Error) => {
      if (settled) return
      failure = error.message
      terminate()
      finish(null)
    }
    const feed = (stream: "stdout" | "stderr") => (text: string) => {
      if (settled) return
      total += Buffer.byteLength(text)
      if (total > (options.maxOutput ?? MAX_OUTPUT)) {
        fail(new Error("CLI output limit exceeded"))
        return
      }
      if (stream === "stdout") stdout += text
      else stderr += text
      pending[stream] += text
      let boundary: number
      while ((boundary = pending[stream].indexOf("\n")) >= 0) {
        const line = pending[stream].slice(0, boundary).replace(/\r$/, "")
        pending[stream] = pending[stream].slice(boundary + 1)
        emit(stream, line)
      }
    }
    // String decoding is incremental too: UTF-8 characters can span chunks.
    child.stdout!.setEncoding("utf8").on("data", feed("stdout")).on("error", fail)
    child.stderr!.setEncoding("utf8").on("data", feed("stderr")).on("error", fail)
    child.on("error", fail)
    child.once("close", (code) => {
      closed = true
      if (killTimer) clearTimeout(killTimer)
      finish(code)
      children.delete(run)
      if (options.runId && running.get(options.runId) === run) running.delete(options.runId)
      resolveExit()
    })
    return run
  }

  async function libraryRoot(binary: string, cwd: string, res: ServerResponse): Promise<string> {
    if (configPending) await configPending
    if (!configCache || configCache.binary !== binary || configCache.cwd !== cwd
        || configCache.stamp !== await configStamp(configCache.filename)) {
      // Share the first config lookup across a chapter's concurrent page loads.
      if (!configPending) {
        configPending = (async () => {
          const run = startProcess(binary, cwd, ["config", "--json"], { res, maxOutput: MAX_BODY })
          const timeout = setTimeout(() => run.cancel(), 5000)
          try {
            const result = await run.done
            if (!result.ok) throw new Error("cannot resolve configured library")
            const config = JSON.parse(result.stdout) as LibraryConfig
            if (typeof config.output_root !== "string" || !config.output_root.trim()
                || typeof config.config_path !== "string" || !config.config_path) {
              throw new Error("CLI config has no library root/config path")
            }
            const filename = path.resolve(cwd, config.config_path)
            configCache = { binary, cwd, root: path.resolve(cwd, config.output_root),
              filename, stamp: await configStamp(filename) }
          } finally { clearTimeout(timeout) }
        })().finally(() => { configPending = undefined })
      }
      await configPending
    }
    if (!configCache) throw new Error("cannot resolve configured library")
    const canonical = await fs.realpath(configCache.root)
    if (!(await fs.stat(canonical)).isDirectory()) throw new Error("library is not a directory")
    return canonical
  }

  async function closeChildren() {
    closing = true
    const active = [...children]
    for (const run of active) run.cancel()
    await Promise.all(active.map((run) => run.exited))
  }

  function install(server: ViteDevServer) {
    server.middlewares.use(async (req, res, next) => {
      const url = new URL(req.url ?? "/", "http://localhost")
      if (!["/__cli/context", "/__cli/run", "/__cli/stream", "/__cli/cancel",
        "/__open-folder", "/__page", "/__cover"].includes(url.pathname)) {
        next()
        return
      }
      try {
        guardRequest(req, Boolean(server.config.server.https))
        const binary = resolveBinary(root)
        const cwd = resolveCwd(root)
        if (url.pathname === "/__cli/context" && req.method === "GET") {
          sendJSON(res, 200, { binary, cwd })
          return
        }
        if (url.pathname === "/__cli/run" && req.method === "POST") {
          const payload = await readBody(req)
          const run = startProcess(binary, cwd, safeArgs(payload.args), { res })
          sendJSON(res, 200, await run.done)
          return
        }
        if (url.pathname === "/__cli/stream" && req.method === "POST") {
          const payload = await readBody(req)
          const runId = safeRunId(payload.runId)
          const args = safeArgs(payload.args)
          const run = startProcess(binary, cwd, args, {
            runId, res,
            onLine: (stream, line) => {
              if (!res.destroyed && !res.writableEnded) {
                res.write(`data: ${JSON.stringify({ run_id: runId, stream, line })}\n\n`)
              }
            },
          })
          res.statusCode = 200
          res.setHeader("content-type", "text/event-stream; charset=utf-8")
          res.setHeader("cache-control", "no-store")
          res.setHeader("x-content-type-options", "nosniff")
          res.setHeader("connection", "keep-alive")
          res.flushHeaders()
          const result = await run.done
          if (!res.destroyed && !res.writableEnded) {
            res.end(`event: done\ndata: ${JSON.stringify(result)}\n\n`)
          }
          return
        }
        if (url.pathname === "/__cli/cancel" && req.method === "POST") {
          const payload = await readBody(req)
          sendJSON(res, 200, running.get(safeRunId(payload.runId))?.cancel() ?? false)
          return
        }
        if (url.pathname === "/__open-folder" && req.method === "POST") {
          const payload = await readBody(req)
          const library = await libraryRoot(binary, cwd, res)
          const target = await confinedPath(library, cwd, payload.path)
          if (!target || !(await fs.stat(target)).isDirectory()) {
            throw new BridgeError(400, "folder is not inside the configured library")
          }
          const opener = process.platform === "darwin" ? "open"
            : process.platform === "win32" ? "explorer" : "xdg-open"
          const result = await startProcess(opener, cwd, [target], { res }).done
          sendJSON(res, result.ok ? 200 : 500, { ok: result.ok, error: result.ok ? undefined : result.stderr })
          return
        }
        if ((url.pathname === "/__page" || url.pathname === "/__cover") && req.method === "GET") {
          const library = await libraryRoot(binary, cwd, res)
          const image = await readImage(library, cwd, url.searchParams.get("path"),
            url.pathname === "/__page" ? "page" : "cover")
          if (!image) {
            sendJSON(res, 404, { error: "image not found in configured library" })
            return
          }
          if (!res.destroyed && !res.writableEnded) {
            res.statusCode = 200
            res.setHeader("content-type", image.mime)
            res.setHeader("x-content-type-options", "nosniff")
            res.setHeader("cache-control", "no-store")
            res.end(image.bytes)
          }
          return
        }
        sendJSON(res, 405, { error: "method not allowed" })
      } catch (error) {
        if (res.headersSent) {
          if (!res.destroyed && !res.writableEnded) res.destroy()
          return
        }
        sendJSON(res, error instanceof BridgeError ? error.status : 500, {
          ok: false, code: null, stdout: "",
          stderr: error instanceof Error ? error.message : String(error),
          error: error instanceof Error ? error.message : String(error),
        })
      }
    })
  }

  return {
    name: "komiku-cli-bridge",
    apply: "serve",
    config(config) {
      const host = config.server?.host
      if (host !== undefined && (typeof host !== "string" || !isLoopback(host))) {
        throw new Error("The CLI dev bridge requires a loopback Vite host")
      }
      return { server: { host: host ?? "127.0.0.1", cors: false } }
    },
    configResolved(config) { root = config.root },
    configureServer(server) {
      closing = false
      install(server)
      server.httpServer?.once("close", () => { void closeChildren() })
    },
    // Runs during server.close(), before waiting for active SSE connections.
    closeBundle: closeChildren,
  }
}
