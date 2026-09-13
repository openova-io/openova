import { useCallback, useState } from 'react'
import { errorText } from '../api/client'
import { t } from '../i18n'
import { buildsDiffer, pageBuild, serverBuild } from './build'

/**
 * useAction — one busy flag + one error + one success line for a panel's
 * mutations (#6867). `run` shows the server's message verbatim on failure
 * and the given label on success, then reloads through `after`.
 *
 * A FAILURE WHILE THE PAGE IS STALE NAMES THAT AS THE CAUSE. The worst part
 * of the 0.1.42 report was not that the save failed — it was that it appeared
 * to do nothing, and the user was left to conclude the feature was broken. A
 * page running a build the server has moved past sends what that build knew
 * how to send; when the server then refuses it, the honest message is which
 * two builds are involved and that a reload fixes it, never a bare "failed".
 * The persistent notice the shell shows (components/BuildNotice.tsx) carries
 * the reload button, and is on screen the whole time this message can appear.
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
      setError(failureText(e))
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

/**
 * What a failed mutation says. The server's own message, unless the page is
 * running a different build from the server — in which case that is the cause
 * and is said first, with the server's message kept for whoever reads on.
 */
export function failureText(e: unknown): string {
  const server = errorText(e)
  const page = pageBuild()
  const current = serverBuild()
  if (!buildsDiffer(page, current)) return server
  return t('build.writeFailed', { page, server: current, error: server })
}
