/**
 * Tests for mothershipTokenRedirect.ts — issue #1773, DoD gate D0.
 *
 * The pure-function `decideMothershipTokenRedirect` is the only thing
 * we can unit-test in jsdom — `runMothershipTokenRedirect()` calls
 * `window.location.replace()` which jsdom flags as a navigation we
 * can't actually fire from a test. The decision function is the
 * behaviour worth pinning down.
 */

import { describe, it, expect } from 'vitest'
import {
  decideMothershipTokenRedirect,
  extractChrootConsoleURL,
} from './mothershipTokenRedirect'

/**
 * Build a minimal, non-signed JWT with the given payload for testing.
 * Mirrors `buildFakeJWT` in `oidc.test.ts`.
 */
function buildFakeJWT(payload: Record<string, unknown>): string {
  const header = btoa(JSON.stringify({ alg: 'none', typ: 'JWT' }))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=/g, '')
  const body = btoa(JSON.stringify(payload))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=/g, '')
  return `${header}.${body}.`
}

describe('extractChrootConsoleURL', () => {
  it('returns origin from a string aud pointing at a chroot console', () => {
    expect(extractChrootConsoleURL('https://console.t22.omantel.biz')).toBe(
      'https://console.t22.omantel.biz',
    )
  })

  it('returns origin from a single-entry array aud', () => {
    expect(
      extractChrootConsoleURL(['https://console.otech23.omani.works']),
    ).toBe('https://console.otech23.omani.works')
  })

  it('strips path/query/hash and returns origin only', () => {
    expect(
      extractChrootConsoleURL('https://console.t22.omantel.biz/auth?x=y#z'),
    ).toBe('https://console.t22.omantel.biz')
  })

  it('returns null for missing aud', () => {
    expect(extractChrootConsoleURL(undefined)).toBeNull()
    expect(extractChrootConsoleURL(null)).toBeNull()
  })

  it('returns null when aud points back at the Mothership (self-loop guard)', () => {
    expect(extractChrootConsoleURL('https://console.openova.io')).toBeNull()
  })

  it('returns null for non-https schemes', () => {
    expect(extractChrootConsoleURL('http://console.t22.omantel.biz')).toBeNull()
  })

  it('returns null when hostname does not start with console.', () => {
    expect(extractChrootConsoleURL('https://auth.t22.omantel.biz')).toBeNull()
  })

  it('returns null for non-URL strings', () => {
    expect(extractChrootConsoleURL('not-a-url')).toBeNull()
    expect(extractChrootConsoleURL('')).toBeNull()
  })

  it('returns null for non-string non-array shapes', () => {
    expect(extractChrootConsoleURL(42)).toBeNull()
    expect(extractChrootConsoleURL({ aud: 'x' })).toBeNull()
  })
})

describe('decideMothershipTokenRedirect', () => {
  it('returns the chroot /auth/handover URL when ?token= carries an aud pointing at a Sovereign chroot', () => {
    const token = buildFakeJWT({
      aud: ['https://console.t22.omantel.biz'],
      sovereign_fqdn: 't22.omantel.biz',
      exp: 9999999999,
    })
    const url = decideMothershipTokenRedirect({
      hostname: 'console.openova.io',
      search: `?token=${token}`,
    })
    expect(url).toBe(
      `https://console.t22.omantel.biz/auth/handover?token=${encodeURIComponent(token)}`,
    )
  })

  it('also accepts a string aud (non-array)', () => {
    const token = buildFakeJWT({
      aud: 'https://console.acme.example.com',
    })
    const url = decideMothershipTokenRedirect({
      hostname: 'console.openova.io',
      search: `?token=${token}`,
    })
    expect(url).toBe(
      `https://console.acme.example.com/auth/handover?token=${encodeURIComponent(token)}`,
    )
  })

  it('returns null when there is no ?token= query param', () => {
    expect(
      decideMothershipTokenRedirect({
        hostname: 'console.openova.io',
        search: '?other=value',
      }),
    ).toBeNull()
    expect(
      decideMothershipTokenRedirect({
        hostname: 'console.openova.io',
        search: '',
      }),
    ).toBeNull()
  })

  it('returns null when the hostname is not the Mothership', () => {
    const token = buildFakeJWT({
      aud: ['https://console.t22.omantel.biz'],
    })
    expect(
      decideMothershipTokenRedirect({
        hostname: 'console.t22.omantel.biz',
        search: `?token=${token}`,
      }),
    ).toBeNull()
  })

  it('returns null when the JWT is malformed (not 3 parts)', () => {
    expect(
      decideMothershipTokenRedirect({
        hostname: 'console.openova.io',
        search: '?token=not.a.valid.jwt.with.too.many.dots',
      }),
    ).toBeNull()
    expect(
      decideMothershipTokenRedirect({
        hostname: 'console.openova.io',
        search: '?token=garbage',
      }),
    ).toBeNull()
  })

  it('returns null when the JWT has no aud claim', () => {
    const token = buildFakeJWT({ sub: 'user-1', exp: 9999999999 })
    expect(
      decideMothershipTokenRedirect({
        hostname: 'console.openova.io',
        search: `?token=${token}`,
      }),
    ).toBeNull()
  })

  it('returns null when aud points back at the Mothership (self-loop guard)', () => {
    const token = buildFakeJWT({
      aud: ['https://console.openova.io'],
    })
    expect(
      decideMothershipTokenRedirect({
        hostname: 'console.openova.io',
        search: `?token=${token}`,
      }),
    ).toBeNull()
  })

  it('returns null when aud is not a chroot console URL', () => {
    const token = buildFakeJWT({
      aud: ['https://auth.t22.omantel.biz/realms/sovereign'],
    })
    expect(
      decideMothershipTokenRedirect({
        hostname: 'console.openova.io',
        search: `?token=${token}`,
      }),
    ).toBeNull()
  })

  it('preserves the JWT verbatim across the redirect (no claim mutation)', () => {
    // Sovereign-side /auth/handover does its own RS256 signature
    // verification — we MUST forward the raw token without
    // re-encoding the payload. Use a token whose body contains
    // characters that would round-trip badly under naive base64
    // mangling and assert the URL still contains it.
    const token = buildFakeJWT({
      aud: ['https://console.t22.omantel.biz'],
      email: 'op+tag@example.com',
      deployment_id: 'dep-123_ABC',
    })
    const url = decideMothershipTokenRedirect({
      hostname: 'console.openova.io',
      search: `?token=${token}`,
    })
    expect(url).not.toBeNull()
    // The token in the URL is encodeURIComponent'd; reverse it and
    // compare to the original to assert bit-for-bit preservation.
    const u = new URL(url as string)
    expect(u.searchParams.get('token')).toBe(token)
    expect(u.pathname).toBe('/auth/handover')
  })
})
