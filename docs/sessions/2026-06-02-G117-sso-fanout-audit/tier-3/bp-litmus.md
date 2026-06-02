# Tier-3 SSO audit — `bp-litmus` (LitmusChaos ChaosCenter)

> Federates to: per-Org realm via 2-hop (per-Org realm → `sovereign` realm broker → catalyst-pin IdP).
> Chart path: `platform/litmus/chart/`
> Chart version on `main`: `1.0.0` (Chart.yaml:15)
> Topology row (audit doc): rtz-A 🔵 S / rtz-B 🔵 S (App-tier per-cluster singleton — chaos experiments are intentionally cluster-scoped)

## Current SSO state — **NONE**

| Surface | Status | File:line |
|---|---|---|
| `oidc.*` / `sso.*` values block | MISSING | n/a |
| Upstream chart OIDC handle (`litmus.server.authServer.OIDC_*`) | NOT WIRED (chart has no `templates/`; values lacks any auth-server OIDC block) | n/a |
| Per-app SSO Secret materialization | MISSING | n/a |
| Per-Org realm registration | MISSING | n/a |
| `kc_idp_hint=catalyst-pin` injection | MISSING | n/a |

## Upstream chart OIDC handle — **WEAK / EXPERIMENTAL**

LitmusChaos's auth-server (`litmuschaos/litmusportal-auth-server` 3.28) supports OIDC via env vars (verified in upstream source 3.x line):

- `OIDC_PROVIDER` — discovery URL
- `OIDC_CLIENT_ID`
- `OIDC_CLIENT_SECRET`
- `OIDC_REDIRECT_URI`
- `OIDC_ENABLED` — bool

**But:** as of 3.28, LitmusChaos OIDC integration is upstream-experimental and the role-mapping plane (KC group → ChaosCenter project role) is manual. The ChaosCenter UI does NOT have a "Sign in with Keycloak" button by default — it requires upstream config + a custom build of the frontend image to surface the SSO button.

## Required Wave-2 patch plan — **DEFERRED v0 / minimal v1**

### Option A — full SSO wiring (NOT recommended for B5)

Would require:

1. New `templates/sso-externalsecret.yaml` ExternalSecret pulling from `sso/<org-realm>/litmus`.
2. New `templates/sso-app-registration.yaml` registering `litmus` client in per-Org realm.
3. Upstream chart values overlay extending `litmus.server.authServer.env[]` with OIDC_* envs.
4. Custom ChaosCenter frontend image with SSO button or post-install Job that patches the login UI ConfigMap.
5. Role-mapping job that maps KC `org-{slug}-admins` group → ChaosCenter `Owner` role on the org's default project.

Estimated patch: ~150 lines + a new image build pipeline. **Out of B5 scope.**

### Option B — local-account auth only, document as KNOWN GAP (recommended for B5)

LitmusChaos chart-side stays as-is. Cluster-admin-tier operators authenticate to ChaosCenter via the chart-auto-generated `admin` user (password in `litmus-portal-admin-secret`). Document the SSO gap as a follow-up under a fresh `G118.3 — bp-litmus full SSO` sub-EPIC, gated on:

- Upstream LitmusChaos 4.x (planned 2026Q3) which is expected to land first-class OIDC.
- An optional Catalyst-side `bp-litmus-sso-wrapper` chart that fronts ChaosCenter with oauth2-proxy (same pattern as `bp-cilium` Hubble UI).

### Recommendation

**Wave-2 SSO agent should SKIP `bp-litmus`** for the v1 fan-out and document the gap. The chart will not regress, and the topology row in `2026-06-02-per-blueprint-topology-audit.md:128` already says "n/a" for switchover (chaos experiments are cluster-local).

## Verification probe (current state — confirms gap, not SSO success)

```bash
# Today's state — ChaosCenter login form returns 200 (local-account only)
curl -ksI "https://litmus.<org>.<sov-fqdn>/auth/login"
# Expected: HTTP/2 200 (HTML login form, NOT a 302 redirect to KC)

# After Option A would land — Expected: HTTP/2 302 → https://auth.<sov-fqdn>/realms/<org-realm>/protocol/openid-connect/auth?...
```

## Chart-patch size estimate

| Option | Files | Lines added | Lines removed |
|---|---|---|---|
| **Option A — full SSO (DEFERRED)** | 3 new templates + values.yaml + new image | ~150 + image pipeline | 0 |
| **Option B — defer with docs (recommended)** | 0 chart changes | 0 | 0 |

If Wave-2 picks Option B, lockstep: zero (no chart bump). Only `docs/sessions/2026-06-02-per-blueprint-topology-audit.md` row 128 gets a note. Track follow-up in `docs/ledger/TRACKER.md`.

## DROP from active Wave-2 worklist

Reduces Tier-3 active patch count from 8 to **7 apps** (matrix, langfuse, temporal, librechat, openmeter [API-only], wordpress-tenant; + bp-litmus deferred under G118.3).
