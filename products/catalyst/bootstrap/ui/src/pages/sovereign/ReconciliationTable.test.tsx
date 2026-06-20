/**
 * ReconciliationTable.test.tsx — locks the scientific-table contract the
 * founder asked for ("similar to jobs view … filterable and sortable …
 * must contain non-flux items"): search, the Class/Kind/State filters, the
 * sortable column headers, and the attention-first default order.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent, within } from '@testing-library/react'
import { ReconciliationTable } from './ReconciliationTable'
import { compareReconNodes, matchReconNode } from './reconciliation.shared'
import type { ReconciliationNode } from '@/lib/reconciliation.api'

afterEach(cleanup)

const NODES: ReconciliationNode[] = [
  { id: 'bp-cilium', label: 'bp-cilium', kind: 'HelmRelease', state: 'Reconciled' },
  { id: 'bp-keycloak', label: 'bp-keycloak', kind: 'HelmRelease', state: 'Reconciled', dependsOn: ['bp-cilium'] },
  { id: 'bp-newapi', label: 'bp-newapi', kind: 'HelmRelease', state: 'Degraded', dependsOn: ['bp-keycloak'] },
  { id: 'kustomization/bootstrap-kit', label: 'Bootstrap', kind: 'Kustomization', state: 'Reconciled' },
  { id: 'certificate/catalyst-system/wildcard', label: 'wildcard', kind: 'Certificate', state: 'Reconciling', message: 'issuing' },
  { id: 'cluster/cnpg/shared-pg-a', label: 'shared-pg-a', kind: 'Cluster', state: 'Drifted', message: 'Cluster in healthy state' },
  { id: 'externalsecret/openbao/kc-creds', label: 'kc-creds', kind: 'ExternalSecret', state: 'Reconciled' },
]

describe('ReconciliationTable pure helpers', () => {
  it('matchReconNode searches name/kind/state/message/deps', () => {
    const n = NODES[4] // certificate, message "issuing"
    expect(matchReconNode(n, 'wild')).toBe(true)
    expect(matchReconNode(n, 'certificate')).toBe(true)
    expect(matchReconNode(n, 'reconciling')).toBe(true)
    expect(matchReconNode(n, 'issuing')).toBe(true)
    expect(matchReconNode(NODES[2], 'bp-keycloak')).toBe(true) // via dependsOn
    expect(matchReconNode(n, 'zzz')).toBe(false)
    expect(matchReconNode(n, '')).toBe(true)
  })

  it('compareReconNodes default state-priority surfaces Degraded before Reconciled', () => {
    const a = NODES[2] // Degraded
    const b = NODES[0] // Reconciled
    expect(compareReconNodes(a, b, 'state', 'asc')).toBeLessThan(0)
    expect(compareReconNodes(b, a, 'state', 'asc')).toBeGreaterThan(0)
    // desc flips it
    expect(compareReconNodes(a, b, 'state', 'desc')).toBeGreaterThan(0)
  })
})

describe('ReconciliationTable component', () => {
  it('renders one row per reconciler incl. non-Flux kinds, attention-first', () => {
    render(<ReconciliationTable nodes={NODES} />)
    const rows = screen.getAllByTestId('reconciliation-table-row')
    expect(rows).toHaveLength(7)
    // Default sort = state priority: the Degraded bp-newapi is first.
    expect(rows[0].getAttribute('data-node-id')).toBe('bp-newapi')
    // Non-Flux kinds are present.
    const kinds = rows.map((r) => r.getAttribute('data-kind'))
    expect(kinds).toContain('Certificate')
    expect(kinds).toContain('Cluster')
    expect(kinds).toContain('ExternalSecret')
    expect(screen.getByTestId('reconciliation-table-count').textContent).toBe('7/7')
  })

  it('filters by Class (non-Flux only)', () => {
    render(<ReconciliationTable nodes={NODES} />)
    fireEvent.change(screen.getByTestId('reconciliation-table-filter-class'), { target: { value: 'Certificate' } })
    const rows = screen.getAllByTestId('reconciliation-table-row')
    expect(rows).toHaveLength(1)
    expect(rows[0].getAttribute('data-kind')).toBe('Certificate')
    expect(screen.getByTestId('reconciliation-table-count').textContent).toBe('1/7')
  })

  it('filters by State', () => {
    render(<ReconciliationTable nodes={NODES} />)
    fireEvent.change(screen.getByTestId('reconciliation-table-filter-state'), { target: { value: 'Reconciled' } })
    const rows = screen.getAllByTestId('reconciliation-table-row')
    expect(rows.every((r) => r.getAttribute('data-state') === 'Reconciled')).toBe(true)
  })

  it('search narrows to matching rows', () => {
    render(<ReconciliationTable nodes={NODES} />)
    fireEvent.change(screen.getByTestId('reconciliation-table-search'), { target: { value: 'shared-pg' } })
    const rows = screen.getAllByTestId('reconciliation-table-row')
    expect(rows).toHaveLength(1)
    expect(rows[0].getAttribute('data-node-id')).toBe('cluster/cnpg/shared-pg-a')
  })

  it('clicking the Name header sorts lexically and toggles direction', () => {
    render(<ReconciliationTable nodes={NODES} />)
    const nameSort = screen.getByTestId('reconciliation-table-sort-name')
    fireEvent.click(nameSort) // asc by label
    let rows = screen.getAllByTestId('reconciliation-table-row')
    const labelsAsc = rows.map((r) => within(r).getByText(/.+/, { selector: '.recon-name' }).textContent)
    const sorted = [...labelsAsc].sort((a, b) => (a ?? '').localeCompare(b ?? ''))
    expect(labelsAsc).toEqual(sorted)
    fireEvent.click(nameSort) // desc
    rows = screen.getAllByTestId('reconciliation-table-row')
    const labelsDesc = rows.map((r) => within(r).getByText(/.+/, { selector: '.recon-name' }).textContent)
    expect(labelsDesc).toEqual([...sorted].reverse())
  })

  it('renders the empty state when filters exclude everything', () => {
    render(<ReconciliationTable nodes={NODES} />)
    fireEvent.change(screen.getByTestId('reconciliation-table-search'), { target: { value: 'nonexistent-zzz' } })
    expect(screen.getByTestId('reconciliation-table-empty')).toBeTruthy()
  })
})
