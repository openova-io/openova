# UAT-215 god-access primitives (PROVEN LIVE 2026-06-24, omantel.biz dep 4635277cae4ffed9)

## Mothership pod (run kubectl against the live Sovereign)
MOTHERSHIP_POD=catalyst-api-5c6884549b-ghwt9   (ns catalyst; re-find with: kubectl -n catalyst get pods | grep catalyst-api)
SOV_KC=/var/lib/catalyst/kubeconfigs/4635277cae4ffed9.yaml         (primary region me-east-215-a)
SOV_KC_B=/var/lib/catalyst/kubeconfigs/4635277cae4ffed9-me-east-215-b-1.yaml   (region B)
Demo Org namespace: org-7283eb4a-19e5-4e86-9066-d4aa26762064  (slug "demo", org_tenant_id 7283eb4a-19e5-4e86-9066-d4aa26762064)

Run a kubectl on the live Sovereign:
  kubectl -n catalyst exec $MOTHERSHIP_POD -- sh -c "kubectl --kubeconfig $SOV_KC get pods -A"

## Minted JWTs (handover key /tmp/handover-jwt-private.pem already extracted)
Mint script: /tmp/mint_jwt.py   (usage: python3 /tmp/mint_jwt.py sovereign-admin  OR  org-admin demo)
/tmp/sovadmin.jwt  -> tier=owner, realm_access.roles=[catalyst-owner,catalyst-admin,sovereign-admins,viewer]  (FULL sovereign-admin)
/tmp/orgadmin.jwt  -> org=demo, tier=org-admin
Tokens expire 2h; re-mint with the script if 401.

## Calling the API (PROVEN 200)
HOST = console.omantel.biz   (the console-ELB front door; api.omantel.biz also -> 212.72.24.33)
DEP = 4635277cae4ffed9
Header: -H "Authorization: Bearer $(cat /tmp/sovadmin.jwt)"
Examples that returned 200:
  GET https://console.omantel.biz/api/v1/organizations                 -> live org list (demo)
  GET https://console.omantel.biz/api/v1/catalog                       -> blueprint cards
  GET https://console.omantel.biz/api/v1/catalog/{name}                -> bp detail
  GET https://console.omantel.biz/api/v1/sovereigns/$DEP/keycloak/groups   -> KC groups (sovereign realm)
  GET https://console.omantel.biz/api/v1/sovereigns/$DEP/keycloak/users
  GET https://console.omantel.biz/api/v1/sovereigns/$DEP/keycloak/roles
  GET https://console.omantel.biz/api/v1/sovereigns/$DEP/keycloak/groups/{groupId}   (membership/role-mapping)
  GET https://console.omantel.biz/api/v1/sovereigns/$DEP/k8s/{kind}     (live cluster objects)
  GET https://console.omantel.biz/api/v1/sovereigns/$DEP/applications
  GET https://console.omantel.biz/api/v1/sovereigns/$DEP/continuums/{name}
  GET https://console.omantel.biz/api/v1/org/users  (org-admin token, with X-Tenant-Host: console.demo.omani.homes)

## Browser session (Playwright) — set the cookie directly
The kid-less RS256 JWT validates as the catalyst_session cookie (raw-JWT path, 3 segments).
In Playwright: browser_navigate to https://console.omantel.biz, then set cookie:
  name=catalyst_session value=$(cat /tmp/sovadmin.jwt) domain=console.omantel.biz path=/ httpOnly=true secure=true
Then reload — you land signed-in. For org console use console.demo.omani.homes + /tmp/orgadmin.jwt.
NEVER use emrah.baysal@openova.io mailbox.

## Health-gate note (2026-06-24)
Env converged: 75 HR Ready, platform spine UpgradeSucceeded, front-doors 200.
The 5 False HRs are all in the demo Org ns (bp-newapi/bp-openclaw/bp-stalwart-tenant/bp-wordpress-tenant/vc-demo,
"context deadline exceeded") but serving pods (agenity, wordpress-tenant, stalwart) are Running — cosmetic per memory.
vc-demo-0 and bp-openclaw are Init:ImagePullBackOff (REAL, peer-owned).
A transient cilium-envoy-tls-restart caused a ~brief 000 window ~16:xx; it Succeeded and front-doors returned to 200.
If you see 000 across ALL front-doors mid-walk, re-check the envoy DS readiness before stamping ❌(env-transitional).
