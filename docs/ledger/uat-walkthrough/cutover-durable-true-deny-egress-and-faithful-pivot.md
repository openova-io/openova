# #3379 — Cutover PROOF integrity: durable fact · true deny-egress · faithful pivot · audit fidelity · host-loop + zero residual

> **Scope:** this walkthrough proves the INTEGRITY, DURABILITY, and BREADTH of the cutover sovereignty
> proof — NOT that the 11 steps run (that is the predecessor #3379 scope, already green on hw150). Every
> row is `Go-to | do | see | ☐`. Ships against `bp-self-sovereign-cutover` chart **0.1.74** + catalyst-api.
>
> **What changed (the five faces):**
> 1. **#3667 durable fact** — `cutoverComplete=true` is sealed in OpenBao (`secret/catalyst/cutover-complete`),
>    a chart upgrade can no longer revert the flag or re-fire the 600s hold (status ConfigMap is init-only).
> 2. **#3671 faithful pivot** — the engine flips `registriesYamlActive=v2` after `harbor-prewarm`; the
>    registry-pivot DaemonSet writes a per-node v2 ack which step-04 COUNTS; the run REFUSES
>    `cutoverComplete=true` while the key is still `v1`.
> 3. **#3678 true deny-egress** — the 600s-hold CCNP is a TRUE default-deny (enableDefaultDeny.egress:true
>    + intra-cluster allow-list) instead of a subtractive 4-CIDR block; an active call-home assertion FAILS
>    the proof if `console.openova.io` (or any mothership host) is reachable from a pod under the hold.
> 4. **#3681 audit fidelity** — `cutoverStartedAt` is written ONCE (true first run); resume/re-fire advance a
>    separate `cutoverLastAttemptStartedAt`.
> 5. **#3695 host loop + zero residual** — step-10's sso-bridge override MERGES with yq into the existing
>    `values:` (no duplicate-key BuildFailed); step-08 HARD-gates on bootstrap-kit/sovereign-tls/sme-tenants
>    Ready and rolls EVERY external-registry workload under deny-egress.

## Section 0 — pre-state (reproduce the hw150 defects on the OLD chart, then confirm fixed)

| Go to | Do | See | ☐ |
|---|---|---|---|
| terminal (any cutover-complete env on OLD chart ≤0.1.73) | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.cutoverComplete}'` then `helm upgrade bp-self-sovereign-cutover ...` (or `flux reconcile hr bp-self-sovereign-cutover --with-source`) | OLD: flag reverts to `false`, the auto-trigger Job re-fires the whole cutover incl. the 600s hold | ☐ |
| terminal | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.registriesYamlActive}'` | OLD: `v1` on a `cutoverComplete=true` cluster (node containerd still on mothership Harbor) | ☐ |
| terminal | `kubectl get ccnp cutover-egress-block -o yaml` during a hold | OLD: `enableDefaultDeny.egress:false` + 4-entry `egressDeny.toCIDRSet` (subtractive; console.openova.io reachable) | ☐ |
| terminal | `kubectl get kustomization -n flux-system bootstrap-kit` | OLD: `BuildFailed` — `mapping key "values" already defined` (duplicate from step-10 awk injector) | ☐ |

## Section 1 — Durability (#3667): a chart upgrade cannot un-prove or re-fire

| Go to | Do | See | ☐ |
|---|---|---|---|
| terminal (fresh prov, chart 0.1.74, after cutover reached `cutoverComplete=true`) | `kubectl get secret cutover-complete -n catalyst` (note: stored via OpenBao KV at `secret/catalyst/cutover-complete`) → and read it: `kubectl exec -n openbao <bao-pod> -- bao kv get secret/catalyst/cutover-complete` | the durable seal EXISTS — `cutoverComplete:"true"`, `cutoverStartedAt`, `cutoverFinishedAt`, `sealedAt` | ☐ |
| terminal | capture the run-epoch Jobs: `kubectl get jobs -n catalyst -l cutover.openova.io/run-epoch -o name | sort > /tmp/before.txt` | a fixed set of completed cutover Jobs | ☐ |
| terminal | `flux reconcile hr bp-self-sovereign-cutover -n flux-system --with-source` (simulates a routine chart-pin reconcile) | HR reconciles green; the status ConfigMap is NOT re-authored (init-only `lookup` guard) | ☐ |
| terminal | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.cutoverComplete}'` | STILL `true` (or re-derived true within one reconcile from the seal) | ☐ |
| terminal | `kubectl get jobs -n catalyst -l cutover.openova.io/run-epoch -o name | sort > /tmp/after.txt; diff /tmp/before.txt /tmp/after.txt` | EMPTY diff — NO new run-epoch Jobs, the 600s hold did NOT re-fire | ☐ |
| terminal | `kubectl get ccnp cutover-egress-block` | `NotFound` — no fresh deny-egress hold was armed | ☐ |
| terminal | `kubectl rollout restart deploy/catalyst-api -n catalyst-system; kubectl logs -n catalyst-system deploy/catalyst-api | grep cutover-resume` | log: "already complete (sealed); … fired nothing" — resume hook no-ops on the seal | ☐ |
| browser | open `console.<fqdn>` → admin → Sovereignty card | still "Sovereign" badge + NO "Achieve True Sovereignty" CTA (backend backfills cutoverComplete=true from the seal even if the CM was reverted) | ☐ |

