import { Outlet, Link } from '@tanstack/react-router'
import { X, Sun, Moon, Shield, Lock, Code2, Check } from 'lucide-react'
import { IS_SAAS } from '@/shared/constants/env'
import { useWizardStore } from '@/entities/deployment/store'
import { useTheme } from '@/shared/lib/useTheme'

export const WIZARD_STEPS = [
  { id: 1, label: 'Organisation',   description: 'Name, domain, contact' },
  { id: 2, label: 'Cloud provider', description: 'Select target cloud' },
  { id: 3, label: 'Credentials',    description: 'API access token' },
  { id: 4, label: 'Infrastructure', description: 'Regions, nodes, sizing' },
  { id: 5, label: 'Components',     description: 'Platform components' },
  { id: 6, label: 'Review',         description: 'Confirm and provision' },
]

function OOLogo({ size = 40 }: { size?: number }) {
  return (
    <svg
      width={size}
      height={size}
      viewBox="140 60 420 280"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      aria-hidden="true"
    >
      <defs>
        <linearGradient id="oo-g" x1="0%" y1="0%" x2="100%" y2="0%">
          <stop offset="0%" stopColor="#38BDF8" />
          <stop offset="100%" stopColor="#818CF8" />
        </linearGradient>
      </defs>
      <path
        d="M 300 88.1966 A 150 150 0 1 0 350 200 A 150 150 0 1 1 400 311.8034"
        fill="none"
        stroke="url(#oo-g)"
        strokeWidth="80"
        strokeLinecap="butt"
      />
    </svg>
  )
}

