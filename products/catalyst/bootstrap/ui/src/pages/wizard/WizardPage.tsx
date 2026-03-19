import { AnimatePresence, motion } from 'framer-motion'
import { useWizardStore } from '@/entities/deployment/store'
import { StepOrg } from './steps/StepOrg'
import { StepProvider } from './steps/StepProvider'
import { StepCredentials } from './steps/StepCredentials'
import { StepInfrastructure } from './steps/StepInfrastructure'
import { StepComponents } from './steps/StepComponents'
import { StepReview } from './steps/StepReview'

const STEPS = [StepOrg, StepProvider, StepCredentials, StepInfrastructure, StepComponents, StepReview]

const variants = {
  enter: (dir: number) => ({ x: dir > 0 ? 40 : -40, opacity: 0 }),
  center: { x: 0, opacity: 1 },
  exit: (dir: number) => ({ x: dir < 0 ? 40 : -40, opacity: 0 }),
}

export function WizardPage() {
  const { currentStep } = useWizardStore()

  // currentStep is 1-indexed; array is 0-indexed
  const idx = Math.max(0, Math.min(currentStep - 1, STEPS.length - 1))
  const StepComponent = STEPS[idx]!

  return (
    <div className="flex items-start justify-center min-h-full p-6 md:p-10">
      <div className="w-full max-w-2xl">
        <AnimatePresence mode="wait" custom={currentStep}>
          <motion.div
            key={currentStep}
            custom={currentStep}
            variants={variants}
            initial="enter"
            animate="center"
            exit="exit"
            transition={{ duration: 0.22, ease: [0.4, 0, 0.2, 1] }}
          >
            <StepComponent />
          </motion.div>
        </AnimatePresence>
      </div>
    </div>
  )
}
