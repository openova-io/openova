import { defineConfig } from 'astro/config';
import svelte from '@astrojs/svelte';

// G117 Catalyst console — Astro+Svelte 5 shell. The new G117 surfaces live as
// Svelte components under `src/routes/**/+page.svelte` (SvelteKit-style file
// naming kept for future migration), wrapped in thin Astro pages under
// `src/pages/` per the bridge documented in src/routes/README.md.
export default defineConfig({
  output: 'static',
  integrations: [svelte()],
  server: {
    port: 4323,
    host: '0.0.0.0',
  },
  vite: {
    server: {
      proxy: {
        // Catalyst API base path. Set CATALYST_API_TARGET to point at a real
        // Sovereign's catalyst-api (e.g. https://api.hw86.omani.works). When
        // unset, falls back to the local mock backend so dev + Playwright work
        // without a live cluster.
        '/catalyst/v1': {
          target: process.env.CATALYST_API_TARGET || 'http://127.0.0.1:4555',
          changeOrigin: true,
        },
      },
    },
  },
});
