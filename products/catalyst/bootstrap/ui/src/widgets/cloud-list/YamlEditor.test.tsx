/**
 * YamlEditor.test.tsx — EPIC-4 Slice R3 (#1099) — owner in Git vs
 * break-glass apply + diff render + validate flow.
 */

import { describe, it, expect, afterEach, vi, beforeEach } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import type { EditorView } from '@codemirror/view'

import { YamlEditor } from './YamlEditor'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

/** Replace the editor's whole document, mimicking a user edit. The editor is
 *  a CodeMirror contenteditable (not a textarea), so edits are driven through
 *  the live EditorView captured via YamlEditor's onCreateEditor test seam. */
function setEditorText(view: EditorView, text: string) {
  view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } })
}

afterEach(() => cleanup())
beforeEach(() => {
  global.fetch = vi.fn()
})

const manualObj: K8sObject = {
  apiVersion: 'v1',
  kind: 'ConfigMap',
  metadata: {
    name: 'cm-manual',
    namespace: 'default',
    labels: { 'managed-by': 'manual' },
  },
  data: { foo: 'bar' } as unknown as Record<string, unknown>,
} as K8sObject

const fluxObj: K8sObject = {
  apiVersion: 'v1',
  kind: 'Service',
  metadata: {
    name: 'svc-flux',
    namespace: 'default',
    labels: { 'helm.toolkit.fluxcd.io/name': 'svc-flux', 'helm.toolkit.fluxcd.io/namespace': 'default' },
  },
  spec: { ports: [] } as Record<string, unknown>,
} as K8sObject

describe('YamlEditor — flux vs manual branch', () => {
  it('renders managed-by: manual when no flux annotation present', () => {
    render(
      <YamlEditor deploymentId="dep" kind="configmap" ns="default" name="cm-manual" obj={manualObj} />,
    )
    expect(screen.getByTestId('yaml-editor-managed-by').textContent).toContain('manual')
    expect(screen.getByTestId('yaml-editor-apply').textContent).toContain('Apply')
  })

  it('renders Managed by Git with the owner and offers no Apply for a Git-owned object', () => {
    render(<YamlEditor deploymentId="dep" kind="service" ns="default" name="svc-flux" obj={fluxObj} />)
    expect(screen.getByTestId('yaml-editor-managed-by').textContent).toContain('Managed by Git: HelmRelease default/svc-flux')
    expect(screen.queryByTestId('yaml-editor-apply')).toBeNull()
    expect(screen.getByTestId('yaml-editor-validate')).toBeTruthy()
  })
})

describe('YamlEditor — diff toggle', () => {
  it('toggles diff view on / off', () => {
    render(<YamlEditor deploymentId="dep" kind="configmap" ns="default" name="cm-manual" obj={manualObj} />)
    expect(screen.queryByTestId('yaml-editor-diff')).toBeNull()
    fireEvent.click(screen.getByTestId('yaml-editor-toggle-diff'))
    expect(screen.getByTestId('yaml-editor-diff')).toBeTruthy()
    fireEvent.click(screen.getByTestId('yaml-editor-toggle-diff'))
    expect(screen.queryByTestId('yaml-editor-diff')).toBeNull()
  })
})

describe('YamlEditor — validate dry-run', () => {
  it('happy path surfaces success message', async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      new Response(JSON.stringify({ name: 'cm-manual', dryRun: true, resourceVersion: '7' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    render(<YamlEditor deploymentId="dep" kind="configmap" ns="default" name="cm-manual" obj={manualObj} />)
    fireEvent.click(screen.getByTestId('yaml-editor-validate'))
    await waitFor(() => {
      expect(screen.getByTestId('yaml-editor-validate-ok').textContent).toContain('resourceVersion 7')
    })
  })

  it('error path surfaces error message', async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      new Response('boom', { status: 400 }),
    )
    render(<YamlEditor deploymentId="dep" kind="configmap" ns="default" name="cm-manual" obj={manualObj} />)
    fireEvent.click(screen.getByTestId('yaml-editor-validate'))
    await waitFor(() => {
      expect(screen.getByTestId('yaml-editor-validate-err').textContent).toContain('400')
    })
  })
})

describe('YamlEditor — break-glass Apply', () => {
  const directObj: K8sObject = {
    apiVersion: 'v1',
    kind: 'ConfigMap',
    metadata: {
      name: 'cm-flux',
      namespace: 'acme',
      labels: {
        'catalyst.openova.io/organization': 'acme',
      },
    },
    data: { foo: 'bar' } as unknown as Record<string, unknown>,
  } as K8sObject

  it('clicking Apply posts the YAML and the reason to /apply', async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      new Response(
        JSON.stringify({ name: 'cm-flux', dryRun: false, resourceVersion: '42' }),
        { status: 200, headers: { 'Content-Type': 'application/json' } },
      ),
    )
    let view: EditorView | null = null
    render(
      <YamlEditor
        deploymentId="dep-z3"
        kind="configmap"
        ns="acme"
        name="cm-flux"
        obj={directObj}
        onCreateEditor={(v) => {
          view = v
        }}
      />,
    )
    // Edit the editor so dirty=true (Apply enabled).
    await waitFor(() => expect(view).not.toBeNull())
    setEditorText(view!, 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: cm-flux\n')
    fireEvent.change(screen.getByTestId('yaml-editor-reason'), { target: { value: 'rotate the cache key' } })
    await waitFor(() =>
      expect((screen.getByTestId('yaml-editor-apply') as HTMLButtonElement).disabled).toBe(false),
    )
    fireEvent.click(screen.getByTestId('yaml-editor-apply'))

    await waitFor(() => {
      expect(screen.getByTestId('yaml-editor-apply-ok').textContent).toContain('resourceVersion 42')
    })

    // Verify the request shape.
    const fetchSpy = global.fetch as ReturnType<typeof vi.fn>
    expect(fetchSpy).toHaveBeenCalledTimes(1)
    const [calledURL, calledOpts] = fetchSpy.mock.calls[0] as [string, RequestInit]
    expect(calledURL).toContain('/k8s/configmap/acme/cm-flux/apply')
    const body = JSON.parse(calledOpts.body as string)
    expect(body.yaml).toContain('cm-flux')
    expect(body.reason).toBe('rotate the cache key')
  })

  it('an answer 409 managed-by-git shows Managed by Git with the owner and removes Apply', async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      new Response(JSON.stringify({ error: 'managed-by-git', code: '409', owner: 'Kustomization flux-system/apps' }), { status: 409 }),
    )
    let view: EditorView | null = null
    render(
      <YamlEditor
        deploymentId="dep-z3-err"
        kind="configmap"
        ns="acme"
        name="cm-flux"
        obj={directObj}
        onCreateEditor={(v) => {
          view = v
        }}
      />,
    )
    await waitFor(() => expect(view).not.toBeNull())
    setEditorText(view!, 'changed: 1\n')
    fireEvent.change(screen.getByTestId('yaml-editor-reason'), { target: { value: 'rotate the cache key' } })
    await waitFor(() =>
      expect((screen.getByTestId('yaml-editor-apply') as HTMLButtonElement).disabled).toBe(false),
    )
    fireEvent.click(screen.getByTestId('yaml-editor-apply'))
    await waitFor(() => {
      expect(screen.getByTestId('yaml-editor-managed-by').textContent).toContain('Managed by Git: Kustomization flux-system/apps')
    })
    expect(screen.queryByTestId('yaml-editor-apply')).toBeNull()
    expect(screen.queryByTestId('yaml-editor-apply-err')).toBeNull()
  })
})
