/**
 * EnterOrgButton.test.tsx — the B2 Enter-org action (issue #3378 DoD 6).
 * The button mints the support session + opens the handover URL; it is
 * hidden on the parent row ("you are already inside it", §5).
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { EnterOrgButton } from './EnterOrgButton'
import { parentRowFromSelf, subOrgRowFromRecord, type EnterOrgResult } from '@/lib/organizations.api'

afterEach(() => cleanup())

const SUB = subOrgRowFromRecord({
  id: 'tnt-1',
  orgName: 'ACME Corp',
  consoleHost: 'console.acme.omani.homes',
  subdomain: 'acme',
  parentDomain: 'omani.homes',
  plan: 'pro',
  kind: 'customer',
  tier: 'org',
  billingMode: 'real',
  isolation: 'vcluster',
  status: 'active',
  region: 'me-east-215-a',
  ownerEmail: 'owner@acme.example',
  createdAt: '2026-06-13T00:00:00Z',
  updatedAt: '2026-06-13T00:00:00Z',
  lastError: '',
})

const RESULT: EnterOrgResult = {
  handoverURL: 'https://console.acme.hw130.omantel.biz/auth/handover?token=abc',
  supportPrincipal: 'support+emrah@acme.hw130.omantel.biz',
  org: 'acme',
  expiresAt: new Date(Date.now() + 60 * 60 * 1000).toISOString(),
  auditEventId: 'enter-org-acme-xyz',
}

describe('EnterOrgButton — B2 support session (DoD 6)', () => {
  it('is hidden on the parent row (§5: already inside it)', () => {
    const parent = parentRowFromSelf({ deploymentId: 'd1', sovereignFQDN: 'hw130.omantel.biz' })
    const { container } = render(<EnterOrgButton org={parent} />)
    expect(container.querySelector('[data-testid="enter-org-button"]')).toBeNull()
  })

  it('mints the session and opens the handover URL on click', async () => {
    const onEnter = vi.fn(async () => RESULT)
    const open = vi.fn()
    render(<EnterOrgButton org={SUB} onEnterOverride={onEnter} openOverride={open} />)
    fireEvent.click(screen.getByTestId('enter-org-button'))
    await waitFor(() => expect(onEnter).toHaveBeenCalledWith('acme'))
    expect(open).toHaveBeenCalledWith(RESULT.handoverURL)
    // the support-principal + expiry confirmation surfaces
    expect(await screen.findByTestId('enter-org-confirm')).toBeTruthy()
    expect(screen.getByTestId('enter-org-confirm').textContent).toContain('support+emrah')
  })

  it('surfaces the error when the mint fails', async () => {
    const onEnter = vi.fn(async () => { throw new Error('insufficient-role') })
    render(<EnterOrgButton org={SUB} onEnterOverride={onEnter} openOverride={() => {}} />)
    fireEvent.click(screen.getByTestId('enter-org-button'))
    expect(await screen.findByTestId('enter-org-error')).toBeTruthy()
    expect(screen.getByTestId('enter-org-error').textContent).toContain('insufficient-role')
  })

  // #3378 enter-org gap: a still-provisioning sub-org has no live console,
  // so the button must be disabled (no blank-tab handover) with a clear
  // "not live yet" state, NOT open a blank tab.
  it('disables the button + explains when the org console is not live yet', () => {
    const provisioning = { ...SUB, status: 'provisioning' as const }
    const open = vi.fn()
    render(<EnterOrgButton org={provisioning} onEnterOverride={vi.fn()} openOverride={open} />)
    const btn = screen.getByTestId('enter-org-button') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
    expect(screen.getByTestId('enter-org-not-live').textContent).toContain('Console not live yet')
    // clicking a disabled button must not mint/open anything
    fireEvent.click(btn)
    expect(open).not.toHaveBeenCalled()
  })
})
