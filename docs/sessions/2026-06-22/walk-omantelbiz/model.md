# model epic — live walk on omantel.biz (dep `4635277cae4ffed9`) — 2026-06-22

Env: PERMANENT omantel.biz Sovereign, me-east-215-a/-b (2-region Huawei). Walked READ-ONLY via Playwright (console.omantel.biz, warm owner session) + mothership-kubectl against both region kubeconfigs.

Front doors: console 200, marketplace 200, api/healthz 200, api/readyz 200, console.demo.omani.homes 200.

## Live cluster facts (mothership-kubectl)
- Region-A: 6 nodes Ready, 64/76 HR Ready (9 False). Region-B: 6 nodes Ready, 51 HR Ready.
- Organizations (orgs.openova.io): `omantel-biz` (internal/org/showback). Console directory ALSO renders a `Demo Org` (customer/org/real/vcluster/active, owner demo@openova.io) projected from the funnel — its CR backing (`vc-demo`, `bp-keycloak`) is still converging.
- Applications (apps.openova.io): `agenity` (omantel-biz, bp-agenity@0.1.0, Degraded), `shared-pg` / `shared-pg-b` / `shared-pg-c` (bp-postgres@0.2.5, active-hot-standby, Ready).
- vClusters: dmz / mgmt / rtz / omantel-biz all 1/1 Ready.
- CNPG: 11 clusters; the 3 shared-pg multi-region instances all 3/3 healthy (North Star #2).

## Per-row evidence
- **Row 1** PASS — bare `https://console.omantel.biz/` 302->`/dashboard`, signed-in, no login form (`evidence/model-dashboard.png`). (NOTE: on a *cold/expired* session the console shows the passwordless PIN login at `/login?next=/dashboard`; a persisted owner session lands zero-click. During the walk the sovereign-side catalyst-api rolled once and the PIN-issue upstream returned a transient 503 "no healthy upstream" for ~2min, then recovered.)
- **Row 2** PASS — full sidebar renders exactly: Dashboard / Cloud / Apps / Catalog / Sandbox / Jobs / Compliance / Users / Organizations / Settings (`evidence/model-dashboard.png`).
- **Row 4** PASS — app-detail tab strip generality proven on the `agenity` Application page: canonical strip Overview / Topology / Resources / Compliance / Logs / Settings / Members / Endpoints / Jobs / Dependencies (`evidence/model-app-agenity-tabstrip.png`).
- **Row 5/6** PASS — Organizations directory shows the customer `Demo Org` row: KIND=customer, TIER=org, BILLING=real, ISOLATION=vcluster, STATUS=Active (`evidence/model-organizations.png`).
- **Row 7** PASS — Org detail `/organizations/demo` renders canonical fields: slug `demo` (NOT `sme-<uuid>`), kind customer, tier org, billing real, isolation vcluster, status active, owner demo@openova.io, Console `console.demo.omani.homes` (`evidence/model-org-demo-detail.png`).
- **Row 8** PASS — Create-organization link `/organizations/new` present + reachable (Create organization button on directory).
- **Row 9/10/11/12** WARN — directory + detail consistently report Demo Org Active + vcluster isolation; BUT the real vcluster backing (`vc-demo`) is InstallFailed / `bp-keycloak-0` ImagePullBackOff (peer-agent-owned). UI is green but the backing is not yet live -> "active" is projector-asserted, not convergence-proven. Re-walk when vc-demo converges.
- **Row 13** PASS — `/catalog/bp-postgres` detail renders with **+ New instance** button + multi-instance/shareable badge + Instances table (shared-pg/-b/-c, active-hot-standby, Ready) (`evidence/model-bp-postgres-detail.png`).
- **Row 14/24** PASS — shared-PG reuse model LIVE: bp-postgres Instances table lists the 3 shared instances; the shareable blueprint surface renders.
- **Row 15/19** PASS — Apps list renders one card per Application (49 apps), each carrying a Platform/BOOTSTRAP badge + bundled-dependency chips + status (INSTALLED/INSTALLING/PENDING/FAILED) — NOT one card per HelmRelease/pod (`evidence/model-apps.png`).
- **Row 16** PASS(render) — app page Topology tab present in the canonical strip (agenity page). Live save-persist not exercised (read-only walk).
- **Row 17** PASS — Dashboard treemap Layer-1 default = **Organization**, Layer-2 = Application; drillable layer pickers (Sovereign/Region/Cluster/vCluster/Family/Namespace) (`evidence/model-dashboard.png`).
- **Row 18** PASS — treemap cells scanned: NO ephemeral Job-pod cell (`cutover-*`, `*-snapshot-save-*`, `scan-*`) appears; cells are app/family-level.
- **Row 20/21/25** WARN — treemap + showback are platform-correct (a single "Platform overhead" roll-up cell present); customer estate distinctness depends on the Demo Org vcluster fully converging.
- **Row 22** PASS — Showback shows a single visually-distinct **Platform overhead** roll-up cell on the treemap holding control-plane workloads (`evidence/model-dashboard.png`).
- **Row 23** FAIL — only one customer Org (Demo) exists; no 2nd Org running an app -> cannot assert a second attributed showback row.
