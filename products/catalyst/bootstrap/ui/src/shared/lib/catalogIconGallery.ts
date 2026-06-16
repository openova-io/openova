/**
 * catalogIconGallery — #3668 (founder item §5B, the icon-picker half):
 * enumerate the icons ALREADY available to the console so the catalog edit
 * can present a VISUAL gallery (a profile-icon-picker grid) instead of a bare
 * filename text input.
 *
 * The founder's verbatim complaint: "why I am not able to see the already
 * uploaded logos? i need to have profile icon picker like approach." The
 * answer is to render the vendored `public/component-logos/*` assets — the
 * same bundled logos every blueprint card already falls back to
 * (`componentGroups.logoUrl`, the #3702 `resolveCatalogIcon` fallback) — as a
 * selectable grid of real thumbnails.
 *
 * This module is the single source of the gallery list: it reads the
 * canonical `ALL_COMPONENTS` catalog (the same list the wizard + grid read)
 * and projects every component that ships a vendored `logoUrl` into a
 * `{ id, label, url }` gallery entry. No second hand-maintained list, no
 * per-blueprint branch — one generic mechanism (founder rule #4).
 *
 * Pure + dependency-light (just the static component catalog) so it is
 * unit-testable in isolation and cheap to import into the picker.
 */

import { ALL_COMPONENTS } from '@/pages/wizard/steps/componentGroups'
import { isRenderableSrc } from './resolveCatalogIcon'

/** One selectable icon in the visual picker gallery. */
export interface GalleryIcon {
  /** Component id the asset belongs to (e.g. `grafana`) — the gallery key. */
  id: string
  /** Human label shown under / as the thumbnail's alt + title. */
  label: string
  /** The resolvable `<img src>` for the vendored asset. */
  url: string
}

/**
 * AVAILABLE_ICONS — every vendored component logo, as a sorted gallery list.
 *
 * Derived from `ALL_COMPONENTS`: a component contributes an entry iff its
 * `logoUrl` is a renderable src (an actual vendored asset — `logoUrl: null`
 * components draw a letter-mark and have nothing to show in the gallery).
 * De-duped by url (two components can't share a vendored file today, but the
 * guard keeps the grid clean), sorted by label for a stable, scannable grid.
 */
export const AVAILABLE_ICONS: GalleryIcon[] = (() => {
  const seenUrl = new Set<string>()
  const out: GalleryIcon[] = []
  for (const c of ALL_COMPONENTS) {
    const url = (c.logoUrl ?? '').trim()
    if (!isRenderableSrc(url) || seenUrl.has(url)) continue
    seenUrl.add(url)
    out.push({ id: c.id, label: c.name, url })
  }
  out.sort((a, b) => a.label.localeCompare(b.label))
  return out
})()

/**
 * findGalleryIcon — the gallery entry whose url matches `src`, or undefined.
 * Lets the picker highlight the currently-selected thumbnail when the field's
 * value is one of the vendored assets (vs a custom URL / uploaded data: URI).
 */
export function findGalleryIcon(src: string | undefined | null): GalleryIcon | undefined {
  const v = (src ?? '').trim()
  if (v === '') return undefined
  return AVAILABLE_ICONS.find((g) => g.url === v)
}
