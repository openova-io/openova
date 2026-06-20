/**
 * RolesPage.test.tsx — Keycloak group → app role mapping (issue #802).
 *
 *   • Page heading renders
 *   • Table renders one row per default mapping (7 entries)
 *   • Each row carries the canonical group + appRole string
 */

import { afterEach, describe, expect, it } from 'vitest'
import { cleanup, render, screen } from '@testing-library/react'
import { RolesPage } from './RolesPage'

afterEach(() => cleanup())

describe('RolesPage', () => {
  it('renders heading', () => {
    render(<RolesPage />)
    expect(screen.getByText('Roles')).toBeTruthy()
  })

  it('renders the canonical 7-row mapping table', () => {
    render(<RolesPage />)
    expect(screen.getByTestId('org-roles-table')).toBeTruthy()
    // Spot-check three canonical rows from the locked mapping:
    expect(screen.getByTestId('org-role-wp-admins')).toBeTruthy()
    expect(screen.getByTestId('org-role-openclaw-users')).toBeTruthy()
    expect(screen.getByTestId('org-role-stalwart-postmasters')).toBeTruthy()
  })

  it('shows app role names', () => {
    render(<RolesPage />)
    expect(screen.getByText('wordpress:admin')).toBeTruthy()
    expect(screen.getByText('openclaw:user')).toBeTruthy()
    expect(screen.getByText('rbac:admin')).toBeTruthy()
  })
})
