import { describe, expect, it } from 'vitest'
import { isTagDim, isValidTagKey, tagDim, tagEntries, tagKeyOf } from './tags'

describe('tag dimensions', () => {
  it('accepts the server key rule and nothing else', () => {
    for (const k of ['team', 'Env', 'cost-centre', 'a.b/c:d@e_f', 'k'.repeat(128)]) expect(isValidTagKey(k)).toBe(true)
    for (const k of ['', 'a b', "te'am", 'te"am', 'a;b', 'a=b', '$1', 'k'.repeat(129)]) expect(isValidTagKey(k)).toBe(false)
  })
  it('reads the key off a tag dimension only when it is valid', () => {
    expect(tagKeyOf('tag:team')).toBe('team')
    expect(tagKeyOf('tag:Env')).toBe('Env')
    expect(tagKeyOf('tag:')).toBeNull()
    expect(tagKeyOf("tag:te'am")).toBeNull()
    expect(tagKeyOf('team')).toBeNull()
    expect(tagKeyOf('Tag:team')).toBeNull()
    expect(tagKeyOf(null)).toBeNull()
    expect(isTagDim('tag:team')).toBe(true)
    expect(isTagDim('kind')).toBe(false)
    expect(tagDim('team')).toBe('tag:team')
  })
  it('lists a resource’s tags sorted, tolerating junk shapes', () => {
    expect(tagEntries({ team: 'platform', Env: 'prod', novalue: '' })).toEqual([
      { key: 'Env', value: 'prod' },
      { key: 'novalue', value: '' },
      { key: 'team', value: 'platform' },
    ])
    expect(tagEntries({ n: 3, b: true })).toEqual([{ key: 'b', value: 'true' }, { key: 'n', value: '3' }])
    expect(tagEntries(['team=a'])).toEqual([])
    expect(tagEntries('team=a')).toEqual([])
    expect(tagEntries(null)).toEqual([])
    expect(tagEntries(undefined)).toEqual([])
  })
})
