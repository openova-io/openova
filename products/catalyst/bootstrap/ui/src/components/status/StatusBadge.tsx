/**
 * StatusBadge / StatusReason / RegionStatusList — the one shared renderer
 * for the D7 status contract (ADR-0015, Refs #7067).
 *
 * Everything here renders API values VERBATIM. The only transformation is
 * the colour tone, chosen from `state`; the text the operator reads is
 * exactly what `statusOf(object)` returned. An absent status renders
 * `Unknown` — the honest answer, never a guessed `Pending`/`Healthy`.
 */
import type { CSSProperties } from 'react'
import type { ObjectStatus, RegionObjectStatus } from '@/lib/objectStatus'
import { objectStateOf } from '@/lib/objectStatus'

type Tone = 'ok' | 'bad' | 'unknown'

function toneOf(state: string): Tone {
  if (state === 'Ready') return 'ok'
  if (state === 'NotReady') return 'bad'
  return 'unknown'
}

const TONE_STYLE: Record<Tone, CSSProperties> = {
  ok: { background: 'rgba(34,197,94,0.15)', color: 'rgb(22,163,74)', borderColor: 'rgba(34,197,94,0.4)' },
  bad: { background: 'rgba(239,68,68,0.15)', color: 'rgb(220,38,38)', borderColor: 'rgba(239,68,68,0.4)' },
  unknown: { background: 'rgba(148,163,184,0.15)', color: 'rgb(100,116,139)', borderColor: 'rgba(148,163,184,0.4)' },
}

export interface StatusBadgeProps {
  status?: { state?: string; reason?: string; message?: string } | null
  testId?: string
}

/** The state pill. Title carries reason + message verbatim for hover. */
export function StatusBadge({ status, testId = 'status-badge' }: StatusBadgeProps) {
  const state = objectStateOf(status)
  const tone = toneOf(state)
  const title = [status?.reason, status?.message].filter(Boolean).join(': ')
  return (
    <span
      className="chip status-badge"
      data-testid={testId}
      data-state={state}
      title={title || undefined}
      style={{ border: '1px solid', fontWeight: 600, ...TONE_STYLE[tone] }}
    >
      {state}
    </span>
  )
}

export interface StatusReasonProps {
  status?: { state?: string; reason?: string; message?: string } | null
  testId?: string
}

/** Reason + message, verbatim. Renders nothing when both are empty. */
export function StatusReason({ status, testId = 'status-reason' }: StatusReasonProps) {
  const reason = status?.reason?.trim() ?? ''
  const message = status?.message?.trim() ?? ''
  if (!reason && !message) return null
  return (
    <span className="status-reason" data-testid={testId}>
      {reason ? (
        <strong data-testid={`${testId}-reason`}>{reason}</strong>
      ) : null}
      {reason && message ? ' — ' : null}
      {message ? <span data-testid={`${testId}-message`}>{message}</span> : null}
    </span>
  )
}

/** Badge + reason on one line — the common composition. */
export function StatusLine({ status, testId = 'status' }: { status?: ObjectStatus | RegionObjectStatus | null; testId?: string }) {
  return (
    <span className="status-line" data-testid={testId} style={{ display: 'inline-flex', gap: 8, alignItems: 'baseline', flexWrap: 'wrap' }}>
      <StatusBadge status={status} testId={`${testId}-badge`} />
      <StatusReason status={status} testId={`${testId}-reason`} />
    </span>
  )
}

/** One row per region from objectStatus.perRegion, verbatim. */
export function RegionStatusList({ status, testId = 'region-status' }: { status?: ObjectStatus | null; testId?: string }) {
  const rows = status?.perRegion ?? []
  if (rows.length === 0) return null
  return (
    <ul className="region-status-list" data-testid={testId} style={{ listStyle: 'none', padding: 0, margin: 0 }}>
      {rows.map((r) => (
        <li key={r.region} data-testid={`${testId}-${r.region}`}>
          <code>{r.region}</code> <StatusLine status={r} testId={`${testId}-${r.region}-line`} />
        </li>
      ))}
    </ul>
  )
}
