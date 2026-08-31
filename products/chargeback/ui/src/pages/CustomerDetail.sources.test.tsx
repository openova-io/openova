import { describe, expect, it } from 'vitest'
import { asList } from '../api/client'

// GET /customers/{id}/sources answers with an envelope: {"sources":[…]}.
// The detail page must unwrap it (it previously read a non-existent
// c.sources off GET /customers/{id} and always rendered "No cost sources").
describe('sources envelope unwrap', () => {
  it('unwraps {sources:[…]}', () => {
    expect(asList({ sources: [{ id: 'a' }] }, 'sources')).toHaveLength(1)
  })
  it('tolerates a bare array and unknown envelopes', () => {
    expect(asList([{ id: 'a' }], 'sources')).toHaveLength(1)
    expect(asList({ unexpected: 1 }, 'sources')).toHaveLength(0)
  })
})
