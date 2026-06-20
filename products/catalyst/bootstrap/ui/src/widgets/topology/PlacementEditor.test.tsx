/**
 * PlacementEditor.test.tsx — #3969 the ONE placement editor.
 *
 * Locks: target rows render with role/standby-type controls; the derived
 * pattern updates live; a role-flip is the switchover (§6.3); the
 * multi-Primary gate disables the 2nd Primary radio for a primary+standby
 * blueprint (DoD 5); owned-deps follow toggles; [+ add target] adds a row.
 */
import { describe, it, expect, afterEach, vi } from 'vitest'
import { render, screen, cleanup, fireEvent } from '@testing-library/react'

import { PlacementEditor } from './PlacementEditor'
import type { PlacementTarget } from '@/shared/lib/placement'

afterEach(() => {
  cleanup()
  vi.clearAllMocks()
})

const hotStandby: PlacementTarget[] = [
  { region: 'region-a', cluster: 'mgmt-A', vcluster: 'mgmt', role: 'Primary' },
  { region: 'region-b', cluster: 'mgmt-B', vcluster: 'mgmt', role: 'Standby', standbyType: 'Hot' },
]

function renderEditor(overrides: Partial<React.ComponentProps<typeof PlacementEditor>> = {}) {
  return render(
    <PlacementEditor
      sovereignId="s"
      applicationName="keycloak"
      initialTargets={hotStandby}
      capability="primary+standby"
      availableRegions={['region-a', 'region-b', 'region-c']}
      availableClusters={['mgmt-A', 'mgmt-B', 'mgmt-C']}
      ownedDependencies={[{ name: 'keycloak-pg', follow: true }]}
      disableNetwork
      {...overrides}
    />,
  )
}

describe('PlacementEditor', () => {
  it('renders target rows + derived pattern + capability line', () => {
    renderEditor()
    expect(screen.getByTestId('placement-editor-target-0')).toBeTruthy()
    expect(screen.getByTestId('placement-editor-target-1')).toBeTruthy()
    expect(screen.getByTestId('placement-editor-derived-pattern').textContent).toContain('active-hot-standby')
    expect(screen.getByTestId('placement-editor-capability').textContent).toContain('primary + standby')
    // The owned dep follows by default.
    expect(screen.getByTestId('placement-editor-owned-keycloak-pg')).toBeTruthy()
  })

  it('switchover = flip the Primary role on the editor (§6.3)', () => {
    renderEditor()
    // Flip region-b to Primary; region-a auto-stays selectable as Primary but
    // we flip it to Standby first to satisfy the single-Primary gate.
    fireEvent.click(screen.getByTestId('placement-editor-target-0-role-standby'))
    fireEvent.click(screen.getByTestId('placement-editor-target-1-role-primary'))
    // Pattern stays active-hot-standby (region-a is now a Hot standby).
    expect(screen.getByTestId('placement-editor-derived-pattern').textContent).toContain('active-hot-standby')
    // region-b card now shows the Primary radio checked.
    const bPrimary = screen.getByTestId('placement-editor-target-1-role-primary') as HTMLInputElement
    expect(bPrimary.checked).toBe(true)
  })

  it('disables the 2nd Primary for a primary+standby blueprint (DoD 5)', () => {
    renderEditor()
    // region-b is Standby; its Primary radio must be disabled (one Primary
    // already exists on region-a).
    const bPrimary = screen.getByTestId('placement-editor-target-1-role-primary') as HTMLInputElement
    expect(bPrimary.disabled).toBe(true)
    expect(screen.getByTestId('placement-editor-target-1-multiprimary-reason')).toBeTruthy()
  })

  it('allows a 2nd Primary for a multi-primary blueprint', () => {
    renderEditor({ capability: 'multi-primary' })
    const bPrimary = screen.getByTestId('placement-editor-target-1-role-primary') as HTMLInputElement
    expect(bPrimary.disabled).toBe(false)
  })

  it('[+ add target] adds a Standby row (singleton -> multi-region, §6.4)', () => {
    renderEditor({
      initialTargets: [{ region: 'region-a', cluster: 'mgmt-A', vcluster: 'mgmt', role: 'Primary' }],
    })
    expect(screen.getByTestId('placement-editor-derived-pattern').textContent).toContain('singleton')
    fireEvent.click(screen.getByTestId('placement-editor-add-target'))
    expect(screen.getByTestId('placement-editor-target-1')).toBeTruthy()
    // A new Hot standby flips the derived pattern.
    expect(screen.getByTestId('placement-editor-derived-pattern').textContent).toContain('active-hot-standby')
  })

  it('owned-dep follow toggle decouples the dep', () => {
    renderEditor()
    const follow = screen.getByTestId('placement-editor-owned-keycloak-pg-follow') as HTMLInputElement
    expect(follow.checked).toBe(true)
    fireEvent.click(follow)
    expect(follow.checked).toBe(false)
  })

  it('embedded mode reports targets via onChange and hides the action bar', () => {
    const onChange = vi.fn()
    renderEditor({ embedded: true, onChange })
    expect(screen.queryByTestId('placement-editor-actions')).toBeNull()
    expect(onChange).toHaveBeenCalled()
  })
})
