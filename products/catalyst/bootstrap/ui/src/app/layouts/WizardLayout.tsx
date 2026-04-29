import { Fragment } from 'react'
import { Outlet, Link } from '@tanstack/react-router'
import { X, Sun, Moon, Check } from 'lucide-react'
import { IS_SAAS } from '@/shared/constants/env'
import { useWizardStore } from '@/entities/deployment/store'
import { useWizardNav } from '@/shared/lib/wizardNav'
import { useTheme } from '@/shared/lib/useTheme'
import { OOLogo } from '@/shared/ui/OOLogo'

/**
 * Wizard step list — seven progress stops in dependency order. StepSuccess
 * is the terminal destination after StepReview launches provisioning; it
 * is not part of the visible progress, so it is not in this list.
 *
 * Order rationale:
 *   1. Organisation — who you are. Independent of every other choice.
 *   2. Topology     — number of regions + HA shape + AIR-GAP add-on.
 *                     Decides how many region rows the next step needs.
 *   3. Provider     — per-region: cloud provider + provider's region +
 *                     that provider's control-plane SKU + worker SKU +
 *                     count. SKU vocabulary is per-provider (cx32 ≠
 *                     Standard_D4s_v5 ≠ m6i.xlarge), so sizing must live
 *                     INSIDE this step, not in topology.
 *   4. Credentials  — once each region has a provider, collect the API
 *                     token each chosen provider needs, plus the SSH key.
 *   5. Components   — platform building-block selection.
 *   6. Domain       — pool subdomain or BYO domain + admin email.
 *   7. Review       — POST body preview + launch.
 *
 * Topology BEFORE Provider is the dependency-correct order: a provider is
 * a per-region property, picked AFTER topology decides how many regions
 * exist. SKU choices belong INSIDE the provider step because every cloud
 * has its own instance-type vocabulary (see shared/constants/providerSizes.ts).
 */
export const WIZARD_STEPS = [
  { id: 1, label: 'Organisation', desc: 'Industry, size, HQ, compliance'   },
  { id: 2, label: 'Topology',     desc: 'Regions, HA, air-gap'             },
  { id: 3, label: 'Provider',     desc: 'Cloud + region + sizing per slot' },
  { id: 4, label: 'Credentials',  desc: 'API tokens + SSH key'             },
  { id: 5, label: 'Components',   desc: 'Platform building blocks'         },
  { id: 6, label: 'Domain',       desc: 'Pool or BYO + admin email'        },
  { id: 7, label: 'Review',       desc: 'Confirm and provision'            },
]

/**
 * Unified wizard shell — the seven-step progress indicator lives in the top
 * header band (NOT the step body), matching the nova/core console page-header
 * pattern. Closes GitHub issue #174.
 *
 * Header contract (kept in sync with `core/console/src/components/Sidebar.svelte`
 * — the only branded chrome in nova today):
 *   - 56px tall (h-14) — same as nova's logo row
 *   - 1px solid border-bottom using a theme-driven border token
 *   - 16px horizontal padding
 *   - Brand mark + wordmark on the left
 *   - Theme toggle + Exit on the right
 *   - Step indicator slotted into the centre of the header band, with an
 *     accessible mobile fallback ("Step X of Y · Label") below 720px
 *
 * Every dimension/colour comes from the wizard's CSS-variable token set
 * (`--wiz-*` in `src/app/globals.css`); no inline literals.
 */
