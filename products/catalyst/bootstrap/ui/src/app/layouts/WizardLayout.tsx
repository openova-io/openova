import { Fragment } from 'react'
import { Outlet, Link } from '@tanstack/react-router'
import { X, Sun, Moon, Check } from 'lucide-react'
import { IS_SAAS } from '@/shared/constants/env'
import { useWizardStore } from '@/entities/deployment/store'
import { useTheme } from '@/shared/lib/useTheme'
import { OOLogo } from '@/shared/ui/OOLogo'

export const WIZARD_STEPS = [
  { id: 1, label: 'Organisation', desc: 'Name, domain, contact'    },
  { id: 2, label: 'Topology',     desc: 'Regions and clusters'      },
  { id: 3, label: 'Provider',     desc: 'Cloud provider per region' },
  { id: 4, label: 'Credentials',  desc: 'API access tokens'         },
  { id: 5, label: 'Components',   desc: 'Platform building blocks'  },
  { id: 6, label: 'Review',       desc: 'Confirm and provision'     },
]

/**
 * Wizard shell — stepper lives INSIDE the header row (logo · stepper · chrome).
 * Reclaims the vertical space a dedicated stepper row would otherwise waste,
 * and matches SME's pattern for a unified wizard surface.
 */
export function WizardLayout() {
  const { currentStep, setStep } = useWizardStore()
  const { theme, toggle } = useTheme()

  return (
    <div className="corp-body">
      {/* ── Header with integrated stepper ──────────────────────── */}
      <header className="corp-header">
        <Link to={IS_SAAS ? '/app/dashboard' : '/'} className="corp-logo">
          <OOLogo h={22} id="wiz-logo" />
          <div className="corp-brand">
            <div className="corp-brand-primary">OpenOva</div>
            <div className="corp-brand-secondary">Corporate</div>
          </div>
        </Link>

        <nav className="corp-stepper" aria-label="Wizard progress">
          {WIZARD_STEPS.map((step, i) => {
            const done    = step.id < currentStep
            const active  = step.id === currentStep
            const clickable = done

            return (
              <Fragment key={step.id}>
                <button
                  type="button"
                  className={`corp-step ${active ? 'active' : ''} ${done ? 'done' : ''}`}
                  onClick={() => clickable && setStep(step.id)}
                  disabled={!clickable && !active}
                  aria-current={active ? 'step' : undefined}
                  title={step.label}
                >
                  <span className="corp-step-num">
                    {done ? <Check size={12} strokeWidth={2.8} /> : step.id}
                  </span>
                  <span className="corp-step-label">{step.label}</span>
                </button>
                {i < WIZARD_STEPS.length - 1 && (
                  <span
                    className={`corp-step-sep ${done ? 'done' : ''}`}
                    aria-hidden
                  />
                )}
              </Fragment>
            )
          })}
        </nav>

        <div className="corp-header-actions">
          <button
            onClick={toggle}
            aria-label="Toggle theme"
            title="Toggle light / dark"
            className="corp-icon-btn"
          >
            {theme === 'dark' ? <Sun size={14} /> : <Moon size={14} />}
          </button>
          <Link to={IS_SAAS ? '/app/dashboard' : '/'}>
            <button className="corp-icon-btn" aria-label="Exit wizard" title="Exit wizard">
              <X size={14} />
            </button>
          </Link>
        </div>
      </header>

      {/* ── Step content — flows directly below header ──────────── */}
      <main className="corp-main">
        <Outlet />
      </main>

      <style>{`
        .corp-body {
          min-height: 100vh;
          background: var(--wiz-page-bg);
          color: var(--wiz-text-md);
          display: flex;
          flex-direction: column;
          font-family: Inter, ui-sans-serif, system-ui, sans-serif;
        }

        /* ── Header with 3 zones: logo | stepper | actions ─────────── */
        .corp-header {
          position: sticky;
          top: 0;
          z-index: 100;
          background: color-mix(in srgb, var(--wiz-page-bg) 88%, transparent);
          backdrop-filter: blur(12px);
          -webkit-backdrop-filter: blur(12px);
          border-bottom: 1px solid rgba(var(--wiz-ch), 0.08);
          padding: 0.75rem 1.25rem;
          display: grid;
          grid-template-columns: auto 1fr auto;
          align-items: center;
          gap: 1.5rem;
        }

        .corp-logo {
          display: flex;
          align-items: center;
          gap: 0.6rem;
          text-decoration: none;
          color: inherit;
        }

        .corp-brand { line-height: 1; }

        .corp-brand-primary {
          font-size: 13px;
          font-weight: 700;
          color: var(--wiz-text-hi);
          letter-spacing: -0.01em;
        }

        .corp-brand-secondary {
          font-size: 9px;
          font-weight: 600;
          letter-spacing: 0.18em;
          text-transform: uppercase;
          color: var(--wiz-text-sub);
          margin-top: 2px;
        }

        .corp-header-actions {
          display: flex;
          align-items: center;
          gap: 0.5rem;
        }

        .corp-icon-btn {
          background: transparent;
          border: 1px solid rgba(var(--wiz-ch), 0.1);
          color: var(--wiz-text-sub);
          width: 32px;
          height: 32px;
          border-radius: 7px;
          cursor: pointer;
          display: inline-flex;
          align-items: center;
          justify-content: center;
          transition: border-color 0.15s, color 0.15s, background 0.15s;
          padding: 0;
        }

        .corp-icon-btn:hover {
          border-color: rgba(var(--wiz-accent-ch), 0.6);
          color: var(--wiz-text-hi);
          background: rgba(var(--wiz-accent-ch), 0.1);
        }

        /* ── Inline stepper (compact pill style, sits in header row) ─── */
        .corp-stepper {
          display: flex;
          align-items: center;
          justify-content: center;
          gap: 0.2rem;
          flex-wrap: nowrap;
          overflow: hidden;
        }

        .corp-step {
          display: inline-flex;
          flex-direction: row;
          align-items: center;
          gap: 0.4rem;
          padding: 0.3rem 0.55rem;
          border-radius: 999px;
          background: transparent;
          border: 1px solid transparent;
          color: var(--wiz-text-sub);
          cursor: pointer;
          font: inherit;
          white-space: nowrap;
          transition: all 0.15s ease;
        }

        .corp-step:disabled {
          cursor: not-allowed;
        }

        .corp-step:hover:not(:disabled) {
          background: rgba(var(--wiz-ch), 0.04);
        }

        .corp-step-num {
          width: 22px;
          height: 22px;
          flex: 0 0 22px;
          border-radius: 50%;
          background: rgba(var(--wiz-ch), 0.05);
          border: 1.5px solid rgba(var(--wiz-ch), 0.18);
          display: inline-flex;
          align-items: center;
          justify-content: center;
          font-weight: 700;
          font-size: 0.72rem;
          transition: all 0.2s ease;
        }

        .corp-step-label {
          font-size: 0.78rem;
          font-weight: 500;
          letter-spacing: -0.005em;
          line-height: 1;
        }

        /* Active */
        .corp-step.active {
          background: rgba(var(--wiz-accent-ch), 0.1);
          border-color: rgba(var(--wiz-accent-ch), 0.25);
        }
        .corp-step.active .corp-step-num {
          background: rgba(var(--wiz-accent-ch), 1);
          border-color: rgba(var(--wiz-accent-ch), 1);
          color: #fff;
          box-shadow: 0 0 0 3px rgba(var(--wiz-accent-ch), 0.18);
        }
        .corp-step.active .corp-step-label {
          color: var(--wiz-text-hi);
          font-weight: 600;
        }

        /* Done */
        .corp-step.done .corp-step-num {
          background: #22C55E;
          border-color: #22C55E;
          color: #fff;
        }
        .corp-step.done {
          color: var(--wiz-text-md);
        }
        .corp-step.done .corp-step-label {
          color: var(--wiz-text-md);
        }

        /* Separator between pills */
        .corp-step-sep {
          width: 16px;
          height: 1.5px;
          background: rgba(var(--wiz-ch), 0.15);
          border-radius: 1px;
          flex-shrink: 0;
          transition: background 0.2s ease;
        }
        .corp-step-sep.done {
          background: #22C55E;
        }

        /* ── Main content ────────────────────────────────────────── */
        .corp-main {
          flex: 1;
          width: 100%;
          max-width: 1100px;
          margin: 0 auto;
          padding: 2rem 1.25rem 4rem;
        }

        /* ── Responsive ──────────────────────────────────────────── */
        /* Laptop: compress separators */
        @media (max-width: 1200px) {
          .corp-step-sep { width: 10px; }
          .corp-step { padding: 0.3rem 0.45rem; gap: 0.35rem; }
        }

        /* Tablet: hide labels, dots-only */
        @media (max-width: 980px) {
          .corp-step-label { display: none; }
          .corp-step { padding: 0.25rem; }
          .corp-step.active, .corp-step:hover:not(:disabled) {
            background: transparent;
            border-color: transparent;
          }
          .corp-stepper { gap: 0.15rem; }
          .corp-step-sep { width: 12px; }
        }

        /* Phone: stack header into two rows (logo/chrome + stepper) */
        @media (max-width: 640px) {
          .corp-header {
            grid-template-columns: auto auto;
            grid-template-areas:
              "logo actions"
              "stepper stepper";
            gap: 0.5rem;
          }
          .corp-logo { grid-area: logo; }
          .corp-header-actions { grid-area: actions; }
          .corp-stepper { grid-area: stepper; justify-content: flex-start; overflow-x: auto; padding-bottom: 2px; }
          .corp-step-num { width: 20px; height: 20px; font-size: 0.68rem; }
        }
      `}</style>
    </div>
  )
}
