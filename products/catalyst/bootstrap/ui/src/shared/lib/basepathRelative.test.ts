/**
 * basepathRelative tests — issue #1090 cluster B.
 *
 * The deep-link round-trip across the PIN flow depends on the `next=`
 * value being the POST-basepath route path, not the raw browser path.
 * If we get this wrong, VerifyPinPage's `navigate({ to: next })` either
 * 404s (basepath stripped twice) or double-prefixes (basepath added
 * back to a path that already had it). Both manifest as "deep link
 * lost" — the symptom we are fixing.
 */

import { describe, it, expect } from 'vitest'
import { pathRelativeToBasepath } from './basepathRelative'

describe('pathRelativeToBasepath', () => {
  it('strips the /sovereign basepath from contabo paths', () => {
    expect(pathRelativeToBasepath('/sovereign/dashboard')).toBe('/dashboard')
    expect(pathRelativeToBasepath('/sovereign/users')).toBe('/users')
    expect(pathRelativeToBasepath('/sovereign/jobs/timeline')).toBe('/jobs/timeline')
  })

  it('preserves search params when stripping the basepath', () => {
    expect(pathRelativeToBasepath('/sovereign/cloud', '?view=graph')).toBe('/cloud?view=graph')
    expect(pathRelativeToBasepath('/sovereign/users', '?filter=admin')).toBe('/users?filter=admin')
  })

  it('preserves provision deep-links with deploymentId (TC-R-081..084)', () => {
    expect(
      pathRelativeToBasepath('/sovereign/provision/sovereign-omantel.biz/jobs/timeline'),
    ).toBe('/provision/sovereign-omantel.biz/jobs/timeline')
    expect(pathRelativeToBasepath('/sovereign/provision/sov-acme/cloud')).toBe(
      '/provision/sov-acme/cloud',
    )
    expect(pathRelativeToBasepath('/sovereign/provision/sov-acme/users')).toBe(
      '/provision/sov-acme/users',
    )
    expect(pathRelativeToBasepath('/sovereign/provision/sov-acme/settings')).toBe(
      '/provision/sov-acme/settings',
    )
  })

  it('returns the path unchanged when no /sovereign prefix is present (Sovereign cluster)', () => {
    // On a chroot Sovereign the browser URL has no /sovereign prefix —
    // the same helper must be a no-op there so the same code path works
    // in both topologies.
    expect(pathRelativeToBasepath('/dashboard')).toBe('/dashboard')
    expect(pathRelativeToBasepath('/cloud', '?view=list')).toBe('/cloud?view=list')
    expect(pathRelativeToBasepath('/jobs/abc-123')).toBe('/jobs/abc-123')
  })

  it('handles the bare /sovereign path (edge case)', () => {
    // No trailing slash, no descendant route — a visit to literally
    // `/sovereign` resolves to the root. Returning '/' keeps the next=
    // round-trip semantically equivalent to "back to the index".
    expect(pathRelativeToBasepath('/sovereign')).toBe('/')
  })

  it('does NOT strip a /sovereignty prefix (similar but distinct)', () => {
    // The basepath check uses '/sovereign/' (with trailing slash) so
    // unrelated routes that happen to share the prefix string are not
    // mangled. /sovereignty/preview is a real route — see router.tsx.
    expect(pathRelativeToBasepath('/sovereignty/preview')).toBe('/sovereignty/preview')
  })

  it('always returns a leading slash', () => {
    // Defensive — the function MUST return a path that begins with '/'
    // because navigate({ to }) treats slashless paths as relative. The
    // input is always a window.location.pathname which itself begins
    // with '/', but the contract is enforced explicitly.
    expect(pathRelativeToBasepath('/')).toBe('/')
    expect(pathRelativeToBasepath('/sovereign/')).toBe('/')
  })
})