export function WizardLayout() {
  const { currentStep, setStep } = useWizardStore()
  const nav = useWizardNav((s) => s.nav)
  const { theme, toggle } = useTheme()
  const totalSteps = WIZARD_STEPS.length
  const progressPct = Math.round((currentStep / totalSteps) * 100)
  const currentLabel =
    WIZARD_STEPS.find((s) => s.id === currentStep)?.label ?? ''

  return (
    <div className="corp-body">
      {/* ── Header ─────────────────────────────────────────────── */}
      <header
        className="corp-header"
        data-testid="wizard-header"
        aria-label="Wizard header"
      >
        {/* Brand block — clicking returns home (per #162). The OOLogo SVG
            renders the canonical OpenOva mark from /brand/logo-mark.svg. */}
        <Link to={IS_SAAS ? '/app/dashboard' : '/'} className="corp-logo" data-testid="wizard-logo">
          <OOLogo h={28} id="wiz-logo" />
          <div className="corp-brand">
            <div className="corp-brand-primary">OpenOva</div>
            <div className="corp-brand-secondary">Catalyst</div>
          </div>
        </Link>

        {/* Step indicator — desktop layout sits inline with the header,
            mirroring nova's "page chrome" treatment. The breakpoint mirror
            in CSS swaps to a compact "Step X of Y" string on mobile. */}
        <nav
          className="corp-stepper"
          data-testid="wizard-stepper"
          aria-label="Wizard progress"
        >
          {WIZARD_STEPS.map((step, i) => {
            const done    = step.id < currentStep
            const active  = step.id === currentStep
            const clickable = done

            return (
              <Fragment key={step.id}>
                <button
                  type="button"
                  data-testid={`wizard-step-${step.id}`}
                  className={`corp-step ${active ? 'active' : ''} ${done ? 'done' : ''}`}
                  onClick={() => clickable && setStep(step.id)}
                  disabled={!clickable && !active}
                  aria-current={active ? 'step' : undefined}
                  title={step.label}
                >
                  <span className="corp-step-num">
                    {done ? <Check size={12} strokeWidth={2.75} /> : step.id}
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

        {/* Mobile-only collapsed indicator — hidden on desktop via CSS. */}
        <div className="corp-stepper-compact" data-testid="wizard-stepper-compact" aria-hidden>
          <strong>Step {currentStep}</strong>
          <span>of {totalSteps}</span>
          <span className="corp-stepper-compact-label">· {currentLabel}</span>
        </div>

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

      {/* ── Step body — stepper has been hoisted into the header above,
            so the body now starts directly with the step's title/form. ── */}
      <main className="corp-main">
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

        /* ── Header — mirrors nova's chrome (h-14, 16px X-padding,
              theme-token border-bottom). ───────────────────────────── */
        .corp-header {
          position: sticky;
          top: 0;
          z-index: 100;
          background: color-mix(in srgb, var(--wiz-page-bg) 92%, transparent);
          backdrop-filter: blur(10px);
          -webkit-backdrop-filter: blur(10px);
          border-bottom: 1px solid var(--wiz-border);
          height: 56px;                /* nova h-14 — single source of truth */
          padding: 0 1rem;             /* nova px-4 */
          display: flex;
          align-items: center;
          gap: 1.25rem;
        }

        .corp-logo {
          display: flex;
          align-items: center;
          gap: 0.55rem;
          text-decoration: none;
          color: inherit;
          flex-shrink: 0;
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
          gap: 0.4rem;
          margin-left: auto;
          flex-shrink: 0;
        }

        .corp-icon-btn {
          background: transparent;
          border: 1px solid var(--wiz-border);
          color: var(--wiz-text-sub);
          width: 30px;
          height: 30px;
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

        /* ── Step indicator — sits inline with the header on desktop,
              collapses on mobile. ───────────────────────────────────── */
        .corp-stepper {
          display: flex;
          align-items: center;
          justify-content: center;
          gap: 0.3rem;
          flex: 1;
          min-width: 0;
        }

        .corp-step {
          display: inline-flex;
          flex-direction: row;
          align-items: center;
          gap: 0.45rem;
          background: transparent;
          border: none;
          color: var(--wiz-text-sub);
          cursor: pointer;
          padding: 0.2rem 0.45rem;
          font: inherit;
          border-radius: 6px;
          transition: color 0.15s ease, background 0.15s ease;
        }

        .corp-step:hover:not(:disabled) {
          color: var(--wiz-text-md);
        }

        .corp-step:disabled {
          cursor: not-allowed;
        }

        .corp-step-num {
          width: 22px;
          height: 22px;
          border-radius: 50%;
          background: rgba(var(--wiz-ch), 0.04);
          border: 1.5px solid rgba(var(--wiz-ch), 0.15);
          display: inline-flex;
          align-items: center;
          justify-content: center;
          font-weight: 600;
          font-size: 11px;
          flex-shrink: 0;
          transition: all 0.2s ease;
        }

        .corp-step.active .corp-step-num {
          background: rgba(var(--wiz-accent-ch), 1);
          border-color: rgba(var(--wiz-accent-ch), 1);
          color: #fff;
          box-shadow: 0 0 0 3px rgba(var(--wiz-accent-ch), 0.15);
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

        .corp-step.done .corp-step-label {
          color: var(--wiz-text-md);
        }

        .corp-step-label {
          font-size: 12px;
          line-height: 1.2;
          white-space: nowrap;
        }

        .corp-step-sep {
          width: 18px;
          height: 1.5px;
          background: rgba(var(--wiz-ch), 0.15);
          flex-shrink: 0;
          transition: background 0.2s ease;
        }

        .corp-step-sep.done {
          background: rgba(var(--wiz-success-ch), 0.6);
        }

        /* Mobile-collapsed step indicator — hidden on desktop. */
        .corp-stepper-compact {
          display: none;
          align-items: center;
          gap: 0.35rem;
          color: var(--wiz-text-md);
          font-size: 13px;
          flex: 1;
          min-width: 0;
          overflow: hidden;
        }
        .corp-stepper-compact strong { color: var(--wiz-text-hi); font-weight: 700; }
        .corp-stepper-compact-label {
          color: var(--wiz-text-sub);
          overflow: hidden;
          text-overflow: ellipsis;
          white-space: nowrap;
        }

        /* ── Main ─────────────────────────────────────────────────── */
        .corp-main {
          flex: 1;
          width: 100%;
          max-width: 1100px;
          margin: 0 auto;
          padding: 2rem 1.25rem 4rem;
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

        /* ── Responsive header — at narrow widths drop the per-step labels
              first, then collapse the whole stepper into a "Step X of Y"
              string so the 7 dots don't overflow on phones. ─────────── */
        @media (max-width: 1024px) {
          .corp-step-label { display: none; }
          .corp-step-sep { width: 12px; }
        }

        @media (max-width: 720px) {
          .corp-header { gap: 0.65rem; }
          .corp-stepper { display: none; }
          .corp-stepper-compact { display: flex; }
          .corp-brand-secondary { display: none; }
        }

        @media (max-width: 480px) {
          .corp-stepper-compact-label { display: none; }
        }
      `}</style>
    </div>
  )
}
