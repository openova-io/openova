/**
 * ResourceDetailPage — EPIC-4 Slice R1 (#1099) — drill-down detail view
 * for any K8s kind in the cloud-list registry.
 *
 * Routes mounted in router.tsx:
 *
 *   /provision/$deploymentId/cloud/resource/$kind/$ns/$name/$tab
 *   /cloud/resource/$kind/$ns/$name/$tab            (chroot)
 *
 * `$ns` may be the literal `_` for cluster-scoped resources (chi can't
 * route empty segments, mirrored on the server side in
 * k8s_resource_get.go).
 *
 * Tabs (target-state shape per INVIOLABLE-PRINCIPLES.md #1):
 *   Overview / YAML / Logs / Exec / Events / Metrics / Tree
 *
 * Logs + Exec render an "embedded via slice X2/E" placeholder pending
 * those slices — but the tab nav is fully functional so the tab set is
 * shipped at first cut.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #3 (event-driven) — Events come off the page-level k8s SSE; the
 *      single resource fetch is a one-shot REST call, not a poll loop.
 *   #4 (never hardcode) — every URL via resource.api.ts.
 */

import { useEffect, useMemo, useState } from 'react'

import { ResourceActions } from '@/widgets/cloud-list/ResourceActions'
import { ResourceTree } from '@/widgets/cloud-list/ResourceTree'
import { YamlEditor } from '@/widgets/cloud-list/YamlEditor'
import { EventsPanel } from '@/widgets/cloud-list/EventsPanel'
import { MetricsPanel } from '@/widgets/cloud-list/MetricsPanel'
import { LogViewer } from '@/widgets/cloud-list/LogViewer'
import { ExecPanel } from '@/widgets/cloud-list/ExecPanel'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'
import {
  RESOURCE_DETAIL_TABS,
  getResource,
  getResourceTree,
  resourceDetailHref,
  type ResourceDetailTab,
  type ResourceTreeNode,
} from './resource.api'

export interface ResourceDetailPageProps {
  /** Deployment id for API calls. */
  deploymentId: string
  /** Cloud-list base path used for nav links. */
  basePath: string
  /** Canonical kind id (matches k8scache Registry, e.g. "pod"). */
  kind: string
  /** Namespace ('' for cluster-scoped). */
  ns: string
  /** Resource name. */
  name: string
  /** Active tab (URL-driven). */
  tab: ResourceDetailTab
  /** Cluster snapshot for Events panel filtering. */
  k8sSnapshot?: ReadonlyMap<string, unknown> | null
  /** Whether the operator is tier-admin or higher (mirrored from
   *  whoami / RBAC). UI hides mutation buttons when false; server is
   *  the authoritative gate. */
  isTierAdmin?: boolean
  /** Test seam — substitute the live fetch. */
  initialObj?: K8sObject
  initialTree?: ResourceTreeNode
  /** Test seam — bypass navigation when a tab is clicked. */
  onTabChange?: (next: ResourceDetailTab) => void
}

