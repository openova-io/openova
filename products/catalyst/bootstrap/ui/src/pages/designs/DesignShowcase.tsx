import { useState } from 'react'
import { ChevronLeft, ChevronRight, Check } from 'lucide-react'

/* ─────────────────────────────────────────────────────────────────────────
   LOGO  — correct viewBox 0 0 700 400, strokeWidth 100, aspect 7:4
   h prop drives height; width is always h * 1.75 to preserve logo shape
───────────────────────────────────────────────────────────────────────── */
function OOLogo({ h = 28, c1 = '#38BDF8', c2 = '#818CF8', id = 'oo' }: {
  h?: number; c1?: string; c2?: string; id?: string
}) {
  const w = Math.round(h * 1.75)
  return (
    <svg width={w} height={h} viewBox="0 0 700 400" fill="none" aria-hidden="true" style={{ flexShrink: 0 }}>
      <defs>
        <linearGradient id={id} x1="0%" y1="0%" x2="100%" y2="0%">
          <stop offset="0%" stopColor={c1} />
          <stop offset="100%" stopColor={c2} />
        </linearGradient>
      </defs>
      <path
        d="M 300 88.1966 A 150 150 0 1 0 350 200 A 150 150 0 1 1 400 311.8034"
        fill="none" stroke={`url(#${id})`} strokeWidth="100" strokeLinecap="butt"
      />
    </svg>
  )
}

/* ─────────────────────────────────────────────────────────────────────────
   STEP DATA  — shared across all 10 designs
───────────────────────────────────────────────────────────────────────── */
const STEP_META = [
  { n: 1, label: 'Organisation',   desc: 'Name, domain, contact' },
  { n: 2, label: 'Cloud provider', desc: 'Select target cloud' },
  { n: 3, label: 'Credentials',    desc: 'API access token' },
  { n: 4, label: 'Infrastructure', desc: 'Regions, nodes, sizing' },
  { n: 5, label: 'Components',     desc: 'Platform building blocks' },
  { n: 6, label: 'Review',         desc: 'Confirm and provision' },
]

const PROVIDERS = ['Hetzner Cloud', 'Amazon AWS', 'Oracle OCI', 'More soon…']
const COMPONENTS = [
  { g: 'GUARDIAN', t: 'Security — certs, secrets, policy, IAM', on: true },
  { g: 'CORTEX',   t: 'AI Hub — LLM serving, RAG, vectors',    on: false },
  { g: 'FABRIC',   t: 'Data — Kafka, Flink, Temporal, CDC',     on: true },
  { g: 'RELAY',    t: 'Communication — email, video, chat',     on: false },
  { g: 'INSIGHTS', t: 'AIOps — metrics, logs, traces, chaos',   on: true },
  { g: 'PILOT',    t: 'GitOps — Flux, Gitea, Crossplane',       on: true },
]
const REVIEW_ROWS = [
  ['Organisation', 'Acme Corp · acme.io'],
  ['Provider',     'Hetzner Cloud'],
  ['Region',       'Falkenstein, DE (fsn1)'],
  ['Control plane','cx31 · 2 vCPU, 8 GB RAM'],
  ['Workers',      '3 × cx31'],
  ['Components',   'GUARDIAN, FABRIC, INSIGHTS, PILOT'],
]
const INFRA_FIELDS = [
  { l: 'Primary region',  p: 'Falkenstein, DE (fsn1)',     h: 'EU Central — low latency' },
  { l: 'Control plane',   p: 'cx31 · 2 vCPU, 8 GB RAM',  h: 'Recommended for production' },
  { l: 'Worker nodes',    p: '3 × cx31 (HA topology)',    h: 'Minimum 2 for high availability' },
]
const ORG_FIELDS = [
  { l: 'Organisation name',       p: 'Acme Corp',          h: 'Cluster owner identifier' },
  { l: 'Domain',                  p: 'acme.io',            h: 'Service URLs + TLS certificates' },
  { l: 'Technical contact email', p: 'platform@acme.io',   h: 'Receives cert-manager alerts' },
]

/* ─────────────────────────────────────────────────────────────────────────
   STEP CONTENT RENDERER  — each design calls this with its own theme
   theme = { bg, border, inputBg, text, muted, accent, radius, provCard, provActive }
───────────────────────────────────────────────────────────────────────── */
type Theme = {
  text:       string; muted: string; dim: string
  inputBg:    string; inputBorder: string; inputText: string
  cardBg:     string; cardBorder:  string
  accent:     string; accentText:  string
  radius:     number; gap:         number
  font:       string
}

function StepBody({ step, theme, mono = false }: { step: number; theme: Theme; mono?: boolean }) {
  const fStyle = (active = false): React.CSSProperties => ({
    height: 42, borderRadius: theme.radius, border: `1.5px solid ${active ? theme.accent : theme.inputBorder}`,
    background: theme.inputBg, display: 'flex', alignItems: 'center',
    paddingLeft: 12, paddingRight: 12,
    fontSize: 13, color: theme.inputText,
    fontFamily: mono ? 'monospace' : theme.font,
  })

  if (step === 1 || step === 4) {
    const fields = step === 1 ? ORG_FIELDS : INFRA_FIELDS
    return (
      <div style={{ display: 'flex', flexDirection: 'column', gap: theme.gap }}>
        {fields.map(f => (
          <div key={f.l} style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
            <label style={{ fontSize: 12, fontWeight: 500, color: theme.muted, fontFamily: theme.font }}>{f.l}</label>
            <div style={fStyle()}><span style={{ opacity: 0.4 }}>{f.p}</span></div>
            <span style={{ fontSize: 11, color: theme.dim, fontFamily: theme.font }}>{f.h}</span>
          </div>
        ))}
      </div>
    )
  }

  if (step === 2) return (
    <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 10 }}>
      {PROVIDERS.map((p, i) => (
        <div key={p} style={{ padding: '14px 16px', borderRadius: theme.radius, border: `1.5px solid ${i === 0 ? theme.accent : theme.cardBorder}`, background: i === 0 ? `${theme.accent}14` : theme.cardBg, cursor: i === 0 ? 'pointer' : 'default', opacity: i > 1 ? 0.35 : 1, display: 'flex', alignItems: 'center', gap: 8 }}>
          {i === 0 && <div style={{ width: 6, height: 6, borderRadius: '50%', background: theme.accent, flexShrink: 0 }} />}
          <span style={{ fontSize: 13, fontWeight: i === 0 ? 600 : 400, color: i === 0 ? theme.text : theme.muted, fontFamily: theme.font }}>{p}</span>
        </div>
      ))}
    </div>
  )

  if (step === 3) return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 10 }}>
      <div style={{ display: 'flex', flexDirection: 'column', gap: 5 }}>
        <label style={{ fontSize: 12, fontWeight: 500, color: theme.muted }}>Hetzner Cloud API token</label>
        <div style={fStyle()}>
          <span style={{ letterSpacing: 3, opacity: 0.25 }}>{'•'.repeat(40)}</span>
        </div>
      </div>
      <div style={{ padding: '10px 14px', borderRadius: theme.radius, border: `1px solid ${theme.cardBorder}`, background: theme.cardBg }}>
        <div style={{ fontSize: 11, fontWeight: 500, color: theme.muted, marginBottom: 6 }}>How to get a token</div>
        <ol style={{ margin: 0, paddingLeft: 16, display: 'flex', flexDirection: 'column', gap: 4 }}>
          {['Go to Hetzner Cloud Console', 'Security → API Tokens', 'Generate with Read & Write', 'Paste above'].map((s, i) => (
            <li key={i} style={{ fontSize: 11, color: theme.dim }}>{s}</li>
          ))}
        </ol>
      </div>
      <button style={{ alignSelf: 'flex-start', fontSize: 12, fontWeight: 500, color: theme.accent, background: 'none', border: 'none', cursor: 'pointer', padding: 0, textDecoration: 'underline', textUnderlineOffset: 3 }}>
        No token? Skip — explore in demo mode →
      </button>
    </div>
  )

  if (step === 5) return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      {COMPONENTS.map(c => (
        <div key={c.g} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '10px 14px', borderRadius: theme.radius, border: `1px solid ${c.on ? theme.accent : theme.cardBorder}`, background: c.on ? `${theme.accent}0d` : theme.cardBg }}>
          <div style={{ width: 18, height: 18, borderRadius: 4, border: `1.5px solid ${c.on ? theme.accent : theme.cardBorder}`, background: c.on ? theme.accent : 'transparent', display: 'flex', alignItems: 'center', justifyContent: 'center', flexShrink: 0 }}>
            {c.on && <Check size={10} color={theme.accentText} strokeWidth={3} />}
          </div>
          <div>
            <div style={{ fontSize: 12, fontWeight: 700, color: theme.text, fontFamily: mono ? 'monospace' : theme.font }}>{c.g}</div>
            <div style={{ fontSize: 11, color: theme.dim }}>{c.t}</div>
          </div>
        </div>
      ))}
    </div>
  )

  // step 6 — review
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>
      {REVIEW_ROWS.map(([k, v]) => (
        <div key={k} style={{ display: 'flex', gap: 0, padding: '10px 0', borderBottom: `1px solid ${theme.cardBorder}` }}>
          <span style={{ width: 140, fontSize: 12, color: theme.muted, fontFamily: theme.font, flexShrink: 0 }}>{k}</span>
          <span style={{ fontSize: 12, fontWeight: 500, color: theme.text, fontFamily: theme.font }}>{v}</span>
        </div>
      ))}
      <div style={{ marginTop: 20, padding: '12px 16px', borderRadius: theme.radius, border: `1px solid ${theme.accent}40`, background: `${theme.accent}0d`, fontSize: 12, color: theme.muted }}>
        ⚡ Provisioning takes ~8 minutes and runs entirely in your Hetzner account.
      </div>
    </div>
  )
}

