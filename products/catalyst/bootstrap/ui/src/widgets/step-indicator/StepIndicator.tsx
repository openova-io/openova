import { motion } from 'framer-motion'
import { Check } from 'lucide-react'
import { cn } from '@/shared/lib/utils'

export interface WizardStep {
  id: number
  label: string
  description: string
}

interface StepIndicatorProps {
  steps: WizardStep[]
  currentStep: number
  completedSteps: number[]
  onStepClick?: (step: number) => void
}

export function StepIndicator({
  steps,
  currentStep,
  completedSteps,
  onStepClick,
}: StepIndicatorProps) {
  return (
    <nav aria-label="Wizard progress" className="flex flex-col gap-1">
      {steps.map((step, index) => {
        const isCompleted = completedSteps.includes(step.id)
        const isCurrent = currentStep === step.id
        const isClickable = isCompleted && onStepClick

        return (
          <div key={step.id} className="flex items-start gap-3">
            <div className="flex flex-col items-center self-stretch">
              <button
                onClick={() => isClickable && onStepClick(step.id)}
                disabled={!isClickable}
                aria-current={isCurrent ? 'step' : undefined}
                aria-label={`Step ${step.id}: ${step.label}${isCompleted ? ' (completed)' : isCurrent ? ' (current)' : ''}`}
                className={cn(
                  'relative flex h-7 w-7 items-center justify-center rounded-full',
                  'text-xs font-semibold transition-all duration-200 shrink-0',
                  'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[--color-brand-500]',
                  isCompleted && [
                    'bg-[--color-brand-500] text-white',
                    isClickable && 'cursor-pointer hover:bg-[--color-brand-400]',
                  ],
                  isCurrent && [
                    'bg-[--color-surface-2] border-2 border-[--color-brand-500]',
                    'text-[--color-brand-500]',
                  ],
                  !isCompleted && !isCurrent && [
                    'bg-[--color-surface-2] border border-[--color-surface-border]',
                    'text-[--color-text-disabled]',
                    'cursor-default',
                  ]
                )}
              >
                {isCompleted ? (
                  <motion.div
                    initial={{ scale: 0 }}
                    animate={{ scale: 1 }}
                    transition={{ type: 'spring', stiffness: 400, damping: 20 }}
                  >
                    <Check className="h-3.5 w-3.5" strokeWidth={2.5} />
                  </motion.div>
                ) : (
                  <span>{step.id}</span>
                )}

                {isCurrent && (
                  <motion.div
                    className="absolute inset-0 rounded-full border-2 border-[--color-brand-500]"
                    animate={{ scale: [1, 1.3, 1], opacity: [0.5, 0, 0.5] }}
                    transition={{ duration: 2, repeat: Infinity, ease: 'easeInOut' }}
                  />
                )}
              </button>

              {index < steps.length - 1 && (
                <div className="relative mt-1 w-px flex-1 overflow-hidden">
                  <div className="absolute inset-0 bg-[--color-surface-border]" />
                  {isCompleted && (
                    <motion.div
                      className="absolute inset-0 bg-[--color-brand-500]/40"
                      initial={{ scaleY: 0, originY: 0 }}
                      animate={{ scaleY: 1 }}
                      transition={{ duration: 0.4, ease: 'easeOut' }}
                    />
                  )}
                </div>
              )}
            </div>

            <div className={cn('pb-6 pt-0.5', index === steps.length - 1 && 'pb-0')}>
              <p
                className="text-sm font-medium leading-tight transition-colors duration-200"
                style={{
                  color: isCurrent
                    ? 'var(--color-text-primary)'
                    : isCompleted
                    ? 'var(--color-text-secondary)'
                    : 'var(--color-text-disabled)',
                }}
              >
                {step.label}
              </p>
              <p
                className="mt-0.5 text-xs"
                style={{ color: 'var(--color-text-muted)' }}
              >
                {step.description}
              </p>
            </div>
          </div>
        )
      })}
    </nav>
  )
}
