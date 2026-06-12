/// <reference types="vitest" />
import type { Plugin } from 'vite'
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { resolve } from 'path'
import { spawnSync } from 'node:child_process'

const REPO_ROOT = resolve(__dirname, '../../../..')

/**
 * Vite plugin that re-runs scripts/build-catalog.mjs whenever any
 * platform/<name>/blueprint.yaml or products/<name>/blueprint.yaml changes
 * during dev. Keeps the wizard's StepComponents grid live-reloading without
 * requiring a manual `npm run build:catalog` after every Blueprint edit.
 *
 * Build mode runs the same script via `prebuild` in package.json, so this
 * plugin only matters in dev.
 */
function rebuildCatalogOnYamlChange(): Plugin {
  const catalogScript = resolve(__dirname, 'scripts/build-catalog.mjs')

  function runCatalog(reason: string) {
    const r = spawnSync(process.execPath, [catalogScript], { stdio: 'inherit' })
    if (r.status !== 0) {
      // eslint-disable-next-line no-console
      console.error(`[build-catalog] failed (${reason})`)
    }
  }

  return {
    name: 'catalyst-rebuild-catalog',
    apply: 'serve',
    configureServer(server) {
      // Watch the entire monorepo's platform/ + products/ for blueprint.yaml
      // changes. Vite's chokidar instance handles dedup + glob.
      server.watcher.add([
        resolve(REPO_ROOT, 'platform/**/blueprint.yaml'),
        resolve(REPO_ROOT, 'products/**/blueprint.yaml'),
      ])
      const isBlueprintFile = (path: string) => /(?:^|\/)blueprint\.yaml$/.test(path)
      const handle = (event: string) => (path: string) => {
        if (!isBlueprintFile(path)) return
        runCatalog(`${event} ${path}`)
        server.ws.send({ type: 'full-reload', path: '*' })
      }
      server.watcher.on('add', handle('add'))
      server.watcher.on('change', handle('change'))
      server.watcher.on('unlink', handle('unlink'))
    },
  }
}

export default defineConfig({
  // base: '/' — path-agnostic for all deployment contexts.
  //
  // On Sovereigns (console.<sov>.omani.works/) the HTTPRoute passes
  // through to nginx root; assets must be at /assets/*, not
  // /sovereign/assets/*. On contabo (console.openova.io/sovereign/*),
  // the Traefik strip-sovereign Middleware strips the /sovereign prefix
  // BEFORE forwarding to nginx, so nginx still sees /assets/* — both
  // cases resolve correctly with base: '/'.
  //
  // Issue #596: the previous base: '/sovereign/' caused blank pages on
  // every Sovereign cluster because the browser requested
  // /sovereign/assets/index-*.js but nginx (serving dist at /) returned 404.
  base: '/',
  plugins: [tailwindcss(), react(), rebuildCatalogOnYamlChange()],
  resolve: {
    alias: {
      // OpenovaFlow Foundation — the @openova/* packages live in
      // products/openova-flow/{core,canvas}/. They publish source-only
      // entry points (./src/index.ts) so Vite + Vitest + tsc all bind
      // straight to TS source. Workspaces (root package.json) hoist
      // react/react-dom/d3-* into the monorepo's node_modules tree so
      // the canvas source's bare imports resolve via standard
      // upward-walking node_modules — no per-package node_modules
      // sibling needed. Per-package aliases are listed BEFORE the '@'
      // alias because @rollup/plugin-alias matches whole-name (so
      // ordering is academic) but the documented convention is
      // "longer key first" defensively.
      '@openova/flow-core': resolve(__dirname, '../../../openova-flow/core/src/index.ts'),
      '@openova/flow-canvas': resolve(__dirname, '../../../openova-flow/canvas/src/index.ts'),
      '@': resolve(__dirname, './src'),
    },
    // Workspaces install one copy of react/react-dom at the monorepo
    // root; dedupe makes sure both the catalyst-ui bundle AND the
    // flow-canvas source bind to the same instance (otherwise React
    // throws "invalid hook call" when two copies coexist).
    dedupe: ['react', 'react-dom'],
  },
  server: {
    port: 5173,
    proxy: {
      // With base: '/', API_BASE = '/api' in dev too. Direct proxy to
      // catalyst-api on localhost:8080 — no prefix rewrite needed.
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
      // #3370 — the /catalyst/v1/* route group (instances, contexts,
      // launch-url, endpoints) is served WITHOUT the /api prefix in
      // production (the bp-catalyst-platform HTTPRoute forwards
      // PathPrefix /catalyst/ to catalyst-api — see catalog.api.ts
      // getApplicationInstances). Mirror that here so the dev server
      // can exercise the instances/Contexts surfaces too.
      '/catalyst': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
      // #3370 — /auth/handover is served SERVER-SIDE in production (the
      // HTTPRoute hands it to catalyst-api, which validates the RS256
      // handover JWT and sets the HttpOnly catalyst_session cookie
      // BEFORE the SPA ever loads). Proxy it in dev for the same
      // behaviour so the real handover-redemption flow works against a
      // local catalyst-api.
      '/auth/handover': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  // Vitest config — drives `npm run test` in this package. The test runner
  // shares Vite's `resolve.alias` so `@/...` imports work in tests too.
  test: {
    environment: 'jsdom',
    globals: false,
    css: false,
    include: ['src/**/*.test.{ts,tsx}'],
    setupFiles: ['src/test/setup.ts'],
  },
})
