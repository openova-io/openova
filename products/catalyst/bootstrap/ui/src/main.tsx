import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { runMothershipTokenRedirect } from './shared/lib/mothershipTokenRedirect'
import { router } from './app/router'
import { bootstrapOrgConsole } from './shared/lib/orgConsoleDiscover'
import { installFetchAuthInterceptor } from './shared/lib/authedFetch'
import './app/globals.css'

/**
 * Issue #1773 — DoD gate D0 (the founder's #1 pinned gate per
 * `feedback_handover_redirect_is_critical_d0.md`).
 *
 * If the operator lands on the Mothership with a handover JWT
 * (`?token=<JWT>`) on the URL — e.g. fresh tab from the wizard
 * SuccessPage, share-link, browser history paste — bounce them
 * straight to the Sovereign chroot's `/auth/handover` BEFORE we
 * boot the router, install fetch interceptors, or render the DOM.
 *
 * If `runMothershipTokenRedirect()` returns true the browser is
 * already navigating away (window.location.replace). The remainder
 * of bootstrap is skipped to avoid:
 *
 *   - flashing the Mothership Jobs UI before the navigation lands
 *   - triggering the rootRoute auth gate on a host that is not
 *     the operator's Sovereign (would redirect to /login pointlessly)
 *   - racing fetch interceptors against the navigation
 *
 * On non-Mothership hosts and on Mothership-without-?token=, the
 * function is a no-op and bootstrap proceeds normally.
 */
if (!runMothershipTokenRedirect()) {
  installFetchAuthInterceptor()

  const queryClient = new QueryClient({
    defaultOptions: {
      queries: { staleTime: 30_000, retry: 1 },
    },
  })

  const root = document.getElementById('root')
  if (!root) throw new Error('Root element not found')

  /**
   * Org-console discovery (issue #802, #795 [Q-mine-1]).
   *
   * The same SPA bundle serves both otech-admin (`console.<otech-fqdn>`)
   * and Organization-admin (`console.<org-domain>` — free-subdomain
   * `console.acme.<otech-fqdn>` OR BYO domain `console.acme.com`).
   * Org-console context is discovered from `window.location.host` against
   * the back-end registry — NOT from path/subdomain string parsing —
   * so a BYO CNAME-fronted domain resolves the same way as a
   * platform-hosted subdomain.
   *
   * Discovery is fire-and-forget at boot — the result is cached in
   * the orgConsoleDiscover module so any component that needs it (sidebar
   * nav, OIDC bootstrap) reads `getOrgConsoleContext()` synchronously after
   * the promise settles. Failure modes (404 / 503 / network error) all
   * fall through to the catalyst-zero default surface — no throw, no
   * boot block.
   */
  void bootstrapOrgConsole().catch(() => {
    /* discovery failures are surfaced via getOrgConsoleContext().status; no boot abort. */
  })

  createRoot(root).render(
    <StrictMode>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </StrictMode>,
  )
}
