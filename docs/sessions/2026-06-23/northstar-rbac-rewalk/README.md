# North-Star demo-user journey — live RBAC re-walk (2026-06-23, omantel.biz)

**Env:** PERMANENT omantel.biz Sovereign, dep `4635277cae4ffed9`, demo Org `org-7283eb4a-19e5-4e86-9066-d4aa26762064`.
**Method:** minted RS256 handover JWTs signed with the Sovereign's own `/var/lib/catalyst/handover-jwt-private.pem`
(validated by `ValidateToken`→`LocalPublicKey`); probed the LIVE public API with `Authorization: Bearer` +
`X-Tenant-Host`. No PIN, no founder mailbox touched. HTTP codes are the evidence.

Journey: **demo user → org 7283eb4a → Agenity → app/deployment view (no 404) → no global-admin.**

| # | Leg | Surface | Result | Verdict |
|---|-----|---------|--------|---------|
| 1 | demo user authenticated | `GET /api/v1/whoami` (demo@ org-admin, console.demo.omani.homes) | **200** — `email=demo@openova.io, mode=sovereign, deploymentId=4635277cae4ffed9, tier=org-admin` | ✅ |
| 2 | org bound to omantel.biz dep | `GET /api/v1/sovereign/self` | **200** — `{deploymentId:4635277cae4ffed9, sovereignFQDN:omantel.biz}` | ✅ |
| 3 | Agenity present in apps grid | `GET /api/v1/sovereign/apps` | **200** — app `agenity` (blueprint `bp-agenity`) in the org grid | ✅ |
| 4 | org app/deployment view renders, NO 404 | `GET /api/v1/sovereign/apps` | **200** — full live app list w/ per-app status (installing/installed/bootstrap) | ✅ |
| 5 | NO global-admin access | `GET /api/v1/deployments` (demo token) | **403 `org-scoped-forbidden`** — "this Organization session has no access to Sovereign-admin or cross-organization resources" | ✅ |
| 5b | sovereign dep denied to org | `GET /api/v1/deployments/4635277cae4ffed9` + `/events` (demo token) | **403 `org-scoped-forbidden`** (both) | ✅ |

## Notes / honest caveats
- **Leg 4 surface clarification:** the org-user's app/deployment view is `/api/v1/sovereign/apps` (OrgScopeGuard-allowlisted) → 200 with real data. The literal `/api/v1/deployments/{id}/events` SSE is a **sovereign-admin** surface; for an org session it correctly returns **403** (that is leg-5 isolation, not a bug). On the Sovereign's own API that path 404s for the mothership dep-id because the Sovereign does not hold the mothership's deployment record — architecturally correct.
- **Operator-side mothership stream NOT evidenced here:** `console.openova.io/api/v1/*` returns chi `404 page not found` (operator API is a different host/surface); out of scope for the demo-user journey — not claimed.
- **agenity status-projection lag:** the apps grid shows `status:installing` for agenity, yet its pod serves `/app/` live (HTTP 200, NEW dashboard `Dashboard.BmDlYS5Q.js` — see the #4118/#4173 cache fix). Functional; the grid status projection lags. Worth a follow-up, not a journey blocker.
- The agentic chat→provision legs (UAT rows 218–223) were walked + screenshot-evidenced 2026-06-22; this re-walk re-confirms the RBAC/org-scope spine live on 2026-06-23 after the #4171/#4172/#4173 merges.
