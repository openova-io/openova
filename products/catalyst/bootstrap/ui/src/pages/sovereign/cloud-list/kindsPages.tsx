/**
 * cloud-list/kindsPages.tsx — page wrapper components for K8s-backed
 * kinds. Lives in a `.tsx` file because kinds.ts is a `.ts` (no JSX
 * allowed) per the existing project convention.
 *
 * Each wrapper picks the column set + tagline for one kind and
 * delegates rendering to the generic K8sListPage.
 */

import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'
import { K8sListPage } from './K8sListPage'
import {
  COL_NAME,
  COL_NAMESPACE,
  COL_TARGET_NAMESPACE,
  COL_AGE,
  colSpec,
  colStatus,
  colReconcileStatus,
  colCnpgStatus,
  colCatalystCrStatus,
  colReadyCount,
  colPodRestarts,
  colPodPhase,
  type CellTone,
} from './k8sColumns'

/** Pod READY column — `<ready containers>/<total containers>`, coloured by
 *  readiness. k9s shows this as the headline health signal. */
function podReadyColumn() {
  const counts = (o: K8sObject): { ready: number; total: number } => {
    const cs = (o.status as Record<string, unknown> | undefined)?.[
      'containerStatuses'
    ] as Array<Record<string, unknown>> | undefined
    if (!Array.isArray(cs)) return { ready: 0, total: 0 }
    const ready = cs.filter((c) => c?.['ready'] === true).length
    return { ready, total: cs.length }
  }
  return {
    header: 'Ready',
    extract: (o: K8sObject) => {
      const { ready, total } = counts(o)
      return total === 0 ? '—' : `${ready}/${total}`
    },
    tone: (o: K8sObject): CellTone | undefined => {
      const { ready, total } = counts(o)
      if (total === 0) return undefined
      if (ready >= total) return 'ok'
      if (ready === 0) return 'err'
      return 'warn'
    },
  }
}

export function PodsListPage() {
  return (
    <K8sListPage
      kind="pod"
      title="Pods"
      tagline="Running containers across all namespaces."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        podReadyColumn(),
        colPodPhase('Status'),
        colPodRestarts('Restarts'),
        colSpec('nodeName', 'Node'),
        COL_AGE,
      ]}
    />
  )
}

export function DeploymentsListPage() {
  return (
    <K8sListPage
      kind="deployment"
      title="Deployments"
      tagline="Stateless replicated workloads."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        // k9s-style ready/desired with green/amber/red readiness colour.
        colReadyCount('readyReplicas', 'replicas', { header: 'Ready' }),
        colStatus('availableReplicas', 'Available'),
        COL_AGE,
      ]}
    />
  )
}

export function StatefulSetsListPage() {
  return (
    <K8sListPage
      kind="statefulset"
      title="StatefulSets"
      tagline="Ordered, persistent workloads (databases, queues)."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        colReadyCount('readyReplicas', 'replicas', { header: 'Ready' }),
        COL_AGE,
      ]}
    />
  )
}

export function DaemonSetsListPage() {
  return (
    <K8sListPage
      kind="daemonset"
      title="DaemonSets"
      tagline="One pod per node — agents, CSI drivers, log shippers."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        colStatus('desiredNumberScheduled', 'Desired'),
        // Both counts live in status for DaemonSets.
        colReadyCount('numberReady', 'desiredNumberScheduled', {
          header: 'Ready',
          desiredFrom: 'status',
        }),
        COL_AGE,
      ]}
    />
  )
}

export function ReplicaSetsListPage() {
  return (
    <K8sListPage
      kind="replicaset"
      title="ReplicaSets"
      tagline="Owned by Deployments — kept here for diagnostics."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        colSpec('replicas', 'Desired'),
        colReadyCount('readyReplicas', 'replicas', { header: 'Ready' }),
        COL_AGE,
      ]}
    />
  )
}

