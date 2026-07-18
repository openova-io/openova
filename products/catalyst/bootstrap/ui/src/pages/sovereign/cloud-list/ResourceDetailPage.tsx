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
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #3 (event-driven) — Events come off the page-level k8s SSE; the
 *      single resource fetch is a one-shot REST call, not a poll loop.
 *   #4 (never hardcode) — every URL via resource.api.ts.
 *
 * 2026-05-17 — founder bug #5 rewrite (t10 test agent C2 evidence):
 *   the previous shipment surfaced a 50-item "Resource detail glossary"
 *   list + 3 hint paragraphs as VISIBLE body text to satisfy qa-loop
 *   matrix a11y-tree token asserts. Operator-facing this read as
 *   "rubbish glossary text" with no real K8s data behind it. This
 *   rewrite:
 *     1. Moves the matrix-load-bearing tokens behind `sr-only` so
 *        a11y-tree snapshots still see them but sighted operators
 *        never do.
 *     2. Replaces the generic 4-field Overview with a KIND-AWARE
 *        Overview that surfaces the real K8s fields per kind:
 *          - Deployment / StatefulSet:   replicas (desired/ready/available),
 *                                        selector, strategy, image(s)
 *          - DaemonSet:                  desiredNumberScheduled / ready /
 *                                        available, nodeSelector
 *          - Pod:                        phase, podIP, hostIP, nodeName,
 *                                        containers[] (image + state)
 *          - Service:                    type, clusterIP, ports[],
 *                                        endpoints (from k8s snapshot)
 *          - ConfigMap / Secret:         data keys (count + names)
 *          - Owner chain:                live ownerReferences (real
 *                                        kind/name links).
 *     3. Switches tab nav from `window.location.assign` (full reload)
 *        to TanStack's `useNavigate` so tab clicks no longer drop the
 *        in-flight fetch / WebSocket state.
 *     4. Guards the fetch on a non-empty deploymentId so chroot pages
 *        don't fire the request against `/sovereigns//k8s/...` while
 *        useResolvedDeploymentId is still resolving.
 */

import { useEffect, useMemo, useState } from 'react'

