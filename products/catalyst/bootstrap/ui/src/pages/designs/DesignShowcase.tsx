import { useState } from 'react'
import { Eye, ChevronLeft, ChevronRight } from 'lucide-react'

/* ─── Shared mock data ─────────────────────────────────────────────────── */
const STEPS = ['Organisation', 'Provider', 'Credentials', 'Infrastructure', 'Components', 'Review']
const STEP = 1
const TOTAL = STEPS.length

function OOLogo({ size = 32, color = '#38BDF8' }: { size?: number; color?: string }) {
  return (
    <svg width={size} height={size} viewBox="140 60 420 280" fill="none">
      <defs>
        <linearGradient id={`g${size}`} x1="0%" y1="0%" x2="100%" y2="0%">
          <stop offset="0%" stopColor={color} />
          <stop offset="100%" stopColor="#818CF8" />
        </linearGradient>
      </defs>
      <path d="M 300 88.1966 A 150 150 0 1 0 350 200 A 150 150 0 1 1 400 311.8034"
        fill="none" stroke={`url(#g${size})`} strokeWidth="80" strokeLinecap="butt" />
    </svg>
  )
}

/* ─── Mock form fields ─────────────────────────────────────────────────── */
function Field({ label, placeholder, value, style = {}, inputStyle = {}, labelStyle = {} }:
  { label: string; placeholder: string; value?: string; style?: React.CSSProperties; inputStyle?: React.CSSProperties; labelStyle?: React.CSSProperties }) {
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 6, ...style }}>
      <label style={{ fontSize: 13, fontWeight: 500, color: 'inherit', opacity: 0.7, ...labelStyle }}>{label}</label>
      <div style={{
        height: 42, borderRadius: 8, border: '1.5px solid currentColor',
        opacity: 0.3, display: 'flex', alignItems: 'center',
        paddingLeft: 12, fontSize: 13, color: 'inherit', ...inputStyle,
      }}>
        {value || <span style={{ opacity: 0.4 }}>{placeholder}</span>}
      </div>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════════════════════
   DESIGN 1 — COSMOS
   Centered frosted glass card · dark radial gradient · step dots above card
══════════════════════════════════════════════════════════════════════════ */
function D1() {
  return (
    <div style={{
      minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center',
      background: 'radial-gradient(ellipse at 50% 40%, #0f1e3a 0%, #06080f 60%, #030508 100%)',
      fontFamily: 'Inter, sans-serif', padding: 24,
      position: 'relative', overflow: 'hidden',
    }}>
      {/* ambient orbs */}
      <div style={{ position: 'absolute', top: '10%', left: '20%', width: 600, height: 600, borderRadius: '50%', background: 'radial-gradient(circle, rgba(56,189,248,0.06) 0%, transparent 60%)', pointerEvents: 'none' }} />
      <div style={{ position: 'absolute', bottom: '10%', right: '15%', width: 400, height: 400, borderRadius: '50%', background: 'radial-gradient(circle, rgba(129,140,248,0.05) 0%, transparent 60%)', pointerEvents: 'none' }} />

      <div style={{
        width: '100%', maxWidth: 460,
        background: 'rgba(255,255,255,0.04)',
        backdropFilter: 'blur(24px)',
        border: '1px solid rgba(255,255,255,0.1)',
        borderRadius: 20, padding: '2rem',
        boxShadow: '0 32px 80px rgba(0,0,0,0.6), inset 0 1px 0 rgba(255,255,255,0.08)',
      }}>
        {/* Logo */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 28 }}>
          <OOLogo size={28} />
          <span style={{ fontSize: 13, fontWeight: 600, color: 'rgba(255,255,255,0.5)', letterSpacing: '0.08em' }}>OPENOVA CATALYST</span>
        </div>

        {/* Step dots */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 6, marginBottom: 32 }}>
          {STEPS.map((_, i) => (
            <div key={i} style={{
              height: 4, flex: 1, borderRadius: 2,
              background: i < STEP ? 'linear-gradient(90deg,#38BDF8,#818CF8)' : i === STEP - 1 ? 'linear-gradient(90deg,#38BDF8,#818CF8)' : 'rgba(255,255,255,0.1)',
              opacity: i === STEP - 1 ? 1 : i < STEP ? 0.9 : 0.3,
            }} />
          ))}
        </div>

        <div style={{ color: 'rgba(255,255,255,0.9)' }}>
          <div style={{ fontSize: 11, fontWeight: 600, letterSpacing: '0.15em', color: '#38BDF8', marginBottom: 8, textTransform: 'uppercase' }}>Step {STEP} of {TOTAL}</div>
          <h2 style={{ fontSize: '1.625rem', fontWeight: 700, letterSpacing: '-0.025em', margin: '0 0 6px', lineHeight: 1.2 }}>Your organisation</h2>
          <p style={{ fontSize: 13, opacity: 0.4, margin: '0 0 28px', lineHeight: 1.6 }}>This stays in your environment. We never see it.</p>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
            <Field label="Organisation name" placeholder="Acme Corp" />
            <Field label="Domain" placeholder="acme.io" />
            <Field label="Technical contact" placeholder="platform@acme.io" />
          </div>

          <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 32 }}>
            <button style={{ height: 40, padding: '0 20px', borderRadius: 8, border: 'none', background: 'linear-gradient(135deg,#38BDF8,#818CF8)', color: '#fff', fontWeight: 600, fontSize: 14, cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 8 }}>
              Continue <ChevronRight size={15} />
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════════════════════
   DESIGN 2 — EDITORIAL
   White · giant watermark step number · serif heading · magazine feel
