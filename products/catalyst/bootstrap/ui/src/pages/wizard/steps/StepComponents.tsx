import { useState } from 'react'
import { ChevronDown, ChevronUp, Lock } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { DEFAULT_COMPONENT_GROUPS } from '@/entities/deployment/model'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'

type Tier = 'mandatory' | 'recommended' | 'optional'

interface ComponentDef {
  id: string
  name: string
  desc: string
  tier: Tier
}

interface GroupDef {
  id: string
  productName: string
  subtitle: string
  tag: 'Required' | 'Optional'
  tagColor: string
  required: boolean
  components: ComponentDef[]
}

const GROUPS: GroupDef[] = [
  /* ── CORE ─────────────────────────────────────────────────────── */
  {
    id: 'pilot', productName: 'PILOT', subtitle: 'GitOps & IaC',
    tag: 'Required', tagColor: '#F87171', required: true,
    components: [
      { id: 'flux',       name: 'Flux CD',    desc: 'GitOps delivery engine',   tier: 'mandatory' },
      { id: 'crossplane', name: 'Crossplane', desc: 'Cloud CRDs / IaC',          tier: 'mandatory' },
      { id: 'gitea',      name: 'Gitea',      desc: 'Internal Git server',       tier: 'mandatory' },
      { id: 'opentofu',   name: 'OpenTofu',   desc: 'IaC (Terraform fork)',      tier: 'mandatory' },
    ],
  },
  {
    id: 'spine', productName: 'SPINE', subtitle: 'Networking & Service Mesh',
    tag: 'Required', tagColor: '#F87171', required: true,
    components: [
      { id: 'cilium',       name: 'Cilium',      desc: 'CNI & eBPF service mesh',         tier: 'mandatory' },
      { id: 'coraza',       name: 'Coraza WAF',  desc: 'L7 web application firewall',     tier: 'mandatory' },
      { id: 'external-dns', name: 'External DNS',desc: 'DNS record automation',           tier: 'mandatory' },
      { id: 'envoy',        name: 'Envoy',       desc: 'L7 proxy',                        tier: 'mandatory' },
      { id: 'k8gb',         name: 'k8gb',        desc: 'Global server load balancing',    tier: 'mandatory' },
      { id: 'frpc',         name: 'frpc',        desc: 'Reverse tunnel',                  tier: 'recommended' },
      { id: 'netbird',      name: 'NetBird',     desc: 'Mesh VPN',                        tier: 'mandatory' },
      { id: 'strongswan',   name: 'strongSwan',  desc: 'IPsec gateway',                   tier: 'optional' },
    ],
  },
  {
    id: 'surge', productName: 'SURGE', subtitle: 'Scaling & Resilience',
    tag: 'Required', tagColor: '#F87171', required: true,
    components: [
      { id: 'vpa',       name: 'VPA',       desc: 'Vertical pod autoscaling',     tier: 'mandatory' },
      { id: 'keda',      name: 'KEDA',      desc: 'Event-driven autoscaling',     tier: 'mandatory' },
      { id: 'reloader',  name: 'Reloader',  desc: 'Config-change pod reload',     tier: 'mandatory' },
      { id: 'continuum', name: 'Continuum', desc: 'HA orchestration',             tier: 'recommended' },
    ],
  },
  {
    id: 'silo', productName: 'SILO', subtitle: 'Storage & Registry',
    tag: 'Required', tagColor: '#F87171', required: true,
    components: [
      { id: 'minio',  name: 'MinIO',  desc: 'S3-compatible object storage',   tier: 'mandatory' },
      { id: 'velero', name: 'Velero', desc: 'Backup & disaster recovery',     tier: 'mandatory' },
      { id: 'harbor', name: 'Harbor', desc: 'Container registry',             tier: 'mandatory' },
    ],
  },
  /* ── SIDE (cross-cutting, always present) ─────────────────────── */
  {
    id: 'guardian', productName: 'GUARDIAN', subtitle: 'Security & Identity',
    tag: 'Required', tagColor: '#F87171', required: true,
    components: [
      { id: 'falco',            name: 'Falco',          desc: 'Runtime threat detection',        tier: 'recommended' },
      { id: 'kyverno',          name: 'Kyverno',        desc: 'Policy as code',                  tier: 'mandatory' },
      { id: 'trivy',            name: 'Trivy',          desc: 'Vulnerability scanning',          tier: 'recommended' },
      { id: 'syft-grype',       name: 'Syft + Grype',   desc: 'SBOM & CVE analysis',             tier: 'recommended' },
      { id: 'sigstore',         name: 'Sigstore',       desc: 'Supply chain trust',              tier: 'recommended' },
      { id: 'keycloak',         name: 'Keycloak',       desc: 'Identity & access management',    tier: 'recommended' },
      { id: 'openbao',          name: 'OpenBao',        desc: 'Secrets vault',                   tier: 'mandatory' },
      { id: 'external-secrets', name: 'External Secrets',desc: 'K8s secret sync (ESO)',          tier: 'mandatory' },
      { id: 'cert-manager',     name: 'Cert-Manager',   desc: 'TLS certificate automation',      tier: 'mandatory' },
    ],
  },
  {
    id: 'insights', productName: 'INSIGHTS', subtitle: 'AIOps & Observability',
    tag: 'Required', tagColor: '#F87171', required: true,
    components: [
      { id: 'grafana',       name: 'Grafana',       desc: 'Dashboards & alerting',         tier: 'recommended' },
      { id: 'opentelemetry', name: 'OpenTelemetry', desc: 'Unified telemetry pipeline',    tier: 'recommended' },
      { id: 'alloy',         name: 'Alloy',         desc: 'Telemetry agent',               tier: 'recommended' },
      { id: 'loki',          name: 'Loki',          desc: 'Log aggregation',               tier: 'recommended' },
      { id: 'mimir',         name: 'Mimir',         desc: 'Metrics store',                 tier: 'recommended' },
      { id: 'tempo',         name: 'Tempo',         desc: 'Distributed tracing',           tier: 'recommended' },
      { id: 'opensearch',    name: 'OpenSearch',    desc: 'Search & analytics',            tier: 'recommended' },
      { id: 'litmus',        name: 'Litmus',        desc: 'Chaos engineering',             tier: 'optional' },
      { id: 'openmeter',     name: 'OpenMeter',     desc: 'Usage metering',                tier: 'optional' },
      { id: 'specter',       name: 'Specter',       desc: 'AIOps brain',                   tier: 'optional' },
    ],
  },
  /* ── À LA CARTE ───────────────────────────────────────────────── */
  {
    id: 'fabric', productName: 'FABRIC', subtitle: 'Data & Integration',
    tag: 'Optional', tagColor: '#A78BFA', required: false,
    components: [
      { id: 'cnpg',       name: 'CloudNative PG', desc: 'PostgreSQL operator',         tier: 'recommended' },
      { id: 'valkey',     name: 'Valkey',         desc: 'Redis-compatible cache',       tier: 'recommended' },
      { id: 'strimzi',    name: 'Strimzi',        desc: 'Apache Kafka operator',        tier: 'recommended' },
      { id: 'debezium',   name: 'Debezium',       desc: 'Change data capture',          tier: 'recommended' },
      { id: 'flink',      name: 'Apache Flink',   desc: 'Stream processing',            tier: 'optional' },
      { id: 'temporal',   name: 'Temporal',       desc: 'Workflow orchestration',       tier: 'optional' },
      { id: 'clickhouse', name: 'ClickHouse',     desc: 'Analytics database',           tier: 'optional' },
      { id: 'ferretdb',   name: 'FerretDB',       desc: 'MongoDB-compatible DB',        tier: 'optional' },
      { id: 'iceberg',    name: 'Iceberg',        desc: 'Data lakehouse format',        tier: 'optional' },
      { id: 'superset',   name: 'Superset',       desc: 'BI & dashboards',              tier: 'optional' },
    ],
  },
  {
    id: 'cortex', productName: 'CORTEX', subtitle: 'AI & Machine Learning',
    tag: 'Optional', tagColor: '#A78BFA', required: false,
    components: [
      { id: 'kserve',    name: 'KServe',    desc: 'Model serving platform',       tier: 'mandatory' },
      { id: 'knative',   name: 'Knative',   desc: 'Serverless runtime',           tier: 'optional' },
      { id: 'axon',      name: 'Axon',      desc: 'LLM gateway (SaaS)',           tier: 'recommended' },
      { id: 'neo4j',     name: 'Neo4j',     desc: 'Graph database',               tier: 'optional' },
      { id: 'vllm',      name: 'vLLM',      desc: 'LLM inference engine',         tier: 'optional' },
      { id: 'milvus',    name: 'Milvus',    desc: 'Vector database',              tier: 'optional' },
      { id: 'bge',       name: 'BGE',       desc: 'Embedding model server',       tier: 'optional' },
      { id: 'langfuse',  name: 'LangFuse',  desc: 'LLM observability & tracing',  tier: 'optional' },
      { id: 'librechat', name: 'LibreChat', desc: 'AI chat interface',            tier: 'optional' },
    ],
  },
  {
    id: 'relay', productName: 'RELAY', subtitle: 'Communication',
    tag: 'Optional', tagColor: '#A78BFA', required: false,
    components: [
      { id: 'stalwart', name: 'Stalwart', desc: 'SMTP/IMAP/JMAP mail server',    tier: 'mandatory' },
      { id: 'livekit',  name: 'LiveKit',  desc: 'WebRTC video & audio',          tier: 'recommended' },
      { id: 'stunner',  name: 'STUNner',  desc: 'Kubernetes TURN/STUN gateway',  tier: 'recommended' },
      { id: 'matrix',   name: 'Matrix',   desc: 'Federated messaging',           tier: 'optional' },
      { id: 'ntfy',     name: 'Ntfy',     desc: 'Push notifications',            tier: 'optional' },
    ],
  },
]

