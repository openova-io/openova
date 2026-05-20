# 5-Pillar Definition of Done

**Status:** Authoritative. **Updated:** 2026-05-20.

This document is the **end-user Definition of Done** for the Catalyst platform.
Every dispatch in this repo must answer:

> Which of the 5 pillars does this work move forward, and which deterministic step (Phase 0 / 1 / 2) does it advance?

If the answer is "none," **the work is wrong — pick differently.**

The 5 pillars are **inseparable** — none alone is a viable platform.
Pillar work is **strictly tertiary** to operator-console polish, cosmetic-guards
re-enable, treemap drill-down quality, jobs region filter, admin sidebar nav, etc.

For domain conventions used in every test, see [`DOMAINS-CANON.md`](DOMAINS-CANON.md).
For the per-PR walk choreography, see [`WALK-RUNBOOK-2026-05-20.md`](WALK-RUNBOOK-2026-05-20.md).
For multi-region BCP gates, see [`SOVEREIGN-MULTI-REGION-DOD.md`](SOVEREIGN-MULTI-REGION-DOD.md).
For the architectural principles each pillar enforces, see [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md).

---

## The 5 inseparable pillars

| # | Pillar | What "shipped" looks like |
|---|---|---|
| **1** | **Marketplace + voucher onboarding** | Anonymous visitor reaches the operator-branded marketplace → picks the canonical Postgres-backed bundle → completes signup (email + 6-digit PIN magic-link) → Organization CR created. |
| **2** | **Multi-region BCP topology choice at signup** | Wizard exposes region/topology choice during signup; customer picks N regions; system provisions across all N in one pass. Not a Day-2 upgrade. |
| **3** | **Two independent CNPG clusters with ReplicaCluster sync + region-kill failover** | One CNPG cluster per region; synchronous `ReplicaCluster` replication over Cilium ClusterMesh on the DMZ WireGuard-over-public-IPs data plane; region-kill test passes with zero transactions lost. |
| **4** | **Sandbox + auto-mounted MCP plugin with full org knowledge** | Sandbox launches the chosen agent CLI; **`openova-sandbox-mcp` auto-mounts at session start** with every org resource (apps, vClusters, conn-strings via OpenBao+SPIRE SVID, Gitea repos, IAM, region health). User pastes zero credentials. Agent answers prompts with full org context and mutates resources via MCP tool calls. |
| **5** | **Sovereign independence post-cutover** | After `bp-self-sovereign-cutover` runs, zero egress to `harbor.openova.io`, `ghcr.io/openova-io`, or `github.com/openova-io` — proved by a 10-minute deny-egress NetworkPolicy hold (Principle #11). |

---

## The deterministic test — Phase 0 / 1 / 2

The test is **deterministic** — one fresh prov, one run, all phases pass in order.
No retries, no "works if you wait longer."

### Phase 0 — Operator issues voucher via BSS

Voucher operations live in the operator console's **BSS menu** (Business Support
System), **NOT in any `admin.<sovereign-fqdn>` subdomain**. The legacy `admin.*`
references in older docs/agents are outdated.

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

### Phase 2 — Customer launches Sandbox; agent provisions an additional app via MCP

**This is the most important test.** It exercises Pillar 4 end-to-end and
proves that an agent acting on behalf of the tenant can mutate the
Organization's resources entirely through the auto-mounted MCP plugin —
without the user typing any credential.

| Step | Action | Outcome |
|---|---|---|
| 2a | Tenant logs in at `console.<orgslug>.omani.homes` | Dashboard renders |
| 2b | Opens **Sandbox** | Sandbox session launches with agent set to **`qwen-code`** (NOT claude-code — `qwen-code` routes through newapi → Sovereign-hosted Qwen, **zero Anthropic cost leak**) |
| 2c | `openova-sandbox-mcp` auto-mounts at session start | 49 MCP tools available with zero user-typed config (full handler set per `products/sandbox/mcp-server/internal/tools/registry.go`) |
| 2d | Customer prompts qwen-code to provision an **additional application** in their Organization | Agent uses MCP tools (`sandbox.db.provision`, `sandbox.auth.provisionRealm`, `marketplace.app.install`, etc.) — new app CNPG cluster + namespace + HelmRelease + Gitea repo materialise |
| 2e | New app reachable | At `<newapp>.<orgslug>.omani.homes` |

### Orthogonal — D31 region-kill BCP failover

Run **in parallel** with Phase 0/1/2 to exercise Pillar 3:

| Action | Pass criterion |
|---|---|
| Kill primary region while the Org is running (instance destroy or NetworkPolicy isolation) | `failover-controller` flips traffic ≤ 30 s; replica CNPG promotes via `ReplicaCluster`; Cilium ClusterMesh keeps inter-region pod-to-pod alive; **zero transactions lost** (verified by a counter incrementing through the failover) |

