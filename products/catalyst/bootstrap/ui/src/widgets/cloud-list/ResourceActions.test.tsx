/**
 * ResourceActions.test.tsx — EPIC-4 Slice R6 (#1099) — scale / restart
 * / delete confirm gates.
 */

import { describe, it, expect, afterEach, beforeEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'

import { ResourceActions } from './ResourceActions'

afterEach(() => cleanup())
beforeEach(() => {
  global.fetch = vi.fn()
})

describe('ResourceActions — visibility', () => {
  it('shows the disabled banner when the caller is not tier-admin', () => {
    render(<ResourceActions deploymentId="dep" kind="deployment" ns="default" name="wp" disabled />)
    expect(screen.getByTestId('resource-actions-disabled')).toBeTruthy()
    expect(screen.queryByTestId('resource-actions-scale')).toBeNull()
    expect(screen.queryByTestId('resource-actions-restart')).toBeNull()
    expect(screen.queryByTestId('resource-actions-delete')).toBeNull()
  })

  it('hides scale + restart for kinds that do not support them', () => {
    render(<ResourceActions deploymentId="dep" kind="pod" ns="default" name="p" />)
    expect(screen.queryByTestId('resource-actions-scale')).toBeNull()
    expect(screen.queryByTestId('resource-actions-restart')).toBeNull()
    expect(screen.getByTestId('resource-actions-delete')).toBeTruthy()
  })

  it('shows scale + restart for Deployment', () => {
    render(<ResourceActions deploymentId="dep" kind="deployment" ns="default" name="wp" />)
    expect(screen.getByTestId('resource-actions-scale')).toBeTruthy()
    expect(screen.getByTestId('resource-actions-restart')).toBeTruthy()
  })
})

describe('ResourceActions — scale flow', () => {
  it('rejects negative replicas without firing the network call', async () => {
    render(<ResourceActions deploymentId="dep" kind="deployment" ns="default" name="wp" />)
    fireEvent.change(screen.getByTestId('resource-actions-replicas'), { target: { value: '-3' } })
    fireEvent.click(screen.getByTestId('resource-actions-scale'))
    await waitFor(() => {
      expect(screen.getByTestId('resource-actions-err').textContent).toContain('non-negative')
    })
    expect(global.fetch).not.toHaveBeenCalled()
  })

  it('happy path POSTs to /scale and surfaces success', async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockResolvedValueOnce(
      new Response(JSON.stringify({ name: 'wp', replicas: 5 }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    )
    const onComplete = vi.fn()
    render(
      <ResourceActions
        deploymentId="dep"
        kind="deployment"
        ns="default"
        name="wp"
        onActionComplete={onComplete}
      />,
    )
    fireEvent.change(screen.getByTestId('resource-actions-replicas'), { target: { value: '5' } })
    fireEvent.click(screen.getByTestId('resource-actions-scale'))
    await waitFor(() => {
      expect(screen.getByTestId('resource-actions-msg').textContent).toContain('5')
    })
    expect(onComplete).toHaveBeenCalledWith('scale')
  })
})

describe('ResourceActions — delete confirmation gate', () => {
  it('opens modal on click, confirm button stays disabled until name typed', () => {
    render(<ResourceActions deploymentId="dep" kind="pod" ns="default" name="wp-1" />)
    fireEvent.click(screen.getByTestId('resource-actions-delete'))
    const modal = screen.getByTestId('resource-actions-delete-modal')
    expect(modal).toBeTruthy()
    const commit = screen.getByTestId('resource-actions-delete-commit') as HTMLButtonElement
    expect(commit.disabled).toBe(true)
    fireEvent.change(screen.getByTestId('resource-actions-delete-confirm'), {
      target: { value: 'wp-1' },
    })
    expect(commit.disabled).toBe(false)
  })

  it('cancel closes the modal without firing network call', () => {
    render(<ResourceActions deploymentId="dep" kind="pod" ns="default" name="wp-1" />)
    fireEvent.click(screen.getByTestId('resource-actions-delete'))
    fireEvent.click(screen.getByTestId('resource-actions-delete-cancel'))
    expect(screen.queryByTestId('resource-actions-delete-modal')).toBeNull()
    expect(global.fetch).not.toHaveBeenCalled()
  })
})