const TIER_BADGE: Record<Tier, { label: string; color: string }> = {
  mandatory:   { label: 'M', color: '#F87171' },
  recommended: { label: 'R', color: '#38BDF8' },
  optional:    { label: 'O', color: '#A78BFA' },
}

function SelectionDot({ n, total, color }: { n: number; total: number; color: string }) {
  const base: React.CSSProperties = { fontSize: 18, lineHeight: 1, flexShrink: 0, display: 'flex', alignItems: 'center' }
  if (n === 0)     return <span style={{ ...base, color: 'rgba(255,255,255,0.2)' }}>○</span>
  if (n >= total)  return <span style={{ ...base, color, filter: 'drop-shadow(0 0 4px currentColor)' }}>●</span>
  return <span style={{ ...base, color: '#F59E0B' }}>◑</span>
}

function GroupCard({ group, open, onToggle }: { group: GroupDef; open: boolean; onToggle: () => void }) {
  const store = useWizardStore()
  const bp = useBreakpoint()

  const storedIds  = store.componentGroups[group.id] ?? []
  // For required groups always count mandatory components as selected even if store is fresh
  const mandatoryIds = group.components.filter(c => c.tier === 'mandatory').map(c => c.id)
  const selectedIds  = group.required
    ? [...new Set([...mandatoryIds, ...storedIds])]
    : storedIds

  function toggleAll() {
    if (group.required) return
    if (selectedIds.length === 0) {
      // Turn on: pre-select M + R
      const defaults = DEFAULT_COMPONENT_GROUPS[group.id]?.length
        ? DEFAULT_COMPONENT_GROUPS[group.id]
        : group.components.filter(c => c.tier !== 'optional').map(c => c.id)
      store.setGroupComponents(group.id, defaults)
    } else {
      store.setGroupComponents(group.id, [])
    }
  }

  function toggleOne(c: ComponentDef) {
    const locked = group.required && c.tier === 'mandatory'
    if (locked) return
    const allIds = group.components.map(x => x.id)
    store.toggleGroupComponent(group.id, c.id, allIds)
  }

  const colsInner = bp === 'mobile' ? '1fr' : bp === 'tablet' ? '1fr 1fr' : '1fr 1fr 1fr'

  return (
    <div style={{
      borderRadius: 10,
      border: selectedIds.length > 0 ? '1.5px solid rgba(255,255,255,0.1)' : '1.5px solid rgba(255,255,255,0.06)',
      background: selectedIds.length > 0 ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.1)',
      overflow: 'hidden', transition: 'all 0.15s',
    }}>
      {/* Header */}
      <div
        style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '11px 14px', cursor: group.required ? 'default' : 'pointer' }}
        onClick={toggleAll}
      >
        <SelectionDot n={selectedIds.length} total={group.components.length} color={group.tagColor} />

        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 13, fontWeight: 700, color: selectedIds.length > 0 ? 'rgba(255,255,255,0.85)' : 'rgba(255,255,255,0.35)' }}>
              {group.productName}
            </span>
            <span style={{
              fontSize: 9, fontWeight: 700, letterSpacing: '0.06em', textTransform: 'uppercase',
              color: group.tagColor, background: `${group.tagColor}15`,
              border: `1px solid ${group.tagColor}30`, borderRadius: 4, padding: '2px 6px',
            }}>
              {group.required && <Lock size={8} style={{ display: 'inline', marginRight: 3, verticalAlign: 'middle' }} />}
              {group.tag}
            </span>
          </div>
          <div style={{ fontSize: 11, color: 'rgba(255,255,255,0.35)', marginTop: 1 }}>
            {group.subtitle} · {selectedIds.length}/{group.components.length} selected
          </div>
        </div>

        <button
          type="button"
          onClick={e => { e.stopPropagation(); onToggle() }}
          style={{ width: 26, height: 26, borderRadius: 6, border: '1px solid rgba(255,255,255,0.08)', background: 'rgba(255,255,255,0.04)', color: 'rgba(255,255,255,0.3)', display: 'flex', alignItems: 'center', justifyContent: 'center', cursor: 'pointer', flexShrink: 0 }}
        >
          {open ? <ChevronUp size={13} /> : <ChevronDown size={13} />}
        </button>
      </div>

      {/* Expanded list */}
      {open && (
        <div style={{ borderTop: '1px solid rgba(255,255,255,0.06)', padding: '8px 14px 12px' }}>
          <div style={{ display: 'grid', gridTemplateColumns: colsInner, gap: 4 }}>
            {group.components.map(c => {
              const on     = selectedIds.includes(c.id)
              const locked = group.required && c.tier === 'mandatory'
              const badge  = TIER_BADGE[c.tier]
              return (
                <div
                  key={c.id}
                  onClick={() => toggleOne(c)}
                  style={{
                    display: 'flex', alignItems: 'center', gap: 8,
                    padding: '6px 10px', borderRadius: 7,
                    background: on ? 'rgba(255,255,255,0.04)' : 'transparent',
                    cursor: locked ? 'default' : 'pointer',
                    transition: 'background 0.12s',
                  }}
                >
                  {/* Checkbox */}
                  <div style={{
                    width: 16, height: 16, borderRadius: 4, flexShrink: 0,
                    border: on ? 'none' : '1.5px solid rgba(255,255,255,0.15)',
                    background: on ? group.tagColor : 'transparent',
                    display: 'flex', alignItems: 'center', justifyContent: 'center',
                    transition: 'all 0.15s',
                  }}>
                    {on && (
                      <svg width={9} height={9} viewBox="0 0 12 12" fill="none">
                        <path d="M2 6l3 3 5-5" stroke="#fff" strokeWidth={2} strokeLinecap="round" strokeLinejoin="round"/>
                      </svg>
                    )}
                  </div>

                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 5 }}>
                      <span style={{ fontSize: 12, fontWeight: on ? 600 : 400, color: on ? 'rgba(255,255,255,0.85)' : 'rgba(255,255,255,0.4)' }}>{c.name}</span>
                      <span style={{ fontSize: 8, fontWeight: 700, padding: '1px 4px', borderRadius: 3, background: `${badge.color}15`, border: `1px solid ${badge.color}30`, color: badge.color }}>
                        {badge.label}
                      </span>
                    </div>
                    <div style={{ fontSize: 10, color: 'rgba(255,255,255,0.22)', lineHeight: 1.3 }}>{c.desc}</div>
                  </div>

                  {locked && <Lock size={10} style={{ color: 'rgba(255,255,255,0.2)', flexShrink: 0 }} />}
                </div>
              )
            })}
          </div>
        </div>
      )}
    </div>
  )
}

