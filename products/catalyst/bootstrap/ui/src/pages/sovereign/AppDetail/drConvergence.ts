/**
 * drConvergence — desired-state switchover helpers for the Topology tab.
 *
 * The operator declares the Primary in placement; the continuum-controller
 * converges the writer onto it. These helpers read that convergence off the
 * replication-status payload (catalyst-api continuum_convergence.go).
 */
import type { ContinuumReplicationStatus } from '@/lib/continuum.api'

/** Fast poll while converging, so the step counter moves live. */
export const DR_CONVERGING_POLL_MS = 3_000
/** Calm cadence otherwise. */
export const DR_STEADY_POLL_MS = 30_000

/**
 * drIsConverging — the controller is moving (or failing to move) the writer
 * onto the DECLARED Primary. Only meaningful when a Primary is declared and
 * the reading is live: an older backend (no desiredPrimaryRegion) or a
 * fallback envelope never reads as converging.
 */
export function drIsConverging(st: ContinuumReplicationStatus | undefined): boolean {
  if (!st || st.source !== 'live' || !st.desiredPrimaryRegion) return false
  if (st.switchoverInProgress) return true
  return st.converged === false
}

/** drPollInterval — the replication-status refetch interval for a reading. */
export function drPollInterval(st: ContinuumReplicationStatus | undefined): number {
  return drIsConverging(st) ? DR_CONVERGING_POLL_MS : DR_STEADY_POLL_MS
}
