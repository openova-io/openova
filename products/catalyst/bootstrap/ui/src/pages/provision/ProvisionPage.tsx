/**
 * ProvisionPage — real-time provisioning DAG, served as a SPA route at
 * `/sovereign/provision/$deploymentId`. Replaces the legacy static
 * public/provision.html artefact.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #1 (waterfall is the contract — every
 * deliverable in target-state shape) and #4 (never hardcode), this page
 * does not ship any component lists, region-pair mocks, or "Waiting for
 * Flux..." copy. Every bubble is computed at runtime from:
 *
 *   1. ALL_BLUEPRINTS + BOOTSTRAP_KIT — emitted by
 *      scripts/build-catalog.mjs from the monorepo's
 *      platform/<name>/blueprint.yaml + clusters/_template/bootstrap-kit/.
 *      Single source of truth shared with the wizard's StepComponents grid.
 *   2. The wizard zustand store (`useWizardStore`) — provides the operator's
 *      `selectedComponents`, the resolved Sovereign FQDN, and the topology
 *      summary at render time. The deploymentId comes from the URL params,
 *      not the store, so deep-linking to a past provision is supported.
 *   3. The catalyst-api SSE stream at <BASE>api/v1/deployments/<id>/logs.
 *
 * The DAG = one Hetzner-infra supernode → one Flux-bootstrap supernode →
 * bootstrap-kit Blueprints in numerical install order → user-selected
 * Blueprints with transitive hard deps expanded. Phase events from the SSE
 * stream (tofu-init|tofu-plan|tofu-apply|tofu-output|flux-bootstrap) drive
 * the bubble state machine. Raw `tofu` stdout/stderr lines stream into the
 * log panel + the currently-running bubble's expandable detail; the four
 * hcloud_* resource markers (network, firewall, server, load_balancer)
 * advance the Hetzner-infra supernode's internal sub-progress.
 *
 * Per-component states beyond `flux-bootstrap` are NOT emitted by the
 * current catalyst-api — the bootstrap-kit Blueprints reconcile inside the
 * new cluster via Flux Kustomizations. The follow-on path is for
 * catalyst-api to watch those Kustomizations and emit per-Blueprint events
 * on the same SSE stream; this page is already shaped to consume that, see
 * `applyEvent()` — unknown phases beyond flux-bootstrap match by Blueprint
 * id and flip that bubble to running.
 */

import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useParams, useRouter } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { resolveSovereignDomain } from '@/entities/deployment/model'
import { ALL_BLUEPRINTS, BOOTSTRAP_KIT } from '@/shared/constants/catalog.generated'
import { API_BASE } from '@/shared/config/urls'

/* ── Catalog ──────────────────────────────────────────────────────────── */

interface CatalogComponent {
  id: string
  slug: string
  title: string
  summary: string
  category: string | null
  section: string | null
  visibility: string
  depends: string[]
}

interface CatalogBootstrap {
  id: string
  slug: string
  label: string
  file: string
  order: number
}

interface ResolvedCatalog {
  components: CatalogComponent[]
  bootstrapKit: CatalogBootstrap[]
}

/**
 * Strip the typed catalog down to the fields this page consumes — same
 * shape the legacy public/catalog.js used to expose globally, now sourced
 * directly from the typed module so there is one source of truth.
 */
function loadCatalog(): ResolvedCatalog {
  const components: CatalogComponent[] = ALL_BLUEPRINTS.map((b) => ({
    id: b.id,
    slug: b.slug,
    title: b.title,
    summary: b.summary,
    category: b.category,
    section: b.section,
    visibility: b.visibility,
    depends: [...b.depends],
  }))
  const bootstrapKit: CatalogBootstrap[] = BOOTSTRAP_KIT.map((b) => ({
    id: b.id,
    slug: b.slug,
    label: b.label,
    file: b.file,
    order: b.order,
  }))
  return { components, bootstrapKit }
}

/* ── Phase + DAG primitives ───────────────────────────────────────────── */

const HETZNER_INFRA_ID = 'hetzner-infra'
const FLUX_BOOTSTRAP_ID = 'flux-bootstrap'

// OpenTofu phase ids the catalyst-api emits during Phase 0. Lifted from
// products/catalyst/bootstrap/api/internal/provisioner/provisioner.go +
// mirrored in src/shared/constants/bootstrap-phases.ts.
const TOFU_PHASES = new Set(['tofu-init', 'tofu-plan', 'tofu-apply', 'tofu-output'])

// The four Hetzner resource families the OpenTofu module declares — each
// becomes a sub-bubble on the Hetzner-infra supernode and advances when its
// "hcloud_<family>." marker appears in the tofu stdout stream during
// tofu-apply. Order = visual + dependency order.
const HCLOUD_FAMILIES: { id: string; label: string }[] = [
  { id: 'hcloud_network', label: 'Network' },
  { id: 'hcloud_firewall', label: 'Firewall' },
  { id: 'hcloud_server', label: 'Servers' },
  { id: 'hcloud_load_balancer', label: 'Load balancer' },
]

type NodeKind = 'super' | 'bootstrap' | 'component'
type NodeStatus = 'pending' | 'running' | 'done' | 'failed'

interface DagNode {
  id: string
  label: string
  sub: string
  kind: NodeKind
  status: NodeStatus
  prog: number
  hcloudSeen?: Set<string>
  hcloudCounts?: Record<string, number>
}

interface DagEdge {
  from: string
  to: string
}

interface ProvisionEvent {
  time: string
  phase: string
  level?: 'info' | 'warn' | 'error'
  message?: string
}

interface DeploymentSnapshot {
  id?: string
  status?: string
  startedAt?: string
  finishedAt?: string | null
  sovereignFQDN?: string
  region?: string
  error?: string
  result?: {
    sovereignFQDN: string
    controlPlaneIP: string
    loadBalancerIP: string
    consoleURL: string
    gitopsRepoURL: string
  }
}

type StreamStatus = 'connecting' | 'streaming' | 'completed' | 'failed' | 'unreachable'

/* ── Helpers ──────────────────────────────────────────────────────────── */

function normaliseComponentId(id: string | null | undefined): string | null {
  if (typeof id !== 'string') return null
  return id.startsWith('bp-') ? id : `bp-${id}`
}

function expandWithDependencies(seedIds: string[], components: CatalogComponent[]): string[] {
  const byId: Record<string, CatalogComponent> = Object.create(null)
  for (const c of components) byId[c.id] = c
  const visited = new Set<string>()
  const stack: string[] = []
  for (const id of seedIds) {
    const norm = normaliseComponentId(id)
    if (norm && byId[norm]) stack.push(norm)
  }
  while (stack.length > 0) {
    const next = stack.pop()!
    if (visited.has(next)) continue
    visited.add(next)
    const node = byId[next]
    if (!node) continue
    for (const d of node.depends || []) {
      const nd = normaliseComponentId(d)
      if (nd && !visited.has(nd) && byId[nd]) stack.push(nd)
    }
  }
  // Stable order = catalog index ascending so the layout below is reproducible.
  const indexById: Record<string, number> = Object.create(null)
  components.forEach((c, i) => {
    indexById[c.id] = i
  })
  return [...visited].sort((a, b) => (indexById[a] ?? 0) - (indexById[b] ?? 0))
}

