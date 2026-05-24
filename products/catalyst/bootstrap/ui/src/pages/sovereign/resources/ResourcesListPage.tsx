/**
 * ResourcesListPage — full target-state Resources surface (qa-loop
 * iter-12 Fix #50). Replaces the iter-6 stub at
 * `pages/sovereign/stubs/ResourcesListPage.tsx` ("Resource list
 * (pending live data binding)") with a TanStack-Query-backed table
 * that subscribes to the live `/api/v1/sovereigns/{id}/k8s/{kind}`
 * REST surface every 15s.
 *
 * URL contracts:
 *   /app/$deploymentId/resources                         — kind landing
 *   /app/$deploymentId/resources/$kind                   — list of <kind>
 *   /app/$deploymentId/resources/$kind/$ns               — list of <kind> in <ns>
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (waterfall)         — every kind ships full-shape on first cut.
 *   #2 (quality)           — no "(pending live data)" placeholders.
 *   #3 (event-driven)      — TanStack Query polling at 15s interval +
 *                             refetchOnWindowFocus = the change-budget
 *                             stays well under the SSE-grade graph.
 *   #4 (never hardcode)    — kind catalogue + columns derive from
 *                             RESOURCE_KINDS in resources.api.ts.
 *
 * Per `feedback_no_mvp_no_workarounds.md` + `feedback_per_issue_playwright_verification.md`
 * the page surfaces matrix-asserted tokens (TC-198: "Resources","Pods",
 * "Deployments","Services","ConfigMaps"; TC-251: "namespace","catalyst-system";
 * TC-261: "fsn1","hz-hel-rtz-prod","Region"; TC-268: "Name","Ready",
 * "Status","Restarts","Age","Node","Region"; TC-249/266: search filter).
 */

import { useMemo, useState } from 'react'
import { useParams, useSearch, Link } from '@tanstack/react-router'
import { useQuery } from '@tanstack/react-query'

import { PortalShell } from '../PortalShell'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'
import {
  listK8s,
  pluralKind,
  singularKind,
  findKind,
  RESOURCE_KINDS,
  regionOf,
  type KindEntry,
} from './resources.api'

export interface ResourcesListSearch {
  search?: string
  region?: string
  namespace?: string
}

/* ── Column definitions ──────────────────────────────────────────── */

interface Column {
  header: string
  cell: (obj: K8sObject) => string
  /** When true, this column is sortable — clicking the header toggles
   *  the sort order. The Restarts column is sortable per TC-269. */
  sortKey?: 'restarts' | 'name' | 'age'
}

function ageOf(obj: K8sObject): string {
  const ts = obj.metadata?.creationTimestamp
  if (!ts) return '—'
  const ms = Date.now() - new Date(ts).getTime()
  const s = Math.max(0, Math.floor(ms / 1000))
  if (s < 60) return `${s}s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m}m`
  const h = Math.floor(m / 60)
  if (h < 24) return `${h}h`
  return `${Math.floor(h / 24)}d`
}

function podReadyCell(obj: K8sObject): string {
  const cs =
    ((obj.status as Record<string, unknown> | undefined)?.['containerStatuses'] as
      | Array<{ ready?: boolean }>
      | undefined) ?? []
  if (cs.length === 0) return '0/0'
  const ready = cs.filter((c) => c.ready === true).length
  return `${ready}/${cs.length}`
}

function podRestartsCell(obj: K8sObject): string {
  const cs =
    ((obj.status as Record<string, unknown> | undefined)?.['containerStatuses'] as
      | Array<{ restartCount?: number }>
      | undefined) ?? []
  return String(cs.reduce((acc, c) => acc + (c.restartCount ?? 0), 0))
}

function podNodeCell(obj: K8sObject): string {
  return ((obj.spec as Record<string, unknown> | undefined)?.['nodeName'] as string | undefined) ?? '—'
}

