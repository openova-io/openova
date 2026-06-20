import { describe, expect, it } from 'vitest'
import {
  isTenantKind,
  parseTenantID,
  parseTenantKind,
  type TenantID,
  type TenantKind,
} from './tenant'

describe('TenantID branded type', () => {
  it('accepts a non-empty string', () => {
    const t: TenantID = parseTenantID('tenant-acme')
    expect(t).toBe('tenant-acme')
  })

  it('rejects empty string', () => {
    expect(() => parseTenantID('')).toThrow(/invalid TenantID/)
  })

  it('rejects whitespace-only string', () => {
    expect(() => parseTenantID('   ')).toThrow(/invalid TenantID/)
  })

  it('rejects non-string types', () => {
    expect(() => parseTenantID(null)).toThrow(/invalid TenantID/)
    expect(() => parseTenantID(undefined)).toThrow(/invalid TenantID/)
    expect(() => parseTenantID(42)).toThrow(/invalid TenantID/)
    expect(() => parseTenantID({})).toThrow(/invalid TenantID/)
  })
})

describe('TenantKind', () => {
  it('parses both legal kinds', () => {
    const otech: TenantKind = parseTenantKind('otech')
    const org: TenantKind = parseTenantKind('org')
    expect(otech).toBe('otech')
    expect(org).toBe('org')
  })

  it('rejects anything else', () => {
    expect(() => parseTenantKind('admin')).toThrow(/invalid TenantKind/)
    expect(() => parseTenantKind(null)).toThrow(/invalid TenantKind/)
    expect(() => parseTenantKind('')).toThrow(/invalid TenantKind/)
  })

  it('isTenantKind narrows correctly', () => {
    expect(isTenantKind('otech')).toBe(true)
    expect(isTenantKind('org')).toBe(true)
    expect(isTenantKind('foo')).toBe(false)
    expect(isTenantKind(null)).toBe(false)
  })
})
