# #3376 FUNNEL — user acceptance walk (voucher → running app)

**The story:** a stranger with a voucher ends **signed into their own org's console with the purchased app running** — entirely in the browser, on a **cut-over** Sovereign, zero-touch. Two actors: the **operator** (mints the voucher in BSS) and the **stranger** (the customer, fresh browser).

This doc is the click-by-click acceptance walk for #3376. **Section A** = pre-walk gate checks for the three folded blockers (a/b/c). **Section B** = the customer acceptance walk voucher→running-app. **Section C** = the generality proof (second slug + second pool-TLD). **Appendix** = automated probes (NOT acceptance — the founder-walkable UI rows above are).

> **Build status (2026-06-17).** The #3376 fixes are committed on `fix/3376-funnel-voucher-to-running-app` (charts bumped: bp-catalyst-platform 1.4.654, bp-self-sovereign-cutover 0.1.74; slot pins in lockstep). The live walk runs on a **fresh cut-over prov** carrying these pins — the ticket lifecycle (merge → env roll → live-walk → UAT flip → close). Rows below are the acceptance script; the ☐ checkboxes flip with evidence once the carrying env reconciles. The three gate-checks in Section A are the binary "is the blocker cleared" probes the founder runs FIRST.

---

## Section A — pre-walk gate checks (the three blockers cleared)

Run these on the fresh cut-over prov BEFORE the customer walk. Each maps to one DoD box.

