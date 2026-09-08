import { describe, expect, it } from 'vitest'
import { showsRawKey } from './dims'

// The explorer prints a group's key under its label only when the key itself
// says something. A UUID does not (#6867).
describe('showsRawKey', () => {
  it('prints a readable code', () => {
    expect(showsRawKey('eip', 'Elastic IP')).toBe(true)
    expect(showsRawKey('ecs.m7n.xlarge.8', 'Elastic Cloud Server')).toBe(true)
    expect(showsRawKey('me-east-215-a', 'me-east-215-a · region')).toBe(true)
  })
  it('hides an opaque identifier', () => {
    expect(showsRawKey('9692021e-ce13-4bbd-9429-292e5f9218cd', 'Omantel')).toBe(false)
    expect(showsRawKey('9692021E-CE13-4BBD-9429-292E5F9218CD', 'Omantel')).toBe(false)
  })
  it('hides a key that adds nothing', () => {
    expect(showsRawKey('eip', 'eip')).toBe(false)
    expect(showsRawKey('other', 'Other')).toBe(false)
    expect(showsRawKey('', 'Untagged')).toBe(false)
  })
})
