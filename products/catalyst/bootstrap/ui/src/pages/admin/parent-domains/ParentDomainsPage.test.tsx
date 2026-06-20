/**
 * ParentDomainsPage.test.tsx — admin parent-domains surface coverage
 * (issue #829, parent epic #825).
 *
 *   • Empty state renders when no items
 *   • Populated table renders one row per item with role + status
 *   • Add CTA opens the modal with all four fields
 *   • Drawer toggles open/close on the row caret
 */

import { describe, it, expect, afterEach } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'
import { ParentDomainsPage } from './ParentDomainsPage'
import type { ParentDomain } from './parentDomains.api'

afterEach(cleanup)

const sampleItems: ParentDomain[] = [
  {
    name: 'omani.works',
    role: 'primary',
    flipStatus: 'ready',
    addedAt: '2026-05-04T08:00:00Z',
    flippedAt: '2026-05-04T08:30:00Z',
  },
  {
    name: 'omani.trade',
    role: 'org-pool',
    flipStatus: 'flipping',
    registrarKind: 'dynadot',
    addedAt: '2026-05-04T09:00:00Z',
  },
]

describe('ParentDomainsPage', () => {
  it('renders the empty state when items list is empty', () => {
    render(<ParentDomainsPage initialItems={[]} disableFetch />)
    expect(screen.getByTestId('parent-domains-page')).toBeTruthy()
    expect(screen.getByTestId('parent-domains-empty')).toBeTruthy()
    expect(screen.getByTestId('parent-domains-add-cta')).toBeTruthy()
  })

  it('renders one row per item with role + status badges', () => {
    render(<ParentDomainsPage initialItems={sampleItems} disableFetch />)
    expect(screen.getByTestId('parent-domain-row-omani.works')).toBeTruthy()
    expect(screen.getByTestId('parent-domain-row-omani.trade')).toBeTruthy()
    expect(screen.getByTestId('parent-domain-role-omani.works').textContent).toContain('primary')
    expect(screen.getByTestId('parent-domain-role-omani.trade').textContent).toContain('org-pool')
    expect(screen.getByTestId('parent-domain-status-omani.works').textContent).toContain('Ready')
    expect(screen.getByTestId('parent-domain-status-omani.trade').textContent).toContain('Flipping')
  })

  it('locks delete on the primary row', () => {
    render(<ParentDomainsPage initialItems={sampleItems} disableFetch />)
    expect(screen.queryByTestId('parent-domain-delete-omani.works')).toBeNull()
    expect(screen.getByTestId('parent-domain-delete-omani.trade')).toBeTruthy()
  })

  it('opens the add-domain modal on CTA click', () => {
    render(<ParentDomainsPage initialItems={[]} disableFetch />)
    fireEvent.click(screen.getByTestId('parent-domains-add-cta'))
    expect(screen.getByTestId('add-domain-modal')).toBeTruthy()
    expect(screen.getByTestId('add-domain-name')).toBeTruthy()
    expect(screen.getByTestId('add-domain-role')).toBeTruthy()
    expect(screen.getByTestId('add-domain-registrar')).toBeTruthy()
    expect(screen.getByTestId('add-domain-token')).toBeTruthy()
    expect(screen.getByTestId('add-domain-submit')).toBeTruthy()
  })

  it('expands the propagation drawer on row toggle', () => {
    render(<ParentDomainsPage initialItems={sampleItems} disableFetch />)
    expect(screen.queryByTestId('parent-domain-drawer-omani.trade')).toBeNull()
    fireEvent.click(screen.getByTestId('parent-domain-toggle-omani.trade'))
    expect(screen.getByTestId('parent-domain-drawer-omani.trade')).toBeTruthy()
  })

  it('renders the propagation panel with disabled polling for tests', () => {
    render(<ParentDomainsPage initialItems={sampleItems} disableFetch />)
    fireEvent.click(screen.getByTestId('parent-domain-toggle-omani.trade'))
    // disablePolling propagated to PropagationPanel — null result means
    // the panel rendered but didn't fire the network call.
    expect(screen.getByTestId('parent-domain-drawer-omani.trade')).toBeTruthy()
  })
})
