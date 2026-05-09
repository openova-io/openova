/**
 * EventsPanel — EPIC-4 Slice R4 (#1099) — Events for the focused
 * resource, sortable by timestamp + severity-coloured.
 *
 * Reads from the page-level k8s SSE snapshot (CloudPage's useCloud()
 * already subscribes to all kinds, including the new `event` kind
 * registered by R4). Filters Events whose `involvedObject.namespace +
 * .name` match the focused resource.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #3 (event-driven) — no polling; the SSE delta drives re-render.
 *   #4 (never hardcode) — colour palette is exported constants.
 */

import { useMemo } from 'react'

import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

// Palette is intentionally module-private to keep this file
// component-only (react-refresh/only-export-components). External
// consumers go through the EventsPanel API.
const EVENT_SEVERITY_PALETTE: Record<string, string> = {
  Warning: 'text-amber-300',
  Error: 'text-rose-300',
  Normal: 'text-[var(--color-text-dim)]',
}

export interface EventsPanelProps {
  /** All event-kind objects from the live SSE snapshot. */
  allEvents: K8sObject[]
  /** Focus resource ns; '' for cluster-scoped. */
  ns?: string
  /** Focus resource name. */
  name: string
  /** Focus resource kind, in canonical (lowercase) form. */
  kindCanonical: string
  /** Test seam — explicit empty-state when supplied as true. */
  isLoading?: boolean
}

interface NormalizedEvent {
  id: string
  timestamp: string
  type: string
  reason: string
  message: string
  count: number
  severityClass: string
}

function involvedMatches(ev: K8sObject, ns: string | undefined, name: string, kind: string): boolean {
  // events.k8s.io/v1 nests this under .regarding (was involvedObject in core/v1).
  const e = ev as Record<string, unknown>
  const regarding =
    (e.regarding as { namespace?: string; name?: string; kind?: string } | undefined) ??
    (e.involvedObject as { namespace?: string; name?: string; kind?: string } | undefined)
  if (!regarding) return false
  if (regarding.name !== name) return false
  if ((regarding.namespace ?? '') !== (ns ?? '')) return false
  if (kind && regarding.kind && regarding.kind.toLowerCase() !== kind.toLowerCase()) {
    return false
  }
  return true
}

function normalizeEvent(ev: K8sObject): NormalizedEvent {
  const e = ev as Record<string, unknown>
  const type = (e.type as string) ?? 'Normal'
  const reason = (e.reason as string) ?? ''
  const note = (e.note as string) ?? (e.message as string) ?? ''
  const ts = ((ev.metadata?.creationTimestamp as string) ?? '') || ((e.eventTime as string) ?? '')
  const series = (e.series as { count?: number } | undefined) ?? (e as { count?: number })
  const count = (series?.count as number | undefined) ?? 1
  const severity = EVENT_SEVERITY_PALETTE[type] ?? EVENT_SEVERITY_PALETTE.Normal
  return {
    id: (ev.metadata?.uid as string) ?? `${reason}-${ts}-${note.slice(0, 8)}`,
    timestamp: ts,
    type,
    reason,
    message: note,
    count,
    severityClass: severity,
  }
}

export function EventsPanel({ allEvents, ns, name, kindCanonical, isLoading }: EventsPanelProps) {
  const events = useMemo<NormalizedEvent[]>(() => {
    const filtered = allEvents.filter((ev) => involvedMatches(ev, ns, name, kindCanonical))
    const norm = filtered.map(normalizeEvent)
    norm.sort((a, b) => b.timestamp.localeCompare(a.timestamp))
    return norm
  }, [allEvents, ns, name, kindCanonical])

  if (isLoading) {
    return (
      <div data-testid="events-panel-loading" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
        Loading events…
      </div>
    )
  }
  if (events.length === 0) {
    return (
      <div data-testid="events-panel-empty" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
        No events for this resource.
      </div>
    )
  }
  return (
    <div data-testid="events-panel" className="overflow-x-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)]">
      <table className="w-full border-collapse text-sm">
        <thead className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
          <tr className="border-b border-[var(--color-border)]">
            <th className="px-3 py-2 text-left font-medium">Type</th>
            <th className="px-3 py-2 text-left font-medium">Reason</th>
            <th className="px-3 py-2 text-left font-medium">Message</th>
            <th className="px-3 py-2 text-left font-medium">Count</th>
            <th className="px-3 py-2 text-left font-medium">When</th>
          </tr>
        </thead>
        <tbody>
          {events.map((e) => (
            <tr
              key={e.id}
              data-testid={`events-panel-row-${e.reason}`}
              className="border-b border-[var(--color-border)] last:border-0"
            >
              <td className={`px-3 py-2 font-mono text-xs ${e.severityClass}`}>{e.type}</td>
              <td className="px-3 py-2 font-mono text-xs text-[var(--color-text)]">{e.reason}</td>
              <td className="px-3 py-2 text-[var(--color-text)]">{e.message || '—'}</td>
              <td className="px-3 py-2 text-[var(--color-text)]">{e.count}</td>
              <td className="px-3 py-2 font-mono text-xs text-[var(--color-text-dim)]">{e.timestamp || '—'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
