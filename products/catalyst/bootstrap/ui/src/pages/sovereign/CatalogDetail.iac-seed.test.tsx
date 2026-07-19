/**
 * CatalogDetail.iac-seed.test.tsx — #5124 regression lock-in.
 *
 * Root cause (hw256 V6 catalog-edit walk): the Edit-IaC `YamlEditor` seeded
 * its buffer from the store's stale `CatalogItem.raw` (the pre-edit static
 * shape — bare name, no `spec.version`, no card-form fields), NOT the
 * currently-committed `catalog-sovereign/<repo>/blueprint.yaml`. Committing
 * from that stale seed silently REVERTED prior card-form edits (icon /
 * title / summary) that were already in the committed file, and Flux then
 * reconciled the reverted file into the live CR.
 *
 * The fix (PR #5164): `GET /api/v1/catalog/{name}/iac` returns the
 * currently-committed file; CatalogDetail fetches it on "Edit IaC" open and
 * passes it as `YamlEditor`'s `seedYaml` (which wins over the `obj`-derived
 * seed). This test locks in the CatalogDetail-level wiring contract end to
 * end — independent of `YamlEditor`'s own CodeMirror-rendering tests
 * (YamlEditor.test.tsx / YamlEditor.iac.test.ts) — by capturing the props
 * `CatalogDetail` actually hands the editor and by driving a real commit
 * through the unmocked `catalog.api` write path.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import {
  RouterProvider,
  createRouter,
  createRootRoute,
  createRoute,
  createMemoryHistory,
  Outlet,
} from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { YamlEditorProps } from '@/widgets/cloud-list/YamlEditor'

// ── Mocks ──────────────────────────────────────────────────────────────
const adminHolder = { value: true }
vi.mock('@/shared/lib/useCatalogAdmin', () => ({
  useCatalogAdmin: () => adminHolder.value,
}))

// Replace the real CodeMirror-backed YamlEditor with a tiny harness that
// (a) records every props object CatalogDetail hands it, so the test can
// assert what it was seeded with, and (b) exposes a "Commit" button wired
// straight to the real `onCommit` — so a click drives the SAME
// saveCatalogBlueprintIaC → PUT /catalog/{name}/iac write path production
// does, without needing to mount the real editor surface.
const yamlEditorRenders: YamlEditorProps[] = []
vi.mock('@/widgets/cloud-list/YamlEditor', () => ({
  YamlEditor: (props: YamlEditorProps) => {
    yamlEditorRenders.push(props)
    return (
      <div data-testid="mock-yaml-editor">
        <pre data-testid="mock-yaml-editor-seed">{props.seedYaml ?? '(no seedYaml)'}</pre>
        <button
          type="button"
          data-testid="mock-yaml-editor-commit"
          onClick={() => {
            void props.onCommit?.(props.seedYaml ?? '')
          }}
        >
          Commit
        </button>
      </div>
    )
  },
}))

// Import AFTER mocks.
import { CatalogDetail } from './CatalogDetail'

// The STALE store shape — mirrors the hw256 root cause: bare name, no
// `spec.version`, no card-form fields folded in yet.
const STALE_RAW_CATALOG = {
  name: 'alloy',
  version: '1.0.0',
  card: { title: 'Alloy' },
  origin: 1,
  source: 'gitea',
  raw: { metadata: { name: 'bp-alloy' } },
}

// The CURRENTLY-COMMITTED file — carries a prior card-form edit
// (`card.title` renamed + an icon) that the stale raw above does NOT have.
// If the editor seeded from `cat.raw` instead, this content would be lost
// on commit — the exact #5124 defect.
const COMMITTED_YAML = [
  'apiVersion: catalyst.openova.io/v1alpha1',
  'kind: Blueprint',
  'metadata:',
  '  name: bp-alloy',
  'spec:',
  '  version: "9.9.9"',
  '  card:',
  '    title: Alloy (renamed via card form)',
  '    iconLight: https://example.test/alloy-light.svg',
  '',
].join('\n')

function jsonRes(body: unknown, status = 200) {
  return Promise.resolve({
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
  } as unknown as Response)
}

const putCalls: { url: string; body: { blueprintYaml?: string } }[] = []

function installFetch() {
  putCalls.length = 0
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString()
    const method = (init?.method ?? 'GET').toUpperCase()
    if (url.includes('/iac')) {
      if (method === 'PUT') {
        putCalls.push({
          url,
          body: init?.body ? (JSON.parse(init.body as string) as { blueprintYaml?: string }) : {},
        })
        return jsonRes({
          slug: 'alloy',
          path: 'catalog-sovereign/bp-alloy/blueprint.yaml',
          committed: true,
        })
      }
      // GET /catalog/{name}/iac — the committed-file read (#5124).
      return jsonRes({
        blueprintYaml: COMMITTED_YAML,
        path: 'catalog-sovereign/bp-alloy/blueprint.yaml',
      })
    }
    if (url.includes('/instances')) return jsonRes({ items: [] })
    if (url.includes('/catalog/')) return jsonRes(STALE_RAW_CATALOG)
    return jsonRes({})
  }) as typeof fetch
}

function renderCatalog(blueprintName = 'alloy') {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const catalogRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/catalog/$blueprintName',
    component: CatalogDetail,
  })
  const tree = rootRoute.addChildren([catalogRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: [`/catalog/${blueprintName}`] }),
  })
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })
  return render(
    <QueryClientProvider client={qc}>
      <RouterProvider router={router} />
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  adminHolder.value = true
  yamlEditorRenders.length = 0
  installFetch()
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('CatalogDetail Edit-IaC seed source (#5124)', () => {
  it('seeds the YamlEditor from the CURRENTLY-COMMITTED file, not the stale store raw', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('catalog-detail-edit-iac'))

    // The committed-file fetch is async (fired from the click handler) — wait
    // for it to land before asserting the seed.
    await waitFor(() => {
      expect(screen.getByTestId('mock-yaml-editor-seed').textContent).toContain(
        'renamed via card form',
      )
    })
    const seed = screen.getByTestId('mock-yaml-editor-seed').textContent ?? ''
    // The committed file's fields survived into the seed…
    expect(seed).toContain('version: "9.9.9"')
    expect(seed).toContain('iconLight: https://example.test/alloy-light.svg')
    // …and the seed is NOT derived from the stale store raw (which carries
    // none of the above — just a bare `metadata.name`).
    expect(seed).not.toBe(JSON.stringify(STALE_RAW_CATALOG.raw))

    // The LAST render's props are the source of truth for what was passed.
    const lastProps = yamlEditorRenders[yamlEditorRenders.length - 1]
    expect(lastProps.seedYaml).toBe(COMMITTED_YAML)
  })

  it('opens with no seed yet (committedIac cleared to null) before the fetch resolves', async () => {
    // Guards against a stale seed leaking across opens: setCommittedIac(null)
    // must run synchronously with setEditingIaC(true), before the async fetch
    // resolves — so the very first render of the (now-open) editor never
    // shows a leftover seed from a previous open.
    let resolveIac: ((v: { blueprintYaml: string }) => void) | undefined
    globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString()
      const method = (init?.method ?? 'GET').toUpperCase()
      if (url.includes('/iac') && method === 'GET') {
        return new Promise((resolve) => {
          resolveIac = (v) =>
            resolve({
              ok: true,
              status: 200,
              json: () => Promise.resolve(v),
              text: () => Promise.resolve(JSON.stringify(v)),
            } as unknown as Response)
        })
      }
      if (url.includes('/instances')) return jsonRes({ items: [] })
      if (url.includes('/catalog/')) return jsonRes(STALE_RAW_CATALOG)
      return jsonRes({})
    }) as typeof fetch

    renderCatalog()
    fireEvent.click(await screen.findByTestId('catalog-detail-edit-iac'))
    // Before the fetch resolves, the editor is open but seedYaml is undefined
    // (not a stale/leftover value).
    await waitFor(() => expect(screen.getByTestId('mock-yaml-editor')).toBeTruthy())
    expect(screen.getByTestId('mock-yaml-editor-seed').textContent).toBe('(no seedYaml)')

    resolveIac?.({ blueprintYaml: COMMITTED_YAML })
    await waitFor(() => {
      expect(screen.getByTestId('mock-yaml-editor-seed').textContent).toContain(
        'renamed via card form',
      )
    })
  })

  it('committing the unedited seed round-trips the committed content verbatim — no card-field loss', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('catalog-detail-edit-iac'))
    await waitFor(() => {
      expect(screen.getByTestId('mock-yaml-editor-seed').textContent).toContain(
        'renamed via card form',
      )
    })

    // Commit WITHOUT editing — the exact "open Edit-IaC, immediately commit"
    // path that silently reverted card-form edits before #5124's fix.
    fireEvent.click(screen.getByTestId('mock-yaml-editor-commit'))

    await waitFor(() => expect(putCalls.length).toBe(1))
    // The committed write carries the SAME content that was seeded — the
    // prior card-form edits (title/icon/version) survive the round trip.
    expect(putCalls[0].body.blueprintYaml).toBe(COMMITTED_YAML)
    expect(putCalls[0].body.blueprintYaml).toContain('renamed via card form')
    expect(putCalls[0].body.blueprintYaml).toContain('iconLight')
  })

  it('falls back to cat.raw-derived seeding when nothing is committed yet (404) — never worse than before', async () => {
    globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString()
      const method = (init?.method ?? 'GET').toUpperCase()
      if (url.includes('/iac') && method === 'GET') {
        return jsonRes({ error: 'not-committed' }, 404)
      }
      if (url.includes('/instances')) return jsonRes({ items: [] })
      if (url.includes('/catalog/')) return jsonRes(STALE_RAW_CATALOG)
      return jsonRes({})
    }) as typeof fetch

    renderCatalog()
    fireEvent.click(await screen.findByTestId('catalog-detail-edit-iac'))
    await waitFor(() => expect(screen.getByTestId('mock-yaml-editor')).toBeTruthy())
    // 404 → getCatalogBlueprintIaC resolves null → seedYaml stays undefined,
    // so YamlEditor's own obj-derived fallback takes over (unchanged from
    // pre-#5124 behavior — never a fabricated/blank seed).
    await waitFor(() => {
      const lastProps = yamlEditorRenders[yamlEditorRenders.length - 1]
      expect(lastProps.seedYaml).toBeUndefined()
    })
  })
})
