/**
 * WorkerNodeFormFields.test.tsx — regression guard for UAT row 214
 * (issue #5100 / PR #5102).
 *
 * The Taints hint + placeholder used to read `tenant=dmz:NoSchedule`
 * (a purely illustrative example — no shipped chart uses a `tenant=`
 * taint key). PR #5102 rewrote both to `org=dmz:NoSchedule` per
 * docs/GLOSSARY.md ("tenant" → Organization). This test locks the
 * rendered copy so it can't silently regress back to the banned term.
 */

import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { WorkerNodeFormFields } from './WorkerNodeFormFields'

afterEach(() => cleanup())

const baseValues = {
  name: 'worker-eu-3',
  sku: '',
  role: 'worker' as const,
  taints: '',
  labels: '',
}

describe('WorkerNodeFormFields — Taints hint (row 214)', () => {
  it('renders the org=dmz example, not tenant=dmz', () => {
    render(
      <WorkerNodeFormFields values={baseValues} onChange={vi.fn()} provider="hetzner" />,
    )
    expect(screen.getByText(/org=dmz:NoSchedule/)).toBeTruthy()
    expect(screen.queryByText(/tenant=dmz/i)).toBeNull()
  })

  it('the taints input placeholder is org=dmz:NoSchedule, not tenant=dmz:NoSchedule', () => {
    render(
      <WorkerNodeFormFields values={baseValues} onChange={vi.fn()} provider="hetzner" />,
    )
    const taints = screen.getByTestId('worker-node-form-taints') as HTMLInputElement
    expect(taints.placeholder).toBe('org=dmz:NoSchedule')
  })

  it('no rendered text on the form contains the banned "tenant" term', () => {
    const { container } = render(
      <WorkerNodeFormFields values={baseValues} onChange={vi.fn()} provider="hetzner" />,
    )
    expect(container.textContent?.toLowerCase()).not.toContain('tenant')
  })
})
