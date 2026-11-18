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

describe('ResourceActions — owner in Git and break-glass', () => {
  const answer = (status: number, body: unknown) => Promise.resolve(new Response(JSON.stringify(body), { status }))
  function deleteWithReason(reason: string) {
    fireEvent.change(screen.getByTestId('resource-actions-reason'), { target: { value: reason } })
    fireEvent.click(screen.getByTestId('resource-actions-delete'))
    fireEvent.change(screen.getByTestId('resource-actions-delete-confirm'), { target: { value: 'wp' } })
    fireEvent.click(screen.getByTestId('resource-actions-delete-commit'))
  }

  it('hides scale and delete for a Git-owned object, shows Managed by Git with the owner, keeps restart', () => {
    render(<ResourceActions deploymentId="dep" kind="deployment" ns="shop" name="wp" gitOwner="HelmRelease shop/wp" />)
    expect(screen.getByTestId('resource-actions-managed-by-git').textContent).toContain('Managed by Git: HelmRelease shop/wp')
    for (const id of ['scale', 'delete', 'reason']) expect(screen.queryByTestId(`resource-actions-${id}`)).toBeNull()
    expect(screen.getByTestId('resource-actions-restart')).toBeTruthy()
  })

  it('sends the reason with scale and delete', async () => {
    ;(global.fetch as ReturnType<typeof vi.fn>).mockImplementation(() => answer(200, { name: 'wp', replicas: 2, message: 'deleted' }))
    render(<ResourceActions deploymentId="dep" kind="deployment" ns="shop" name="wp" />)
    fireEvent.change(screen.getByTestId('resource-actions-replicas'), { target: { value: '2' } })
    fireEvent.change(screen.getByTestId('resource-actions-reason'), { target: { value: 'load test of the shop' } })
    fireEvent.click(screen.getByTestId('resource-actions-scale'))
    await waitFor(() => expect(screen.getByTestId('resource-actions-msg').textContent).toContain('2'))
    deleteWithReason('load test of the shop')
    await waitFor(() => expect(screen.getByTestId('resource-actions-msg').textContent).toBe('deleted'))
    const calls = (global.fetch as ReturnType<typeof vi.fn>).mock.calls as [string, RequestInit][]
    expect(JSON.parse(calls[0][1].body as string)).toEqual({ replicas: 2, reason: 'load test of the shop' })
    expect(calls[1][0]).toContain('/k8s/deployment/shop/wp?reason=load%20test%20of%20the%20shop')
  })

  it('turns an answer 409 managed-by-git into Managed by Git with the owner', async () => {
    const owner = 'the intent lock (helmreleases.helm.toolkit.fluxcd.io)'
    ;(global.fetch as ReturnType<typeof vi.fn>).mockImplementation(() => answer(409, { error: 'managed-by-git', code: '409', owner }))
    render(<ResourceActions deploymentId="dep" kind="helmrelease" ns="shop" name="wp" />)
    deleteWithReason('remove the old release')
    await waitFor(() => expect(screen.getByTestId('resource-actions-managed-by-git').textContent).toContain(owner))
    expect(screen.queryByTestId('resource-actions-delete')).toBeNull()
    expect(screen.queryByTestId('resource-actions-err')).toBeNull()
  })
})