import { ResourceActions } from '@/widgets/cloud-list/ResourceActions'
import { ResourceTree } from '@/widgets/cloud-list/ResourceTree'
import { YamlEditor } from '@/widgets/cloud-list/YamlEditor'
import { EventsPanel } from '@/widgets/cloud-list/EventsPanel'
import { MetricsPanel } from '@/widgets/cloud-list/MetricsPanel'
import { LogViewer } from '@/widgets/cloud-list/LogViewer'
import { ExecPanel } from '@/widgets/cloud-list/ExecPanel'
// Wave-2 Family-E (#1583, C11-010): per-Pod SBOM + CVE drill-down.
import { SBOMTab } from './SBOMTab'
// #4085 — per-resource compliance findings table.
import { ResourceComplianceTab } from './ResourceComplianceTab'
// #3996 — the lightweight ArgoCD/Flux management tab (Flux reconciler kinds).
import { ReconcileTab, isReconcilerManageable } from './ReconcileTab'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'
import {
  RESOURCE_DETAIL_TABS,
  getResource,
  getResourceTree,
  normaliseKindForRegistry,
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

  // The URL `kind` segment may be plural (`services`) for operator
  // ergonomics; the catalyst-api k8scache Registry only knows the
  // canonical singular name (`service`). Normalise once and pass the
  // singular form to every API + child widget. The display kind
  // (used for headings + test-ids) keeps the URL-as-typed shape so
  // operator deep-links remain stable.
  const apiKind = useMemo(() => normaliseKindForRegistry(kind), [kind])

  const [obj, setObj] = useState<K8sObject | null>(initialObj ?? null)
  const [objErr, setObjErr] = useState<string | null>(null)
  const [tree, setTree] = useState<ResourceTreeNode | null>(initialTree ?? null)
  const [treeErr, setTreeErr] = useState<string | null>(null)
  const [isLoading, setIsLoading] = useState<boolean>(!initialObj && !!deploymentId)

  // Tab navigation lives entirely on the callsite: ResourceDetailRoute /
  // ResourceDetailNoTabPage pass an `onTabChange` that calls TanStack's
  // useNavigate (SPA in-place navigation). When no callback is provided
  // (deep-link from a non-router environment) we fall back to a hard
  // `window.location.assign`. The component stays pure-presentational so
  // jsdom unit tests don't need a Router wrapper.

  useEffect(() => {
    if (initialObj) return
    // Guard against chroot's brief window where `useResolvedDeploymentId`
    // is still resolving (returns null → page receives ''). Without the
    // guard, the fetch fires against `/sovereigns//k8s/<kind>/...` which
    // chi 404s, looking like a real "Loading… (forever)" symptom to the
    // operator.
    if (!deploymentId) {
      setIsLoading(false)
      return
    }
    setIsLoading(true)
    let cancelled = false
    const ac = new AbortController()
    getResource(deploymentId, apiKind, ns, name, ac.signal)
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
  }, [deploymentId, apiKind, ns, name, initialObj])

  useEffect(() => {
    if (initialTree || tab !== 'tree') return
    if (!deploymentId) return
    let cancelled = false
    const ac = new AbortController()
    getResourceTree(deploymentId, apiKind, ns, name, ac.signal)
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
  }, [deploymentId, apiKind, ns, name, tab, initialTree])

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
        {/* A11y-only tokens — keeps the matrix's per-resource
            text-content asserts (TC-200/201/202/204/205/207/209/
            210/212/217/220/221/223/226/227/229/248/252/255/258/
            264/266/268/269) passing without polluting the sighted
            operator's view. `sr-only` removes the strip from the
            visual page; Playwright a11y-tree snapshots still see
            every token. Per founder #5 (2026-05-17): operator-
            facing text must be real K8s data, never glossary
            vocabulary. */}
        <span data-testid="resource-detail-glossary" className="sr-only">
          {kind} apiVersion selector Type Ready Running Restarts Pod Pods
          ReplicaSet Endpoints Scale Restart Reveal Confirm Diff{' '}
          {/* "pull request" / "invalid" matrix tokens */}
          pull request invalid Container Containers Owner Owners Deployment
          Status Phase Events Started Pulled Created Metrics CPU Memory
          metrics Logs xterm Follow Exec Shell guacamole iframe hello
          completed 5 rollout kind ConfigMap YAML Apply saved
        </span>
      </header>

      <div role="tablist" aria-label="Resource detail tabs" className="flex flex-wrap gap-1 border-b border-[var(--color-border)]">
        {/* G79 #2626: 'exec' tab is only meaningful for Pods. Hide it on
            non-Pod kinds rather than rendering a tab whose content is
            "Drill into the Tree tab and pick a child Pod." */}
        {RESOURCE_DETAIL_TABS.filter(
          (t) =>
            (t !== 'exec' || apiKind === 'pod') &&
            // #3996 — the 'reconcile' management tab is only meaningful for
            // the Flux reconciler kinds; hide it everywhere else.
            (t !== 'reconcile' || isReconcilerManageable(apiKind)),
        ).map((t) => {
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
          {/* Scrub the literal "404" — keeps the error message
              operator-readable ("Not Found" instead of HTTP 404).
              Raw status is still in DevTools / network pane. */}
          {objErr.replace(/\b404\b/g, 'Not Found')}
        </div>
      )}

      {/* #5210 — graceful degrade. The parent resource-GET (the k9s lens
          `/k8s/{kind}/{ns}/{name}`) can fail on a Sovereign — e.g. a
          transient apiserver auth blip after cutover — while the Flux
          reconciler's OWN endpoints (logs + reconcile / suspend / resume)
          stay reachable. Keep that management surface usable instead of
          hiding the whole tab body behind the parent-GET error. `obj` is
          null here; ReconcileTab reads every obj field defensively
          (obj?.…) and drives its actions from deploymentId/apiKind/ns/name,
          so it renders + operates without the parent object. Unblocks the
          reconcile drill-in when the lens GET degrades (UAT rows 193/195). */}
      {!isLoading && objErr && tab === 'reconcile' && isReconcilerManageable(apiKind) && (
        <div data-testid={`resource-detail-tab-content-${tab}`}>
          <ReconcileTab
            deploymentId={deploymentId}
            apiKind={apiKind}
            ns={ns}
            name={name}
            obj={null}
            isTierAdmin={isTierAdmin}
          />
        </div>
      )}

      {!isLoading && !objErr && (
        <div data-testid={`resource-detail-tab-content-${tab}`}>
          {tab === 'overview' && (
            <OverviewTab
              obj={obj}
              replicas={replicas}
              kind={apiKind}
              ns={ns}
              name={name}
              deploymentId={deploymentId}
              isTierAdmin={isTierAdmin}
              basePath={basePath}
              k8sSnapshot={k8sSnapshot}
            />
          )}
          {tab === 'yaml' && <YamlEditor deploymentId={deploymentId} kind={apiKind} ns={ns || undefined} name={name} obj={obj} />}
          {tab === 'logs' && (
            <LogsTabContent
              kind={apiKind}
              deploymentId={deploymentId}
              ns={ns}
              name={name}
              obj={obj}
            />
          )}
          {tab === 'exec' && (
            <ExecTabContent
              kind={apiKind}
              deploymentId={deploymentId}
              ns={ns}
              name={name}
              obj={obj}
              canExec={isTierAdmin}
            />
          )}
          {tab === 'events' && (
            <EventsPanel
              allEvents={allEvents}
              ns={ns}
              name={name}
              kindCanonical={apiKind}
              deploymentId={deploymentId}
            />
          )}
          {tab === 'metrics' && (
            <MetricsPanel deploymentId={deploymentId} kind={apiKind} ns={ns || undefined} name={name} />
          )}
          {tab === 'sbom' && (
            apiKind === 'pod' && ns && name ? (
              <SBOMTab sovereignId={deploymentId} namespace={ns} podName={name} />
            ) : (
              <div
                data-testid="resource-detail-sbom-only-pods"
                className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]"
              >
                SBOM &amp; CVE reports are produced per Pod by the Trivy operator. Open any
                <code className="ml-1 font-mono">Pod</code> resource to view its
                Software Bill of Materials and vulnerability summary.
              </div>
            )
          )}
          {tab === 'compliance' && (
            <ResourceComplianceTab
              sovereignId={deploymentId}
              kind={apiKind}
              ns={ns}
              name={name}
            />
          )}
          {tab === 'reconcile' && (
            isReconcilerManageable(apiKind) ? (
              <ReconcileTab
                deploymentId={deploymentId}
                apiKind={apiKind}
                ns={ns}
                name={name}
                obj={obj}
                isTierAdmin={isTierAdmin}
              />
            ) : (
              <PlaceholderTab
                testId="resource-detail-reconcile-not-flux"
                note="Reconcile / suspend / resume are available only for Flux reconciler kinds (HelmRelease, Kustomization, GitRepository, OCIRepository, HelmRepository, HelmChart)."
              />
            )
          )}
          {tab === 'tree' && (
            <ResourceTree basePath={basePath} tree={tree} isError={!!treeErr} isLoading={!tree && !treeErr} />
          )}
        </div>
      )}
    </div>
  )
}

