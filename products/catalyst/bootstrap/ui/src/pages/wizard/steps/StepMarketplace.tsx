/**
 * StepMarketplace — Marketplace mode opt-in (issue #710 wave 3a).
 *
 * Inserted between StepComponents and StepDomain. The toggle decides
 * whether the operator wants to turn this Sovereign into a SaaS
 * platform (per-Org subdomains, public storefront at
 * `marketplace.<sovereign-fqdn>`, isolated per-Org shells) or keep it
 * as a private single-estate install.
 *
 * Wiring (already in place — this step is the missing UI seam):
 *
 *   StepMarketplace toggle  →  store.marketplaceEnabled
 *                           →  StepReview POST /v1/deployments
 *                              { …, marketplaceEnabled: true }
 *                           →  catalyst-api provisioner.Request.MarketplaceEnabled
 *                           →  OpenTofu var marketplace_enabled
 *                           →  cloud-init → Flux substitute
 *                           →  bp-catalyst-platform values.yaml
 *                              ingress.marketplace.enabled = true
 *                           →  marketplace HTTPRoutes rendered
 *
 * The brand fields (name / tagline / primary colour) are PURELY
 * cosmetic and persist on the wizard state so a future settings
 * page can read them without re-prompting. The chart only consumes
 * the enabled flag for now (companion PRs land settings + catalog
 * admin surfaces).
 *
 * Visual style mirrors StepComponents: StepShell wrapper, card grid
 * within `--wiz-bg-sub` surfaces, body fields in `--wiz-bg-input`,
 * inline help text in `--wiz-text-sub`. Footer Back/Next buttons are
 * published via useStepNav() — same contract every other step uses.
 */

import { useWizardStore } from '@/entities/deployment/store'
import { resolveSovereignDomain } from '@/entities/deployment/model'
import { StepShell, useStepNav } from './_shared'

const FIELD_INPUT_STYLE: React.CSSProperties = {
  width: '100%',
  padding: '0.55rem 0.7rem',
  background: 'var(--wiz-bg-input)',
  border: '1px solid var(--wiz-border-sub)',
  borderRadius: 7,
  color: 'var(--wiz-text-hi)',
  font: 'inherit',
  fontSize: '0.88rem',
}

const FIELD_LABEL_STYLE: React.CSSProperties = {
  display: 'flex',
  flexDirection: 'column',
  gap: 4,
  fontSize: '0.78rem',
  fontWeight: 600,
  color: 'var(--wiz-text-md)',
  letterSpacing: '0.02em',
}

const FIELD_HELP_STYLE: React.CSSProperties = {
  fontSize: '0.74rem',
  fontWeight: 400,
  color: 'var(--wiz-text-sub)',
  lineHeight: 1.45,
}