See [`SOVEREIGN-MULTI-REGION-DOD.md`](SOVEREIGN-MULTI-REGION-DOD.md) D31 for the
full verifier procedure and the multi-region architecture invariants A1–A6.

---

## Mapping each pillar to the deterministic steps

| Pillar | Steps it covers |
|---|---|
| Pillar 1 — Marketplace + signup | Phase 0 (all), Phase 1 step 1a (voucher email), Phase 1 step 1b (redeem + checkout), Phase 1 step 1c (post-signup landing) |
| Pillar 2 — Multi-region BCP at signup | Phase 1 step 1b (wizard region-selection step) |
| Pillar 3 — 2 CNPG clusters + region-kill failover | Phase 1 step 1b (provisioning the 2 clusters), orthogonal D31 (the kill test) |
| Pillar 4 — Sandbox + auto-mounted MCP | Phase 2 steps 2a–2e |
| Pillar 5 — Sovereign independence | Implicit in all of the above; verified separately by the `bp-self-sovereign-cutover` 10-minute deny-egress hold (see below + Principle #11) |

---

## Pillar 5 — `bp-self-sovereign-cutover` and the 8-tether pivot

A franchised Sovereign emerging from Phase 1 is operationally tethered to the
OpenOva mothership in **eight** places (full map in
[`ADR-0002`](adr/0002-post-handover-sovereignty-cutover.md) §2.1 and in
[`ARCHITECTURE.md`](ARCHITECTURE.md) §11.1):

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

**`bp-self-sovereign-cutover`** installs **dormant** at bootstrap-kit slot 06a
during Phase 1 and is triggered post-handover by the operator's
"Achieve True Sovereignty" CTA. Eight sequential Jobs pivot the tethers in
dependency order; the **final step is a 10-minute deny-egress NetworkPolicy
hold** against `github.com`, `ghcr.io`, and `harbor.openova.io`. The only
condition under which `cutoverComplete=true` is set is that the cluster
reconciles green during this hold. **No cutover claim without the
egress-block proof.**

See [`ADR-0002`](adr/0002-post-handover-sovereignty-cutover.md) for the full
architecture, alternatives considered, and the per-step contract.

---

## Pillar 4 — `openova-sandbox-mcp` auto-mount mechanism

When a Sandbox session attaches, the `sandbox-pty-server` (per
`products/sandbox/pty-server/`) writes the chosen agent's `mcp.json` config to
every canonical agent-config path (claude-code, qwen-code, opencode, aider,
cline) and starts the MCP server as a stdio subprocess of the agent process.
The server exposes 49 handlers grouped under namespaces such as
`sandbox.db.*`, `sandbox.auth.*`, `marketplace.app.*`, `sandbox.git.*`,
`sandbox.iam.*`, etc. (full registry in
`products/sandbox/mcp-server/internal/tools/registry.go`).

Authentication is SPIFFE/SPIRE-issued SVID — the agent's caller-identity is the
tenant Organization's workload identity, never a long-lived API key. The agent
never sees credentials; the MCP tool calls carry the SVID.

Per Principle #1 (the waterfall is the contract) and Principle #2 (never
compromise from quality), Pillar 4 is **not** "we'll ship a stub MCP server now
and wire real tools later." A Sandbox session that boots without the full 49
tools is Pillar 4 unshipped, regardless of how good the chrome looks.

---

## Customer-sync — how each Sovereign gets the catalog

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
Sovereign's Flux reconciles **exclusively** from its local Gitea + Harbor — never
back to `github.com/openova-io` or `ghcr.io/openova-io` (Principle #11).

---

## What "shipped" means

A pillar is **shipped** when an operator (or a read-only Playwright
verification agent — never a verification agent that ships fixes) walks a
**fresh prov** through the pillar-relevant steps and produces:

1. A **screenshot** (`.playwright-mcp/t<NN>-<surface>-<YYYY-MM-DD>.png`)
2. A **non-empty wire-level capture** (log line, curl output, kubectl output,
   or HAR file)
3. A **working downstream artifact** (the new app reachable, the failover
   counter intact, the egress-block proof recorded)

One PR landing **does not** ship a pillar. One walk-with-screenshot does.
Every PR against a surface flips that surface back to 🔴 UNVERIFIED in
[`TRUST.md`](TRUST.md) until re-walked.

See [`WALK-RUNBOOK-2026-05-20.md`](WALK-RUNBOOK-2026-05-20.md) for the per-PR
walk choreography and [`ANTI-PATTERN-CATALOG.md`](ANTI-PATTERN-CATALOG.md) for
the failure-modes operators must hunt for during the walk.
