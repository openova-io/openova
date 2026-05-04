/**
 * PinInput6 — paste-friendly 6-box numeric PIN input (issue #688).
 *
 * UX modelled on bank / Google verification flows:
 *
 *   • 6 separate `<input maxLength=1 inputMode="numeric" pattern="[0-9]*">`
 *     boxes side-by-side. Each box accepts a single decimal digit.
 *   • Typing a digit auto-advances focus to the next box.
 *   • Backspace on an empty box moves focus to the previous box and
 *     clears it; backspace on a filled box clears the current box.
 *   • Pasting anywhere on the row extracts the first 6 digits from the
 *     clipboard (regex /\d/g), distributes them across the boxes,
 *     focuses the last filled box, and — if the paste filled all 6 —
 *     auto-submits the value.
 *   • Pressing Enter on a fully-filled row submits.
 *
 * The component is uncontrolled internally (each box has its own ref +
 * value) so reactivity stays inside the component; the parent only
 * receives `onComplete(pin: string)` when the row is full and
 * `onChange(pin: string)` for every keystroke.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (no hardcoded URLs / endpoints):
 * this component is purely presentational; the parent owns the fetch.
 */

import { useRef, useEffect, useState, useCallback, type KeyboardEvent, type ClipboardEvent, type ChangeEvent } from 'react'

export interface PinInput6Props {
  /** Number of boxes — fixed at 6 per the founder spec on #688. */
  length?: 6
  /** Whether the row is disabled (e.g. while submitting). */
  disabled?: boolean
  /** Auto-focus the first box on mount. Defaults to true. */
  autoFocus?: boolean
  /** Fires with the joined string on every keystroke. */
  onChange?: (pin: string) => void
  /** Fires once the 6th digit lands. Receives the full 6-digit string. */
  onComplete?: (pin: string) => void
  /** Pre-fill value (e.g. for E2E tests). String must contain ≤6 digits. */
  value?: string
  /**
   * data-testid prefix. Each box gets `${testId}-${index}`.
   * Defaults to "pin-box".
   */
  testId?: string
}

