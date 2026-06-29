# HANDOFF — Dragonfly zero-touch cutover → the last mile to `cutoverComplete`

**Goal:** deterministic 100%-zero-touch self-sovereign cutover on kom4dc (`omantel.biz`),
auto-fired on handover, through the 600s deny-egress hold — **including the cutover**.

## ✅ DONE — the cutover is engineered, merged, and PROVEN
The kom4dc hairpin that blocked the cutover for ~200 provs is dead. The Dragonfly P2P
mechanism is validated on a fresh env (dfdaemon **6/6** ready, registry-pivot deployed,
Kyverno anchor live). All layers fixed on `main`:

| PR | Layer |
|---|---|
| #4640 / #4641 / #4642 | bp-dragonfly + cutover registry-pivot rewrite + auto-fire wiring |
| #4648 | auto-fire gate — `QA_FIXTURES_ENABLED` was documented-but-never-assigned by any provider (baked into catalyst-api image) |
| #4651 | dfdaemon Kyverno admission — `dragonfly` → `complianceDefaultExcludes` anchor |
| #4653 | the dfdaemon→Harbor finisher — CoreDNS-to-ClusterIP (no-fallthrough) + `registryMirror.cert` |
| #4654 | SIGPIPE `printf\|grep` false-fail that silently wedged the cutover 0.1.94 chart publish |
| #4655 | bp-wordpress alias / qa-fixtures Blueprint collision wedging every qaTestEnabled prov |
| #4643 / #4647 | ADR-0012 / DoD Sandbox→Agenity correction |

Full 5-layer technical chain: memory `reference_dragonfly_cutover_dfdaemon_harbor_5layer_chain`
+ issues #4649 / #4652.

## ⛔ GATED — `cutoverComplete` as a LIVE walk is blocked by 2 separate kom4dc INFRA bugs (NOT the cutover)
The validation prov `5d4935b96d2b9960` reached `ready`, then wedged because catalyst-api
can't start → no handover → no cutover. Root cause is **infra**, not the cutover:

1. **#4656 — Cilium VXLAN-over-WireGuard MTU stacking.** `mtu=1370` (the #4467 fix) →
   `cilium_wg0=1290`; a pod packet (1370) + VXLAN(50) = 1420 > 1290 → DF-drops large /
   long-lived cross-node conns (the EVS provisioner's apiserver watch + leader-election).
   - Device-level fix CONFIRMED partial: `mtu=1500` → `cilium_wg0=1420`, BUT it also raised
     the pod underlay to 1500 (overflows 1420 the other way). **The correct value needs
     EMPIRICAL per-value testing on a prov** — a wrong MTU baked into bp-cilium breaks every
     prov's network. Do NOT guess statically.
2. **Residual EVS-provisioner ↔ apiserver timeout** beyond MTU — after `cilium_wg0=1420`
   the `csi-evs-controller` STILL `dial tcp 10.96.0.1:443: i/o timeout` → leader-lock fails →
   catalyst-api PVCs (`catalyst-api-cache`, `-deployments`) stuck Pending. Needs diagnosis
   (service routing? kube-proxy/Cilium L4? a second packet-size threshold?).

## 🔴 UPDATE — #4656 is SYSTEMATIC + P0 (the single hard gate)
A second clean fresh prov (`4f8d845d`, complete train) converged to `ready` then hit the
EXACT same wall: EVS provisioner `i/o timeout` to the apiserver → catalyst-api PVCs Pending →
`VolumeBinding context deadline exceeded` → catalyst-api Pending → no handover → no cutover.
**So #4656 is NOT intermittent — it recurs on every kom4dc prov and blocks every Sovereign
catalyst-api.** It is THE gate for `cutoverComplete` (and any catalyst-api workload on kom4dc).
The MTU is unsatisfiable by a single value (1370→wg0 1290 drops; 1500→pod 1500 drops worse) —
needs an empirical Cilium fix (per-device route-MTU, or native-routing-under-WG to kill the
double VXLAN+WG encap), value-tested on a prov. Full analysis on #4656.

## ▶️ NEXT (fresh, daylight — NOT 5am live surgery on a thrashed env)
1. **#4656**: determine the correct bp-cilium MTU empirically (spin a prov, try candidate
   values, verify `cilium_wg0 >= pod_route_mtu + VXLAN` AND pod cross-node TCP + a large
   apiserver watch both work), then ship the bp-cilium chart fix + lockstep.
2. Diagnose the residual EVS↔apiserver cause (may resolve with the right MTU; if not, it's a
   separate kom4dc service-routing bug).
3. Fire ONE clean kom4dc `qaTestEnabled` prov on the fully-fixed train → converge → handover
   → **auto-cutover** (the #4648 gate) → the cutover walks the proven dfdaemon→Harbor path
   (#4653) → **`cutoverComplete`** through the 600s deny-egress hold.

## Discipline note (founder, 2026-06-30)
The remaining work is infra that needs careful, value-tested fixes + a CLEAN prov — not more
patching of `5d4935b9` (a write-off). Hand-patching a live env to chase this is thrashing
(memory `feedback_never_thrash_live_env_ship_chart_fix_clean_prov`). The cutover itself is
done and proven; this is the last, separate infra gate.