| # | Gate (blocker) | Command | Expected (cleared) | ☐ |
|---|---|---|---|---|
| A1 | **(a) provisioning Init wedge** | `kubectl -n sme get pods \| grep provisioning` | BOTH replicas `1/1 Running`, **no Init wait** (the init container now DATA-probes the Gitea token: `GET /api/v1/user → 200`, immune to the cutover no-op path that skipped the annotation + the chart branch that stripped it) | ☐ |
| A2 | **(b) Org-namespace create RBAC** | `kubectl auth can-i create namespaces --as=system:serviceaccount:catalyst-system:catalyst-api-cutover-driver` | `yes` (cutover-driver ClusterRole now grants `namespaces:[create]` — folds PR #3664) | ☐ |
| A3 | **(c) sovereign-clean marketplace** | `curl -sk https://marketplace.<fqdn>/redeem/?code=X \| grep -oE 'openova\.io'` | **empty** (the served bundle assembles the mothership URL from fragments at runtime + every franchised host derives a sovereign console; `postbuild` CI guard fails the image build if any literal returns) | ☐ |
| A4 | **(a) sme-tenants reconciler healthy** | `kubectl -n flux-system get kustomization sme-tenants` | **Ready=True** (the repo-bootstrap Job now seeds an empty `clusters/<fqdn>/sme-tenants/kustomization.yaml` so the Flux path exists pre-first-tenant — was "path not found" → silently excluded from the cutover egress proof) | ☐ |
| A5 | **(a) cutover proof no longer excludes the customer path** | `kubectl -n catalyst logs job/cutover-egress-block-test-<ts>` | log shows `#3376: flux-system/sme-tenants Ready` BEFORE the 600s hold, **no** `baseline KS flux-system/sme-tenants=False` exclusion; the hold passes | ☐ |

---

## Section B — the customer acceptance walk (voucher → running app)

The terminal evidence (B7) is ONE screenshot: the purchased app serving inside `…<orgslug>.<pool-tld>`, reached zero-click as the Org owner.

| # | Action (real human) | Sovereign-clean expectation | ☐ |
|---|---|---|---|
| B1 | Operator mints a voucher in the operator console **BSS menu** (a weak code is **rejected**; an omitted code auto-generates a high-entropy `VCH-XXXXXXXXXXXX`) | Voucher row created; weak code (`< 12 chars` / `< 6 distinct`) rejected with a clear hint (entropy enforced server-side in `voucher_code.go`) | ☐ |
| B2 | Customer opens `marketplace.<fqdn>/redeem/?code=<CODE>` (fresh browser) | Green "Voucher valid — N OMR credit" card; page brands THIS Sovereign, **zero** mothership refs (A3) | ☐ |
| B3 | Sign up → `/plans` → `/apps` (add WordPress) → `/addons` (subdomain + pool-TLD) → `/bcp` (topology) | Plan/app/BCP pickers render and function (Pillar 2 present) | ☐ |
| B4 | `/review` → `/checkout`, PIN-login, confirm Org | `POST /tenant/orgs` writes the row + publishes `tenant.created`; the provisioning consumer (now Running, A1) consumes it and POSTs the `orgs.openova.io/v1 Organization` CR; the `/checkout` timeline advances to **Done** (no hang) | ☐ |
| B5 | (verify) Organization CR materialised | `kubectl get organizations.orgs.openova.io -A` shows **≥ 1** within ~60s of checkout; `organization-controller` logs show it fanned out a per-Org vCluster + Keycloak group/realm + Gitea org + UserAccess | ☐ |
| B6 | Land on `console.<slug>.<pool-tld>` | Dashboard loads with publicly-trusted TLS, **signed-in ZERO-CLICK** as the Org owner (consumes #3374's per-Org realm contract); the purchased app shows installing → running | ☐ |
| B7 | **TERMINAL** — open the purchased app | The org's **WordPress site loads over HTTPS on its own FQDN** — it actually serves | ☐ |
| B8 | Returning customer reopens `marketplace.<fqdn>` signed-in | Redirected to `console.<slug>.<pool-tld>` (THIS Sovereign), **never** `console.openova.io/nova` — verify the redirect `Location` / network trace (A3 makes the franchised host derive a sovereign console) | ☐ |
| B9 | Voucher rate-limit | A burst of checkout attempts returns **429** after N per customer (`#2941` per-customer bucket in `billing/handlers.go`); a legitimate single flow is unaffected | ☐ |

---

## Section C — generality proof (DoD-9)

Repeat Section B with a **different slug AND a different pool-TLD** (e.g. `omani.rest`) → a second Organization CR + second working console + second running app, with `git grep` showing **zero** code specific to either slug/TLD.

| # | Action | Expected | ☐ |
|---|---|---|---|
| C1 | Mint a 2nd voucher, redeem on a 2nd slug under `omani.rest` | 2nd Organization CR + 2nd console + 2nd running app (terminal screenshot) | ☐ |
| C2 | `git grep -nE '<slug1>\|<slug2>\|omani\.homes\|omani\.rest' core/ products/` over the funnel code | zero slug/TLD-specific code (slug + parent-domain are data, PRINCIPLES §4) | ☐ |

---

## Appendix — automated probes (NOT acceptance)

These are unit/render checks that gate the PR; the founder-walkable UI rows in Sections A–C are the acceptance.

- `cd core/marketplace && npm run build` → the `postbuild` guard `scripts/check-served-domain-hygiene.mjs` scans `dist/` and FAILS on any `openova.io` literal in the served bundle (blocker-c regression guard; green = 0 literals across all 10 served pages).
- `cd core/services/billing && go test ./handlers/ -run 'Voucher|Checkout|RateLimit|Redeem|CodeStrength'` → entropy (`TestGenerateVoucherCode_ShapeAndEntropy`, `TestValidateVoucherCodeStrength`) + per-customer rate-limit (`TestCheckoutRateLimiter_2941`) + redeem-preview (`TestRedeemVoucherPreview_*`) all PASS.
- `helm template … products/catalyst/chart --set global.sovereignFQDN=<fqdn> --set ingress.marketplace.enabled=true` → renders the init `wait-for-gitea-token` DATA probe, the repo-bootstrap `sme-tenants` seed step, and the cutover-driver `namespaces:[create]` rule.
- `helm template … platform/self-sovereign-cutover/chart` → renders the step-08 `sme-tenants` Ready assertion + the step-09 no-op annotation stamp.
- `bash scripts/check-bootstrap-kit-pin-sync.sh --changed-only --base origin/main` → PASS (chart 1.4.654 / 0.1.74 ↔ slot pins).
- `dash -n` on the three new Job shell blocks → POSIX-clean (busybox ash `/bin/sh` safe).

---

### Prior record (superseded by the build above)

**LIVE re-walk 2026-06-15 — hw139.omani.works (deployment `c89aa7059556b342`).** The **front half** (anonymous storefront → Plans → Stack → Add-ons → BCP/Topology → Review → Checkout email/Send-code → OTP screen) walked green anonymously with headless Playwright (✅ 7), and the NATS/billing-502 fix was confirmed live (all billing endpoints non-502, `/api/auth/magic-link` POST → 200, catalog → 200). The **back half** (voucher mint → tenant provisioned → signed-in with the app running) was the env-mutating funnel op and dead-ended at the provisioning `Init:0/1` wedge — exactly blocker (a) this ticket fixes. That hw139 front-half evidence remains valid; the back-half rows are now the Section B acceptance script above, unblocked by the three fixes.
