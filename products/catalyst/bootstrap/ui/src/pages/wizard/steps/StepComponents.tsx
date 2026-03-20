import { useState } from 'react'
import { ChevronDown, ChevronUp, Lock } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { StepShell, useStepNav } from './_shared'

/* ─────────────────────────────────────────────────────────────────────────
   Component groups — Linux-style package group selection.
   Full ●  Partial ◑  Empty ○
   Click group header → toggle all components in group
   Click "›" → expand to drill into individual components
─────────────────────────────────────────────────────────────────────────── */

interface ComponentDef { id: string; name: string; desc: string }

interface GroupDef {
  id: string
  name: string
  tag: 'Required' | 'Recommended' | 'Optional'
  tagColor: string
  desc: string
  required?: boolean
  components: ComponentDef[]
}

const GROUPS: GroupDef[] = [
  {
    id: 'security', name: 'Security & Compliance', tag: 'Required', tagColor: '#F87171', required: true,
    desc: 'Runtime threat detection, policy enforcement, SBOM, CVE scanning, WAF, supply chain',
    components: [
      { id: 'falco',      name: 'Falco',        desc: 'Runtime threat detection' },
      { id: 'kyverno',    name: 'Kyverno',      desc: 'Policy as code' },
      { id: 'trivy',      name: 'Trivy',        desc: 'Vulnerability scanning' },
      { id: 'syft-grype', name: 'Syft + Grype', desc: 'SBOM & CVE analysis' },
      { id: 'coraza',     name: 'Coraza WAF',   desc: 'Web application firewall' },
      { id: 'sigstore',   name: 'Sigstore',     desc: 'Supply chain security' },
    ],
  },
  {
    id: 'identity', name: 'Identity & Secrets', tag: 'Required', tagColor: '#F87171', required: true,
    desc: 'Authentication, authorisation, secrets lifecycle',
    components: [
      { id: 'keycloak',         name: 'Keycloak',         desc: 'Enterprise identity provider' },
      { id: 'openbao',          name: 'OpenBao',          desc: 'Secrets management (Vault fork)' },
      { id: 'external-secrets', name: 'External Secrets', desc: 'K8s secret synchronisation' },
    ],
  },
  {
    id: 'networking', name: 'Networking & Ingress', tag: 'Required', tagColor: '#F87171', required: true,
    desc: 'eBPF service mesh, certificates, DNS automation',
    components: [
      { id: 'cilium',        name: 'Cilium',       desc: 'eBPF networking & service mesh' },
      { id: 'cert-manager',  name: 'Cert-Manager', desc: 'Automated certificate management' },
      { id: 'external-dns',  name: 'External DNS', desc: 'DNS record automation' },
    ],
  },
  {
    id: 'gitops', name: 'GitOps & Platform Ops', tag: 'Required', tagColor: '#F87171', required: true,
    desc: 'Continuous delivery, infrastructure control, auto-reload, right-sizing',
    components: [
      { id: 'flux',       name: 'Flux CD',    desc: 'GitOps continuous delivery' },
      { id: 'crossplane', name: 'Crossplane', desc: 'Infrastructure as code' },
      { id: 'reloader',   name: 'Reloader',   desc: 'Config-change pod reload' },
      { id: 'vpa',        name: 'VPA',        desc: 'Vertical pod autoscaling' },
    ],
  },
  {
    id: 'observability', name: 'Observability', tag: 'Recommended', tagColor: '#38BDF8',
    desc: 'Metrics, logs, traces, unified dashboards',
    components: [
      { id: 'grafana',       name: 'Grafana',       desc: 'Dashboards & alerting' },
      { id: 'opentelemetry', name: 'OpenTelemetry', desc: 'Unified telemetry pipeline' },
    ],
  },
  {
    id: 'data', name: 'Data & Storage', tag: 'Recommended', tagColor: '#38BDF8',
    desc: 'PostgreSQL, caching, object storage, analytics',
    components: [
      { id: 'cnpg',       name: 'CloudNative PG', desc: 'PostgreSQL operator' },
      { id: 'valkey',     name: 'Valkey',         desc: 'Redis-compatible cache (OSS)' },
      { id: 'minio',      name: 'MinIO',          desc: 'S3-compatible object storage' },
      { id: 'clickhouse', name: 'ClickHouse',     desc: 'Real-time analytics database' },
      { id: 'ferretdb',   name: 'FerretDB',       desc: 'MongoDB-compatible DB' },
    ],
  },
  {
    id: 'resilience', name: 'Resilience & Scaling', tag: 'Recommended', tagColor: '#38BDF8',
    desc: 'Backup, event-driven autoscaling, chaos engineering',
    components: [
      { id: 'velero', name: 'Velero', desc: 'Cluster backup & disaster recovery' },
      { id: 'keda',   name: 'KEDA',   desc: 'Event-driven autoscaling' },
      { id: 'litmus', name: 'Litmus', desc: 'Chaos engineering framework' },
    ],
  },
  {
    id: 'ai', name: 'AI & Machine Learning', tag: 'Optional', tagColor: '#A78BFA',
    desc: 'LLM serving, vector DB, embeddings, RAG, observability',
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
    id: 'events', name: 'Event & Integration', tag: 'Optional', tagColor: '#A78BFA',
    desc: 'Kafka streaming, CDC, stream processing, workflow orchestration',
    components: [
      { id: 'strimzi',  name: 'Strimzi',       desc: 'Apache Kafka operator' },
      { id: 'debezium', name: 'Debezium',       desc: 'Change data capture' },
      { id: 'flink',    name: 'Apache Flink',   desc: 'Stateful stream processing' },
      { id: 'temporal', name: 'Temporal',       desc: 'Durable workflow orchestration' },
    ],
  },
  {
    id: 'comms', name: 'Communication', tag: 'Optional', tagColor: '#A78BFA',
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

function getState(_groupId: string, selected: string[], total: number): SelectionState {
  const n = selected.length
  if (n === 0) return 'empty'
  if (n === total) return 'full'
  return 'partial'
}

function SelectionDot({ state, color }: { state: SelectionState; color: string }) {
  const base: React.CSSProperties = { fontSize: 18, lineHeight: 1, flexShrink: 0, display: 'flex', alignItems: 'center' }
  if (state === 'full')    return <span style={{ ...base, color, filter: 'drop-shadow(0 0 4px currentColor)' }}>●</span>
  if (state === 'partial') return <span style={{ ...base, color: '#F59E0B' }}>◑</span>
  return <span style={{ ...base, color: 'rgba(255,255,255,0.2)' }}>○</span>
}

function GroupCard({ group }: { group: GroupDef }) {
  const store = useWizardStore()
  const [open, setOpen] = useState(false)
  const selectedIds = store.componentGroups[group.id] ?? []
  const allIds = group.components.map(c => c.id)
  const state = getState(group.id, selectedIds, group.components.length)

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
      border: state !== 'empty'
        ? '1.5px solid rgba(255,255,255,0.1)'
        : '1.5px solid rgba(255,255,255,0.06)',
      background: state !== 'empty' ? 'rgba(255,255,255,0.03)' : 'rgba(0,0,0,0.1)',
      overflow: 'hidden',
      transition: 'all 0.15s',
    }}>
      {/* Group header */}
      <div
        style={{ display: 'flex', alignItems: 'center', gap: 10, padding: '11px 14px', cursor: 'pointer' }}
        onClick={() => !group.required && toggleAll()}
      >
        <SelectionDot state={state} color={group.tagColor} />

        <div style={{ flex: 1, minWidth: 0 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
            <span style={{ fontSize: 13, fontWeight: 600, color: state !== 'empty' ? 'rgba(255,255,255,0.85)' : 'rgba(255,255,255,0.35)' }}>
              {group.name}
            </span>
            <span style={{
              fontSize: 9, fontWeight: 700, letterSpacing: '0.06em', textTransform: 'uppercase',
              color: group.tagColor, background: `${group.tagColor}15`,
              border: `1px solid ${group.tagColor}30`, borderRadius: 4, padding: '2px 6px',
            }}>
              {group.required ? <Lock size={8} style={{ display: 'inline', marginRight: 3, verticalAlign: 'middle' }} /> : null}
              {group.tag}
            </span>
          </div>
          <div style={{ fontSize: 11, color: 'rgba(255,255,255,0.28)', marginTop: 1, lineHeight: 1.35 }}>
            {selectedIds.length}/{group.components.length} selected · {group.desc}
          </div>
        </div>

        {/* Expand toggle */}
        <button
          type="button"
          onClick={e => { e.stopPropagation(); setOpen(v => !v) }}
          style={{ width: 26, height: 26, borderRadius: 6, border: '1px solid rgba(255,255,255,0.08)', background: 'rgba(255,255,255,0.04)', color: 'rgba(255,255,255,0.3)', display: 'flex', alignItems: 'center', justifyContent: 'center', cursor: 'pointer', flexShrink: 0 }}
        >
          {open ? <ChevronUp size={13} /> : <ChevronDown size={13} />}
        </button>
      </div>

      {/* Expanded component list */}
      {open && (
        <div style={{ borderTop: '1px solid rgba(255,255,255,0.06)', padding: '8px 14px 12px' }}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
            {group.components.map(c => {
              const on = selectedIds.includes(c.id)
              const locked = group.required
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

export function StepComponents() {
  const { next, back } = useStepNav()
  const store = useWizardStore()
  const bp = useBreakpoint()

  const totalSelected = GROUPS.reduce((sum, g) => sum + (store.componentGroups[g.id]?.length ?? 0), 0)
  const totalAll = GROUPS.reduce((sum, g) => sum + g.components.length, 0)

  const groupCols = bp === 'mobile' ? '1fr' : '1fr 1fr'

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

      {/* Group grid: 2-col on tablet/desktop, 1-col on mobile */}
      <div style={{ display: 'grid', gridTemplateColumns: groupCols, gap: 8 }}>
        {GROUPS.map(g => <GroupCard key={g.id} group={g} />)}
      </div>
    </StepShell>
  )
}
