/**
 * Per-component logo tile surface — sourced from each project's
 * canonical brand artwork.
 *
 * The earlier 2-tone classification (light=slate-900, color=slate-100)
 * was synthetic and ignored that every brand publishes its mark on a
 * specific surface colour. Some marks (Grafana Alloy's grey wordmark,
 * FerretDB's fawn glyph) become illegible on a generic slate-100 tile;
 * placing each mark on its OWN brand surface — the surface the project
 * itself uses on its homepage / press kit — restores brand fidelity
 * the same way the SME marketplace does by baking surface into PNGs.
 *
 * Sources (per id, see `LOGO_SURFACE` comments):
 *   - Project's homepage hero strip (preferred)
 *   - Project's "Brand" / "Press kit" page
 *   - Project's GitHub README header banner
 *   - The vendored SVG's own primary fill where the artwork is
 *     intrinsically a single colour on transparent (e.g. Cilium's
 *     hexagon mosaic over navy on cilium.io).
 *
 * Both wizard themes use the SAME tile colour — homepage logos look
 * identical regardless of viewer theme, and the wizard mirrors that
 * convention. The card BODY surrounding the tile still flips with the
 * wizard theme (`--wiz-bg-input`); only the LOGO TILE is brand-locked.
 *
 * Convention for the few OpenOva-internal letter-mark components
 * (axon, bge, continuum, specter, powerdns) without a finalized
 * upstream brand mark: each is assigned a distinct slate / navy tone
 * from the OpenOva platform palette so the letter mark reads cleanly
 * and the tile doesn't visually clash with any neighbouring brand
 * tile in the same family.
 */

export interface LogoSurface {
  /** Tile background — the brand's canonical surface colour. */
  background: string
  /** Hairline border — uses a low-alpha derivative of the surface. */
  border: string
  /** Foreground for the letter-mark fallback (`IconFallback`). */
  text: string
}

/**
 * Per-id brand surface. Every component id in `componentGroups.ts`
 * has an entry here (63 ids). New components added there must add a
 * corresponding entry; the helper falls back to a neutral slate
 * surface only when an id is genuinely missing from the catalog.
 *
 * Trailing comment on each line is the source URL or rationale.
 */
