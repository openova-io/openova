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
