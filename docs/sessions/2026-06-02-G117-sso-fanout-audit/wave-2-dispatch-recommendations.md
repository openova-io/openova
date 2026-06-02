# Wave-2 SSO fan-out — concrete dispatch plan

> Consumes `00-summary.md` of this directory. Aligns with `G117-worktree-policy.md` Wave-2 staging (slot C-N file-touch matrix).
> Per-slot worktree isolation MANDATORY because every slot touches `platform/<bp>/chart/templates/` AND must serialize against `platform/keycloak/chart/templates/configmap-per-org-realm.yaml` writes from Slot C4.

## Active worklist after audit drops/defers (9 apps)

```
Tier-2 (3):  bp-guacamole, bp-powerdns-admin, bp-cilium(Hubble UI)
Tier-3 (5):  bp-matrix, bp-langfuse, bp-temporal, bp-librechat, bp-wordpress-tenant
Tier-3-API (1): bp-openmeter
```

## Recommended split — 3 parallel agents (B5/D1, B5/D2, B5/D3 slot scheme)

Sub-agent cap floor 2, ceiling 3 per `feedback_cap_2_3_business_priority_gate.md`. 3 slots used here because:

- All slots are end-user-DoD pillar work (Pillar 4 — Sandbox + auto-mount across all SSO-capable apps; Pillar 1 — Org onboarding into multi-app silent SSO).
- The 9-app worklist splits cleanly into disjoint chart sets — zero file overlap across slots, no worktree-conflict risk.
- Slot 4 (Wave-2.C4 KC realm-config) is a serializing prereq, NOT one of these 3 SSO fan-out slots.

### Slot **D1 — Tier-2 small + Tier-3-API** (LOW complexity, fast win)

**Apps:** bp-powerdns-admin, bp-openmeter
**Why pair:** Both are smallest-LoC, lowest-risk patches. bp-powerdns-admin is +4/-1 (one query-param append + auto-derive default). bp-openmeter is +50/0 (no browser flow, just JWT validation config + service-account AppRegistration). Combined ~55 LoC delta, ~2 chart bumps.
**Files touched:**

- `platform/powerdns-admin/chart/templates/sso-oauth-source-externalsecret.yaml`
- `platform/powerdns-admin/chart/values.yaml`
- `platform/powerdns-admin/chart/Chart.yaml` (0.1.0 → 0.1.1)
- `platform/powerdns-admin/blueprint.yaml` (version lockstep)
- `platform/openmeter/chart/templates/sso-app-registration.yaml` (new)
- `platform/openmeter/chart/values.yaml`
- `platform/openmeter/chart/Chart.yaml` (1.0.1 → 1.1.0)
- `platform/openmeter/blueprint.yaml`
- `clusters/_template/bootstrap-kit/<slot>-powerdns-admin.yaml` + `<slot>-openmeter.yaml` pin bumps

**Estimated PR size:** ~55 lines added, ~1 removed across 9 files.
**Isolation:** worktree `feat/g117-5-sso-fan-out-d1`.
**Dependencies:** None hard-blocking — bp-powerdns-admin already integrates with bp-sso-bridge; bp-openmeter is JWT-validation-only.
**Verifier:** Independent sub-agent runs `helm template` + `helm lint` on both charts; verifies AppRegistration ConfigMap label + Secret references resolve.

### Slot **D2 — Tier-2 medium + Tier-3 GOOD** (MEDIUM complexity, browser-flow stack)

**Apps:** bp-guacamole, bp-librechat, bp-wordpress-tenant
**Why pair:** bp-librechat + bp-wordpress-tenant are the two Tier-3 charts with full OPENID_* envFrom wiring already in place — small patches needed (~60-70 LoC each). bp-guacamole is the largest Tier-2 patch but its complexity is mostly DELETING the divergent realm-patch ConfigMap (a routine cleanup).
**Files touched:**