interface BuildNodesArgs {
  selectedComponents: string[]
  catalog: ResolvedCatalog
}

function buildNodes({ selectedComponents, catalog }: BuildNodesArgs): DagNode[] {
  const componentsById: Record<string, CatalogComponent> = Object.create(null)
  for (const c of catalog.components) componentsById[c.id] = c
  const bootstrapIds = new Set(catalog.bootstrapKit.map((b) => b.id))

  const out: DagNode[] = []

  // Hetzner-infra supernode — holds the four hcloud_* sub-bubbles, driven
  // by the tofu-* phases.
  out.push({
    id: HETZNER_INFRA_ID,
    label: 'Hetzner infra',
    sub: 'OpenTofu Phase 0',
    kind: 'super',
    status: 'pending',
    prog: 0,
    hcloudSeen: new Set<string>(),
    hcloudCounts: Object.create(null),
  })

  // Flux-bootstrap supernode — cloud-init handoff to in-cluster Flux +
  // Crossplane.
  out.push({
    id: FLUX_BOOTSTRAP_ID,
    label: 'Flux bootstrap',
    sub: 'cloud-init → in-cluster',
    kind: 'super',
    status: 'pending',
    prog: 0,
  })

  // Bootstrap-kit Blueprints, in numeric install order.
  for (const b of catalog.bootstrapKit) {
    const meta = componentsById[b.id]
    out.push({
      id: b.id,
      label: meta?.title || b.label || b.slug,
      sub: meta?.section || 'bootstrap-kit',
      kind: 'bootstrap',
      status: 'pending',
      prog: 0,
    })
  }

  // User selection — drop bootstrap-kit ids (already represented above) and
  // drop unknown ids (catalog might trail an outdated wizard payload).
  const seedIds = selectedComponents
    .map(normaliseComponentId)
    .filter((id): id is string => !!id && !!componentsById[id] && !bootstrapIds.has(id))
  const expanded = expandWithDependencies(seedIds, catalog.components).filter(
    (id) => !bootstrapIds.has(id),
  )
  for (const id of expanded) {
    const meta = componentsById[id]
    if (!meta) continue
    out.push({
      id,
      label: meta.title || meta.slug || id,
      sub: meta.section || 'selected component',
      kind: 'component',
      status: 'pending',
      prog: 0,
    })
  }
  return out
}

function buildEdges(nodes: DagNode[], catalog: ResolvedCatalog): DagEdge[] {
  const idSet = new Set(nodes.map((n) => n.id))
  const edges: DagEdge[] = []
  // Sequential flow: Hetzner → Flux-bootstrap.
  edges.push({ from: HETZNER_INFRA_ID, to: FLUX_BOOTSTRAP_ID })
  // Add Blueprint dep edges among bootstrap + selected components.
  const byId: Record<string, CatalogComponent> = Object.create(null)
  for (const c of catalog.components) byId[c.id] = c
  for (const n of nodes) {
    if (n.kind === 'super') continue
    const meta = byId[n.id]
    if (!meta) continue
    for (const d of meta.depends || []) {
      const dnorm = normaliseComponentId(d)
      if (dnorm && idSet.has(dnorm) && dnorm !== n.id) {
        edges.push({ from: dnorm, to: n.id })
      }
    }
  }
  // Roots (in-degree 0) get an explicit Flux-bootstrap → root edge.
  const inDeg: Record<string, number> = Object.create(null)
  for (const n of nodes) inDeg[n.id] = 0
  for (const e of edges) inDeg[e.to] = (inDeg[e.to] || 0) + 1
  for (const n of nodes) {
    if (n.kind === 'super') continue
    if (inDeg[n.id] === 0) {
      edges.push({ from: FLUX_BOOTSTRAP_ID, to: n.id })
    }
  }
  return edges
}

interface LayoutInfo {
  layer: Record<string, number>
  buckets: Record<number, string[]>
  layers: number[]
}

function computeLayout(nodes: DagNode[], edges: DagEdge[]): LayoutInfo {
  const layer: Record<string, number> = Object.create(null)
  layer[HETZNER_INFRA_ID] = 0
  layer[FLUX_BOOTSTRAP_ID] = 1
  const adj: Record<string, string[]> = Object.create(null)
  const indeg: Record<string, number> = Object.create(null)
  for (const n of nodes) {
    if (n.kind === 'super') continue
    adj[n.id] = []
    indeg[n.id] = 0
  }
  for (const e of edges) {
    if (e.from === HETZNER_INFRA_ID || e.from === FLUX_BOOTSTRAP_ID) continue
    if (e.to === HETZNER_INFRA_ID || e.to === FLUX_BOOTSTRAP_ID) continue
    if (!adj[e.from]) continue
    adj[e.from]!.push(e.to)
    indeg[e.to] = (indeg[e.to] || 0) + 1
  }
  const ready: string[] = []
  for (const id of Object.keys(indeg)) if (indeg[id] === 0) ready.push(id)
  while (ready.length > 0) {
    const id = ready.shift()!
    if (!(id in layer)) layer[id] = 2
    for (const m of adj[id] || []) {
      layer[m] = Math.max(layer[m] || 0, (layer[id] || 2) + 1)
      indeg[m]--
      if (indeg[m] === 0) ready.push(m)
    }
  }
  for (const n of nodes) if (!(n.id in layer)) layer[n.id] = 2
  const buckets: Record<number, string[]> = Object.create(null)
  for (const n of nodes) {
    const L = layer[n.id]!
    if (!buckets[L]) buckets[L] = []
    buckets[L]!.push(n.id)
  }
  return {
    layer,
    buckets,
    layers: Object.keys(buckets).map(Number).sort((a, b) => a - b),
  }
}

/* ── DAG construction wrapper (pure — used by tests) ─────────────────── */

interface DagSpec {
  nodes: DagNode[]
  edges: DagEdge[]
  layoutInfo: LayoutInfo
}

export function buildDag(selectedComponents: string[]): DagSpec {
  const catalog = loadCatalog()
  const nodes = buildNodes({ selectedComponents, catalog })
  const edges = buildEdges(nodes, catalog)
  const layoutInfo = computeLayout(nodes, edges)
  return { nodes, edges, layoutInfo }
}

/* ── SSE event reducer (pure) ─────────────────────────────────────────── */

interface NodeMutationContext {
  nodesById: Record<string, DagNode>
  detailLines: Record<string, ProvisionEvent[]>
  /** Last non-error phase id seen — used to attribute failures. */
  activePhase: string | null
}

function nodeForPhase(ctx: NodeMutationContext, phase: string): DagNode | null {
  if (phase === FLUX_BOOTSTRAP_ID) return ctx.nodesById[FLUX_BOOTSTRAP_ID] ?? null
  if (phase === 'tofu' || TOFU_PHASES.has(phase)) {
    return ctx.nodesById[HETZNER_INFRA_ID] ?? null
  }
  const norm = normaliseComponentId(phase)
  if (norm && ctx.nodesById[norm]) return ctx.nodesById[norm]
  return null
}

