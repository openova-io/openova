# Live walk — demo customer console is Org-scoped (#4110) + Open Agenity card (#4120) — omantel.biz, 2026-06-22

Env: omantel.biz Sovereign (Huawei kom4dc), dep `4635277cae4ffed9`. catalyst-api `fd587a2` (PR #4121 host-anchor merged + rolled). catalyst-ui `dc30503`.

## #4110 — customer is Org-scoped, NOT sovereign-admin god-mode — PASS
`GET https://console.demo.omani.homes/api/v1/whoami` (live session `demo@openova.io`):
```json
{ "email":"demo@openova.io","tier":"org-admin","orgScoped":true,"org":"demo",
  "realm_access":{"roles":["org-admin"]},"deploymentId":"4635277cae4ffed9","sovereignFQDN":"omantel.biz","mode":"sovereign" }
```
Sovereign-endpoint enforcement on the Org host (OrgScopeGuard):
- `GET /api/v1/deployments`    → **403**
- `GET /api/v1/organizations`  → **403**

SPA sidebar = `Apps / Catalog / Sandbox / Users / Settings` ONLY. No Deployments, Organizations directory, BSS/Billing, Cloud explorer, or Parent-domains. User chip = "User · demo.omani.homes". The sovereign god-mode estate the founder saw is GONE. (Screenshot: `demo-apps-scoped-open-agenity.png`.)

## #4120 — Open Agenity card renders — PASS
`agenity-demo` app card shows **INSTALLED** + "Open agenity-demo — single-click silent sign-in, opens in a new tab". `GET /api/v1/org/applications` now responds (was 404 on the pre-roll `c5f7034`).

## Still open (separate, North-Star runtime layer — rows 220-223)
agenity dashboard SERVES (chepherd 0.9.4 daemon; web frontend upstream-versioned 0.5.x) but shows "runtime offline · 0 workers": the chat-to-provision runtime needs the Anthropic OAuth token (`sk-ant-oat01`) wired — `ANTHROPIC_API_KEY` strips it. Next fix.

## Deployment-stream 404 (founder symptom) — RESOLVED
Live `browser_evaluate` on `console.demo.omani.homes` (org session `demo@`):
```
location after /dashboard nav : /apps   (org customer redirected off the sovereign dashboard)
deploymentStreamErrorVisible  : false   ("Couldn't reach the deployment stream" GONE)
has404TextVisible             : false
GET /api/v1/deployments/4635277cae4ffed9        -> 403  (was the 404 the founder saw; now host-anchored-gated)
GET /api/v1/deployments/4635277cae4ffed9/events -> 403
```
The wizard/deployment-stream is `enabled=!orgScoped` (#4119), so the org customer never fetches the Sovereign deployment `4635277cae4ffed9` → no 404. The guard returns 403 on the raw endpoint (defense-in-depth).
