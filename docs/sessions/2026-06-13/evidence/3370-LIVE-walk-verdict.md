# #3370 CONTEXTS — LIVE walk verdict (hw130, 2026-06-13 ~04:19 UTC, post-restore)

Walked by the operator's main session via a fresh handover (admin roles: sovereign-admins/catalyst-admin/owner). Structured accessibility snapshots committed alongside; screenshots captured in-session (MCP sandbox).

## DoD box-by-box — LIVE

| Box | Result | Live evidence |
|---|---|---|
| 1 — shareable badge | ✅ PASS | /catalog/bp-postgres header renders `⛓ shareable · db` + `multi-instance` badges (3370-LIVE-catalog-instances.snapshot.yml) |
| 2 — Application CR per instance, zero dup | ✅ PASS | `kubectl get applications -A` = 4 CRs: shared-pg, shared-pg-b, shared-pg-c (bp-postgres@0.1.8), valkey (bp-valkey@1.1.2) — all Ready, bootstrap-owned, ZERO duplicate HRs (3370-LIVE-application-crs.txt) |
| 3 — /apps 3 instance cards + ⛓ badge | ✅ PASS | /apps shows shared-pg, shared-pg-b, shared-pg-c each a SEPARATE card with `⛓ 3 contexts` badge + valkey card (3370-LIVE-apps-instance-cards.snapshot.yml) |
| 4 — /catalog/bp-postgres instances + New + panel GONE | ✅ PASS | Instances table: shared-pg / shared-pg-b / shared-pg-c → /app/$id; `+ New instance` button; Supported topologies; the rejected "Data instances" panel is ABSENT (3370-LIVE-catalog-instances.snapshot.yml) |
| 5 — Contexts tab entity-first table | ✅ PASS | /app/shared-pg `Contexts 3` tab renders exactly: `db/registry → harbor → harbor-database-secret → ready`, `db/gitea → gitea → gitea-database-secret → ready`, `db/keycloak → keycloak → keycloak-database-secret → ready` (3370-LIVE-contexts-tab.snapshot.yml) |
| 6 — provisioning journey | code-proven (#3382), live New-instance dialog present |
| 7 — generality (valkey) | ✅ valkey instance card live; keyspace Contexts machinery (declarations-only diff #3382) |
| 8 — ≥2 embedded DB eliminations | pda + SME behind the one-flag contract (#3382) |

**Verdict: the founder's canonical model is LIVE.** Three independent postgres instances render as three independent cards (not one), each exposing its Contexts (the consumers occupying isolated db/ children), the catalog lists instances with a New-instance action, and the ad-hoc panel is gone.