export function applyEventToContext(ctx: NodeMutationContext, ev: ProvisionEvent): void {
  const node = nodeForPhase(ctx, ev.phase)
  // Phase-state machine
  if (TOFU_PHASES.has(ev.phase)) {
    const hetzner = ctx.nodesById[HETZNER_INFRA_ID]
    if (hetzner) {
      if (hetzner.status === 'pending') hetzner.status = 'running'
      if (ev.level === 'error') hetzner.status = 'failed'
      if (ev.phase === 'tofu-init') hetzner.prog = Math.max(hetzner.prog, 0.15)
      else if (ev.phase === 'tofu-plan') hetzner.prog = Math.max(hetzner.prog, 0.3)
      else if (ev.phase === 'tofu-output') {
        hetzner.prog = 1
        hetzner.status = 'done'
      }
    }
  } else if (ev.phase === 'tofu') {
    const hetzner = ctx.nodesById[HETZNER_INFRA_ID]
    if (hetzner) {
      if (hetzner.status === 'pending') hetzner.status = 'running'
      const msg = ev.message || ''
      hetzner.hcloudSeen = hetzner.hcloudSeen || new Set()
      hetzner.hcloudCounts = hetzner.hcloudCounts || Object.create(null)
      for (const f of HCLOUD_FAMILIES) {
        if (msg.indexOf(f.id) >= 0) {
          hetzner.hcloudSeen.add(f.id)
          hetzner.hcloudCounts![f.id] = (hetzner.hcloudCounts![f.id] || 0) + 1
        }
      }
      const seen = hetzner.hcloudSeen.size
      const target = 0.3 + (seen / HCLOUD_FAMILIES.length) * 0.6
      if (target > hetzner.prog) hetzner.prog = target
      if (ev.level === 'error') hetzner.status = 'failed'
    }
  } else if (ev.phase === FLUX_BOOTSTRAP_ID) {
    const hetzner = ctx.nodesById[HETZNER_INFRA_ID]
    if (hetzner && hetzner.status !== 'failed') {
      hetzner.status = 'done'
      hetzner.prog = 1
    }
    const flux = ctx.nodesById[FLUX_BOOTSTRAP_ID]
    if (flux) {
      if (flux.status === 'pending') flux.status = 'running'
      flux.prog = Math.max(flux.prog, 0.5)
      if (ev.level === 'error') flux.status = 'failed'
    }
  } else if (node) {
    if (node.status === 'pending') node.status = 'running'
    if (ev.level === 'error') node.status = 'failed'
  }

  if (ev.level !== 'error') ctx.activePhase = ev.phase

  const targetId = (node && node.id) || HETZNER_INFRA_ID
  if (!ctx.detailLines[targetId]) ctx.detailLines[targetId] = []
  ctx.detailLines[targetId]!.push(ev)
}

/* ── React component ──────────────────────────────────────────────────── */

interface ProvisionPageProps {
  /** Test seam — injected by tests so the SSE EventSource isn't opened. */
  disableStream?: boolean
}

