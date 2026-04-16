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
 * Unified wizard shell — horizontal stepper matching the SME product
 * (sme.openova.io). Dark/light theme, flat palette, same card surfaces,
 * so the two products feel like a single family.
 */
export function WizardLayout() {
  const { currentStep, setStep } = useWizardStore()
  const { theme, toggle } = useTheme()

  return (
    <div className="corp-body">
      {/* ── Header ─────────────────────────────────────────────── */}
      <header className="corp-header">
        <Link to={IS_SAAS ? '/app/dashboard' : '/'} className="corp-logo">
          <OOLogo h={22} id="wiz-logo" />
          <div className="corp-brand">
            <div className="corp-brand-primary">OpenOva</div>
            <div className="corp-brand-secondary">Corporate</div>
          </div>
        </Link>
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

      {/* ── Stepper + content ─────────────────────────────────── */}
      <main className="corp-main">
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
                >
                  <span className="corp-step-num">
                    {done ? <Check size={14} strokeWidth={2.5} /> : step.id}
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

        <div className="corp-step-content">
          <Outlet />
        </div>
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

        /* ── Header (mirrors SME's sme-header) ────────────────────── */
        .corp-header {
          position: sticky;
          top: 0;
          z-index: 100;
          background: color-mix(in srgb, var(--wiz-page-bg) 90%, transparent);
          backdrop-filter: blur(10px);
          -webkit-backdrop-filter: blur(10px);
          border-bottom: 1px solid rgba(var(--wiz-ch), 0.08);
          padding: 0.9rem 1.5rem;
          display: flex;
          justify-content: space-between;
          align-items: center;
        }

        .corp-logo {
          display: flex;
          align-items: center;
          gap: 0.65rem;
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
          width: 34px;
          height: 34px;
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

        /* ── Main ─────────────────────────────────────────────────── */
        .corp-main {
          flex: 1;
          width: 100%;
          max-width: 1100px;
          margin: 0 auto;
          padding: 2rem 1.25rem 4rem;
        }

        /* ── Stepper (mirrors SME's .stepper) ─────────────────────── */
        .corp-stepper {
          display: flex;
          align-items: center;
          justify-content: center;
          gap: 0.35rem;
          margin-bottom: 2.25rem;
          flex-wrap: nowrap;
        }

        .corp-step {
          display: flex;
          flex-direction: column;
          align-items: center;
          gap: 0.35rem;
          background: transparent;
          border: none;
          color: var(--wiz-text-sub);
          cursor: pointer;
          padding: 0.25rem 0.4rem;
          font: inherit;
        }

        .corp-step:disabled {
          cursor: not-allowed;
          opacity: 0.6;
        }

        .corp-step-num {
          width: 32px;
          height: 32px;
          border-radius: 50%;
          background: rgba(var(--wiz-ch), 0.04);
          border: 2px solid rgba(var(--wiz-ch), 0.15);
          display: flex;
          align-items: center;
          justify-content: center;
          font-weight: 600;
          font-size: 0.88rem;
          transition: all 0.2s ease;
        }

        .corp-step.active .corp-step-num {
          background: rgba(var(--wiz-accent-ch), 1);
          border-color: rgba(var(--wiz-accent-ch), 1);
          color: #fff;
          box-shadow: 0 0 0 4px rgba(var(--wiz-accent-ch), 0.15);
        }

        .corp-step.done .corp-step-num {
          background: rgba(var(--wiz-success-ch), 1);
          border-color: rgba(var(--wiz-success-ch), 1);
          color: #fff;
        }

        .corp-step.active .corp-step-label {
          color: var(--wiz-text-hi);
          font-weight: 600;
        }

        .corp-step.done {
          color: var(--wiz-text-md);
        }

        .corp-step-label {
          font-size: 0.8rem;
          line-height: 1.2;
          white-space: nowrap;
        }

        .corp-step-sep {
          width: 44px;
          height: 2px;
          background: rgba(var(--wiz-ch), 0.15);
          margin-top: -20px;  /* visually centre between circles */
          transition: background 0.2s ease;
        }

        .corp-step-sep.done {
          background: rgba(var(--wiz-success-ch), 1);
        }

        .corp-step-content {
          width: 100%;
        }

        /* ── Responsive — 6 steps need to stay legible on small screens ── */
        @media (max-width: 900px) {
          .corp-step-sep { width: 28px; }
        }

        @media (max-width: 720px) {
          .corp-step-label { display: none; }
          .corp-step-num { width: 28px; height: 28px; font-size: 0.8rem; }
          .corp-step-sep { width: 20px; margin-top: 0; }
          .corp-stepper { gap: 0.15rem; }
        }
      `}</style>
    </div>
  )
}
