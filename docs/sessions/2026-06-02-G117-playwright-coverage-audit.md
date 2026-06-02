# G117 Playwright coverage audit + W4 spec scaffolds — 2026-06-02

> **Aux-4 read-only audit.** Pre-stage for W4 verification waves. No edits to
> existing `products/catalyst/console/tests/e2e/` content. New scaffolds land
> here under `docs/sessions/2026-06-02-G117-playwright-scaffolds/` for
> Wave-1.B5 / Wave-2 promotion when ready.
>
> Parent EPIC: [#2737](https://github.com/openova-io/openova/issues/2737).

## 1. What exists today

Inventory of `.spec.ts` / `.spec.js` / `.test.ts` under
`products/catalyst/console/tests/`:

| File | Tests | LOC | What it covers | Mock or live? |
|---|---:|---:|---|---|
| `tests/e2e/catalog-multi-instance.spec.ts` | 4 | 75 | G117.2 catalog drill-down (grafana multi-instance "+ New", harbor singleton-per-Org hide, row-click → `/apps/{id}`, topology+status badges) | mock |
| `tests/e2e/new-instance-topology-picker.spec.ts` | 4 | 73 | G117.2 new-instance form (renders `supportedTopologies`, single-region auto-selects singleton, submit → `/apps/{id}`, singleton-per-Org 409) | mock |
| `tests/e2e/launch-silent-sso.spec.ts` | 4 | 92 | G117.4 Launch button (overview surface, URL carries `prompt=none&kc_idp_hint=catalyst-pin`, Endpoints tab Launch vs raw Open, disabled when not Ready) | mock |
| **TOTAL** | **12** | **240** | 3 specs, all under `tests/e2e/`, all mock-mode | — |

Config + fixtures:

- `products/catalyst/console/playwright.config.ts` — single chromium project, `workers: 1`, retries-on-CI=1, `MOCK_API=1` Astro dev-server on `:4323`
- `products/catalyst/console/tests/e2e/fixtures/mock-blueprints.yaml` — 4 Blueprints seeded (grafana, harbor, gitea, openbao); 3 Application instances seeded (2 grafana + 1 harbor); 1 apps entry + 1 endpoints entry keyed by UUID
- `products/catalyst/console/scripts/build-mock-fixture.mjs` — runs via `predev` / `pretest:e2e` to regenerate `src/lib/mock/blueprint-data.ts` from the YAML

`.playwright-mcp/` is **not** a test harness — it's a directory of MCP-server-generated console-log dumps from interactive Playwright MCP sessions (logs from May 22 to Jun 2). No spec files live there.

## 2. Baseline run — main HEAD

```bash
cd products/catalyst/console && npx playwright test --reporter=list
```

**Result** (against `main@9744cde6`, 2026-06-02 17:49 UTC):

- **12 passed, 0 failed, 0 skipped** in **10.3s**
- 0 flakies (single run + `workers: 1` config — re-run not warranted given clean first pass)
- 1 non-fatal Svelte warning: `EndpointsTab.svelte:39:16 — state_referenced_locally` on `initialEndpoints` (does NOT fail the suite; flagged as a code-quality cleanup for Wave-1.B5)

**The suite IS runnable.** No P0 blocker for W4.

## 3. Coverage matrix — G117 sub-EPIC DoD claims

`EXISTS` = spec present and asserts the DoD claim · `STUB` = spec present
but asserts only a placeholder/no real DoD evidence · `MISSING` = no spec yet.

| G117 sub-EPIC | DoD claim | Target spec file | Status | Notes |
|---|---|---|---|---|
| **G117.1** | Malformed `spec.topology` rejected by admission webhook | `tests/e2e/admission.spec.ts` | **MISSING** | Needs catalyst-api admission-webhook endpoint or `kubectl apply` against a kind cluster + a Blueprint CR fixture with invalid topology |
| **G117.1** | Valid `spec.topology` accepted; defaults resolved | `tests/e2e/admission.spec.ts` (sibling test) | **MISSING** | Same harness as above; happy-path assertion |
| **G117.2** | Catalog drill-down shows N instance list (not single card) | `tests/e2e/catalog-multi-instance.spec.ts` | **EXISTS** | `grafana page renders Blueprint + multi-instance "+ New" button` covers it via two `instance-row-<uuid>` testid assertions |
| **G117.2** | New-instance dialog honors `spec.topology.supported` | `tests/e2e/new-instance-topology-picker.spec.ts` | **EXISTS** | `topology picker renders blueprint.supportedTopologies` enumerates both `active-hot-standby` + `singleton` radios |
| **G117.2** | Multi-region-required topologies disabled on single-region prov | `tests/e2e/new-instance-topology-picker.spec.ts` | **EXISTS** | `single-region Sovereign auto-selects singleton + disables multi-region` |
| **G117.2** | Singleton-per-Org uniqueness enforced server-side | `tests/e2e/new-instance-topology-picker.spec.ts` | **EXISTS** | `singleton-per-Org Blueprint enforces uniqueness server-side` (409 path) |
| **G117.3** | Endpoint add → Gitea PR opens | `tests/e2e/endpoint-add-via-pr.spec.ts` | **MISSING** | Wave-2 work; needs Gitea API mock or live `gitea.<sov>` |
| **G117.3** | PR auto-merges + endpoint goes live in <2 min | `tests/e2e/endpoint-add-via-pr.spec.ts` (sibling) | **MISSING** | Wall-clock assertion; needs live polling of `Endpoint.status` |
| **G117.3** | DNS-conflict pre-check blocks colliding hostname | `tests/e2e/endpoint-add-via-pr.spec.ts` (sibling) | **MISSING** | Pre-flight call to `/sovereign/api/v1/endpoints/check?hostname=<x>` |
| **G117.3** | Endpoint rename → 301 from old hostname | `tests/e2e/endpoint-rename.spec.ts` | **MISSING** | Needs live HTTP probe + cert reuse |
| **G117.3** | Endpoint rename → SSO `redirect_uri` preserved in KC client | `tests/e2e/endpoint-rename.spec.ts` (sibling) | **MISSING** | Needs KC Admin API mock or live `kcadm.sh get clients/<id>/...` |
| **G117.4** | Launch button URL = silent OIDC `prompt=none&kc_idp_hint=catalyst-pin` | `tests/e2e/launch-silent-sso.spec.ts` | **EXISTS** | `Launch button URL carries silent-SSO params` matches both regexes |
| **G117.4** | Launch button opens in new tab via `window.open` | `tests/e2e/launch-silent-sso.spec.ts` | **EXISTS** | `Launch button URL carries silent-SSO params` (spy on `window.open`) |
| **G117.4** | Launch <500ms from click to authenticated session | `tests/e2e/launch-silent-sso.spec.ts` | **STUB** | Existing spec asserts URL shape but NOT wall-clock <500ms; the `<500ms` claim needs live KC + a `performance.now()` measurement |
| **G117.4** | Expired session → fallback to interactive `prompt=login` | `tests/e2e/launch-sso-fallback.spec.ts` | **MISSING** | KC returns `error=login_required` on `prompt=none` when no session; UI must retry with `prompt=login` |
| **G117.4** | Endpoints tab shows Launch for ssoEnabled + raw Open for ssoDisabled | `tests/e2e/launch-silent-sso.spec.ts` | **EXISTS** | `Endpoints tab shows Launch button for ssoEnabled + raw Open for SSO-disabled` |
| **G117.4** | Launch hidden when endpoint not Ready | `tests/e2e/launch-silent-sso.spec.ts` | **EXISTS** | `Launch button is disabled when endpoint not Ready` (creates Pending instance + tab-endpoints assertion) |
| **G117.5** | 17 apps silent-SSO from Launch button (Tier-1+2+3 fan-out) | `tests/e2e/launch-silent-sso.spec.ts` (parameterized) | **MISSING** | Existing spec only exercises grafana; needs `test.describe.each(APPS)` over the 17-app list against live hw86 |
| **G117.5** | Cross-Org realm isolation (Org-A user denied at Org-B Matrix) | `tests/e2e/cross-org-isolation.spec.ts` | **MISSING** | Tier-3 per-Org realm assertion; needs 2 Orgs + 1 Tier-3 app (Matrix) + KC token-exchange |
| **G117.5** | Per-Org realm has `catalyst-pin` → `sovereign` IdP federation chain (2-hop) | `tests/e2e/per-org-realm-federation.spec.ts` | **MISSING** | Needs KC Admin API mock or live `kcadm.sh get identity-provider/instances --realm <org>` |
| **G117.6** | 3-instance grafana in 1 Environment reconcile cleanly (3 HRs, 3 ingresses, 3 storage PVCs) | `tests/e2e/multi-instance-reconcile.spec.ts` | **MISSING** | Needs catalyst-api `POST /apps/instances` ×3 + Flux + kubectl HR poll |
| **G117.6** | Topology override at install respects `spec.topology.supported` | `tests/e2e/topology-override.spec.ts` | **MISSING** | Negative-path: try installing grafana with topology=`bogus` → expect 400 |
| **G117.6** | application-controller fans HRs across host clusters per topology | `tests/e2e/topology-override.spec.ts` (sibling) | **MISSING** | active-hot-standby singleton → 2 HRs in mgmt-A + mgmt-B; singleton → 1 HR; etc. |
| **Regression** | PIN login (existing flow on hw86) | `tests/e2e/regression/pin-login.spec.ts` | **MISSING** | No regression dir yet; needs hw86 baseline harness |
| **Regression** | Voucher redeem | `tests/e2e/regression/voucher-redeem.spec.ts` | **MISSING** | Same; BSS-menu walk |
| **Regression** | Sandbox MCP | `tests/e2e/regression/sandbox-mcp.spec.ts` | **MISSING** | Same; Pillar-4 walk |

Roll-up by status:

- **EXISTS**: 7 (all under G117.2 + G117.4)
- **STUB**: 1 (Launch <500ms wall-clock)
- **MISSING**: 19 (all of G117.1, G117.3, G117.5, G117.6, regression; partial G117.4)

## 4. Coverage gaps by sub-EPIC

- **G117.1** — 0 / 2 DoD claims covered. Needs an admission-webhook smoke test (kind cluster + Blueprint CRs).
- **G117.2** — 4 / 4 DoD claims covered. **Closed.**
- **G117.3** — 0 / 5 DoD claims covered. Needs Gitea PR pipeline harness + DNS-conflict mock + cert reuse + KC client mutation mock.
- **G117.4** — 4 / 6 DoD claims covered (URL shape + Endpoints-tab Launch vs Open + Ready-gate; wall-clock + expired-session fallback missing).
- **G117.5** — 0 / 3 DoD claims covered. The 17-app fan-out is the heaviest live-Sovereign spec (best run against hw86 in W4).
- **G117.6** — 0 / 3 DoD claims covered. Needs live catalyst-api + Flux + kubectl-against-cluster harness.
- **Regression** — 0 / 3 covered. Needs hw86 baseline directory.

## 5. Spec scaffolds (under `docs/sessions/.../scaffolds/`)

Wave-1.B5 promotes the green-light scaffolds into `tests/e2e/`. The scaffolds
deliberately use `test.skip` on every assertion that depends on a live
catalyst-api / live KC / live kubectl handle — so they import cleanly into
the suite without breaking the baseline green, and unskip-tracked TBDs gate
each unskip.

| Scaffold | Sub-EPIC | DoD claims covered |
|---|---|---|
| `g117.1/admission.spec.ts` | G117.1 | 2 |
| `g117.3/endpoint-add-via-pr.spec.ts` | G117.3 | 3 |
| `g117.3/endpoint-rename.spec.ts` | G117.3 | 2 |
| `g117.4/launch-sso-fallback.spec.ts` | G117.4 | 1 |
| `g117.4/launch-silent-sso-wallclock.spec.ts` | G117.4 | 1 |
| `g117.5/launch-silent-sso-fanout.spec.ts` | G117.5 | 1 (17 apps parameterized) |
| `g117.5/cross-org-isolation.spec.ts` | G117.5 | 1 |
| `g117.5/per-org-realm-federation.spec.ts` | G117.5 | 1 |
| `g117.6/multi-instance-reconcile.spec.ts` | G117.6 | 1 |
| `g117.6/topology-override.spec.ts` | G117.6 | 2 |
| `regression/pin-login.spec.ts` | regression | 1 |
| `regression/voucher-redeem.spec.ts` | regression | 1 |
| `regression/sandbox-mcp.spec.ts` | regression | 1 |

13 spec scaffolds covering the 19 MISSING + 1 STUB rows above. Each scaffold:

- Imports `import { test, expect } from '@playwright/test'`
- Wraps the body in a `test.describe('<sub-EPIC> — <topic>', ...)` block
- Lays out 3-5 `test()` blocks per DoD claim (happy-path + 2 edge cases)
- Marks any live-dependent assertion with `test.skip(condition, '<reason>')`
- Comments inline at the assertion line that proves the DoD pass

## 6. Promotion plan (Wave-1.B5 / Wave-2 / W4)

| Wave | Action | Scope |
|---|---|---|
| Wave-1.B5 | Promote `g117.1/admission.spec.ts` once admission webhook lands (G117.1 PR) | mock-mode kind cluster OR static-fixture validation pass |
| Wave-2 | Promote `g117.3/endpoint-*` once Endpoints tab + PR pipeline lands | Gitea PR API mock + DNS-check API mock |
| Wave-2 | Promote `g117.4/launch-sso-fallback.spec.ts` once UI retry-with-prompt-login lands | KC `error=login_required` mock |
| Wave-2 | Promote `g117.6/topology-override.spec.ts` once application-controller honors topology | Mock-mode `POST /apps/instances` with `topology=bogus` → 400 |
| W4 | Promote `g117.4/launch-silent-sso-wallclock.spec.ts` (live hw86) | `performance.now()` measurement against actual KC |
| W4 | Promote `g117.5/launch-silent-sso-fanout.spec.ts` (live hw86) | 17-app parameterized list with live SSO |
| W4 | Promote `g117.5/cross-org-isolation.spec.ts` (live hw86) | 2 Orgs + Tier-3 Matrix |
| W4 | Promote `g117.5/per-org-realm-federation.spec.ts` (live hw86) | KC Admin API |
| W4 | Promote `g117.6/multi-instance-reconcile.spec.ts` (live hw86) | catalyst-api + Flux + kubectl |
| W4 | Promote `regression/*` (live hw86) | Pillar-0/1/4 baseline walks |

## 7. Anti-theater notes

The 12 existing tests are **mock-mode only**. Every one of the EXISTS rows
above is mock-mode coverage of the DoD claim — **not** live-Sovereign
verification. The W4 verification waves are the ones that flip the
`docs/ledger/TRUST.md` rows to VERIFIED-PASS; mock-mode green is necessary
but NOT sufficient. The scaffolds here exist explicitly to grow live
coverage in Wave-2 + W4.

The Svelte `state_referenced_locally` warning in `EndpointsTab.svelte:39`
is a real bug latent in the existing G117.2 code path — flag for the
Wave-1.B5 cleanup pass; not in scope here.

## 8. Suite-config observations

- `fullyParallel: false` + `workers: 1` — single worker by design (the mock
  dev-server's regeneration race + the global `window.__openCalls` spy in
  the SSO spec both assume single-tab ordering). Parameterized live-mode
  expansion in W4 will need to reconcile this with the 17-app fan-out, e.g.
  by parallelizing across `test.describe.parallel` or by sharding via CI
  matrix.
- `webServer.url: 'http://127.0.0.1:4323/catalog/grafana'` — Playwright
  polls THAT URL to confirm readiness; if the mock fixture's grafana
  Blueprint is renamed or the route is moved, this URL break the suite.
  Flag for the Wave-1.B5 cleanup (use `/` as readiness probe instead).
- `forbidOnly: !!process.env.CI` — good guard. `retries: process.env.CI ? 1 : 0`
  — flaky-test policy is 1 retry on CI; local must always pass first try.

## 9. References

- EPIC: [#2737](https://github.com/openova-io/openova/issues/2737)
- Sub-EPICs: #2741 (G117.2) · #2742 (G117.3) · #2743 (G117.4) · #2744 (G117.5) · #2745 (G117.6)
- Topology audit: `docs/sessions/2026-06-02-per-blueprint-topology-audit.md`
- Agent brief template: `.claude/templates/G117-agent-brief.md`
- Playwright config: `products/catalyst/console/playwright.config.ts`
- Mock fixture: `products/catalyst/console/tests/e2e/fixtures/mock-blueprints.yaml`
