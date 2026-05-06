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
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { useK8sCacheStream, type K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

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

/* ── Page ───────────────────────────────────────────────────────── */

export function K8sListPage({
  kind,
  title,
  tagline,
  columns,
  sortByName = true,
}: K8sListPageProps) {
  const { deploymentId } = useResolvedDeploymentId()
  const { snapshot, status } = useK8sCacheStream(deploymentId ?? '', {
    enabled: !!deploymentId,
    kinds: [kind],
  })

  const rows = useMemo(() => {
    const out: K8sObject[] = []
    for (const [key, obj] of snapshot.entries()) {
      // Snapshot keys are `${kind}:${ns}/${name}` or `${kind}:${name}`
      // for cluster-scoped. Filter to the requested kind only — the
      // shared cache may carry other kinds when this page is mounted
      // alongside the graph (which subscribes to a wider set).
      if (!key.startsWith(`${kind}:`)) continue
      out.push(obj)
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
  }, [snapshot, kind, sortByName])

  return (
    <div data-testid={`cloud-${kind}-list`}>
      <div className="mb-4">
        <h2 className="text-lg font-semibold text-[var(--color-text-strong)]">{title}</h2>
        <p className="text-sm text-[var(--color-text-dim)]">{tagline}</p>
      </div>
      {status === 'connecting' && rows.length === 0 ? (
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
          Connecting to live cluster stream…
        </div>
      ) : status === 'error' && rows.length === 0 ? (
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
                return (
                  <tr
                    key={id}
                    className="border-b border-[var(--color-border)] last:border-0"
                    data-testid={`cloud-${kind}-row-${obj.metadata?.name ?? i}`}
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
