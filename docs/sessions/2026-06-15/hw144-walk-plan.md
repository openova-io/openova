# hw144 — comprehensive walk plan (the 100% validation)

Env: `hw144.omani.works` (`d8e798bdf1b4256b`), 2-region, **all 5 fixes baked** (catalyst `c7cde4d`):
cutover-half-pivot #3568 · newapi #3564 · openbao-raft #3562 · Open-button #3570 · shared-PG-DR #3572.

**Sign-in:** `python3 -c 'import jwt,time;k=open("/tmp/hw-priv.pem").read();n=int(time.time());print("https://console.hw144.omani.works/auth/handover?token="+jwt.encode({...,"aud":["https://console.hw144.omani.works"],"deployment_id":"d8e798bdf1b4256b","role":"sovereign-admin","email":"emrah.baysal@openova.io","exp":n+600},k,"RS256"))'` → MCP Playwright `browser_navigate`.

## NS#1 — every app in a vCluster (#3373)
- `/dashboard` treemap LAYER1=vCluster → 4 groups host/mgmt/dmz/rtz; `/apps` 49 INSTALLED.
- ✅ when: 9 re-homed apps in target vClusters + treemap renders. Screenshot `/dashboard` treemap.

## NS#2 — 3 shared-PG cards + contexts (#3370) [Q1/Q2 re-prove]
- `/apps` → `shared-pg`/`-b`/`-c` instance cards with ⛓ context badges.
- `/app/shared-pg` Topology tab → **MUST now read `active-hot-standby` (cross-region), NOT `singleton`** (the #3572 fix — Q1's gap closed). Screenshot.
- Contexts tab → many-to-many table.
- ✅ when: 3 cards + 11 contexts AND shared-pg topology = active-hot-standby.

## NS#3 — zero-login SSO (#3374) [Q6 re-prove]
- Bare URLs signed-in admin: console · grafana(`/admin/users`) · gitea(`/admin`) · harbor(`/harbor/users`) · openbao(`/ui/`) · keycloak(`/admin/sovereign/console/`) · guacamole · pdns-admin(`/dashboard/`) · hubble · marketplace.
- **newapi** (`/app/.../newapi`): 1st bare-URL visit signed-in, **2nd visit ALSO signed-in** (no "already bound" — the #3564 idempotent re-login). Screenshot both.
- `/apps` grid → **per-card "↗ Open" buttons render** (the #3570 restore — Q6's gap closed). Screenshot.
- ✅ when: 11-12/12 zero-click + newapi re-login lands + Open buttons on grid.

## NS#4 — region-kill preserves a CONSUMER's data (#3375 + #3572)
- `/app/bp-cnpg-pair` + shared-pg cards → active-hot-standby, Switchover enabled.
- **Walk:** write a keycloak realm marker → kill region-a (cordon+delete primary) → region-b promotes → **the keycloak realm SURVIVES** on the promoted shared-pg (the #3572 cross-region data DR — the deep gap).
- ✅ when: RTO measured + RPO=0 + the keycloak realm marker present post-promotion.

## Cutover — the keystone (#3379)
- Drive the 11 steps to `cutoverComplete=true`. The #3568 half-pivot fix → strip ghcr.io only AFTER the source pivot → **no wedge** (watch HRs stay Ready through the strip).
- ✅ when (4 proofs): `cutoverComplete=true` · the 600s `cutover-egress-block` CCNP held green · `ghcr-pull` keys `registry.hw144…` · Flux GitRepository = local Gitea.

## Funnel back-half (#3376)
- Voucher-credit → provisioned-tenant (org-active) completion.

After each: update `docs/ledger/UAT.md` + the per-surface walkthrough with the live screenshot; flip the row only on a screenshot.
