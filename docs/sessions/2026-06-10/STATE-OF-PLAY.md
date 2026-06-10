# OpenOva — State of Play (honest assessment, 2026-06-10)

> One artifact to close the gap between **ground reality**, the **agent view**, and the **human view**.
> Updated by the operating agent at the end of each working block. Pair with `docs/ledger/UAT.md` (the click-by-click walk checklist that is the *acceptance* truth).

## TL;DR — the blunt bottom line

- **NOT done. 0 pillars walked. DoD ≈ 28%** (operator-walked-fresh-prov standard — PR-merge ≠ walked).
- The keystone 2-VPC prov (hw125) **FAILED to converge** — but the failure was *diagnostically decisive*: it surfaced and let us fix, at source, the persistent Pillar-3 multi-region blocker that had silently killed every prior multi-region prov.
- This was a **diagnose-and-fix block**, not a walk block. The walks are the *next* phase and are now mechanically structured.

## What is tangible (verifiable — anti-theater)

| Deliverable | State |
|---|---|
| PR #3233 — IPv4-pin bootstrap hosts (the IPv6 CNI killer fix) | open/green |
| PR #3234 — ES webhook-gate timeout (the convergence-cascade fix) | open/green |
| PR #3231 — OpenBao zero-click server-side shim | open/green |
| PR #3229 — gate dead AppDetail "Open" button | open/green |
| PR #3230 — cloud-init dash-lint CI gate | **merged** |
| PR #3227 — pdns → pdns-admin redirect | **merged** |
| Issues closed (static-verified) | #2931 #2930 #2872 #2864 #2851 #2740 #3129 |
| Root cause pinned | IPv6 helm-repo multi-region CNI killer (memory + #3232) |

## Where we are — evidence-based completion (from the 19-agent census)

| Pillar / EPIC | % | Walked? | The ONE blocking gap |
|---|---|---|---|
| Pillar-1 Marketplace/voucher/BSS | 62 | front-half only | checkout→Org→tenant-login back-half unwalked |
| Pillar-2 BCP topology at signup | ~60 | partial | needs a converged 2-region prov |
| **Pillar-3 region-kill DR** | **~25** | **no** | **was: region-b CNI dead (IPv6). NOW FIXED at source (#3233). Needs ClusterMesh + cnpg-pair on a clean re-prov.** |
| Pillar-4 Sandbox + MCP | 42 | no | needs a live Sandbox CR for an Org |
| Pillar-5 cutover (egress-hold) | 45 | no | the 10-min deny-egress hold has never run |
| EPIC-0..6 | 35–78 | mixed | mostly code-complete, unwalked |
| Crossplane handover | 35 | no | not reconciling day-2 XRs (still tofu) |
| vCluster isolation | 35 | no | host-pods vs vcluster containment unproven |

## What THIS session actually changed (be precise)

- **Cracked the #1 deficit's root cause** (Pillar-3 multi-region): the secondary VPC's `helm repo add cilium` hit IPv6 on an IPv4-only Huawei VPC → no CNI → no ClusterMesh. Intermittent per region = why it was never pinned. Validated by salvaging region-b live (4/4 Ready).
- **Collapsed the convergence cascade to one root**: the ES webhook-gate's 20m poll > the HR's 15m Helm timeout stalled ES → stores → secrets → gitea-sso → catalyst-platform. Fixed (#3234).
- **Did NOT walk any pillar.** The hw125 prov is `failed` (the wedges, now fixed at source but not yet re-deployed).

## Remaining work to 100% — structured, executable by a regular-effort agent

1. **Merge** #3233 + #3234 (convergence fixes; no catalyst-api roll) and #3229 + #3231 (catalyst-api; sequential, expect a mothership roll).
2. **Ship #3236** — cnpg-pair wiring: have catalyst-api `AutoEstablishClusterMesh` flip `SOVEREIGN_ENABLE_CNPG_PAIR=true` **after** it confirms the mesh (zero-touch, correctly gated — never enable-before-mesh, the #3196 anti-pattern).
3. **Clean re-prov** (founder-gated wipe): `bash scripts/sovereign-lifecycle.sh wipe <id>` → poll 404 → ~15m VPC-free → `fire hw126 omantel.biz`.
4. **Monitor + verify**: convergence to all `bp-* Ready`; ClusterMesh auto-establishes with both regions healthy.
5. **Walk** Pillars 1/2/3/4/5 + vCluster containment, recording each row in `docs/ledger/UAT.md` with a screenshot.

## Runbook — the proven techniques (so a regular agent needs no re-discovery)

- **Fire/wipe (mechanically safe)**: `scripts/sovereign-lifecycle.sh` (auto reset-uat-on-fire + capture-before-wipe). Auth: mothership JWT at `/tmp/hw-priv.pem`.
- **kubectl on a Sovereign**: `kubectl -n catalyst exec <catalyst-api-pod> -- cat /var/lib/catalyst/kubeconfigs/<deployment-id>.yaml` (primary) + `<id>-me-east-215-b-1.yaml` (region-b). The Sovereign API EIP (`:6443`) is reachable from the bastion; SSH (`:22`) is NOT.
- **Secondary-region host forensic (no SSH)**: host-network + `tolerations:[{operator: Exists}]` + privileged hostPath `/` pod → `cat /host/var/log/cloud-init-output.log`.
- **Convergence check**: `kubectl get hr -A` — non-`True` `bp-*` are the wedges; read `.status.conditions[].message`.
- **Salvage a CNI-dead region (last resort, contaminates zero-touch)**: `helm install cilium cilium/cilium --version <match region-a> -f <region-b cilium-values> --kubeconfig <region-b>` from the bastion (IPv4).

## Can a regular-effort Opus 4.8 agent take over from here? **YES — for the structured execution.**

The high-effort part (multi-angle diagnosis, creative forensics, root-causing the intermittent killer) is **done**. What remains is execution of a *known* plan with *proven* techniques, plus the one founder-gated decision (when to fire the wipe). A regular agent following steps 1–5 above + the runbook can carry it to walked pillars without ultracode or workflows.

## How to track progress (the two artifacts, together)

- **`docs/ledger/UAT.md`** = ground-truth acceptance. Each row is one UI click; flips ☐→✅ only when walked live with a screenshot. This is the *real* progress bar.
- **This file** = the strategic view (what's shipped, what's left, the runbook). Update it at the end of each block.

## Execution model for the remaining work (decided 2026-06-10, founder-confirmed)

**Spine = ONE orchestrator agent holding live state** (deployment IDs, kubeconfigs, wedge history), routing each piece of work to the cheapest model that does it reliably. Per-spawn `model` is set explicitly on every dispatch — never default-blind.

| Work type | Route to | Example |
|---|---|---|
| Judgment / design / novel-wedge forensics | orchestrator itself (or a strong-model subagent for trap-laden implementation) | #3238 cnpg gate (briefed with the design decided + the #3196 anti-pattern guard) |
| Mechanical multi-step | `model: opus` subagent | re-prov watch, UAT evidence capture, merges |
| Bulk reads / inventories | `model: haiku` / Explore | repo sweeps |
| Trivial one-liners | inline, no agent | CI re-runs, label flips |

**Workflows** (script-managed agents): only for known-in-advance, parallel, stateless breadth — the one remaining fit is the **5-pillar UAT walk fan-out after the prov converges** (5 Opus agents, one per pillar). **Agent team**: not justified until post-100% standing tracks.

**The anti-theater invariant that makes any mechanism safe: no step counts as done without a clickable artifact** (merged PR, HR Ready output, UAT row + screenshot). Trust no agent report without re-querying live state (CLAUDE.md L7).
