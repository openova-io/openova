/**
 * ConsoleCloudPage — Sovereign Console /console/cloud.
 *
 * Reads /api/v1/sovereign/cloud (issue #933). Six lists in one response:
 *
 *   - Nodes (kubectl get nodes)
 *   - Namespaces
 *   - Ingresses (networking.k8s.io/v1)
 *   - HTTPRoutes (gateway.networking.k8s.io/v1)
 *   - LoadBalancer-typed Services
 *   - StorageClasses + PVCs
 *
 * Renders each list as its own collapsible section so an operator's
 * mental model maps 1:1 to the cluster's actual surface area. The
 * Console Cloud page is the operator's "where do I click to reach X"
 * surface — Ingresses + HTTPRoutes carry an https://hostname URL
 * affordance that opens the in-cluster service in a new tab.
 */

import { useEffect, useState } from 'react'
import {
  Cloud,
  Server,
  Box,
  Globe,
  Network,
  HardDrive,
  Database,
  RefreshCw,
  AlertCircle,
  ExternalLink,
} from 'lucide-react'
import { API_BASE } from '@/shared/config/urls'
import { loadTokens } from '@/shared/lib/oidc'

interface NodeRow {
  name: string
  status: string
  roles: string[]
  kubeletVersion: string
  os: string
  arch: string
  internalIP: string
  externalIP?: string
  capacityCPU: string
  capacityMemory: string
}
interface NamespaceRow {
  name: string
  status: string
  createdAt: string
}
interface IngressRow {
  name: string
  namespace: string
  hosts: string[]
  url?: string
}
interface LBRow {
  name: string
  namespace: string
  type: string
  clusterIP?: string
  externalIP?: string
  ports: string[]
}
interface SCRow {
  name: string
  provisioner: string
  isDefault: boolean
  reclaimPolicy: string
}
interface PVCRow {
  name: string
  namespace: string
  storageClass: string
  capacity: string
  status: string
}

interface CloudResponse {
  nodes: NodeRow[]
  namespaces: NamespaceRow[]
  ingresses: IngressRow[]
  httpRoutes: IngressRow[]
  loadBalancers: LBRow[]
  storageClasses: SCRow[]
  pvcs: PVCRow[]
}

type PageState =
  | { status: 'loading' }
  | { status: 'loaded'; data: CloudResponse }
  | { status: 'error'; message: string }

async function fetchCloud(): Promise<CloudResponse> {
  const tokens = loadTokens()
  const resp = await fetch(`${API_BASE}/v1/sovereign/cloud`, {
    headers: {
      Accept: 'application/json',
      ...(tokens ? { Authorization: `Bearer ${tokens.accessToken}` } : {}),
    },
  })
  if (!resp.ok) throw new Error(`status ${resp.status}`)
  return (await resp.json()) as CloudResponse
}

function Section({
  title,
  icon,
  count,
  children,
  testId,
}: {
  title: string
  icon: React.ReactNode
  count: number
  children: React.ReactNode
  testId: string
}) {
  return (
    <section
      className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-5"
      data-testid={testId}
    >
      <div className="mb-3 flex items-center gap-2">
        <div className="flex h-7 w-7 items-center justify-center rounded-lg bg-[var(--color-bg)]">
          {icon}
        </div>
        <h2 className="text-sm font-semibold text-[var(--color-text-strong)]">{title}</h2>
        <span className="text-xs text-[var(--color-text-dim)]">({count})</span>
      </div>
      {children}
    </section>
  )
}

function EmptyRow({ label }: { label: string }) {
  return <p className="text-xs text-[var(--color-text-dim)]">{label}</p>
}

