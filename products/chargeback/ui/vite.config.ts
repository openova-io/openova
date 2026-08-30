/// <reference types="vitest" />
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

// bp-chargeback UI (EPIC #6723, ADR-0014 D4/D5).
//
// Built into the Go binary via embed (products/chargeback/Containerfile
// stage 1 runs `npm ci && npm run build` here; the Go stage embeds
// ui/dist/). The binary serves the bundle at `/` and the JSON API at
// `/api/v1`, so `base: '/'` and the same-origin fetch in src/api/client.ts
// are the whole wiring — no runtime config, no env-injected API base.
export default defineConfig({
  base: '/',
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    port: 5174,
    proxy: {
      // Dev only: forward the API to a locally running chargeback binary
      // (LISTEN_ADDR=:8080). The cookie is same-site through the proxy, so
      // the PIN → cb_session flow works unchanged.
      '/api': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
  test: {
    environment: 'node',
    include: ['src/**/*.test.ts'],
  },
})
