/**
 * WorkerNodeFormFields.test.tsx — regression guard for UAT row 214
 * (issue #5100 / PR #5102).
 *
 * The Taints hint + placeholder used to read the banned org-rename term
 * as a `key=value` example (`…=dmz:NoSchedule`) — a purely illustrative
 * string; no shipped chart uses that taint key. PR #5102 rewrote both to
 * `org=dmz:NoSchedule` per docs/GLOSSARY.md. This test locks the
 * rendered copy so it can't silently regress back to the banned term.
 *
 * Carried forward into PR #5247 (the code-token purge) so #5203 can be
 * closed as superseded without dropping this guard.
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
  it('renders the org=dmz example, not the banned-term example', () => {
    render(
      <WorkerNodeFormFields values={baseValues} onChange={vi.fn()} provider="hetzner" />,
    )
    expect(screen.getByText(/org=dmz:NoSchedule/)).toBeTruthy()
    expect(screen.queryByText(/tenant=dmz/i)).toBeNull()
  })

  it('the taints input placeholder is org=dmz:NoSchedule', () => {
    render(
      <WorkerNodeFormFields values={baseValues} onChange={vi.fn()} provider="hetzner" />,
    )
    const taints = screen.getByTestId('worker-node-form-taints') as HTMLInputElement
    expect(taints.placeholder).toBe('org=dmz:NoSchedule')
  })

  it('no rendered text on the form contains the banned org-rename term', () => {
    const { container } = render(
      <WorkerNodeFormFields values={baseValues} onChange={vi.fn()} provider="hetzner" />,
    )
    expect(container.textContent?.toLowerCase()).not.toContain('tenant')
  })
})
