/**
 * MarketplaceFamilyPage — long-form family / portfolio page reachable from
 * a chip click on any wizard component card. Surface contract:
 *
 *   • Chrome reuses the wizard layout (header + persistent footer-less main).
 *   • Title, tagline, multi-paragraph overview drawn from FAMILY_COPY.
 *   • Capability bullets surface what the operator gets when the family
 *     is installed.
 *   • Member list — every component in the family, with the same chip
 *     palette as the wizard card grid. Clicking a member opens the
 *     product detail page.
 *   • Family dependencies — every product the operator implicitly pulls in
 *     by installing this family.
 *   • "Back to wizard" returns to the wizard route. Wizard state is held
 *     in zustand + persist (localStorage) so navigation away does NOT
 *     drop selections.
 */

import { useNavigate, useParams, Link } from '@tanstack/react-router'
import { ArrowLeft, ArrowRight, ExternalLink } from 'lucide-react'
import {
  PRODUCTS,
  componentsByProduct,
  findProduct,
  type ComponentEntry,
  type Product,
} from '@/pages/wizard/steps/componentGroups'
import { FAMILY_COPY, componentCopy, familyChipPalette } from './marketplaceCopy'

interface MemberRowProps {
  entry: ComponentEntry
}

function MemberRow({ entry }: MemberRowProps) {
  const palette = familyChipPalette(entry.product)
  return (
    <Link
      to="/marketplace/product/$componentId"
      params={{ componentId: entry.id }}
      data-testid={`marketplace-family-member-${entry.id}`}
      style={{
        display: 'flex',
        alignItems: 'center',
        gap: '0.85rem',
        padding: '0.85rem 1rem',
        borderRadius: 10,
        background: 'var(--wiz-bg-sub)',
        border: '1px solid var(--wiz-border-sub)',
        textDecoration: 'none',
        color: 'inherit',
        transition: 'border-color 0.15s, transform 0.15s, background 0.15s',
      }}
      onMouseEnter={(e) => {
        e.currentTarget.style.borderColor = palette.border
        e.currentTarget.style.transform = 'translateY(-1px)'
      }}
      onMouseLeave={(e) => {
        e.currentTarget.style.borderColor = 'var(--wiz-border-sub)'
        e.currentTarget.style.transform = 'translateY(0)'
      }}
    >
      <div style={{ flex: 1, minWidth: 0 }}>
        <div
          style={{
            display: 'flex',
            alignItems: 'baseline',
            gap: '0.5rem',
            flexWrap: 'wrap',
          }}
        >
          <span
            style={{
              color: 'var(--wiz-text-hi)',
              fontSize: '0.95rem',
              fontWeight: 600,
            }}
          >
            {entry.name}
          </span>
          <span
            style={{
              fontSize: '0.7rem',
              fontWeight: 600,
              padding: '0.1rem 0.45rem',
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
        <p
          style={{
            margin: '0.2rem 0 0',
            color: 'var(--wiz-text-md)',
            fontSize: '0.8rem',
            lineHeight: 1.45,
          }}
        >
          {entry.desc}
        </p>
      </div>
      <ArrowRight
        size={16}
        aria-hidden
        style={{ color: 'var(--wiz-text-sub)', flexShrink: 0 }}
      />
    </Link>
  )
}

function FamilyDependencyChip({ product }: { product: Product }) {
  const palette = familyChipPalette(product.id)
  return (
    <Link
      to="/marketplace/family/$familyId"
      params={{ familyId: product.id }}
      data-testid={`marketplace-family-dep-${product.id}`}
      style={{
        display: 'inline-flex',
        alignItems: 'center',
        gap: '0.4rem',
        padding: '0.35rem 0.7rem',
        borderRadius: 999,
        background: palette.bg,
        color: palette.fg,
        border: `1px solid ${palette.border}`,
        textDecoration: 'none',
        fontSize: '0.8rem',
        fontWeight: 600,
        letterSpacing: '0.04em',
      }}
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
      <main className="marketplace-shell" data-testid="marketplace-family-not-found">
        <button
          type="button"
          className="marketplace-back"
          onClick={() => navigate({ to: '/wizard' })}
          data-testid="marketplace-back"
        >
          <ArrowLeft size={14} aria-hidden /> Back to wizard
        </button>
        <h1 className="marketplace-title">Family not found</h1>
        <p style={{ color: 'var(--wiz-text-md)' }}>
          No product family is registered under <code>{familyId}</code>.
        </p>
        <FamilyShellStyles />
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
    <main className="marketplace-shell" data-testid={`marketplace-family-${product.id}`}>
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
        data-testid="marketplace-family-hero"
        style={{ borderColor: palette.border }}
      >
        <span
          className="marketplace-hero-chip"
          style={{ background: palette.bg, color: palette.fg, borderColor: palette.border }}
        >
          {product.name} family
        </span>
        <h1 className="marketplace-title">{product.name}</h1>
        <p className="marketplace-subtitle">{product.subtitle}</p>
        {copy && (
          <p className="marketplace-tagline">{copy.tagline}</p>
        )}
      </header>

      <section className="marketplace-section">
        <h2 className="marketplace-section-title">Overview</h2>
        {(copy?.overview ?? [product.description]).map((paragraph, i) => (
          <p key={i} className="marketplace-paragraph">
            {paragraph}
          </p>
        ))}
      </section>

      {copy && copy.capabilities.length > 0 && (
        <section className="marketplace-section">
          <h2 className="marketplace-section-title">What you get</h2>
          <ul className="marketplace-bullets" data-testid="marketplace-family-capabilities">
            {copy.capabilities.map((cap, i) => (
              <li key={i}>{cap}</li>
            ))}
          </ul>
        </section>
      )}

      <section className="marketplace-section">
        <h2 className="marketplace-section-title">Components ({members.length})</h2>
        <div
          className="marketplace-member-grid"
          data-testid="marketplace-family-members"
        >
          {members.map((entry) => (
            <MemberRow key={entry.id} entry={entry} />
          ))}
        </div>
      </section>

      {(dependencyProducts.length > 0 || componentLevelOtherFamilies.length > 0) && (
        <section className="marketplace-section">
          <h2 className="marketplace-section-title">Comes with</h2>
          <p className="marketplace-paragraph" style={{ marginTop: 0 }}>
            Installing the {product.name} family also brings in:
          </p>
          <div
            className="marketplace-deps"
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

      <section className="marketplace-section">
        <h2 className="marketplace-section-title">Upstream projects</h2>
        <ul className="marketplace-bullets">
          {members.map((entry) => {
            const cc = componentCopy(entry.id)
            return (
              <li key={entry.id}>
                <strong style={{ color: 'var(--wiz-text-hi)' }}>{entry.name}</strong>
                {' — '}
                <a
                  href={cc.upstreamUrl}
                  target="_blank"
                  rel="noopener noreferrer"
                  style={{ color: 'var(--wiz-accent)' }}
                >
                  {cc.upstreamLabel} <ExternalLink size={11} aria-hidden style={{ marginLeft: 2 }} />
                </a>
              </li>
            )
          })}
        </ul>
      </section>

      <FamilyShellStyles />
    </main>
  )
}

/** Shell + hero styles — shared with MarketplaceProductPage. */
function FamilyShellStyles() {
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
      .marketplace-hero-chip {
        display: inline-block;
        padding: 0.25rem 0.65rem;
        border-radius: 999px;
        font-size: 0.7rem;
        font-weight: 700;
        letter-spacing: 0.06em;
        text-transform: uppercase;
        border: 1px solid;
        margin-bottom: 0.85rem;
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
      .marketplace-tagline {
        margin: 0.85rem 0 0;
        font-size: 0.95rem;
        color: var(--wiz-text-md);
        line-height: 1.55;
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
      .marketplace-member-grid {
        display: grid;
        grid-template-columns: 1fr;
        gap: 0.55rem;
      }
      @media (min-width: 720px) {
        .marketplace-member-grid { grid-template-columns: 1fr 1fr; }
      }
      .marketplace-deps {
        display: flex;
        flex-wrap: wrap;
        gap: 0.5rem;
      }
    `}</style>
  )
}
