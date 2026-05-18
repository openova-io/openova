/**
 * SandboxProvisioningPanel — real-time provisioning progress panel
 * shown on `/sandbox/$id` while the sandbox-controller materialises the
 * Sandbox CR. Replaces the prior "blank until xterm.js attaches"
 * behaviour that PR #1670 left in place (the WebSocket reconnect loop
 * fired ~6 retries against a non-existent ingress before giving up).
 *
 * Wire path:
 *
 *   browser ──GET /api/v1/sandbox/sessions/{id}──▶ catalyst-api
 *           (polled every 2s via TanStack Query)
 *
 * The handler projects `.status.{phase, conditions[]}` from the Sandbox
 * CR (PR #1673). The phase progression is:
 *
 *   Pending → Provisioning → Ready
 *                          ↘ Failed
 *
 * Each stage maps to a concrete controller side-effect; we mirror the
 * canonical six-stage shape from `core/controllers/sandbox/internal/
 * controller.go` so the operator sees the same waterfall the controller
 * walks:
 *
 *   1. Namespace      — sandbox-<owner-uid> ns + labels
 *   2. RBAC           — ServiceAccount + Role + RoleBinding
 *   3. PVCs           — repo + claude-memory + cache volumes
 *   4. pty Pod        — pty-server pod + Service
 *   5. MCP Pod        — sandbox-mcp-server pod + Service
 *   6. newapi Token   — minted via newapi.User then secret-bound
 *
 * Stage detection is by Condition Type:
 *
 *   - `NamespaceReady`           → stage 1 done
 *   - `RBACReady`                → stage 2 done
 *   - `PVCsReady`                → stage 3 done
 *   - `PtyReady`                 → stage 4 done
 *   - `McpReady`                 → stage 5 done
 *   - `TokenReady`               → stage 6 done
 *   - `Ready` (status=True)      → terminal success
 *
 * Failure detection is by:
 *
 *   - `phase === 'Failed'`       → render conditions[] + retry CTA
 *   - any condition with status='False' AND a reason → red-row inline
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (target-state) — every stage chip renders from first paint,
 *      flipping from "Waiting" to "In progress" to "Done" as the
 *      controller observes each side-effect. Operator never sees an
 *      empty surface.
 *   #4 (never hardcode) — colour tokens come from the design system
 *      (emerald/amber/rose ramps matching AppDetail's phase chip).
 *
 * Design-system inheritance (per founder ruling 2026-05-17
 * feedback_subagents_inherit_design_system.md): no bespoke layout, no
 * hex colours, no new card components. Card chrome mirrors
 * SandboxSession's `sandbox-session-card` shell verbatim (rounded-xl,
 * var(--color-bg-2) on var(--color-border), 5-unit padding). Badge
 * tones reuse the same emerald/amber/rose Tailwind classes the
 * ConnectionBadge + AppDetail phase chip already use.
 */

import type { Sandbox, SandboxCondition, SandboxPhase } from '@/lib/sandbox.api'

/**
 * Canonical provisioning stage shape. Each stage maps to one
 * controller-managed Condition Type; the FE infers stage status from
 * the matching condition (presence + `status` field).
 */
export interface ProvisioningStage {
  /** Stage id — also the condition type the controller emits. */
  id: string
  /** Operator-facing label. */
  label: string
  /** Optional sub-label hint. */
  hint?: string
}

export const PROVISIONING_STAGES: readonly ProvisioningStage[] = [
  {
    id: 'NamespaceReady',
    label: 'Namespace',
    hint: 'sandbox-<owner> namespace + labels',
  },
  {
    id: 'RBACReady',
    label: 'RBAC',
    hint: 'ServiceAccount + Role + RoleBinding',
  },
  {
    id: 'PVCsReady',
    label: 'Storage',
    hint: 'repo + claude-memory + cache PVCs',
  },
  {
    id: 'PtyReady',
    label: 'pty Pod',
    hint: 'pty-server Pod + Service',
  },
  {
    id: 'McpReady',
    label: 'MCP Pod',
    hint: 'sandbox-mcp-server Pod + Service',
  },
  {
    id: 'TokenReady',
    label: 'newapi Token',
    hint: 'minted + secret-bound',
  },
] as const

