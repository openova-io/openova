/**
 * PinInput6.test.tsx — paste-friendly 6-digit PIN input (issue #688).
 *
 * ARCHITECTURE NOTE (why this file was migrated — #5404)
 * -----------------------------------------------------
 * PinInput6 was re-architected (see the docblock at PinInput6.tsx:1-31,
 * founder rule "paste must just work" 2026-05-04) from *6 real inputs*
 * to **ONE real `<input maxLength=6>` overlaid on 6 DECORATIVE boxes**:
 *
 *   - `pin-box-input` is the only focusable/editable element. Every
 *     keystroke, deletion and paste flows through it (PinInput6.tsx:161-187).
 *   - `pin-box-{0..5}` are `<div>`s that MIRROR the digits — they render
 *     `{digit}` as text (PinInput6.tsx:149) and carry
 *     `aria-label="Digit N: <digit|empty>"` (PinInput6.tsx:133). They have
 *     no `.value` and no `.disabled`.
 *
 * So the assertions below read box **text**, not `.value`, and drive all
 * interaction through the single overlay input.
 *
 * What we assert (the behaviours the original file was written to protect):
 *   - Typing a digit mirrors into the box and advances the "next-to-fill"
 *     highlight (the surviving expression of the old auto-advance).
 *   - Pasting "123456" anywhere on the row fills all 6 boxes.
 *   - Pasting "Your code is 372 458." extracts only the digits.
 *   - Pasting more than 6 digits keeps only the first 6.
 *   - Pasting alphanumerics drops the letters.
 *   - Deleting (Backspace) clears the last digit and steps the highlight back.
 *   - Enter does NOT double-fire onComplete — submission is the parent
 *     form's job now (VerifyPinPage.tsx:245-252, PinSignInModal.tsx:369).
 *   - onComplete fires once on the 6th digit.
 *   - Initial focus lands on the overlay input when autoFocus is true.
 *   - Clicking any decorative box focuses the overlay input.
 *   - disabled marks the real input disabled and skips auto-focus.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { PinInput6 } from './PinInput6'

afterEach(() => cleanup())

/** The 6 decorative mirror boxes (divs — no value/disabled of their own). */
function getBoxes(): HTMLElement[] {
  const out: HTMLElement[] = []
  for (let i = 0; i < 6; i += 1) {
    out.push(screen.getByTestId(`pin-box-${i}`))
  }
  return out
}

/** Digits currently rendered by the 6 mirror boxes. */
function boxDigits(): string[] {
  return getBoxes().map((b) => b.textContent ?? '')
}

/** The single real input that owns every keystroke + paste. */
function overlayInput(): HTMLInputElement {
  return screen.getByTestId('pin-box-input') as HTMLInputElement
}

/**
 * Index of the box carrying the "next-to-fill" ring, or -1.
 *
 * PinInput6.tsx:128 computes `isActive = !disabled && pin.length === i`
 * and line 143 renders it as `ring-[3px]`. With one shared input there is
 * no per-box DOM focus any more, so this ring IS the auto-advance the
 * original test asserted via `document.activeElement`.
 */
function activeBoxIndex(): number {
  return getBoxes().findIndex((b) => b.className.includes('ring-[3px]'))
}

/**
 * Simulate what the browser does on paste (or on any native edit).
 *
 * PinInput6 deliberately has NO paste handler (PinInput6.tsx:16-27): the
 * browser writes the pasted text into the ONE real input itself and React
 * sees exactly one `change`. Firing a synthetic `paste` event would be a
 * no-op against this component, so the faithful simulation of the shipped
 * design is the `change` the browser produces.
 */
function nativeEdit(value: string) {
  fireEvent.change(overlayInput(), { target: { value } })
}