══════════════════════════════════════════════════════════════════════════ */
function D2() {
  return (
    <div style={{ minHeight: '100vh', background: '#FAFAF9', fontFamily: 'Georgia, "Times New Roman", serif', display: 'flex', flexDirection: 'column' }}>
      {/* Top bar */}
      <header style={{ height: 56, borderBottom: '1px solid #E7E5E4', display: 'flex', alignItems: 'center', padding: '0 48px', justifyContent: 'space-between', background: '#fff' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <OOLogo size={22} color="#0369A1" />
          <span style={{ fontFamily: 'Inter, sans-serif', fontSize: 14, fontWeight: 600, color: '#1C1917', letterSpacing: '-0.01em' }}>OpenOva Catalyst</span>
        </div>
        <div style={{ fontFamily: 'Inter, sans-serif', fontSize: 12, color: '#A8A29E', letterSpacing: '0.1em', textTransform: 'uppercase' }}>
          {STEP} / {TOTAL}
        </div>
      </header>

      <div style={{ flex: 1, display: 'flex' }}>
        {/* Left — watermark + step context */}
        <div style={{ width: 320, padding: '64px 48px', position: 'relative', borderRight: '1px solid #E7E5E4', display: 'flex', flexDirection: 'column', background: '#fff' }}>
          <div style={{ fontSize: 160, fontWeight: 900, color: '#F5F5F4', lineHeight: 1, position: 'absolute', top: 40, left: 28, userSelect: 'none', fontFamily: 'Georgia, serif' }}>0{STEP}</div>
          <div style={{ position: 'relative', marginTop: 120 }}>
            <div style={{ fontSize: 11, fontWeight: 400, letterSpacing: '0.2em', color: '#A8A29E', textTransform: 'uppercase', fontFamily: 'Inter, sans-serif', marginBottom: 12 }}>Organisation</div>
            <div style={{ width: 40, height: 2, background: '#0369A1', marginBottom: 20 }} />
            <p style={{ fontSize: 13, lineHeight: 1.8, color: '#78716C', fontFamily: 'Inter, sans-serif' }}>
              Name your cluster ownership and set your platform defaults.
            </p>
          </div>
          <div style={{ marginTop: 'auto' }}>
            {STEPS.map((s, i) => (
              <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 12, paddingBottom: 10, opacity: i === STEP - 1 ? 1 : 0.3 }}>
                <div style={{ width: 20, height: 20, borderRadius: '50%', border: `1px solid ${i === STEP - 1 ? '#0369A1' : '#D6D3D1'}`, display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 9, fontFamily: 'Inter, sans-serif', fontWeight: 600, color: i === STEP - 1 ? '#0369A1' : '#A8A29E' }}>{i + 1}</div>
                <span style={{ fontSize: 12, color: i === STEP - 1 ? '#1C1917' : '#A8A29E', fontFamily: 'Inter, sans-serif' }}>{s}</span>
              </div>
            ))}
          </div>
        </div>

        {/* Right — form */}
        <div style={{ flex: 1, padding: '80px 64px', maxWidth: 560 }}>
          <h2 style={{ fontSize: '2.5rem', fontWeight: 700, letterSpacing: '-0.03em', color: '#1C1917', margin: '0 0 12px', lineHeight: 1.15 }}>
            Tell us about<br />your organisation.
          </h2>
          <p style={{ fontSize: 14, color: '#78716C', margin: '0 0 40px', lineHeight: 1.7, fontFamily: 'Inter, sans-serif' }}>
            Used to name your clusters and configure platform defaults. Stays entirely in your environment.
          </p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 20, color: '#1C1917', fontFamily: 'Inter, sans-serif' }}>
            <Field label="Organisation name" placeholder="Acme Corp" inputStyle={{ borderRadius: 0, borderTop: 'none', borderLeft: 'none', borderRight: 'none', borderColor: '#D6D3D1', opacity: 1, background: 'transparent', color: '#1C1917', paddingLeft: 0 }} labelStyle={{ opacity: 0.6 }} />
            <Field label="Domain" placeholder="acme.io" inputStyle={{ borderRadius: 0, borderTop: 'none', borderLeft: 'none', borderRight: 'none', borderColor: '#D6D3D1', opacity: 1, background: 'transparent', color: '#1C1917', paddingLeft: 0 }} labelStyle={{ opacity: 0.6 }} />
            <Field label="Technical contact email" placeholder="platform@acme.io" inputStyle={{ borderRadius: 0, borderTop: 'none', borderLeft: 'none', borderRight: 'none', borderColor: '#D6D3D1', opacity: 1, background: 'transparent', color: '#1C1917', paddingLeft: 0 }} labelStyle={{ opacity: 0.6 }} />
          </div>
          <div style={{ display: 'flex', justifyContent: 'flex-end', marginTop: 48, gap: 12 }}>
            <button style={{ height: 40, padding: '0 20px', borderRadius: 2, border: '1px solid #D6D3D1', background: 'transparent', color: '#78716C', fontSize: 13, cursor: 'pointer', fontFamily: 'Inter, sans-serif' }}>Back</button>
            <button style={{ height: 40, padding: '0 28px', borderRadius: 2, border: 'none', background: '#0369A1', color: '#fff', fontWeight: 600, fontSize: 13, cursor: 'pointer', fontFamily: 'Inter, sans-serif', letterSpacing: '0.02em' }}>Continue →</button>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════════════════════
   DESIGN 3 — TERMINAL
   Black · monospace · CLI prompt · green-on-dark · developer aesthetic
