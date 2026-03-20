import { Outlet, Link } from '@tanstack/react-router'
import { X, Sun, Moon, Check } from 'lucide-react'
import { IS_SAAS } from '@/shared/constants/env'
import { useWizardStore } from '@/entities/deployment/store'
import { useTheme } from '@/shared/lib/useTheme'
import { useBreakpoint } from '@/shared/lib/useBreakpoint'
import { OOLogo } from '@/shared/ui/OOLogo'

export const WIZARD_STEPS = [
  { id: 1, label: 'Organisation', desc: 'Name, domain, contact'    },
  { id: 2, label: 'Topology',     desc: 'Regions and clusters'      },
  { id: 3, label: 'Provider',     desc: 'Cloud provider per region' },
  { id: 4, label: 'Credentials',  desc: 'API access tokens'         },
  { id: 5, label: 'Components',   desc: 'Platform building blocks'  },
  { id: 6, label: 'Review',       desc: 'Confirm and provision'     },
]

export function WizardLayout() {
  const { currentStep, setStep } = useWizardStore()
  const { theme, toggle } = useTheme()
  const bp = useBreakpoint()

  const isMobile  = bp === 'mobile'
  const isTablet  = bp === 'tablet'
  const isDesktop = bp === 'desktop'

  return (
    <div style={{
      position: 'fixed', inset: 0,
      display: 'flex',
      flexDirection: isMobile ? 'column' : 'row',
      background: 'radial-gradient(ellipse at 30% 20%, #0c1e40 0%, #06080f 70%)',
      fontFamily: 'Inter, sans-serif',
      overflow: 'hidden',
    }}>
      {/* Ambient glows */}
      <div style={{ position: 'absolute', top: '-10%', left: '25%', width: 600, height: 600, borderRadius: '50%', background: 'radial-gradient(circle, rgba(56,189,248,0.06) 0%, transparent 65%)', pointerEvents: 'none', zIndex: 0 }} />
      <div style={{ position: 'absolute', bottom: '-10%', right: '5%',  width: 400, height: 400, borderRadius: '50%', background: 'radial-gradient(circle, rgba(129,140,248,0.05) 0%, transparent 65%)', pointerEvents: 'none', zIndex: 0 }} />

      {/* ── MOBILE: top bar with progress pill ───────────────────────── */}
      {isMobile && (
        <div style={{
          flexShrink: 0, height: 56, zIndex: 10,
          background: 'rgba(255,255,255,0.03)',
          backdropFilter: 'blur(20px)', WebkitBackdropFilter: 'blur(20px)',
          borderBottom: '1px solid rgba(255,255,255,0.07)',
          display: 'flex', alignItems: 'center', padding: '0 16px', gap: 14,
        }}>
          <OOLogo h={20} id="wiz-logo-m" />
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ fontSize: 10, color: 'rgba(255,255,255,0.28)', letterSpacing: '0.06em' }}>
              STEP {currentStep} OF {WIZARD_STEPS.length}
            </div>
            <div style={{ fontSize: 13, fontWeight: 600, color: 'rgba(255,255,255,0.85)', lineHeight: 1.2, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {WIZARD_STEPS[currentStep - 1].label}
            </div>
          </div>
          {/* Step dots */}
          <div style={{ display: 'flex', gap: 4, flexShrink: 0 }}>
            {WIZARD_STEPS.map(s => (
              <div
                key={s.id}
                onClick={() => s.id < currentStep && setStep(s.id)}
                style={{
                  height: 6, borderRadius: 3,
                  width: s.id === currentStep ? 18 : 6,
                  background: s.id < currentStep
                    ? 'linear-gradient(90deg, #38BDF8, #818CF8)'
                    : s.id === currentStep ? '#38BDF8' : 'rgba(255,255,255,0.12)',
                  cursor: s.id < currentStep ? 'pointer' : 'default',
                  transition: 'all 0.3s',
                }}
              />
            ))}
          </div>
        </div>
      )}

      {/* ── TABLET: collapsed icon-only sidebar (52 px) ───────────────── */}
      {isTablet && (
        <div style={{
          width: 52, flexShrink: 0, zIndex: 10,
          background: 'rgba(255,255,255,0.025)',
          backdropFilter: 'blur(20px)', WebkitBackdropFilter: 'blur(20px)',
          borderRight: '1px solid rgba(255,255,255,0.07)',
          display: 'flex', flexDirection: 'column', alignItems: 'center',
          padding: '20px 0 16px',
          gap: 4,
        }}>
          <OOLogo h={18} id="wiz-logo-t" />
          <div style={{ width: 1, height: 16, background: 'rgba(255,255,255,0.07)', margin: '8px 0' }} />
          {WIZARD_STEPS.map(step => {
            const done    = step.id < currentStep
            const current = step.id === currentStep
            return (
              <div
                key={step.id}
                onClick={() => done && setStep(step.id)}
                title={step.label}
                style={{
                  width: 32, height: 32, borderRadius: '50%',
                  display: 'flex', alignItems: 'center', justifyContent: 'center',
                  fontSize: 11, fontWeight: 700,
                  background: done
                    ? 'linear-gradient(135deg, #38BDF8, #818CF8)'
                    : current ? 'rgba(56,189,248,0.15)' : 'rgba(255,255,255,0.05)',
                  border: current
                    ? '2px solid #38BDF8'
                    : done ? 'none' : '1.5px solid rgba(255,255,255,0.1)',
                  color: done ? '#fff' : current ? '#38BDF8' : 'rgba(255,255,255,0.25)',
                  boxShadow: current ? '0 0 0 3px rgba(56,189,248,0.12)' : 'none',
                  cursor: done ? 'pointer' : 'default',
                  transition: 'all 0.25s',
                }}
              >
                {done ? <Check size={11} strokeWidth={2.5} /> : step.id}
              </div>
            )
          })}
          {/* Progress bar at bottom */}
          <div style={{ flex: 1 }} />
          <div style={{ width: 3, height: 60, borderRadius: 2, background: 'rgba(255,255,255,0.08)', overflow: 'hidden', margin: '0 0 8px' }}>
            <div style={{
              width: '100%',
              height: `${((currentStep - 1) / WIZARD_STEPS.length) * 100}%`,
              background: 'linear-gradient(180deg, #38BDF8, #818CF8)',
              transition: 'height 0.4s',
            }} />
          </div>
        </div>
      )}

      {/* ── DESKTOP: full 260px sidebar ───────────────────────────────── */}
      {isDesktop && (
        <div style={{
          width: 260, flexShrink: 0, zIndex: 10,
          background: 'rgba(255,255,255,0.025)',
          backdropFilter: 'blur(20px)', WebkitBackdropFilter: 'blur(20px)',
          borderRight: '1px solid rgba(255,255,255,0.07)',
          display: 'flex', flexDirection: 'column',
          padding: '28px 20px',
        }}>
          {/* Logo */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 36 }}>
            <OOLogo h={24} id="wiz-logo-d" />
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
      )}

      {/* ── SCROLLABLE CONTENT ────────────────────────────────────────── */}
      <div id="wizard-body" style={{
        flex: 1, overflowY: 'auto',
        display: 'flex', flexDirection: 'column',
        padding: isMobile ? '20px 16px 40px' : isTablet ? '28px 28px 48px' : '36px 48px 56px',
        zIndex: 1,
      }}>
        <div style={{ width: '100%', maxWidth: isDesktop ? 960 : '100%', margin: '0 auto' }}>
          <Outlet />
        </div>
      </div>

      {/* ── TOP-RIGHT CONTROLS ───────────────────────────────────────── */}
      <div style={{ position: 'absolute', top: isMobile ? 12 : 18, right: 16, display: 'flex', gap: 8, zIndex: 20 }}>
        <button
          onClick={toggle}
          aria-label="Toggle theme"
          style={{ width: 30, height: 30, borderRadius: 8, background: 'rgba(255,255,255,0.06)', border: '1px solid rgba(255,255,255,0.1)', color: 'rgba(255,255,255,0.4)', display: 'flex', alignItems: 'center', justifyContent: 'center', cursor: 'pointer' }}
        >
          {theme === 'dark' ? <Sun size={13} /> : <Moon size={13} />}
        </button>
        <Link to={IS_SAAS ? '/app/dashboard' : '/'}>
          <button
            aria-label="Exit wizard"
            style={{ width: 30, height: 30, borderRadius: 8, background: 'rgba(255,255,255,0.06)', border: '1px solid rgba(255,255,255,0.1)', color: 'rgba(255,255,255,0.4)', display: 'flex', alignItems: 'center', justifyContent: 'center', cursor: 'pointer' }}
          >
            <X size={13} />
          </button>
        </Link>
      </div>
    </div>
  )
}
