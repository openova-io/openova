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
import { CLOUD_K8S_TONE_CSS } from './cloudListCss'
import { REGION_COLUMN } from './k8sColumns'
import type { K8sListPageProps } from './k8sColumns'

// Re-export the column types + builders + tone classifiers (#4084) from
// their new home so existing importers of './K8sListPage' keep working;
// the definitions live in k8sColumns.ts to keep THIS file component-only
// (react-refresh/only-export-components).
export type { CellTone, K8sListColumn, K8sListPageProps } from './k8sColumns'

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
      // Snapshot keys are `${kind}:${ns}/${name}@${cluster}` (or
      // `${kind}:${name}@${cluster}` for cluster-scoped kinds) — the
      // region suffix is a SUFFIX precisely so this prefix filter is
      // unaffected by it (#5571).
      if (!key.startsWith(`${kind}:`)) continue
      out.push(obj as K8sObject)
    }
    if (sortByName) {
      out.sort((a, b) => {
        const na = a.metadata?.namespace ?? ''
        const nb = b.metadata?.namespace ?? ''
        if (na !== nb) return na.localeCompare(nb)
        const nameA = a.metadata?.name ?? ''
        const nameB = b.metadata?.name ?? ''
        if (nameA !== nameB) return nameA.localeCompare(nameB)
        // #5571: same ns/name in two regions — keep the ordering
        // deterministic so the two rows never swap between renders.
        return (a.clusterId ?? '').localeCompare(b.clusterId ?? '')
      })
    }
    return out
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [k8sSnapshot, k8sRevision, kind, sortByName])

  // #5571: append a Region column whenever the stream is attributing
  // objects to a cluster. Rendered even for a single region — an
  // operator must be able to see WHICH region a set came from, since
  // "only region-a is listed" and "the estate only has region-a" are
  // otherwise indistinguishable on a security-posture page.
  const effectiveColumns = useMemo(
    () => (rows.some((r) => r.clusterId) ? [...columns, REGION_COLUMN] : columns),
    [rows, columns],
  )

  return (
    <div data-testid={`cloud-${kind}-list`}>
      <style>{CLOUD_K8S_TONE_CSS}</style>
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
                {effectiveColumns.map((c) => (
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
                    data-region={obj.clusterId ?? ''}
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
                    {effectiveColumns.map((c) => {
                      const text = c.extract(obj)
                      const tone = c.tone?.(obj)
                      return (
                        <td key={c.header} className="px-3 py-2 text-[var(--color-text)]">
                          {tone ? (
                            // k9s-style status chip (#4084). The text stays
                            // inside the chip so colour is never the only
                            // signal (accessible to colour-blind operators).
                            <span
                              className="cloud-k8s-tone"
                              data-tone={tone}
                              data-testid={`cloud-${kind}-cell-tone`}
                            >
                              {text}
                            </span>
                          ) : (
                            text
                          )}
                        </td>
                      )
                    })}
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
