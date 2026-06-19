/**
 * ReadinessChip — the Convergence-Monitor top-bar readiness chip (#3925
 * surface D). Rendered in the PortalShell header on EVERY operator page.
 *
 * A single status pill that answers the operator's "is my Sovereign
 * ready, converging, degraded, or running an operation?" question at a
 * glance, backed by the deployment-status API:
 *
 *   READY                 ⇔ status=="ready" AND no in-flight operation
 *   CONVERGING            ⇔ status in the in-flight provision enum
 *   DEGRADED              ⇔ secondaryDegraded==true on a ready env
 *   OPERATION-IN-PROGRESS ⇔ operationInProgress==true (a finite cutover /
 *                           DR-switchover Job group is non-terminal —
 *                           distinct from initial provisioning)
 *
 * Precedence (ticket §2 surface-D + §5): an in-flight OPERATION wins over
 * READY/DEGRADED because a stuck cutover must never read as "still
 * provisioning" nor as a plain "ready". CONVERGING (initial provision)
 * wins over everything because the env isn't usable yet.
 *
 * Click → Dashboard (the wizard/treemap surface). When an operation is in
 * progress the chip also renders a thin banner under the header with a
 * "view progress" link to the Dashboard, so the operator is unmistakably
 * told an operation is running (DECISION: banner + toggle, never an
 * auto-flip-back — founder-agreed).
 *
 * Self-contained data: the chip polls GET /api/v1/deployments/{id} on a
 * short interval (independent of the SSE machinery in useDeploymentEvents,
 * which closes once status flips ready) so it stays fresh THROUGH a
 * post-ready cutover. Per docs/INVIOLABLE-PRINCIPLES.md #4 the poll URL is
 * built from API_BASE, never inlined.
 */

import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { API_BASE } from '@/shared/config/urls'

/** The four chip states, in render-precedence order (see deriveChipState). */
export type ChipState = 'converging' | 'operation' | 'degraded' | 'ready'

/** In-flight provision statuses — mirror of useDeploymentEvents.ts
 *  IN_FLIGHT_STATUSES + api/internal/handler/deployments.go isInFlightStatus(). */
const IN_FLIGHT_STATUSES: ReadonlySet<string> = new Set([
  'pending',
  'provisioning',
  'tofu-applying',
  'flux-bootstrapping',
  'phase1-watching',
])

/** Minimal projection of GET /deployments/{id} the chip consumes. */
export interface ReadinessSnapshot {
  status?: string
  secondaryDegraded?: boolean
  operationInProgress?: boolean
}

/**
 * deriveChipState resolves the single chip state from the deployment
 * status projection. Precedence (highest first):
 *   1. CONVERGING  — the initial provision is still running; the env is
 *      not usable yet, so this dominates everything.
 *   2. OPERATION   — a finite cutover/DR operation is in flight on an
 *      already-provisioned env; must read distinctly from both READY and
 *      "still provisioning".
 *   3. DEGRADED    — a secondary region lags on a ready env.
 *   4. READY       — converged, idle, no operation.
 */
export function deriveChipState(snap: ReadinessSnapshot | null | undefined): ChipState {
  const status = (snap?.status ?? '').trim().toLowerCase()
  if (IN_FLIGHT_STATUSES.has(status)) return 'converging'
  if (snap?.operationInProgress === true) return 'operation'
  if (snap?.secondaryDegraded === true) return 'degraded'
  if (status === 'ready') return 'ready'
  // Unknown / failed / partial — treat a non-ready, non-in-flight status
  // as converging so the chip never falsely claims READY. (A terminal
  // `failed` env still wants attention, not a green chip.)
  if (status === 'failed' || status === 'partial-failure') return 'degraded'
  return 'converging'
}

interface ChipTone {
  label: string
  dot: string
  bg: string
  border: string
  fg: string
  pulse: boolean
}

const CHIP_TONE: Record<ChipState, ChipTone> = {
  ready: {
    label: 'Ready',
    dot: '#4ADE80',
    bg: 'rgba(74,222,128,0.10)',
    border: 'rgba(74,222,128,0.35)',
    fg: '#4ADE80',
    pulse: false,
  },
  converging: {
    label: 'Converging',
    dot: '#38BDF8',
    bg: 'rgba(56,189,248,0.10)',
    border: 'rgba(56,189,248,0.35)',
    fg: '#38BDF8',
    pulse: true,
  },
  degraded: {
    label: 'Degraded',
    dot: '#FBBF24',
    bg: 'rgba(251,191,36,0.10)',
    border: 'rgba(251,191,36,0.35)',
    fg: '#FBBF24',
    pulse: false,
  },
  operation: {
    label: 'Operation in progress',
    dot: '#818CF8',
    bg: 'rgba(129,140,248,0.10)',
    border: 'rgba(129,140,248,0.35)',
    fg: '#818CF8',
    pulse: true,
  },
}

/** Poll cadence for the chip's own status query (ms). Short enough that a
 *  cutover start/finish is reflected promptly, long enough not to hammer
 *  the API. */
const CHIP_POLL_MS = 5_000

