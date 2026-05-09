/**
 * MetricsPanel.test.tsx — EPIC-4 Slice R5 (#1099) — sparkline render +
 * unavailable empty-state.
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup } from '@testing-library/react'

import { MetricsPanel } from './MetricsPanel'
import type { MetricsResponse } from '@/pages/sovereign/cloud-list/resource.api'

afterEach(() => cleanup())

describe('MetricsPanel', () => {
  it('renders unavailable state when source is unavailable', () => {
    const initial: MetricsResponse = {
      kind: 'pod',
      ns: 'default',
      name: 'wp-1',
      current: {},
      series: [],
      source: 'unavailable',
    } as unknown as MetricsResponse
    render(
      <MetricsPanel deploymentId="dep" kind="pod" ns="default" name="wp-1" initial={initial} />,
    )
    expect(screen.getByTestId('metrics-panel-unavailable')).toBeTruthy()
  })

  it('renders CPU + memory + sparkline when source = metrics.k8s.io', () => {
    const initial: MetricsResponse = {
      kind: 'pod',
      name: 'wp-1',
      namespace: 'default',
      current: { cpuMilli: 250, memBytes: 512 * 1024 * 1024 },
      series: [
        { cpuMilli: 100, memBytes: 100 * 1024 * 1024 },
        { cpuMilli: 250, memBytes: 512 * 1024 * 1024 },
      ],
      source: 'metrics.k8s.io',
    }
    render(
      <MetricsPanel deploymentId="dep" kind="pod" ns="default" name="wp-1" initial={initial} />,
    )
    expect(screen.getByTestId('metrics-panel')).toBeTruthy()
    expect(screen.getByTestId('metrics-panel-cpu').textContent).toContain('250m')
    expect(screen.getByTestId('metrics-panel-memory').textContent).toContain('Mi')
  })

  it('renders pod-count footer when present', () => {
    const initial: MetricsResponse = {
      kind: 'deployment',
      name: 'wp',
      namespace: 'default',
      current: { cpuMilli: 100, memBytes: 0, podCount: 3 },
      series: [{ cpuMilli: 100, memBytes: 0, podCount: 3 }],
      source: 'metrics.k8s.io',
    }
    render(
      <MetricsPanel deploymentId="dep" kind="deployment" ns="default" name="wp" initial={initial} />,
    )
    expect(screen.getByTestId('metrics-panel-podcount').textContent).toContain('3 pods')
  })
})
