import { Outlet, Link } from '@tanstack/react-router'
import { OctagonAlert, X } from 'lucide-react'
import { IS_SAAS } from '@/shared/constants/env'
import { StepIndicator, type WizardStep } from '@/widgets/step-indicator/StepIndicator'
import { useWizardStore } from '@/entities/deployment/store'
import { Button } from '@/shared/ui/button'

const WIZARD_STEPS: WizardStep[] = [
  { id: 1, label: 'Organisation', description: 'Name, domain, contact' },
  { id: 2, label: 'Cloud provider', description: 'Select target cloud' },
  { id: 3, label: 'Credentials', description: 'API access token' },
  { id: 4, label: 'Infrastructure', description: 'Regions, nodes, sizing' },
  { id: 5, label: 'Components', description: 'Platform components' },
  { id: 6, label: 'Review', description: 'Confirm and provision' },
]

export function WizardLayout() {
  const { currentStep, completedSteps, setStep } = useWizardStore()

  return (
    <div className="flex min-h-dvh bg-[--color-surface-0]">
      {/* Left sidebar — step indicator */}
      <aside className="hidden md:flex md:w-64 lg:w-72 shrink-0 flex-col border-r border-[--color-surface-border] bg-[--color-surface-1] p-6">
        <div className="flex items-center gap-2.5 mb-10">
          <div className="flex h-7 w-7 items-center justify-center rounded-[--radius-md] bg-[--color-brand-500]">
            <OctagonAlert className="h-4 w-4 text-white" />
          </div>
          <span className="text-sm font-semibold text-[oklch(92%_0.01_250)] tracking-tight">Catalyst</span>
        </div>

        <div className="mb-6">
          <p className="text-xs font-semibold text-[oklch(45%_0.01_250)] uppercase tracking-wider mb-1">
            New deployment
          </p>
          <p className="text-xs text-[oklch(35%_0.01_250)]">
            Step {currentStep} of {WIZARD_STEPS.length}
          </p>
        </div>

        <StepIndicator
          steps={WIZARD_STEPS}
          currentStep={currentStep}
          completedSteps={completedSteps}
          onStepClick={setStep}
        />

        <div className="mt-auto pt-6 border-t border-[--color-surface-border]">
          <p className="text-xs text-[oklch(35%_0.01_250)] leading-relaxed">
            All provisioning runs in your cloud account. Credentials are used only during setup and never stored on our servers.
          </p>
        </div>
      </aside>

      {/* Main wizard area */}
      <div className="flex flex-1 flex-col">
        {/* Top bar */}
        <header className="flex h-14 items-center justify-between border-b border-[--color-surface-border] px-6">
          <div className="flex items-center gap-2.5 md:hidden">
            <div className="flex h-6 w-6 items-center justify-center rounded-[--radius-sm] bg-[--color-brand-500]">
              <OctagonAlert className="h-3.5 w-3.5 text-white" />
            </div>
            <span className="text-sm font-semibold text-[oklch(92%_0.01_250)]">Catalyst</span>
          </div>
          <div className="hidden md:block" />
          <Link to={IS_SAAS ? '/app/dashboard' : '/'}>
            <Button variant="ghost" size="icon-sm" aria-label="Exit wizard">
              <X className="h-4 w-4" />
            </Button>
          </Link>
        </header>

        <div className="flex-1 overflow-auto">
          <Outlet />
        </div>
      </div>
    </div>
  )
}
