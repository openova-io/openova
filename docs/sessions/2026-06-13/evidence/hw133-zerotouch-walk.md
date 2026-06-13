# hw133.omani.works — fresh-prov ZERO-TOUCH validation walk (2026-06-13)

**Deployment** `40c4e17667b600eb` · 2-region (me-east-215-a / -b) · `SHARED_PG=true` · fired with the #3409 powerdns fix.
**Convergence:** zero-touch to `status: ready` (no hand-patch, no manual unwedge). Console `200`, 49/49 deployments INSTALLED.
**Login:** none — a signed `/auth/handover` token lands directly in the console as `emrah.baysal@openova.io`, role `sovereign-admin`.

This walk proves the founder mandate: *"newly created environments follow the same approach seamlessly zero-touch."* It also closes the loop on the regression this session found + fixed (#3405 → #3409): hw131/hw132 wedged on powerdns; hw133 with the fix converged.

| # | Go to URL | Action | You should see | Result |
|---|---|---|---|---|
| 1 | `https://console.hw133.omani.works/auth/handover?token=<signed>` | paste a freshly-minted handover JWT | redirect `302 → /dashboard`, **no login form** | ✅ landed `/dashboard`, title "OpenOva Corporate", avatar **E** (emrah), sidebar footer "Tenant hw133.omani.works" — [01](hw133-walk/hw133-01-dashboard-signed-in-admin.png) |
| 2 | (dashboard) | read the resource treemap | cluster `hw-me-east-215-a-rtz-prod`, **powerdns** tile present (the fix held), keycloak/cnpg-pair/mgmt+dmz+rtz vclusters, "96 items" | ✅ powerdns 1%, keycloak 10%, all 3 vclusters rendered — [01](hw133-walk/hw133-01-dashboard-signed-in-admin.png) |
| 3 | `/apps` | view Deployments tab | header **"✓ Sovereign ready — hw133.omani.works"**, **49 deployments INSTALLED** incl. PowerDNS, Keycloak, CNPG, Harbor, all vclusters | ✅ 49 Deployments / 63 Catalog, every tile INSTALLED — [02](hw133-walk/hw133-02-apps-49-installed-ready.png) |
| 4 | `/organizations` | view the single Organizations menu | one menu (BSS+OSS merged); **parent org `hw133.omani.works` as first row** (internal / corporate / showback / vcluster / Active); showback table day-one | ✅ parent-first-citizen row + per-app showback (13578 units, 13229m CPU) + Commerce/Billing/Domains sub-nav — [03](hw133-walk/hw133-03-organizations-parent-showback.png) |
| 5 | `/organizations` showback | find the shared-PG instances (#3370) | **three** shared instances `shared-pg`, `shared-pg-b`, `shared-pg-c` in `shared-data` ns | ✅ all three present (SHARED_PG=true reproduced zero-touch) — [03](hw133-walk/hw133-03-organizations-parent-showback.png) |
| 6 | `https://grafana.hw133.omani.works/` | type the bare URL (#3374 SSO) | lands on the Grafana home **signed-in**, no login form | ✅ "Home - Dashboards - Grafana", "Welcome to Grafana", Profile button — zero-click — [04](hw133-walk/hw133-04-grafana-zero-click-signed-in.png) |

## What this validates (zero-touch, on a fresh env)
- **Fresh-prov convergence** — `status: ready`, 49/49 installed, no manual intervention. The #3405→#3409 regression is fixed (powerdns up).
- **#3374 SSO** — console + grafana zero-click signed-in as admin; no login UI anywhere (north star 3).
- **#3378 ORGANIZATIONS** — one merged menu, parent-org first-citizen, showback day-one.
- **#3370 shared-PG** — the three shared-PG instances reproduce (north star 2 substrate).
- **vCluster containment (north star 1)** — mgmt/dmz/rtz vclusters installed; SME apps run in the `sme` namespace.
- **Multi-region (north star 4) substrate** — 2 regions materialized, cnpg-pair primary present; the live region-kill DR walk (cross-VPC promotion) is the remaining deeper walk, now unblocked by #3307 VPC peering at boot.

## Remaining deeper walks (not blockers to the zero-touch proof)
- **#3376 FUNNEL** — voucher → tenant Org → signed-in (the SME stack is up in the `sme` ns; the end-to-end redeem walk is next).
- **#3375 region-kill** — continuum-driven cross-region promotion (substrate present; the kill+promote walk is next).
- **#3379 cutover** — post-handover 8-tether pivot + 10-min egress hold.
