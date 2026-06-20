/**
 * K8sListPage — generic list view for any kind in the k8scache
 * registry. Subscribes to the same SSE stream the architecture-graph
 * widget uses (one shared connection per page is established at the
 * widget level; this page opens an additional one when rendered
 * standalone via /cloud?view=list&kind=X).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #3 (event-driven) — no polling. Live snapshot via SSE; the table
 *      re-renders on every applied delta.
 *   #4 (never hardcode) — column extractors are passed in by the
 *      kind catalogue (kinds.ts), not hardcoded per page.
 */

import { useMemo } from 'react'
import { useCloud } from '../CloudPage'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { resourceDetailHref } from './resource.api'

export interface K8sListColumn {
  /** Column header — short, ≤24 chars. */
  header: string
  /** Pull a string from the object. Returns "—" by convention when
   *  the field is absent. */
  extract: (obj: K8sObject) => string
}

export interface K8sListPageProps {
  /** Kind name as registered in the k8scache registry (e.g. "pod",
   *  "deployment", "service"). */
  kind: string
  /** H1-style page title. */
  title: string
  /** One-line description rendered under the title. */
  tagline: string
  /** Column definitions. Order is render order (left → right). */
  columns: K8sListColumn[]
  /** When true, items are sorted by namespace then name. Default true. */
  sortByName?: boolean
}

/* ── Common column extractors ──────────────────────────────────── */

export const COL_NAME: K8sListColumn = {
  header: 'Name',
  extract: (o) => o.metadata?.name ?? '—',
}

export const COL_NAMESPACE: K8sListColumn = {
  header: 'Namespace',
  extract: (o) => o.metadata?.namespace ?? '—',
}

export const COL_AGE: K8sListColumn = {
  header: 'Age',
  extract: (o) => {
    const ts = o.metadata?.creationTimestamp
    if (!ts) return '—'
    const ms = Date.now() - new Date(ts).getTime()
    const s = Math.max(0, Math.floor(ms / 1000))
    if (s < 60) return `${s}s`
    const m = Math.floor(s / 60)
    if (m < 60) return `${m}m`
    const h = Math.floor(m / 60)
    if (h < 24) return `${h}h`
    return `${Math.floor(h / 24)}d`
  },
}

/** Pull a status field from `obj.status.<key>` as a string. */
export function colStatus(key: string, header = 'Status'): K8sListColumn {
  return {
    header,
    extract: (o) => {
      const v = (o.status as Record<string, unknown> | undefined)?.[key]
      return v == null ? '—' : String(v)
    },
  }
}

/** Pull a spec field from `obj.spec.<key>` as a string. */
export function colSpec(key: string, header: string): K8sListColumn {
  return {
    header,
    extract: (o) => {
      const v = (o.spec as Record<string, unknown> | undefined)?.[key]
      return v == null ? '—' : String(v)
    },
  }
}

/* ── Reconciler-status helpers (#3978 / Refs #3970) ───────────────── */

/**
 * Find a status condition by `type` on a K8s object. Flux/cert-manager/
 * ESO/CNPG + the Catalyst CRs all expose `status.conditions[]` with the
 * canonical {type,status,reason,message} shape.
 */
function findCondition(
  o: K8sObject,
  type: string,
): { status?: string; reason?: string; message?: string } | undefined {
  const conds = (o.status as Record<string, unknown> | undefined)?.[
    'conditions'
  ] as Array<Record<string, unknown>> | undefined
  if (!Array.isArray(conds)) return undefined
  const c = conds.find((x) => x?.['type'] === type)
  if (!c) return undefined
  return {
    status: c['status'] as string | undefined,
    reason: c['reason'] as string | undefined,
    message: c['message'] as string | undefined,
  }
}

