import { defineConfig } from 'astro/config';
import svelte from '@astrojs/svelte';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  base: '/nova',
  integrations: [svelte()],
  vite: {
    plugins: [tailwindcss()],
    server: {
      proxy: {
        '/api': {
          // Local-dev only (astro dev): point at a locally running gateway
          // (core/services/gateway, default :8080) or override via env.
          // The old hardcoded target was a dead pre-rename mothership host.
          target: process.env.CONSOLE_DEV_API_PROXY ?? 'http://localhost:8080',
          changeOrigin: true,
        },
      },
    },
  },
  output: 'static',
});
