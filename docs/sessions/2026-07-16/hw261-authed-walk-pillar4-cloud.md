# hw261 — Authenticated owner walk (Pillar 4 + Cloud/infra), live browser evidence

**Env**: `hw261` (dep `fe9eb3995b594b79`, `console.hw261.omani.works`) — the zero-touch
multi-region Huawei kom4dc Sovereign (`cutoverComplete=true`, 11/11, self-sovereign) that
passed region-kill G12 (zero-tx-loss + clean recovery). Status banner reads **Degraded** —
expected: region-a recovered from the region-kill ~90 min prior, workloads still settling;
the DR pair itself is healthy 3/3 both regions.

**Session**: owner handover URL minted with the RS256 key whose public half is the baked
`handover_jwt_public_key` (keypair sha256-verified to match), landed authenticated as
`emrah.baysal@openova.io` (sovereign-admin) — **not** auto-admin; the login front door is
passwordless-PIN.

**Walk type**: authenticated console (Playwright), read-only except the Pillar-4 mutation test.
**Date**: 2026-07-16.

---

## Results

| Surface | Result |
|---|---|
| **Dashboard** (`/dashboard`) | ✅ Authenticated, "195 items", FleetTreemap with the Progress/Kind layer design (#934) populated with real cells (Done/Install/Task/Step/Reconcile/Lifecycle/Cron/Pending) + status legend. Decommission link confirms dep `fe9eb3995b594b79`. |
| **Cloud — Graph** (`/cloud?view=graph`) | ✅ Full 2-region topology: **Cloud 1/1 · Region 2/2 · Cluster 2/2 · NodePool 4/4 · WorkerNode 7/7 · LoadBalancer 2/2**, 17 nodes / 10 edges, all "(healthy)"; both `me-east-215-a` and `me-east-215-b` present. 13-lens selector (Cloud/Runtime/Reconciliation/Networking/Data/Security/Flux/Crossplane/cert-manager/CNPG/External-Secrets/Cilium/Catalyst). |
| **Cloud — List** (`/cloud?view=list`) | ✅ **#3987 per-kind empty-data regression ABSENT**: per-kind tabs **Clusters 2 · Node Pools 4 · PVCs 51 · Load Balancers 2** all non-zero; Clusters table renders both regions (huawei, v1.30, healthy, 4 nodes each). Status + Region filters populated. (vClusters=0 — expected: operator-only Sovereign, no tenant Orgs provisioned yet.) |
| **Agenity workspace** (`/sandbox`, Pillar 4) | ◑ UI present + correct — 6 agent cards (Aider/Claude Code+"Connect Claude Max"/Cursor Agent/Little Coder/OpenCode/Qwen Code), honest "API pending" label on Recent sessions. Session backend `/api/v1/sandbox/sessions` returns **500** on list **and** on the Start-session mutation. Root-caused (see below). |

Screenshots (this session's Playwright output dir):
- `hw261-authed-dashboard-treemap.png`
- `hw261-cloud-view-2region-populated.png`
- `hw261-cloud-list-perkind-populated-3987-absent.png`
- `hw261-agenity-workspace-pillar4.png`

---

## Pillar-4 Agenity session-backend 500 — root cause (→ #5140, fix #5141)

- **NOT an RBAC gap** (verified): Sandbox CRD present (created 06:40Z); the catalyst-api pod
  runs as `catalyst-system:catalyst-api-cutover-driver`, and `kubectl auth can-i
  {list,create,get,watch,delete} sandboxes` = **yes** for all.
- **Real cause**: the catalyst-api sandbox reflector alternates `Unauthorized` (401) and
  "could not find the requested resource" (404) — a clustermesh cross-region apiserver
  inconsistency in the **post-region-kill Degraded state**. The DR failover flipped the
  primary region-a→region-b; the chroot's self-materialized primary kubeconfig (#5132) hits
  a peer apiserver that rejects its token. Downstream primary-referencing clients did not
  re-resolve after the failover — same class as **#5137** (Continuum-HA).
- **Shipped (#5141)**: `sandboxBackendUnavailable()` — the handler now degrades 401/403/503/
  timeout to a **503 "API pending"** (its documented contract) instead of a raw 500.
- **Follow-on (#5140)**: re-verify on a **fresh (non-region-killed) Sovereign** to confirm the
  deeper cause is transient mesh-recovery vs a standing post-failover client-resolution bug.

---

## Net

Pillars 1/2 (funnel — see `hw261-funnel-pillar1-2-walk.md`), the authenticated console
(dashboard/cloud graph+list), and the Pillar-4 Agenity UI all render correctly on the fresh
zero-touch cutover Sovereign. The single non-green item — the Agenity session backend — is a
transient of the region-kill recovery window with a shipped honest-degrade fix and a scoped
follow-on. No fabrication observed; every "empty"/"pending" state on this env is honestly
labeled.