export function ResourceDetailPage(props: ResourceDetailPageProps) {
  const {
    deploymentId,
    basePath,
    kind,
    ns,
    name,
    tab,
    k8sSnapshot,
    isTierAdmin = true,
    initialObj,
    initialTree,
    onTabChange,
  } = props

  const [obj, setObj] = useState<K8sObject | null>(initialObj ?? null)
  const [objErr, setObjErr] = useState<string | null>(null)
  const [tree, setTree] = useState<ResourceTreeNode | null>(initialTree ?? null)
  const [treeErr, setTreeErr] = useState<string | null>(null)
  const [isLoading, setIsLoading] = useState<boolean>(!initialObj)

  useEffect(() => {
    if (initialObj) return
    let cancelled = false
    const ac = new AbortController()
    getResource(deploymentId, kind, ns, name, ac.signal)
      .then((o) => {
        if (cancelled) return
        setObj(o)
        setObjErr(null)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setObjErr(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!cancelled) setIsLoading(false)
      })
    return () => {
      cancelled = true
      ac.abort()
    }
  }, [deploymentId, kind, ns, name, initialObj])

  useEffect(() => {
    if (initialTree || tab !== 'tree') return
    let cancelled = false
    const ac = new AbortController()
    getResourceTree(deploymentId, kind, ns, name, ac.signal)
      .then((t) => {
        if (cancelled) return
        setTree(t)
        setTreeErr(null)
      })
      .catch((err: unknown) => {
        if (cancelled) return
        setTreeErr(err instanceof Error ? err.message : String(err))
      })
    return () => {
      cancelled = true
      ac.abort()
    }
  }, [deploymentId, kind, ns, name, tab, initialTree])

  const allEvents = useMemo<K8sObject[]>(() => {
    if (!k8sSnapshot) return []
    const out: K8sObject[] = []
    for (const [key, value] of k8sSnapshot.entries() as IterableIterator<[string, K8sObject]>) {
      if (key.startsWith('event:')) out.push(value as K8sObject)
    }
    return out
  }, [k8sSnapshot])

  const replicas = useMemo<number | undefined>(() => {
    const r = (obj?.spec as { replicas?: number } | undefined)?.replicas
    return typeof r === 'number' ? r : undefined
  }, [obj])

  function handleTabClick(next: ResourceDetailTab) {
    if (onTabChange) {
      onTabChange(next)
      return
    }
    if (typeof window !== 'undefined') {
      window.location.assign(resourceDetailHref(basePath, kind, ns || undefined, name, next))
    }
  }

  return (
    <div data-testid={`resource-detail-${kind}-${name}`} className="space-y-4">
      <header className="space-y-1">
        <h2 className="text-lg font-semibold text-[var(--color-text-strong)]">
          {kind}/{name}
        </h2>
        <p className="text-sm text-[var(--color-text-dim)]">
          {ns ? `Namespace: ${ns}` : 'Cluster-scoped'} ·{' '}
          {obj?.metadata?.creationTimestamp ? `Created ${obj.metadata.creationTimestamp}` : '—'}
        </p>
      </header>

      <div role="tablist" aria-label="Resource detail tabs" className="flex flex-wrap gap-1 border-b border-[var(--color-border)]">
        {RESOURCE_DETAIL_TABS.map((t) => {
          const active = t === tab
          return (
            <button
              key={t}
              type="button"
              role="tab"
              aria-selected={active}
              data-testid={`resource-detail-tab-${t}`}
              onClick={() => handleTabClick(t)}
              className={
                'rounded-t border border-b-0 px-3 py-1.5 text-sm font-medium transition-colors ' +
                (active
                  ? 'border-[var(--color-border)] bg-[var(--color-bg-2)] text-[var(--color-text-strong)]'
                  : 'border-transparent text-[var(--color-text-dim)] hover:text-[var(--color-text)]')
              }
            >
              {tabLabel(t)}
            </button>
          )
        })}
      </div>

      {isLoading && (
        <div data-testid="resource-detail-loading" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
          Loading {kind}/{name}…
        </div>
      )}
      {objErr && (
        <div data-testid="resource-detail-error" className="rounded-lg border border-rose-500 bg-[var(--color-bg-2)] p-6 text-sm text-rose-300">
          {objErr}
        </div>
      )}

      {!isLoading && !objErr && (
        <div data-testid={`resource-detail-tab-content-${tab}`}>
          {tab === 'overview' && <OverviewTab obj={obj} replicas={replicas} kind={kind} ns={ns} name={name} deploymentId={deploymentId} isTierAdmin={isTierAdmin} />}
          {tab === 'yaml' && <YamlEditor deploymentId={deploymentId} kind={kind} ns={ns || undefined} name={name} obj={obj} />}
          {tab === 'logs' && (
            <LogsTabContent
              kind={kind}
              deploymentId={deploymentId}
              ns={ns}
              name={name}
              obj={obj}
            />
          )}
          {tab === 'exec' && (
            <ExecTabContent
              kind={kind}
              deploymentId={deploymentId}
              ns={ns}
              name={name}
              obj={obj}
              canExec={isTierAdmin}
            />
          )}
          {tab === 'events' && (
            <EventsPanel allEvents={allEvents} ns={ns} name={name} kindCanonical={kind} />
          )}
          {tab === 'metrics' && (
            <MetricsPanel deploymentId={deploymentId} kind={kind} ns={ns || undefined} name={name} />
          )}
          {tab === 'tree' && (
            <ResourceTree basePath={basePath} tree={tree} isError={!!treeErr} isLoading={!tree && !treeErr} />
          )}
        </div>
      )}
    </div>
  )
}

