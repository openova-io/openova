import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from '@tanstack/react-router'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { router } from './app/router'
import { bootstrapTenant } from './shared/lib/tenantDiscover'
import './app/globals.css'

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
