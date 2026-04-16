import { useWizardStore } from '@/entities/deployment/store'

interface StepShellProps {
  title: string
  description: string
  children: React.ReactNode
  onNext: () => void
  onBack?: () => void
  nextLabel?: React.ReactNode
  nextDisabled?: boolean
  nextLoading?: boolean
}

/**
 * StepShell — flat container matching SME's pattern (no outer card).
 * Title + description, then step content, then a sticky-bottom nav row
 * that mirrors the SME wizard footer (Back/Continue buttons, solid SME
 * colors, no gradients).
 */
export function StepShell({
  title,
  description,
  children,
  onNext,
  onBack,
  nextLabel = 'Continue',
  nextDisabled,
  nextLoading,
}: StepShellProps) {
  return (
    <div className="corp-step-shell">
      {/* Heading */}
      <h2 className="corp-step-title">{title}</h2>
      <p className="corp-step-desc">{description}</p>

      {/* Step content — child cards flow flat, no outer wrapper */}
      <div className="corp-step-children">{children}</div>

      {/* Sticky footer — SME pattern */}
      <footer className="corp-step-footer">
        <div className="corp-step-footer-inner">
          {onBack ? (
            <button type="button" className="corp-btn-back" onClick={onBack}>
              ← Back
            </button>
          ) : (
            <div />
          )}

          <button
            type="button"
            className="corp-btn-next"
            onClick={onNext}
            disabled={nextDisabled || nextLoading}
          >
            {nextLoading ? (
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
              <>
                {nextLabel} →
              </>
            )}
          </button>
        </div>
      </footer>

      <style>{`
        .corp-step-shell {
          display: flex;
          flex-direction: column;
          padding-bottom: 6rem; /* reserve space for sticky footer */
        }

        .corp-step-title {
          font-size: clamp(1.35rem, 3vw, 1.75rem);
          font-weight: 700;
          letter-spacing: -0.015em;
          color: var(--wiz-text-hi);
          margin: 0 0 0.4rem;
          line-height: 1.2;
        }

        .corp-step-desc {
          font-size: 0.92rem;
          line-height: 1.55;
          color: var(--wiz-text-sub);
          margin: 0 0 1.75rem;
          max-width: 680px;
        }

        .corp-step-children {
          display: flex;
          flex-direction: column;
          gap: 1rem;
        }

        /* ── Sticky footer — mirrors SME .wizard-footer ───────────── */
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
          gap: 0.75rem;
          max-width: 1100px;
          margin: 0 auto;
        }

        /* ── SME-style buttons ────────────────────────────────────── */
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

        .corp-spin {
          animation: corp-spin 1s linear infinite;
        }

        @keyframes corp-spin {
          to { transform: rotate(360deg); }
        }

        @media (max-width: 640px) {
          .corp-step-footer { padding: 0.7rem 0.9rem; }
          .corp-btn-next, .corp-btn-back { font-size: 0.88rem; padding: 0.5rem 0.9rem; }
        }
      `}</style>
    </div>
  )
}

export function useStepNav() {
  const { currentStep, setStep, markStepComplete } = useWizardStore()

  function next() {
    markStepComplete(currentStep)
    setStep(currentStep + 1)
  }

  function back() {
    setStep(currentStep - 1)
  }

  return { next, back, currentStep }
}