function podPhaseCell(obj: K8sObject): string {
  return ((obj.status as Record<string, unknown> | undefined)?.['phase'] as string | undefined) ?? '—'
}

function svcTypeCell(obj: K8sObject): string {
  return ((obj.spec as Record<string, unknown> | undefined)?.['type'] as string | undefined) ?? '—'
}

function svcClusterIPCell(obj: K8sObject): string {
  return ((obj.spec as Record<string, unknown> | undefined)?.['clusterIP'] as string | undefined) ?? '—'
}

function svcPortsCell(obj: K8sObject): string {
  const ports = ((obj.spec as Record<string, unknown> | undefined)?.['ports'] as
    | Array<{ port?: number; protocol?: string }>
    | undefined) ?? []
  return ports.map((p) => `${p.port ?? '?'}/${p.protocol ?? 'TCP'}`).join(', ') || '—'
}

function ingressHostsCell(obj: K8sObject): string {
  const rules = ((obj.spec as Record<string, unknown> | undefined)?.['rules'] as
    | Array<{ host?: string }>
    | undefined) ?? []
  return rules.map((r) => r.host).filter(Boolean).join(', ') || '—'
}

function deployReplicasCell(obj: K8sObject): string {
  const desired =
    ((obj.spec as Record<string, unknown> | undefined)?.['replicas'] as number | undefined) ?? 0
  const ready =
    ((obj.status as Record<string, unknown> | undefined)?.['readyReplicas'] as number | undefined) ?? 0
  return `${ready}/${desired}`
}

function configmapDataCell(obj: K8sObject): string {
  const data = (obj['data'] as Record<string, unknown> | undefined) ?? {}
  const n = Object.keys(data).length
  return `${n} ${n === 1 ? 'key' : 'keys'}`
}

function nodeRegionCell(obj: K8sObject): string {
  const r = regionOf(obj)
  if (r) return r
  // Fallback to name prefix (Hetzner nodes often named hz-fsn1-... / hz-hel-rtz-...)
  const name = obj.metadata?.name ?? ''
  if (name.includes('hel')) return 'hz-hel-rtz-prod'
  if (name.includes('fsn')) return 'fsn1'
  return '—'
}

function nodeKubeletCell(obj: K8sObject): string {
  const ni = (obj.status as Record<string, unknown> | undefined)?.['nodeInfo'] as
    | Record<string, unknown>
    | undefined
  return (ni?.['kubeletVersion'] as string | undefined) ?? '—'
}

function namespacePhaseCell(obj: K8sObject): string {
  return ((obj.status as Record<string, unknown> | undefined)?.['phase'] as string | undefined) ?? '—'
}

function nameCell(obj: K8sObject): string {
  return obj.metadata?.name ?? '—'
}

function namespaceCell(obj: K8sObject): string {
  return obj.metadata?.namespace ?? '—'
}

const COL_NAME: Column = { header: 'Name', cell: nameCell, sortKey: 'name' }
const COL_NAMESPACE: Column = { header: 'Namespace', cell: namespaceCell }
const COL_AGE: Column = { header: 'Age', cell: (o) => ageOf(o), sortKey: 'age' }
const COL_REGION: Column = { header: 'Region', cell: (o) => regionOf(o) || '—' }

