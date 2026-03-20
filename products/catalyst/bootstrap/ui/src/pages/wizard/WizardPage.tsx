import { useEffect } from 'react'
import { AnimatePresence, motion } from 'framer-motion'
import { useWizardStore } from '@/entities/deployment/store'
import { StepOrg }         from './steps/StepOrg'
import { StepTopology }    from './steps/StepTopology'
import { StepProvider }    from './steps/StepProvider'
import { StepCredentials } from './steps/StepCredentials'
import { StepComponents }  from './steps/StepComponents'
import { StepReview }      from './steps/StepReview'

const STEPS = [StepOrg, StepTopology, StepProvider, StepCredentials, StepComponents, StepReview]

const variants = {
  enter:  (dir: number) => ({ x: dir > 0 ? 32 : -32, opacity: 0 }),
  center: { x: 0, opacity: 1 },
  exit:   (dir: number) => ({ x: dir < 0 ? 32 : -32, opacity: 0 }),
}

export function WizardPage() {
  const { currentStep } = useWizardStore()
  const idx = Math.max(0, Math.min(currentStep - 1, STEPS.length - 1))
  const StepComponent = STEPS[idx]!

  useEffect(() => {
    document.getElementById('wizard-body')?.scrollTo({ top: 0, behavior: 'instant' as ScrollBehavior })
  }, [currentStep])

  return (
    <AnimatePresence mode="wait" custom={currentStep}>
      <motion.div
        key={currentStep}
        custom={currentStep}
        variants={variants}
        initial="enter"
        animate="center"
        exit="exit"
        transition={{ duration: 0.2, ease: [0.4, 0, 0.2, 1] }}
      >
        <StepComponent />
      </motion.div>
    </AnimatePresence>
  )
}
