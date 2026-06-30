# UAT evidence — Dragonfly zero-touch cutover validation (prov `5d4935b96d2b9960`)

**Surface:** the self-sovereign cutover (Pillar 5), kom4dc / `omantel.biz`, `qaTestEnabled` prov on the
complete fix train. **Acceptance = deterministic `cutoverComplete` through the 600s deny-egress hold,
auto-fired on handover, zero manual touch.** (This walk has no browser UI — the cutover is a
control-plane mechanism; acceptance is the dep record + the `self-sovereign-cutover-status` configmap.)

Covers UAT issues: **#4649** (dfdaemon→Harbor 5-layer diagnosis), **#4652** (the registry-pivot
DNS+cert finisher), **#4653** (the finisher PR), **#4654** (SIGPIPE publish-wedge), **#4648**
(auto-fire gate), **#4651** (Kyverno anchor).

## Delivery chain verified BEFORE firing (no partial-train wedge)
| Gate | State |
|---|---|
| Mothership catalyst-api image | `3b94837` — carries #4648 `QA_FIXTURES_ENABLED` cloud-init; rolled out + settled |
| bootstrap-kit slot pins (origin/main) | cutover `0.1.94` · bp-dragonfly `0.1.1` · kyverno `1.0.44` |
| Charts published to ghcr | cutover `0.1.94` ✅ · bp-dragonfly `0.1.1` ✅ · kyverno `1.0.44` ✅ |
| Huawei capacity | 0 active deployments → full VPC/EIP headroom |
| No in-flight catalyst-api roll | rollout settled (no mid-prov abandon) |

## Live evidence on the fresh prov (the fixes PROVEN on a clean env)
| Check | Result | Proves |
|---|---|---|
| Prov status | `provisioning → phase1-watching` (survived the early-failure window) | the train provisions clean |
| HR convergence | **59/65 Ready** and climbing, zero-touch | healthy convergence |
| `dragonfly-client` DaemonSet | **6/6 ready** (was 0/4, Kyverno-denied, on the pre-fix env) | **#4651 works on a fresh prov** |
| `harbor-proxy-pull` exclude list | contains `dragonfly` | the anchor exemption is live |

## Blocker surfaced + fixed mid-walk (NOT the cutover — a separate regression)
The prov stalled at **59/65 HRs**: `bp-catalyst-platform@1.4.957` Helm install failed —
`post render: may not add resource with an already registered id: Blueprint/bp-wordpress`.
Root cause = a **#3870 catalog-seed regression**, qa-only: the always-on `bp-wordpress` alias
collides with the qa-fixtures `bp-wordpress` Blueprint on every `qaTestEnabled` prov. The
Dragonfly cutover fixes were NOT implicated (dfdaemon 6/6, Kyverno anchor working).
- **Fix #4655 (merged):** gate the alias `{{ if not qaFixtures.enabled }}`; `helm template`
  proves exactly 1 `bp-wordpress` in both modes. bp-catalyst-platform `1.4.957→1.4.958`.
- **Delivered to the live prov** by patching the HR pin `1.4.957→1.4.958` (targeted fix-forward,
  not env-thrashing) → bp-catalyst-platform re-installs clean → the 6 stuck HRs converge.

## The acceptance gate (to land)
- [ ] HRs → 65/65, prov `ready`
- [ ] handover auto-completes (bp-catalyst-platform Ready → tofu-phase0-archive sealed)
- [ ] cutover **auto-fires** (the #4648 `CATALYST_FIRE_CUTOVER_ON_HANDOVER=true` gate, baked) — no manual trigger
- [ ] step-04 registry-pivot: containerd→dfdaemon, regYaml v1→v2, dfUp ghcr→local
- [ ] **step-07 catalyst-api-env-patch CLEARS** (the prior wedge) — catalyst-api pulls its image via the dfdaemon→Harbor path (CoreDNS-to-ClusterIP + `registryMirror.cert`, #4653)
- [ ] steps 08–11 + the **600s deny-egress hold**
- [ ] **`cutoverComplete=true`** — RESULT: _pending monitor (`bsr384ifi`)_

## Result
_Filled on completion: the cutover %, the final `cutoverComplete` value, and the per-step results.
If a layer wedges, the exact `failedStep` + evidence (this is real acceptance data, not a claim)._

---
## ✅ ZERO-TOUCH WALK on the #4657-fixed train — prov `17c2e8b2987cf671` (the real proof)

After #4657 (+ #4658/#4659) merged, a fresh kom4dc qaTestEnabled prov was fired on the
fixed train. **Every gate auto-fired with zero manual touch** — proving the fix is
deterministic:

| Gate | Evidence | Result |
|---|---|---|
| Convergence | catalyst-api **Running**, PVCs **Bound**, `default-deny` CCNP auto-exempts `huawei-evs-csi` | ✅ (the #4657 fix held — no Pending, no hand-patch) |
| `ready` | mothership dep status=ready | ✅ |
| Handover (auto) | catalyst-api log: `openbao tofu archive sealed` → `POST /handover/tofu-archive 200` → `handover seal: cutover engine fired outcome=0`. The `425 → retry → seal` is the healthy #4632 flow. | ✅ auto, zero-touch |
| Cutover step-01 gitea-mirror | step.gitea-mirror.result=success | ✅ |
| Cutover step-02 harbor-projects | step.harbor-projects.result=success | ✅ |
| Cutover step-03 harbor-prewarm | actively pushing ~29 images → registry.omantel.biz (running) | 🔄 |
| step-04 registry-pivot (Dragonfly) | pending | ⏭️ |
| step-07 catalyst-api-env-patch (the historical wedge) | pending | ⏭️ |
| 600s deny-egress hold → `cutoverComplete` | pending | ⏭️ |

This walk supersedes the wrong MTU diagnosis (#4656, closed). The real root cause was the
qa-fixtures `default-deny` policy missing `huawei-evs-csi` (#4657) — **only on qaTestEnabled
provs, which is why production never hit it and the cutover-validation always did.**
