// @vitest-environment happy-dom
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ICON_BACKUP, ICON_PLAN_M, ICON_RESILIENCE, ICON_SSL, brandedPackages } from '../panels/estimate/fixture'

/**
 * Icons and branding on the Packages tab, walked in a browser document
 * (DESIGN.md §22.10): the matrix shows each feature's icon on its tile, the
 * group's icon on its heading, the column's accent and badge, the floor
 * item's icon — and nothing where none is set. The feature modal picks an
 * icon from what BSS holds and a tile colour, and saves them on the
 * feature; Upload POSTs the raw file under its own type and takes the id the
 * server answers; a file that is not an icon is refused before it is sent.
 * The package settings modal saves the icon, the accent and the badge with
 * the rest of the settings, whole. A group's icon is set from its heading.
 */

;(globalThis as unknown as { IS_REACT_ACT_ENVIRONMENT: boolean }).IS_REACT_ACT_ENVIRONMENT = true

const UPLOADED = '9'.repeat(64)
const features = {
  features: [
    { id: 'f0', key: 'ssl', name: 'Unlimited free SSL', blurb: '', kind: 'boolean', group: 'floor', teaser: false, sort_order: 0, icon_id: ICON_SSL },
    { id: 'f1', key: 'bandwidth', name: 'Bandwidth', blurb: '', kind: 'quantity', group: 'capacity', unit: 'Mbps', addon_sku: 'eip.bandwidth_mbps', teaser: false, sort_order: 1 },
    { id: 'f7', key: 'backup', name: 'Backup', blurb: '', kind: 'boolean', group: 'resilience', addon_sku: 'addon.backup', teaser: false, sort_order: 7, icon_id: ICON_BACKUP, icon_bg: '#FFE4E6' },
  ],
  groups: brandedPackages.groups,
}
const stored = [
  { id: ICON_BACKUP, src: `/api/v1/public/icons/${ICON_BACKUP}`, content_type: 'image/svg+xml', size: 300, references: 1 },
  { id: ICON_SSL, src: `/api/v1/public/icons/${ICON_SSL}`, content_type: 'image/svg+xml', size: 280, references: 1 },
]

vi.mock('../lib/useQuery', () => ({
  useQuery: (path: string | null) => ({
    data: path === '/pricebooks/pb1/packages' ? brandedPackages : path === '/features' ? features : null,
    error: '',
    loading: false,
    reload: async () => {},
    setData: () => {},
  }),
}))

const writes: Array<{ method: string; path: string; body: unknown; type?: string }> = []

vi.mock('../api/client', () => ({
  api: {
    get: async (path: string) => (path === '/icons' ? { icons: stored } : {}),
    put: async (path: string, body: unknown) => {
      writes.push({ method: 'PUT', path, body })
      return {}
    },
    patch: async (path: string, body: unknown) => {
      writes.push({ method: 'PATCH', path, body })
      return { ...(body as object), id: 'f', name: 'saved' }
    },
    post: async (path: string, body: unknown) => {
      writes.push({ method: 'POST', path, body })
      return { id: 'f', name: 'saved' }
    },
    postRaw: async (path: string, body: unknown, type: string) => {
      writes.push({ method: 'POST', path, body, type })
      return { id: UPLOADED, src: `/api/v1/public/icons/${UPLOADED}` }
    },
    del: async () => ({}),
  },
  asList: (data: unknown, key: string) => ((data as Record<string, unknown[]>)?.[key] ?? []) as unknown[],
  errorText: (e: unknown) => String(e),
}))

import { PackagesPanel } from './PackagesPanel'

const book = { id: 'pb1', name: 'OpenOva plans', scope: 'platform', currency: 'OMR', annual_divisor: 8760, bill_stopped: 'compute', created_at: '2026-01-01T00:00:00Z', items: [] } as never

let host: HTMLDivElement
let root: Root | undefined

