import { describe, expect, it } from 'vitest'
import { badgeProblem, iconFileProblem, iconIdOf, iconRefOf, iconSrc, isColour, normalizeColour, resolveIconSrc } from './icons'

const ID = 'a'.repeat(64)

describe('icons and branding (DESIGN.md §22.10)', () => {
  it('resolves a document src against the URL the document was read from', () => {
    expect(resolveIconSrc(`/api/v1/public/icons/${ID}`, 'https://chargeback.t99.omani.works/api/v1/public/packages')).toBe(`https://chargeback.t99.omani.works/api/v1/public/icons/${ID}`)
    // The storefront on another origin still loads the bytes from the API's origin.
    expect(resolveIconSrc(`/api/v1/public/icons/${ID}`, 'https://chargeback.t99.omani.works/api/v1/public/packages')).not.toContain('marketplace')
    // An absolute src stays as it is; an empty one is empty.
    expect(resolveIconSrc('https://cdn.example/x.svg', 'https://chargeback.t99.omani.works/')).toBe('https://cdn.example/x.svg')
    expect(resolveIconSrc('', 'https://chargeback.t99.omani.works/')).toBe('')
    // With no document URL, the page's own origin (the console, the calculator).
    expect(resolveIconSrc(`/api/v1/public/icons/${ID}`)).toMatch(new RegExp(`^https?://[^/]+/api/v1/public/icons/${ID}$`))
  })

  it('reads an id off a src and names a stored icon the way the document does', () => {
    expect(iconIdOf({ src: `/api/v1/public/icons/${ID}`, alt: 'Backup' })).toBe(ID)
    expect(iconIdOf({ src: '/api/v1/public/icons/not-an-id', alt: 'x' })).toBe('')
    expect(iconIdOf(undefined)).toBe('')
    expect(iconSrc(ID)).toBe(`/api/v1/public/icons/${ID}`)
    expect(iconSrc('')).toBe('')
    expect(iconRefOf(ID, 'Backup', '#FFE4E6')).toEqual({ src: `/api/v1/public/icons/${ID}`, alt: 'Backup', bg: '#FFE4E6' })
    // No bg key at all when none is set — the document's own rule.
    expect(iconRefOf(ID, 'Backup', '')).toEqual({ src: `/api/v1/public/icons/${ID}`, alt: 'Backup' })
    expect(iconRefOf('', 'Backup', '#FFE4E6')).toBeUndefined()
  })

  it('takes a colour only as #RRGGBB and stores it upper-case', () => {
    for (const ok of ['', '#3b82f6', '#3B82F6', ' #FFE4E6 ']) expect(isColour(ok)).toBe(true)
    for (const bad of ['red', '#fff', '3B82F6', '#3B82F6FF', '#GGGGGG']) expect(isColour(bad)).toBe(false)
    expect(normalizeColour(' #3b82f6 ')).toBe('#3B82F6')
    expect(normalizeColour('  ')).toBe('')
  })

  it('refuses a file that is not an icon before uploading it, and a badge that is too long', () => {
    expect(iconFileProblem({ type: 'image/svg+xml', size: 300, name: 'a.svg' })).toBe('')
    expect(iconFileProblem({ type: 'image/png', size: 64 * 1024, name: 'a.png' })).toBe('')
    expect(iconFileProblem({ type: 'image/gif', size: 300, name: 'a.gif' })).toMatch(/SVG, PNG or WebP/)
    expect(iconFileProblem({ type: 'image/png', size: 64 * 1024 + 1, name: 'big.png' })).toMatch(/at most 64 KiB/)
    expect(iconFileProblem({ type: 'image/svg+xml', size: 0, name: 'e.svg' })).toMatch(/empty/)
    expect(badgeProblem('Most popular')).toBe('')
    expect(badgeProblem('x'.repeat(25))).toMatch(/at most 24/)
  })
})
