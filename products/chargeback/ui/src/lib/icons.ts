// Icons and branding (DESIGN.md §22.10). BSS holds every visual of the
// package table — content-addressed icons, a tile colour per feature, an
// accent and a badge per package — and the packages document names them.
// These are the pure rules the console and the public calculator draw by.

import type { IconRef } from '../api/types'

// The API's own path, as api/client's API_BASE (not imported: pages that
// mock the client still draw icons).
const API_BASE = '/api/v1'

/** Where an icon id is published: the path the document's `src` carries. */
export const ICON_PUBLIC_PREFIX = '/api/v1/public/icons/'

/** The content types an icon may have, as the upload accepts them. */
export const ICON_TYPES = ['image/svg+xml', 'image/png', 'image/webp'] as const

/** The largest icon BSS keeps: 64 KiB. */
export const ICON_MAX_BYTES = 64 * 1024

/** The longest badge a package carries. */
export const BADGE_MAX = 24

/** The document URL `src` is resolved against: the public packages document on the API's own origin. */
export function packagesDocumentUrl(): string {
  const here = typeof window !== 'undefined' && window.location ? window.location.href : 'http://localhost/'
  return new URL(API_BASE + '/public/packages', here).toString()
}

/**
 * An icon's `src` resolved against the URL of the document that named it.
 * `src` is a path relative to the origin that served the document, so a page
 * that read the document from another origin (the storefront) still loads
 * the bytes from that origin. An absolute or malformed `src` is returned as
 * it is.
 */
export function resolveIconSrc(src: string, documentUrl: string = packagesDocumentUrl()): string {
  if (!src) return ''
  try {
    return new URL(src, documentUrl).toString()
  } catch {
    return src
  }
}

/** The path of a stored icon by id ("" for none). */
export function iconSrc(id: string | null | undefined): string {
  return id ? ICON_PUBLIC_PREFIX + id : ''
}

/** The id a published icon names: the last segment of its `src`. */
export function iconIdOf(icon: IconRef | null | undefined): string {
  if (!icon?.src) return ''
  const seg = icon.src.split('?')[0].split('/').filter(Boolean)
  const last = seg[seg.length - 1] ?? ''
  return /^[0-9a-f]{64}$/.test(last) ? last : ''
}

/** A stored icon as the document would name it, or undefined when there is none. */
export function iconRefOf(id: string | null | undefined, alt: string, bg?: string | null): IconRef | undefined {
  if (!id) return undefined
  return bg ? { src: iconSrc(id), alt, bg } : { src: iconSrc(id), alt }
}

const COLOUR = /^#[0-9A-Fa-f]{6}$/

/** "#RRGGBB" or empty — the only colours BSS stores. */
export function isColour(v: string): boolean {
  return v.trim() === '' || COLOUR.test(v.trim())
}

/** The colour as BSS stores it: upper-case, or "" for none. */
export function normalizeColour(v: string): string {
  return v.trim() === '' ? '' : v.trim().toUpperCase()
}

/** Why a file cannot be uploaded as an icon, or "" when it can. */
export function iconFileProblem(file: { type: string; size: number; name?: string }): string {
  if (!(ICON_TYPES as readonly string[]).includes(file.type)) return `${file.name || 'the file'} is ${file.type || 'of an unknown type'}; an icon is an SVG, PNG or WebP image`
  if (file.size > ICON_MAX_BYTES) return `${file.name || 'the file'} is ${Math.ceil(file.size / 1024)} KiB; an icon is at most 64 KiB`
  if (file.size === 0) return `${file.name || 'the file'} is empty`
  return ''
}

/** Why a badge cannot be saved, or "" when it can. */
export function badgeProblem(v: string): string {
  return [...v.trim()].length > BADGE_MAX ? `a badge is at most ${BADGE_MAX} characters` : ''
}
