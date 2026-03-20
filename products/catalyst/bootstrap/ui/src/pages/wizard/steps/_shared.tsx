import { ChevronLeft, ChevronRight } from 'lucide-react'
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
    <div style={{
      background: 'rgba(var(--wiz-ch),0.04)',
      backdropFilter: 'blur(28px)',
      WebkitBackdropFilter: 'blur(28px)',
      border: '1px solid rgba(var(--wiz-ch),0.08)',
      borderRadius: 20,
      padding: '2rem',
      boxShadow: '0 32px 80px rgba(0,0,0,0.5), inset 0 1px 0 rgba(var(--wiz-ch),0.06)',
    }}>
      {/* Heading */}
      <h2 style={{
        fontSize: 'clamp(1.25rem, 3vw, 1.625rem)',
        fontWeight: 700,
        letterSpacing: '-0.03em',
        color: 'var(--color-text-primary)',
        margin: '0 0 6px',
        lineHeight: 1.2,
      }}>
        {title}
      </h2>
      <p style={{
        fontSize: 13,
        lineHeight: 1.65,
        color: 'rgba(var(--wiz-ch),0.35)',
        margin: '0 0 24px',
        maxWidth: 640,
      }}>
        {description}
      </p>

      {/* Step content */}
      <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem', marginBottom: '2rem' }}>
        {children}
      </div>

      {/* Navigation */}
      <div style={{
        display: 'flex', alignItems: 'center', justifyContent: 'space-between',
        marginTop: 8,
      }}>
        {onBack ? (
          <button
            onClick={onBack}
            style={{
              display: 'inline-flex', alignItems: 'center', gap: 6,
              height: 38, paddingLeft: 12, paddingRight: 14,
              borderRadius: 8, border: '1px solid rgba(var(--wiz-ch),0.1)',
              background: 'transparent',
              color: 'rgba(var(--wiz-ch),0.3)',
              fontSize: 13, fontWeight: 500, cursor: 'pointer',
              fontFamily: 'Inter, sans-serif',
              transition: 'all 0.15s',
            }}
            onMouseEnter={e => {
              (e.currentTarget as HTMLButtonElement).style.background = 'rgba(var(--wiz-ch),0.06)'
              ;(e.currentTarget as HTMLButtonElement).style.color = 'rgba(var(--wiz-ch),0.6)'
            }}
            onMouseLeave={e => {
              (e.currentTarget as HTMLButtonElement).style.background = 'transparent'
              ;(e.currentTarget as HTMLButtonElement).style.color = 'rgba(var(--wiz-ch),0.3)'
            }}
          >
            <ChevronLeft size={14} />
            Back
          </button>
        ) : <div />}

        <button
          onClick={onNext}
          disabled={nextDisabled || nextLoading}
          style={{
            display: 'inline-flex', alignItems: 'center', gap: 8,
            height: 40, paddingLeft: 22, paddingRight: 18,
            borderRadius: 8, border: 'none',
            background: nextDisabled || nextLoading
              ? 'rgba(var(--wiz-ch),0.06)'
              : 'linear-gradient(135deg, #38BDF8 0%, #0EA5E9 100%)',
            color: nextDisabled || nextLoading ? 'rgba(var(--wiz-ch),0.2)' : '#fff',
            fontSize: 14, fontWeight: 600,
            cursor: nextDisabled || nextLoading ? 'not-allowed' : 'pointer',
            fontFamily: 'Inter, sans-serif',
            transition: 'all 0.15s',
            boxShadow: nextDisabled || nextLoading
              ? 'none'
              : '0 1px 3px rgba(56,189,248,0.3), 0 4px 16px rgba(56,189,248,0.15)',
          }}
        >
          {nextLoading ? (
            <>
              <svg className="animate-spin" width={14} height={14} viewBox="0 0 24 24" fill="none">
                <circle cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="3" opacity="0.25"/>
                <path fill="currentColor" opacity="0.75" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/>
              </svg>
              Working…
            </>
          ) : (
            <>
              {nextLabel}
              {!nextDisabled && <ChevronRight size={14} />}
            </>
          )}
        </button>
      </div>
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
