import { describe, expect, it } from 'vitest'
import { cellSubText, cellText } from './packages'

// A grow-only cell (DESIGN.md §22.11) reads as available in grow mode,
// billed as usage — never as an unpriced add-on.
describe('grow-only cells', () => {
  const dr = { kind: 'level', levels: ['single region', 'active-passive'] }
  it('a level cell keeps its level and names the next one in grow mode', () => {
    const c = { state: 'optional', level: 0, grow_only: true, included_from: 'plan.xl' }
    expect(cellText(dr, c, 'OMR')).toBe('single region')
    expect(cellSubText(dr, c, 'OMR')).toBe('active-passive in grow mode, billed as usage')
  })
  it('a boolean cell reads "In grow mode"', () => {
    const c = { state: 'optional', grow_only: true }
    expect(cellText({ kind: 'boolean' }, c, 'OMR')).toBe('In grow mode')
    expect(cellSubText({ kind: 'boolean' }, c, 'OMR')).toBe('billed as usage')
  })
})
