import { Outlet, Link } from '@tanstack/react-router'
import { X, Sun, Moon, Check } from 'lucide-react'
import { IS_SAAS } from '@/shared/constants/env'
import { useWizardStore } from '@/entities/deployment/store'
import { useTheme } from '@/shared/lib/useTheme'
import { OOLogo } from '@/shared/ui/OOLogo'

export const WIZARD_STEPS = [
  { id: 1, label: 'Organisation', desc: 'Name, domain, contact'        },
  { id: 2, label: 'Topology',     desc: 'Regions and clusters'          },
  { id: 3, label: 'Provider',     desc: 'Cloud provider per region'     },
  { id: 4, label: 'Credentials',  desc: 'API access tokens'             },
  { id: 5, label: 'Components',   desc: 'Platform building blocks'      },
  { id: 6, label: 'Review',       desc: 'Confirm and provision'         },
]

export function WizardLayout() {
  const { currentStep, setStep } = useWizardStore()
  const { theme, toggle } = useTheme()

  return (
    /*
     * Atlas 2-pane layout:
     * Left sidebar (260 px) — logo + vertical step rail + progress bar
     * Right content (flex 1) — scrollable, full-width outlet
     */
    <div style={{
      position: 'fixed', inset: 0,
      display: 'flex', flexDirection: 'row',
      background: 'radial-gradient(ellipse at 30% 20%, #0c1e40 0%, #06080f 70%)',
      fontFamily: 'Inter, sans-serif',
      overflow: 'hidden',
    }}>
      {/* ── Ambient glows ─────────────────────────────────────────────── */}
      <div style={{ position: 'absolute', top: '-10%', left: '25%', width: 600, height: 600, borderRadius: '50%', background: 'radial-gradient(circle, rgba(56,189,248,0.06) 0%, transparent 65%)', pointerEvents: 'none', zIndex: 0 }} />
      <div style={{ position: 'absolute', bottom: '-10%', right: '5%',  width: 400, height: 400, borderRadius: '50%', background: 'radial-gradient(circle, rgba(129,140,248,0.05) 0%, transparent 65%)', pointerEvents: 'none', zIndex: 0 }} />

      {/* ── LEFT SIDEBAR ─────────────────────────────────────────────── */}
      <div style={{
        width: 260, flexShrink: 0,
        background: 'rgba(255,255,255,0.025)',
        backdropFilter: 'blur(20px)', WebkitBackdropFilter: 'blur(20px)',
        borderRight: '1px solid rgba(255,255,255,0.07)',
        display: 'flex', flexDirection: 'column',
        padding: '28px 20px',
        zIndex: 10,
      }}>
        {/* Logo + wordmark */}
        <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 36 }}>
          <OOLogo h={24} id="wiz-logo" />
          <div style={{ lineHeight: 1 }}>
            <div style={{ fontSize: 13, fontWeight: 700, color: 'rgba(255,255,255,0.82)', letterSpacing: '-0.01em' }}>OpenOva</div>
            <div style={{ fontSize: 9, fontWeight: 600, color: 'rgba(255,255,255,0.28)', letterSpacing: '0.18em', textTransform: 'uppercase', marginTop: 2 }}>Catalyst</div>
          </div>
        </div>

        {/* Vertical step list */}
        <div style={{ display: 'flex', flexDirection: 'column', gap: 2, flex: 1 }}>
          {WIZARD_STEPS.map((step, i) => {
            const done    = step.id < currentStep
            const current = step.id === currentStep
            return (
              <div key={step.id} style={{ position: 'relative' }}>
                {/* Connector line */}
                {i < WIZARD_STEPS.length - 1 && (
                  <div style={{
                    position: 'absolute', left: 19, top: 38,
                    width: 1.5, height: 10,
                    background: done ? 'rgba(56,189,248,0.4)' : 'rgba(255,255,255,0.07)',
                  }} />
                )}
                <div
                  onClick={() => done && setStep(step.id)}
                  style={{
                    display: 'flex', alignItems: 'center', gap: 12,
                    padding: '9px 12px', borderRadius: 10,
                    cursor: done ? 'pointer' : 'default',
                    background: current ? 'rgba(56,189,248,0.08)' : 'transparent',
                    border: current ? '1px solid rgba(56,189,248,0.2)' : '1px solid transparent',
                    transition: 'all 0.15s',
                  }}
                >
                  <div style={{
                    width: 26, height: 26, borderRadius: '50%', flexShrink: 0,
                    display: 'flex', alignItems: 'center', justifyContent: 'center',
                    fontSize: 10, fontWeight: 700,
                    background: done
                      ? 'linear-gradient(135deg, #38BDF8, #818CF8)'
                      : current ? 'rgba(56,189,248,0.15)' : 'rgba(255,255,255,0.05)',
                    border: current
                      ? '2px solid #38BDF8'
                      : done ? 'none' : '1.5px solid rgba(255,255,255,0.1)',
                    color: done ? '#fff' : current ? '#38BDF8' : 'rgba(255,255,255,0.2)',
                    boxShadow: current ? '0 0 0 3px rgba(56,189,248,0.12)' : 'none',
                    transition: 'all 0.25s',
                  }}>
                    {done ? <Check size={11} strokeWidth={2.5} /> : step.id}
                  </div>
                  <div style={{ flex: 1, minWidth: 0 }}>
                    <div style={{
                      fontSize: 12, fontWeight: current ? 600 : 400,
                      color: current
                        ? 'rgba(255,255,255,0.85)'
                        : done ? 'rgba(255,255,255,0.45)' : 'rgba(255,255,255,0.2)',
                      lineHeight: 1.3,
                    }}>
                      {step.label}
                    </div>
                    <div style={{ fontSize: 10, color: 'rgba(255,255,255,0.2)', marginTop: 1 }}>
                      {step.desc}
                    </div>
                  </div>
                </div>
              </div>
            )
          })}
        </div>

        {/* Progress footer */}
        <div style={{ borderTop: '1px solid rgba(255,255,255,0.06)', paddingTop: 18, marginTop: 18 }}>
          <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 8 }}>
            <span style={{ fontSize: 11, color: 'rgba(255,255,255,0.25)' }}>Progress</span>
            <span style={{ fontSize: 11, fontWeight: 600, color: '#38BDF8' }}>
              {Math.round(((currentStep - 1) / WIZARD_STEPS.length) * 100)}%
            </span>
          </div>
          <div style={{ height: 3, borderRadius: 2, background: 'rgba(255,255,255,0.08)' }}>
            <div style={{
              height: '100%',
              width: `${((currentStep - 1) / WIZARD_STEPS.length) * 100}%`,
              borderRadius: 2,
              background: 'linear-gradient(90deg, #38BDF8, #818CF8)',
              transition: 'width 0.4s',
            }} />
          </div>
        </div>
      </div>

      {/* ── RIGHT SCROLLABLE CONTENT ─────────────────────────────────── */}
      <div id="wizard-body" style={{
        flex: 1, overflowY: 'auto',
        display: 'flex', flexDirection: 'column',
        padding: '36px 48px 56px',
        zIndex: 1,
      }}>
        {/* Cap at 960 px so content never stretches on ultra-wide */}
        <div style={{ width: '100%', maxWidth: 960, margin: '0 auto' }}>
          <Outlet />
        </div>
      </div>

      {/* ── TOP-RIGHT CONTROLS ───────────────────────────────────────── */}
      <div style={{ position: 'absolute', top: 18, right: 20, display: 'flex', gap: 8, zIndex: 20 }}>
        <button
          onClick={toggle}
          aria-label={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
          style={{ width: 32, height: 32, borderRadius: 8, background: 'rgba(255,255,255,0.06)', border: '1px solid rgba(255,255,255,0.1)', color: 'rgba(255,255,255,0.4)', display: 'flex', alignItems: 'center', justifyContent: 'center', cursor: 'pointer' }}
        >
          {theme === 'dark' ? <Sun size={14} /> : <Moon size={14} />}
        </button>
        <Link to={IS_SAAS ? '/app/dashboard' : '/'}>
          <button
            aria-label="Exit wizard"
            style={{ width: 32, height: 32, borderRadius: 8, background: 'rgba(255,255,255,0.06)', border: '1px solid rgba(255,255,255,0.1)', color: 'rgba(255,255,255,0.4)', display: 'flex', alignItems: 'center', justifyContent: 'center', cursor: 'pointer' }}
          >
            <X size={14} />
          </button>
        </Link>
      </div>
    </div>
  )
}
