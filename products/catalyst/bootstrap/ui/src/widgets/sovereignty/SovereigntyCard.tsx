/**
 * SovereigntyCard — admin-console landing surface for the cutover
 * (issues #790 / #793).
 *
 * Three rendering modes, driven by status.state + cutover progress:
 *
 *   • `tethered`, no cutover started
 *     ──> "Soft-tethered to mothership" badge
 *         + "Achieve True Sovereignty" primary CTA
 *
 *   • `tethered`, cutover in flight (some step running)
 *     ──> Progress card (always 8 rows visible)
 *         + button hidden / disabled
 *
 *   • `sovereign`
 *     ──> "Independent" green badge
 *         + summary stats (mirroredCommitSHA, harborProjectCount,
 *           egressTestPassed) inside the progress card's terminal frame
 *
 * The CTA opens an explanation modal first — the cutover is irreversible
 * and includes an egress-block self-test that briefly drops outbound
 * connectivity to mothership-controlled hosts. The modal calls
 * `startCutover()` on confirm; on success the progress card mounts.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall): badge + button + modal + progress card all ship in
 *      this first cut. No "for now we'll just show the badge" path.
 *   #4 (never hardcode): URLs come from useCutoverEvents (which uses
 *      API_BASE); no inline `/api/...` literals in this component.
 *
 * The component owns NO fetch logic — everything routes through
 * useCutoverEvents (or a parent-supplied override for tests).
 */

import { useState } from 'react'
import { Crown, ShieldCheck, ShieldOff } from 'lucide-react'
import { Button } from '@/shared/ui/button'
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/shared/ui/dialog'
import { CUTOVER_STEPS } from '@/shared/types/cutover'
import { CutoverProgressCard } from './CutoverProgressCard'
import {
  useCutoverEvents,
  type UseCutoverEventsResult,
} from './useCutoverEvents'

export interface SovereigntyCardProps {
  /**
   * Test seam — when set, this object replaces the hook output in the
   * card. Used by SovereigntyCard.test.tsx to render every state shape
   * deterministically without a real EventSource.
   */
  override?: UseCutoverEventsResult
}

/**
 * Heuristic: a cutover is "in flight" if at least one step has moved
 * off `pending` AND the overall state is still `tethered`. Once the
 * state flips to `sovereign`, we treat it as terminal.
 */
function isInFlight(res: UseCutoverEventsResult): boolean {
  if (res.status.state === 'sovereign') return false
  return res.status.steps.some(
    (s) => s.status === 'running' || s.status === 'done' || s.status === 'failed',
  )
}

export function SovereigntyCard({ override }: SovereigntyCardProps) {
  // Always call the hook so the rules-of-hooks lint passes; test seam
  // simply ignores the result when `override` is provided.
  const live = useCutoverEvents({ disableStream: !!override })
  const res = override ?? live
  const { status, streamError, startCutover, starting, startError } = res

  const [modalOpen, setModalOpen] = useState(false)
  const inFlight = isInFlight(res)
  const cutoverComplete = status.state === 'sovereign'

  async function handleConfirm() {
    try {
      await startCutover()
      setModalOpen(false)
    } catch {
      // startError is already set by the hook; modal stays open so the
      // operator can read the message + retry.
    }
  }

  return (
    <section
      data-testid="sovereignty-card"
      data-cutover-state={status.state}
      aria-label="Sovereignty status"
      className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
    >
      <header className="flex flex-wrap items-start justify-between gap-4">
        <div className="flex items-start gap-3">
          <span
            aria-hidden
            className={`flex h-10 w-10 shrink-0 items-center justify-center rounded-lg ${
              cutoverComplete
                ? 'bg-green-500/15 text-green-400'
                : 'bg-amber-500/15 text-amber-400'
            }`}
          >
            {cutoverComplete ? (
              <ShieldCheck className="h-5 w-5" />
            ) : (
              <ShieldOff className="h-5 w-5" />
            )}
          </span>
          <div>
            <h2 className="text-base font-semibold text-[var(--color-text-strong)]">
              Cluster sovereignty
            </h2>
            <p className="mt-0.5 max-w-prose text-xs text-[var(--color-text-dim)]">
              {cutoverComplete
                ? 'This Sovereign is operationally independent of the openova-io mothership. Container pulls, GitOps source, and Helm catalogs all resolve locally.'
                : 'This Sovereign is currently soft-tethered to the openova-io mothership for container pulls and GitOps source. Achieving true sovereignty repoints registries, mirrors the catalog into local Gitea, and runs an egress-block self-test.'}
            </p>
          </div>
        </div>
        <span
          data-testid="sovereignty-badge"
          className={`inline-flex items-center gap-1.5 rounded-full px-3 py-1 text-[11px] font-semibold uppercase tracking-wider ${
            cutoverComplete
              ? 'border border-green-500/40 bg-green-500/10 text-green-300'
              : 'border border-amber-500/40 bg-amber-500/10 text-amber-300'
          }`}
        >
          {cutoverComplete ? (
            <>
              <Crown className="h-3 w-3" />
              <span>Sovereign</span>
            </>
          ) : (
            <>
              <ShieldOff className="h-3 w-3" />
              <span>Tethered</span>
            </>
          )}
        </span>
      </header>

      {/* Achievement summary — ALWAYS-rendered in `sovereign` mode so
          the operator sees the proof inline on the dashboard, not just
          inside the progress card's terminal frame. */}
      {cutoverComplete ? (
        <dl
          data-testid="sovereignty-stats"
          className="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-3"
        >
          <SovereigntyStat
            label="Mirrored commit"
            value={status.mirroredCommitSHA?.slice(0, 12) ?? '—'}
            testId="sovereignty-stat-sha"
          />
          <SovereigntyStat
            label="Harbor projects"
            value={status.harborProjectCount?.toString() ?? '—'}
            testId="sovereignty-stat-harbor"
          />
          <SovereigntyStat
            label="Egress test"
            value={
              status.egressTestPassed === true
                ? 'passed'
                : status.egressTestPassed === false
                  ? 'failed'
                  : '—'
            }
            testId="sovereignty-stat-egress"
          />
        </dl>
      ) : null}

      {/* CTA — visible only in pure `tethered` (no in-flight cutover). */}
      {!cutoverComplete && !inFlight ? (
        <div className="mt-5 flex flex-wrap items-center gap-3">
          <Button
            data-testid="cutover-start-button"
            variant="primary"
            size="md"
            onClick={() => setModalOpen(true)}
            loading={starting}
          >
            <Crown className="h-4 w-4" />
            Achieve True Sovereignty
          </Button>
          <p className="max-w-prose text-[11px] text-[var(--color-text-dim)]">
            Runs the {CUTOVER_STEPS.length}-step cutover. ~10 minutes.
            Includes a 10-minute egress-block self-test against github.com
            + ghcr.io + harbor.openova.io.
          </p>
        </div>
      ) : null}

      {/* Stream-level error — only when no in-flight progress card is
          rendered (otherwise the progress card surfaces the error). */}
      {!inFlight && !cutoverComplete && streamError ? (
        <p
          data-testid="sovereignty-stream-error"
          role="alert"
          className="mt-3 rounded-md border border-amber-500/40 bg-amber-500/5 p-2 text-xs text-amber-300"
        >
          {streamError}
        </p>
      ) : null}

      {/* In-flight or terminal — render the full 8-step progress card. */}
      {(inFlight || cutoverComplete) ? (
        <div className="mt-5">
          <CutoverProgressCard status={status} streamError={streamError} />
        </div>
      ) : null}

      <ConfirmCutoverModal
        open={modalOpen}
        onOpenChange={setModalOpen}
        onConfirm={handleConfirm}
        starting={starting}
        startError={startError}
      />
    </section>
  )
}

