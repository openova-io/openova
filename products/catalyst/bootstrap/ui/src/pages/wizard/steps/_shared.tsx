import { Button } from '@/shared/ui/button'
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
    <div className="flex flex-col gap-8">
      <div>
        <h2 className="text-xl font-semibold text-[oklch(92%_0.01_250)]">{title}</h2>
        <p className="mt-1.5 text-sm text-[oklch(50%_0.01_250)] leading-relaxed">{description}</p>
      </div>

      <div className="flex flex-col gap-6">{children}</div>

      <div className="flex items-center justify-between pt-2">
        {onBack ? (
          <Button variant="ghost" size="md" onClick={onBack}>
            <ArrowLeft className="h-4 w-4" />
            Back
          </Button>
        ) : (
          <div />
        )}
        <Button
          size="md"
          onClick={onNext}
          disabled={nextDisabled}
          loading={nextLoading}
        >
          {nextLabel}
          {!nextLoading && <ArrowRight className="h-4 w-4" />}
        </Button>
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
