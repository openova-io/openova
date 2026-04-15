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

      {/* ── TABLET: icon-only rail, balls-only (no bg, right-aligned) ─── */}
      {isTablet && (
        <div style={{
          width: 52, flexShrink: 0, zIndex: 10,
          /* NO bg, NO border — fully transparent */
          display: 'flex', flexDirection: 'column', alignItems: 'center',
          padding: '20px 12px 16px',
        }}>
          <OOLogo h={18} id="wiz-logo-t" />
          <div style={{ height: 28 }} />

          {/* Balls only — fixed 18 px gap between them */}
          <div style={{ display: 'flex', flexDirection: 'column', gap: 18, alignItems: 'center' }}>
            {WIZARD_STEPS.map((step, i) => {
              const done    = step.id < currentStep
              const current = step.id === currentStep
              const prevFilled = step.id - 1 < currentStep
              return (
                <div key={step.id} style={{ position: 'relative' }}>
                  {/* Rail above (except first) */}
                  {i > 0 && (
                    <div style={{
                      position: 'absolute',
                      top: -18, left: '50%', transform: 'translateX(-50%)',
                      width: 2, height: 18,
                      borderRadius: 1,
                      background: prevFilled
                        ? 'linear-gradient(180deg, rgba(56,189,248,0.7), rgba(129,140,248,0.5))'
                        : 'rgba(var(--wiz-ch), 0.2)',
                      transition: 'background 0.3s',
                    }} />
                  )}

                  {/* Ball */}
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
                      position: 'relative',
                    }}
                  >
                    {done ? <Check size={11} strokeWidth={2.5} /> : step.id}
                  </div>
                </div>
              )
            })}
          </div>

          <div style={{ flex: 1 }} />
          <div style={{ fontSize: 10, color: 'var(--wiz-text-sub)', fontWeight: 600 }}>{progressPct}%</div>
        </div>
      )}

      {/* ── DESKTOP: transparent column, right-aligned stepper, labels-left ── */}
      {isDesktop && (
        <div style={{
          width: 200, flexShrink: 0, zIndex: 10,
          /* NO bg, NO border — fully transparent */
          display: 'flex', flexDirection: 'column',
          /* Zero right padding — balls sit flush against main content */
          padding: '28px 0 28px 20px',
        }}>
          {/* Logo — right-aligned to match stepper direction */}
          <div style={{ display: 'flex', alignItems: 'center', gap: 10, justifyContent: 'flex-end', marginBottom: 44, paddingRight: 6 }}>
            <OOLogo h={22} id="wiz-logo-d" />
            <div style={{ lineHeight: 1, textAlign: 'right' }}>
              <div style={{ fontSize: 12, fontWeight: 700, color: 'var(--wiz-text-hi)', letterSpacing: '-0.01em' }}>OpenOva</div>
              <div style={{ fontSize: 8, fontWeight: 600, color: 'var(--wiz-text-sub)', letterSpacing: '0.18em', textTransform: 'uppercase', marginTop: 2 }}>Corporate</div>
            </div>
          </div>

          {/* Stepper — fixed 22 px gap (no stretch), right-aligned so balls
              sit flush against the content card. Labels render LEFT of balls. */}
          <div style={{
            display: 'flex', flexDirection: 'column',
            gap: 22,
            alignItems: 'flex-end',
          }}>
            {WIZARD_STEPS.map((step, i) => {
              const done       = step.id < currentStep
              const current    = step.id === currentStep
              const prevFilled = step.id - 1 < currentStep
              const clickable  = done

              return (
                <div
                  key={step.id}
                  onClick={() => clickable && setStep(step.id)}
                  style={{
                    position: 'relative',
                    display: 'flex', alignItems: 'center', gap: 12,
                    cursor: clickable ? 'pointer' : 'default',
                  }}
                >
                  {/* Rail above this ball (except first) — connects from previous */}
                  {i > 0 && (
                    <div style={{
                      position: 'absolute',
                      top: -22, right: 13, /* ball_width/2 - rail_width/2 = 14 - 1 */
                      width: 2, height: 22,
                      borderRadius: 1,
                      background: prevFilled
                        ? 'linear-gradient(180deg, rgba(56,189,248,0.7), rgba(129,140,248,0.5))'
                        : 'rgba(var(--wiz-ch), 0.2)',
                      transition: 'background 0.3s',
                    }} />
                  )}

                  {/* Label — LEFT of ball, one word */}
                  <span style={{
                    fontSize: 12,
                    fontWeight: current ? 700 : done ? 500 : 400,
                    color: current
                      ? 'var(--wiz-text-hi)'
                      : done ? 'var(--wiz-text-md)' : 'var(--wiz-text-sub)',
                    transition: 'all 0.2s',
                    whiteSpace: 'nowrap',
                    letterSpacing: '-0.005em',
                  }}>
                    {step.label}
                  </span>

                  {/* Ball */}
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
                    position: 'relative',
                    zIndex: 2,
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
                </div>
              )
            })}
          </div>

          {/* Spacer so progress sits at bottom */}
          <div style={{ flex: 1, minHeight: 40 }} />

          {/* Progress — compact, right-aligned */}
          <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'flex-end', gap: 8 }}>
            <span style={{ fontSize: 10, color: 'var(--wiz-text-sub)', fontWeight: 500 }}>Progress</span>
            <div style={{ width: 72, height: 2, borderRadius: 1, background: 'rgba(var(--wiz-ch), 0.1)' }}>
              <div style={{
                height: '100%',
                width: `${progressPct}%`,
                borderRadius: 1,
                background: 'linear-gradient(90deg, #38BDF8, #818CF8)',
                transition: 'width 0.4s',
              }} />
            </div>
            <span style={{ fontSize: 10, fontWeight: 700, color: '#38BDF8', minWidth: 26, textAlign: 'right' }}>{progressPct}%</span>
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
