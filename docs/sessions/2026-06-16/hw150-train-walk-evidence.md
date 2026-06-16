# hw150 — IaC-first train, live walk evidence (2026-06-16)

Live validation of the 7-car "IaC-first train" on a **fresh zero-touch 2-region prov**.
Each row is a determination against the founder's verbatim review items / north-stars —
not a path-level green. `✅ met · 🟡 partial · ❌ not met`.

## Environment

| Field | Value |
|---|---|
| Deployment | `1290a8ef7f48f276` |
| FQDN | `hw150.omantel.biz` (pool `omantel.biz`, LE-rotation off `omani.works`) |
| Regions | primary `me-east-215-a` (212.72.24.28) + secondary `me-east-215-b` (212.72.24.39) |
| Image | catalyst-api/ui `80a9d2c` · chart `bp-catalyst-platform@1.4.651` |
| Convergence | zero-touch; 56→63 primary HRs Ready; console HTTP 200; wildcard cert Ready |
| Train on main | T0 #3649 · T1 #3650 · T2 #3655 · T3 #3652 · T4 #3658 · T5 #3653 · T6 #3651 (HEAD `80a9d2c`) |

## Determinations

| # | Item (verbatim intent) | Verdict | Evidence |
|---|---|---|---|
| NS#3 | SSO — every external-facing app, URL lands signed in as emrah.baysal admin | ✅ | grafana.hw150 → after one-time email-OTP (catalyst-pin → Keycloak sovereign realm → grafana) lands **signed in**, full UI, Profile avatar. `hw150-ns3-grafana-signedin.png`. Console itself authenticated via the same OTP. |
| #1 | Catalog detail page editable **in place** (no chip+popup); render not "shitty" | ✅ | `/catalog/bp-alloy` → inline **Edit** button → embeds an **"Edit catalog entry"** form *in the page* (Display name / Summary / Supported-topologies checkboxes / theme icons / Save·Cancel), no navigation, no modal. Rich render: About + Instance(Open-instance) + Supported-topologies(`singleton` canonical). `hw150-item1-catalog-inline-edit.png`. |
| #8 | IaC is the single source of truth; catalog UI edit updates the IaC | 🟡 | Save **persists in UI** and correctly **attempts the IaC commit** to `catalog-sovereign/bp-alloy/blueprint.yaml` via Gitea (the IaC-as-truth mechanism is wired). BUT the live commit fails: `gitea POST …/contents/blueprint.yaml: context deadline exceeded`. Gitea itself responds in 0.08s, so root cause is likely the `catalog-sovereign` repo not yet seeded on a fresh env, or a short ctx deadline. **→ follow-up.** |
| NS#4 | Agreed multi-region apps actually run multi-region | ✅ | Secondary `me-east-215-b` runs full independent gitea/openbao/keycloak/harbor/grafana/powerdns/guacamole + vcluster trio. **CNPG cross-region WAL streaming proven**: replica `pg_stat_wal_receiver.status=streaming` from `cnpg-pair-…-primary-mesh:5432`. |
| NS#1 | Every application runs IN a vCluster (dmz/mgmt/rtz) | 🟡 | 3 vclusters real & functional with **27 workloads inside** (coraza / loki·mimir·nats·tempo / seaweedfs·valkey·vllm). But ~58 platform app pods (gitea/openbao/keycloak/harbor/…) run on the **host**; **no tenant Applications exist yet** to exercise the catalog `tier:` placement. Founder judgment on host-resident shared services. |
| NS#2 | Provisioning creates 3 shared PG instances → 3 cards, 6-7 apps many-to-many | ❌ | `SOVEREIGN_ENABLE_SHARED_PG` unset this prov → `shared-data` ns empty, the 3 `bp-postgres-shared{,-b,-c}` HRs render `enabled:false` (0 bytes); consumers fell back to per-app embedded CNPG clusters. Feature shipped in code but OFF on hw150. |
| #3 | postgres create accepts canonical topology (no `active-hotstandby not in supported`) | 🟡 | Blueprint CRD `topology.supported` = canonical `[active-active, active-hot-standby, active-passive, singleton]` ✅; no CR stuck on topology rejection ✅. BUT `cnpgpairs.dr.openova.io` CRD + catalyst-api code (`core/services/catalog/store/store.go:38`, `provisioning/gitops/gitops.go:677`) still carry un-hyphenated `active-hotstandby` → canonicalization **incomplete**. |

## Open follow-ups (surfaced by this walk)

1. **#8 catalog-edit→IaC commit times out** — verify `catalog-sovereign/<bp>/blueprint.yaml` repo seeding on fresh provs; widen the Gitea write context deadline. (mechanism present; not landing.)
2. **#3 topology canonicalization incomplete** — canonicalize `cnpgpairs` CRD enum + the `active-hotstandby` literals in `core/services/catalog/store/store.go` + `provisioning/gitops/gitops.go`.
3. **NS#2 shared-PG OFF** — the 3-instance model needs `SOVEREIGN_ENABLE_SHARED_PG=true` on the fire (prior attempt #3283 deadlocked); needs a clean enablement path.

## Still to walk (this env, pre-wipe)

T2 de-hardcode (generic render across blueprints) · T4 UI faithfulness (Open-button timing, jobs freshness, new-instance no-flash) · T5 DR switchover (Continuum) · T3 flux-first activities · T1 cutover (dormant→post-handover).

_Evidence images live alongside this file under `evidence/` once exported from the Playwright run dir._
