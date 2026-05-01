/**
 * PortalShell — pixel-port of core/console/src/components/PortalShell.svelte.
 *
 * Layout contract (matches canonical 1:1):
 *   • flex min-h-screen wrapper
 *   • left rail: <Sidebar /> w-56 fixed
 *   • main: ml-56 flex-1 with a 56px sticky header band hosting the
 *     ThemeToggle (top-right) and a 32px main content area.
 *
 * Per issue #366 item 2, the header band is a 3-slot flex grid:
 *   • Left  — optional `headerSlotLeft` (breadcrumb / sub-nav / back-link).
 *   • Centre — `pageTitle` rendered as <h1> at `[data-testid=portal-header-title]`.
 *   • Right — optional `headerSlotRight` (FQDN switcher etc.) +
 *             ThemeToggle.
 *
 * Each slot occupies `flex: 1` so the title is visually centred in
 * the page header, regardless of whether the side slots have content.
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
import { ThemeToggle } from '@/components/ThemeToggle'

interface PortalShellProps {
  /** Stable deploymentId from the URL parameter. */
  deploymentId: string
  /** Resolved Sovereign FQDN (passed through to Sidebar's tenant slot). */
  sovereignFQDN?: string | null
  /** Page title shown in the header centre slot (issue #366 item 2). */
  pageTitle?: string
  /** Optional left slot — breadcrumb / sub-nav / back link. */
  headerSlotLeft?: ReactNode
  /** Optional right slot — FQDN switcher / page-specific affordances.
   *  Rendered LEFT of the ThemeToggle. */
  headerSlotRight?: ReactNode
  children: ReactNode
}

export function PortalShell({
  deploymentId,
  sovereignFQDN,
  pageTitle,
  headerSlotLeft,
  headerSlotRight,
  children,
}: PortalShellProps) {
  return (
    <div
      className="flex min-h-screen bg-[var(--color-bg)] text-[var(--color-text)]"
      data-testid="sov-portal-shell"
    >
      <Sidebar deploymentId={deploymentId} sovereignFQDN={sovereignFQDN} />
      <div className="ml-56 flex flex-1 flex-col">
        {/* Sovereign portal top header band — 3 equal slots so the page
            title sits centred regardless of side-slot content (#366). */}
        <header
          data-testid="portal-header"
          className="sticky top-0 z-40 flex h-14 items-center gap-3 border-b border-[var(--color-border)] bg-[var(--color-bg-2)]/90 px-4 backdrop-blur"
        >
          <div
            data-testid="portal-header-left"
            className="flex flex-1 min-w-0 items-center justify-start gap-2"
          >
            {headerSlotLeft}
          </div>
          <div
            data-testid="portal-header-center"
            className="flex flex-1 min-w-0 items-center justify-center"
          >
            {pageTitle ? (
              <h1
                data-testid="portal-header-title"
                className="truncate text-base font-semibold text-[var(--color-text-strong)]"
              >
                {pageTitle}
              </h1>
            ) : null}
          </div>
          <div
            data-testid="portal-header-right"
            className="flex flex-1 min-w-0 items-center justify-end gap-3"
          >
            {headerSlotRight}
            <ThemeToggle />
          </div>
        </header>
        <main className="flex-1 p-8">{children}</main>
      </div>
    </div>
  )
}
