/**
 * SwitchoverDialog.test.tsx — Vitest unit tests for the EPIC-6 Slice
 * U-DR-1 (#1101) confirm dialog. Uses disableNetwork seam so no fetch
 * is required.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

import { SwitchoverDialog } from './SwitchoverDialog'
import { SWITCHOVER_STEPS } from '@/lib/continuum.api'

afterEach(() => cleanup())

describe('SwitchoverDialog', () => {
  it('renders the diff with from-region → to-region', () => {
    render(
      <SwitchoverDialog
        sovereignId="dep-1"
        continuumName="dr-wp"
        applicationName="wp-prod"
        fromRegion="hz-fsn-rtz-prod"
        toRegion="hz-hel-rtz-prod"
        onClose={() => {}}
        disableNetwork
      />,
    )
    const diff = screen.getByTestId('continuum-switchover-dialog-diff')
    expect(diff.textContent).toContain('hz-fsn-rtz-prod')
    expect(diff.textContent).toContain('hz-hel-rtz-prod')
  })

  it('renders all 7 steps from K-Cont-2 SWITCHOVER_STEPS', () => {
    render(
      <SwitchoverDialog
        sovereignId="dep-1"
        continuumName="dr-wp"
        applicationName="wp-prod"
        fromRegion="a"
        toRegion="b"
        onClose={() => {}}
        disableNetwork
      />,
    )
    for (const step of SWITCHOVER_STEPS) {
      expect(screen.getByTestId(`continuum-switchover-dialog-step-${step.id}`)).toBeTruthy()
    }
  })

  it('cancel calls onClose without triggering confirm', () => {
    const onClose = vi.fn()
    const onConfirmed = vi.fn()
    render(
      <SwitchoverDialog
        sovereignId="dep-1"
        continuumName="dr-wp"
        applicationName="wp-prod"
        fromRegion="a"
        toRegion="b"
        onClose={onClose}
        onConfirmed={onConfirmed}
        disableNetwork
      />,
    )
    fireEvent.click(screen.getByTestId('continuum-switchover-dialog-cancel'))
    expect(onClose).toHaveBeenCalled()
    expect(onConfirmed).not.toHaveBeenCalled()
  })

  it('confirm calls onConfirmed when network is disabled', () => {
    const onClose = vi.fn()
    const onConfirmed = vi.fn()
    render(
      <SwitchoverDialog
        sovereignId="dep-1"
        continuumName="dr-wp"
        applicationName="wp-prod"
        fromRegion="a"
        toRegion="b"
        onClose={onClose}
        onConfirmed={onConfirmed}
        disableNetwork
      />,
    )
    fireEvent.click(screen.getByTestId('continuum-switchover-dialog-confirm'))
    expect(onConfirmed).toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
  })

  it('renders estimated duration + write-disruption block', () => {
    render(
      <SwitchoverDialog
        sovereignId="dep-1"
        continuumName="dr-wp"
        applicationName="wp-prod"
        fromRegion="a"
        toRegion="b"
        onClose={() => {}}
        disableNetwork
      />,
    )
    const est = screen.getByTestId('continuum-switchover-dialog-estimates')
    expect(est.textContent).toContain('60s')
    expect(est.textContent).toContain('5s')
  })
})
