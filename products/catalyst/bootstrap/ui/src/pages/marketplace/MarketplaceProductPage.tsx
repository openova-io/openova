/**
 * MarketplaceProductPage — long-form product / component detail page.
 *
 * Reachable from a card-body click in the wizard grid. Surface contract:
 *
 *   • Hero mirrors the canonical marketplace detail (.detail-hero in
 *     core/marketplace/src/components/AppDetail.svelte): logo on the left,
 *     name + tagline + meta-row (family + tier) in the centre, primary
 *     CTA on the right.
 *   • Long-form positioning + integration paragraphs from COMPONENT_COPY.
 *   • Highlights bullets — feature surface.
 *   • Dependency graph: depends-on (this component pulls in X) and
 *     depended-on-by (X needs this component). Each badge is a Link to
 *     the matching product detail page.
 *   • Family chip linking to the family portfolio page.
 *   • Upstream project link.
 *   • Select / Deselect CTA toggling the wizard store. Mandatory components
 *     show a read-only "Always installed" pill — they can't be toggled by
 *     design.
 *   • Back-to-wizard preserves wizard state (zustand + persist).
 *
 * Design language: every dimension and colour comes from the wizard's
 * --wiz-* tokens, which mirror the canonical marketplace's --color-*
 * tokens — same scale, same hierarchy, same flat surfaces.
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
import { MarketplaceShellStyles } from './MarketplaceFamilyPage'

interface DepBadgeProps {
  entry: ComponentEntry
}

/**
 * DepBadge — small surface chip with mono-font name + dim group tag.
 * Mirrors the canonical marketplace's .detail-dependencies li shape.
 */
