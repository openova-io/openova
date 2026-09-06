import { useCallback, useState } from 'react'
import { errorText } from '../api/client'

/**
 * useAction — one busy flag + one error + one success line for a panel's
 * mutations (#6867). `run` shows the server's message verbatim on failure
 * and the given label on success, then reloads through `after`.
 */
export function useAction() {
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [ok, setOk] = useState('')

  const run = useCallback(async (label: string, fn: () => Promise<unknown>, after?: () => void | Promise<void>): Promise<boolean> => {
    setBusy(true)
    setError('')
    setOk('')
    try {
      await fn()
      setOk(label)
      if (after) await after()
      return true
    } catch (e) {
      setError(errorText(e))
      return false
    } finally {
      setBusy(false)
    }
  }, [])

  const clear = useCallback(() => {
    setError('')
    setOk('')
  }, [])

  return { busy, error, ok, run, clear, setError }
}