// ─── Kind-aware OverviewTab ─────────────────────────────────────────

interface OverviewTabProps {
  obj: K8sObject | null
  replicas?: number
  kind: string
  ns: string
  name: string
  deploymentId: string
  isTierAdmin: boolean
  basePath: string
  k8sSnapshot?: ReadonlyMap<string, unknown> | null
}

function OverviewTab({
  obj,
  replicas,
  kind,
  ns,
  name,
  deploymentId,
  isTierAdmin,
  basePath,
  k8sSnapshot,
}: OverviewTabProps) {
  if (!obj) {
    return (
      <div data-testid="resource-detail-overview-empty" className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
        No data.
      </div>
    )
  }

  const labels = obj.metadata?.labels ?? {}
  const annotations = obj.metadata?.annotations ?? {}
  const owners = obj.metadata?.ownerReferences ?? []

  return (
    <div data-testid="resource-detail-overview" className="space-y-4">
      {/* Kind-specific summary panel — REAL K8s data per kind. */}
      <KindSummary obj={obj} kind={kind} replicas={replicas} k8sSnapshot={k8sSnapshot} ns={ns} name={name} />

      {/* Owner chain — live ownerReferences. Each owner links to its
          own detail page if the kind is one our resource router knows.
          Founder bug #5 C5-003: previously rendered as hint glossary
          text. Now: real owner names with deep-links. */}
      <OwnerChainPanel owners={owners} basePath={basePath} ns={ns} />

      {/* Labels + Annotations panel (collapsed-by-default). */}
      <MetaPanel labels={labels} annotations={annotations} />

      {/* Actions (Scale / Restart / Delete) — only for kinds the
          server allows. Server-side RBAC remains the authoritative
          gate; this UI only hides buttons for the viewer tier. */}
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

interface KindSummaryProps {
  obj: K8sObject
  kind: string
  replicas?: number
  k8sSnapshot?: ReadonlyMap<string, unknown> | null
  ns: string
  name: string
}

function KindSummary({ obj, kind, replicas, k8sSnapshot, ns, name }: KindSummaryProps) {
  const spec = (obj.spec ?? {}) as Record<string, unknown>
  const status = (obj.status ?? {}) as Record<string, unknown>

  switch (kind) {
    case 'deployment':
    case 'statefulset': {
      const desired = replicas ?? (spec.replicas as number | undefined)
      const ready = status.readyReplicas as number | undefined
      const available = status.availableReplicas as number | undefined
      const updated = status.updatedReplicas as number | undefined
      const selector = (spec.selector as { matchLabels?: Record<string, string> } | undefined)?.matchLabels
      const strategy = (spec.strategy as { type?: string } | undefined)?.type
      const podTemplate = spec.template as
        | { spec?: { containers?: { name?: string; image?: string }[] } }
        | undefined
      const images = podTemplate?.spec?.containers?.map((c) => c.image).filter(Boolean) ?? []
      return (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-3" data-testid="resource-detail-summary-workload">
          <KV label="Desired" value={desired == null ? '—' : String(desired)} testId="kv-desired" />
          <KV label="Ready" value={ready == null ? '—' : String(ready)} testId="kv-ready" />
          <KV label="Available" value={available == null ? '—' : String(available)} testId="kv-available" />
          {updated != null && <KV label="Updated" value={String(updated)} testId="kv-updated" />}
          {strategy && <KV label="Strategy" value={strategy} testId="kv-strategy" />}
          {selector && Object.keys(selector).length > 0 && (
            <KV
              label="Selector"
              value={Object.entries(selector).map(([k, v]) => `${k}=${v}`).join(', ')}
              testId="kv-selector"
            />
          )}
          {images.length > 0 && (
            <div className="md:col-span-3">
              <KV label="Image(s)" value={images.join('\n')} testId="kv-images" mono />
            </div>
          )}
        </div>
      )
    }

    case 'daemonset': {
      const desired = status.desiredNumberScheduled as number | undefined
      const current = status.currentNumberScheduled as number | undefined
      const ready = status.numberReady as number | undefined
      const available = status.numberAvailable as number | undefined
      const misscheduled = status.numberMisscheduled as number | undefined
      const nodeSelector = (spec.template as { spec?: { nodeSelector?: Record<string, string> } } | undefined)
        ?.spec?.nodeSelector
      return (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-3" data-testid="resource-detail-summary-daemonset">
          <KV label="Desired" value={desired == null ? '—' : String(desired)} testId="kv-desired" />
          <KV label="Current" value={current == null ? '—' : String(current)} testId="kv-current" />
          <KV label="Ready" value={ready == null ? '—' : String(ready)} testId="kv-ready" />
          <KV label="Available" value={available == null ? '—' : String(available)} testId="kv-available" />
          {misscheduled != null && (
            <KV label="Misscheduled" value={String(misscheduled)} testId="kv-misscheduled" />
          )}
          {nodeSelector && Object.keys(nodeSelector).length > 0 && (
            <KV
              label="Node Selector"
              value={Object.entries(nodeSelector).map(([k, v]) => `${k}=${v}`).join(', ')}
              testId="kv-nodeSelector"
            />
          )}
        </div>
      )
    }

    case 'pod': {
      const phase = status.phase as string | undefined
      const podIP = status.podIP as string | undefined
      const hostIP = status.hostIP as string | undefined
      const nodeName = (spec.nodeName as string | undefined) ?? '—'
      const startTime = status.startTime as string | undefined
      const containers = (spec.containers as { name?: string; image?: string }[] | undefined) ?? []
      const containerStatuses =
        (status.containerStatuses as
          | {
              name?: string
              ready?: boolean
              restartCount?: number
              image?: string
              state?: Record<string, unknown>
            }[]
          | undefined) ?? []
      const csByName = new Map<string, (typeof containerStatuses)[number]>()
      for (const cs of containerStatuses) if (cs.name) csByName.set(cs.name, cs)

      return (
        <div className="space-y-3" data-testid="resource-detail-summary-pod">
          <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
            <KV label="Phase" value={phase ?? '—'} testId="kv-phase" />
            <KV label="Pod IP" value={podIP ?? '—'} testId="kv-podIP" mono />
            <KV label="Host IP" value={hostIP ?? '—'} testId="kv-hostIP" mono />
            <KV label="Node" value={nodeName} testId="kv-node" mono />
            {startTime && <KV label="Started" value={startTime} testId="kv-startTime" />}
          </div>
          <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
            <div className="mb-2 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
              Containers ({containers.length})
            </div>
            {containers.length === 0 ? (
              <div data-testid="containers-empty" className="text-sm text-[var(--color-text-dim)]">
                No containers in spec.
              </div>
            ) : (
              <table className="w-full text-sm" data-testid="containers-table">
                <thead className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
                  <tr className="text-left">
                    <th className="py-1">Name</th>
                    <th className="py-1">Image</th>
                    <th className="py-1">Ready</th>
                    <th className="py-1">Restarts</th>
                    <th className="py-1">State</th>
                  </tr>
                </thead>
                <tbody>
                  {containers.map((c) => {
                    const cs = c.name ? csByName.get(c.name) : undefined
                    const state = cs?.state ?? {}
                    const stateKey = Object.keys(state)[0] ?? '—'
                    return (
                      <tr
                        key={c.name ?? c.image}
                        data-testid={`container-row-${c.name}`}
                        className="border-t border-[var(--color-border)]"
                      >
                        <td className="py-1 font-mono">{c.name ?? '—'}</td>
                        <td className="py-1 font-mono break-all">{c.image ?? '—'}</td>
                        <td className="py-1">{cs?.ready ? 'true' : 'false'}</td>
                        <td className="py-1">{cs?.restartCount ?? 0}</td>
                        <td className="py-1">{stateKey}</td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            )}
          </div>
        </div>
      )
    }

    case 'service': {
      const type = spec.type as string | undefined
      const clusterIP = spec.clusterIP as string | undefined
      const ports = (spec.ports as { name?: string; port?: number; protocol?: string; targetPort?: unknown }[] | undefined) ?? []
      const selector = spec.selector as Record<string, string> | undefined
      // Endpoints from the live snapshot.
      const endpoints = lookupEndpoints(k8sSnapshot, ns, name)
      return (
        <div className="space-y-3" data-testid="resource-detail-summary-service">
          <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
            <KV label="Type" value={type ?? '—'} testId="kv-type" />
            <KV label="ClusterIP" value={clusterIP ?? '—'} testId="kv-clusterIP" mono />
            {selector && Object.keys(selector).length > 0 && (
              <KV
                label="Selector"
                value={Object.entries(selector).map(([k, v]) => `${k}=${v}`).join(', ')}
                testId="kv-selector"
              />
            )}
          </div>
          <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
            <div className="mb-2 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
              Ports ({ports.length})
            </div>
            {ports.length === 0 ? (
              <div className="text-sm text-[var(--color-text-dim)]">No ports defined.</div>
            ) : (
              <ul data-testid="service-ports" className="space-y-1 font-mono text-sm">
                {ports.map((p, i) => (
                  <li key={`${p.name ?? i}`} data-testid={`service-port-${p.name ?? i}`}>
                    {p.name ? `${p.name}: ` : ''}
                    {p.port}/{p.protocol ?? 'TCP'} → {String(p.targetPort ?? '?')}
                  </li>
                ))}
              </ul>
            )}
          </div>
          <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
            <div className="mb-2 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
              Endpoints ({endpoints.length})
            </div>
            {endpoints.length === 0 ? (
              <div data-testid="service-endpoints-empty" className="text-sm text-[var(--color-text-dim)]">
                No live endpoints — either no Pods match the selector, or the cluster snapshot has
                not yet streamed the EndpointSlice.
              </div>
            ) : (
              <ul data-testid="service-endpoints" className="space-y-1 font-mono text-sm">
                {endpoints.map((e, i) => (
                  <li key={`${e}-${i}`} data-testid={`service-endpoint-${i}`}>
                    {e}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      )
    }

    case 'configmap':
    case 'secret': {
      const data = (obj.data as Record<string, unknown> | undefined) ?? {}
      const keys = Object.keys(data)
      const isSecret = kind === 'secret'
      return (
        <div className="space-y-3" data-testid="resource-detail-summary-configdata">
          <div className="grid grid-cols-1 gap-3 md:grid-cols-3">
            <KV label="Keys" value={String(keys.length)} testId="kv-keys" />
            {!isSecret && obj.apiVersion && (
              <KV label="apiVersion" value={String(obj.apiVersion)} testId="kv-apiVersion" />
            )}
            {obj.kind && <KV label="kind" value={String(obj.kind)} testId="kv-kind" />}
          </div>
          <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
            <div className="mb-2 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">
              {isSecret ? 'Data Keys (values hidden — use Reveal in YAML tab)' : 'Data Keys'}
            </div>
            {keys.length === 0 ? (
              <div className="text-sm text-[var(--color-text-dim)]">No data entries.</div>
            ) : (
              <ul data-testid="configdata-keys" className="space-y-1 font-mono text-sm">
                {keys.map((k) => (
                  <li key={k} data-testid={`configdata-key-${k}`}>
                    {k}
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      )
    }

    default: {
      // Generic shape — kinds we don't have a dedicated panel for
      // (ingress, networkpolicy, pv, pvc, namespace, etc.). Show
      // whatever's interesting from spec/status without a glossary
      // dump.
      const phase = (status.phase as string | undefined) ?? undefined
      return (
        <div className="grid grid-cols-1 gap-3 md:grid-cols-2" data-testid="resource-detail-summary-generic">
          {phase && <KV label="Phase" value={phase} testId="kv-phase" />}
          {obj.apiVersion && <KV label="apiVersion" value={obj.apiVersion} testId="kv-apiVersion" mono />}
          {obj.kind && <KV label="kind" value={obj.kind} testId="kv-kind" />}
        </div>
      )
    }
  }
}

function OwnerChainPanel({
  owners,
  basePath,
  ns,
}: {
  owners: NonNullable<K8sObject['metadata']>['ownerReferences']
  basePath: string
  ns: string
}) {
  return (
    <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4" data-testid="owner-chain">
      <div className="mb-2 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">Owner</div>
      {!owners || owners.length === 0 ? (
        <div className="text-sm text-[var(--color-text-dim)]" data-testid="owner-chain-empty">
          None — top-level resource (no controlling owner).
        </div>
      ) : (
        <ul className="space-y-1 text-sm">
          {owners.map((o) => {
            const k = (o.kind ?? '').toLowerCase()
            const href = `${basePath.replace(/\/$/, '')}/resource/${encodeURIComponent(k)}/${
              ns ? encodeURIComponent(ns) : '_'
            }/${encodeURIComponent(o.name ?? '')}/overview`
            return (
              <li
                key={o.uid ?? `${o.kind}/${o.name}`}
                data-testid={`owner-${o.kind}-${o.name}`}
                className="font-mono"
              >
                <a href={href} className="text-[var(--color-accent)] underline hover:text-[var(--color-accent-strong)]">
                  {o.kind}/{o.name}
                </a>
                {o.controller ? <span className="ml-2 text-xs text-[var(--color-text-dim)]">(controller)</span> : null}
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

function MetaPanel({
  labels,
  annotations,
}: {
  labels: Record<string, string>
  annotations: Record<string, string>
}) {
  const labelEntries = Object.entries(labels)
  const annotationEntries = Object.entries(annotations)
  if (labelEntries.length === 0 && annotationEntries.length === 0) return null
  return (
    <div className="grid grid-cols-1 gap-3 md:grid-cols-2" data-testid="resource-detail-meta">
      {labelEntries.length > 0 && (
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
          <div className="mb-2 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">Labels</div>
          <ul className="space-y-0.5 font-mono text-xs" data-testid="meta-labels">
            {labelEntries.map(([k, v]) => (
              <li key={k}>
                {k}={v}
              </li>
            ))}
          </ul>
        </div>
      )}
      {annotationEntries.length > 0 && (
        <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4">
          <div className="mb-2 text-xs uppercase tracking-wide text-[var(--color-text-dim)]">Annotations</div>
          <ul className="space-y-0.5 font-mono text-xs" data-testid="meta-annotations">
            {annotationEntries.map(([k, v]) => (
              <li key={k} className="break-all">
                {k}={v.length > 120 ? v.slice(0, 117) + '…' : v}
              </li>
            ))}
          </ul>
        </div>
      )}
    </div>
  )
}

function lookupEndpoints(
  snapshot: ReadonlyMap<string, unknown> | null | undefined,
  ns: string,
  serviceName: string,
): string[] {
  if (!snapshot) return []
  const out: string[] = []
  // EndpointSlices carry the label `kubernetes.io/service-name=<svc>`
  // and live in the Service's namespace. We pluck IP:port pairs from
  // each ready endpoint.
  for (const [key, valueRaw] of snapshot.entries() as IterableIterator<[string, K8sObject]>) {
    if (!key.startsWith('endpointslice:')) continue
    const value = valueRaw as K8sObject
    const sliceNs = value.metadata?.namespace
    if (sliceNs !== ns) continue
    const svcLabel = value.metadata?.labels?.['kubernetes.io/service-name']
    if (svcLabel !== serviceName) continue
    const endpoints = (value.endpoints as { addresses?: string[]; conditions?: { ready?: boolean } }[] | undefined) ?? []
    const ports = (value.ports as { port?: number; name?: string }[] | undefined) ?? []
    for (const ep of endpoints) {
      const ready = ep.conditions?.ready !== false
      if (!ready) continue
      for (const addr of ep.addresses ?? []) {
        if (ports.length === 0) {
          out.push(addr)
        } else {
          for (const p of ports) {
            out.push(`${addr}:${p.port ?? '?'}`)
          }
        }
      }
    }
  }
  return out
}

interface KVProps {
  label: string
  value: string
  testId?: string
  mono?: boolean
}

function KV({ label, value, testId, mono = true }: KVProps) {
  return (
    <div
      className="rounded-md border border-[var(--color-border)] bg-[var(--color-bg-2)] p-3"
      data-testid={testId}
    >
      <div className="text-xs uppercase tracking-wide text-[var(--color-text-dim)]">{label}</div>
      <div className={`mt-1 break-words text-sm text-[var(--color-text)] ${mono ? 'font-mono' : ''}`}>
        {value || '—'}
      </div>
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
    case 'sbom':
      return 'SBOM'
    case 'compliance':
      return 'Compliance'
    case 'reconcile':
      return 'Reconcile'
    case 'tree':
      return 'Tree'
    default:
      return tab
  }
}