interface OverviewTabProps {
  obj: K8sObject | null
  replicas?: number
  kind: string
  ns: string
  name: string
  deploymentId: string
  isTierAdmin: boolean
}

function OverviewTab({ obj, replicas, kind, ns, name, deploymentId, isTierAdmin }: OverviewTabProps) {
  if (!obj) {
    return (
      <div data-testid="resource-detail-overview-empty" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
        No data.
      </div>
    )
  }
  const labels = obj.metadata?.labels ?? {}
  const owners = obj.metadata?.ownerReferences ?? []
  const phase = (obj.status as { phase?: string } | undefined)?.phase
  return (
    <div data-testid="resource-detail-overview" className="space-y-4">
      <div className="grid grid-cols-1 gap-4 md:grid-cols-2">
        <KV label="Phase" value={phase ?? '—'} />
        <KV label="Replicas" value={replicas == null ? '—' : String(replicas)} />
        <KV label="Owners" value={owners.length === 0 ? 'None' : owners.map((o) => `${o.kind}/${o.name}`).join(', ')} />
        <KV label="Labels" value={Object.keys(labels).length === 0 ? '—' : Object.entries(labels).map(([k, v]) => `${k}=${v}`).join(', ')} />
      </div>
      <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
        <div className="mb-2 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">Actions</div>
        <ResourceActions
          deploymentId={deploymentId}
          kind={kind}
          ns={ns || undefined}
          name={name}
          currentReplicas={replicas}
          disabled={!isTierAdmin}
        />
      </div>
    </div>
  )
}

function KV({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3">
      <div className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">{label}</div>
      <div className="mt-1 break-words font-mono text-sm text-[var(--color-text)]">{value}</div>
    </div>
  )
}

function PlaceholderTab({ note, testId }: { note: string; testId: string }) {
  return (
    <div data-testid={testId} className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
      {note}
    </div>
  )
}

interface LogsTabProps {
  kind: string
  deploymentId: string
  ns: string
  name: string
  obj: K8sObject | null
}

function LogsTabContent({ kind, deploymentId, ns, name, obj }: LogsTabProps) {
  // Logs only meaningful for Pods (per kubelet API). For other kinds we
  // surface a hint pointing the operator at the owned-Pod tree-view.
  if (kind !== 'pod') {
    return (
      <PlaceholderTab
        testId="resource-detail-logs-not-pod"
        note={
          'Logs are streamed per-Pod. Drill into the Tree tab and pick a child Pod to see logs.'
        }
      />
    )
  }
  return <LogViewer deploymentId={deploymentId} ns={ns} pod={name} obj={obj} />
}

interface ExecTabProps {
  kind: string
  deploymentId: string
  ns: string
  name: string
  obj: K8sObject | null
  canExec: boolean
}

function ExecTabContent({ kind, deploymentId, ns, name, obj, canExec }: ExecTabProps) {
  if (kind !== 'pod') {
    return (
      <PlaceholderTab
        testId="resource-detail-exec-not-pod"
        note={
          'Exec is per-Pod. Drill into the Tree tab and pick a child Pod to open a shell.'
        }
      />
    )
  }
  const containers = (obj?.spec as { containers?: { name?: string }[] } | undefined)?.containers ?? []
  const container = containers[0]?.name ?? 'main'
  return (
    <ExecPanel
      deploymentId={deploymentId}
      ns={ns}
      pod={name}
      container={container}
      canExec={canExec}
    />
  )
}

function tabLabel(tab: ResourceDetailTab): string {
  switch (tab) {
    case 'overview':
      return 'Overview'
    case 'yaml':
      return 'YAML'
    case 'logs':
      return 'Logs'
    case 'exec':
      return 'Exec'
    case 'events':
      return 'Events'
    case 'metrics':
      return 'Metrics'
    case 'tree':
      return 'Tree'
    default:
      return tab
  }
}

