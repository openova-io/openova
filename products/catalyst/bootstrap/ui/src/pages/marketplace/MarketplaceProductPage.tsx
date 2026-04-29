/**
 * MarketplaceProductPage — long-form product / component detail page.
 *
 * Reachable from a card-body click in the wizard grid. Surface contract:
 *
 *   • Hero title with version-style metadata (tier + family chip).
 *   • Long-form positioning + integration paragraphs from COMPONENT_COPY.
 *   • Highlights bullets — feature surface.
 *   • Dependency graph: depends-on (this component pulls in X) and
 *     depended-on-by (X needs this component).
 *   • Family chip linking to the family portfolio page.
 *   • Upstream project link.
 *   • Select / Deselect CTA toggling the wizard store. Mandatory components
 *     show a read-only "INSTALLED ON EVERY SOVEREIGN" pill — they can't be
 *     toggled by design.
 *   • Back-to-wizard preserves wizard state (zustand + persist).
 */

import { useNavigate, useParams, Link } from '@tanstack/react-router'
import { ArrowLeft, ExternalLink, Lock, Plus, Check } from 'lucide-react'
import { useWizardStore } from '@/entities/deployment/store'
import {
  ALL_COMPONENTS,
  findComponent,
  findDependents,
  findProduct,
  type ComponentEntry,
} from '@/pages/wizard/steps/componentGroups'
import { componentCopy, familyChipPalette } from './marketplaceCopy'

interface DepBadgeProps {
  entry: ComponentEntry
}

function DepBadge({ entry }: DepBadgeProps) {
  const palette = familyChipPalette(entry.product)
  return (
    <Link
      to="/marketplace/product/$componentId"
      params={{ componentId: entry.id }}
      data-testid={`marketplace-dep-${entry.id}`}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: '0.4rem',
        padding: '0.4rem 0.75rem',
        borderRadius: 8,
        background: 'var(--wiz-bg-sub)',
        border: '1px solid var(--wiz-border-sub)',
        textDecoration: 'none',
        color: 'var(--wiz-text-md)',
        fontSize: '0.82rem',
        transition: 'border-color 0.15s, color 0.15s',
      }}
      onMouseEnter={(e) => {
        e.currentTarget.style.borderColor = palette.border
        e.currentTarget.style.color = 'var(--wiz-text-hi)'
      }}
      onMouseLeave={(e) => {
        e.currentTarget.style.borderColor = 'var(--wiz-border-sub)'
        e.currentTarget.style.color = 'var(--wiz-text-md)'
      }}
    >
      <span
        aria-hidden
        style={{
          width: 8,
          height: 8,
          borderRadius: 2,
          background: palette.fg,
        }}
      />
      <strong style={{ fontWeight: 600 }}>{entry.name}</strong>
      <span style={{ color: 'var(--wiz-text-sub)' }}>· {entry.groupName}</span>
    </Link>
  )
}

