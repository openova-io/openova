# G117.5 Wave-2 SSO fan-out — Tier-2 + Tier-3 chart audit summary

> **Refs**: EPIC #2737 · G117.5 #2744 · G117 Wave-0 brief at `.claude/templates/G117-agent-brief.md`
> **Memory inputs**: `feedback_g113_sso_idr_defaultprovider_fix.md` · `feedback_sso_manual_recovery_procedure.md` · `feedback_chart_credential_persistence_defense.md`
> **Topology cross-ref**: `docs/sessions/2026-06-02-per-blueprint-topology-audit.md`

## Apps audited — 13 total (4 Tier-2 + 9 Tier-3)

### Tier-2 (federate to `sovereign` realm directly — single hop)

| App | Chart on `main` | Audit verdict | Est. patch (LoC) |
|---|---|---|---|
| `bp-guacamole` | `0.1.31` | **PARTIAL** — OIDC env vars wired, SealedSecret placeholder, divergent `catalyst` realm-patch ConfigMap to remove | +32 / -93 |
| `bp-powerdns-admin` | `0.1.0` | **GOOD** — ExternalSecret + envFrom complete, only `kc_idp_hint` query-param append needed | +4 / -1 |
| `bp-cilium` (Hubble UI) | `1.3.6` | **SCAFFOLD-ONLY** — HTTPRoute exists, OIDC enforcement absent; needs full oauth2-proxy sidecar | +117 / -2 |
| `bp-sigstore` | `1.0.0` | **NO UI EXISTS — DROP** (policy-controller admission webhook only) | 0 |

**Tier-2 fan-out reduces to 3 active apps after sigstore drop.**

### Tier-3 (federate to per-Org realm via 2-hop — per-Org realm → sovereign realm broker → catalyst-pin)

| App | Chart on `main` | Audit verdict | Est. patch (LoC) |
|---|---|---|---|
| `bp-matrix` | `1.0.1` | **PARTIAL** — `oidc.*` values block + upstream subchart path exist; AppRegistration + ExternalSecret + 2-hop wiring missing | +75 / -5 |
| `bp-langfuse` | `1.0.0` | **NONE** — full SSO stack to author | +80 / 0 |
| `bp-temporal` | `1.0.1` | **PARTIAL** — JWT validation done, Web UI OIDC client missing | +95 / -15 |
| `bp-librechat` | `1.0.0` | **GOOD** — full OPENID_* envFrom; only ExternalSecret + AppRegistration + auto-derive missing | +62 / -4 |
| `bp-opensearch` | (no chart) | **CHART DOES NOT EXIST — DROP** | 0 (deferred under G118.1) |
| `bp-openmeter` | `1.0.1` | **NONE** — API-only (reclassify Tier-3-API: JWT-validation only, no browser flow) | +50 / 0 |
| `bp-litmus` | `1.0.0` | **NONE + WEAK upstream OIDC — DEFER** under G118.3 | 0 (recommended) |
| `bp-wordpress-tenant` | `0.3.2` | **BEST IN TIER-3** — full chain via `openid-connect-generic` plugin; minimal patches needed | +67 / -3 |
| `bp-iceberg` | (no chart) | **CHART DOES NOT EXIST — DROP** | 0 (deferred under G118.2) |

**Tier-3 fan-out reduces to 6 active apps after opensearch / litmus / iceberg drops** (matrix, langfuse, temporal, librechat, openmeter, wordpress-tenant).

## Rollup — SSO chart-readiness across all 13 apps

| Verdict | Count | Apps |
|---|---|---|
| **GOOD** (most wiring present; small chart patches) | 2 | bp-powerdns-admin, bp-librechat |
| **BEST** (canonical reference chart for tier) | 1 | bp-wordpress-tenant |
| **PARTIAL** (50-80% wiring; medium patches) | 3 | bp-guacamole, bp-matrix, bp-temporal |
| **SCAFFOLD-ONLY** (decorative gate, real enforcement absent) | 1 | bp-cilium (Hubble UI) |
| **NONE** (zero OIDC chart wiring) | 2 | bp-langfuse, bp-openmeter |
| **DROP — no UI** | 1 | bp-sigstore |
| **DROP — chart does not exist** | 2 | bp-opensearch, bp-iceberg |
| **DEFER — upstream weak / image rebuild needed** | 1 | bp-litmus |

## Wave-2 active worklist after drops + defers

**9 apps to ship SSO patches for in Wave-2:**

- Tier-2: bp-guacamole, bp-powerdns-admin, bp-cilium (Hubble UI)
- Tier-3 (browser flow): bp-matrix, bp-langfuse, bp-temporal, bp-librechat, bp-wordpress-tenant
- Tier-3-API (token validation only): bp-openmeter

