# #4179 funnel close-gate walk — 2026-06-24

Post-#4222 roll close-gate attempt on the live omantel.biz Sovereign.

## Verdict: NOT closed — redirect host correct, pool DNS NXDOMAIN (blocked on #4236)

- #4222 catalyst-api (chart 1.4.812, image 0452adb) rolled live after clearing two infra faults:
  - bp-cnpg webhook-gate Job flux-managed denial — fixed via #4226/PR #4227 (bp-cnpg 1.0.12).
  - CNPG webhook hijack by a rogue per-Org operator — recovered (scale-0 + webhook recreate + helm-controller restart).
- Pool env live: CATALYST_POOL_POWERDNS_API_URL=https://pdns.openova.io, reflected key 48 bytes,
  log "org-tenant: powerdns writer wired (pool-domain free-subdomain writes target central pdns)".

## Walk
- Fresh stranger, slug `f4179done` on `.omani.works`, M plan, WordPress, single-region.
- PIN 185299 from rtz Valkey; voucher F4179DONEVCH2026 → 0 OMR due → Launch.
- POST /api/tenant/orgs → 201, request carried parent_domain=omani.works.
- Org CR f4179done Ready (vCluster+Keycloak+Gitea); redirect host = console.f4179done.omani.works (CORRECT).

## Close gate FAILED
- console.f4179done.omani.works → DNS_PROBE_FINISHED_NXDOMAIN (evidence/4179-close-gate-nxdomain.png).
- Central pdns omani.works zone: 0 f4179done rrsets (other orgs present — publish path healthy).
- Root cause #4236: the marketplace funnel (tenant-service → provisioning-service) never invokes the
  catalyst-api org-tenant pipeline where #4222's PoolWriter lives, so the pool A-record is never written.