/**
 * colReconcileStatus — render the canonical Ready condition of a
 * continuous reconciler in the Reconciliation vocabulary
 * (Reconciled / Reconciling / Degraded — NEVER Success/Failed). Mirrors
 * the backend reconciliation_dag.go sticky state machine:
 *   Ready=True              → Reconciled
 *   Ready=False (Stalled)   → Degraded   (reason contains "stall"/"retries exhausted")
 *   Ready=False (otherwise) → Reconciling (Flux is still retrying)
 *   no Ready condition yet   → Reconciling
 *
 * `conditionType` defaults to "Ready"; CNPG Clusters carry no Ready
 * condition so callers pass a phase-based extractor instead.
 */
export function colReconcileStatus(
  header = 'Status',
  conditionType = 'Ready',
): K8sListColumn {
  return {
    header,
    extract: (o) => {
      const c = findCondition(o, conditionType)
      if (!c || c.status == null) return 'Reconciling'
      if (c.status === 'True') return 'Reconciled'
      // Ready=False: terminal-Degraded only when Flux has given up
      // (Stalled), otherwise it's still self-healing → Reconciling.
      const reason = (c.reason ?? '').toLowerCase()
      const stalled =
        reason.includes('stall') ||
        reason.includes('retriesexhausted') ||
        reason.includes('exhausted')
      return stalled ? 'Degraded' : 'Reconciling'
    },
  }
}

/**
 * colCnpgStatus — CNPG Cluster has NO Ready condition; its health lives
 * in `status.phase` ("Cluster in healthy state", "Setting up primary",
 * "Failing over", …). Map onto the recon vocabulary so the column reads
 * the same as every other reconciler.
 */
export function colCnpgStatus(header = 'Status'): K8sListColumn {
  return {
    header,
    extract: (o) => {
      const phase = (o.status as Record<string, unknown> | undefined)?.[
        'phase'
      ] as string | undefined
      if (!phase) return 'Reconciling'
      const p = phase.toLowerCase()
      if (p.includes('healthy')) return 'Reconciled'
      if (p.includes('fail') || p.includes('unrecoverable')) return 'Degraded'
      return 'Reconciling'
    },
  }
}

/**
 * colCatalystCrStatus — the Catalyst control-plane CRs (Application /
 * Environment / Organization / Continuum / UserAccess) expose a
 * `status.phase` and/or a Ready condition. Prefer the Ready condition;
 * fall back to phase. Recon vocabulary.
 */
export function colCatalystCrStatus(header = 'Status'): K8sListColumn {
  return {
    header,
    extract: (o) => {
      const c = findCondition(o, 'Ready')
      if (c && c.status != null) {
        if (c.status === 'True') return 'Reconciled'
        const reason = (c.reason ?? '').toLowerCase()
        if (reason.includes('degraded') || reason.includes('error'))
          return 'Degraded'
        return 'Reconciling'
      }
      const phase = (o.status as Record<string, unknown> | undefined)?.[
        'phase'
      ] as string | undefined
      if (!phase) return 'Reconciling'
      const p = phase.toLowerCase()
      if (p.includes('ready') || p.includes('reconciled') || p.includes('active'))
        return 'Reconciled'
      if (p.includes('fail') || p.includes('error') || p.includes('degraded'))
        return 'Degraded'
      return 'Reconciling'
    },
  }
}

/* ── Page ───────────────────────────────────────────────────────── */