function DepBadge({ entry }: DepBadgeProps) {
  return (
    <Link
      to="/marketplace/product/$componentId"
      params={{ componentId: entry.id }}
      data-testid={`marketplace-dep-${entry.id}`}
      className="mp-dep-tile"
    >
      <strong>{entry.name}</strong>
      <span className="mp-dep-tile-group">· {entry.groupName}</span>
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
      <main className="mp-shell" data-testid="marketplace-product-not-found">
        <button
          type="button"
          className="mp-back"
          onClick={() => navigate({ to: '/wizard' })}
          data-testid="marketplace-back"
        >
          <ArrowLeft size={14} aria-hidden /> Back to wizard
        </button>
        <h1 className="mp-title">Component not found</h1>
        <p className="mp-paragraph">
          No component is registered under <code>{componentId}</code>.
        </p>
        <MarketplaceShellStyles />
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
    <main className="mp-shell" data-testid={`marketplace-product-${entry.id}`}>
      <button
        type="button"
        className="mp-back"
        onClick={() => navigate({ to: '/wizard' })}
        data-testid="marketplace-back"
      >
        <ArrowLeft size={14} aria-hidden /> Back to wizard
      </button>

      {/* ── Hero ──────────────────────────────────────────────────────
            Mirrors the canonical .detail-hero row from AppDetail.svelte:
            logo on the left, name/tagline/meta in the centre, primary CTA
            on the right. Bordered only at the bottom. */}
      <header className="mp-product-hero" data-testid="marketplace-product-hero">
        {entry.logoUrl ? (
          <img
            src={entry.logoUrl}
            alt={`${entry.name} logo`}
            className="mp-product-logo"
          />
        ) : (
          <span className="mp-product-icon">{entry.name.charAt(0)}</span>
        )}

        <div className="mp-product-hero-body">
          <h1 className="mp-title">{entry.name}</h1>
          <p className="mp-subtitle">{entry.desc}</p>
          <div className="mp-meta-row">
            {family && (
              <Link
                to="/marketplace/family/$familyId"
                params={{ familyId: family.id }}
                data-testid="marketplace-product-family-chip"
                className="mp-meta-chip mp-meta-family"
                style={{
                  background: palette.bg,
                  color: palette.fg,
                  borderColor: palette.border,
                }}
              >
                {family.name}
              </Link>
            )}
            <span
              data-testid="marketplace-product-tier"
              className={`mp-meta-chip mp-meta-tier mp-meta-tier-${entry.tier}`}
            >
              {entry.tier}
            </span>
          </div>
        </div>

        {isMandatory ? (
          <span
            data-testid="marketplace-product-tier-pill"
            className="mp-cta mp-cta-locked"
          >
            <Lock size={12} strokeWidth={2.5} aria-hidden /> Always installed
          </span>
        ) : (
          <button
            type="button"
            onClick={handleToggle}
            data-testid="marketplace-product-toggle"
            aria-pressed={selected}
            className={`mp-cta ${selected ? 'mp-cta-added' : 'mp-cta-add'}`}
          >
            {selected ? (
              <>
                <Check size={14} strokeWidth={2.5} aria-hidden /> Remove from stack
              </>
            ) : (
              <>
                <Plus size={14} strokeWidth={2.5} aria-hidden /> Add to stack
              </>
            )}
          </button>
        )}
      </header>

      <section className="mp-section">
        <h2 className="mp-section-title">About</h2>
        <p className="mp-paragraph">{copy.positioning}</p>
        <p className="mp-paragraph">{copy.integration}</p>
      </section>

      {copy.highlights.length > 0 && (
        <section className="mp-section">
          <h2 className="mp-section-title">Highlights</h2>
          <ul
            className="mp-bullets"
            data-testid="marketplace-product-highlights"
          >
            {copy.highlights.map((h, i) => (
              <li key={i}>{h}</li>
            ))}
          </ul>
        </section>
      )}

      {(directDeps.length > 0 || directDependents.length > 0) && (
        <section className="mp-section">
          <h2 className="mp-section-title">Dependency graph</h2>
          {directDeps.length > 0 && (
            <div className="mp-dep-block">
              <p className="mp-paragraph mp-paragraph-lead">
                <strong>{entry.name}</strong> requires:
              </p>
              <div
                className="mp-dep-tile-row"
                data-testid="marketplace-product-depends"
              >
                {directDeps.map((d) => (
                  <DepBadge key={d.id} entry={d} />
                ))}
              </div>
            </div>
          )}
          {directDependents.length > 0 && (
            <div className="mp-dep-block">
              <p className="mp-paragraph mp-paragraph-lead">Depended on by:</p>
              <div
                className="mp-dep-tile-row"
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

      <section className="mp-section">
        <h2 className="mp-section-title">Upstream project</h2>
        <p className="mp-paragraph">
          <a
            href={copy.upstreamUrl}
            target="_blank"
            rel="noopener noreferrer"
            data-testid="marketplace-product-upstream"
            className="mp-link"
          >
            {copy.upstreamLabel}
            <ExternalLink size={12} aria-hidden />
          </a>
        </p>
      </section>

      <MarketplaceShellStyles />
      <ProductHeroStyles />
    </main>
  )
}

/**
 * Product-hero–specific styles. The shared shell styles (typography,
 * sections, bullets, dep chips) come from <MarketplaceShellStyles />;
 * this block adds the hero row geometry, the meta-chip variants, and the
 * primary CTA — all calibrated to mirror the canonical marketplace's
 * .detail-hero, .detail-meta, and .detail-add from AppDetail.svelte.
 */
function ProductHeroStyles() {
  return (
    <style>{`
      .mp-product-hero {
        display: flex;
        align-items: flex-start;
        gap: 1.2rem;
        padding: 1.5rem 0 1.75rem;
        border-bottom: 1px solid var(--wiz-border-sub);
        margin-bottom: 1.5rem;
        flex-wrap: wrap;
      }
      .mp-product-logo {
        width: 80px;
        height: 80px;
        border-radius: 18px;
        object-fit: cover;
        flex-shrink: 0;
        background: var(--wiz-bg-card);
        padding: 8px;
      }
      .mp-product-icon {
        width: 80px;
        height: 80px;
        border-radius: 18px;
        display: inline-flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
        color: var(--wiz-text-hi);
        background: rgba(var(--wiz-accent-ch), 0.12);
        font-size: 1.75rem;
        font-weight: 700;
      }
      .mp-product-hero-body {
        flex: 1 1 320px;
        min-width: 0;
      }
      .mp-meta-row {
        display: flex;
        flex-wrap: wrap;
        gap: 0.4rem;
        margin-top: 0.6rem;
      }

      /* Meta chips — match canonical .detail-meta span: 0.72rem font,
         weight 600, radius 4px, 12% accent tint. */
      .mp-meta-chip {
        display: inline-flex;
        align-items: center;
        padding: 0.2rem 0.55rem;
        border-radius: 4px;
        font-size: 0.72rem;
        font-weight: 600;
        text-decoration: none;
        border: 1px solid transparent;
        text-transform: capitalize;
        letter-spacing: 0.01em;
      }
      .mp-meta-family {
        transition: filter 0.15s;
      }
      .mp-meta-family:hover { filter: brightness(1.1); }

      .mp-meta-tier-mandatory {
        background: rgba(74, 222, 128, 0.16);
        color: #4ADE80;
      }
      .mp-meta-tier-recommended {
        background: rgba(56, 189, 248, 0.16);
        color: #38BDF8;
      }
      .mp-meta-tier-optional {
        background: rgba(167, 139, 250, 0.16);
        color: #A78BFA;
      }
      [data-theme="light"] .mp-meta-tier-mandatory { color: #047857; background: rgba(5, 150, 105, 0.12); }
      [data-theme="light"] .mp-meta-tier-recommended { color: #0369A1; background: rgba(2, 132, 199, 0.12); }
      [data-theme="light"] .mp-meta-tier-optional { color: #7C3AED; background: rgba(124, 58, 237, 0.12); }

      /* Primary CTA — mirrors .detail-add from AppDetail.svelte:
         0.65rem 1.5rem padding, radius 8px, weight 600, 0.88rem font. */
      .mp-cta {
        flex-shrink: 0;
        display: inline-flex;
        align-items: center;
        gap: 0.4rem;
        padding: 0.6rem 1.4rem;
        border-radius: 8px;
        border: 1px solid rgba(var(--wiz-accent-ch), 1);
        background: rgba(var(--wiz-accent-ch), 1);
        color: #fff;
        font: inherit;
        font-size: 0.88rem;
        font-weight: 600;
        cursor: pointer;
        text-decoration: none;
        transition: filter 0.15s, background 0.15s, color 0.15s, border-color 0.15s;
        white-space: nowrap;
      }
      .mp-cta-add:hover { filter: brightness(0.92); }
      .mp-cta-added {
        background: transparent;
        color: var(--wiz-text-sub);
        border-color: var(--wiz-border);
      }
      .mp-cta-added:hover {
        color: rgba(239, 68, 68, 1);
        border-color: rgba(239, 68, 68, 0.6);
      }
      .mp-cta-locked {
        background: rgba(var(--wiz-success-ch), 0.16);
        border-color: rgba(var(--wiz-success-ch), 0.35);
        color: rgba(var(--wiz-success-ch), 1);
        font-size: 0.78rem;
        padding: 0.5rem 1rem;
        cursor: default;
      }

      /* Dependency tile — small surface chip with mono-font name +
         dim group tag, mirroring .detail-dependencies li. */
      .mp-dep-block {
        margin-bottom: 1rem;
      }
      .mp-dep-block:last-child { margin-bottom: 0; }
      .mp-dep-tile-row {
        display: flex;
        flex-wrap: wrap;
        gap: 0.4rem;
      }
      .mp-dep-tile {
        display: inline-flex;
        align-items: baseline;
        gap: 0.35rem;
        padding: 0.3rem 0.65rem;
        background: var(--wiz-bg-sub);
        border: 1px solid var(--wiz-border-sub);
        border-radius: 6px;
        text-decoration: none;
        color: var(--wiz-text-md);
        font-size: 0.78rem;
        font-family: var(--font-mono, "JetBrains Mono", ui-monospace, monospace);
        transition: border-color 0.15s, color 0.15s;
      }
      .mp-dep-tile strong {
        font-weight: 600;
        color: var(--wiz-text-hi);
      }
      .mp-dep-tile-group {
        font-family: Inter, ui-sans-serif, system-ui, sans-serif;
        color: var(--wiz-text-sub);
        font-size: 0.72rem;
        text-transform: capitalize;
      }
      .mp-dep-tile:hover {
        border-color: rgba(var(--wiz-accent-ch), 0.6);
      }
      .mp-dep-tile:hover strong {
        color: var(--wiz-accent);
      }

      @media (max-width: 600px) {
        .mp-product-hero { gap: 1rem; }
        .mp-product-logo, .mp-product-icon { width: 64px; height: 64px; border-radius: 14px; font-size: 1.5rem; }
        .mp-cta { font-size: 0.85rem; padding: 0.55rem 1.1rem; }
      }
    `}</style>
  )
}

/** Re-export catalog symbols some tests / debug surfaces reach for. */
export const __MARKETPLACE_PRODUCT_CATALOG_SIZE__ = ALL_COMPONENTS.length
