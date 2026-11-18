/** ResourcesApplyPage.test.tsx — the apply sends its reason; 409 managed-by-git shows Managed by Git. */

import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

import { ResourcesApplyPage } from './ResourcesApplyPage'

vi.mock('@tanstack/react-router', () => ({ useParams: () => ({ deploymentId: 'dep' }) }))
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({ useResolvedDeploymentId: () => ({ deploymentId: 'dep' }) }))
vi.mock('../PortalShell', () => ({ PortalShell: ({ children }: { children: React.ReactNode }) => <div>{children}</div> }))

const YAML = 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: banner\n  namespace: shop\n'

function applyWith(status: number, body: unknown) {
  global.fetch = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify(body), { status }))
  render(
    <QueryClientProvider client={new QueryClient()}>
      <ResourcesApplyPage />
    </QueryClientProvider>,
  )
  fireEvent.change(screen.getByTestId('apply-yaml-editor'), { target: { value: YAML } })
  fireEvent.change(screen.getByTestId('apply-reason'), { target: { value: 'new banner for the sale' } })
  fireEvent.click(screen.getByTestId('apply-submit'))
}

afterEach(() => cleanup())

describe('ResourcesApplyPage — break-glass apply', () => {
  it('posts the YAML and the reason, and no commit message', async () => {
    applyWith(200, { items: [{ kind: 'ConfigMap', namespace: 'shop', name: 'banner', created: true }], created: 1, updated: 0 })
    await waitFor(() => expect(screen.getByTestId('apply-result-row-ConfigMap-shop-banner').textContent).toContain('created'))
    const [url, init] = (global.fetch as ReturnType<typeof vi.fn>).mock.calls[0] as [string, RequestInit]
    expect(url).toContain('/v1/sovereigns/dep/k8s/apply')
    expect(JSON.parse(init.body as string)).toEqual({ yaml: YAML, reason: 'new banner for the sale' })
  })

  it('an answer 409 managed-by-git shows Managed by Git with the owner', async () => {
    applyWith(409, { error: 'managed-by-git', code: '409', owner: 'HelmRelease shop/wp' })
    await waitFor(() => expect(screen.getByTestId('apply-managed-by-git').textContent).toContain('Managed by Git: HelmRelease shop/wp'))
    expect(screen.queryByTestId('apply-error')).toBeNull()
  })
})
