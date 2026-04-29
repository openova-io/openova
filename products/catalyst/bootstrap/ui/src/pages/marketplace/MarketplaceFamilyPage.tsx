/**
 * MarketplaceFamilyPage — long-form family / portfolio page reachable from
 * a chip click on any wizard component card. Surface contract:
 *
 *   • Chrome reuses the wizard's design tokens. Layout mirrors the canonical
 *     marketplace at https://marketplace.openova.io/apps/ — flat hero,
 *     borderless sections divided by a 1px subtle border, 1rem section
 *     heads, 0.9rem body copy at line-height 1.7.
 *   • Title, tagline, multi-paragraph overview drawn from FAMILY_COPY.
 *   • Capability bullets surface what the operator gets when the family
 *     is installed.
 *   • Member list — every component in the family. Each entry shows the
 *     component logo, name, tier pill, and one-line description (mirrors
 *     the canonical "related apps" tile shape).
 *   • Family dependencies — every product the operator implicitly pulls in
 *     by installing this family.
 *   • "Back to wizard" returns to the wizard route. Wizard state is held
 *     in zustand + persist (localStorage) so navigation away does NOT
 *     drop selections.
 *
 * Design language: tokens come from --wiz-* (defined in
 * src/app/globals.css) which mirror the canonical marketplace's
 * --color-* tokens — same scale, same hierarchy, same flat surfaces.
 */

import { useNavigate, useParams, Link } from '@tanstack/react-router'
import { ArrowLeft, ExternalLink } from 'lucide-react'
import {
  PRODUCTS,
  componentsByProduct,
  findProduct,
  type ComponentEntry,
  type Product,
} from '@/pages/wizard/steps/componentGroups'
import { FAMILY_COPY, componentCopy, familyChipPalette } from './marketplaceCopy'
import { getLogoToneStyle } from '@/pages/wizard/steps/logoTone'

interface MemberRowProps {
  entry: ComponentEntry
}

/**
 * MemberRow — mirrors the canonical marketplace's .related-card tile:
 * 36×36 logo on the left, name (strong) + one-line tagline stacked on the
 * right. Hover transitions the border to the accent without lifting the
 * card; matches the marketplace's restrained motion.
 */
function MemberRow({ entry }: MemberRowProps) {
  const tone = getLogoToneStyle(entry.id)
  // Per-brand surface override — see logoTone.ts and the .mp-related-logo
  // CSS rule below. Inline style is the cleanest way to vary the tile
  // surface per-component without exploding into one CSS class per
  // component-id; the static rule still owns geometry (size, radius,
  // padding, object-fit), and inline style only overrides the
  // background / border colour pair driven by per-id brand metadata.
  const tileStyle: React.CSSProperties = {
    background: tone.background,
    borderColor: tone.border,
  }
  return (
    <Link
      to="/marketplace/product/$componentId"
      params={{ componentId: entry.id }}
      data-testid={`marketplace-family-member-${entry.id}`}
      className="mp-related-card"
    >
      {entry.logoUrl ? (
        <img
          src={entry.logoUrl}
          alt={`${entry.name} logo`}
          className="mp-related-logo"
          loading="lazy"
          style={tileStyle}
        />
      ) : (
        <span
          className="mp-related-icon"
          style={{ ...tileStyle, color: tone.text }}
        >
          {entry.name.charAt(0)}
        </span>
      )}
      <div className="mp-related-body">
        <strong>{entry.name}</strong>
        <p>{entry.desc}</p>
        <span className={`mp-tier mp-tier-${entry.tier}`}>{entry.tier}</span>
      </div>
    </Link>
  )
}

function FamilyDependencyChip({ product }: { product: Product }) {
  return (
    <Link
      to="/marketplace/family/$familyId"
      params={{ familyId: product.id }}
      data-testid={`marketplace-family-dep-${product.id}`}
      className="mp-dep-chip"
    >
      {product.name}
      <ExternalLink size={11} aria-hidden />
    </Link>
  )
}