/* ─────────────────────────────────────────────────────────────────────────
   DESIGN 1  —  COSMOS
   Dark radial gradient · frosted glass card · step dots on card
───────────────────────────────────────────────────────────────────────── */
function D1() {
  const [s, setS] = useState(1)
  const T: Theme = { text: 'rgba(255,255,255,0.9)', muted: 'rgba(255,255,255,0.45)', dim: 'rgba(255,255,255,0.22)', inputBg: 'rgba(255,255,255,0.06)', inputBorder: 'rgba(255,255,255,0.12)', inputText: 'rgba(255,255,255,0.5)', cardBg: 'rgba(255,255,255,0.04)', cardBorder: 'rgba(255,255,255,0.1)', accent: '#38BDF8', accentText: '#fff', radius: 8, gap: 14, font: 'Inter,sans-serif' }
  return (
    <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'radial-gradient(ellipse at 50% 35%, #0c1e40 0%, #06080f 60%)', fontFamily: 'Inter,sans-serif', padding: 24, position: 'relative', overflow: 'hidden' }}>
      <div style={{ position: 'absolute', top: '-15%', left: '10%', width: 600, height: 600, borderRadius: '50%', background: 'radial-gradient(circle, rgba(56,189,248,0.07) 0%, transparent 65%)', pointerEvents: 'none' }} />
      <div style={{ position: 'absolute', bottom: '0%', right: '5%', width: 400, height: 400, borderRadius: '50%', background: 'radial-gradient(circle, rgba(129,140,248,0.05) 0%, transparent 65%)', pointerEvents: 'none' }} />
      <div style={{ width: '100%', maxWidth: 480, background: 'rgba(255,255,255,0.04)', backdropFilter: 'blur(24px)', border: '1px solid rgba(255,255,255,0.1)', borderRadius: 20, padding: '2rem', boxShadow: '0 32px 80px rgba(0,0,0,0.6), inset 0 1px 0 rgba(255,255,255,0.08)' }}>
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 24 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <OOLogo h={22} id="d1" />
            <span style={{ fontSize: 12, fontWeight: 600, color: 'rgba(255,255,255,0.4)', letterSpacing: '0.1em' }}>OPENOVA CATALYST</span>
          </div>
          <span style={{ fontSize: 11, color: 'rgba(255,255,255,0.25)' }}>{s}/{STEP_META.length}</span>
        </div>
        {/* Progress dots */}
        <div style={{ display: 'flex', gap: 4, marginBottom: 28 }}>
          {STEP_META.map((_, i) => <div key={i} style={{ height: 3, flex: 1, borderRadius: 2, background: i < s ? 'linear-gradient(90deg,#38BDF8,#818CF8)' : 'rgba(255,255,255,0.08)' }} />)}
        </div>
        <div style={{ fontSize: 10, fontWeight: 600, letterSpacing: '0.15em', color: '#38BDF8', marginBottom: 6, textTransform: 'uppercase' }}>Step {s} — {STEP_META[s-1].label}</div>
        <h2 style={{ fontSize: '1.5rem', fontWeight: 700, letterSpacing: '-0.025em', color: '#fff', margin: '0 0 6px' }}>{s === 1 ? 'Your organisation' : s === 2 ? 'Cloud provider' : s === 3 ? 'Connect credentials' : s === 4 ? 'Infrastructure' : s === 5 ? 'Platform components' : 'Review & provision'}</h2>
        <p style={{ fontSize: 13, color: 'rgba(255,255,255,0.35)', margin: '0 0 24px', lineHeight: 1.6 }}>{STEP_META[s-1].desc}</p>
        <StepBody step={s} theme={T} />
        <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 28, alignItems: 'center' }}>
          <button onClick={() => setS(Math.max(1, s-1))} style={{ fontSize: 13, color: 'rgba(255,255,255,0.25)', background: 'none', border: 'none', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 4, visibility: s === 1 ? 'hidden' : 'visible' }}><ChevronLeft size={14} /> Back</button>
          <button onClick={() => setS(Math.min(6, s+1))} style={{ height: 40, padding: '0 24px', borderRadius: 8, border: 'none', background: 'linear-gradient(135deg,#38BDF8,#818CF8)', color: '#fff', fontWeight: 600, fontSize: 14, cursor: 'pointer' }}>
            {s === 6 ? '🚀 Provision cluster' : 'Continue'} {s < 6 && <ChevronRight size={13} style={{ display: 'inline', verticalAlign: 'middle' }} />}
          </button>
        </div>
      </div>
    </div>
  )
}

