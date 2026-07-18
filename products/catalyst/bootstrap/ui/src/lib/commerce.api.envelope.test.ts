/**
 * commerce.api.envelope.test.ts — unit lock-in for parseCatalogEditEnvelope
 * (#5113 facet-a / UAT row 132, issue #5223): folding the catalyst-api
 * {stored, committed, reason} card-save envelope onto CatalogSaveVerdict.
 * A legacy raw-row body must yield committed:null (verdict UNKNOWN), never
 * a fabricated committed:true.
 */

import { describe, it, expect } from 'vitest'
import { parseCatalogEditEnvelope } from './commerce.api'

describe('parseCatalogEditEnvelope', () => {
  it('reads the honest envelope verbatim', () => {
    expect(
      parseCatalogEditEnvelope(
        { stored: true, committed: true, store: { slug: 'wordpress' } },
        'wordpress',
      ),
    ).toEqual({ slug: 'wordpress', stored: true, committed: true, reason: undefined })
  })

  it('carries the failure reason on committed:false', () => {
    expect(
      parseCatalogEditEnvelope(
        { stored: true, committed: false, reason: 'flux dry-run rejected' },
        'alloy',
      ),
    ).toEqual({
      slug: 'alloy',
      stored: true,
      committed: false,
      reason: 'flux dry-run rejected',
    })
  })

  it('legacy raw store row (no envelope) → committed:null, not a fabricated green', () => {
    expect(parseCatalogEditEnvelope({ slug: 'wordpress', name: 'WordPress' }, 'wordpress')).toEqual({
      slug: 'wordpress',
      stored: true,
      committed: null,
    })
    expect(parseCatalogEditEnvelope(null, 'x')).toEqual({ slug: 'x', stored: true, committed: null })
  })
})