- `platform/guacamole/chart/templates/secret-sso-oidc-credentials.yaml` (new)
- `platform/guacamole/chart/templates/keycloak-realm-config.yaml` (DELETE)
- `platform/guacamole/chart/templates/guacamole-deployment.yaml` (kc_idp_hint append)
- `platform/guacamole/chart/values.yaml` + `Chart.yaml` (0.1.31 → 0.2.0) + `blueprint.yaml`
- `platform/librechat/chart/templates/sso-externalsecret.yaml` (new)
- `platform/librechat/chart/templates/sso-app-registration.yaml` (new)
- `platform/librechat/chart/templates/deployment.yaml` (auto-derive)
- `platform/librechat/chart/values.yaml` + `Chart.yaml` (1.0.0 → 1.1.0) + `blueprint.yaml`
- `platform/wordpress-tenant/chart/templates/sso-secret.yaml` (new)
- `platform/wordpress-tenant/chart/templates/sso-app-registration.yaml` (new)
- `platform/wordpress-tenant/chart/templates/oidc-config-job.yaml` (kc_idp_hint append)
- `platform/wordpress-tenant/chart/templates/_helpers.tpl` (auto-derive)
- `platform/wordpress-tenant/chart/values.yaml` + `Chart.yaml` (0.3.2 → 0.4.0) + `blueprint.yaml`
- `clusters/_template/bootstrap-kit/<3 slot pin bumps>`

