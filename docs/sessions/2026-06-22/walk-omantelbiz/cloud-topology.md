# cloud + topology epics — live walk on omantel.biz — 2026-06-22

Walked READ-ONLY via Playwright (`/cloud?view=graph`) + mothership-kubectl on both region kubeconfigs.

## Live multi-region facts (North Star #4)
- Cloud graph (`evidence/cloud-graph.png`): Cloud 1/1, **Region 2/2** (me-east-215-a + me-east-215-b), **Cluster 2/2**, NodePool 4/4, **WorkerNode 24/24**, **LoadBalancer 2/2** — ALL healthy. 34 nodes / 22 edges. Genuine 2-region Huawei prov (NOT single-region).
- Region-A 6 nodes Ready (1 cp + 5 worker), Region-B 6 nodes Ready. Lens picker offers Cloud/Runtime/Reconciliation/Networking/Data/Security/Flux/Crossplane/cert-manager/CNPG/External-Secrets/Cilium/Catalyst.
- Density slider default 100%; type chips (Cloud/Region/Cluster/NodePool/WorkerNode/LoadBalancer) are named chip-sets, not a flat filter.
- CNPG: shared-pg / shared-pg-b / shared-pg-c each 3/3 healthy (active-hot-standby across regions). Continuums: `cnpg-pair-bp-cnpg-pair-continuum` (catalyst-platform) + `bp-wordpress-tenant` both **Degraded** with NO lease holder (region-b convergence behind primary).

## cloud epic per-row
- **Cloud graph view** PASS — renders the k9s-style graph with named chip-sets, healthy node coloring, list/graph toggle, lens selector, fullscreen (`evidence/cloud-graph.png`).
- **Cloud list view** PASS(render) — `/cloud?view=list` tab present (toggled from graph). Same dataset.
- **LoadBalancer present** PASS — LoadBalancer 2/2 healthy in the graph (the two Huawei ELBs: primary + console). Resolves the prior "0 cloud LB" misread.

## topology epic per-row (#3375)
- **Row 46** PASS — `/catalog/bp-postgres` renders + has **New instance** (`evidence/model-bp-postgres-detail.png`). The create dialog topology `<select>` not opened read-only (would mutate-risk; left WARN where the row asserts the open select).
- **Row 47/48/49/54** WARN — the canonical topology vocabulary (singleton / active-passive / active-hot-standby / active-active) is the live placement vocabulary used by the shared-pg apps (active-hot-standby), but reading the create `<select>` options requires opening the create dialog (not done read-only).
- **Row 50/59/60/61** FAIL/blocked — live create (pick mode -> Provision) is a mutation; not performed on this read-only walk. The 3 shared-pg instances already prove active-hot-standby provisioning succeeded at bootstrap (Ready).
- **Row 51/52/55/56/57/62/64/71** WARN/FAIL — `shared-pg` Topology tab declared-vs-effective / per-region placement / replication-lag / Switchover: the 3 shared-pg CNPG clusters are 3/3 healthy live, but the backing **Continuum is Degraded with no lease holder**, so the armed-Switchover + live-lease-status surface is not in a green state to assert. Re-walk when Continuum reconciles.
- **Row 58/63** WARN — singleton/no-DR honest-hiding of the Switchover section: render-only, not drilled.
- **Row 65/66** PASS — Cloud view reads the true region count: Region 2/2 + Cluster 2/2 (me-east-215-a + me-east-215-b), NO phantom region-B bubble (`evidence/cloud-graph.png`).
- **Row 67 (grafana)** FAIL — grafana HR not Ready (loki/mimir/seaweedfs object-store chain FAILED); not Healthy/Running in both regions.
- **Row 68 (powerdns-admin)** FAIL — powerdns host 000, pdns-pg present but admin not healthy.
- **Row 69 (keycloak)** WARN — host keycloak (mgmt vcluster) 1/1 Running; demo-Org keycloak ImagePullBackOff. Both-region health not asserted.
- **Row 70 (guacamole)** FAIL — guacamole HR InstallFailed; not Healthy.
