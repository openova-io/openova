/**
 * ResourceDetailPage.widgets.test.tsx — EPIC #1099 Group A trust-recovery
 * audit lockdown (Refs #1099, follow-up to PR #2059's Events fix).
 *
 * After PR #2059 lit up EventsPanel by extending `GRAPH_K8S_KINDS` with
 * `event`, this file locks the other three Group A widgets in the
 * resource-detail page (YamlEditor, MetricsPanel, ResourceActions)
 * against the same class of "dark widget" regression — i.e. a future
 * refactor accidentally muting a panel because its mount point was
 * removed from `ResourceDetailPage`.
 *
 * Investigation reference: PR #2059 root-caused EventsPanel as
 * "DARK-VIA-KINDS-OMISSION". The audit of the remaining widgets
 * concluded ALREADY-LIT for all 3:
 *
 *   - YamlEditor — receives `obj` from `getResource` REST (independent
 *     of `useK8sCacheStream`), backend wired in `cmd/api/main.go:818,
 *     826, 833, 834`, full validate/apply with flux→PR routing.
 *
 *   - MetricsPanel — direct REST via `getResourceMetrics`, backend
 *     wired at `cmd/api/main.go:817`, metrics-server + mimir clients
 *     with operator-readable "Metrics unavailable" fallback.
 *
 *   - ResourceActions — direct REST via scale/restart/delete, backends
 *     wired at `cmd/api/main.go:820, 827, 835`, type-the-name
 *     confirmation modal already in place (no one-click delete).
 *
 * These integration assertions complement the unit tests in
 * `widgets/cloud-list/{YamlEditor,MetricsPanel,ResourceActions}.test.tsx`
 * — those cover widget behaviour in isolation; THIS file pins the
 * MOUNT POINT (the `ResourceDetailPage` tab/Overview rendering)
 * so a future refactor cannot accidentally re-introduce theater.
 */

import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

import { ResourceDetailPage } from './ResourceDetailPage'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

const sampleDeployment: K8sObject = {
  apiVersion: 'apps/v1',
  kind: 'Deployment',
  metadata: {
    name: 'wp',
    namespace: 'default',
    uid: 'uid-dep',
    creationTimestamp: '2026-05-10T10:00:00Z',
    labels: { app: 'wp' },
  },
  spec: { replicas: 3, selector: { matchLabels: { app: 'wp' } } } as Record<string, unknown>,
  status: { readyReplicas: 3, availableReplicas: 3 } as Record<string, unknown>,
}

afterEach(() => cleanup())
beforeEach(() => {
  // MetricsPanel and YamlEditor fire REST calls on mount; stub fetch so
  // the assertions only need to observe the panel test-ids, not the
  // ultimate REST response. The widgets' own unit tests cover the
  // network-success / network-error branches.
  global.fetch = vi.fn().mockResolvedValue(
    new Response(
      JSON.stringify({
        kind: 'deployment',
        namespace: 'default',
        name: 'wp',
        current: { cpuMilli: 0, memBytes: 0 },
        series: [],
        source: 'unavailable',
      }),
      { status: 200, headers: { 'Content-Type': 'application/json' } },
    ),
  )
})