══════════════════════════════════════════════════════════════════════════ */
function D3() {
  return (
    <div style={{ minHeight: '100vh', background: '#0A0A0A', fontFamily: '"JetBrains Mono", "Fira Code", monospace', color: '#4ADE80', display: 'flex', flexDirection: 'column', padding: '24px 32px' }}>
      {/* Top path */}
      <div style={{ fontSize: 12, color: '#374151', marginBottom: 32 }}>
        <span style={{ color: '#4ADE80' }}>openova@catalyst</span>
        <span style={{ color: '#6B7280' }}>:</span>
        <span style={{ color: '#60A5FA' }}>~/bootstrap</span>
        <span style={{ color: '#6B7280' }}>$ </span>
        <span style={{ color: '#D1D5DB' }}>wizard --step {STEP}/{TOTAL} --mode selfhosted</span>
        <span style={{ animation: 'blink 1s infinite' }}>█</span>
      </div>

      <div style={{ maxWidth: 680, width: '100%', margin: '0 auto', flex: 1 }}>
        {/* Step header */}
        <div style={{ marginBottom: 32 }}>
          <div style={{ fontSize: 11, color: '#374151', marginBottom: 8 }}>{'#'} STEP {STEP}/{TOTAL} — ORGANISATION</div>
          <div style={{ color: '#4ADE80', fontSize: 13, marginBottom: 4 }}>{'>'} Initialising cluster configuration...</div>
          <div style={{ color: '#6B7280', fontSize: 13 }}>{'>'} Enter organisation details below</div>
        </div>

        {/* Progress bar */}
        <div style={{ display: 'flex', gap: 3, marginBottom: 40 }}>
          {STEPS.map((s, i) => (
            <div key={i} style={{ flex: 1, textAlign: 'center' }}>
              <div style={{ fontSize: 9, color: i < STEP ? '#4ADE80' : '#1F2937', marginBottom: 4 }}>{s.slice(0, 6)}</div>
              <div style={{ height: 2, background: i < STEP ? '#4ADE80' : '#1F2937' }} />
            </div>
          ))}
        </div>

        {/* Fields */}
        {[
          { key: 'org', label: 'ORG_NAME', placeholder: 'AcmeCorp', hint: '# Used as cluster owner identifier' },
          { key: 'domain', label: 'ORG_DOMAIN', placeholder: 'acme.io', hint: '# Primary domain for TLS + service URLs' },
          { key: 'email', label: 'CONTACT_EMAIL', placeholder: 'platform@acme.io', hint: '# cert-manager alert recipient' },
        ].map(f => (
          <div key={f.key} style={{ marginBottom: 24 }}>
            <div style={{ fontSize: 11, color: '#374151', marginBottom: 4 }}>{f.hint}</div>
            <div style={{ display: 'flex', alignItems: 'center', gap: 0 }}>
              <span style={{ color: '#4ADE80', fontSize: 13, marginRight: 8 }}>{'$'} {f.label}=</span>
              <div style={{ flex: 1, height: 34, background: '#111', border: '1px solid #1F2937', borderRadius: 4, display: 'flex', alignItems: 'center', paddingLeft: 10, fontSize: 13 }}>
                <span style={{ color: '#6B7280' }}>"{f.placeholder}"</span>
              </div>
            </div>
          </div>
        ))}

        <div style={{ display: 'flex', gap: 16, marginTop: 40 }}>
          <button style={{ height: 36, padding: '0 20px', background: 'transparent', border: '1px solid #1F2937', color: '#374151', fontSize: 12, cursor: 'pointer', borderRadius: 4, fontFamily: 'inherit' }}>
            {'<'} --back
          </button>
          <button style={{ height: 36, padding: '0 24px', background: '#052E16', border: '1px solid #166534', color: '#4ADE80', fontSize: 12, cursor: 'pointer', borderRadius: 4, fontFamily: 'inherit', fontWeight: 600 }}>
            --next {'>'} {'>'} {'>'}
          </button>
        </div>

        <div style={{ marginTop: 48, borderTop: '1px solid #111', paddingTop: 16, fontSize: 11, color: '#1F2937' }}>
          {'# '} OpenOva Catalyst Bootstrap v1.0 · All data processed locally
        </div>
      </div>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════════════════════
   DESIGN 4 — AURORA
   Animated gradient mesh · frosted glass · vibrant · bold colours
══════════════════════════════════════════════════════════════════════════ */
function D4() {
  return (
    <div style={{
      minHeight: '100vh', display: 'flex', alignItems: 'center', justifyContent: 'center',
      background: 'linear-gradient(135deg, #1e003e 0%, #0a1628 30%, #001a3e 60%, #002020 100%)',
      fontFamily: 'Inter, sans-serif', padding: 24, position: 'relative', overflow: 'hidden',
    }}>
      {/* Mesh blobs */}
      {[
        { top: '-20%', left: '-10%', size: 700, color: 'rgba(139,92,246,0.15)' },
        { top: '40%', right: '-15%', size: 600, color: 'rgba(56,189,248,0.12)' },
        { bottom: '-20%', left: '30%', size: 500, color: 'rgba(16,185,129,0.08)' },
      ].map((b, i) => (
        <div key={i} style={{ position: 'absolute', width: b.size, height: b.size, borderRadius: '50%', background: `radial-gradient(circle, ${b.color} 0%, transparent 70%)`, ...b, pointerEvents: 'none' }} />
      ))}

      <div style={{
        width: '100%', maxWidth: 520,
        background: 'rgba(255,255,255,0.05)',
        backdropFilter: 'blur(32px)',
        WebkitBackdropFilter: 'blur(32px)',
        border: '1px solid rgba(255,255,255,0.12)',
        borderRadius: 24,
        padding: '2.5rem',
        boxShadow: '0 0 0 1px rgba(255,255,255,0.04) inset, 0 40px 100px rgba(0,0,0,0.5)',
        position: 'relative',
      }}>
        {/* Top accent line */}
        <div style={{ position: 'absolute', top: 0, left: '15%', right: '15%', height: 1, background: 'linear-gradient(90deg, transparent, rgba(139,92,246,0.8), rgba(56,189,248,0.8), transparent)', borderRadius: 1 }} />

        {/* Logo row */}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between', marginBottom: 32 }}>
          <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
            <OOLogo size={32} color="#A78BFA" />
            <div>
              <div style={{ fontSize: 10, fontWeight: 600, letterSpacing: '0.15em', color: 'rgba(255,255,255,0.35)', textTransform: 'uppercase' }}>OpenOva</div>
              <div style={{ fontSize: 15, fontWeight: 700, color: '#A78BFA', letterSpacing: '-0.01em' }}>Catalyst</div>
            </div>
          </div>
          <div style={{ display: 'flex', gap: 4 }}>
            {STEPS.map((_, i) => (
              <div key={i} style={{ width: 6, height: 6, borderRadius: '50%', background: i < STEP ? 'linear-gradient(135deg, #A78BFA, #38BDF8)' : 'rgba(255,255,255,0.1)', boxShadow: i === STEP - 1 ? '0 0 8px rgba(167,139,250,0.6)' : 'none' }} />
            ))}
          </div>
        </div>

        <h2 style={{ fontSize: '1.75rem', fontWeight: 700, letterSpacing: '-0.025em', color: '#fff', margin: '0 0 8px', lineHeight: 1.2 }}>
          Your organisation
        </h2>
        <p style={{ fontSize: 13, color: 'rgba(255,255,255,0.35)', margin: '0 0 28px', lineHeight: 1.6 }}>
          Stays in your environment — we never touch it.
        </p>

        <div style={{ display: 'flex', flexDirection: 'column', gap: 14 }}>
          {[
            { label: 'Organisation name', ph: 'Acme Corp' },
            { label: 'Domain', ph: 'acme.io' },
            { label: 'Technical contact', ph: 'platform@acme.io' },
          ].map(f => (
            <div key={f.label}>
              <label style={{ fontSize: 12, fontWeight: 500, color: 'rgba(255,255,255,0.45)', display: 'block', marginBottom: 6 }}>{f.label}</label>
              <div style={{ height: 42, borderRadius: 10, border: '1px solid rgba(255,255,255,0.12)', background: 'rgba(255,255,255,0.05)', display: 'flex', alignItems: 'center', paddingLeft: 14, fontSize: 14, color: 'rgba(255,255,255,0.25)' }}>
                {f.ph}
              </div>
            </div>
          ))}
        </div>

        <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 28, alignItems: 'center' }}>
          <button style={{ fontSize: 13, color: 'rgba(255,255,255,0.25)', background: 'none', border: 'none', cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 4 }}>
            <ChevronLeft size={14} /> Back
          </button>
          <button style={{ height: 42, padding: '0 28px', borderRadius: 10, border: 'none', background: 'linear-gradient(135deg, #A78BFA 0%, #38BDF8 100%)', color: '#fff', fontWeight: 600, fontSize: 14, cursor: 'pointer', boxShadow: '0 4px 20px rgba(167,139,250,0.35)' }}>
            Continue →
          </button>
        </div>
      </div>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════════════════════
   DESIGN 5 — FORGE
   Bold 50/50 split · massive left branding · Stripe-checkout feel
══════════════════════════════════════════════════════════════════════════ */
function D5() {
  return (
    <div style={{ minHeight: '100vh', display: 'flex', fontFamily: 'Inter, sans-serif' }}>
      {/* Left — brand */}
      <div style={{
        width: '45%', flexShrink: 0,
        background: 'linear-gradient(155deg, #020617 0%, #0C1A2E 50%, #050C18 100%)',
        padding: '56px 48px', display: 'flex', flexDirection: 'column',
        position: 'relative', overflow: 'hidden',
      }}>
        <div style={{ position: 'absolute', top: '15%', left: '10%', width: 400, height: 400, borderRadius: '50%', background: 'radial-gradient(circle, rgba(56,189,248,0.08) 0%, transparent 65%)', pointerEvents: 'none' }} />

        <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginBottom: 'auto' }}>
          <OOLogo size={36} />
          <div>
            <div style={{ fontSize: 10, fontWeight: 600, color: 'rgba(255,255,255,0.3)', letterSpacing: '0.15em', textTransform: 'uppercase' }}>OpenOva</div>
            <div style={{ fontSize: 16, fontWeight: 700, color: '#38BDF8' }}>Catalyst</div>
          </div>
        </div>

        <div style={{ marginTop: 'auto', marginBottom: 'auto' }}>
          <div style={{ fontSize: 11, fontWeight: 600, color: '#38BDF8', letterSpacing: '0.15em', textTransform: 'uppercase', marginBottom: 20 }}>Bootstrap Wizard</div>
          <h1 style={{ fontSize: 'clamp(2rem, 4vw, 3rem)', fontWeight: 800, color: '#fff', lineHeight: 1.1, letterSpacing: '-0.03em', margin: '0 0 20px' }}>
            Enterprise<br />Kubernetes<br />in minutes.
          </h1>
          <p style={{ fontSize: 14, color: 'rgba(255,255,255,0.35)', lineHeight: 1.7, maxWidth: 300, margin: '0 0 40px' }}>
            Six steps to a production-grade cluster. Runs entirely in your cloud account.
          </p>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            {STEPS.map((s, i) => (
              <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 12, opacity: i === STEP - 1 ? 1 : i < STEP ? 0.6 : 0.2 }}>
                <div style={{ width: 24, height: 24, borderRadius: '50%', background: i < STEP ? 'linear-gradient(135deg,#38BDF8,#818CF8)' : i === STEP - 1 ? 'rgba(56,189,248,0.15)' : 'rgba(255,255,255,0.05)', border: i === STEP - 1 ? '1.5px solid #38BDF8' : '1.5px solid rgba(255,255,255,0.1)', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 10, fontWeight: 700, color: i === STEP - 1 ? '#38BDF8' : '#fff' }}>
                  {i + 1}
                </div>
                <span style={{ fontSize: 13, color: i === STEP - 1 ? '#fff' : 'rgba(255,255,255,0.5)', fontWeight: i === STEP - 1 ? 600 : 400 }}>{s}</span>
                {i === STEP - 1 && <span style={{ fontSize: 10, background: 'rgba(56,189,248,0.15)', color: '#38BDF8', padding: '2px 8px', borderRadius: 20, fontWeight: 600, border: '1px solid rgba(56,189,248,0.3)' }}>current</span>}
              </div>
            ))}
          </div>
        </div>
      </div>

      {/* Right — form */}
      <div style={{ flex: 1, background: '#F8FAFC', display: 'flex', alignItems: 'center', justifyContent: 'center', padding: '48px 64px' }}>
        <div style={{ width: '100%', maxWidth: 420 }}>
          <div style={{ fontSize: 11, fontWeight: 600, color: '#94A3B8', letterSpacing: '0.1em', textTransform: 'uppercase', marginBottom: 8 }}>Step {STEP} of {TOTAL}</div>
          <h2 style={{ fontSize: '1.75rem', fontWeight: 700, color: '#0F172A', letterSpacing: '-0.025em', margin: '0 0 6px' }}>Your organisation</h2>
          <p style={{ fontSize: 13, color: '#64748B', margin: '0 0 32px', lineHeight: 1.6 }}>Cluster ownership and platform defaults.</p>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
            {[
              { label: 'Organisation name', ph: 'Acme Corp', hint: 'Used as cluster owner identifier' },
              { label: 'Domain', ph: 'acme.io', hint: 'Used for service URLs and TLS certificates' },
              { label: 'Technical contact', ph: 'platform@acme.io', hint: 'Receives cert-manager alerts' },
            ].map(f => (
              <div key={f.label}>
                <label style={{ fontSize: 13, fontWeight: 500, color: '#374151', display: 'block', marginBottom: 6 }}>{f.label}</label>
                <div style={{ height: 42, borderRadius: 8, border: '1.5px solid #E2E8F0', background: '#fff', display: 'flex', alignItems: 'center', paddingLeft: 12, fontSize: 14, color: '#94A3B8', boxShadow: '0 1px 2px rgba(0,0,0,0.04)' }}>{f.ph}</div>
                <div style={{ fontSize: 11, color: '#94A3B8', marginTop: 4 }}>{f.hint}</div>
              </div>
            ))}
          </div>

          <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 36, alignItems: 'center' }}>
            <button style={{ fontSize: 13, color: '#94A3B8', background: 'none', border: 'none', cursor: 'pointer' }}>← Back</button>
            <button style={{ height: 42, padding: '0 28px', borderRadius: 8, border: 'none', background: '#0F172A', color: '#fff', fontWeight: 600, fontSize: 14, cursor: 'pointer', letterSpacing: '-0.01em' }}>
              Continue →
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════════════════════
   DESIGN 6 — WORKSPACE
   App chrome · top toolbar with step tabs · VS Code-like sidebar