export const LOGO_SURFACE: Record<string, LogoSurface> = {
  /* ── PILOT ─────────────────────────────────────────────────────── */
  // Flux — Kubernetes blue ; fluxcd.io hero hex glyph fills `#326CE5` (vendored SVG fill matches).
  flux:         { background: '#326CE5', border: 'rgba(255,255,255,0.18)', text: '#ffffff' },
  // Crossplane — multi-colour hand on dark ; crossplane.io brand uses dark `#1F2937` with `#FFCD3C` accent. Vendored SVG is multi-colour on transparent — dark surface lets the colours pop.
  crossplane:   { background: '#1F2937', border: 'rgba(255,255,255,0.10)', text: '#FFCD3C' },
  // Gitea — tea-leaf green on white ; gitea.com hero uses white surface with `#609926` mark (vendored SVG fill matches).
  gitea:        { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#609926' },
  // OpenTofu — black surface with yellow accent ; opentofu.org hero uses `#0F1115` near-black with `#FFDA18` accent.
  opentofu:     { background: '#0F1115', border: 'rgba(255,255,255,0.10)', text: '#FFDA18' },
  // vCluster — Loft.sh signature orange ; vcluster.com hero uses `#FF6600` (vendored SVG fill matches).
  vcluster:     { background: '#FF6600', border: 'rgba(255,255,255,0.18)', text: '#ffffff' },

  /* ── SPINE ─────────────────────────────────────────────────────── */
  // Cilium — hexagon mosaic over navy ; cilium.io hero uses `#1A2236` navy. Vendored SVG is the multi-colour hex mosaic on transparent — navy lets the colours read.
  cilium:       { background: '#1A2236', border: 'rgba(255,255,255,0.12)', text: '#ffffff' },
  // Coraza — OWASP red on near-black ; coraza.io hero uses `#1A1A1A` with red `#E53E3E` accent. Vendored PNG is red-on-transparent.
  coraza:       { background: '#1A1A1A', border: 'rgba(255,255,255,0.10)', text: '#E53E3E' },
  // PowerDNS — internal letter-mark ; navy from OpenOva SPINE palette.
  powerdns:     { background: '#0B1B33', border: 'rgba(255,255,255,0.10)', text: '#60A5FA' },
  // External-DNS — Kubernetes-sigs project ; vendored mark is wordmark on white. Use white tile to match.
  'external-dns': { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#326CE5' },
  // Envoy — purple wordmark on white ; envoyproxy.io hero uses purple mark on white. Vendored SVG fills `#B31AAB` / `#D163CE`.
  envoy:        { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#B31AAB' },
  // frpc — fatedier blue on white ; github.com/fatedier/frp banner uses `#477EE5` on white (vendored SVG fill matches).
  frpc:         { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#477EE5' },
  // NetBird — orange on dark navy ; netbird.io hero uses `#101724` with `#F68330` orange accent.
  netbird:      { background: '#101724', border: 'rgba(255,255,255,0.10)', text: '#F68330' },
  // strongSwan — deep red on white ; strongswan.org uses red wordmark on white.
  strongswan:   { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#9F1313' },

  /* ── SURGE ─────────────────────────────────────────────────────── */
  // VPA — Kubernetes-sigs project ; vendored SVG fill is `#326CE5` on white. Match.
  vpa:          { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#326CE5' },
  // KEDA — KEDA primary blue ; keda.sh hero uses `#326DE6` with white wordmark on dark.
  keda:         { background: '#326DE6', border: 'rgba(255,255,255,0.18)', text: '#ffffff' },
  // Reloader — Stakater orange on white ; reloader README banner uses orange `#FA8303` on white.
  reloader:     { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#FA8303' },
  // Continuum — internal letter-mark ; deep teal from OpenOva SURGE palette.
  continuum:    { background: '#0E2A2C', border: 'rgba(255,255,255,0.10)', text: '#5EEAD4' },

  /* ── SILO ──────────────────────────────────────────────────────── */
  // SeaweedFS — sea-blue gradient ; seaweedfs README banner uses gradient blue on white.
  seaweedfs:    { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#0E7C7B' },
  // Velero — Velero light blue on dark ; velero.io hero uses `#1B1F26` with `#239DE0` accent (vendored SVG fill matches).
  velero:       { background: '#1B1F26', border: 'rgba(255,255,255,0.10)', text: '#239DE0' },
  // Harbor — CNCF Harbor blue/green on white ; goharbor.io hero uses gradient `#60B932`/`#4596D8` (vendored SVG fills match).
  harbor:       { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#4596D8' },

  /* ── GUARDIAN ──────────────────────────────────────────────────── */
  // Falco — Falco teal on dark ; falco.org hero uses `#0E1116` with `#00B5AD` accent.
  falco:        { background: '#0E1116', border: 'rgba(255,255,255,0.10)', text: '#00B5AD' },
  // Kyverno — Kyverno blue/teal on white ; kyverno.io hero uses gradient blue on white.
  kyverno:      { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#0095D5' },
  // Trivy — Aqua trident purple wordmark on white ; trivy README uses purple wordmark on white.
  trivy:        { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#1904DA' },
  // Syft + Grype — Anchore navy ; anchore.com hero uses navy `#0F1837` with cyan accent. Vendored PNG is white-on-dark.
  'syft-grype': { background: '#0F1837', border: 'rgba(255,255,255,0.10)', text: '#3FA9F5' },
  // Sigstore — canonical cream surface with deep-blue mark ; sigstore.dev exposes `--c-egg-white #f6f0eb` as its surface and the CNCF artwork repo (sigstore/community/artwork/sigstore/icons/cream) ships `#faf7ef` cream as the brand backplate for the dark-blue mark `#2e2f71`. The vendored sigstore.svg is monochrome navy on transparent; the previous navy backplate rendered the mark invisible. Pairing the navy mark with the canonical cream surface restores brand fidelity (sigstore brand = navy + red signature on cream).
  sigstore:     { background: '#FAF7EF', border: 'rgba(15,23,42,0.10)', text: '#2E2F71' },
  // Keycloak — Keycloak grey-key wordmark on white ; keycloak.org uses grey/red key on white. Vendored SVG fills include `#4D4D4D` and `#E0E0E0`.
  keycloak:     { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#4D4D4D' },
  // OpenBao — bao palace teal on near-black ; openbao.org hero uses `#1A1A1A` with teal accent. Vendored SVG is white wordmark on transparent.
  openbao:      { background: '#1A1A1A', border: 'rgba(255,255,255,0.10)', text: '#02A5A5' },
  // External-Secrets — kubernetes-sigs project ; external-secrets.io uses Kubernetes blue on white.
  'external-secrets': { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#326CE5' },
  // Cert-Manager — Jetstack blue mosaic ; cert-manager.io hero uses Kubernetes blue on white. Vendored SVG fills include `#326CE5` (Kubernetes blue) on white.
  'cert-manager': { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#326CE5' },

  /* ── INSIGHTS ──────────────────────────────────────────────────── */
  // Grafana — orange-yellow gradient on dark navy ; grafana.com hero uses `#0B0F19` with the `#F46800` accent gradient. Vendored SVG runs `#F05A28` → `#FBCA0A`.
  grafana:      { background: '#0B0F19', border: 'rgba(255,255,255,0.10)', text: '#F46800' },
  // OpenTelemetry — OTel deep purple on white ; opentelemetry.io hero uses `#425CC7` purple with `#F5A800` accent on white.
  opentelemetry:{ background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#425CC7' },
  // Alloy — Grafana Alloy on white ; grafana.com/oss/alloy uses the icon-only orange swirl mark on a white surface. The vendored SVG is the canonical 44x44 swirl-only mark (`#FD6F00` on transparent), matching how Grafana presents Alloy in their hero strip.
  alloy:        { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#FD6F00' },
  // Loki — Grafana Loki amber on dark ; grafana.com/oss/loki uses `#0B0F19` with `#FFC832` accent. Vendored PNG is amber wordmark on transparent.
  loki:         { background: '#0B0F19', border: 'rgba(255,255,255,0.10)', text: '#FFC832' },
  // Mimir — Grafana Mimir cyan on dark ; grafana.com/oss/mimir uses `#0B0F19` with `#34CDF9` accent. Vendored PNG is white wordmark on transparent.
  mimir:        { background: '#0B0F19', border: 'rgba(255,255,255,0.10)', text: '#34CDF9' },
  // Tempo — Grafana Tempo gradient mark on dark ; grafana.com/oss/tempo serves `grafana-tempo.svg` as the canonical icon (yellow→orange linear gradient `#fff100` → `#f05a28`). Original viewBox is non-square (121.85x99.17) so the vendored SVG re-frames it into a square viewBox `0 -11.34 121.85 121.85` with the icon centered. Dark surface `#0B0F19` matches grafana.com hero treatment and lets the warm-gradient mark pop.
  tempo:        { background: '#0B0F19', border: 'rgba(255,255,255,0.10)', text: '#F46800' },
  // OpenSearch — OpenSearch deep blue on white ; opensearch.org hero uses `#005EB8` on white. Vendored SVG fills `#005EB8` and `#003B5C`.
  opensearch:   { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#005EB8' },
  // Litmus — LitmusChaos periwinkle backplate ; the canonical CNCF artwork (cncf/artwork/projects/litmus/icon/color) bakes a `#878EDE` periwinkle backplate into the icon itself. We extend that backplate to the tile surface so the logo sits flush with no visible seam between the SVG's internal rectangle and the surrounding tile. Inner mark uses `#5A44BA` deep purple over the periwinkle.
  litmus:       { background: '#878EDE', border: 'rgba(255,255,255,0.18)', text: '#ffffff' },
  // OpenMeter — magenta on dark ; openmeter.io uses `#1F1F1F` with `#F23173` accent.
  openmeter:    { background: '#1F1F1F', border: 'rgba(255,255,255,0.10)', text: '#F23173' },
  // Specter — internal letter-mark ; muted indigo from OpenOva INSIGHTS palette.
  specter:      { background: '#1E1B4B', border: 'rgba(255,255,255,0.10)', text: '#C7D2FE' },

  /* ── FABRIC ────────────────────────────────────────────────────── */
  // CloudNative PG — PostgreSQL blue elephant on white ; cloudnative-pg.io uses `#336791` mark on white.
  cnpg:         { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#336791' },
  // Valkey — Valkey crimson wordmark on white ; valkey.io uses `#DC382D` (Redis-derived) on white.
  valkey:       { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#DC382D' },
  // Strimzi — Strimzi cyan on navy ; strimzi.io hero uses `#192C47` with `#54BAD8` accent. Vendored SVG fills match.
  strimzi:      { background: '#192C47', border: 'rgba(255,255,255,0.12)', text: '#54BAD8' },
  // Debezium — green-cyan accent on dark ; debezium.io uses dark surface with `#91D443` and `#48BFE0` accents. Vendored SVG fills match.
  debezium:     { background: '#1F2937', border: 'rgba(255,255,255,0.10)', text: '#91D443' },
  // Apache Flink — squirrel on white ; flink.apache.org renders the multi-colour `flink_squirrel_500.png` mark on a `#FFFFFF` body bg in the hero strip. The vendored SVG is the same multi-colour squirrel (plum `#430A1D` outline + coral `#E65270` accents + cream highlights) on transparent — placing it on white matches the apache.org canonical surface treatment.
  flink:        { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#E65270' },
  // Temporal — Temporal signature blue ; temporal.io hero uses `#127ED1` with white wordmark. Vendored SVG is white-on-transparent.
  temporal:     { background: '#127ED1', border: 'rgba(255,255,255,0.18)', text: '#ffffff' },
  // ClickHouse — ClickHouse yellow on yellow ; clickhouse.com brand uses `#FFCC00` yellow with black wordmark.
  clickhouse:   { background: '#FFCC00', border: 'rgba(15,23,42,0.18)', text: '#000000' },
  // FerretDB — fawn glyph on navy ; ferretdb.com hero uses `#042B41` deep navy with white wordmark. The PNG mark is greyscale on transparent — navy keeps brand fidelity.
  ferretdb:     { background: '#042B41', border: 'rgba(255,255,255,0.12)', text: '#D4A574' },
  // Iceberg — Apache Iceberg blue glacier on white ; iceberg.apache.org uses `#277ABE` blue on white. Vendored SVG fills match.
  iceberg:      { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#277ABE' },
  // Superset — Apache Superset cyan on white ; superset.apache.org uses `#20A7C9` mark on white.
  superset:     { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#20A7C9' },

  /* ── CORTEX ────────────────────────────────────────────────────── */
  // KServe — Kubernetes blue cloud on white ; kserve.github.io uses `#326CE5` mark on white. Vendored SVG fills match.
  kserve:       { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#326CE5' },
  // Knative — Knative blue on white ; knative.dev uses `#1A73E8` mark on white.
  knative:      { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#1A73E8' },
  // Axon — internal letter-mark ; deep purple from OpenOva CORTEX palette.
  axon:         { background: '#2D1B69', border: 'rgba(255,255,255,0.12)', text: '#A78BFA' },
  // Neo4j — Neo4j blue on white ; neo4j.com hero uses `#018BFF` mark on white. Vendored SVG fill matches.
  neo4j:        { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#018BFF' },
  // vLLM — vLLM magenta on near-black ; docs.vllm.ai uses `#0F0F23` with `#FA64C5` accent. Vendored PNG is white wordmark.
  vllm:         { background: '#0F0F23', border: 'rgba(255,255,255,0.10)', text: '#FA64C5' },
  // Milvus — Milvus cyan on near-black ; milvus.io uses `#0E0F2C` with `#33B5F1` accent. Vendored SVG fill matches.
  milvus:       { background: '#0E0F2C', border: 'rgba(255,255,255,0.10)', text: '#33B5F1' },
  // BGE — internal letter-mark ; muted teal from OpenOva CORTEX palette (BGE is BAAI's embedding-model family, no upstream brand mark).
  bge:          { background: '#0E2A2C', border: 'rgba(255,255,255,0.10)', text: '#5EEAD4' },
  // LangFuse — Langfuse mint on near-black ; langfuse.com hero uses `#0A0A0A` with `#83CDA1` accent. Vendored PNG is white wordmark.
  langfuse:     { background: '#0A0A0A', border: 'rgba(255,255,255,0.10)', text: '#83CDA1' },
  // LibreChat — LibreChat cyan-blue gradient on dark ; librechat.ai hero uses `#0E1828` with `#21FACF` accent. Vendored SVG runs `#21facf` → `#0970ef`.
  librechat:    { background: '#0E1828', border: 'rgba(255,255,255,0.10)', text: '#21FACF' },

  /* ── RELAY ─────────────────────────────────────────────────────── */
  // Stalwart — coral red wordmark on navy ; stalw.art hero uses `#100E42` deep navy with `#DB2D54` red accent. Vendored SVG fills match.
  stalwart:     { background: '#100E42', border: 'rgba(255,255,255,0.12)', text: '#DB2D54' },
  // LiveKit — aqua/cyan gradient on near-black ; livekit.io hero uses `#070D1B` with cyan/aqua gradient. Vendored SVG fills include `#5BBFE4` and `#7AFAE1`.
  livekit:      { background: '#070D1B', border: 'rgba(255,255,255,0.10)', text: '#7AFAE1' },
  // STUNner — l7mp orange on dark ; l7mp.io / stunner uses dark surface with `#FF7849` orange accent.
  stunner:      { background: '#0F172A', border: 'rgba(255,255,255,0.10)', text: '#FF7849' },
  // Matrix — Matrix.org black-on-white ; the matrix.org/branding page declares the official colours as `#000000` Black and `#FFFFFF` White. The vendored SVG fills `#000` on transparent — placing the black wordmark on a white backplate is the canonical pairing (the previous black backplate rendered the mark invisible).
  matrix:       { background: '#FFFFFF', border: 'rgba(15,23,42,0.10)', text: '#000000' },
  // Ntfy — canonical ntfy.sh teal gradient with the tile surface flush against the gradient's deep-teal anchor. The canonical logo (`https://ntfy.sh/_next/static/media/logo.077f6a13.svg`, vendored as ntfy.svg) is a 50x50 square SVG with a `#348878` → `#56bda8` linear gradient. We extend the gradient's start colour `#348878` to the tile so the logo's deep-teal corner sits flush with the surrounding surface — the bg colour blooms out of the logo with no visible seam.
  ntfy:         { background: '#348878', border: 'rgba(255,255,255,0.18)', text: '#ffffff' },
}

/**
 * Neutral fallback — used ONLY when an id is missing from `LOGO_SURFACE`.
 * The neutral slate avoids the previous synthetic 2-tone trap; if the
 * fallback ever fires in production it signals a missing entry, which
 * is a content-side fix (add the id above), not an algorithmic one.
 */
const FALLBACK_SURFACE: LogoSurface = {
  background: '#0F172A',
  border: 'rgba(255,255,255,0.10)',
  text: '#F8FAFC',
}

/**
 * Resolve the brand surface for a component id. Returns the per-id
 * entry from `LOGO_SURFACE`, or the neutral fallback when an id is
 * missing (which should be treated as a catalog bug — add the id).
 */
export function getLogoSurface(componentId: string | undefined): LogoSurface {
  if (!componentId) return FALLBACK_SURFACE
  return LOGO_SURFACE[componentId] ?? FALLBACK_SURFACE
}

/* ─────────────────────────────────────────────────────────────────────
 * Backwards-compatibility shim
 *
 * The earlier 2-tone API (`getLogoToneStyle`) is still imported by
 * StepComponents / StepReview / Marketplace pages. We retain it so the
 * call-site refactor is mechanical, but the underlying data is now
 * the per-brand map above. Both functions return the same shape.
 * ─────────────────────────────────────────────────────────────────── */

export type LogoToneStyle = LogoSurface
export const getLogoToneStyle = getLogoSurface
