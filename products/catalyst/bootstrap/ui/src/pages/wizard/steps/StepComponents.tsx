import { useState } from 'react'
import { ChevronDown, ChevronUp, Lock } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'

/* ─────────────────────────────────────────────────────────────────────────
   Component groups — each maps to one product block from the deck (page 5).
   GUARDIAN, SURGE, SPINE, INSIGHTS, SILO, CORTEX, FABRIC, RELAY
─────────────────────────────────────────────────────────────────────────── */

interface ComponentDef {
  id: string
  name: string
  desc: string
  required?: boolean   // per-component lock (independent of group.required)
}

interface GroupDef {
  id: string
  productName: string
  name: string          // subtitle / concept description
  tag: 'Required' | 'Recommended' | 'Optional'
  tagColor: string
  desc: string
  required?: boolean    // locks header toggle; individual required? comps still locked inside
  components: ComponentDef[]
}

const GROUPS: GroupDef[] = [
  {
    id: 'guardian',
    productName: 'GUARDIAN',
    name: 'Security, Identity & Core Data',
    tag: 'Required', tagColor: '#F87171', required: true,
    desc: 'Runtime security, policy, secrets, identity, core databases',
    components: [
      { id: 'falco',            name: 'Falco',          desc: 'Runtime threat detection',        required: true },
      { id: 'kyverno',          name: 'Kyverno',        desc: 'Policy as code',                  required: true },
      { id: 'trivy',            name: 'Trivy',          desc: 'Vulnerability scanning',          required: true },
      { id: 'syft-grype',       name: 'Syft + Grype',   desc: 'SBOM & CVE analysis',             required: true },
      { id: 'coraza',           name: 'Coraza WAF',     desc: 'Web application firewall',        required: true },
      { id: 'sigstore',         name: 'Sigstore',       desc: 'Supply chain security',           required: true },
      { id: 'keycloak',         name: 'Keycloak',       desc: 'Enterprise identity provider',    required: true },
      { id: 'openbao',          name: 'OpenBao',        desc: 'Secrets management',              required: true },
      { id: 'external-secrets', name: 'External Secrets', desc: 'K8s secret sync',              required: true },
      { id: 'cnpg',             name: 'CloudNative PG', desc: 'PostgreSQL operator',             required: true },
      { id: 'valkey',           name: 'Valkey',         desc: 'Redis-compatible cache',          required: true },
      { id: 'ferretdb',         name: 'FerretDB',       desc: 'MongoDB-compatible DB',           required: true },
    ],
  },
  {
    id: 'surge',
    productName: 'SURGE',
    name: 'Networking & Ingress',
    tag: 'Required', tagColor: '#F87171', required: true,
    desc: 'eBPF service mesh, certificates, DNS automation',
    components: [
      { id: 'cilium',       name: 'Cilium',       desc: 'eBPF networking & service mesh',   required: true },
      { id: 'cert-manager', name: 'Cert-Manager', desc: 'Automated certificate management', required: true },
      { id: 'external-dns', name: 'External DNS', desc: 'DNS record automation',            required: true },
    ],
  },
  {
    id: 'spine',
    productName: 'SPINE',
    name: 'GitOps & Platform Ops',
    tag: 'Required', tagColor: '#F87171', required: true,
    desc: 'Continuous delivery, infrastructure control, auto-reload, right-sizing',
    components: [
      { id: 'flux',       name: 'Flux CD',    desc: 'GitOps continuous delivery', required: true },
      { id: 'crossplane', name: 'Crossplane', desc: 'Infrastructure as code',     required: true },
      { id: 'reloader',   name: 'Reloader',   desc: 'Config-change pod reload',   required: true },
      { id: 'vpa',        name: 'VPA',        desc: 'Vertical pod autoscaling',   required: true },
    ],
  },
  {
    id: 'insights',
    productName: 'INSIGHTS',
    name: 'Observability',
    tag: 'Recommended', tagColor: '#38BDF8',
    desc: 'Metrics, logs, traces, unified dashboards',
    components: [
      { id: 'grafana',       name: 'Grafana',       desc: 'Dashboards & alerting' },
      { id: 'opentelemetry', name: 'OpenTelemetry', desc: 'Unified telemetry pipeline' },
    ],
  },
  {
    id: 'silo',
    productName: 'SILO',
    name: 'Resilience, Scaling & Storage',
    tag: 'Recommended', tagColor: '#38BDF8',
    desc: 'Backup, event-driven autoscaling, object storage, chaos engineering',
    components: [
      { id: 'velero', name: 'Velero', desc: 'Cluster backup & disaster recovery' },
      { id: 'keda',   name: 'KEDA',   desc: 'Event-driven autoscaling' },
      { id: 'minio',  name: 'MinIO',  desc: 'S3-compatible object storage' },
      { id: 'litmus', name: 'Litmus', desc: 'Chaos engineering framework' },
    ],
  },
  {
    id: 'cortex',
    productName: 'CORTEX',
    name: 'AI & Machine Learning',
    tag: 'Optional', tagColor: '#A78BFA',
    desc: 'LLM serving, vector DB, embeddings, RAG, AI observability',
    components: [
      { id: 'kserve',    name: 'KServe',    desc: 'Model serving platform' },
      { id: 'vllm',      name: 'vLLM',      desc: 'High-throughput LLM inference' },
      { id: 'milvus',    name: 'Milvus',    desc: 'Vector database' },
      { id: 'bge',       name: 'BGE',       desc: 'Embedding model server' },
      { id: 'langfuse',  name: 'Langfuse',  desc: 'LLM observability & tracing' },
      { id: 'librechat', name: 'LibreChat', desc: 'AI chat interface' },
    ],
  },
  {
    id: 'fabric',
    productName: 'FABRIC',
    name: 'Event, Integration & Analytics',
    tag: 'Optional', tagColor: '#A78BFA',
    desc: 'Kafka streaming, CDC, stream processing, workflow orchestration, analytics',
    components: [
      { id: 'strimzi',    name: 'Strimzi',       desc: 'Apache Kafka operator' },
      { id: 'debezium',   name: 'Debezium',       desc: 'Change data capture' },
      { id: 'flink',      name: 'Apache Flink',   desc: 'Stateful stream processing' },
      { id: 'temporal',   name: 'Temporal',       desc: 'Durable workflow orchestration' },
      { id: 'clickhouse', name: 'ClickHouse',     desc: 'Real-time analytics database' },
    ],
  },
  {
    id: 'relay',
    productName: 'RELAY',
    name: 'Communication',
    tag: 'Optional', tagColor: '#A78BFA',
    desc: 'Email, WebRTC video, real-time chat, TURN relay',
    components: [
      { id: 'stalwart', name: 'Stalwart', desc: 'SMTP/IMAP/JMAP mail server' },
      { id: 'livekit',  name: 'LiveKit',  desc: 'WebRTC SFU for video/audio' },
      { id: 'stunner',  name: 'Stunner',  desc: 'Kubernetes TURN/STUN gateway' },
      { id: 'matrix',   name: 'Matrix',   desc: 'Federated real-time messaging' },
    ],
  },
]