export function ConfigMapsListPage() {
  return (
    <K8sListPage
      kind="configmap"
      title="ConfigMaps"
      tagline="Key-value config bundles. Values are server-redacted in transit."
      columns={[COL_NAMESPACE, COL_NAME, COL_AGE]}
    />
  )
}

export function SecretsListPage() {
  return (
    <K8sListPage
      kind="secret"
      title="Secrets"
      tagline="Sensitive bundles. Values stripped server-side; only key names + age listed."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Type',
          extract: (o) => (o['type'] as string | undefined) ?? '—',
        },
        COL_AGE,
      ]}
    />
  )
}

export function NamespacesListPage() {
  return (
    <K8sListPage
      kind="namespace"
      title="Namespaces"
      tagline="Logical partitions. Per-namespace RBAC + resource quotas live here."
      columns={[
        COL_NAME,
        colStatus('phase', 'Phase', { tone: true }),
        COL_AGE,
      ]}
    />
  )
}

export function NodesListPage() {
  return (
    <K8sListPage
      kind="node"
      title="Nodes"
      tagline="Kubelets currently joined to the cluster (raw K8s view)."
      columns={[
        COL_NAME,
        {
          header: 'Kubelet',
          extract: (o) => {
            const ni = (o.status as Record<string, unknown> | undefined)?.[
              'nodeInfo'
            ] as Record<string, unknown> | undefined
            return (ni?.['kubeletVersion'] as string | undefined) ?? '—'
          },
        },
        {
          header: 'OS',
          extract: (o) => {
            const ni = (o.status as Record<string, unknown> | undefined)?.[
              'nodeInfo'
            ] as Record<string, unknown> | undefined
            return (ni?.['osImage'] as string | undefined) ?? '—'
          },
        },
        COL_AGE,
      ]}
    />
  )
}

export function PersistentVolumesListPage() {
  return (
    <K8sListPage
      kind="persistentvolume"
      title="PersistentVolumes"
      tagline="Cluster-scoped backing volumes — one per Bound PVC."
      columns={[
        COL_NAME,
        colSpec('storageClassName', 'Class'),
        colStatus('phase', 'Phase', { tone: true }),
        COL_AGE,
      ]}
    />
  )
}

export function EndpointSlicesListPage() {
  return (
    <K8sListPage
      kind="endpointslice"
      title="EndpointSlices"
      tagline="Service backend addresses. Successor to legacy Endpoints."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Address Type',
          extract: (o) => (o['addressType'] as string | undefined) ?? '—',
        },
        COL_AGE,
      ]}
    />
  )
}

export function ServicesListPage() {
  return (
    <K8sListPage
      kind="service"
      title="Services"
      tagline="Per-namespace service IPs + selectors."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        colSpec('type', 'Type'),
        colSpec('clusterIP', 'ClusterIP'),
        COL_AGE,
      ]}
    />
  )
}

export function IngressesListPage() {
  return (
    <K8sListPage
      kind="ingress"
      title="Ingresses"
      tagline="HTTP routing — host headers + TLS terminators."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Hosts',
          extract: (o) => {
            const rules = ((o.spec as Record<string, unknown> | undefined)?.[
              'rules'
            ] ?? []) as Array<{ host?: string }>
            return rules.map((r) => r.host).filter(Boolean).join(', ') || '—'
          },
        },
        COL_AGE,
      ]}
    />
  )
}

/* ── Network + security coverage (#3998) ──────────────────────────────
 *
 * The gateway-api front door (Gateway + HTTPRoute) and the security
 * posture (NetworkPolicy + CiliumNetworkPolicy / ClusterwideNetworkPolicy)
 * are LIVE on every Sovereign but had no cloud-view page — so the front
 * door + micro-segmentation were invisible. These wrappers render them as
 * standard K8sListPages driven by the live k8scache SSE snapshot (the GVRs
 * are already registered in api/internal/k8scache/kinds.go: gateway /
 * httproute / networkpolicy / ciliumnetworkpolicy /
 * ciliumclusterwidenetworkpolicy, all on the SERVED versions verified
 * against hw173). Each carries the columns the operator needs.
 * ────────────────────────────────────────────────────────────────── */

