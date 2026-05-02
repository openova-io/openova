/**
 * NotificationsPage — dedicated full-page surface for the in-memory
 * notification list (#531 item 1).
 *
 * Rationale: the bell dropdown shows the same list compactly, but the
 * page-level surface gives the operator room to scroll long error
 * traces, review every active notification at once, and clear them in
 * bulk without juggling a popover.
 *
 * Layout contract — same chrome as Apps / Jobs / Cloud / Settings:
 *   • PortalShell (Sidebar + top header band, page title left-aligned)
 *   • Body: header row with bulk-clear CTA + the `<NotificationListPanel />`
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), the empty
 * state copy is centralised in the panel component so the bell and
 * the page can never drift apart.
 */

import { useParams } from '@tanstack/react-router'
import { PortalShell } from './PortalShell'
import { useDeploymentEvents } from './useDeploymentEvents'
import {
  NotificationListPanel,
  useNotifications,
} from '@/shared/ui/notifications'

export interface NotificationsPageProps {
  /** Test seam — disables the live SSE attach. */
  disableStream?: boolean
}

export function NotificationsPage({
  disableStream = false,
}: NotificationsPageProps = {}) {
  const params = useParams({
    from: '/provision/$deploymentId/notifications' as never,
  }) as { deploymentId: string }
  const deploymentId = params.deploymentId

  const { snapshot } = useDeploymentEvents({
    deploymentId,
    applicationIds: [],
    disableStream,
  })
  const sovereignFQDN = snapshot?.sovereignFQDN ?? snapshot?.result?.sovereignFQDN ?? null

  const { items, dismiss, dismissAll } = useNotifications()

  return (
    <PortalShell
      deploymentId={deploymentId}
      sovereignFQDN={sovereignFQDN}
      pageTitle="Notifications"
    >
      <div className="mx-auto max-w-3xl" data-testid="notifications-page">
        <header className="mb-4 flex items-center justify-between gap-3">
          <p className="text-sm text-[var(--color-text-dim)]">
            Provisioning failures and other status updates emitted by the
            Sovereign control plane.
          </p>
          {items.length > 0 ? (
            <button
              type="button"
              data-testid="notifications-page-clear-all"
              onClick={() => dismissAll()}
              className="rounded-md border border-[var(--color-border)] bg-transparent px-3 py-1 text-xs text-[var(--color-text-dim)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)]"
            >
              Clear all
            </button>
          ) : null}
        </header>
        <NotificationListPanel items={items} dismiss={dismiss} variant="page" />
      </div>
    </PortalShell>
  )
}
