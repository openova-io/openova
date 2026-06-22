# SSO-landing + app-health re-walk — omantel.biz (PERMANENT env)

**Env:** Sovereign omantel.biz, Huawei kom4dc, dep `4635277cae4ffed9`, region me-east-215-a/-b. catalyst-api `fd587a2`, catalyst-ui `dc30503`.
**Date:** 2026-06-22. **Mode:** READ-ONLY verification (no code PRs, no Provision writes).
**Why:** A health-gate showed the named HRs converged to `ready=True` (the prior walk caught them mid-install → false ❌), so the SSO-landing + app-health rows became walkable.

## Health-gate (pre-walk)
Region A: `bp-guacamole`, `bp-powerdns`, `bp-powerdns-admin`, `bp-newapi`, `bp-grafana`, `bp-loki`, `bp-mimir`, `bp-tempo`, `bp-nats-jetstream`, `bp-seaweedfs`, `bp-valkey`, host `bp-keycloak` — all `READY=True`. No cutover in progress. Env quiescent.
Region B: the single-region mgmt apps' HRs (`bp-grafana`/`bp-guacamole`/`bp-powerdns-admin`) read `False` — `dependency 'mgmt/bp-keycloak' is not ready` (their home is region A).

## Login
Sovereign-admin = `emrah.baysal@openova.io` (openova.io mailbox, tier=owner, roles catalyst-admin+catalyst-owner). Passwordless 6-digit PIN issued via `POST /api/v1/auth/pin/issue`, read from the Stalwart mailbox over in-pod IMAP, verified via `POST /api/v1/auth/pin/verify` (field `pin`) → `catalyst_session` cookie. `/api/v1/whoami` confirmed the signed-in principal before walking.

## Verdicts

| Row | Test | Old | New | Evidence |
|---|---|---|---|---|
| 35 | Guacamole bare URL → connections list signed-in | ❌ | ✅ | `guacamole-connections-signed-in.png` — OIDC vs `auth.omantel.biz/realms/sovereign`, id_token `emrah.baysal@openova.io` groups incl `sovereign-admins` → Home connections list |
| 36 | PowerDNS-Admin bare URL → dashboard signed-in | ❌ | ✅ | `pdns-admin-dashboard-signed-in.png` — `/dashboard/` signed-in, full Admin menu, live omantel.biz zone |
| 37 | newapi 1st hit → `/console` signed-in | ❌ | ❌ | `newapi-signing-in-503.png` — HR Ready but app workload ABSENT (only `newapi-pg` CNPG); `/api/*` 503 |
| 38 | newapi 2nd hit → `/console` signed-in | ❌ | ❌ | same root as 37 |
| 67 | grafana console reports Healthy/Running both regions | ❌ | ⚠️ | `grafana-app-degraded-hr-ready.png` — pod 3/3, HR Ready, no write-host crashloop; console rollup still DEGRADED (seaweedfs ImagePullBackOff + region-B keycloak) |
| 68 | powerdns-admin console reports Healthy | ❌ | ✅ | `pdns-admin-dashboard-signed-in.png` — pod 1/1, HR Ready, live dashboard renders zone (DB host resolved) |
| 70 | guacamole console reports Healthy both regions | ❌ | ⚠️ | `guacamole-app-degraded.png` — pods Running, HR Ready region-A, web UI live; console rollup DEGRADED (region-B keycloak) |
| 114 | newapi opens signed in, console renders | ❌ | ❌ | `newapi-signing-in-503.png` — app workload absent |
| 115 | Guacamole 2nd hit → connections list | ❌ | ✅ | `guacamole-connections-2nd-hit.png` — session persisted, connections home renders |
| 50/59/60/61 | topology Provision write | ❌ | ❌ | NOT attempted — Provision is a WRITE mutation, out of scope for a read-only verifier on the permanent env |

## Key findings
- **newapi (rows 37/38/114) — genuine env-independent ❌:** `bp-newapi` HR reports "install succeeded" but the `newapi` namespace contains ONLY the CNPG Postgres (`newapi-pg-1/2`). No newapi app Deployment/StatefulSet/Service/HTTPRoute. The static SSO shell serves (200) but `/api/user/self` + `/api/oauth/state` return 503 → the sign-in shim hangs on "Signing you in…". The HR "ready" is misleading — it only deployed the DB half.
- **grafana/guacamole (rows 67/70) — ⚠️ not ✅:** each app's own HR is Ready in region A and the live surface works, but the console app-page **rollup** badge reads DEGRADED because (a) the region-B HR copy is blocked on `mgmt/bp-keycloak`, and (b) grafana's companion seaweedfs object-store is ImagePullBackOff. The original crashloop/InstallFailed reasons are cleared, but the console does not report Healthy in BOTH regions.
