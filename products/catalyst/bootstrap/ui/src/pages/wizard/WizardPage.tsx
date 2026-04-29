import { useEffect } from 'react'
import { AnimatePresence, motion } from 'framer-motion'
import { useWizardStore } from '@/entities/deployment/store'
import { StepOrg }         from './steps/StepOrg'
import { StepDomain }      from './steps/StepDomain'
import { StepTopology }    from './steps/StepTopology'
import { StepProvider }    from './steps/StepProvider'
import { StepCredentials } from './steps/StepCredentials'
import { StepComponents }  from './steps/StepComponents'
import { StepReview }      from './steps/StepReview'
import { StepSuccess }     from './steps/StepSuccess'

// Step order (must match WIZARD_STEPS in WizardLayout.tsx exactly):
//
//   1. StepOrg          — org profile (industry / size / HQ / compliance).
//                         The admin email lives in StepDomain (it pairs
//                         naturally with the Sovereign's external surface).
//   2. StepTopology     — template, region count, HA flag, AIR-GAP add-on.
//                         Decides how many region rows the next step needs.
//   3. StepProvider     — per-region: cloud provider + provider's region +
//                         that provider's control-plane SKU + worker SKU +
//                         count. SKU vocabulary is per-provider, which is
//                         why sizing lives here, not in topology.
//   4. StepCredentials  — API tokens (per chosen provider) + SSH key.
//   5. StepComponents   — unified marketplace catalog.
//   6. StepDomain       — pool subdomain or BYO domain + admin email.
//   7. StepReview       — single source of truth for the POST body.
//   8. StepSuccess      — provisioning result (terminal).
const STEPS = [
  StepOrg,
  StepTopology,
  StepProvider,
  StepCredentials,
  StepComponents,
  StepDomain,
  StepReview,
  StepSuccess,
]

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
