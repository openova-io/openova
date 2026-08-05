/**
 * CatalogDetail.iac-baseline-5610.test.tsx — #5610 facet B lock-in.
 *
 * Reported: after a SUCCESSFUL "Commit IaC", the catalog Edit-IaC editor still
 * shows "• unsaved changes" right next to its own "Committed to IaC ✓"
 * message, and "Commit IaC" stays enabled inviting a redundant re-commit of
 * identical bytes. It only clears on a full page reload.
 *
 * Mechanism: YamlEditor's dirty state is `yaml.trim() !== initial.trim()`
 * (YamlEditor.tsx:213) and `initial` is a `useMemo` over the `seedYaml` prop
 * (:185). CatalogDetail owns that prop (`committedIac`). If the just-committed
 * bytes are not fed back as the new seed, the baseline stays at the PRE-commit
 * file forever and `dirty` can never go false.
 *
 * The fix landed in CatalogDetail (`setCommittedIac(yaml)` in `onCommit`) with
 * no test, so this file pins both halves of the chain — deliberately, because
 * either half alone is token-passing:
 *
 *   PART 1 (here) — CatalogDetail re-baselines: after a successful commit the
 *            props it hands YamlEditor carry the just-committed YAML as
 *            `seedYaml`, and after a FAILED one they do not.
 *   PART 2 (widgets/cloud-list/YamlEditor.baseline-5610.test.tsx) — that seed
 *            is genuinely what clears the pill, asserted against the REAL
 *            widget. Separate file because the two halves need opposite mock
 *            states for the same module.
 *
 * Part 1 without part 2 would assert a prop nobody proved matters; part 2
 * without part 1 would assert a widget nobody proved is wired that way.
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

const adminHolder = { value: true }
vi.mock('@/shared/lib/useCatalogAdmin', () => ({
  useCatalogAdmin: () => adminHolder.value,
}))

/** Records every props object CatalogDetail hands the editor, and drives the
 *  REAL `onCommit` with a chosen buffer — the same path "Commit IaC" takes. */
const yamlEditorRenders: YamlEditorProps[] = []
const committedBuffer = { value: '' }
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
            // The real YamlEditor catches a throwing onCommit into its
            // `applyError` panel; mirror that so a failed-commit case does not
            // surface as an unhandled rejection.
            void props.onCommit?.(committedBuffer.value)?.catch(() => {})
          }}
        >
          Commit
        </button>
      </div>
    )
  },
}))

import { CatalogDetail } from './CatalogDetail'

const CATALOG = {
  name: 'alloy',
  version: '1.0.2',
  card: { title: 'Alloy', summary: 'Unified node agent' },
  origin: 'upstream',
  source: 'gitea',
  raw: { metadata: { name: 'bp-alloy' }, spec: { version: '1.0.2' } },
}

/** The file as committed BEFORE this edit. */
const COMMITTED_YAML = [
  'apiVersion: catalyst.openova.io/v1',
  'kind: Blueprint',
  'metadata:',
  '  name: bp-alloy',
  'spec:',
  '  version: 1.0.2',
  '',
].join('\n')

/** The operator's one-line edit (the issue's exact 1.0.2 → 1.0.3 walk). */
const EDITED_YAML = COMMITTED_YAML.replace('version: 1.0.2', 'version: 1.0.3')

function jsonRes(body: unknown, status = 200) {
  return Promise.resolve({
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(JSON.stringify(body)),
  } as unknown as Response)
}

function installFetch() {
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
    const url = typeof input === 'string' ? input : input.toString()
    const method = (init?.method ?? 'GET').toUpperCase()
    if (url.includes('/iac')) {
      if (method === 'PUT') {
        return jsonRes({
          slug: 'alloy',
          path: 'catalog-sovereign/bp-alloy/blueprint.yaml',
          committed: true,
        })
      }
      return jsonRes({
        blueprintYaml: COMMITTED_YAML,
        path: 'catalog-sovereign/bp-alloy/blueprint.yaml',
      })
    }
    if (url.includes('/instances')) return jsonRes({ items: [] })
    if (url.includes('/catalog/')) return jsonRes(CATALOG)
    return jsonRes({})
  }) as typeof fetch
}

function renderCatalog() {
  const rootRoute = createRootRoute({ component: () => <Outlet /> })
  const catalogRoute = createRoute({
    getParentRoute: () => rootRoute,
    path: '/catalog/$blueprintName',
    component: CatalogDetail,
  })
  const tree = rootRoute.addChildren([catalogRoute])
  const router = createRouter({
    routeTree: tree,
    history: createMemoryHistory({ initialEntries: ['/catalog/alloy'] }),
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
  committedBuffer.value = EDITED_YAML
  installFetch()
})

afterEach(() => {
  cleanup()
  vi.restoreAllMocks()
})

describe('#5610 facet B — PART 1: CatalogDetail re-baselines the editor on a successful commit', () => {
  it('feeds the just-committed YAML back as seedYaml', async () => {
    renderCatalog()
    fireEvent.click(await screen.findByTestId('catalog-detail-edit-iac'))
    // Non-vacuity: the editor first opens on the PRE-edit committed file.
    await waitFor(() =>
      expect(screen.getByTestId('mock-yaml-editor-seed').textContent).toContain('version: 1.0.2'),
    )

    fireEvent.click(screen.getByTestId('mock-yaml-editor-commit'))

    // The baseline moved to the bytes that were just committed.
    await waitFor(() =>
      expect(screen.getByTestId('mock-yaml-editor-seed').textContent).toContain('version: 1.0.3'),
    )
    expect(yamlEditorRenders[yamlEditorRenders.length - 1].seedYaml).toBe(EDITED_YAML)
  })

  it('does NOT re-baseline when the commit did not land', async () => {
    // A failed commit must leave the operator's edit flagged as unsaved —
    // re-baselining on failure would be the #5113-facet-D fabricated-green
    // defect in a new place.
    globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) => {
      const url = typeof input === 'string' ? input : input.toString()
      const method = (init?.method ?? 'GET').toUpperCase()
      if (url.includes('/iac') && method === 'PUT') {
        return jsonRes({ slug: 'alloy', committed: false, reason: 'gitea unreachable' })
      }
      if (url.includes('/iac')) return jsonRes({ blueprintYaml: COMMITTED_YAML })
      if (url.includes('/instances')) return jsonRes({ items: [] })
      if (url.includes('/catalog/')) return jsonRes(CATALOG)
      return jsonRes({})
    }) as typeof fetch

    renderCatalog()
    fireEvent.click(await screen.findByTestId('catalog-detail-edit-iac'))
    await waitFor(() =>
      expect(screen.getByTestId('mock-yaml-editor-seed').textContent).toContain('version: 1.0.2'),
    )
    fireEvent.click(screen.getByTestId('mock-yaml-editor-commit'))

    await waitFor(() => expect(yamlEditorRenders.length).toBeGreaterThan(1))
    expect(yamlEditorRenders[yamlEditorRenders.length - 1].seedYaml).toBe(COMMITTED_YAML)
  })
})
