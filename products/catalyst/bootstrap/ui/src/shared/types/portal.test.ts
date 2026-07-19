import { describe, expect, it } from 'vitest'
import {
  isPortalKind,
  parsePortalID,
  parsePortalKind,
  type PortalID,
  type PortalKind,
} from './portal'

describe('PortalID branded type', () => {
  it('accepts a non-empty string', () => {
    const t: PortalID = parsePortalID('portal-acme')
    expect(t).toBe('portal-acme')
  })

  it('rejects empty string', () => {
    expect(() => parsePortalID('')).toThrow(/invalid PortalID/)
  })

  it('rejects whitespace-only string', () => {
    expect(() => parsePortalID('   ')).toThrow(/invalid PortalID/)
  })

  it('rejects non-string types', () => {
    expect(() => parsePortalID(null)).toThrow(/invalid PortalID/)
    expect(() => parsePortalID(undefined)).toThrow(/invalid PortalID/)
    expect(() => parsePortalID(42)).toThrow(/invalid PortalID/)
    expect(() => parsePortalID({})).toThrow(/invalid PortalID/)
  })
})

describe('PortalKind', () => {
  it('parses both legal kinds', () => {
    const otech: PortalKind = parsePortalKind('otech')
    const org: PortalKind = parsePortalKind('org')
    expect(otech).toBe('otech')
    expect(org).toBe('org')
  })

  it('rejects anything else', () => {
    expect(() => parsePortalKind('admin')).toThrow(/invalid PortalKind/)
    expect(() => parsePortalKind(null)).toThrow(/invalid PortalKind/)
    expect(() => parsePortalKind('')).toThrow(/invalid PortalKind/)
  })

  it('isPortalKind narrows correctly', () => {
    expect(isPortalKind('otech')).toBe(true)
    expect(isPortalKind('org')).toBe(true)
    expect(isPortalKind('foo')).toBe(false)
    expect(isPortalKind(null)).toBe(false)
  })
})
