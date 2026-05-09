/**
 * PublishPage.test.tsx — unit tests for EPIC-2 slice P (#1097)
 * Blueprint publishing form.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

// Stub useResolvedDeploymentId so the test bypasses TanStack Router.
vi.mock('@/shared/lib/useResolvedDeploymentId', () => ({
  useResolvedDeploymentId: () => ({ deploymentId: 'dep-1', isLoading: false }),
}))

import { PublishPage } from './PublishPage'

function withQuery(node: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return <QueryClientProvider client={qc}>{node}</QueryClientProvider>
}

afterEach(() => cleanup())

describe('PublishPage', () => {
  it('renders form with default template', () => {
    render(withQuery(<PublishPage initialOrg="acme" disableNetwork />))
    expect(screen.getByTestId('publish-page')).toBeTruthy()
    expect((screen.getByTestId('publish-page-org') as HTMLInputElement).value).toBe('acme')
    const yaml = screen.getByTestId('publish-page-yaml') as HTMLTextAreaElement
    expect(yaml.value).toContain('apiVersion: catalyst.openova.io/v1')
  })

  it('Publish button disabled when org is empty', () => {
    render(withQuery(<PublishPage disableNetwork />))
    const btn = screen.getByTestId('publish-page-submit') as HTMLButtonElement
    expect(btn.disabled).toBe(true)
  })

  it('shows success state on submit (test seam)', async () => {
    render(withQuery(<PublishPage initialOrg="acme" disableNetwork />))
    fireEvent.click(screen.getByTestId('publish-page-submit'))
    expect(await screen.findByTestId('publish-page-success')).toBeTruthy()
  })
})
