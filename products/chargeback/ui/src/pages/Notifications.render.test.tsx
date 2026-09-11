import { createElement, type ComponentType } from 'react'
import { renderToString } from 'react-dom/server'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { describe, expect, it, vi } from 'vitest'
import type { Me, NotifyCatalogue, NotifyDeliveriesDoc, NotifyPreferencesDoc } from '../api/types'

/**
 * Configure → Notifications rendered with the documents the Go side produces
 * (DESIGN.md §21): the catalogue with each event's channels and whether it
 * can be switched off, the SMS channel declared and honestly refusing, the
 * delivery log with failures visible, and the customer's own view — which
 * offers no switch at all on an invoice or a dunning notice.
 *
 * Effects do not run under renderToString, so each surface shows its initial
 * state; what these assert is what a reader SEES.
 */

const catalogue: NotifyCatalogue = {
  default_locale: 'en',
  locales: ['en'],
  statuses: ['sent', 'retrying', 'failed', 'suppressed', 'unavailable'],
  channels: [
    { name: 'email', available: true },
    {
      name: 'sms',
      available: false,
      reason:
        "SMS has no transport in this build: Omantel's gateway specification has not been provided, so there is no endpoint, credential shape, message payload or delivery-receipt semantics to implement against.",
    },
  ],
  events: [
    {
      key: 'statement.issued',
      title: 'Statement issued',
      description: 'The invoice: the period, the waterfall, the biggest lines and a link.',
      category: 'billing',
      mandatory: true,
      channels: ['email'],
      default_on: true,
      source: 'internal/api/statements.go',
      payload: [{ name: 'document', description: 'the plain-text statement' }],
    },
    {
      key: 'collections.reminder',
      title: 'Payment reminder',
      description: 'One dunning stage: before the due date, on it, or after it.',
      category: 'collections',
      mandatory: true,
      channels: ['email'],
      default_on: true,
      source: 'internal/collections/evaluator.go',
    },
    {
      key: 'account.low_balance',
      title: 'Low balance',
      description: 'A prepaid account has fallen below its alert threshold.',
      category: 'collections',
      mandatory: false,
      channels: ['email'],
      default_on: true,
      source: 'internal/collections/wallet.go',
    },
  ],
  templates: [
    { event: 'statement.issued', locale: 'en', subject: '{{.subject}}', body: '{{.document}}' },
    { event: 'collections.reminder', locale: 'en', subject: 'Overdue: {{.invoice_title}}', body: 'Hello {{.customer_name}},' },
    { event: 'account.low_balance', locale: 'en', subject: 'Low balance: {{.available}} {{.currency}} left on your account', body: 'Hello {{.customer_name}},' },
  ],
}

const sovereignPrefs: NotifyPreferencesDoc = {
  scope: 'sovereign',
  preferences: [],
  channels: catalogue.channels,
  locales: ['en'],
  events: catalogue.events,
  effective: [
    { event: 'statement.issued', title: 'Statement issued', category: 'billing', mandatory: true, enabled: true, channels: ['email'], locale: 'en', source: 'catalogue default' },
    { event: 'collections.reminder', title: 'Payment reminder', category: 'collections', mandatory: true, enabled: true, channels: ['email'], locale: 'en', source: 'catalogue default' },
    { event: 'account.low_balance', title: 'Low balance', category: 'collections', mandatory: false, enabled: false, channels: ['email'], locale: 'en', source: 'sovereign' },
  ],
}

const customerPrefs: NotifyPreferencesDoc = {
  scope: 'customer:c1',
  customer_id: 'c1',
  preferences: [],
  channels: catalogue.channels,
  locales: ['en'],
  events: catalogue.events,
  effective: [
    { event: 'statement.issued', title: 'Statement issued', category: 'billing', mandatory: true, enabled: true, channels: ['email'], locale: 'en', source: 'catalogue default' },
    { event: 'collections.reminder', title: 'Payment reminder', category: 'collections', mandatory: true, enabled: true, channels: ['email'], locale: 'en', source: 'catalogue default' },
    { event: 'account.low_balance', title: 'Low balance', category: 'collections', mandatory: false, enabled: true, channels: ['email'], locale: 'en', source: 'catalogue default' },
  ],
}

const deliveries: NotifyDeliveriesDoc = {
  since: '2026-09-05T00:00:00Z',
  limit: 200,
  statuses: catalogue.statuses,
  stats: [
    { status: 'sent', count: 41 },
    { status: 'failed', count: 2 },
    { status: 'unavailable', count: 1 },
  ],
  deliveries: [
    {
      id: 'd1',
      event: 'collections.reminder',
      customer_id: 'c1',
      channel: 'email',
      recipient: 'ap@acme.example',
      subject: 'Overdue: Invoice INV-000123 for 1000 OMR, 7 days past due',
      attempt: 3,
      status: 'failed',
      reason: '550 mailbox unavailable',
      at: '2026-09-11T09:15:00Z',
    },
    {
      id: 'd2',
      event: 'account.low_balance',
      customer_id: 'c1',
      channel: 'sms',
      recipient: 'ap@acme.example',
      attempt: 1,
      status: 'unavailable',
      reason: "SMS has no transport in this build: Omantel's gateway specification has not been provided.",
      at: '2026-09-11T09:10:00Z',
    },
  ],
}

