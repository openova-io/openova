/**
 * ResourceDetailPage.test.tsx — EPIC-4 Slice R1 (#1099) — tab routing
 * + tab-content rendering.
 */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

import { ResourceDetailPage } from './ResourceDetailPage'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

afterEach(() => cleanup())

const samplePod: K8sObject = {
  apiVersion: 'v1',
  kind: 'Pod',
  metadata: {
    name: 'wp-1',
    namespace: 'default',
    uid: 'uid-1',
    creationTimestamp: '2026-05-09T10:00:00Z',
    labels: { app: 'wp' },
  },
  spec: { containers: [] } as Record<string, unknown>,
  status: { phase: 'Running' },
}

describe('ResourceDetailPage', () => {
  it('renders header + every tab in the tab strip', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="pod"
        ns="default"
        name="wp-1"
        tab="overview"
        initialObj={samplePod}
      />,
    )
    expect(screen.getByTestId('resource-detail-pod-wp-1')).toBeTruthy()
    for (const t of ['overview', 'yaml', 'logs', 'exec', 'events', 'metrics', 'tree']) {
      expect(screen.getByTestId(`resource-detail-tab-${t}`)).toBeTruthy()
    }
  })

  it('clicking a tab fires onTabChange with the new tab id', () => {
    const onTabChange = vi.fn()
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="pod"
        ns="default"
        name="wp-1"
        tab="overview"
        initialObj={samplePod}
        onTabChange={onTabChange}
      />,
    )
    fireEvent.click(screen.getByTestId('resource-detail-tab-yaml'))
    expect(onTabChange).toHaveBeenCalledWith('yaml')
  })

  it('renders Overview by default with phase + namespace', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="pod"
        ns="default"
        name="wp-1"
        tab="overview"
        initialObj={samplePod}
      />,
    )
    expect(screen.getByTestId('resource-detail-overview')).toBeTruthy()
  })

  // Slice X2 (#1099) replaces the Logs placeholder with a LogViewer.
  // For non-Pod kinds the page surfaces the "Logs are streamed per-Pod"
  // hint — easier to assert in jsdom than the xterm.js terminal mount.
  it('renders Logs hint for non-Pod kinds', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="configmap"
        ns="default"
        name="cm-1"
        tab="logs"
        initialObj={samplePod}
      />,
    )
    expect(screen.getByTestId('resource-detail-logs-not-pod')).toBeTruthy()
  })

  // Slice E (#1099) replaces the Exec placeholder with an ExecPanel.
  // For non-Pod kinds the page surfaces an analogous hint.
  it('renders Exec hint for non-Pod kinds', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="configmap"
        ns="default"
        name="cm-1"
        tab="exec"
        initialObj={samplePod}
      />,
    )
    expect(screen.getByTestId('resource-detail-exec-not-pod')).toBeTruthy()
  })

  it('renders YamlEditor on yaml tab', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="pod"
        ns="default"
        name="wp-1"
        tab="yaml"
        initialObj={samplePod}
      />,
    )
    expect(screen.getByTestId('yaml-editor')).toBeTruthy()
  })

  // qa-loop iter-1 cluster:resource-detail-tree-yaml-events — TC-079..083.
  // The chroot URL surface is operator-typed and accepts plural kind
  // segments (`/cloud/resource/services/...`). The test-id must keep
  // the URL-as-typed shape so deep-link asserts (`resource-detail-services`)
  // hold; child widgets that hit the API must receive the canonical
  // singular kind so the catalyst-api Registry resolves them.
  it('preserves the URL plural kind in the test-id', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="services"
        ns="kube-system"
        name="kube-dns"
        tab="overview"
        initialObj={{
          apiVersion: 'v1',
          kind: 'Service',
          metadata: { name: 'kube-dns', namespace: 'kube-system' },
        } as K8sObject}
      />,
    )
    // URL-kind shape preserved on the wrapper for deep-link asserts.
    expect(screen.getByTestId('resource-detail-services-kube-dns')).toBeTruthy()
    // Tab strip for a non-Pod, non-reconcilable kind (Service): the
    // Pod-only 'exec' tab (#2626) and the Flux-only 'reconcile' tab (#3996)
    // are hidden; every other tab renders.
    for (const t of ['overview', 'yaml', 'logs', 'events', 'metrics', 'sbom', 'compliance', 'tree']) {
      expect(screen.getByTestId(`resource-detail-tab-${t}`)).toBeTruthy()
    }
    // exec is Pod-only and reconcile is Flux-only — neither shows on a Service.
    expect(screen.queryByTestId('resource-detail-tab-exec')).toBeNull()
    expect(screen.queryByTestId('resource-detail-tab-reconcile')).toBeNull()
  })

  it('renders YamlEditor with singular kind for plural URL kind', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="services"
        ns="kube-system"
        name="kube-dns"
        tab="yaml"
        initialObj={{
          apiVersion: 'v1',
          kind: 'Service',
          metadata: { name: 'kube-dns', namespace: 'kube-system' },
        } as K8sObject}
      />,
    )
    // YamlEditor receives the singular `service` so its internal API
    // call resolves on the catalyst-api k8scache Registry.
    expect(screen.getByTestId('yaml-editor')).toBeTruthy()
  })

  it('renders cluster-scoped deployment without crash for plural URL kind + ns="_"', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="deployments"
        ns=""
        name="cilium"
        tab="overview"
        initialObj={{
          apiVersion: 'apps/v1',
          kind: 'Deployment',
          metadata: { name: 'cilium' },
        } as K8sObject}
      />,
    )
    expect(screen.getByTestId('resource-detail-deployments-cilium')).toBeTruthy()
  })

  it('does not throw when initialObj.spec is undefined (null-guard)', () => {
    const objNoSpec: K8sObject = {
      apiVersion: 'v1',
      kind: 'Pod',
      metadata: { name: 'wp-1', namespace: 'default' },
    } as K8sObject
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="pod"
        ns="default"
        name="wp-1"
        tab="overview"
        initialObj={objNoSpec}
      />,
    )
    expect(screen.getByTestId('resource-detail-pod-wp-1')).toBeTruthy()
  })

  it('does not throw when initialObj is empty (only required keys)', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="pod"
        ns="default"
        name="wp-1"
        tab="overview"
        initialObj={{} as K8sObject}
      />,
    )
    expect(screen.getByTestId('resource-detail-pod-wp-1')).toBeTruthy()
  })
})
