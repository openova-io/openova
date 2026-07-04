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
 *   grey   (--color-text-dimmer) dormant — installed-but-never-fired
 *                              (the tethered cutover chart, #4731); a
 *                              distinct FAINT grey (see the treemap's
 *                              dashed dormant cell) so a parked tether is
 *                              never confused with pending queued work.
 *
 * In-progress MUST be visibly distinct (blue) — never rendered as grey
 * or green. Pending is grey — never amber (amber is reserved for
 * genuine warning states). Dormant is grey too but visually parked
 * (fainter fill + dashed outline on the treemap) — dormant ≠ pending,
 * the exact confusion the founder flagged on the converged treemap.
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
  // #4731 — a component that is INSTALLED but has never been fired: the
  // tethered bp-self-sovereign-cutover chart before the operator triggers
  // cutover. Grey like pending but PARKED, never "queued behind a running
  // chain". Rendered distinct on the treemap (fainter + dashed) so the 11
  // dormant cutover steps stop masquerading as pending.
  | 'dormant'

/** CSS colour token per semantic kind (for inline styles / CSS strings). */
export const STATUS_KIND_COLOR: Record<StatusKind, string> = {
  success: 'var(--color-success)',
  'in-progress': 'var(--color-accent)',
  warning: 'var(--color-warn)',
  failed: 'var(--color-danger)',
  pending: 'var(--color-text-dim)',
  // A DISTINCT, dimmer grey token (not pending's) so dormant is its own
  // colour — the treemap further sets it apart with a fainter fill + dashed
  // outline. dormant ≠ pending, by token and by treatment.
  dormant: 'var(--color-text-dimmer)',
}

/** Tailwind badge classes per kind — tinted background + solid text. */
export const STATUS_KIND_BADGE_CLASSES: Record<StatusKind, string> = {
  success: 'bg-[var(--color-success)]/15 text-[var(--color-success)]',
  'in-progress': 'bg-[var(--color-accent)]/15 text-[var(--color-accent)]',
  warning: 'bg-[var(--color-warn)]/15 text-[var(--color-warn)]',
  failed: 'bg-[var(--color-danger)]/15 text-[var(--color-danger)]',
  pending: 'bg-[var(--color-text-dim)]/15 text-[var(--color-text-dim)]',
  // Dashed border marks the parked/dormant tether apart from solid pending.
  dormant: 'border border-dashed border-[var(--color-text-dimmer)]/40 bg-[var(--color-text-dimmer)]/5 text-[var(--color-text-dimmer)]',
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
    // #4731 — installed-but-never-fired (the tethered cutover chart). Grey
    // like pending, but parked; the treemap collapses the dormant cutover
    // steps into one dormant leaf so they never pollute the pending bucket.
    case 'dormant':
      return 'dormant'
    default:
      return 'pending'
  }
}
