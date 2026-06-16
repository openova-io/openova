# #3375 TOPOLOGY / DR — one canonical vocabulary, built + region-kill-proven

> **Scope of THIS document.** #3375 is a large, multi-surface ticket
> (10 DoD boxes spanning the Go control plane, 7+ charts, the UI, and a
> live region-kill). This walkthrough tracks the **foundational
> control-plane spine** delivered in the PR `feat: #3375 topology/DR one
> vocabulary spine` — the pure-code, fully-unit-tested vertical that the
> remaining chart + live-walk work builds on. The live region-kill walk
> (DoD-9) and the cross-region chart wiring (DoD-8) follow on a fresh
> 2-region prov; they are enumerated under **Remaining for the live
> walk** below, not claimed here. No live ✅ is fabricated for an
> unwalked surface (memory `feedback_each_new_env_flushes_all_evidence`).

## What the spine delivers (this PR)

| # | DoD box (issue §9) | Delivered in the spine | Evidence |
|---|---|---|---|
| 1 | One vocabulary end-to-end | `fleet.api.ts` `TopologyMode` is now the canonical 4-class set; a `canonicalizeTopologyMode` routes every topology post site (the fleet filter + the CrossSovereign picker) through the one vocabulary; the `TopologyEditor` now offers **`active-passive`** (it could not before, §3(d)) so all 4 canonical classes are selectable. | `useFleet.test.ts`, `TopologyEditor.test.tsx`, `CrossSovereignView.test.tsx` (21/21 green); `npm run build` clean |
| 2 | One placement model (`replicas:0` on the fanned-out passive HR) | The topology fan-out (`render/fanout.go`) — the SOLE model — now applies the `replicas:0` + `_openova_standby:true` standby scale-down to a passive cluster's HR, instead of rendering it byte-identical to the active HR while a parallel path computed the scale. | `TestFanoutHRs_PassiveHRCarriesReplicasZero`, `TestFanoutHRs_SingletonNotScaledDown` (`-race` green) |
| 5 | Switchover is real — **generality proof** (stateless promoter) | A generic `stateless` Promoter is registered in the switchover engine (`promoter.go` + `sequence.go` Validate); its state-store steps are no-ops, the 5 mechanism-agnostic steps carry the whole switchover. **Zero app-name literal** — the SAME engine drives sso-bridge, livekit, vllm. | `stateless_test.go` (6 tests incl. the generality + no-op proofs, `-race` green) |
| 7 | Integrity gate refuses a half-built topology — **generality proof** | A generic `DeclaredDRStandbyIntegrity` gate (`provisioner.go`) wired into the Phase-1 watch (`phase1_watch.go`) downgrades an otherwise-`ready` active-hot-standby deployment to **`failed` / `standby-region-absent`** when the standby region never came up (the hw150 lying-flag). Keys on `bcpTopology` + region counts — **zero app-name literal**; leaves the slow-secondary surface path untouched. | `dr_standby_integrity_test.go`, `phase1_dr_integrity_test.go` (`-race` green) |
| — | Decorative-declaration enforcement (§5.3) | The blueprint validator now flags a `replication.backend` that names a cross-region state store (`cnpg-pair`, …) when the blueprint neither binds a `backingServices[]` nor is itself a data-instance provider — the grafana decorative hole. Advisory (surfaced on `status.conditions`), generic, name-free. | `TestValidate_DecorativeReplicationBackend_Flagged` + 3 exemption tests (`-race` green) |

### Automated test evidence (NOT acceptance — the appendix)

Per `feedback_uat_doc_must_be_ui_walk.md`, these are unit/golden gates, not a UI walk:

```
core/controllers $ go test -race ./continuum/internal/switchover/... \
    ./application/internal/render/... ./blueprint/internal/validate/...   # ok
products/catalyst/bootstrap/api $ go test ./internal/provisioner/ ./internal/handler/  # ok
products/catalyst/bootstrap/ui  $ npm run build                                        # ok (tsc + vite)
products/catalyst/bootstrap/ui  $ npx vitest run src/lib/useFleet.test.ts \
    src/widgets/topology/TopologyEditor.test.tsx \
    src/pages/dashboard/CrossSovereignView.test.tsx                                     # 21/21
```

## Walkable on a fresh 2-region prov after this rolls (the UI half)

> Every row is one UI action. Run after a fresh 2-region prov picks up the rolled catalyst-api/ui image.

| Go to (URL) | Then do (click / type) | You should see | Result |
|---|---|---|---|
| `/app/<grafana>` → Topology tab | Read the **Topology mode** radios in the placement editor | **Four** radios — `single-region`, `active-active`, `active-hotstandby`, **`active-passive`** (new) | ☐ |
| `/dashboard` Cross-Sovereign view | Open the **topology** filter dropdown | Options are the canonical vocabulary — Singleton / Active-Active / **Active-Hot-Standby** / **Active-Passive** | ☐ |
| `/dashboard` Cross-Sovereign view | Select **Active-Hot-Standby**, watch the network tab | The request carries `topology=active-hot-standby` (canonical), never `active-hotstandby` | ☐ |
| `kubectl get hr -A -l catalyst.openova.io/app=<hot-standby-app>` | Diff the active vs passive HR `spec.values` | The **mgmt-B (passive)** HR carries `replicas: 0` + `_openova_standby: true`; the mgmt-A (active) HR does not | ☐ |
| Provision active-hot-standby with the standby region capped | Read the deployment record + Sovereign Settings | Record is **`failed`**, reason `standby-region-absent`; **no** green active-hot-standby badge | ☐ |

## Remaining for the live walk (NOT in this spine — follow-on)

These are the issue's larger items that need a fresh 2-region prov and/or the
per-app Continuum producer; they are explicitly **not** claimed by this PR:

- **DoD-3 / DoD-4** — the application-controller (or a slot) minting **one
  `Continuum` CR per DR-capable Application** + the journey building the
  per-app 2-region cnpg-pair (the backing-service generator emitting a
  replica Cluster + externalClusters WAL). The spine makes the engine
  *able* to drive these (stateless promoter + the `replicas:0` model + the
  enforcement that rejects decorative backends); the producer itself is the
  next increment.
- **DoD-6** — the `TopologyTab.tsx` live-DR gate (render the DR section +
  Switchover only when the API reports a live backing; bind live
  replication lag). Meaningful only once DoD-3 produces per-app Continuum
  CRs to read.
- **DoD-8** — the cross-region chart wiring (`<instance>-mesh-rw`, the
  openbao S3 cred mirror, the guacd emptyDir fallback). Chart work,
  walked on a 2-region env.
- **DoD-9** — the live region-kill within RTO/RPO on a healthy 2-region
  prov. Needs DoD-3/4/8 + a converged 2-region env.

Consume, do not re-build: the per-app Application-CR write spine is **#3687**
(this ticket consumes it).
