/**
 * resolveCatalogIcon — #3668 §5B: the SINGLE icon-resolution path for the
 * catalog (hero, grid card, /apps tile, instance page).
 *
 * The model (ticket §2): "the rendered icon is the IaC icon
 * (`spec.card.iconLight` / `iconDark`, theme-aware), not a value baked into
 * the console bundle." Before #3668 the catalog detail/grid read a build-time
 * TypeScript table (`componentGroups.logoUrl`) and DISCARDED the API's
 * `card.iconLight` / `iconDark`, so an admin's icon edit landed in IaC + the
 * API response and was thrown away at the last render step — the edit was
 * theater.
 *
 * Resolution order (founder rule #2 — render = read the source):
 *   1. the IaC icon for the active theme (`card.iconDark` in dark theme,
 *      `card.iconLight` in light theme), with the OTHER theme's icon as a
 *      same-source fallback so a one-theme edit still renders in both;
 *   2. the legacy single `card.icon` IF it is a console-resolvable src (an
 *      absolute URL or a rooted/relative path — NOT a bare SVG filename,
 *      which has no resolvable base and must fall through);
 *   3. the build-time vendored asset (`componentGroups.logoUrl`) — the
 *      pre-#3668 source, now the fallback when the IaC carries no icon;
 *   4. null → the caller renders the letter-mark.
 *
 * This is one generic mechanism for every blueprint (founder rule #4): no
 * per-blueprint branch, no per-field special-casing. Pure — unit-testable in
 * isolation.
 */

export type IconTheme = 'light' | 'dark'

export interface CatalogIconCard {
  icon?: string
  iconLight?: string
  iconDark?: string
}

/** isRenderableSrc — true when `s` is something an <img src> can resolve:
 *  an absolute URL (http/https), a data: URI, or a rooted/relative path
 *  ('/x', './x', '../x'). A bare token like "grafana.svg" has no
 *  console-resolvable base (the pre-#3668 reason `card.icon` was discarded)
 *  so it is NOT renderable as a src. */
export function isRenderableSrc(s: string | undefined | null): boolean {
  const v = (s ?? '').trim()
  if (v === '') return false
  return (
    /^https?:\/\//i.test(v) ||
    v.startsWith('data:') ||
    v.startsWith('/') ||
    v.startsWith('./') ||
    v.startsWith('../')
  )
}

/**
 * resolveCatalogIcon — the IaC-first icon src for a catalog card.
 *
 * @param card     the catalog item's card (carries the IaC icons).
 * @param theme    the active console theme.
 * @param fallback the build-time vendored asset URL (e.g.
 *                 `findComponent(name)?.logoUrl`) — used ONLY when the IaC
 *                 carries no usable icon. Pass null when there is none.
 * @returns the resolved <img src>, or null when nothing resolves (letter-mark).
 */
export function resolveCatalogIcon(
  card: CatalogIconCard | undefined,
  theme: IconTheme,
  fallback: string | null | undefined,
): string | null {
  const light = (card?.iconLight ?? '').trim()
  const dark = (card?.iconDark ?? '').trim()

  // 1. theme-preferred IaC icon, with the other theme's IaC icon as a
  //    same-source fallback (a one-theme edit still renders in both themes).
  const themed = theme === 'dark' ? dark || light : light || dark
  if (isRenderableSrc(themed)) return themed

  // 2. the legacy single icon, only when it is itself a resolvable src.
  if (isRenderableSrc(card?.icon)) return (card!.icon as string).trim()

  // 3. the build-time vendored asset (pre-#3668 source → now the fallback).
  const fb = (fallback ?? '').trim()
  if (fb !== '') return fb

  // 4. letter-mark.
  return null
}
