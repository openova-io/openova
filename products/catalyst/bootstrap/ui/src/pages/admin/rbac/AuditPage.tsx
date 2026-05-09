/**
 * AuditPage — RBAC audit trail (EPIC-3 #1098 slice U8).
 *
 * Route: `/admin/rbac/audit` (mothership) + `/rbac/audit` (chroot).
 *
 * Sources:
 *   - REST baseline: GET /api/v1/sovereigns/{id}/audit/rbac (paginated)
 *   - SSE live tail: GET /api/v1/sovereigns/{id}/audit/rbac/stream
 *
 * The page renders a live table — every new event arrives via SSE and
 * is prepended to the list. Filters: actor (substring search) +
 * since (time-range). Pagination via the `nextOffset` cursor.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import { PortalShell } from '@/pages/sovereign/PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import {
  auditActionVerb,
  listRBACAudit,
  rbacAuditStreamURL,
  type AuditEvent,
  type AuditListResponse,
} from './rbac.api'
import {
  actionPalette,
  formatAuditTimestamp,
  mergeAuditEvents,
  resultPalette,
} from './auditHelpers'

export interface AuditPageProps {
  /** Test seam — pre-fill deployment id without TanStack Router. */
  initialDeploymentId?: string
  /** Test seam — pre-fill audit data without network. */
  initialAudit?: AuditListResponse
  /** Test seam — disable SSE stream attach. */
  disableStream?: boolean
}

const TIME_RANGES: { label: string; sinceDelta: number | null }[] = [
  { label: 'All time', sinceDelta: null },
  { label: 'Last 1h', sinceDelta: 60 * 60 * 1000 },
  { label: 'Last 24h', sinceDelta: 24 * 60 * 60 * 1000 },
  { label: 'Last 7d', sinceDelta: 7 * 24 * 60 * 60 * 1000 },
]

export function AuditPage({
  initialDeploymentId,
  initialAudit,
  disableStream = false,
}: AuditPageProps = {}) {
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = initialDeploymentId ?? resolvedId ?? ''

  const [actorQ, setActorQ] = useState('')
  const [rangeIdx, setRangeIdx] = useState(0)
  const [streamed, setStreamed] = useState<AuditEvent[]>([])
  const [streamErr, setStreamErr] = useState<string | null>(null)

  // The `since` filter recomputes on every range change. Use a state
  // value rather than Date.now() in render to keep the React purity
  // check happy. Updating this when the range changes (rather than
  // every render tick) is fine — the audit baseline refresh on a
  // rangeIdx change picks up the latest cutoff.
  const [pageMountTs] = useState(() => Date.now())
  const since = useMemo(() => {
    const range = TIME_RANGES[rangeIdx]
    if (!range || range.sinceDelta == null) return undefined
    return new Date(pageMountTs - range.sinceDelta).toISOString()
  }, [rangeIdx, pageMountTs])

  const auditQ = useQuery({
    queryKey: ['rbac-audit', deploymentId, actorQ, since],
    queryFn: () => listRBACAudit(deploymentId, { actor: actorQ || undefined, since, limit: 100 }),
    enabled: !!deploymentId && !initialAudit,
    staleTime: 5_000,
  })

  // SSE live tail — prepends new events into the streamed buffer. The
  // buffer is bounded so a long-lived tab can't grow unbounded; older
  // events drop off. Heartbeat pings (`: ping ...`) are ignored.
  const esRef = useRef<EventSource | null>(null)
  useEffect(() => {
    if (disableStream || !deploymentId) return
    if (typeof window === 'undefined' || typeof EventSource === 'undefined') return
    const es = new EventSource(rbacAuditStreamURL(deploymentId), { withCredentials: true })
    esRef.current = es
    es.onmessage = (msg) => {
      try {
        const parsed = JSON.parse(msg.data) as AuditEvent
        setStreamed((prev) => {
          const next = [parsed, ...prev]
          return next.slice(0, 200)
        })
      } catch {
        // Ignore malformed frame; SSE heartbeat lines never reach
        // onmessage (they don't carry a `data:` prefix).
      }
    }
    es.onerror = () => {
      setStreamErr('Audit stream disconnected — refresh the page to reconnect.')
    }
    return () => {
      es.close()
      esRef.current = null
    }
  }, [deploymentId, disableStream])

  const audit: AuditListResponse =
    initialAudit ??
    auditQ.data ?? {
      items: [],
      total: 0,
    }

  // Merge streamed events on top of the REST page. De-dup by
  // (auditType, ts, userAccessRef) so an event that arrives via both
  // paths shows once.
  const allRows = useMemo(() => mergeAuditEvents(streamed, audit.items), [streamed, audit.items])

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="RBAC audit">
      <div data-testid="rbac-audit-page" className="mx-auto max-w-6xl px-6 py-4">
        <div className="mb-4 flex items-center justify-between">
          <div>
            <h1 className="text-base font-semibold text-[var(--color-text-strong)]">RBAC audit</h1>
            <p className="text-xs text-[var(--color-text-dim)]">
              Live trail of grant create / update / delete + tier rotations on this Sovereign.
              Source: NATS <code className="font-mono">catalyst.audit</code>.
            </p>
          </div>
          <div className="flex items-center gap-2">
            <input
              type="search"
              data-testid="audit-actor"
              placeholder="filter by actor…"
              value={actorQ}
              onChange={(e) => setActorQ(e.target.value)}
              className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs"
              aria-label="Filter by actor"
            />
            <select
              data-testid="audit-range"
              value={rangeIdx}
              onChange={(e) => setRangeIdx(Number(e.target.value))}
              className="rounded border border-[var(--color-border)] bg-[var(--color-bg-2)] px-2 py-1 text-xs"
              aria-label="Time range"
            >
              {TIME_RANGES.map((r, i) => (
                <option key={r.label} value={i}>
                  {r.label}
                </option>
              ))}
            </select>
          </div>
        </div>

        {streamErr ? (
          <div data-testid="audit-stream-err" className="mb-3 rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
            {streamErr}
          </div>
        ) : null}

        <div className="overflow-x-auto rounded-md border border-[var(--color-border)]">
          <table className="w-full text-sm">
            <thead>
              <tr className="border-b border-[var(--color-border)] bg-[var(--color-bg-2)] text-left text-xs uppercase text-[var(--color-text-dim)]">
                <th className="px-3 py-2">Timestamp</th>
                <th className="px-3 py-2">Actor</th>
                <th className="px-3 py-2">Action</th>
                <th className="px-3 py-2">Target user</th>
                <th className="px-3 py-2">Application</th>
                <th className="px-3 py-2">Tier</th>
                <th className="px-3 py-2">Result</th>
              </tr>
            </thead>
            <tbody>
              {auditQ.isLoading && allRows.length === 0 ? (
                <tr>
                  <td colSpan={7} data-testid="audit-loading" className="px-3 py-4 text-center text-xs text-[var(--color-text-dim)]">
                    Loading…
                  </td>
                </tr>
              ) : allRows.length === 0 ? (
                <tr>
                  <td colSpan={7} data-testid="audit-empty" className="px-3 py-4 text-center text-xs text-[var(--color-text-dim)]">
                    No audit events yet for this Sovereign.
                  </td>
                </tr>
              ) : (
                allRows.map((ev, i) => (
                  <AuditRow key={`${ev.ts}-${ev.userAccessRef}-${i}`} ev={ev} index={i} />
                ))
              )}
            </tbody>
          </table>
        </div>
      </div>
    </PortalShell>
  )
}