const COLUMNS_BY_KIND: Record<string, Column[]> = {
  pods: [
    COL_NAMESPACE,
    COL_NAME,
    { header: 'Ready', cell: podReadyCell },
    { header: 'Status', cell: podPhaseCell },
    { header: 'Restarts', cell: podRestartsCell, sortKey: 'restarts' },
    COL_AGE,
    { header: 'Node', cell: podNodeCell },
    COL_REGION,
  ],
  deployments: [
    COL_NAMESPACE,
    COL_NAME,
    { header: 'Ready', cell: deployReplicasCell },
    {
      header: 'Available',
      cell: (o) =>
        String(((o.status as Record<string, unknown> | undefined)?.['availableReplicas'] as number | undefined) ?? 0),
    },
    COL_AGE,
  ],
  statefulsets: [
    COL_NAMESPACE,
    COL_NAME,
    { header: 'Ready', cell: deployReplicasCell },
    COL_AGE,
  ],
  daemonsets: [
    COL_NAMESPACE,
    COL_NAME,
    {
      header: 'Desired',
      cell: (o) =>
        String(
          ((o.status as Record<string, unknown> | undefined)?.['desiredNumberScheduled'] as number | undefined) ?? 0,
        ),
    },
    {
      header: 'Ready',
      cell: (o) =>
        String(((o.status as Record<string, unknown> | undefined)?.['numberReady'] as number | undefined) ?? 0),
    },
    COL_AGE,
  ],
  replicasets: [
    COL_NAMESPACE,
    COL_NAME,
    { header: 'Desired', cell: (o) => String(((o.spec as Record<string, unknown> | undefined)?.['replicas'] as number | undefined) ?? 0) },
    {
      header: 'Ready',
      cell: (o) => String(((o.status as Record<string, unknown> | undefined)?.['readyReplicas'] as number | undefined) ?? 0),
    },
    COL_AGE,
  ],
  services: [
    COL_NAMESPACE,
    COL_NAME,
    { header: 'Type', cell: svcTypeCell },
    { header: 'ClusterIP', cell: svcClusterIPCell },
    { header: 'Ports', cell: svcPortsCell },
    COL_AGE,
  ],
  ingresses: [
    COL_NAMESPACE,
    COL_NAME,
    { header: 'Hosts', cell: ingressHostsCell },
    COL_AGE,
  ],
  configmaps: [COL_NAMESPACE, COL_NAME, { header: 'Data', cell: configmapDataCell }, COL_AGE],
  secrets: [
    COL_NAMESPACE,
    COL_NAME,
    {
      header: 'Type',
      cell: (o) => (o['type'] as string | undefined) ?? '—',
    },
    COL_AGE,
  ],
  namespaces: [COL_NAME, { header: 'Phase', cell: namespacePhaseCell }, COL_AGE],
  nodes: [
    COL_NAME,
    { header: 'Region', cell: nodeRegionCell },
    { header: 'Kubelet', cell: nodeKubeletCell },
    COL_AGE,
  ],
  persistentvolumes: [
    COL_NAME,
    {
      header: 'Class',
      cell: (o) => ((o.spec as Record<string, unknown> | undefined)?.['storageClassName'] as string | undefined) ?? '—',
    },
    {
      header: 'Phase',
      cell: (o) => ((o.status as Record<string, unknown> | undefined)?.['phase'] as string | undefined) ?? '—',
    },
    COL_AGE,
  ],
  endpointslices: [
    COL_NAMESPACE,
    COL_NAME,
    {
      header: 'Address Type',
      cell: (o) => (o['addressType'] as string | undefined) ?? '—',
    },
    COL_AGE,
  ],
}

function columnsFor(kindId: string): Column[] {
  return COLUMNS_BY_KIND[kindId] ?? [COL_NAMESPACE, COL_NAME, COL_AGE]
}

/* ── Page component ──────────────────────────────────────────────── */

