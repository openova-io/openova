/**
 * FalcoAlerts — runtime-security alerts surface (slice C11-008,
 * Wave-2 Family-E).
 *
 * Reads from `/api/v1/sovereigns/{id}/compliance/falco` which projects
 * Falcosidekick → k8s Events into a table the operator can scan. The
 * page is mounted as a sub-tab of the SRE-Lead and Security-Lead
 * dashboards (see CategoryDataStatus side-by-side rendering); it is
 * also reachable directly via `/compliance/runtime` once router.tsx
 * adds the route in Wave-2 collector PR.
 *
 * Empty-state matrix:
 *   • installed=false        → "Falco not yet deployed in this Sovereign."
 *   • installed=true, items=0 → "Falco running — no alerts on this window."
 *   • installed=true, items>0 → table with time, priority pill, rule, output.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #2 we never seed synthetic events:
 * empty means empty, no fixture rows.
 */

import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { getFalcoEvents, type FalcoEvent } from '@/lib/compliance.api'

export interface FalcoAlertsProps {
  /** Sovereign id (deploymentId on chroot). */
  sovereignId: string
  /** Test seam — bypass fetch. */
  initialData?: { items: FalcoEvent[]; total: number; installed: boolean; source: string; updatedAt: string }
  /** Filter cap (default 200, max 1000). */
  limit?: number
}

const PRIORITY_PALETTE: Record<string, { bg: string; fg: string; border: string }> = {
  EMERGENCY: { bg: 'rgba(220, 38, 38, 0.18)', fg: '#fecaca', border: 'rgba(220, 38, 38, 0.55)' },
  ALERT:     { bg: 'rgba(220, 38, 38, 0.15)', fg: '#fecaca', border: 'rgba(220, 38, 38, 0.45)' },
  CRITICAL:  { bg: 'rgba(239, 68, 68, 0.15)', fg: '#fecaca', border: 'rgba(239, 68, 68, 0.45)' },
  ERROR:     { bg: 'rgba(249, 115, 22, 0.15)', fg: '#fed7aa', border: 'rgba(249, 115, 22, 0.45)' },
  WARNING:   { bg: 'rgba(245, 158, 11, 0.12)', fg: '#fcd34d', border: 'rgba(245, 158, 11, 0.45)' },
  NOTICE:    { bg: 'rgba(59, 130, 246, 0.10)', fg: '#bfdbfe', border: 'rgba(59, 130, 246, 0.35)' },
  INFO:      { bg: 'rgba(34, 197, 94, 0.10)', fg: '#bbf7d0', border: 'rgba(34, 197, 94, 0.35)' },
  DEBUG:     { bg: 'rgba(125, 125, 125, 0.10)', fg: '#cbd5e1', border: 'rgba(125, 125, 125, 0.35)' },
}

const ALL_PRIORITIES = ['EMERGENCY', 'ALERT', 'CRITICAL', 'ERROR', 'WARNING', 'NOTICE', 'INFO', 'DEBUG']
const DEFAULT_PRIORITIES = ['CRITICAL', 'ERROR', 'WARNING']