export function ProvisionPage({ disableStream = false }: ProvisionPageProps = {}) {
  // Route id is registered against the router's INTERNAL path
  // (`/provision/$deploymentId`) — basepath '/sovereign' is stripped before
  // route matching. Using the basepath-prefixed string here threw
  // "Invariant failed" on every navigation.
  const params = useParams({ from: '/provision/$deploymentId' as never }) as {
    deploymentId: string
  }
  const deploymentId = params.deploymentId
  const router = useRouter()

  const store = useWizardStore()
  const selectedComponents = store.selectedComponents
  const sovereignFQDN = useMemo(() => resolveSovereignDomain(store), [store])
  const topology = store.topology
  const regionCloudRegions = store.regionCloudRegions

  // Build DAG once per selection — selectedComponents is sorted in the store.
  const dag = useMemo(
    () => buildDag(selectedComponents),
    [selectedComponents],
  )

  const [nodes, setNodes] = useState<DagNode[]>(() => dag.nodes.map((n) => ({ ...n })))
  const [detailLines, setDetailLines] = useState<Record<string, ProvisionEvent[]>>({})
  const [selectedNodeId, setSelectedNodeId] = useState<string | null>(null)
  const [streamStatus, setStreamStatus] = useState<StreamStatus>('connecting')
  const [snapshot, setSnapshot] = useState<DeploymentSnapshot | null>(null)
  const [streamError, setStreamError] = useState<string | null>(null)
  const [startedAt, setStartedAt] = useState<number | null>(null)
  const [finishedAt, setFinishedAt] = useState<number | null>(null)
  const [, forceTick] = useState(0)
  const [retryNonce, setRetryNonce] = useState(0)
  const [sbCollapsed, setSbCollapsed] = useState(false)
  const [logCollapsed, setLogCollapsed] = useState(false)
  const [theme, setTheme] = useState<'dark' | 'light'>('dark')

  // Re-seed nodes when selection changes (e.g. operator returned to wizard
  // and adjusted before clicking retry from the failure card).
  useEffect(() => {
    setNodes(dag.nodes.map((n) => ({ ...n })))
    setDetailLines({})
    setSelectedNodeId(null)
  }, [dag])

  // Keep the elapsed clock ticking once we're streaming.
  useEffect(() => {
    if (streamStatus !== 'streaming') return
    const t = setInterval(() => forceTick((n) => n + 1), 1000)
    return () => clearInterval(t)
  }, [streamStatus])

  // Wire the SSE stream. The ref holds the active event source so tests +
  // retry can shut it down cleanly.
  const esRef = useRef<EventSource | null>(null)
  const activePhaseRef = useRef<string | null>(null)

  useEffect(() => {
    if (disableStream) return
    if (!deploymentId) return
    const url = `${API_BASE}/v1/deployments/${encodeURIComponent(deploymentId)}/logs`
    setStreamStatus('connecting')
    setStreamError(null)
    setSnapshot(null)
    setStartedAt(null)
    setFinishedAt(null)
    activePhaseRef.current = null

    const es = new EventSource(url)
    esRef.current = es

    es.onopen = () => {
      setStreamStatus('streaming')
      setStartedAt((prev) => prev ?? Date.now())
    }

    es.onmessage = (msg) => {
      try {
        const ev = JSON.parse(msg.data) as ProvisionEvent
        // Auto-select Hetzner-infra on the first event so the operator sees
        // the live log slice immediately.
        setSelectedNodeId((prev) => prev ?? HETZNER_INFRA_ID)
        setNodes((prevNodes) => {
          const nextNodes = prevNodes.map((n) => ({
            ...n,
            hcloudSeen: n.hcloudSeen ? new Set(n.hcloudSeen) : undefined,
            hcloudCounts: n.hcloudCounts ? { ...n.hcloudCounts } : undefined,
          }))
          const nodesById: Record<string, DagNode> = Object.create(null)
          for (const n of nextNodes) nodesById[n.id] = n
          const ctx: NodeMutationContext = {
            nodesById,
            detailLines: {},
            activePhase: activePhaseRef.current,
          }
          applyEventToContext(ctx, ev)
          activePhaseRef.current = ctx.activePhase
          // Merge per-node detail lines into the React state map.
          setDetailLines((prevLines) => {
            const next = { ...prevLines }
            for (const id of Object.keys(ctx.detailLines)) {
              next[id] = [...(next[id] || []), ...(ctx.detailLines[id] || [])]
            }
            return next
          })
          return nextNodes
        })
      } catch (err) {
        const synthetic: ProvisionEvent = {
          time: new Date().toISOString(),
          phase: 'stream',
          level: 'warn',
          message: `[provision] dropped malformed event: ${String(err)}`,
        }
        setDetailLines((prev) => ({
          ...prev,
          [HETZNER_INFRA_ID]: [...(prev[HETZNER_INFRA_ID] || []), synthetic],
        }))
      }
    }

    const onDone = (msg: MessageEvent) => {
      try {
        const snap = JSON.parse(msg.data) as DeploymentSnapshot
        setSnapshot(snap)
        setFinishedAt(Date.now())
        if (snap?.status === 'ready') {
          setNodes((prev) =>
            prev.map((n) =>
              n.status === 'running' || n.status === 'pending'
                ? { ...n, status: 'done', prog: 1 }
                : n,
            ),
          )
          setStreamStatus('completed')
        } else {
          setStreamStatus('failed')
          setStreamError(snap?.error ?? `Deployment ended with status=${snap?.status ?? 'unknown'}`)
          // Mark active running bubble as failed for visual attribution.
          if (activePhaseRef.current) {
            setNodes((prev) => {
              const phase = activePhaseRef.current!
              return prev.map((n) => {
                const isMatch =
                  (TOFU_PHASES.has(phase) && n.id === HETZNER_INFRA_ID) ||
                  (phase === FLUX_BOOTSTRAP_ID && n.id === FLUX_BOOTSTRAP_ID) ||
                  n.id === phase ||
                  n.id === normaliseComponentId(phase)
                return isMatch && n.status !== 'done' ? { ...n, status: 'failed' } : n
              })
            })
          }
        }
      } catch (err) {
        setStreamStatus('failed')
        setStreamError(`Failed to parse final snapshot: ${String(err)}`)
      }
      es.close()
    }
    es.addEventListener('done', onDone as EventListener)

    es.onerror = () => {
      // EventSource auto-reconnects unless we close. The browser handles
      // transient blips. If the connection was never established (and the
      // deployment id is bogus / 404), readyState transitions to CLOSED
      // very quickly — that's our cue to surface "unreachable".
      if (es.readyState === EventSource.CLOSED) {
        setStreamStatus((prev) => {
          if (prev === 'completed') return prev
          // If we never saw an open event, the URL is unreachable.
          return prev === 'connecting' ? 'unreachable' : 'failed'
        })
        setStreamError((prev) => prev ?? 'SSE connection closed before completion')
      }
    }

    return () => {
      es.removeEventListener('done', onDone as EventListener)
      es.close()
      esRef.current = null
    }
  }, [deploymentId, retryNonce, disableStream])

  /* ── Derived values for paint ─────────────────────────────────────── */

  const total = nodes.length
  const totalSelected = nodes.filter((n) => n.kind !== 'super').length
  const progressPct = useMemo(() => {
    if (total === 0) return 0
    let acc = 0
    for (const n of nodes) {
      if (n.status === 'done') acc += 1
      else if (n.status === 'running') acc += Math.max(0.5, n.prog || 0.5)
    }
    return Math.min(100, Math.round((acc / total) * 100))
  }, [nodes, total])
  const elapsedSec = startedAt
    ? Math.floor(((finishedAt ?? Date.now()) - startedAt) / 1000)
    : 0
  const elapsedLabel = `${Math.floor(elapsedSec / 60)}m ${String(elapsedSec % 60).padStart(2, '0')}s`

  const topbarMeta = useMemo(() => {
    const meta: string[] = []
    if (topology) meta.push(String(topology).toUpperCase())
    const regions = Object.values(regionCloudRegions).filter(Boolean)
    if (regions.length > 0) meta.push(regions.join(' + '))
    meta.push(`${totalSelected} component${totalSelected === 1 ? '' : 's'}`)
    return meta.join(' · ')
  }, [topology, regionCloudRegions, totalSelected])

  const consoleURL = snapshot?.result?.consoleURL ?? ''
  const consoleHostLabel = snapshot?.result?.sovereignFQDN ?? sovereignFQDN

  /* ── Layout sizing for the SVG ────────────────────────────────────── */

  const svgRef = useRef<SVGSVGElement | null>(null)
  const [svgSize, setSvgSize] = useState<{ w: number; h: number }>({ w: 960, h: 600 })
  useLayoutEffect(() => {
    if (!svgRef.current) return
    const update = () => {
      const rc = svgRef.current!.getBoundingClientRect()
      if (rc.width > 100) setSvgSize({ w: rc.width, h: rc.height })
    }
    update()
    const ro = new ResizeObserver(update)
    ro.observe(svgRef.current)
    return () => ro.disconnect()
  }, [])

  const layout = useMemo(() => {
    const W = svgSize.w
    const H = svgSize.h
    const PAD_X = 70
    const PAD_Y = 60
    const layers = dag.layoutInfo.layers
    const buckets = dag.layoutInfo.buckets
    const nLayers = layers.length
    const innerW = Math.max(W - 2 * PAD_X, 1)
    const pos: Record<string, { x: number; y: number }> = Object.create(null)
    layers.forEach((L, li) => {
      const x = nLayers === 1 ? W / 2 : PAD_X + (li / (nLayers - 1)) * innerW
      const ids = buckets[L] || []
      const innerH = Math.max(H - 2 * PAD_Y, 1)
      const slot = innerH / Math.max(ids.length, 1)
      ids.forEach((id, i) => {
        const y = PAD_Y + slot * (i + 0.5)
        pos[id] = { x, y }
      })
    })
    return pos
  }, [svgSize, dag])

  /* ── Failure / unreachable surface ────────────────────────────────── */

  const isFailed = streamStatus === 'failed' || streamStatus === 'unreachable'
  const failureMessage = streamError ?? snapshot?.error ?? null

  function retryProvision() {
    // Re-open the same deployment id stream; the catalyst-api will replay
    // buffered events from its store. If the operator wants to issue a
    // brand-new deployment, the "Back to wizard" CTA below is the path.
    setRetryNonce((n) => n + 1)
  }

  function backToWizard() {
    router.navigate({ to: '/wizard' })
  }

  /* ── Render ───────────────────────────────────────────────────────── */

  return (
    <div className="provision-shell" data-theme={theme}>
      <style>{provisionCss}</style>
      <div className="page">
        <header className="topbar">
          <span className="tb-logo">OpenOva</span>
          <div className="tb-sep" />
          <div>
            <div className="tb-org" data-testid="topbar-fqdn">
              {sovereignFQDN || 'Sovereign'}
            </div>
            <div className="tb-meta" data-testid="topbar-meta">
              {topbarMeta}
            </div>
          </div>
          <div className="tb-r">
            <StatusPill status={streamStatus} />
            <div className="prog-w" aria-label="Provisioning progress">
              <div className="prog-b">
                <div className="prog-f" style={{ width: `${progressPct}%` }} />
              </div>
              <span className="prog-p" data-testid="progress-pct">
                {progressPct}%
              </span>
              <span className="prog-t">{elapsedLabel}</span>
            </div>
            {streamStatus === 'completed' && consoleURL && (
              <a
                className="ibtn cta"
                href={consoleURL}
                target="_blank"
                rel="noopener noreferrer"
                data-testid="open-console"
              >
                Open {consoleHostLabel || 'Console'} →
              </a>
            )}
            <button
              type="button"
              className="ibtn"
              onClick={() => setSbCollapsed((v) => !v)}
              title="Toggle sidebar"
            >
              ⊟
            </button>
            <button
              type="button"
              className="ibtn"
              onClick={() => setLogCollapsed((v) => !v)}
              title="Toggle log panel"
            >
              ⊞
            </button>
            <button
              type="button"
              className="ibtn"
              onClick={() => setTheme((t) => (t === 'dark' ? 'light' : 'dark'))}
              title="Toggle theme"
            >
              {theme === 'dark' ? '☀' : '☽'}
            </button>
          </div>
        </header>

        {isFailed && (
          <FailureCard
            deploymentId={deploymentId}
            status={streamStatus}
            message={failureMessage}
            sovereignFQDN={sovereignFQDN}
            onRetry={retryProvision}
            onBack={backToWizard}
          />
        )}

        <div className="body">
          <Sidebar
            collapsed={sbCollapsed}
            nodes={nodes}
            selectedNodeId={selectedNodeId}
            onSelect={(id) => setSelectedNodeId(id)}
          />
          <div className="dag-panel">
            <div className="dag-graph">
              <svg
                ref={svgRef}
                id="gsvg"
                xmlns="http://www.w3.org/2000/svg"
                viewBox={`0 0 ${svgSize.w} ${svgSize.h}`}
                preserveAspectRatio="xMidYMid meet"
              >
                <defs>
                  <marker
                    id="m-done"
                    viewBox="0 0 8 8"
                    refX="7"
                    refY="4"
                    markerWidth="5"
                    markerHeight="5"
                    orient="auto-start-reverse"
                  >
                    <path d="M0,1L7,4L0,7Z" fill="rgba(167,243,208,.55)" />
                  </marker>
                  <marker
                    id="m-act"
                    viewBox="0 0 8 8"
                    refX="7"
                    refY="4"
                    markerWidth="5"
                    markerHeight="5"
                    orient="auto-start-reverse"
                  >
                    <path d="M0,1L7,4L0,7Z" fill="rgba(186,230,253,.65)" />
                  </marker>
                  <marker
                    id="m-wait"
                    viewBox="0 0 8 8"
                    refX="7"
                    refY="4"
                    markerWidth="5"
                    markerHeight="5"
                    orient="auto-start-reverse"
                  >
                    <path d="M0,1L7,4L0,7Z" fill="rgba(148,163,184,.40)" />
                  </marker>
                  <marker
                    id="m-fail"
                    viewBox="0 0 8 8"
                    refX="7"
                    refY="4"
                    markerWidth="5"
                    markerHeight="5"
                    orient="auto-start-reverse"
                  >
                    <path d="M0,1L7,4L0,7Z" fill="rgba(248,113,113,.65)" />
                  </marker>
                </defs>
                <DagEdges edges={dag.edges} layout={layout} nodes={nodes} />
                <DagNodes
                  nodes={nodes}
                  layout={layout}
                  selectedNodeId={selectedNodeId}
                  onSelect={setSelectedNodeId}
                />
              </svg>
            </div>
          </div>
          <LogPanel
            collapsed={logCollapsed}
            nodes={nodes}
            selectedNodeId={selectedNodeId}
            detailLines={detailLines}
          />
        </div>
      </div>
    </div>
  )
}