/** Render a Gateway status condition (Programmed / Accepted) as a short
 *  string. Gateway has no Ready condition; Programmed=True is the
 *  "front door is live" signal. */
function gatewayConditionStatus(o: K8sObject, type: string): string {
  const conds = (o.status as Record<string, unknown> | undefined)?.[
    'conditions'
  ] as Array<Record<string, unknown>> | undefined
  if (!Array.isArray(conds)) return '—'
  const c = conds.find((x) => x?.['type'] === type)
  const s = c?.['status'] as string | undefined
  return s ?? '—'
}

export function GatewaysListPage() {
  return (
    <K8sListPage
      kind="gateway"
      title="Gateways"
      tagline="Gateway API front doors — the real ingress path. Class / address / Programmed state."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Class',
          extract: (o) => {
            const cls = (o.spec as Record<string, unknown> | undefined)?.[
              'gatewayClassName'
            ] as string | undefined
            return cls ?? '—'
          },
        },
        {
          header: 'Address',
          extract: (o) => {
            // Prefer status.addresses (the assigned front-door IP/host);
            // fall back to spec.addresses (operator-requested). On
            // nodeIPAM/DNAT clouds (Huawei) the EIP is wired outside the
            // Gateway CR so this can legitimately be "—" — the address is
            // surfaced via the LoadBalancer/Service path instead.
            const fromStatus = (o.status as Record<string, unknown> | undefined)?.[
              'addresses'
            ] as Array<{ value?: string }> | undefined
            const fromSpec = (o.spec as Record<string, unknown> | undefined)?.[
              'addresses'
            ] as Array<{ value?: string }> | undefined
            const addrs = (fromStatus && fromStatus.length ? fromStatus : fromSpec) ?? []
            const vals = addrs.map((a) => a?.value).filter(Boolean)
            return vals.length ? vals.join(', ') : '—'
          },
        },
        {
          header: 'Listeners',
          extract: (o) => {
            const l = (o.spec as Record<string, unknown> | undefined)?.[
              'listeners'
            ] as unknown[] | undefined
            return Array.isArray(l) ? String(l.length) : '—'
          },
        },
        {
          header: 'Programmed',
          extract: (o) => gatewayConditionStatus(o, 'Programmed'),
          // True = front door is live (green); False = not programmed (red);
          // missing (—) = still settling (amber).
          tone: (o): CellTone | undefined => {
            const s = gatewayConditionStatus(o, 'Programmed')
            if (s === 'True') return 'ok'
            if (s === 'False') return 'err'
            return 'warn'
          },
        },
        COL_AGE,
      ]}
    />
  )
}

export function HttpRoutesListPage() {
  return (
    <K8sListPage
      kind="httproute"
      title="HTTPRoutes"
      tagline="Gateway API routes — hostnames + the Gateway they attach to + rule count."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Hostnames',
          extract: (o) => {
            const hosts = ((o.spec as Record<string, unknown> | undefined)?.[
              'hostnames'
            ] ?? []) as string[]
            return Array.isArray(hosts) && hosts.length ? hosts.join(', ') : '—'
          },
        },
        {
          header: 'Parent Gateway',
          extract: (o) => {
            const refs = ((o.spec as Record<string, unknown> | undefined)?.[
              'parentRefs'
            ] ?? []) as Array<{ name?: string; namespace?: string }>
            const names = refs
              .map((r) => (r?.namespace ? `${r.namespace}/${r.name}` : r?.name))
              .filter(Boolean)
            return names.length ? names.join(', ') : '—'
          },
        },
        {
          header: 'Rules',
          extract: (o) => {
            const rules = (o.spec as Record<string, unknown> | undefined)?.[
              'rules'
            ] as unknown[] | undefined
            return Array.isArray(rules) ? String(rules.length) : '—'
          },
        },
        COL_AGE,
      ]}
    />
  )
}

/** Format a podSelector/endpointSelector.matchLabels map into a compact
 *  k=v string, or "(all)" for an empty selector (which selects every pod
 *  in scope — the default-deny baseline shape). */