export function K8sListPage({
  kind,
  title,
  tagline,
  columns,
  sortByName = true,
}: K8sListPageProps) {
  // Read from the page-level shared snapshot owned by CloudPage.
  // ONE EventSource per page (subscribing to all kinds) feeds every
  // chip count, every list page, and the architecture graph — the
  // alternative (per-page subscriptions) starved under the HTTP/1.1
  // 6-connections-per-origin limit because each open SSE stream
  // holds a connection slot for its lifetime.
  //
  // The snapshot Map is mutated in-place by useK8sCacheStream so its
  // reference is STABLE across deltas — listing `k8sSnapshot` alone in
  // the useMemo deps would never recompute past the first render. The
  // `k8sRevision` counter bumps on every applied event, so adding it to
  // the deps is what actually makes the list re-derive when new objects
  // arrive over SSE. Without this, services / ingresses / deployments /
  // statefulsets / daemonsets / namespaces / nodes all rendered "No X
  // objects" while the graph view (which keeps its own revision-keyed
  // memo) showed full counts.
  const { k8sSnapshot, k8sStatus, k8sRevision, deploymentId } = useCloud()
  const cloudBasePath =
    DETECTED_MODE.mode === 'sovereign' || !deploymentId
      ? '/cloud'
      : `/provision/${deploymentId}/cloud`

  const rows = useMemo(() => {
    const out: K8sObject[] = []
    if (!k8sSnapshot) return out
    for (const [key, obj] of k8sSnapshot.entries()) {
      // Snapshot keys are `${kind}:${ns}/${name}` or `${kind}:${name}`
      // for cluster-scoped. Filter to the requested kind only.
      if (!key.startsWith(`${kind}:`)) continue
      out.push(obj as K8sObject)
    }
    if (sortByName) {
      out.sort((a, b) => {
        const na = a.metadata?.namespace ?? ''
        const nb = b.metadata?.namespace ?? ''
        if (na !== nb) return na.localeCompare(nb)
        return (a.metadata?.name ?? '').localeCompare(b.metadata?.name ?? '')
      })
    }
    return out
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [k8sSnapshot, k8sRevision, kind, sortByName])

  return (
    <div data-testid={`cloud-${kind}-list`}>
      <div className="mb-4">
        <h2 className="text-lg font-semibold text-[var(--color-text-strong)]">{title}</h2>
        <p className="text-sm text-[var(--color-text-dim)]">{tagline}</p>
      </div>
      {k8sStatus ==='connecting' && rows.length === 0 ? (
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
          Connecting to live cluster stream…
        </div>
      ) : k8sStatus ==='error' && rows.length === 0 ? (
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
          Stream temporarily unreachable; reconnecting automatically.
        </div>
      ) : rows.length === 0 ? (
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
          No <code className="font-mono">{kind}</code> objects in this cluster.
        </div>
      ) : (
        <div className="overflow-x-auto rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)]">
          <table className="w-full border-collapse text-sm" data-testid={`cloud-${kind}-table`}>
            <thead className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
              <tr className="border-b border-[var(--color-border)]">
                {columns.map((c) => (
                  <th key={c.header} className="px-3 py-2 text-left font-medium">
                    {c.header}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((obj, i) => {
                const id = obj.metadata?.uid ?? `${obj.metadata?.namespace}/${obj.metadata?.name}/${i}`
                const name = obj.metadata?.name ?? ''
                const ns = obj.metadata?.namespace ?? ''
                const href = name
                  ? resourceDetailHref(cloudBasePath, kind, ns || undefined, name)
                  : null
                const onRowClick = () => {
                  if (!href || typeof window === 'undefined') return
                  window.location.assign(href)
                }
                return (
                  <tr
                    key={id}
                    className={
                      'border-b border-[var(--color-border)] last:border-0 ' +
                      (href ? 'cursor-pointer hover:bg-[var(--color-bg-3)]' : '')
                    }
                    data-testid={`cloud-${kind}-row-${name || i}`}
                    onClick={onRowClick}
                    onKeyDown={(e) => {
                      if (!href) return
                      if (e.key === 'Enter' || e.key === ' ') {
                        e.preventDefault()
                        onRowClick()
                      }
                    }}
                    role={href ? 'link' : undefined}
                    tabIndex={href ? 0 : undefined}
                  >
                    {columns.map((c) => (
                      <td key={c.header} className="px-3 py-2 text-[var(--color-text)]">
                        {c.extract(obj)}
                      </td>
                    ))}
                  </tr>
                )
              })}
            </tbody>
          </table>
          <div className="px-3 py-2 text-xs text-[var(--color-text-dim)]">
            {rows.length} {rows.length === 1 ? 'item' : 'items'}
          </div>
        </div>
      )}
    </div>
  )
}
