/**
 * statusColors — ONE semantic status → colour mapping for every console
 * surface (#4704 follow-up, founder spec 2026-07-03).
 *
 * The contract, identical on the Dashboard progress pane, the treemap
 * and the Jobs table:
 *
 *   green  (--color-success)   success / converged / completed / ready
 *   blue   (--color-accent)    in-progress / reconciling / installing
 *   amber  (--color-warn)      warning / degraded / partial
 *   red    (--color-danger)    failed / error
 *   grey   (--color-text-dim)  pending / not-started / suspended
 *
 * In-progress MUST be visibly distinct (blue) — never rendered as grey
 * or green. Pending is grey — never amber (amber is reserved for
 * genuine warning states).
 *
 * All values are the theme CSS custom properties from globals.css (both
 * dark + light themes define them), never raw hex — per
 * docs/PRINCIPLES.md #4. The treemap's continuous health gradient
 * (lib/treemap.types.ts healthColor) interpolates between the SAME
 * anchor colours (#ef4444 → #f59e0b → #10b981), so percentage surfaces
 * stay consistent with these discrete kinds.
 */

export type StatusKind =
  | 'success'
  | 'in-progress'
  | 'warning'
  | 'failed'
  | 'pending'

/** CSS colour token per semantic kind (for inline styles / CSS strings). */
export const STATUS_KIND_COLOR: Record<StatusKind, string> = {
  success: 'var(--color-success)',
  'in-progress': 'var(--color-accent)',
  warning: 'var(--color-warn)',
  failed: 'var(--color-danger)',
  pending: 'var(--color-text-dim)',
}

/** Tailwind badge classes per kind — tinted background + solid text. */
export const STATUS_KIND_BADGE_CLASSES: Record<StatusKind, string> = {
  success: 'bg-[var(--color-success)]/15 text-[var(--color-success)]',
  'in-progress': 'bg-[var(--color-accent)]/15 text-[var(--color-accent)]',
  warning: 'bg-[var(--color-warn)]/15 text-[var(--color-warn)]',
  failed: 'bg-[var(--color-danger)]/15 text-[var(--color-danger)]',
  pending: 'bg-[var(--color-text-dim)]/15 text-[var(--color-text-dim)]',
}

/**
 * Classify a raw backend status string into a semantic kind. Unknown /
 * empty statuses classify as `pending` (grey) — never green, so a
 * missing status can't masquerade as success.
 */
export function statusKindOf(raw: string | null | undefined): StatusKind {
  const s = (raw ?? '').trim().toLowerCase()
  switch (s) {
    case 'succeeded':
    case 'success':
    case 'completed':
    case 'complete':
    case 'ready':
    case 'installed':
    case 'converged':
    case 'healthy':
    case 'done':
      return 'success'
    case 'running':
    case 'in-progress':
    case 'inprogress':
    case 'installing':
    case 'reconciling':
    case 'progressing':
    case 'provisioning':
    case 'deploying':
    case 'upgrading':
    case 'starting':
    case 'tofu-applying':
    case 'flux-bootstrapping':
    case 'phase1-watching':
      return 'in-progress'
    case 'warning':
    case 'degraded':
    case 'partial':
    case 'partial-failure':
    case 'stalled':
    case 'out-of-sync':
      return 'warning'
    // #4731 — `failing` is the Jobs HEALTH axis (issue #3646 §4c) word
    // for a broken recurring/reconciler row; same semantic red as a
    // one-shot `failed`.
    case 'failing':
    case 'failed':
    case 'failure':
    case 'error':
    case 'errored':
      return 'failed'
    default:
      return 'pending'
  }
}