beforeEach(() => {
  writes.length = 0
  host = document.createElement('div')
  document.body.appendChild(host)
})

afterEach(async () => {
  await act(async () => root?.unmount())
  host.remove()
})

async function mount() {
  await act(async () => {
    root = createRoot(host)
    root.render(<PackagesPanel book={book} canManage />)
  })
}

const q = <T extends Element = HTMLElement>(sel: string) => host.querySelector(sel) as T | null
const byText = (sel: string, text: string) => Array.from(host.querySelectorAll<HTMLElement>(sel)).find((e) => e.textContent?.trim() === text) ?? null
const click = async (el: Element | null) => {
  expect(el).not.toBeNull()
  await act(async () => {
    ;(el as HTMLElement).click()
  })
}
const type = async (el: HTMLInputElement | null, value: string) => {
  expect(el).not.toBeNull()
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')!.set!
    setter.call(el, value)
    el!.dispatchEvent(new Event('input', { bubbles: true }))
  })
}

describe('icons and branding on the Packages tab', () => {
  it('shows each icon, tile, accent and badge the book carries — and nothing where none is set', async () => {
    await mount()
    const backupRow = q('[data-testid="feature-row-backup"]')!
    const tile = backupRow.querySelector<HTMLElement>('.pkg-icon.tiled')
    expect(tile?.style.background).toMatch(/#ffe4e6|rgb\(255, 228, 230\)/i)
    expect(tile?.querySelector('img')?.getAttribute('src')).toMatch(new RegExp(`/api/v1/public/icons/${ICON_BACKUP}$`))
    expect(q('[data-testid="feature-row-bandwidth"] .pkg-icon')).toBeNull()
    expect(q('[data-testid="floor-ssl"] img')?.getAttribute('src')).toMatch(new RegExp(`${ICON_SSL}$`))
    expect(q('[data-testid="group-resilience"] img')?.getAttribute('src')).toMatch(new RegExp(`${ICON_RESILIENCE}$`))
    expect(q('[data-testid="group-capacity"] .pkg-icon')).toBeNull()
    const m = q('[data-testid="pkg-head-plan.m"]')!
    expect(m.className).toContain('accented')
    expect(m.style.borderTopColor).toMatch(/#3b82f6|rgb\(59, 130, 246\)/i)
    expect(q('[data-testid="pkg-badge-plan.m"]')?.textContent).toBe('Most popular')
    expect(m.querySelector('img')?.getAttribute('src')).toMatch(new RegExp(`${ICON_PLAN_M}$`))
    expect(q('[data-testid="pkg-badge-plan.l"]')).toBeNull()
    expect(q('[data-testid="pkg-head-plan.l"]')!.className).not.toContain('accented')
  })

  it('picks a stored icon and a tile colour for a feature and saves both', async () => {
    await mount()
    await click(q('[data-testid="feature-row-bandwidth"] button.link'))
    expect(q('[data-testid="feature-editor"]')).not.toBeNull()
    expect(q('[data-testid="icon-preview"]')?.textContent).toBe('none')
    await click(byText('[data-testid="icon-field"] button', 'Choose existing'))
    const grid = q('[data-testid="icon-grid"]')!
    expect(grid.querySelectorAll('button[role="option"]')).toHaveLength(2)
    await click(grid.querySelectorAll('button[role="option"]')[1])
    expect(q('[data-testid="icon-grid"]')).toBeNull()
    expect(q('[data-testid="icon-preview"] img')?.getAttribute('src')).toMatch(new RegExp(`${ICON_SSL}$`))
    await type(q<HTMLInputElement>('input[aria-label="Icon background"]'), '#e0f2fe')
    await click(byText('button', 'Save'))
    const w = writes.find((x) => x.method === 'PATCH')!
    expect(w.path).toBe('/features/f1')
    expect(w.body).toMatchObject({ icon_id: ICON_SSL, icon_bg: '#E0F2FE', name: 'Bandwidth' })
  })

  it('uploads an icon file raw under its own type, refuses one that is not an icon, and removes an icon', async () => {
    await mount()
    await click(q('[data-testid="feature-row-backup"] button.link'))
    const input = q<HTMLInputElement>('[data-testid="icon-file"]')!
    const gif = new File(['GIF89a'], 'logo.gif', { type: 'image/gif' })
    await act(async () => {
      Object.defineProperty(input, 'files', { value: [gif], configurable: true })
      input.dispatchEvent(new Event('change', { bubbles: true }))
    })
    expect(q('[data-testid="icon-field"] .err')?.textContent).toMatch(/SVG, PNG or WebP/)
    expect(writes).toHaveLength(0)
    const svg = new File(['<svg xmlns="http://www.w3.org/2000/svg"/>'], 'logo.svg', { type: 'image/svg+xml' })
    await act(async () => {
      Object.defineProperty(input, 'files', { value: [svg], configurable: true })
      input.dispatchEvent(new Event('change', { bubbles: true }))
    })
    expect(writes[0]).toMatchObject({ method: 'POST', path: '/icons', type: 'image/svg+xml' })
    expect(q('[data-testid="icon-preview"] img')?.getAttribute('src')).toMatch(new RegExp(`${UPLOADED}$`))
    await click(byText('[data-testid="icon-field"] button', 'Remove'))
    await click(byText('button', 'Save'))
    const w = writes.find((x) => x.method === 'PATCH')!
    expect(w.body).toMatchObject({ icon_id: '', icon_bg: '#FFE4E6' })
  })

  it('saves the icon, the accent and the badge with the package settings, whole', async () => {
    await mount()
    await click(q('button[aria-label="Settings of L"]'))
    expect(q('[data-testid="package-settings-editor"]')).not.toBeNull()
    await type(q<HTMLInputElement>('input[aria-label="Accent"]'), '#1d4ed8')
    await type(q<HTMLInputElement>('input[aria-label="Badge"]'), 'Best value')
    await click(byText('[data-testid="icon-field"] button', 'Choose existing'))
    await click(q('[data-testid="icon-grid"] button[role="option"]'))
    await click(byText('button', 'Save'))
    const w = writes.find((x) => x.method === 'PUT')!
    expect(w.path).toBe('/pricebooks/pb1/packages/plan.l/settings')
    expect(w.body).toMatchObject({ icon_id: ICON_BACKUP, accent: '#1D4ED8', badge: 'Best value', vcpu: '4', recommended: false })
  })

  it('keeps M’s branding when its settings are saved unchanged, and refuses a malformed accent', async () => {
    await mount()
    await click(q('button[aria-label="Settings of M"]'))
    expect(q<HTMLInputElement>('input[aria-label="Badge"]')?.value).toBe('Most popular')
    await type(q<HTMLInputElement>('input[aria-label="Accent"]'), 'blue')
    expect(byText('button', 'Save')?.hasAttribute('disabled')).toBe(true)
    await type(q<HTMLInputElement>('input[aria-label="Accent"]'), '#3B82F6')
    await click(byText('button', 'Save'))
    expect(writes.find((x) => x.method === 'PUT')!.body).toMatchObject({ icon_id: ICON_PLAN_M, accent: '#3B82F6', badge: 'Most popular' })
  })

  it('sets a group’s icon from its heading', async () => {
    await mount()
    await click(q('button[aria-label="Icon of Capacity"]'))
    expect(q('[data-testid="group-icon-editor"]')).not.toBeNull()
    await click(byText('[data-testid="icon-field"] button', 'Choose existing'))
    await click(q('[data-testid="icon-grid"] button[role="option"]'))
    await click(byText('button', 'Save'))
    expect(writes).toContainEqual({ method: 'PUT', path: '/feature-groups/capacity', body: { icon_id: ICON_BACKUP } })
  })
})