/* ── AuditRow component (exported for unit testing) ──────────────── */

/** AuditRow — one row in the audit trail. */
export function AuditRow({ ev, index }: { ev: AuditEvent; index: number }) {
  return (
    <tr
      data-testid={`audit-row-${index}`}
      data-audit-type={ev.auditType}
      className="border-b border-[var(--color-border)] last:border-b-0"
    >
      <td className="px-3 py-2 text-xs text-[var(--color-text-dim)]" data-testid={`audit-ts-${index}`}>
        {formatAuditTimestamp(ev.ts)}
      </td>
      <td className="px-3 py-2 text-xs">
        <code className="font-mono text-[var(--color-text)]">{ev.actor || 'system'}</code>
      </td>
      <td className="px-3 py-2 text-xs">
        <ActionPill auditType={ev.auditType} />
      </td>
      <td className="px-3 py-2 text-xs">
        <code className="font-mono">{ev.targetUserEmail || ev.targetUser || '—'}</code>
      </td>
      <td className="px-3 py-2 text-xs">
        <code className="font-mono">{ev.targetApp || '— global —'}</code>
      </td>
      <td className="px-3 py-2 text-xs">
        {ev.previousTier && ev.previousTier !== ev.tier ? (
          <span data-testid={`audit-tier-${index}`}>
            <code className="font-mono text-[var(--color-text-dim)]">{ev.previousTier}</code>
            {' → '}
            <code className="font-mono text-[var(--color-text)]">{ev.tier}</code>
          </span>
        ) : (
          <code className="font-mono text-[var(--color-text)]" data-testid={`audit-tier-${index}`}>
            {ev.tier ?? '—'}
          </code>
        )}
      </td>
      <td className="px-3 py-2 text-xs">
        <ResultPill result={ev.result || 'ok'} />
      </td>
    </tr>
  )
}

/** ActionPill — small label that color-codes the audit-type. */
export function ActionPill({ auditType }: { auditType: string }) {
  const palette = actionPalette(auditType)
  return (
    <span
      data-testid={`audit-action-pill-${auditType}`}
      className="inline-flex items-center rounded-md px-2 py-0.5 text-[10px] font-semibold uppercase"
      style={{ background: palette.bg, color: palette.fg, border: `1px solid ${palette.border}` }}
    >
      {auditActionVerb(auditType)}
    </span>
  )
}

function ResultPill({ result }: { result: string }) {
  const palette = resultPalette(result)
  return (
    <span
      className="inline-flex items-center rounded-md px-2 py-0.5 text-[10px] font-semibold uppercase"
      style={{ background: palette.bg, color: palette.fg, border: `1px solid ${palette.border}` }}
    >
      {result}
    </span>
  )
}
