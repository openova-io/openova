/**
 * Compute the current page path relative to the router basepath.
 *
 * Issue #1090 (cluster B): the mothership auth guards need to preserve
 * the operator's deep-link target across the PIN flow so the POST-verify
 * navigate({ to: next }) lands them BACK on the requested page. The
 * `next` value MUST be the POST-basepath route path because TanStack
 * Router's `navigate` resolves relative to the configured basepath:
 *
 *   On contabo (Catalyst-Zero) basepath = '/sovereign'.
 *   On Sovereign clusters basepath = '/'.
 *
 *   raw browser path     →  pathRelativeToBasepath()
 *   /sovereign/dashboard →  /dashboard
 *   /sovereign/cloud?v=g →  /cloud?v=g
 *   /dashboard           →  /dashboard
 *   /cloud?v=g           →  /cloud?v=g
 *
 * Always returns a path that begins with `/`, including search params.
 */
export function pathRelativeToBasepath(
  pathname: string,
  search: string = '',
): string {
  let p = pathname + search
  if (p.startsWith('/sovereign/')) p = p.slice('/sovereign'.length)
  else if (p === '/sovereign') p = '/'
  if (!p.startsWith('/')) p = '/' + p
  return p
}

/**
 * Browser-aware variant — reads `window.location` and falls back to '/'
 * outside a browser (SSR / unit tests without jsdom). Pure callers
 * (e.g. unit tests of the function itself) should use the parameterised
 * `pathRelativeToBasepath` directly.
 */
export function currentPathRelativeToBasepath(): string {
  if (typeof window === 'undefined') return '/'
  return pathRelativeToBasepath(window.location.pathname, window.location.search)
}