/** Per-stage status — drives the chip colour + icon. */
export type StageStatus = 'waiting' | 'in-progress' | 'done' | 'failed'

/**
 * deriveStageStatus — projects the controller's flat conditions[] list
 * onto the canonical 6-stage shape so the panel can render a stable
 * waterfall regardless of the order conditions arrive in.
 *
 * Rules:
 *   - condition.status='True'  + matching type → 'done'
 *   - condition.status='False' + matching type → 'failed'
 *   - no condition with this type, but a prior stage is 'done' or
 *     the overall phase is Provisioning → 'in-progress'
 *   - otherwise → 'waiting'
 */
export function deriveStageStatuses(
  phase: SandboxPhase,
  conditions: readonly SandboxCondition[],
  stages: readonly ProvisioningStage[] = PROVISIONING_STAGES,
): StageStatus[] {
  const byType = new Map<string, SandboxCondition>()
  for (const c of conditions) {
    if (c.type) byType.set(c.type, c)
  }
  // Walk the canonical stage list once. The first stage without a
  // matching True/False condition is the "active" one — render it as
  // in-progress (Provisioning) / failed (Failed) / waiting (Pending) /
  // done (Ready). Every stage AFTER the active one stays waiting until
  // the next condition arrives.
  const out: StageStatus[] = []
  let foundActive = false
  for (const stage of stages) {
    const cond = byType.get(stage.id)
    if (cond) {
      const s = cond.status.toLowerCase()
      if (s === 'true') {
        out.push('done')
        continue
      }
      if (s === 'false') {
        out.push('failed')
        // The first failed stage halts the waterfall — every stage
        // after it stays 'waiting'.
        foundActive = true
        continue
      }
    }
    // No condition for this stage yet — it's either the active one or
    // still queued.
    if (!foundActive) {
      if (phase === 'Provisioning') {
        out.push('in-progress')
      } else if (phase === 'Failed') {
        // Phase=Failed without a per-stage False — surface as failed
        // on the first un-done stage so the operator sees where the
        // controller stopped.
        out.push('failed')
      } else if (phase === 'Ready') {
        // Phase=Ready with no per-stage True condition is a contract
        // bug — treat as done so the panel doesn't deadlock the UI.
        out.push('done')
      } else {
        // Pending / empty — first stage is the one the controller
        // will pick up next, so render it as in-progress; later
        // stages stay waiting.
        out.push(out.length === 0 ? 'in-progress' : 'waiting')
      }
      foundActive = true
    } else {
      out.push('waiting')
    }
  }
  return out
}

/**
 * Returns true when the provisioning panel should hide itself and let
 * the parent surface render the terminal (xterm.js). The session is
 * Ready when phase is explicitly 'Ready' OR all six stages have a
 * matching True condition (defensive fallback for older controller
 * versions that may not write `.status.phase=Ready` yet).
 */
export function isSandboxReady(sandbox: Sandbox | null | undefined): boolean {
  if (!sandbox) return false
  if (sandbox.phase === 'Ready') return true
  const statuses = deriveStageStatuses(sandbox.phase, sandbox.conditions)
  return statuses.length > 0 && statuses.every((s) => s === 'done')
}

/** Returns true when the panel should show the failure detail + retry. */
export function isSandboxFailed(sandbox: Sandbox | null | undefined): boolean {
  if (!sandbox) return false
  if (sandbox.phase === 'Failed') return true
  return sandbox.conditions.some(
    (c) => c.status.toLowerCase() === 'false' && !!c.reason,
  )
}

