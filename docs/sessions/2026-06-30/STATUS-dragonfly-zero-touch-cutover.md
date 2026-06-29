# STATUS — Dragonfly zero-touch cutover (omantel.biz / kom4dc)

**As of 2026-06-30, live-validated on prov `fbf043d2` (no fresh prov burned).**

## Goal
Drive the Sovereign to a **deterministic zero-touch cutover** (Pillar 5): provision →
converge → handover → **auto-cutover** → `cutoverComplete`, 100% no manual touch, on the
no-hairpin kom4dc cloud. The blocker was the cutover registry-pivot wedging because kom4dc
nodes can't hairpin to their own public EIP. Fix = **Dragonfly** P2P (per-node dfdaemon
mirror at `127.0.0.1`), per founder direction.

## THE breakthrough (this session)
**The Dragonfly registry-pivot WORKS on kom4dc — the hairpin is dead.** Manual-triggered
cutover on the live converged env walked steps 1–6 green:
`registry-pivot` ✅ → `registriesYaml` flipped **v1→v2**, dfdaemon upstream flipped
**ghcr→local**. This is what NEVER worked with the hand-rolled hairpin for ~200 provs.

## WBS / Gantt

```
            ◀──────────────── COMPLETE ────────────────▶│◀ now ▶│
0  Design (cloud-agnostic/DR/isolation/blast-radius)      ████████        ✅
1  bp-dragonfly chart (slot 06)                               ████████    ✅ #4640 merged
2  Validate Dragonfly DEPLOYS on kom4dc                          ██████   ✅ #4639
2b Defuse 5/5 VPC quota landmine                                  ████    ✅ 1/5
3  Cutover registry-pivot → dfdaemon mirror + hostAlias            █████   ✅ #4641 merged
4a Live-prove the Dragonfly registry-pivot (steps 1–6, regYaml v2) ████   ✅ PROVEN
4b ADR-0012 + DoD Sandbox→Agenity correction                       ████   ✅ #4643 #4647
─────────────────────────────────────────────────────────────────────────────────────
5a Auto-fire gate root-cause (QA_FIXTURES_ENABLED never wired)      🔄 #4648 (CI green-verified local)
5b dfdaemon Kyverno readOnlyRootFilesystem DENY → DS=0 pods         🔄 fix in progress (#4640 follow-up)
6  Re-prov clean kom4dc qaTestEnabled → AUTO-cutover → COMPLETE     ⏭️  ◀ final acceptance
```

## Two remaining bugs (both caught by live validation, no wasted prov)

**5a — auto-fire gate (FIXED, statically verified):** `QA_FIXTURES_ENABLED` was a
*documented but never-assigned* postBuild-substitute var → `qaFixtures.enabled` (and thus
`CATALYST_FIRE_CUTOVER_ON_HANDOVER`, #4642) always rendered `false` on **every** cloud →
the cutover never auto-fired anywhere. Fix #4648: declare the var on Huawei + thread it
through all `templatefile()` callers + add the substitute key. Proven with `tofu validate`
+ a render test (emits `QA_FIXTURES_ENABLED: "true"`) + both CI render gates run locally
(exit 0). **Baked cloud-init → inert until catalyst-api image rebuilds.**

**5b — dfdaemon Kyverno DENY (fix in progress):** the `dragonfly-client` DaemonSet is
DENIED by the cluster's `readOnlyRootFilesystem=true` ENFORCE policy → DESIRED=4 CURRENT=0.
With zero dfdaemon pods, the node's `127.0.0.1:4001` mirror is dead → catalyst-api re-pull
(cutover step-07) gets `connection refused` → ImagePullBackOff → wedge at 54%. Fix:
bp-dragonfly chart sets `readOnlyRootFilesystem=true` + emptyDir mounts for dfdaemon's
writable paths (socket/cache/log) + OTel annotation. (Also Audit-flagged: dfdaemon images
come from docker.io, not the local Harbor — `harbor-proxy-pull` will matter post-cutover.)

## Pillar 4 correction (founder, 2026-06-30)
**Sandbox is dead — replaced by Agenity.** Per-Org Agenity workspace + `bp-openova-mcp`
(SSO/RBAC-scoped `agenity-mcp-bearer`). Canonical DoD doc swept (#4647); Sandbox menu to be
removed.

## What's left for the deterministic zero-touch proof
1. Land 5a (#4648) + 5b (dfdaemon Kyverno fix) → catalyst-api + bp-dragonfly images rebuild.
2. **One** clean kom4dc `qaTestEnabled` prov on the full train → converge → handover →
   **auto**-cutover (5a opens the gate) → registry-pivot (proven) → catalyst-api re-pull via
   a now-Kyverno-compliant dfdaemon (5b) → `cutoverComplete` through the 600s egress hold.

## Process change (founder demand: "verify basics before the cycle")
- Trace the full chain statically + render-prove the expected value **before** any prov.
- Validate mechanisms on an already-converged live env (manual trigger) before re-proving.
- Run the actual CI gates locally before pushing (caught the missing-var render-harness gap
  before merge, not after a wasted walk).
