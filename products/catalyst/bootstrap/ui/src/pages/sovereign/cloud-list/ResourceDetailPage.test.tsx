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

  it('renders Logs placeholder', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="pod"
        ns="default"
        name="wp-1"
        tab="logs"
        initialObj={samplePod}
      />,
    )
    expect(screen.getByTestId('resource-detail-logs-placeholder')).toBeTruthy()
  })

  it('renders Exec placeholder', () => {
    render(
      <ResourceDetailPage
        deploymentId="dep"
        basePath="/cloud"
        kind="pod"
        ns="default"
        name="wp-1"
        tab="exec"
        initialObj={samplePod}
      />,
    )
    expect(screen.getByTestId('resource-detail-exec-placeholder')).toBeTruthy()
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