export function StepMarketplace() {
  const { next, back } = useStepNav()
  const store = useWizardStore()

  const fqdn = resolveSovereignDomain(store)
  // Show a stable "marketplace.<fqdn>" preview when the user has filled
  // in the domain step ahead of this one; otherwise dim-fall back to a
  // generic placeholder so the operator still understands what the
  // toggle will produce.
  const marketplaceURL = fqdn ? `marketplace.${fqdn}` : 'marketplace.<your-sovereign-fqdn>'

  const enabled = store.marketplaceEnabled
  const brand = store.marketplaceBrand

  return (
    <StepShell
      title="Marketplace mode"
      description={
        'Marketplace mode turns this Sovereign into a multi-Organization SaaS platform. ' +
        'You publish apps from your unified catalog, customers sign up at the storefront, ' +
        'and each Organization runs in an isolated subdomain shell. Leave it off to provision a ' +
        'single-Organization private Sovereign — you can flip the toggle later from Settings.'
      }
      onNext={next}
      onBack={back}
    >
      <div style={{ display: 'flex', flexDirection: 'column', gap: '1rem' }}>
        {/* ── Toggle card ──────────────────────────────────────── */}
        <div
          data-testid="step-marketplace-toggle-card"
          style={{
            border: `1.5px solid ${enabled ? '#4ADE80' : 'var(--wiz-border-sub)'}`,
            borderRadius: 12,
            padding: '0.95rem 1.1rem',
            display: 'flex',
            alignItems: 'flex-start',
            gap: '1rem',
            transition: 'border-color 0.15s, background 0.15s',
            background: enabled
              ? 'color-mix(in srgb, #4ADE80 6%, var(--wiz-bg-sub))'
              : 'var(--wiz-bg-sub)',
          }}
        >
          <div style={{ flex: 1, minWidth: 0 }}>
            <div
              style={{
                fontSize: '0.95rem',
                fontWeight: 600,
                color: 'var(--wiz-text-hi)',
                marginBottom: 4,
              }}
            >
              Enable Marketplace mode
            </div>
            <p
              style={{
                margin: 0,
                color: 'var(--wiz-text-md)',
                fontSize: '0.82rem',
                lineHeight: 1.5,
              }}
            >
              Turn this Sovereign into a SaaS platform. Customers sign up at{' '}
              <code
                style={{
                  fontFamily: 'JetBrains Mono, monospace',
                  fontSize: '0.78rem',
                  color: 'var(--wiz-accent)',
                  background: 'rgba(56,189,248,0.08)',
                  padding: '0.05rem 0.35rem',
                  borderRadius: 4,
                }}
              >
                {marketplaceURL}
              </code>
              , get isolated per-Organization subdomains, and consume apps you publish from your
              unified catalog.
            </p>
          </div>

          {/* Pill switch — accessibility role="switch" plus the
              floating ball element. Clicking anywhere on the switch
              flips the store flag. */}
          <button
            type="button"
            role="switch"
            aria-checked={enabled}
            data-testid="step-marketplace-toggle"
            onClick={() => store.setMarketplaceEnabled(!enabled)}
            style={{
              flexShrink: 0,
              width: 44,
              height: 24,
              padding: 0,
              border: 'none',
              borderRadius: 999,
              cursor: 'pointer',
              position: 'relative',
              background: enabled ? '#4ADE80' : 'rgba(148,163,184,0.35)',
              transition: 'background 0.15s',
            }}
            aria-label={enabled ? 'Disable Marketplace mode' : 'Enable Marketplace mode'}
          >
            <span
              aria-hidden
              style={{
                position: 'absolute',
                top: 2,
                left: enabled ? 22 : 2,
                width: 20,
                height: 20,
                borderRadius: '50%',
                background: '#fff',
                transition: 'left 0.18s ease',
                boxShadow: '0 1px 3px rgba(0,0,0,0.25)',
              }}
            />
          </button>
        </div>

        {/* ── Brand fields (revealed when enabled) ─────────────── */}
        {enabled && (
          <div
            data-testid="step-marketplace-brand"
            style={{
              background: 'var(--wiz-bg-sub)',
              border: '1px solid var(--wiz-border-sub)',
              borderRadius: 12,
              padding: '1rem 1.1rem',
              display: 'flex',
              flexDirection: 'column',
              gap: '0.85rem',
            }}
          >
            <div>
              <h3
                style={{
                  margin: '0 0 0.25rem',
                  fontSize: '0.88rem',
                  fontWeight: 600,
                  color: 'var(--wiz-text-hi)',
                }}
              >
                Storefront branding
              </h3>
              <p
                style={{
                  margin: 0,
                  fontSize: '0.78rem',
                  color: 'var(--wiz-text-sub)',
                  lineHeight: 1.5,
                }}
              >
                Optional. All three fields can be left blank — the storefront falls back to
                platform defaults, and you can edit them later from the post-launch Settings
                page without re-running the wizard.
              </p>
            </div>

            <div
              style={{
                display: 'grid',
                gridTemplateColumns: 'repeat(auto-fit, minmax(220px, 1fr))',
                gap: '0.7rem',
              }}
            >
              <label style={FIELD_LABEL_STYLE}>
                Brand name
                <input
                  type="text"
                  data-testid="step-marketplace-brand-name"
                  placeholder="e.g. Acme Marketplace"
                  value={brand.name}
                  onChange={(e) => store.setMarketplaceBrand({ name: e.target.value })}
                  style={FIELD_INPUT_STYLE}
                  maxLength={64}
                />
                <span style={FIELD_HELP_STYLE}>Shown in the storefront header.</span>
              </label>

              <label style={FIELD_LABEL_STYLE}>
                Tagline
                <input
                  type="text"
                  data-testid="step-marketplace-brand-tagline"
                  placeholder="e.g. Apps for the regulated cloud"
                  value={brand.tagline}
                  onChange={(e) => store.setMarketplaceBrand({ tagline: e.target.value })}
                  style={FIELD_INPUT_STYLE}
                  maxLength={120}
                />
                <span style={FIELD_HELP_STYLE}>One-line subtitle below the brand name.</span>
              </label>

              <label style={FIELD_LABEL_STYLE}>
                Primary colour
                <div style={{ display: 'flex', gap: '0.4rem', alignItems: 'center' }}>
                  <input
                    type="text"
                    data-testid="step-marketplace-brand-color"
                    placeholder="#38BDF8"
                    value={brand.primaryColor}
                    onChange={(e) =>
                      store.setMarketplaceBrand({ primaryColor: e.target.value })
                    }
                    style={{ ...FIELD_INPUT_STYLE, fontFamily: 'JetBrains Mono, monospace' }}
                    maxLength={7}
                    pattern="^#?[0-9A-Fa-f]{6}$"
                  />
                  {/^#?[0-9A-Fa-f]{6}$/.test(brand.primaryColor) && (
                    <span
                      aria-hidden
                      data-testid="step-marketplace-brand-color-swatch"
                      style={{
                        width: 30,
                        height: 30,
                        flexShrink: 0,
                        borderRadius: 6,
                        border: '1px solid var(--wiz-border-sub)',
                        background: brand.primaryColor.startsWith('#')
                          ? brand.primaryColor
                          : `#${brand.primaryColor}`,
                      }}
                    />
                  )}
                </div>
                <span style={FIELD_HELP_STYLE}>
                  CSS hex (e.g. <code>#38BDF8</code>). Used as the storefront accent.
                </span>
              </label>
            </div>
          </div>
        )}
      </div>
    </StepShell>
  )
}
