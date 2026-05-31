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

import { useEffect, useMemo, useState } from 'react'

import { API_BASE } from '@/shared/config/urls'
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
  /** All event-kind objects from the live SSE snapshot. Kept for
   *  test/legacy paths; primary source is the deploymentId-keyed
   *  apiserver-direct fetch (G89 #2636). */
  allEvents: K8sObject[]
  /** Focus resource ns; '' for cluster-scoped. */
  ns?: string
  /** Focus resource name. */
  name: string
  /** Focus resource kind, in canonical (lowercase) form. */
  kindCanonical: string
  /** Sovereign deploymentId — drives the apiserver-direct events fetch. */
  deploymentId?: string
  /** Test seam — explicit empty-state when supplied as true. */
  isLoading?: boolean
}

interface ApiserverEvent {
  uid?: string
  regarding?: { kind?: string; name?: string; namespace?: string }
  type?: string
  reason?: string
  note?: string
  eventTime?: string
  series?: { count?: number }
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

function normalizeApiserverEvent(e: ApiserverEvent): NormalizedEvent {
  const type = e.type ?? 'Normal'
  const severity = EVENT_SEVERITY_PALETTE[type] ?? EVENT_SEVERITY_PALETTE.Normal
  const count = e.series?.count ?? 1
  return {
    id: e.uid || `${e.reason ?? ''}-${e.eventTime ?? ''}`,
    timestamp: e.eventTime ?? '',
    type,
    reason: e.reason ?? '',
    message: e.note ?? '',
    count,
    severityClass: severity,
  }
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

export function EventsPanel({ allEvents, ns, name, kindCanonical, deploymentId, isLoading }: EventsPanelProps) {
  // G89 #2636: apiserver-direct events fetch (TBD-V50 forbids caching
  // events as a k8scache kind — they're unbounded). Refetch every 15s
  // for soft live-updates; the SSE-derived `allEvents` is kept as a
  // fallback for tests/legacy paths but is empty in practice.
  const [apiEvents, setApiEvents] = useState<ApiserverEvent[]>([])
  const [fetching, setFetching] = useState<boolean>(!!deploymentId)
  const [fetchErr, setFetchErr] = useState<string | null>(null)

  useEffect(() => {
    if (!deploymentId || !name || !kindCanonical) {
      setFetching(false)
      return
    }
    let aborted = false
    const ac = new AbortController()
    const nsSeg = ns && ns !== '' ? encodeURIComponent(ns) : '_'
    const url = `${API_BASE}/v1/sovereigns/${encodeURIComponent(deploymentId)}/k8s/events-for/${encodeURIComponent(kindCanonical)}/${nsSeg}/${encodeURIComponent(name)}?limit=200`
    const tick = async () => {
      try {
        const r = await fetch(url, { signal: ac.signal })
        if (!r.ok) {
          if (!aborted) {
            setFetchErr(`HTTP ${r.status}`)
            setFetching(false)
          }
          return
        }
        const j = await r.json()
        if (!aborted) {
          setApiEvents(Array.isArray(j.events) ? j.events : [])
          setFetchErr(null)
          setFetching(false)
        }
      } catch (e) {
        if (!aborted) {
          setFetchErr(String(e))
          setFetching(false)
        }
      }
    }
    tick()
    const interval = setInterval(tick, 15_000)
    return () => {
      aborted = true
      ac.abort()
      clearInterval(interval)
    }
  }, [deploymentId, kindCanonical, ns, name])

  const events = useMemo<NormalizedEvent[]>(() => {
    // Prefer the apiserver-direct list when present.
    if (apiEvents.length > 0) {
      const norm = apiEvents.map(normalizeApiserverEvent)
      norm.sort((a, b) => b.timestamp.localeCompare(a.timestamp))
      return norm
    }
    // Fallback: SSE-snapshot filter (tests / legacy seam).
    const filtered = allEvents.filter((ev) => involvedMatches(ev, ns, name, kindCanonical))
    const norm = filtered.map(normalizeEvent)
    norm.sort((a, b) => b.timestamp.localeCompare(a.timestamp))
    return norm
  }, [apiEvents, allEvents, ns, name, kindCanonical])

  // Surface a one-line fetch error inline below the table (don't block
  // SSE-snapshot fallback). isLoading prop wins for tests.
  const showLoading = isLoading || (fetching && events.length === 0)

  if (showLoading) {
    return (
      <div data-testid="events-panel-loading" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
        Loading events…
      </div>
    )
  }
  if (events.length === 0) {
    return (
      <div data-testid="events-panel-empty" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
        No events for this resource.{fetchErr ? ` (${fetchErr})` : ''}
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
