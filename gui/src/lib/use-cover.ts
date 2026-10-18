import { useEffect, useState } from "react"
import { invoke } from "@tauri-apps/api/core"

import { hasCliBridge, isTauri } from "./cli"

// Cover bytes never change under the same path unless the file is replaced, so
// a module-level cache avoids re-reading them on every list render.
const cache = new Map<string, string | null>()
const pending = new Map<string, Promise<string | null>>()

function loadCover(path: string): Promise<string | null> {
  const cached = cache.get(path)
  if (cached !== undefined) return Promise.resolve(cached)
  const inFlight = pending.get(path)
  if (inFlight) return inFlight
  // The dev bridge serves cover bytes over HTTP; Tauri reads them over IPC.
  // `import.meta.env.DEV` is a compile-time constant, so the browser branch is
  // dropped from a production bundle.
  const source =
    import.meta.env.DEV && !isTauri
      ? fetch(`/__cover?path=${encodeURIComponent(path)}`)
          .then((response) => (response.ok ? response.blob() : null))
          .then((blob) => (blob ? URL.createObjectURL(blob) : null))
      : invoke<string | null>("read_cover", { path })
  const request = source
    .then((value) => {
      cache.set(path, value ?? null)
      return value ?? null
    })
    .catch(() => {
      cache.set(path, null)
      return null
    })
    .finally(() => {
      pending.delete(path)
    })
  pending.set(path, request)
  return request
}

/** Resolve a local cover file to a data URL usable in an <img>. */
export function useCover(path?: string): string | undefined {
  const [loaded, setLoaded] = useState<{ path: string; src: string | null } | null>(
    () =>
      path && cache.has(path)
        ? { path, src: cache.get(path) ?? null }
        : null
  )

  useEffect(() => {
    if (!path || !hasCliBridge || cache.has(path)) return
    let cancelled = false
    void loadCover(path).then((value) => {
      if (!cancelled) setLoaded({ path, src: value })
    })
    return () => {
      cancelled = true
    }
  }, [path])

  if (!path || !hasCliBridge) return undefined
  const cached = cache.get(path)
  if (cached !== undefined) return cached ?? undefined
  if (loaded?.path === path) return loaded.src ?? undefined
  return undefined
}

/** Drop a cached cover so a forced re-download becomes visible. */
export function invalidateCover(path: string) {
  cache.delete(path)
  pending.delete(path)
}