function formatSelector(sel: Record<string, unknown> | undefined): string {
  if (!sel) return '—'
  const labels = sel['matchLabels'] as Record<string, string> | undefined
  if (!labels || Object.keys(labels).length === 0) {
    // An explicitly-empty selector matches everything; distinguish from
    // "no selector field at all".
    return Object.prototype.hasOwnProperty.call(sel, 'matchLabels') ||
      Object.keys(sel).length === 0
      ? '(all)'
      : '—'
  }
  return Object.entries(labels)
    .map(([k, v]) => `${k}=${v}`)
    .join(', ')
}

/** Count ingress + egress rule arrays and policyTypes for a (K8s or
 *  Cilium) network policy, reading from either spec or spec.specs[0]. */
function policyRuleSummary(spec: Record<string, unknown> | undefined): {
  ingress: number
  egress: number
} {
  const ing = (spec?.['ingress'] as unknown[] | undefined) ?? []
  const egr = (spec?.['egress'] as unknown[] | undefined) ?? []
  return {
    ingress: Array.isArray(ing) ? ing.length : 0,
    egress: Array.isArray(egr) ? egr.length : 0,
  }
}

export function NetworkPoliciesListPage() {
  return (
    <K8sListPage
      kind="networkpolicy"
      title="Network Policies"
      tagline="K8s NetworkPolicies — pod selector, ingress/egress rule counts, policy types."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Pod Selector',
          extract: (o) =>
            formatSelector(
              (o.spec as Record<string, unknown> | undefined)?.[
                'podSelector'
              ] as Record<string, unknown> | undefined,
            ),
        },
        {
          header: 'Ingress/Egress',
          extract: (o) => {
            const s = policyRuleSummary(
              o.spec as Record<string, unknown> | undefined,
            )
            return `${s.ingress} / ${s.egress}`
          },
        },
        {
          header: 'Policy Types',
          extract: (o) => {
            const t = ((o.spec as Record<string, unknown> | undefined)?.[
              'policyTypes'
            ] ?? []) as string[]
            return Array.isArray(t) && t.length ? t.join(', ') : '—'
          },
        },
        COL_AGE,
      ]}
    />
  )
}

/** Resolve a CiliumNetworkPolicy's effective rule spec — Cilium accepts
 *  EITHER a single `spec` OR a `specs[]` array (mutually exclusive). For
 *  the list summary we read `spec`, falling back to the first `specs[]`
 *  entry. */
function ciliumPolicySpec(o: K8sObject): Record<string, unknown> | undefined {
  const spec = o.spec as Record<string, unknown> | undefined
  if (spec && Object.keys(spec).length > 0) return spec
  const specs = (o as Record<string, unknown>)['specs'] as
    | Array<Record<string, unknown>>
    | undefined
  return Array.isArray(specs) && specs.length > 0 ? specs[0] : undefined
}

/** Aggregate ingress/egress across `spec` AND every `specs[]` entry so the
 *  count reflects the whole policy, not just the first rule block. */
function ciliumRuleSummary(o: K8sObject): { ingress: number; egress: number } {
  const blocks: Array<Record<string, unknown> | undefined> = []
  const spec = o.spec as Record<string, unknown> | undefined
  if (spec && Object.keys(spec).length > 0) blocks.push(spec)
  const specs = (o as Record<string, unknown>)['specs'] as
    | Array<Record<string, unknown>>
    | undefined
  if (Array.isArray(specs)) blocks.push(...specs)
  let ingress = 0
  let egress = 0
  for (const b of blocks) {
    const s = policyRuleSummary(b)
    ingress += s.ingress
    egress += s.egress
  }
  return { ingress, egress }
}

