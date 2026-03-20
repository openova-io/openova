import { Outlet, Link } from '@tanstack/react-router'
import { X, Sun, Moon, Check } from 'lucide-react'
import { IS_SAAS } from '@/shared/constants/env'
import { useWizardStore } from '@/entities/deployment/store'
import { useTheme } from '@/shared/lib/useTheme'
import { OOLogo } from '@/shared/ui/OOLogo'

export const WIZARD_STEPS = [
  { id: 1, label: 'Organisation' },
  { id: 2, label: 'Topology'     },
  { id: 3, label: 'Provider'     },
  { id: 4, label: 'Credentials'  },
  { id: 5, label: 'Components'   },
  { id: 6, label: 'Review'       },
]

export function WizardLayout() {
  const { currentStep, completedSteps, setStep } = useWizardStore()
  const { theme, toggle } = useTheme()

  return (
    /*
     * position: fixed + inset: 0 = the shell owns the full viewport.
     * The header (logo + step rail) is a flex-shrink: 0 child — it never
     * moves. The body below is flex: 1 + overflow-y: auto, so only the
     * card content scrolls. This eliminates the vertical jump completely.
     */
    <div style={{
      position: 'fixed', inset: 0,
      display: 'flex', flexDirection: 'column',
      background: 'radial-gradient(ellipse at 50% 18%, #0c1e40 0%, #06080f 65%)',
      fontFamily: 'Inter, sans-serif',
      overflow: 'hidden',
    }}>
      {/* ── Ambient glows (decorative, pointer-events: none) ─────────── */}
      <div style={{ position: 'absolute', top: '-20%', left: '5%', width: 700, height: 700, borderRadius: '50%', background: 'radial-gradient(circle, rgba(56,189,248,0.06) 0%, transparent 65%)', pointerEvents: 'none', zIndex: 0 }} />
      <div style={{ position: 'absolute', bottom: '-15%', right: '-5%', width: 550, height: 550, borderRadius: '50%', background: 'radial-gradient(circle, rgba(129,140,248,0.04) 0%, transparent 65%)', pointerEvents: 'none', zIndex: 0 }} />

      {/* ── PINNED HEADER ─────────────────────────────────────────────── */}
      {/* flex-shrink: 0 guarantees this never compresses or moves         */}
      <div style={{ flexShrink: 0, zIndex: 10, padding: '28px 24px 20px' }}>

        {/* Logo + wordmark — perfectly centred */}
        <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 12, marginBottom: 28 }}>
          <OOLogo h={26} id="wiz-logo" />
          <div style={{ lineHeight: 1 }}>
            <div style={{ fontSize: 13, fontWeight: 700, color: 'rgba(255,255,255,0.82)', letterSpacing: '-0.01em' }}>OpenOva</div>
            <div style={{ fontSize: 9, fontWeight: 600, color: 'rgba(255,255,255,0.28)', letterSpacing: '0.18em', textTransform: 'uppercase', marginTop: 2 }}>Catalyst</div>
          </div>
        </div>

        {/* Step rail — numbered circles with gradient connectors */}
        <div style={{ display: 'flex', justifyContent: 'center' }}>
          <div style={{ display: 'flex', alignItems: 'flex-start', width: '100%', maxWidth: 580 }}>
            {WIZARD_STEPS.map((step, i) => {
              const done    = completedSteps.includes(step.id)
              const current = currentStep === step.id
              return (
                <div key={step.id} style={{ flex: 1, display: 'flex', flexDirection: 'column', alignItems: 'center', position: 'relative' }}>
                  {/* Connector line between circles */}
                  {i < WIZARD_STEPS.length - 1 && (
                    <div style={{
                      position: 'absolute', top: 14, left: '50%', right: '-50%',
                      height: 1.5, zIndex: 0,
                      background: done
                        ? 'linear-gradient(90deg, #38BDF8, #818CF8)'
                        : 'rgba(255,255,255,0.08)',
                      transition: 'background 0.4s',
                    }} />
                  )}
                  {/* Number / check circle */}
                  <button
                    onClick={() => done && setStep(step.id)}
                    disabled={!done}
                    style={{
                      width: 28, height: 28, borderRadius: '50%',
                      zIndex: 1, position: 'relative',
                      display: 'flex', alignItems: 'center', justifyContent: 'center',
                      fontSize: 11, fontWeight: 700,
                      background: done
                        ? 'linear-gradient(135deg, #38BDF8, #818CF8)'
                        : current
                          ? 'rgba(56,189,248,0.12)'
                          : 'rgba(255,255,255,0.04)',
                      border: current
                        ? '2px solid #38BDF8'
                        : done
                          ? 'none'
                          : '1.5px solid rgba(255,255,255,0.1)',
                      color: done ? '#fff' : current ? '#38BDF8' : 'rgba(255,255,255,0.2)',
                      boxShadow: current ? '0 0 0 4px rgba(56,189,248,0.1)' : 'none',
                      cursor: done ? 'pointer' : 'default',
                      transition: 'all 0.25s',
                    }}
                  >
                    {done ? <Check size={12} strokeWidth={2.5} /> : step.id}
                  </button>
                  {/* Step label */}
                  <div style={{
                    marginTop: 6, fontSize: 9, lineHeight: 1.3, textAlign: 'center',
                    fontWeight: current ? 600 : 400,
                    letterSpacing: '0.04em',
                    color: current
                      ? '#38BDF8'
                      : done
                        ? 'rgba(255,255,255,0.4)'
                        : 'rgba(255,255,255,0.18)',
                    transition: 'color 0.25s',
                  }}>
                    {step.label}
                  </div>
                </div>
              )
            })}
          </div>
        </div>
      </div>

      {/* ── SCROLLABLE BODY ───────────────────────────────────────────── */}
      {/* flex: 1 + overflow-y: auto — only THIS region moves when content grows */}
      <div id="wizard-body" style={{
        flex: 1,
        overflowY: 'auto',
        display: 'flex',
        flexDirection: 'column',
        alignItems: 'center',
        padding: '24px 24px 56px',
        zIndex: 1,
      }}>
        <div style={{ width: '100%', maxWidth: 540 }}>
          <Outlet />
        </div>
      </div>

      {/* ── TOP-RIGHT CONTROLS ────────────────────────────────────────── */}
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
