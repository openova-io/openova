/**
 * KindChipStrip.test.tsx — the ONE shared chip strip + its curate-visible-
 * chips behaviour (the founder's chosen curate UX, built into the single
 * component so BOTH /cloud and /jobs inherit it).
 *
 * Covered:
 *   • remove ✕ → the chip leaves the inline strip AND appears in `+ More`
 *     (with a re-add affordance);
 *   • re-add (+) → the chip returns inline;
 *   • the removed-set persists across a remount (localStorage);
 *   • the currently-active chip can NEVER be removed (no ✕ affordance);
 *   • the affordances are real, aria-labelled buttons (keyboard-accessible).
 *
 * A small synthetic catalogue keeps primary/overflow membership explicit so
 * the assertions do not depend on the real Cloud/Jobs catalogues.
 */

import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { cleanup, render, screen, fireEvent } from '@testing-library/react'
import { KindChipStrip, type KindChipEntry } from './KindChipStrip'

type K = 'a' | 'b' | 'c' | 'x'

const CATALOGUE: ReadonlyArray<KindChipEntry<K>> = [
  { id: 'a', label: 'Alpha', icon: 'M0 0', category: 'compute', primary: true, hasData: true },
  { id: 'b', label: 'Bravo', icon: 'M0 0', category: 'compute', primary: true, hasData: true },
  { id: 'c', label: 'Charlie', icon: 'M0 0', category: 'network', primary: true, hasData: true },
  { id: 'x', label: 'Xray', icon: 'M0 0', category: 'storage', primary: false, hasData: true },
]

const STORAGE_KEY = 'test-kindstrip-hidden'
const PREFIX = 'test-kind'

function allCounts(): Record<K, number | null> {
  return { a: 3, b: 4, c: 5, x: 6 }
}

function renderStrip(props?: {
  activeKind?: K | null
  counts?: Record<K, number | null>
  onChange?: (k: K) => void
}) {
  return render(
    <KindChipStrip<K>
      catalogue={CATALOGUE}
      activeKind={props?.activeKind ?? 'a'}
      counts={props?.counts ?? allCounts()}
      onChange={props?.onChange ?? (() => {})}
      testidPrefix={PREFIX}
      storageKey={STORAGE_KEY}
    />,
  )
}

function openMore() {
  fireEvent.click(screen.getByTestId(`${PREFIX}-chip-more`))
}

beforeEach(() => {
  try {
    window.localStorage.clear()
  } catch {
    /* noop */
  }
})
afterEach(cleanup)

describe('KindChipStrip — base render', () => {
  it('renders inline primary chips and the overflow in + More; count-0 non-active hidden', () => {
    renderStrip({ activeKind: 'a', counts: { a: 3, b: 0, c: 5, x: 6 } })
    // a (active), c (count>0) inline; b is primary but count 0 + non-active → hidden.
    expect(screen.getByTestId(`${PREFIX}-chip-a`)).toBeTruthy()
    expect(screen.queryByTestId(`${PREFIX}-chip-b`)).toBeNull()
    expect(screen.getByTestId(`${PREFIX}-chip-c`)).toBeTruthy()
    // x is natural overflow → in the popover.
    openMore()
    expect(screen.getByTestId(`${PREFIX}-chip-more-item-x`)).toBeTruthy()
  })

  it('single-select: clicking a chip fires onChange with its id', () => {
    const onChange = vi.fn()
    renderStrip({ activeKind: 'a', onChange })
    fireEvent.click(screen.getByTestId(`${PREFIX}-chip-c`))
    expect(onChange).toHaveBeenCalledWith('c')
  })
})

