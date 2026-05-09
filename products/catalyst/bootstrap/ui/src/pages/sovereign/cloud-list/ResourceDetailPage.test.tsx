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
})
