import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { router } from './app/router'
import { bootstrapTenant } from './shared/lib/tenantDiscover'
import { installFetchAuthInterceptor } from './shared/lib/authedFetch'
import './app/globals.css'

// Install the OIDC bearer-token fetch interceptor BEFORE any module
// touches fetch(). Sovereign Console (chroot) holds the access_token
// in sessionStorage after its own PKCE OIDC flow; without this
// interceptor every /api/v1/ fetch would 401 because the SPA can't
// set the catalyst_session cookie that the BE's session middleware
// expects on mother. Mother itself keeps using cookies — the
// interceptor is a transparent no-op when sessionStorage has no OIDC
// tokens.
installFetchAuthInterceptor()

const queryClient = new QueryClient({
  defaultOptions: {
    queries: { staleTime: 30_000, retry: 1 },
  },
})

const root = document.getElementById('root')
if (!root) throw new Error('Root element not found')

/**
 * Tenant discovery (issue #802, #795 [Q-mine-1]).
 *
 * The same SPA bundle serves both otech-admin (`console.<otech-fqdn>`)
 * and SME-admin (`console.<sme-domain>` — free-subdomain
 * `console.acme.<otech-fqdn>` OR BYO domain `console.acme.com`).
 * Tenant context is discovered from `window.location.host` against
 * the back-end registry — NOT from path/subdomain string parsing —
 * so a BYO CNAME-fronted domain resolves the same way as a
 * platform-hosted subdomain.
 *
 * Discovery is fire-and-forget at boot — the result is cached in
 * the tenantDiscover module so any component that needs it (sidebar
 * nav, OIDC bootstrap) reads `getTenantContext()` synchronously after
 * the promise settles. Failure modes (404 / 503 / network error) all
 * fall through to the catalyst-zero default surface — no throw, no
 * boot block.
 */
void bootstrapTenant().catch(() => {
  /* discovery failures are surfaced via getTenantContext().status; no boot abort. */
})

createRoot(root).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>
)
