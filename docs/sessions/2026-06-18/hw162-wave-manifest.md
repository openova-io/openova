# hw162 wave manifest — status/uat triage (2026-06-18)

**Question per issue:** does it need a CODE FIX before/in this wave (⇒ hw162 is NOT final),
or is it **walk-verify** (the hw162 browser walk decides pass/fail)? **Result: every one is
already on the train or walk-verify — no pre-walk code fix outstanding.** hw162 is the candidate
final run for all 22; any FAIL the walk surfaces generates the *next* wave (the walk determines it,
it cannot be pre-decided for a walk-item).

`FIX@MAIN` = the commit on `origin/main` that lands the fix (so it's on hw162's train).

## CONVERGE-class (could block hw162 coming up — must be on the train)
| Issue | Determination | FIX@MAIN / note |
|---|---|---|
| #3731 raw-ExternalSecret deadlock | on train (THIS wave) | Fix C `bootstrap-kit-crs` in #3806 → `0f9543b91` |
| #3763 gitea-flux-auth-sync hook crashloop | on train | `1fda13419` (polls PAT, no FATAL-on-empty) |
| #3734 cert-manager InstallFailed loop | on train | `e8635e63a` |
| #3735 source-controller self-DoS (IPv4 kom4dc) | on train | `855f1d251` |
| #3736 SHARED_PG keycloak blocker | on train + **N/A** | `e8366fa30`; gated on `SHARED_PG=true`, hw162 fires standard |
| #3726 github-IPv4 node pin | on train | `6fdd6cc42` |
| #3720 catalyst-build cloud-init >32256 | on train | `6fdd6cc42` |
| #3724 make 32256B size-gate a *required* check | **defer (config, non-code)** | GitHub branch-protection setting; the gate already RUNS (it caught #3806's overflow) |

## walk-class (fix merged → the hw162 walk VERIFIES, does not re-code)
| Issue | FIX@MAIN |
|---|---|
| #3785 FUNNEL WordPress kyverno-deny | `ac8e023e0` |
| #3747 handover export 5-min budget | `5e95cead6` |
| #3741 powerdns-admin realm-seed | `5e95cead6` |
| #3740 cnpg cross-region replica async | `a922666bc` |
| #3722 nat-eip poisoned-pool drain | `d12944132` |
| #3716 nat-eip-preflight wiring | `d12944132` |
| #3687 Organization/Application CR LIVE | `aab306416` |
| #3668 catalog single-source IaC | `015a29e3c` |
| #3646 jobs one honest canvas | `7b8edcfeb` |
| #3642 NS#1 migrate 7 apps → mgmt vCluster | `9a854cf5b` (+ this wave's host-ns/CRS fixes) |
| #3379 cutover cutoverComplete durable | `fba7fe48f` |
| #3376 funnel voucher→signed-in own org | `6c11bc7b2` |
| #3375 topology/DR vocabulary | `6f864ac81` |
| #3374 SSO zero-login everywhere | `5d818d4cf` |

**Conclusion:** wave fully loaded; 21/22 fixed on main or in #3806, #3724 is a non-code config defer.
hw162 walk is the verification gate — it produces the pass/fail per walk-item and any next-wave work.

---

## hw162 convergence result (live, 03:05Z) — DEADLOCK BROKEN ✅

The A+B+C train fixed the hw161 env-killer. Verified live on hw162 (`4d61885518c38a4d`):

- **Fix A (CoreDNS `.server`)** ✅ — `GitRepository openova` = **Ready/True**; github resolves + clones (the exact path that crash-froze hw161's CoreDNS → 0 kube-dns endpoints → 0 HRs is gone).
- **Fix C (bootstrap-kit-crs dependsOn tier)** ✅ — kustomize-controller logged "Dependencies do not meet ready condition, retrying" for `bootstrap-kit-crs` + `sovereign-tls` — the ordering works as designed (CRD-dependent CRs wait for bootstrap-kit + its operators).
- **The atomic-dry-run deadlock is gone** — bootstrap-kit reconciled and created **64 HelmReleases** (0 → 64) the moment kustomize-controller came up. hw161 was permanently stuck at 0; hw162 is flowing.

**Caveat (separate, known, non-fatal #809):** flux controller images pull slowly through the harbor proxy — `kustomize-controller:v1.4.0` took **9m59s** to pull, delaying first reconcile ~26 min into phase-1. Slow, not wedged. The 64 HRs are now installing (operators → CRDs → bootstrap-kit-crs CRs → apps); readiness watcher fires the walk at HRs-Ready ≥ 50.