describe('KindChipStrip — curate visible chips', () => {
  it('remove ✕ → the chip leaves the inline strip and appears in + More with a re-add affordance', () => {
    renderStrip({ activeKind: 'a' })
    // b is a non-active inline chip → it has a ✕ remove affordance.
    const removeBtn = screen.getByTestId(`${PREFIX}-chip-b-remove`)
    expect(removeBtn).toBeTruthy()
    fireEvent.click(removeBtn)
    // b is gone from the inline strip …
    expect(screen.queryByTestId(`${PREFIX}-chip-b`)).toBeNull()
    // … and now lives in the popover, distinguished by a re-add (+) button.
    openMore()
    expect(screen.getByTestId(`${PREFIX}-chip-more-item-b`)).toBeTruthy()
    expect(screen.getByTestId(`${PREFIX}-chip-more-item-b-add`)).toBeTruthy()
    // The natural overflow (x) is still there, WITHOUT a re-add affordance.
    expect(screen.getByTestId(`${PREFIX}-chip-more-item-x`)).toBeTruthy()
    expect(screen.queryByTestId(`${PREFIX}-chip-more-item-x-add`)).toBeNull()
    // Persisted as a JSON array of ids.
    expect(window.localStorage.getItem(STORAGE_KEY)).toBe(JSON.stringify(['b']))
  })

  it('re-add (+) → the chip returns to the inline strip', () => {
    renderStrip({ activeKind: 'a' })
    fireEvent.click(screen.getByTestId(`${PREFIX}-chip-b-remove`))
    expect(screen.queryByTestId(`${PREFIX}-chip-b`)).toBeNull()
    openMore()
    fireEvent.click(screen.getByTestId(`${PREFIX}-chip-more-item-b-add`))
    // b is inline again.
    expect(screen.getByTestId(`${PREFIX}-chip-b`)).toBeTruthy()
    // And the removed-set is emptied in storage.
    expect(window.localStorage.getItem(STORAGE_KEY)).toBe(JSON.stringify([]))
  })

  it('the removed-set persists across a remount', () => {
    const first = renderStrip({ activeKind: 'a' })
    fireEvent.click(screen.getByTestId(`${PREFIX}-chip-b-remove`))
    expect(window.localStorage.getItem(STORAGE_KEY)).toBe(JSON.stringify(['b']))
    first.unmount()
    // Fresh mount reads the persisted removed-set: b is still curated out.
    renderStrip({ activeKind: 'a' })
    expect(screen.queryByTestId(`${PREFIX}-chip-b`)).toBeNull()
    openMore()
    expect(screen.getByTestId(`${PREFIX}-chip-more-item-b-add`)).toBeTruthy()
  })

  it('NEVER allows removing the currently-active chip (no ✕ on the active chip)', () => {
    renderStrip({ activeKind: 'a' })
    // The active chip has no remove affordance …
    expect(screen.queryByTestId(`${PREFIX}-chip-a-remove`)).toBeNull()
    // … while the non-active inline chips do.
    expect(screen.getByTestId(`${PREFIX}-chip-b-remove`)).toBeTruthy()
    expect(screen.getByTestId(`${PREFIX}-chip-c-remove`)).toBeTruthy()
  })

  it('a user-removed kind that becomes active is shown inline again (active override)', () => {
    // Pre-seed b as removed.
    window.localStorage.setItem(STORAGE_KEY, JSON.stringify(['b']))
    renderStrip({ activeKind: 'b' })
    // b is active → shown inline despite being in the removed-set …
    const chip = screen.getByTestId(`${PREFIX}-chip-b`)
    expect(chip.getAttribute('data-active')).toBe('true')
    // … and being active, it is not removable.
    expect(screen.queryByTestId(`${PREFIX}-chip-b-remove`)).toBeNull()
  })

  it('the remove + re-add affordances are aria-labelled buttons (keyboard-accessible)', () => {
    renderStrip({ activeKind: 'a' })
    const removeBtn = screen.getByTestId(`${PREFIX}-chip-b-remove`)
    expect(removeBtn.tagName.toLowerCase()).toBe('button')
    expect(removeBtn.getAttribute('aria-label')).toContain('Bravo')
    fireEvent.click(removeBtn)
    openMore()
    const addBtn = screen.getByTestId(`${PREFIX}-chip-more-item-b-add`)
    expect(addBtn.tagName.toLowerCase()).toBe('button')
    expect(addBtn.getAttribute('aria-label')).toContain('Bravo')
  })

  it('is safe when localStorage throws (private-mode) — renders the default strip', () => {
    const getItem = vi
      .spyOn(Storage.prototype, 'getItem')
      .mockImplementation(() => {
        throw new Error('denied')
      })
    // Should not throw; renders inline chips normally.
    expect(() => renderStrip({ activeKind: 'a' })).not.toThrow()
    expect(screen.getByTestId(`${PREFIX}-chip-a`)).toBeTruthy()
    expect(screen.getByTestId(`${PREFIX}-chip-b`)).toBeTruthy()
    getItem.mockRestore()
  })
})
