import { useEffect } from 'react'
import { AnimatePresence, motion } from 'framer-motion'
import { Link } from '@tanstack/react-router'
import { useWizardStore } from '@/entities/deployment/store'
import { useSession } from '@/shared/lib/useSession'
import { useInflightDeployment } from '@/shared/lib/useInflightDeployment'
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
//                         a per-Org SaaS storefront at
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

  // The operator may legitimately want to start a SECOND provision
  // even when one is already in flight (founder framing 2026-05-05:
  // 'maybe I'll provision one more'). So we DON'T auto-redirect; we
  // surface a banner pointing at the inflight deployment's monitor
  // and let the operator continue stepping through the wizard.
  // Replaces the previous useEffect-redirect that bounced /sovereign/wizard
  // → /provision/<id>/dashboard unconditionally on signed-in load.
  const session = useSession()
  const { inflight, completed } = useInflightDeployment({
    ownerEmail: session.email,
    enabled: !session.loading && session.signedIn,
  })

  useEffect(() => {
    document.getElementById('wizard-body')?.scrollTo({ top: 0, behavior: 'instant' as ScrollBehavior })
  }, [currentStep])

  // History banner — operator has at least one completed/wiped/failed
  // deployment but no live one. Render a single-line strip pointing at
  // /deployments so the previous run is easy to reopen.
  const hasHistory =
    !session.loading && session.signedIn && !inflight && completed.length > 0

  // Inflight banner — operator has a deployment currently provisioning.
  // The wizard stays interactive (so they can start a SECOND), but a
  // strip at the top points at the inflight monitor for one-click resume.
  const hasInflight =
    !session.loading && session.signedIn && inflight

  return (
    <>
      {hasInflight && inflight && (
        <div
          role="status"
          data-testid="wizard-inflight-banner"
          style={{
            display: 'flex',
            alignItems: 'center',
            gap: 12,
            padding: '8px 16px',
            margin: '0 0 16px 0',
            background: 'rgba(168,85,247,0.10)',
            border: '1px solid rgba(168,85,247,0.30)',
            borderRadius: 8,
            fontSize: 13,
            color: 'var(--wiz-text-md)',
          }}
        >
          <span style={{ flex: 1 }}>
            You have a deployment in flight: <strong>{inflight.sovereignFQDN ?? inflight.id}</strong>.
          </span>
          <Link
            to={'/provision/$deploymentId/dashboard' as never}
            params={{ deploymentId: inflight.id } as never}
            data-testid="wizard-inflight-banner-link"
            style={{
              padding: '4px 10px',
              borderRadius: 6,
              background: 'rgba(168,85,247,0.20)',
              color: '#a855f7',
              fontSize: 12,
              fontWeight: 600,
              textDecoration: 'none',
              border: '1px solid rgba(168,85,247,0.45)',
            }}
          >
            Open monitor →
          </Link>
        </div>
      )}
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
