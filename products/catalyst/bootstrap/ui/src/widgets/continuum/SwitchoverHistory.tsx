/**
 * SwitchoverHistory — EPIC-6 Slice U-DR-1 (#1101).
 *
 * Tabular view of past switchover audit events from
 * GET /api/v1/sovereigns/{id}/audit/continuum (filtered to the
 * `continuum-switchover` audit-type). Click a row → modal with the 7
 * step audit events that made up that switchover (matched by
 * timestamp proximity to the row's event).
 *
 * Pure presentation — receives the audit events as a prop. The parent
 * (DRSection) wires the React Query subscription + the SSE stream so
 * the table updates as new events arrive.
 */

import { useMemo, useState } from 'react'

import type { ContinuumAuditEvent } from '@/lib/continuum.api'

export interface SwitchoverHistoryProps {
  events: ContinuumAuditEvent[]
  /** Optional filter — only render events whose Application matches. */
  applicationName?: string
}

interface Row {
  ev: ContinuumAuditEvent
  /** Step events bundled by timestamp proximity (within 60s before+after). */
  stepEvents: ContinuumAuditEvent[]
}

const SWITCHOVER_TYPES = new Set([
  'continuum-switchover',
  'continuum-switchover-requested',
])

const STEP_TYPES = new Set([
  'continuum-switchover',
  'continuum-cnpg-promotable',
  'continuum-cnpg-lag-breach',
  'continuum-lease-acquired',
  'continuum-lease-lost',
  'continuum-failback-pending',
  'continuum-failback-completed',
  'continuum-error',
])

export function SwitchoverHistory({ events, applicationName }: SwitchoverHistoryProps) {
  const [openIdx, setOpenIdx] = useState<number | null>(null)

  const rows: Row[] = useMemo(() => {
    const filtered = events.filter((e) => {
      if (!SWITCHOVER_TYPES.has(e.auditType)) return false
      if (applicationName && e.targetApp && e.targetApp !== applicationName) return false
      return true
    })
    // Newest-first per the catalyst-api list endpoint contract.
    return filtered.map((ev) => ({
      ev,
      stepEvents: bundleStepEvents(events, ev),
    }))
  }, [events, applicationName])

  if (rows.length === 0) {
    return (
      <div
        data-testid="continuum-history-empty"
        className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3 text-xs text-[var(--color-text-dim)]"
      >
        No switchover events recorded yet.
      </div>
    )
  }

  return (
    <div className="continuum-history" data-testid="continuum-history">
      <table className="w-full text-xs">
        <thead className="text-left text-[var(--color-text-dim)]">
          <tr>
            <th className="pb-1.5">Timestamp</th>
            <th className="pb-1.5">From</th>
            <th className="pb-1.5">To</th>
            <th className="pb-1.5">Result</th>
            <th className="pb-1.5">Initiated by</th>
            <th className="pb-1.5">Detail</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((row, idx) => {
            const fromTo = parseFromTo(row.ev.detail)
            return (
              <tr
                key={`${row.ev.ts}-${idx}`}
                data-testid={`continuum-history-row-${idx}`}
                className="cursor-pointer border-t border-[var(--color-border)] hover:bg-[var(--color-bg-2)]"
                onClick={() => setOpenIdx(idx)}
              >
                <td className="py-1.5 font-mono text-[var(--color-text)]">
                  {new Date(row.ev.ts).toLocaleString()}
                </td>
                <td className="py-1.5 font-mono text-[var(--color-text-dim)]">{fromTo.from}</td>
                <td className="py-1.5 font-mono text-[var(--color-text-dim)]">{fromTo.to}</td>
                <td className="py-1.5">
                  <span
                    className={`inline-flex rounded-md px-1.5 py-0.5 text-[10px] font-semibold uppercase ${
                      (row.ev.result ?? 'ok') === 'ok'
                        ? 'bg-green-500/10 text-green-400'
                        : 'bg-red-500/10 text-red-400'
                    }`}
                  >
                    {row.ev.result ?? 'ok'}
                  </span>
                </td>
                <td className="py-1.5 text-[var(--color-text-dim)]">{row.ev.actor ?? '—'}</td>
                <td className="py-1.5 text-[var(--color-text-dim)]">{row.ev.detail ?? ''}</td>
              </tr>
            )
          })}
        </tbody>
      </table>

      {openIdx !== null ? (
        <div
          role="dialog"
          aria-modal="true"
          data-testid="continuum-history-modal"
          className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 p-4"
          onClick={() => setOpenIdx(null)}
        >
          <div
            className="max-h-[80vh] w-full max-w-2xl overflow-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-5"
            onClick={(e) => e.stopPropagation()}
          >
            <div className="mb-3 flex items-baseline justify-between">
              <h3 className="text-base font-semibold text-[var(--color-text)]">
                Switchover audit trail
              </h3>
              <button
                type="button"
                data-testid="continuum-history-modal-close"
                className="text-xs text-[var(--color-text-dim)] hover:text-[var(--color-text)]"
                onClick={() => setOpenIdx(null)}
              >
                Close
              </button>
            </div>
            <p className="mb-3 text-xs text-[var(--color-text-dim)]">
              Events emitted by the K-Cont-2 reconciler around{' '}
              {new Date(rows[openIdx].ev.ts).toLocaleString()} (per K-Cont-2 the 7 steps emit one
              audit event each).
            </p>
            <ul className="space-y-1.5 text-xs">
              {rows[openIdx].stepEvents.map((s, i) => (
                <li
                  key={i}
                  data-testid={`continuum-history-modal-step-${i}`}
                  className="grid grid-cols-[10rem_8rem_1fr] items-baseline gap-2"
                >
                  <code className="font-mono text-[var(--color-text-dim)]">
                    {new Date(s.ts).toLocaleTimeString()}
                  </code>
                  <code className="font-mono text-[var(--color-accent)]">{s.auditType}</code>
                  <span className="text-[var(--color-text)]">{s.detail ?? ''}</span>
                </li>
              ))}
            </ul>
          </div>
        </div>
      ) : null}
    </div>
  )
}

/**
 * bundleStepEvents picks all step-shaped audit events within ±90s of
 * the row's timestamp. The K-Cont-2 sequencer happy path is <60s end
 * to end so a 90s window catches every step + any retry padding
 * without bleeding into adjacent switchovers.
 */
function bundleStepEvents(all: ContinuumAuditEvent[], row: ContinuumAuditEvent): ContinuumAuditEvent[] {
  const rowTs = new Date(row.ts).getTime()
  if (!Number.isFinite(rowTs)) return [row]
  const windowMs = 90_000
  const out = all.filter((e) => {
    if (!STEP_TYPES.has(e.auditType)) return false
    const t = new Date(e.ts).getTime()
    if (!Number.isFinite(t)) return false
    return Math.abs(t - rowTs) <= windowMs
  })
  // Sort oldest-first so the modal reads chronologically.
  out.sort((a, b) => new Date(a.ts).getTime() - new Date(b.ts).getTime())
  if (out.length === 0) return [row]
  return out
}

/**
 * parseFromTo extracts a "<from> → <to>" pair from the audit event's
 * Detail string (the catalyst-api stamps it as "switchover requested:
 * <from> → <to> (...)"). Returns "—" placeholders when the format
 * doesn't match.
 */
function parseFromTo(detail: string | undefined): { from: string; to: string } {
  if (!detail) return { from: '—', to: '—' }
  const m = /([a-z0-9-]+)\s*(?:→|->|→|to)\s*([a-z0-9-]+)/i.exec(detail)
  if (!m) return { from: '—', to: '—' }
  return { from: m[1], to: m[2] }
}
