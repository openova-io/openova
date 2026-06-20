/**
 * cloud-list/kindsPages.tsx — page wrapper components for K8s-backed
 * kinds. Lives in a `.tsx` file because kinds.ts is a `.ts` (no JSX
 * allowed) per the existing project convention.
 *
 * Each wrapper picks the column set + tagline for one kind and
 * delegates rendering to the generic K8sListPage.
 */

import {
  K8sListPage,
  COL_NAME,
  COL_NAMESPACE,
  COL_AGE,
  colSpec,
  colStatus,
  colReconcileStatus,
  colCnpgStatus,
  colCatalystCrStatus,
} from './K8sListPage'

export function PodsListPage() {
  return (
    <K8sListPage
      kind="pod"
      title="Pods"
      tagline="Running containers across all namespaces."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        colStatus('phase', 'Phase'),
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
        colSpec('replicas', 'Replicas'),
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
        colSpec('replicas', 'Replicas'),
        colStatus('readyReplicas', 'Ready'),
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
        colStatus('numberReady', 'Ready'),
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
        colStatus('readyReplicas', 'Ready'),
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
        colStatus('phase', 'Phase'),
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
        colStatus('phase', 'Phase'),
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

export function StorageClassesListPage() {
  return (
    <div className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-6 text-sm text-[var(--color-text-dim)]">
      <h2 className="mb-1 text-lg font-semibold text-[var(--color-text-strong)]">Storage Classes</h2>
      <p>
        The <code className="font-mono">storage.k8s.io/StorageClass</code> GVR is not yet in the
        catalyst-api k8scache registry. Tracking work to add it lives in
        {' '}
        <a href="https://github.com/openova-io/openova/issues/321" className="underline">
          issue #321
        </a>
        .
      </p>
    </div>
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
export function PolicyReportsListPage() {
  return (
    <K8sListPage
      kind="policyreport"
      title="Policy Reports"
      tagline="Per-namespace Kyverno PolicyReport evaluations — pass / fail counts per resource."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
        {
          header: 'Pass',
          extract: (o) => {
            const s = (o['summary'] as Record<string, unknown> | undefined) ?? {}
            const v = s['pass']
            return v == null ? '—' : String(v)
          },
        },
        {
          header: 'Fail',
          extract: (o) => {
            const s = (o['summary'] as Record<string, unknown> | undefined) ?? {}
            const v = s['fail']
            return v == null ? '—' : String(v)
          },
        },
        {
          header: 'Warn',
          extract: (o) => {
            const s = (o['summary'] as Record<string, unknown> | undefined) ?? {}
            const v = s['warn']
            return v == null ? '—' : String(v)
          },
        },
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
        {
          header: 'Pass',
          extract: (o) => {
            const s = (o['summary'] as Record<string, unknown> | undefined) ?? {}
            const v = s['pass']
            return v == null ? '—' : String(v)
          },
        },
        {
          header: 'Fail',
          extract: (o) => {
            const s = (o['summary'] as Record<string, unknown> | undefined) ?? {}
            const v = s['fail']
            return v == null ? '—' : String(v)
          },
        },
        {
          header: 'Warn',
          extract: (o) => {
            const s = (o['summary'] as Record<string, unknown> | undefined) ?? {}
            const v = s['warn']
            return v == null ? '—' : String(v)
          },
        },
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
      tagline="Flux HelmReleases — one per installed Blueprint. Chart / revision / reconcile state."
      columns={[
        COL_NAMESPACE,
        COL_NAME,
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
