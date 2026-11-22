/**
 * StepComponents — the wizard step that shows what the new Sovereign installs.
 *
 * Read-only. The bootstrap kit (clusters/_template/bootstrap-kit) installs every
 * release of every ring; a ring can be switched off only after provisioning
 * (docs/adr/0020, S12), and foundation and provider never are. The list is
 * generated from the kit and the catalogue by tools/guards/cmd/kit-rings
 * (componentGroups.generated.ts): there is no hand list here, no product family
 * and no selection. A kit slot whose Blueprint has no chart is not listed.
 */
import { StepShell, useStepNav } from './_shared'
import { RINGS } from './componentGroups.generated'

export const RINGS_NOTICE = 'Every ring is installed. Rings can be switched off after provisioning.'

export function StepComponents() {
  const { next, back } = useStepNav()
  const total = RINGS.reduce((n, ring) => n + ring.releases.length, 0)

  return (
    <StepShell
      title="What will be installed"
      description={`${total} kit releases in ${RINGS.length} rings.`}
      onNext={next}
      onBack={back}
    >
      <p data-testid="rings-notice" style={{ fontSize: 13, color: 'var(--wiz-text-md)', margin: '0 0 1rem' }}>
        {RINGS_NOTICE}
      </p>
      <div data-testid="rings-list" style={{ display: 'flex', flexDirection: 'column', gap: '0.85rem' }}>
        {RINGS.map(ring => (
          <section
            key={ring.name}
            data-testid={`ring-${ring.name}`}
            aria-label={ring.title}
            style={{ border: '1px solid var(--wiz-border)', borderRadius: 8, padding: '0.75rem 0.9rem' }}
          >
            <header style={{ display: 'flex', alignItems: 'baseline', gap: '0.5rem', marginBottom: '0.5rem' }}>
              <h3 style={{ margin: 0, fontSize: 14, fontWeight: 600, color: 'var(--wiz-text-hi)' }}>{ring.title}</h3>
              <span data-testid={`ring-${ring.name}-count`} style={{ fontSize: 11, color: 'var(--wiz-text-sub)' }}>
                {ring.releases.length}
              </span>
              {ring.required && (
                <span
                  data-testid={`ring-${ring.name}-required`}
                  style={{ fontSize: 10, fontWeight: 700, color: 'var(--wiz-accent)', textTransform: 'uppercase' }}
                >
                  Required
                </span>
              )}
            </header>
            {ring.releases.length === 0 ? (
              <p style={{ margin: 0, fontSize: 11, color: 'var(--wiz-text-hint)' }}>No kit release in this ring yet.</p>
            ) : (
              <ul
                style={{
                  listStyle: 'none',
                  margin: 0,
                  padding: 0,
                  display: 'grid',
                  gridTemplateColumns: 'repeat(auto-fill, minmax(220px, 1fr))',
                  gap: '0.35rem 1rem',
                }}
              >
                {ring.releases.map(release => (
                  <li
                    key={release.blueprint}
                    data-testid={`ring-release-${release.blueprint}`}
                    style={{ fontSize: 12, color: 'var(--wiz-text-md)' }}
                  >
                    <span style={{ color: 'var(--wiz-text-hi)', fontWeight: 600 }}>{release.title}</span>{' '}
                    <span style={{ color: 'var(--wiz-text-sub)' }}>
                      {release.blueprint} · {release.class} · {release.lifecycle}
                    </span>
                  </li>
                ))}
              </ul>
            )}
          </section>
        ))}
      </div>
    </StepShell>
  )
}
