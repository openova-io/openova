import { useEffect } from 'react'
import { AnimatePresence, motion } from 'framer-motion'
import { Link, useNavigate } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { useSession } from '@/shared/lib/useSession'
import { useInflightDeployment } from '@/shared/lib/useInflightDeployment'
import { DETECTED_MODE } from '@/shared/lib/detectMode'
import { StepOrg }         from './steps/StepOrg'
import { StepDomain }      from './steps/StepDomain'
import { StepTopology }    from './steps/StepTopology'
import { StepProvider }    from './steps/StepProvider'
import { StepCredentials } from './steps/StepCredentials'
import { StepComponents }  from './steps/StepComponents'
import { StepMarketplace } from './steps/StepMarketplace'
import { StepReview }      from './steps/StepReview'
import { StepSuccess }     from './steps/StepSuccess'
import { StepNSDelegation } from './steps/StepNSDelegation'

// Step order (must match WIZARD_STEPS in WizardLayout.tsx exactly):
//
//   1. StepOrg          — org profile (industry / size / HQ / compliance).
//                         The admin-email field was decommissioned in
//                         #762; orgEmail is now stamped from
//                         session.email by the catalyst-api on
//                         POST /v1/deployments (PR #759).
//   2. StepTopology     — template, region count, HA flag, AIR-GAP add-on.
//                         Decides how many region rows the next step needs.
//   3. StepProvider     — per-region: cloud provider + provider's region +
//                         that provider's control-plane SKU + worker SKU +
//                         count. SKU vocabulary is per-provider, which is
//                         why sizing lives here, not in topology.
//   4. StepComponents   — unified marketplace catalog.
//   5. StepMarketplace  — opt into Marketplace mode (issue #710 wave 3a).
//                         The toggle decides whether the Sovereign exposes
//                         a per-tenant SaaS storefront at
//                         marketplace.<sovereign-fqdn>.
//   6. StepDomain       — pool subdomain or BYO domain (admin email
//                         decommissioned in #762).
//   7. StepCredentials  — API tokens (per chosen provider) + SSH key.
//                         Moved from position 4 to 7 in #973 so the
//                         cloud API token is the final input captured
//                         before Review — operators shape the
//                         deployment first, hand over keys last.
//   8. StepReview       — single source of truth for the POST body.
//   9. StepSuccess      — provisioning result.
//  10. StepNSDelegation — post-handover parent-zone NS delegation
//                         (omantel.omani.works → Sovereign-owned PowerDNS).
//                         Closes #374. Pure runbook-emitter by default;
//                         "auto-apply" toggle gates a stub catalyst-api
//                         endpoint that PDM will wire in Phase 8.
const STEPS = [
  StepOrg,
  StepTopology,
  StepProvider,
  StepComponents,
  StepMarketplace,
  StepDomain,
  StepCredentials,
  StepReview,
  StepSuccess,
  StepNSDelegation,
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

  // Issue #747 — auto-redirect a signed-in operator who already owns
  // an in-flight deployment back to /provision/<id>. This is the bug
  // surfaced when the founder refreshed the wizard tab during otech90
  // provisioning and lost the progress page. We deliberately wait
  // until the session is resolved before deciding — running the
  // redirect during session.loading would race the cookie check and
  // bounce an anonymous visitor erroneously.
  const navigate = useNavigate()
  const session = useSession()
  const { inflight, completed } = useInflightDeployment({
    ownerEmail: session.email,
    enabled: !session.loading && session.signedIn,
  })

  useEffect(() => {
    if (session.loading) return
    if (!session.signedIn) return
    if (!inflight) return
    // replace:true so the wizard URL doesn't sit in history — a
    // back-button press from /provision/<id> should land on the
    // referrer, not on a doomed wizard step.
    //
    // Target depends on mode:
    //   • mothership (catalyst-zero): /provision/$deploymentId/dashboard
    //     — the parameterised mothership URL where deploymentId scopes
    //     the surface.
    //   • Sovereign self-mode: /dashboard (clean root, sovereign is
    //     implicit from hostname).
    //
    // Bug history: pre-fix this called navigate({to:'/dashboard',
    // params:{deploymentId}}) unconditionally. On the mothership the
    // bare /dashboard matched the Sovereign-Console clean-root route
    // which renders SovereignConsoleLayout — that layout's mothership
    // guard then redirected back to /sovereign/, indexRoute redirected
    // to /wizard, WizardPage saw inflight again and looped.
    if (DETECTED_MODE.mode === 'sovereign') {
      navigate({
        to: '/dashboard',
        replace: true,
      })
    } else {
      navigate({
        to: '/provision/$deploymentId/dashboard' as never,
        params: { deploymentId: inflight.id } as never,
        replace: true,
      })
    }
  }, [session.loading, session.signedIn, inflight, navigate])

  useEffect(() => {
    document.getElementById('wizard-body')?.scrollTo({ top: 0, behavior: 'instant' as ScrollBehavior })
  }, [currentStep])

  // While the redirect is pending render nothing — surfacing a
  // half-rendered Step 1 for one frame before TanStack Router replaces
  // the route is jarring.
  if (!session.loading && session.signedIn && inflight) {
    return null
  }

  // History banner — operator has at least one completed/wiped/failed
  // deployment but no live one. Render a single-line strip pointing at
  // /deployments so the previous run is easy to reopen.
  const hasHistory =
    !session.loading && session.signedIn && !inflight && completed.length > 0

  return (
    <>
      {hasHistory && (
        <div
          role="status"
          data-testid="wizard-history-banner"
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 12,
            padding: '8px 16px',
            margin: '0 0 16px 0',
            background: 'rgba(56,189,248,0.08)',
            border: '1px solid rgba(56,189,248,0.18)',
            borderRadius: 8,
            fontSize: 13,
            color: 'var(--wiz-text-md)',
          }}
        >
          <span style={{ flex: 1 }}>
            You have {completed.length === 1 ? 'a previous deployment' : `${completed.length} previous deployments`} on
            file.
          </span>
          <Link
            to={'/deployments' as never}
            data-testid="wizard-history-banner-link"
            style={{
              padding: '4px 10px',
              borderRadius: 6,
              background: 'rgba(56,189,248,0.15)',
              color: '#38bdf8',
              fontSize: 12,
              fontWeight: 600,
              textDecoration: 'none',
              border: '1px solid rgba(56,189,248,0.35)',
            }}
          >
            View your previous deployments
          </Link>
        </div>
      )}
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
    </>
  )
}
