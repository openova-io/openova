/**
 * Mode-aware route path builder.
 *
 * On a Sovereign cluster (`console.<sov-fqdn>`), operator-facing pages
 * live at clean root URLs:
 *
 *   /dashboard
 *   /apps
 *   /apps/$componentId
 *   /jobs
 *   /jobs/$jobId
 *   /cloud
 *   /users
 *   /users/new
 *   /users/$name
 *   /settings
 *   /settings/marketplace
 *   /catalog
 *   /parent-domains
 *
 * On the contabo mothership wizard (`console.openova.io/sovereign/...`),
 * the same pages live under the per-deployment transient prefix
 * `/provision/$deploymentId/...` because the operator monitors many
 * different deployments from one mothership.
 *
 * Internal `<Link to=...>` and `router.navigate({to:...})` calls MUST go
 * through this helper so a single component renders with the correct
 * URL on both surfaces. Per docs/INVIOLABLE-PRINCIPLES.md #4 — never
 * hardcode paths in callers.
 *
 * The helper accepts both inputs (page name + optional deploymentId)
 * and returns the target path. On Sovereign the deploymentId is silently
 * ignored — the URL stays clean.
 */

import { DETECTED_MODE } from './detectMode'

export type SovereignPage =
  | '' // root → /dashboard on sovereign, /provision/$id on contabo
  | 'dashboard'
  | 'apps'
  | 'jobs'
  | 'cloud'
  | 'users'
  | 'settings'
  | 'settings/marketplace'
  | 'notifications'
  | 'parent-domains'
  | 'catalog'

interface PathOptions {
  /** Required on contabo (mothership). Ignored on Sovereign. */
  deploymentId?: string
  /** Optional sub-path appended after the page (e.g. a job id). */
  sub?: string
}

/**
 * Return the route path for an operator-facing page. Mode-aware:
 *
 *   sovereignPath('dashboard')                              → '/dashboard'   (sovereign)
 *   sovereignPath('dashboard', { deploymentId: 'abc123' })  → '/provision/abc123/dashboard' (contabo)
 *   sovereignPath('jobs', { deploymentId, sub: 'jid42' })   → '/jobs/jid42' or '/provision/$id/jobs/jid42'
 *   sovereignPath('apps', { deploymentId, sub: '$cid' })    → '/apps/$cid'  or '/provision/$id/app/$cid'
 *   sovereignPath('')                                       → '/dashboard'  on sovereign, '/provision/$id' on contabo
 */
export function sovereignPath(page: SovereignPage, opts: PathOptions = {}): string {
  const { deploymentId = '', sub } = opts
  const isSovereign = DETECTED_MODE.mode === 'sovereign'

  // Special-case: contabo's /provision/$id root view is the apps page; on
  // sovereign the root view redirects to /dashboard.
  if (page === '') {
    return isSovereign ? '/dashboard' : `/provision/${deploymentId}`
  }

  // Apps detail: contabo mounts it at /provision/$id/app/$componentId;
  // on sovereign it's /apps/$componentId.
  if (page === 'apps' && sub) {
    return isSovereign ? `/apps/${sub}` : `/provision/${deploymentId}/app/${sub}`
  }

  if (isSovereign) {
    return sub ? `/${page}/${sub}` : `/${page}`
  }
  return sub ? `/provision/${deploymentId}/${page}/${sub}` : `/provision/${deploymentId}/${page}`
}

/**
 * sovereignPathOrDeployments — `sovereignPath` with an id-less mothership
 * fallback (#4704 Task B).
 *
 * On the mothership every per-deployment page lives under
 * `/provision/$deploymentId/...`. When a caller has NO deployment id
 * (SSE not landed yet, stale store, deep-link without context), the old
 * fallbacks emitted a bare `/<page>` — and on the mothership a
 * `/provision/${''}/<page>` template collapses via path canonicalisation
 * to `/provision/<page>`, so the literal word ("jobs", "dashboard", …)
 * lands in the `$deploymentId` slot. The UI then polls
 * `/api/v1/deployments/jobs` (404) and banners
 * "The path segment \"jobs\" is not a valid deployment id".
 *
 * Rule: in mothership mode a per-deployment link MUST carry a real
 * deployment id; without one, route to the deployments list instead.
 * Sovereign mode is untouched — clean root URLs (`/jobs`, `/dashboard`)
 * are the canonical surface there and need no id.
 */
export function sovereignPathOrDeployments(
  page: SovereignPage,
  opts: PathOptions = {},
): string {
  const isSovereign = DETECTED_MODE.mode === 'sovereign'
  if (!isSovereign && !opts.deploymentId) return '/deployments'
  return sovereignPath(page, opts)
}