/* ── Sub-components ───────────────────────────────────────────────────── */

function StatusPill({ status }: { status: StreamStatus }) {
  const cls =
    status === 'completed'
      ? 'pill ok'
      : status === 'failed' || status === 'unreachable'
        ? 'pill err'
        : 'pill'
  const text =
    status === 'connecting'
      ? 'Connecting'
      : status === 'streaming'
        ? 'Provisioning'
        : status === 'completed'
          ? 'Ready'
          : status === 'unreachable'
            ? 'Unreachable'
            : 'Failed'
  return (
    <div className={cls} data-testid="status-pill">
      <div className="pdot" />
      <span className="pill-t">{text}</span>
    </div>
  )
}

interface FailureCardProps {
  deploymentId: string
  status: StreamStatus
  message: string | null
  sovereignFQDN: string
  onRetry: () => void
  onBack: () => void
}

function FailureCard({ deploymentId, status, message, sovereignFQDN, onRetry, onBack }: FailureCardProps) {
  const isUnreachable = status === 'unreachable'
  const heading = isUnreachable ? 'Couldn’t reach the deployment stream' : 'Provisioning failed'
  const detail = isUnreachable
    ? `We couldn't open the SSE stream for deployment ${deploymentId}. The catalyst-api may be unreachable, or the deployment id is unknown to the backend.`
    : `The catalyst-api emitted a terminal failure for deployment ${deploymentId}.`
  return (
    <div className="failure-card" role="alert" data-testid="failure-card">
      <div className="failure-card-head">
        <span className="failure-card-tag">{isUnreachable ? 'Unreachable' : 'Failed'}</span>
        <h2>{heading}</h2>
      </div>
      <p className="failure-card-detail">{detail}</p>
      {message && (
        <pre className="failure-card-error" data-testid="failure-error">
          {message}
        </pre>
      )}
      <div className="failure-card-meta">
        <span>
          <strong>Deployment id:</strong>{' '}
          <code data-testid="failure-deployment-id">{deploymentId}</code>
        </span>
        {sovereignFQDN && (
          <span>
            <strong>FQDN:</strong> <code>{sovereignFQDN}</code>
          </span>
        )}
      </div>
      <div className="failure-card-hint">
        <strong>Open logs:</strong>{' '}
        <code>kubectl -n catalyst-system logs deploy/catalyst-api</code>
      </div>
      <div className="failure-card-actions">
        <button type="button" className="ibtn cta" onClick={onRetry} data-testid="failure-retry">
          Retry provision
        </button>
        <button type="button" className="ibtn" onClick={onBack} data-testid="failure-back">
          Back to wizard
        </button>
      </div>
    </div>
  )
}

interface SidebarProps {
  collapsed: boolean
  nodes: DagNode[]
  selectedNodeId: string | null
  onSelect: (id: string) => void
}

