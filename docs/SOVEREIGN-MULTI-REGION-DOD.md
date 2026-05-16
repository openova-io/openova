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

## DoD gates (D0–D31)

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
| **D19** | **Apps + Cloud counter consistency.** Apps page Deployments tab count MUST equal Catalog "INSTALLED" count MUST equal `kubectl get hr -A` Ready count. Cloud canvas kind chips MUST NOT show `N/0` for resources that exist (vCluster, LoadBalancer, Bucket, Volume, PVC). PVC count in graph view MUST equal PVC count in list view. App card hrefs MUST NOT have doubled prefix (`/app/bp-bp-*` was observed on 10/44 cards on t129). Caught on t129 2026-05-16. | Playwright MCP |
| **D20** | **Jobs page surfaces all-region jobs with region filter.** Jobs view MUST show per-region prefixes (`nbg1-1:`, `sin-2:`) for every app on a multi-region Sovereign, plus an App-filter that lets the operator narrow to a single region. The unexplained `56/58` counter that appears on t129 MUST resolve to an actionable filter or be removed. Caught on t129 2026-05-16. | Playwright MCP |
| **D21** | **Operator pre-populated as owner-tier on /users post-handover.** Sovereign Console /users MUST list the operator who completed PIN-login as `tier=owner` with their email. Today /users renders empty on a freshly-converged Sovereign. Caught on t129 2026-05-16. | Playwright MCP |
| **D22** | **Settings page shows real values.** /settings MUST render real values for Region, Capacity, ControlPlaneSize, Created (timestamp), DeploymentID, Pool subdomain — NOT `—` placeholders or "API PENDING" badges. Operator MUST be able to see what their Sovereign actually is. Caught on t129 2026-05-16: every field empty + multiple API-PENDING badges. | Playwright MCP |
| **D23** | **Sovereign-side /wizard route does not collide with post-handover landing.** After PIN-login + handover, operator's browser MUST land on `/dashboard` (or the canonical post-handover surface), NEVER on `/wizard` (which is the mothership new-prov flow). Caught on t129 2026-05-16: PIN-verify routes to `/wizard` instead of `/dashboard`. | Playwright MCP |
| **D24** | **Mothership-only views absent from Sovereign Console.** The Sovereign Console MUST NOT expose: `/app/dashboard` (mothership fleet view — `7 Sovereigns` with fabricated duplicates rendered on t129), `/app/settings` (mothership settings), `+ New deployment` button. The Sovereign Console is for ONE Sovereign; the mothership fleet view is somebody else's UI. Caught on t129 2026-05-16. | Playwright MCP |
| **D25** | **All operator-facing service hostnames reachable + correctly wired.** `keycloak.<fqdn>` / `openbao.<fqdn>` / `openova-flow.<fqdn>` / `prometheus.<fqdn>` / `mimir.<fqdn>` / `loki.<fqdn>` / `tempo.<fqdn>` / `argo.<fqdn>` / `workspaces.<fqdn>` MUST return non-zero HTTP. `harbor.<fqdn>` / `registry.<fqdn>` / `guacamole.<fqdn>` / `marketplace.<fqdn>` MUST return their app page, not 404. No service config may carry a dev hostname (e.g. `gitea.catalyst.local`) in production HTML. Caught on t129 2026-05-16: 9 hostnames returned HTTP 000, 4 returned 404, gitea page showed dev hostname. | curl + Playwright MCP |
| **D26** | **CSP allows fonts or self-hosts woff2.** Operator MUST NOT see system-font fallback on Sovereign Console pages. Either fonts.googleapis.com is allowed by CSP, or fonts are self-hosted (no external dependency). Caught on t129 2026-05-16. | Playwright MCP |
| **D27** | **Marketplace enabled on the Sovereign.** `MARKETPLACE_ENABLED=true` flows from provision body → bp-catalyst-platform → Sovereign Console: a `/marketplace` route returns 200 with a non-empty catalog page (apps + voucher admin) — NOT 404, NOT a "marketplace disabled" stub. `kubectl get hr -A` shows `bp-marketplace` (or whichever HR backs the marketplace) Ready=True on the chroot. The mothership provision wizard MUST default `marketplace.enabled=true` (zero-touch — operator never toggles a flag). Added 2026-05-16 (founder ruling: tenant org flow is part of the Sovereign DoD). | Playwright MCP + kubectl |
| **D28** | **Voucher issuance from owner-tier UI.** Owner (the operator who PIN-logged-in per D21) opens Sovereign Console marketplace admin → issues a voucher for tenant onboarding. Voucher artifact MUST persist (CR + DB row), MUST be emailed to the chosen recipient via the Sovereign's outbound SMTP, AND MUST be visible in the admin's voucher list. Issuance must be one-click (no kubectl, no API call). Test recipient: `demo@openova.io` or `support@openova.io`. Added 2026-05-16. | Playwright MCP |
| **D29** | **Voucher-based organization (tenant) provisioning is zero-touch.** Recipient opens the voucher email → clicks redeem link → PIN-login as `demo@openova.io` (or `support@openova.io`) → lands on an organization-creation wizard → completes the form → a new `Organization`/`Tenant` CR is created → tenant namespace + RBAC + bootstrap apps converge → recipient is auto-redirected to their tenant home page. NO operator intervention beyond the voucher email. Added 2026-05-16. | Playwright MCP |
| **D30** | **Free-subdomain selection from operator-curated pool.** Organization wizard step MUST present a subdomain picker populated from the configured pool: `omani.homes`, `omani.rest`, `omani.trades` (and any others the operator has provisioned). Tenant chooses a free subdomain (e.g., `acme.omani.homes`) → cert provisions → tenant landing page resolves on the chosen FQDN with publicly-trusted TLS. The pool MUST come from a Sovereign-side CR/config (not hardcoded). Added 2026-05-16. | Playwright MCP + dig + curl |
| **D31** | **Tenant application with CNPG active-hot-standby replication.** Inside the new tenant, user picks a CNPG-backed app from the marketplace (e.g., Ghost or WordPress) → selects "active hot-standby" → app installs with a CNPG Cluster that replicates across the Sovereign's regions (primary + at least one replica). `kubectl get cluster.postgresql.cnpg.io -A` in the tenant context shows `instances` distributed across regions (region label / topology spread). Failover test: cordoning the primary region brings the replica to primary, app remains reachable on its FQDN within the documented RTO. Added 2026-05-16. | Playwright MCP + kubectl + curl |

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