## Section 2 — Faithful pivot (#3671): containerd actually flips, step-04 verifies per node

| Go to | Do | See | ☐ |
|---|---|---|---|
| terminal (fresh cutover, chart 0.1.74) | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.registriesYamlActive}'` | `v2` (the engine flipped it after harbor-prewarm) | ☐ |
| terminal | `kubectl get cm self-sovereign-cutover-status -n catalyst -o json | jq -r '.data | to_entries[] | select(.key|startswith("node.")) | "\(.key)=\(.value)"'` | one `node.<name>.registriesYaml=v2` per node — acks == node count (step-04 counted these) | ☐ |
| terminal | `kubectl debug node/<node> -it --image=busybox -- cat /host/etc/rancher/k3s/registries.yaml` | upstreams rewritten to `harbor.<fqdn>/v2/proxy-*` (NOT `harbor.openova.io`) | ☐ |
| terminal | with mothership egress denied, roll a NON-openova image: `kubectl run dockerhub-probe --image=docker.io/library/busybox:1.36 -- sleep 30; kubectl get pod dockerhub-probe -w` | reaches `Running` — `docker.io` pull resolved through LOCAL Harbor (proves the certs.d mirror, not just the openova path #3647 masked) | ☐ |
| terminal (NEGATIVE) | on a scratch prov, leave one node at v1 (skip its ack) → re-run cutover | step-04 does NOT green; the run reports `failedStep=registry-pivot` and `cutoverComplete` stays false | ☐ |

## Section 3 — True deny-egress (#3678): block EVERY mothership tether, not 4 CIDRs

| Go to | Do | See | ☐ |
|---|---|---|---|
| terminal (during the 600s hold of a fresh cutover) | `kubectl get ccnp cutover-egress-block -o yaml` | `enableDefaultDeny.egress: true` + an `egress:` allow-list (toCIDR 10.42/10.43/10.96 + toEntities kube-apiserver/host/remote-node/cluster + DNS toPorts) — NOT a 4-entry `egressDeny.toCIDRSet` | ☐ |
| terminal | exec a pod under the policy: `kubectl exec -n <ns> <pod> -- curl -m5 https://console.openova.io/healthz; echo "exit=$?"` | FAILS to connect (exit non-zero / http_code 000) — the mothership front door is black-holed | ☐ |
| terminal | same pod: `kubectl exec -n <ns> <pod> -- curl -m5 -k https://kubernetes.default/healthz; echo "exit=$?"` | SUCCEEDS — no #3640 apiserver-strand (allow-list keeps 10.96.0.1 reachable) | ☐ |
| terminal | step-08 log: `kubectl logs -n catalyst job/<egress-block-test-job> | grep -A6 'active call-home'` | each mothership host logged "OK — … unreachable (… http_code=000) — denied as required"; final "PASS — every mothership host black-holed" | ☐ |
| terminal | `kubectl get secret ghcr-pull -n flux-system -o jsonpath='{.data.\.dockerconfigjson}' | base64 -d | jq '.auths | keys'` | keys `registry.<fqdn>` / `harbor.<fqdn>` — NOT `ghcr.io` | ☐ |
| terminal (NEGATIVE, GENERALITY) | on a scratch prov inject a synthetic tether (`CATALYST_PIN_ISSUER=https://console.openova.io`) then run cutover | step-08 call-home FAILS → `cutoverComplete` stays false — proves the proof catches an ARBITRARY mothership tether, not the 4 enumerated hosts | ☐ |

## Section 4 — Audit fidelity (#3681): the durable start-time is not corrupted on resume

| Go to | Do | See | ☐ |
|---|---|---|---|
| terminal (a cutover that experienced a mid-run catalyst-api restart) | `kubectl get cm self-sovereign-cutover-status -n catalyst -o json | jq -r '.data | {cutoverStartedAt, cutoverLastAttemptStartedAt, "first_step_start": ."step.gitea-mirror.startedAt"}'` | `cutoverStartedAt` ≈ the EARLIEST `step.*.startedAt` (within seconds), NOT the resume moment | ☐ |
| terminal | compare `cutoverLastAttemptStartedAt` | equals the restart/resume moment (the latest attempt) | ☐ |
| terminal (unit) | `cd products/catalyst/bootstrap/api && go test -race -run TestRunCutover_StartedAtWrittenOnceAcrossResume ./internal/handler/` | PASS — two `runCutover` calls leave `cutoverStartedAt` byte-identical while the attempt field advances | ☐ |
| browser | Sovereignty card | "Started <true-first-run-time>" + elapsed reflects the REAL full run (not the 11-min fiction hw150 showed) | ☐ |

## Section 5 — Host GitOps loop + zero residual tether (#3695)

| Go to | Do | See | ☐ |
|---|---|---|---|
| terminal (fresh prov to `cutoverComplete=true`, chart 0.1.74) | `kubectl get kustomization -n flux-system bootstrap-kit sovereign-tls sme-tenants` | all `Ready=True` at the post-cutover revision (NOT BuildFailed) | ☐ |
| terminal | `kubectl get pods -A -o jsonpath='{range .items[*]}{range .spec.containers[*]}{.image}{"\n"}{end}{end}' | grep -E 'harbor.openova.io|ghcr.io/openova-io|github.com/openova' | sort -u` | EMPTY — no live Pod pulls from a mothership registry | ☐ |
| terminal | `kubectl get hr -n flux-system bp-sso-bridge -o jsonpath='{.spec.values}'` then `kubectl get pods -n sso-bridge -o jsonpath='{.items[*].spec.containers[*].image}'` | `global.registryMirror=harbor.<fqdn>`; sso-bridge-reconciler image is `harbor.<fqdn>/...` (NOT harbor.openova.io) | ☐ |
| terminal | push a trivial commit to local Gitea (`openova/openova`) → `flux reconcile source git openova -n flux-system` | `bootstrap-kit` `lastAppliedRevision` advances — the host loop reconciles local git | ☐ |
| terminal (idempotency) | re-run cutover → `git show` the local Gitea `clusters/_template/bootstrap-kit/13b-bp-sso-bridge.yaml` | exactly ONE `values:` key under the HelmRelease spec; no BuildFailed | ☐ |
| terminal | step-08 log: `kubectl logs -n catalyst job/<egress-block-test-job> | grep -E 'host GitOps loop|external-registry workload'` | "host GitOps loop healthy"; "every external-registry workload rolled to Running pulling from the local registry under deny-egress" | ☐ |
| terminal (NEGATIVE, GENERALITY) | leave one HR on `harbor.openova.io` → run cutover | the fresh-pull loop rolls EVERY external-registry workload; that one `ImagePullBackOff`s → proof FAILS → `cutoverComplete` stays false | ☐ |

## Section 6 — End-to-end (downstream confirmation)

| Go to | Do | See | ☐ |
|---|---|---|---|
| browser (post-cutover) | create an Organization in the console | namespace + vcluster materialize via the (now-healthy) host Flux loop — ZERO mothership dependency | ☐ |
| browser | recipient redeems a voucher → lands on their tenant home | works with the 3 mothership hosts black-holed at the firewall | ☐ |
| terminal | operator black-holes `console.openova.io` + `ghcr.io` + `harbor.openova.io` at the firewall | the Sovereign keeps reconciling + serving | ☐ |

## Appendix A — automated guards (NOT acceptance; acceptance is the founder walking Sections 1-6)

| Check | Command | Expected |
|---|---|---|
| Go unit + race (the new tests) | `cd products/catalyst/bootstrap/api && go test -race ./internal/handler/` | ok |
| Durable-fact + invariant + audit tests | `go test -race -run 'TestResumeInterruptedCutover_NoOpWhenSealedDespiteRevertedCM|TestSpawnCutoverEngine_NoOpWhenSealedDespiteRevertedCM|TestRunCutover_SealsDurableFactOnSuccess|TestHandleCutoverStatus_BackfillsFromSeal|TestRunCutover_RefusesCompleteWhenRegistriesYamlV1|TestRunCutover_FlipsRegistriesYamlV2AfterHarborPrewarm|TestRunCutover_StartedAtWrittenOnceAcrossResume' ./internal/handler/` | ok |
| Chart render + lint | `helm lint platform/self-sovereign-cutover/chart && helm template ssc platform/self-sovereign-cutover/chart --set sovereign.fqdn=hw151.omantel.biz` | 0 failures |
| Embedded shell scripts POSIX-safe | `dash -n` each step's podSpec args + pivot.sh (busybox-ash parity) | all OK |
| Default-deny CCNP shape | render step-08 default-deny branch | valid CCNP: `enableDefaultDeny.egress:true` + allow `egress:` rules, NO `egressDeny.toCIDRSet` |
| Catalog-seed lockstep | `bash scripts/check-catalog-seed-lockstep.sh` | PASS |

---

**Status:** chart `bp-self-sovereign-cutover` **0.1.74** + catalyst-api (`Refs #3379`). Built + unit/render/POSIX-verified.
The live founder-walk of Sections 1-6 on a fresh prov to `cutoverComplete=true` is the acceptance gate — PENDING that prov.
