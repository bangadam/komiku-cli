import { useCallback, useEffect, useRef, useState } from "react"

export interface AsyncState<T> {
  data: T | null
  loading: boolean
  error: string | null
  reload: () => void
  setData: (value: T | null) => void
}

interface SettledResult<T> {
  /** Serialized request key the result belongs to. */
  key: string
  data: T | null
  error: string | null
}

/**
 * Minimal async loader with loading/error state. The loader is held in a ref
 * so an inline arrow function does not retrigger the effect every render, and
 * the dependency list is folded into a single key so the effect can declare a
 * plain dependency array.
 */
export function useAsync<T>(
  loader: () => Promise<T>,
  deps: unknown[] = []
): AsyncState<T> {
  const loaderRef = useRef(loader)
  const [attempt, setAttempt] = useState(0)
  const [result, setResult] = useState<SettledResult<T> | null>(null)
  const key = `${attempt}:${JSON.stringify(deps)}`

  useEffect(() => {
    loaderRef.current = loader
  }, [loader])

  useEffect(() => {
    let cancelled = false

    loaderRef
      .current()
      .then((value) => {
        if (cancelled) return
        setResult({ key, data: value, error: null })
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setResult((current) => ({
          key,
          data: current?.data ?? null,
          error: err instanceof Error ? err.message : String(err),
        }))
      })

    return () => {
      cancelled = true
    }
  }, [key])

  const reload = useCallback(() => setAttempt((value) => value + 1), [])

  const setData = useCallback(
    (value: T | null) => {
      setResult((current) => ({
        key,
        data: value,
        error: current && current.key === key ? current.error : null,
      }))
    },
    [key]
  )

  const settled = result && result.key === key ? result : null

  return {
    data: result?.data ?? null,
    loading: settled === null,
    error: settled?.error ?? null,
    reload,
    setData,
  }
}
