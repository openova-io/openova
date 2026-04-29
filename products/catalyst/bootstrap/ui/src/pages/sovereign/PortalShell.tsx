/**
 * PortalShell — pixel-port of core/console/src/components/PortalShell.svelte.
 *
 * Layout contract (matches canonical 1:1):
 *   • flex min-h-screen wrapper
 *   • left rail: <Sidebar /> w-56 fixed
 *   • main: ml-56 flex-1 p-8
 *
 * The canonical shell handles auth + tenant resolution; in the
 * Sovereign-provision wizard context that's not relevant — the wizard
 * runs unauthenticated and the deploymentId IS the tenant. The shell
 * therefore only needs the deployment id + an optional resolved
 * sovereign FQDN to mirror the same chrome.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every layout
 * value is a Tailwind utility (so it follows core/console's CSS), not
 * an inlined px / hex.
 */

import type { ReactNode } from 'react'
import { Sidebar } from './Sidebar'

interface PortalShellProps {
  /** Stable deploymentId from the URL parameter. */
  deploymentId: string
  /** Resolved Sovereign FQDN (passed through to Sidebar's tenant slot). */
  sovereignFQDN?: string | null
  children: ReactNode
}

export function PortalShell({ deploymentId, sovereignFQDN, children }: PortalShellProps) {
  return (
    <div
      className="flex min-h-screen bg-[var(--color-bg)] text-[var(--color-text)]"
      data-testid="sov-portal-shell"
    >
      <Sidebar deploymentId={deploymentId} sovereignFQDN={sovereignFQDN} />
      <main className="ml-56 flex-1 p-8">{children}</main>
    </div>
  )
}
