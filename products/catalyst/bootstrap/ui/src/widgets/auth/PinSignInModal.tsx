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

import { useEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { motion, AnimatePresence } from 'framer-motion'
import { X, Mail, KeyRound, ArrowRight, Check, Copy } from 'lucide-react'
import { Button } from '@/shared/ui/button'
import { Input } from '@/shared/ui/input'
import { PinInput6 } from '@/components/PinInput6'
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
  const [pinResetCounter, setPinResetCounter] = useState(0)
  const [emailCopied, setEmailCopied] = useState(false)
  const emailCopyTimer = useRef<ReturnType<typeof setTimeout> | null>(null)

  async function copyEmailToClipboard() {
    try {
      await navigator.clipboard.writeText(email.trim())
      setEmailCopied(true)
      if (emailCopyTimer.current) clearTimeout(emailCopyTimer.current)
      emailCopyTimer.current = setTimeout(() => setEmailCopied(false), 1500)
    } catch {
      const sel = window.getSelection()
      const tgt = document.getElementById('pin-modal-email-pill-text')
      if (sel && tgt) {
        const range = document.createRange()
        range.selectNodeContents(tgt)
        sel.removeAllRanges()
        sel.addRange(range)
      }
    }
  }

  // Reset on open/close so a closed-then-reopened modal starts fresh.
  useEffect(() => {
    if (open) {
      setStage('email')
      setStatus('idle')
      setPin('')
      setErrorMsg('')
      setEmail(initialEmail)
      setPinResetCounter((n) => n + 1)
    }
  }, [open, initialEmail])

  // Auto-submit when 6 digits are filled by the PinInput6 component —
  // matches Apple iCloud / Stripe verification flows where the user
  // never has to click Verify after typing the final digit.
  async function maybeAutoVerify(value: string) {
    if (value.length === 6 && stage === 'pin' && status !== 'submitting') {
      await submitPinValue(value)
    }
  }

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
    // Magic-link fallback: PIN endpoints are the canonical sign-in path
    // (#688). The magic-link path remains for accounts whose Sovereign is
    // mid-roll and hasn't yet shipped the PIN handlers — fail-open keeps
    // wizard sign-in available regardless of API SHA.
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
    await submitPinValue(pin)
  }

  async function submitPinValue(value: string) {
    const trimmed = value.trim()
    if (trimmed.length !== 6) return
    setStatus('submitting')
    setErrorMsg('')
    try {
      const res = await fetch(`${API_BASE}/v1/auth/pin/verify`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ email: email.trim(), pin: trimmed }),
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
      // Clear the boxes so the user can retype without backspace-chasing.
      setPin('')
      setPinResetCounter((n) => n + 1)
    }
  }

  if (!open) return null

  // Render through createPortal into document.body. Without the portal
  // the modal is positioned fixed within whatever ancestor has a
  // CSS `transform`, `filter`, or `will-change: transform` (the
  // framer-motion animated topbar that hosts ProfileMenu does — every
  // motion.div applies a transform during animation). Per CSS spec,
  // a transformed ancestor becomes the containing block for fixed-
  // position descendants, so the backdrop covers ONLY the topbar
  // region, not the viewport — exactly the bug reported live
  // 2026-05-04 (popup tucked top-right of the wizard instead of
  // dimming the whole page).
  const modal = (
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
            // 480px wide so 6 PIN boxes (each 56px) + 5 gaps (12px)
            // = 396px content fits with 28px internal padding on each
            // side. Was 420px which clipped the last box on smaller
            // viewports — caught live 2026-05-04.
            width: 'min(480px, calc(100vw - 32px))',
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
            <form onSubmit={submitPin} style={{ display: 'flex', flexDirection: 'column', gap: 14 }} noValidate>
              {/* Copyable email pill — mirrors VerifyPinPage so the
                  inline modal and the standalone page feel like the
                  same product. Click copies, icon flips to check. */}
              <div style={{ display: 'flex', justifyContent: 'center' }}>
                <button
                  type="button"
                  onClick={copyEmailToClipboard}
                  data-testid="pin-modal-email-pill"
                  title={emailCopied ? 'Copied' : 'Copy email'}
                  style={{
                    display: 'inline-flex',
                    alignItems: 'center',
                    gap: 8,
                    padding: '5px 12px',
                    fontSize: 13,
                    fontWeight: 500,
                    color: 'var(--wiz-text-md, #e2e8f0)',
                    background: 'var(--wiz-bg-soft, rgba(255,255,255,0.04))',
                    border: '1px solid var(--wiz-border, rgba(255,255,255,0.12))',
                    borderRadius: 999,
                    cursor: 'pointer',
                    transition: 'background 120ms, border-color 120ms',
                  }}
                >
                  <span id="pin-modal-email-pill-text" style={{ userSelect: 'all' }}>
                    {email}
                  </span>
                  {emailCopied ? (
                    <Check size={14} style={{ color: '#22c55e' }} aria-label="Copied" />
                  ) : (
                    <Copy size={14} style={{ color: 'var(--wiz-text-sub, #94a3b8)' }} aria-label="Copy email" />
                  )}
                </button>
              </div>

              {/* 6-box PIN input. Paste a 6-digit code anywhere on the
                  row and all 6 boxes fill — fan-out via PinInput6's
                  wrapper-level onPaste. Auto-submits on the 6th digit
                  (Apple iCloud / Stripe parity). */}
              <PinInput6
                key={pinResetCounter}
                disabled={status === 'submitting'}
                onChange={setPin}
                onComplete={(value) => void maybeAutoVerify(value)}
                testId="pin-modal-pin"
              />

              {status === 'error' && errorMsg && (
                <p
                  role="alert"
                  data-testid="pin-modal-error"
                  style={{
                    margin: 0,
                    fontSize: 12,
                    color: '#ef4444',
                    textAlign: 'center',
                  }}
                >
                  {errorMsg}
                </p>
              )}

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
              <p
                style={{
                  margin: '2px 0 0',
                  fontSize: 11,
                  color: 'var(--wiz-text-sub, #94a3b8)',
                  textAlign: 'center',
                  lineHeight: 1.5,
                }}
              >
                Codes expire after 10 minutes — check your spam folder.
              </p>
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

  // SSR safety: in Vitest happy-dom and Node SSR, document is undefined.
  // Fall back to inline render — the test asserts on data-testid, not
  // the portal'd DOM tree.
  if (typeof document === 'undefined') return modal
  return createPortal(modal, document.body)
}
