/* eslint-disable no-undef */
/**
 * provision.js — runtime for the dynamic provisioning DAG.
 *
 * Responsibilities:
 *   1. Read the wizard's persisted state from localStorage to recover the
 *      deploymentId, selected component ids, sovereign FQDN, and topology
 *      summary needed to render the page.
 *   2. Materialise the DAG nodes from window.CATALYST_CATALOG (emitted by
 *      scripts/build-catalog.mjs) — Hetzner-infra supernode, Flux-bootstrap
 *      supernode, the bootstrap-kit Blueprints in numeric install order, and
 *      every user-selected Blueprint with its transitive HARD dependencies
 *      expanded.
 *   3. Lay out the DAG and render it into <svg id="gsvg">.
 *   4. Open an EventSource against
 *        <BASE>api/v1/deployments/<id>/logs
 *      and translate phase events to bubble state transitions; raw `tofu`
 *      stdout/stderr lines stream into the log panel and into the active
 *      bubble's expandable detail. The four hcloud_* resource markers
 *      (network, firewall, server, load_balancer) advance the
 *      Hetzner-infra supernode's internal sub-progress.
 *   5. On the SSE `done` event: if the snapshot's status is `ready`, surface
 *      the "Open Console →" CTA pointing at result.consoleURL; if `failed`,
 *      flip the active bubble to failed and display the error.
 *
 * No bundler step. Loaded as a plain <script> by provision.html.
 */
