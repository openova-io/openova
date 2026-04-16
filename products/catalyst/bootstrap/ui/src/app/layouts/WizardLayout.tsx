import { Fragment } from 'react'
import { Outlet, Link } from '@tanstack/react-router'
import { X, Sun, Moon, Check } from 'lucide-react'
import { IS_SAAS } from '@/shared/constants/env'
import { useWizardStore } from '@/entities/deployment/store'
import { useWizardNav } from '@/shared/lib/wizardNav'
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
  const nav = useWizardNav((s) => s.nav)
  const { theme, toggle } = useTheme()
  const totalSteps = WIZARD_STEPS.length
  const progressPct = Math.round((currentStep / totalSteps) * 100)

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

      {/* Persistent footer — never unmounts on step change.
          Reads nav state published by the current step. */}
      <footer className="corp-step-footer">
        <div className="corp-step-footer-inner">
          <div className="corp-step-footer-info">
            <span className="corp-step-counter">
              <strong>Step {currentStep}</strong> of {totalSteps}
            </span>
            <span className="corp-step-divider" aria-hidden>·</span>
            <span className="corp-step-current">{nav.stepTitle ?? ''}</span>
            <span className="corp-step-progress-pill" aria-hidden>{progressPct}%</span>
          </div>

          <div className="corp-step-footer-actions">
            {nav.onBack ? (
              <button type="button" className="corp-btn-back" onClick={nav.onBack}>
                ← Back
              </button>
            ) : null}
            <button
              type="button"
              className="corp-btn-next"
              onClick={nav.onNext}
              disabled={!nav.onNext || nav.nextDisabled || nav.nextLoading}
            >
              {nav.nextLoading ? (
                <>
                  <svg
                    className="corp-spin"
                    width={14}
                    height={14}
                    viewBox="0 0 24 24"
                    fill="none"
                    aria-hidden
                  >
                    <circle cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="3" opacity="0.25" />
                    <path
                      fill="currentColor"
                      opacity="0.8"
                      d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"
                    />
                  </svg>
                  Working…
                </>
              ) : (
                <>{nav.nextLabel ?? 'Continue'} →</>
              )}
            </button>
          </div>
        </div>
      </footer>

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

        /* ── Persistent sticky footer (was inside StepShell — moved
              here so it doesn't unmount on step change) ─────────────── */
        .corp-step-footer {
          position: fixed;
          bottom: 0;
          left: 0;
          right: 0;
          background: color-mix(in srgb, var(--wiz-page-bg) 95%, transparent);
          backdrop-filter: blur(10px);
          -webkit-backdrop-filter: blur(10px);
          border-top: 1px solid rgba(var(--wiz-ch), 0.08);
          z-index: 50;
          padding: 0.8rem 1.25rem;
        }
        .corp-step-footer-inner {
          display: flex;
          justify-content: space-between;
          align-items: center;
          gap: 1rem;
          max-width: 1100px;
          margin: 0 auto;
        }

        .corp-step-footer-info {
          display: flex;
          align-items: center;
          gap: 0.6rem;
          color: var(--wiz-text-sub);
          font-size: 0.88rem;
          min-width: 0;
        }
        .corp-step-counter strong {
          color: var(--wiz-text-hi);
          font-weight: 700;
        }
        .corp-step-divider { color: rgba(var(--wiz-ch), 0.25); }
        .corp-step-current {
          color: var(--wiz-text-md);
          font-weight: 600;
          overflow: hidden;
          text-overflow: ellipsis;
          white-space: nowrap;
        }
        .corp-step-progress-pill {
          display: inline-block;
          margin-left: 0.35rem;
          padding: 0.18rem 0.55rem;
          background: rgba(var(--wiz-accent-ch), 0.12);
          color: rgba(var(--wiz-accent-ch), 1);
          border-radius: 999px;
          font-weight: 700;
          font-size: 0.75rem;
        }
        .corp-step-footer-actions {
          display: flex;
          align-items: center;
          gap: 0.5rem;
          flex-shrink: 0;
        }

        .corp-btn-back {
          padding: 0.55rem 1rem;
          border-radius: 7px;
          background: transparent;
          color: var(--wiz-text-sub);
          border: 1px solid rgba(var(--wiz-ch), 0.18);
          font: inherit;
          font-size: 0.9rem;
          font-weight: 600;
          cursor: pointer;
          transition: all 0.15s;
        }
        .corp-btn-back:hover {
          color: var(--wiz-text-hi);
          border-color: rgba(var(--wiz-ch), 0.35);
        }

        .corp-btn-next {
          padding: 0.65rem 1.3rem;
          border-radius: 7px;
          background: rgba(var(--wiz-accent-ch), 1);
          color: #fff;
          border: none;
          font: inherit;
          font-size: 0.95rem;
          font-weight: 600;
          cursor: pointer;
          display: inline-flex;
          align-items: center;
          gap: 0.4rem;
          transition: background 0.15s, transform 0.1s;
          box-shadow: 0 1px 2px rgba(var(--wiz-accent-ch), 0.3);
        }
        .corp-btn-next:hover:not(:disabled) {
          background: color-mix(in srgb, rgba(var(--wiz-accent-ch), 1) 90%, black);
          transform: translateY(-1px);
        }
        .corp-btn-next:disabled {
          background: rgba(var(--wiz-ch), 0.1);
          color: rgba(var(--wiz-ch), 0.35);
          cursor: not-allowed;
          box-shadow: none;
        }
        .corp-spin { animation: corp-spin 1s linear infinite; }
        @keyframes corp-spin { to { transform: rotate(360deg); } }

        @media (max-width: 820px) {
          .corp-step-progress-pill { display: none; }
        }
        @media (max-width: 640px) {
          .corp-step-footer { padding: 0.7rem 0.9rem; }
          .corp-btn-next, .corp-btn-back { font-size: 0.88rem; padding: 0.5rem 0.9rem; }
          .corp-step-current, .corp-step-divider { display: none; }
          .corp-step-footer-info { font-size: 0.8rem; }
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
