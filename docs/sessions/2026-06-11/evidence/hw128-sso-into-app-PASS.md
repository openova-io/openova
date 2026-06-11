# hw128 — SSO-into-app PASS (#3272 / #3271) — live walk 2026-06-11

Real email+PIN walk on the freshly-converged hw128 (zero-touch, 57/60 HRs Ready).

| Step | Action | Result |
|---|---|---|
| 1 | Navigate grafana.hw128.omani.works | Grafana /login with "Sign in with OpenOva SSO" |
| 2 | Click SSO | redirected to console.hw128/login carrying the **cross-host** next `https://api.hw128…/oidc/auth?...redirect_uri=https://auth.hw128…/broker/catalyst-pin/endpoint` |
| 3 | Page shows | "We'll take you to <the cross-host OAuth URL> after you sign in" — #3272 **preserves** the next param (#3271 dropped it) |
| 4 | Enter emrah.baysal@openova.io, Send code | /login/verify, PIN emailed |
| 5 | Read PIN 840152 from real Stalwart mailbox (IMAP mail.openova.io) | code received |
| 6 | Enter PIN | **redirected to grafana.hw128.omani.works/?orgId=1 — "Home - Dashboards - Grafana", AUTHENTICATED INSIDE THE APP** |

**PASS** — lands in the app, not console/dashboard. Contrast: hw126 (pre-#3272, PR #3270) landed on console/dashboard = FAIL. Screenshot: hw128-grafana-sso-PASS-landed-in-app.png.
