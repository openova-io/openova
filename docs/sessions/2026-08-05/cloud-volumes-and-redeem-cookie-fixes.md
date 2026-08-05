# 2026-08-05 — two shipped fixes + backlog triage (off origin/main)

Consolidation-sweep work under #960. Two genuinely-open, single-file-scoped
defects root-caused by reading code, fixed with falsifiable tests, and merged
clean (both pure code — no chart/pin, so no deploy-bot treadmill exposure).

## Shipped (verified MERGED on origin/main)

### #5611 — cloud Volumes page reported a false 0 (PR #5685, `8f66d461f`)
- **Class:** honest-status / fabrication (a positive claim of zero never derived).
- **Root cause:** `internal/infrastructure/topology_loader.go::buildStorage` returned
  `Volumes: []Volume{}` **hardcoded** — the doc comment claimed a Crossplane
  managed-resource source that was never queried. On hw292 (50 EVS block volumes)
  the page rendered "Volumes 0 / No volumes yet" while PVCs (51) and the
  PersistentVolumes chip (97) read correctly.
- **Fix:** `loadVolumes` mirrors `loadPVCs`, sourcing live PersistentVolumes — the
  provider-agnostic projection of the cloud block volume attached to a node
  (hcloud-csi on Hetzner, `evs.csi.huaweicloud.com` on Huawei), unlike the
  hcloud-only `volume.hcloud` XRC that returns nothing on Huawei. Per-PV mapping:
  capacity ← `spec.capacity.storage`; Attachment ← `spec.claimRef` (ns/name,
  "detached" when unbound); Region ← CSI node-affinity topology zone/region;
  Status ← phase→TopologyStatus (Bound→healthy, Released→degraded, Failed→failed).
- **Test:** `TestLoadVolumes_FromLivePVs_5611` — falsifiable (fails 0≠2 on the old
  hardcode, passes on the fix). Full `internal/infrastructure` suite green.
- **Umbrella:** #3987 (cloud per-kind list empty-data). Deploy-gated: the live
  "Volumes N" proof rides the next fresh prov.

### #5421 — marketplace redeem trapped authed owners on the funnel (PR #5686, `4bdcf38ca`)
- **Pillar 1** (marketplace onboarding, UAT row 3).
- **Root cause:** `core/marketplace/src/pages/redeem.astro::init()` gated ownership
  on the marketplace-origin `localStorage['org-token']` and bailed to the
  anonymous funnel when it was absent — *before* the cookie-borne `getMyOrgs()`
  was ever called. But an owner authenticates on the **console** origin, so the
  **marketplace** origin's per-origin localStorage legitimately has no token; the
  session that spans both subdomains is the `catalyst_session` cookie, which the
  same-origin `/api/tenant/orgs` request carries by default. The page short-
  circuited before making it, so an authed owner saw the anonymous signup funnel.
- **Fix:** extracted the pre-check into a unit-testable `redeemOwnerGate` helper
  that bails to the funnel **only** for the demo/partner opt-out — never on token
  presence. `init()` otherwise consults the server (cookie) and funnels only on
  no-live-Org / error. No server change (the cookie was already sent); the client
  just needed to stop pre-empting it. Loading copy neutralised ("One moment…").
- **Test:** `redeemOwnerGate.test.ts` — cookie-only-owner → `consult-server` on the
  fix, `funnel` on the pre-fix gate (falsifiable). Marketplace vitest (19) +
  `astro build` + domain-hygiene postbuild green.
- Deploy-gated: the live UAT-row-3 proof rides the next fresh prov / marketplace
  image roll.

## Triage findings — "issue open ≠ unfixed" (carry into #960)

The GitHub-search "no merged PR" heuristic is unreliable; several open
area/catalyst issues are already **fixed in code**, left open pending a walk or
an external gate. Verified this pass:

| issue | state | note |
|---|---|---|
| #5661 single-region wizard | **fixed-in-code** (#5662 merged) | open only as `status/blocked-ext` on the PARKED mothership-pivot (#996) |
| #5559 catalog-seed 6 inert CRs | fixed-in-code (#5577/#5638/#5641) | |
| #5601 DR switchover vs StandbyAvailable | fixed-in-code (#5621) | |
| #5460 console silent re-establish | fixed-in-code (#5464/#5627) | |
| #5596 cutover certifies w/ region-B ghcr | fixed-in-code (#5629) | |
| #5642 catalyst-api OOM loop | fixed-in-code (#5645) | |

Genuinely open but **not the clean single-file loop** (deliberately not rushed):

- **#5435** banned-term `tenant` in showback — needs an architecture decision the
  issue author left open: rename the live `tenant` Deployment (cascades to
  Containerfile / chart / Service DNS `tenant.org-services.svc` / consumers, live-
  DNS-rename risk) vs a showback-boundary display map (band-aid, leaves the term
  in `kubectl`). Lesson worth carrying: **a banned term can reach an operator
  surface from a runtime K8s object name — structurally invisible to source
  greps** (every prior sweep grepped console source; the string isn't there).
- **#5571** /k8s/stream serves one region as the whole estate — multi-region SSE
  fan-out + region-tagging + UI region column (build, not a one-file fix).
- **#5609** active-passive topology never selectable — multi-blueprint product
  decision.
- **#5613 / #5600** — treemap Org-attribution fan-out / post-handover census
  lifecycle — dedicated-session builds.