describe('ResourceDetailPage — Group A widget mount points (Refs #1099, follow-up to PR #2059)', () => {
  it('Overview tab mounts ResourceActions inline for a tier-admin operator', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="deployment"
        ns="default"
        name="wp"
        tab="overview"
        initialObj={sampleDeployment}
        isTierAdmin={true}
      />,
    )
    // ResourceActions widget present (would render `resource-actions-disabled`
    // banner if isTierAdmin was false — the not-dark assertion is the
    // visible action surface, not the disabled banner).
    expect(screen.getByTestId('resource-actions')).toBeTruthy()
    // Scale + Restart + Delete buttons all render for a Deployment.
    expect(screen.getByTestId('resource-actions-scale')).toBeTruthy()
    expect(screen.getByTestId('resource-actions-restart')).toBeTruthy()
    expect(screen.getByTestId('resource-actions-delete')).toBeTruthy()
  })

  it('Overview tab shows the disabled banner when caller is not tier-admin (server is still authoritative)', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="deployment"
        ns="default"
        name="wp"
        tab="overview"
        initialObj={sampleDeployment}
        isTierAdmin={false}
      />,
    )
    expect(screen.getByTestId('resource-actions-disabled')).toBeTruthy()
    // Action buttons hidden client-side; server-side authorization
    // remains the source of truth per INVIOLABLE-PRINCIPLES.md #5.
    expect(screen.queryByTestId('resource-actions-scale')).toBeNull()
    expect(screen.queryByTestId('resource-actions-restart')).toBeNull()
    expect(screen.queryByTestId('resource-actions-delete')).toBeNull()
  })

  it('Delete button opens a type-the-name confirmation modal (no one-click delete)', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="deployment"
        ns="default"
        name="wp"
        tab="overview"
        initialObj={sampleDeployment}
        isTierAdmin={true}
      />,
    )
    fireEvent.click(screen.getByTestId('resource-actions-delete'))
    const modal = screen.getByTestId('resource-actions-delete-modal')
    expect(modal).toBeTruthy()
    // Commit button is disabled until the name is typed exactly — the
    // type-the-name gate is the canonical destructive-action defence
    // for #1099. Locking it here ensures a future refactor cannot
    // silently downgrade to a one-click delete.
    const commit = screen.getByTestId('resource-actions-delete-commit') as HTMLButtonElement
    expect(commit.disabled).toBe(true)
  })

  it('Metrics tab mounts MetricsPanel (initial fetch fires; tab is not dark)', async () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="deployment"
        ns="default"
        name="wp"
        tab="metrics"
        initialObj={sampleDeployment}
      />,
    )
    // The widget initially renders its loading state, then transitions
    // to either `metrics-panel` (lit) or `metrics-panel-unavailable`
    // (metrics-server not installed). Both are operator-visible — the
    // dark anti-pattern would be no test-id rendering at all.
    await waitFor(() => {
      const lit =
        screen.queryByTestId('metrics-panel') ||
        screen.queryByTestId('metrics-panel-unavailable') ||
        screen.queryByTestId('metrics-panel-error') ||
        screen.queryByTestId('metrics-panel-loading')
      expect(lit).not.toBeNull()
    })
    // Fetch should have fired against the metrics endpoint — the
    // dark-via-no-fetch anti-pattern (component renders empty without
    // ever calling the API) is the explicit regression we want to
    // pin against.
    const fetchMock = global.fetch as ReturnType<typeof vi.fn>
    expect(fetchMock).toHaveBeenCalled()
    const url = fetchMock.mock.calls[0][0] as string
    expect(url).toMatch(/\/k8s\/metrics\/deployment\/default\/wp/)
  })

  it('YAML tab mounts YamlEditor with the live object seeded into the editor (non-empty)', async () => {
    const { container } = render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="deployment"
        ns="default"
        name="wp"
        tab="yaml"
        initialObj={sampleDeployment}
      />,
    )
    expect(screen.getByTestId('yaml-editor')).toBeTruthy()
    // The editor must seed from the live object so the operator can see the
    // resource's current shape — the dark anti-pattern would be an empty
    // editor on a populated resource. The editor is the shared CodeView
    // (lazy CodeMirror), so wait for the editor surface to mount and assert
    // against its rendered content.
    await waitFor(() => expect(container.querySelector('.cm-content')).toBeTruthy())
    const editorText = container.querySelector('.cm-content')?.textContent ?? ''
    expect(editorText.length).toBeGreaterThan(0)
    expect(editorText).toContain('Deployment')
    expect(editorText).toContain('wp')
    // Validate + Apply buttons mount unconditionally — the server is
    // the authoritative gate per INVIOLABLE-PRINCIPLES.md #5.
    expect(screen.getByTestId('yaml-editor-validate')).toBeTruthy()
    expect(screen.getByTestId('yaml-editor-apply')).toBeTruthy()
  })
})
