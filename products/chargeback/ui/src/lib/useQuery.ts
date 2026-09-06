import { useCallback, useEffect, useRef, useState } from 'react'
import { api, errorText } from '../api/client'

/**
 * useQuery — GET a JSON document and expose {data, error, loading, reload}.
 * Re-fetches when `path` changes; a stale response from an earlier path is
 * dropped so a fast-changing explorer never shows the previous window's
 * numbers under the new window's title. `path` null = idle.
 */
export function useQuery<T>(path: string | null, deps: unknown[] = []) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(Boolean(path))
  const seq = useRef(0)

  const load = useCallback(async () => {
    if (!path) {
      setData(null)
      setLoading(false)
      return
    }
    const mine = ++seq.current
    setLoading(true)
    try {
      const d = await api.get<T>(path)
      if (mine !== seq.current) return
      setData(d)
      setError('')
    } catch (e) {
      if (mine !== seq.current) return
      setError(errorText(e))
    } finally {
      if (mine === seq.current) setLoading(false)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [path, ...deps])

  useEffect(() => {
    void load()
  }, [load])

  return { data, error, loading, reload: load, setData }
}
