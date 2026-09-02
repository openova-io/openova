/**
 * AppConsoleRoute.test.tsx — #6805.
 *
 * A Blueprint may register its own Sovereign-console sidebar entry through
 * `spec.consoleUI.sidebarRoute` (#2370, EPIC #6723 lane C). Two shipped
 * Blueprints use it — bp-agenity and bp-chargeback — and both declared
 * `/apps/<id>/dashboard`, a shape the router did not serve: it had `/apps`
 * (the grid) and the singular `/app/$componentId[/$tab]` and nothing else
 * under `/apps/`. Clicking either entry rendered the SPA's Not Found page.
 * Reported live on hw307 for both entries, 2026-09-02.
 *
 * Two layers here, because the defect had two halves:
 *   1. the ROUTE-EXISTS guard below is what would have caught it — every
 *      Blueprint-declared sidebarRoute must be matched by a real router path;
 *   2. the state tests pin what the route does once it exists, including the
 *      three states where there is no front door to open. Those are not
 *      interchangeable: "not in the catalog", "not installed" and "installed
 *      but still coming up" ask different things of the operator.
 */
import { readFileSync, readdirSync, existsSync } from 'node:fs'
import { join } from 'node:path'
import { describe, it, expect } from 'vitest'
import { load } from 'js-yaml'
import { resolveAppConsoleState, launchKeyFor, appLabel, type AppRow } from './AppConsoleRoute'

// ── 1. the guard ────────────────────────────────────────────────────────────
// Resolved from the vitest cwd (the ui package root) like the other fs-based
// suites here; the repo root is four levels up
// (products/catalyst/bootstrap/ui).
const REPO_ROOT = join(process.cwd(), '..', '..', '..', '..')
const ROUTER = join(process.cwd(), 'src', 'app', 'router.tsx')

/** Every `path: '...'` literal declared in router.tsx. */
function routerPaths(): string[] {
  const src = readFileSync(ROUTER, 'utf8')
  return [...src.matchAll(/^\s*path: '([^']+)'/gm)].map((m) => m[1])
}

/**
 * A declared route matches a router path when they have the same number of
 * segments and every router segment either equals the declared one or is a
 * `$param` placeholder. Segment count is part of the match on purpose:
 * `/apps` does NOT serve `/apps/bp-agenity/dashboard`, which is precisely the
 * mistake this guard exists to catch.
 */
export function routeIsServed(declared: string, paths: string[]): boolean {
  const want = declared.split('/').filter(Boolean)
  return paths.some((p) => {
    const got = p.split('/').filter(Boolean)
    if (got.length !== want.length) return false
    return got.every((seg, i) => seg.startsWith('$') || seg === want[i])
  })
}

/** Blueprint files that register a sidebar entry, as `{file, route}`. */
function declaredSidebarRoutes(): Array<{ file: string; route: string }> {
  const out: Array<{ file: string; route: string }> = []
  for (const dir of ['products', 'platform']) {
    const base = join(REPO_ROOT, dir)
    if (!existsSync(base)) continue
    for (const entry of readdirSync(base)) {
      const file = join(base, entry, 'blueprint.yaml')
      if (!existsSync(file)) continue
      let doc: unknown
      try {
        doc = load(readFileSync(file, 'utf8'))
      } catch {
        continue // a chart-authoring suite owns YAML validity, not this guard
      }
      const spec = (doc as { spec?: { consoleUI?: { sidebarRoute?: unknown } } })?.spec
      const route = spec?.consoleUI?.sidebarRoute
      if (typeof route === 'string' && route !== '') out.push({ file: `${dir}/${entry}`, route })
    }
  }
  return out
}

describe('Blueprint-declared sidebar routes', () => {
  const declared = declaredSidebarRoutes()
  const paths = routerPaths()

  it('finds the shipped declarations — the guard is never vacuous', () => {
    // Without this, deleting every consoleUI block (or breaking the walk)
    // would turn the check below into an empty loop that always passes.
    expect(paths.length).toBeGreaterThan(20)
    const routes = declared.map((d) => d.route)
    expect(routes).toContain('/apps/bp-agenity/dashboard')
    expect(routes).toContain('/apps/bp-chargeback/dashboard')
  })

  it('serves every declared sidebarRoute from a real router path', () => {
    const unserved = declared.filter((d) => !routeIsServed(d.route, paths))
    expect(unserved).toEqual([])
  })

  it('rejects the pre-fix router — a shorter path must not claim a deeper route', () => {
    // The exact state before #6805: `/apps` and `/app/$componentId[/$tab]`
    // existed and nothing served the declared route.
    const before = ['/apps', '/app/$componentId', '/app/$componentId/$tab']
    expect(routeIsServed('/apps/bp-agenity/dashboard', before)).toBe(false)
    expect(routeIsServed('/apps/bp-agenity/dashboard', ['/apps/$appId/$view'])).toBe(true)
    expect(routeIsServed('/apps/bp-agenity', ['/apps/$appId'])).toBe(true)
  })
})

// ── 2. what the route does ──────────────────────────────────────────────────
const INSTALLED: AppRow = { id: 'bp-chargeback', status: 'ready', externalURL: 'https://chargeback.example' }

describe('resolveAppConsoleState', () => {
  it('prefers the silent-SSO launch URL over the raw external URL', () => {
    expect(resolveAppConsoleState('bp-chargeback', INSTALLED, 'https://sso.example/x', true)).toEqual({
      kind: 'launch',
      url: 'https://sso.example/x',
    })
  })

  it('falls back to the external URL when the app has no SSO endpoint', () => {
    expect(resolveAppConsoleState('bp-chargeback', INSTALLED, null, true)).toEqual({
      kind: 'launch',
      url: 'https://chargeback.example',
    })
  })

  it('reports installed-but-not-up rather than launching nowhere', () => {
    // The live hw307 case: bp-chargeback present, its pre-install hook unable
    // to pull its image, so no front door exists yet.
    const row: AppRow = { id: 'bp-chargeback', status: 'installing' }
    expect(resolveAppConsoleState('bp-chargeback', row, null, true)).toEqual({
      kind: 'installing',
      appId: 'bp-chargeback',
      status: 'installing',
    })
  })

  it('defaults a missing status rather than rendering an empty state', () => {
    expect(resolveAppConsoleState('bp-agenity', { id: 'bp-agenity' }, null, true)).toEqual({
      kind: 'installing',
      appId: 'bp-agenity',
      status: 'pending',
    })
  })

  it('separates not-installed from unknown — they need different actions', () => {
    expect(resolveAppConsoleState('bp-agenity', undefined, null, true).kind).toBe('not-installed')
    expect(resolveAppConsoleState('bp-nope', undefined, null, false).kind).toBe('unknown-app')
  })
})

describe('launch key and label', () => {
  it('strips the bp- prefix the backend resolver strips', () => {
    expect(launchKeyFor('bp-agenity')).toBe('agenity')
    expect(launchKeyFor('shared-pg')).toBe('shared-pg')
  })

  it('labels an app from its id without inventing a title', () => {
    expect(appLabel('bp-chargeback')).toBe('Chargeback')
    expect(appLabel('bp-agenity')).toBe('Agenity')
  })
})
