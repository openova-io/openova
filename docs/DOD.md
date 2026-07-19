# Definition of Done

> **What this is**: the canonical end-user Definition of Done for OpenOva — the
> deterministic 2-phase test, the 5 inseparable pillars, all DoD gates (D0–D35),
> the domains canon (which domains, when, why), and the operator / customer /
> tenant persona journeys.
>
> **Authority**: 📐 PERMANENT canon. Reviewed PRs only. The generic
> cross-project DoD principle (operator-walks-a-fresh-prov, no theater) lives in
> user-global `~/.claude/CLAUDE.md` §2. This file is the OpenOva-specific
> elaboration.
>
> **Pointers**: see [`PRINCIPLES.md`](PRINCIPLES.md) for engineering rules,
> [`ARCHITECTURE.md`](ARCHITECTURE.md) for system shape,
> [`RUNBOOKS.md`](RUNBOOKS.md) for operator how-tos.
>
> **Status**: Authoritative. **Updated**: 2026-05-20.
> Supersedes the legacy split 5-PILLAR-DOD + DOMAINS-CANON +
> SOVEREIGN-MULTI-REGION-DOD + PERSONAS-AND-JOURNEYS —
> consolidated here per the lean-doc strategy (PR #2076 / PR #2084).
> Pillar 4 is delivered through the per-Org **Agenity** workspace
> (`products/agenity/`) plus the **`bp-openova-mcp`** server
> (`products/openova-mcp/`); the legacy Sandbox concept + menu are **dead** and
> **removed** (founder, 2026-06-30). MCP auth is the per-Org `agenity-mcp-bearer`
> — an Org-scoped RS256 session bearer minted into OpenBao at
> `secret/catalyst/agenity/<slug>/mcp-bearer`, SSO/RBAC-scoped to the
> Organization — not the old sandbox-pty-server stdio trust boundary. See
> §1 Pillar 4 below.

---

## §0 — True DoD: `deployment.status=ready` is NOT proof (G18 / #2561)

**Refs: #2561 (G18, 2026-05-28).** The single most-common pattern of fake
"done" claims in this repo has been agents treating catalyst-api's
`deployment.status=ready` flag as proof of convergence. **It is not.** The
flag is a one-shot snapshot set by the Phase-1 watcher when N HelmReleases
reach Ready=True — and then **never re-evaluated**. After that point HRs
can and do flip back to False (Flux upgrade cycles, Kyverno admission
denials, runtime OOM crashloops, manual surgical patches). The Console
"Jobs" tab continues to show the live stuck reality; the deployment record
keeps lying.

### The contract

Claiming a Sovereign "ready" without a green run of
[`scripts/sovereign-dod-verify.sh`](../scripts/sovereign-dod-verify.sh)
attached to the issue is **illegal output**. The verifier re-evaluates LIVE
STATE across six layers at the moment of the check:

| Layer | What it checks |
|---|---|
| L1 | Deployment record: status, handover, CP IP, LB IP |
| L2 | Both regions' kubeconfigs reachable; ≥ 4 nodes Ready per region |
| L3 | Every required HR has `status.conditions[type=Ready].status=True` **now**; zero False, zero Unknown (excluding Hetzner-suspended) |
| L4 | Every catalyst-system control-plane pod is Ready (api, ui, catalog, 4 controllers, marketplace-api) |
| L5 | HTTPS responds with 2xx/3xx/40x on console / api / auth / gitea / harbor / bao / marketplace / pdns; TLS cert is from REAL Let's Encrypt (not staging) |
| L6 | **Zero-touch audit**: no `kubectl-set`, `kubectl-patch`, `kubectl-edit` managers on tracked resources; no `kubectl.kubernetes.io/restartedAt` annotations on any tracked Deployment/DaemonSet/StatefulSet (catches `kubectl rollout restart` surgery). |

L6 is the **load-bearing** check. Without it the verifier is gameable.
Operators (and agents) who restart a DaemonSet or `kubectl set image` a
deployment to "fix" a stuck HR have NOT achieved zero-touch — the
verifier fails loudly and shows exactly which resource was touched.

### How to run

```bash
./scripts/sovereign-dod-verify.sh <deployment-id>
```

- Exits 0 only on full pass (≈ 85 checks). Paste full stdout into the issue
  comment to satisfy the contract.
- Exits 1 on any failure. Output names every failing check; do not claim
  ready.
- Exits 2 on usage/setup error.

### What this replaces

Every prior "phase1Outcome=ready ✅" claim in agent reports. The flag is
still useful as a hint ("Phase 1 reached its synchronous high-water mark")
but it is not the contract.

### Adjacent: `status/completed` label gating (future G19)

A GitHub workflow (filed as follow-up G19) will refuse to apply
`status/completed` on any issue whose comments do not contain the
verifier's full output ending in `✅ TRUE READY — all N checks pass.`. Until
that ships, the discipline is contractual: agents and operators MUST run
the verifier and paste its output.

---

## §1 — The 5 inseparable pillars

Every dispatch in this repo must answer:

> Which of the 5 pillars does this work move forward, and which deterministic
> step (Phase 0 / 1 / 2) does it advance?

If the answer is "none," **the work is wrong — pick differently.**

The 5 pillars are **inseparable** — none alone is a viable platform. Pillar
work is **strictly primary**; operator-console polish, cosmetic-guard
re-enables, treemap drill-down quality, jobs region filter, admin sidebar nav
are tertiary operator-debugger surfaces and must never displace pillar work.

| # | Pillar | What "shipped" looks like |
|---|---|---|
| **1** | **Marketplace + voucher onboarding** | Anonymous visitor reaches the operator-branded marketplace → picks the canonical Postgres-backed bundle → completes signup (email + 6-digit PIN magic-link) → Organization CR created. |
| **2** | **Multi-region BCP topology choice at signup** | Wizard exposes region / topology choice during signup; customer picks N regions; system provisions across all N in one pass. Not a Day-2 upgrade. |
| **3** | **Two independent CNPG clusters with ReplicaCluster sync + region-kill failover** | One CNPG cluster per region; synchronous `ReplicaCluster` replication over Cilium ClusterMesh on the DMZ WireGuard-over-public-IPs data plane; region-kill test passes with zero transactions lost. |
| **4** | **Per-Org Agenity workspace + SSO/RBAC-scoped OpenOva MCP with full org knowledge** | The customer launches their per-Org **Agenity** workspace (the Sandbox concept + menu are **removed/replaced**). The chepherd-based solo agent (Claude Opus, OAuth-authenticated from its init-seeded `~/.claude/.credentials.json`) attaches the **`bp-openova-mcp`** server (`dependsOn bp-agenity`) via the per-Org `agenity-mcp-bearer` (SSO/RBAC-scoped, minted into OpenBao). The agent sees every org resource (apps, vClusters, conn-strings via OpenBao, Gitea repos, IAM, region health), pastes zero credentials, answers prompts with full org context, and mutates resources via MCP tool calls (e.g. `create_application`). |
| **5** | **Sovereign independence post-cutover** | After `bp-self-sovereign-cutover` runs, zero egress to `harbor.openova.io`, `ghcr.io/openova-io`, or `github.com/openova-io` — proved by a 10-minute deny-egress NetworkPolicy hold (Principle #11). |

### Pillar 4 — Per-Org Agenity workspace + `bp-openova-mcp`

> **Terminology correction (founder, 2026-06-30):** the **Sandbox** concept is
> **dead — replaced entirely by Agenity.** The Sandbox menu/surface is to be
> **removed**. Any "Sandbox" mention elsewhere in this doc is historical /
> superseded; the live Pillar-4 surface is the per-Org **Agenity** workspace and
> `openova-sandbox-mcp` → **`bp-openova-mcp`**.

The customer installs the per-Org **Agenity** workspace (`products/agenity/`,
a single-region singleton Application Blueprint) into their Organization. The
StatefulSet boots the chepherd runtime headless and the chepherd-based **solo
agent** runs Claude **Opus** with `CHEPHERD_DEFAULT_MODEL` +
`CHEPHERD_EXTRA_MCP_JSON` (which carries the `openova` MCP-server stanza),
authenticating from its init-seeded `~/.claude/.credentials.json` (OAuth mode —
the operator's own OAuth credential, seeded from OpenBao). The
**`bp-openova-mcp`** server (`products/openova-mcp/`, `dependsOn bp-agenity`)
exposes the org-knowledge + mutation tool surface; it is a thin facade that
forwards the per-Org `agenity-mcp-bearer` to the live catalyst-api, so the
endpoint's own authz is the final word. That bearer is an Org-scoped RS256
session JWT minted into OpenBao at `secret/catalyst/agenity/<slug>/mcp-bearer`
(SSO/RBAC-scoped to that Organization), projected into the pod as
`OPENOVA_MCP_BEARER`. The agent sees every org resource (apps, vClusters,
conn-strings via OpenBao, Gitea repos, IAM, region health) and pastes zero
credentials.

Per Principle #1 (the waterfall is the contract) and Principle #2 (never
compromise from quality), Pillar 4 is **not** "ship a stub MCP server now and
wire real tools later." An Agenity workspace that boots without the full
OpenOva-MCP tool surface (read tools plus the `create_application` write path)
working end-to-end is Pillar 4 unshipped, regardless of how good the chrome
looks.

### Pillar 5 — `bp-self-sovereign-cutover` and the 8-tether pivot

A franchised Sovereign emerging from Phase 1 is operationally tethered to the
OpenOva mothership in **eight** places (full map in
[`adr/0002-post-handover-sovereignty-cutover.md`](adr/0002-post-handover-sovereignty-cutover.md)
§2.1 and in [`ARCHITECTURE.md`](ARCHITECTURE.md) §11.1):

| # | Tether | Phase |
|---|---|---|
| 1 | Flux `GitRepository.url = github.com/openova-io/openova` | P0 |
| 2 | containerd `registries.yaml` rewrites every upstream registry → `https://harbor.openova.io` | P0 |
| 3 | OCI `HelmRepository` urls = `oci://ghcr.io/openova-io` | P0 |
| 4 | `catalyst-api` env fallback to `https://github.com/openova-io/openova` | P0 |
| 5 | `flux-system/ghcr-pull` Secret seeded for private GHCR pulls | P0 |
| 6 | Crossplane provider packages from `xpkg.upbound.io` | P1 |
| 7 | Catalyst-authored images = `ghcr.io/openova-io/openova/*` | P0 |
| 8 | OS package mirrors during cloud-init (`apt`, `get.k3s.io`) | P2 (cold-start only) |

`bp-self-sovereign-cutover` installs **dormant** at bootstrap-kit slot 06a
during Phase 1 and is triggered post-handover by the operator's "Achieve True
Sovereignty" CTA. Eight sequential Jobs pivot the tethers in dependency order;
the **final step is a 10-minute deny-egress NetworkPolicy hold** against
`github.com`, `ghcr.io`, and `harbor.openova.io`. The only condition under
which `cutoverComplete=true` is set is that the cluster reconciles green
during this hold. **No cutover claim without the egress-block proof.** Full
choreography in §7 below.

---

## §2 — The deterministic test (Phase 0 / 1 / 2)

The test is **deterministic** — one fresh prov, one run, all phases pass in
order. No retries, no "works if you wait longer."

### Phase 0 — Operator issues voucher via BSS

Voucher operations live in the operator console's **BSS menu** (Business
Support System), **NOT in any `admin.<sovereign-fqdn>` subdomain**. The legacy
`admin.*` references in older docs / agents are outdated.

| Step | Action | URL / Outcome |
|---|---|---|
| 0a | Operator logs in to the Sovereign Console | `https://console.t<NN>.omani.works` |
| 0b | Navigate to the **BSS menu** | Sidebar → BSS (NOT `admin.<fqdn>/...`) |
| 0c | Issue voucher | Voucher artifact created + delivered to recipient via Sovereign outbound SMTP |

### Phase 1 — Customer redeems voucher (Postgres-backed app onboarding)

| Step | Action | URL / Outcome |
|---|---|---|
| 1a | Customer receives voucher email | Canonical URL pattern per `core/services/notification/templates/templates.go`: `https://marketplace.t<NN>.omani.works/redeem/?code=<CODE>` (**slash before `?` is mandatory**) |
| 1b | Customer redeems → checkout → picks the Postgres-backed bundle | Org provisions across the 2 chosen regions with **2 independent CNPG clusters** (ReplicaCluster sync over ClusterMesh on the WG-public-IP DMZ data plane) |
| 1c | Org URL after signup | `https://console.<orgslug>.omani.homes` (default pool TLD; pool also has `omani.rest` and `omani.trade` per `core/services/parent-domain/sovereign_parent_domains.go`) |

### Phase 2 — Customer launches Agenity; agent provisions an additional app via MCP

**This is the most important test.** It exercises Pillar 4 end-to-end and
proves that an agent acting on behalf of the tenant can mutate the
Organization's resources entirely through the OpenOva MCP — without the user
typing any credential.

| Step | Action | Outcome |
|---|---|---|
| 2a | Tenant logs in at `console.<orgslug>.omani.homes` | Dashboard renders |
| 2b | Opens **Agenity** | The Agenity workspace launches its chepherd-based solo agent on Claude **Opus** (OAuth-authenticated from the init-seeded `~/.claude/.credentials.json` — the operator's own OAuth credential, so **no Anthropic cost leak**) |
| 2c | The `openova` MCP server attaches | The `bp-openova-mcp` tool surface is available with zero user-typed config — RBAC-scoped to exactly what that User can do in the console (the tool set the agent sees == the User's console surface) |
| 2d | Customer prompts the agent to provision an **additional application** in their Organization | Agent calls the OpenOva MCP provision/auth/install tools (the write path is `create_application`, which forwards the per-Org bearer to the catalyst-api `POST /api/v1/sovereigns/{id}/applications` install seam) — new app CNPG cluster + namespace + HelmRelease + Gitea repo materialise |
| 2e | New app reachable | At `<newapp>.<orgslug>.omani.homes` |

### Orthogonal — D31 region-kill BCP failover

Run **in parallel** with Phase 0 / 1 / 2 to exercise Pillar 3. See §3 (D31)
for the gate definition and §6 below for the full counter-test continuity
procedure.

### Mapping each pillar to the deterministic steps

| Pillar | Steps it covers |
|---|---|
| Pillar 1 — Marketplace + signup | Phase 0 (all), Phase 1 step 1a (voucher email), Phase 1 step 1b (redeem + checkout), Phase 1 step 1c (post-signup landing) |
| Pillar 2 — Multi-region BCP at signup | Phase 1 step 1b (wizard region-selection step) |
| Pillar 3 — 2 CNPG clusters + region-kill failover | Phase 1 step 1b (provisioning the 2 clusters), orthogonal D31 (the kill test) |
| Pillar 4 — Agenity + OpenOva MCP | Phase 2 steps 2a–2e |
| Pillar 5 — Sovereign independence | Implicit in all of the above; verified separately by the `bp-self-sovereign-cutover` 10-minute deny-egress hold (see §7 + Principle #11) |

### What "shipped" means

A pillar is **shipped** when an operator (or a read-only Playwright
verification agent — never a verification agent that ships fixes) walks a
**fresh prov** through the pillar-relevant steps and produces:

1. A **screenshot** (`.playwright-mcp/t<NN>-<surface>-<YYYY-MM-DD>.png`)
2. A **non-empty wire-level capture** (log line, curl output, kubectl output, or HAR file)
3. A **working downstream artifact** (the new app reachable, the failover counter intact, the egress-block proof recorded)

One PR landing **does not** ship a pillar. One walk-with-screenshot does.
Every PR against a surface flips that surface back to 🔴 UNVERIFIED in
[`TRUST.md`](ledger/TRUST.md) until re-walked.

---

## §3 — DoD gates (D0–D35)

This is the convergence contract. Every wipe → create → test cycle must
validate every gate below before claiming a Sovereign is converged. Founder
ruling 2026-05-15: silent compromise from these gates is a quality violation.

### Architecture invariants (never compromise)

| ID | Rule |
|---|---|
| A1 | **3 regions minimum.** If a provider has capacity / zone constraints, swap regions — never silently drop to 2. |
| A2 | **Inter-region link = DMZ WireGuard over PUBLIC IPs.** No `hcloud_network` cross-region, no VPC peering, no Huawei VPC — provider-agnostic, always over the DMZ WG endpoint. |
| A3 | **Cilium ClusterMesh apiserver Service = LoadBalancer** (public IP through DMZ WG). The word `NodePort` must never appear in clustermesh-apiserver Service spec on any Sovereign. |
| A4 | **vCluster topology**: primary region = MGMT+DMZ; each secondary region = DMZ+RTZ. Cross-vCluster intra-region traffic stays inside host k3s via Cilium. |
| A5 | **Zero public exposure of K8s control-plane endpoints.** `kubectl get svc -A` on a converged Sovereign returns no NodePort for clustermesh-apiserver, kube-apiserver, or etcd. |
| A6 | **Provider-mix is the canonical case.** Assume 1 region Hetzner, 1 AWS, 1 Huawei. Code must work for that even when the active test prov is all-Hetzner. |

### Gates D0–D35

Every gate must pass on a SINGLE fresh provision in one continuous run. No
partial credit.

**D0 sits ABOVE D1.** Without successful handover + auto-redirect, the
operator never sees that provisioning succeeded — every other gate becomes
invisible from their perspective. The zero-touch contract is end-to-end
OPERATOR experience, not just backend convergence.

| # | Gate | Verifier |
|---|---|---|
| **D0** | **Successful handover + auto-redirect to Sovereign Console.** Once `deployment.status=ready` AND `deployment.handoverFiredAt != null`, the mothership UI auto-routes operator's browser to `deployment.handoverURL` (`/auth/handover?token=<jwt>` on the Sovereign Console). Synthetic `Apps` / `Handover` per-region stage rows MUST be marked Succeeded (or not-applicable), never stuck Pending after handover fires. **No operator action required** — they should land on the Sovereign Console without copying / typing the FQDN. | Playwright MCP |
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
| **D15** | **`/cloud?view=graph` canvas accurate per kind.** `vCluster N/N` non-zero (6/6 on a converged 3-region prov — 1 mgmt + 3 dmz + 2 rtz). `LoadBalancer N/N` non-zero (clustermesh-apiserver LBs + ingress LBs counted). `Cluster N/N` matches actual 3 regions. **No kind chip shows `0/0` for a resource that actually exists in the cluster.** | Playwright MCP |
| **D16** | **`/dashboard` Layer-1 / Layer-2 grouping renders multi-region.** Selecting Layer-1=Cluster on `/dashboard` MUST emit 3 cluster-grouped bubbles (one per region), not a single Sovereign. Layer-2=Namespace MUST emit namespace bubbles WITHIN each cluster bubble. The hierarchy `Cloud → Region → Cluster → vCluster → Namespace → Application` must collapse correctly per the operator's Layer-1 / Layer-2 selection. | Playwright MCP |
| **D17** | **Application detail route `/app/<name>` shows the application, not "deployment id malformed".** Clicking any application card in `/apps` MUST navigate to a route where the URL segment is the application name (e.g. `bp-cnpg`) and the renderer treats it as an application reference, NOT as a deployment id. The notifications drawer MUST NOT contain "Deployment id in the URL is malformed" entries for valid app-name segments. | Playwright MCP |
| **D18** | **Sovereign-side catalyst-api can self-monitor Phase-1 install state.** The chroot Sovereign catalyst-api MUST be able to fetch its own cluster's kubeconfig (or use in-cluster service account) to observe HelmRelease state. The notifications drawer MUST NOT contain "Per-component install monitoring is unavailable for this deployment — the Catalyst API couldn't fetch the new cluster's kubeconfig" entries. Operator should never need to drop to `kubectl get helmrelease` to know per-app install state. | Playwright MCP |
| **D19** | **Apps + Cloud counter consistency.** Apps page Deployments tab count MUST equal Catalog "INSTALLED" count MUST equal `kubectl get hr -A` Ready count. Cloud canvas kind chips MUST NOT show `N/0` for resources that exist (vCluster, LoadBalancer, Bucket, Volume, PVC). PVC count in graph view MUST equal PVC count in list view. App card hrefs MUST NOT have doubled prefix (`/app/bp-bp-*`). | Playwright MCP |
| **D20** | **Jobs page surfaces all-region jobs with region filter.** Jobs view MUST show per-region prefixes (`nbg1-1:`, `sin-2:`) for every app on a multi-region Sovereign, plus an App-filter that lets the operator narrow to a single region. Any unexplained `N/M` counter MUST resolve to an actionable filter or be removed. | Playwright MCP |
| **D21** | **Operator pre-populated as owner-tier on /users post-handover.** Sovereign Console /users MUST list the operator who completed PIN-login as `tier=owner` with their email. /users MUST NOT render empty on a freshly-converged Sovereign. | Playwright MCP |
| **D22** | **Settings page shows real values.** /settings MUST render real values for Region, Capacity, ControlPlaneSize, Created (timestamp), DeploymentID, Pool subdomain — NOT `—` placeholders or "API PENDING" badges. Operator MUST be able to see what their Sovereign actually is. | Playwright MCP |
| **D23** | **Sovereign-side /wizard route does not collide with post-handover landing.** After PIN-login + handover, operator's browser MUST land on `/dashboard` (or the canonical post-handover surface), NEVER on `/wizard` (which is the mothership new-prov flow). | Playwright MCP |
| **D24** | **Mothership-only views absent from Sovereign Console.** The Sovereign Console MUST NOT expose: `/app/dashboard` (mothership fleet view), `/app/settings` (mothership settings), `+ New deployment` button. The Sovereign Console is for ONE Sovereign; the mothership fleet view is a different UI. | Playwright MCP |
| **D25** | **All operator-facing service hostnames reachable + correctly wired.** `keycloak.<fqdn>` / `openbao.<fqdn>` / `openova-flow.<fqdn>` / `prometheus.<fqdn>` / `mimir.<fqdn>` / `loki.<fqdn>` / `tempo.<fqdn>` / `argo.<fqdn>` / `workspaces.<fqdn>` MUST return non-zero HTTP. `harbor.<fqdn>` / `registry.<fqdn>` / `guacamole.<fqdn>` / `marketplace.<fqdn>` MUST return their app page, not 404. No service config may carry a dev hostname (e.g. `gitea.catalyst.local`) in production HTML. | curl + Playwright MCP |
| **D26** | **CSP allows fonts or self-hosts woff2.** Operator MUST NOT see system-font fallback on Sovereign Console pages. Either `fonts.googleapis.com` is allowed by CSP, or fonts are self-hosted (no external dependency). | Playwright MCP |
| **D27** | **Marketplace enabled on the Sovereign.** `MARKETPLACE_ENABLED=true` flows from provision body → bp-catalyst-platform → Sovereign Console: a `/marketplace` route returns 200 with a non-empty catalog page (apps + voucher admin) — NOT 404, NOT a "marketplace disabled" stub. `kubectl get hr -A` shows `bp-marketplace` (or whichever HR backs the marketplace) Ready=True on the chroot. The mothership provision wizard MUST default `marketplace.enabled=true` (zero-touch — operator never toggles a flag). | Playwright MCP + kubectl |
| **D28** | **Voucher issuance from owner-tier UI.** Owner (the operator who PIN-logged-in per D21) opens Sovereign Console marketplace admin → issues a voucher for tenant onboarding. Voucher artifact MUST persist (CR + DB row), MUST be emailed to the chosen recipient via the Sovereign's outbound SMTP, AND MUST be visible in the admin's voucher list. Issuance must be one-click (no kubectl, no API call). Test recipient: `hatice.yildiz@openova.io` (canonical operator-test address) or any other Sovereign-side mailbox the operator controls. | Playwright MCP |
| **D29** | **Voucher-based organization (tenant) provisioning is zero-touch.** Recipient opens the voucher email → clicks redeem link → PIN-login as the test recipient (e.g. `hatice.yildiz@openova.io`) → lands on an organization-creation wizard → completes the form → a new `Organization` / Tenant CR is created → tenant namespace + RBAC + bootstrap apps converge → recipient is auto-redirected to their tenant home page. NO operator intervention beyond the voucher email. | Playwright MCP |
| **D30** | **Free-subdomain selection from operator-curated pool.** Organization wizard step MUST present a subdomain picker populated from the configured pool: `omani.homes`, `omani.rest`, `omani.trade` (singular — see §4 below), and any others the operator has provisioned. Tenant chooses a free subdomain (e.g. `acme.omani.homes`) → cert provisions → tenant landing page resolves on the chosen FQDN with publicly-trusted TLS. The pool MUST come from a Sovereign-side CR / config (not hardcoded). | Playwright MCP + dig + curl |
| **D31** | **Tenant application with CNPG active-hot-standby replication.** Inside the new tenant, user picks a CNPG-backed app from the marketplace (e.g. Ghost or WordPress) → selects "active hot-standby" → app installs with a CNPG `Cluster` that replicates across the Sovereign's regions (primary + at least one replica). `kubectl get cluster.postgresql.cnpg.io -A` in the tenant context shows `instances` distributed across regions (region label / topology spread). Failover test: cordoning the primary region brings the replica to primary, app remains reachable on its FQDN within the documented RTO (≤ 30 s). Full counter-test continuity procedure in §6. | Playwright MCP + kubectl + curl |
| **D32** | **Agenity workspace installable per Org.** A User installs **`bp-agenity`** (`products/agenity/`) from the catalog into their Organization; its StatefulSet (`chepherd run --headless`), Service, and `agenity.<org>.<fqdn>` HTTPRoute materialise and the pod becomes Ready (`/healthz` 200) within the documented window. `kubectl -n <org>-prod get statefulset,svc,httproute,externalsecret -l catalyst.openova.io/blueprint=bp-agenity` shows all present; the Agenity console loads at `agenity.<org>.<sovereign-fqdn>` behind the Org's Keycloak SSO. Agenity is a single-region per-Org singleton (no Sandbox CRD, no `sandbox-controller` — both are removed). | kubectl + Playwright MCP |
| **D33** | **Agenity workspace opens and the OpenOva MCP attaches.** Opening the Agenity console spawns the chepherd-based solo agent on Claude **Opus** (the pod env carries `CHEPHERD_DEFAULT_MODEL=claude-opus-*` + `CHEPHERD_EXTRA_MCP_JSON` with the `openova` server stanza; OAuth auth from the init-seeded `~/.claude/.credentials.json`). The spawned agent's `.mcp.json` lists **both** the `chepherd` runtime MCP and the **`openova`** (`bp-openova-mcp`) server; the OpenOva MCP is reachable using the per-Org `agenity-mcp-bearer` projected as `OPENOVA_MCP_BEARER` (minted into OpenBao at `secret/catalyst/agenity/<slug>/mcp-bearer`). `whoami` over the MCP resolves the Org-scoped identity; the tool set is RBAC-filtered to the User's console surface. | Playwright MCP + kubectl |
| **D34** | **newapi Sovereign-side LLM gateway routes to a backend model.** `https://newapi.<fqdn>/v1/chat/completions` accepts an HS256 org-scoped JWT (issued by `core/services/auth`), authenticates the request, and proxies to a configured backend. The reference backend for this gate is a **partner-hosted Qwen** wired via the operator-supplied `newapi-channel-qwen-partner` Secret (see `docs/RUNBOOKS.md` §Operator-setup). A round-trip `curl` with a valid JWT returns a non-empty `choices[0].message.content` within 30 s. **No Anthropic / OpenAI cloud calls leave the Sovereign by default** — BYOS is opt-in per-user. | curl + kubectl |
| **D35** | **NATS broker round-trips `catalyst.tenant.created` + `catalyst.order.placed` end-to-end.** SME tenant + billing dispatchers PUBLISH to NATS JetStream (subjects `catalyst.tenant.created`, `catalyst.tenant.updated`, `catalyst.order.placed`, `catalyst.invoice.paid` observed via `nats sub 'catalyst.>'`). The Organization controller CONSUMES both subjects (the D35 consume-leg subscribes to `catalyst.tenant.created` + `catalyst.order.placed` in `core/controllers/organization/cmd/main.go`; round-trip wire test per the contract added in `56e04ac8a`). Round-trip test: issue a voucher → redeem it → measure latency from billing-service publish to Org controller reconcile-start ≤ 2 s. **Convergence is NOT declared until both legs are wired** — polling-the-API workaround does not satisfy this gate. | NATS CLI + kubectl logs |

> **DoD grows.** Every iteration of test-writer / test-executor finds more
> operator-visible bugs. Append the gate, ship the fix, re-validate. The list
> is the convergence contract; do not declare convergence until every appended
> gate passes on a single fresh prov.

### Trigger phrases that mean STOP — about to compromise

- "for now let's just do 2 regions"
- "the matrix expects X — let me synth X into the response"
- "Hetzner private net spans zones, let me use that for cross-region"
- "ClusterMesh on NodePort is fine for testing"
- "ash / sin is different zone, let me just stay in eu-central"
- "private NIC link between regions is faster"

If any of these appear in your reasoning → STOP, re-read this file, fix the
root cause.

### Cycle protocol

Before any `tofu apply` or `POST /api/v1/deployments`:

1. Read this file (or the auto-memory mirror at
   `~/.claude/projects/-home-openova-repos-openova/memory/sovereign_multiregion_dod.md`).
2. Log the D0–D35 list to the loop output.
3. Refuse to mark convergence until each D0–D35 has been individually checked.

---

## §4 — Domains canon

This section is the **single source of truth** for FQDN patterns used in
Catalyst test provs and tenant Organizations. Every test, walk, agent
dispatch, and provisioning request must use the patterns below.

### Test-Sovereign FQDNs

| Layer | Pattern | Notes |
|---|---|---|
| Sovereign (test) | `t<NN>.omani.works` | `<NN>` increments with every fresh prov (t39, t40, …). |
| Sovereign (test fallback) | `t<NN>.omantel.biz` | Use when `omani.works` hits a Let's Encrypt rate limit. Swap weekly. |
| Sovereign Console | `console.t<NN>.omani.works` | Operator-facing console UI. |
| Marketplace | `marketplace.t<NN>.omani.works` | Customer-facing storefront for the operator-curated catalog. |
| Operator services | `keycloak.t<NN>.omani.works`, `openbao.t<NN>.omani.works`, `openova-flow.t<NN>.omani.works`, `prometheus.t<NN>.omani.works`, `mimir.t<NN>.omani.works`, `loki.t<NN>.omani.works`, `tempo.t<NN>.omani.works`, `argo.t<NN>.omani.works`, `workspaces.t<NN>.omani.works`, `harbor.t<NN>.omani.works`, `registry.t<NN>.omani.works`, `guacamole.t<NN>.omani.works` | Per §3 D25. |

**Voucher operations live in the operator console's BSS menu, NOT in any
`admin.<sovereign-fqdn>` subdomain.** The legacy `admin.*` references in older
docs and agents are outdated.

### Let's Encrypt rate-limit fallback policy

`omani.works` is the default test TLD. When Let's Encrypt rate-limits issuance
on that TLD (typically after many wipe → create cycles in a week), swap to
`t<NN>.omantel.biz` for the affected week. Both TLDs are operator-owned and
both have the Catalyst NS records pre-provisioned. Never improvise a third
test TLD — adding one to the canon must go through a PR against this section.

### Tenant-Organization FQDNs

Tenant Organizations receive a free subdomain from an **operator-curated pool**
allocated at signup. The pool population is defined in
`core/services/parent-domain/sovereign_parent_domains.go` (the canonical Go
source — pool TLDs are not hardcoded in tests).

| Pattern | Example | Notes |
|---|---|---|
| `<orgslug>.omani.homes` | `acme.omani.homes` | **Default** — first NS-ready entry in registration order per `core/services/sme/sme_tenant.go:514-521`. |
| `<orgslug>.omani.rest` | `acme.omani.rest` | Pool alternate. |
| `<orgslug>.omani.trade` | `acme.omani.trade` | Pool alternate. **Note: singular `trade`, not `trades`** — earlier docs that said `omani.trades` are wrong; do not reintroduce the plural. |

The tenant console URL pattern is `console.<orgslug>.<pool-tld>` — e.g.
`console.acme.omani.homes`. Additional tenant-installed apps are reachable at
`<newapp>.<orgslug>.<pool-tld>` — e.g. `notes.acme.omani.homes`.

### Voucher redeem URL

The canonical voucher-email link pattern (per
`core/services/notification/templates/templates.go`):

```
https://marketplace.t<NN>.omani.works/redeem/?code=<CODE>
```

**The slash before `?` is mandatory** — both URL ends are part of the Phase 1
step 1a contract and must be byte-for-byte stable.

### Forbidden in tests

The following strings must **never** appear in test code, test data,
operator-walk runbooks, fresh-prov provisioning bodies, or any artifact that
exercises the 5-Pillar deterministic path:

- `openova.io` — and any subdomain (`console.openova.io`, `marketplace.openova.io`, etc.)
- `omantel.openova.io` — legacy operator-sample FQDN, dead
- `eventforge.io` — never an OpenOva domain; never the canonical app name
- `Nova Cloud` — never the operator brand for the test stack

`openova.io` is reserved for the **OpenOva marketing site** (the
`openova-private/website/` repo) and the **mothership control plane** during
Phase 0 + Phase 1 cold-start. After
[`bp-self-sovereign-cutover`](adr/0002-post-handover-sovereignty-cutover.md)
runs, every reference to `openova.io` from a franchised Sovereign is a
Principle #11 violation.

### When to switch + why

| Situation | Switch to | Why |
|---|---|---|
| Fresh prov on a clean LE quota | `t<NN>.omani.works` | Default; cheapest, most stable. |
| LE rate-limit on `omani.works` | `t<NN>.omantel.biz` | Same Catalyst NS records, different LE quota. |
| Tenant Organization signup, default | `<orgslug>.omani.homes` | First NS-ready entry; quietest pool TLD. |
| `omani.homes` pool exhausted in a Sovereign | `<orgslug>.omani.rest` then `<orgslug>.omani.trade` | Round-robin within the operator's pool config. |
| Any test or walk artifact | NEVER `openova.io` / `omantel.openova.io` / `eventforge.io` / `Nova Cloud` | Reserved for mothership / marketing surfaces only; appearance in a tenant test = Principle #11 violation. |

### Domain hygiene checks

`docs/trust-audit-*.md` and PR review hunt for:

1. **`openova.io` leaks in test data** — any `*_test.go` / `*.spec.ts` / `*.feature` literal containing `openova.io` is a leak.
2. **Hardcoded operator FQDNs** — any code path that pins the operator domain to a literal instead of reading it from a runtime parameter (`SOVEREIGN_FQDN`, `--sovereign-fqdn`, etc.). See [`PRINCIPLES.md`](PRINCIPLES.md) §4 (never hardcode).
3. **Tenant-Org URL pattern drift** — any path that emits `<orgslug>.openova.io` or `<orgslug>.<sovereign-fqdn>` instead of `<orgslug>.<pool-tld>`. The pool TLD is the source of truth.
4. **`admin.<sovereign-fqdn>` references** — voucher and billing operations live in the BSS menu inside the operator console; an `admin.*` subdomain means a stale reference.
5. **Plural `omani.trades`** — must be singular `omani.trade`. Any new occurrence is a regression.

When in doubt, defer to [`GLOSSARY.md`](GLOSSARY.md) and the Go source files
named above.

---

## §5 — Persona journeys

How different people use Catalyst. Defer to [`GLOSSARY.md`](GLOSSARY.md) for
terminology. The journeys described below use Catalyst surfaces (console / Git
/ API) that are partially design-stage — see
[`STATUS.md`](STATUS.md).

### 5.1 Personas

| # | Persona | Where they live | Tools they use |
|---|---|---|---|
| **P1** | **OpenOva Engineer** | github.com/openova-io | Catalyst codebase, Blueprint repos |
| **P2** | **`sovereign-admin`** | Catalyst admin UI + Sovereign Gitea | Browser UI, Git, kubectl (debug) |
| **P3** | **Support Agent** (within a Sovereign's operations team) | Catalyst admin UI in support mode | Browser UI |
| **P4** | **`org-admin`** | Org-scoped Catalyst console | Browser UI, occasional Git |
| **P5** | **SME End User** (e.g. Ahmed, pharmacy owner on Omantel) | Marketplace + the App they installed | Browser only |
| **P6** | **SME Power User** (e.g. Ahmed's tech-savvy nephew) | Console with Developer mode toggled on | Browser, occasionally Git |
| **P7** | **Corporate DevOps / SRE** (e.g. Layla at a customer-hosted Sovereign) | Git + console in advanced view | Browser, Git, kubectl-on-own-vcluster, IDE |
| **P8** | **Corporate App Developer** (e.g. Omar at a customer-hosted Sovereign) | Console + Git for own service repos | Browser, Git, IDE |
| **P9** | **Security / Compliance Officer** (e.g. Khalid, CISO) | Audit dashboards + EnvironmentPolicy editor | Browser |
| **P10** | **Billing Admin** | Billing console | Browser |

### 5.2 Surfaces

The three first-class surfaces (full list and rationale in
[`ARCHITECTURE.md`](ARCHITECTURE.md) §7):

- **UI** — Catalyst console. Form / Advanced / IaC editor depths.
- **Git** — direct push or PR to the Application's Gitea repo (one repo per App; branches `develop` / `staging` / `main` map to dev / stg / prod), or to private Blueprint repos (`shared-blueprints` per-Org or `catalog-sovereign` Sovereign-wide).
- **API** — REST + GraphQL, for portal integrations.

Plus one debug-only surface:

- **kubectl** — inside one's own vcluster. Read-mostly, never used to mutate Catalyst-managed resources.

**There is no fourth surface.** Terraform, Pulumi, "catalystctl install" are
not part of this model.

### 5.3 Personas × Journeys matrix

Cells show which surface(s) the persona uses for that journey. **Bold** =
primary. *Italic* = secondary. Empty = not applicable.

|  | P1 | P2 | P3 | P4 | P5 | P6 | P7 | P8 | P9 | P10 |
|---|---|---|---|---|---|---|---|---|---|---|
| **J1** Build & publish Blueprint to public catalog | **Git** + CI | | | | | | | | | |
| **J2** Provision a Sovereign | | **UI**+Git | | | | | | | | |
| **J3** Onboard an Organization | | **UI** | **UI** | | | | | | | |
| **J4** Create an Environment | | **UI** | | **UI** | auto on signup | *UI* | **UI** | | view audit | |
| **J5** Install Application from catalog | | | | **UI** | **UI** form | **UI** | UI + **Git** | UI + **Git** | view audit | view cost |
| **J6** Configure Application | | | | **UI** | **UI** form | **UI** | UI + **Git** | UI + **Git** | view audit | |
| **J7** Author private Blueprint | | *Git*+CI | | | | **UI** + Git | **Git** + CI | **Git** + CI | review + sign | |
| **J8** Author Crossplane Composition (advanced) | **Git** + CI | *Git*+CI | | | | | **Git** + CI | | review | |
| **J9** Promote between Environments | | | | **UI** | | | **UI** + Git PR | **UI** + Git PR | **UI** approve | |
| **J10** Observe runtime / debug | | UI dashboards | UI dashboards | UI dashboards | App's own UI | UI | UI + *kubectl* | UI + *kubectl* | UI audit | |
| **J11** Rotate credentials | | **UI** + auto | | UI + auto | auto | UI | UI + auto | | **UI** + policy | |
| **J12** Audit / compliance review | | UI | UI | UI | | | UI (own changes) | UI (own changes) | **UI** export to SIEM | |
| **J13** Billing & quotas | | UI quotas | UI read | UI invoices | UI plan | | | | | **UI** |
| **J14** Off-board / migrate | | UI export | | UI | UI cancel | | UI export | | audit | UI final invoice |

### 5.4 Operator journey — BSS menu (Phase 0 walkthrough)

The operator's primary mutation surface is the **BSS menu** in the Sovereign
Console sidebar — Business Support System for voucher issuance, billing,
plan/quota administration, and tenant lifecycle operations. The BSS menu
**replaces** the dead `admin.<sovereign-fqdn>` subdomain pattern.

| Step | Surface | Action |
|---|---|---|
| O0 | Sovereign Console at `console.t<NN>.omani.works` | PIN-login as owner-tier (D21 must pass — operator pre-populated). |
| O1 | Sidebar → BSS → Vouchers | Issue voucher to test recipient (e.g. `hatice.yildiz@openova.io`). Voucher CR + DB row materialise (D28). |
| O2 | Sovereign outbound SMTP | Recipient receives voucher email with canonical URL `https://marketplace.t<NN>.omani.works/redeem/?code=<CODE>` (slash mandatory). |
| O3 | Sidebar → BSS → Tenants (post-redeem) | New Organization appears in the operator's tenant list with the chosen pool subdomain (D30). |
| O4 | Sidebar → Settings | Region / Capacity / ControlPlaneSize / Created / DeploymentID / Pool subdomain populated with real values (D22). |

What the operator never touches: `admin.<fqdn>`, kubectl, raw NATS, raw SQL.

### 5.5 Customer journey — voucher → checkout → Org (SME, Ahmed)

**Cast.** Ahmed owns 4 small pharmacies in Muscat. No IT staff. He has a
laptop and a credit card. (Sovereign for this example is the Omantel-run
Sovereign for SMEs.)

```
Day 1 — 14:00
─────────────
1. Ahmed receives the voucher email from his Omantel sales rep.
   Link points at his Sovereign's marketplace, e.g.
   https://marketplace.<sovereign-fqdn>/redeem/?code=<CODE>
2. Clicks the link → PIN-login (email + 6-digit PIN magic-link).
3. Picks the canonical Postgres-backed bundle from the operator-curated catalog
   (e.g. bp-bundle-pharmacy: ERPNext + WooCommerce + Stalwart-mail + Postgres + Redis).
4. Org wizard: picks subdomain `muscatpharmacy.<pool-tld>` from the picker
   (D30 — pool comes from operator-curated Sovereign config), confirms
   business details + 2-region BCP topology (Pillar 2).
5. Catalyst auto-creates: Organization "muscatpharmacy", Environment
   "muscatpharmacy-prod", vcluster "muscatpharmacy" on the chosen primary region.
   2 independent CNPG clusters provisioned, ReplicaCluster sync over
   ClusterMesh (Pillar 3). Environment-controller spins up the vcluster
   in ~60 seconds.
6. Provisioning service creates 5 Application Gitea repos under
   gitea.<location-code>.<sovereign-fqdn>/muscatpharmacy/ (one repo per App:
   erpnext, woocommerce, pharmacy-mail, shared-postgres, shared-redis), each
   with develop/staging/main branches and initial manifests.
   Webhook → projector → Flux in the muscatpharmacy vcluster picks up the
   N new GitRepository sources and reconciles.
7. ~3 minutes later: Ahmed sees green checkmarks on his dashboard.
   Each App card has an "Open" button.
   Click ERPNext → SSO via the Sovereign's Keycloak realm for muscatpharmacy → he's in.
─────────────
Day 1 — 14:08 — Ahmed is selling.
```

**What he never saw**: Git, kubectl, vcluster, Flux, Blueprint, YAML,
JetStream. **His mental model**: "I have a Sovereign account. I bought a
bundle. It works."

### 5.6 Tenant journey — login → Agenity → OpenOva MCP → new app (Phase 2)

This is the deterministic Phase 2 walkthrough from §2 expressed as a journey
narrative. Same tenant Organization (Ahmed's muscatpharmacy, or any tenant
created in §5.5).

| Step | Surface | Action |
|---|---|---|
| T0 | `console.<orgslug>.omani.homes` | Tenant PIN-logs-in. Dashboard renders (Pillar 1 + Pillar 2 already shipped). |
| T1 | Tenant Console → Agenity | The User installs / opens the per-Org **Agenity** workspace; its chepherd-based solo agent runs Claude **Opus**, OAuth-authenticated from the init-seeded `~/.claude/.credentials.json` (the operator's own OAuth credential — no Anthropic cost leak). |
| T2 | Agenity workspace | The `openova` (`bp-openova-mcp`) MCP server attaches with zero user-typed config, reachable via the per-Org `agenity-mcp-bearer` (`OPENOVA_MCP_BEARER`). Its tool set is RBAC-scoped to the User's console surface. User pastes zero credentials. |
| T3 | Agent prompt | Tenant prompts: *"install a notes app backed by Postgres in my Org, public on `notes.<orgslug>.omani.homes`."* |
| T4 | Agent action | The agent calls the OpenOva MCP `create_application` tool (Org-pinned, tier-admin-gated), which forwards the per-Org bearer to the catalyst-api install seam. The new app's CNPG cluster + namespace + HelmRelease + Gitea repo materialise. |
| T5 | New app | Reachable at `https://notes.<orgslug>.omani.homes` with publicly-trusted TLS. |

The tenant **never** typed a kubeconfig, never opened Git, never copied a DB
connection string. Pillar 4 shipped end-to-end.

### 5.7 Corporate journey — Layla at a customer-hosted Sovereign

**Cast.** Layla is an SRE on a 12-person Cloud Platform team at a customer
that runs its own Sovereign on Hetzner. Their internal Organizations are
`core-banking`, `digital-channels`, `analytics`, `corporate-it`. Their default
tooling is Git + IDE.

```
09:00  Coffee. Opens VS Code. Branch: bp-bd-payment-rail
─────────────────────────────────────────────────────────────────────────
       She's authoring a private Blueprint for a payment-rail microservice
       with Postgres + Redis dependencies.

09:15  Pushes to gitea.<location-code>.<customer-sovereign>.local/digital-channels/shared-blueprints/
       bp-bd-payment-rail. CI in the customer's GitHub Actions runner pool
       (running inside the Sovereign) builds the image, signs the Blueprint
       with cosign, publishes to the local OCI registry. blueprint-controller
       picks it up — visible as a private card in the digital-channels Org.

10:00  Switches to her Application repo:
       gitea.<location-code>.<customer-sovereign>.local/digital-channels/payment-rail
       Checks out branch `develop` (the dev environment branch).
       Edits values.yaml (config tweak).
       Catalyst console (Plan view) shows the diff: what will change,
       dependency impact, drift, cost delta. Like `terraform plan`, but
       served by the API on the Git diff.

10:15  Happy. Commits to develop. Webhook → projector → Flux in the
       digital-channels vcluster (watching the develop branch on this
       Application repo) reconciles in 30s. Audit log captures her as
       committer at the App-repo level.

11:00  Need to debug the staging deployment of the same App.
       Browser: console → digital-channels-stg → payment-rail card
       → Logs tab. Then Topology tab to see across regions.
       Or, drops into kubectl scoped to her vcluster:
         $ kubectl --context=hz-fsn-rtz-prod-digital-channels logs -n payment-rail
       Direct kubectl, scoped strictly to her Org's vcluster (vcluster name
       per NAMING §1.5 is the Org name, not the Sovereign name — Layla's Org
       is `digital-channels`). The customer's sovereign-admin grants this via
       a JIT elevation flow.

14:00  Promotion stg → uat. From the payment-rail Application card,
       clicks "Promote staging → uat". Catalyst opens a Gitea PR
       within the same payment-rail repo: source branch `staging`,
       target branch (a feature branch tracking uat config). The
       EnvironmentPolicy CR for digital-channels-uat (in
       system/catalyst-config/policies/) requires team-platform approver
       and an RE score ≥ 80%. Reviewer approves via Gitea web UI (or
       via the Catalyst console's PR view — same backend). Auto-merge.
       Flux in the uat-bound vcluster reconciles.

15:00  New Environment needed for a fraud lab. From the console:
       "New Environment in analytics" → fills name "fraud-lab-dev" →
       picks "small" topology (1 region, single bb=rtz). Environment-controller
       creates the vcluster and bootstraps Flux pointing at the develop
       branch of every Application repo in the analytics Org. No new repos
       are created (Application repos exist already, indexed by branch).
       Ready in 60s. Layla now has a new throwaway dev Environment.

16:00  Business asks for the customer's existing Backstage portal to show
       Catalyst-managed services. Layla integrates: Backstage queries
       Catalyst REST API at https://api.<location-code>.<customer-sovereign>.local/v1/applications,
       authenticated via workload identity (Backstage runs inside the
       Sovereign). Backstage's service catalog now includes Catalyst
       Applications alongside other systems. No code change in Catalyst —
       the API was already there.
```

**What Layla DOES use**: UI (for promotion approvals, observability,
EnvironmentPolicy editing), Git (for Blueprint authoring in `shared-blueprints`
and per-Application config in each App's repo with `develop` / `staging` /
`main` branches), kubectl (for debugging her own vcluster), and the API (for
integrating Backstage). She **never** writes Crossplane code unless she's
contributing a new Composition upstream as a Blueprint — and even then it's
via a Gitea PR.

**What Layla doesn't use**: Terraform, Pulumi, a "catalystctl" CLI, or any
other tool that bypasses Git.

### 5.8 Application card (the user's primary handle)

The card is the user's view of an Application in their Environment. Anatomy
below; full UX in the console docs.

```
┌────────────────────────────────────────────────────────────────┐
│  🌐  marketing-site                                       ⋮   │  ← name + menu
│      bp-wordpress @ 1.3.0                                      │  ← Blueprint + version
├────────────────────────────────────────────────────────────────┤
│  ●  Running         🔗 acme.com  ↗                             │  ← status + endpoint
│                                                                 │
│  📍 eu-central                          5 / 5 pods              │  ← placement + health
│  💾 postgres → shared-postgres (own card)                       │  ← key dependency (linked)
│                                                                 │
│  Last deploy: 2h ago by Layla     ⏵ View history                │  ← provenance
│                                                                 │
│  [ Open app ↗ ]    [ Settings ]    [ Logs ]    [ Topology ]    │  ← primary actions
└────────────────────────────────────────────────────────────────┘
```

States via the status badge:

| State | Meaning |
|---|---|
| ● Running (green) | All replicas healthy, traffic flowing |
| ◐ Installing (blue) | Flux reconciling, progress shown inline |
| ◑ Updating (blue) | Config or version change rolling out |
| ◒ Degraded (amber) | Partial — `3/5 pods, 2 unhealthy` |
| ◓ Failed (red) | Install or update failed, "View error" button |
| ○ Paused (grey) | Manually paused, scale-to-zero |
| ◔ Pending approval (purple) | Promotion PR open, awaiting reviewers |

Clicking the card opens the **detail page** with tabs: Overview, Settings,
Topology, Secrets, Observability, History, Manifests.

The **Topology** tab is where Placement edits happen — single-region →
active-active, region picker, failover policy. The **Manifests** tab is the
Monaco IaC editor.

### 5.9 Catalog vs Applications-in-use view

The Marketplace renders **Blueprint cards** (something to install) — visually
distinct from Application cards (something running). The Blueprint detail
page is the "where is this Blueprint running in my Org" view — a query, not a
chain object. The Environment view groups Application cards by status, with
backing services (Postgres, Redis, etc.) in their own section.

### 5.10 Default UI mode by Sovereign type

| Setting | SME-style default | Corporate default (customer self-host) |
|---|---|---|
| Console default depth | Form view | Advanced view + IaC editor toggle on |
| Developer mode (Blueprint Studio) | Hidden, off | Visible by role |
| Multi-Environment promotion features | Hidden when only 1 Env | Visible always |
| EnvironmentPolicy editor | Hidden by default | Visible by role |
| `kubectl` access for users | Off | On for `org-developer` and above |
| Git access for users | Off (sovereign-admin can flip per-Org) | On |
| Marketplace features (search, bundles, ratings) | All on | All on but de-emphasized |
| Specter / AIOps Blueprint included by default | Optional | Recommended (Cortex + Specter on top) |

Each Sovereign sets its defaults at provisioning time; users within can
override via per-user preferences within the role permissions allowed.

---

## §6 — Multi-region BCP test (D31 detail)

D31 is the **region-kill BCP failover** gate — the verifier for Pillar 3. Run
**in parallel** with Phase 0 / 1 / 2 on the same fresh prov.

### Preconditions

1. Tenant Organization exists (created via §2 Phase 1).
2. Tenant has installed a CNPG-backed app via the marketplace with **active
   hot-standby** selected. Two independent CNPG `Cluster` CRs exist — one in
   each chosen region — with `ReplicaCluster` sync over Cilium ClusterMesh on
   the DMZ WireGuard data plane (A2 + A3).
3. App reachable on its FQDN with publicly-trusted TLS.

### Counter-test continuity procedure

The continuity check is a **monotonic counter** that increments through the
region kill. Any gap, replay, or skipped value = failed gate.

| Step | Action | Pass criterion |
|---|---|---|
| 1 | Start the counter writer: a client process that `INSERT … RETURNING id` every 100 ms against the app's primary CNPG endpoint, recording each returned `id` and timestamp locally. | Counter increments monotonically — no holes pre-failover. |
| 2 | Kill the primary region. Two valid kill modes: (a) instance destroy via the cloud-provider API; (b) `NetworkPolicy` isolation that drops all traffic in/out of the primary region's namespaces. | Primary region becomes unreachable within ≤ 5 s. |
| 3 | `failover-controller` (per Continuum CR) flips traffic to the replica region. Replica CNPG `ReplicaCluster` promotes to primary. Cilium ClusterMesh keeps inter-region pod-to-pod alive across the surviving regions. | Failover RTO ≤ 30 s end-to-end (kill → counter writer reconnects to the new primary). |
| 4 | Counter writer reconnects via the app's FQDN (DNS / LB now points at the surviving region). | Writer resumes within the 30 s window; **no transaction lost** — the next written `id` is `last_id + 1`, never less, never with a gap. |
| 5 | (Optional, ledger-grade) After failover, walk the durable WAL and confirm every `id` from before the kill is present in the new primary's data plane. | Zero `id` gaps in the replica-promoted-to-primary's data. |

### Hard requirements

- The kill MUST be a real region kill, not a Pod restart or a Deployment
  scale-to-zero. A single-region prov cannot satisfy D31 — see PR #1599 shape
  in [`PRINCIPLES.md`](PRINCIPLES.md) ("multi-region claim
  on single-region prov").
- The failover must be triggered by `failover-controller` reading the
  Continuum CR — never by a human flipping DNS by hand.
- The counter writer's local log + the post-failover database state are the
  **only acceptable evidence**. Operator-walk screenshots alone do not
  satisfy D31.

### Service-plane contract during the kill — control-plane availability (deliberate, #5267)

Pillar 3's DR contract is **data-plane** continuity: CNPG promotion, customer
workloads, and the gateway VIP in the surviving region. The **Catalyst
control plane** (catalyst-api serving console / api / marketplace in
`catalyst-system`) is **primary-region-only by design** and is NOT part of
the failover contract. This was decided twice, independently:

- **G2 (#2574)**: `bp-catalyst-platform` installs only where
  `sovereign_region_role == "primary"` — bootstrap-kit slot 13 renders
  `suspend: true` on every secondary CP (`SECONDARY_HR_SUSPEND`).
- **Chart design**: catalyst-api is single-replica with
  `strategy: Recreate`, persisting the deployment store (tofu workdirs,
  flat-file JSON deployment records) and the k8scache snapshots on two
  **RWO EVS PVCs** whose PVs carry zone-scoped nodeAffinity — the volume
  cannot attach in the other region, so a region-b replica cannot mount the
  store. The store is single-writer flat-file with no leader election; a
  region-b replica with its own PVC would fork the deployment/tofu state
  (split-brain over infrastructure truth — double-provision / mis-wipe
  hazards), and catalyst-api's in-process singleton loops (phase-1 watch,
  deploy reconciler, cutover driver) would run twice.

**Expected observable behavior during a primary-region kill** (live-confirmed
hw277 G12, 2026-07-19 — #5267):

| Surface | During outage | On failback | Meaning |
|---|---|---|---|
| Gateway VIP (`https://console.<sovereign-fqdn>/`) | **HTTP 404 with `server: envoy`** | HTTP 200 | TLS terminates on the surviving region's envoy (#5246 both-region ELB pool); the 404 proves the gateway is alive and the control-plane backend is deliberately absent |
| Customer data plane (tenant Applications, CNPG) | Serving from the surviving region | Serving | The actual Pillar-3 contract |
| Console / api / marketplace (control plane) | Unreachable — the 404 above | Returns automatically, no manual action | Control plane restarts with the primary region |

A whole-outage connection **timeout / black-hole** on the gateway VIP is a
**FAIL** (#5244 shape — ELB pool was primary-region-only). The
404-with-`server: envoy` signature is the **PASS** signature for the
service-plane check: it distinguishes "gateway failed over, control plane
deliberately absent" from "gateway dead".

During the outage the sovereign-admin observes/drives via `kubectl` against
the surviving region's cluster; D31 itself needs no console — the failover
is machine-driven (`failover-controller` + counter writer, per the hard
requirements above).

Control-plane HA is **deferred with reason**: it requires a persistence
redesign (deployment store moved off RWO-EVS flat files onto a DR-replicated
database, plus leader election for the singleton loops) — not a
replica-count bump. The contract is CI-gated by
`products/catalyst/chart/tests/api-single-writer-dr-contract.sh`, which
fails any chart change that raises catalyst-api replicas, drops
`strategy: Recreate`, or widens the PVC access mode without revisiting this
section.

---

## §7 — Pillar 5 sovereignty cutover

The Phase 1 → cutover transition is the proof that a franchised Sovereign can
operate independently of the OpenOva mothership.

### Choreography

1. `bp-self-sovereign-cutover` installs **dormant** at bootstrap-kit slot 06a
   during Phase 1. It is present but inert — no Jobs running, no tether
   pivots active.
2. Post-handover, the operator clicks the "Achieve True Sovereignty" CTA in
   the Sovereign Console.
3. The Blueprint runs **eight sequential Jobs**, each pivoting one of the
   tethers listed in §1 Pillar 5. Tethers are pivoted in dependency order so
   the cluster never loses its ability to pull what it needs at each step
   (e.g. Harbor proxy-cache is warmed before containerd registries.yaml
   flips).
4. The **final step is a 10-minute deny-egress NetworkPolicy hold** against
   `github.com`, `ghcr.io`, and `harbor.openova.io`. During the hold:
   - Flux must continue to reconcile (sources are now local Gitea + Harbor).
   - All HelmReleases must remain Ready=True.
   - No image-pull errors, no Git fetch errors, no upstream registry hits.
5. `cutoverComplete=true` is set **only if** the cluster reconciles green
   during the full 10-minute hold. Any hiccup = the cutover failed; rollback
   to pre-cutover state, fix the root cause, re-run.

### Verification (the only acceptable evidence)

- **Egress-block proof**: a Hubble / NetworkPolicy log showing zero allowed
  flows to `github.com`, `ghcr.io`, `harbor.openova.io` for the duration of
  the 10-minute hold.
- **Reconcile-green proof**: `kubectl get hr -A -o jsonpath=…` showing every
  HelmRelease Ready=True at minute 0 and minute 10 of the hold.
- **Operator-walk screenshot**: the Sovereign Console's "Sovereignty" tab
  showing `cutoverComplete=true` with the timestamp of the hold.

**No cutover claim without all three.** See
[`adr/0002-post-handover-sovereignty-cutover.md`](adr/0002-post-handover-sovereignty-cutover.md)
for the full architecture, alternatives considered, and per-step contract.

### Customer-sync — how each Sovereign keeps the catalog after cutover

Each franchised Sovereign's Gitea **mirrors** the public catalog from this
repo (`openova-io/openova`):

```
GitHub (openova-io/openova)              Per-Sovereign Gitea (mirrored)
─────────────────────────────              ───────────────────────────────
platform/cilium/        ────sync────>    gitea.<location-code>.<sovereign-domain>/catalog/bp-cilium/
products/cortex/        ────sync────>    gitea.<location-code>.<sovereign-domain>/catalog/bp-cortex/
...
```

Sovereigns pull on their own schedule (default daily). Air-gapped Sovereigns
mirror via offline media. After `bp-self-sovereign-cutover` completes, the
Sovereign's Flux reconciles **exclusively** from its local Gitea + Harbor —
never back to `github.com/openova-io` or `ghcr.io/openova-io` (Principle #11).