function Sidebar({ collapsed, nodes, selectedNodeId, onSelect }: SidebarProps) {
  const sections = [
    { key: 'phase0', label: 'Phase 0 — Cloud', items: nodes.filter((n) => n.kind === 'super') },
    { key: 'bootstrap', label: 'Bootstrap kit', items: nodes.filter((n) => n.kind === 'bootstrap') },
    {
      key: 'selected',
      label: 'Selected components',
      items: nodes.filter((n) => n.kind === 'component'),
    },
  ]
  return (
    <aside className={`sidebar${collapsed ? ' collapsed' : ''}`}>
      <div className="sb-in">
        <div className="sb-hdr">DEPLOYMENT PROGRESS</div>
        {sections.map((sec) => {
          if (sec.items.length === 0) return null
          const done = sec.items.filter((n) => n.status === 'done').length
          const running = sec.items.filter((n) => n.status === 'running').length
          const failed = sec.items.some((n) => n.status === 'failed')
          const pct = Math.round(((done + running * 0.5) / sec.items.length) * 100)
          const dotState =
            done === sec.items.length
              ? 'done'
              : running > 0
                ? 'running'
                : failed
                  ? 'failed'
                  : ''
          return (
            <div key={sec.key} data-testid={`sidebar-section-${sec.key}`}>
              <div className="sec-row">
                <span className="sec-arr open">▶</span>
                <span className={`sec-dot ${dotState}`} />
                <div style={{ flex: 1, minWidth: 0 }}>
                  <div className="sec-name">{sec.label}</div>
                  <div className="sec-meta">
                    {done}/{sec.items.length} ready · {running} active
                  </div>
                </div>
                <span className="sec-badge">{pct}%</span>
              </div>
              <div className="sb-prog">
                <div className="sb-prog-f" style={{ width: `${pct}%` }} />
              </div>
              <div className="node-rows open">
                {sec.items.map((n) => (
                  <button
                    type="button"
                    key={n.id}
                    className={`n-row${selectedNodeId === n.id ? ' selected' : ''}`}
                    onClick={() => onSelect(n.id)}
                    data-testid={`sidebar-node-${n.id}`}
                  >
                    <span className={`n-dot ${n.status}`} />
                    <span className="n-name">{n.label}</span>
                  </button>
                ))}
              </div>
            </div>
          )
        })}
      </div>
    </aside>
  )
}

interface DagEdgesProps {
  edges: DagEdge[]
  layout: Record<string, { x: number; y: number }>
  nodes: DagNode[]
}

function DagEdges({ edges, layout, nodes }: DagEdgesProps) {
  const nodeById: Record<string, DagNode> = Object.create(null)
  for (const n of nodes) nodeById[n.id] = n
  return (
    <g id="L-edges">
      {edges.map((e, i) => {
        const fp = layout[e.from]
        const tp = layout[e.to]
        if (!fp || !tp) return null
        const fn = nodeById[e.from]
        const tn = nodeById[e.to]
        const fr = fn && fn.kind === 'super' ? 30 : 20
        const tr = tn && tn.kind === 'super' ? 30 : 20
        const dx = tp.x - fp.x
        const dy = tp.y - fp.y
        const d = Math.sqrt(dx * dx + dy * dy) || 1
        const nx = dx / d
        const ny = dy / d
        const sx = (fp.x + nx * (fr + 1)).toFixed(1)
        const sy = (fp.y + ny * (fr + 1)).toFixed(1)
        const ex = (tp.x - nx * (tr + 8)).toFixed(1)
        const ey = (tp.y - ny * (tr + 8)).toFixed(1)
        let mode: 'wait' | 'done' | 'act' | 'fail' = 'wait'
        if (fn && fn.status === 'done' && tn && tn.status !== 'pending') mode = 'done'
        else if (fn && fn.status === 'running') mode = 'act'
        if (fn && fn.status === 'failed') mode = 'fail'
        const stroke =
          mode === 'done'
            ? 'rgba(167,243,208,.5)'
            : mode === 'act'
              ? 'rgba(186,230,253,.6)'
              : mode === 'fail'
                ? 'rgba(248,113,113,.6)'
                : 'rgba(148,163,184,.30)'
        return (
          <path
            key={`${e.from}-${e.to}-${i}`}
            d={`M${sx},${sy} L${ex},${ey}`}
            fill="none"
            stroke={stroke}
            strokeWidth={1.5}
            strokeLinecap="round"
            markerEnd={`url(#m-${mode})`}
          />
        )
      })}
    </g>
  )
}

interface DagNodesProps {
  nodes: DagNode[]
  layout: Record<string, { x: number; y: number }>
  selectedNodeId: string | null
  onSelect: (id: string) => void
}

function DagNodes({ nodes, layout, selectedNodeId, onSelect }: DagNodesProps) {
  const SF: Record<NodeStatus, string> = {
    done: '#166534',
    running: '#1e3a5f',
    failed: '#7f1d1d',
    pending: '#0d1726',
  }
  const SUPER_COLOR = '#818CF8'
  const BOOTSTRAP_COLOR = '#38BDF8'
  const COMPONENT_COLOR = '#A78BFA'
  function colorFor(n: DagNode): string {
    if (n.kind === 'super') return SUPER_COLOR
    if (n.kind === 'bootstrap') return BOOTSTRAP_COLOR
    return COMPONENT_COLOR
  }
  return (
    <g id="L-nodes">
      {nodes.map((n) => {
        const p = layout[n.id]
        if (!p) return null
        const r = n.kind === 'super' ? 30 : 20
        const stroke = colorFor(n)
        const isSelected = selectedNodeId === n.id
        const labelColor =
          n.status === 'pending' ? 'var(--lo)' : colorFor(n)
        const arc =
          n.prog && n.prog > 0 && n.status !== 'pending' ? (
            <circle
              r={r}
              fill="none"
              stroke={colorFor(n)}
              strokeWidth={3.5}
              strokeLinecap="round"
              transform="rotate(-90)"
              opacity={0.92}
              strokeDasharray={`${(n.prog * 2 * Math.PI * r).toFixed(2)} ${(2 * Math.PI * r).toFixed(2)}`}
            />
          ) : null
        const glyph =
          n.status === 'done' || n.status === 'failed' ? (
            <text
              fontSize={12}
              textAnchor="middle"
              dominantBaseline="central"
              fontFamily="Inter"
              fontWeight={800}
              fill="rgba(5,12,30,0.95)"
              pointerEvents="none"
            >
              {n.status === 'done' ? '✓' : '✕'}
            </text>
          ) : null
        return (
          <g
            key={n.id}
            className={`ng${isSelected ? ' selected' : ''}`}
            transform={`translate(${p.x.toFixed(1)},${p.y.toFixed(1)})`}
            onClick={() => onSelect(n.id)}
            data-testid={`dag-node-${n.id}`}
          >
            <circle r={r + 9} className="nhov" stroke={stroke} fill="none" />
            <circle r={r} fill={SF[n.status] ?? SF.pending} />
            <circle
              r={r}
              fill="none"
              stroke={stroke}
              strokeWidth={1.5}
              opacity={n.status === 'pending' ? 0.3 : 0.7}
            />
            {arc}
            {glyph}
            <text y={r + 14} className="nlabel" fill={labelColor} textAnchor="middle">
              {n.label}
            </text>
            <text y={r + 24} className="nsub" textAnchor="middle">
              {n.sub}
            </text>
          </g>
        )
      })}
    </g>
  )
}