export function MarketplaceProductPage() {
  const { componentId } = useParams({ from: '/marketplace/product/$componentId' })
  const navigate = useNavigate()
  const entry = findComponent(componentId)
  const store = useWizardStore()
  const selected = store.selectedComponents.includes(componentId)

  if (!entry) {
    return (
      <main className="marketplace-shell" data-testid="marketplace-product-not-found">
        <button
          type="button"
          className="marketplace-back"
          onClick={() => navigate({ to: '/wizard' })}
          data-testid="marketplace-back"
        >
          <ArrowLeft size={14} aria-hidden /> Back to wizard
        </button>
        <h1 className="marketplace-title">Component not found</h1>
        <p style={{ color: 'var(--wiz-text-md)' }}>
          No component is registered under <code>{componentId}</code>.
        </p>
        <ProductShellStyles />
      </main>
    )
  }

  const family = findProduct(entry.product)
  const palette = familyChipPalette(entry.product)
  const copy = componentCopy(entry.id)
  const directDeps = (entry.dependencies ?? [])
    .map((id) => findComponent(id))
    .filter((c): c is ComponentEntry => !!c)
  const directDependents = findDependents(entry.id)
    .map((id) => findComponent(id))
    .filter((c): c is ComponentEntry => !!c)

  const isMandatory = entry.tier === 'mandatory'

  function handleToggle() {
    if (isMandatory) return
    if (selected) {
      // The wizard's confirm-cascade dialog only fires inside the wizard; on
      // this surface we go straight to the store, which itself protects
      // mandatory ids and cascades non-mandatory dependents identically.
      store.removeComponent(entry!.id)
    } else {
      store.addComponent(entry!.id)
    }
  }

  return (
    <main className="marketplace-shell" data-testid={`marketplace-product-${entry.id}`}>
      <button
        type="button"
        className="marketplace-back"
        onClick={() => navigate({ to: '/wizard' })}
        data-testid="marketplace-back"
      >
        <ArrowLeft size={14} aria-hidden /> Back to wizard
      </button>

      <header
        className="marketplace-hero"
        style={{ borderColor: palette.border }}
        data-testid="marketplace-product-hero"
      >
        <div
          style={{
            display: 'flex',
            justifyContent: 'space-between',
            alignItems: 'flex-start',
            gap: '1rem',
            flexWrap: 'wrap',
          }}
        >
          <div style={{ minWidth: 0, flex: '1 1 320px' }}>
            {family && (
              <Link
                to="/marketplace/family/$familyId"
                params={{ familyId: family.id }}
                data-testid="marketplace-product-family-chip"
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: '0.35rem',
                  padding: '0.25rem 0.65rem',
                  borderRadius: 999,
                  background: palette.bg,
                  color: palette.fg,
                  border: `1px solid ${palette.border}`,
                  textDecoration: 'none',
                  fontSize: '0.7rem',
                  fontWeight: 700,
                  letterSpacing: '0.06em',
                  textTransform: 'uppercase',
                  marginBottom: '0.85rem',
                }}
              >
                {family.name} family
              </Link>
            )}
            <h1 className="marketplace-title">{entry.name}</h1>
            <p className="marketplace-subtitle">{entry.desc}</p>
          </div>

          <div
            style={{
              display: 'flex',
              flexDirection: 'column',
              alignItems: 'flex-end',
              gap: '0.5rem',
              flexShrink: 0,
            }}
          >
            {isMandatory ? (
              <span
                data-testid="marketplace-product-tier-pill"
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: '0.35rem',
                  padding: '0.45rem 0.85rem',
                  borderRadius: 999,
                  background: 'rgba(74,222,128,0.16)',
                  color: '#4ADE80',
                  fontSize: '0.75rem',
                  fontWeight: 700,
                  letterSpacing: '0.04em',
                  textTransform: 'uppercase',
                  border: '1px solid rgba(74,222,128,0.35)',
                }}
              >
                <Lock size={11} strokeWidth={3} aria-hidden /> Installed on every Sovereign
              </span>
            ) : (
              <button
                type="button"
                onClick={handleToggle}
                data-testid="marketplace-product-toggle"
                aria-pressed={selected}
                style={{
                  display: 'inline-flex',
                  alignItems: 'center',
                  gap: '0.4rem',
                  padding: '0.55rem 1.1rem',
                  borderRadius: 8,
                  border: '1px solid ' + (selected ? '#4ADE80' : 'rgba(var(--wiz-accent-ch), 1)'),
                  background: selected ? 'rgba(74,222,128,0.12)' : 'rgba(var(--wiz-accent-ch), 1)',
                  color: selected ? '#4ADE80' : '#fff',
                  font: 'inherit',
                  fontSize: '0.88rem',
                  fontWeight: 600,
                  cursor: 'pointer',
                  transition: 'background 0.15s, transform 0.1s, color 0.15s',
                }}
              >
                {selected ? (
                  <>
                    <Check size={14} strokeWidth={3} aria-hidden /> Selected — click to remove
                  </>
                ) : (
                  <>
                    <Plus size={14} strokeWidth={2.5} aria-hidden /> Add to Sovereign
                  </>
                )}
              </button>
            )}
            <span
              data-testid="marketplace-product-tier"
              style={{
                fontSize: '0.7rem',
                fontWeight: 600,
                padding: '0.15rem 0.5rem',
                borderRadius: 999,
                background:
                  entry.tier === 'mandatory'
                    ? 'rgba(74,222,128,0.16)'
                    : entry.tier === 'recommended'
                      ? 'rgba(56,189,248,0.16)'
                      : 'rgba(167,139,250,0.16)',
                color:
                  entry.tier === 'mandatory'
                    ? '#4ADE80'
                    : entry.tier === 'recommended'
                      ? '#38BDF8'
                      : '#A78BFA',
                textTransform: 'uppercase',
                letterSpacing: '0.04em',
              }}
            >
              {entry.tier}
            </span>
          </div>
        </div>
      </header>

      <section className="marketplace-section">
        <h2 className="marketplace-section-title">What it does</h2>
        <p className="marketplace-paragraph">{copy.positioning}</p>
        <p className="marketplace-paragraph">{copy.integration}</p>
      </section>

      {copy.highlights.length > 0 && (
        <section className="marketplace-section">
          <h2 className="marketplace-section-title">Highlights</h2>
          <ul
            className="marketplace-bullets"
            data-testid="marketplace-product-highlights"
          >
            {copy.highlights.map((h, i) => (
              <li key={i}>{h}</li>
            ))}
          </ul>
        </section>
      )}

      {(directDeps.length > 0 || directDependents.length > 0) && (
        <section className="marketplace-section">
          <h2 className="marketplace-section-title">Dependency graph</h2>
          {directDeps.length > 0 && (
            <div style={{ marginBottom: '1rem' }}>
              <p className="marketplace-paragraph" style={{ marginBottom: '0.5rem' }}>
                <strong style={{ color: 'var(--wiz-text-hi)' }}>{entry.name}</strong> requires:
              </p>
              <div
                className="marketplace-deps"
                data-testid="marketplace-product-depends"
              >
                {directDeps.map((d) => (
                  <DepBadge key={d.id} entry={d} />
                ))}
              </div>
            </div>
          )}
          {directDependents.length > 0 && (
            <div>
              <p className="marketplace-paragraph" style={{ marginBottom: '0.5rem' }}>
                Depended on by:
              </p>
              <div
                className="marketplace-deps"
                data-testid="marketplace-product-dependents"
              >
                {directDependents.map((d) => (
                  <DepBadge key={d.id} entry={d} />
                ))}
              </div>
            </div>
          )}
        </section>
      )}

      <section className="marketplace-section">
        <h2 className="marketplace-section-title">Upstream project</h2>
        <p className="marketplace-paragraph">
          <a
            href={copy.upstreamUrl}
            target="_blank"
            rel="noopener noreferrer"
            data-testid="marketplace-product-upstream"
            style={{
              color: 'var(--wiz-accent)',
              fontWeight: 600,
              display: 'inline-flex',
              alignItems: 'center',
              gap: '0.35rem',
            }}
          >
            {copy.upstreamLabel}
            <ExternalLink size={12} aria-hidden />
          </a>
        </p>
      </section>

      <ProductShellStyles />
    </main>
  )
}

