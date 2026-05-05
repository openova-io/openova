/**
 * providerSizes.ts — region/SKU availability tests (issue #916).
 *
 * The two pure functions added in #916 are the source of truth for the
 * wizard's StepProvider SKU filtering AND the catalyst-api's
 * `provisioner.Request.Validate()` cross-check. They MUST stay
 * symmetric with the Go-side mirror at
 * `products/catalyst/bootstrap/api/internal/provisioner/sku_availability.go`
 * — both sides read the same `availableRegions` field on each NodeSize.
 *
 * Otech109 evidence: cpx32 (4 vCPU / 8 GB AMD shared) is listed in
 * Hetzner /v1/server_types but POST /v1/servers in `ash` is rejected
 * with `{"error":{"code":"invalid_input","message":"unsupported
 * location for server type"}}`. The wizard must catch that at request
 * validation time — before the tofu module runs.
 */

import { describe, it, expect } from 'vitest'
import {
  PROVIDER_NODE_SIZES,
  isSkuAvailableInRegion,
  suggestAlternativeSkus,
  findNodeSize,
} from './providerSizes'

describe('isSkuAvailableInRegion (issue #916)', () => {
  it('returns true for an unknown SKU id (delegates to downstream)', () => {
    expect(isSkuAvailableInRegion('hetzner', 'cpx9999', 'fsn1')).toBe(true)
  })

  it('returns true when SKU has no availableRegions constraint (= every region)', () => {
    // cpx22 explicitly does NOT have availableRegions today —
    // it's the recommended CP default, orderable wherever the
    // operator picks a Hetzner region. Sanity check.
    const cpx22 = findNodeSize('hetzner', 'cpx22')!
    expect(cpx22).toBeDefined()
    expect(cpx22.availableRegions).toBeUndefined()
    expect(isSkuAvailableInRegion('hetzner', 'cpx22', 'fsn1')).toBe(true)
    expect(isSkuAvailableInRegion('hetzner', 'cpx22', 'ash')).toBe(true)
  })

  it('cpx32 is orderable in EU DCs (fsn1/nbg1/hel1)', () => {
    expect(isSkuAvailableInRegion('hetzner', 'cpx32', 'fsn1')).toBe(true)
    expect(isSkuAvailableInRegion('hetzner', 'cpx32', 'nbg1')).toBe(true)
    expect(isSkuAvailableInRegion('hetzner', 'cpx32', 'hel1')).toBe(true)
  })

  it('cpx32 is NOT orderable in US DCs (ash/hil) — otech109 root cause', () => {
    // Failing assertions here mean the wizard would let an operator
    // dispatch otech109's exact failure mode (cpx32 + ash → tofu
    // rejected after CP + LB + firewall already created).
    expect(isSkuAvailableInRegion('hetzner', 'cpx32', 'ash')).toBe(false)
    expect(isSkuAvailableInRegion('hetzner', 'cpx32', 'hil')).toBe(false)
  })

  it('cpx21 has empty availableRegions (= orderable nowhere new)', () => {
    const cpx21 = findNodeSize('hetzner', 'cpx21')!
    expect(cpx21.availableRegions).toEqual([])
    // Every region returns false — Hetzner rejects every cpx21 POST.
    for (const region of ['fsn1', 'nbg1', 'hel1', 'ash', 'hil']) {
      expect(isSkuAvailableInRegion('hetzner', 'cpx21', region)).toBe(false)
    }
  })

  it('cpx31 has empty availableRegions (= orderable nowhere new)', () => {
    const cpx31 = findNodeSize('hetzner', 'cpx31')!
    expect(cpx31.availableRegions).toEqual([])
    for (const region of ['fsn1', 'nbg1', 'hel1', 'ash', 'hil']) {
      expect(isSkuAvailableInRegion('hetzner', 'cpx31', region)).toBe(false)
    }
  })

  it('hyperscaler SKUs default to "available everywhere" (no availableRegions field)', () => {
    // Spot-check one SKU per hyperscaler — none of them have
    // availableRegions today so all of these must pass.
    expect(isSkuAvailableInRegion('aws', 'm6i.xlarge', 'eu-central-1')).toBe(true)
    expect(isSkuAvailableInRegion('azure', 'Standard_D4s_v5', 'westeurope')).toBe(true)
    expect(isSkuAvailableInRegion('oci', 'VM.Standard.E5.Flex.2.16', 'eu-frankfurt-1')).toBe(true)
    expect(isSkuAvailableInRegion('huawei', 'c7n.xlarge.2', 'eu-west-101')).toBe(true)
  })
})

