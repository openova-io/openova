# Tier-3 SSO audit — `bp-opensearch` (OpenSearch Dashboards)

> Federates to: per-Org realm via 2-hop (per-Org realm → `sovereign` realm broker → catalyst-pin IdP).
> Chart path: `platform/opensearch/` (**SCAFFOLD-ONLY — no chart subdir on `main`**)
> Topology row (audit doc): rtz-A 🟢 A / rtz-B 🟢 A (App-tier active-active via CCR)

## Current SSO state — **CHART DOES NOT EXIST**

```bash
$ ls /home/openova/repos/openova/platform/opensearch/
README.md
```

`platform/opensearch/` is README-only. No `chart/` subdir, no `blueprint.yaml`. The audit doc row at `docs/sessions/2026-06-02-per-blueprint-topology-audit.md:106` is **aspirational** — Wave-2 SSO fan-out cannot wire SSO to a non-existent chart.

## Recommendation

Wave-2 SSO agent should **NOT** attempt to add SSO templates to `platform/opensearch/`. Two follow-ups:

1. **Document the gap** in `docs/STATUS.md` — `bp-opensearch` is design-stage, not built.
2. **Open a fresh sub-EPIC** (e.g. `G118.1 — bp-opensearch chart`) to author the chart umbrella around `opensearch-project/helm-charts` (`opensearch` + `opensearch-dashboards`). SSO wiring follows the standard Tier-3 pattern documented in this audit suite (see `bp-langfuse.md` or `bp-temporal.md`).
3. **When `bp-opensearch` chart lands**, the SSO patch plan is:
   - OpenSearch Dashboards `opensearch_security.openid.connect_url` = `https://auth.<sov>/realms/<org-realm>/.well-known/openid-configuration`
   - OpenSearch Dashboards `opensearch_security.openid.client_id` = `opensearch-dashboards`
   - Per-app SSO Secret via ExternalSecret pattern (`sso/<org-realm>/opensearch-dashboards`)
   - AppRegistration ConfigMap labeled `sso.openova.io/app-registration: opensearch-dashboards`
   - OpenSearch security plugin role-mapping from KC groups → OpenSearch roles (`all_access`, `kibana_user`, etc.) — same as Grafana team-sync pattern in G91

## Estimated chart-patch size (when chart exists)

| File | Lines added | Lines removed |
|---|---|---|
| `platform/opensearch/chart/templates/sso-externalsecret.yaml` (new) | ~30 | 0 |
| `platform/opensearch/chart/templates/sso-app-registration.yaml` (new) | ~25 | 0 |
| `platform/opensearch/chart/values.yaml` (`opensearch-dashboards.config.opensearch_dashboards_yml.opensearch_security.openid.*` overrides) | ~25 | 0 |
| `platform/opensearch/chart/templates/security-config-job.yaml` (OpenSearch security plugin role-mapping bootstrap, new) | ~50 | 0 |
| **Estimated total** | **~130** | **0** |

## DROP from Wave-2 worklist

Reduces Tier-3 from 9 apps to **8 apps** (matrix, langfuse, temporal, librechat, openmeter, litmus, wordpress-tenant; iceberg also drops — see `bp-iceberg.md`).
