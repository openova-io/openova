/**
 * LoginPage — email-entry step of the 6-digit PIN auth flow (issue #688).
 *
 * Replaces the magic-link login screen. The operator types their email,
 * we POST /api/v1/auth/pin/issue, then route them to /login/verify with
 * the email + requestId in the URL so a refresh on the verify screen
 * keeps state. The PIN itself never enters the URL.
 */

import { useState } from 'react'
import { motion } from 'framer-motion'
import { ArrowRight } from 'lucide-react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { AuthShell } from '@/app/layouts/AuthLayout'
import { Button } from '@/shared/ui/button'
import { Input } from '@/shared/ui/input'
import { API_BASE } from '@/shared/config/urls'

type State = 'idle' | 'sending' | 'error'

export function LoginPage() {
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as Record<string, string | undefined>
  const next = search['next']
  const initialError = search['error']

  const [email, setEmail] = useState('')
  const [state, setState] = useState<State>('idle')
  const [errorMsg, setErrorMsg] = useState<string>(
    initialError === 'pin-expired'
      ? 'Your sign-in code expired. Request a new one.'
      : initialError === 'attempts-exceeded'
        ? 'Too many wrong attempts. Request a new code.'
        : initialError === 'flow_changed'
          ? 'The sign-in flow has changed. Enter your email to receive a 6-digit code.'
          : '',
  )

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!email.trim()) return
    setState('sending')
    setErrorMsg('')
    try {
      const res = await fetch(`${API_BASE}/v1/auth/pin/issue`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ email: email.trim() }),
      })
      const body = (await res.json().catch(() => ({}))) as {
        ok?: boolean
        requestId?: string
        error?: string
        detail?: string
        retryAfterSec?: number
      }
      if (!res.ok || !body.ok || !body.requestId) {
        if (body.error === 'pin-rate-limited') {
          setErrorMsg(
            `Please wait ${body.retryAfterSec ?? 60}s before requesting another code.`,
          )
        } else {
          setErrorMsg(body.detail ?? body.error ?? `HTTP ${res.status}`)
        }
        setState('error')
        return
      }

      // Route to /login/verify with email + requestId in the URL so a
      // refresh keeps state. PIN never enters the URL.
      const params: Record<string, string> = {
        email: email.trim(),
        requestId: body.requestId,
      }
      if (next) params.next = next
      navigate({
        to: '/login/verify',
        search: params,
      })
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : 'Something went wrong')
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
        <div className="flex flex-col gap-2 text-center">
          <h1 className="text-2xl font-semibold tracking-tight text-[oklch(94%_0.01_250)]">
            Sign in
          </h1>
          <p className="text-[15px] text-[oklch(58%_0.01_250)]">
            We'll email you a 6-digit code to verify it's you.
          </p>
        </div>

        <form onSubmit={onSubmit} className="flex flex-col gap-4" noValidate>
          <Input
            label="Email"
            type="email"
            placeholder="you@company.com"
            autoComplete="email"
            autoFocus
            value={email}
            onChange={(e) => setEmail(e.target.value)}
            error={state === 'error' ? errorMsg : undefined}
            data-testid="login-email"
          />

          <Button
            type="submit"
            loading={state === 'sending'}
            disabled={state === 'sending' || !email.trim()}
            size="lg"
            className="mt-1 w-full"
            data-testid="login-submit"
          >
            Send code
            <ArrowRight className="h-4 w-4" />
          </Button>
        </form>
      </motion.div>
    </AuthShell>
  )
}
