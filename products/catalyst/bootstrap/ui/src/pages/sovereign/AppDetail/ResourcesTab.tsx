/**
 * ResourcesTab — live K8s resource view scoped to the focused
 * Application.
 *
 * For each canonical kind (Deployment, Service, Ingress, Pod,
 * ConfigMap, Secret, PVC) the tab fires a TanStack Query against the
 * catalyst-api list surface
 *   GET /api/v1/sovereigns/{id}/k8s/{kind}?namespace=...&labelSelector=...
 * filtered by `app.kubernetes.io/instance=<applicationName>` (the
 * standard Helm-installed identity label).
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (target-state, no MVP) — every kind renders its own table the
 *      first time, no quick-link redirect placeholder.
 *   #4 (never hardcode) — every URL flows through API_BASE.
 *
 * Test seam: matrix TC-072 asserts `Deployment`, `Service`, `Pod`
 * tokens in the rendered body. The headers (`<th>` text) carry these
 * literals on every render even when the API list returns zero items.
 */

import { useQuery } from '@tanstack/react-query'

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { useResolvedDeploymentId } from '@/shared/lib/useResolvedDeploymentId'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'

interface KindSpec {
  /** Plural URL segment used to build the resource-detail href. */
  plural: string
  /** Canonical singular kind segment used by the catalyst-api k8s registry. */
  singular: string
  /** Header label rendered above the table. */
  label: string
  /** Per-row badge — extracted from the K8sObject. */
  status?: (obj: K8sObject) => string | undefined
}

const KINDS: ReadonlyArray<KindSpec> = [
  {
    plural: 'deployments',
    singular: 'deployment',
    label: 'Deployment',
    status: (o) => {
      const s = (o.status ?? {}) as { readyReplicas?: number; replicas?: number }
      return `${s.readyReplicas ?? 0}/${s.replicas ?? 0}`
    },
  },
  {
    plural: 'services',
    singular: 'service',
    label: 'Service',
    status: (o) => {
      const spec = (o.spec ?? {}) as { type?: string; clusterIP?: string }
      return spec.type ?? spec.clusterIP ?? ''
    },
  },
  {
    plural: 'ingresses',
    singular: 'ingress',
    label: 'Ingress',
    status: (o) => {
      const spec = (o.spec ?? {}) as { rules?: Array<{ host?: string }> }
      return spec.rules?.[0]?.host ?? ''
    },
  },
  {
    plural: 'pods',
    singular: 'pod',
    label: 'Pod',
    status: (o) => {
      const s = (o.status ?? {}) as { phase?: string }
      return s.phase ?? ''
    },
  },
  {
    plural: 'configmaps',
    singular: 'configmap',
    label: 'ConfigMap',
  },
  {
    plural: 'secrets',
    singular: 'secret',
    label: 'Secret',
    status: (o) => ((o as Record<string, unknown>).type as string) ?? '',
  },
  {
    plural: 'persistentvolumeclaims',
    singular: 'persistentvolumeclaim',
    label: 'PersistentVolumeClaim',
    status: (o) => {
      const s = (o.status ?? {}) as { phase?: string }
      return s.phase ?? ''
    },
  },
]

async function fetchKindList(
  sovereignId: string,
  kind: string,
  namespace: string,
  labelSelector: string,
  signal?: AbortSignal,
): Promise<K8sObject[]> {
  const url = `${API_BASE}/v1/sovereigns/${encodeURIComponent(
    sovereignId,
  )}/k8s/${encodeURIComponent(kind)}?namespace=${encodeURIComponent(
    namespace,
  )}&labelSelector=${encodeURIComponent(labelSelector)}&limit=100`
  const res = await authedFetch(url, { signal })
  if (!res.ok) {
    throw new Error(`list ${kind}: HTTP ${res.status}`)
  }
  const body = (await res.json()) as { items?: K8sObject[] }
  return body.items ?? []
}

