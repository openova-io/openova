import { Outlet, Link } from '@tanstack/react-router'
import { X, Sun, Moon } from 'lucide-react'
import { IS_SAAS } from '@/shared/constants/env'
import { StepIndicator, type WizardStep } from '@/widgets/step-indicator/StepIndicator'
import { useWizardStore } from '@/entities/deployment/store'
import { Button } from '@/shared/ui/button'
import { useTheme } from '@/shared/lib/useTheme'

const WIZARD_STEPS: WizardStep[] = [
  { id: 1, label: 'Organisation',   description: 'Name, domain, contact' },
  { id: 2, label: 'Cloud provider', description: 'Select target cloud' },
  { id: 3, label: 'Credentials',    description: 'API access token' },
  { id: 4, label: 'Infrastructure', description: 'Regions, nodes, sizing' },
  { id: 5, label: 'Components',     description: 'Platform components' },
  { id: 6, label: 'Review',         description: 'Confirm and provision' },
]

function OpenOvaLogo({ className }: { className?: string }) {
  return (
    <svg
      viewBox="140 60 420 280"
      fill="none"
      xmlns="http://www.w3.org/2000/svg"
      className={className}
      aria-hidden="true"
    >
      <defs>
        <linearGradient id="oo-logo-grad" x1="0%" y1="0%" x2="100%" y2="0%">
          <stop offset="0%" stopColor="#38BDF8" />
          <stop offset="100%" stopColor="#818CF8" />
        </linearGradient>
      </defs>
      <path
        d="M 300 88.1966 A 150 150 0 1 0 350 200 A 150 150 0 1 1 400 311.8034"
        fill="none"
        stroke="url(#oo-logo-grad)"
        strokeWidth="80"
        strokeLinecap="butt"
      />
    </svg>
  )
}

export function WizardLayout() {
  const { currentStep, completedSteps, setStep } = useWizardStore()
  const { theme, toggle } = useTheme()

  return (
    <div className="flex min-h-dvh" style={{ backgroundColor: 'var(--color-surface-0)' }}>
      {/* Left sidebar */}
      <aside
        className="hidden md:flex md:w-64 lg:w-72 shrink-0 flex-col p-6"
        style={{
          backgroundColor: 'var(--color-surface-1)',
          borderRight: '1px solid var(--color-surface-border)',
        }}
      >
        {/* Logo + branding */}
        <div className="flex items-center gap-3 mb-10">
          <OpenOvaLogo className="h-8 w-8 shrink-0" />
          <div className="flex flex-col leading-tight">
            <span
              className="text-[11px] font-medium uppercase tracking-widest"
              style={{ color: 'var(--color-text-muted)' }}
            >
              OpenOva
            </span>
            <span
              className="text-sm font-semibold tracking-tight"
              style={{ color: 'var(--color-brand-500)' }}
            >
              Catalyst
            </span>
          </div>
        </div>

        <div className="mb-6">
          <p
            className="text-xs font-semibold uppercase tracking-wider mb-1"
            style={{ color: 'var(--color-text-disabled)' }}
          >
            New deployment
          </p>
          <p className="text-xs" style={{ color: 'var(--color-text-muted)' }}>
            Step {currentStep} of {WIZARD_STEPS.length}
          </p>
        </div>

        <StepIndicator
          steps={WIZARD_STEPS}
          currentStep={currentStep}
          completedSteps={completedSteps}
          onStepClick={setStep}
        />

        <div
          className="mt-auto pt-6"
          style={{ borderTop: '1px solid var(--color-surface-border)' }}
        >
          <p
            className="text-xs leading-relaxed"
            style={{ color: 'var(--color-text-muted)' }}
          >
            All provisioning runs in your cloud account. Credentials are used only during setup and never stored on our servers.
          </p>
        </div>
      </aside>

      {/* Main area */}
      <div className="flex flex-1 flex-col">
        {/* Top bar */}
        <header
          className="flex h-14 items-center justify-between px-6"
          style={{ borderBottom: '1px solid var(--color-surface-border)' }}
        >
          {/* Mobile logo */}
          <div className="flex items-center gap-2.5 md:hidden">
            <OpenOvaLogo className="h-6 w-6 shrink-0" />
            <span className="text-sm font-semibold" style={{ color: 'var(--color-brand-500)' }}>
              Catalyst
            </span>
          </div>
          <div className="hidden md:block" />

          <div className="flex items-center gap-1">
            {/* Theme toggle */}
            <Button
              variant="ghost"
              size="icon-sm"
              onClick={toggle}
              aria-label={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
              title={theme === 'dark' ? 'Light mode' : 'Dark mode'}
            >
              {theme === 'dark'
                ? <Sun className="h-4 w-4" />
                : <Moon className="h-4 w-4" />
              }
            </Button>

            {/* Exit */}
            <Link to={IS_SAAS ? '/app/dashboard' : '/'}>
              <Button variant="ghost" size="icon-sm" aria-label="Exit wizard">
                <X className="h-4 w-4" />
              </Button>
            </Link>
          </div>
        </header>

        <div className="flex-1 overflow-auto">
          <Outlet />
        </div>
      </div>
    </div>
  )
}