export interface ReadinessChipProps {
  /** Resolved deployment id — drives both the poll and the Dashboard link. */
  deploymentId: string
  /** Test seam — synthetic snapshot bypassing the live poll. */
  snapshotOverride?: ReadinessSnapshot | null
  /** Test seam — disable the network poll (jsdom). */
  disablePoll?: boolean
}

/**
 * useReadinessSnapshot polls GET /deployments/{id} and returns the
 * status projection the chip reads. Kept tiny + independent of
 * useDeploymentEvents so the chip stays live after the SSE stream closes.
 */
function useReadinessSnapshot(
  deploymentId: string,
  enabled: boolean,
): ReadinessSnapshot | null {
  const q = useQuery<ReadinessSnapshot | null>({
    queryKey: ['readiness-chip', deploymentId],
    queryFn: async () => {
      const res = await fetch(
        `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}`,
        { headers: { Accept: 'application/json' }, credentials: 'include' },
      )
      if (!res.ok) return null
      return (await res.json()) as ReadinessSnapshot
    },
    enabled: enabled && !!deploymentId,
    refetchInterval: CHIP_POLL_MS,
    staleTime: CHIP_POLL_MS,
    placeholderData: (prev) => prev,
  })
  return q.data ?? null
}

export function ReadinessChip({
  deploymentId,
  snapshotOverride,
  disablePoll = false,
}: ReadinessChipProps) {
  const polled = useReadinessSnapshot(deploymentId, !disablePoll && snapshotOverride === undefined)
  const snapshot = snapshotOverride !== undefined ? snapshotOverride : polled
  const state = deriveChipState(snapshot)
  const tone = CHIP_TONE[state]

  // The Dashboard link target differs by surface: on the mothership the
  // wizard tracks /provision/$id/dashboard; on the chroot Sovereign
  // Console the clean /dashboard route renders the same component. We
  // always link to `/dashboard` — TanStack resolves it against the active
  // basepath, and the chroot redirect map handles the prefix — mirroring
  // StatusStrip's breadcrumb which links to `/dashboard` too.
  return (
    <Link
      to={'/dashboard' as never}
      data-testid="readiness-chip"
      data-state={state}
      className="inline-flex items-center gap-2 rounded-full border px-3 py-1 text-xs font-bold uppercase tracking-wide no-underline transition-colors"
      style={{ background: tone.bg, borderColor: tone.border, color: tone.fg }}
      title={`Sovereign status: ${tone.label}`}
      aria-label={`Sovereign status: ${tone.label}. Open the Dashboard.`}
    >
      <span
        data-testid="readiness-chip-dot"
        className={tone.pulse ? 'readiness-chip-dot-pulse' : undefined}
        style={{
          width: 7,
          height: 7,
          borderRadius: '50%',
          flexShrink: 0,
          background: tone.dot,
        }}
        aria-hidden
      />
      <span data-testid="readiness-chip-label">{tone.label}</span>
      <style>{READINESS_CHIP_CSS}</style>
    </Link>
  )
}

/**
 * OperationBanner — the thin under-header banner shown ONLY while an
 * operation is in progress (#3925 surface D, DECISION: banner + toggle,
 * not auto-flip-back). Carries a "view progress" link to the Dashboard.
 * Renders null in every other state so the header band stays clean.
 */
export function OperationBanner({
  deploymentId,
  snapshotOverride,
  disablePoll = false,
}: ReadinessChipProps) {
  const polled = useReadinessSnapshot(deploymentId, !disablePoll && snapshotOverride === undefined)
  const snapshot = snapshotOverride !== undefined ? snapshotOverride : polled
  const state = deriveChipState(snapshot)
  if (state !== 'operation') return null
  return (
    <div
      data-testid="operation-banner"
      role="status"
      className="flex items-center gap-3 border-b border-[color:rgba(129,140,248,0.30)] bg-[color:rgba(129,140,248,0.08)] px-4 py-2 text-xs text-[var(--color-text)]"
    >
      <span
        className="readiness-chip-dot-pulse"
        style={{ width: 7, height: 7, borderRadius: '50%', background: '#818CF8', flexShrink: 0 }}
        aria-hidden
      />
      <span className="font-medium">
        An operation is in progress — the Sovereign is running a finite
        cutover / switchover. Convergence is unaffected.
      </span>
      <Link
        to={'/dashboard' as never}
        data-testid="operation-banner-link"
        className="ml-auto rounded-md border border-[color:rgba(129,140,248,0.40)] px-2 py-0.5 font-semibold text-[#818CF8] no-underline hover:bg-[color:rgba(129,140,248,0.15)]"
      >
        View progress →
      </Link>
      <style>{READINESS_CHIP_CSS}</style>
    </div>
  )
}

const READINESS_CHIP_CSS = `
.readiness-chip-dot-pulse {
  animation: readiness-chip-pulse 2s ease-in-out infinite;
}
@keyframes readiness-chip-pulse {
  0%, 100% { opacity: 1; transform: scale(1); }
  50%      { opacity: 0.4; transform: scale(0.65); }
}
`