describe('PinInput6 — typing', () => {
  it('renders 6 decorative boxes + 1 overlay input with inputmode="numeric" and maxlength=6', () => {
    render(<PinInput6 />)
    const boxes = getBoxes()
    expect(boxes).toHaveLength(6)
    const input = screen.getByTestId('pin-box-input') as HTMLInputElement
    expect(input.getAttribute('inputmode')).toBe('numeric')
    expect(input.getAttribute('maxlength')).toBe('6')
  })

  it('the decorative boxes are not inputs — the overlay input is the only editable element', () => {
    render(<PinInput6 />)
    // Guards the re-architecture at PinInput6.tsx:117-152: if a box ever
    // becomes an <input> again the single-input paste contract is broken.
    for (const b of getBoxes()) {
      expect(b.tagName).toBe('DIV')
    }
    expect(overlayInput().tagName).toBe('INPUT')
  })

  it('auto-focuses the overlay input on mount', () => {
    render(<PinInput6 />)
    const input = screen.getByTestId('pin-box-input') as HTMLInputElement
    expect(document.activeElement).toBe(input)
  })

  it('typing a digit mirrors it into box 0 and advances the next-to-fill box', () => {
    render(<PinInput6 />)
    expect(activeBoxIndex()).toBe(0)
    nativeEdit('3')
    expect(boxDigits()).toEqual(['3', '', '', '', '', ''])
    // Old design moved DOM focus to box 1; new design keeps focus on the
    // single input and moves the next-to-fill ring instead.
    expect(activeBoxIndex()).toBe(1)
    expect(document.activeElement).toBe(overlayInput())
  })

  it('exposes each digit on the mirror box aria-label', () => {
    render(<PinInput6 />)
    nativeEdit('37')
    // PinInput6.tsx:133 — the boxes are the accessible surface since the
    // overlay input is visually hidden.
    expect(getBoxes()[0].getAttribute('aria-label')).toBe('Digit 1: 3')
    expect(getBoxes()[1].getAttribute('aria-label')).toBe('Digit 2: 7')
    expect(getBoxes()[2].getAttribute('aria-label')).toBe('Digit 3: empty')
  })

  it('typing a non-digit is dropped', () => {
    render(<PinInput6 />)
    // extractDigits (PinInput6.tsx:45-47) filters in handleChange:102.
    nativeEdit('a')
    expect(boxDigits()).toEqual(['', '', '', '', '', ''])
    expect(overlayInput().value).toBe('')
  })

  it('Backspace clears the last digit and steps the next-to-fill box back', () => {
    render(<PinInput6 />)
    nativeEdit('3')
    nativeEdit('37')
    expect(activeBoxIndex()).toBe(2)
    // The overlay input owns deletion natively: Backspace at the end of the
    // value drops the last character and fires `input`. jsdom implements no
    // native editing, so we hand it the value the browser would produce and
    // assert the component's response.
    nativeEdit('3')
    expect(boxDigits()).toEqual(['3', '', '', '', '', ''])
    expect(activeBoxIndex()).toBe(1)
  })

  it('clicking a decorative box focuses the overlay input', () => {
    render(<PinInput6 />)
    const input = overlayInput()
    input.blur()
    expect(document.activeElement).not.toBe(input)
    // PinInput6.tsx:122 — onClick={focusInput} on the wrapper; clicks on any
    // box bubble to it (PinInput6.tsx:10, 106-113).
    fireEvent.click(getBoxes()[3])
    expect(document.activeElement).toBe(input)
  })
})

describe('PinInput6 — paste', () => {
  it('pasting "123456" fills all 6 boxes', () => {
    const onComplete = vi.fn()
    render(<PinInput6 onComplete={onComplete} />)
    nativeEdit('123456')
    expect(boxDigits()).toEqual(['1', '2', '3', '4', '5', '6'])
    expect(onComplete).toHaveBeenCalledWith('123456')
  })

  // ⚠️ BROWSER CAVEAT for the three "noisy paste" cases below.
  // The overlay input carries maxLength={6} (PinInput6.tsx:167). Real
  // browsers truncate pasted text to maxlength BEFORE firing `input`, so in
  // Chrome/Firefox "Your code is 372 458." arrives as "Your c" and no digits
  // land. jsdom does not enforce maxlength, so these tests pin the
  // component's extraction contract (extractDigits PinInput6.tsx:45-47 +
  // handleChange:102) — which is exactly the logic that exists to serve
  // these cases — and NOT the end-to-end browser paste. Reported as a
  // separate defect; the cap is redundant (the input is controlled by `pin`,
  // already sliced to 6 at line 102), so the fix is to drop maxLength.
  it('pasting "Your code is 372 458." extracts the digits only', () => {
    render(<PinInput6 />)
    nativeEdit('Your code is 372 458.')
    expect(boxDigits()).toEqual(['3', '7', '2', '4', '5', '8'])
  })

  it('pasting 7+ digits keeps only the first 6', () => {
    render(<PinInput6 />)
    nativeEdit('1234567890')
    expect(boxDigits()).toEqual(['1', '2', '3', '4', '5', '6'])
  })

  it('pasting alphanumerics extracts only digits', () => {
    render(<PinInput6 />)
    nativeEdit('abc1d2e3-XX4Y5Z6')
    expect(boxDigits()).toEqual(['1', '2', '3', '4', '5', '6'])
  })

  it('pasting fewer than 6 digits fills from the left and stops', () => {
    render(<PinInput6 />)
    nativeEdit('372')
    expect(boxDigits()).toEqual(['3', '7', '2', '', '', ''])
    expect(activeBoxIndex()).toBe(3)
  })

  it('paste anywhere on the row (not just the first box) still distributes from box 0', () => {
    render(<PinInput6 />)
    // Old design: 6 inputs, so a paste could land on box 3. New design: any
    // click on the row focuses the ONE input (PinInput6.tsx:122), so the
    // paste always lands there and always fills from index 0.
    fireEvent.click(getBoxes()[3])
    expect(document.activeElement).toBe(overlayInput())
    nativeEdit('123456')
    expect(boxDigits()).toEqual(['1', '2', '3', '4', '5', '6'])
  })
})