export interface ResourcesTabProps {
  applicationName: string
  /** Sovereign id (the URL segment in /sovereigns/{id}/k8s/...). */
  sovereignId: string
  /**
   * Install namespace — the namespace the workload's pods/services
   * actually live in. For Application CRs this is `spec.targetNamespace`;
   * for bootstrap-kit HRs this is `spec.targetNamespace` (the HR may
   * be in flux-system, the workload is in `alloy/`, `cert-manager/`,
   * `kube-system/`, etc).
   *
   * Family B (2026-05-17 t10 C4-005): previously this prop was wired
   * to the Application CR's *own* namespace, which on chroot
   * Sovereigns defaulted to "default" — so every list query missed
   * every install. Now wired from `apiApp.targetNamespace`.
   */
  namespace: string
  /**
   * Family B (2026-05-17 t10 C4-005): pod/service identity label.
   * - Wizard-installed Application CRs:
   *   `app.kubernetes.io/instance=<applicationName>` (catalyst standard)
   * - Bootstrap-kit HRs:
   *   `app.kubernetes.io/name=<chartName>` (upstream Helm standard;
   *   `instance` is set by Flux to the HR name but upstream pod
   *   manifests use `name` for the canonical identity).
   *
   * The backend chooses the right one per source and hands it back
   * verbatim. Defaults to `instance=<applicationName>` for callers
   * that haven't been migrated yet (backwards-compatible).
   */
  labelSelector?: string
  /** Test seam — bypass network calls. */
  disableNetwork?: boolean
}

export function ResourcesTab({
  applicationName,
  sovereignId,
  namespace,
  labelSelector,
  disableNetwork = false,
}: ResourcesTabProps) {
  const { deploymentId: chrootDepId } = useResolvedDeploymentId()
  const deploymentId = chrootDepId ?? sovereignId
  const cloudPath =
    DETECTED_MODE.mode === 'sovereign' || !deploymentId
      ? '/cloud'
      : `/provision/${deploymentId}/cloud`
  const effectiveLabelSelector =
    labelSelector?.trim() || `app.kubernetes.io/instance=${applicationName}`

  return (
    <div className="resources-tab" data-testid="app-tab-resources-panel-content">
      <div className="resources-header">
        <p className="resources-intro" data-testid="app-resources-filter-banner">
          Live K8s objects backing{' '}
          <code className="font-mono text-[var(--color-text)]">{applicationName}</code>{' '}
          (filtered by <code>{effectiveLabelSelector}</code> in namespace{' '}
          <code>{namespace}</code>).
        </p>
      </div>

      {KINDS.map((kind) => (
        <ResourceKindTable
          key={kind.singular}
          kind={kind}
          sovereignId={sovereignId}
          namespace={namespace}
          labelSelector={effectiveLabelSelector}
          disableNetwork={disableNetwork}
          cloudPath={cloudPath}
        />
      ))}

      <style>{`
        .resources-tab { display: flex; flex-direction: column; gap: 1rem; }
        .resources-header { padding-bottom: 0.4rem; border-bottom: 1px solid var(--color-border); }
        .resources-intro { margin: 0; font-size: 0.82rem; color: var(--color-text-dim); }
        .resource-kind-block {
          border: 1px solid var(--color-border);
          border-radius: 6px;
          background: var(--color-surface);
          overflow: hidden;
        }
        .resource-kind-header {
          display: flex; align-items: center; justify-content: space-between;
          padding: 0.45rem 0.7rem;
          background: color-mix(in srgb, var(--color-border) 30%, transparent);
        }
        .resource-kind-title {
          margin: 0; font-size: 0.85rem; font-weight: 600;
          color: var(--color-text-strong);
        }
        .resource-kind-count {
          font-size: 0.72rem; color: var(--color-text-dim);
          padding: 0.1rem 0.5rem; border-radius: 999px;
          background: color-mix(in srgb, var(--color-border) 60%, transparent);
        }
        .resource-table { width: 100%; border-collapse: collapse; font-size: 0.82rem; }
        .resource-table th {
          text-align: left; font-weight: 600; color: var(--color-text-dim);
          padding: 0.35rem 0.7rem; border-bottom: 1px solid var(--color-border);
          text-transform: uppercase; letter-spacing: 0.04em; font-size: 0.7rem;
        }
        .resource-table td {
          padding: 0.4rem 0.7rem;
          border-bottom: 1px solid var(--color-border);
          color: var(--color-text);
        }
        .resource-table tr:last-child td { border-bottom: none; }
        .resource-table a { color: var(--color-accent); text-decoration: none; }
        .resource-table a:hover { text-decoration: underline; }
        .resource-empty {
          padding: 0.6rem 0.7rem;
          font-size: 0.78rem; color: var(--color-text-dim);
          font-style: italic;
        }
        .resource-error {
          padding: 0.6rem 0.7rem;
          font-size: 0.78rem; color: var(--color-danger);
        }
      `}</style>
    </div>
  )
}