export function FalcoAlerts({ sovereignId, initialData, limit = 200 }: FalcoAlertsProps) {
  const [selectedPrio, setSelectedPrio] = useState<string[]>(DEFAULT_PRIORITIES)

  const q = useQuery({
    queryKey: ['compliance', sovereignId, 'falco', selectedPrio.join(','), limit],
    queryFn: () => getFalcoEvents(sovereignId, { limit, priorities: selectedPrio }),
    enabled: !initialData && !!sovereignId,
    staleTime: 15_000,
    refetchInterval: 30_000,
  })

  const data = initialData ?? q.data
  const items = data?.items ?? []
  const installed = data?.installed ?? false

  function togglePrio(p: string) {
    setSelectedPrio((cur) => (cur.includes(p) ? cur.filter((x) => x !== p) : [...cur, p]))
  }

  return (
    <div data-testid="falco-alerts-panel" className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
      <div className="mb-3 flex items-center justify-between">
        <h2 className="text-base font-semibold text-[var(--color-text-strong)]">
          Falco — runtime security alerts
        </h2>
        <span className="text-[10px] uppercase tracking-wide text-[var(--color-text-dim)]" data-testid="falco-alerts-source">
          source: {data?.source ?? '—'} · updated {data?.updatedAt ?? '—'}
        </span>
      </div>

      <div className="mb-3 flex flex-wrap items-center gap-1 text-[10px]" data-testid="falco-priority-chips">
        <span className="mr-1 uppercase text-[var(--color-text-dim)]">priority:</span>
        {ALL_PRIORITIES.map((p) => {
          const active = selectedPrio.includes(p)
          const palette = PRIORITY_PALETTE[p] ?? PRIORITY_PALETTE.INFO
          return (
            <button
              key={p}
              type="button"
              data-testid={`falco-prio-chip-${p}`}
              onClick={() => togglePrio(p)}
              className="rounded-md border px-2 py-0.5 font-semibold uppercase transition"
              style={{
                background: active ? palette.bg : 'transparent',
                color: active ? palette.fg : 'var(--color-text-dim)',
                borderColor: active ? palette.border : 'var(--color-border)',
              }}
            >
              {p}
            </button>
          )
        })}
      </div>

      {!installed ? (
        <p className="text-xs text-[var(--color-text-dim)]" data-testid="falco-alerts-empty-not-installed">
          Falco runtime-security DaemonSet is not yet deployed in this Sovereign. Install
          {' '}
          <code className="font-mono">bp-falco</code>{' '}
          (via the marketplace) to start collecting kernel-syscall alerts here.
        </p>
      ) : items.length === 0 ? (
        <p className="text-xs text-[var(--color-text-dim)]" data-testid="falco-alerts-empty-no-events">
          Falco is running — no alerts on the selected priorities within the recent window. Widen
          the priority chips above to see lower-severity activity.
        </p>
      ) : (
        <div className="overflow-x-auto">
          <table className="w-full border-collapse text-xs" data-testid="falco-alerts-table">
            <thead>
              <tr className="border-b border-[var(--color-border)] text-left uppercase text-[var(--color-text-dim)]">
                <th className="px-2 py-1.5">Time</th>
                <th className="px-2 py-1.5">Priority</th>
                <th className="px-2 py-1.5">Rule</th>
                <th className="px-2 py-1.5">Namespace / Pod</th>
                <th className="px-2 py-1.5">Output</th>
              </tr>
            </thead>
            <tbody>
              {items.map((ev, i) => (
                <FalcoRow key={`${ev.time}-${i}`} ev={ev} index={i} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function FalcoRow({ ev, index }: { ev: FalcoEvent; index: number }) {
  const palette = PRIORITY_PALETTE[(ev.priority ?? '').toUpperCase()] ?? PRIORITY_PALETTE.INFO
  return (
    <tr
      data-testid={`falco-row-${index}`}
      className="border-b border-[var(--color-border)] hover:bg-[var(--color-bg)]"
    >
      <td className="px-2 py-1.5 font-mono text-[10px] text-[var(--color-text-dim)]">
        {ev.time || '—'}
      </td>
      <td className="px-2 py-1.5">
        <span
          className="inline-flex items-center rounded-md border px-1.5 py-0.5 text-[10px] font-semibold uppercase"
          style={{ background: palette.bg, color: palette.fg, borderColor: palette.border }}
          data-testid={`falco-row-${index}-prio`}
        >
          {ev.priority || 'INFO'}
        </span>
      </td>
      <td className="px-2 py-1.5 font-mono">{ev.rule || '—'}</td>
      <td className="px-2 py-1.5 font-mono text-[var(--color-text-dim)]">
        {ev.namespace ? <>{ev.namespace}{ev.pod ? ` / ${ev.pod}` : ''}</> : '—'}
      </td>
      <td className="px-2 py-1.5 text-[var(--color-text)]">{ev.output || '—'}</td>
    </tr>
  )
}