describe('PinInput6 — submit', () => {
  it('onComplete fires once when the 6th digit lands via typing', () => {
    const onComplete = vi.fn()
    render(<PinInput6 onComplete={onComplete} />)
    const digits = ['3', '7', '2', '4', '5', '8']
    digits.forEach((_, i) => {
      nativeEdit(digits.slice(0, i + 1).join(''))
    })
    expect(onComplete).toHaveBeenCalledWith('372458')
    expect(onComplete).toHaveBeenCalledTimes(1)
  })

  it('Enter on a filled row does not re-fire onComplete (the parent form submits)', () => {
    const onComplete = vi.fn()
    render(<PinInput6 onComplete={onComplete} />)
    nativeEdit('372458')
    expect(onComplete).toHaveBeenCalledWith('372458')
    onComplete.mockClear()
    // PinInput6 has no key handler at all any more — it is presentational
    // (PinInput6.tsx:29-30, INVIOLABLE #4). Enter-to-submit now comes from
    // the wrapping <form> in the consumers (VerifyPinPage.tsx:245-252,
    // PinSignInModal.tsx:369-454), so the component must NOT fire a second
    // onComplete — a one-time PIN double-submit would be a real bug.
    fireEvent.keyDown(overlayInput(), { key: 'Enter' })
    expect(onComplete).not.toHaveBeenCalled()
  })

  it('onChange fires on every keystroke with the joined value', () => {
    const onChange = vi.fn()
    render(<PinInput6 onChange={onChange} />)
    nativeEdit('3')
    nativeEdit('37')
    // initial mount fires onChange once with empty string
    expect(onChange).toHaveBeenCalledWith('')
    expect(onChange).toHaveBeenCalledWith('3')
    expect(onChange).toHaveBeenCalledWith('37')
  })
})

describe('PinInput6 — controlled-ish initial value', () => {
  it('respects the value prop on initial render', () => {
    render(<PinInput6 value="372458" />)
    expect(boxDigits()).toEqual(['3', '7', '2', '4', '5', '8'])
    expect(overlayInput().value).toBe('372458')
  })

  it('strips non-digits in the value prop', () => {
    render(<PinInput6 value="3-7-2 458" />)
    expect(boxDigits()).toEqual(['3', '7', '2', '4', '5', '8'])
  })
})

describe('PinInput6 — disabled', () => {
  it('disables the overlay input, dims every box and skips auto-focus', () => {
    render(<PinInput6 disabled />)
    // The real control is the single overlay input (PinInput6.tsx:170).
    const input = overlayInput()
    expect(input.disabled).toBe(true)
    // The boxes express disabled as the dimmed style (PinInput6.tsx:146)
    // and drop the next-to-fill ring (PinInput6.tsx:128 `!disabled && …`).
    for (const b of getBoxes()) {
      expect(b.className).toContain('opacity-50')
    }
    expect(activeBoxIndex()).toBe(-1)
    // autoFocus is skipped while disabled (PinInput6.tsx:79).
    expect(document.activeElement).not.toBe(input)
  })
})
