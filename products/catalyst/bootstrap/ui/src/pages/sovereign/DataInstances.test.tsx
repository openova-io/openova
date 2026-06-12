/**
 * DataInstances.test.tsx — #3188 lock-in for the ADR-0010 "Data instances"
 * panel ported into the DEPLOYED React console (catalyst-ui).
 *
 * The panel renders:
 *   • ONE PostgreSQL engine-class card (shown once) with the live count.
 *   • One data-instance card per bp-postgres install, with its status/topology
 *     pills.
 *   • The honest "No PostgreSQL data instances yet" empty state on [].
 *   • The honest per-instance "bindings not yet surfaced by the API" state
 *     (no catalyst-api endpoint exposes the binding rows today — never
 *     fabricated).
 *
 * This is a fetch round-trip test (the established convention in this tree,
 * functionally the MSW round-trip the brief asks for): the stubbed
 * `fetch().json()` returns a body whose field names match the Go `json:` tags
 * of `applicationSummary` (endpoint_handler.go:119) VERBATIM — `name`,
 * `topology`, `status`, `blueprint`, `org`, `id` (lowercase camelCase). A
 * casing mismatch would silently yield undefined fields (documented prior
 * outage, memory feedback_ts_field_casing_vs_go_json_tag); the assertions
 * below would then fail, which is exactly the regression guard we want.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { DataInstances } from './DataInstances'

/**
 * Wire body for GET /catalyst/v1/catalog/bp-postgres/instances. Field names
 * are the Go `json:` tags VERBATIM (lowercase camelCase) — the casing the
 * backend actually emits.
 */
const TWO_INSTANCES = {
  items: [
    {
      id: 'aaaaaaaa-1111',
      name: 'shared-pg',
      blueprint: 'postgres',
      org: 'sovereign',
      topology: 'singleton',
      status: 'Ready',
    },
    {
      id: 'bbbbbbbb-2222',
      name: 'gitea-pg',
      blueprint: 'postgres',
      org: 'sovereign',
      topology: 'active-hot-standby',
      status: 'Ready',
    },
  ],
}

function jsonRes(body: unknown, status = 200) {
  return Promise.resolve({
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as unknown as Response)
}

function installFetch(body: unknown, status = 200) {
  globalThis.fetch = ((input: RequestInfo | URL) => {
    const url = typeof input === 'string' ? input : input.toString()
    if (url.includes('/instances')) return jsonRes(body, status)
    return jsonRes({})
  }) as typeof fetch
}

function renderPanel() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <DataInstances blueprint="bp-postgres" />
    </QueryClientProvider>,
  )
}

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('DataInstances — #3188 ADR-0010 panel (catalyst-ui)', () => {
  beforeEach(() => {
    installFetch(TWO_INSTANCES)
  })

  it('renders the PostgreSQL engine-class card once, with the live instance count', async () => {
    renderPanel()
    const card = await screen.findByTestId('engine-class-card')
    expect(card.textContent).toContain('PostgreSQL')
    expect(card.textContent).toContain('bp-cnpg')
    // Two data-instances in the mocked response → "2 instances".
    await waitFor(() =>
      expect(screen.getByTestId('engine-instance-count').textContent).toContain(
        '2 instances',
      ),
    )
  })

  it('renders one data-instance card per bp-postgres install (correct field casing)', async () => {
    renderPanel()
    const cards = await screen.findAllByTestId('data-instance-card')
    expect(cards.length).toBe(2)
    // Names/topology/status read from the wire body via the camelCase Go
    // json tags — a casing mismatch would render undefined and fail these.
    const text = cards.map((c) => c.textContent).join(' ')
    expect(text).toContain('shared-pg')
    expect(text).toContain('gitea-pg')
    expect(text).toContain('active-hot-standby')
    expect(text).toContain('Ready')
  })

  it('renders the honest "bindings not surfaced" state per bindings-less instance (never fabricates rows)', async () => {
    renderPanel()
    const empties = await screen.findAllByTestId('data-instance-no-bindings')
    expect(empties.length).toBe(2)
    // No consumers table is fabricated when the API exposes no binding rows.
    expect(screen.queryByTestId('consumers-table')).toBeNull()
    expect(screen.queryByTestId('binding-row')).toBeNull()
  })

  it('renders the Consumers table from the wire `bindings[]` rows (#3188 many-to-many)', async () => {
    // Wire body with server-side bindings: shared-pg carries two shared
    // Database-CR rows, pdns-pg carries the implicit dedicated row. Field
    // names are the Go `instanceBinding` json tags VERBATIM.
    installFetch({
      items: [
        {
          id: 'aaaaaaaa-1111',
          name: 'shared-pg',
          blueprint: 'postgres',
          org: 'shared-data',
          topology: 'ha',
          status: 'Ready',
          bindings: [
            { database: 'gitea', role: 'gitea', mode: 'shared', secret: 'gitea-database-secret', consumer: 'gitea' },
            { database: 'registry', role: 'harbor', mode: 'shared', secret: 'harbor-database-secret', consumer: 'harbor' },
          ],
        },
        {
          id: 'bbbbbbbb-2222',
          name: 'pdns-pg',
          blueprint: 'postgres',
          org: 'powerdns',
          topology: 'singleton',
          status: 'Ready',
          bindings: [
            { database: 'app', role: 'app', mode: 'dedicated', secret: '', consumer: 'powerdns' },
          ],
        },
      ],
    })
    renderPanel()
    const tables = await screen.findAllByTestId('consumers-table')
    expect(tables.length).toBe(2)
    const rows = screen.getAllByTestId('binding-row')
    expect(rows.length).toBe(3)
    const text = rows.map((r) => r.textContent).join(' ')
    // database · role · mode · secret · consumer, casing per the Go tags.
    expect(text).toContain('gitea-database-secret')
    expect(text).toContain('registry')
    expect(text).toContain('harbor')
    expect(text).toContain('shared')
    expect(text).toContain('dedicated')
    expect(text).toContain('powerdns')
    // shared-pg has 2 consumers → SHARED pill; no honest-gap line remains.
    expect(screen.queryByTestId('data-instance-no-bindings')).toBeNull()
  })

  it('renders the honest empty state on [] (model gated off)', async () => {
    installFetch({ items: [] })
    renderPanel()
    const empty = await screen.findByTestId('data-instances-empty')
    expect(empty.textContent).toContain('No PostgreSQL data instances yet')
    // Engine card still shows, with a zero count.
    expect(screen.getByTestId('engine-instance-count').textContent).toContain(
      '0 instances',
    )
    expect(screen.queryByTestId('data-instance-card')).toBeNull()
  })
})
