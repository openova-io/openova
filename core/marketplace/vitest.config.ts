// #5634 (UAT row 92) — unit tests for the funnel's plain TypeScript modules.
// The page-level Astro/Svelte surfaces stay covered by the Playwright suite in
// `playwright/`; this config only picks up `src/**/*.test.ts`.
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'jsdom',
    include: ['src/**/*.test.ts'],
  },
});
