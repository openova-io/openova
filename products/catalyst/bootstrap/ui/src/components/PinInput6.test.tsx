/**
 * PinInput6.test.tsx — paste-friendly 6-digit PIN input (issue #688).
 *
 * What we assert:
 *   - Typing one digit per box auto-advances focus.
 *   - Pasting "123456" anywhere splits across all 6 boxes.
 *   - Pasting "Your code is 372 458." extracts only the digits.
 *   - Pasting more than 6 digits keeps only the first 6.
 *   - Pasting alphanumerics drops the letters.
 *   - Backspace on an empty box steps back; on a filled box clears.
 *   - Enter submits when all 6 boxes are filled.
 *   - onComplete fires once on the 6th digit.
 *   - Initial focus lands on the first box when autoFocus is true.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { PinInput6 } from './PinInput6'

afterEach(() => cleanup())

function getBoxes(): HTMLInputElement[] {
  const out: HTMLInputElement[] = []
  for (let i = 0; i < 6; i += 1) {
    out.push(screen.getByTestId(`pin-box-${i}`) as HTMLInputElement)
  }
  return out
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

  it('auto-focuses the overlay input on mount', () => {
    render(<PinInput6 />)
    const input = screen.getByTestId('pin-box-input') as HTMLInputElement
    expect(document.activeElement).toBe(input)
  })

  it('typing a digit advances focus to the next box', () => {
    render(<PinInput6 />)
    const boxes = getBoxes()
    fireEvent.change(boxes[0], { target: { value: '3' } })
    expect(boxes[0].value).toBe('3')
    expect(document.activeElement).toBe(boxes[1])
  })

  it('typing a non-digit is dropped', () => {
    render(<PinInput6 />)
    const boxes = getBoxes()
    // onChange is the path we filter — letters are stripped before set.
    fireEvent.change(boxes[0], { target: { value: 'a' } })
    expect(boxes[0].value).toBe('')
  })

  it('Backspace on empty box steps back to the previous box', () => {
    render(<PinInput6 />)
    const boxes = getBoxes()
    fireEvent.change(boxes[0], { target: { value: '3' } })
    fireEvent.change(boxes[1], { target: { value: '7' } })
    expect(document.activeElement).toBe(boxes[2])
    // boxes[2] is empty — backspace should step back to boxes[1] and clear.
    fireEvent.keyDown(boxes[2], { key: 'Backspace' })
    expect(document.activeElement).toBe(boxes[1])
    expect(boxes[1].value).toBe('')
  })
})

describe('PinInput6 — paste', () => {
  it('pasting "123456" fills all 6 boxes', () => {
    const onComplete = vi.fn()
    render(<PinInput6 onComplete={onComplete} />)
    const boxes = getBoxes()
    fireEvent.paste(boxes[0], {
      clipboardData: {
        getData: () => '123456',
      },
    })
    expect(boxes.map((b) => b.value)).toEqual(['1', '2', '3', '4', '5', '6'])
    expect(onComplete).toHaveBeenCalledWith('123456')
  })

  it('pasting "Your code is 372 458." extracts the digits only', () => {
    render(<PinInput6 />)
    const boxes = getBoxes()
    fireEvent.paste(boxes[0], {
      clipboardData: {
        getData: () => 'Your code is 372 458.',
      },
    })
    expect(boxes.map((b) => b.value)).toEqual(['3', '7', '2', '4', '5', '8'])
  })

  it('pasting 7+ digits keeps only the first 6', () => {
    render(<PinInput6 />)
    const boxes = getBoxes()
    fireEvent.paste(boxes[0], {
      clipboardData: {
        getData: () => '1234567890',
      },
    })
    expect(boxes.map((b) => b.value)).toEqual(['1', '2', '3', '4', '5', '6'])
  })

  it('pasting alphanumerics extracts only digits', () => {
    render(<PinInput6 />)
    const boxes = getBoxes()
    fireEvent.paste(boxes[0], {
      clipboardData: {
        getData: () => 'abc1d2e3-XX4Y5Z6',
      },
    })
    expect(boxes.map((b) => b.value)).toEqual(['1', '2', '3', '4', '5', '6'])
  })

  it('pasting fewer than 6 digits fills from the left and stops', () => {
    render(<PinInput6 />)
    const boxes = getBoxes()
    fireEvent.paste(boxes[0], {
      clipboardData: {
        getData: () => '372',
      },
    })
    expect(boxes.map((b) => b.value)).toEqual(['3', '7', '2', '', '', ''])
  })

  it('paste anywhere on the row (not just first box) still distributes from box 0', () => {
    render(<PinInput6 />)
    const boxes = getBoxes()
    // Paste while box 3 has focus — should still spread from index 0.
    fireEvent.paste(boxes[3], {
      clipboardData: {
        getData: () => '123456',
      },
    })
    expect(boxes.map((b) => b.value)).toEqual(['1', '2', '3', '4', '5', '6'])
  })
})

describe('PinInput6 — submit', () => {
  it('onComplete fires once when the 6th digit lands via typing', () => {
    const onComplete = vi.fn()
    render(<PinInput6 onComplete={onComplete} />)
    const boxes = getBoxes()
    const digits = ['3', '7', '2', '4', '5', '8']
    digits.forEach((d, i) => {
      fireEvent.change(boxes[i], { target: { value: d } })
    })
    expect(onComplete).toHaveBeenCalledWith('372458')
    expect(onComplete).toHaveBeenCalledTimes(1)
  })

  it('Enter on a filled row triggers onComplete', () => {
    const onComplete = vi.fn()
    render(<PinInput6 onComplete={onComplete} />)
    const boxes = getBoxes()
    const digits = ['3', '7', '2', '4', '5', '8']
    digits.forEach((d, i) => {
      fireEvent.change(boxes[i], { target: { value: d } })
    })
    onComplete.mockClear()
    fireEvent.keyDown(boxes[5], { key: 'Enter' })
    expect(onComplete).toHaveBeenCalledWith('372458')
  })

  it('onChange fires on every keystroke with the joined value', () => {
    const onChange = vi.fn()
    render(<PinInput6 onChange={onChange} />)
    const boxes = getBoxes()
    fireEvent.change(boxes[0], { target: { value: '3' } })
    fireEvent.change(boxes[1], { target: { value: '7' } })
    // initial mount fires onChange once with empty string
    expect(onChange).toHaveBeenCalledWith('')
    expect(onChange).toHaveBeenCalledWith('3')
    expect(onChange).toHaveBeenCalledWith('37')
  })
})

describe('PinInput6 — controlled-ish initial value', () => {
  it('respects the value prop on initial render', () => {
    render(<PinInput6 value="372458" />)
    const boxes = getBoxes()
    expect(boxes.map((b) => b.value)).toEqual(['3', '7', '2', '4', '5', '8'])
  })

  it('strips non-digits in the value prop', () => {
    render(<PinInput6 value="3-7-2 458" />)
    const boxes = getBoxes()
    expect(boxes.map((b) => b.value)).toEqual(['3', '7', '2', '4', '5', '8'])
  })
})

describe('PinInput6 — disabled', () => {
  it('marks all boxes disabled and skips auto-focus', () => {
    render(<PinInput6 disabled />)
    const boxes = getBoxes()
    for (const b of boxes) {
      expect(b.disabled).toBe(true)
    }
    expect(document.activeElement).not.toBe(boxes[0])
  })
})
