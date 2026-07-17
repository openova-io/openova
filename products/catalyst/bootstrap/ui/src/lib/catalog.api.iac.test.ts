// catalog.api.iac.test.ts — #5124: getCatalogBlueprintIaC reads the
// CURRENTLY-COMMITTED blueprint.yaml so the Edit-IaC editor seeds from the IaC
// source of truth, and falls back to null (→ caller uses store raw) on any
// not-committed / error path so the seed is never WORSE than before.
import { describe, it, expect, vi, beforeEach } from 'vitest'

const authedFetch = vi.fn()
vi.mock('@/shared/lib/authedFetch', () => ({ authedFetch: (...a: unknown[]) => authedFetch(...a) }))

import { getCatalogBlueprintIaC } from './catalog.api'

function jsonRes(status: number, body: unknown) {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: async () => body,
    text: async () => JSON.stringify(body),
  }
}

describe('#5124 getCatalogBlueprintIaC', () => {
  beforeEach(() => authedFetch.mockReset())

  it('returns the committed blueprintYaml verbatim on 200', async () => {
    const yaml = 'apiVersion: catalyst.openova.io/v1\nkind: Blueprint\nmetadata:\n  name: bp-alloy\n'
    authedFetch.mockResolvedValueOnce(jsonRes(200, { blueprintYaml: yaml, path: 'catalog-sovereign/bp-alloy/blueprint.yaml' }))
    await expect(getCatalogBlueprintIaC('bp-alloy')).resolves.toBe(yaml)
    expect(authedFetch).toHaveBeenCalledWith(
      expect.stringMatching(/\/catalog\/bp-alloy\/iac$/),
      expect.objectContaining({ headers: expect.objectContaining({ Accept: 'application/json' }) }),
    )
  })

  it('returns null on 404 (nothing committed yet) → caller falls back to store raw', async () => {
    authedFetch.mockResolvedValueOnce(jsonRes(404, { error: 'not-committed' }))
    await expect(getCatalogBlueprintIaC('bp-neverseen')).resolves.toBeNull()
  })

  it('returns null when the committed file is empty (never seed a blank over raw)', async () => {
    authedFetch.mockResolvedValueOnce(jsonRes(200, { blueprintYaml: '' }))
    await expect(getCatalogBlueprintIaC('bp-alloy')).resolves.toBeNull()
  })

  it('returns null on 503 (gitea unwired)', async () => {
    authedFetch.mockResolvedValueOnce(jsonRes(503, { error: 'gitea-unwired' }))
    await expect(getCatalogBlueprintIaC('bp-alloy')).resolves.toBeNull()
  })

  it('returns null when the transport throws (never blocks opening the editor)', async () => {
    authedFetch.mockRejectedValueOnce(new Error('network down'))
    await expect(getCatalogBlueprintIaC('bp-alloy')).resolves.toBeNull()
  })
})