export interface SandboxProvisioningPanelProps {
  /**
   * Last-known Sandbox row from the poll. `null` when the row has been
   * deleted (404) — the panel renders a "Session no longer exists"
   * state pointing the operator back to the landing page.
   */
  sandbox: Sandbox | null
  /**
   * Polling lifecycle — surfaces a small spinner next to the phase
   * badge so the operator can tell the data is live.
   */
  isPolling: boolean
  /**
   * Network-level error from the poll (not a controller failure). When
   * set, surfaces a small warning row above the stages without
   * disturbing the waterfall.
   */
  pollError?: string | null
  /** Operator action — clicked when phase===Failed. Optional; when
   *  omitted the retry button hides. */
  onRetry?: () => void
  /** Operator action — clicked from the "no longer exists" state. */
  onBackToLanding?: () => void
  /** Indicates a retry is in-flight (disables the retry button). */
  isRetrying?: boolean
}

export function SandboxProvisioningPanel({
  sandbox,
  isPolling,
  pollError,
  onRetry,
  onBackToLanding,
  isRetrying = false,
}: SandboxProvisioningPanelProps) {
  // Session gone (deleted in another tab or by an operator) — short-
  // circuit before computing stage statuses on a null row.
  if (!sandbox) {
    return (
      <section
        data-testid="sandbox-provisioning-panel"
        data-state="missing"
        className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
      >
        <header className="mb-4 flex items-start justify-between gap-3">
          <div>
            <h2 className="text-base font-semibold text-[var(--color-text-strong)]">
              Session no longer exists
            </h2>
            <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
              The Sandbox session has been deleted. Start a new session
              from the landing page.
            </p>
          </div>
          <PhaseBadge phase="" isPolling={isPolling} state="missing" />
        </header>
        {onBackToLanding ? (
          <button
            type="button"
            onClick={onBackToLanding}
            data-testid="sandbox-provisioning-back-to-landing"
            className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-1.5 text-xs text-[var(--color-text)] hover:border-[var(--color-accent)] hover:text-[var(--color-accent)]"
          >
            Back to sandbox
          </button>
        ) : null}
      </section>
    )
  }

  const phase = sandbox.phase
  const stageStatuses = deriveStageStatuses(phase, sandbox.conditions)
  const failed = isSandboxFailed(sandbox)
  // Surface only the False conditions that have a reason — the True
  // ones are the per-stage progress markers, not actionable errors.
  const failureConditions = sandbox.conditions.filter(
    (c) => c.status.toLowerCase() === 'false' && !!c.reason,
  )

  return (
    <section
      data-testid="sandbox-provisioning-panel"
      data-state={failed ? 'failed' : phase ? phase.toLowerCase() : 'pending'}
      data-phase={phase}
      className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
    >
      <header className="mb-4 flex items-start justify-between gap-3">
        <div>
          <h2 className="text-base font-semibold text-[var(--color-text-strong)]">
            Provisioning Sandbox
          </h2>
          <p className="mt-0.5 text-xs text-[var(--color-text-dim)]">
            The sandbox-controller is reconciling{' '}
            <span className="font-mono">{sandbox.id}</span>. The terminal
            attaches automatically once every stage is ready.
          </p>
        </div>
        <PhaseBadge
          phase={phase}
          isPolling={isPolling}
          state={failed ? 'failed' : undefined}
        />
      </header>

      {pollError ? (
        <div
          data-testid="sandbox-provisioning-poll-error"
          className="mb-3 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-300"
        >
          <span className="font-medium">Polling glitched</span>: {pollError}.
          The panel will retry on the next tick — no action required.
        </div>
      ) : null}

      <ol
        aria-label="Sandbox provisioning stages"
        data-testid="sandbox-provisioning-stages"
        className="flex flex-col gap-2"
      >
        {PROVISIONING_STAGES.map((stage, i) => (
          <StageRow
            key={stage.id}
            stage={stage}
            status={stageStatuses[i] ?? 'waiting'}
            condition={sandbox.conditions.find((c) => c.type === stage.id)}
          />
        ))}
      </ol>

      {failed && failureConditions.length > 0 ? (
        <div
          data-testid="sandbox-provisioning-failure"
          className="mt-4 rounded-md border border-rose-500/40 bg-rose-500/10 px-3 py-3 text-xs text-rose-200"
        >
          <p className="mb-2 font-semibold text-rose-300">
            Provisioning failed
          </p>
          <ul
            data-testid="sandbox-provisioning-failure-conditions"
            className="flex flex-col gap-1.5"
          >
            {failureConditions.map((c) => (
              <li
                key={`${c.type}-${c.reason ?? ''}`}
                data-testid={`sandbox-provisioning-failure-${c.type}`}
                className="flex flex-col gap-0.5"
              >
                <span className="font-mono text-[11px] uppercase tracking-wide text-rose-300">
                  {c.type}
                  {c.reason ? ` · ${c.reason}` : ''}
                </span>
                {c.message ? (
                  <span className="text-rose-100/90">{c.message}</span>
                ) : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {failed && onRetry ? (
        <div className="mt-4 flex items-center justify-end gap-2">
          <button
            type="button"
            onClick={onRetry}
            disabled={isRetrying}
            data-testid="sandbox-provisioning-retry"
            className="rounded-md border border-[var(--color-accent)] bg-[var(--color-accent)]/10 px-3 py-1.5 text-xs font-medium text-[var(--color-accent)] hover:bg-[var(--color-accent)]/20 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {isRetrying ? 'Retrying…' : 'Delete + retry'}
          </button>
        </div>
      ) : null}
    </section>
  )
}

/* ── Phase badge ─────────────────────────────────────────────────── */

interface PhaseBadgeProps {
  phase: SandboxPhase
  isPolling: boolean
  /** Optional override state (e.g. 'missing' for the deleted-row card). */
  state?: 'failed' | 'missing'
}

/**
 * PhaseBadge — pill mirroring the controller phase. Colour ramps stick
 * to the AppDetail phase chip palette (PR #1581 + earlier) so the
 * design system stays coherent across surfaces:
 *
 *   - Ready          → emerald (steady-state success)
 *   - Provisioning   → amber + spinner (transient)
 *   - Pending        → amber (queued, controller hasn't picked it up)
 *   - Failed         → rose (terminal failure)
 *   - missing        → neutral grey (session deleted)
 */
function PhaseBadge({ phase, isPolling, state }: PhaseBadgeProps) {
  let label: string
  let tone: string
  let showSpinner = false

  if (state === 'failed' || phase === 'Failed') {
    label = 'Failed'
    tone = 'border-rose-500/40 bg-rose-500/10 text-rose-300'
  } else if (state === 'missing') {
    label = 'Missing'
    tone =
      'border-[var(--color-border)] bg-transparent text-[var(--color-text-dim)]'
  } else if (phase === 'Ready') {
    label = 'Ready'
    tone = 'border-emerald-500/40 bg-emerald-500/10 text-emerald-300'
  } else if (phase === 'Provisioning') {
    label = 'Provisioning'
    tone = 'border-amber-500/40 bg-amber-500/10 text-amber-300'
    showSpinner = true
  } else if (phase === 'Pending' || phase === '') {
    label = 'Pending'
    tone = 'border-amber-500/40 bg-amber-500/10 text-amber-300'
    showSpinner = isPolling
  } else {
    label = phase
    tone =
      'border-[var(--color-border)] bg-transparent text-[var(--color-text-dim)]'
  }

  return (
    <span
      data-testid="sandbox-provisioning-phase-badge"
      data-phase={phase || 'unknown'}
      className={
        'inline-flex items-center gap-1.5 rounded-full border px-2 py-0.5 text-[10px] font-medium uppercase tracking-wide ' +
        tone
      }
    >
      {showSpinner ? <Spinner /> : null}
      {label}
    </span>
  )
}

/* ── Stage row ───────────────────────────────────────────────────── */

interface StageRowProps {
  stage: ProvisioningStage
  status: StageStatus
  condition?: SandboxCondition
}

/**
 * StageRow — one row in the six-step waterfall. The icon column flips
 * between a check (done), spinner (in-progress), error glyph (failed),
 * and a hollow circle (waiting). The text + hint row stays stable so
 * the operator can read it before, during, and after each transition.
 *
 * data-testid + data-status pair lets Playwright assert per-stage
 * progress: `[data-testid="sandbox-provisioning-stage-NamespaceReady"]
 *           [data-status="done"]`.
 */
function StageRow({ stage, status, condition }: StageRowProps) {
  let icon: React.ReactNode
  let labelTone: string

  switch (status) {
    case 'done':
      icon = <CheckIcon />
      labelTone = 'text-emerald-300'
      break
    case 'in-progress':
      icon = <Spinner />
      labelTone = 'text-amber-300'
      break
    case 'failed':
      icon = <FailIcon />
      labelTone = 'text-rose-300'
      break
    case 'waiting':
    default:
      icon = <HollowCircle />
      labelTone = 'text-[var(--color-text-dim)]'
      break
  }

  // Per-stage condition hint — surfaced as a small secondary line so
  // an operator who's debugging a specific stage can see the
  // reason/message without scrolling to the failure block.
  const hint = condition?.message || condition?.reason || stage.hint

  return (
    <li
      data-testid={`sandbox-provisioning-stage-${stage.id}`}
      data-status={status}
      className="flex items-start gap-3 rounded-md border border-[var(--color-border)] bg-[var(--color-bg)] px-3 py-2"
    >
      <span className="mt-0.5 inline-flex h-4 w-4 shrink-0 items-center justify-center">
        {icon}
      </span>
      <div className="min-w-0 flex-1">
        <p
          className={`text-sm font-medium ${labelTone}`}
          data-testid={`sandbox-provisioning-stage-${stage.id}-label`}
        >
          {stage.label}
        </p>
        {hint ? (
          <p
            className="truncate text-xs text-[var(--color-text-dim)]"
            data-testid={`sandbox-provisioning-stage-${stage.id}-hint`}
          >
            {hint}
          </p>
        ) : null}
      </div>
    </li>
  )
}

/* ── Icons — single-stroke SVGs matching the SovereignSidebar family ── */

function CheckIcon() {
  return (
    <svg
      className="h-4 w-4 text-emerald-300"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={2}
      aria-hidden
    >
      <path strokeLinecap="round" strokeLinejoin="round" d="M5 13l4 4L19 7" />
    </svg>
  )
}

function FailIcon() {
  return (
    <svg
      className="h-4 w-4 text-rose-300"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={2}
      aria-hidden
    >
      <path
        strokeLinecap="round"
        strokeLinejoin="round"
        d="M6 6l12 12M18 6L6 18"
      />
    </svg>
  )
}

function HollowCircle() {
  return (
    <svg
      className="h-3 w-3 text-[var(--color-text-dim)]"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth={2}
      aria-hidden
    >
      <circle cx="12" cy="12" r="9" />
    </svg>
  )
}

function Spinner() {
  // Same spin animation Token/AppDetail use; defined inline so the
  // panel works without depending on tailwind plugin keyframes.
  return (
    <span
      data-testid="sandbox-provisioning-spinner"
      className="inline-block h-3 w-3 rounded-full border-2 border-current border-t-transparent"
      style={{ animation: 'sov-sandbox-spin 0.7s linear infinite' }}
    />
  )
}

/* Keyframes — registered once via the component module so the spinner
 * works without touching global.css. Mirrors the inline `<style>`
 * pattern AppDetail uses (APP_DETAIL_CSS). */
if (
  typeof document !== 'undefined' &&
  !document.getElementById('sandbox-provisioning-keyframes')
) {
  const el = document.createElement('style')
  el.id = 'sandbox-provisioning-keyframes'
  el.textContent =
    '@keyframes sov-sandbox-spin { to { transform: rotate(360deg); } }'
  document.head.appendChild(el)
}
