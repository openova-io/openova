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

  const progressPct = Math.round(((currentStep - 1) / WIZARD_STEPS.length) * 100)

  return (
    <div style={{
      position: 'fixed', inset: 0,
      display: 'flex',
      flexDirection: isMobile ? 'column' : 'row',
      background: 'var(--wiz-page-bg)',
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
          background: 'rgba(var(--wiz-ch), 0.025)',
          backdropFilter: 'blur(20px)', WebkitBackdropFilter: 'blur(20px)',
          borderBottom: '1px solid var(--wiz-border-sub)',
          display: 'flex', alignItems: 'center', padding: '0 16px', gap: 14,
        }}>
          <OOLogo h={20} id="wiz-logo-m" />
          <div style={{ flex: 1, minWidth: 0 }}>
            <div style={{ fontSize: 10, color: 'var(--wiz-text-sub)', letterSpacing: '0.06em' }}>
              STEP {currentStep} OF {WIZARD_STEPS.length}
            </div>
            <div style={{ fontSize: 13, fontWeight: 600, color: 'var(--wiz-text-hi)', lineHeight: 1.2, whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
              {WIZARD_STEPS[currentStep - 1].label}
            </div>
          </div>
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
                    : s.id === currentStep ? '#38BDF8' : 'var(--wiz-border)',
                  cursor: s.id < currentStep ? 'pointer' : 'default',
                  transition: 'all 0.3s',
                }}
              />
            ))}
          </div>
        </div>
      )}

      {/* ── TABLET: collapsed icon-only rail (52 px) ──────────────────── */}
      {isTablet && (
        <div style={{
          width: 52, flexShrink: 0, zIndex: 10,
          /* NO distinct bg, NO border — rail belongs to the page */
          display: 'flex', flexDirection: 'column', alignItems: 'center',
          padding: '20px 0 16px',
        }}>
          <OOLogo h={18} id="wiz-logo-t" />
          <div style={{ width: 1, height: 16, background: 'var(--wiz-border-sub)', margin: '8px 0' }} />

          {/* Step column — justify-content: space-between spreads circles over full height */}
          <div style={{
            flex: 1,
            display: 'flex', flexDirection: 'column',
            justifyContent: 'space-between',
            alignItems: 'center',
            paddingBottom: 20,
          }}>
            {WIZARD_STEPS.map((step, i) => {
              const done    = step.id < currentStep
              const current = step.id === currentStep
              const isLast  = i === WIZARD_STEPS.length - 1
              return (
                <div key={step.id} style={{ position: 'relative', display: 'flex', flexDirection: 'column', alignItems: 'center', flex: isLast ? 'none' : 1 }}>
                  {/* Circle */}
                  <div
                    onClick={() => done && setStep(step.id)}
                    title={step.label}
                    style={{
                      width: 28, height: 28, borderRadius: '50%',
                      display: 'flex', alignItems: 'center', justifyContent: 'center',
                      fontSize: 11, fontWeight: 700,
                      background: done
                        ? 'linear-gradient(135deg, #38BDF8, #818CF8)'
                        : current ? 'rgba(56,189,248,0.15)' : 'transparent',
                      border: current
                        ? '2px solid #38BDF8'
                        : done ? 'none' : '1.5px solid var(--wiz-border)',
                      color: done ? '#fff' : current ? '#38BDF8' : 'var(--wiz-text-hint)',
                      boxShadow: current ? '0 0 0 4px rgba(56,189,248,0.15)' : 'none',
                      cursor: done ? 'pointer' : 'default',
                      transition: 'all 0.25s',
                      zIndex: 2,
                    }}
                  >
                    {done ? <Check size={11} strokeWidth={2.5} /> : step.id}
                  </div>

                  {/* Rail below (except on last step) */}
                  {!isLast && (
                    <div style={{
                      flex: 1, width: 1.5, minHeight: 24,
                      background: done
                        ? 'linear-gradient(180deg, rgba(56,189,248,0.6), rgba(129,140,248,0.35))'
                        : current
                          ? 'linear-gradient(180deg, rgba(56,189,248,0.5), var(--wiz-border-sub))'
                          : 'var(--wiz-border-sub)',
                      backgroundImage: done || current ? undefined : 'repeating-linear-gradient(180deg, var(--wiz-border-sub) 0px, var(--wiz-border-sub) 3px, transparent 3px, transparent 6px)',
                      backgroundSize: done || current ? undefined : '1.5px 6px',
                      marginTop: 4, marginBottom: 4,
                    }} />
                  )}
                </div>
              )
            })}
          </div>

          <div style={{ fontSize: 10, color: 'var(--wiz-text-sub)', fontWeight: 600, letterSpacing: '0.04em' }}>{progressPct}%</div>
        </div>
      )}

      {/* ── DESKTOP: full 260px rail (no background, no border) ───────── */}
      {isDesktop && (
        <div style={{
          width: 260, flexShrink: 0, zIndex: 10,
          /* NO bg, NO border — integrated into page */
          display: 'flex', flexDirection: 'column',
          padding: '28px 24px',
        }}>
          {/* Logo */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, marginBottom: 32 }}>
            <OOLogo h={24} id="wiz-logo-d" />
            <div style={{ lineHeight: 1 }}>
              <div style={{ fontSize: 13, fontWeight: 700, color: 'var(--wiz-text-hi)', letterSpacing: '-0.01em' }}>OpenOva</div>
              <div style={{ fontSize: 9, fontWeight: 600, color: 'var(--wiz-text-sub)', letterSpacing: '0.18em', textTransform: 'uppercase', marginTop: 2 }}>Corporate</div>
            </div>
          </div>

          {/* Vertical timeline — circles + growing rails, space-between so it fills the height */}
          <div style={{
            flex: 1,
            display: 'flex', flexDirection: 'column',
            minHeight: 0,
          }}>
            {WIZARD_STEPS.map((step, i) => {
              const done    = step.id < currentStep
              const current = step.id === currentStep
              const isLast  = i === WIZARD_STEPS.length - 1

              return (
                <div key={step.id} style={{
                  display: 'flex',
                  flexDirection: 'column',
                  flex: isLast ? 'none' : 1,
                  minHeight: isLast ? 'auto' : 56,
                }}>
                  {/* Row: circle + label + description */}
                  <div
                    onClick={() => done && setStep(step.id)}
                    style={{
                      display: 'flex', alignItems: 'center', gap: 14,
                      padding: '6px 0',
                      cursor: done ? 'pointer' : 'default',
                      position: 'relative',
                    }}
                  >
                    {/* Circle */}
                    <div style={{
                      width: 28, height: 28, borderRadius: '50%', flexShrink: 0,
                      display: 'flex', alignItems: 'center', justifyContent: 'center',
                      fontSize: 11, fontWeight: 700,
                      background: done
                        ? 'linear-gradient(135deg, #38BDF8, #818CF8)'
                        : current ? 'rgba(56,189,248,0.15)' : 'transparent',
                      border: current
                        ? '2px solid #38BDF8'
                        : done ? 'none' : '1.5px solid var(--wiz-border)',
                      color: done ? '#fff' : current ? '#38BDF8' : 'var(--wiz-text-hint)',
                      boxShadow: current ? '0 0 0 4px rgba(56,189,248,0.15)' : 'none',
                      transition: 'all 0.25s',
                      zIndex: 2,
                      position: 'relative',
                    }}>
                      {done ? <Check size={12} strokeWidth={2.5} /> : step.id}

                      {/* Pulse ring for current step */}
                      {current && (
                        <span
                          aria-hidden
                          style={{
                            position: 'absolute', inset: -4,
                            borderRadius: '50%',
                            border: '1.5px solid rgba(56,189,248,0.5)',
                            animation: 'wiz-step-pulse 2.2s ease-in-out infinite',
                            pointerEvents: 'none',
                          }}
                        />
                      )}
                    </div>

                    {/* Label + description */}
                    <div style={{ flex: 1, minWidth: 0 }}>
                      <div style={{
                        fontSize: 13, fontWeight: current ? 600 : 500,
                        color: current
                          ? 'var(--wiz-text-hi)'
                          : done ? 'var(--wiz-text-md)' : 'var(--wiz-text-sub)',
                        lineHeight: 1.2,
                        transition: 'color 0.25s',
                      }}>
                        {step.label}
                      </div>
                      <div style={{
                        fontSize: 11,
                        color: current ? 'var(--wiz-text-lo)' : 'var(--wiz-text-sub)',
                        marginTop: 2,
                        transition: 'color 0.25s',
                      }}>
                        {step.desc}
                      </div>
                    </div>
                  </div>

                  {/* Growing rail — only not on last */}
                  {!isLast && (
                    <div style={{
                      marginLeft: 13.25,  /* align under circle centre (14 - rail_w/2) */
                      width: 1.5,
                      flex: 1,
                      minHeight: 16,
                      background: done
                        ? 'linear-gradient(180deg, rgba(56,189,248,0.5), rgba(129,140,248,0.3))'
                        : current
                          ? 'linear-gradient(180deg, rgba(56,189,248,0.45), var(--wiz-border-sub) 70%)'
                          : 'transparent',
                      backgroundImage: done || current ? undefined : 'repeating-linear-gradient(180deg, var(--wiz-border-sub) 0px, var(--wiz-border-sub) 3px, transparent 3px, transparent 7px)',
                      backgroundSize: done || current ? undefined : '1.5px 7px',
                      transition: 'background 0.3s',
                    }} />
                  )}
                </div>
              )
            })}
          </div>

          {/* Progress — integrated at the end of the rail, no hard divider */}
          <div style={{ marginTop: 12, paddingTop: 14 }}>
            <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 8 }}>
              <span style={{ fontSize: 11, color: 'var(--wiz-text-sub)', fontWeight: 500 }}>Progress</span>
              <span style={{ fontSize: 11, fontWeight: 700, color: '#38BDF8' }}>
                {progressPct}%
              </span>
            </div>
            <div style={{ height: 3, borderRadius: 2, background: 'var(--wiz-border-sub)' }}>
              <div style={{
                height: '100%',
                width: `${progressPct}%`,
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
        padding: isMobile ? '20px 16px 40px' : isTablet ? '28px 28px 48px' : '36px 40px 56px',
        zIndex: 1,
      }}>
        <div style={{ width: '100%', maxWidth: isDesktop ? 960 : '100%', margin: '0 auto' }}>
          <Outlet />
        </div>
      </div>

      {/* ── TOP-RIGHT CONTROLS ───────────────────────────────────────── */}
      <div style={{ position: 'absolute', top: isMobile ? 12 : 18, right: 16, display: 'flex', gap: 8, zIndex: 20, alignItems: 'center' }}>
        <button
          onClick={toggle}
          aria-label="Toggle theme"
          style={{ width: 30, height: 30, borderRadius: 8, background: 'var(--wiz-border-sub)', border: '1px solid var(--wiz-border)', color: 'var(--wiz-text-sub)', display: 'flex', alignItems: 'center', justifyContent: 'center', cursor: 'pointer' }}
        >
          {theme === 'dark' ? <Sun size={13} /> : <Moon size={13} />}
        </button>
        <Link to={IS_SAAS ? '/app/dashboard' : '/'}>
          <button
            aria-label="Exit wizard"
            style={{ width: 30, height: 30, borderRadius: 8, background: 'var(--wiz-border-sub)', border: '1px solid var(--wiz-border)', color: 'var(--wiz-text-sub)', display: 'flex', alignItems: 'center', justifyContent: 'center', cursor: 'pointer' }}
          >
            <X size={13} />
          </button>
        </Link>
      </div>

      {/* Pulse animation — injected once at layout level */}
      <style>{`
        @keyframes wiz-step-pulse {
          0%, 100% { opacity: 0.4; transform: scale(1); }
          50%      { opacity: 0;   transform: scale(1.35); }
        }
      `}</style>
    </div>
  )
}