type SelectionState = 'full' | 'partial' | 'empty'

function getState(selected: string[], total: number): SelectionState {
  if (selected.length === 0) return 'empty'
  if (selected.length === total) return 'full'
  return 'partial'
}

function SelectionDot({ state, color }: { state: SelectionState; color: string }) {
  const base: React.CSSProperties = { fontSize: 18, lineHeight: 1, flexShrink: 0, display: 'flex', alignItems: 'center' }
  if (state === 'full')    return <span style={{ ...base, color, filter: 'drop-shadow(0 0 4px currentColor)' }}>●</span>
  if (state === 'partial') return <span style={{ ...base, color: '#F59E0B' }}>◑</span>
  return <span style={{ ...base, color: 'rgba(255,255,255,0.2)' }}>○</span>
}

function GroupCard({ group, open, onToggle }: { group: GroupDef; open: boolean; onToggle: () => void }) {
  const store = useWizardStore()
  const bp = useBreakpoint()
  const selectedIds = store.componentGroups[group.id] ?? []
  const allIds = group.components.map(c => c.id)
  const state = getState(selectedIds, group.components.length)

  function toggleAll() {
    if (group.required) return
    store.setGroupComponents(group.id, state === 'full' ? [] : allIds)
  }

  function toggleOne(id: string) {
    store.toggleGroupComponent(group.id, id, allIds)
  }

  return (
    <div style={{
      borderRadius: 10,
      border: state !== 'empty' ? '1.5px solid rgba(255,255,255,0.1)' : '1.5px solid rgba(255,255,255,0.06)',
      background: state !== 'empty' ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.1)',
      overflow: 'hidden', transition: 'all 0.15s',
    }}>
      {/* Header */}
      <div
        style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '11px 14px', cursor: 'pointer' }}
        onClick={() => !group.required && toggleAll()}
      >
        <SelectionDot state={state} color={group.tagColor} />
        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 13, fontWeight: 700, color: state !== 'empty' ? 'rgba(255,255,255,0.85)' : 'rgba(255,255,255,0.35)' }}>
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
            {group.name} · {selectedIds.length}/{group.components.length} selected
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
          <div style={{ display: 'grid', gridTemplateColumns: bp === 'mobile' ? '1fr' : bp === 'tablet' ? '1fr 1fr' : '1fr 1fr 1fr', gap: 4 }}>
            {group.components.map(c => {
              const on = selectedIds.includes(c.id)
              const locked = !!(c.required ?? group.required)
              return (
                <div
                  key={c.id}
                  onClick={() => !locked && toggleOne(c.id)}
                  style={{
                    display: 'flex', alignItems: 'center', gap: 10,
                    padding: '6px 10px', borderRadius: 7,
                    background: on ? 'rgba(255,255,255,0.04)' : 'transparent',
                    cursor: locked ? 'default' : 'pointer',
                    transition: 'background 0.12s',
                  }}
                >
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
                    <div style={{ fontSize: 12, fontWeight: on ? 600 : 400, color: on ? 'rgba(255,255,255,0.85)' : 'rgba(255,255,255,0.4)' }}>{c.name}</div>
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

/* ── CardGrid at module level — stable type identity, never remounted ─── */
function CardGrid({ groups, cols, onOpen }: {
  groups: GroupDef[]
  cols: string
  onOpen: (id: string) => void
}) {
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

  const totalSelected = GROUPS.reduce((sum, g) => sum + (store.componentGroups[g.id]?.length ?? 0), 0)
  const totalAll = GROUPS.reduce((sum, g) => sum + g.components.length, 0)

  const colCount = bp === 'mobile' ? 1 : bp === 'tablet' ? 2 : 3
  const cols = bp === 'mobile' ? '1fr' : bp === 'tablet' ? '1fr 1fr' : '1fr 1fr 1fr'

  const [openGroupId, setOpenGroupId] = useState<string | null>(null)

  /* ── Row-boundary split ────────────────────────────────────────────────
     All cards that share the expanded card's visual row — whether to its
     left or right — shift below it. Only complete rows above stay above.
  ────────────────────────────────────────────────────────────────────── */
  const openIdx = openGroupId ? GROUPS.findIndex(g => g.id === openGroupId) : -1
  const rowStart = openIdx > -1 ? Math.floor(openIdx / colCount) * colCount : -1

  const beforeGroups = openIdx > -1 ? GROUPS.slice(0, rowStart) : GROUPS
  const openGroup    = openIdx > -1 ? GROUPS[openIdx] : null
  // row-mates before the expanded card + everything after
  const afterGroups  = openIdx > -1
    ? [...GROUPS.slice(rowStart, openIdx), ...GROUPS.slice(openIdx + 1)]
    : []

  return (
    <StepShell
      title="Platform components"
      description="Select the components to install. Required groups are locked. Click a group header to toggle all, or expand › to pick individual components."
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

      {/* Complete rows above expanded card */}
      {beforeGroups.length > 0 && (
        <CardGrid groups={beforeGroups} cols={cols} onOpen={setOpenGroupId} />
      )}

      {/* Expanded card — full width */}
      {openGroup && (
        <GroupCard
          group={openGroup}
          open={true}
          onToggle={() => setOpenGroupId(null)}
        />
      )}

      {/* Row-mates of expanded card + all rows below */}
      {afterGroups.length > 0 && (
        <CardGrid groups={afterGroups} cols={cols} onOpen={setOpenGroupId} />
      )}
    </StepShell>
  )
}
