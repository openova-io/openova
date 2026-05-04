/**
 * SovereigntyPreviewPage — layout-free preview surface for the
 * sovereignty cutover widget (issues #790 / #793).
 *
 * Purpose: render the production `SovereigntyCard` without the
 * SovereignConsoleLayout's OIDC/whoami auth gate so the Playwright
 * regression suite (e2e/sovereignty.spec.ts) can verify the visible
 * states deterministically — `tethered`, mid-flight, `sovereign`,
 * failed-step — without reproducing the full Sovereign-mode hostname
 * detection. The PRODUCTION mount is `ConsoleDashboardPage`; this
 * preview is a parallel mount that keeps the same component but
 * removes the auth shell.
 *
 * The page renders the card unmodified and lets the live SSE consumer
 * hook drive state from the API. The Playwright spec mocks the GET
 * /status response + the SSE stream via route interception.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md:
 *   #2 (never compromise): this is a test harness, NOT a "stripped-
 *      down" parallel implementation. The card it renders is the same
 *      component the production dashboard mounts.
 *   #4 (never hardcode): URLs are owned by the hook + API_BASE.
 */

import { SovereigntyCard } from '@/widgets/sovereignty'

export function SovereigntyPreviewPage() {
  return (
    <main
      data-testid="sovereignty-preview-page"
      className="mx-auto min-h-screen max-w-3xl bg-[var(--color-bg)] px-6 py-10 text-[var(--color-text)]"
    >
      <header className="mb-6">
        <h1 className="text-2xl font-semibold text-[var(--color-text-strong)]">
          Sovereignty cutover preview
        </h1>
        <p className="mt-1 text-sm text-[var(--color-text-dim)]">
          Layout-free harness for the SovereigntyCard widget. The
          production mount lives on /console/dashboard.
        </p>
      </header>
      <SovereigntyCard />
    </main>
  )
}
