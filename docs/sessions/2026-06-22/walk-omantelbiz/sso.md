# sso epic (#3374) — live walk on omantel.biz — 2026-06-22

Walked READ-ONLY. Console host-app SSO probed by HTTP code from the host + Playwright render of the console.

## Host front-door HTTP codes (curl, primary ELB)
- console 200, marketplace 200, api/healthz 200, registry(harbor) 200
- gitea 303 (->SSO), grafana 302 (->SSO), auth(keycloak) 302, hubble 302, guacamole 302, powerdns 000 (DOWN)

## Per-row evidence
- **Row 26** PASS — bare console `/dashboard` lands signed-in, no PIN/login/"Sign in with" button (warm session) (`evidence/model-dashboard.png`). Cold-session behaviour = passwordless 6-digit email PIN form at `/login` (the only login UI), per North Star #3.
- **Row 27** WARN — avatar (top-right) area renders but reads generic "User" / "omantel.biz", NOT "Signed in as emrah.baysal@openova.io". The principal name is not resolved into the avatar (cosmetic identity-display gap). A Sign-out item was not confirmed.
- **Row 28/41/45** PASS — Users page (`/users`, "User Access") renders the pre-seeded owner row `emrah.baysal@openova.io` with the `omantel-biz (admin)` grant + tier filters (viewer/developer/operator/admin/owner) + New (`evidence/sso-users.png`). Matches live `useraccesses.access.openova.io` CRs.
- **Row 29** PASS — re-open the bare console URL with the persisted session -> lands signed-in again, no PIN re-prompt (observed across ~15 navigations until session TTL lapsed; after lapse the PIN form appears = expected, then a fresh session lands signed-in again).
- **Row 30 (Grafana)** WARN — grafana host returns 302 to the SSO flow (chart status INSTALLING); did not reach a signed-in Grafana Home (grafana HR not Ready: loki/mimir object-store deps FAILED).
- **Row 31 (Gitea)** WARN — gitea host returns 303 to SSO (chart INSTALLING); signed-in dashboard not reached.
- **Row 32 (Harbor)** WARN — harbor host (registry.omantel.biz) returns 200 but Harbor chart is INSTALLING; signed-in `/harbor/projects` not confirmed.
- **Row 33 (OpenBao)** WARN — openbao chart INSTALLING; bare UI not reached.
- **Row 34 (Keycloak admin)** WARN — auth.omantel.biz 302 (host keycloak `keycloak-0-x-keycloak-x-mgmt-vcluster` is 1/1 Running); admin-console drill-in not walked read-only.
- **Row 35 (Guacamole)** FAIL — guacamole HR InstallFailed (`Helm install failed ... failed post-install`); host 302 but no connections list. powerdns + guacamole-pg are stuck "Setting up primary".
- **Row 36 (PowerDNS-Admin)** FAIL — powerdns.omantel.biz returns 000 (no healthy upstream); pdns-admin not reachable.
- **Row 37/38 (newapi)** FAIL — `bp-newapi` HR is FAILED (dep bp-keycloak not ready in the demo Org); newapi `/console` not reachable.
- **Row 39 (Hubble)** WARN — hubble host 302 to SSO; authenticated Hubble UI not walked read-only.
- **Row 40 (Marketplace)** PASS — marketplace root renders without a spurious forced login; no `console.openova.io`/`omantel.openova.io`/bare `openova.io` host literal in the HTML, no "tenant" banned-term leak. (It does redirect an unauth visitor into the passwordless PIN flow, which is the by-design auth-once model.)
- **Row 42/43/44** WARN — Keycloak realm internals (Groups `/sovereign-admins`, role mapping `catalyst-admin`, realm-management `realm-admin`) require an authenticated Keycloak admin-console drill-in; not exercised in the read-only console walk. Host keycloak is up; demo-Org keycloak is ImagePullBackOff.
