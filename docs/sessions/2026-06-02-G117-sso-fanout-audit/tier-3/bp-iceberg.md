# Tier-3 SSO audit — `bp-iceberg` (catalog REST UI)

> Federates to: per-Org realm via 2-hop (per-Org realm → `sovereign` realm broker → catalyst-pin IdP).
> Chart path: `platform/iceberg/` (**SCAFFOLD-ONLY — no chart subdir on `main`**)
> Topology row (audit doc): rtz-A 🟢 A / rtz-B 🟢 A (App-tier active-active — all data on S3)

## Current SSO state — **CHART DOES NOT EXIST**

```bash
$ ls /home/openova/repos/openova/platform/iceberg/
README.md
```

`platform/iceberg/` is README-only. No `chart/` subdir, no `blueprint.yaml`. The audit doc row at `docs/sessions/2026-06-02-per-blueprint-topology-audit.md:126` is **aspirational** — Wave-2 SSO fan-out cannot wire SSO to a non-existent chart.

Furthermore, the audit doc lists the "endpoint" as `iceberg.<org>.<sov>` (catalog REST) — Iceberg has multiple potential "UI" surfaces:

1. **REST catalog API** (`iceberg-rest-fixture` upstream) — JSON HTTP API, not a UI; OIDC support is via Bearer-token validation (resource-server pattern, NOT browser OIDC client flow).
2. **Hive Metastore-style catalog** — no UI.
3. **Tabular.io / Apache Polaris / Lakekeeper** — third-party Iceberg catalog implementations with web UIs and full OIDC, but each is a different upstream chart.

The dispatch brief calls this "catalog REST UI" which is ambiguous — REST API doesn't have a UI in the conventional sense. SSO for a REST catalog is JWT/token-validation, not browser-redirect.

## Recommendation

Wave-2 SSO agent should **NOT** attempt to add SSO templates to `platform/iceberg/`. Three follow-ups:

1. **Document the gap** in `docs/STATUS.md` — `bp-iceberg` is design-stage, not built. Note the ambiguity about which Iceberg catalog implementation is targeted (recommend Apache Polaris for OIDC parity with the rest of Tier-3).
2. **Open a fresh sub-EPIC** (`G118.2 — bp-iceberg chart`) to pick a catalog implementation + author the chart.
3. **When the chart lands**:
   - If REST-only catalog: SSO is **JWT validation only** — KC discovery URL in catalog config + Resource Server bearer-token check. NO browser OIDC client flow, NO AppRegistration ConfigMap, NO `kc_idp_hint` injection (no browser redirect). Maps to Tier-2 of a NEW classification ("API-only with JWT validation") — different audit row structure.
   - If Apache Polaris (has UI): standard Tier-3 pattern from `bp-langfuse.md` / `bp-temporal.md`.

## Estimated chart-patch size (when chart exists, Polaris case)

| File | Lines added | Lines removed |
|---|---|---|
| `platform/iceberg/chart/templates/sso-externalsecret.yaml` (new) | ~30 | 0 |
| `platform/iceberg/chart/templates/sso-app-registration.yaml` (new) | ~25 | 0 |
| `platform/iceberg/chart/values.yaml` (Polaris OIDC config) | ~25 | 0 |
| **Estimated total** | **~80** | **0** |

## DROP from Wave-2 worklist

Reduces Tier-3 from 9 apps to **7 apps** when combined with bp-opensearch drop (matrix, langfuse, temporal, librechat, openmeter, litmus, wordpress-tenant).