**Estimated PR size:** ~170 added, ~100 removed (the bulk of the removal is bp-guacamole's deleted realm-patch ConfigMap).
**Isolation:** worktree `feat/g117-5-sso-fan-out-d2`.
**Dependencies:** Soft — Wave-2.C4 per-Org realm template should land first OR bp-sso-bridge reconciler must be ready to consume AppRegistration ConfigMaps before fresh-prov verification. If C4 not ready, charts still install (chart-side Secret materialization via `lookup`-or-generate covers the gap).
**Verifier:** Independent sub-agent runs `helm template` on all 3 charts; verifies no rendered Secret leaks plaintext into ConfigMap; checks `helm.sh/resource-policy: keep` annotation present on per-app SSO Secrets.

### Slot **D3 — Tier-2 large + Tier-3 NONE + Tier-3 PARTIAL** (HIGH complexity, biggest patches)

**Apps:** bp-cilium (Hubble UI), bp-langfuse, bp-matrix, bp-temporal
**Why grouped:** Largest chart edits — bp-cilium is +117/-2 (oauth2-proxy sidecar + Service + Secret + HTTPRoute backendRefs swap). bp-langfuse is +80/0 (full SSO stack from scratch). bp-matrix is +75/-5 (Synapse OIDC providers + 2-hop). bp-temporal is +95/-15 (Web UI OIDC + auto-derive). Together ~370 LoC delta across 4 charts.
**Files touched:**

- bp-cilium: 4 new templates + httproute patch + values + Chart.yaml (1.3.6 → 1.4.0) + blueprint.yaml
- bp-langfuse: 2 new templates + values + Chart.yaml (1.0.0 → 1.1.0) + blueprint.yaml
- bp-matrix: 2 new templates + values (extraConfig + sovereign.fqdn) + Chart.yaml (1.0.1 → 1.1.0) + blueprint.yaml
- bp-temporal: 2 new templates + keycloak-oidc-config patch + values + Chart.yaml (1.0.1 → 1.1.0) + blueprint.yaml
- 4 bootstrap-kit slot pin bumps

**Estimated PR size:** ~370 added, ~25 removed across ~25 files.
**Isolation:** worktree `feat/g117-5-sso-fan-out-d3`.
**Dependencies:** HARD — bp-cilium oauth2-proxy MUST gate Hubble UI exposure (`catalystOverlay.hubbleUI.enabled=true` already on by default in bootstrap-kit; without oauth2-proxy in front, fresh prov regresses to publicly-accessible Hubble). bp-matrix Synapse needs `client_secret_path` (file, not env) — volume mount must be added. Wave-2.C4 per-Org realm template SHOULD be ready before fresh-prov verification.
**Verifier:** Independent sub-agent runs `helm template` for each chart with realistic per-Sovereign overlay values; verifies HTTPRoute backendRefs after bp-cilium edit point at oauth2-proxy Service (NOT direct hubble-ui Service); verifies Matrix Synapse Pod spec has a volume mount for `/secrets/oidc/`.

## Serialization & cross-slot contract

- **All 3 slots depend on Wave-2.C4** (`platform/keycloak/chart/templates/configmap-per-org-realm.yaml`) for end-to-end fresh-prov verification, but chart-side `helm template` + lint can proceed without C4 done.
- **Bootstrap-kit `clusters/_template/bootstrap-kit/kustomization.yaml`** must be touched by EACH slot to pin the updated chart versions. Per Wave-2.C5 staging in `G117-worktree-policy.md`, this serializes AFTER C1+C2+C3+C4. For SSO fan-out, each slot's PR ships its own pin bump; Wave-2.C5 reconciles the total bootstrap-kit update.
- **GHCR push lock** — per worktree policy, `flock /tmp/ghcr-push.lock` if 2+ slots publish OCI artifacts concurrently. With 3 slots × ~3 charts each (~9 GHCR pushes), serialize via the flock to avoid GHCR rate-limit + race conditions.

## Anti-collision pre-commit hook (recommended)

Per `G117-worktree-policy.md` §"Anti-collision pre-commit hook". Each Wave-2 SSO slot's worktree drops the hook into `.git/hooks/pre-commit`. Refuses to commit if files in the diff overlap with sibling worktree's staged files.

Slot D1 / D2 / D3 chart sets are DISJOINT, so the hook should never fire — but it catches accidental scope creep (e.g. if a slot edits `platform/keycloak/chart/`, the hook surfaces a likely Wave-2.C4 contract breach).

## Pre-dispatch briefing template for each Wave-2 slot

Per `feedback_pre_dispatch_briefing_required.md` 4-line pattern:

```
DISPATCHING Wave-2 SSO Slot D<N> — <app list>
PROBLEM:    G117 Wave-2 SSO fan-out — current chart wiring per audit/tier-<N>/<app>.md
REMEDIATION: ship per-app chart patches per `wave-2-dispatch-recommendations.md` Slot D<N>
EXPECTED:   PR titled `feat(g117.5): Wave-2 SSO Slot D<N> — <app list>` opens, all charts helm-template green, verifier sub-agent verdict `status/uat-OK` on #2744
```

## Estimated wall-clock for Wave-2

- D1 (small): ~2-3h agent time
- D2 (medium): ~4-5h
- D3 (large): ~6-8h
- C4 prereq (per-Org realm config): ~3-4h
- C5 bootstrap-kit pin sweep: ~30 min

If C4 + D1/D2/D3 dispatch in parallel and C4 races to finish first, total wall-clock ~8h. Verifier loop adds ~1-2h per slot. Fresh-prov walk + 9-app silent-SSO verification (Playwright at `tests/e2e/playwright/tests/g117-sso-fanout-tier1-2-3.spec.ts`) adds ~1h.

## Post-merge — verification gate

Per G117 brief Acceptance DoD:

1. PR opens cleanly, `gh pr checks` all green.
2. Verifier sub-agent verdict posted on #2744.
3. Fresh-prov walk: PIN-login at `console.<sov>` → dashboard → click Launch on each of the 9 apps → silent SSO (zero password prompts) → screenshot evidence per app.
4. HAR capture of the 4-hop / 8-hop curl chains per app, attached as PR artifact.
5. Cross-Org isolation test for the 5 Tier-3 browser-flow apps — Org-A user MUST NOT auth into Org-B's app instance.

## Open follow-ups (fresh sub-EPIC numbers)

| Sub-EPIC | Topic | Source |
|---|---|---|
| G118.1 | bp-opensearch chart authorship (currently scaffold-only) | `tier-3/bp-opensearch.md` |
| G118.2 | bp-iceberg chart authorship (currently scaffold-only) — pick Polaris vs REST-only | `tier-3/bp-iceberg.md` |
| G118.3 | bp-litmus full SSO wrapper (needs ChaosCenter frontend or oauth2-proxy fronting) | `tier-3/bp-litmus.md` |
| G117.X | bp-sigstore — remove from G117.5 Tier-2 (no UI); update audit doc row 79 | `tier-2/bp-sigstore.md` |
| G117.X | Per-Org realm naming — harmonize `sme-<subdomain>` → `org-<slug>` (back-compat alias period) | `tier-3/bp-wordpress-tenant.md` |

These are noted, NOT pre-filed per `feedback_amnesia_antipatterns_2026_05_20.md` L8 ("Filing N TBDs while DoD = 0/5 = open-count inflation"). Open them when Wave-2 ships green AND a maintainer is ready to pick them up.