export function CiliumNetworkPoliciesListPage() {
  return (
    <K8sListPage
      kind="ciliumnetworkpolicy"
      title="Cilium Network Policies"
      tagline="CiliumNetworkPolicies — L3-L7 micro-segmentation. Endpoint selector + ingress/egress."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Endpoint Selector',
          extract: (o) =>
            formatSelector(
              ciliumPolicySpec(o)?.['endpointSelector'] as
                | Record<string, unknown>
                | undefined,
            ),
        },
        {
          header: 'Ingress/Egress',
          extract: (o) => {
            const s = ciliumRuleSummary(o)
            return `${s.ingress} / ${s.egress}`
          },
        },
        COL_AGE,
      ]}
    />
  )
}

export function CiliumClusterwideNetworkPoliciesListPage() {
  return (
    <K8sListPage
      kind="ciliumclusterwidenetworkpolicy"
      title="Cilium Clusterwide Network Policies"
      tagline="Cluster-scoped CiliumClusterwideNetworkPolicies — the baseline deny + cross-namespace rules."
      columns={[
        COL_NAME,
        {
          header: 'Endpoint Selector',
          extract: (o) =>
            formatSelector(
              ciliumPolicySpec(o)?.['endpointSelector'] as
                | Record<string, unknown>
                | undefined,
            ),
        },
        {
          header: 'Ingress/Egress',
          extract: (o) => {
            const s = ciliumRuleSummary(o)
            return `${s.ingress} / ${s.egress}`
          },
        },
        COL_AGE,
      ]}
    />
  )
}

/**
 * StorageClass columns (#5611).
 *
 * StorageClass carries `provisioner` / `reclaimPolicy` /
 * `volumeBindingMode` / `allowVolumeExpansion` at the TOP level of the
 * object — not under `spec` — so `colSpec` does not apply and each
 * column extracts directly.
 */
function colTop(key: string, header: string) {
  return {
    header,
    extract: (o: K8sObject) => {
      const v = o[key]
      return v == null ? '—' : String(v)
    },
  }
}

/** The `storageclass.kubernetes.io/is-default-class` annotation is what
 *  `kubectl get sc` renders as the "(default)" name suffix. */
const SC_DEFAULT_ANNOTATION = 'storageclass.kubernetes.io/is-default-class'
const COL_SC_DEFAULT = {
  header: 'Default',
  extract: (o: K8sObject) =>
    o.metadata?.annotations?.[SC_DEFAULT_ANNOTATION] === 'true' ? 'yes' : '—',
  tone: (o: K8sObject): CellTone | undefined =>
    o.metadata?.annotations?.[SC_DEFAULT_ANNOTATION] === 'true' ? 'ok' : undefined,
}

/**
 * StorageClassesListPage — #5611.
 *
 * Was a stub reading "the storage.k8s.io/StorageClass GVR is not yet in
 * the catalyst-api k8scache registry", with `hasData: false` in kinds.ts
 * so the chip rendered the not-collected marker "—" on a Sovereign
 * serving 2 classes per region. The GVR is now registered
 * (api/internal/k8scache/kinds.go `storageclass`), so this renders live
 * objects like every other wired kind — including the #5571 Region
 * column, which matters here because BOTH regions serve a class named
 * `evs-ssd` and they must not read as one.
 */
export function StorageClassesListPage() {
  return (
    <K8sListPage
      kind="storageclass"
      title="Storage Classes"
      tagline="Provisioner + reclaim policy presets backing every PVC."
      columns={[
        COL_NAME,
        colTop('provisioner', 'Provisioner'),
        colTop('reclaimPolicy', 'Reclaim Policy'),
        colTop('volumeBindingMode', 'Binding Mode'),
        colTop('allowVolumeExpansion', 'Expandable'),
        COL_SC_DEFAULT,
        COL_AGE,
      ]}
    />
  )
}

export function DnsZonesListPage() {
  return (
    <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
      <h2 className="mb-1 text-lg font-semibold text-[var(--color-text-strong)]">DNS Zones</h2>
      <p>
        DNS zones live in the per-Sovereign PowerDNS, not in K8s. A future iteration will surface them via the catalyst-api PowerDNS adapter.
      </p>
    </div>
  )
}