/* ─────────────────────────────────────────────────────────────────────────
   DESIGN 2  —  EDITORIAL
   White · giant watermark step number · serif heading · left margin list
───────────────────────────────────────────────────────────────────────── */
function D2() {
  const [s, setS] = useState(1)
  const T: Theme = { text: '#1C1917', muted: '#78716C', dim: '#A8A29E', inputBg: '#fff', inputBorder: '#D6D3D1', inputText: '#1C1917', cardBg: '#FAFAF9', cardBorder: '#E7E5E4', accent: '#0369A1', accentText: '#fff', radius: 0, gap: 18, font: 'Inter,sans-serif' }
  const TITLES = ['Your organisation', 'Cloud provider', 'Connect credentials', 'Infrastructure', 'Platform components', 'Review & provision']
  return (
    <div style={{ minHeight: '100vh', background: '#FAFAF9', fontFamily: 'Inter,sans-serif', display: 'flex', flexDirection: 'column' }}>
      <header style={{ height: 52, borderBottom: '1px solid #E7E5E4', display: 'flex', alignItems: 'center', padding: '0 48px', justifyContent: 'space-between', background: '#fff' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}><OOLogo h={20} c1="#0369A1" c2="#0284C7" id="d2" /><span style={{ fontSize: 14, fontWeight: 600, color: '#1C1917' }}>OpenOva Catalyst</span></div>
        <div style={{ fontSize: 11, color: '#A8A29E', letterSpacing: '0.12em', textTransform: 'uppercase' }}>{s} / {STEP_META.length}</div>
      </header>
      <div style={{ flex: 1, display: 'flex' }}>
        <div style={{ width: 280, padding: '48px 36px', borderRight: '1px solid #E7E5E4', background: '#fff', position: 'relative', overflow: 'hidden' }}>
          <div style={{ fontSize: 140, fontWeight: 900, color: '#F5F5F4', lineHeight: 1, position: 'absolute', top: 20, left: 16, userSelect: 'none', fontFamily: 'Georgia,serif' }}>0{s}</div>
          <div style={{ position: 'relative', marginTop: 100 }}>
            <div style={{ fontSize: 10, letterSpacing: '0.2em', color: '#A8A29E', textTransform: 'uppercase', marginBottom: 8 }}>{STEP_META[s-1].label}</div>
            <div style={{ width: 32, height: 2, background: '#0369A1', marginBottom: 16 }} />
            <p style={{ fontSize: 12, lineHeight: 1.8, color: '#78716C' }}>{STEP_META[s-1].desc}</p>
          </div>
          <div style={{ marginTop: 'auto', position: 'absolute', bottom: 40, left: 36, right: 36 }}>
            {STEP_META.map((st, i) => (
              <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8, paddingBottom: 8, opacity: i === s-1 ? 1 : i < s ? 0.5 : 0.22 }}>
                <div style={{ width: 18, height: 18, borderRadius: '50%', border: `1px solid ${i < s ? '#0369A1' : '#D6D3D1'}`, display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 8, fontWeight: 700, color: i < s ? '#fff' : '#A8A29E', background: i < s ? '#0369A1' : 'transparent', flexShrink: 0 }}>{i < s - 1 ? '✓' : i+1}</div>
                <span style={{ fontSize: 11, color: i === s-1 ? '#1C1917' : '#A8A29E', fontWeight: i === s-1 ? 600 : 400 }}>{st.label}</span>
              </div>
            ))}
          </div>
        </div>
        <div style={{ flex: 1, padding: '64px 64px', maxWidth: 560 }}>
          <h2 style={{ fontSize: '2.25rem', fontWeight: 700, letterSpacing: '-0.03em', color: '#1C1917', margin: '0 0 10px', fontFamily: 'Georgia,serif', lineHeight: 1.15 }}>{TITLES[s-1]}</h2>
          <p style={{ fontSize: 13, color: '#78716C', margin: '0 0 36px', lineHeight: 1.7, borderLeft: '3px solid #E7E5E4', paddingLeft: 12 }}>{STEP_META[s-1].desc} — stays in your environment, never stored on our servers.</p>
          <StepBody step={s} theme={T} />
          <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 40, alignItems: 'center' }}>
            <button onClick={() => setS(Math.max(1,s-1))} style={{ fontSize: 13, color: '#78716C', background: 'none', border: 'none', cursor: 'pointer', visibility: s===1?'hidden':'visible' }}>← Back</button>
            <button onClick={() => setS(Math.min(6,s+1))} style={{ height: 40, padding: '0 28px', borderRadius: 0, border: 'none', background: '#0369A1', color: '#fff', fontWeight: 600, fontSize: 13, cursor: 'pointer' }}>{s===6?'Provision →':'Continue →'}</button>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ─────────────────────────────────────────────────────────────────────────
   DESIGN 3  —  TERMINAL
   Black · monospace · CLI aesthetic · green text · developer-first
───────────────────────────────────────────────────────────────────────── */
function D3() {
  const [s, setS] = useState(1)
  const T: Theme = { text: '#D1FAE5', muted: '#4ADE80', dim: '#374151', inputBg: '#111', inputBorder: '#1F2937', inputText: '#4ADE80', cardBg: '#0D0D0D', cardBorder: '#1F2937', accent: '#4ADE80', accentText: '#000', radius: 4, gap: 18, font: 'monospace' }
  return (
    <div style={{ minHeight: '100vh', background: '#0A0A0A', fontFamily: '"JetBrains Mono","Fira Code",monospace', color: '#4ADE80', display: 'flex', flexDirection: 'column', padding: '24px 32px' }}>
      <div style={{ fontSize: 11, color: '#374151', marginBottom: 24 }}>
        <span style={{ color: '#4ADE80' }}>openova@catalyst</span><span style={{ color: '#6B7280' }}>:</span><span style={{ color: '#60A5FA' }}>~/bootstrap</span><span style={{ color: '#6B7280' }}>$ </span><span style={{ color: '#D1D5DB' }}>wizard --step {s}/{STEP_META.length} --mode selfhosted</span>
      </div>
      <div style={{ display: 'flex', gap: 3, marginBottom: 28 }}>
        {STEP_META.map((sm, i) => (
          <div key={i} style={{ flex: 1 }}>
            <div style={{ fontSize: 8, color: i < s ? '#4ADE80' : '#1F2937', marginBottom: 3, textAlign: 'center' }}>{sm.label.slice(0,5)}</div>
            <div style={{ height: 2, background: i < s ? '#4ADE80' : '#1F2937' }} />
          </div>
        ))}
      </div>
      <div style={{ maxWidth: 680, width: '100%', margin: '0 auto', flex: 1 }}>
        <div style={{ color: '#374151', fontSize: 11, marginBottom: 8 }}># STEP {s}/{STEP_META.length} — {STEP_META[s-1].label.toUpperCase()}</div>
        <div style={{ color: '#4ADE80', fontSize: 13, marginBottom: 4 }}>{'>'} {s===1?'Initialising cluster configuration...':s===2?'Selecting cloud provider...':s===3?'Loading credentials validator...':s===4?'Configuring infrastructure topology...':s===5?'Selecting platform components...':'Preparing deployment manifest...'}</div>
        <div style={{ color: '#6B7280', fontSize: 13, marginBottom: 24 }}>{'>'} {STEP_META[s-1].desc}</div>
        <StepBody step={s} theme={T} mono />
        <div style={{ display: 'flex', gap: 12, marginTop: 32 }}>
          <button onClick={() => setS(Math.max(1,s-1))} style={{ height: 34, padding: '0 16px', background: 'transparent', border: '1px solid #1F2937', color: '#374151', fontSize: 11, cursor: 'pointer', borderRadius: 4, fontFamily: 'inherit', visibility: s===1?'hidden':'visible' }}>{'<'} --back</button>
          <button onClick={() => setS(Math.min(6,s+1))} style={{ height: 34, padding: '0 20px', background: '#052E16', border: '1px solid #166534', color: '#4ADE80', fontSize: 11, cursor: 'pointer', borderRadius: 4, fontFamily: 'inherit', fontWeight: 600 }}>{s===6?'$ ./provision.sh':'--next >>>'}</button>
        </div>
        <div style={{ marginTop: 40, borderTop: '1px solid #111', paddingTop: 12, fontSize: 10, color: '#1F2937' }}># OpenOva Catalyst Bootstrap v1.0 · All data processed locally · Zero external calls</div>
      </div>
    </div>
  )
}

