/**
 * catalogIconGallery tests — #3668 (founder item §5B, icon-picker half).
 *
 * The gallery is the list of icons the visual picker shows ("i need to … see
 * the already uploaded logos"). These pin that it projects the vendored
 * component logos (the same assets the card falls back to) into a clean,
 * sorted, de-duped, renderable-only list — one generic mechanism over the
 * canonical ALL_COMPONENTS catalog, no hand-maintained second list.
 */

import { describe, it, expect } from 'vitest'
import { AVAILABLE_ICONS, findGalleryIcon } from './catalogIconGallery'
import { isRenderableSrc } from './resolveCatalogIcon'

describe('AVAILABLE_ICONS', () => {
  it('is non-empty (the console ships vendored logos to pick from)', () => {
    expect(AVAILABLE_ICONS.length).toBeGreaterThan(10)
  })

  it('every entry has a renderable src + id + label', () => {
    for (const g of AVAILABLE_ICONS) {
      expect(g.id).toBeTruthy()
      expect(g.label).toBeTruthy()
      expect(isRenderableSrc(g.url)).toBe(true)
    }
  })

  it('includes well-known component logos (grafana, harbor, cilium)', () => {
    const ids = new Set(AVAILABLE_ICONS.map((g) => g.id))
    expect(ids.has('grafana')).toBe(true)
    expect(ids.has('harbor')).toBe(true)
    expect(ids.has('cilium')).toBe(true)
  })

  it('is sorted by label and de-duped by url', () => {
    const labels = AVAILABLE_ICONS.map((g) => g.label)
    const sorted = [...labels].sort((a, b) => a.localeCompare(b))
    expect(labels).toEqual(sorted)
    const urls = AVAILABLE_ICONS.map((g) => g.url)
    expect(new Set(urls).size).toBe(urls.length)
  })

  it('excludes components with no vendored logo (logoUrl: null → letter-mark)', () => {
    // postgres is a catalog component that ships NO vendored asset
    // (logoUrl: null → letter-mark), so it must not appear in the gallery.
    const ids = new Set(AVAILABLE_ICONS.map((g) => g.id))
    expect(ids.has('postgres')).toBe(false)
  })
})

describe('findGalleryIcon', () => {
  it('returns the gallery entry whose url matches', () => {
    const first = AVAILABLE_ICONS[0]
    expect(findGalleryIcon(first.url)?.id).toBe(first.id)
  })

  it('returns undefined for a custom URL / data URI / empty', () => {
    expect(findGalleryIcon('https://cdn/custom.svg')).toBeUndefined()
    expect(findGalleryIcon('data:image/png;base64,AAAA')).toBeUndefined()
    expect(findGalleryIcon('')).toBeUndefined()
    expect(findGalleryIcon(undefined)).toBeUndefined()
  })
})