;(function () {
  'use strict'

  // ── Constants ─────────────────────────────────────────────────────────────
  const NS = 'http://www.w3.org/2000/svg'
  const WIZARD_LS_KEY = 'openova-catalyst-wizard'

  // The four Hetzner resource families the OpenTofu module declares — each
  // becomes a sub-bubble on the Hetzner-infra supernode and advances when its
  // corresponding "hcloud_<family>." marker appears in the tofu stdout
  // stream during tofu-apply. Order = visual + dependency order.
  const HCLOUD_FAMILIES = [
    { id: 'hcloud_network',        label: 'Network'       },
    { id: 'hcloud_firewall',       label: 'Firewall'      },
    { id: 'hcloud_server',         label: 'Servers'       },
    { id: 'hcloud_load_balancer',  label: 'Load balancer' },
  ]

  // OpenTofu phase ids the catalyst-api emits during Phase 0. Lifted from
  // products/catalyst/bootstrap/api/internal/provisioner/provisioner.go and
  // mirrored in src/shared/constants/bootstrap-phases.ts. Kept here as plain
  // strings because this static page must not import the typed module.
  const TOFU_PHASES = new Set(['tofu-init', 'tofu-plan', 'tofu-apply', 'tofu-output'])
  const HETZNER_INFRA_ID = 'hetzner-infra'
  const FLUX_BOOTSTRAP_ID = 'flux-bootstrap'

  // ── Element refs ──────────────────────────────────────────────────────────
  const $ = (id) => document.getElementById(id)
  const svgEl = $('gsvg')
  const Le = $('L-edges')
  const Ln = $('L-nodes')
  const sbIn = $('sb-in')
  const lstream = $('lstream')
  const lchip = $('lchip')
  const lstat = $('lstat')
  const tt = $('tt')

  function mk(t, a) {
    const e = document.createElementNS(NS, t)
    if (a) for (const k of Object.keys(a)) e.setAttribute(k, a[k])
    return e
  }

  // ── Storage / wizard recovery ─────────────────────────────────────────────
  function readWizardState() {
    try {
      const raw = window.localStorage.getItem(WIZARD_LS_KEY)
      if (!raw) return null
      const wrapper = JSON.parse(raw)
      // Zustand persist middleware shape: { state, version }
      return wrapper && wrapper.state ? wrapper.state : null
    } catch (_) {
      return null
    }
  }

  // Resolve <BASE>api by mirroring src/shared/config/urls.ts. The Vite
  // `base` config is "/sovereign/" in production; provision.html is served
  // at <base>provision.html, so stripping the trailing filename gives us
  // exactly the path tier prefix the React wizard's API_BASE uses.
  function deriveApiBase() {
    const here = window.location.pathname
    // Strip the trailing "provision.html" (or any final segment) so we land
    // on the same prefix as the React app at "/sovereign/".
    const idx = here.lastIndexOf('/')
    const base = idx >= 0 ? here.slice(0, idx + 1) : '/'
    return `${base}api`
  }

  // ── Catalog access ────────────────────────────────────────────────────────
  function catalog() {
    const c = window.CATALYST_CATALOG || {}
    return {
      components: Array.isArray(c.components) ? c.components : [],
      bootstrapKit: Array.isArray(c.bootstrapKit) ? c.bootstrapKit : [],
    }
  }

  // Catalog component ids come from blueprint.yaml metadata.name (always
  // "bp-<slug>"). The wizard's `selectedComponents` field stores the SAME
  // ids — see ALL_COMPONENTS in pages/wizard/steps/componentGroups.ts +
  // BLUEPRINT_BY_ID in shared/constants/catalog.generated.ts. If a future
  // store version diverges we normalise here defensively.
  function normaliseComponentId(id) {
    if (typeof id !== 'string') return null
    return id.startsWith('bp-') ? id : `bp-${id}`
  }

  // Walk the `depends` graph from each seed and return the closure (seeds
  // included, deduped, in a stable order = catalog index ascending).
  function expandWithDependencies(seedIds, components) {
    const byId = Object.create(null)
    for (const c of components) byId[c.id] = c
    const visited = new Set()
    const stack = []
    for (const id of seedIds) {
      const norm = normaliseComponentId(id)
      if (norm && byId[norm]) stack.push(norm)
    }
    while (stack.length > 0) {
      const next = stack.pop()
      if (visited.has(next)) continue
      visited.add(next)
      const node = byId[next]
      if (!node) continue
      for (const d of node.depends || []) {
        const nd = normaliseComponentId(d)
        if (nd && !visited.has(nd) && byId[nd]) stack.push(nd)
      }
    }
    // Stable order = catalog index ascending so the layout below is
    // reproducible across runs.
    const indexById = Object.create(null)
    components.forEach((c, i) => { indexById[c.id] = i })
    return [...visited].sort((a, b) => (indexById[a] ?? 0) - (indexById[b] ?? 0))
  }

  // ── DAG construction ──────────────────────────────────────────────────────
  /**
   * Build the ordered list of nodes from the catalog + wizard selection.
   * Returns [{id, label, sub, kind, status, prog, sources}] where:
   *   kind ∈ {'super', 'bootstrap', 'component'}
   *   status ∈ {'pending', 'running', 'done', 'failed'}
   *   sources: bootstrap-kit ids whose Blueprint metadata contributed (for
   *     tooltip context).
   */
  function buildNodes(wizard) {
    const cat = catalog()
    const componentsById = Object.create(null)
    for (const c of cat.components) componentsById[c.id] = c
    const bootstrapIds = new Set(cat.bootstrapKit.map(b => b.id))

    const out = []

    // Hetzner-infra supernode: holds the four hcloud_* sub-bubbles and is
    // driven by the tofu-* phases.
    out.push({
      id: HETZNER_INFRA_ID,
      label: 'Hetzner infra',
      sub: 'OpenTofu Phase 0',
      kind: 'super',
      status: 'pending',
      prog: 0,
      hcloudSeen: new Set(),
      hcloudCounts: Object.create(null),
    })

    // Flux-bootstrap supernode: marks the cloud-init handoff to in-cluster
    // Flux + Crossplane.
    out.push({
      id: FLUX_BOOTSTRAP_ID,
      label: 'Flux bootstrap',
      sub: 'cloud-init → in-cluster',
      kind: 'super',
      status: 'pending',
      prog: 0,
    })

    // Bootstrap-kit Blueprints, in numeric install order.
    for (const b of cat.bootstrapKit) {
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
    const seedRaw = Array.isArray(wizard?.selectedComponents) ? wizard.selectedComponents : []
    const seedIds = seedRaw
      .map(normaliseComponentId)
      .filter(id => id && componentsById[id] && !bootstrapIds.has(id))
    const expanded = expandWithDependencies(seedIds, cat.components)
      .filter(id => !bootstrapIds.has(id))
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

  /**
   * Compute the edge list between adjacent nodes. The DAG is linear at
   * the supernode level (Hetzner → Flux → bootstrap-kit chain → selected
   * components). Within bootstrap-kit + selected components, we add the
   * Blueprint `depends` edges so the visual reflects the install graph.
   */
  function buildEdges(nodes) {
    const ids = nodes.map(n => n.id)
    const idSet = new Set(ids)
    const edges = []
    // Sequential flow: Hetzner → Flux-bootstrap.
    edges.push({ from: HETZNER_INFRA_ID, to: FLUX_BOOTSTRAP_ID })
    // Flux-bootstrap fans out to every bootstrap-kit + selected component
    // root that has no other in-edge in the kit. We compute that after we
    // wire the dependency edges below; for now collect the dep edges.
    const cat = catalog()
    const byId = Object.create(null)
    for (const c of cat.components) byId[c.id] = c
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
    // Compute in-degree among non-supernode nodes. Roots (in-degree 0)
    // get an explicit Flux-bootstrap → root edge so the graph is a single
    // connected component.
    const inDeg = Object.create(null)
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

  /**
   * Layout: layered top-down. Layer 0 = Hetzner-infra; Layer 1 = Flux-
   * bootstrap; Layer 2..N = topological layers of remaining nodes.
   *
   * Topological layering uses Kahn's algorithm operating on the dependency
   * edges only (exclude the explicit Flux-bootstrap→root edges so they do
   * not collapse all bootstrap-kit nodes into one giant fan). Nodes inside
   * each layer are kept in their original order, which preserves the
   * bootstrap-kit's numeric install order and the catalog index for
   * selected components.
   */
  function computeLayout(nodes, edges) {
    const layer = Object.create(null)
    layer[HETZNER_INFRA_ID] = 0
    layer[FLUX_BOOTSTRAP_ID] = 1
    // Build a forward adjacency for dep-only edges (between non-supernode
    // nodes); ignore Flux-bootstrap→X edges so we don't bias the layering.
    const adj = Object.create(null)
    const indeg = Object.create(null)
    for (const n of nodes) {
      if (n.kind === 'super') continue
      adj[n.id] = []
      indeg[n.id] = 0
    }
    for (const e of edges) {
      if (e.from === HETZNER_INFRA_ID || e.from === FLUX_BOOTSTRAP_ID) continue
      if (e.to === HETZNER_INFRA_ID || e.to === FLUX_BOOTSTRAP_ID) continue
      if (!adj[e.from]) continue
      adj[e.from].push(e.to)
      indeg[e.to] = (indeg[e.to] || 0) + 1
    }
    // Kahn's algorithm; assign layer = max(parent layers) + 1, base = 2.
    const ready = []
    for (const id of Object.keys(indeg)) if (indeg[id] === 0) ready.push(id)
    while (ready.length > 0) {
      const id = ready.shift()
      if (!(id in layer)) layer[id] = 2
      for (const m of adj[id] || []) {
        layer[m] = Math.max(layer[m] || 0, (layer[id] || 2) + 1)
        indeg[m]--
        if (indeg[m] === 0) ready.push(m)
      }
    }
    // Fallback for any node we somehow missed (shouldn't happen): layer 2.
    for (const n of nodes) if (!(n.id in layer)) layer[n.id] = 2

    // Bucket by layer in node order (preserves bootstrap-kit order +
    // catalog order within selected components).
    const buckets = Object.create(null)
    for (const n of nodes) {
      const L = layer[n.id]
      if (!buckets[L]) buckets[L] = []
      buckets[L].push(n.id)
    }
    return { layer, buckets, layers: Object.keys(buckets).map(Number).sort((a, b) => a - b) }
  }

  // ── State ─────────────────────────────────────────────────────────────────
  const state = {
    wizard: null,
    apiBase: deriveApiBase(),
    nodes: [],
    edges: [],
    layoutInfo: null,
    nodeById: Object.create(null),
    selectedNodeId: null,
    // Per-node accumulated tofu/raw lines for the expandable detail.
    detailLines: Object.create(null),
    startedAt: null,
    finishedAt: null,
    activePhase: null,
    snapshot: null,
    streamStatus: 'connecting', // connecting | streaming | completed | failed | none
    es: null,
    timer: null,
    svgW: 960,
    svgH: 600,
  }

  // ── Empty (no deployment) renderer ────────────────────────────────────────
  function renderEmpty(reason) {
    const body = $('body')
    body.innerHTML = `
      <div class="empty">
        <h1>No active deployment</h1>
        <p>${reason}</p>
        <p>Return to the wizard, complete the configuration, and click
          <strong style="color:var(--hi)">Launch OpenOva</strong> on the review step.</p>
        <a href="./">← Back to wizard</a>
      </div>
    `
    $('status-pill').classList.add('err')
    $('status-pill-t').textContent = 'No deployment'
    $('tb-org').textContent = '—'
    $('tb-meta').textContent = 'No active deployment'
  }

  // ── Topbar ────────────────────────────────────────────────────────────────
  function paintTopbar() {
    const w = state.wizard || {}
    const fqdn = w.sovereignSubdomain && w.sovereignPoolDomain
      ? `${w.sovereignSubdomain}.${w.sovereignPoolDomain}`
      : (w.sovereignFQDN || w.byoDomain || w.orgDomain || w.orgName || 'Sovereign')
    const meta = []
    if (w.topology) meta.push(String(w.topology).toUpperCase())
    if (Array.isArray(w.regionCloudRegions) && w.regionCloudRegions.length > 0) {
      meta.push(w.regionCloudRegions.filter(Boolean).join(' + '))
    }
    const totalSelected = state.nodes.filter(n => n.kind !== 'super').length
    meta.push(`${totalSelected} components`)
    $('tb-org').textContent = fqdn
    $('tb-meta').textContent = meta.join(' · ')
  }

  function paintStatus() {
    const pill = $('status-pill')
    const t = $('status-pill-t')
    pill.classList.remove('ok', 'err')
    if (state.streamStatus === 'connecting') {
      t.textContent = 'Connecting'
    } else if (state.streamStatus === 'streaming') {
      t.textContent = 'Provisioning'
    } else if (state.streamStatus === 'completed') {
      pill.classList.add('ok'); t.textContent = 'Ready'
    } else if (state.streamStatus === 'failed') {
      pill.classList.add('err'); t.textContent = 'Failed'
    }
  }

  function paintProgress() {
    const total = state.nodes.length
    if (total === 0) return
    let acc = 0
    for (const n of state.nodes) {
      if (n.status === 'done') acc += 1
      else if (n.status === 'running') acc += Math.max(0.5, n.prog || 0.5)
    }
    const pct = Math.min(100, Math.round((acc / total) * 100))
    $('prog-f').style.width = `${pct}%`
    $('prog-p').textContent = `${pct}%`
    const elapsed = state.startedAt
      ? Math.floor(((state.finishedAt || Date.now()) - state.startedAt) / 1000)
      : 0
    const m = Math.floor(elapsed / 60)
    const s = elapsed % 60
    $('prog-t').textContent = `${m}m ${String(s).padStart(2, '0')}s`
  }

  // ── SVG render ────────────────────────────────────────────────────────────
  const SF = { done: '#166534', running: '#1e3a5f', failed: '#7f1d1d', pending: '#0d1726' }
  const SC = { done: '#86efac', running: '#bae6fd', failed: '#fca5a5', pending: 'rgba(255,255,255,0.30)' }
  const SUPER_COLOR = '#818CF8'
  const BOOTSTRAP_COLOR = '#38BDF8'
  const COMPONENT_COLOR = '#A78BFA'

  function colorFor(node) {
    if (node.kind === 'super') return SUPER_COLOR
    if (node.kind === 'bootstrap') return BOOTSTRAP_COLOR
    return COMPONENT_COLOR
  }

  function buildSVG() {
    Le.innerHTML = ''
    Ln.innerHTML = ''
    state.nodeById = Object.create(null)
    for (const n of state.nodes) state.nodeById[n.id] = n

    // Edges first so they're under nodes.
    state.edges.forEach(e => {
      const p = mk('path', {
        id: `ep-${e.from}-${e.to}`,
        fill: 'none',
        stroke: 'rgba(148,163,184,.28)',
        'stroke-width': '1.5',
        'stroke-linecap': 'round',
        'marker-end': 'url(#m-wait)',
      })
      Le.appendChild(p)
    })

    // Nodes
    state.nodes.forEach(n => {
      const r = n.kind === 'super' ? 30 : 20
      const stroke = colorFor(n)
      const g = mk('g', { id: `nd-${n.id}`, class: 'ng' })
      g.appendChild(mk('circle', { r: (r + 9).toString(), class: 'nhov', stroke }))
      g.appendChild(mk('circle', { r: r.toString(), fill: SF.pending }))
      g.appendChild(mk('circle', { r: r.toString(), fill: 'none', stroke, 'stroke-width': '1.5', opacity: '.5' }))
      // Progress arc (added on render).
      // Status icon (added on render).
      const lb = mk('text', { y: (r + 14).toString(), class: 'nlabel', fill: SC.pending })
      lb.textContent = n.label
      g.appendChild(lb)
      const sb = mk('text', { y: (r + 24).toString(), class: 'nsub' })
      sb.textContent = n.sub || ''
      g.appendChild(sb)
      g.addEventListener('click', () => selectNode(n.id))
      g.addEventListener('mouseenter', (ev) => showTT(ev, nodeTTHtml(n)))
      g.addEventListener('mousemove', moveTT)
      g.addEventListener('mouseleave', hideTT)
      Ln.appendChild(g)
    })
    layoutSVG()
  }

  function layoutSVG() {
    if (!state.layoutInfo) return
    const rc = svgEl.getBoundingClientRect()
    if (rc.width > 100) { state.svgW = rc.width; state.svgH = rc.height }
    svgEl.setAttribute('viewBox', `0 0 ${state.svgW} ${state.svgH}`)
    const W = state.svgW, H = state.svgH
    const PAD_X = 70, PAD_Y = 60
    const layers = state.layoutInfo.layers
    const buckets = state.layoutInfo.buckets
    const nLayers = layers.length
    const innerW = Math.max(W - 2 * PAD_X, 1)
    // Position by layer: x = layer-fraction; y spreads nodes within the
    // layer evenly across the band.
    const pos = Object.create(null)
    layers.forEach((L, li) => {
      const x = nLayers === 1 ? W / 2 : PAD_X + (li / (nLayers - 1)) * innerW
      const ids = buckets[L]
      const innerH = Math.max(H - 2 * PAD_Y, 1)
      const slot = innerH / Math.max(ids.length, 1)
      ids.forEach((id, i) => {
        const y = PAD_Y + slot * (i + 0.5)
        pos[id] = { x, y }
      })
    })
    state.pos = pos

    // Apply transforms
    state.nodes.forEach(n => {
      const g = $(`nd-${n.id}`)
      if (!g) return
      const p = pos[n.id] || { x: W / 2, y: H / 2 }
      g.setAttribute('transform', `translate(${p.x.toFixed(1)},${p.y.toFixed(1)})`)
    })
    // Edge paths
    state.edges.forEach(e => {
      const p = $(`ep-${e.from}-${e.to}`)
      if (!p) return
      const fp = pos[e.from], tp = pos[e.to]
      if (!fp || !tp) { p.setAttribute('display', 'none'); return }
      p.removeAttribute('display')
      const fnNode = state.nodeById[e.from]
      const tnNode = state.nodeById[e.to]
      const fr = fnNode && fnNode.kind === 'super' ? 30 : 20
      const tr = tnNode && tnNode.kind === 'super' ? 30 : 20
      const dx = tp.x - fp.x, dy = tp.y - fp.y
      const d = Math.sqrt(dx * dx + dy * dy) || 1
      const nx = dx / d, ny = dy / d
      const sx = (fp.x + nx * (fr + 1)).toFixed(1)
      const sy = (fp.y + ny * (fr + 1)).toFixed(1)
      const ex = (tp.x - nx * (tr + 8)).toFixed(1)
      const ey = (tp.y - ny * (tr + 8)).toFixed(1)
      p.setAttribute('d', `M${sx},${sy} L${ex},${ey}`)
    })
  }

  function paintSVG() {
    state.nodes.forEach(n => {
      const g = $(`nd-${n.id}`)
      if (!g) return
      // Body fill
      const body = g.children[1]   // [hov, body, ring, ...]
      if (body) body.setAttribute('fill', SF[n.status] || SF.pending)
      const ring = g.children[2]
      if (ring) ring.setAttribute('opacity', n.status === 'pending' ? '.3' : '.7')
      // Label color
      const lb = g.querySelector('.nlabel')
      if (lb) lb.setAttribute('fill', n.status === 'pending' ? 'rgba(255,255,255,0.45)' : colorFor(n))
      // Progress arc
      let arc = g.querySelector('.narc')
      const r = n.kind === 'super' ? 30 : 20
      if (n.prog && n.prog > 0 && n.status !== 'pending') {
        const circ = 2 * Math.PI * r
        if (!arc) {
          arc = mk('circle', {
            r: r.toString(), class: 'narc', fill: 'none',
            stroke: colorFor(n), 'stroke-width': '3.5', 'stroke-linecap': 'round',
            transform: 'rotate(-90)', opacity: '.92',
          })
          g.appendChild(arc)
        }
        arc.setAttribute('stroke-dasharray', `${(n.prog * circ).toFixed(2)} ${circ.toFixed(2)}`)
      } else if (arc) {
        arc.remove()
      }
      // Status glyph for done / failed
      let glyph = g.querySelector('.nglyph')
      if (n.status === 'done' || n.status === 'failed') {
        if (!glyph) {
          glyph = mk('text', {
            class: 'nglyph', 'font-size': '12', 'text-anchor': 'middle',
            'dominant-baseline': 'central', 'font-family': 'Inter',
            'font-weight': '800', fill: 'rgba(5,12,30,0.95)', 'pointer-events': 'none',
          })
          g.appendChild(glyph)
        }
        glyph.textContent = n.status === 'done' ? '✓' : '✕'
      } else if (glyph) {
        glyph.remove()
      }
      // Selected highlight
      if (state.selectedNodeId === n.id) g.classList.add('selected')
      else g.classList.remove('selected')
    })
    // Edge marker tinting
    state.edges.forEach(e => {
      const p = $(`ep-${e.from}-${e.to}`)
      if (!p) return
      const fnNode = state.nodeById[e.from]
      const tnNode = state.nodeById[e.to]
      let mode = 'wait'
      if (fnNode && fnNode.status === 'done' && tnNode && tnNode.status !== 'pending') mode = 'done'
      else if (fnNode && fnNode.status === 'running') mode = 'act'
      if (fnNode && fnNode.status === 'failed') mode = 'fail'
      const stroke = mode === 'done' ? 'rgba(167,243,208,.5)'
        : mode === 'act' ? 'rgba(186,230,253,.6)'
        : mode === 'fail' ? 'rgba(248,113,113,.6)'
        : 'rgba(148,163,184,.30)'
      p.setAttribute('stroke', stroke)
      p.setAttribute('marker-end', `url(#m-${mode})`)
    })
  }

  // ── Sidebar ───────────────────────────────────────────────────────────────
  const sectionState = { phase0: 'open', bootstrap: 'open', selected: 'open' }

  function renderSidebar() {
    sbIn.innerHTML = ''
    const hdr = document.createElement('div')
    hdr.className = 'sb-hdr'
    hdr.textContent = 'DEPLOYMENT PROGRESS'
    sbIn.appendChild(hdr)

    const sections = [
      { key: 'phase0',    label: 'Phase 0 — Cloud',    nodes: state.nodes.filter(n => n.kind === 'super') },
      { key: 'bootstrap', label: 'Bootstrap kit',      nodes: state.nodes.filter(n => n.kind === 'bootstrap') },
      { key: 'selected',  label: 'Selected components', nodes: state.nodes.filter(n => n.kind === 'component') },
    ]
    for (const sec of sections) {
      if (sec.nodes.length === 0) continue
      const isOpen = sectionState[sec.key] === 'open'
      const done = sec.nodes.filter(n => n.status === 'done').length
      const running = sec.nodes.filter(n => n.status === 'running').length
      const pct = Math.round(((done + running * 0.5) / sec.nodes.length) * 100)
      const dotState = done === sec.nodes.length ? 'done' : running > 0 ? 'running' : sec.nodes.some(n => n.status === 'failed') ? 'failed' : ''
      const row = document.createElement('div')
      row.className = 'sec-row'
      row.innerHTML = `<span class="sec-arr${isOpen ? ' open' : ''}">▶</span><span class="sec-dot ${dotState}"></span><div style="flex:1;min-width:0"><div class="sec-name">${sec.label}</div><div class="sec-meta">${done}/${sec.nodes.length} ready · ${running} active</div></div><span class="sec-badge">${pct}%</span>`
      row.onclick = () => { sectionState[sec.key] = isOpen ? 'closed' : 'open'; renderSidebar() }
      sbIn.appendChild(row)
      const prog = document.createElement('div')
      prog.className = 'sb-prog'
      prog.innerHTML = `<div class="sb-prog-f" style="width:${pct}%"></div>`
      sbIn.appendChild(prog)
      const wrap = document.createElement('div')
      wrap.className = `node-rows${isOpen ? ' open' : ''}`
      for (const n of sec.nodes) {
        const r = document.createElement('div')
        r.className = `n-row${state.selectedNodeId === n.id ? ' selected' : ''}`
        r.onclick = () => selectNode(n.id)
        r.innerHTML = `<span class="n-dot ${n.status}"></span><span class="n-name">${n.label}</span><span class="n-time">${n.timeLabel || ''}</span>`
        wrap.appendChild(r)
      }
      sbIn.appendChild(wrap)
    }
  }

  // ── Tooltip ───────────────────────────────────────────────────────────────
  function moveTT(ev) {
    const x = ev.clientX + 16, y = ev.clientY + 16
    const W = window.innerWidth, H = window.innerHeight
    tt.style.left = (x + tt.offsetWidth > W ? ev.clientX - tt.offsetWidth - 10 : x) + 'px'
    tt.style.top = (y + tt.offsetHeight > H ? ev.clientY - tt.offsetHeight - 10 : y) + 'px'
  }
  function showTT(ev, html) { tt.innerHTML = html; tt.style.display = 'block'; moveTT(ev) }
  function hideTT() { tt.style.display = 'none' }

  function nodeTTHtml(n) {
    const lines = state.detailLines[n.id] || []
    const last = lines.slice(-3).map(l => l.message).join('\n')
    let extra = ''
    if (n.id === HETZNER_INFRA_ID) {
      const rows = HCLOUD_FAMILIES.map(f => {
        const c = n.hcloudCounts ? (n.hcloudCounts[f.id] || 0) : 0
        const seen = n.hcloudSeen ? n.hcloudSeen.has(f.id) : false
        return `<div class="ttr"><span class="ttk">${f.label}</span><span class="ttv">${c > 0 ? `${c} ops` : (seen ? 'started' : 'pending')}</span></div>`
      }).join('')
      extra = rows
    }
    return `<div class="ttn">${n.label}</div>
<div class="ttr"><span class="ttk">Kind</span><span class="ttv">${n.kind}</span></div>
<div class="ttr"><span class="ttk">Status</span><span class="ttv">${n.status}</span></div>
${extra}
<div class="ttr"><span class="ttk">Events</span><span class="ttv">${lines.length}</span></div>
${last ? `<div class="ttd">${escapeHtml(last)}</div>` : ''}`
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, c => (
      { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c] || c
    ))
  }

  // ── Selection + log panel ─────────────────────────────────────────────────
  function selectNode(id) {
    state.selectedNodeId = id
    paintLog()
    paintSVG()
    renderSidebar()
  }

  function paintLog() {
    const id = state.selectedNodeId
    if (!id) {
      lstream.innerHTML = `<div class="lempty">Select a bubble to view its log slice; the full stream is appended chronologically as the catalyst-api emits it.</div>`
      lchip.textContent = '—'
      lstat.textContent = ''
      return
    }
    const n = state.nodeById[id]
    if (!n) return
    lchip.textContent = n.label
    const evs = state.detailLines[id] || []
    lstat.textContent = `${evs.length} event${evs.length === 1 ? '' : 's'} · ${n.status}`
    if (evs.length === 0) {
      lstream.innerHTML = `<div class="lempty">No events yet for <strong style="color:var(--md)">${n.label}</strong>. The catalyst-api SSE stream feeds this panel as work begins.</div>`
      return
    }
    lstream.innerHTML = evs.map((e, i) => {
      const ts = (e.time || '').slice(11, 19) || '—'
      const cur = (i === evs.length - 1 && n.status === 'running') ? '<span class="lcur"></span>' : ''
      const cls = `ll ll-${e.level || 'info'}`
      const phaseChip = e.phase && e.phase !== 'tofu' ? `<span class="ll-meta">${escapeHtml(e.phase)}</span>` : ''
      return `<div class="${cls}"><span class="ll-ts">${ts}</span><span class="ll-msg">${phaseChip}${escapeHtml(e.message || '')}${cur}</span></div>`
    }).join('')
    lstream.scrollTop = lstream.scrollHeight
  }

  // ── SSE wiring ────────────────────────────────────────────────────────────
  function startStream(deploymentId) {
    const url = `${state.apiBase}/v1/deployments/${encodeURIComponent(deploymentId)}/logs`
    state.streamStatus = 'connecting'
    paintStatus()
    const es = new EventSource(url)
    state.es = es
    es.onopen = () => {
      state.streamStatus = 'streaming'
      state.startedAt = state.startedAt || Date.now()
      paintStatus()
    }
    es.onmessage = (msg) => {
      try {
        const ev = JSON.parse(msg.data)
        applyEvent(ev)
      } catch (err) {
        applyEvent({ time: new Date().toISOString(), phase: 'stream', level: 'warn', message: `[provision.html] dropped malformed event: ${err}` })
      }
    }
    es.addEventListener('done', (msg) => {
      try {
        const snap = JSON.parse(msg.data)
        state.snapshot = snap
        state.finishedAt = Date.now()
        if (snap && snap.status === 'ready') {
          // Mark every still-running bubble as done; the wizard's contract
          // says steady-state was reached when status==='ready'.
          for (const n of state.nodes) {
            if (n.status === 'running' || n.status === 'pending') {
              n.status = 'done'; n.prog = 1
            }
          }
          state.streamStatus = 'completed'
          if (snap.result && snap.result.consoleURL) {
            const cta = $('cta-console')
            cta.href = snap.result.consoleURL
            cta.textContent = `Open ${snap.result.sovereignFQDN || 'Console'} →`
            cta.style.display = ''
          }
        } else {
          state.streamStatus = 'failed'
          // Find the active phase and mark it failed
          if (state.activePhase) {
            const n = nodeForPhase(state.activePhase)
            if (n) { n.status = 'failed' }
          }
        }
        paintStatus(); paintSVG(); renderSidebar(); paintProgress()
      } catch (err) {
        // Non-fatal — leave the stream marked failed for visibility.
        state.streamStatus = 'failed'
        paintStatus()
      }
      es.close()
    })
    es.onerror = () => {
      // EventSource auto-reconnects unless we close. The browser handles
      // transient blips; only flip to failed when the connection is closed
      // and we never saw a `done` event.
      if (es.readyState === EventSource.CLOSED && state.streamStatus !== 'completed') {
        state.streamStatus = 'failed'
        paintStatus()
      }
    }
  }

  /**
   * Look up the node that owns a given catalyst-api phase id.
   * tofu-* + tofu (raw stdout) → Hetzner-infra supernode
   * flux-bootstrap            → Flux-bootstrap supernode
   * any other id              → matching catalog component (bp-<slug>)
   */
  function nodeForPhase(phase) {
    if (phase === FLUX_BOOTSTRAP_ID) return state.nodeById[FLUX_BOOTSTRAP_ID]
    if (phase === 'tofu' || TOFU_PHASES.has(phase)) return state.nodeById[HETZNER_INFRA_ID]
    // Forward-compatible: catalyst-api MAY one day emit per-Blueprint events
    // (e.g. phase="bp-cilium") when it watches Flux Kustomizations on the new
    // cluster — see the comment block at the top of provision.html. Honour
    // those by routing onto the matching bubble. Until then this branch is a
    // no-op (the bubble simply stays pending after flux-bootstrap done).
    const norm = normaliseComponentId(phase)
    if (norm && state.nodeById[norm]) return state.nodeById[norm]
    return null
  }

  function applyEvent(ev) {
    // Always append to the chronological master log (selected via the
    // Hetzner-infra detail when a tofu line, or the matching node detail
    // otherwise). The full stream is also visible by selecting the
    // currently-running bubble, since per-node lines are accumulated below.
    const node = nodeForPhase(ev.phase)

    // Phase-state machine
    if (TOFU_PHASES.has(ev.phase)) {
      const hetzner = state.nodeById[HETZNER_INFRA_ID]
      if (hetzner) {
        if (hetzner.status === 'pending') hetzner.status = 'running'
        if (ev.level === 'error') hetzner.status = 'failed'
        // Coarse progress: tofu-init=.15, plan=.30, apply=running, output=.95
        if (ev.phase === 'tofu-init') hetzner.prog = Math.max(hetzner.prog, 0.15)
        else if (ev.phase === 'tofu-plan') hetzner.prog = Math.max(hetzner.prog, 0.30)
        else if (ev.phase === 'tofu-output') {
          hetzner.prog = 1
          hetzner.status = 'done'
        }
      }
    } else if (ev.phase === 'tofu') {
      // Raw stdout/stderr from the `tofu` exec; parse for hcloud_* markers
      // to advance the Hetzner-infra sub-progress.
      const hetzner = state.nodeById[HETZNER_INFRA_ID]
      if (hetzner) {
        if (hetzner.status === 'pending') hetzner.status = 'running'
        const msg = ev.message || ''
        for (const f of HCLOUD_FAMILIES) {
          if (msg.indexOf(f.id) >= 0) {
            hetzner.hcloudSeen = hetzner.hcloudSeen || new Set()
            hetzner.hcloudCounts = hetzner.hcloudCounts || Object.create(null)
            hetzner.hcloudSeen.add(f.id)
            hetzner.hcloudCounts[f.id] = (hetzner.hcloudCounts[f.id] || 0) + 1
          }
        }
        // Advance prog from 0.30 → 0.90 as resources come up.
        const seen = hetzner.hcloudSeen ? hetzner.hcloudSeen.size : 0
        const target = 0.30 + (seen / HCLOUD_FAMILIES.length) * 0.60
        if (target > hetzner.prog) hetzner.prog = target
        if (ev.level === 'error') hetzner.status = 'failed'
      }
    } else if (ev.phase === FLUX_BOOTSTRAP_ID) {
      // Flux-bootstrap event — close out Hetzner-infra (if not done), open
      // the Flux-bootstrap bubble.
      const hetzner = state.nodeById[HETZNER_INFRA_ID]
      if (hetzner && hetzner.status !== 'failed') { hetzner.status = 'done'; hetzner.prog = 1 }
      const flux = state.nodeById[FLUX_BOOTSTRAP_ID]
      if (flux) {
        if (flux.status === 'pending') flux.status = 'running'
        flux.prog = Math.max(flux.prog, 0.5)
        if (ev.level === 'error') flux.status = 'failed'
      }
    } else if (node) {
      // Per-Blueprint event (forward-compatible path).
      if (node.status === 'pending') node.status = 'running'
      if (ev.level === 'error') node.status = 'failed'
    }

    // Track the active phase for failure attribution.
    if (ev.level !== 'error') state.activePhase = ev.phase

    // Per-bubble detail lines — accumulate so selecting a bubble shows its
    // slice of the stream.
    const targetId = (node && node.id) || HETZNER_INFRA_ID
    if (!state.detailLines[targetId]) state.detailLines[targetId] = []
    state.detailLines[targetId].push(ev)

    // First event of the stream — auto-select Hetzner-infra so the user sees
    // something immediately.
    if (!state.selectedNodeId) state.selectedNodeId = HETZNER_INFRA_ID

    paintSVG(); paintProgress(); paintStatus(); renderSidebar(); paintLog()
  }

  // ── Topbar toggles (also used by the empty state) ─────────────────────────
  window.toggleSB = function () { $('sb').classList.toggle('collapsed') }
  window.toggleLog = function () { $('lp').classList.toggle('collapsed') }
  window.toggleTheme = function () {
    const isDark = document.documentElement.getAttribute('data-theme') === 'dark'
    document.documentElement.setAttribute('data-theme', isDark ? 'light' : 'dark')
    const btn = $('tbtn'); if (btn) btn.textContent = isDark ? '☽' : '☀'
  }

  // ── Entry point ───────────────────────────────────────────────────────────
  function main() {
    state.wizard = readWizardState()
    if (!state.wizard) {
      renderEmpty('We could not find a wizard session in this browser. Either the deployment was launched in a different browser, or localStorage was cleared.')
      return
    }
    const deploymentId = state.wizard.deploymentId
    if (!deploymentId) {
      renderEmpty('Your wizard session is present, but no deployment has been launched yet — the catalyst-api never returned a deployment id for this browser.')
      return
    }
    state.nodes = buildNodes(state.wizard)
    state.edges = buildEdges(state.nodes)
    state.layoutInfo = computeLayout(state.nodes, state.edges)
    paintTopbar()
    buildSVG()
    paintSVG()
    paintProgress()
    paintStatus()
    renderSidebar()
    paintLog()
    // Keep elapsed timer ticking.
    state.timer = setInterval(paintProgress, 1000)
    // Re-layout on resize.
    window.addEventListener('resize', layoutSVG)
    // Wire the SSE stream.
    startStream(deploymentId)
  }

  // Defer to next frame so the SVG container has a real bounding box.
  requestAnimationFrame(main)
})()