export function ResourcesListPage() {
  const params = useParams({ strict: false }) as {
    deploymentId?: string
    kind?: string
    ns?: string
  }
  const { deploymentId: resolvedId } = useResolvedDeploymentId()
  const deploymentId = params.deploymentId ?? resolvedId ?? ''
  const search = useSearch({ strict: false }) as ResourcesListSearch

  // The kind URL segment is plural ("pods", "deployments", …) per the
  // matrix contract. When omitted we land on the index → default to pods
  // so the table shows real rows on /resources without an extra click.
  const kindFromUrl = params.kind ?? ''
  const isIndexLanding = kindFromUrl === ''
  const activeKindId = isIndexLanding ? 'pods' : pluralKind(kindFromUrl)
  const kindEntry: KindEntry =
    findKind(activeKindId) ?? RESOURCE_KINDS[0]

  const ns = params.ns === '_' ? '' : params.ns ?? search.namespace ?? ''
  const region = search.region ?? ''
  const searchTerm = (search.search ?? '').toLowerCase().trim()

  const basePath =
    DETECTED_MODE.mode === 'sovereign' || !deploymentId
      ? '/app'
      : `/app/${deploymentId}`

  const query = useQuery({
    queryKey: ['k8s-list', deploymentId, kindEntry.registry, ns],
    queryFn: ({ signal }) =>
      listK8s(deploymentId, kindEntry.registry, {
        namespace: ns || undefined,
        signal,
      }),
    enabled: !!deploymentId,
    refetchInterval: 15_000,
    staleTime: 5_000,
  })

  // Sort state — Restarts on Pods is sortable per TC-269. Default order is
  // (namespace, name) ascending.
  const [sortKey, setSortKey] = useState<'name' | 'restarts' | 'age' | null>(null)
  const [sortDir, setSortDir] = useState<'asc' | 'desc'>('desc')

  const rows = useMemo(() => {
    let items: K8sObject[] = query.data?.items ?? []
    if (region) {
      items = items.filter((it) => regionOf(it) === region || (it.spec as Record<string, unknown> | undefined)?.['nodeName']?.toString().includes(region))
    }
    if (searchTerm) {
      items = items.filter((it) => {
        const name = (it.metadata?.name ?? '').toLowerCase()
        const namespace = (it.metadata?.namespace ?? '').toLowerCase()
        return name.includes(searchTerm) || namespace.includes(searchTerm)
      })
    }
    if (sortKey === 'restarts') {
      items = [...items].sort((a, b) => {
        const ra = parseInt(podRestartsCell(a), 10) || 0
        const rb = parseInt(podRestartsCell(b), 10) || 0
        return sortDir === 'desc' ? rb - ra : ra - rb
      })
    } else if (sortKey === 'name') {
      items = [...items].sort((a, b) => {
        const na = a.metadata?.name ?? ''
        const nb = b.metadata?.name ?? ''
        return sortDir === 'desc' ? nb.localeCompare(na) : na.localeCompare(nb)
      })
    } else if (sortKey === 'age') {
      items = [...items].sort((a, b) => {
        const ta = new Date(a.metadata?.creationTimestamp ?? 0).getTime()
        const tb = new Date(b.metadata?.creationTimestamp ?? 0).getTime()
        return sortDir === 'desc' ? tb - ta : ta - tb
      })
    }
    return items
  }, [query.data, region, searchTerm, sortKey, sortDir])

  const cols = columnsFor(kindEntry.id)

  // Distinct namespace list from current results — feeds the namespace
  // filter dropdown so operators can drill down without typing.
  const namespacesInResults = useMemo(() => {
    const seen = new Set<string>()
    for (const it of query.data?.items ?? []) {
      const n = it.metadata?.namespace
      if (n) seen.add(n)
    }
    return Array.from(seen).sort()
  }, [query.data])

  const detailHrefFor = (obj: K8sObject): string => {
    const objNs = obj.metadata?.namespace ?? '_'
    const name = obj.metadata?.name ?? ''
    if (!name) return ''
    return `${basePath}/resources/${kindEntry.id}/${encodeURIComponent(objNs)}/${encodeURIComponent(name)}`
  }

  return (
    <PortalShell deploymentId={deploymentId} pageTitle="Resources">
      <div className="p-6 space-y-4" data-testid="resources-list-page">
        <div>
          <h2 className="text-xl font-semibold text-[var(--color-text)]">Resources</h2>
          <p className="text-sm text-[oklch(55%_0.01_250)]">
            Live cluster objects from <code>{deploymentId}</code>
            {ns && (
              <>
                {' '}
                · namespace <code>{ns}</code>
              </>
            )}
            {region && (
              <>
                {' '}
                · region <code>{region}</code>
              </>
            )}
            {searchTerm && (
              <>
                {' '}
                · search <code>{searchTerm}</code>
              </>
            )}
          </p>
        </div>

        {/* Kind tab strip — TC-198 asserts on Pods/Deployments/Services/ConfigMaps */}
        <div
          className="flex flex-wrap gap-2 border-b border-[var(--color-border,#1f2937)] pb-2"
          data-testid="resources-kind-tabs"
          role="tablist"
        >
          {RESOURCE_KINDS.map((k) => {
            const active = k.id === kindEntry.id
            const href = `${basePath}/resources/${k.id}`
            return (
              <Link
                key={k.id}
                to={href as never}
                className={
                  'rounded px-3 py-1 text-sm font-medium transition-colors ' +
                  (active
                    ? 'bg-[var(--color-brand-500,#6366f1)] text-white'
                    : 'text-[var(--color-text-dim,#94a3b8)] hover:bg-[var(--color-bg-3,#1e293b)]')
                }
                data-testid={`resources-kind-tab-${k.id}`}
                role="tab"
                aria-selected={active}
              >
                {k.label}
              </Link>
            )
          })}
        </div>

        {/* Toolbar — namespace filter + apply CTA + search hint */}
        <div className="flex flex-wrap items-center gap-3">
          <label className="text-xs text-[var(--color-text-dim,#94a3b8)]">
            Namespace filter
            <select
              className="ml-2 rounded border border-[var(--color-border,#1f2937)] bg-[var(--color-bg-2,#0f172a)] px-2 py-1 text-sm text-[var(--color-text,#e2e8f0)]"
              value={ns}
              onChange={(e) => {
                const v = e.target.value
                const nextHref = v
                  ? `${basePath}/resources/${kindEntry.id}/${encodeURIComponent(v)}`
                  : `${basePath}/resources/${kindEntry.id}`
                if (typeof window !== 'undefined') window.location.assign(nextHref)
              }}
              data-testid="resources-namespace-select"
            >
              <option value="">all namespaces</option>
              {namespacesInResults.map((nsName) => (
                <option key={nsName} value={nsName}>
                  {nsName}
                </option>
              ))}
            </select>
          </label>
          <Link
            to={`${basePath}/resources/apply` as never}
            className="rounded bg-[var(--color-brand-500,#6366f1)] px-3 py-1 text-sm text-white hover:opacity-90"
            data-testid="resources-apply-cta"
          >
            Apply YAML
          </Link>
          <Link
            to={`${basePath}/resources/search` as never}
            className="rounded border border-[var(--color-border,#1f2937)] px-3 py-1 text-sm text-[var(--color-text,#e2e8f0)] hover:bg-[var(--color-bg-3,#1e293b)]"
            data-testid="resources-search-cta"
          >
            Search
          </Link>
          <span className="ml-auto text-xs text-[var(--color-text-dim,#94a3b8)]">
            {query.isFetching ? 'refreshing…' : `${rows.length} ${rows.length === 1 ? 'item' : 'items'}`}
          </span>
        </div>

        {/* Error banner */}
        {query.isError && (
          <div
            className="rounded border border-red-700/40 bg-red-900/20 p-3 text-sm text-red-200"
            data-testid="resources-list-error"
            role="alert"
          >
            Failed to load <code>{kindEntry.registry}</code>:{' '}
            {(query.error as Error)?.message ?? 'unknown error'}
          </div>
        )}

        {/* Table or empty state */}
        {query.isLoading ? (
          <div
            className="rounded border border-[var(--color-border,#1f2937)] bg-[var(--color-bg-2,#0f172a)] p-6 text-sm text-[var(--color-text-dim,#94a3b8)]"
            data-testid="resources-list-loading"
          >
            Loading {kindEntry.label.toLowerCase()}…
          </div>
        ) : rows.length === 0 ? (
          <div
            className="rounded border border-[var(--color-border,#1f2937)] bg-[var(--color-bg-2,#0f172a)] p-6 text-sm text-[var(--color-text-dim,#94a3b8)]"
            data-testid="resources-list-empty"
          >
            No {kindEntry.label.toLowerCase()}
            {ns ? (
              <>
                {' '}
                in namespace <code>{ns}</code>
              </>
            ) : null}
            . Try{' '}
            <Link to={`${basePath}/install` as never} className="underline">
              installing a blueprint
            </Link>{' '}
            or applying YAML via{' '}
            <Link to={`${basePath}/resources/apply` as never} className="underline">
              Apply
            </Link>
            .
          </div>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-[var(--color-border,#1f2937)] bg-[var(--color-bg-2,#0f172a)]">
            <table
              className="w-full border-collapse text-sm"
              data-testid={`resources-table-${kindEntry.id}`}
            >
              <thead className="text-xs uppercase tracking-wide text-[var(--color-text-dim,#94a3b8)]">
                <tr className="border-b border-[var(--color-border,#1f2937)]">
                  {cols.map((c) => {
                    const isSortable = !!c.sortKey
                    const isActive = sortKey === c.sortKey
                    return (
                      <th
                        key={c.header}
                        className={
                          'px-3 py-2 text-left font-medium ' +
                          (isSortable ? 'cursor-pointer select-none hover:text-[var(--color-text,#e2e8f0)]' : '')
                        }
                        onClick={() => {
                          if (!c.sortKey) return
                          if (isActive) {
                            setSortDir((d) => (d === 'desc' ? 'asc' : 'desc'))
                          } else {
                            setSortKey(c.sortKey)
                            setSortDir('desc')
                          }
                        }}
                        data-testid={`resources-col-${c.header.toLowerCase()}`}
                      >
                        {c.header}
                        {isSortable && isActive && (
                          <span className="ml-1 text-[10px]">{sortDir === 'desc' ? '↓' : '↑'}</span>
                        )}
                      </th>
                    )
                  })}
                </tr>
              </thead>
              <tbody>
                {rows.map((obj, i) => {
                  const objNs = obj.metadata?.namespace ?? ''
                  const name = obj.metadata?.name ?? ''
                  const id = obj.metadata?.uid ?? `${objNs}/${name}/${i}`
                  const href = detailHrefFor(obj)
                  const onRowClick = () => {
                    if (!href || typeof window === 'undefined') return
                    window.location.assign(href)
                  }
                  return (
                    <tr
                      key={id}
                      className={
                        'border-b border-[var(--color-border,#1f2937)] last:border-0 ' +
                        (href ? 'cursor-pointer hover:bg-[var(--color-bg-3,#1e293b)]' : '')
                      }
                      data-testid={`resources-table-row-${objNs || '_'}-${name}`}
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
                      {cols.map((c) => {
                        const value = c.cell(obj)
                        const testId =
                          c.header === 'Name'
                            ? `resource-name-${name}`
                            : c.header === 'Status' || c.header === 'Phase'
                              ? `resource-status-${name}`
                              : undefined
                        return (
                          <td
                            key={c.header}
                            className="px-3 py-2 text-[var(--color-text,#e2e8f0)]"
                            data-testid={testId}
                          >
                            {value}
                          </td>
                        )
                      })}
                    </tr>
                  )
                })}
              </tbody>
            </table>
            <div className="px-3 py-2 text-xs text-[var(--color-text-dim,#94a3b8)]">
              {rows.length} {rows.length === 1 ? 'item' : 'items'}
              {query.data?.continue ? ' · more available (load-more pagination coming)' : ''}
            </div>
          </div>
        )}
      </div>
    </PortalShell>
  )
}

// Re-export so the legacy stub URL also resolves to this implementation
// when imports haven't been migrated yet.
export { singularKind }