interface LogPanelProps {
  collapsed: boolean
  nodes: DagNode[]
  selectedNodeId: string | null
  detailLines: Record<string, ProvisionEvent[]>
}

function LogPanel({ collapsed, nodes, selectedNodeId, detailLines }: LogPanelProps) {
  const node = selectedNodeId ? nodes.find((n) => n.id === selectedNodeId) ?? null : null
  const evs = selectedNodeId ? detailLines[selectedNodeId] ?? [] : []
  return (
    <div className={`log-panel${collapsed ? ' collapsed' : ''}`}>
      <div className="lp-in">
        <div className="lhdr">
          <span className="llbl">Live Log</span>
          <span className="lchip">{node?.label ?? '—'}</span>
          <span className="lstat">
            {evs.length} event{evs.length === 1 ? '' : 's'}
            {node ? ` · ${node.status}` : ''}
          </span>
        </div>
        <div className="lstream" data-testid="log-stream">
          {!node ? (
            <div className="lempty">
              Select a bubble to view its log slice; the full stream is appended chronologically as
              the catalyst-api emits it.
            </div>
          ) : evs.length === 0 ? (
            <div className="lempty">
              No events yet for <strong style={{ color: 'var(--md)' }}>{node.label}</strong>.
              The catalyst-api SSE stream feeds this panel as work begins.
            </div>
          ) : (
            evs.map((e, i) => {
              const ts = (e.time || '').slice(11, 19) || '—'
              const lvl = e.level || 'info'
              return (
                <div key={i} className={`ll ll-${lvl}`}>
                  <span className="ll-ts">{ts}</span>
                  <span className="ll-msg">
                    {e.phase && e.phase !== 'tofu' && (
                      <span className="ll-meta">{e.phase}</span>
                    )}
                    {e.message ?? ''}
                  </span>
                </div>
              )
            })
          )}
        </div>
      </div>
    </div>
  )
}

/* ── Inline CSS, ported from public/provision.html ─────────────────────── */