/* ── CardGrid at module level — stable type, never remounted ─────── */
function CardGrid({ groups, cols, onOpen }: { groups: GroupDef[]; cols: string; onOpen: (id: string) => void }) {
  return (
    <div style={{ display: 'grid', gridTemplateColumns: cols, gap: 8 }}>
      {groups.map(g => (
        <GroupCard key={g.id} group={g} open={false} onToggle={() => onOpen(g.id)} />
      ))}
    </div>
  )
}

export function StepComponents() {
  const { next, back } = useStepNav()
  const store = useWizardStore()
  const bp = useBreakpoint()

  const totalSelected = GROUPS.reduce((sum, g) => {
    const stored = store.componentGroups[g.id] ?? []
    const mandatory = g.required ? g.components.filter(c => c.tier === 'mandatory').map(c => c.id) : []
    return sum + new Set([...mandatory, ...stored]).size
  }, 0)
  const totalAll = GROUPS.reduce((sum, g) => sum + g.components.length, 0)

  const colCount = bp === 'mobile' ? 1 : bp === 'tablet' ? 2 : 3
  const cols     = bp === 'mobile' ? '1fr' : bp === 'tablet' ? '1fr 1fr' : '1fr 1fr 1fr'

  const [openGroupId, setOpenGroupId] = useState<string | null>(null)

  /* Row-boundary split — all row-mates (left + right) shift below expanded card */
  const openIdx      = openGroupId ? GROUPS.findIndex(g => g.id === openGroupId) : -1
  const rowStart     = openIdx > -1 ? Math.floor(openIdx / colCount) * colCount : -1
  const beforeGroups = openIdx > -1 ? GROUPS.slice(0, rowStart) : GROUPS
  const openGroup    = openIdx > -1 ? GROUPS[openIdx] : null
  const afterGroups  = openIdx > -1
    ? [...GROUPS.slice(rowStart, openIdx), ...GROUPS.slice(openIdx + 1)]
    : []

  return (
    <StepShell
      title="Platform components"
      description="Required blocks are pre-selected and locked. Toggle recommended/optional components. Click an optional block to enable it."
      onNext={next}
      onBack={back}
    >
      {/* Summary bar */}
      <div style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '8px 12px', borderRadius: 8, background: 'rgba(56,189,248,0.05)', border: '1px solid rgba(56,189,248,0.12)' }}>
        <span style={{ fontSize: 12, color: 'rgba(56,189,248,0.7)', fontWeight: 600 }}>
          {totalSelected} of {totalAll} components selected
        </span>
        <div style={{ flex: 1, height: 3, borderRadius: 2, background: 'rgba(255,255,255,0.08)', overflow: 'hidden' }}>
          <div style={{ height: '100%', width: `${(totalSelected / totalAll) * 100}%`, background: 'linear-gradient(90deg, #38BDF8, #818CF8)', transition: 'width 0.3s' }} />
        </div>
      </div>

      {beforeGroups.length > 0 && <CardGrid groups={beforeGroups} cols={cols} onOpen={setOpenGroupId} />}
      {openGroup && <GroupCard group={openGroup} open={true} onToggle={() => setOpenGroupId(null)} />}
      {afterGroups.length > 0 && <CardGrid groups={afterGroups} cols={cols} onOpen={setOpenGroupId} />}
    </StepShell>
  )
}