/**
 * Extract decimal digits from arbitrary text. Used by the paste handler
 * so a user pasting "Your code is 372 458." still drops "372458" into
 * the boxes.
 */
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
  const refs = useRef<Array<HTMLInputElement | null>>([])
  const [digits, setDigits] = useState<string[]>(() => {
    const init = Array<string>(N).fill('')
    if (value) {
      const seed = extractDigits(value).slice(0, N).split('')
      seed.forEach((d, i) => {
        init[i] = d
      })
    }
    return init
  })

  // Notify parent when digits change.
  useEffect(() => {
    const joined = digits.join('')
    onChange?.(joined)
    if (joined.length === N && digits.every((d) => d !== '')) {
      onComplete?.(joined)
    }
    // We intentionally don't include onChange/onComplete in the deps —
    // adding them would re-run on every parent render (function identity
    // changes). The contract is: notify on digit change.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [digits])

  // Initial focus.
  useEffect(() => {
    if (autoFocus && !disabled) {
      refs.current[0]?.focus()
    }
  }, [autoFocus, disabled])

  const setDigitAt = useCallback((index: number, value: string) => {
    setDigits((prev) => {
      if (prev[index] === value) return prev
      const next = prev.slice()
      next[index] = value
      return next
    })
  }, [])

  const focusBox = useCallback((index: number) => {
    const el = refs.current[index]
    if (el) {
      el.focus()
      // Selection-end by default so a re-focus doesn't surprise the user.
      el.setSelectionRange(el.value.length, el.value.length)
    }
  }, [])

  const handleChange = useCallback(
    (index: number) => (e: ChangeEvent<HTMLInputElement>) => {
      const raw = e.target.value
      // Accept exactly one digit. If the user typed multiple chars (e.g.
      // an autofill suggestion), distribute them across the remaining
      // boxes — same fan-out as the paste handler.
      const cleaned = extractDigits(raw)
      if (cleaned.length === 0) {
        setDigitAt(index, '')
        return
      }
      if (cleaned.length === 1) {
        setDigitAt(index, cleaned)
        if (index < N - 1) {
          focusBox(index + 1)
        }
        return
      }
      // Multiple digits — fan out across remaining boxes starting at index.
      setDigits((prev) => {
        const next = prev.slice()
        let i = index
        for (const d of cleaned.split('')) {
          if (i >= N) break
          next[i] = d
          i += 1
        }
        // Focus the last filled box (or the next empty one).
        const target = Math.min(i, N - 1)
        // Defer focus to next frame so React commits the change first.
        queueMicrotask(() => focusBox(target))
        return next
      })
    },
    [setDigitAt, focusBox],
  )

  const handleKeyDown = useCallback(
    (index: number) => (e: KeyboardEvent<HTMLInputElement>) => {
      const key = e.key
      if (key === 'Backspace') {
        // If current is empty, step back; otherwise clear current.
        if (digits[index] === '' && index > 0) {
          e.preventDefault()
          setDigitAt(index - 1, '')
          focusBox(index - 1)
        } else if (digits[index] !== '') {
          // Default behaviour: clear the current box (let the input do it).
        }
        return
      }
      if (key === 'ArrowLeft' && index > 0) {
        e.preventDefault()
        focusBox(index - 1)
        return
      }
      if (key === 'ArrowRight' && index < N - 1) {
        e.preventDefault()
        focusBox(index + 1)
        return
      }
      if (key === 'Enter') {
        const joined = digits.join('')
        if (joined.length === N && digits.every((d) => d !== '')) {
          onComplete?.(joined)
        }
        return
      }
      // Block letters / non-digit single chars at keystroke time so the
      // user gets immediate visual feedback rather than a silently
      // discarded change.
      if (key.length === 1 && (key < '0' || key > '9')) {
        e.preventDefault()
      }
    },
    [digits, setDigitAt, focusBox, onComplete],
  )

  // Paste handler — fan out a multi-digit clipboard payload across all
  // 6 boxes regardless of which box was the paste target. We do this in
  // ONE place (here on each input) instead of dual per-box + wrapper
  // handlers because the dual approach raced: the per-box handler's
  // preventDefault prevented the native paste, AND the bubbled
  // wrapper-level handler ran on the same event, both calling
  // setDigits — non-deterministic merge order in React 18 batched
  // updates left some boxes empty.
  //
  // Single path: per-box handler reads the clipboard text, fans out to
  // the digits array starting at the paste index, calls preventDefault
  // so the native paste doesn't ALSO write to the input. onChange is
  // unchanged and still handles single-character typing.
  const handlePaste = useCallback(
    (index: number) => (e: ClipboardEvent<HTMLInputElement>) => {
      const text = e.clipboardData.getData('text')
      const cleaned = extractDigits(text)
      if (cleaned.length === 0) return
      e.preventDefault()
      setDigits((prev) => {
        const next = prev.slice()
        let i = index
        for (const d of cleaned.slice(0, N - index).split('')) {
          if (i >= N) break
          next[i] = d
          i += 1
        }
        const target = Math.min(i, N - 1)
        queueMicrotask(() => focusBox(target))
        return next
      })
    },
    [focusBox],
  )

  const handleFocus = useCallback(
    (_index: number) => (e: React.FocusEvent<HTMLInputElement>) => {
      // Select the existing digit so re-typing replaces it.
      const el = e.currentTarget
      if (el.value.length > 0) {
        el.setSelectionRange(0, el.value.length)
      }
    },
    [],
  )

  // Wrapper-level paste handler — fires when the user pastes anywhere
  // inside the row, including the gaps between boxes. Same fan-out
  // logic as the per-input handler.
  const handleWrapperPaste = useCallback((e: ClipboardEvent<HTMLDivElement>) => {
    const text = e.clipboardData.getData('text')
    const cleaned = extractDigits(text).slice(0, N)
    if (cleaned.length === 0) return
    e.preventDefault()
    setDigits(() => {
      const next = Array<string>(N).fill('')
      for (let i = 0; i < cleaned.length; i += 1) {
        next[i] = cleaned.charAt(i)
      }
      const last = Math.min(cleaned.length, N) - 1
      queueMicrotask(() =>
        focusBox(Math.min(last + (cleaned.length < N ? 1 : 0), N - 1)),
      )
      return next
    })
  }, [focusBox])

  return (
    <div
      role="group"
      aria-label="Sign-in code"
      className="flex items-center justify-center gap-2.5 sm:gap-3"
      data-testid={testId}
      onPaste={handleWrapperPaste}
    >
      {Array.from({ length: N }).map((_, i) => {
        const filled = digits[i] !== ''
        return (
          <input
            key={i}
            ref={(el) => {
              refs.current[i] = el
            }}
            type="text"
            inputMode="numeric"
            pattern="[0-9]*"
            // Only the first box gets one-time-code so iOS SMS autofill
            // works without Chrome intercepting paste events on every
            // box (caught live 2026-05-04: pasting a 6-digit string
            // into any box silently dropped digits because Chrome's
            // SMS-autofill intercepted the paste event).
            autoComplete={i === 0 ? 'one-time-code' : 'off'}
            // maxLength=6 (NOT 1) so a paste of "123456" into any box
            // arrives intact in onChange — handleChange fans the chars
            // across the remaining boxes. With maxLength=1 the browser
            // truncated to a single char BEFORE handleChange ran, so
            // paste only ever filled one box.
            maxLength={6}
            value={digits[i]}
            disabled={disabled}
            onChange={handleChange(i)}
            onKeyDown={handleKeyDown(i)}
            onPaste={handlePaste(i)}
            onFocus={handleFocus(i)}
            data-testid={`${testId}-${i}`}
            aria-label={`Digit ${i + 1}`}
            className={[
              // iCloud-style: 56×64 box, 1.5px border, soft shadow,
              // larger digit, smooth focus ring, slight scale on focus.
              'w-14 h-16 sm:w-16 sm:h-[72px]',
              'text-center text-2xl sm:text-3xl font-semibold tabular-nums tracking-tight',
              'rounded-xl border-[1.5px] bg-[--color-surface-1] text-[--color-text-primary]',
              'shadow-[0_1px_0_oklch(100%_0_0/0.04),_inset_0_-1px_0_oklch(100%_0_0/0.02)]',
              filled
                ? 'border-[--color-brand-500]/70'
                : 'border-[--color-surface-border]',
              'focus:border-[--color-brand-500] focus:outline-none focus:ring-[3px] focus:ring-[--color-brand-500]/30 focus:scale-[1.04]',
              'transition-[border-color,box-shadow,transform] duration-150 ease-out',
              'disabled:opacity-50 disabled:cursor-not-allowed',
              'caret-transparent', // hide caret — the digit IS the caret
              // Reduce browser autofill background flash on Chrome / Safari
              '[&:-webkit-autofill]:bg-[--color-surface-1]',
            ].join(' ')}
          />
        )
      })}
    </div>
  )
}