export function MarketplaceFamilyPage() {
  const { familyId } = useParams({ from: '/marketplace/family/$familyId' })
  const navigate = useNavigate()
  const product = findProduct(familyId)
  const copy = product ? FAMILY_COPY[product.id] : undefined
  const palette = familyChipPalette(familyId)

  if (!product) {
    return (
      <main className="mp-shell" data-testid="marketplace-family-not-found">
        <button
          type="button"
          className="mp-back"
          onClick={() => navigate({ to: '/wizard' })}
          data-testid="marketplace-back"
        >
          <ArrowLeft size={14} aria-hidden /> Back to wizard
        </button>
        <h1 className="mp-title">Family not found</h1>
        <p className="mp-paragraph">
          No product family is registered under <code>{familyId}</code>.
        </p>
        <MarketplaceShellStyles />
      </main>
    )
  }

  const members = componentsByProduct(product.id)
  const dependencyProducts = product.familyDependencies
    .map((id) => findProduct(id))
    .filter((p): p is Product => !!p)

  // Pre-compute additional dependency products that this family pulls in via
  // component-level dependencies (other-family components referenced by any
  // member). Surfaces "what other families come along when you install this
  // one" alongside the explicit familyDependencies.
  const componentLevelOtherFamilies = (() => {
    const seen = new Set<string>([product.id])
    const out: Product[] = []
    for (const m of members) {
      for (const depId of m.dependencies ?? []) {
        const depEntry = componentsByProduct(product.id).find((c) => c.id === depId)
        if (depEntry) continue // same family
        const owner = PRODUCTS.find((p) => p.components.includes(depId))
        if (!owner || seen.has(owner.id)) continue
        seen.add(owner.id)
        if (!product.familyDependencies.includes(owner.id)) {
          out.push(owner)
        }
      }
    }
    return out
  })()

  return (
    <main className="mp-shell" data-testid={`marketplace-family-${product.id}`}>
      <button
        type="button"
        className="mp-back"
        onClick={() => navigate({ to: '/wizard' })}
        data-testid="marketplace-back"
      >
        <ArrowLeft size={14} aria-hidden /> Back to wizard
      </button>

      {/* ── Hero ──────────────────────────────────────────────────────
            Flat, bordered only at the bottom — mirrors the canonical
            .detail-hero pattern. The family chip on top replaces the
            logo; the remaining hierarchy (h1 / subtitle / tagline) is
            identical to the canonical detail page. */}
      <header
        className="mp-hero"
        data-testid="marketplace-family-hero"
      >
        <span
          className="mp-hero-chip"
          style={{
            background: palette.bg,
            color: palette.fg,
            borderColor: palette.border,
          }}
        >
          {product.name} family
        </span>
        <h1 className="mp-title">{product.name}</h1>
        <p className="mp-subtitle">{product.subtitle}</p>
        {copy && <p className="mp-tagline">{copy.tagline}</p>}
      </header>

      <section className="mp-section">
        <h2 className="mp-section-title">Overview</h2>
        {(copy?.overview ?? [product.description]).map((paragraph, i) => (
          <p key={i} className="mp-paragraph">
            {paragraph}
          </p>
        ))}
      </section>

      {copy && copy.capabilities.length > 0 && (
        <section className="mp-section">
          <h2 className="mp-section-title">What you get</h2>
          <ul className="mp-bullets" data-testid="marketplace-family-capabilities">
            {copy.capabilities.map((cap, i) => (
              <li key={i}>{cap}</li>
            ))}
          </ul>
        </section>
      )}

      <section className="mp-section">
        <h2 className="mp-section-title">Components ({members.length})</h2>
        <div
          className="mp-related-grid"
          data-testid="marketplace-family-members"
        >
          {members.map((entry) => (
            <MemberRow key={entry.id} entry={entry} />
          ))}
        </div>
      </section>

      {(dependencyProducts.length > 0 || componentLevelOtherFamilies.length > 0) && (
        <section className="mp-section">
          <h2 className="mp-section-title">Comes with</h2>
          <p className="mp-paragraph mp-paragraph-lead">
            Installing the {product.name} family also brings in:
          </p>
          <div
            className="mp-deps"
            data-testid="marketplace-family-dependencies"
          >
            {dependencyProducts.map((p) => (
              <FamilyDependencyChip key={p.id} product={p} />
            ))}
            {componentLevelOtherFamilies.map((p) => (
              <FamilyDependencyChip key={`indirect-${p.id}`} product={p} />
            ))}
          </div>
        </section>
      )}

      <section className="mp-section">
        <h2 className="mp-section-title">Upstream projects</h2>
        <ul className="mp-bullets">
          {members.map((entry) => {
            const cc = componentCopy(entry.id)
            return (
              <li key={entry.id}>
                <strong>{entry.name}</strong>
                {' — '}
                <a
                  href={cc.upstreamUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  className="mp-link"
                >
                  {cc.upstreamLabel}
                  <ExternalLink size={11} aria-hidden />
                </a>
              </li>
            )
          })}
        </ul>
      </section>

      <MarketplaceShellStyles />
    </main>
  )
}

/**
 * Shared shell styles — every typographic and chromatic decision tracks the
 * canonical marketplace at https://marketplace.openova.io/apps/. Numbers
 * mirror core/marketplace/src/components/AppDetail.svelte:
 *
 *   • Page width: 800–980px (canonical AppDetail = 800px; we widen slightly
 *     to accommodate the 2-up component grid)
 *   • Hero h1: 1.5rem / weight 700
 *   • Subtitle: 0.9rem / dim
 *   • Section h2: 1rem / weight 600
 *   • Body p: 0.9rem / line-height 1.7
 *   • Bullets: 0.85rem / line-height 1.6
 *   • Sections separated by 1px subtle borders (no card backgrounds — flat
 *     scroll, same as the canonical detail page)
 *   • Chips: 0.72rem / weight 600 / radius 4px / 12% accent tint
 *   • Tier pills: 0.65rem / weight 600 / radius 999px
 *   • Related/member tile: 36×36 logo + strong name (0.82rem) + dim tagline
 *
 * Exported as a colocated <style> block so each page is fully
 * self-contained and inherits whichever theme (--wiz-*) is active.
 */
export function MarketplaceShellStyles() {
  return (
    <style>{`
      .mp-shell {
        max-width: 900px;
        margin: 0 auto;
        padding: 1.5rem 1.25rem 4rem;
        color: var(--wiz-text-md);
        font-family: Inter, ui-sans-serif, system-ui, sans-serif;
      }
      .mp-back {
        display: inline-flex;
        align-items: center;
        gap: 0.4rem;
        padding: 0.4rem 0.75rem;
        background: transparent;
        border: 1px solid var(--wiz-border-sub);
        border-radius: 7px;
        color: var(--wiz-text-sub);
        cursor: pointer;
        font: inherit;
        font-size: 0.82rem;
        font-weight: 500;
        margin-bottom: 1.25rem;
        transition: border-color 0.15s, color 0.15s;
        text-decoration: none;
      }
      .mp-back:hover {
        border-color: rgba(var(--wiz-accent-ch), 0.6);
        color: var(--wiz-text-hi);
      }

      /* Hero — flat, no background fill, no border-radius. Section break
         is a 1px bottom border that matches the canonical detail-hero. */
      .mp-hero {
        padding: 1.5rem 0 1.75rem;
        border-bottom: 1px solid var(--wiz-border-sub);
        margin-bottom: 1.5rem;
      }
      .mp-hero-chip {
        display: inline-block;
        padding: 0.2rem 0.55rem;
        border-radius: 999px;
        font-size: 0.7rem;
        font-weight: 700;
        letter-spacing: 0.06em;
        text-transform: uppercase;
        border: 1px solid;
        margin-bottom: 0.85rem;
      }
      .mp-title {
        margin: 0;
        font-size: 1.5rem;
        font-weight: 700;
        color: var(--wiz-text-hi);
        letter-spacing: -0.01em;
        line-height: 1.25;
      }
      .mp-subtitle {
        margin: 0.35rem 0 0;
        font-size: 0.9rem;
        color: var(--wiz-text-sub);
        line-height: 1.5;
      }
      .mp-tagline {
        margin: 0.85rem 0 0;
        font-size: 0.9rem;
        color: var(--wiz-text-md);
        line-height: 1.6;
      }

      /* Section — borderless container, separated only by section-title
         hierarchy. Mirrors .detail-section from AppDetail.svelte. */
      .mp-section {
        padding: 1.25rem 0;
        border-bottom: 1px solid var(--wiz-border-sub);
      }
      .mp-section:last-of-type { border-bottom: none; }
      .mp-section-title {
        margin: 0 0 0.6rem;
        font-size: 1rem;
        font-weight: 600;
        color: var(--wiz-text-hi);
        letter-spacing: -0.005em;
      }
      .mp-paragraph {
        margin: 0 0 0.75rem;
        font-size: 0.9rem;
        line-height: 1.7;
        color: var(--wiz-text-md);
      }
      .mp-paragraph:last-child { margin-bottom: 0; }
      .mp-paragraph-lead {
        font-size: 0.85rem;
        color: var(--wiz-text-sub);
        margin-bottom: 0.6rem;
      }
      .mp-paragraph strong { color: var(--wiz-text-hi); font-weight: 600; }

      .mp-bullets {
        margin: 0;
        padding: 0;
        list-style: none;
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
        gap: 0.5rem 1rem;
      }
      .mp-bullets li {
        display: flex;
        align-items: flex-start;
        gap: 0.55rem;
        font-size: 0.85rem;
        line-height: 1.5;
        color: var(--wiz-text-md);
      }
      .mp-bullets li::before {
        content: '';
        width: 6px;
        height: 6px;
        border-radius: 50%;
        background: rgba(var(--wiz-success-ch), 1);
        flex-shrink: 0;
        margin-top: 0.55em;
      }
      .mp-bullets li strong { color: var(--wiz-text-hi); font-weight: 600; }

      /* Member grid — auto-fill 260px tiles, mirrors the canonical
         .related-grid on AppDetail.svelte. Logo + name + tagline +
         tier pill in the body column. */
      .mp-related-grid {
        display: grid;
        grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
        gap: 0.55rem;
      }
      .mp-related-card {
        display: flex;
        align-items: flex-start;
        gap: 0.7rem;
        padding: 0.7rem 0.8rem;
        background: var(--wiz-bg-sub);
        border: 1px solid var(--wiz-border-sub);
        border-radius: 8px;
        text-decoration: none;
        color: inherit;
        transition: border-color 0.15s, background 0.15s, transform 0.15s;
      }
      .mp-related-card:hover {
        border-color: rgba(var(--wiz-accent-ch), 0.6);
        transform: translateY(-1px);
      }
      /* Logo tile — geometry only. The tile *surface* (background,
         border, fallback letter colour) is driven per-asset by
         logoTone.ts → inline style on the element below, mirroring
         the canonical SME marketplace's per-asset PNG approach.
         Keep this rule's geometry (size, radius, padding, object-fit)
         in sync with the matching tiles in StepComponents.tsx (.corp-comp-card),
         StepReview.tsx (ComponentMiniCard) and MarketplaceProductPage.tsx
         (.mp-product-logo). */
      .mp-related-logo {
        width: 36px;
        height: 36px;
        border-radius: 8px;
        object-fit: contain;
        flex-shrink: 0;
        border: 1px solid transparent;
        padding: 6px;
        box-sizing: border-box;
      }
      .mp-related-icon {
        width: 36px;
        height: 36px;
        border-radius: 8px;
        display: inline-flex;
        align-items: center;
        justify-content: center;
        flex-shrink: 0;
        border: 1px solid transparent;
        font-size: 0.95rem;
        font-weight: 700;
      }
      .mp-related-body {
        flex: 1;
        min-width: 0;
        display: flex;
        flex-direction: column;
        gap: 0.2rem;
      }
      .mp-related-body strong {
        display: block;
        color: var(--wiz-text-hi);
        font-size: 0.88rem;
        font-weight: 600;
        line-height: 1.3;
      }
      .mp-related-body p {
        margin: 0;
        color: var(--wiz-text-sub);
        font-size: 0.78rem;
        line-height: 1.45;
        display: -webkit-box;
        -webkit-line-clamp: 2;
        -webkit-box-orient: vertical;
        overflow: hidden;
      }

      /* Tier pill — mirrors .detail-meta span proportions from the
         canonical detail page. Three colour variants, each muted to
         a 16% tint over the wizard surface. */
      .mp-tier {
        display: inline-flex;
        align-items: center;
        align-self: flex-start;
        padding: 0.1rem 0.45rem;
        border-radius: 999px;
        font-size: 0.62rem;
        font-weight: 600;
        letter-spacing: 0.04em;
        text-transform: uppercase;
        margin-top: 0.1rem;
      }
      .mp-tier-mandatory {
        background: rgba(74, 222, 128, 0.16);
        color: #4ADE80;
      }
      .mp-tier-recommended {
        background: rgba(56, 189, 248, 0.16);
        color: #38BDF8;
      }
      .mp-tier-optional {
        background: rgba(167, 139, 250, 0.16);
        color: #A78BFA;
      }
      [data-theme="light"] .mp-tier-mandatory { color: #047857; background: rgba(5, 150, 105, 0.12); }
      [data-theme="light"] .mp-tier-recommended { color: #0369A1; background: rgba(2, 132, 199, 0.12); }
      [data-theme="light"] .mp-tier-optional { color: #7C3AED; background: rgba(124, 58, 237, 0.12); }

      /* Dependency / family chips — small radius (4px), 12% accent tint,
         exactly the canonical .detail-cat proportion. */
      .mp-deps {
        display: flex;
        flex-wrap: wrap;
        gap: 0.4rem;
      }
      .mp-dep-chip {
        display: inline-flex;
        align-items: center;
        gap: 0.35rem;
        padding: 0.25rem 0.6rem;
        border-radius: 4px;
        font-size: 0.72rem;
        font-weight: 600;
        letter-spacing: 0.02em;
        text-decoration: none;
        background: rgba(var(--wiz-accent-ch), 0.12);
        color: rgba(var(--wiz-accent-ch), 1);
        border: 1px solid rgba(var(--wiz-accent-ch), 0.25);
        transition: background 0.15s, border-color 0.15s;
      }
      .mp-dep-chip:hover {
        background: rgba(var(--wiz-accent-ch), 0.18);
        border-color: rgba(var(--wiz-accent-ch), 0.45);
      }

      /* Inline link — accent-coloured, no underline by default, underline
         on hover. Tracks --wiz-accent so it follows theme. */
      .mp-link {
        color: var(--wiz-accent);
        text-decoration: none;
        font-weight: 600;
        display: inline-flex;
        align-items: center;
        gap: 0.3rem;
      }
      .mp-link:hover { text-decoration: underline; }

      @media (max-width: 600px) {
        .mp-shell { padding: 1rem 1rem 3rem; }
        .mp-title { font-size: 1.35rem; }
        .mp-related-grid { grid-template-columns: 1fr; }
      }
    `}</style>
  )
}