══════════════════════════════════════════════════════════════════════════ */
function D6() {
  return (
    <div style={{ minHeight: '100vh', display: 'flex', flexDirection: 'column', fontFamily: 'Inter, sans-serif', background: '#1E1E2E' }}>
      {/* Title bar */}
      <div style={{ height: 32, background: '#141420', display: 'flex', alignItems: 'center', paddingLeft: 16, gap: 6, flexShrink: 0 }}>
        {['#FF5F57','#FFBD2E','#28CA42'].map((c,i) => <div key={i} style={{ width: 12, height: 12, borderRadius: '50%', background: c }} />)}
        <span style={{ fontSize: 11, color: 'rgba(255,255,255,0.3)', marginLeft: 12, fontFamily: 'monospace' }}>OpenOva Catalyst — New Deployment</span>
      </div>

      {/* Toolbar */}
      <div style={{ height: 40, background: '#1C1C2A', borderBottom: '1px solid rgba(255,255,255,0.06)', display: 'flex', alignItems: 'center', paddingLeft: 16, gap: 2, flexShrink: 0 }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginRight: 20 }}>
          <OOLogo size={18} />
          <span style={{ fontSize: 12, fontWeight: 600, color: 'rgba(255,255,255,0.6)' }}>Catalyst</span>
        </div>
        {STEPS.map((s, i) => (
          <div key={i} style={{ height: 32, padding: '0 14px', borderRadius: 4, display: 'flex', alignItems: 'center', fontSize: 12, cursor: 'pointer', background: i === STEP - 1 ? 'rgba(56,189,248,0.12)' : 'transparent', color: i === STEP - 1 ? '#38BDF8' : 'rgba(255,255,255,0.3)', fontWeight: i === STEP - 1 ? 500 : 400, borderBottom: i === STEP - 1 ? '2px solid #38BDF8' : '2px solid transparent', position: 'relative', top: 1, gap: 6 }}>
            {i < STEP - 1 && <span style={{ fontSize: 9, background: '#22C55E', color: '#fff', borderRadius: 10, padding: '1px 5px', fontWeight: 700 }}>✓</span>}
            {s}
          </div>
        ))}
      </div>

      <div style={{ flex: 1, display: 'flex', overflow: 'hidden' }}>
        {/* Activity bar */}
        <div style={{ width: 48, background: '#141420', display: 'flex', flexDirection: 'column', alignItems: 'center', paddingTop: 8, gap: 4, flexShrink: 0 }}>
          {['⬡', '⚙', '📋', '🔍'].map((icon, i) => (
            <div key={i} style={{ width: 32, height: 32, borderRadius: 6, display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 14, cursor: 'pointer', opacity: i === 0 ? 1 : 0.3, background: i === 0 ? 'rgba(56,189,248,0.15)' : 'transparent', borderLeft: i === 0 ? '2px solid #38BDF8' : '2px solid transparent' }}>
              {icon}
            </div>
          ))}
        </div>

        {/* Sidebar */}
        <div style={{ width: 200, background: '#1A1A28', borderRight: '1px solid rgba(255,255,255,0.06)', padding: 12, flexShrink: 0 }}>
          <div style={{ fontSize: 10, fontWeight: 600, color: 'rgba(255,255,255,0.25)', letterSpacing: '0.1em', textTransform: 'uppercase', marginBottom: 8, paddingLeft: 4 }}>DEPLOYMENT</div>
          {STEPS.map((s, i) => (
            <div key={i} style={{ padding: '6px 8px', borderRadius: 4, fontSize: 12, color: i === STEP - 1 ? '#fff' : 'rgba(255,255,255,0.3)', background: i === STEP - 1 ? 'rgba(56,189,248,0.1)' : 'transparent', cursor: 'pointer', display: 'flex', gap: 8, alignItems: 'center', marginBottom: 1 }}>
              <span style={{ color: i < STEP - 1 ? '#22C55E' : i === STEP - 1 ? '#38BDF8' : 'rgba(255,255,255,0.15)', fontSize: 10 }}>{i < STEP - 1 ? '✓' : i === STEP - 1 ? '▶' : '○'}</span>
              {s}
            </div>
          ))}
        </div>

        {/* Main content */}
        <div style={{ flex: 1, padding: '40px 48px', overflowY: 'auto' }}>
          <div style={{ maxWidth: 520 }}>
            <div style={{ fontSize: 10, fontWeight: 600, color: '#38BDF8', letterSpacing: '0.1em', textTransform: 'uppercase', marginBottom: 8 }}>organisation.yaml</div>
            <h2 style={{ fontSize: '1.5rem', fontWeight: 600, color: '#E2E8F0', letterSpacing: '-0.02em', margin: '0 0 6px' }}>Organisation settings</h2>
            <p style={{ fontSize: 13, color: 'rgba(255,255,255,0.3)', margin: '0 0 32px', lineHeight: 1.6 }}>Configure your cluster ownership and platform defaults.</p>

            <div style={{ display: 'flex', flexDirection: 'column', gap: 18 }}>
              {[
                { label: 'org_name', ph: '"Acme Corp"', comment: '# Cluster owner identifier' },
                { label: 'domain', ph: '"acme.io"', comment: '# Service URLs + TLS' },
                { label: 'contact_email', ph: '"platform@acme.io"', comment: '# cert-manager alerts' },
              ].map(f => (
                <div key={f.label} style={{ background: '#12121E', borderRadius: 8, border: '1px solid rgba(255,255,255,0.06)', padding: '12px 16px' }}>
                  <div style={{ fontSize: 11, color: 'rgba(255,255,255,0.2)', marginBottom: 6, fontFamily: 'monospace' }}>{f.comment}</div>
                  <div style={{ display: 'flex', alignItems: 'center', gap: 8, fontFamily: 'monospace', fontSize: 13 }}>
                    <span style={{ color: '#818CF8' }}>{f.label}</span>
                    <span style={{ color: 'rgba(255,255,255,0.2)' }}>:</span>
                    <span style={{ color: '#4ADE80', opacity: 0.5 }}>{f.ph}</span>
                  </div>
                </div>
              ))}
            </div>

            <div style={{ display: 'flex', gap: 10, marginTop: 32 }}>
              <button style={{ height: 36, padding: '0 16px', background: 'rgba(255,255,255,0.05)', border: '1px solid rgba(255,255,255,0.08)', borderRadius: 6, color: 'rgba(255,255,255,0.4)', fontSize: 12, cursor: 'pointer' }}>← Back</button>
              <button style={{ height: 36, padding: '0 20px', background: '#38BDF8', border: 'none', borderRadius: 6, color: '#000', fontSize: 12, fontWeight: 700, cursor: 'pointer', letterSpacing: '0.02em' }}>CONTINUE →</button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════════════════════
   DESIGN 7 — JOURNEY
   Horizontal segmented step rail · Airbnb/booking feel · light + clean
══════════════════════════════════════════════════════════════════════════ */
function D7() {
  return (
    <div style={{ minHeight: '100vh', background: '#fff', fontFamily: 'Inter, sans-serif', display: 'flex', flexDirection: 'column' }}>
      {/* Header */}
      <header style={{ padding: '20px 48px', display: 'flex', alignItems: 'center', justifyContent: 'space-between', borderBottom: '1px solid #F3F4F6' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
          <OOLogo size={28} color="#6366F1" />
          <span style={{ fontSize: 15, fontWeight: 700, color: '#111827', letterSpacing: '-0.01em' }}>OpenOva Catalyst</span>
        </div>
        <button style={{ fontSize: 13, color: '#6B7280', background: 'none', border: 'none', cursor: 'pointer' }}>✕ Exit</button>
      </header>

      {/* Step rail */}
      <div style={{ padding: '24px 48px', borderBottom: '1px solid #F3F4F6' }}>
        <div style={{ display: 'flex', alignItems: 'center', gap: 0, position: 'relative' }}>
          {STEPS.map((s, i) => (
            <div key={i} style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', position: 'relative' }}>
              {i < STEPS.length - 1 && (
                <div style={{ position: 'absolute', top: 15, left: '50%', right: '-50%', height: 2, background: i < STEP - 1 ? '#6366F1' : '#E5E7EB', zIndex: 0 }} />
              )}
              <div style={{ width: 30, height: 30, borderRadius: '50%', background: i < STEP ? '#6366F1' : i === STEP - 1 ? '#fff' : '#F9FAFB', border: i === STEP - 1 ? '2.5px solid #6366F1' : i < STEP ? 'none' : '2px solid #E5E7EB', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 12, fontWeight: 700, color: i < STEP ? '#fff' : i === STEP - 1 ? '#6366F1' : '#9CA3AF', zIndex: 1, position: 'relative', boxShadow: i === STEP - 1 ? '0 0 0 4px rgba(99,102,241,0.1)' : 'none' }}>
                {i < STEP - 1 ? '✓' : i + 1}
              </div>
              <div style={{ marginTop: 8, fontSize: 11, fontWeight: i === STEP - 1 ? 600 : 400, color: i === STEP - 1 ? '#111827' : i < STEP ? '#6366F1' : '#9CA3AF', textAlign: 'center' }}>{s}</div>
            </div>
          ))}
        </div>
      </div>

      {/* Content */}
      <div style={{ flex: 1, display: 'flex', alignItems: 'flex-start', justifyContent: 'center', padding: '64px 24px' }}>
        <div style={{ width: '100%', maxWidth: 480 }}>
          <h2 style={{ fontSize: '1.875rem', fontWeight: 700, color: '#111827', letterSpacing: '-0.025em', margin: '0 0 8px' }}>Tell us about your organisation</h2>
          <p style={{ fontSize: 14, color: '#6B7280', margin: '0 0 36px', lineHeight: 1.6 }}>This information stays in your environment and is used to name your clusters.</p>

          <div style={{ display: 'flex', flexDirection: 'column', gap: 20 }}>
            {[
              { label: 'Organisation name', ph: 'Acme Corp', hint: 'Used as the cluster owner identifier' },
              { label: 'Domain', ph: 'acme.io', hint: 'Your primary domain for service URLs and TLS' },
              { label: 'Technical contact email', ph: 'platform@acme.io', hint: 'Receives cert-manager expiry alerts' },
            ].map(f => (
              <div key={f.label}>
                <label style={{ fontSize: 13, fontWeight: 500, color: '#374151', display: 'block', marginBottom: 6 }}>{f.label}</label>
                <div style={{ height: 46, borderRadius: 10, border: '2px solid #E5E7EB', background: '#fff', display: 'flex', alignItems: 'center', paddingLeft: 14, fontSize: 14, color: '#D1D5DB', transition: 'all 0.15s' }}>{f.ph}</div>
                <div style={{ fontSize: 12, color: '#9CA3AF', marginTop: 5 }}>{f.hint}</div>
              </div>
            ))}
          </div>

          <div style={{ display: 'flex', justifyContent: 'space-between', marginTop: 40, alignItems: 'center' }}>
            <button style={{ height: 44, padding: '0 20px', background: '#fff', border: '1.5px solid #E5E7EB', borderRadius: 10, color: '#374151', fontSize: 14, cursor: 'pointer', fontWeight: 500 }}>← Back</button>
            <button style={{ height: 44, padding: '0 32px', background: '#6366F1', border: 'none', borderRadius: 10, color: '#fff', fontWeight: 600, fontSize: 14, cursor: 'pointer', boxShadow: '0 4px 14px rgba(99,102,241,0.3)' }}>Continue →</button>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════════════════════
   DESIGN 8 — PAPER
   GOV.UK/document style · pure white · numbered margin · no chrome
══════════════════════════════════════════════════════════════════════════ */
function D8() {
  return (
    <div style={{ minHeight: '100vh', background: '#fff', fontFamily: '"Helvetica Neue", Arial, sans-serif', display: 'flex', flexDirection: 'column' }}>
      {/* Thin top bar */}
      <div style={{ height: 6, background: '#0B57D0' }} />

      <div style={{ flex: 1, maxWidth: 800, margin: '0 auto', padding: '48px 32px', width: '100%' }}>
        {/* Breadcrumb */}
        <div style={{ fontSize: 13, color: '#0B57D0', marginBottom: 24, display: 'flex', alignItems: 'center', gap: 6 }}>
          <span style={{ textDecoration: 'underline', cursor: 'pointer' }}>OpenOva Catalyst</span>
          <span style={{ color: '#9CA3AF' }}>›</span>
          <span style={{ textDecoration: 'underline', cursor: 'pointer' }}>New deployment</span>
          <span style={{ color: '#9CA3AF' }}>›</span>
          <span style={{ color: '#374151' }}>Organisation</span>
        </div>

        <div style={{ display: 'flex', gap: 64 }}>
          {/* Left margin — step list */}
          <div style={{ width: 180, flexShrink: 0 }}>
            <div style={{ fontSize: 11, fontWeight: 700, color: '#6B7280', letterSpacing: '0.08em', textTransform: 'uppercase', marginBottom: 16, paddingBottom: 8, borderBottom: '2px solid #E5E7EB' }}>Steps</div>
            {STEPS.map((s, i) => (
              <div key={i} style={{ display: 'flex', gap: 10, alignItems: 'flex-start', marginBottom: 12, opacity: i > STEP - 1 ? 0.4 : 1 }}>
                <div style={{ width: 22, height: 22, borderRadius: '50%', background: i < STEP ? '#0B57D0' : 'transparent', border: `2px solid ${i === STEP - 1 ? '#0B57D0' : '#D1D5DB'}`, display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 11, fontWeight: 700, color: i < STEP ? '#fff' : i === STEP - 1 ? '#0B57D0' : '#6B7280', flexShrink: 0, marginTop: 1 }}>
                  {i < STEP - 1 ? '✓' : i + 1}
                </div>
                <span style={{ fontSize: 13, color: i === STEP - 1 ? '#111827' : '#6B7280', fontWeight: i === STEP - 1 ? 600 : 400, lineHeight: 1.5 }}>{s}</span>
              </div>
            ))}
          </div>

          {/* Right — form */}
          <div style={{ flex: 1 }}>
            <h1 style={{ fontSize: '1.75rem', fontWeight: 700, color: '#111827', margin: '0 0 6px', letterSpacing: '-0.01em' }}>Organisation</h1>
            <p style={{ fontSize: 14, color: '#6B7280', margin: '0 0 32px', lineHeight: 1.7, borderLeft: '4px solid #E5E7EB', paddingLeft: 14 }}>
              This information is used to name your clusters and configure platform defaults. It stays in your environment and is never stored on our servers.
            </p>

            <div style={{ display: 'flex', flexDirection: 'column', gap: 24 }}>
              {[
                { label: 'Organisation name', ph: 'Acme Corp', hint: 'Used as the cluster owner identifier' },
                { label: 'Domain', ph: 'acme.io', hint: 'Your primary domain — used for service URLs and TLS certificates' },
                { label: 'Technical contact email', ph: 'platform@acme.io', hint: 'Receives cert-manager expiry alerts and critical notifications' },
              ].map(f => (
                <div key={f.label}>
                  <label style={{ fontSize: 14, fontWeight: 700, color: '#111827', display: 'block', marginBottom: 4 }}>{f.label}</label>
                  <div style={{ fontSize: 13, color: '#6B7280', marginBottom: 8 }}>{f.hint}</div>
                  <div style={{ height: 40, border: '2px solid #111827', borderRadius: 0, background: '#fff', display: 'flex', alignItems: 'center', paddingLeft: 12, fontSize: 14, color: '#9CA3AF', maxWidth: 360 }}>{f.ph}</div>
                </div>
              ))}
            </div>

            <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginTop: 40 }}>
              <button style={{ height: 42, padding: '0 24px', background: '#0B57D0', border: 'none', color: '#fff', fontSize: 14, fontWeight: 700, cursor: 'pointer' }}>Continue</button>
              <button style={{ height: 42, padding: '0 20px', background: 'transparent', border: 'none', color: '#0B57D0', fontSize: 14, cursor: 'pointer', textDecoration: 'underline' }}>← Previous step</button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════════════════════
   DESIGN 9 — ELECTRIC
   Dark cyberpunk · neon glow borders · gradient headings · bold unexpected
══════════════════════════════════════════════════════════════════════════ */
function D9() {
  return (
    <div style={{ minHeight: '100vh', background: '#050508', fontFamily: 'Inter, sans-serif', display: 'flex', padding: 0 }}>
      {/* Neon left strip */}
      <div style={{ width: 4, background: 'linear-gradient(to bottom, #00D2FF, #7B2FFF, #FF2D78)', flexShrink: 0 }} />

      <div style={{ flex: 1, display: 'flex' }}>
        {/* Left panel */}
        <div style={{ width: 320, padding: '48px 32px', display: 'flex', flexDirection: 'column', borderRight: '1px solid rgba(0,210,255,0.08)', flexShrink: 0 }}>
          <div style={{ marginBottom: 48 }}>
            <OOLogo size={48} color="#00D2FF" />
            <div style={{ marginTop: 16 }}>
              <div style={{ fontSize: 11, letterSpacing: '0.2em', color: 'rgba(0,210,255,0.4)', textTransform: 'uppercase', marginBottom: 4 }}>OpenOva</div>
              <div style={{ fontSize: 22, fontWeight: 800, letterSpacing: '-0.02em', background: 'linear-gradient(90deg, #00D2FF, #7B2FFF)', WebkitBackgroundClip: 'text', WebkitTextFillColor: 'transparent' }}>CATALYST</div>
            </div>
          </div>

          {STEPS.map((s, i) => (
            <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 12, padding: '10px 0', borderBottom: '1px solid rgba(255,255,255,0.03)', opacity: i > STEP - 1 ? 0.2 : 1 }}>
              <div style={{ width: 28, height: 28, borderRadius: 6, border: `1px solid ${i === STEP - 1 ? 'rgba(0,210,255,0.5)' : 'rgba(255,255,255,0.06)'}`, background: i === STEP - 1 ? 'rgba(0,210,255,0.08)' : 'transparent', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 11, fontWeight: 700, color: i === STEP - 1 ? '#00D2FF' : i < STEP ? '#7B2FFF' : 'rgba(255,255,255,0.2)', boxShadow: i === STEP - 1 ? '0 0 12px rgba(0,210,255,0.2), inset 0 0 8px rgba(0,210,255,0.05)' : 'none' }}>
                {i < STEP - 1 ? '✓' : i + 1}
              </div>
              <span style={{ fontSize: 13, fontWeight: i === STEP - 1 ? 600 : 400, color: i === STEP - 1 ? '#fff' : 'rgba(255,255,255,0.4)', letterSpacing: i === STEP - 1 ? '0.02em' : 0 }}>{s}</span>
              {i === STEP - 1 && <div style={{ marginLeft: 'auto', width: 6, height: 6, borderRadius: '50%', background: '#00D2FF', boxShadow: '0 0 8px #00D2FF' }} />}
            </div>
          ))}
        </div>

        {/* Right — form */}
        <div style={{ flex: 1, padding: '64px 56px', display: 'flex', flexDirection: 'column', justifyContent: 'center' }}>
          <div style={{ maxWidth: 520 }}>
            <div style={{ fontSize: 11, fontWeight: 600, letterSpacing: '0.2em', color: 'rgba(0,210,255,0.5)', textTransform: 'uppercase', marginBottom: 16 }}>
              {'// '} STEP_{STEP.toString().padStart(2,'0')} :: ORGANISATION
            </div>
            <h2 style={{ fontSize: 'clamp(1.75rem, 3vw, 2.5rem)', fontWeight: 800, letterSpacing: '-0.03em', margin: '0 0 12px', background: 'linear-gradient(90deg, #fff 40%, rgba(255,255,255,0.4))', WebkitBackgroundClip: 'text', WebkitTextFillColor: 'transparent', lineHeight: 1.1 }}>
              Organisation<br />details.
            </h2>
            <p style={{ fontSize: 13, color: 'rgba(255,255,255,0.25)', margin: '0 0 36px', lineHeight: 1.7 }}>Cluster ownership and platform defaults — runs only in your account.</p>

            <div style={{ display: 'flex', flexDirection: 'column', gap: 16 }}>
              {[
                { label: 'Organisation name', ph: 'Acme Corp' },
                { label: 'Domain', ph: 'acme.io' },
                { label: 'Technical contact', ph: 'platform@acme.io' },
              ].map(f => (
                <div key={f.label}>
                  <label style={{ fontSize: 11, fontWeight: 600, color: 'rgba(0,210,255,0.5)', letterSpacing: '0.1em', textTransform: 'uppercase', display: 'block', marginBottom: 8 }}>{f.label}</label>
                  <div style={{ height: 44, borderRadius: 6, border: '1px solid rgba(0,210,255,0.15)', background: 'rgba(0,210,255,0.03)', display: 'flex', alignItems: 'center', paddingLeft: 14, fontSize: 14, color: 'rgba(255,255,255,0.2)', fontFamily: 'monospace', boxShadow: 'inset 0 1px 0 rgba(0,210,255,0.05)' }}>{f.ph}</div>
                </div>
              ))}
            </div>

            <div style={{ display: 'flex', gap: 12, marginTop: 36 }}>
              <button style={{ height: 44, padding: '0 20px', background: 'rgba(255,255,255,0.03)', border: '1px solid rgba(255,255,255,0.08)', borderRadius: 6, color: 'rgba(255,255,255,0.3)', fontSize: 13, cursor: 'pointer' }}>← Back</button>
              <button style={{ height: 44, padding: '0 28px', borderRadius: 6, border: '1px solid rgba(0,210,255,0.4)', background: 'rgba(0,210,255,0.08)', color: '#00D2FF', fontSize: 13, fontWeight: 700, cursor: 'pointer', letterSpacing: '0.05em', textTransform: 'uppercase', boxShadow: '0 0 20px rgba(0,210,255,0.15)', transition: 'all 0.2s' }}>
                CONTINUE →
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════════════════════
   DESIGN 10 — WHISPER
   Typeform-inspired · one focused field · full-screen · absolute minimal
══════════════════════════════════════════════════════════════════════════ */
function D10() {
  const [activeField, setActiveField] = useState(0)
  const fields = ['Organisation name', 'Domain', 'Technical contact email']
  const placeholders = ['Acme Corp', 'acme.io', 'platform@acme.io']
  const hints = ['Used as your cluster owner identifier', 'Primary domain for TLS + service URLs', 'Receives critical platform alerts']

  return (
    <div style={{ minHeight: '100vh', background: '#FAFAFA', fontFamily: 'Inter, sans-serif', display: 'flex', flexDirection: 'column' }}>
      {/* Thin progress */}
      <div style={{ height: 3, background: '#F0F0F0', flexShrink: 0 }}>
        <div style={{ height: '100%', width: `${(STEP / TOTAL) * 100}%`, background: 'linear-gradient(90deg, #FF6B6B, #FF8E53)', transition: 'width 0.5s' }} />
      </div>

      {/* Logo minimal */}
      <div style={{ padding: '20px 32px', display: 'flex', alignItems: 'center', gap: 8 }}>
        <OOLogo size={20} color="#FF6B6B" />
        <span style={{ fontSize: 13, fontWeight: 600, color: '#374151', letterSpacing: '-0.01em' }}>OpenOva Catalyst</span>
      </div>

      {/* Main content */}
      <div style={{ flex: 1, display: 'flex', alignItems: 'center', justifyContent: 'center', padding: '0 24px' }}>
        <div style={{ width: '100%', maxWidth: 600 }}>
          {/* Context */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 48 }}>
            {fields.map((_, i) => (
              <div key={i} style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                <div style={{ width: 24, height: 24, borderRadius: '50%', background: i <= activeField ? '#FF6B6B' : '#F0F0F0', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 11, fontWeight: 700, color: i <= activeField ? '#fff' : '#9CA3AF', cursor: 'pointer', transition: 'all 0.2s' }} onClick={() => setActiveField(i)}>
                  {i + 1}
                </div>
                {i < fields.length - 1 && <div style={{ width: 32, height: 1, background: i < activeField ? '#FF6B6B' : '#E5E7EB' }} />}
              </div>
            ))}
          </div>

          {/* Active field — big */}
          <div>
            <div style={{ fontSize: 11, fontWeight: 600, color: '#FF6B6B', letterSpacing: '0.1em', textTransform: 'uppercase', marginBottom: 12 }}>
              {activeField + 1} →
            </div>
            <label style={{ fontSize: 'clamp(1.5rem, 3vw, 2.25rem)', fontWeight: 700, color: '#111827', letterSpacing: '-0.025em', display: 'block', marginBottom: 8, lineHeight: 1.2 }}>
              {fields[activeField]}
            </label>
            <p style={{ fontSize: 14, color: '#9CA3AF', marginBottom: 24, lineHeight: 1.6 }}>{hints[activeField]}</p>

            <div style={{ position: 'relative' }}>
              <div style={{ fontSize: 'clamp(1rem, 2vw, 1.25rem)', color: '#D1D5DB', paddingBottom: 12, borderBottom: '2px solid #FF6B6B', letterSpacing: '-0.01em' }}>
                {placeholders[activeField]}
              </div>
            </div>

            <div style={{ display: 'flex', alignItems: 'center', gap: 12, marginTop: 28 }}>
              <button
                onClick={() => setActiveField(Math.min(fields.length - 1, activeField + 1))}
                style={{ height: 44, padding: '0 28px', background: '#FF6B6B', border: 'none', borderRadius: 8, color: '#fff', fontWeight: 600, fontSize: 15, cursor: 'pointer', display: 'flex', alignItems: 'center', gap: 8, boxShadow: '0 4px 14px rgba(255,107,107,0.3)' }}>
                OK <span style={{ fontSize: 18, opacity: 0.8 }}>✓</span>
              </button>
              <span style={{ fontSize: 12, color: '#D1D5DB' }}>press <kbd style={{ background: '#F3F4F6', border: '1px solid #E5E7EB', borderRadius: 4, padding: '2px 6px', fontFamily: 'monospace', fontSize: 11 }}>Enter</kbd></span>
            </div>
          </div>
        </div>
      </div>

      {/* Bottom nav */}
      <div style={{ padding: '16px 32px', borderTop: '1px solid #F3F4F6', display: 'flex', justifyContent: 'space-between', alignItems: 'center' }}>
        <span style={{ fontSize: 12, color: '#9CA3AF' }}>Step {STEP} of {TOTAL} — Organisation</span>
        <div style={{ display: 'flex', gap: 8 }}>
          <button onClick={() => setActiveField(Math.max(0, activeField - 1))} style={{ width: 32, height: 32, borderRadius: 6, border: '1px solid #E5E7EB', background: '#fff', cursor: 'pointer', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 16, color: '#6B7280' }}>↑</button>
          <button onClick={() => setActiveField(Math.min(fields.length - 1, activeField + 1))} style={{ width: 32, height: 32, borderRadius: 6, border: '1px solid #E5E7EB', background: '#fff', cursor: 'pointer', display: 'flex', alignItems: 'center', justifyContent: 'center', fontSize: 16, color: '#6B7280' }}>↓</button>
        </div>
      </div>
    </div>
  )
}

/* ══════════════════════════════════════════════════════════════════════════
   PICKER
══════════════════════════════════════════════════════════════════════════ */
const DESIGNS = [
  { id: 1, name: 'Cosmos',     tagline: 'Frosted glass card on dark radial gradient',        component: D1 },
  { id: 2, name: 'Editorial',  tagline: 'Magazine feel · watermark step number · serif',      component: D2 },
  { id: 3, name: 'Terminal',   tagline: 'CLI aesthetic · monospace · developer-first',         component: D3 },
  { id: 4, name: 'Aurora',     tagline: 'Animated gradient mesh · glassmorphism · vibrant',   component: D4 },
  { id: 5, name: 'Forge',      tagline: 'Bold 50/50 split · massive branding · Stripe feel',  component: D5 },
  { id: 6, name: 'Workspace',  tagline: 'App chrome · tab navigation · VS Code sidebar',      component: D6 },
  { id: 7, name: 'Journey',    tagline: 'Horizontal step rail · Airbnb/booking feel',         component: D7 },
  { id: 8, name: 'Paper',      tagline: 'GOV.UK document style · numbered margin · no chrome',component: D8 },
  { id: 9, name: 'Electric',   tagline: 'Neon glow borders · gradient headings · cyberpunk',  component: D9 },
  { id: 10, name: 'Whisper',   tagline: 'Typeform-inspired · one field at a time · focused',  component: D10 },
]

export function DesignShowcase() {
  const [active, setActive] = useState<number | null>(null)

  if (active !== null) {
    const design = DESIGNS.find(d => d.id === active)!
    const Component = design.component
    return (
      <div style={{ position: 'relative' }}>
        {/* Floating back button */}
        <div style={{ position: 'fixed', top: 16, right: 16, zIndex: 9999, display: 'flex', gap: 8 }}>
          <button
            onClick={() => setActive(active > 1 ? active - 1 : active)}
            disabled={active === 1}
            style={{ height: 36, padding: '0 14px', borderRadius: 8, border: '1px solid rgba(255,255,255,0.2)', background: 'rgba(0,0,0,0.7)', color: '#fff', fontSize: 13, cursor: 'pointer', backdropFilter: 'blur(8px)' }}
          >← Prev</button>
          <button
            onClick={() => setActive(null)}
            style={{ height: 36, padding: '0 16px', borderRadius: 8, border: '1px solid rgba(255,255,255,0.2)', background: 'rgba(0,0,0,0.7)', color: '#fff', fontSize: 13, cursor: 'pointer', backdropFilter: 'blur(8px)', display: 'flex', alignItems: 'center', gap: 6 }}
          >
            <Eye size={14} /> All designs
          </button>
          <button
            onClick={() => setActive(active < 10 ? active + 1 : active)}
            disabled={active === 10}
            style={{ height: 36, padding: '0 14px', borderRadius: 8, border: '1px solid rgba(255,255,255,0.2)', background: 'rgba(0,0,0,0.7)', color: '#fff', fontSize: 13, cursor: 'pointer', backdropFilter: 'blur(8px)' }}
          >Next →</button>
        </div>
        <div style={{ position: 'fixed', top: 16, left: 16, zIndex: 9999 }}>
          <div style={{ height: 36, padding: '0 16px', borderRadius: 8, background: 'rgba(0,0,0,0.7)', color: '#fff', fontSize: 13, backdropFilter: 'blur(8px)', display: 'flex', alignItems: 'center', gap: 8, border: '1px solid rgba(255,255,255,0.1)' }}>
            <span style={{ opacity: 0.5 }}>Design</span>
            <span style={{ fontWeight: 700 }}>{active}/10</span>
            <span style={{ opacity: 0.5 }}>—</span>
            <span>{design.name}</span>
          </div>
        </div>
        <Component />
      </div>
    )
  }

  return (
    <div style={{ minHeight: '100vh', background: '#09090B', fontFamily: 'Inter, sans-serif', padding: '48px 40px' }}>
      <div style={{ maxWidth: 1100, margin: '0 auto' }}>
        <div style={{ marginBottom: 48, textAlign: 'center' }}>
          <div style={{ fontSize: 11, fontWeight: 600, letterSpacing: '0.15em', color: '#38BDF8', textTransform: 'uppercase', marginBottom: 12 }}>Design Concepts</div>
          <h1 style={{ fontSize: '2.5rem', fontWeight: 800, color: '#fff', letterSpacing: '-0.03em', margin: '0 0 12px' }}>10 Wizard Designs</h1>
          <p style={{ fontSize: 14, color: '#52525B', lineHeight: 1.7 }}>Click any design to preview it full-screen. Use Prev/Next to compare.</p>
        </div>

        <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(300px, 1fr))', gap: 20 }}>
          {DESIGNS.map(d => (
            <button
              key={d.id}
              onClick={() => setActive(d.id)}
              style={{
                background: '#111113', border: '1.5px solid #1F1F23',
                borderRadius: 16, overflow: 'hidden', cursor: 'pointer',
                textAlign: 'left', transition: 'all 0.2s', padding: 0,
              }}
              onMouseEnter={e => {
                const b = e.currentTarget as HTMLButtonElement
                b.style.borderColor = '#38BDF8'
                b.style.boxShadow = '0 0 0 1px rgba(56,189,248,0.2), 0 8px 32px rgba(0,0,0,0.4)'
                b.style.transform = 'translateY(-2px)'
              }}
              onMouseLeave={e => {
                const b = e.currentTarget as HTMLButtonElement
                b.style.borderColor = '#1F1F23'
                b.style.boxShadow = 'none'
                b.style.transform = 'none'
              }}
            >
              {/* Preview iframe-ish */}
              <div style={{ height: 180, background: '#050507', display: 'flex', alignItems: 'center', justifyContent: 'center', borderBottom: '1px solid #1F1F23', position: 'relative', overflow: 'hidden' }}>
                <div style={{ position: 'absolute', inset: 0, transform: 'scale(0.35)', transformOrigin: 'top center', pointerEvents: 'none', width: '286%', marginLeft: '-93%' }}>
                  <d.component />
                </div>
              </div>
              <div style={{ padding: '16px 20px' }}>
                <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 6 }}>
                  <span style={{ fontSize: 11, fontWeight: 700, color: '#38BDF8', background: 'rgba(56,189,248,0.1)', padding: '2px 8px', borderRadius: 20 }}>{d.id}</span>
                  <span style={{ fontSize: 15, fontWeight: 700, color: '#fff', letterSpacing: '-0.01em' }}>{d.name}</span>
                </div>
                <p style={{ fontSize: 12, color: '#52525B', margin: 0, lineHeight: 1.5 }}>{d.tagline}</p>
              </div>
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}