/* ─────────────────────────────────────────────────────────────────────────
   DESIGN 4  —  AURORA
   Gradient mesh bg · glassmorphism · vibrant purple-blue-teal
───────────────────────────────────────────────────────────────────────── */
function D4() {
  const [s, setS] = useState(1)
  const T: Theme = { text: '#fff', muted: 'rgba(255,255,255,0.45)', dim: 'rgba(255,255,255,0.22)', inputBg: 'rgba(255,255,255,0.06)', inputBorder: 'rgba(255,255,255,0.14)', inputText: 'rgba(255,255,255,0.45)', cardBg: 'rgba(255,255,255,0.04)', cardBorder: 'rgba(255,255,255,0.1)', accent: '#A78BFA', accentText: '#fff', radius: 10, gap: 14, font: 'Inter,sans-serif' }
  return (
    <div style={{ minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center', background: 'linear-gradient(135deg,#1e003e 0%,#0a1628 35%,#001a3e 65%,#002020 100%)', fontFamily: 'Inter,sans-serif', padding: 24, position: 'relative', overflow: 'hidden' }}>
      {[{t:'-20%',l:'-10%',s:700,c:'rgba(139,92,246,0.14)'},{t:'45%',r:'-15%',s:600,c:'rgba(56,189,248,0.1)'},{b:'-20%',l:'30%',s:500,c:'rgba(16,185,129,0.07)'}].map((b,i)=>(
        <div key={i} style={{ position:'absolute', width:b.s, height:b.s, borderRadius:'50%', background:`radial-gradient(circle,${b.c} 0%,transparent 70%)`, ...b as any, pointerEvents:'none' }} />
      ))}
      <div style={{ width:'100%', maxWidth:500, background:'rgba(255,255,255,0.05)', backdropFilter:'blur(32px)', WebkitBackdropFilter:'blur(32px)', border:'1px solid rgba(255,255,255,0.12)', borderRadius:24, padding:'2.25rem', boxShadow:'0 40px 100px rgba(0,0,0,0.5)', position:'relative' }}>
        <div style={{ position:'absolute', top:0, left:'20%', right:'20%', height:1, background:'linear-gradient(90deg,transparent,rgba(167,139,250,0.7),rgba(56,189,248,0.7),transparent)' }} />
        <div style={{ display:'flex', alignItems:'center', justifyContent:'space-between', marginBottom:28 }}>
          <div style={{ display:'flex', alignItems:'center', gap:10 }}>
            <OOLogo h={24} c1="#A78BFA" c2="#38BDF8" id="d4" />
            <div><div style={{ fontSize:9, fontWeight:600, letterSpacing:'0.18em', color:'rgba(255,255,255,0.3)', textTransform:'uppercase', lineHeight:1 }}>OpenOva</div><div style={{ fontSize:14, fontWeight:700, color:'#A78BFA', lineHeight:1.2 }}>Catalyst</div></div>
          </div>
          <div style={{ display:'flex', gap:5 }}>
            {STEP_META.map((_,i) => <div key={i} style={{ width:7, height:7, borderRadius:'50%', background: i<s ? 'linear-gradient(135deg,#A78BFA,#38BDF8)' : 'rgba(255,255,255,0.1)', boxShadow: i===s-1 ? '0 0 8px rgba(167,139,250,0.7)' : 'none' }} />)}
          </div>
        </div>
        <div style={{ fontSize:10, fontWeight:600, letterSpacing:'0.12em', color:'#A78BFA', textTransform:'uppercase', marginBottom:8 }}>Step {s} · {STEP_META[s-1].label}</div>
        <h2 style={{ fontSize:'1.625rem', fontWeight:700, letterSpacing:'-0.025em', color:'#fff', margin:'0 0 6px' }}>{['Your organisation','Cloud provider','Connect credentials','Infrastructure','Platform components','Review & provision'][s-1]}</h2>
        <p style={{ fontSize:13, color:'rgba(255,255,255,0.3)', margin:'0 0 22px', lineHeight:1.6 }}>{STEP_META[s-1].desc}</p>
        <StepBody step={s} theme={T} />
        <div style={{ display:'flex', justifyContent:'space-between', marginTop:24, alignItems:'center' }}>
          <button onClick={()=>setS(Math.max(1,s-1))} style={{ fontSize:13, color:'rgba(255,255,255,0.2)', background:'none', border:'none', cursor:'pointer', visibility:s===1?'hidden':'visible' }}><ChevronLeft size={13} style={{verticalAlign:'middle'}} /> Back</button>
          <button onClick={()=>setS(Math.min(6,s+1))} style={{ height:42, padding:'0 28px', borderRadius:10, border:'none', background:'linear-gradient(135deg,#A78BFA,#38BDF8)', color:'#fff', fontWeight:600, fontSize:14, cursor:'pointer', boxShadow:'0 4px 20px rgba(167,139,250,0.3)' }}>{s===6?'🚀 Provision':'Continue →'}</button>
        </div>
      </div>
    </div>
  )
}

/* ─────────────────────────────────────────────────────────────────────────
   DESIGN 5  —  FORGE
   Bold 50/50 split · massive left branding · Stripe-checkout feel
───────────────────────────────────────────────────────────────────────── */
function D5() {
  const [s, setS] = useState(1)
  const T: Theme = { text:'#0F172A', muted:'#64748B', dim:'#94A3B8', inputBg:'#fff', inputBorder:'#E2E8F0', inputText:'#94A3B8', cardBg:'#F8FAFC', cardBorder:'#E2E8F0', accent:'#0F172A', accentText:'#fff', radius:8, gap:16, font:'Inter,sans-serif' }
  return (
    <div style={{ minHeight:'100vh', display:'flex', fontFamily:'Inter,sans-serif' }}>
      <div style={{ width:'42%', flexShrink:0, background:'linear-gradient(155deg,#020617 0%,#0C1A2E 55%,#050C18 100%)', padding:'52px 44px', display:'flex', flexDirection:'column', position:'relative', overflow:'hidden' }}>
        <div style={{ position:'absolute', top:'10%', left:'5%', width:400, height:400, borderRadius:'50%', background:'radial-gradient(circle,rgba(56,189,248,0.08) 0%,transparent 65%)', pointerEvents:'none' }} />
        <div style={{ display:'flex', alignItems:'center', gap:12, marginBottom:48 }}><OOLogo h={28} id="d5" /><div><div style={{ fontSize:9, fontWeight:600, color:'rgba(255,255,255,0.3)', letterSpacing:'0.15em', textTransform:'uppercase' }}>OpenOva</div><div style={{ fontSize:15, fontWeight:700, color:'#38BDF8' }}>Catalyst</div></div></div>
        <div style={{ flex:1 }}>
          <div style={{ fontSize:10, fontWeight:600, color:'#38BDF8', letterSpacing:'0.15em', textTransform:'uppercase', marginBottom:16 }}>Bootstrap Wizard</div>
          <h1 style={{ fontSize:'clamp(1.75rem,3.5vw,2.75rem)', fontWeight:800, color:'#fff', lineHeight:1.1, letterSpacing:'-0.03em', margin:'0 0 18px' }}>Enterprise<br/>Kubernetes<br/>in minutes.</h1>
          <p style={{ fontSize:13, color:'rgba(255,255,255,0.32)', lineHeight:1.7, margin:'0 0 36px', maxWidth:280 }}>Six steps. Production-grade cluster. Runs in your cloud account.</p>
          {STEP_META.map((sm,i)=>(
            <div key={i} style={{ display:'flex', alignItems:'center', gap:10, paddingBottom:10, opacity:i===s-1?1:i<s?0.5:0.18 }}>
              <div style={{ width:22, height:22, borderRadius:'50%', background:i<s?'linear-gradient(135deg,#38BDF8,#818CF8)':i===s-1?'rgba(56,189,248,0.12)':'rgba(255,255,255,0.04)', border:i===s-1?'1.5px solid #38BDF8':'1.5px solid rgba(255,255,255,0.1)', display:'flex', alignItems:'center', justifyContent:'center', fontSize:9, fontWeight:700, color:i===s-1?'#38BDF8':'#fff' }}>{i<s-1?'✓':i+1}</div>
              <span style={{ fontSize:12, color:i===s-1?'#fff':'rgba(255,255,255,0.4)', fontWeight:i===s-1?600:400 }}>{sm.label}</span>
              {i===s-1&&<span style={{ fontSize:9, background:'rgba(56,189,248,0.15)', color:'#38BDF8', padding:'1px 7px', borderRadius:20, fontWeight:600, border:'1px solid rgba(56,189,248,0.3)', marginLeft:'auto' }}>now</span>}
            </div>
          ))}
        </div>
      </div>
      <div style={{ flex:1, background:'#F8FAFC', display:'flex', alignItems:'center', justifyContent:'center', padding:'48px 56px' }}>
        <div style={{ width:'100%', maxWidth:420 }}>
          <div style={{ fontSize:10, fontWeight:600, color:'#94A3B8', letterSpacing:'0.1em', textTransform:'uppercase', marginBottom:8 }}>Step {s} of {STEP_META.length}</div>
          <h2 style={{ fontSize:'1.625rem', fontWeight:700, color:'#0F172A', letterSpacing:'-0.025em', margin:'0 0 6px' }}>{['Your organisation','Cloud provider','Connect credentials','Infrastructure','Platform components','Review & provision'][s-1]}</h2>
          <p style={{ fontSize:13, color:'#64748B', margin:'0 0 28px', lineHeight:1.6 }}>{STEP_META[s-1].desc}</p>
          <StepBody step={s} theme={T} />
          <div style={{ display:'flex', justifyContent:'space-between', marginTop:32, alignItems:'center' }}>
            <button onClick={()=>setS(Math.max(1,s-1))} style={{ fontSize:13, color:'#94A3B8', background:'none', border:'none', cursor:'pointer', visibility:s===1?'hidden':'visible' }}>← Back</button>
            <button onClick={()=>setS(Math.min(6,s+1))} style={{ height:42, padding:'0 28px', borderRadius:8, border:'none', background:'#0F172A', color:'#fff', fontWeight:600, fontSize:14, cursor:'pointer' }}>{s===6?'🚀 Provision':'Continue →'}</button>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ─────────────────────────────────────────────────────────────────────────
   DESIGN 6  —  WORKSPACE
   macOS app chrome · toolbar step tabs · VS Code-style sidebar
───────────────────────────────────────────────────────────────────────── */
function D6() {
  const [s, setS] = useState(1)
  const T: Theme = { text:'#E2E8F0', muted:'rgba(255,255,255,0.35)', dim:'rgba(255,255,255,0.2)', inputBg:'#12121E', inputBorder:'rgba(255,255,255,0.08)', inputText:'rgba(255,255,255,0.25)', cardBg:'#12121E', cardBorder:'rgba(255,255,255,0.07)', accent:'#38BDF8', accentText:'#000', radius:6, gap:16, font:'Inter,sans-serif' }
  return (
    <div style={{ minHeight:'100vh', display:'flex', flexDirection:'column', fontFamily:'Inter,sans-serif', background:'#1E1E2E' }}>
      <div style={{ height:30, background:'#141420', display:'flex', alignItems:'center', paddingLeft:16, gap:5, flexShrink:0 }}>
        {['#FF5F57','#FFBD2E','#28CA42'].map((c,i)=><div key={i} style={{ width:11, height:11, borderRadius:'50%', background:c }} />)}
        <span style={{ fontSize:10, color:'rgba(255,255,255,0.25)', marginLeft:12, fontFamily:'monospace' }}>OpenOva Catalyst — {STEP_META[s-1].label}</span>
      </div>
      <div style={{ height:38, background:'#1C1C2A', borderBottom:'1px solid rgba(255,255,255,0.06)', display:'flex', alignItems:'center', paddingLeft:12, gap:1, flexShrink:0 }}>
        <div style={{ display:'flex', alignItems:'center', gap:7, marginRight:16 }}><OOLogo h={16} id="d6" /><span style={{ fontSize:11, fontWeight:600, color:'rgba(255,255,255,0.5)' }}>Catalyst</span></div>
        {STEP_META.map((sm,i)=>(
          <div key={i} onClick={()=>i<s&&setS(i+1)} style={{ height:30, padding:'0 12px', display:'flex', alignItems:'center', fontSize:11, cursor:i<s?'pointer':'default', background:i===s-1?'rgba(56,189,248,0.1)':'transparent', color:i===s-1?'#38BDF8':i<s?'rgba(255,255,255,0.45)':'rgba(255,255,255,0.2)', fontWeight:i===s-1?500:400, borderBottom:i===s-1?'2px solid #38BDF8':'2px solid transparent', position:'relative', top:1, gap:5 }}>
            {i<s-1&&<span style={{ fontSize:8, background:'#22C55E', color:'#fff', borderRadius:10, padding:'1px 4px' }}>✓</span>}
            {sm.label}
          </div>
        ))}
      </div>
      <div style={{ flex:1, display:'flex', overflow:'hidden' }}>
        <div style={{ width:44, background:'#141420', display:'flex', flexDirection:'column', alignItems:'center', paddingTop:6, gap:2, flexShrink:0 }}>
          {['⬡','⚙','🔍','📋'].map((ic,i)=><div key={i} style={{ width:30, height:30, borderRadius:5, display:'flex', alignItems:'center', justifyContent:'center', fontSize:13, cursor:'pointer', opacity:i===0?1:0.25, background:i===0?'rgba(56,189,248,0.12)':'transparent', borderLeft:i===0?'2px solid #38BDF8':'2px solid transparent' }}>{ic}</div>)}
        </div>
        <div style={{ width:190, background:'#1A1A28', borderRight:'1px solid rgba(255,255,255,0.05)', padding:10, flexShrink:0 }}>
          <div style={{ fontSize:9, fontWeight:600, color:'rgba(255,255,255,0.2)', letterSpacing:'0.1em', textTransform:'uppercase', marginBottom:6, paddingLeft:4 }}>DEPLOYMENT</div>
          {STEP_META.map((sm,i)=>(
            <div key={i} onClick={()=>i<s&&setS(i+1)} style={{ padding:'5px 7px', borderRadius:4, fontSize:11, color:i===s-1?'#fff':i<s?'rgba(255,255,255,0.4)':'rgba(255,255,255,0.18)', background:i===s-1?'rgba(56,189,248,0.1)':'transparent', cursor:i<s?'pointer':'default', display:'flex', gap:7, alignItems:'center', marginBottom:1 }}>
              <span style={{ color:i<s-1?'#22C55E':i===s-1?'#38BDF8':'rgba(255,255,255,0.12)', fontSize:9 }}>{i<s-1?'✓':i===s-1?'▶':'○'}</span>
              {sm.label}
            </div>
          ))}
        </div>
        <div style={{ flex:1, padding:'36px 44px', overflowY:'auto' }}>
          <div style={{ maxWidth:500 }}>
            <div style={{ fontSize:9, fontWeight:600, color:'#38BDF8', letterSpacing:'0.12em', textTransform:'uppercase', marginBottom:6 }}>{STEP_META[s-1].label.toLowerCase().replace(/ /g,'-')}.yaml</div>
            <h2 style={{ fontSize:'1.375rem', fontWeight:600, color:'#E2E8F0', letterSpacing:'-0.02em', margin:'0 0 5px' }}>{['Organisation settings','Cloud provider','Credentials','Infrastructure config','Component selection','Review manifest'][s-1]}</h2>
            <p style={{ fontSize:12, color:'rgba(255,255,255,0.28)', margin:'0 0 24px', lineHeight:1.6 }}>{STEP_META[s-1].desc}</p>
            <StepBody step={s} theme={T} />
            <div style={{ display:'flex', gap:8, marginTop:28 }}>
              <button onClick={()=>setS(Math.max(1,s-1))} style={{ height:32, padding:'0 14px', background:'rgba(255,255,255,0.04)', border:'1px solid rgba(255,255,255,0.07)', borderRadius:5, color:'rgba(255,255,255,0.3)', fontSize:11, cursor:'pointer', visibility:s===1?'hidden':'visible' }}>← Back</button>
              <button onClick={()=>setS(Math.min(6,s+1))} style={{ height:32, padding:'0 18px', background:'#38BDF8', border:'none', borderRadius:5, color:'#000', fontSize:11, fontWeight:700, cursor:'pointer' }}>{s===6?'PROVISION →':'CONTINUE →'}</button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ─────────────────────────────────────────────────────────────────────────
   DESIGN 7  —  JOURNEY
   Horizontal segmented step rail · Airbnb/booking feel · light + indigo
───────────────────────────────────────────────────────────────────────── */
function D7() {
  const [s, setS] = useState(1)
  const T: Theme = { text:'#111827', muted:'#6B7280', dim:'#9CA3AF', inputBg:'#fff', inputBorder:'#E5E7EB', inputText:'#9CA3AF', cardBg:'#F9FAFB', cardBorder:'#E5E7EB', accent:'#6366F1', accentText:'#fff', radius:10, gap:18, font:'Inter,sans-serif' }
  return (
    <div style={{ minHeight:'100vh', background:'#fff', fontFamily:'Inter,sans-serif', display:'flex', flexDirection:'column' }}>
      <header style={{ padding:'18px 48px', display:'flex', alignItems:'center', justifyContent:'space-between', borderBottom:'1px solid #F3F4F6' }}>
        <div style={{ display:'flex', alignItems:'center', gap:10 }}><OOLogo h={24} c1="#6366F1" c2="#8B5CF6" id="d7" /><span style={{ fontSize:14, fontWeight:700, color:'#111827', letterSpacing:'-0.01em' }}>OpenOva Catalyst</span></div>
        <button style={{ fontSize:12, color:'#9CA3AF', background:'none', border:'none', cursor:'pointer' }}>✕ Exit</button>
      </header>
      <div style={{ padding:'22px 48px', borderBottom:'1px solid #F3F4F6' }}>
        <div style={{ display:'flex', alignItems:'center' }}>
          {STEP_META.map((sm,i)=>(
            <div key={i} style={{ flex:1, display:'flex', flexDirection:'column', alignItems:'center', position:'relative' }}>
              {i<STEP_META.length-1&&<div style={{ position:'absolute', top:14, left:'50%', right:'-50%', height:2, background:i<s-1?'#6366F1':'#E5E7EB', zIndex:0 }} />}
              <div onClick={()=>i<s&&setS(i+1)} style={{ width:28, height:28, borderRadius:'50%', background:i<s?'#6366F1':i===s-1?'#fff':'#F9FAFB', border:i===s-1?'2.5px solid #6366F1':i<s?'none':'2px solid #E5E7EB', display:'flex', alignItems:'center', justifyContent:'center', fontSize:11, fontWeight:700, color:i<s?'#fff':i===s-1?'#6366F1':'#9CA3AF', zIndex:1, position:'relative', boxShadow:i===s-1?'0 0 0 4px rgba(99,102,241,0.12)':'none', cursor:i<s?'pointer':'default' }}>
                {i<s-1?'✓':i+1}
              </div>
              <div style={{ marginTop:6, fontSize:10, fontWeight:i===s-1?600:400, color:i===s-1?'#111827':i<s?'#6366F1':'#9CA3AF', textAlign:'center' }}>{sm.label}</div>
            </div>
          ))}
        </div>
      </div>
      <div style={{ flex:1, display:'flex', alignItems:'flex-start', justifyContent:'center', padding:'56px 24px' }}>
        <div style={{ width:'100%', maxWidth:480 }}>
          <h2 style={{ fontSize:'1.875rem', fontWeight:700, color:'#111827', letterSpacing:'-0.025em', margin:'0 0 8px' }}>{['Your organisation','Cloud provider','Connect credentials','Infrastructure','Platform components','Review & provision'][s-1]}</h2>
          <p style={{ fontSize:13, color:'#6B7280', margin:'0 0 32px', lineHeight:1.6 }}>{STEP_META[s-1].desc} — stays in your environment.</p>
          <StepBody step={s} theme={T} />
          <div style={{ display:'flex', justifyContent:'space-between', marginTop:36, alignItems:'center' }}>
            <button onClick={()=>setS(Math.max(1,s-1))} style={{ height:44, padding:'0 20px', background:'#fff', border:'1.5px solid #E5E7EB', borderRadius:10, color:'#374151', fontSize:13, cursor:'pointer', fontWeight:500, visibility:s===1?'hidden':'visible' }}>← Back</button>
            <button onClick={()=>setS(Math.min(6,s+1))} style={{ height:44, padding:'0 32px', background:'#6366F1', border:'none', borderRadius:10, color:'#fff', fontWeight:600, fontSize:14, cursor:'pointer', boxShadow:'0 4px 14px rgba(99,102,241,0.3)' }}>{s===6?'🚀 Provision':'Continue →'}</button>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ─────────────────────────────────────────────────────────────────────────
   DESIGN 8  —  PAPER
   GOV.UK document style · pure white · numbered margin · zero chrome
───────────────────────────────────────────────────────────────────────── */
function D8() {
  const [s, setS] = useState(1)
  const T: Theme = { text:'#111827', muted:'#6B7280', dim:'#9CA3AF', inputBg:'#fff', inputBorder:'#111827', inputText:'#9CA3AF', cardBg:'#F9FAFB', cardBorder:'#E5E7EB', accent:'#0B57D0', accentText:'#fff', radius:0, gap:22, font:'"Helvetica Neue",Arial,sans-serif' }
  return (
    <div style={{ minHeight:'100vh', background:'#fff', fontFamily:'"Helvetica Neue",Arial,sans-serif', display:'flex', flexDirection:'column' }}>
      <div style={{ height:5, background:'#0B57D0' }} />
      <div style={{ flex:1, maxWidth:820, margin:'0 auto', padding:'40px 32px', width:'100%' }}>
        <div style={{ fontSize:12, color:'#0B57D0', marginBottom:20, display:'flex', alignItems:'center', gap:6 }}>
          <span style={{ textDecoration:'underline', cursor:'pointer' }}>OpenOva Catalyst</span>
          <span style={{ color:'#9CA3AF' }}>›</span>
          <span style={{ textDecoration:'underline', cursor:'pointer' }}>New deployment</span>
          <span style={{ color:'#9CA3AF' }}>›</span>
          <span style={{ color:'#374151' }}>{STEP_META[s-1].label}</span>
        </div>
        <div style={{ display:'flex', gap:56 }}>
          <div style={{ width:170, flexShrink:0 }}>
            <div style={{ fontSize:10, fontWeight:700, color:'#6B7280', letterSpacing:'0.08em', textTransform:'uppercase', marginBottom:14, paddingBottom:6, borderBottom:'2px solid #E5E7EB' }}>Progress</div>
            {STEP_META.map((sm,i)=>(
              <div key={i} onClick={()=>i<s&&setS(i+1)} style={{ display:'flex', gap:8, alignItems:'flex-start', marginBottom:10, opacity:i>s-1?0.35:1, cursor:i<s?'pointer':'default' }}>
                <div style={{ width:20, height:20, borderRadius:'50%', background:i<s?'#0B57D0':'transparent', border:`2px solid ${i===s-1?'#0B57D0':'#D1D5DB'}`, display:'flex', alignItems:'center', justifyContent:'center', fontSize:9, fontWeight:700, color:i<s?'#fff':i===s-1?'#0B57D0':'#6B7280', flexShrink:0, marginTop:2 }}>{i<s-1?'✓':i+1}</div>
                <span style={{ fontSize:12, color:i===s-1?'#111827':'#6B7280', fontWeight:i===s-1?700:400, lineHeight:1.5 }}>{sm.label}</span>
              </div>
            ))}
          </div>
          <div style={{ flex:1 }}>
            <h1 style={{ fontSize:'1.625rem', fontWeight:700, color:'#111827', margin:'0 0 5px', letterSpacing:'-0.01em' }}>{STEP_META[s-1].label}</h1>
            <p style={{ fontSize:13, color:'#6B7280', margin:'0 0 28px', lineHeight:1.7, borderLeft:'4px solid #E5E7EB', paddingLeft:12 }}>{STEP_META[s-1].desc} — stays in your environment, never stored externally.</p>
            <StepBody step={s} theme={T} />
            <div style={{ display:'flex', alignItems:'center', gap:12, marginTop:36 }}>
              <button onClick={()=>setS(Math.min(6,s+1))} style={{ height:40, padding:'0 24px', background:'#0B57D0', border:'none', color:'#fff', fontSize:13, fontWeight:700, cursor:'pointer' }}>{s===6?'Provision cluster':'Continue'}</button>
              {s>1&&<button onClick={()=>setS(Math.max(1,s-1))} style={{ height:40, padding:'0 16px', background:'transparent', border:'none', color:'#0B57D0', fontSize:13, cursor:'pointer', textDecoration:'underline' }}>← Previous</button>}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ─────────────────────────────────────────────────────────────────────────
   DESIGN 9  —  ELECTRIC
   Dark cyberpunk · neon cyan glow · gradient headings · left strip
───────────────────────────────────────────────────────────────────────── */
function D9() {
  const [s, setS] = useState(1)
  const T: Theme = { text:'rgba(255,255,255,0.9)', muted:'rgba(0,210,255,0.5)', dim:'rgba(255,255,255,0.2)', inputBg:'rgba(0,210,255,0.03)', inputBorder:'rgba(0,210,255,0.15)', inputText:'rgba(255,255,255,0.25)', cardBg:'rgba(0,210,255,0.03)', cardBorder:'rgba(0,210,255,0.12)', accent:'#00D2FF', accentText:'#000', radius:6, gap:14, font:'Inter,sans-serif' }
  return (
    <div style={{ minHeight:'100vh', background:'#050508', fontFamily:'Inter,sans-serif', display:'flex' }}>
      <div style={{ width:4, background:'linear-gradient(to bottom,#00D2FF,#7B2FFF,#FF2D78)', flexShrink:0 }} />
      <div style={{ flex:1, display:'flex' }}>
        <div style={{ width:300, padding:'44px 28px', display:'flex', flexDirection:'column', borderRight:'1px solid rgba(0,210,255,0.07)', flexShrink:0 }}>
          <div style={{ marginBottom:44 }}>
            <OOLogo h={36} c1="#00D2FF" c2="#7B2FFF" id="d9" />
            <div style={{ marginTop:14 }}>
              <div style={{ fontSize:9, letterSpacing:'0.2em', color:'rgba(0,210,255,0.35)', textTransform:'uppercase', marginBottom:3 }}>OpenOva</div>
              <div style={{ fontSize:20, fontWeight:800, letterSpacing:'-0.02em', background:'linear-gradient(90deg,#00D2FF,#7B2FFF)', WebkitBackgroundClip:'text', WebkitTextFillColor:'transparent' }}>CATALYST</div>
            </div>
          </div>
          {STEP_META.map((sm,i)=>(
            <div key={i} onClick={()=>i<s&&setS(i+1)} style={{ display:'flex', alignItems:'center', gap:10, padding:'9px 0', borderBottom:'1px solid rgba(255,255,255,0.03)', opacity:i>s-1?0.15:1, cursor:i<s?'pointer':'default' }}>
              <div style={{ width:26, height:26, borderRadius:5, border:`1px solid ${i===s-1?'rgba(0,210,255,0.5)':'rgba(255,255,255,0.05)'}`, background:i===s-1?'rgba(0,210,255,0.08)':'transparent', display:'flex', alignItems:'center', justifyContent:'center', fontSize:10, fontWeight:700, color:i===s-1?'#00D2FF':i<s?'#7B2FFF':'rgba(255,255,255,0.15)', boxShadow:i===s-1?'0 0 12px rgba(0,210,255,0.2)':'none' }}>{i<s-1?'✓':i+1}</div>
              <span style={{ fontSize:12, fontWeight:i===s-1?600:400, color:i===s-1?'#fff':'rgba(255,255,255,0.35)' }}>{sm.label}</span>
              {i===s-1&&<div style={{ marginLeft:'auto', width:5, height:5, borderRadius:'50%', background:'#00D2FF', boxShadow:'0 0 6px #00D2FF' }} />}
            </div>
          ))}
        </div>
        <div style={{ flex:1, padding:'56px 48px', display:'flex', flexDirection:'column', justifyContent:'center' }}>
          <div style={{ maxWidth:500 }}>
            <div style={{ fontSize:10, fontWeight:600, letterSpacing:'0.2em', color:'rgba(0,210,255,0.45)', textTransform:'uppercase', marginBottom:14 }}>// STEP_{String(s).padStart(2,'0')} :: {STEP_META[s-1].label.toUpperCase().replace(/ /g,'_')}</div>
            <h2 style={{ fontSize:'clamp(1.625rem,3vw,2.25rem)', fontWeight:800, letterSpacing:'-0.03em', margin:'0 0 10px', background:'linear-gradient(90deg,#fff 50%,rgba(255,255,255,0.3))', WebkitBackgroundClip:'text', WebkitTextFillColor:'transparent', lineHeight:1.1 }}>
              {['Organisation<br/>details.','Cloud<br/>provider.','Connect<br/>credentials.','Infrastructure<br/>config.','Platform<br/>components.','Review &<br/>provision.'][s-1].replace('<br/>','\n')}
            </h2>
            <p style={{ fontSize:12, color:'rgba(255,255,255,0.22)', margin:'0 0 28px', lineHeight:1.7 }}>{STEP_META[s-1].desc} — runs only in your cloud account.</p>
            <StepBody step={s} theme={T} />
            <div style={{ display:'flex', gap:10, marginTop:28 }}>
              {s>1&&<button onClick={()=>setS(Math.max(1,s-1))} style={{ height:42, padding:'0 18px', background:'rgba(255,255,255,0.03)', border:'1px solid rgba(255,255,255,0.07)', borderRadius:5, color:'rgba(255,255,255,0.25)', fontSize:12, cursor:'pointer' }}>← Back</button>}
              <button onClick={()=>setS(Math.min(6,s+1))} style={{ height:42, padding:'0 24px', borderRadius:5, border:'1px solid rgba(0,210,255,0.4)', background:'rgba(0,210,255,0.08)', color:'#00D2FF', fontSize:12, fontWeight:700, cursor:'pointer', letterSpacing:'0.05em', textTransform:'uppercase', boxShadow:'0 0 20px rgba(0,210,255,0.12)' }}>
                {s===6?'PROVISION →':'CONTINUE →'}
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ─────────────────────────────────────────────────────────────────────────
   DESIGN 10  —  WHISPER
   Typeform-inspired · full-screen focused · warm coral · field-by-field
───────────────────────────────────────────────────────────────────────── */
function D10() {
  const [s, setS] = useState(1)

  const T: Theme = { text:'#111827', muted:'#9CA3AF', dim:'#D1D5DB', inputBg:'transparent', inputBorder:'#FF6B6B', inputText:'#D1D5DB', cardBg:'#FFF5F5', cardBorder:'#FECACA', accent:'#FF6B6B', accentText:'#fff', radius:8, gap:20, font:'Inter,sans-serif' }

  const stepTitles = ['Your organisation','Cloud provider','Connect credentials','Infrastructure','Platform components','Review & provision']

  return (
    <div style={{ minHeight:'100vh', background:'#FAFAFA', fontFamily:'Inter,sans-serif', display:'flex', flexDirection:'column' }}>
      <div style={{ height:3, background:'#F0F0F0', flexShrink:0 }}>
        <div style={{ height:'100%', width:`${(s/6)*100}%`, background:'linear-gradient(90deg,#FF6B6B,#FF8E53)', transition:'width 0.5s' }} />
      </div>
      <div style={{ padding:'18px 32px', display:'flex', alignItems:'center', justifyContent:'space-between' }}>
        <div style={{ display:'flex', alignItems:'center', gap:8 }}><OOLogo h={20} c1="#FF6B6B" c2="#FF8E53" id="d10" /><span style={{ fontSize:13, fontWeight:600, color:'#374151' }}>OpenOva Catalyst</span></div>
        <div style={{ display:'flex', gap:4 }}>
          {STEP_META.map((_,i)=><div key={i} style={{ width:24, height:24, borderRadius:'50%', background:i===s-1?'#FF6B6B':i<s?'#FECACA':'#F3F4F6', display:'flex', alignItems:'center', justifyContent:'center', fontSize:9, fontWeight:700, color:i===s-1?'#fff':i<s?'#FF6B6B':'#9CA3AF', cursor:i<s?'pointer':'default' }} onClick={()=>i<s&&setS(i+1)}>{i<s-1?'✓':i+1}</div>)}
        </div>
      </div>
      <div style={{ flex:1, display:'flex', alignItems:'center', justifyContent:'center', padding:'0 24px' }}>
        <div style={{ width:'100%', maxWidth:620 }}>
          <div style={{ fontSize:11, fontWeight:600, color:'#FF6B6B', letterSpacing:'0.12em', textTransform:'uppercase', marginBottom:10 }}>{s} →</div>
          <h2 style={{ fontSize:'clamp(1.75rem,4vw,2.5rem)', fontWeight:700, color:'#111827', letterSpacing:'-0.03em', margin:'0 0 8px', lineHeight:1.15 }}>{stepTitles[s-1]}</h2>
          <p style={{ fontSize:13, color:'#9CA3AF', margin:'0 0 36px', lineHeight:1.6 }}>{STEP_META[s-1].desc}</p>
          <StepBody step={s} theme={T} />
          <div style={{ display:'flex', alignItems:'center', gap:12, marginTop:32 }}>
            {s>1&&<button onClick={()=>setS(Math.max(1,s-1))} style={{ height:44, padding:'0 20px', background:'#fff', border:'1.5px solid #E5E7EB', borderRadius:8, color:'#6B7280', fontSize:13, cursor:'pointer', fontWeight:500 }}>← Back</button>}
            <button onClick={()=>setS(Math.min(6,s+1))} style={{ height:44, padding:'0 28px', background:'#FF6B6B', border:'none', borderRadius:8, color:'#fff', fontWeight:600, fontSize:14, cursor:'pointer', display:'flex', alignItems:'center', gap:8, boxShadow:'0 4px 14px rgba(255,107,107,0.3)' }}>
              {s===6?'🚀 Provision cluster':'OK'} {s<6&&<span style={{ fontSize:16, opacity:0.8 }}>✓</span>}
            </button>
            <span style={{ fontSize:11, color:'#D1D5DB' }}>or press <kbd style={{ background:'#F3F4F6', border:'1px solid #E5E7EB', borderRadius:4, padding:'2px 6px', fontFamily:'monospace', fontSize:10 }}>Enter</kbd></span>
          </div>
        </div>
      </div>
      <div style={{ padding:'14px 32px', borderTop:'1px solid #F3F4F6', display:'flex', justifyContent:'space-between', alignItems:'center' }}>
        <span style={{ fontSize:11, color:'#9CA3AF' }}>Step {s} of {STEP_META.length} — {STEP_META[s-1].label}</span>
        <span style={{ fontSize:11, color:'#D1D5DB' }}>All data stays in your environment</span>
      </div>
    </div>
  )
}

/* ─────────────────────────────────────────────────────────────────────────
   PICKER  — grid of 10 designs
───────────────────────────────────────────────────────────────────────── */
const DESIGNS = [
  { id:1, name:'Cosmos',     tag:'Frosted glass card · dark radial gradient · progress dots',    bg:'#06080f',  ac:'#38BDF8', comp:D1 },
  { id:2, name:'Editorial',  tag:'White · giant watermark number · serif · magazine feel',        bg:'#FAFAF9',  ac:'#0369A1', comp:D2 },
  { id:3, name:'Terminal',   tag:'CLI prompt · monospace · green on black · developer-first',     bg:'#0A0A0A',  ac:'#4ADE80', comp:D3 },
  { id:4, name:'Aurora',     tag:'Purple-blue gradient mesh · glassmorphism · vibrant',           bg:'#0a1628',  ac:'#A78BFA', comp:D4 },
  { id:5, name:'Forge',      tag:'Bold 50/50 split · massive left headline · Stripe feel',        bg:'#020617',  ac:'#38BDF8', comp:D5 },
  { id:6, name:'Workspace',  tag:'macOS chrome · toolbar tabs · VS Code sidebar',                 bg:'#1E1E2E',  ac:'#38BDF8', comp:D6 },
  { id:7, name:'Journey',    tag:'Horizontal step rail · Airbnb booking feel · indigo',           bg:'#fff',     ac:'#6366F1', comp:D7 },
  { id:8, name:'Paper',      tag:'GOV.UK document style · numbered margin · zero chrome',         bg:'#fff',     ac:'#0B57D0', comp:D8 },
  { id:9, name:'Electric',   tag:'Neon cyan glow · gradient headings · cyberpunk left strip',     bg:'#050508',  ac:'#00D2FF', comp:D9 },
  { id:10,name:'Whisper',    tag:'Typeform-inspired · one step at a time · warm coral',           bg:'#FAFAFA',  ac:'#FF6B6B', comp:D10 },
]

export function DesignShowcase() {
  const [active, setActive] = useState<number | null>(null)

  if (active !== null) {
    const d = DESIGNS.find(x => x.id === active)!
    const Comp = d.comp
    return (
      <div>
        {/* Controls overlay */}
        <div style={{ position:'fixed', top:12, left:'50%', transform:'translateX(-50%)', zIndex:9999, display:'flex', gap:6, alignItems:'center' }}>
          <button onClick={()=>setActive(active>1?active-1:active)} disabled={active===1} style={{ height:32, padding:'0 12px', borderRadius:7, border:'1px solid rgba(255,255,255,0.18)', background:'rgba(0,0,0,0.75)', color:'#fff', fontSize:12, cursor:'pointer', backdropFilter:'blur(10px)', opacity:active===1?0.4:1 }}>← Prev</button>
          <div style={{ height:32, padding:'0 14px', borderRadius:7, background:'rgba(0,0,0,0.75)', color:'#fff', fontSize:12, backdropFilter:'blur(10px)', display:'flex', alignItems:'center', gap:8, border:'1px solid rgba(255,255,255,0.1)' }}>
            <span style={{ opacity:0.4 }}>#</span><span style={{ fontWeight:700 }}>{active}</span><span style={{ opacity:0.4 }}>·</span><span>{d.name}</span><span style={{ opacity:0.3 }}>— use Continue/Back within the design to walk all 6 steps</span>
          </div>
          <button onClick={()=>setActive(null)} style={{ height:32, padding:'0 12px', borderRadius:7, border:'1px solid rgba(255,255,255,0.18)', background:'rgba(0,0,0,0.75)', color:'#fff', fontSize:12, cursor:'pointer', backdropFilter:'blur(10px)' }}>All designs</button>
          <button onClick={()=>setActive(active<10?active+1:active)} disabled={active===10} style={{ height:32, padding:'0 12px', borderRadius:7, border:'1px solid rgba(255,255,255,0.18)', background:'rgba(0,0,0,0.75)', color:'#fff', fontSize:12, cursor:'pointer', backdropFilter:'blur(10px)', opacity:active===10?0.4:1 }}>Next →</button>
        </div>
        <Comp />
      </div>
    )
  }

  return (
    <div style={{ minHeight:'100vh', background:'#09090B', fontFamily:'Inter,sans-serif', padding:'44px 36px' }}>
      <div style={{ maxWidth:1080, margin:'0 auto' }}>
        <div style={{ textAlign:'center', marginBottom:44 }}>
          <div style={{ fontSize:10, fontWeight:600, letterSpacing:'0.16em', color:'#38BDF8', textTransform:'uppercase', marginBottom:10 }}>Design Concepts</div>
          <h1 style={{ fontSize:'2.25rem', fontWeight:800, color:'#fff', letterSpacing:'-0.03em', margin:'0 0 10px' }}>10 Wizard Designs</h1>
          <p style={{ fontSize:13, color:'#52525B', lineHeight:1.7 }}>Click any to preview full-screen. Use Prev/Next to compare. Continue/Back inside each design walks all 6 steps.</p>
        </div>
        <div style={{ display:'grid', gridTemplateColumns:'repeat(auto-fill,minmax(290px,1fr))', gap:18 }}>
          {DESIGNS.map(d => (
            <button key={d.id} onClick={()=>setActive(d.id)} style={{ background:'#111113', border:'1.5px solid #1F1F23', borderRadius:14, overflow:'hidden', cursor:'pointer', textAlign:'left', padding:0, transition:'all 0.2s' }}
              onMouseEnter={e=>{(e.currentTarget as HTMLElement).style.cssText+='border-color:#38BDF8;box-shadow:0 0 0 1px rgba(56,189,248,0.15),0 8px 28px rgba(0,0,0,0.5);transform:translateY(-2px)'}}
              onMouseLeave={e=>{(e.currentTarget as HTMLElement).style.borderColor='#1F1F23';(e.currentTarget as HTMLElement).style.boxShadow='none';(e.currentTarget as HTMLElement).style.transform='none'}}
            >
              {/* Color swatch top */}
              <div style={{ height:72, background:d.bg, display:'flex', alignItems:'center', justifyContent:'center', position:'relative', overflow:'hidden', borderBottom:'1px solid #1F1F23' }}>
                <div style={{ position:'absolute', inset:0, background:`radial-gradient(circle at 30% 50%, ${d.ac}18 0%, transparent 60%)` }} />
                <OOLogo h={28} c1={d.ac} c2={d.id===10?'#FF8E53':d.id===8?'#0284C7':d.id===7?'#8B5CF6':'#818CF8'} id={`pick${d.id}`} />
                <div style={{ position:'absolute', top:8, right:10, display:'flex', gap:3 }}>
                  {[1,2,3,4,5,6].map(i=><div key={i} style={{ width:6, height:6, borderRadius:'50%', background:d.ac, opacity:i===1?1:0.2 }} />)}
                </div>
              </div>
              <div style={{ padding:'14px 18px' }}>
                <div style={{ display:'flex', alignItems:'center', gap:8, marginBottom:5 }}>
                  <span style={{ fontSize:10, fontWeight:700, color:d.ac, background:`${d.ac}18`, padding:'2px 8px', borderRadius:20 }}>{d.id}</span>
                  <span style={{ fontSize:14, fontWeight:700, color:'#fff', letterSpacing:'-0.01em' }}>{d.name}</span>
                </div>
                <p style={{ fontSize:11, color:'#52525B', margin:0, lineHeight:1.5 }}>{d.tag}</p>
              </div>
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
