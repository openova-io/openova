# Sovereign Multi-Region Definition of Done

**This file is the convergence contract.** Every wipe → create → test cycle must validate every gate below before claiming a Sovereign is converged. Founder ruling 2026-05-15: silent compromise from these gates is a quality violation.

Mirrored to auto-memory at `~/.claude/projects/-home-openova-repos-openova-private/memory/sovereign_multiregion_dod.md`.

---

## Architecture invariants (never compromise)

| ID | Rule |
|---|---|
| A1 | **3 regions minimum.** If a provider has capacity/zone constraints, swap regions — never silently drop to 2. |
| A2 | **Inter-region link = DMZ WireGuard over PUBLIC IPs.** No `hcloud_network` cross-region, no VPC peering, no Huawei VPC — provider-agnostic, always over the DMZ WG endpoint. |
| A3 | **Cilium ClusterMesh apiserver Service = LoadBalancer** (public IP through DMZ WG). The word `NodePort` must never appear in clustermesh-apiserver Service spec on any Sovereign. |
| A4 | **vCluster topology:** primary region = MGMT+DMZ; each secondary region = DMZ+RTZ. Cross-vCluster intra-region traffic stays inside host k3s via Cilium. |
| A5 | **Zero public exposure of K8s control-plane endpoints.** `kubectl get svc -A` on a converged Sovereign returns no NodePort for clustermesh-apiserver, kube-apiserver, or etcd. |
| A6 | **Provider-mix is the canonical case.** Assume 1 region Hetzner, 1 AWS, 1 Huawei. Code must work for that even when the active test prov is all-Hetzner. |

---

## DoD gates (D0–D14)

Every gate must pass on a SINGLE fresh provision in one continuous run. No partial credit.

**D0 sits ABOVE D1.** Without successful handover + auto-redirect, the operator never sees that provisioning succeeded — every other gate becomes invisible from their perspective. The zero-touch contract is end-to-end OPERATOR experience, not just backend convergence.

| # | Gate | Verifier |
|---|---|---|
| **D0** | **Successful handover + auto-redirect to Sovereign Console.** Once `deployment.status=ready` AND `deployment.handoverFiredAt != null`, the mothership UI auto-routes operator's browser to `deployment.handoverURL` (`/auth/handover?token=<jwt>` on the Sovereign Console). Synthetic `Apps` / `Handover` per-region stage rows MUST be marked Succeeded (or not-applicable), never stuck Pending after handover fires. **No operator action required** — they should land on the Sovereign Console without copying/typing the FQDN. Cost-on-failure: operator has no idea their Sovereign is ready, sees synthetic stages stuck Pending on mothership, gives up. | Playwright MCP |
| D1 | `dig console.<fqdn> @1.1.1.1` returns primary LB IP (auto-written by catalyst-api after Phase-0) | dig |
| D2 | `curl https://console.<fqdn>/` → 200, cert publicly trusted (verify=0) | curl |
| D3 | PIN-login: enter email → receive PIN via IMAP → enter PIN → dashboard | Playwright MCP |
| D4 | Keycloak SSO: PIN-login bounces through Keycloak once, lands on `/dashboard` with session cookie | Playwright MCP |
| D5 | `/cloud` view: renders **all 3 regions**, no stuck spinners | Playwright MCP |
| D6 | `/jobs` view: **0 pending, 0 running** — every job in terminal state | Playwright MCP |
| D7 | Mothership flow Jobs ≡ child Sovereign Jobs (same IDs, same statuses) | Playwright MCP diff |
| D8 | `kubectl --context <child> get hr -A` shows **all 135 HRs Ready=True** across all clusters | kubectl |
| D9 | clustermesh-apiserver Pod Ready in every region, no restarts, no x509 errors | kubectl |
| D10 | `cilium clustermesh status` shows OK for every peer cluster | cilium CLI |
| D11 | Inter-region pod-to-pod packet test passes, hubble-flow shows WireGuard traversal | kubectl + hubble |
| D12 | `kubectl get svc -A \| grep clustermesh` shows only `LoadBalancer` (no NodePort) | kubectl |
| D13 | Canvas flow page: sibling-deps edges render, no orphan bubbles, no phantom pillars | Playwright MCP |
| D14 | Operator re-login after browser refresh works without re-PIN within session TTL | Playwright MCP |
| **D15** | **`/cloud?view=graph` canvas accurate per kind.** `vCluster N/N` non-zero (6/6 on a converged 3-region prov — 1 mgmt + 3 dmz + 2 rtz). `LoadBalancer N/N` non-zero (clustermesh-apiserver LBs + ingress LBs counted). `Cluster N/N` matches actual 3 regions. **No kind chip shows `0/0` for a resource that actually exists in the cluster.** Caught on t129 2026-05-16: vCluster Pods + Service Running but canvas adapter rendered 0/0. | Playwright MCP |
| **D16** | **`/dashboard` Layer-1 / Layer-2 grouping renders multi-region.** Selecting Layer-1=Cluster on `/dashboard` MUST emit 3 cluster-grouped bubbles (one per region), not a single Sovereign. Layer-2=Namespace MUST emit namespace bubbles WITHIN each cluster bubble. The hierarchy `Cloud → Region → Cluster → vCluster → Namespace → Application` must collapse correctly per the operator's Layer-1/Layer-2 selection. Caught on t129 2026-05-16: Layer-1=Cluster still showed only 1 Sovereign. | Playwright MCP |
| **D17** | **Application detail route `/app/<name>` shows the application, not "deployment id malformed".** Clicking any application card in `/apps` MUST navigate to a route where the URL segment is the application name (e.g. `bp-cnpg`) and the renderer treats it as an application reference, NOT as a deployment id. The notifications drawer MUST NOT contain "Deployment id in the URL is malformed" entries for valid app-name segments. Caught on t129 2026-05-16: clicking on Alloy/Catalyst-Platform/bp-cnpg etc. all generated malformed-id errors. | Playwright MCP |
| **D18** | **Sovereign-side catalyst-api can self-monitor Phase-1 install state.** The chroot Sovereign catalyst-api MUST be able to fetch its own cluster's kubeconfig (or use in-cluster service account) to observe HelmRelease state. The notifications drawer MUST NOT contain "Per-component install monitoring is unavailable for this deployment — the Catalyst API couldn't fetch the new cluster's kubeconfig" entries. Operator should never need to drop to `kubectl get helmrelease` to know per-app install state. Caught on t129 2026-05-16. | Playwright MCP |

> **DoD grows.** Every iteration of test-writer/test-executor finds more operator-visible bugs. Append the gate, ship the fix, re-validate. The list is the convergence contract; do not declare convergence until every appended gate passes on a single fresh prov.

---

## Trigger phrases that mean STOP — about to compromise

- "for now let's just do 2 regions"
- "the matrix expects X — let me synth X into the response"
- "Hetzner private net spans zones, let me use that for cross-region"
- "ClusterMesh on NodePort is fine for testing"
- "ash/sin is different zone, let me just stay in eu-central"
- "private NIC link between regions is faster"

If any of these appear in your reasoning → STOP, re-read this file, fix the root cause.

---

## Cycle protocol

Before any `tofu apply` or `POST /api/v1/deployments`:

1. Read this file (or the memory mirror).
2. Log the D1–D14 list to the loop output.
3. Refuse to mark convergence until each D1–D14 has been individually checked.