export function WizardLayout() {
  const { currentStep, completedSteps, setStep } = useWizardStore()
  const { theme, toggle } = useTheme()

  const totalSteps = WIZARD_STEPS.length
  // progress bar: 0% on step 1, 100% on step 6
  const progress = Math.round(((currentStep - 1) / (totalSteps - 1)) * 100)

  return (
    <div style={{ display: 'flex', minHeight: '100dvh', fontFamily: 'var(--font-sans)' }}>

      {/* ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ */}
      {/* LEFT PANEL — always dark, brand panel                       */}
      {/* ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ */}
      <aside
        className="hidden md:flex"
        style={{
          width: 340,
          flexShrink: 0,
          flexDirection: 'column',
          padding: '2.5rem 2rem',
          position: 'relative',
          overflow: 'hidden',
          background: 'linear-gradient(160deg, #0D1117 0%, #09090F 55%, #07090E 100%)',
          borderRight: '1px solid rgba(255,255,255,0.06)',
        }}
      >
        {/* Ambient brand glow — top-left */}
        <div style={{
          position: 'absolute', top: -120, left: -120,
          width: 480, height: 480, borderRadius: '50%',
          background: 'radial-gradient(circle, rgba(56,189,248,0.07) 0%, transparent 65%)',
          pointerEvents: 'none',
        }} />
        {/* Subtle mid glow */}
        <div style={{
          position: 'absolute', bottom: 80, right: -80,
          width: 320, height: 320, borderRadius: '50%',
          background: 'radial-gradient(circle, rgba(129,140,248,0.04) 0%, transparent 65%)',
          pointerEvents: 'none',
        }} />

        {/* ── Logo ─────────────────────────────────────────────── */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 14, marginBottom: 56, position: 'relative', zIndex: 1 }}>
          <div style={{ position: 'relative', flexShrink: 0 }}>
            {/* Glow behind logo */}
            <div style={{
              position: 'absolute', inset: -10,
              background: 'radial-gradient(circle, rgba(56,189,248,0.18) 0%, transparent 70%)',
              borderRadius: '50%',
            }} />
            <OOLogo size={44} />
          </div>
          <div>
            <div style={{
              fontSize: 10, fontWeight: 600, letterSpacing: '0.18em',
              textTransform: 'uppercase', color: 'rgba(255,255,255,0.35)',
              marginBottom: 3, lineHeight: 1,
            }}>
              OpenOva
            </div>
            <div style={{
              fontSize: 18, fontWeight: 700, letterSpacing: '-0.02em',
              color: '#38BDF8', lineHeight: 1,
            }}>
              Catalyst
            </div>
            <div style={{
              fontSize: 11, color: 'rgba(255,255,255,0.2)',
              marginTop: 3, lineHeight: 1,
            }}>
              Bootstrap Wizard
            </div>
          </div>
        </div>

        {/* ── Step list ────────────────────────────────────────── */}
        <nav aria-label="Wizard progress" style={{ position: 'relative', zIndex: 1, flex: 1 }}>
          {WIZARD_STEPS.map((step, idx) => {
            const isCompleted = completedSteps.includes(step.id)
            const isCurrent = currentStep === step.id
            const isClickable = isCompleted

            return (
              <div key={step.id} style={{ display: 'flex', alignItems: 'flex-start', gap: 14 }}>
                {/* Dot + connector column */}
                <div style={{ display: 'flex', flexDirection: 'column', alignItems: 'center', flexShrink: 0 }}>
                  <button
                    onClick={() => isClickable && setStep(step.id)}
                    disabled={!isClickable}
                    aria-current={isCurrent ? 'step' : undefined}
                    style={{
                      width: 28, height: 28,
                      borderRadius: '50%',
                      border: isCompleted
                        ? 'none'
                        : isCurrent
                          ? '2px solid #38BDF8'
                          : '1.5px solid rgba(255,255,255,0.12)',
                      background: isCompleted
                        ? 'linear-gradient(135deg, #38BDF8, #818CF8)'
                        : isCurrent
                          ? 'rgba(56,189,248,0.08)'
                          : 'rgba(255,255,255,0.03)',
                      color: isCompleted ? '#fff' : isCurrent ? '#38BDF8' : 'rgba(255,255,255,0.2)',
                      fontSize: 11, fontWeight: 600,
                      cursor: isClickable ? 'pointer' : 'default',
                      display: 'flex', alignItems: 'center', justifyContent: 'center',
                      transition: 'all 0.2s',
                      flexShrink: 0,
                      boxShadow: isCurrent ? '0 0 0 3px rgba(56,189,248,0.12)' : 'none',
                      position: 'relative',
                    }}
                  >
                    {isCompleted
                      ? <Check size={13} strokeWidth={2.5} />
                      : <span>{step.id}</span>
                    }
                  </button>

                  {/* Connector line */}
                  {idx < WIZARD_STEPS.length - 1 && (
                    <div style={{
                      width: 1, height: 32, marginTop: 2,
                      background: isCompleted
                        ? 'linear-gradient(to bottom, rgba(56,189,248,0.4), rgba(56,189,248,0.1))'
                        : 'rgba(255,255,255,0.07)',
                      transition: 'background 0.3s',
                    }} />
                  )}
                </div>

                {/* Label */}
                <div style={{ paddingTop: 4, paddingBottom: idx < WIZARD_STEPS.length - 1 ? 0 : 0, minHeight: 28 + 32 }}>
                  <div style={{
                    fontSize: 13, fontWeight: isCurrent ? 600 : 400,
                    color: isCompleted
                      ? 'rgba(255,255,255,0.5)'
                      : isCurrent
                        ? 'rgba(255,255,255,0.9)'
                        : 'rgba(255,255,255,0.2)',
                    lineHeight: 1.3,
                    transition: 'color 0.2s',
                  }}>
                    {step.label}
                  </div>
                  {isCurrent && (
                    <div style={{ fontSize: 11, color: 'rgba(56,189,248,0.6)', marginTop: 2, lineHeight: 1 }}>
                      {step.description}
                    </div>
                  )}
                </div>
              </div>
            )
          })}
        </nav>

        {/* ── Trust signals ────────────────────────────────────── */}
        <div style={{
          position: 'relative', zIndex: 1,
          borderTop: '1px solid rgba(255,255,255,0.06)',
          paddingTop: '1.5rem', marginTop: '1.5rem',
          display: 'flex', flexDirection: 'column', gap: 10,
        }}>
          {[
            { icon: Lock,    text: 'Runs only in your cloud account' },
            { icon: Shield,  text: 'Credentials never leave your browser' },
            { icon: Code2,   text: 'Fully open source — audit everything' },
          ].map(({ icon: Icon, text }) => (
            <div key={text} style={{ display: 'flex', alignItems: 'center', gap: 10 }}>
              <Icon size={13} style={{ color: 'rgba(56,189,248,0.4)', flexShrink: 0 }} />
              <span style={{ fontSize: 11, color: 'rgba(255,255,255,0.22)', lineHeight: 1.4 }}>{text}</span>
            </div>
          ))}
        </div>
      </aside>

      {/* ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ */}
      {/* RIGHT PANEL — themed, form area                            */}
      {/* ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━ */}
      <main style={{
        flex: 1,
        display: 'flex',
        flexDirection: 'column',
        backgroundColor: 'var(--color-surface-0)',
        position: 'relative',
      }}>

        {/* Top-right controls — float above content */}
        <div style={{
          position: 'absolute', top: 20, right: 24,
          display: 'flex', alignItems: 'center', gap: 6, zIndex: 20,
        }}>
          {/* Mobile logo */}
          <div className="md:hidden" style={{ display: 'flex', alignItems: 'center', gap: 8, marginRight: 8 }}>
            <OOLogo size={24} />
            <span style={{ fontSize: 14, fontWeight: 700, color: '#38BDF8' }}>Catalyst</span>
          </div>

          <button
            onClick={toggle}
            aria-label={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
            style={{
              width: 32, height: 32, borderRadius: 8,
              border: '1px solid var(--color-surface-border)',
              background: 'var(--color-surface-2)',
              color: 'var(--color-text-muted)',
              display: 'flex', alignItems: 'center', justifyContent: 'center',
              cursor: 'pointer', transition: 'all 0.15s',
            }}
          >
            {theme === 'dark' ? <Sun size={14} /> : <Moon size={14} />}
          </button>

          <Link to={IS_SAAS ? '/app/dashboard' : '/'}>
            <button
              aria-label="Exit wizard"
              style={{
                width: 32, height: 32, borderRadius: 8,
                border: '1px solid var(--color-surface-border)',
                background: 'var(--color-surface-2)',
                color: 'var(--color-text-muted)',
                display: 'flex', alignItems: 'center', justifyContent: 'center',
                cursor: 'pointer', transition: 'all 0.15s',
              }}
            >
              <X size={14} />
            </button>
          </Link>
        </div>

        {/* Progress bar — across full top of right panel */}
        <div style={{
          position: 'absolute', top: 0, left: 0, right: 0,
          height: 2,
          background: 'var(--color-surface-border)',
        }}>
          <div style={{
            height: '100%',
            width: `${progress}%`,
            background: 'linear-gradient(to right, #38BDF8, #818CF8)',
            transition: 'width 0.5s cubic-bezier(0.4, 0, 0.2, 1)',
          }} />
        </div>

        {/* Scrollable form area — centered content */}
        <div style={{
          flex: 1,
          overflowY: 'auto',
          display: 'flex',
          flexDirection: 'column',
          alignItems: 'center',
          paddingTop: '6rem',
          paddingBottom: '4rem',
          paddingLeft: '1.5rem',
          paddingRight: '1.5rem',
        }}>
          {/* Step counter */}
          <div style={{
            width: '100%', maxWidth: 520,
            marginBottom: '0.5rem',
            display: 'flex', alignItems: 'center', gap: 10,
          }}>
            <span style={{
              fontSize: 11, fontWeight: 600,
              textTransform: 'uppercase', letterSpacing: '0.1em',
              color: 'var(--color-brand-500)',
            }}>
              Step {currentStep} of {totalSteps}
            </span>
            <div style={{
              flex: 1, height: 1,
              background: 'var(--color-surface-border)',
            }} />
          </div>

          {/* Step content */}
          <div style={{ width: '100%', maxWidth: 520 }}>
            <Outlet />
          </div>
        </div>
      </main>
    </div>
  )
}