// Wave-2 Family-E (#1583, C11-005/C11-006): Kyverno PolicyReport
// (namespaced) + ClusterPolicyReport (cluster-scoped) surfaces.
// Both kinds are already registered in the catalyst-api k8scache
// (kinds.go: `policyreport` + `clusterpolicyreport`); these wrappers
// render them as standard list pages with the canonical columns the
// operator needs (resource being evaluated, pass/fail counts).
/** A policy-report summary column. `pass` colours green when >0; `fail`
 *  red when >0 (else green 0); `warn` amber when >0 (else muted). */
function policyReportSummaryColumn(key: 'pass' | 'fail' | 'warn') {
  const num = (o: K8sObject): number | null => {
    const s = (o['summary'] as Record<string, unknown> | undefined) ?? {}
    const v = s[key]
    return v == null ? null : Number(v)
  }
  const header = key.charAt(0).toUpperCase() + key.slice(1)
  return {
    header,
    extract: (o: K8sObject) => {
      const n = num(o)
      return n == null ? '—' : String(n)
    },
    tone: (o: K8sObject): CellTone | undefined => {
      const n = num(o)
      if (n == null) return undefined
      if (key === 'pass') return n > 0 ? 'ok' : 'muted'
      if (key === 'fail') return n > 0 ? 'err' : 'ok'
      // warn
      return n > 0 ? 'warn' : 'muted'
    },
  }
}

export function PolicyReportsListPage() {
  return (
    <K8sListPage
      kind="policyreport"
      title="Policy Reports"
      tagline="Per-namespace Kyverno PolicyReport evaluations — pass / fail counts per resource."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        policyReportSummaryColumn('pass'),
        policyReportSummaryColumn('fail'),
        policyReportSummaryColumn('warn'),
        COL_AGE,
      ]}
    />
  )
}

export function ClusterPolicyReportsListPage() {
  return (
    <K8sListPage
      kind="clusterpolicyreport"
      title="Cluster Policy Reports"
      tagline="Cluster-scoped Kyverno ClusterPolicyReport evaluations — pass / fail counts."
      columns={[
        COL_NAME,
        policyReportSummaryColumn('pass'),
        policyReportSummaryColumn('fail'),
        policyReportSummaryColumn('warn'),
        COL_AGE,
      ]}
    />
  )
}

/* ── Reconciler kinds (#3978 / Refs #3970) ────────────────────────────
 *
 * #3970 should have ADDED the reconciler resource types to the rich
 * per-kind list — instead it replaced the whole list with a flat
 * NAME·KIND·FAMILY·STATUS table. #3978 restores the per-kind catalogue
 * AND adds these reconciler kinds, each with its OWN attribute columns,
 * driven by the live k8scache SSE snapshot (the GVRs are registered in
 * api/internal/k8scache/kinds.go). Every status column renders the
 * Reconciliation vocabulary — Reconciled / Reconciling / Degraded —
 * NEVER Success/Failed (continuous reconcilers have no finite end).
 * ────────────────────────────────────────────────────────────────── */

export function HelmReleasesListPage() {
  return (
    <K8sListPage
      kind="helmrelease"
      title="HelmReleases"
      tagline="Flux HelmReleases — one per installed Blueprint. Target namespace / chart / revision / reconcile state."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        // #4281 — the HelmRelease RECORD lives in flux-system for every
        // host-shared platform Blueprint; the workload actually runs in
        // spec.targetNamespace. Surface it so the operator sees the real
        // home, not a misleading "everything's in flux-system".
        COL_TARGET_NAMESPACE,
        {
          header: 'Chart',
          extract: (o) => {
            const chart = (
              (o.spec as Record<string, unknown> | undefined)?.['chart'] as
                | Record<string, unknown>
                | undefined
            )?.['spec'] as Record<string, unknown> | undefined
            const name = chart?.['chart'] as string | undefined
            return name ?? '—'
          },
        },
        {
          header: 'Revision',
          extract: (o) => {
            const st = o.status as Record<string, unknown> | undefined
            // helm-controller exposes lastAppliedRevision / history[0].chartVersion.
            const rev =
              (st?.['lastAppliedRevision'] as string | undefined) ??
              (st?.['lastAttemptedRevision'] as string | undefined)
            if (rev) return rev
            const hist = st?.['history'] as
              | Array<Record<string, unknown>>
              | undefined
            const v = Array.isArray(hist) && hist.length > 0
              ? (hist[0]?.['chartVersion'] as string | undefined)
              : undefined
            return v ?? '—'
          },
        },
        colReconcileStatus('Status'),
        COL_AGE,
      ]}
    />
  )
}

