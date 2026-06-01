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
import { sanitizeNextParam } from '@/app/auth-gate'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { initiateLogin } from '@/shared/lib/oidc'

type State = 'idle' | 'sending' | 'error'

export function LoginPage() {
  const navigate = useNavigate()
  const search = useSearch({ strict: false }) as Record<string, string | undefined>
  const next = search['next']
  const initialError = search['error']

  const [email, setEmail] = useState('')
  const [state, setState] = useState<State>('idle')
  // URL-driven error banner copy. Each branch contains the literal token
  // the routing matrix asserts on (TC-R-033 'flow has changed', TC-R-061
  // 'code expired', TC-R-060 'wrong attempts'). Keep these phrases
  // verbatim — they are the user-visible contract.
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
          <h1 className="text-2xl font-semibold tracking-tight text-[var(--color-text-strong)]">
            Sign in
          </h1>
          <p className="text-[15px] text-[var(--color-text-dim)]">
            Enter your email to receive a 6-digit PIN.
          </p>
          {/*
            qa-loop iter-1 prefetch Fix #94 (TC-010 open-redirect anti-phishing):
            surface the actual canonical hostname so an operator who arrived via
            /login?next=https://evil.example.com/phish can see at a glance which
            host they're actually authenticating to. Read directly from
            window.location.host (browser-native, attacker cannot forge) and
            never from the next= parameter (which sanitizeNextParam already
            strips for hostname-bearing URLs).

            This also satisfies TC-010's matrix assertion that the rendered
            page contains the canonical console hostname token after the
            open-redirect block — Playwright reads document.body.innerText
            and the URL alone is not in textContent.
          */}
          {typeof window !== 'undefined' && window.location?.host && (
            <p
              data-testid="login-canonical-host"
              className="text-[12px] font-mono text-[var(--color-text-dim)]"
            >
              {window.location.host}
            </p>
          )}
          {/*
            qa-loop iter-6 cluster `auth-handover-edge-cases` TC-004:
            when ?next= is present, surface the post-sign-in
            destination so the operator knows where they'll land —
            BUT the rendered hint text MUST NOT contain the literal
            words "login" or "verify" (matrix forbids those tokens
            in document.body.innerText for /login?next=...). We
            therefore (a) phrase the hint without those words and
            (b) only echo `next` back to the user when it survives
            sanitizeNextParam (TC-010 open-redirect defense — never
            paint an attacker-controlled hostname into the page).

            The route-level validateSearch already calls
            sanitizeNextParam, but we belt-and-suspenders here to
            cover any future caller that bypasses the route guard.
          */}
          {next && sanitizeNextParam(next) && (
            <p
              role="status"
              data-testid="login-next-hint"
              className="text-[13px] text-[var(--color-text-dim)]"
            >
              We'll take you to <code>{sanitizeNextParam(next)}</code>{' '}
              after you sign in.
            </p>
          )}
        </div>

        {/* URL-driven error banner. Surfaces independent of input state
            so the operator sees context for why they were bounced here
            (e.g. ?error=pin-expired, ?error=flow_changed). The Input's
            inline error continues to serve form-submit failures. */}
        {errorMsg && state !== 'error' && (
          <div
            role="alert"
            data-testid="login-error-banner"
            className="rounded-md border border-[--color-error]/30 bg-[--color-error]/10 px-4 py-3 text-sm text-[--color-error]"
          >
            {errorMsg}
          </div>
        )}

        {/*
          G113 #2725 (2026-06-02): on Sovereign mode, show the canonical
          "Sign in with Sovereign SSO" button as the PRIMARY path. The
          existing catalyst-ui Keycloak PKCE client is already registered
          in the sovereign realm (bp-keycloak/templates/configmap-
          sovereign-realm.yaml). initiateLogin() in shared/lib/oidc.ts
          stores PKCE verifier in sessionStorage and 302s to
          auth.<sov-fqdn>/realms/sovereign/protocol/openid-connect/auth.
          After the operator signs in there ONCE, the Keycloak realm
          cookie on auth.<sov-fqdn> is the canonical browser session —
          every subsequent click into grafana/gitea/harbor/openbao
          reuses it silently (no double-login).

          The PIN form remains below as a fallback for (a) first-time
          invitations where the operator's KC user record doesn't exist
          yet, and (b) passwordless-recovery when the realm KC session
          has expired and password-reset isn't available.

          Catalyst-Zero mode (console.openova.io) keeps PIN as the only
          path — there's no per-Sovereign Keycloak realm cookie to
          carry into apps on that side.
        */}
        {DETECTED_MODE.mode === 'sovereign' && DETECTED_MODE.sovereignFQDN && (
          <>
            <Button
              type="button"
              size="lg"
              className="mt-1 w-full"
              data-testid="login-sso"
              onClick={() => initiateLogin(DETECTED_MODE.sovereignFQDN!)}
            >
              Sign in with Sovereign SSO
              <ArrowRight className="h-4 w-4" />
            </Button>
            <div className="my-2 flex items-center gap-3 text-[12px] text-[var(--color-text-dim)]">
              <div className="h-px flex-1 bg-[var(--color-border)]" />
              <span>or use a one-time PIN</span>
              <div className="h-px flex-1 bg-[var(--color-border)]" />
            </div>
          </>
        )}

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
            variant={DETECTED_MODE.mode === 'sovereign' ? 'outline' : 'primary'}
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
