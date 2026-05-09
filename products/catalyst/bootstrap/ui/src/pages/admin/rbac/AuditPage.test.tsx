/**
 * AuditPage.test.tsx — vitest coverage for the U8 audit-trail row
 * renderer + ActionPill color mapping.
 */

import { cleanup, render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, describe, expect, it, vi } from 'vitest'

afterEach(() => cleanup())

import { ActionPill, AuditPage, AuditRow } from './AuditPage'
import type { AuditEvent, AuditListResponse } from './rbac.api'

vi.mock('@/pages/sovereign/PortalShell', () => ({
  PortalShell: ({ children }: { children: React.ReactNode }) => <div data-testid="shell">{children}</div>,
}))

vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: '' }),
}))

const ev = (over: Partial<AuditEvent>): AuditEvent => ({
  auditType: 'rbac-grant-created',
  ts: '2026-05-09T00:00:00Z',
  ...over,
})

function withQuery(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{ui}</QueryClientProvider>
}

describe('AuditPage', () => {
  it('renders empty-state when no events', () => {
    const audit: AuditListResponse = { items: [], total: 0 }
    render(withQuery(<AuditPage initialDeploymentId="dep-1" initialAudit={audit} disableStream />))
    expect(screen.getByTestId('audit-empty')).toBeTruthy()
  })

  it('renders one row per event', () => {
    const audit: AuditListResponse = {
      items: [
        ev({ ts: '2026-05-09T01:00:00Z', actor: 'alice@acme.io', userAccessRef: 'r1' }),
        ev({
          ts: '2026-05-09T00:00:00Z',
          auditType: 'rbac-tier-changed',
          actor: 'bob@acme.io',
          previousTier: 'viewer',
          tier: 'admin',
          userAccessRef: 'r2',
        }),
      ],
      total: 2,
    }
    render(withQuery(<AuditPage initialDeploymentId="dep-1" initialAudit={audit} disableStream />))
    expect(screen.getByTestId('audit-row-0')).toBeTruthy()
    expect(screen.getByTestId('audit-row-1')).toBeTruthy()
  })
})

// Wrap AuditRow in a <table> when rendering standalone — without
// the table parent React renders the <tr>'s children as direct
// document children in addition to the row, doubling test-id matches.
function renderAuditRow(ev: AuditEvent, index: number) {
  return render(
    <table>
      <tbody>
        <AuditRow ev={ev} index={index} />
      </tbody>
    </table>,
  )
}

describe('AuditRow', () => {
  it('renders the actor / target / tier / action columns', () => {
    const event = ev({
      auditType: 'rbac-grant-created',
      actor: 'alice@acme.io',
      targetUserEmail: 'bob@acme.io',
      targetApp: 'wordpress',
      tier: 'admin',
      userAccessRef: 'rbac-bob-wp',
    })
    renderAuditRow(event, 0)
    expect(screen.getByTestId('audit-row-0').textContent).toContain('alice@acme.io')
    expect(screen.getByTestId('audit-row-0').textContent).toContain('bob@acme.io')
    expect(screen.getByTestId('audit-row-0').textContent).toContain('wordpress')
    expect(screen.getByTestId('audit-tier-0').textContent).toContain('admin')
  })

  it('renders tier rotation arrow when previousTier differs', () => {
    const event = ev({
      auditType: 'rbac-tier-changed',
      previousTier: 'viewer',
      tier: 'admin',
    })
    renderAuditRow(event, 1)
    const tier = screen.getByTestId('audit-tier-1')
    expect(tier.textContent).toContain('viewer')
    expect(tier.textContent).toContain('→')
    expect(tier.textContent).toContain('admin')
  })

  it('falls back to "system" actor when none recorded', () => {
    const event = ev({ actor: undefined })
    renderAuditRow(event, 0)
    expect(screen.getByTestId('audit-row-0').textContent).toContain('system')
  })

  it('renders the global tag when no targetApp', () => {
    const event = ev({ targetApp: undefined })
    renderAuditRow(event, 0)
    expect(screen.getByTestId('audit-row-0').textContent).toContain('global')
  })
})

describe('ActionPill', () => {
  it('shows "Grant created" for rbac-grant-created', () => {
    render(<ActionPill auditType="rbac-grant-created" />)
    expect(screen.getByTestId('audit-action-pill-rbac-grant-created').textContent).toBe('Grant created')
  })
  it('shows "Tier rotated" for rbac-tier-changed', () => {
    render(<ActionPill auditType="rbac-tier-changed" />)
    expect(screen.getByTestId('audit-action-pill-rbac-tier-changed').textContent).toBe('Tier rotated')
  })
  it('falls back to the raw audit-type for unknown', () => {
    render(<ActionPill auditType="continuum-foo" />)
    expect(screen.getByTestId('audit-action-pill-continuum-foo').textContent).toBe('continuum-foo')
  })
})