export function KustomizationsListPage() {
  return (
    <K8sListPage
      kind="kustomization"
      title="Kustomizations"
      tagline="Flux Kustomizations — the bootstrap-kit tiers. Path / reconcile state / last applied."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        colSpec('path', 'Path'),
        colReconcileStatus('Status'),
        {
          header: 'Last Applied',
          extract: (o) => {
            const rev = (o.status as Record<string, unknown> | undefined)?.[
              'lastAppliedRevision'
            ] as string | undefined
            return rev ?? '—'
          },
        },
        COL_AGE,
      ]}
    />
  )
}

export function GitRepositoriesListPage() {
  return (
    <K8sListPage
      kind="gitrepository"
      title="GitRepositories"
      tagline="Flux GitRepository sources — the catalog mirror Flux reconciles from. URL / branch / state."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        colSpec('url', 'URL'),
        {
          header: 'Branch',
          extract: (o) => {
            const ref = (o.spec as Record<string, unknown> | undefined)?.[
              'ref'
            ] as Record<string, unknown> | undefined
            return (
              (ref?.['branch'] as string | undefined) ??
              (ref?.['tag'] as string | undefined) ??
              (ref?.['commit'] as string | undefined) ??
              '—'
            )
          },
        },
        colReconcileStatus('Status'),
        COL_AGE,
      ]}
    />
  )
}

export function CertificatesListPage() {
  return (
    <K8sListPage
      kind="certificate"
      title="Certificates"
      tagline="cert-manager Certificates. Issuer / expiry (NotAfter) / Ready state."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Issuer',
          extract: (o) => {
            const ref = (o.spec as Record<string, unknown> | undefined)?.[
              'issuerRef'
            ] as Record<string, unknown> | undefined
            return (ref?.['name'] as string | undefined) ?? '—'
          },
        },
        {
          header: 'Not After',
          extract: (o) => {
            const na = (o.status as Record<string, unknown> | undefined)?.[
              'notAfter'
            ] as string | undefined
            return na ?? '—'
          },
        },
        colReconcileStatus('Ready'),
        COL_AGE,
      ]}
    />
  )
}

export function ExternalSecretsListPage() {
  return (
    <K8sListPage
      kind="externalsecret"
      title="ExternalSecrets"
      tagline="External-Secrets ExternalSecrets — sync openbao→K8s Secret. Store / refresh / Ready state."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Store',
          extract: (o) => {
            const ref = (o.spec as Record<string, unknown> | undefined)?.[
              'secretStoreRef'
            ] as Record<string, unknown> | undefined
            return (ref?.['name'] as string | undefined) ?? '—'
          },
        },
        colSpec('refreshInterval', 'Refresh'),
        colReconcileStatus('Ready'),
        COL_AGE,
      ]}
    />
  )
}

export function CnpgClustersListPage() {
  return (
    <K8sListPage
      kind="cnpgcluster"
      title="CNPG Clusters"
      tagline="CloudNativePG Postgres Clusters. Instances / primary / reconcile state."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        colSpec('instances', 'Instances'),
        {
          header: 'Primary',
          extract: (o) => {
            const st = o.status as Record<string, unknown> | undefined
            return (
              (st?.['currentPrimary'] as string | undefined) ??
              (st?.['targetPrimary'] as string | undefined) ??
              '—'
            )
          },
        },
        colCnpgStatus('Status'),
        COL_AGE,
      ]}
    />
  )
}

