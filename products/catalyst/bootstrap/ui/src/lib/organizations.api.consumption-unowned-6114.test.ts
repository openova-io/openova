/**
 * organizations.api.consumption-unowned-6114.test.ts — the getConsumption
 * mapper must carry the #6114 orphan fields through to the panel.
 *
 * Why this file exists as a separate guard: getConsumption rebuilds the
 * response object field by field rather than passing the body through, so
 * any field it does not explicitly name is silently dropped. Every
 * ShowbackPanel test seeds its feed via the `initialOverride` prop and
 * therefore never traverses this function — a mapper that dropped
 * `unownedOrgs` would leave the orphan warning permanently unrenderable in
 * the browser while the whole panel suite stayed green. That is the
 * "the guard tested a surface that cannot fail" shape, so the mapping is
 * asserted here, on the function the live page actually calls.
 */

import { describe, expect, it, vi, beforeEach } from 'vitest'

let responseBody: unknown = {}
let responseOk = true

vi.mock('@/shared/lib/authedFetch', () => ({
  authedFetch: async () => ({
    ok: responseOk,
    json: async () => responseBody,
  }),
}))

const { getConsumption } = await import('./organizations.api')

beforeEach(() => {
  responseOk = true
  responseBody = {}
})

describe('getConsumption — #6114 orphan fields survive the mapper', () => {
  it('carries unownedOrgs through with its exact values', async () => {
    responseBody = {
      totalCostUnits: 2168,
      pending: false,
      unownedOrgs: ['g7doora'],
      orgs: [
        { org: 'hw293vch', isParent: false, isPlatform: false, costUnits: 404, cpuMilli: 400, memoryGiB: 1, storageGiB: 0, apps: [] },
        { org: '__unowned__', isParent: false, isPlatform: false, isUnowned: true, costUnits: 1764, cpuMilli: 1750, memoryGiB: 3.5, storageGiB: 0, apps: [] },
      ],
    }

    const feed = await getConsumption()

    expect(feed.unownedOrgs).toEqual(['g7doora'])
    // The isUnowned flag rides along on the org rows the mapper passes
    // through wholesale — asserted so a future mapper that starts
    // reshaping org rows cannot drop it unnoticed.
    expect(feed.orgs.find((o) => o.org === '__unowned__')?.isUnowned).toBe(true)
    // CONTROL: the genuine Organization's numbers are untouched.
    expect(feed.orgs.find((o) => o.org === 'hw293vch')?.costUnits).toBe(404)
    expect(feed.totalCostUnits).toBe(2168)
  })

  it('yields an empty list, not undefined, when the estate has no orphans', async () => {
    responseBody = { totalCostUnits: 900, pending: false, orgs: [] }

    const feed = await getConsumption()

    expect(feed.unownedOrgs).toEqual([])
  })

  it('tolerates a malformed unownedOrgs without crashing the panel', async () => {
    responseBody = { totalCostUnits: 0, pending: false, orgs: [], unownedOrgs: 'g7doora' }

    const feed = await getConsumption()

    expect(feed.unownedOrgs).toEqual([])
  })
})
