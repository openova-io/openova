import { describe, expect, it } from 'vitest'
import { hasErrors, parseProjectIds, validateActivation } from './activation'

const ok = {
  region: 'om-east-1',
  projectIds: '3b1d0d0e5f2c4a7d8e9f0a1b2c3d4e5f\n7c2e1f0a9b8d4c6e5f4a3b2c1d0e9f8a',
  accessKey: 'AKIAEXAMPLE0123456789',
  secretKey: 'sK/EXAMPLE+secret+key+0123456789abcdef',
}

describe('parseProjectIds', () => {
  it('splits on newline, comma, semicolon and whitespace and dedupes', () => {
    expect(parseProjectIds('a1b2c3d4, e5f6a7b8;a1b2c3d4\n c9d0e1f2 ')).toEqual(['a1b2c3d4', 'e5f6a7b8', 'c9d0e1f2'])
  })
})

describe('validateActivation', () => {
  it('accepts a complete form', () => {
    expect(validateActivation(ok)).toEqual({})
    expect(hasErrors(validateActivation(ok))).toBe(false)
  })

  it('requires every field', () => {
    const e = validateActivation({ region: '', projectIds: '', accessKey: '', secretKey: '' })
    expect(Object.keys(e).sort()).toEqual(['accessKey', 'projectIds', 'region', 'secretKey'])
  })

  it('rejects a malformed region and a bad project id', () => {
    const e = validateActivation({ ...ok, region: 'OM East', projectIds: 'short' })
    expect(e.region).toContain('lowercase')
    expect(e.projectIds).toContain('invalid project id: short')
  })

  it('rejects keys with spaces or wrong lengths', () => {
    expect(validateActivation({ ...ok, accessKey: 'has space here' }).accessKey).toBeDefined()
    expect(validateActivation({ ...ok, accessKey: 'short' }).accessKey).toBeDefined()
    expect(validateActivation({ ...ok, secretKey: 'tooshort' }).secretKey).toContain('16-128')
    expect(validateActivation({ ...ok, secretKey: ok.secretKey + ' ' }).secretKey).toContain('whitespace')
  })
})
