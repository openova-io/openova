/**
 * PortalShell — pixel-port of core/console/src/components/PortalShell.svelte.
 *
 * Layout contract (matches canonical 1:1):
 *   • flex min-h-screen wrapper
 *   • left rail: <Sidebar /> w-56 fixed
 *   • main: ml-56 flex-1 with a 56px sticky header band hosting the
 *     NotificationBell + ThemeToggle + ProfileMenu (top-right) and the
 *     page title left-aligned at the start of the band.
 *
 * Per founder mandate #531 (2026-05-02), the page title is left-aligned
 * (sat in the LEFT slot), and a NotificationBell is rendered to the
 * LEFT of the ThemeToggle in the right slot. The previous 3-slot
 * centred-title layout (#366 item 2) has been retired — operators
 * universally read titles from the upper-left of the surface, and the
 * centred treatment was cited as one of the polish bugs to remove.
 *
 * Header layout (left → right):
 *   • title (h1, left)            — `pageTitle`
 *   • optional left slot          — `headerSlotLeft` (breadcrumb / sub-nav)
 *   • flex spacer
 *   • optional right slot         — `headerSlotRight` (FQDN switcher etc.)
 *   • <NotificationBell />        — global notifications affordance
 *   • <ThemeToggle />             — theme switcher
 *   • <ProfileMenu />             — signed-in user identity (#750)
 *
 * Issue #750 — identity placement: the provisioning surface previously
 * embedded a bespoke "Operator / Provisioning session" card in the
 * bottom-left of the Sidebar. That widget is gone; the canonical
 * <ProfileMenu /> (the same widget the wizard header uses) now lives
 * in the top-right of this shell so identity placement is consistent
 * across wizard + provisioning + Sovereign-console.
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
import { NotificationBell } from '@/shared/ui/notifications'
import { ProfileMenu } from '@/widgets/auth/ProfileMenu'
import { DETECTED_MODE } from '@/shared/lib/detectMode'

interface PortalShellProps {
  /** Stable deploymentId from the URL parameter. */
  deploymentId: string
  /** Resolved Sovereign FQDN (passed through to Sidebar's tenant slot). */
  sovereignFQDN?: string | null
  /** Page title shown left-aligned at the start of the header. */
  pageTitle?: string
  /** Optional left slot — breadcrumb / sub-nav / back link. Renders to
   *  the right of the title so a back-link reads as a sub-affordance
   *  of the page's identity. */
  headerSlotLeft?: ReactNode
  /** Optional right slot — FQDN switcher / page-specific affordances.
   *  Rendered LEFT of the NotificationBell + ThemeToggle. */
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
  // On Sovereign mode (chroot Sovereign Console at console.<sov-fqdn>),
  // SovereignConsoleLayout already mounts SovereignSidebar with the
  // chroot-correct clean-root URLs (/jobs, /apps, etc.). PortalShell is
  // shared with the wizard's mother-mode pages (/provision/$id/X) which
  // use the deploymentId-templated Sidebar below. Rendering both at once
  // produces TWO overlapping sidebars whose links resolve differently —
  // the inner Sidebar renders /provision//jobs (empty $deploymentId)
  // because the chroot route doesn't carry that param. Skip the inner
  // Sidebar in Sovereign mode; the outer SovereignSidebar covers it.
  // Caught on otech127 2026-05-05.
  const renderSidebar = DETECTED_MODE.mode !== 'sovereign'
  const mainOffset = renderSidebar ? 'ml-56' : ''
  return (
    <div
      className="flex min-h-screen bg-[var(--color-bg)] text-[var(--color-text)]"
      data-testid="sov-portal-shell"
    >
      {renderSidebar ? (
        <Sidebar deploymentId={deploymentId} sovereignFQDN={sovereignFQDN} />
      ) : null}
      <div className={`${mainOffset} flex flex-1 flex-col`}>
        {/* Sovereign portal top header band — title-left, controls-right. */}
        <header
          data-testid="portal-header"
          className="sticky top-0 z-40 flex h-14 items-center gap-3 border-b border-[var(--color-border)] bg-[var(--color-bg-2)]/90 px-4 backdrop-blur"
        >
          <div
            data-testid="portal-header-left"
            className="flex min-w-0 flex-1 items-center gap-3"
          >
            {pageTitle ? (
              <h1
                data-testid="portal-header-title"
                className="truncate text-base font-semibold text-[var(--color-text-strong)]"
              >
                {pageTitle}
              </h1>
            ) : null}
            {headerSlotLeft}
          </div>
          <div
            data-testid="portal-header-right"
            className="flex shrink-0 items-center gap-3"
          >
            {headerSlotRight}
            <NotificationBell />
            <ThemeToggle />
            {/* Issue #750 — signed-in user identity in the top-right
                slot (anonymous renders [Sign in], signed-in renders the
                email-initial avatar with a Sign-out dropdown). */}
            <ProfileMenu />
          </div>
        </header>
        <main className="flex-1 p-8">{children}</main>
      </div>
    </div>
  )
}