interface ResourceKindTableProps {
  kind: KindSpec
  sovereignId: string
  namespace: string
  labelSelector: string
  disableNetwork: boolean
  cloudPath: string
}

function ResourceKindTable({
  kind,
  sovereignId,
  namespace,
  labelSelector,
  disableNetwork,
  cloudPath,
}: ResourceKindTableProps) {
  const q = useQuery({
    queryKey: ['app-resources', sovereignId, namespace, labelSelector, kind.singular],
    queryFn: ({ signal }) =>
      fetchKindList(sovereignId, kind.singular, namespace, labelSelector, signal),
    enabled: !disableNetwork && !!sovereignId && !!namespace && !!labelSelector,
    refetchInterval: 30_000,
    staleTime: 15_000,
  })

  const items = q.data ?? []

  return (
    <div
      className="resource-kind-block"
      data-testid={`app-resources-block-${kind.singular}`}
    >
      <div className="resource-kind-header">
        <h4
          className="resource-kind-title"
          data-testid={`app-resources-kind-${kind.singular}`}
        >
          {kind.label}
        </h4>
        <span
          className="resource-kind-count"
          data-testid={`app-resources-count-${kind.singular}`}
        >
          {q.isPending ? '…' : items.length}
        </span>
      </div>

      {q.isError ? (
        <p className="resource-error" data-testid={`app-resources-error-${kind.singular}`}>
          {(q.error as Error)?.message ?? 'list failed'}
        </p>
      ) : items.length === 0 && !q.isPending ? (
        <p className="resource-empty" data-testid={`app-resources-empty-${kind.singular}`}>
          No {kind.label} objects.
        </p>
      ) : (
        <table className="resource-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Namespace</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            {items.map((obj) => {
              // HOST coordinates — these resolve against the host
              // apiserver (the mothership holds only the host
              // kubeconfig), so the resource-tree drill-down href and
              // the React key MUST use them.
              const hostName = obj.metadata?.name ?? '?'
              const hostNs = obj.metadata?.namespace ?? namespace
              // #3939 (#3642 sibling): prefer the de-mangled in-vCluster
              // identity for DISPLAY. For pre-#3642 (non-synced) rows the
              // catalyst-api omits these fields, so we fall back to the
              // host coordinates and the rendering is unchanged.
              const displayName = obj.displayName ?? hostName
              const displayNs = obj.vclusterNamespace ?? hostNs
              const status = kind.status ? kind.status(obj) ?? '' : ''
              const href = `${cloudPath}/resource/${encodeURIComponent(
                kind.plural,
              )}/${encodeURIComponent(hostNs)}/${encodeURIComponent(
                hostName,
              )}/overview`
              return (
                <tr
                  key={`${hostNs}/${hostName}`}
                  data-testid={`app-detail-resource-row-${kind.singular}-${hostNs}-${hostName}`}
                >
                  <td>
                    <a href={href} data-host-name={hostName} data-host-namespace={hostNs}>
                      {displayName}
                    </a>
                  </td>
                  <td>{displayNs}</td>
                  <td>{status}</td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
    </div>
  )
}
