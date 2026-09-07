import type { TagDimension } from '../api/types'

/**
 * Tag dimensions (EPIC #6867 follow-up). A resource tag is the cost-allocation
 * dimension cloud consoles group by; here it is the dynamic dimension
 * `tag:<key>`. The key rule mirrors the server's (store.TagKeyRule): a key
 * that fails it is refused with a 400 there, so the UI never offers one.
 */
export const TAG_PREFIX = 'tag:'
export const TAG_KEY_RE = /^[A-Za-z0-9_.:/@-]{1,128}$/
/** The group records without the key fall into (server constant). */
export const UNTAGGED = '(untagged)'

export function isValidTagKey(key: string): boolean {
  return TAG_KEY_RE.test(key)
}

/** The key of a `tag:<key>` dimension, or null when it is not a valid tag dimension. */
export function tagKeyOf(dim: string | null | undefined): string | null {
  if (!dim || !dim.startsWith(TAG_PREFIX)) return null
  const key = dim.slice(TAG_PREFIX.length)
  return isValidTagKey(key) ? key : null
}

export function isTagDim(dim: string | null | undefined): dim is TagDimension {
  return tagKeyOf(dim) !== null
}

export function tagDim(key: string): TagDimension {
  return `${TAG_PREFIX}${key}`
}

export interface TagEntry {
  key: string
  value: string
}

/**
 * The tags of a resource as the collectors store them (attrs.tags, an object
 * key → value), sorted by key. Anything that is not an object yields none.
 */
export function tagEntries(v: unknown): TagEntry[] {
  if (!v || typeof v !== 'object' || Array.isArray(v)) return []
  return Object.entries(v as Record<string, unknown>)
    .filter(([k]) => k !== '')
    .map(([k, val]) => ({ key: k, value: val === null || val === undefined ? '' : String(val) }))
    .sort((a, b) => a.key.localeCompare(b.key))
}
