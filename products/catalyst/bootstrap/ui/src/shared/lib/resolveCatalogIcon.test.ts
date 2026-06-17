/**
 * resolveCatalogIcon tests — #3668 §5B.
 *
 * The model: the rendered catalog icon is the IaC icon
 * (card.iconLight / iconDark, theme-aware), NOT the build-time bundled
 * asset. Before #3668 the detail page read the build-time map and discarded
 * the API's theme icons, so an admin's icon edit never rendered. These pin
 * the single resolution path: IaC first (theme-correct), bundled asset as
 * fallback, letter-mark last; plus the bare-filename guard.
 */

import { describe, it, expect } from 'vitest'
import { resolveCatalogIcon, isRenderableSrc } from './resolveCatalogIcon'

describe('resolveCatalogIcon', () => {
  it('renders the IaC light icon in the light theme (over the bundled asset)', () => {
    expect(
      resolveCatalogIcon(
        { iconLight: 'https://cdn/light.svg', iconDark: 'https://cdn/dark.svg' },
        'light',
        '/component-logos/alloy.svg',
      ),
    ).toBe('https://cdn/light.svg')
  })

  it('renders the IaC dark icon in the dark theme', () => {
    expect(
      resolveCatalogIcon(
        { iconLight: 'https://cdn/light.svg', iconDark: 'https://cdn/dark.svg' },
        'dark',
        '/component-logos/alloy.svg',
      ),
    ).toBe('https://cdn/dark.svg')
  })

  it('falls back to the other theme icon when one theme is unset (one-theme edit renders in both)', () => {
    expect(
      resolveCatalogIcon({ iconLight: 'https://cdn/light.svg' }, 'dark', null),
    ).toBe('https://cdn/light.svg')
  })

  it('accepts a data-URI IaC icon (the DoD red-dot edit) over the bundled asset', () => {
    const redDot = 'data:image/png;base64,iVBORw0KGgoAAAANS'
    expect(
      resolveCatalogIcon({ iconLight: redDot }, 'light', '/component-logos/alloy.svg'),
    ).toBe(redDot)
  })

  it('falls back to the bundled asset when the IaC carries no icon', () => {
    expect(resolveCatalogIcon({}, 'light', '/component-logos/alloy.svg')).toBe(
      '/component-logos/alloy.svg',
    )
  })

  it('returns null (letter-mark) when neither IaC nor bundled asset is present', () => {
    expect(resolveCatalogIcon({}, 'dark', null)).toBeNull()
    expect(resolveCatalogIcon(undefined, 'dark', '')).toBeNull()
  })

  it('does NOT treat a bare SVG filename as a renderable src (no resolvable base)', () => {
    // The legacy card.icon is often a bare "grafana.svg" — it must fall
    // through to the bundled asset, not render as a broken <img src>.
    expect(
      resolveCatalogIcon({ icon: 'grafana.svg' }, 'light', '/component-logos/grafana.svg'),
    ).toBe('/component-logos/grafana.svg')
  })

  it('uses a rooted/relative legacy card.icon when it is a resolvable path', () => {
    expect(resolveCatalogIcon({ icon: '/icons/x.svg' }, 'light', null)).toBe('/icons/x.svg')
  })
})

describe('isRenderableSrc', () => {
  it('accepts absolute URLs, data URIs, and rooted/relative paths', () => {
    expect(isRenderableSrc('https://x/y.svg')).toBe(true)
    expect(isRenderableSrc('http://x/y.svg')).toBe(true)
    expect(isRenderableSrc('data:image/png;base64,AAAA')).toBe(true)
    expect(isRenderableSrc('/a/b.svg')).toBe(true)
    expect(isRenderableSrc('./b.svg')).toBe(true)
    expect(isRenderableSrc('../b.svg')).toBe(true)
  })

  it('rejects bare tokens and empties', () => {
    expect(isRenderableSrc('grafana.svg')).toBe(false)
    expect(isRenderableSrc('')).toBe(false)
    expect(isRenderableSrc(undefined)).toBe(false)
    expect(isRenderableSrc(null)).toBe(false)
  })
})