const provisionCss = `
.provision-shell{
  --bg:radial-gradient(ellipse at 20% 10%,#0b1c3a 0%,#070a12 75%);
  --s1:rgba(255,255,255,.04);
  --s2:rgba(255,255,255,.07);
  --bd:rgba(255,255,255,.07);
  --bd2:rgba(255,255,255,.14);
  --hi:rgba(255,255,255,.92);
  --md:rgba(255,255,255,.65);
  --lo:rgba(255,255,255,.40);
  --hint:rgba(255,255,255,.22);
  --acc:#38BDF8;
  --accd:rgba(56,189,248,.10);
  --log:rgba(2,6,15,.75);
  --ok:#4ADE80;
  --warn:#FBBF24;
  --err:#F87171;
  font-family:'Inter',system-ui,sans-serif;
  font-size:13px;
  color:var(--md);
  background:var(--bg);
  display:flex;
  flex-direction:column;
  min-height:100dvh;
  height:100dvh;
}
.provision-shell[data-theme="light"]{
  --bg:radial-gradient(ellipse at 20% 10%,#dbeafe 0%,#f0f9ff 75%);
  --s1:#fff;--s2:#f8fafc;--bd:#e2e8f0;--bd2:#cbd5e1;
  --hi:#0f172a;--md:#334155;--lo:#475569;--hint:#94a3b8;
  --acc:#0284c7;--accd:rgba(2,132,199,.08);--log:#f8fafc;
}
.provision-shell *,.provision-shell *::before,.provision-shell *::after{box-sizing:border-box;margin:0;padding:0}
.provision-shell .page{display:flex;flex-direction:column;flex:1;overflow:hidden}
.provision-shell .topbar{flex-shrink:0;height:48px;display:flex;align-items:center;padding:0 14px;gap:0;background:var(--s1);border-bottom:1px solid var(--bd);backdrop-filter:blur(20px);z-index:50}
.provision-shell .tb-logo{font-size:11px;font-weight:800;letter-spacing:.14em;color:var(--acc);text-transform:uppercase}
.provision-shell .tb-sep{width:1px;height:18px;background:var(--bd);margin:0 12px}
.provision-shell .tb-org{font-size:13px;font-weight:700;color:var(--hi)}
.provision-shell .tb-meta{font-size:10px;color:var(--hint);margin-top:1px}
.provision-shell .tb-r{display:flex;align-items:center;gap:8px;margin-left:auto}
.provision-shell .pill{display:flex;align-items:center;gap:5px;padding:3px 10px;border-radius:20px;background:var(--accd);border:1px solid rgba(56,189,248,.20)}
.provision-shell .pill.ok{background:rgba(74,222,128,.10);border-color:rgba(74,222,128,.20)}
.provision-shell .pill.ok .pdot{background:var(--ok);animation:none}
.provision-shell .pill.err{background:rgba(248,113,113,.10);border-color:rgba(248,113,113,.20)}
.provision-shell .pill.err .pdot{background:var(--err);animation:none}
.provision-shell .pdot{width:5px;height:5px;border-radius:50%;background:var(--acc);animation:provpulse 2s ease-in-out infinite}
@keyframes provpulse{0%,100%{opacity:1;transform:scale(1)}50%{opacity:.4;transform:scale(.65)}}
.provision-shell .pill-t{font-size:10px;font-weight:600;color:var(--acc)}
.provision-shell .pill.ok .pill-t{color:var(--ok)}
.provision-shell .pill.err .pill-t{color:var(--err)}
.provision-shell .prog-w{display:flex;align-items:center;gap:6px}
.provision-shell .prog-b{width:100px;height:3px;border-radius:2px;background:var(--bd);overflow:hidden}
.provision-shell .prog-f{height:100%;width:0%;background:linear-gradient(90deg,var(--acc),#818CF8);transition:width .4s}
.provision-shell .prog-p{font-size:11px;font-weight:700;color:var(--acc);min-width:34px;text-align:right}
.provision-shell .prog-t{font-size:10px;color:var(--hint);min-width:48px;text-align:right;font-variant-numeric:tabular-nums}
.provision-shell .ibtn{width:26px;height:26px;border-radius:6px;background:var(--bd);border:1px solid var(--bd2);color:var(--lo);cursor:pointer;display:flex;align-items:center;justify-content:center;font-size:11px;transition:all .15s;text-decoration:none}
.provision-shell .ibtn:hover{background:var(--s2);color:var(--md)}
.provision-shell .ibtn.cta{background:var(--accd);border-color:rgba(56,189,248,.4);color:var(--acc);font-weight:700;padding:0 10px;width:auto;font-size:11px;letter-spacing:.04em}
.provision-shell .body{flex:1;display:flex;overflow:hidden;min-height:0}
.provision-shell .sidebar{width:248px;flex-shrink:0;display:flex;flex-direction:column;border-right:1px solid var(--bd);overflow:hidden;transition:width .28s}
.provision-shell .sidebar.collapsed{width:0}
.provision-shell .sb-in{min-width:248px;display:flex;flex-direction:column;height:100%;overflow-y:auto}
.provision-shell .sb-hdr{font-size:10px;font-weight:700;letter-spacing:.10em;text-transform:uppercase;color:var(--hint);padding:10px 12px 6px}
.provision-shell .sec-row{display:flex;align-items:center;gap:7px;padding:8px 12px 6px;border-top:1px solid var(--bd);cursor:default;user-select:none}
.provision-shell .sec-dot{width:7px;height:7px;border-radius:50%;flex-shrink:0;background:var(--bd2)}
.provision-shell .sec-dot.running{background:var(--acc);animation:provpulse 1.5s ease-in-out infinite}
.provision-shell .sec-dot.done{background:var(--ok)}
.provision-shell .sec-dot.failed{background:var(--err)}
.provision-shell .sec-name{font-size:12px;font-weight:700;color:var(--hi)}
.provision-shell .sec-meta{font-size:9px;color:var(--hint);margin-top:1px}
.provision-shell .sec-badge{font-size:8px;font-weight:700;padding:2px 6px;border-radius:4px;color:var(--acc);background:var(--accd);border:1px solid rgba(56,189,248,.22);flex-shrink:0}
.provision-shell .sec-arr{font-size:9px;color:var(--hint);transition:transform .18s;flex-shrink:0;width:12px;text-align:center}
.provision-shell .sec-arr.open{transform:rotate(90deg)}
.provision-shell .sb-prog{height:2px;border-radius:1px;background:var(--bd);margin:0 12px 5px;overflow:hidden}
.provision-shell .sb-prog-f{height:100%;background:linear-gradient(90deg,var(--acc),#818CF8);transition:width .4s}
.provision-shell .node-rows{overflow:hidden;max-height:0;transition:max-height .22s ease}
.provision-shell .node-rows.open{max-height:9999px}
.provision-shell .n-row{display:flex;align-items:center;gap:6px;padding:4px 12px 4px 28px;cursor:pointer;background:none;border:0;width:100%;text-align:left}
.provision-shell .n-row:hover{background:var(--s1)}
.provision-shell .n-row.selected{background:var(--s2)}
.provision-shell .n-dot{width:5px;height:5px;border-radius:50%;flex-shrink:0;background:var(--bd2)}
.provision-shell .n-dot.done{background:var(--ok)}
.provision-shell .n-dot.running{background:var(--acc);animation:provpulse 1.2s ease-in-out infinite}
.provision-shell .n-dot.failed{background:var(--err)}
.provision-shell .n-name{font-size:10px;color:var(--md);flex:1;white-space:nowrap;overflow:hidden;text-overflow:ellipsis}
.provision-shell .dag-panel{flex:1;display:flex;flex-direction:column;overflow:hidden;min-width:0}
.provision-shell .dag-graph{flex:1;position:relative;overflow:hidden}
.provision-shell #gsvg{width:100%;height:100%;display:block;cursor:default}
.provision-shell .nlabel{font-size:10px;font-family:'Inter',sans-serif;font-weight:600}
.provision-shell .nsub{font-size:8px;font-family:'Inter',sans-serif;fill:var(--hint)}
.provision-shell .ng{cursor:pointer}
.provision-shell .ng .nhov{opacity:0;stroke-width:1.5;transition:opacity .12s;pointer-events:none}
.provision-shell .ng:hover .nhov{opacity:.6}
.provision-shell .ng.selected .nhov{opacity:1}
.provision-shell .log-panel{width:340px;flex-shrink:0;display:flex;flex-direction:column;border-left:1px solid var(--bd);background:var(--log);transition:width .28s;overflow:hidden}
.provision-shell .log-panel.collapsed{width:0}
.provision-shell .lp-in{min-width:340px;display:flex;flex-direction:column;height:100%}
.provision-shell .lhdr{display:flex;align-items:center;gap:7px;padding:7px 11px;border-bottom:1px solid var(--bd);flex-shrink:0}
.provision-shell .llbl{font-size:9px;font-weight:700;letter-spacing:.10em;text-transform:uppercase;color:var(--hint)}
.provision-shell .lchip{font-size:9px;font-weight:600;padding:2px 7px;border-radius:4px;color:var(--acc);background:var(--accd);border:1px solid rgba(56,189,248,.18)}
.provision-shell .lstat{margin-left:auto;font-size:9px;color:var(--hint);font-variant-numeric:tabular-nums}
.provision-shell .lstream{flex:1;overflow-y:auto;padding:8px 11px;font-family:'JetBrains Mono',monospace;font-size:9.5px;line-height:1.7}
.provision-shell .ll{display:flex;gap:9px;align-items:flex-start}
.provision-shell .ll-ts{color:var(--hint);flex-shrink:0;font-size:8.5px;min-width:54px}
.provision-shell .ll-msg{flex:1;word-break:break-word;white-space:pre-wrap}
.provision-shell .ll-info .ll-msg{color:var(--md)}
.provision-shell .ll-warn .ll-msg{color:var(--warn)}
.provision-shell .ll-error .ll-msg{color:var(--err)}
.provision-shell .ll-meta{font-size:8.5px;color:var(--hint);background:var(--s1);padding:0 5px;border-radius:3px;margin-right:5px}
.provision-shell .lempty{padding:14px;color:var(--hint);font-size:10.5px;line-height:1.6}
.provision-shell .failure-card{margin:14px;padding:18px 22px;border-radius:12px;border:1px solid rgba(248,113,113,.35);background:rgba(248,113,113,.06);color:var(--md);display:flex;flex-direction:column;gap:10px}
.provision-shell .failure-card-head{display:flex;align-items:center;gap:10px}
.provision-shell .failure-card-tag{font-size:10px;font-weight:800;letter-spacing:.1em;text-transform:uppercase;color:#fff;background:var(--err);border-radius:4px;padding:3px 8px}
.provision-shell .failure-card h2{font-size:16px;font-weight:700;color:var(--hi);letter-spacing:.01em;margin:0}
.provision-shell .failure-card-detail{font-size:12px;color:var(--md);line-height:1.6}
.provision-shell .failure-card-error{font-family:'JetBrains Mono',monospace;font-size:11px;color:var(--err);background:rgba(0,0,0,.30);border:1px solid rgba(248,113,113,.30);border-radius:8px;padding:10px 12px;white-space:pre-wrap;word-break:break-word;max-height:200px;overflow:auto}
.provision-shell .failure-card-meta{display:flex;flex-wrap:wrap;gap:14px;font-size:11px;color:var(--md)}
.provision-shell .failure-card-meta strong{color:var(--lo);font-weight:600;margin-right:4px}
.provision-shell .failure-card-meta code{font-family:'JetBrains Mono',monospace;font-size:11px;color:var(--hi)}
.provision-shell .failure-card-hint{font-size:11px;color:var(--md)}
.provision-shell .failure-card-hint code{font-family:'JetBrains Mono',monospace;color:var(--hi);background:var(--s1);padding:2px 6px;border-radius:4px}
.provision-shell .failure-card-actions{display:flex;gap:10px;margin-top:4px}
.provision-shell .failure-card-actions .ibtn{height:32px;padding:0 14px;font-size:11px}
@media(max-width:900px){.provision-shell .sidebar{width:0}}
@media(max-width:640px){.provision-shell .log-panel{width:0}}
`