vi.mock('../lib/useQuery', () => {
  const docFor = (path: string): unknown => {
    if (path === '/notifications/events') return catalogue
    if (path === '/notifications/preferences') return sovereignPrefs
    if (path.startsWith('/notifications/deliveries')) return deliveries
    if (path.endsWith('/notifications/preferences')) return customerPrefs
    if (path.includes('/notifications/deliveries')) return deliveries
    throw new Error(`no fixture for ${path}`)
  }
  return {
    useQuery: (path: string | null) => ({ data: path ? docFor(path) : null, error: '', loading: false, reload: async () => {}, setData: () => {} }),
  }
})

const sovereign: Me = {
  email: 'ops@nc.example',
  role: 'sovereign-admin',
  roles: [{ role: 'sovereign-admin', scope_kind: 'sovereign' }],
  permissions: { sovereign: ['metering.read', 'rating.manage', 'customers.manage', 'billing.issue', 'billing.collect', 'settings.manage', 'audit.read'] },
  scopes: ['sovereign'],
}

const readOnly: Me = {
  email: 'fin@nc.example',
  role: 'finance-viewer',
  roles: [{ role: 'finance-viewer', scope_kind: 'sovereign' }],
  permissions: { sovereign: ['metering.read', 'audit.read'] },
  scopes: ['sovereign'],
}

const owner: Me = {
  email: 'ap@acme.example',
  role: 'customer-admin',
  customer_id: 'c1',
  roles: [{ role: 'customer-owner', scope_kind: 'customer', customer_id: 'c1' }],
  permissions: { 'customer:c1': ['metering.read', 'customer.self.manage'] },
  scopes: ['customer:c1'],
}

const viewer: Me = {
  email: 'read@acme.example',
  role: 'customer-viewer',
  customer_id: 'c1',
  roles: [{ role: 'customer-viewer', scope_kind: 'customer', customer_id: 'c1' }],
  permissions: { 'customer:c1': ['metering.read'] },
  scopes: ['customer:c1'],
}

let session: Me = sovereign
vi.mock('../auth/session', () => ({
  useSession: () => ({ me: session, loading: false, logout: async () => {}, reload: async () => {} }),
  SessionProvider: ({ children }: { children: unknown }) => children,
}))

import { MyNotifications, Notifications, PreferenceModal, TemplateModal } from './Notifications'
import { navFor } from '../layout/Shell'

function render(Page: ComponentType, path = '/notifications', url = path): string {
  const html = renderToString(createElement(MemoryRouter, { initialEntries: [url] }, createElement(Routes, null, createElement(Route, { path, element: createElement(Page) })))).replace(/<!-- -->/g, '')
  expect(html).not.toMatch(/NaN|undefined|\[object Object\]/)
  return html
}

