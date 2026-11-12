// G117.2 W2.C2 — multi-instance Application create DoD spec.
//
// Per W2.C2 brief: this spec creates 3 grafana instances in same Org
// (multi-instance enabled on bp-grafana), asserts 3 distinct UIDs, then
// asserts a 4th create with multiInstance.maxPerOrg=3 returns HTTP 409
// with code: max-per-org-exceeded.
//
// One mode: LIVE (post-handover Sovereign walk). SOV_FQDN set → POST against
// `https://api.${SOV_FQDN}/catalyst/v1/apps/instances` with a Bearer token
// from $CATALYST_TOKEN. Asserts the 409 contract on the cap.
//
// A second mode, MOCK, walked the catalog pages of a console scaffold that
// was never shipped. The scaffold and the MOCK cases were removed by work
// package FND-097. The same coverage for the shipped console is in
// CatalogDetail.test.tsx and AppsPage.test.tsx under
// products/catalyst/bootstrap/ui.

import { test, expect } from '@playwright/test'

const SOV_FQDN = (process.env.SOV_FQDN || '').trim()
const CATALYST_TOKEN = process.env.CATALYST_TOKEN || ''

test.describe('G117.2 W2.C2 — multi-instance create flow', () => {
  test.describe('LIVE mode (SOV_FQDN set)', () => {
    test.skip(!SOV_FQDN, 'Set SOV_FQDN to run the live POST-against-catalyst-api walk')

    test('POSTs 3 grafana instances + 4th hits max-per-org-exceeded 409', async ({ request }) => {
      const base = `https://api.${SOV_FQDN}/catalyst/v1`
      const headers: Record<string, string> = {
        'Content-Type': 'application/json',
      }
      if (CATALYST_TOKEN) headers.Authorization = `Bearer ${CATALYST_TOKEN}`

      // ASSUMPTION: this Sovereign's grafana Blueprint is configured
      // multiInstance.enabled=true, maxPerOrg=3. If your fresh prov uses
      // different values, override via CATALYST_MAXPERORG env.
      const expectedCap = Number(process.env.CATALYST_MAXPERORG || 3)

      const uids = new Set<string>()
      for (let i = 1; i <= expectedCap; i++) {
        const name = `obs-${i}`
        const r = await request.post(`${base}/apps/instances`, {
          headers,
          data: { blueprint: 'grafana', org: 'acme', name, topology: 'singleton' },
        })
        expect(r.status(), `create #${i} (${name})`).toBe(201)
        const body = await r.json()
        expect(body.name, `create #${i} name`).toBe(name)
        expect(body.id, `create #${i} id`).toBeTruthy()
        uids.add(body.id)
      }
      expect(uids.size, 'distinct UIDs').toBe(expectedCap)

      // The (cap+1)-th must 409 with max-per-org-exceeded.
      const over = await request.post(`${base}/apps/instances`, {
        headers,
        data: { blueprint: 'grafana', org: 'acme', name: `obs-${expectedCap + 1}`, topology: 'singleton' },
      })
      expect(over.status(), '4th create over the cap').toBe(409)
      const overBody = await over.json()
      expect(overBody.code, '4th create code').toBe('max-per-org-exceeded')
    })
  })
})