export function ConsoleCloudPage() {
  const [state, setState] = useState<PageState>({ status: 'loading' })

  const reload = () => {
    setState({ status: 'loading' })
    fetchCloud()
      .then((data) => setState({ status: 'loaded', data }))
      .catch((err: unknown) => {
        const msg = err instanceof Error ? err.message : 'Unknown error'
        setState({ status: 'error', message: msg })
      })
  }

  useEffect(() => {
    reload()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  return (
    <div data-testid="console-cloud-page">
      <div className="mb-6 flex items-start justify-between gap-4">
        <div>
          <h1 className="text-2xl font-semibold text-[var(--color-text-strong)]">Cloud</h1>
          <p className="mt-1 text-sm text-[var(--color-text-dim)]">
            Live topology of this Sovereign cluster — nodes, namespaces, ingress, storage.
          </p>
        </div>
        <button
          type="button"
          onClick={reload}
          className="flex h-9 items-center gap-2 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] px-3 text-sm text-[var(--color-text)] hover:bg-[var(--color-bg-3)]"
          data-testid="cloud-refresh"
        >
          <RefreshCw className="h-4 w-4" />
          Refresh
        </button>
      </div>

      {state.status === 'loading' ? (
        <div className="flex items-center gap-2 text-sm text-[var(--color-text-dim)]" data-testid="cloud-loading">
          <RefreshCw className="h-4 w-4 animate-spin" />
          Loading cluster topology…
        </div>
      ) : state.status === 'error' ? (
        <div
          className="rounded-xl border border-[var(--color-border)] bg-[var(--color-bg-2)] p-8 text-center"
          data-testid="cloud-error"
        >
          <AlertCircle className="mx-auto mb-3 h-10 w-10 text-red-400" />
          <p className="text-sm font-medium text-[var(--color-text)]">Couldn’t load cluster topology</p>
          <p className="mt-1 text-xs text-[var(--color-text-dim)]">{state.message}</p>
        </div>
      ) : (
        <div className="grid grid-cols-1 gap-4 lg:grid-cols-2">
          {/* Nodes */}
          <Section
            title="Nodes"
            icon={<Server className="h-4 w-4 text-blue-400" />}
            count={state.data.nodes.length}
            testId="cloud-nodes"
          >
            {state.data.nodes.length === 0 ? (
              <EmptyRow label="No nodes visible." />
            ) : (
              <div className="space-y-2">
                {state.data.nodes.map((n) => (
                  <div
                    key={n.name}
                    className="rounded-lg border border-[var(--color-border)] bg-[var(--color-bg)] p-3 text-xs"
                    data-testid={`cloud-node-${n.name}`}
                  >
                    <div className="flex items-center justify-between">
                      <span className="font-semibold text-[var(--color-text-strong)]">{n.name}</span>
                      <span
                        className={`text-[10px] uppercase ${n.status === 'Ready' ? 'text-green-400' : 'text-red-400'}`}
                      >
                        {n.status}
                      </span>
                    </div>
                    <div className="mt-1 grid grid-cols-2 gap-x-3 text-[var(--color-text-dim)]">
                      <span>{n.roles.join(', ') || 'worker'}</span>
                      <span>{n.kubeletVersion}</span>
                      <span>{n.internalIP}</span>
                      <span>
                        {n.capacityCPU} cores · {n.capacityMemory}
                      </span>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </Section>

          {/* Namespaces */}
          <Section
            title="Namespaces"
            icon={<Box className="h-4 w-4 text-purple-400" />}
            count={state.data.namespaces.length}
            testId="cloud-namespaces"
          >
            {state.data.namespaces.length === 0 ? (
              <EmptyRow label="No namespaces visible." />
            ) : (
              <ul className="grid grid-cols-2 gap-1 text-xs">
                {state.data.namespaces.map((n) => (
                  <li
                    key={n.name}
                    className="truncate text-[var(--color-text)]"
                    data-testid={`cloud-ns-${n.name}`}
                  >
                    {n.name}{' '}
                    <span className="text-[10px] text-[var(--color-text-dim)]">{n.status}</span>
                  </li>
                ))}
              </ul>
            )}
          </Section>

          {/* Ingresses */}
          <Section
            title="Ingresses"
            icon={<Globe className="h-4 w-4 text-amber-400" />}
            count={state.data.ingresses.length}
            testId="cloud-ingresses"
          >
            {state.data.ingresses.length === 0 ? (
              <EmptyRow label="No ingresses." />
            ) : (
              <ul className="space-y-1 text-xs">
                {state.data.ingresses.map((i) => (
                  <li key={`${i.namespace}/${i.name}`} className="flex items-center gap-2">
                    <span className="font-medium text-[var(--color-text-strong)]">{i.name}</span>
                    <span className="text-[var(--color-text-dim)]">{i.namespace}</span>
                    {i.url ? (
                      <a
                        href={i.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="ml-auto inline-flex items-center gap-1 text-[var(--color-accent)] hover:underline"
                      >
                        {i.hosts[0]}
                        <ExternalLink className="h-3 w-3" />
                      </a>
                    ) : null}
                  </li>
                ))}
              </ul>
            )}
          </Section>

          {/* HTTPRoutes */}
          <Section
            title="HTTPRoutes"
            icon={<Globe className="h-4 w-4 text-green-400" />}
            count={state.data.httpRoutes.length}
            testId="cloud-httproutes"
          >
            {state.data.httpRoutes.length === 0 ? (
              <EmptyRow label="No HTTPRoutes (Gateway API not in use)." />
            ) : (
              <ul className="space-y-1 text-xs">
                {state.data.httpRoutes.map((i) => (
                  <li key={`${i.namespace}/${i.name}`} className="flex items-center gap-2">
                    <span className="font-medium text-[var(--color-text-strong)]">{i.name}</span>
                    <span className="text-[var(--color-text-dim)]">{i.namespace}</span>
                    {i.url ? (
                      <a
                        href={i.url}
                        target="_blank"
                        rel="noopener noreferrer"
                        className="ml-auto inline-flex items-center gap-1 text-[var(--color-accent)] hover:underline"
                      >
                        {i.hosts[0]}
                        <ExternalLink className="h-3 w-3" />
                      </a>
                    ) : null}
                  </li>
                ))}
              </ul>
            )}
          </Section>

          {/* Load balancers */}
          <Section
            title="Load Balancers"
            icon={<Network className="h-4 w-4 text-sky-400" />}
            count={state.data.loadBalancers.length}
            testId="cloud-loadbalancers"
          >
            {state.data.loadBalancers.length === 0 ? (
              <EmptyRow label="No LoadBalancer services." />
            ) : (
              <ul className="space-y-1 text-xs">
                {state.data.loadBalancers.map((lb) => (
                  <li key={`${lb.namespace}/${lb.name}`} className="text-[var(--color-text)]">
                    <span className="font-medium text-[var(--color-text-strong)]">{lb.name}</span>{' '}
                    <span className="text-[var(--color-text-dim)]">
                      {lb.namespace} · {lb.externalIP || 'pending'} · {lb.ports.join(', ')}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Section>

          {/* Storage classes */}
          <Section
            title="Storage Classes"
            icon={<HardDrive className="h-4 w-4 text-orange-400" />}
            count={state.data.storageClasses.length}
            testId="cloud-storage-classes"
          >
            {state.data.storageClasses.length === 0 ? (
              <EmptyRow label="No storage classes." />
            ) : (
              <ul className="space-y-1 text-xs">
                {state.data.storageClasses.map((sc) => (
                  <li key={sc.name} className="text-[var(--color-text)]">
                    <span className="font-medium text-[var(--color-text-strong)]">{sc.name}</span>{' '}
                    <span className="text-[var(--color-text-dim)]">
                      {sc.provisioner}
                      {sc.isDefault ? ' · default' : ''}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Section>

          {/* PVCs */}
          <Section
            title="Persistent Volume Claims"
            icon={<Database className="h-4 w-4 text-rose-400" />}
            count={state.data.pvcs.length}
            testId="cloud-pvcs"
          >
            {state.data.pvcs.length === 0 ? (
              <EmptyRow label="No PVCs." />
            ) : (
              <ul className="space-y-1 text-xs">
                {state.data.pvcs.map((p) => (
                  <li key={`${p.namespace}/${p.name}`} className="text-[var(--color-text)]">
                    <span className="font-medium text-[var(--color-text-strong)]">{p.name}</span>{' '}
                    <span className="text-[var(--color-text-dim)]">
                      {p.namespace} · {p.capacity} · {p.storageClass} · {p.status}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </Section>
        </div>
      )}

      {state.status === 'loaded' &&
      state.data.nodes.length === 0 &&
      state.data.namespaces.length === 0 ? (
        <div className="mt-4 rounded-lg border border-[var(--color-border)] bg-[var(--color-bg-2)] p-4 text-center">
          <Cloud className="mx-auto mb-2 h-8 w-8 text-[var(--color-text-dim)]" />
          <p className="text-xs text-[var(--color-text-dim)]">
            The cluster appears empty. If you just provisioned, give Flux a minute to reconcile.
          </p>
        </div>
      ) : null}
    </div>
  )
}
