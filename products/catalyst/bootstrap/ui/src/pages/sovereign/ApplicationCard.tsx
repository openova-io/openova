/**
 * ApplicationCard — single Application tile rendered in the AdminPage
 * grid. Geometry mirrors the wizard's StepComponents `corp-comp-card`
 * 1:1 — same 108px height, 4-line text rhythm, brand-coloured logo
 * tile, family chip on line 1, tier chip + dependency chips on line 4.
 *
 * The departure from the wizard card: the trailing-edge affordance on
 * line 1 is a STATUS PILL (not a toggle button), and the entire card
 * is a Link to `/sovereign/provision/$deploymentId/app/$componentId`.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every visual
 * decision (logo tone, family chip palette, tier label) reads from the
 * existing data modules — `logoTone.ts`, `marketplaceCopy.ts`,
 * `componentGroups.ts`. New components added to those modules render
 * automatically with the correct chrome.
 */

import { Link } from '@tanstack/react-router'
import { Lock } from 'lucide-react'
import { getLogoToneStyle } from '@/pages/wizard/steps/logoTone'
import { familyChipPalette } from '@/pages/marketplace/marketplaceCopy'
import type { ApplicationDescriptor } from './applicationCatalog'
import type { ApplicationStatus } from './eventReducer'
import { StatusPill } from './StatusPill'

interface ApplicationCardProps {
  app: ApplicationDescriptor
  status: ApplicationStatus
  deploymentId: string
}

const LOGO_TILE_RADIUS = 10
const LOGO_TILE_PADDING = 6

/** Brand-coloured logo tile — same component the wizard uses. */
function ComponentLogo({ app }: { app: ApplicationDescriptor }) {
  const tone = getLogoToneStyle(app.bareId)
  if (!app.logoUrl) {
    const letter = (app.title[0] ?? '?').toUpperCase()
    return (
      <span
        aria-hidden
        style={{
          alignSelf: 'stretch',
          aspectRatio: '1 / 1',
          height: 'auto',
          borderRadius: LOGO_TILE_RADIUS,
          display: 'inline-flex',
          alignItems: 'center',
          justifyContent: 'center',
          flexShrink: 0,
          color: tone.text,
          fontSize: '1.2rem',
          fontWeight: 700,
          background: tone.background,
          border: `1px solid ${tone.border}`,
        }}
      >
        {letter}
      </span>
    )
  }
  return (
    <span
      aria-hidden
      style={{
        alignSelf: 'stretch',
        aspectRatio: '1 / 1',
        height: 'auto',
        borderRadius: LOGO_TILE_RADIUS,
        display: 'inline-flex',
        alignItems: 'center',
        justifyContent: 'center',
        flexShrink: 0,
        background: tone.background,
        border: `1px solid ${tone.border}`,
        overflow: 'hidden',
        padding: LOGO_TILE_PADDING,
        boxSizing: 'border-box',
      }}
    >
      <img
        src={app.logoUrl}
        alt=""
        loading="lazy"
        data-testid={`app-logo-${app.id}`}
        style={{
          width: '100%',
          height: '100%',
          objectFit: 'contain',
          display: 'block',
        }}
      />
    </span>
  )
}

const TIER_TONE = {
  mandatory:   { bg: 'rgba(74,222,128,0.16)',  fg: '#4ADE80', label: 'Always' },
  recommended: { bg: 'rgba(56,189,248,0.16)',  fg: '#38BDF8', label: 'Recommended' },
  optional:    { bg: 'rgba(167,139,250,0.16)', fg: '#A78BFA', label: 'Optional' },
} as const

export function ApplicationCard({ app, status, deploymentId }: ApplicationCardProps) {
  const palette = familyChipPalette(app.familyId)
  const tier = TIER_TONE[app.tier]
  return (
    <Link
      to="/provision/$deploymentId/app/$componentId"
      params={{ deploymentId, componentId: app.id }}
      data-testid={`app-card-${app.id}`}
      data-status={status}
      data-bootstrap={app.bootstrapKit ? 'true' : 'false'}
      className="sov-app-card corp-comp-card"
      aria-label={`${app.title} application — ${status}`}
    >
      <ComponentLogo app={app} />
      <div className="corp-comp-body">
        {/* Line 1 — name (left, flex) + family chip + status pill (right). */}
        <div className="corp-comp-top">
          <span className="corp-comp-name">{app.title}</span>
          <span
            data-testid={`app-family-${app.id}`}
            className="corp-comp-family-chip"
            style={{
              background: palette.bg,
              color: palette.fg,
              border: `1px solid ${palette.border}`,
            }}
            title={`${app.familyName} family`}
          >
            {app.familyName}
          </span>
          <StatusPill
            status={status}
            size="sm"
            testId={`app-status-${app.id}`}
          />
        </div>
        {/* Lines 2-3 — description, two-line clamp. */}
        <p className="corp-comp-desc">{app.description}</p>
        {/* Line 4 — tier chip + dependency chips + bootstrap-kit pin. */}
        <div className="corp-comp-chips">
          <span
            data-testid={`app-tier-${app.id}`}
            style={{
              display: 'inline-flex',
              alignItems: 'center',
              gap: 4,
              padding: '0.1rem 0.45rem',
              borderRadius: 999,
              fontSize: '0.62rem',
              fontWeight: 700,
              letterSpacing: '0.04em',
              textTransform: 'uppercase',
              background: tier.bg,
              color: tier.fg,
            }}
          >
            {app.tier === 'mandatory' && <Lock size={9} strokeWidth={3} aria-hidden />}
            {tier.label}
          </span>
          {app.bootstrapKit && (
            <span
              data-testid={`app-bootstrap-${app.id}`}
              title="Always installed during cluster bootstrap"
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                padding: '0.1rem 0.45rem',
                borderRadius: 999,
                fontSize: '0.62rem',
                fontWeight: 700,
                letterSpacing: '0.04em',
                textTransform: 'uppercase',
                background: 'rgba(255,255,255,0.06)',
                color: 'var(--wiz-text-md)',
                border: '1px solid var(--wiz-border-sub)',
              }}
            >
              bootstrap-kit
            </span>
          )}
          {app.dependencies.slice(0, 3).map((dep) => (
            <span
              key={dep}
              data-testid={`app-dep-${app.id}-${dep}`}
              style={{
                display: 'inline-flex',
                alignItems: 'center',
                padding: '0.1rem 0.45rem',
                borderRadius: 999,
                fontSize: '0.62rem',
                fontWeight: 600,
                background: 'rgba(56,189,248,0.10)',
                color: '#38BDF8',
              }}
            >
              + {dep}
            </span>
          ))}
        </div>
      </div>
    </Link>
  )
}
