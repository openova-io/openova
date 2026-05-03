/**
 * PinSignInModal — inline sign-in modal for the wizard's anonymous flow
 * (issue #689).
 *
 * Two-step UX:
 *   1. Email entry form. POST /api/v1/auth/pin/issue.
 *   2. Six-digit PIN entry. POST /api/v1/auth/pin/verify, which on
 *      success sets the session cookie and the modal calls onAuthenticated.
 *
 * If issue #688 (PIN auth endpoints) hasn't landed yet, the issue path
 * falls back to /api/v1/auth/magic-link as a placeholder so this modal
 * still exposes a working sign-in flow during the inter-PR window. The
 * fallback is detected by the response status (404 / 405 from the PIN
 * endpoint → fall back to magic-link). Once #688 lands the fallback is
 * unreachable but the dead branch is harmless and the cost of removing
 * it later is one Edit.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #10 (credential hygiene) the entered
 * PIN never logs to console, never appears in URLs, and is held only in
 * component state for the brief window between entry and POST. The
 * email is fine to log — it's not a secret.
 */

import { useEffect, useState } from 'react'
import { motion, AnimatePresence } from 'framer-motion'
import { X, Mail, KeyRound, ArrowRight } from 'lucide-react'
import { Button } from '@/shared/ui/button'
import { Input } from '@/shared/ui/input'
import { API_BASE } from '@/shared/config/urls'

export interface PinSignInModalProps {
  /** Whether the modal is mounted/visible. */
  open: boolean
  /** Close handler — fires on ESC, backdrop click, or X. */
  onClose: () => void
  /** Fires on successful PIN verify (cookie is set by then). */
  onAuthenticated: (email: string) => void
  /** Optional pre-filled email (e.g. from wizard's orgEmail field). */
  initialEmail?: string
  /** Optional title override (default: "Sign in to OpenOva"). */
  title?: string
  /** Optional subtitle below the title. */
  subtitle?: string
}

type Stage = 'email' | 'pin' | 'magic-link-sent'
type Status = 'idle' | 'submitting' | 'error'

