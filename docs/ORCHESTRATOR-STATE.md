# Orchestrator State — Catalyst-Zero Waterfall

**Updated:** 2026-04-29. **Live state, not aspirational.**
**Latest commit on main:** `dd578d1c`. **catalyst-build CI:** ✅ green; PDM build (`pool-domain-manager-build`) and PowerDNS blueprint-release CI both ✅ green.

This file is the durable hand-off record for the multi-agent orchestration of the Catalyst-Zero consolidation + first-Hetzner-Sovereign waterfall. Read first when resuming work.

---

## Ticket-board snapshot (live numbers)

| | Count |
|---|---|
| Closed (this session) | 74 |
| Open | 43 |
| Parent issue | [#43](https://github.com/openova-io/openova/issues/43) |

## Group status

| Group | Tickets | Status | Branch / commits |
|---|---|---|---|
| **A — Consolidation** | 9/9 | ✅ Closed | `3c2f7e4` |
| **B — SME backend services** | 10/10 | ✅ Closed | `7646840` |
| **C — Catalyst-Zero cutover** | 8 | 🟢 Unblocked — catalyst-build CI green at `333b859`. Branch `group-c-cutover-catalyst-zero` ready in **openova-private**. Awaits operator-confirmed merge + `kubectl annotate` on Contabo. | parallel `openova-public` GitRepository (does not orphan unrelated workloads) |
| **D — Wizard** | 10/10 | ✅ Closed | `854a063` + `e87913a` + `171ff9c` + `3440bf7` + `cf60bd7` |
| **E — Provisioner** | 13 | ✅ Mostly closed via Lesson #24 revert + OpenTofu canonical path | `e668637` (revert), `e7a74f0` (variables/main/outputs), `cf60bd7` (retry endpoint) |
| **F — Bootstrap-kit charts** | 14/14 | ✅ All 11 OCI artifacts published + cosigned + SBOM-attested | `8c0f766`, `441ebae`, `62d9c7d`, `8efc6e0`, `046e5eb` |
| **G — DNS multi-domain** | 6 | ✅ Superseded by PowerDNS authoritative (#167) + pool-domain-manager (#163, #168) + registrar adapters (#170 — Cloudflare/Namecheap/GoDaddy/OVH/Dynadot). Dynadot is now one of five registrar adapters inside PDM, not the authoritative DNS surface. | `0190c605` (#167), `2854d652` (#163), `f7773943` (#168), `567d7e1f` (#170), `67fdecb7` (#171 k8gb retire) |
| **H — Franchise + voucher** | 7/7 | ✅ Closed | `f2951af` |
| **I — Wizard UX** | 6/6 | ✅ Closed | `2bcf564` + `cf60bd7` |
| **J — Hetzner infra** | 6/6 | ✅ Closed (cx32→cx42 sizing-bug fix is real) | `e5550d7` |
| **K — Documentation** | 7/8 | ✅ 7 closed; #134 (omantel ✅) deferred to DoD | `dc3f50d` |
| **L — Testing** | 8/8 | ✅ Closed (Hetzner test gated on `HETZNER_TEST_TOKEN` repo secret) | `9519c1e` |
| **M — End-to-end DoD** | 9 | 📐 Blocked on operator-provided Hetzner credentials + Group C cutover merge | — |

## Architectural compliance (Lesson #24 closed)

- **OpenTofu** owns Phase 0 — `infra/hetzner/{versions,variables,main,outputs}.tf` + `cloudinit-{control-plane,worker}.tftpl`.
- **Crossplane** is the day-2 IaC — 5 XRDs + 5 Compositions at `platform/crossplane/compositions/` under canonical `compose.openova.io/v1alpha1` group (server, network, firewall, loadbalancer, pool-allocation — the latter wraps PDM `/v1/reserve`).
- **Flux** is the GitOps reconciler — `clusters/_template/` + `clusters/omantel.omani.works/` with HelmReleases in dependency order via `dependsOn`. The bootstrap-kit ships with bp-powerdns (#167) installed in `openova-system` on the mgt cluster.
- **Blueprints** are the install unit — 12 cosigned `bp-<name>:<semver>` OCI artifacts at `ghcr.io/openova-io/` (the original 11 G2 charts plus bp-powerdns at 1.0.6). CI fan-out via `.github/workflows/blueprint-release.yaml`.
- **DNS architecture** — bp-powerdns is authoritative for every Sovereign zone (per-Sovereign zone model, #168). pool-domain-manager (`core/pool-domain-manager/`) allocates pool subdomains and exposes registrar adapters for byo-api flow (#163, #170). k8gb is retired (#171).
- **Inviolable principles** anchored in `docs/INVIOLABLE-PRINCIPLES.md` + `~/.claude/.../memory/feedback_inviolable_principles.md` + global CLAUDE.md.

## What still needs to happen for DoD

1. **Operator confirms Group C cutover** in openova-private + runs `kubectl annotate gitrepository/{flux-system,openova-public}` on Contabo. (~5-min outage, fully reversible.)
2. **Operator provides real Hetzner Cloud API token + project ID** to the wizard at `https://console.openova.io/sovereign`.
3. **Operator provides SSH public key** (StepCredentials).
4. **Operator picks domain** — three modes (#169): pool (`omani.works` → subdomain `omantel`), byo-manual, or byo-api with a registrar token (Cloudflare/Namecheap/GoDaddy/OVH/Dynadot).
5. **Click Provision.** Wizard POSTs to `/api/v1/deployments`. catalyst-api runs `tofu init && tofu apply`. Cloud-init bootstraps k3s + Flux + GitRepository pointing at `clusters/omantel.omani.works/`. Flux reconciles HelmReleases in dependency order. ~10 minutes total.
6. **DNS**: PDM `/v1/commit` creates the per-Sovereign PowerDNS zone, writes the canonical 6-record set via PowerDNS REST API, and updates the parent-zone NS delegation via the matching registrar adapter (Dynadot for pool; selected adapter for byo-api; skipped for byo-manual which polls customer-side propagation instead).
7. **TLS** auto-issued via cert-manager DNS-01 against the per-Sovereign PowerDNS zone (cert-manager-webhook-pdns) for wildcard certs, or HTTP-01 fallback.
8. omantel-admin logs into `console.omantel.omani.works`, issues a voucher via `/admin/billing`.
9. Tenant redeems at `omantel.omani.works/redeem?code=...`, creates Org, installs first App.

## Active parallel work

- Group G is closed-out by the new DNS architecture (#167/#163/#168/#170/#171).
- Open follow-ups: #175 (transitive-mandatory cascade UX), #173 (component-card logo render fix), #170/#168/#169/#167/#163/#162/#161/#171 awaiting UAT-to-completed move.

## Resume protocol

1. Read this file.
2. `cd /home/openova/repos/openova && git pull origin main`.
3. `gh issue list --repo openova-io/openova --state open --label area/platform` to see remaining tickets.
4. Dispatch parallel agents on independent tickets via Agent tool with `isolation: "worktree"`.
5. The user's standing instruction: 24-hour-no-stop, ≥5 parallel agents, never violate `INVIOLABLE-PRINCIPLES.md`.

## References

- `docs/INVIOLABLE-PRINCIPLES.md` — non-negotiable rules
- `docs/PROVISIONING-PLAN.md` — canonical 8-phase plan
- `docs/AUDIT-PROCEDURE.md` — on-demand validation
- `docs/RUNBOOK-PROVISIONING.md` — operator-level guide
- `docs/FRANCHISE-MODEL.md` — voucher mechanism