function SovereigntyStat({
  label,
  value,
  testId,
}: {
  label: string
  value: string
  testId: string
}) {
  return (
    <div
      data-testid={testId}
      className="rounded-lg border border-[var(--color-border)]/50 bg-[var(--color-bg)] p-3"
    >
      <dt className="text-[10px] uppercase tracking-wider text-[var(--color-text-dim)]">
        {label}
      </dt>
      <dd className="mt-1 truncate font-mono text-sm text-[var(--color-text-strong)]">
        {value}
      </dd>
    </div>
  )
}

function ConfirmCutoverModal({
  open,
  onOpenChange,
  onConfirm,
  starting,
  startError,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  onConfirm: () => Promise<void>
  starting: boolean
  startError: string | null
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        data-testid="cutover-confirm-modal"
        className="max-w-2xl"
      >
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2 text-lg font-semibold text-[var(--color-text-strong)]">
            <Crown className="h-5 w-5 text-amber-400" />
            Achieve True Sovereignty
          </DialogTitle>
          <DialogDescription className="text-xs text-[var(--color-text-dim)]">
            This runs a {CUTOVER_STEPS.length}-step cutover that makes this
            Sovereign cluster operationally independent of the openova-io
            mothership. The process takes about 10 minutes and includes a
            final 10-minute egress-block self-test.
          </DialogDescription>
        </DialogHeader>

        {/* Step overview — rendered from the canonical CUTOVER_STEPS list so
            it can never drift from the engine's real chain (the per-step
            LIVE status renders in CutoverProgressCard once the cutover runs). */}
        <ol className="mt-4 list-decimal space-y-1 pl-5 text-[12px] text-[var(--color-text)]">
          {CUTOVER_STEPS.map((s) => (
            <li key={s.id}>{s.label}</li>
          ))}
        </ol>

        <p className="mt-3 rounded-md border border-amber-500/40 bg-amber-500/5 p-2 text-[11px] text-amber-300">
          During the egress-block test, outbound traffic to github.com,
          ghcr.io, and harbor.openova.io will be denied. All Flux
          reconciles MUST stay green; if any fail, the cutover halts and
          surfaces the error.
        </p>

        {startError ? (
          <p
            data-testid="cutover-confirm-error"
            role="alert"
            className="mt-3 rounded-md border border-red-500/40 bg-red-500/5 p-2 text-xs text-red-300"
          >
            {startError}
          </p>
        ) : null}

        <div className="mt-5 flex justify-end gap-2">
          <DialogClose asChild>
            <Button
              data-testid="cutover-confirm-cancel"
              variant="ghost"
              size="md"
              type="button"
            >
              Cancel
            </Button>
          </DialogClose>
          <Button
            data-testid="cutover-confirm-button"
            variant="primary"
            size="md"
            type="button"
            onClick={() => {
              void onConfirm()
            }}
            loading={starting}
          >
            <Crown className="h-4 w-4" />
            Start cutover
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  )
}
