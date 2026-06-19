/**
 * LogsTab — live Pod log viewer for the focused Application.
 *
 * Picks the Pods that belong to this Application via the matching
 * `app.kubernetes.io/instance=<appName>` label, then exposes a Pod +
 * container picker and streams the chosen container's log over the
 * catalyst-api WebSocket surface
 *   GET /api/v1/sovereigns/{id}/k8s/logs/{ns}/{pod}/{container}
 *
 * For tests / SSR / Playwright body assertions there is ALWAYS a
 * static header row that surfaces the Application's name and
 * blueprint — TC-073 asserts the literal `Logs` + `wordpress` tokens
 * regardless of whether the WS connection has produced any frames yet.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #1 (target-state, no MVP) — real WS stream first, no placeholder.
 *   #4 (never hardcode) — every URL flows through API_BASE.
 */

import { useEffect, useMemo, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'

import { API_BASE } from '@/shared/config/urls'
import { authedFetch } from '@/shared/lib/authedFetch'
import type { K8sObject } from '@/widgets/architecture-graph/useK8sCacheStream'
import { loadTokens } from '@/shared/lib/oidc'

export interface LogsTabProps {
  /** Application name (matches app.kubernetes.io/instance). */
  applicationName: string
  /** Sovereign id (the URL segment in /sovereigns/{id}/k8s/...). */
  sovereignId: string
  /**
   * Install namespace — where the workload's pods actually live.
   * For Application CRs this is `spec.targetNamespace`; for
   * bootstrap-kit HRs this is the HR's `spec.targetNamespace`.
   *
   * Family B (2026-05-17 t10 C4-007): previously this was always
   * "default" on chroot Sovereigns — so log queries returned zero
   * pods even though the pods were Running in the real namespace.
   */
  namespace: string
  /**
   * Family B (2026-05-17 t10 C4-007): pod identity label.
   * - Wizard installs:    `app.kubernetes.io/instance=<applicationName>`
   * - Bootstrap-kit HRs:  `app.kubernetes.io/name=<chartName>`
   *
   * Backend hands back the right one per source. Defaults to
   * `instance=<applicationName>` when omitted (backwards-compatible
   * with wizard-installed apps that already work).
   */
  labelSelector?: string
  /** Blueprint name (used in the human header — e.g. `bp-wordpress`). */
  blueprint?: string
  /** Test seam — bypass network calls (used in unit tests). */
  disableNetwork?: boolean
}

interface PodOption {
  /**
   * HOST pod name — the value passed into the log-stream WS URL, which
   * resolves against the HOST apiserver (the mothership holds only the
   * host kubeconfig). For mgmt-vCluster-synced pods this is the loft
   * syncer's mangled name (`<pod>-x-gitea-x-mgmt-vcluster`).
   */
  name: string
  /** HOST namespace — same host-coordinate rule as `name`. */
  namespace: string
  /**
   * #3939 (#3642 sibling): de-mangled in-vCluster name to SHOW in the
   * picker (`gitea-75d9f486fb-g8hsr`). Falls back to the host `name`
   * for pre-#3642 (non-synced) pods where the API omits `displayName`.
   */
  displayName: string
  containers: string[]
}

/**
 * Fetch the Pod list scoped to this Application via the canonical
 * GET /api/v1/sovereigns/{id}/k8s/pod?namespace=...&labelSelector=...
 * surface (k8s.go HandleK8sList).
 */
async function fetchAppPods(
  sovereignId: string,
  namespace: string,
  labelSelector: string,
  signal?: AbortSignal,
): Promise<PodOption[]> {
  const url = `${API_BASE}/v1/sovereigns/${encodeURIComponent(
    sovereignId,
  )}/k8s/pod?namespace=${encodeURIComponent(namespace)}&labelSelector=${encodeURIComponent(
    labelSelector,
  )}&limit=50`
  const res = await authedFetch(url, { signal })
  if (!res.ok) {
    throw new Error(`pods list: HTTP ${res.status}`)
  }
  const body = (await res.json()) as { items?: K8sObject[] }
  const pods: PodOption[] = []
  for (const item of body.items ?? []) {
    const podName = item.metadata?.name
    const podNs = item.metadata?.namespace ?? namespace
    if (!podName) continue
    const spec = (item.spec ?? {}) as { containers?: Array<{ name: string }> }
    const containers = (spec.containers ?? []).map((c) => c.name).filter(Boolean)
    // host coords (`name`/`namespace`) drive the WS log-stream URL;
    // `displayName` is the de-mangled label shown in the picker (#3939).
    pods.push({
      name: podName,
      namespace: podNs,
      displayName: item.displayName ?? podName,
      containers,
    })
  }
  return pods
}

export function LogsTab({
  applicationName,
  sovereignId,
  namespace,
  labelSelector,
  blueprint,
  disableNetwork = false,
}: LogsTabProps) {
  const effectiveLabelSelector =
    labelSelector?.trim() || `app.kubernetes.io/instance=${applicationName}`
  const podsQ = useQuery({
    queryKey: ['app-logs-pods', sovereignId, namespace, effectiveLabelSelector],
    queryFn: ({ signal }) =>
      fetchAppPods(sovereignId, namespace, effectiveLabelSelector, signal),
    enabled: !disableNetwork && !!sovereignId && !!namespace && !!effectiveLabelSelector,
    refetchInterval: 30_000,
    staleTime: 15_000,
  })

  const pods: PodOption[] = useMemo(() => podsQ.data ?? [], [podsQ.data])
  const [selectedPod, setSelectedPod] = useState<string>('')
  const [selectedContainer, setSelectedContainer] = useState<string>('')

  // Auto-select the first pod + container when the list resolves.
  useEffect(() => {
    if (pods.length === 0) return
    if (!selectedPod || !pods.find((p) => p.name === selectedPod)) {
      setSelectedPod(pods[0].name)
    }
  }, [pods, selectedPod])

  useEffect(() => {
    const pod = pods.find((p) => p.name === selectedPod)
    if (!pod) return
    if (!selectedContainer || !pod.containers.includes(selectedContainer)) {
      setSelectedContainer(pod.containers[0] ?? '')
    }
  }, [pods, selectedPod, selectedContainer])

  // ── WebSocket log stream ────────────────────────────────────────
  const [lines, setLines] = useState<string[]>([])
  const [streamState, setStreamState] = useState<
    'idle' | 'connecting' | 'open' | 'closed' | 'error'
  >('idle')
  const wsRef = useRef<WebSocket | null>(null)

  useEffect(() => {
    if (disableNetwork) return
    if (!selectedPod || !selectedContainer || !sovereignId || !namespace) return

    let aborted = false
    setLines([])
    setStreamState('connecting')

    ;(async () => {
      // Browser WebSocket can't carry custom headers; the catalyst-api
      // accepts the access_token query-string fallback for both SSE +
      // WS surfaces (same as useK8sStream/useComplianceStream).
      let accessToken: string | undefined
      try {
        const t = await loadTokens()
        accessToken = t?.accessToken
      } catch {
        // ignore — the catalyst-api allows un-authed Sovereign reads
        // when chrootEnsureDeployment matches.
      }
      if (aborted) return

      const proto =
        typeof window !== 'undefined' && window.location.protocol === 'https:' ? 'wss' : 'ws'
      const host = typeof window !== 'undefined' ? window.location.host : ''
      // API_BASE is `/api` (relative) on the chroot; build an absolute
      // ws://host/api/... URL.
      const apiPath = API_BASE.startsWith('http')
        ? API_BASE.replace(/^https?/, proto)
        : `${proto}://${host}${API_BASE}`
      const params = new URLSearchParams({
        follow: 'true',
        tailLines: '200',
      })
      if (accessToken) params.set('access_token', accessToken)
      const url = `${apiPath}/v1/sovereigns/${encodeURIComponent(
        sovereignId,
      )}/k8s/logs/${encodeURIComponent(namespace)}/${encodeURIComponent(
        selectedPod,
      )}/${encodeURIComponent(selectedContainer)}?${params.toString()}`

      let ws: WebSocket
      try {
        ws = new WebSocket(url)
      } catch {
        if (!aborted) setStreamState('error')
        return
      }
      wsRef.current = ws
      ws.binaryType = 'arraybuffer'

      ws.onopen = () => {
        if (!aborted) setStreamState('open')
      }
      ws.onmessage = (ev) => {
        if (aborted) return
        let text: string
        if (typeof ev.data === 'string') {
          text = ev.data
        } else if (ev.data instanceof ArrayBuffer) {
          text = new TextDecoder('utf-8', { fatal: false }).decode(ev.data)
        } else {
          return
        }
        setLines((prev) => {
          const next = [...prev, text]
          // Cap at 1000 lines to keep memory bounded.
          if (next.length > 1000) next.splice(0, next.length - 1000)
          return next
        })
      }
      ws.onerror = () => {
        if (!aborted) setStreamState('error')
      }
      ws.onclose = () => {
        if (!aborted) setStreamState('closed')
      }
    })()

    return () => {
      aborted = true
      const ws = wsRef.current
      if (ws && ws.readyState !== WebSocket.CLOSED) {
        try {
          ws.close()
        } catch {
          // ignore
        }
      }
      wsRef.current = null
    }
  }, [disableNetwork, selectedPod, selectedContainer, sovereignId, namespace])

  // Header sentence — surfaces both the matrix-asserted `Logs` and
  // `wordpress` tokens (TC-073) on EVERY render, regardless of WS
  // connection state.
  const headerSentence = blueprint
    ? `Logs for ${applicationName} (${blueprint.replace(/^bp-/, '')})`
    : `Logs for ${applicationName}`

  return (
    <div className="logs-tab" data-testid="app-tab-logs-panel-content">
      <div className="logs-header" data-testid="app-logs-header">
        <h3 className="logs-title">{headerSentence}</h3>
        <div className="logs-pickers">
          <label className="logs-picker-label">
            Pod
            <select
              value={selectedPod}
              onChange={(e) => setSelectedPod(e.target.value)}
              data-testid="app-logs-pod-picker"
              disabled={pods.length === 0}
            >
              {pods.length === 0 ? <option value="">no pods found</option> : null}
              {pods.map((p) => (
                // value = HOST pod name (drives the log-stream WS URL);
                // label = de-mangled in-vCluster name (#3939).
                <option key={p.name} value={p.name}>
                  {p.displayName}
                </option>
              ))}
            </select>
          </label>
          <label className="logs-picker-label">
            Container
            <select
              value={selectedContainer}
              onChange={(e) => setSelectedContainer(e.target.value)}
              data-testid="app-logs-container-picker"
              disabled={!selectedPod}
            >
              {(pods.find((p) => p.name === selectedPod)?.containers ?? []).map((c) => (
                <option key={c} value={c}>
                  {c}
                </option>
              ))}
            </select>
          </label>
          <span
            className={`logs-state logs-state-${streamState}`}
            data-testid="app-logs-stream-state"
          >
            {streamState}
          </span>
        </div>
      </div>

      {podsQ.isError ? (
        <p className="logs-empty" data-testid="app-logs-pods-error">
          Failed to list Pods: {(podsQ.error as Error)?.message ?? 'unknown error'}
        </p>
      ) : null}

      {pods.length === 0 && !podsQ.isPending && !podsQ.isError ? (
        <p className="logs-empty" data-testid="app-logs-no-pods">
          No Pods labelled <code>{effectiveLabelSelector}</code> in
          namespace <code>{namespace}</code>.
        </p>
      ) : null}

      <pre className="logs-stream" data-testid="app-logs-stream">
        {lines.length === 0
          ? streamState === 'connecting'
            ? `Connecting to ${selectedPod || 'pod'}…`
            : streamState === 'open'
              ? '(no log lines yet)'
              : streamState === 'error'
                ? '(stream error — see browser console)'
                : streamState === 'closed'
                  ? '(stream closed)'
                  : '(waiting for stream)'
          : lines.join('')}
      </pre>

      <style>{`
        .logs-tab { display: flex; flex-direction: column; gap: 0.6rem; }
        .logs-header {
          display: flex; flex-direction: column; gap: 0.4rem;
          padding: 0.4rem 0;
          border-bottom: 1px solid var(--color-border);
        }
        .logs-title { margin: 0; font-size: 0.95rem; font-weight: 600; color: var(--color-text-strong); }
        .logs-pickers { display: flex; gap: 0.6rem; flex-wrap: wrap; align-items: end; }
        .logs-picker-label {
          display: flex; flex-direction: column; gap: 0.15rem;
          font-size: 0.7rem; color: var(--color-text-dim);
          text-transform: uppercase; letter-spacing: 0.04em;
        }
        .logs-picker-label select {
          background: var(--color-surface); color: var(--color-text);
          border: 1px solid var(--color-border); border-radius: 4px;
          padding: 0.25rem 0.4rem; font-size: 0.8rem; min-width: 12rem;
        }
        .logs-state {
          align-self: end;
          padding: 0.2rem 0.5rem; border-radius: 4px;
          font-size: 0.7rem; text-transform: uppercase;
          background: color-mix(in srgb, var(--color-border) 50%, transparent);
          color: var(--color-text-dim);
        }
        .logs-state-open { background: color-mix(in srgb, var(--color-success) 15%, transparent); color: var(--color-success); }
        .logs-state-error { background: color-mix(in srgb, var(--color-danger) 15%, transparent); color: var(--color-danger); }
        .logs-state-connecting { background: color-mix(in srgb, var(--color-accent) 15%, transparent); color: var(--color-accent); }
        .logs-empty {
          padding: 0.6rem; margin: 0;
          font-size: 0.82rem; color: var(--color-text-dim);
          background: var(--color-surface); border: 1px solid var(--color-border); border-radius: 4px;
        }
        .logs-stream {
          font-family: ui-monospace, SFMono-Regular, "JetBrains Mono", monospace;
          font-size: 0.78rem;
          line-height: 1.45;
          color: var(--color-text);
          background: #0b0d10;
          border: 1px solid var(--color-border);
          border-radius: 4px;
          padding: 0.6rem 0.8rem;
          margin: 0;
          height: 22rem;
          overflow: auto;
          white-space: pre-wrap;
          word-break: break-word;
        }
      `}</style>
    </div>
  )
}
