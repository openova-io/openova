import { describe, expect, it } from 'vitest'
import { Article, article } from './word'

// The vocabularies this serves, spelled out: every tax kind and every contract
// line kind that reaches a sentence a user reads.
describe('article', () => {
  it('picks a or an across the closed vocabularies it serves', () => {
    for (const [w, want] of [
      ['standard', 'a'], ['zero-rated', 'a'], ['reverse charge', 'a'],
      ['exempt', 'an'], ['out of state', 'an'], ['allowance', 'an'],
      ['tier', 'a'], ['commitment', 'a'], ['', 'a'], ['  Exempt  ', 'an'],
    ] as const) {
      expect(`${w}:${article(w)}`).toBe(`${w}:${want}`)
    }
  })

  it('capitalises for the start of a sentence', () => {
    expect(Article('exempt')).toBe('An')
    expect(Article('standard')).toBe('A')
  })
})
