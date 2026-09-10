import { createElement } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { Me } from '../api/types'

/**
 * Configure → Access rendered for a sovereign-admin and for a billing-operator
 * (DESIGN.md §10.9): the bindings table shows the implicit OPERATOR_EMAILS row
 * as configured and the explicit rows with Revoke; the group mappings name
 * the header they are read from; the roles card lists all six; and a
 * principal without settings.manage gets the read-only notice, not the forms.
 */

const bindings = {
  bindings: [
    { id: 'b1', subject_email: 'bo@nc.example', role: 'billing-operator', scope_kind: 'sovereign', granted_by: 'ops@nc.example', granted_at: '2026-09-10T08:00:00Z', source: 'binding' },
    { id: 'b2', subject_email: 'owner@acme.example', role: 'customer-owner', scope_kind: 'customer', customer_id: 'c1', customer_name: 'ACME LLC', granted_by: 'admin_email', granted_at: '2026-09-10T08:00:00Z', source: 'binding' },
  ],
  implicit: [{ subject_email: 'ops@nc.example', role: 'sovereign-admin', scope_kind: 'sovereign', granted_by: 'OPERATOR_EMAILS', source: 'config' }],
}
const mappings = {
  mappings: [{ id: 'm1', group_name: 'finance', role: 'finance-viewer', scope_kind: 'sovereign', created_at: '2026-09-10T08:00:00Z' }],
  groups_header: 'X-Forwarded-Groups',
}
const roles = {
  roles: [
    { role: 'sovereign-admin', scope_kind: 'sovereign', permissions: ['metering.read', 'settings.manage'], description: 'Everything.' },
    { role: 'billing-operator', scope_kind: 'sovereign', permissions: ['metering.read', 'billing.issue'], description: 'Runs billing.' },
    { role: 'finance-viewer', scope_kind: 'sovereign', permissions: ['metering.read', 'audit.read'], description: 'Reads.' },
    { role: 'customer-owner', scope_kind: 'customer', permissions: ['metering.read', 'account.topup', 'customer.self.manage'], description: 'Owns one customer.' },
    { role: 'customer-billing', scope_kind: 'customer', permissions: ['metering.read', 'account.topup'], description: 'Tops up.' },
    { role: 'customer-viewer', scope_kind: 'customer', permissions: ['metering.read'], description: 'Reads one customer.' },
  ],
}
const customers = { customers: [{ id: 'c1', slug: 'acme', name: 'ACME LLC', admin_email: 'owner@acme.example', billing_mode: 'chargeback', status: 'active' }] }

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => {
    const data = path === '/access/bindings' ? bindings : path === '/access/group-mappings' ? mappings : path === '/access/roles' ? roles : path === '/customers' ? customers : null
    return { data, error: '', loading: false, reload: async () => {}, setData: () => {} }
  },
}))

let who: Me = { email: 'ops@nc.example', role: 'operator', permissions: { sovereign: ['settings.manage', 'metering.read'] }, roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign', source: 'config' }] }
vi.mock('../auth/session', () => ({
  useSession: () => ({ me: who, loading: false, refresh: async () => who, logout: async () => {} }),
}))

import { Access } from './Access'

function render(): string {
  return renderToString(createElement(MemoryRouter, null, createElement(Access))).replace(/<!-- -->/g, '')
}

describe('Access page', () => {
  it('shows bindings, the configured admin, group mappings and the roles for a sovereign-admin', () => {
    const html = render()
    expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
    expect(html).toContain('ops@nc.example')
    expect(html).toContain('configured')
    expect(html).toContain('bo@nc.example')
    expect(html).toContain('Billing operator')
    expect(html).toContain('owner@acme.example')
    expect(html).toContain('ACME LLC')
    expect(html).toContain('>Revoke<')
    expect(html).toContain('aria-label="Grant a role"')
    expect(html).toContain('>finance<')
    expect(html).toContain('X-Forwarded-Groups')
    expect(html).toContain('aria-label="Map a group"')
    expect(html).toContain('aria-label="Roles"')
    for (const r of ['Sovereign admin', 'Billing operator', 'Finance viewer', 'Owner', 'Billing', 'Viewer']) expect(html).toContain(r)
    expect(html).toContain('2 bindings · 1 configured sovereign-admin · 1 group mapping')
  })

  it('is read-only without settings.manage', () => {
    who = { email: 'bo@nc.example', role: 'billing-operator', permissions: { sovereign: ['metering.read', 'billing.issue'] }, roles: [{ role: 'billing-operator', scope_kind: 'sovereign' }] }
    const html = render()
    expect(html).toContain('settings.manage')
    expect(html).not.toContain('aria-label="Grant a role"')
    expect(html).not.toContain('>Revoke<')
    expect(html).toContain('aria-label="Roles"')
  })
})
