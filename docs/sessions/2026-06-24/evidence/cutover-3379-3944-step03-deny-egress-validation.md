# Cutover #3379 / #3944 — step-03 prewarm completeness + deny-egress mechanics validation

**Date:** 2026-06-24
**Env:** permanent omantel.biz Sovereign, dep `4635277cae4ffed9` (me-east-215, Huawei)
**Branch:** `fix/3379-3944-cutover-validate`
**Chart:** bp-self-sovereign-cutover 0.1.77 → **0.1.78**

## Context — what was already merged

The full #3379 five-face integrity layer landed on `main` in PR #3699 (commit
`b13a4ff3f`, chart 0.1.74):

- **Face 1 (durable fact)** — `sealCutoverComplete` writes
  `secret/catalyst/cutover-complete` in OpenBao in `runCutover`'s success tail;
  `spawnCutoverEngine` + `ResumeInterruptedCutover` check the seal BEFORE the CM
  key; `09-cutover-status-configmap.yaml` is init-only (lookup guard) so a
  `helm upgrade` never re-asserts `false`.
- **Face 2 (faithful pivot)** — `registriesYamlActive=v2` flipped the moment
  `harbor-prewarm` succeeds; step-04 counts per-node `node.<name>.registriesYaml=v2`
  acks; `runCutover` REFUSES `cutoverComplete=true` while `registriesYamlActive != v2`.
- **Face 3 (true deny-egress)** — `08-egress-block-test-job.yaml` emits a
  `CiliumClusterwideNetworkPolicy` with `enableDefaultDeny.egress:true` + an
  intra-cluster allow-list (toCIDR 10.42/10.43/10.96, toEntities
  kube-apiserver/host/remote-node/cluster, DNS toPorts) — NOT a 4-CIDR
  subtractive block — plus an active call-home assertion that curls
  console.openova.io + mothership hosts FROM a pod under the hold and FAILS the
  proof if any connects.
- **Face 4 (audit fidelity)** — `cutoverStartedAt` written ONCE (prior-status
  guard); resume advances a separate `cutoverLastAttemptStartedAt`.
- **Face 5 (host loop + zero residual)** — step-10 MERGES the sso-bridge image
  override into the existing `values:` mapping with yq; step-08 hard-gates on
  bootstrap-kit/sovereign-tls/sme-tenants Ready and rolls every external-registry
  workload.

The #3944 step-03 `--command-timeout` fix landed in PR #3945 (commit `3381ac30a`,
chart 0.1.77).

## Gap this session closes — #3944 root-cause-2

The merged 0.1.77 fix only addressed #3944 root-cause-**1** (`--command-timeout`).
Root-cause-**2** — *"post-#3642, host `kubectl get pods -A` cannot see in-vCluster
openova-io images"* — was NOT addressed. step-03 Phase A still enumerated the
openova-io image set from RUNNING Pods only.

A running-Pod-only enumeration is incomplete on two paths:
- (a) a vCluster NOT in pod-syncing mode never materialises its pods in a host
  namespace, so a host-side `get pods -A` can't see them;
- (b) even with pod-syncing, an openova-io workload that is momentarily down /
  scaled-to-zero / not-yet-rolled-out (a CronJob between fires, an HR
  mid-reconcile) has no running Pod, so its image is missed.

A missed image never lands in local Harbor → post-cutover ImagePullBackOff the
moment step-06 strips the ghcr.io fallback, or the deny-egress hold passes over
an un-mirrored image.

### Fix (chart-only, 0.1.78)

`03-harbor-prewarm-job.yaml` Phase A now UNIONs the running-Pod image set with
the DECLARED workload-template image set (Deployments + StatefulSets + DaemonSets
+ CronJob/Job pod templates) across every namespace. Declared templates are
present regardless of replica count or schedule, so the union is complete on any
vCluster sync mode and can only WIDEN the prewarm list (every entry is still
skopeo-pushed idempotently) — a strict superset of the prior behaviour. No RBAC
change (the runner ClusterRole already holds get/list on those kinds).

## Live evidence — permanent Sovereign `4635277cae4ffed9`

### Pre-cutover state (clean, not started)

```
$ kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.cutoverComplete}'
false
$ ... registriesYamlActive
v1
$ ... per-node acks (all 6 nodes)
node.<...>.registriesYaml=v1   (×6)
$ kubectl get hr -n flux-system bp-self-sovereign-cutover -o jsonpath='{.spec.chart.spec.version}'
0.1.77    ready=True
```

### Union enumeration — strict-superset proof (live)

Simulated the NEW union enumeration against the live cluster
(`/var/lib/catalyst/kubeconfigs/4635277cae4ffed9.yaml` via the mothership
catalyst-api pod):

```
OLD (running-Pod-only) unique openova-io images : 35
NEW union (pods ∪ declared workload templates)  : 35
LOST (in OLD but not in UNION)                  : (empty)
```

- `LOST` empty ⇒ the union is a strict superset of prior behaviour: **zero
  images lost, zero regression**.
- On this pod-syncing topology the declared set ⊆ running set, so the count is
  unchanged (35 = 35) — the fix's value is for the non-pod-syncing and
  transient-down cases the spec named, where the running set would be a strict
  subset. The completeness guarantee no longer depends on which pods are Running.

## Validation (CI-equivalent, local)

- `helm lint` — 0 failed.
- `helm template` — renders (5430 lines).
- `dash -n` + `sh -n` on the rendered step-03 podSpec script — clean.
- `bash tests/cutover-contract.sh` — **all 28 gates green** (added Case 28 that
  guards the union enumeration so it cannot regress to running-pods-only;
  Cases 26/27 — Phase A2 chart mirror + `--command-timeout` — still pass).
- `go build ./...` + `go test ./internal/handler/` (incl. `cutover_durable_test.go`)
  — green; `blueprints.json` regenerated via `build-catalog.mjs`.

## DoD state

| Box | State | Note |
|---|---|---|
| #3944 — step-03 prewarm no multi-hour 'active' stall | code-complete + mechanism-validated | `--command-timeout` (0.1.77, merged) + union enumeration (0.1.78, this PR). Live step-03 Job execution requires a real cutover to reach step-03. |
| #3379 Faces 1–5 | merged (PR #3699) + present on the live env at 0.1.74+ | The five Go/chart faces are on `main`; this PR does not re-litigate them. |
| #3944 / #3379 — full end-to-end cutover walk | **REQUIRES a real cutover** | Not run on the permanent omantel.biz: it is the customer-facing env (must not disrupt), it is NOT converged (5 demo-Org HRs non-Ready, which step-08's HR-regression gate would fail on), and a cutover irreversibly severs all mothership tethers. The step-03 union + deny-egress CCNP mechanics are validated in isolation per the ticket's explicit escape hatch. |

**Boxes still needing a real cutover walk to close:** #3944 DoD (live step-03 Job
completes), #3379 DoD 1–8 (durability/pivot/deny-egress/audit/host-loop/end-to-end
walked on a fresh prov to `cutoverComplete=true`). These are gated on a dedicated
fresh prov reaching the cutover — not runnable safely on the permanent env.
