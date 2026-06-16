# UAT Walkthrough — SOVEREIGNTY: cutover earns `cutoverComplete=true` only via a durable, reconcile-immune fact and a TRUE deny-egress proof

**Issue:** #3379
**Slug:** `cutover-durable-true-deny-egress-and-faithful-pivot`
**Pillar:** 5 (Sovereign independence) · ADR-0002 · Principle #11 · DoD §7
**Format:** every row is ONE operator action. `☐` is flipped by the founder doing the click and seeing the result.
**Env under test:** the current converged Sovereign (`console.<fqdn>` — e.g. `console.hw150.omantel.biz`), kubeconfig at `/tmp/<env>.kubeconfig`. Replace `<fqdn>` / `<sov>` with the live env.

> **Acceptance rule (founder, 2026-06-03):** this is a click-by-click UI/CLI walk, not a unit test. The automated `go test` rows live in Appendix A and are explicitly **NOT acceptance** — they only pre-stage the source-level guards. Acceptance = the founder walking the numbered rows below on a FRESH prov and seeing each "You should see" outcome.

> **Why this doc exists (the false-proof thesis):** on `hw150` the 11 cutover steps ran green and `cutoverComplete=true` was set, yet the cluster was (a) re-armable to re-fire the whole 600s hold by a routine `helm upgrade` (#3667), (b) still pointing node containerd at the MOTHERSHIP Harbor because `registriesYamlActive` was never flipped to `v2` (#3671), (c) certified by a "deny-egress" that only blocked 4 CIDRs and left `console.openova.io` + ~18 tethers reachable (#3678), (d) carrying a corrupted audit start-time stamped 24 min after the first step (#3681), and (e) running a `bp-sso-bridge` reconciler pod on `harbor.openova.io` while its own host GitOps loop (`bootstrap-kit`) was `BuildFailed` from a duplicate `values:` key the cutover itself injected (#3695). Every section below proves one of those five faces is now closed.

---

## Section 0 — Pre-state: reproduce the false proof on the CURRENT (pre-fix) env

These rows DOCUMENT the gap as it exists today. After the fix lands + a fresh cutover, Sections 1-6 must show the corrected behaviour.

| # | Go to | Do | You should see (TODAY, pre-fix) | ☐ |
|---|---|---|---|---|
| 0.1 | bastion shell | `export KUBECONFIG=/tmp/<env>.kubeconfig` | prompt ready | ☐ |
| 0.2 | bastion | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.cutoverComplete}'` | `true` (cutover claims done) | ☐ |
| 0.3 | bastion | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.registriesYamlActive}'` | `v1` — **the registry pivot pivoted nothing** (#3671) | ☐ |
| 0.4 | bastion | `kubectl get secret cutover-complete -n catalyst` | `NotFound` — **no durable sovereignty seal exists** (#3667) | ☐ |
| 0.5 | bastion | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.cutoverStartedAt}{"  vs first step  "}{.data.step\.gitea-mirror\.startedAt}'` | `cutoverStartedAt` is LATER than the first step's `startedAt` — **corrupt audit** (#3681) | ☐ |
| 0.6 | bastion | `kubectl get kustomization -n flux-system bootstrap-kit` | `READY=False  post build failed for 'bp-sso-bridge': yaml: unmarshal errors … mapping key "values" already defined` — **host GitOps loop dead** (#3695) | ☐ |
| 0.7 | bastion | `kubectl get pods -n sso-bridge -o jsonpath='{.items[*].spec.containers[*].image}'` | `harbor.openova.io/proxy-dockerhub/alpine/k8s:…` — **live MOTHERSHIP tether** on a "complete" cutover (#3695) | ☐ |
| 0.8 | bastion | `kubectl get ccnp cutover-egress-block -o yaml 2>/dev/null \| grep -A3 enableDefaultDeny` (if a hold is live) OR read `08-egress-block-test-job.yaml` | `enableDefaultDeny.egress: false` + a 4-entry `egressDeny.toCIDRSet` — **subtractive, not deny-egress** (#3678) | ☐ |

---

## Section 1 — DURABILITY: a `helm upgrade` cannot revert the flag or re-fire the cutover (#3667)

**Goal:** `cutoverComplete=true` is backed by a sealed OpenBao secret the owning chart's upgrade CANNOT revert; both the resume hook and the post-upgrade auto-trigger read the seal FIRST and no-op.

| # | Go to | Do | You should see (POST-FIX) | ☐ |
|---|---|---|---|---|
| 1.1 | bastion | `kubectl get secret cutover-complete -n catalyst` | the sealed secret EXISTS (sealed in `runCutover`'s success tail, mirroring `tofu-phase0-archive`) | ☐ |
| 1.2 | bastion | snapshot jobs: `kubectl get jobs -n catalyst -l cutover.openova.io/run-epoch -o name > /tmp/jobs-before.txt` | a list (or empty) captured | ☐ |
| 1.3 | bastion | simulate a pin bump: `flux reconcile hr bp-self-sovereign-cutover -n flux-system --with-source` (forces the upgrade path) | `reconciliation finished … applied revision` | ☐ |
| 1.4 | bastion | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.cutoverComplete}'` | STILL `true` (or re-derived `true` within one reconcile from the seal) — **the chart no longer re-asserts `false`** | ☐ |
| 1.5 | bastion | `kubectl get jobs -n catalyst -l cutover.openova.io/run-epoch -o name > /tmp/jobs-after.txt && diff /tmp/jobs-before.txt /tmp/jobs-after.txt` | NO new cutover Jobs (empty diff) — **no spontaneous 600s hold** | ☐ |
| 1.6 | bastion | `kubectl get ciliumclusterwidenetworkpolicy cutover-egress-block` | `NotFound` — the upgrade did NOT re-apply a cluster-wide egressDeny | ☐ |
| 1.7 | bastion | `kubectl rollout restart deploy/catalyst-api -n catalyst && kubectl logs deploy/catalyst-api -n catalyst --since=2m \| grep -i cutover` | log line "already complete (sealed); nothing to resume" — resume hook reads the seal and fires NOTHING | ☐ |
| 1.8 | `https://console.<fqdn>` → Settings → **Sovereignty** | observe the card immediately after the upgrade+restart | steady **"Sovereign — tethers severed"** state; the **"Achieve True Sovereignty"** CTA is HIDDEN (even though the CM was momentarily reverted) | ☐ |

---

## Section 2 — FAITHFUL REGISTRY PIVOT: `registriesYamlActive=v2` flips node containerd to LOCAL Harbor (#3671)

**Goal:** the engine sets `registriesYamlActive=v2` after `harbor-prewarm` succeeds; the registry-pivot DaemonSet rewrites `/etc/rancher/k3s/registries.yaml` on EVERY node to `harbor.<fqdn>`; step-04 fails the cutover if any node is still `v1`. The pivot is node-wide — ALL upstream registries, not just openova-io images.

| # | Go to | Do | You should see (POST-FIX) | ☐ |
|---|---|---|---|---|
| 2.1 | bastion | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.registriesYamlActive}'` | `v2` (today: `v1`) | ☐ |
| 2.2 | bastion | `kubectl get cm self-sovereign-cutover-status -n catalyst -o jsonpath='{.data.step\.registry-pivot\.result}'` | `success` AND the per-node ack count == node count (e.g. `registriesYamlNodesAtV2 == <N>`) | ☐ |
| 2.3 | bastion | find the registry-pivot DS pod: `POD=$(kubectl get pod -n catalyst -l app.kubernetes.io/component=registry-pivot -o name \| head -1)`; then `kubectl exec -n catalyst $POD -- cat /host/etc/rancher/k3s/registries.yaml` | the file rewrites upstreams to **`harbor.<fqdn>`** — NOT `harbor.openova.io` | ☐ |
| 2.4 | bastion | with a deny-egress hold up (Section 3) OR the mothership black-holed, pull a NON-openova image on a node: `kubectl debug node/<node> -it --image=busybox -- chroot /host crictl pull docker.io/library/busybox:latest` | pull SUCCEEDS via local Harbor with mothership egress denied — **proves the pivot is node-wide, not openova-io-only** | ☐ |
| 2.5 | `https://console.<fqdn>` → /jobs (or Sovereignty card) | open step-04 `registry-pivot` | row reads "registry-pivot — N/N nodes at v2" | ☐ |
| 2.6 | bastion (negative) | on a scratch prov, hold one node so its ack never lands, fire cutover | step-04 does NOT green; `cutoverComplete` stays `false` — **step-04 is a REAL verification, not a Ready-only wait** | ☐ |

---

## Section 3 — TRUE DENY-EGRESS: the hold blocks the mothership broadly, not 4 CIDRs (#3678)

**Goal:** the 600s hold is a default-deny-egress with explicit intra-cluster allow-CIDRs, so EVERYTHING public — including `console.openova.io` and any un-enumerated mothership host — is denied for the window, while the apiserver/DNS/local services stay reachable (the #3640 apiserver-strand is NOT reintroduced). An active call-home assertion fails the proof if any mothership host connects.

| # | Go to | Do | You should see (POST-FIX) | ☐ |
|---|---|---|---|---|
| 3.1 | bastion | fire cutover; while step-08 holds, `kubectl run probe --rm -it --image=curlimages/curl --restart=Never -- curl -m5 https://console.openova.io/healthz` | connection **FAILS** (timeout/blocked) — today this SUCCEEDS | ☐ |
| 3.2 | bastion | `kubectl run probe2 --rm -it --image=curlimages/curl --restart=Never -- curl -m5 https://kubernetes.default.svc` | **SUCCEEDS** — apiserver reachable (no #3640 strand) | ☐ |
| 3.3 | bastion | `kubectl get ccnp cutover-egress-block -o yaml` | `enableDefaultDeny.egress: true` (or omitted, default-deny) WITH an explicit intra-cluster `egress.toCIDR` allow set (10.42/16, 10.43/16, 10.96/12, node CIDRs, DNS) — **NOT** a 4-entry `egressDeny.toCIDRSet` | ☐ |
| 3.4 | bastion | read the step-08 log: `kubectl logs -n catalyst job/<egress-block job> \| grep -i call-home` | "call-home to mothership denied + verified" — the step actively curl'd `console.openova.io` + the #2940 hosts FROM a pod under the policy and confirmed each is blocked | ☐ |
| 3.5 | bastion | `kubectl get secret ghcr-pull -n flux-system -o jsonpath='{.data.\.dockerconfigjson}' \| base64 -d` | keys **`harbor.<fqdn>`** (or `registry.<fqdn>`), NOT `ghcr.io` — tether #5 re-keyed (asserted absent of `ghcr.io` during the hold) | ☐ |
| 3.6 | bastion (generality) | on a scratch prov set `CATALYST_PIN_ISSUER=https://console.openova.io` on catalyst-api, re-fire cutover | step-08's call-home assertion **FAILS** and `cutoverComplete` stays `false` — **proves the proof catches an ARBITRARY mothership tether, not just the 4 enumerated hosts** | ☐ |
| 3.7 | `https://console.<fqdn>` → /jobs | open step-08 `egress-block-test` | row reflects a mothership-wide block + the per-host call-home result, not "deny 4 CIDRs" | ☐ |

---

## Section 4 — AUDIT FIDELITY: `cutoverStartedAt` is the true T0 and is never re-stamped on resume (#3681)

**Goal:** `cutoverStartedAt` is written ONCE (first run only); a separate `cutoverLastAttemptStartedAt` carries each resume/re-fire moment; the resume sentinel reads a stable value. The top-level start equals the first step's start.

| # | Go to | Do | You should see (POST-FIX) | ☐ |
|---|---|---|---|---|
| 4.1 | bastion | fire cutover; once step rows begin stamping, `kubectl rollout restart deploy/catalyst-api -n catalyst` MID-RUN (during `harbor-prewarm`) | the resume hook re-enters and continues from the last unfinished step | ☐ |
| 4.2 | bastion | after the cutover finishes: `kubectl get cm self-sovereign-cutover-status -n catalyst -o yaml \| grep -E 'cutoverStartedAt|cutoverLastAttemptStartedAt|step\..*\.startedAt' \| sort` | `cutoverStartedAt` == the EARLIEST `step.*.startedAt` (within seconds) — **NOT** the restart moment | ☐ |
| 4.3 | bastion | same output | `cutoverLastAttemptStartedAt` == the restart moment (the resume attempt time is recorded separately, audit preserved) | ☐ |
| 4.4 | `https://console.<fqdn>` → Settings → **Sovereignty** | read "Started <…>" + elapsed | "Started <true T0>"; elapsed reflects the REAL full run (e.g. ~35 min), with optional "(resumed N times)"; NOT a fictional 11-minute window | ☐ |

---

## Section 5 — HOST GITOPS LOOP SURVIVES + zero residual mothership tether (#3695)

**Goal:** step-10's image-host injection MERGES into the existing `values:` mapping (real YAML processor, idempotent against a file that already carries the key) so `13b-bp-sso-bridge.yaml` stays valid; `bootstrap-kit` reconciles Ready at the post-cutover revision; NO live pod / HR references a mothership registry; the egress test rolls EVERY external-registry workload and asserts ALL host Kustomizations Ready.

| # | Go to | Do | You should see (POST-FIX) | ☐ |
|---|---|---|---|---|
| 5.1 | bastion | `kubectl get kustomization -n flux-system` | `bootstrap-kit`, `sovereign-tls`, and (after ≥1 tenant) `sme-tenants` all **READY=True** at the post-cutover revision | ☐ |
| 5.2 | bastion | `kubectl get kustomization -n flux-system bootstrap-kit -o jsonpath='{.status.lastAppliedRevision}'` | the SHA == the latest cutover commit on local Gitea `openova/openova` main (the two pivot commits APPLIED, not stranded) | ☐ |
| 5.3 | bastion | `kubectl get hr -n flux-system bp-sso-bridge -o jsonpath='{.spec.values}'` then count `values:` keys in `clusters/_template/bootstrap-kit/13b-bp-sso-bridge.yaml` on the applied revision | `global.registryMirror = harbor.<fqdn>`; the file has EXACTLY ONE `values:` key | ☐ |
| 5.4 | bastion | `kubectl get pods -n sso-bridge -o jsonpath='{.items[*].spec.containers[*].image}'` | `harbor.<fqdn>/proxy-dockerhub/alpine/k8s:…` — NOT `harbor.openova.io` | ☐ |
| 5.5 | bastion | `kubectl get pods -A -o jsonpath='{range .items[*]}{range .spec.containers[*]}{.image}{"\n"}{end}{end}' \| grep -E 'harbor.openova.io\|ghcr.io/openova-io\|github.com/openova'` | **EMPTY** — no live pod pulls from the mothership | ☐ |
| 5.6 | bastion (loop-alive) | push a trivial commit to local Gitea `openova/openova` main (e.g. bump an HR `interval`), then `flux reconcile kustomization bootstrap-kit -n flux-system --with-source` | `bootstrap-kit` reconciles it green; `lastAppliedRevision` advances to the new SHA — **the GitOps spine lives WITHOUT the mothership** | ☐ |
| 5.7 | bastion (idempotency) | re-fire cutover (operator "retry") | `13b-bp-sso-bridge.yaml` STILL has exactly ONE `values:` key; no `BuildFailed` | ☐ |
| 5.8 | bastion (generality) | on a scratch prov, deliberately leave one HR on `harbor.openova.io`, fire cutover | step-08's fresh-pull loop rolls EVERY external-registry workload (per-workload pass/fail in the log); the deliberate tether `ImagePullBackOff`s → proof FAILS → `cutoverComplete` stays `false` — **gate is generic across ALL workloads, not hand-tuned to one good case** | ☐ |
| 5.9 | `https://console.<fqdn>` → Settings → **Sovereignty** | observe the card / step-08 row | step-08 shows "verifying re-pullability of N workloads" with per-workload pass/fail; the green "Sovereign" state renders ONLY when all host Kustomizations are Ready + zero residual tethers; otherwise a red "Cutover incomplete — N residual tethers / GitOps loop not reconciling" panel lists offenders | ☐ |

---

## Section 6 — END-TO-END: a cut-over Sovereign onboards a customer with ZERO mothership dependency (the whole journey)

**Goal:** prove the corrected cutover leaves a Sovereign that is independent AND still works — it reconciles local git, mints tokens, and materializes a new Organization.

| # | Go to | Do | You should see | ☐ |
|---|---|---|---|---|
| 6.1 | `https://console.<fqdn>` | bare URL → land `/dashboard` signed-in (no login UI) | dashboard, signed-in as the operator (admin) | ☐ |
| 6.2 | console → Settings → **Sovereignty** | click **"Achieve True Sovereignty"** (fresh prov) | progress card runs steps 01-11; step-08 shows per-workload pull results + the 600s countdown | ☐ |
| 6.3 | console Sovereignty | wait for completion | card shows `cutoverComplete=true` ONLY after all 11 steps + the broadened hold + the all-host-Kustomization-Ready gate passed | ☐ |
| 6.4 | bastion | push a trivial change to local Gitea, watch `bootstrap-kit` reconcile it green | loop lives without the mothership (re-confirms 5.6) | ☐ |
| 6.5 | console → BSS → **Vouchers** | issue a voucher; redeem at `https://marketplace.<fqdn>/redeem/?code=<CODE>` | Organization created → namespace + vcluster materialize via the (healthy) host Flux loop → recipient lands on their tenant home | ☐ |
| 6.6 | bastion | black-hole `github.com` / `ghcr.io` / `harbor.openova.io` at the firewall; wait one reconcile interval | the Sovereign keeps reconciling + serving — **THIS is "fully independent operation"** | ☐ |

---

## Appendix A — Automated guards (NOT acceptance; source-level pre-stage only)

These `go test` / render-test rows protect the fix at source; they are evidence the guard exists, **not** the founder walk.

| # | Command | Pass condition |
|---|---|---|
| A.1 | `go test -race ./products/catalyst/bootstrap/api/internal/handler/ -run Cutover` | green; the new tests pass |
| A.2 | two-call `runCutover` test | `cutoverStartedAt` byte-identical across calls; `cutoverLastAttemptStartedAt` advances (#3681) |
| A.3 | sealed-fact gate test | `spawnCutoverEngine` + `ResumeInterruptedCutover` no-op when `secret/catalyst/cutover-complete` is sealed, even with the CM reverted to `false` (#3667) |
| A.4 | render test on `09-cutover-status-configmap.yaml` | a `helm upgrade` does NOT emit `cutoverComplete:"false"` / `cutoverStartedAt:""` / `registriesYamlActive:"v1"` over a live state (#3667) |
| A.5 | injector unit test (step-10) | feed `13b-bp-sso-bridge.yaml` ALREADY carrying a `values:` block → output parses as valid single-`values:` YAML (#3695) |
| A.6 | cutover invariant test | `runCutover` cannot reach `cutoverComplete=true` while `registriesYamlActive != "v2"` (#3671) |
| A.7 | render test on `08-egress-block-test-job.yaml` | emitted CCNP is default-deny-with-allow-list, not a 4-CIDR subtractive block (#3678) |

---

## Evidence ledger

On a fresh prov, every `☐` above gets a screenshot or `kubectl` capture committed under `docs/sessions/<date>/evidence/` and linked from `docs/ledger/UAT.md`. `cutoverComplete=true` is admissible ONLY when Sections 1-6 are all green on the SAME env (per memory: each new env flushes all prior evidence — re-prove live).
