/**
 * job-detail.fixture.ts — sample execution-log payload for the
 * GitLab-CI-style ExecutionLogs viewer (epic openova#204 item 3).
 *
 * The viewer hits `GET /api/v1/actions/executions/{id}/logs` and
 * receives `{ lines, total, executionFinished }`. This module returns
 * a deterministic 60-line payload spanning all four level badges
 * (INFO/DEBUG/WARN/ERROR) so the viewer can be exercised end-to-end
 * in unit tests, Storybook, and Playwright at-1440px screenshots
 * before the catalyst-api executions endpoint lands.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall, target-state shape),
 * the fixture matches the FINAL wire shape from the spec contract —
 * not a reduced "until backend lands" surface.
 */

import type { ExecutionLogsResponse, LogLine, LogLevel } from '@/components/ExecutionLogs'

/* Anchor the timestamps so a snapshot regression test can compare
 * formatted output byte-for-byte without flake. The viewer slices to
 * `HH:MM:SS.MMM`; the date prefix is irrelevant for visual rendering. */
const BASE_EPOCH_MS = Date.UTC(2026, 3, 29, 15, 0, 0) // 2026-04-29T15:00:00Z

/** Cycle the four levels deterministically: INFO INFO DEBUG INFO WARN ERROR. */
const LEVEL_CYCLE: LogLevel[] = ['INFO', 'INFO', 'DEBUG', 'INFO', 'WARN', 'ERROR']

/** Sample messages — long enough to exercise wrapping, structured enough
 *  that a screenshot review can spot a missing column at a glance. */
const MESSAGES: string[] = [
  'Reconciling HelmRelease bp-cilium in namespace kube-system',
  'Pulling chart oci://ghcr.io/openova-io/openova/charts/bp-cilium:1.4.2',
  'Chart manifest digest: sha256:9b2c4f1a3e5d7b8c0f1a2e3d4b5c6a7e8f9a0b1c',
  'Applied 12 manifests, 3 created, 9 unchanged',
  'Slow chart pull — 8.2s (threshold 5s)',
  'rate limited by ghcr.io api — backing off 1500ms',
  'HelmRelease bp-cilium reconciliation succeeded',
  'Reconciling HelmRelease bp-cert-manager in namespace cert-manager',
  'Pulling chart oci://ghcr.io/openova-io/openova/charts/bp-cert-manager:1.5.0',
  'Applied 18 manifests, 18 created, 0 unchanged',
]

/** Build a single LogLine for index `n` (1-based). */
function makeLine(n: number): LogLine {
  const level = LEVEL_CYCLE[(n - 1) % LEVEL_CYCLE.length]!
  // Stagger by ~370ms per line so the millisecond column has visible
  // variance across all 60 entries.
  const tsMs = BASE_EPOCH_MS + (n - 1) * 370
  const iso = new Date(tsMs).toISOString()
  const msg = MESSAGES[(n - 1) % MESSAGES.length]!
  return {
    lineNumber: n,
    timestamp: iso,
    level,
    message: `[${String(n).padStart(3, '0')}] ${msg}`,
  }
}

/**
 * 60 lines across all four levels — covers the founder spec's "50+
 * across all 4 levels" requirement comfortably and gives the viewer
 * enough body to scroll-test the Resume-tail-pause behaviour.
 */
export const JOB_DETAIL_LOG_LINES: readonly LogLine[] = Array.from(
  { length: 60 },
  (_, i) => makeLine(i + 1),
)

/**
 * In-memory page server — emits a 500-row chunk slice from
 * `JOB_DETAIL_LOG_LINES` matching the wire envelope. Used by the
 * Storybook play function and by Playwright route fixtures.
 */
export function pageJobDetailLogs(args: {
  fromLine: number
  limit: number
  executionFinished?: boolean
}): ExecutionLogsResponse {
  const { fromLine, limit, executionFinished = true } = args
  const slice = JOB_DETAIL_LOG_LINES.filter((l) => l.lineNumber >= fromLine).slice(0, limit)
  return {
    lines: slice,
    total: JOB_DETAIL_LOG_LINES.length,
    executionFinished:
      executionFinished &&
      slice.length > 0 &&
      slice[slice.length - 1]!.lineNumber === JOB_DETAIL_LOG_LINES.length,
  }
}
