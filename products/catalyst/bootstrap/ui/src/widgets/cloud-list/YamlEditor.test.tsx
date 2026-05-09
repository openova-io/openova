/**
 * YamlEditor.test.tsx — EPIC-4 Slice R3 (#1099) — flux-vs-manual
 * branch + diff render + validate flow.
 */

import { describe, it, expect, afterEach, vi, beforeEach } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

import { YamlEditor } from './YamlEditor'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

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
    labels: { 'app.kubernetes.io/managed-by': 'flux' },
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

  it('renders managed-by: flux when label is set', () => {
    render(<YamlEditor deploymentId="dep" kind="service" ns="default" name="svc-flux" obj={fluxObj} />)
    expect(screen.getByTestId('yaml-editor-managed-by').textContent).toContain('flux')
    expect(screen.getByTestId('yaml-editor-apply').textContent).toContain('Open PR')
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
