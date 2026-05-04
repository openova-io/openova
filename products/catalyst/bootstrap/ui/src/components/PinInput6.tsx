/**
 * PinInput6 — Stripe-style 6-digit verification code input.
 *
 * Architecture (founder rule "paste must just work" 2026-05-04):
 *   - ONE real <input maxLength=6> that captures all keystrokes and
 *     paste — no per-box keyboard handling, no per-box paste handlers,
 *     no inter-handler races.
 *   - 6 visible boxes that simply MIRROR the digits the input is
 *     holding. The boxes are decorative — they don't accept input
 *     themselves. Clicking any box focuses the hidden input.
 *   - The input is absolutely positioned over the box row with
 *     transparent text, so the user sees only the box digits but the
 *     browser's native paste / autofill / one-time-code suggestion
 *     all flow into a single canonical input element.
 *
 * Why this beats the previous "6 separate inputs with paste fan-out":
 *   - Previous design: 6 inputs with maxLength=6 each, per-box paste
 *     handler that called preventDefault and manually distributed the
 *     pasted digits via setDigits. This raced with React 18 batched
 *     updates AND with browsers that intercept paste on
 *     `autoComplete=one-time-code` inputs (Chrome's SMS-suggestion
 *     reroute). Result: paste filled some boxes inconsistently and
 *     the founder reported having to type "1 by 1".
 *   - New design: paste goes to the ONE input. Browser sets value to
 *     "123456" natively. onChange fires once. We slice into 6 chars
 *     and update digits state. Boxes re-render. No race, no
 *     interception, no preventDefault gymnastics.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4: presentational only, parent
 * owns the fetch.
 */

import { useEffect, useRef, useState, useCallback } from 'react'

export interface PinInput6Props {
  length?: 6
  disabled?: boolean
  autoFocus?: boolean
  onChange?: (pin: string) => void
  onComplete?: (pin: string) => void
  value?: string
  testId?: string
}

function extractDigits(s: string): string {
  return (s.match(/\d/g) ?? []).join('')
}

export function PinInput6({
  disabled = false,
  autoFocus = true,
  onChange,
  onComplete,
  value,
  testId = 'pin-box',
}: PinInput6Props) {
  const N = 6
  const inputRef = useRef<HTMLInputElement | null>(null)

  // Single source of truth: the joined PIN string. Boxes derive from it.
  const [pin, setPin] = useState<string>(() =>
    value ? extractDigits(value).slice(0, N) : '',
  )

  // Notify parent on change + completion.
  useEffect(() => {
    onChange?.(pin)
    if (pin.length === N) {
      onComplete?.(pin)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pin])

  // Initial focus.
  // Auto-focus on mount AND when the user tab-backs to the page (so
  // they can paste IMMEDIATELY after returning from their email
  // client without having to click). Founder rule 2026-05-04.
  useEffect(() => {
    if (!autoFocus || disabled) return
    const el = inputRef.current
    if (!el) return
    el.focus()
    const onVisible = () => {
      if (document.visibilityState === 'visible' && !disabled && !el.disabled) {
        el.focus()
      }
    }
    const onWindowFocus = () => {
      if (!disabled && !el.disabled) {
        el.focus()
      }
    }
    document.addEventListener('visibilitychange', onVisible)
    window.addEventListener('focus', onWindowFocus)
    return () => {
      document.removeEventListener('visibilitychange', onVisible)
      window.removeEventListener('focus', onWindowFocus)
    }
  }, [autoFocus, disabled])

  const handleChange = useCallback((e: React.ChangeEvent<HTMLInputElement>) => {
    const cleaned = extractDigits(e.target.value).slice(0, N)
    setPin(cleaned)
  }, [])

  const focusInput = useCallback(() => {
    const el = inputRef.current
    if (!el) return
    el.focus()
    // Move cursor to the end so the next keystroke appends.
    const len = el.value.length
    el.setSelectionRange(len, len)
  }, [])

  // Render: row of 6 boxes (decorative) with a single transparent
  // input overlay capturing all interaction.
  return (
    <div
      role="group"
      aria-label="Sign-in code"
      className="relative flex items-center justify-center gap-2.5 sm:gap-3"
      onClick={focusInput}
      data-testid={testId}
    >
      {Array.from({ length: N }).map((_, i) => {
        const digit = pin[i] ?? ''
        const filled = digit !== ''
        const isActive = !disabled && pin.length === i // next-to-fill
        return (
          <div
            key={i}
            data-testid={`${testId}-${i}`}
            aria-label={`Digit ${i + 1}: ${digit || 'empty'}`}
            className={[
              'flex items-center justify-center select-none',
              'w-12 h-14 sm:w-14 sm:h-16',
              'text-center text-2xl sm:text-3xl font-semibold tabular-nums tracking-tight',
              'rounded-xl border-[1.5px] bg-[--color-surface-1] text-[--color-text-primary]',
              'shadow-[0_1px_0_oklch(100%_0_0/0.04),_inset_0_-1px_0_oklch(100%_0_0/0.02)]',
              filled
                ? 'border-[--color-brand-500]/70'
                : isActive
                  ? 'border-[--color-brand-500] ring-[3px] ring-[--color-brand-500]/30'
                  : 'border-[--color-surface-border]',
              'transition-[border-color,box-shadow,transform] duration-150 ease-out',
              disabled ? 'opacity-50' : '',
            ].join(' ')}
          >
            {digit}
          </div>
        )
      })}

      {/* Single real input — captures every keystroke + paste. Visually
          hidden via opacity:0 + transparent caret + same dimensions as
          the box row (so click events on boxes hit the input via
          pointer-events). z-index ensures clicks reach it before any
          decorative element. autoComplete=one-time-code on this single
          input is correct (iOS SMS autofill targets the focused
          input). */}
      <input
        ref={inputRef}
        type="text"
        inputMode="numeric"
        pattern="[0-9]*"
        autoComplete="one-time-code"
        maxLength={N}
        value={pin}
        onChange={handleChange}
        disabled={disabled}
        aria-label="Sign-in code (6 digits)"
        data-testid={`${testId}-input`}
        className="absolute inset-0 w-full h-full opacity-0 cursor-text"
        style={{
          // Disable browser autofill suggestions popping up over the
          // boxes — caret-color hides any caret artefact, font-size
          // matches box height so click targets are correct.
          caretColor: 'transparent',
          color: 'transparent',
          background: 'transparent',
          border: 0,
          outline: 0,
          padding: 0,
          fontSize: 'inherit',
          letterSpacing: 'inherit',
        }}
      />
    </div>
  )
}
