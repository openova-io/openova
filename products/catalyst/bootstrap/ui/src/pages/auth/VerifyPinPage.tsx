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

import { useEffect, useRef, useState } from 'react'
import { motion } from 'framer-motion'
import { ArrowRight, Check, Copy } from 'lucide-react'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { AuthShell } from '@/app/layouts/AuthLayout'
import { Button } from '@/shared/ui/button'
import { PinInput6 } from '@/components/PinInput6'
import { API_BASE } from '@/shared/config/urls'
import { sanitizeNextParam } from '@/app/auth-gate'
import { DETECTED_MODE } from '@/shared/lib/detectMode'

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
  const [copied, setCopied] = useState(false)
  const copyTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  async function copyEmail() {
    try {
      await navigator.clipboard.writeText(email)
      setCopied(true)
      if (copyTimer.current) clearTimeout(copyTimer.current)
      copyTimer.current = setTimeout(() => setCopied(false), 1500)
    } catch {
      // Fallback for browsers without clipboard API: select the text so
      // the user can Cmd-C / Ctrl-C themselves.
      const sel = window.getSelection()
      const range = document.createRange()
      const target = document.getElementById('verify-email-pill-text')
      if (target && sel) {
        range.selectNodeContents(target)
        sel.removeAllRanges()
        sel.addRange(range)
      }
    }
  }

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
        // Mark the rootRoute auth gate (#1090 cluster A2) as satisfied
        // BEFORE navigating. The catalyst_session cookie just set is
        // HttpOnly + invisible to JS, so the gate's hasCatalystSession()
        // check would otherwise fail and bounce the operator right back
        // to /login (regression on omantel 2026-05-09).
        try { sessionStorage.setItem('catalyst:authed', '1') } catch { /* private browsing */ }
        // Hard navigation so the next page boot reads the cookie via
        // /whoami fresh and the auth gate sees the marker. Mirrors the
        // pattern Fix #A (PR #1093) uses on the unauth path.
        //
        // Belt-and-suspenders open-redirect defense (CWE-601): even
        // though /login + /login/verify validateSearch already
        // sanitizes `next`, we sanitize again here in case a future
        // caller bypasses the route's validateSearch. qa-loop iter-4
        // cluster `users-page-null-map-and-open-redirect`.
        //
        // Default landing depends on operating mode:
        //   • Sovereign Console (chroot, post-handover) → `/dashboard`.
        //     The operator has just been redirected from the mothership
        //     handover URL; their Sovereign is converged. Sending them
        //     to `/wizard` (the mothership new-prov wizard) would
        //     re-prompt for organisation details on an already-built
        //     Sovereign and violate D0/D23.  Caught live on t129
        //     (2026-05-16, BUG-015).
        //   • Catalyst-Zero (mothership) → keep `/wizard` so PIN-login
        //     drops the operator into a new-prov flow.
        const sovereignDefault =
          DETECTED_MODE.mode === 'sovereign' ? '/dashboard' : '/wizard'
        let target = sanitizeNextParam(next) ?? sovereignDefault
        if (typeof window !== 'undefined') {
          // window.location.replace is a HARD navigation — it bypasses
          // the TanStack Router's basepath config. On contabo the SPA
          // mounts under `/sovereign/*`; nginx 404s any request that
          // doesn't start with that prefix. The sanitized `next` value
          // may arrive in EITHER form depending on which redirectToLogin
          // variant produced it: SovereignConsoleLayout.tsx:91 (variant
          // A) sets next = window.location.pathname which INCLUDES the
          // basepath, while :178 (variant B) uses
          // currentPathRelativeToBasepath() which STRIPS it. Normalize
          // both forms to "post-basepath" before re-prefixing so we
          // never double-prefix and never miss-prefix.
          //
          // Caught live on prov #82 + #84 (omani.works, 2026-05-14):
          // variant-A produced `next=/sovereign/provision/...` which my
          // original #1486 fix would double-prefix to
          // `/sovereign/sovereign/...` → nginx 404.
          if (target.startsWith('/sovereign/')) target = target.slice('/sovereign'.length)
          else if (target === '/sovereign') target = '/'
          const basepath =
            window.location.pathname.startsWith('/sovereign')
              ? '/sovereign'
              : ''
          const fullTarget = basepath + target
          window.location.replace(fullTarget)
          return
        }
        navigate({ to: target as never, replace: true })
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
        className="flex flex-col gap-9"
      >
        <div className="flex flex-col items-center gap-3 text-center">
          <h1 className="text-2xl font-semibold tracking-tight text-[var(--color-text-strong)]">
            Enter the verification code
          </h1>
          <p className="text-[15px] text-[var(--color-text-dim)] leading-snug">
            A 6-digit code was sent to
          </p>
          <button
            type="button"
            onClick={copyEmail}
            data-testid="verify-email-pill"
            title="Copy email"
            className="group inline-flex items-center gap-2 rounded-full border border-[--color-surface-border] bg-[--color-surface-1] px-3.5 py-1.5 text-sm font-medium text-[var(--color-text)] hover:border-[--color-brand-500]/60 hover:bg-[--color-surface-2] transition-colors"
          >
            <span id="verify-email-pill-text" className="select-all">
              {email}
            </span>
            {copied ? (
              <Check className="h-3.5 w-3.5 text-[--color-success]" aria-label="Copied" />
            ) : (
              <Copy
                className="h-3.5 w-3.5 text-[var(--color-text-dim)] group-hover:text-[--color-brand-400] transition-colors"
                aria-label="Copy email"
              />
            )}
          </button>
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
              className="text-center text-sm text-[--color-error]"
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

        <div className="flex flex-col items-center gap-1.5 text-center">
          <button
            type="button"
            onClick={() =>
              navigate({
                to: '/login',
                search: next ? { next } : {},
              })
            }
            className="text-sm text-[--color-brand-400] hover:text-[--color-brand-300] transition-colors"
            data-testid="verify-back"
          >
            Didn't get a code? Send a new one
          </button>
          <p className="text-xs text-[var(--color-text-dimmer)]">
            Codes expire after 10 minutes — check your spam folder.
          </p>
        </div>
      </motion.div>
    </AuthShell>
  )
}
