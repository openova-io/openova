/**
 * ApplicationCard — pixel-port of the `.app-card` rendered by
 * `core/admin/src/components/CatalogPage.svelte`. The geometry is
 * identical: 108px tall flex row, brand-coloured logo tile (full-
 * height square, 10px radius), body column with a name-line, a 2-line
 * description clamp, and a chip row with a left-edge mask gradient.
 *
 * Departures from the canonical admin card — driven by the provision
 * context, not by aesthetic choice:
 *
 *   1. The card is a Link (not a div). Click opens the per-Application
 *      page (logs / deps / status / overview tabs).
 *   2. The chip row carries a STATUS PILL (pending / installing /
 *      installed / failed / degraded) instead of the canonical FREE /
 *      SYSTEM chips. Status is the single most operationally-relevant
 *      datum for an Application mid-install.
 *   3. The "+ MySQL" / "+ PostgreSQL" / etc. dependency chips remain —
 *      same component-graph dependencies the admin catalog renders,
 *      sourced from the wizard's componentGroups module instead of the
 *      admin API's `app.dependencies` array.
 *
 * The data flow stays untouched — `applicationCatalog.ts`, `logoTone.ts`
 * are the same modules. Only the VISUAL layer is replaced.
 */

import { Link } from '@tanstack/react-router'
import { getLogoToneStyle } from '@/pages/wizard/steps/logoTone'
import { findComponent } from '@/pages/wizard/steps/componentGroups'
import type { ApplicationDescriptor } from './applicationCatalog'
import type { ApplicationStatus } from './eventReducer'
import { StatusPill } from './StatusPill'

interface ApplicationCardProps {
  app: ApplicationDescriptor
  status: ApplicationStatus
  deploymentId: string
}

export function ApplicationCard({ app, status, deploymentId }: ApplicationCardProps) {
  const tone = getLogoToneStyle(app.bareId)
  const letter = (app.title[0] ?? '?').toUpperCase()

  return (
    <Link
      to="/provision/$deploymentId/app/$componentId"
      params={{ deploymentId, componentId: app.id }}
      className="app-card"
      data-testid={`app-card-${app.id}`}
      data-status={status}
      data-bootstrap={app.bootstrapKit ? 'true' : 'false'}
      aria-label={`${app.title} application — ${status}`}
    >
      {app.logoUrl ? (
        <span
          className="app-logo"
          aria-hidden
          style={{
            background: tone.background,
            border: `1px solid ${tone.border}`,
            display: 'inline-flex',
            alignItems: 'center',
            justifyContent: 'center',
            padding: 6,
            boxSizing: 'border-box',
          }}
        >
          <img
            src={app.logoUrl}
            alt=""
            loading="lazy"
            data-testid={`app-logo-${app.id}`}
            style={{ width: '100%', height: '100%', objectFit: 'contain', display: 'block' }}
          />
        </span>
      ) : (
        <span
          className="app-icon"
          aria-hidden
          style={{
            background: tone.background,
            color: tone.text,
            border: `1px solid ${tone.border}`,
          }}
        >
          {letter}
        </span>
      )}

      <div className="app-body">
        <div className="app-top">
          <span className="app-name" title={app.title}>
            {app.title}
          </span>
          <span
            className="app-cat"
            data-testid={`app-family-${app.id}`}
            title={`${app.familyName} family`}
          >
            {app.familyName}
          </span>
        </div>
        <p className="app-desc">{app.description || '—'}</p>
        <div className="app-chips">
          <StatusPill status={status} testId={`app-status-${app.id}`} />
          {app.bootstrapKit && (
            <span className="chip chip-system" data-testid={`app-bootstrap-${app.id}`}>
              SYSTEM
            </span>
          )}
          {app.dependencies.slice(0, 3).map((depId) => {
            const dep = findComponent(depId)
            const depLabel = dep?.name ?? depId
            return (
              <span
                key={depId}
                className="chip chip-dep"
                data-testid={`app-dep-${app.id}-${depId}`}
                title={`Bundled dependency: ${depLabel}`}
              >
                + {depLabel}
              </span>
            )
          })}
        </div>
      </div>
    </Link>
  )
}