/** Shell styles — duplicated identical block so each page is self-contained. */
function ProductShellStyles() {
  return (
    <style>{`
      .marketplace-shell {
        max-width: 980px;
        margin: 0 auto;
        padding: 2rem 1.25rem 4rem;
        color: var(--wiz-text-md);
        font-family: Inter, ui-sans-serif, system-ui, sans-serif;
      }
      .marketplace-back {
        display: inline-flex;
        align-items: center;
        gap: 0.4rem;
        padding: 0.45rem 0.85rem;
        background: transparent;
        border: 1px solid var(--wiz-border-sub);
        border-radius: 7px;
        color: var(--wiz-text-md);
        cursor: pointer;
        font: inherit;
        font-size: 0.85rem;
        margin-bottom: 1.5rem;
        transition: border-color 0.15s, color 0.15s;
      }
      .marketplace-back:hover {
        border-color: rgba(var(--wiz-accent-ch), 0.6);
        color: var(--wiz-text-hi);
      }
      .marketplace-hero {
        padding: 1.5rem 1.6rem;
        border-radius: 14px;
        border: 1.5px solid var(--wiz-border-sub);
        background: var(--wiz-bg-sub);
        margin-bottom: 2rem;
      }
      .marketplace-title {
        margin: 0;
        font-size: 2rem;
        font-weight: 700;
        color: var(--wiz-text-hi);
        letter-spacing: -0.01em;
      }
      .marketplace-subtitle {
        margin: 0.35rem 0 0;
        font-size: 1rem;
        color: var(--wiz-text-md);
      }
      .marketplace-section {
        margin-bottom: 2rem;
      }
      .marketplace-section-title {
        margin: 0 0 0.85rem;
        font-size: 1.05rem;
        font-weight: 600;
        color: var(--wiz-text-hi);
        letter-spacing: -0.005em;
      }
      .marketplace-paragraph {
        margin: 0 0 0.85rem;
        line-height: 1.6;
        color: var(--wiz-text-md);
      }
      .marketplace-bullets {
        margin: 0;
        padding-left: 1.25rem;
        line-height: 1.6;
        color: var(--wiz-text-md);
      }
      .marketplace-bullets li { margin-bottom: 0.4rem; }
      .marketplace-deps {
        display: flex;
        flex-wrap: wrap;
        gap: 0.5rem;
      }
    `}</style>
  )
}

/** Re-export catalog symbols some tests / debug surfaces reach for. */
export const __MARKETPLACE_PRODUCT_CATALOG_SIZE__ = ALL_COMPONENTS.length