describe('Configure → Notifications lists what the product sends', () => {
  it('shows each event with its channels, whether it can be switched off, and its template subject', () => {
    session = sovereign
    const html = render(Notifications)
    expect(html).toContain('Statement issued')
    expect(html).toContain('statement.issued')
    expect(html).toContain('Payment reminder')
    expect(html).toContain('Low balance')
    // A mandatory notice says so, and says what that means.
    expect(html).toContain('Mandatory')
    expect(html).toContain('cannot be switched off')
    expect(html).toContain('a recipient may switch it off')
    // The template subject is on the row, as its source.
    expect(html).toContain('{{.subject}}')
    expect(html).toContain('Low balance: {{.available}} {{.currency}}')
    // The effective setting, and what decided it.
    expect(html).toContain('no preference set')
    expect(html).toContain('set at sovereign')
  })

  it('renders the SMS channel as declared and unavailable, with the reason verbatim', () => {
    session = sovereign
    const html = render(Notifications)
    expect(html).toContain('is declared and cannot deliver')
    expect(html).toContain('Omantel')
    expect(html).toContain('gateway specification has not been provided')
    expect(html).toContain('declared without a transport')
    // It never claims to be able to send.
    expect(html).not.toContain('sms is available')
  })

  it('puts the failures in front: the tile counts them and the table opens on them', () => {
    session = sovereign
    const html = render(Notifications)
    expect(html).toContain('Not delivered')
    // 2 failed + 1 unavailable.
    expect(html).toContain('>3<')
    expect(html).toContain('failed or refused in the last 7 days')
    expect(html).toContain('550 mailbox unavailable')
    expect(html).toContain('Overdue: Invoice INV-000123 for 1000 OMR, 7 days past due')
    // One row per ATTEMPT, and the attempt number is shown.
    expect(html).toContain('Attempt')
    expect(html).toContain('One row per')
    // The filter opens on problems.
    expect(html).toContain('Problems')
    expect(html).toContain('All attempts')
  })

  it('renders for a principal without settings.manage and offers it nothing to change', () => {
    session = readOnly
    const html = render(Notifications)
    expect(html).toContain('Statement issued')
    expect(html).toContain('Read-only')
    expect(html).not.toContain('>Edit<')
    expect(html).not.toContain('>Reset<')
    session = sovereign
  })

  it('is on the Sovereign lens under Configure, and the customer lens has its own', () => {
    expect(navFor(sovereign).find(([title]) => title === 'Configure')?.[1].map(([, label]) => label)).toContain('Notifications')
    expect(navFor(readOnly).find(([title]) => title === 'Configure')?.[1].map(([, label]) => label)).toContain('Notifications')
    // The customer sees ITS notifications page, at its own path.
    const customerItems = navFor(owner).flatMap(([, items]) => items)
    expect(customerItems.map(([, label]) => label)).toContain('Notifications')
    expect(customerItems.find(([, label]) => label === 'Notifications')?.[0]).toBe('/my/notifications')
    // A customer VIEWER still reads the page — the switches inside are what
    // customer.self.manage gates, not the page.
    expect(navFor(viewer).flatMap(([, items]) => items).map(([, label]) => label)).toContain('Notifications')
  })
})

describe("the customer's own notifications", () => {
  it('lists what the account receives and marks the required notices as always sent', () => {
    session = owner
    const html = render(MyNotifications, '/my/notifications')
    expect(html).toContain('What we send you')
    expect(html).toContain('Statement issued')
    expect(html).toContain('Always')
    expect(html).toContain('required — cannot be switched off')
    // An optional one is switchable, and says so.
    expect(html).toContain('Low balance')
    expect(html).toContain('>Change<')
  })

  it('offers a customer VIEWER no switch at all', () => {
    session = viewer
    const html = render(MyNotifications, '/my/notifications')
    expect(html).toContain('Statement issued')
    expect(html).toContain('read-only')
    expect(html).not.toContain('>Change<')
    session = sovereign
  })

  it('shows the account its own delivery attempts, including one the SMS channel refused', () => {
    session = owner
    const html = render(MyNotifications, '/my/notifications')
    expect(html).toContain('Recent messages')
    expect(html).toContain('unavailable')
    expect(html).toContain('Omantel')
    session = sovereign
  })
})

describe('the dialogs', () => {
  it('shows the template source and the payload an event renders from', () => {
    session = sovereign
    const Page: ComponentType = () => createElement(TemplateModal, { event: catalogue.events[0], catalogue, onClose: () => {} })
    const html = render(Page)
    expect(html).toContain('Template — Statement issued')
    expect(html).toContain('{{.document}}')
    expect(html).toContain('internal/api/statements.go')
    expect(html).toContain('the plain-text statement')
    expect(html).toContain('A second language is one more template file')
  })

  it('a mandatory notice cannot be switched off in the form, and says why', () => {
    session = sovereign
    const Page: ComponentType = () =>
      createElement(PreferenceModal, {
        event: catalogue.events[0],
        effective: sovereignPrefs.effective[0],
        channels: catalogue.channels,
        locales: ['en'],
        path: '/notifications/preferences',
        onClose: () => {},
        onDone: () => {},
      })
    const html = render(Page)
    expect(html).toContain('This is a mandatory notice')
    expect(html).toContain('cannot be switched off')
    expect(html).toContain('you may add a channel to it')
    expect(html).toMatch(/aria-label="Receive this message"[^>]*disabled|disabled[^>]*aria-label="Receive this message"/)
  })

  it('an optional notice offers a real switch, and names the channel with no transport', () => {
    session = sovereign
    const Page: ComponentType = () =>
      createElement(PreferenceModal, {
        event: catalogue.events[2],
        effective: sovereignPrefs.effective[2],
        channels: catalogue.channels,
        locales: ['en'],
        path: '/notifications/preferences',
        onClose: () => {},
        onDone: () => {},
      })
    const html = render(Page)
    expect(html).not.toContain('This is a mandatory notice')
    expect(html).toContain('aria-label="Receive this message"')
    expect(html).toContain('aria-label="Channel sms"')
    expect(html).toContain('no transport')
    expect(html).toContain('Omantel')
    expect(html).toContain('gateway specification has not been provided')
  })
})
