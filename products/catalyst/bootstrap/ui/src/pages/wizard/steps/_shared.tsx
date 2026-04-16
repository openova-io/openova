import { useEffect } from 'react'
import { useWizardStore } from '@/entities/deployment/store'
import { useWizardNav } from '@/shared/lib/wizardNav'

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
 * StepShell — flat content container. Title + content + bottom helper text.
 * Nav handlers (Back / Continue) are PUBLISHED to the wizardNav store and
 * rendered by the persistent footer in WizardLayout. Footer DOM never
 * unmounts on step change — no flicker.
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
  const setNav = useWizardNav((s) => s.setNav)

  // Publish current step's nav state to the store on every relevant change
  useEffect(() => {
    setNav({
      onNext,
      onBack,
      nextDisabled,
      nextLoading,
      nextLabel,
      stepTitle: title,
    })
  }, [setNav, onNext, onBack, nextDisabled, nextLoading, nextLabel, title])

  return (
    <div className="corp-step-shell">
      {/* Compact heading — SME pattern, title only at the top */}
      <header className="corp-step-head">
        <h2 className="corp-step-title">{title}</h2>
      </header>

      {/* Step content — child cards flow flat, no outer wrapper */}
      <div className="corp-step-children">{children}</div>

      {/* Helper text moved to the bottom — read it only if you need it */}
      {description && <p className="corp-step-hint">{description}</p>}

      <style>{`
        .corp-step-shell {
          display: flex;
          flex-direction: column;
          padding-bottom: 6rem; /* reserve space for the WizardLayout footer */
        }

        /* Compact header */
        .corp-step-head {
          text-align: left;
          margin-bottom: 1rem;
          padding-bottom: 0.65rem;
          border-bottom: 1px solid var(--wiz-border-sub);
        }

        .corp-step-title {
          font-size: 1.1rem;
          font-weight: 600;
          letter-spacing: -0.005em;
          color: var(--wiz-text-hi);
          margin: 0;
          line-height: 1.3;
        }

        .corp-step-children {
          display: flex;
          flex-direction: column;
          gap: 1rem;
        }

        .corp-step-hint {
          margin: 1.5rem 0 0;
          padding-top: 0.9rem;
          border-top: 1px dashed var(--wiz-border-sub);
          color: var(--wiz-text-sub);
          font-size: 0.82rem;
          line-height: 1.55;
          max-width: 680px;
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
