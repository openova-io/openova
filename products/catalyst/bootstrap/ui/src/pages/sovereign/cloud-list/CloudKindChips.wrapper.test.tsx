/**
 * CloudKindChips.wrapper.test.tsx — proves the thin `CloudKindChips`
 * wrapper (now delegating to the shared `KindChipStrip`) keeps the /cloud
 * surface's established testids and single-select behaviour unchanged after
 * the extract-to-shared refactor.
 */

import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { cleanup, render, screen, fireEvent } from '@testing-library/react'
import { CloudKindChips } from './CloudKindChips'
import { KINDS, type CloudListKind } from './kinds'

beforeEach(() => {
  try {
    window.localStorage.clear()
  } catch {
    /* noop */
  }
})
afterEach(cleanup)

function fullCounts(): Record<CloudListKind, number | null> {
  const c = {} as Record<CloudListKind, number | null>
  for (const k of KINDS) c[k.id] = 5
  return c
}

describe('CloudKindChips wrapper — established /cloud testids + single-select', () => {
  it('renders the cloud-kind-chips container and the primary chip testids', () => {
    render(<CloudKindChips activeKind="clusters" counts={fullCounts()} onChange={() => {}} />)
    expect(screen.getByTestId('cloud-kind-chips')).toBeTruthy()
    // The six primary chips keep their cloud-kind-chip-<id> testids.
    expect(screen.getByTestId('cloud-kind-chip-clusters')).toBeTruthy()
    expect(screen.getByTestId('cloud-kind-chip-vclusters')).toBeTruthy()
    expect(screen.getByTestId('cloud-kind-chip-node-pools')).toBeTruthy()
    // The overflow lives behind the +More popover with its cloud testids.
    fireEvent.click(screen.getByTestId('cloud-kind-chip-more'))
    expect(screen.getByTestId('cloud-kind-chip-more-popover')).toBeTruthy()
    expect(screen.getByTestId('cloud-kind-chip-more-item-pods')).toBeTruthy()
  })

  it('single-select: clicking a chip fires onChange with the CloudListKind id', () => {
    const onChange = vi.fn()
    render(<CloudKindChips activeKind="clusters" counts={fullCounts()} onChange={onChange} />)
    fireEvent.click(screen.getByTestId('cloud-kind-chip-vclusters'))
    expect(onChange).toHaveBeenCalledWith('vclusters')
  })

  it('the active chip has no remove affordance; a non-active one does (curate is on)', () => {
    render(<CloudKindChips activeKind="clusters" counts={fullCounts()} onChange={() => {}} />)
    // clusters is active → not removable; vclusters is a non-active inline chip.
    expect(screen.queryByTestId('cloud-kind-chip-clusters-remove')).toBeNull()
    expect(screen.getByTestId('cloud-kind-chip-vclusters-remove')).toBeTruthy()
  })
})