describe('suggestAlternativeSkus (issue #916)', () => {
  it('suggests EU-orderable shared-amd alternatives when cpx32 picked in ash', () => {
    // Operator picks cpx32 + ash → invalid. The wizard's inline-error
    // surfaces a "use one of these instead" picker. The replacement
    // must be (a) same SkuCategory (shared-amd), (b) orderable in ash
    // — wait, that's the trick: when the REGION is the constraint,
    // suggestions are filtered against that region. cpx32 is in
    // shared-amd; from the catalog the only same-category SKUs
    // currently *orderable* AT ALL are cpx11/cpx22/cpx42/cpx52/cpx62
    // (not cpx21/cpx31 — empty availableRegions).
    //
    // None of those have availableRegions today, so they're treated
    // as orderable in `ash` until proven otherwise. The suggestion
    // list is therefore non-empty, sorted by priceMonth ascending.
    const alts = suggestAlternativeSkus('hetzner', 'cpx32', 'ash', 5)
    expect(alts.length).toBeGreaterThan(0)
    // cpx21 and cpx31 are NOT recommended — they have empty
    // availableRegions, so isSkuAvailableInRegion(_, _, 'ash') is
    // false; suggestAlternativeSkus must skip them.
    expect(alts).not.toContain('cpx21')
    expect(alts).not.toContain('cpx31')
    expect(alts).not.toContain('cpx32') // never suggest the unavailable choice itself
  })

  it('respects the limit parameter', () => {
    expect(suggestAlternativeSkus('hetzner', 'cpx32', 'fsn1', 1).length).toBeLessThanOrEqual(1)
    expect(suggestAlternativeSkus('hetzner', 'cpx32', 'fsn1', 2).length).toBeLessThanOrEqual(2)
  })

  it('returns sorted ascending by priceMonth', () => {
    const alts = suggestAlternativeSkus('hetzner', 'cpx32', 'fsn1', 5)
    for (let i = 1; i < alts.length; i++) {
      const prev = findNodeSize('hetzner', alts[i - 1])!
      const curr = findNodeSize('hetzner', alts[i])!
      expect(curr.priceMonth).toBeGreaterThanOrEqual(prev.priceMonth)
    }
  })

  it('returns [] for an unknown SKU id', () => {
    expect(suggestAlternativeSkus('hetzner', 'cpx9999', 'fsn1')).toEqual([])
  })
})

describe('Hetzner SKU regional matrix invariants', () => {
  it('every CPX SKU with availableRegions explicitly enumerates orderable regions', () => {
    // Defensive — protects against typos like
    // `availableRegions: ['fsn']` (typo for 'fsn1') silently making
    // the SKU unavailable everywhere.
    const validHetznerRegions = new Set([
      'fsn1', 'nbg1', 'hel1', 'ash', 'hil',
    ])
    for (const sku of PROVIDER_NODE_SIZES.hetzner) {
      if (!sku.availableRegions) continue
      for (const region of sku.availableRegions) {
        expect(
          validHetznerRegions.has(region),
          `${sku.id} availableRegions includes invalid region "${region}"`,
        ).toBe(true)
      }
    }
  })

  it('cpx32 (the worker default) MUST be orderable in at least one region', () => {
    // The wizard's `defaultWorkerSizeId('hetzner') === 'cpx32'`. If
    // somebody narrows cpx32's availableRegions to [], every fresh
    // wizard launch would block at the SKU dropdown — surfacing a
    // misconfiguration that breaks the canonical Sovereign topology.
    const cpx32 = findNodeSize('hetzner', 'cpx32')!
    expect(cpx32.availableRegions ?? ['fsn1']).not.toHaveLength(0)
  })
})