export function CnpgDatabasesListPage() {
  return (
    <K8sListPage
      kind="cnpgdatabase"
      title="CNPG Databases"
      tagline="CloudNativePG Database CRs — declarative database lifecycle on a CNPG Cluster."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Cluster',
          extract: (o) => {
            const ref = (o.spec as Record<string, unknown> | undefined)?.[
              'cluster'
            ] as Record<string, unknown> | undefined
            return (ref?.['name'] as string | undefined) ?? '—'
          },
        },
        colSpec('name', 'DB Name'),
        colReconcileStatus('Ready'),
        COL_AGE,
      ]}
    />
  )
}

export function ApplicationsListPage() {
  return (
    <K8sListPage
      kind="application"
      title="Applications"
      tagline="Catalyst Application CRs — installed Blueprints per Environment. Blueprint / reconcile state."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Blueprint',
          extract: (o) => {
            const spec = o.spec as Record<string, unknown> | undefined
            return (
              (spec?.['blueprint'] as string | undefined) ??
              (spec?.['blueprintRef'] as Record<string, unknown> | undefined)?.[
                'name'
              ] as string | undefined ??
              '—'
            )
          },
        },
        colCatalystCrStatus('Status'),
        COL_AGE,
      ]}
    />
  )
}

export function EnvironmentsListPage() {
  return (
    <K8sListPage
      kind="environment"
      title="Environments"
      tagline="Catalyst Environment CRs — logical env per Organization. Org / type / reconcile state."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Organization',
          extract: (o) => {
            const spec = o.spec as Record<string, unknown> | undefined
            return (
              (spec?.['organization'] as string | undefined) ??
              (spec?.['organizationRef'] as
                | Record<string, unknown>
                | undefined)?.['name'] as string | undefined ??
              '—'
            )
          },
        },
        colSpec('type', 'Type'),
        colCatalystCrStatus('Status'),
        COL_AGE,
      ]}
    />
  )
}

export function OrganizationsListPage() {
  return (
    <K8sListPage
      kind="organization"
      title="Organizations"
      tagline="Catalyst Organization CRs — top-level tenancy. Display name / reconcile state."
      columns={[
        COL_NAME,
        {
          header: 'Display Name',
          extract: (o) => {
            const spec = o.spec as Record<string, unknown> | undefined
            return (
              (spec?.['displayName'] as string | undefined) ??
              (spec?.['name'] as string | undefined) ??
              '—'
            )
          },
        },
        colCatalystCrStatus('Status'),
        COL_AGE,
      ]}
    />
  )
}

export function ContinuumsListPage() {
  return (
    <K8sListPage
      kind="continuum"
      title="Continuums"
      tagline="Catalyst Continuum DR CRs — multi-region failover orchestration. Mode / reconcile state."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Mode',
          extract: (o) => {
            const spec = o.spec as Record<string, unknown> | undefined
            return (
              (spec?.['mode'] as string | undefined) ??
              (spec?.['topology'] as string | undefined) ??
              '—'
            )
          },
        },
        colCatalystCrStatus('Status'),
        COL_AGE,
      ]}
    />
  )
}

export function UserAccessesListPage() {
  return (
    <K8sListPage
      kind="useraccess"
      title="User Access"
      tagline="Catalyst UserAccess CRs — RBAC bindings per User. Subject / role / reconcile state."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Subject',
          extract: (o) => {
            const spec = o.spec as Record<string, unknown> | undefined
            return (
              (spec?.['email'] as string | undefined) ??
              (spec?.['subject'] as string | undefined) ??
              (spec?.['user'] as string | undefined) ??
              '—'
            )
          },
        },
        {
          header: 'Role',
          extract: (o) => {
            const spec = o.spec as Record<string, unknown> | undefined
            return (
              (spec?.['role'] as string | undefined) ??
              (spec?.['accessLevel'] as string | undefined) ??
              '—'
            )
          },
        },
        colCatalystCrStatus('Status'),
        COL_AGE,
      ]}
    />
  )
}