export function PinSignInModal({
  open,
  onClose,
  onAuthenticated,
  initialEmail = '',
  title = 'Sign in to OpenOva',
  subtitle,
}: PinSignInModalProps) {
  const [stage, setStage] = useState<Stage>('email')
  const [status, setStatus] = useState<Status>('idle')
  const [email, setEmail] = useState(initialEmail)
  const [pin, setPin] = useState('')
  const [errorMsg, setErrorMsg] = useState('')

  // Reset on open/close so a closed-then-reopened modal starts fresh.
  useEffect(() => {
    if (open) {
      setStage('email')
      setStatus('idle')
      setPin('')
      setErrorMsg('')
      setEmail(initialEmail)
    }
  }, [open, initialEmail])

  // ESC closes the modal.
  useEffect(() => {
    if (!open) return
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [open, onClose])

  async function submitEmail(e: React.FormEvent) {
    e.preventDefault()
    if (!email.trim()) return
    setStatus('submitting')
    setErrorMsg('')

    // Try PIN-issue first (issue #688). Fall back to magic-link if the
    // endpoint is not yet present (404 / 405) — keeps this modal
    // shippable independent of #688's merge order.
    try {
      const res = await fetch(`${API_BASE}/v1/auth/pin/issue`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ email: email.trim() }),
      })
      if (res.ok) {
        setStage('pin')
        setStatus('idle')
        return
      }
      if (res.status === 404 || res.status === 405) {
        // PIN endpoint not deployed yet — fall back to magic-link.
        await sendMagicLink()
        return
      }
      const body = await res.json().catch(() => ({}))
      throw new Error((body as { error?: string }).error ?? `HTTP ${res.status}`)
    } catch (err) {
      // Network error — try magic-link as a last resort. If THAT also
      // fails, show the error.
      try {
        await sendMagicLink()
      } catch {
        setErrorMsg(err instanceof Error ? err.message : 'Sign-in failed')
        setStatus('error')
      }
    }
  }

  async function sendMagicLink() {
    // TODO(#688): Remove this fallback once PIN endpoints are deployed
    // everywhere. Until then this preserves the user-visible "I can sign
    // in from the wizard" capability.
    const res = await fetch(`${API_BASE}/v1/auth/magic-link`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'include',
      body: JSON.stringify({ email: email.trim() }),
    })
    if (!res.ok) {
      const body = await res.json().catch(() => ({}))
      throw new Error((body as { error?: string }).error ?? `HTTP ${res.status}`)
    }
    setStage('magic-link-sent')
    setStatus('idle')
  }

  async function submitPin(e: React.FormEvent) {
    e.preventDefault()
    if (pin.trim().length !== 6) return
    setStatus('submitting')
    setErrorMsg('')
    try {
      const res = await fetch(`${API_BASE}/v1/auth/pin/verify`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ email: email.trim(), pin: pin.trim() }),
      })
      if (!res.ok) {
        const body = await res.json().catch(() => ({}))
        throw new Error((body as { error?: string }).error ?? `Invalid PIN`)
      }
      // Per credential hygiene #10 — clear the PIN from component state
      // immediately after a successful POST so it never lingers in
      // memory longer than necessary.
      setPin('')
      onAuthenticated(email.trim())
    } catch (err) {
      setErrorMsg(err instanceof Error ? err.message : 'Invalid PIN')
      setStatus('error')
    }
  }

  if (!open) return null

  return (
    <AnimatePresence>
      <motion.div
        key="backdrop"
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        exit={{ opacity: 0 }}
        transition={{ duration: 0.15 }}
        onClick={onClose}
        style={{
          position: 'fixed',
          inset: 0,
          background: 'rgba(0,0,0,0.55)',
          backdropFilter: 'blur(4px)',
          WebkitBackdropFilter: 'blur(4px)',
          display: 'flex',
          alignItems: 'center',
          justifyContent: 'center',
          zIndex: 1000,
        }}
        data-testid="pin-signin-modal"
      >
        <motion.div
          key="dialog"
          initial={{ opacity: 0, y: 16, scale: 0.97 }}
          animate={{ opacity: 1, y: 0, scale: 1 }}
          exit={{ opacity: 0, y: 12, scale: 0.97 }}
          transition={{ duration: 0.2, ease: [0.4, 0, 0.2, 1] }}
          onClick={(e) => e.stopPropagation()}
          role="dialog"
          aria-modal="true"
          aria-labelledby="pin-modal-title"
          style={{
            width: 'min(420px, calc(100vw - 32px))',
            background: 'var(--wiz-bg-input, #0f172a)',
            border: '1px solid var(--wiz-border, rgba(255,255,255,0.12))',
            borderRadius: 12,
            padding: '20px 22px 22px',
            boxShadow: '0 24px 60px rgba(0,0,0,0.5)',
            color: 'var(--wiz-text-md, #e2e8f0)',
          }}
        >
          {/* Header */}
          <div style={{ display: 'flex', alignItems: 'flex-start', gap: 12, marginBottom: 16 }}>
            <span
              aria-hidden
              style={{
                width: 36,
                height: 36,
                borderRadius: 10,
                background: 'rgba(56,189,248,0.12)',
                color: '#38BDF8',
                display: 'inline-flex',
                alignItems: 'center',
                justifyContent: 'center',
                flexShrink: 0,
              }}
            >
              {stage === 'pin' ? <KeyRound size={18} /> : <Mail size={18} />}
            </span>
            <div style={{ flex: 1, minWidth: 0 }}>
              <h2
                id="pin-modal-title"
                style={{
                  margin: 0,
                  fontSize: 16,
                  fontWeight: 600,
                  color: 'var(--wiz-text-hi, #f8fafc)',
                  lineHeight: 1.3,
                }}
              >
                {stage === 'magic-link-sent'
                  ? 'Check your inbox'
                  : stage === 'pin'
                    ? 'Enter your 6-digit PIN'
                    : title}
              </h2>
              {(subtitle || stage !== 'email') && (
                <p
                  style={{
                    margin: '4px 0 0',
                    fontSize: 12,
                    color: 'var(--wiz-text-sub, #94a3b8)',
                    lineHeight: 1.4,
                  }}
                >
                  {stage === 'magic-link-sent'
                    ? `We sent a sign-in link to ${email}. The link expires in 15 minutes.`
                    : stage === 'pin'
                      ? `We sent a 6-digit PIN to ${email}. The PIN expires in 10 minutes.`
                      : subtitle}
                </p>
              )}
            </div>
            <button
              type="button"
              aria-label="Close"
              onClick={onClose}
              style={{
                background: 'transparent',
                border: 'none',
                color: 'var(--wiz-text-sub, #94a3b8)',
                cursor: 'pointer',
                padding: 4,
                borderRadius: 6,
                display: 'inline-flex',
              }}
            >
              <X size={16} />
            </button>
          </div>

          {/* Stage 1 — Email */}
          {stage === 'email' && (
            <form onSubmit={submitEmail} style={{ display: 'flex', flexDirection: 'column', gap: 12 }} noValidate>
              <Input
                label="Email"
                type="email"
                placeholder="you@company.com"
                autoComplete="email"
                autoFocus
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                error={status === 'error' ? errorMsg : undefined}
                data-testid="pin-modal-email-input"
              />
              <Button
                type="submit"
                loading={status === 'submitting'}
                disabled={status === 'submitting' || !email.trim()}
                size="lg"
                className="mt-1 w-full"
                data-testid="pin-modal-submit-email"
              >
                Send PIN
                <ArrowRight className="h-4 w-4" />
              </Button>
            </form>
          )}

          {/* Stage 2 — PIN entry */}
          {stage === 'pin' && (
            <form onSubmit={submitPin} style={{ display: 'flex', flexDirection: 'column', gap: 12 }} noValidate>
              <Input
                label="6-digit PIN"
                type="text"
                inputMode="numeric"
                pattern="[0-9]{6}"
                maxLength={6}
                placeholder="123456"
                autoComplete="one-time-code"
                autoFocus
                value={pin}
                onChange={(e) => setPin(e.target.value.replace(/\D/g, '').slice(0, 6))}
                error={status === 'error' ? errorMsg : undefined}
                data-testid="pin-modal-pin-input"
              />
              <Button
                type="submit"
                loading={status === 'submitting'}
                disabled={status === 'submitting' || pin.length !== 6}
                size="lg"
                className="mt-1 w-full"
                data-testid="pin-modal-submit-pin"
              >
                Verify
                <ArrowRight className="h-4 w-4" />
              </Button>
              <button
                type="button"
                onClick={() => {
                  setStage('email')
                  setStatus('idle')
                  setPin('')
                }}
                style={{
                  background: 'transparent',
                  border: 'none',
                  color: 'var(--wiz-text-sub, #94a3b8)',
                  fontSize: 12,
                  cursor: 'pointer',
                  textAlign: 'center',
                  padding: '4px 0',
                }}
              >
                Use a different email
              </button>
            </form>
          )}

          {/* Stage 3 — Magic link sent (PIN endpoint unavailable fallback) */}
          {stage === 'magic-link-sent' && (
            <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
              <p style={{ fontSize: 12, color: 'var(--wiz-text-sub, #94a3b8)', margin: 0, lineHeight: 1.5 }}>
                Click the link in your email to finish signing in. Once you're signed in, return
                to this tab and click Launch again.
              </p>
              <button
                type="button"
                onClick={onClose}
                style={{
                  background: 'transparent',
                  border: '1px solid var(--wiz-border, rgba(255,255,255,0.12))',
                  borderRadius: 8,
                  color: 'var(--wiz-text-md, #e2e8f0)',
                  padding: '8px 12px',
                  cursor: 'pointer',
                  fontSize: 13,
                  fontWeight: 600,
                }}
              >
                OK
              </button>
            </div>
          )}
        </motion.div>
      </motion.div>
    </AnimatePresence>
  )
}
