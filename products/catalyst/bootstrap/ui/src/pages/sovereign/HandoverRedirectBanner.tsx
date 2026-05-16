/**
 * HandoverRedirectBanner — countdown + auto-redirect banner the
 * JobsPage renders when the deployment record signals the Sovereign
 * lifecycle has reached handover.
 *
 * Why this lives alongside the AppsPage version:
 *   AppsPage already drives a global toast + 5-second redirect via
 *   `useDeploymentEvents().handoverReady` (see AppsPage.tsx lines 284-
 *   361). But the operator routinely navigates to /jobs (especially
 *   while watching convergence) — without an in-page redirect there
 *   the operator gets stranded on the mothership Jobs view even
 *   though the Sovereign is ready.
 *
 *   Per founder feedback (Gap D, session_2026_05_16_t117_dod_partial.md):
 *
 *     "After handoverFiredAt, the operator should be auto-routed to
 *      https://console.<fqdn>/auth/handover?token=<jwt>. Currently the
 *      operator stays on the mothership Jobs page indefinitely."
 *
 *   This component renders the SAME affordance as the AppsPage banner
 *   (green hero panel + CTA link) but adds a visible 3-2-1 countdown
 *   + a "Stay on this page" Cancel button so the operator can pin the
 *   mothership view if they want to inspect state first.
 *
 * Why a separate countdown banner (vs. reusing AppsPage's 5-second
 * silent timer):
 *   The AppsPage redirect fires from a global notification toast that
 *   sits in the corner — visually adjacent to the apps grid. On the
 *   JobsPage the operator's attention is on the table; a silent
 *   redirect would feel abrupt. A visible countdown gives 3-2-1
 *   warning and matches the founder's brief:
 *
 *     "Show a 3-2-1 countdown banner before redirect so the operator
 *      can cancel if they want to inspect the mothership state first.
 *      Cancel button = stay on mothership."
 *
 * Idempotency:
 *   The redirect MUST fire at most once per page lifetime — even if
 *   the deployment record's handoverFiredAt arrives via multiple
 *   channels (SSE typed event, GET-replay, snapshot reload). A
 *   `redirectFiredRef` ref guards window.location.assign() so a
 *   re-render with the same handover state doesn't re-navigate.
 *
 * Test seam:
 *   `disableAutoRedirect` short-circuits the timer + window navigate
 *   so vitest can assert the banner DOM + cancel button without a
 *   real timer or jsdom-incompatible window.location mutation. Production
 *   call sites never set it.
 */

import { useEffect, useRef, useState } from 'react'

// CSS for this banner lives in `./HandoverRedirectBanner.css.ts` —
// non-component exports break Vite's HMR boundary
// (react-refresh/only-export-components), so this .tsx file only
// exports the React component + its props interface. The JobsPage
// imports the CSS directly from the `.css.ts` sibling.

/**
 * Visible countdown duration in seconds. 3-2-1 per the brief. The
 * tick interval is fixed at 1s.
 */
const COUNTDOWN_SECONDS = 3

export interface HandoverRedirectBannerProps {
  /**
   * Canonical handover URL — empty string disables the redirect even
   * if `active` is true (defensive guard against partial states).
   */
  handoverURL: string
  /**
   * True when the deployment record proves handover has fired AND the
   * caller wants the redirect to run (chroot Sovereign-side renders
   * suppress this — there's nowhere to redirect to).
   */
  active: boolean
  /**
   * Sovereign FQDN — displayed in the title for operator context.
   * Optional; falls back to "your new Sovereign".
   */
  sovereignFQDN?: string | null
  /**
   * Test seam — when true, the redirect timer + window.location.assign()
   * are not scheduled. The banner DOM still renders so visual + cancel-
   * button assertions work without faking timers / window.location.
   */
  disableAutoRedirect?: boolean
}

/**
 * Inner banner — receives `active === true` as an invariant from its
 * parent wrapper. The wrapper conditionally mounts/unmounts the inner
 * on activation transitions, which gives us a fresh component
 * lifecycle every time handover fires anew (the countdown + cancel
 * state initialise from scratch on mount; React handles the cleanup
 * on unmount). This avoids the react-hooks/set-state-in-effect
 * anti-pattern of resetting state imperatively when a prop flips.
 */
function HandoverRedirectBannerInner(props: HandoverRedirectBannerProps) {
  const { handoverURL, sovereignFQDN, disableAutoRedirect = false } = props

  // Operator cancel — drops the banner and the timer for the rest of
  // this mount's lifetime.
  const [cancelled, setCancelled] = useState(false)
  // Countdown tick — decrements each second.
  const [remaining, setRemaining] = useState<number>(COUNTDOWN_SECONDS)
  // Idempotency guard — once we've fired the redirect for THIS mount,
  // never fire again. Lives across re-renders.
  const redirectFiredRef = useRef(false)

  // Tick driver — 1s interval that decrements `remaining` while the
  // banner is uncancelled. When remaining hits 0 the redirect fires
  // once and the interval is cleared. Disabled entirely under the
  // test seam.
  useEffect(() => {
    if (cancelled || disableAutoRedirect) return
    if (handoverURL === '') return
    if (redirectFiredRef.current) return

    const id = window.setInterval(() => {
      setRemaining((prev) => {
        if (prev <= 1) {
          // Fire the redirect exactly once. Use window.location.assign
          // per the brief — it pushes to history (unlike replace) so
          // the operator's back button still works to return to the
          // mothership view. Guarded by the ref to survive a stray
          // double-tick under React Strict Mode.
          if (!redirectFiredRef.current) {
            redirectFiredRef.current = true
            window.location.assign(handoverURL)
          }
          return 0
        }
        return prev - 1
      })
    }, 1000)

    return () => {
      window.clearInterval(id)
    }
  }, [cancelled, disableAutoRedirect, handoverURL])

  if (cancelled || handoverURL === '') return null

  const targetLabel = sovereignFQDN ?? 'your new Sovereign'

  return (
    <div
      className="handover-redirect-banner"
      data-testid="sov-jobs-handover-redirect-banner"
      role="status"
    >
      <div className="handover-redirect-body">
        <span className="handover-redirect-title">
          ✓ Sovereign is ready{sovereignFQDN ? ` — ${sovereignFQDN}` : ''}
        </span>
        <span
          className="handover-redirect-sub"
          data-testid="sov-jobs-handover-redirect-sub"
        >
          Redirecting to {targetLabel} in{' '}
          <span
            className="handover-redirect-count"
            data-testid="sov-jobs-handover-redirect-countdown"
          >
            {remaining}
          </span>
          {' '}second{remaining === 1 ? '' : 's'}.
        </span>
      </div>
      <div className="handover-redirect-actions">
        <a
          className="handover-redirect-cta"
          href={handoverURL}
          data-testid="sov-jobs-handover-redirect-cta"
        >
          Open your Sovereign console →
        </a>
        <button
          type="button"
          className="handover-redirect-cancel"
          data-testid="sov-jobs-handover-redirect-cancel"
          onClick={() => setCancelled(true)}
        >
          Stay on mothership
        </button>
      </div>
    </div>
  )
}

/**
 * Public wrapper — gates on `active` so the inner banner's state
 * (countdown + cancel + redirect-fired ref) gets a fresh React
 * lifecycle every time the deployment record transitions to
 * handover-fired. The conditional render here is the only React-
 * idiomatic way to "reset" a child's useState without falling back
 * to setState-in-effect (react-hooks/set-state-in-effect).
 */
export function HandoverRedirectBanner(props: HandoverRedirectBannerProps) {
  if (!props.active) return null
  return <HandoverRedirectBannerInner {...props} />
}
