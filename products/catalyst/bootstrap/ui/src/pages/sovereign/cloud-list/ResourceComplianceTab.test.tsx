/**
 * ResourceComplianceTab.test — #4085.
 *
 * Asserts the per-resource Compliance tab renders THIS resource's own
 * findings as a table (policy / severity / status / message) — and the
 * empty state when no policies have evaluated the resource yet.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { ResourceComplianceTab } from './ResourceComplianceTab'
import type { ResourceComplianceResponse } from '@/lib/compliance.api'

afterEach(() => cleanup())

function renderTab(initialData: ResourceComplianceResponse) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <ResourceComplianceTab
        sovereignId="dep"
        kind="deployment"
        ns="acme"
        name="web"
        initialData={initialData}
      />
    </QueryClientProvider>,
  )
}

const FOUND: ResourceComplianceResponse = {
  resource: 'deployment/acme/web',
  kind: 'deployment',
  namespace: 'acme',
  name: 'web',
  found: true,
  score: 50,
  total: 50,
  numerator: 1,
  denominator: 2,
  violations: 1,
  application: 'web',
  environment: 'acme-prod',
  findings: [
    {
      policy: 'disallow-privileged-containers',
      rule: 'privileged',
      result: 'fail',
      message: 'container runs privileged',
      severity: 'high',
      category: 'Pod Security Standards',
      source: 'kyverno',
    },
    {
      policy: 'require-run-as-nonroot',
      rule: 'nonroot',
      result: 'pass',
      severity: 'medium',
      source: 'kyverno',
    },
  ],
  updatedAt: '2026-06-22T00:00:00Z',
}

describe('ResourceComplianceTab', () => {
  it('renders a table of THIS resource own findings (policy/severity/status/message)', () => {
    renderTab(FOUND)
    // The findings table is present (not a redirect-only blurb).
    expect(screen.getByTestId('resource-compliance-table')).toBeTruthy()
    // Both per-policy rows render.
    expect(screen.getByTestId('resource-compliance-row-disallow-privileged-containers')).toBeTruthy()
    expect(screen.getByTestId('resource-compliance-row-require-run-as-nonroot')).toBeTruthy()
    // Status + severity + message for the failing row.
    expect(screen.getByTestId('resource-compliance-result-fail')).toBeTruthy()
    expect(screen.getByTestId('resource-compliance-severity-high')).toBeTruthy()
    expect(screen.getByText('container runs privileged')).toBeTruthy()
    // Score hero + violation count.
    expect(screen.getByTestId('resource-compliance-score-hero')).toBeTruthy()
    expect(screen.getByTestId('resource-compliance-violation-count').textContent).toContain('1 violation')
  })

  it('renders the empty state when no policies have evaluated the resource', () => {
    renderTab({ ...FOUND, found: false, score: null, total: null, findings: [], violations: 0 })
    expect(screen.getByTestId('resource-compliance-empty')).toBeTruthy()
    expect(screen.queryByTestId('resource-compliance-table')).toBeNull()
  })

  it('keeps the drill-out to the full Compliance dashboard as a secondary affordance', () => {
    renderTab(FOUND)
    const link = screen.getByTestId('resource-detail-compliance-drilldown') as HTMLAnchorElement
    expect(link.getAttribute('href')).toContain('/sre/compliance?resource=')
  })
})
