/**
 * e2e/lib/config.ts — central URL + fixture-data registry for the
 * Playwright suite (issue #805 + canon `feedback_never_hardcode_urls.md`).
 *
 * Every URL the Organization-demo spec talks to derives from THIS file. New
 * tests must import names from here rather than inlining hostnames or
 * paths. The motivation:
 *
 *   • The same spec runs against three substrates over its lifetime:
 *     1. The local Vite dev server at http://localhost:5173 (today).
 *     2. A fresh otech with `marketplace.<otech-fqdn>` once #804 lands.
 *     3. Any future Sovereign that pivots its marketplace hostname.
 *
 *   • Hardcoding `acme.example` or `otech.example` in 30 different
 *     `route()` glob patterns turned out to be the canonical
 *     antipattern that #687 flagged in production code; the same
 *     discipline applies to test fixtures so the spec stays green when
 *     the URL surface evolves.
 *
 * Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every
 * environment-derivable value below honours an env var override with a
 * sensible default so CI runners can pin to a known fixture without
 * editing source.
 */

/**
 * Otech (Sovereign) FQDN under test. Defaults to a fixture domain that
 * never resolves — the spec mocks every fetch the SPA makes, so DNS
 * resolution doesn't matter, but the hostname literal still flows into
 * org-console-discovery payloads, OIDC realm URLs, and screenshot filenames
 * so we keep it parameterised.
 */
export const OTECH_FQDN: string = process.env.E2E_OTECH_FQDN ?? 'otech.example'

/**
 * Organization slug under test. Combined with OTECH_FQDN to derive every
 * `*.acme.<otech-fqdn>` host the spec walks through.
 */
export const ORG_SLUG: string = process.env.E2E_ORG_SLUG ?? 'acme'

/**
 * Composed hosts for each surface in the Organization happy path. None of these
 * are dialled at the network layer — the spec mocks them via
 * `page.route` — but they appear verbatim in org-console-discovery payloads
 * and OIDC realm URLs so the SPA's runtime branching keys off the
 * correct org-console kind (the legacy `tenant_kind` wire key).
 */
export const HOSTS = {
  marketplace: `marketplace.${OTECH_FQDN}`,
  orgConsole: `console.${ORG_SLUG}.${OTECH_FQDN}`,
  wordpress: `wordpress.${ORG_SLUG}.${OTECH_FQDN}`,
  openclaw: `openclaw.${ORG_SLUG}.${OTECH_FQDN}`,
  webmail: `mail.${ORG_SLUG}.${OTECH_FQDN}`,
  otechConsole: `console.${OTECH_FQDN}`,
  orgDomain: `${ORG_SLUG}.${OTECH_FQDN}`,
} as const

/**
 * Org-console-discovery payloads. Mirrors the wire shape of
 * `GET /api/v1/tenant/discover?host=<host>` (legacy BE route; the
 * `tenant_id` / `tenant_kind` JSON keys are the wire contract) —
 * `tenant_kind: "org"` branches the SPA into the Organization-tier UX
 * (sidebar entries + /console/org/users routing).
 */
export const ORG_DISCOVERY = {
  host: HOSTS.orgConsole,
  tenant_id: `org-${ORG_SLUG}`,
  tenant_kind: 'org',
  keycloak_realm_url: `https://kc.${OTECH_FQDN}/realms/org-${ORG_SLUG}`,
  keycloak_client_id: 'catalyst-ui',
} as const

/**
 * Email addresses for the synthetic Organization admin and the two end-users
 * the spec creates (alice + bob). Email shape derives from the Organization
 * domain so a future BYO-domain run only needs to flip `ORG_SLUG` and
 * `OTECH_FQDN`.
 */
export const USERS = {
  orgAdmin: `admin@${HOSTS.orgDomain}`,
  alice: `alice@${HOSTS.orgDomain}`,
  bob: `bob@${HOSTS.orgDomain}`,
} as const

/**
 * UUIDs assigned to mock-created users. Pinned so the screenshot
 * filenames + selector chains are deterministic across re-runs.
 */
export const UUIDS = {
  alice: 'uuid-alice-fixture',
  bob: 'uuid-bob-fixture',
} as const

/**
 * Deployment id used for the provisioning surface (Step 2). The
 * `/provision/$deploymentId` page consumes this verbatim.
 */
export const DEPLOYMENT_ID: string =
  process.env.E2E_DEPLOYMENT_ID ?? 'org-acme-fixture-deployment'

/**
 * Screenshot output directory. The CI workflow uploads this on
 * failure — the Organization-demo run also emits screenshots on success because
 * the DoD checklist requires 1440×900 visual proof of every step
 * (issue #805 acceptance criteria).
 */
export const SCREENSHOT_DIR: string =
  process.env.ORG_DEMO_SCREENSHOT_DIR ?? 'e2e/screenshots'

/**
 * Step prefix for screenshot filenames so the artefacts list remains
 * sortable. Format: `805-step{N}-{slug}-1440.png`.
 */
export const SCREENSHOT_PREFIX: string = '805-org-demo'
