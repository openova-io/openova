/**
 * FailbackPanel.test.tsx — Vitest unit tests for the EPIC-6 Slice
 * U-DR-1 (#1101) failback handler UI.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

import { FailbackPanel } from './FailbackPanel'

afterEach(() => cleanup())

describe('FailbackPanel — gating', () => {
  it('shows "owner required" when caller is not owner', () => {
    render(
      <FailbackPanel
        sovereignId="dep-1"
        continuumName="dr-wp"
        isOwner={false}
        isSovereignAdmin={false}
        approvalRequired={false}
        failbackRequested={false}
        failbackApproved={false}
      />,
    )
    expect(screen.getByTestId('continuum-failback-request-disabled')).toBeTruthy()
  })

  it('shows the request button when caller is owner + nothing requested yet', () => {
    render(
      <FailbackPanel
        sovereignId="dep-1"
        continuumName="dr-wp"
        isOwner
        isSovereignAdmin={false}
        approvalRequired={false}
        failbackRequested={false}
        failbackApproved={false}
      />,
    )
    expect(screen.getByTestId('continuum-failback-request-btn')).toBeTruthy()
  })

  it('shows pending + approve button when approvalRequired and caller is sovereign-admin', () => {
    render(
      <FailbackPanel
        sovereignId="dep-1"
        continuumName="dr-wp"
        isOwner
        isSovereignAdmin
        approvalRequired
        failbackRequested
        failbackApproved={false}
      />,
    )
    expect(screen.getByTestId('continuum-failback-pending')).toBeTruthy()
    expect(screen.getByTestId('continuum-failback-approve-btn')).toBeTruthy()
  })

  it('hides approve button for non-sovereign-admin even when approvalRequired', () => {
    render(
      <FailbackPanel
        sovereignId="dep-1"
        continuumName="dr-wp"
        isOwner
        isSovereignAdmin={false}
        approvalRequired
        failbackRequested
        failbackApproved={false}
      />,
    )
    expect(screen.queryByTestId('continuum-failback-approve-btn')).toBeNull()
    expect(screen.getByTestId('continuum-failback-approve-hidden')).toBeTruthy()
  })

  it('shows in-progress message after approval / when no approval needed', () => {
    render(
      <FailbackPanel
        sovereignId="dep-1"
        continuumName="dr-wp"
        isOwner
        isSovereignAdmin={false}
        approvalRequired={false}
        failbackRequested
        failbackApproved={false}
      />,
    )
    expect(screen.getByTestId('continuum-failback-in-progress')).toBeTruthy()
  })
})

describe('FailbackPanel — actions', () => {
  it('clicking request fires onChanged when network is disabled', () => {
    const onChanged = vi.fn()
    render(
      <FailbackPanel
        sovereignId="dep-1"
        continuumName="dr-wp"
        isOwner
        isSovereignAdmin={false}
        approvalRequired={false}
        failbackRequested={false}
        failbackApproved={false}
        disableNetwork
        onChanged={onChanged}
      />,
    )
    fireEvent.click(screen.getByTestId('continuum-failback-request-btn'))
    expect(onChanged).toHaveBeenCalled()
  })

  it('clicking approve fires onChanged when network is disabled', () => {
    const onChanged = vi.fn()
    render(
      <FailbackPanel
        sovereignId="dep-1"
        continuumName="dr-wp"
        isOwner
        isSovereignAdmin
        approvalRequired
        failbackRequested
        failbackApproved={false}
        disableNetwork
        onChanged={onChanged}
      />,
    )
    fireEvent.click(screen.getByTestId('continuum-failback-approve-btn'))
    expect(onChanged).toHaveBeenCalled()
  })
})
