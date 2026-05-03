/**
 * VerifyPinPage — second step of the 6-digit PIN auth flow (issue #688).
 *
 * Renders the paste-friendly PinInput6 component, POSTs the 6-digit
 * code to /api/v1/auth/pin/verify on submit (auto-submit on the 6th
 * digit, or Enter, or "Verify" click), and routes the operator to
 * the wizard on success.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 the API base is sourced from
 * shared/config/urls so the same UI image works on Catalyst-Zero
 * (/sovereign/api/...) and Sovereign clusters (/api/...).
 */

import { useEffect, useState } from 'react'
import { motion } from 'framer-motion'
import { ArrowRight } from 'lucide-react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { AuthShell } from '@/app/layouts/AuthLayout'
import { Button } from '@/shared/ui/button'
import { PinInput6 } from '@/components/PinInput6'
import { API_BASE } from '@/shared/config/urls'

type State = 'idle' | 'verifying' | 'error'

export function VerifyPinPage() {
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as Record<string, string | undefined>
  const email = search['email'] ?? ''
  const requestId = search['requestId'] ?? ''
  const next = search['next']

  const [pin, setPin] = useState('')
  const [state, setState] = useState<State>('idle')
  const [errorMsg, setErrorMsg] = useState('')
  const [resetCounter, setResetCounter] = useState(0) // bumps to clear PinInput6

  // If the user landed here without going through /login (refresh after
  // bookmarking, etc.), kick them back so they can request a new code.
  useEffect(() => {
    if (!email || !requestId) {
      navigate({ to: '/login', replace: true })
    }
  }, [email, requestId, navigate])

  async function submit(value: string) {
    if (state === 'verifying') return
    if (value.length !== 6) return
    setState('verifying')
    setErrorMsg('')
    try {
      const res = await fetch(`${API_BASE}/v1/auth/pin/verify`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ email, pin: value, requestId }),
      })
      const body = (await res.json().catch(() => ({}))) as {
        ok?: boolean
        error?: string
        detail?: string
        attemptsRemaining?: number
      }
      if (res.ok && body.ok) {
        // Success — navigate to the next page (wizard by default).
        const target = (next as never) ?? ('/wizard' as never)
        // Hard navigation isn't required here — the cookie is set on
        // the same origin and TanStack router's beforeLoad guard will
        // re-poll /whoami when /wizard mounts.
        navigate({ to: target, replace: true })
        return
      }
      if (res.status === 401 && body.error === 'pin-invalid') {
        const remaining = body.attemptsRemaining ?? 0
        setErrorMsg(
          remaining > 0
            ? `Code incorrect. ${remaining} ${remaining === 1 ? 'attempt' : 'attempts'} remaining.`
            : 'Code incorrect.',
        )
        setPin('')
        setResetCounter((n) => n + 1)
        setState('error')
        return
      }
      if (res.status === 410) {
        // Expired or attempts-exceeded — bounce to /login so the operator
        // requests a new code.
        navigate({
          to: '/login',
          search: { error: body.error ?? 'pin-expired' },
          replace: true,
        })
        return
      }
      setErrorMsg(body.detail ?? body.error ?? `HTTP ${res.status}`)
      setState('error')
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : 'Network error')
      setState('error')
    }
  }

  return (
    <AuthShell>
      <motion.div
        initial={{ opacity: 0, y: 16 }}
        animate={{ opacity: 1, y: 0 }}
        transition={{ duration: 0.4, ease: [0.4, 0, 0.2, 1] }}
        className="flex flex-col gap-8"
      >
        <div>
          <h1 className="text-xl font-semibold text-[oklch(92%_0.01_250)]">Enter your code</h1>
          <p className="mt-1 text-sm text-[oklch(50%_0.01_250)]">
            We sent a 6-digit code to{' '}
            <span className="font-medium text-[oklch(75%_0.01_250)]">{email}</span>. Paste or type
            it below.
          </p>
        </div>

        <form
          onSubmit={(e) => {
            e.preventDefault()
            void submit(pin)
          }}
          className="flex flex-col gap-6"
          noValidate
        >
          <PinInput6
            key={resetCounter}
            disabled={state === 'verifying'}
            onChange={setPin}
            onComplete={(v) => void submit(v)}
            testId="pin-box"
          />
          {errorMsg && (
            <p
              role="alert"
              data-testid="verify-error"
              className="text-sm text-[--color-error]"
            >
              {errorMsg}
            </p>
          )}

          <Button
            type="submit"
            loading={state === 'verifying'}
            disabled={state === 'verifying' || pin.length !== 6}
            size="lg"
            className="w-full"
            data-testid="verify-submit"
          >
            Verify
            <ArrowRight className="h-4 w-4" />
          </Button>
        </form>

        <button
          type="button"
          onClick={() =>
            navigate({
              to: '/login',
              search: next ? { next } : {},
            })
          }
          className="text-center text-sm text-[--color-brand-400] hover:text-[--color-brand-300] transition-colors"
          data-testid="verify-back"
        >
          Use a different email or get a new code
        </button>
      </motion.div>
    </AuthShell>
  )
}
