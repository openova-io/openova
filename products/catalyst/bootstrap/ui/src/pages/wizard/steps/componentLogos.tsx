/**
 * Inline SVG logo marks for all platform components.
 * 18×18 viewBox — rounded badge with brand color + recognisable shape or letter mark.
 */

function Badge(bg: string, _fg = '#fff', children: React.ReactNode) {
  return (
    <svg viewBox="0 0 18 18" width={16} height={16} style={{ flexShrink: 0, display: 'block' }}>
      <rect width={18} height={18} rx={4} fill={bg} />
      {children}
    </svg>
  )
}

// ── PILOT ─────────────────────────────────────────────────────────
export const logo_flux = Badge('#5468FF', '#fff',
  // Flux: two circular arrows → cycle
  <><path d="M9 4a5 5 0 0 1 4.33 2.5" stroke="#fff" strokeWidth="1.6" fill="none" strokeLinecap="round"/>
    <path d="M13.33 6.5L14.5 4.5l-2.3.5" stroke="#fff" strokeWidth="1.5" fill="none" strokeLinecap="round" strokeLinejoin="round"/>
    <path d="M9 14a5 5 0 0 1-4.33-2.5" stroke="#fff" strokeWidth="1.6" fill="none" strokeLinecap="round"/>
    <path d="M4.67 11.5L3.5 13.5l2.3-.5" stroke="#fff" strokeWidth="1.5" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

export const logo_crossplane = Badge('#D64292', '#fff',
  // Crossplane: X mark
  <><line x1="5" y1="5" x2="13" y2="13" stroke="#fff" strokeWidth="2.2" strokeLinecap="round"/>
    <line x1="13" y1="5" x2="5" y2="13" stroke="#fff" strokeWidth="2.2" strokeLinecap="round"/></>
)

export const logo_gitea = Badge('#609926', '#fff',
  // Gitea: tea cup silhouette
  <><path d="M5 8h8v4a3 3 0 0 1-3 3H8a3 3 0 0 1-3-3V8z" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <path d="M13 9.5c1.1 0 2 .7 2 1.5s-.9 1.5-2 1.5" stroke="#fff" strokeWidth="1.3" fill="none" strokeLinecap="round"/>
    <path d="M7 8V6.5a2 2 0 0 1 4 0V8" stroke="#fff" strokeWidth="1.2" fill="none" strokeLinecap="round"/></>
)

export const logo_opentofu = Badge('#7B42BC', '#fff',
  // OpenTofu: tofu block (rounded rect with dots)
  <><rect x="3" y="5" width="12" height="8" rx="2" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <circle cx="7" cy="9" r="1" fill="#fff"/>
    <circle cx="11" cy="9" r="1" fill="#fff"/></>
)

// ── SPINE ──────────────────────────────────────────────────────────
export const logo_cilium = Badge('#F4A01C', '#fff',
  // Cilium: hexagon (eBPF)
  <path d="M9 3l5 3v6l-5 3-5-3V6z" stroke="#fff" strokeWidth="1.4" fill="none"/>
)

export const logo_coraza = Badge('#2E7D32', '#fff',
  // Coraza WAF: shield
  <path d="M9 3l5 2.5v4C14 12.5 11.5 15 9 16 6.5 15 4 12.5 4 9.5v-4z" stroke="#fff" strokeWidth="1.3" fill="none"/>
)

export const logo_externalDns = Badge('#326CE5', '#fff',
  // External DNS: globe + arrow
  <><circle cx="9" cy="9" r="5" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <path d="M4 9h10M9 4c-1.5 2-1.5 6 0 10M9 4c1.5 2 1.5 6 0 10" stroke="#fff" strokeWidth="1" fill="none"/></>
)

export const logo_envoy = Badge('#AC6199', '#fff',
  // Envoy: E letter stylised
  <><path d="M5 5h6M5 9h5M5 13h6" stroke="#fff" strokeWidth="1.8" strokeLinecap="round"/></>
)

export const logo_k8gb = Badge('#0078D4', '#fff',
  // k8gb: globe with load balance arrows
  <><circle cx="9" cy="9" r="5" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <path d="M7 9h4M9 7v4" stroke="#fff" strokeWidth="1.5" strokeLinecap="round"/></>
)

export const logo_frpc = Badge('#374151', '#fff',
  <><path d="M4 6h4v2H4zM10 6h4v2h-4zM6 10l6-1" stroke="#fff" strokeWidth="1.3" fill="none" strokeLinecap="round"/></>
)

export const logo_netbird = Badge('#0EA5E9', '#fff',
  // NetBird: bird silhouette
  <path d="M3 11c2-2 4-5 6-5s3 2 5 1c-1.5 2-3 4-5 4-1.5 0-3-1-6 0z" fill="#fff"/>
)

export const logo_strongswan = Badge('#CC0000', '#fff',
  // strongSwan: S VPN lock
  <><path d="M6 7a3 3 0 0 1 6 0v2H6V7z" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <rect x="5" y="9" width="8" height="5" rx="1.5" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <circle cx="9" cy="12" r="1" fill="#fff"/></>
)

// ── SURGE ──────────────────────────────────────────────────────────
export const logo_vpa = Badge('#326CE5', '#fff',
  // VPA: upward arrow in box
  <><rect x="4" y="4" width="10" height="10" rx="2" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <path d="M9 12V8M7 10l2-2 2 2" stroke="#fff" strokeWidth="1.5" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

export const logo_keda = Badge('#3B82F6', '#fff',
  // KEDA: K + scale arrows
  <><path d="M5 5v8M5 9l5-4M5 9l5 4" stroke="#fff" strokeWidth="1.6" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

export const logo_reloader = Badge('#F59E0B', '#fff',
  // Reloader: refresh circular arrow
  <><path d="M13 9A4 4 0 1 1 9 5" stroke="#fff" strokeWidth="1.6" fill="none" strokeLinecap="round"/>
    <path d="M9 5l2.5-2-1 2.5" stroke="#fff" strokeWidth="1.5" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

export const logo_continuum = Badge('#38BDF8', '#fff',
  // Continuum: infinity-like loop
  <path d="M4 9c0-2 2-3 4-1.5C10 9 12 11 14 9s-2-5-4-3C8.5 7.5 6 11 4 9z" stroke="#fff" strokeWidth="1.4" fill="none"/>
)

// ── SILO ───────────────────────────────────────────────────────────
export const logo_minio = Badge('#C72E49', '#fff',
  // MinIO: M letter bold
  <path d="M4 13V6l3.5 4L9 8l1.5 2L14 6v7" stroke="#fff" strokeWidth="1.6" fill="none" strokeLinecap="round" strokeLinejoin="round"/>
)

export const logo_velero = Badge('#E85D04', '#fff',
  // Velero: V + shield backup
  <><path d="M5 5l4 8 4-8" stroke="#fff" strokeWidth="1.8" fill="none" strokeLinecap="round" strokeLinejoin="round"/>
    <path d="M7 5h4" stroke="#fff" strokeWidth="1.3" strokeLinecap="round"/></>
)

export const logo_harbor = Badge('#60B932', '#fff',
  // Harbor: lighthouse H
  <><path d="M6 5v8M12 5v8M6 9h6" stroke="#fff" strokeWidth="1.8" strokeLinecap="round"/>
    <path d="M7 5h4" stroke="#fff" strokeWidth="1.3" strokeLinecap="round"/></>
)

// ── GUARDIAN ───────────────────────────────────────────────────────
export const logo_kyverno = Badge('#1D6FA4', '#fff',
  // Kyverno: policy shield with check
  <><path d="M9 3l5 2v4c0 3-2.5 5-5 6-2.5-1-5-3-5-6V5z" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <path d="M6.5 9l2 2 3-3" stroke="#fff" strokeWidth="1.6" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

export const logo_openbao = Badge('#FFB547', '#1a1a1a',
  // OpenBao (Vault fork): key shape
  <><circle cx="8" cy="8" r="3" stroke="#1a1a1a" strokeWidth="1.5" fill="none"/>
    <path d="M10.5 10.5l3 3M13 12l1 1" stroke="#1a1a1a" strokeWidth="1.6" strokeLinecap="round"/></>
)

export const logo_externalSecrets = Badge('#22D3EE', '#fff',
  // ESO: lock with secret dots
  <><path d="M6 8a3 3 0 0 1 6 0v1H6V8z" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <rect x="5" y="9" width="8" height="5" rx="1.5" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <circle cx="9" cy="11.5" r="1" fill="#fff"/></>
)

export const logo_certManager = Badge('#326CE5', '#fff',
  // cert-manager: certificate document
  <><rect x="4" y="3" width="10" height="12" rx="1.5" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <path d="M6.5 7h5M6.5 9.5h5M6.5 12h3" stroke="#fff" strokeWidth="1.2" strokeLinecap="round"/></>
)

export const logo_falco = Badge('#00AEBA', '#fff',
  // Falco: falcon wing sweep
  <path d="M3 14c2-3 4-7 6-9 1 2 3 4 6 5-3 0-5-1-7 0-1.5.7-3 2.5-5 4z" fill="#fff"/>
)

export const logo_trivy = Badge('#1904DA', '#fff',
  // Trivy: scanner magnifier + shield
  <><path d="M9 3l4.5 2v3.5C13.5 12 11.5 14.5 9 15.5 6.5 14.5 4.5 12 4.5 8.5V5z" stroke="#fff" strokeWidth="1.2" fill="none"/>
    <circle cx="9" cy="9" r="2" stroke="#fff" strokeWidth="1.2" fill="none"/>
    <path d="M10.5 10.5l2 2" stroke="#fff" strokeWidth="1.4" strokeLinecap="round"/></>
)

export const logo_syftGrype = Badge('#9D4EDD', '#fff',
  // Syft/Grype: SBOM layers
  <><path d="M4 6h10M4 9h10M4 12h7" stroke="#fff" strokeWidth="1.4" strokeLinecap="round"/>
    <circle cx="13" cy="12" r="2" fill="#9D4EDD" stroke="#fff" strokeWidth="1.3"/></>
)

export const logo_sigstore = Badge('#7C3AED', '#fff',
  // Sigstore: wax seal circle + S
  <><circle cx="9" cy="9" r="5.5" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <path d="M7 7.5a2 2 0 0 1 4 0c0 1-1 1.5-2 2s-2 1-2 2a2 2 0 0 0 4 0" stroke="#fff" strokeWidth="1.5" fill="none" strokeLinecap="round"/></>
)

export const logo_keycloak = Badge('#4D9FEB', '#fff',
  // Keycloak: K + key
  <><path d="M5 5v8M5 9l5-4M5 9l5 4" stroke="#fff" strokeWidth="1.6" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

// ── INSIGHTS ───────────────────────────────────────────────────────
export const logo_grafana = Badge('#F46800', '#fff',
  // Grafana: stylised G / donut chart arc
  <><path d="M14 9A5 5 0 1 1 9 4" stroke="#fff" strokeWidth="1.8" fill="none" strokeLinecap="round"/>
    <path d="M9 4v5h5" stroke="#fff" strokeWidth="1.4" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

export const logo_opentelemetry = Badge('#4F62AE', '#fff',
  // OTel: three interconnected dots (traces)
  <><circle cx="5" cy="9" r="1.5" fill="#fff"/>
    <circle cx="13" cy="5.5" r="1.5" fill="#fff"/>
    <circle cx="13" cy="12.5" r="1.5" fill="#fff"/>
    <path d="M6.5 9L11.5 6M6.5 9l5 3.5" stroke="#fff" strokeWidth="1.2" strokeLinecap="round"/></>
)

export const logo_alloy = Badge('#F46800', '#fff',
  // Alloy (Grafana): collector funnel
  <path d="M4 5h10l-3.5 5v4h-3V10z" stroke="#fff" strokeWidth="1.3" fill="none" strokeLinejoin="round"/>
)

export const logo_loki = Badge('#F9B130', '#1a1a1a',
  // Loki: L letter with log lines
  <><path d="M6 5v8h6" stroke="#1a1a1a" strokeWidth="2" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

export const logo_mimir = Badge('#68B0C0', '#fff',
  // Mimir: M letter
  <path d="M4 13V6l3.5 4L9 8l1.5 2L14 6v7" stroke="#fff" strokeWidth="1.6" fill="none" strokeLinecap="round" strokeLinejoin="round"/>
)

export const logo_tempo = Badge('#E36CCB', '#fff',
  // Tempo: T with timeline
  <><path d="M5 6h8M9 6v7" stroke="#fff" strokeWidth="1.8" fill="none" strokeLinecap="round"/>
    <path d="M6 11h6" stroke="#fff" strokeWidth="1" strokeLinecap="round" strokeDasharray="1 1.5"/></>
)

export const logo_opensearch = Badge('#005EB8', '#fff',
  // OpenSearch: magnifier
  <><circle cx="8" cy="8" r="4" stroke="#fff" strokeWidth="1.5" fill="none"/>
    <path d="M11 11l3 3" stroke="#fff" strokeWidth="1.8" strokeLinecap="round"/></>
)

export const logo_litmus = Badge('#5B42BC', '#fff',
  // Litmus: chaos lightning bolt
  <path d="M11 4L7 9.5h4L7 14" stroke="#fff" strokeWidth="1.8" fill="none" strokeLinecap="round" strokeLinejoin="round"/>
)

export const logo_openmeter = Badge('#7C3AED', '#fff',
  // OpenMeter: bar chart / usage meter
  <><path d="M5 13V9M8 13V7M11 13V5M14 13V9" stroke="#fff" strokeWidth="1.5" strokeLinecap="round"/>
    <path d="M4 13h11" stroke="#fff" strokeWidth="1" strokeLinecap="round"/></>
)

export const logo_specter = Badge('#818CF8', '#fff',
  // Specter (OpenOva AI): eye / radar
  <><ellipse cx="9" cy="9" rx="5" ry="3.5" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <circle cx="9" cy="9" r="1.5" fill="#fff"/></>
)

// ── FABRIC ─────────────────────────────────────────────────────────
export const logo_cnpg = Badge('#336791', '#fff',
  // CloudNativePG: elephant (PostgreSQL)
  <><ellipse cx="9" cy="10" rx="4" ry="4.5" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <path d="M13 8c1.5-1 2-3 1-4" stroke="#fff" strokeWidth="1.3" fill="none" strokeLinecap="round"/>
    <path d="M9 5.5v-2" stroke="#fff" strokeWidth="1.3" strokeLinecap="round"/></>
)

export const logo_valkey = Badge('#DC382D', '#fff',
  // Valkey (Redis fork): V + diamond
  <><path d="M5 6l4 7 4-7" stroke="#fff" strokeWidth="1.8" fill="none" strokeLinecap="round" strokeLinejoin="round"/>
    <path d="M7 6h4" stroke="#fff" strokeWidth="1.3" strokeLinecap="round"/></>
)

export const logo_strimzi = Badge('#003087', '#fff',
  // Strimzi (Kafka): lightning bolt in circle
  <><circle cx="9" cy="9" r="5.5" stroke="#fff" strokeWidth="1.2" fill="none"/>
    <path d="M10.5 5.5L8 9.5h3L8.5 13" stroke="#fff" strokeWidth="1.5" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

export const logo_debezium = Badge('#91D443', '#1a1a1a',
  // Debezium: D + change arrows
  <><path d="M5 5h3a4 4 0 0 1 0 8H5z" stroke="#1a1a1a" strokeWidth="1.4" fill="none"/>
    <path d="M11 8l2-1.5-2-1.5M11 12l2-1.5-2-1.5" stroke="#1a1a1a" strokeWidth="1.3" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

export const logo_flink = Badge('#E6526F', '#fff',
  // Apache Flink: F + stream lines
  <><path d="M5 5h7M5 9h5" stroke="#fff" strokeWidth="1.8" strokeLinecap="round"/>
    <path d="M5 13V5" stroke="#fff" strokeWidth="1.8" strokeLinecap="round"/>
    <path d="M10 12c2-1 4-.5 4 1" stroke="#fff" strokeWidth="1.3" strokeLinecap="round" fill="none"/></>
)

export const logo_temporal = Badge('#118DF0', '#fff',
  // Temporal: T in rounded square + clock
  <><path d="M5.5 6h7M9 6v6" stroke="#fff" strokeWidth="1.8" fill="none" strokeLinecap="round"/>
    <circle cx="13" cy="13" r="2.5" fill="#118DF0" stroke="#fff" strokeWidth="1"/>
    <path d="M13 12v1.2l.8.8" stroke="#fff" strokeWidth="1" strokeLinecap="round"/></>
)

export const logo_clickhouse = Badge('#151515', '#FCFF74',
  // ClickHouse: characteristic dots grid
  <><rect x="4" y="5" width="2.5" height="8" rx="1" fill="#FCFF74"/>
    <rect x="7.75" y="7" width="2.5" height="6" rx="1" fill="#FCFF74"/>
    <rect x="11.5" y="4" width="2.5" height="9" rx="1" fill="#FCFF74"/></>
)

export const logo_ferretdb = Badge('#FF8C00', '#fff',
  // FerretDB: ferret face / F
  <><path d="M5 5h5a3 3 0 0 1 0 6H5" stroke="#fff" strokeWidth="1.6" fill="none" strokeLinecap="round"/>
    <path d="M5 5v8" stroke="#fff" strokeWidth="1.6" strokeLinecap="round"/>
    <path d="M5 9h4" stroke="#fff" strokeWidth="1.4" strokeLinecap="round"/></>
)

export const logo_iceberg = Badge('#008FCC', '#fff',
  // Iceberg: triangle (iceberg tip above water line)
  <><path d="M9 4L13.5 13H4.5z" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <path d="M4 10h10" stroke="#fff" strokeWidth="1" strokeLinecap="round" strokeDasharray="2 1.5"/>
    <path d="M6 13h6" stroke="rgba(255,255,255,0.4)" strokeWidth="1"/></>
)

export const logo_superset = Badge('#20A7C9', '#fff',
  // Superset: S infinity / dashboard
  <path d="M5 9c0-2.5 4-5 6-2.5S15 12 13 13c-2 1-6-1-6-4" stroke="#fff" strokeWidth="1.6" fill="none" strokeLinecap="round"/>
)

// ── CORTEX ─────────────────────────────────────────────────────────
export const logo_kserve = Badge('#326CE5', '#fff',
  // KServe: K + serving arrow
  <><path d="M5 5v8M5 9l5-4M5 9l5 4" stroke="#fff" strokeWidth="1.6" fill="none" strokeLinecap="round" strokeLinejoin="round"/>
    <path d="M12 9h3M13.5 7.5L15 9l-1.5 1.5" stroke="#fff" strokeWidth="1.4" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

export const logo_knative = Badge('#0865AD', '#fff',
  // Knative: K + serverless lambda
  <><path d="M5 5v8M5 9l5-4M5 9l5 4" stroke="#fff" strokeWidth="1.6" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

export const logo_axon = Badge('#38BDF8', '#fff',
  // Axon (OpenOva): neural link A
  <><path d="M4.5 13L9 5l4.5 8" stroke="#fff" strokeWidth="1.7" fill="none" strokeLinecap="round" strokeLinejoin="round"/>
    <path d="M6.5 10.5h5" stroke="#fff" strokeWidth="1.4" strokeLinecap="round"/></>
)

export const logo_neo4j = Badge('#008CC1', '#fff',
  // Neo4j: graph nodes
  <><circle cx="6" cy="9" r="2" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <circle cx="13" cy="6" r="1.5" stroke="#fff" strokeWidth="1.2" fill="none"/>
    <circle cx="13" cy="12" r="1.5" stroke="#fff" strokeWidth="1.2" fill="none"/>
    <path d="M7.9 8.1L11.5 6.7M7.9 9.9l3.6 1.5" stroke="#fff" strokeWidth="1.1"/></>
)

export const logo_vllm = Badge('#1a1a2e', '#fdb515',
  // vLLM: V + LLM token stream
  <><path d="M5 6l4 6 4-6" stroke="#fdb515" strokeWidth="1.8" fill="none" strokeLinecap="round" strokeLinejoin="round"/>
    <path d="M4 12h4M10 12h4" stroke="#fdb515" strokeWidth="1" strokeLinecap="round" strokeDasharray="1 1"/></>
)

export const logo_milvus = Badge('#00D4AA', '#fff',
  // Milvus: vector space dots
  <><circle cx="6" cy="7" r="1.5" fill="#fff"/>
    <circle cx="12" cy="7" r="1.5" fill="#fff"/>
    <circle cx="6" cy="13" r="1.5" fill="#fff"/>
    <circle cx="12" cy="11" r="1.5" fill="#fff"/>
    <path d="M7.5 7h3M7.5 13l3-2" stroke="#fff" strokeWidth="1" strokeLinecap="round" strokeDasharray="1 1"/></>
)

export const logo_bge = Badge('#1E3A5F', '#fff',
  // BGE: embedding wave
  <><path d="M4 9c1-3 2-3 3 0s2 3 3 0 2-3 3 0" stroke="#fff" strokeWidth="1.6" fill="none" strokeLinecap="round"/>
    <path d="M4 12c1-2 2-2 3 0" stroke="#fff" strokeWidth="1.2" fill="none" strokeLinecap="round" strokeDasharray="1 1"/></>
)

export const logo_langfuse = Badge('#EC4899', '#fff',
  // LangFuse: L + trace path
  <><path d="M6 5v8h6" stroke="#fff" strokeWidth="1.8" fill="none" strokeLinecap="round" strokeLinejoin="round"/>
    <circle cx="13" cy="8" r="1.5" fill="#fff" opacity={0.7}/></>
)

export const logo_librechat = Badge('#6366F1', '#fff',
  // LibreChat: speech bubble
  <><path d="M4 5h10v7H8.5L5.5 15V12H4z" stroke="#fff" strokeWidth="1.3" fill="none" strokeLinejoin="round"/>
    <path d="M7 8.5h4M7 10.5h2.5" stroke="#fff" strokeWidth="1.2" strokeLinecap="round"/></>
)

// ── RELAY ──────────────────────────────────────────────────────────
export const logo_stalwart = Badge('#0EA5E9', '#fff',
  // Stalwart mail: envelope
  <><rect x="3.5" y="6" width="11" height="7.5" rx="1.5" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <path d="M3.5 6.5l5.5 4 5.5-4" stroke="#fff" strokeWidth="1.3" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

export const logo_livekit = Badge('#00E5C0', '#1a1a1a',
  // LiveKit: video camera
  <><rect x="3" y="6.5" width="9" height="6" rx="1.5" stroke="#1a1a1a" strokeWidth="1.3" fill="none"/>
    <path d="M12 8.5l3-1.5v5l-3-1.5z" stroke="#1a1a1a" strokeWidth="1.2" fill="none" strokeLinejoin="round"/></>
)

export const logo_stunner = Badge('#8B5CF6', '#fff',
  // STUNner: S + TURN arrows
  <><path d="M6 7a3 3 0 0 1 6 0c0 2-3 3-3 5" stroke="#fff" strokeWidth="1.5" fill="none" strokeLinecap="round"/>
    <circle cx="9" cy="13.5" r="1" fill="#fff"/></>
)

export const logo_matrix = Badge('#0DBD8B', '#fff',
  // Matrix: [ ] brackets
  <><path d="M7 5H5v8h2M11 5h2v8h-2" stroke="#fff" strokeWidth="1.8" fill="none" strokeLinecap="round" strokeLinejoin="round"/></>
)

export const logo_ntfy = Badge('#317AE2', '#fff',
  // Ntfy: bell notification
  <><path d="M9 4a5 5 0 0 0-5 5c0 2 0 3-1 4h12c-1-1-1-2-1-4a5 5 0 0 0-5-5z" stroke="#fff" strokeWidth="1.3" fill="none"/>
    <path d="M7.5 13a1.5 1.5 0 0 0 3 0" stroke="#fff" strokeWidth="1.2" fill="none"/></>
)

/* ── Master logo map ─────────────────────────────────────────────── */
export const COMPONENT_LOGOS: Record<string, React.ReactNode> = {
  flux:             logo_flux,
  crossplane:       logo_crossplane,
  gitea:            logo_gitea,
  opentofu:         logo_opentofu,
  cilium:           logo_cilium,
  coraza:           logo_coraza,
  'external-dns':   logo_externalDns,
  envoy:            logo_envoy,
  k8gb:             logo_k8gb,
  frpc:             logo_frpc,
  netbird:          logo_netbird,
  strongswan:       logo_strongswan,
  vpa:              logo_vpa,
  keda:             logo_keda,
  reloader:         logo_reloader,
  continuum:        logo_continuum,
  minio:            logo_minio,
  velero:           logo_velero,
  harbor:           logo_harbor,
  kyverno:          logo_kyverno,
  openbao:          logo_openbao,
  'external-secrets': logo_externalSecrets,
  'cert-manager':   logo_certManager,
  falco:            logo_falco,
  trivy:            logo_trivy,
  'syft-grype':     logo_syftGrype,
  sigstore:         logo_sigstore,
  keycloak:         logo_keycloak,
  grafana:          logo_grafana,
  opentelemetry:    logo_opentelemetry,
  alloy:            logo_alloy,
  loki:             logo_loki,
  mimir:            logo_mimir,
  tempo:            logo_tempo,
  opensearch:       logo_opensearch,
  litmus:           logo_litmus,
  openmeter:        logo_openmeter,
  specter:          logo_specter,
  cnpg:             logo_cnpg,
  valkey:           logo_valkey,
  strimzi:          logo_strimzi,
  debezium:         logo_debezium,
  flink:            logo_flink,
  temporal:         logo_temporal,
  clickhouse:       logo_clickhouse,
  ferretdb:         logo_ferretdb,
  iceberg:          logo_iceberg,
  superset:         logo_superset,
  kserve:           logo_kserve,
  knative:          logo_knative,
  axon:             logo_axon,
  neo4j:            logo_neo4j,
  vllm:             logo_vllm,
  milvus:           logo_milvus,
  bge:              logo_bge,
  langfuse:         logo_langfuse,
  librechat:        logo_librechat,
  stalwart:         logo_stalwart,
  livekit:          logo_livekit,
  stunner:          logo_stunner,
  matrix:           logo_matrix,
  ntfy:             logo_ntfy,
}
