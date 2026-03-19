import { ArrowLeft, ArrowRight } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'

interface StepShellProps {
  title: string
  description: string
  children: React.ReactNode
  onNext: () => void
  onBack?: () => void
  nextLabel?: string
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
    <div style={{ display: 'flex', flexDirection: 'column', gap: 0 }}>

      {/* ── Heading ─────────────────────────────────────────────── */}
      <div style={{ marginBottom: '2rem' }}>
        <h2 style={{
          fontSize: 'clamp(1.5rem, 3vw, 2rem)',
          fontWeight: 700,
          letterSpacing: '-0.03em',
          lineHeight: 1.15,
          color: 'var(--color-text-primary)',
          margin: 0,
          marginBottom: '0.625rem',
        }}>
          {title}
        </h2>
        <p style={{
          fontSize: 14,
          lineHeight: 1.6,
          color: 'var(--color-text-muted)',
          margin: 0,
          maxWidth: 420,
        }}>
          {description}
        </p>
      </div>

      {/* ── Fields ──────────────────────────────────────────────── */}
      <div style={{ display: 'flex', flexDirection: 'column', gap: '1.25rem', marginBottom: '2.5rem' }}>
        {children}
      </div>

      {/* ── Navigation ──────────────────────────────────────────── */}
      <div style={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        {onBack ? (
          <button
            onClick={onBack}
            style={{
              display: 'inline-flex', alignItems: 'center', gap: 6,
              height: 40, paddingLeft: 12, paddingRight: 16,
              borderRadius: 8, border: '1px solid var(--color-surface-border)',
              background: 'transparent',
              color: 'var(--color-text-muted)',
              fontSize: 14, fontWeight: 500,
              cursor: 'pointer', transition: 'all 0.15s',
            }}
            onMouseEnter={e => {
              (e.currentTarget as HTMLButtonElement).style.background = 'var(--color-surface-2)'
              ;(e.currentTarget as HTMLButtonElement).style.color = 'var(--color-text-secondary)'
            }}
            onMouseLeave={e => {
              (e.currentTarget as HTMLButtonElement).style.background = 'transparent'
              ;(e.currentTarget as HTMLButtonElement).style.color = 'var(--color-text-muted)'
            }}
          >
            <ArrowLeft size={15} />
            Back
          </button>
        ) : (
          <div />
        )}

        <button
          onClick={onNext}
          disabled={nextDisabled || nextLoading}
          style={{
            display: 'inline-flex', alignItems: 'center', gap: 8,
            height: 40, paddingLeft: 20, paddingRight: 16,
            borderRadius: 8, border: 'none',
            background: nextDisabled || nextLoading
              ? 'var(--color-surface-3)'
              : 'linear-gradient(135deg, #38BDF8 0%, #0EA5E9 100%)',
            color: nextDisabled || nextLoading ? 'var(--color-text-disabled)' : '#fff',
            fontSize: 14, fontWeight: 600,
            cursor: nextDisabled || nextLoading ? 'not-allowed' : 'pointer',
            transition: 'all 0.15s',
            opacity: nextDisabled ? 0.5 : 1,
            boxShadow: nextDisabled || nextLoading
              ? 'none'
              : '0 1px 3px rgba(56,189,248,0.25), 0 4px 12px rgba(56,189,248,0.12)',
          }}
        >
          {nextLoading ? (
            <>
              <svg className="animate-spin" width={15} height={15} viewBox="0 0 24 24" fill="none">
                <circle cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="3" opacity="0.25"/>
                <path fill="currentColor" opacity="0.75" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z"/>
              </svg>
              Working…
            </>
          ) : (
            <>
              {nextLabel}
              <ArrowRight size={15} />
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