## Total estimated chart-patch size — Wave-2 SSO fan-out

| Tier | Apps | Lines added | Lines removed | Net |
|---|---|---|---|---|
| Tier-2 | 3 | ~153 | ~96 | ~+57 |
| Tier-3 browser-flow | 5 | ~379 | ~27 | ~+352 |
| Tier-3-API | 1 | ~50 | 0 | +50 |
| **Total Wave-2** | **9** | **~582** | **~123** | **~+459** |

## Cross-cutting Wave-2 prerequisites (out of B5 scope, but blocking)

These must land in adjacent slots BEFORE Wave-2 SSO fan-out can succeed end-to-end:

1. **`platform/keycloak/chart/templates/configmap-per-org-realm.yaml`** (Wave-2.C4 — owned by KC chart maintainer slot).
   - Per-Org realm JSON template parameterized by Org slug.
   - Identity Provider entry `sovereign-broker` pointing at `https://auth.<sov>/realms/sovereign`.
   - Identity Provider Redirector authenticationConfig binding `defaultProvider: sovereign-broker` on per-Org realm browser flow (G113 IDR pattern from PR #2730).
   - `clients[]` array populated by reading `sso.openova.io/app-registration: <client-id>` labeled ConfigMaps in the Org's namespace.

2. **`bp-sso-bridge` reconciler extension** — watches `AppRegistration` ConfigMaps (Option B), mints KC clients in per-Org realm via admin API, writes `client_secret` to OpenBao at `sso/<realm>/<clientId>`. Owned by a separate Wave-2 sub-EPIC.

3. **Sovereign realm `clients[]` extension** — for each per-Org realm, a `org-<slug>-broker` confidential client in the sovereign realm so the per-Org realm can call sovereign realm's authorize endpoint AS a client. Owned by Wave-2.C4 alongside #1.

4. **catalyst-api `org` clientScope mapper enforcement** — sovereign realm already emits `org_id` claim per `configmap-sovereign-realm.yaml:254-275`. Wave-2 must ensure per-Org realm's `firstBrokerLoginFlow` validates this claim equals the realm's expected Org before linking accounts.

5. **`helm.sh/resource-policy: keep` Secret pattern** for every per-app SSO Secret (per `feedback_chart_credential_persistence_defense.md`) — codified across all 5 Tier-3 browser-flow apps.

## Recommended Wave-2 split (parallel-agent dispatch shape)

See `wave-2-dispatch-recommendations.md` in this directory.

## Anti-theater red-flags surfaced during this audit

Per `feedback_six_pillars_target_state_no_workaround.md` and the G117 brief's anti-theater list:

1. **`bp-cilium` Hubble UI `auth: oidc` annotation is decorative** (`hubble-ui-httproute.yaml:52`) — comment at line 27-33 admits the OIDC layer was never wired. Wave-2 MUST not perpetuate the gate-without-enforcement pattern.
2. **`bp-guacamole` divergent realm-patch ConfigMap** targets a `catalyst` realm (`keycloak-realm-config.yaml:42`) — the G117 codified realm is `sovereign`. Two realms can't both be active; one is dead code. Wave-2 DELETES the dead template, not patch-around.
3. **`bp-sigstore` listed in Tier-2 brief without a UI** — Wave-2 must escalate the brief mismatch back to G117 EPIC #2737 rather than fabricate a SSO target.
4. **`bp-opensearch` / `bp-iceberg` charts don't exist** — Wave-2 must not pretend they do. Topology audit row in `2026-06-02-per-blueprint-topology-audit.md` is aspirational; STATUS.md should reflect reality.
5. **`bp-litmus` upstream OIDC is experimental + needs custom frontend image** — Wave-2 must not ship a partial wiring that lands `OIDC_ENABLED=true` env vars on a chart whose UI doesn't have the SSO button.

These match the brief's red-flag list and should appear verbatim in the verifier sub-agent's checklist.

## Banned-terms compliance

Per `docs/GLOSSARY.md` §Banned-terms — this audit consistently uses `Organization` / `org` / `org-<slug>` per the codified per-Org realm naming (locked decision #6). Legacy `sme-<subdomain>` naming surfaces in the `bp-wordpress-tenant` chart and is called out for harmonization in that audit doc.

## Memory + ADR discipline

This audit doc itself is a session artifact, not memory. Per `feedback_amnesia_antipatterns_2026_05_20.md` L8, gaps surfaced are NOT each their own TBD — they consolidate under the existing G117.5 #2744 and the recommended drops/defers map to fresh sub-EPIC numbers (G118.1, G118.2, G118.3).

If Wave-2 implementation surfaces non-obvious gotchas not yet in memory, ship `feedback_<topic>.md` IN THE SAME PR per the G117 brief Memory discipline rule.
