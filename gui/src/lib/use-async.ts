import { useCallback, useEffect, useRef, useState } from "react"

export interface AsyncState<T> {
  data: T | null
  loading: boolean
  error: string | null
  reload: () => void
  setData: (value: T | null) => void
}

/**
 * Minimal async loader with loading/error state. The loader is held in a ref
 * so an inline arrow function does not retrigger the effect every render.
 */
export function useAsync<T>(
  loader: () => Promise<T>,
  deps: unknown[] = []
): AsyncState<T> {
  const [data, setData] = useState<T | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [tick, setTick] = useState(0)

  const loaderRef = useRef(loader)

  useEffect(() => {
    loaderRef.current = loader
  }, [loader])

  useEffect(() => {
    let cancelled = false
    setLoading(true)
    setError(null)

    loaderRef
      .current()
      .then((value) => {
        if (cancelled) return
        setData(value)
        setError(null)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [...deps, tick])

  const reload = useCallback(() => setTick((value) => value + 1), [])

  return { data, loading, error, reload, setData }
}
