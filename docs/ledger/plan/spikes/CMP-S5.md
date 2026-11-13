## CMP-S5 result: do an Organization's workloads pass the `baseline` pod-security level?

**Outcome: no**

One workload fails: the **bp-agenity StatefulSet** (chart `products/agenity/chart` 0.5.34, Pod `bp-agenity-0`, container `chepherd`). It fails three controls:
- **non-default capabilities**: `SYS_ADMIN` is in `capabilities.add`
- **hostPath volumes**: volume `fuse` (`/dev/fuse`)
- **seccompProfile**: set to `Unconfined`

Everything else passes. That includes the vCluster control-plane Pod and every Pod the vCluster syncs into the host namespace.

### Question

Do an Organization's workloads pass the `baseline` level? This covers the vCluster's own control-plane Pod (`vcluster-0`) and the Pods the vCluster syncs into the Organization's host namespace.

### What was run

**Where.** GitHub Actions in `openova-io/openova`, on branch `spike/CMP-S5`. The branch is never merged. The workflow is `.github/workflows/spike-cmp-s5.yaml`; its scripts are in `tests/spikes/CMP-S5/`. One `ubuntu-24.04` job runs a kind cluster. No cloud account and no secret are used.

**Versions.**
- kind v0.25.0, node image `kindest/node:v1.31.2`. Live Sovereigns run k3s v1.31.4. Pod-security policy versions change only with the minor version, so `latest` is the same policy.
- kubectl v1.31.2, helm v3.16.3, yq v4.44.3. Go version from the catalyst-api `go.mod` (1.26.0).
- vcluster chart 0.33.4. That is what the controller's constraint `0.33.*` resolves to on `charts.loft.sh` today; versions 0.33.0 to 0.33.4 exist.
- CloudNativePG chart 0.28.0 (the platform's pin), operator image 1.29.0.
- Gateway API CRDs v1.2.1.

**Inputs.** All inputs came from the product's own renderers, not hand copies:
- The organization-controller's `Render` (Organization `acme`, plan m) gave the namespace and the vCluster HelmRelease values.
- The catalyst-api `renderOrganizationOverlay` gave the Helm values of the six charts that run in every Organization namespace: bp-keycloak 1.5.14, bp-newapi 1.4.157, bp-openclaw 0.2.20, bp-stalwart-tenant 0.1.16, bp-wordpress-tenant 0.6.2 and bp-agenity 0.5.34.
- The five Applications also got the singleton topology contract, as the application-controller adds it.
- Two helper tests were copied into those Go packages inside the CI job only.

**Steps.**
1. **Cluster.** The API-server audit log was on, at Metadata level for Pods and workloads.
2. **Dry-run.** Every pod-bearing object of each chart was created with `--dry-run=server`, one object at a time, so each warning names its object. That covers every Pod, Deployment, StatefulSet, Job and CronJob, plus the openclaw per-user Pod template kept in a ConfigMap. Each object went into two namespaces: one labelled `warn=baseline` and one labelled `warn=restricted`.
3. **The planned experiment.**
   - The Organization namespace was created from the controller's template and labelled `warn=baseline` and `audit=baseline`.
   - The vCluster was installed with the controller's values. The six charts were applied and a CNPG cluster was created.
   - Inside the vCluster ran coredns, a plain probe Pod and a restricted-shaped probe Pod. Two listed catalog charts (bp-matrix and bp-llm-gateway) were installed in a namespace `apps`, so real application Pods were synced to the host.
   - Every workload was restarted: inside the vCluster first, then in the host namespace.
   - The warnings, audit events and pod-security metrics were read.
   - Every live Pod was assessed before and after the restart in two ways: with the namespace-label dry-run (`kubectl label --dry-run=server ... enforce=<level>`), and with a dry-run of its own stored spec.
4. **CMP-068 as written.** The namespace got exactly `enforce=baseline`, `enforce-version=latest` and `warn=restricted`; the experiment labels were removed. Then:
   - `vcluster-0` was deleted and recreated.
   - coredns was deleted inside the vCluster and synced again.
   - A clean Pod and a privileged Pod were created inside the vCluster.
   - Two Pods were applied straight to the host namespace: a privileged Pod, and a Pod with a CSI volume and no service-account token.
   - Every chart workload was scaled to zero and back.
   - A CNPG instance Pod was deleted.
5. **Secondary.** The Blueprints listed in the marketplace were rendered with default values and dry-run the same way.

**Deviations.** None of them touches a pod-security field.
- Images come from upstream registries, because the Sovereign's Harbor proxy is not reachable from CI. Pod security does not read image names.
- The live copies of the charts have no resource requests or limits, so that one kind node holds all the Pods. The dry-runs keep them.
- Objects are created with `--validate=warn`. That is how Helm, which sends no field-validation mode, lands them. See observation 2.
- The live vCluster install leaves out one value, `sync.toHost.customResources` (HTTPRoute sync). See observation 1. The dry-run of the vCluster chart keeps it.

### Measured values

Runs 3 and 4 give the same numbers. Run 2 is the same except for two test defects that run 3 fixed.

| What was judged | Objects | baseline | restricted | Runs |
|---|---|---|---|---|
| vCluster chart (StatefulSet `vcluster`) | 1 | pass | fail | 1 to 4 |
| Organization charts, dry-run | 17 | 16 pass, 1 fail (bp-agenity StatefulSet) | 8 pass, 9 fail | 2 to 4 |
| Live Pods in the Organization namespace, before the restart | 27 | 26 pass, 1 fail (`bp-agenity-0`) | 12 pass, 15 fail | 3, 4 |
| Live Pods, after the restart | 33 | 32 pass, 1 fail (`bp-agenity-0`) | 13 pass, 20 fail | 3, 4 |
| Listed marketplace charts, default values | 34 objects from the 20 charts that render | 32 pass, 2 fail | — | 3, 4 |

**What passes.**
- **`vcluster-0` (the control plane)** passes `baseline` in the dry-run and as a live Pod. It fails `restricted` on allowPrivilegeEscalation, capabilities, runAsNonRoot, runAsUser=0 and seccompProfile.
- **Synced Pods** all pass `baseline`: coredns, the plain probe, the restricted probe, the bp-matrix synapse Pod, and the bp-llm-gateway litellm Pod with its migration Job Pod.
  - coredns fails `restricted` only on seccompProfile.
  - The restricted-shaped probe still passes `restricted` after the sync. So the syncer's translation adds nothing that breaks either level.
- **CNPG instance and initdb Pods** (operator 1.29.0) pass both levels.
- **bp-keycloak** (Keycloak, its PostgreSQL and the config-cli Job) passes both levels.

**What fails.** The result is the same in runs 1 to 4: the bp-agenity StatefulSet fails `baseline` on the three controls named at the top.
- These three fields are rendered only when the chart value `podman.inPod` is true. True is the chart default, and the Organization values do not change it.
- Run 4 rendered the same chart and values with `podman.inPod=false`. Both bp-agenity objects then pass `baseline`.

**Audit (runs 3 and 4).** The Organization namespace has 3 audit events with baseline violations, all for bp-agenity:
- the StatefulSet create
- the Pod create by the statefulset-controller
- the StatefulSet patch at restart

The host-side restart printed one warning, also for bp-agenity.

**Under the CMP-068 labels.** Runs 3 and 4 give the same result on every check.
- **D1.** `vcluster-0` was recreated and became Ready.
- **D2.** The synced coredns was deleted inside the vCluster, synced again and became Ready.
- **D3.** A clean Pod created inside the vCluster was synced to the host.
- **D4.** A privileged Pod created inside the vCluster was refused on the host. The vCluster recorded `SyncError ... pods "probe-priv-x-default-x-vcluster" is forbidden: violates PodSecurity "baseline:latest": privileged`, and the virtual Pod stayed Pending. So enforcement on the host namespace also covers Pods a customer creates inside the vCluster.
- **D5.** A privileged Pod applied to the host namespace was refused: `violates PodSecurity "baseline:latest": privileged (container "main" must not set securityContext.privileged=true)`. The message names the level.
- **D6.** A Pod with a CSI volume and `automountServiceAccountToken: false` was admitted, with the `warn=restricted` warning.
- **D7.** Every chart workload got its Pods back after scale-up, except bp-agenity. bp-agenity got 0 Pods and the event `FailedCreate ... pods "bp-agenity-0" is forbidden: violates PodSecurity "baseline:latest": non-default capabilities ..., hostPath volumes ..., seccompProfile ...`.
- **D8.** A CNPG instance Pod was deleted, and the operator recreated it.
- The API server counted 31 Pod creations refused by `enforce=baseline` in each of runs 3 and 4. The refusals named in the log are for exactly three Pods:
  - `bp-agenity-0`
  - the privileged host probe
  - the privileged vCluster probe, retried by the syncer

### Other observations

These are outside the question, but they matter for the plan.

1. **vCluster 0.33.4 does not start with the controller's values.** In run 1 the control plane crash-looped with this error: `register to host syncer httproutes.gateway.networking.k8s.io: you are trying to use a vCluster pro feature 'Generic Sync' ... not available in the open source vcluster`.
   - The cause is `sync.toHost.customResources` in the rendered values. Without that one key, the control plane was Ready in 32 to 37 seconds (runs 2 to 4).
   - Which 0.33 patch a Sovereign's chart mirror serves was not measured.
   - The control-plane Pod passed `baseline` in run 1 too. Only its runtime failed.
2. **Unknown fields are dropped.** Three objects put container-only fields (`allowPrivilegeEscalation`, `capabilities`, `readOnlyRootFilesystem`) in the Pod-level securityContext: the bp-newapi Deployment, the bp-openclaw controller Deployment and the openclaw per-user Pod template.
   - A strict client refuses these objects (run 1). Server-side apply refuses them in every mode.
   - Otherwise the API server drops the three fields. `baseline` is not affected, but these workloads then fail `restricted`, against what their authors wrote.
3. **`warn=restricted` will warn on most Organization workloads.** 9 of the 17 chart objects fail `restricted`, and so do `vcluster-0` and the synced coredns. The label only warns; it blocks nothing.
4. **Catalog.** The listed chart bp-livekit fails `baseline` on host namespaces (`hostNetwork=true`) and hostPort 7881. An Organization that installs it would be refused once CMP-068 lands. bp-agenity is the other listed chart that fails.

### What was not measured

- **A live Sovereign.** That means k3s instead of kind, the Sovereign's Harbor images, and the platform's Kyverno policies.
  - I read the Kyverno mutate templates and the per-Organization platform-scope policy. None of them changes a pod-security field: they set labels, a Gitea source reference and `priorityClassName`.
  - Pod security runs after mutation, so on a Sovereign it would judge the mutated Pod.
- **Marketplace charts beyond the two installed inside the vCluster** are judged by dry-run only. The probes show that the syncer keeps a Pod's pod-security shape, so the dry-run result also holds for a synced Pod.
- **bp-newapi with default values** does not render, because it requires an OpenBao path. It is measured with the Organization values instead.
- **What a Sovereign resolves `0.33.*` to.**

### What follows for the plan

- **The outcome is no.** The failing workload is bp-agenity (`products/agenity/chart`). The failing controls are:
  - non-default capabilities (`SYS_ADMIN`)
  - hostPath volumes (`/dev/fuse`)
  - seccompProfile (`Unconfined`)
- **CMP-068 waits for the owner of the bp-agenity chart.** If the labels were applied today, the agentic workspace StatefulSet would get no Pod in any Organization.
  - Pod security acts only at admission, so a running Pod keeps running until it is next recreated. This is Kubernetes behaviour and was not measured separately here.
- **Run 4 measured that `podman.inPod=false` clears all three controls.** That switch turns off the in-pod podman spawner, so the chart owner must decide how agent sessions run instead.
- **Everything else CMP-068 needs holds.**
  - The vCluster control plane, its synced Pods, the other five charts and the CNPG Pods all pass `baseline`.
  - The control plane and coredns restart under enforce.
  - A privileged Pod is refused both ways: applied straight to the host, and through the syncer.
  - A CSI Pod without a token is admitted.
  - The privileged-Pod check (CMP-G07) can be built as planned. The refusal message names the level.
- **Two items need owners outside this question:** the vCluster Pro-feature failure (observation 1) and bp-livekit's use of `hostNetwork` (observation 4).

**Evidence.** Branch `spike/CMP-S5` at commit `9596139cd`. Each run's artifact `spike-cmp-s5-out` holds the raw outputs and is kept for 30 days.

## Evidence

Outcome: **no**. The experiment is the branch `spike/CMP-S5` of this repository (never merged). Runs:

- https://github.com/openova-io/openova/actions/runs/43350423153
- https://github.com/openova-io/openova/actions/runs/43356773677
- https://github.com/openova-io/openova/actions/runs/43359218184
- https://github.com/openova-io/openova/actions/runs/43360930256

## What later tasks must take into account

Outcome is "no": the bp-agenity StatefulSet fails `baseline`, and nothing else does. Under the CMP-068 labels its Pod is refused, so CMP-068 must not go ahead before the bp-agenity chart is fixed. Otherwise every Organization's agentic workspace (pillar 4) gets no Pod the next time it is recreated.

The three failing fields are all rendered only when `podman.inPod` is true, which is the chart default; the Organization overlay does not change it. Run 4 measured that `podman.inPod=false` passes `baseline`, but that turns off the in-pod podman agent spawner, so the chart owner needs an alternative.

What could not be measured in CI:
- **A live Sovereign.** It runs k3s v1.31.4 instead of kind 1.31.2 (the pod-security policy is the same), pulls through the Sovereign's Harbor, and runs the platform's Kyverno policies. Kyverno was checked only by reading its templates; no mutation touches a pod-security field.
- **Which vcluster 0.33 patch a Sovereign's chart mirror resolves.**

What later packages must take into account:
- **vcluster 0.33.4 OSS crash.** With the organization-controller's rendered values, the control plane crash-loops: `sync.toHost.customResources` (HTTPRoute) is a vCluster Pro feature ("Generic Sync"). 0.33.4 is what `0.33.*` resolves to on the public repository today. Every package that touches the per-Org vCluster or provisioning should check what Sovereigns actually resolve. Fresh Organizations may not come up.
- **Dropped fields in bp-newapi and bp-openclaw.** These charts put container-only securityContext fields at Pod level. Strict clients and server-side apply refuse the objects; Helm-style creates silently drop the fields. Their intended `restricted` posture never applies, and any move to server-side apply will break them.
- **`warn=restricted` is noisy.** It warns on 9 of the 17 Org chart objects, on vcluster-0 and on the synced coredns.
- **bp-livekit (listed in the marketplace) fails `baseline`** (`hostNetwork=true`, hostPort 7881). After CMP-068 it cannot be installed in an Organization unless it is changed or exempted.
- **The syncer handles refusals as expected.** Under enforce, a privileged Pod created inside the vCluster is refused on the host. The syncer records a SyncError event and the virtual Pod stays Pending, so the console should show that event to users.
- **CMP-G07 can be built as specified.** The refusal message names "baseline:latest", and a CSI Pod without a token is admitted.

Branch `spike/CMP-S5` (commit 9596139cd) is left in place as the evidence.
