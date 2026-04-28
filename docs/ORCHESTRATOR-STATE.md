# Orchestrator State — Catalyst-Zero Waterfall

**Updated:** 2026-04-28. **Live state, not aspirational.**
**Latest commit on main:** `cf60bd7`. **catalyst-build CI:** ✅ green at `333b859`.

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
| **G — DNS multi-domain** | 6 | 🚧 #108 + #109 done (`ec43aac`, `5a1a85a`). #110, #111, #112, #113 in progress. | `group-g-dns-multi-domain` (parallel agent) |
| **H — Franchise + voucher** | 7/7 | ✅ Closed | `f2951af` |
| **I — Wizard UX** | 6/6 | ✅ Closed | `2bcf564` + `cf60bd7` |
| **J — Hetzner infra** | 6/6 | ✅ Closed (cx32→cx42 sizing-bug fix is real) | `e5550d7` |
| **K — Documentation** | 7/8 | ✅ 7 closed; #134 (omantel ✅) deferred to DoD | `dc3f50d` |
| **L — Testing** | 8/8 | ✅ Closed (Hetzner test gated on `HETZNER_TEST_TOKEN` repo secret) | `9519c1e` |
| **M — End-to-end DoD** | 9 | 📐 Blocked on operator-provided Hetzner credentials + Group C cutover merge | — |

## Architectural compliance (Lesson #24 closed)

- **OpenTofu** owns Phase 0 — `infra/hetzner/{versions,variables,main,outputs}.tf` + `cloudinit-{control-plane,worker}.tftpl`.
- **Crossplane** is the day-2 IaC — 4 XRDs + 4 Compositions at `platform/crossplane/compositions/` under canonical `compose.openova.io/v1alpha1` group.
- **Flux** is the GitOps reconciler — `clusters/_template/` + `clusters/omantel.omani.works/` with 11 HelmReleases in dependency order via `dependsOn`.
- **Blueprints** are the install unit — 11 cosigned `bp-<name>:1.0.0` OCI artifacts at `ghcr.io/openova-io/`. CI fan-out via `.github/workflows/blueprint-release.yaml`.
- **Inviolable principles** anchored in `docs/INVIOLABLE-PRINCIPLES.md` + `~/.claude/.../memory/feedback_inviolable_principles.md` + global CLAUDE.md.

## What still needs to happen for DoD

1. **Operator confirms Group C cutover** in openova-private + runs `kubectl annotate gitrepository/{flux-system,openova-public}` on Contabo. (~5-min outage, fully reversible.)
2. **Operator provides real Hetzner Cloud API token + project ID** to the wizard at `https://console.openova.io/sovereign`.
3. **Operator provides SSH public key** (StepCredentials).
4. **Operator picks domain** — pool `omani.works` → subdomain `omantel`.
5. **Click Provision.** Wizard POSTs to `/api/v1/deployments`. catalyst-api runs `tofu init && tofu apply`. Cloud-init bootstraps k3s + Flux + GitRepository pointing at `clusters/omantel.omani.works/`. Flux reconciles 11 HelmReleases in dependency order. ~10 minutes total.
6. **DNS** auto-writes `*.omantel.omani.works → <LB-IP>` via Dynadot (catalyst-dns helper invoked by `null_resource.dns_pool` in tofu).
7. **TLS** auto-issued via cert-manager Let's Encrypt DNS-01 (Group G #113 if completed; otherwise HTTP-01 fallback).
8. omantel-admin logs into `console.omantel.omani.works`, issues a voucher via `/admin/billing`.
9. Tenant redeems at `omantel.omani.works/redeem?code=...`, creates Org, installs first App.

## Active parallel work

- `group-g-dns-multi-domain` — agent finishing #110, #111, #112, #113.
- (Bulk-close agent for A/B/D/E/F has run; closures are reflected in the count above.)

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
