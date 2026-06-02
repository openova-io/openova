// G117 — boot-time mock installer.
//
// Imported by Astro pages when MOCK_API=1. Reads the fixture (which is
// bundled as a static asset at /mock-blueprints.yaml.json — see scripts/
// build-mock-fixture.mjs which converts the YAML to JSON at build) and
// hangs a MockBackend off window.__MOCK_API__ before any Svelte island
// mounts.
//
// Playwright tests skip this file entirely — they call page.addInitScript
// with the fixture inlined.

import { createMockBackend } from './mock';
import type { MockFixture } from './mock';

export async function installMockIfRequested(): Promise<void> {
  if (typeof window === 'undefined') return;
  // import.meta.env is the Astro-provided env bag; MOCK_API only flips at
  // dev build, never in prod.
  const flag =
    (import.meta.env as Record<string, string | undefined>).PUBLIC_MOCK_API ??
    (import.meta.env as Record<string, string | undefined>).MOCK_API;
  if (flag !== '1' && flag !== 'true') return;
  try {
    const res = await fetch('/mock-fixture.json', { cache: 'no-store' });
    if (!res.ok) {
      console.warn('mock fixture fetch non-2xx', res.status);
      return;
    }
    const fx = (await res.json()) as MockFixture;
    window.__MOCK_API__ = createMockBackend(fx);
    console.info('[catalyst-console] mock backend installed', {
      blueprints: Object.keys(fx.blueprints ?? {}),
      apps: Object.keys(fx.apps ?? {}),
    });
  } catch (e) {
    console.warn('mock fixture install failed', e);
  }
}
