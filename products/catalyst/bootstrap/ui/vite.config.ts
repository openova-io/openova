import { defineConfig, type Plugin } from 'vite'
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
  base: '/sovereign/',
  plugins: [tailwindcss(), react(), rebuildCatalogOnYamlChange()],
  resolve: {
    alias: {
      '@': resolve(__dirname, './src'),
    },
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
})
