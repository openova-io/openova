# Stage 2 (W1a): Trust floor

Injected credentials, seal, deny by default, signing, backup and restore.

118 tasks. One task is one pull request. Rows are in the order the tasks can run. "Waits for" lists the tasks that must be merged first; every task also waits for the first two tasks of the plan, `FND-021` and `FND-022`.

[Back to the overview](README.md)

## Areas in this stage

- [FND: Foundation gate and execution rules](#fnd-foundation-gate-and-execution-rules) — 12 tasks
- [CLD: Cloud abstraction](#cld-cloud-abstraction) — 4 tasks
- [SOV: Sovereign lifecycle](#sov-sovereign-lifecycle) — 17 tasks
- [SEC: Secrets and root of trust](#sec-secrets-and-root-of-trust) — 50 tasks
- [NET: Network, zones and cluster classes](#net-network-zones-and-cluster-classes) — 20 tasks
- [CTL: Control plane](#ctl-control-plane) — 4 tasks
- [BSS: Commerce and billing](#bss-commerce-and-billing) — 1 tasks
- [AIP: Agent plane](#aip-agent-plane) — 4 tasks
- [INF: Infrastructure plane](#inf-infrastructure-plane) — 6 tasks

## FND Foundation gate and execution rules

### Group FND-180: Backup and restore drills

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-181**<br>decision, 3 days | **Decision record 0036: backup and restore**<br>One accepted record states, per data class, the schedule, the retention, the object-store path and the form of the restore drill. | FND-106 | stated in its ticket |
| **FND-182**<br>new, 3 days | **Scheduled etcd snapshots to the Sovereign's object store**<br>Every control-plane server takes etcd snapshots on the schedule of record 0036 and stores them in the Sovereign's object store. | FND-181, FND-120, NET-031 | stated in its ticket |
| **FND-183**<br>new, 3 days | **Scheduled backups of the platform databases**<br>Every platform database cluster has a scheduled backup to the Sovereign's object store, on the schedule of record 0036. | FND-181, FND-120, FND-011 | stated in its ticket |
| **FND-184**<br>new, 3 days | **Scheduled Git bundles of the Sovereign's repositories**<br>Every repository of the Sovereign's Git server is bundled to the object store on the schedule of record 0036. | FND-181, FND-120, FND-011 | stated in its ticket |
| **FND-185**<br>test, 5 days | **Restore drills as gate checks**<br>Three gate checks restore an etcd snapshot, a database backup and a Git bundle on every train and compare the result with the source. | FND-182, FND-183, FND-184, FND-034 | Makes FND-G16, FND-G17, FND-G18 required. |

<details><summary>Files each task of this group changes</summary>

- **FND-181**: `docs/adr/0036-backup-and-restore.md`, `docs/adr/README.md`
- **FND-182**: `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/_shared/tests/**`
- **FND-183**: `platform/postgres/chart/templates/**`, `platform/postgres/chart/values.yaml`, `platform/postgres/chart/tests/**`
- **FND-184**: `platform/gitea/chart/templates/**`, `platform/gitea/chart/values.yaml`, `platform/gitea/chart/tests/**`
- **FND-185**: `tests/acceptance/sovereign/internal/suite/check_restore_etcd*.go`, `tests/acceptance/sovereign/internal/suite/check_restore_database*.go`, `tests/acceptance/sovereign/internal/suite/check_restore_git*.go`, `docs/ledger/gates.yaml`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-130**<br>test, 3 days | **The gate suite runs on the local provider pack on every pull request**<br>The gate suite runs on the hermetic local pack on every pull request, and every required check that lists local binds every merge. | CLD-086, CLD-087, FND-034, FND-017 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **FND-130**: `.github/workflows/conformance-local.yaml`, `.github/rulesets/main.json`, `scripts/acceptance-sovereign.sh`

</details>

### Group FND-150: Self-conformance in the Sovereign

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-151**<br>test, 3 days | **The suite as an image, with the `self` selection**<br>The suite is a signed image that runs the self selection inside a cluster and writes a report object. | FND-034, FND-062, FND-130 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **FND-151**: `tests/acceptance/sovereign/Containerfile`, `.github/workflows/build-conformance.yaml`, `tests/acceptance/sovereign/internal/suite/report*.go`, `tests/acceptance/sovereign/internal/suite/runner.go`, `tests/acceptance/sovereign/internal/suite/result.go`, `tests/acceptance/sovereign/cmd/sovereign-acceptance/main.go`, `tests/acceptance/sovereign/internal/kube/kube.go`, `scripts/acceptance-gate.py`, `.github/workflows/conformance-local.yaml`

</details>

### Group FND-140: The release train as a workflow

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-141**<br>test, 3 days | **Flight workflow: one fresh installation with pre-flight and leftover sweep**<br>One workflow makes one fresh installation of a candidate: pre-flight gate, wipe of the standing environment, leftover sweep, fire, gate run. It runs when a merged change touches first-boot files, at most once a week, and twice in a row only at the exit of a stage. | FND-120 | stated in its ticket |
| **FND-144**<br>test, 3 days | **The train publishes the release**<br>A candidate that passed the verdict is built into a release, signed, published, copied to the root origin, and the channel moves last. | FND-141, SOV-225, FND-062 | Makes SOV-G00 required. |

<details><summary>Files each task of this group changes</summary>

- **FND-141**: `.github/workflows/release-train.yaml`, `scripts/sovereign-lifecycle.sh`, `docs/RUNBOOKS.md`
- **FND-144**: `.github/workflows/release-train.yaml`, `docs/RUNBOOKS.md`

</details>

### Group FND-190: Remove the second vulnerability matcher

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-191**<br>delete, 3 days | **Remove the second matcher's job handling from the API**<br>The API holds no retry, exclusion or schedule code for the second matcher's job. | FND-120, FND-171 | stated in its ticket |
| **FND-192**<br>delete, 3 days | **Remove the second matcher from the console**<br>No console page, wizard card or console test names the second matcher. | FND-120 | stated in its ticket |
| **FND-193**<br>delete, 5 days | **Remove the chart `bp-syft-grype`, its slot and its pins**<br>The Blueprint of the second matcher, its kit slot and every pin and policy entry for it are gone, so Trivy is the one matcher a Sovereign runs. | FND-191, FND-192, FND-037, FND-011, NET-030 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **FND-191**: `products/catalyst/bootstrap/api/internal/handler/jobs_retry.go`, `products/catalyst/bootstrap/api/internal/handler/jobs_retry_syft_identity_row176_test.go`, `products/catalyst/bootstrap/api/internal/handler/jobs_scanner_exclusion_test.go`, `products/catalyst/bootstrap/api/internal/handler/schedules.go`, `products/catalyst/bootstrap/api/internal/helmwatch/reconcilers.go`, `products/catalyst/bootstrap/api/internal/helmwatch/reconcilers_test.go`, `products/catalyst/bootstrap/api/internal/helmwatch/scanner_failed_leaf_5929_test.go`, `products/catalyst/bootstrap/api/internal/jobs/reconciler_bridge.go`, `products/catalyst/bootstrap/api/internal/jobs/reconciler_bridge_stale_run_164_test.go`, `docs/ledger/deleted-paths.txt`
- **FND-192**: `products/catalyst/bootstrap/ui/src/entities/deployment/model.ts`, `products/catalyst/bootstrap/ui/src/pages/marketplace/marketplaceCopy.ts`, `products/catalyst/bootstrap/ui/src/pages/sovereign/RetryJobButton.notify-row176.test.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/jobs-schedule/CronScheduleView.actions-6703.test.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/jobs-schedule/cronScheduleModel.adherence-6703.test.ts`, `products/catalyst/bootstrap/ui/src/pages/wizard/steps/componentFootprints.ts`, `products/catalyst/bootstrap/ui/src/pages/wizard/steps/componentGroups.ts`, `products/catalyst/bootstrap/ui/src/pages/wizard/steps/componentLogos.tsx`, `products/catalyst/bootstrap/ui/src/pages/wizard/steps/logoTone.ts`, `.github/workflows/catalyst-build.yaml`
- **FND-193**: `platform/syft-grype/**`, `clusters/_template/bootstrap-kit/33-syft-grype.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `clusters/omantel.omani.works/bootstrap-kit/33-syft-grype.yaml`, `clusters/omantel.omani.works/bootstrap-kit/kustomization.yaml`, `clusters/otech.omani.works/bootstrap-kit/33-syft-grype.yaml`, `clusters/otech.omani.works/bootstrap-kit/kustomization.yaml`, `scripts/expected-bootstrap-deps.yaml`, `platform/network-policies/chart/values.yaml`, `platform/self-sovereign-cutover/chart/values.yaml`, `platform/newapi/chart/Chart.yaml` and 7 more

</details>

## CLD Cloud abstraction

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-080**<br>new, 5 days | **Contract directory: module contract, profile schema, pack schema, conventions, two profiles**<br>As the body, with the schema of section 4.3 as it now stands: no mesh section, apiCIDRs added, controller a legacy value, no cost section, other areas' sections reserved by name only, pack.yaml with tofuProviders. Also publishes docs/contracts/c6-crossplane-conventions.md. Moves CLD-G03 from not-built to expected-fail. | CLD-070, FND-031 | Adds the check CLD-G03. |

<details><summary>Files each task of this group changes</summary>

- **CLD-080**: `infra/providers/_contract/**`, `products/catalyst/bootstrap/api/internal/cloudprofile/**`, `.github/workflows/check-provider-contract.yaml`, `docs/contracts/c6-crossplane-conventions.md`

</details>

### Group CLD-140: Epic: provider overlays, one overlay path

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-142**<br>rework, 3 days | **Neutral storage gate**<br>Chart and slot storage-ready; 16-cnpg depends on it and on no provider release | FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-142**: `platform/storage-ready/**`, `scripts/expected-bootstrap-deps.yaml`

</details>

### Group CLD-085: Epic: the local provider pack

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-086**<br>new, 5 days | **local module and reduced kit**<br>Module meeting contract version 1, proven with a two-region fixture, so two clusters. The reduced kit adds the console slot 13-bp-catalyst-platform and the closure the CLD-055 record lists. Port 53 is served on the machine's own address. Machines answer on TCP 22 from pods and restart after a reboot. The kit generator is scripts/generators/local-kit.py with --check; it reads the exclusion list infra/providers/local/kit-exclude.txt, empty at first, which INF extends. If a seal hook exists for another pack, the local hook is added here with an empty volume device. | CLD-040, CLD-055, CLD-080, CLD-142, FND-102 | stated in its ticket |
| **CLD-087**<br>test, 3 days | **CI job conformance-local**<br>CI job conformance-local. | CLD-086, FND-031 | Makes CLD-G11 required. |

<details><summary>Files each task of this group changes</summary>

- **CLD-086**: `infra/providers/local/**`, `clusters/_template/providers/local/kit/**`, `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/_shared/tests/render.tf`, `scripts/generators/local-kit.py`, `infra/providers/local/kit-exclude.txt`, `infra/providers/local/seal/**`, `infra/providers/_contract/fixtures/**`
- **CLD-087**: `.github/workflows/conformance-local.yaml`, `.github/workflows/conformance-local-cache.yaml`, `scripts/local-pack.sh`

</details>

## SOV Sovereign lifecycle

### Group SOV-100: EPIC: Upstream contract and born-local bootstrap

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-110**<br>new, 3 days | **Contract C5: schema, kit key list, the upstream package and its validator**<br>Frozen C5 schema, Go package upstream (FromMap, ToMap, Validate, Served, Flipped, Load, RegistriesYAML), guard G8 | FND-031 | stated in its ticket |
| **SOV-130**<br>rework, 5 days | **Substrate window, part 1: modules and cloud-init take the variables document**<br>Module input variables; two-endpoint node list with wildcard; k3s flag; ConfigMap sovereign-variables with substituteFrom on five Kustomizations; DNS key moved to an uncommitted Secret; provisioner uses upstream.Served | SOV-060, SOV-110, FND-014, NET-031 | stated in its ticket |
| **SOV-150**<br>rework, 5 days | **Kit sources become substitutions, with a render-equivalence test**<br>Chart sources and pull-secret names read contract keys; byte-identical render proven; package lines left for SOV-240 | SOV-130 | stated in its ticket |
| **SOV-160**<br>rework, 3 days | **Registry projects, the Organization Git token and the two repositories are created at install**<br>Hosted projects, pull robot, Organization token, repositories openova/sovereign-regions and openova/provider-pack at install | CTL-044, SOV-150, FND-011 | stated in its ticket |
| **SOV-180**<br>rework, 5 days | **The API and the controllers read the variables and the mode at run time**<br>As in the body, plus: pinIssuer() returns the Sovereign's own console address, not the mothership issuer. | CTL-035, SOV-110, SOV-130, FND-011 | stated in its ticket |
| **SOV-120**<br>new, 3 days | **The sovereign-ops command, its image, and seed-repo**<br>One Go command with subcommand dispatcher, JSON result line, exit codes, image with tofu, cosign, git, helm; subcommand seed-repo | CTL-045, CTL-110, CTL-112, SOV-110 | stated in its ticket |
| **SOV-170**<br>new, 3 days | **The Sovereign repository is seeded at bootstrap and serves the root**<br>Root Flux objects and variables committed to the Sovereign's own repository and owned by Flux; check SOV.01 | FND-032, FND-120, SOV-120, SOV-130, SOV-160 | Makes SOV-G01 required. |

<details><summary>Files each task of this group changes</summary>

- **SOV-110**: `docs/contracts/c5-upstream-variables.schema.json`, `docs/contracts/c5-kit-keys.txt`, `core/controllers/pkg/upstream/**`, `scripts/check-variables-documents.sh`, `.github/workflows/check-variables-documents.yaml`, `tests/fixtures/variables/**`
- **SOV-130**: `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/_shared/tests/cloudinit_render.tftest.hcl`, `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`
- **SOV-150**: `clusters/_template/**`, `scripts/check-kit-render-equivalence.sh`, `tests/fixtures/variables/today.env`, `.github/workflows/check-kit-render-equivalence.yaml`
- **SOV-160**: `platform/harbor/chart/templates/`, `platform/gitea/chart/templates/`, `clusters/_template/bootstrap-kit/19-harbor.yaml`, `clusters/_template/bootstrap-kit/10-gitea.yaml`
- **SOV-180**: `products/catalyst/bootstrap/api/internal/handoverjwt/signer.go`, `products/catalyst/bootstrap/api/cmd/api/main.go`, `core/controllers/organization/internal/controller/per_org_flux.go`, `core/services/provisioning/gitops/helmrelease_apps.go`, `products/catalyst/chart/templates/api-deployment.yaml`
- **SOV-120**: `core/cmd/sovereign-ops/**`, `.github/workflows/build-sovereign-ops.yaml`
- **SOV-170**: `tests/acceptance/sovereign/internal/suite/`, `tests/acceptance/sovereign/internal/suite/check_born_local.go`

</details>

### Group SOV-200: EPIC: Hand-over as a short ordered flip

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-225**<br>new, 5 days | **The release builder, and the root origin check**<br>As in the body, plus: the builder also takes the images a chart lists in its Chart.yaml annotation artifacthub.io/images (new test TestBuildAddsAnnotatedImages). It implements SOV-G00 as expected-fail; FND-144 flips it. | FND-032, FND-061, FND-141, SOV-120 | Adds the check SOV-G00. |
| **SOV-210**<br>test, 5 days | **Hand-over scenario, part 1: fixture parent and bootstrap on the hermetic local provider pack**<br>Fixture parent registry with a signed fixture release, reduced kit and make bootstrap, run as one job of the workflow conformance-local on the pack that scripts/local-pack.sh brings up. No workflow, runner or report mode of its own. | CLD-086, CLD-087, FND-130, SOV-170, SOV-225 | stated in its ticket |
| **SOV-212**<br>test, 3 days | **Hand-over scenario, part 2: parent stub, hand-over run and assertions**<br>Parent API stub, PowerDNS, mail catcher, S3 store; make handover; make assert runs the registered lifecycle checks that list run-in local. No result.json. | SOV-210 | stated in its ticket |
| **SOV-230**<br>rework, 5 days | **closure-copy, and the harbor-prewarm step body replaced by it**<br>Digest copy of the signed closure, provider-pack tag push, --set-channel; three enumeration templates deleted | FND-144, SOV-120, SOV-160, SOV-212, SOV-225, FND-011 | stated in its ticket |
| **SOV-240**<br>rework, 5 days | **The upstream-flip step; steps 5, 6, 7, 9 and 10 deleted**<br>As in the body, without 'handover-hermetic enforcing': the lifecycle checks that list local are required and bind every merge through the workflow conformance-local. | CTL-110, CTL-112, FND-144, SOV-150, SOV-170, SOV-180, SOV-225, SOV-230, SOV-510, FND-011 | stated in its ticket |
| **SOV-250**<br>rework, 3 days | **registry-pivot only removes the origin endpoint**<br>Node file rendered from the variables, local-only after the flip; Dragonfly upstream local from first boot | FND-032, SOV-060, SOV-240, FND-011 | Makes SOV-G03 required. |

<details><summary>Files each task of this group changes</summary>

- **SOV-225**: `core/cmd/sovereign-ops/internal/releasebuild/**`, `core/controllers/pkg/release/**`, `scripts/release-tool`, `tests/fixtures/release/**`, `tests/acceptance/sovereign/internal/suite/check_origin_serves_release.go`
- **SOV-230**: `core/cmd/sovereign-ops/internal/closure/**`, `platform/self-sovereign-cutover/chart/templates/03-harbor-prewarm-job.yaml`, `platform/self-sovereign-cutover/chart/templates/03a-offline-mirror-resolver.yaml`, `platform/self-sovereign-cutover/chart/templates/03b-bootstrap-kit-pins.yaml`, `platform/self-sovereign-cutover/chart/templates/03c-catalog-chart-set.yaml`, `platform/self-sovereign-cutover/chart/templates/secondary-harbor-prewarm-script-configmap.yaml`, `platform/self-sovereign-cutover/chart/values.yaml`, `docs/ledger/deleted-paths.txt`
- **SOV-240**: `core/cmd/sovereign-ops/internal/flip/**`, `platform/self-sovereign-cutover/chart/templates/05-upstream-flip-job.yaml`, `clusters/_template/infrastructure/providers/base/provider-opentofu.yaml`, `clusters/_template/infrastructure/providers/hetzner/provider-hcloud.yaml`, `platform/self-sovereign-cutover/chart/templates/rbac.yaml`, `platform/self-sovereign-cutover/chart/values.yaml`, `docs/ledger/deleted-paths.txt`
- **SOV-250**: `platform/self-sovereign-cutover/chart/templates/04-registry-pivot-daemonset.yaml`, `platform/self-sovereign-cutover/chart/values.yaml`, `clusters/_template/bootstrap-kit/06-bp-dragonfly.yaml`, `products/catalyst/bootstrap/api/internal/handler/cutover.go`, `tests/acceptance/sovereign/internal/suite/`, `tests/acceptance/sovereign/internal/suite/check_no_outside_source.go`

</details>

### Group SOV-500: EPIC: Release channel and in-place upgrade

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-510**<br>rework, 5 days | **The catalogue is the verified kit artifact**<br>OCIRepository catalog with cosign verification replaces the Git clone; request field releaseVersion; sovereign-release substituteFrom | CTL-620, FND-120, FND-144, SOV-170, SOV-225, NET-031 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SOV-510**: `infra/providers/_shared/cloudinit-control-plane.tftpl`, `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `products/catalyst/bootstrap/api/internal/handler/deployments.go`, `clusters/_template/sovereign-region/**`, `core/cmd/sovereign-ops`, `docs/contracts/c5-kit-keys.txt`

</details>

### Group SOV-700: EPIC: Day-2 with the parent switched off

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-720**<br>new, 5 days | **Kubernetes upgrade by system-upgrade-controller**<br>As in the body, plus: both Plans carry spec.window, default 22:00 to 00:30 UTC, outside the reboot span of the infrastructure area. The ticket quotes the four steps of a new kit slot in place of the git-log instruction. | CTL-035, FND-032, SOV-150, SOV-230, SOV-250, FND-011 | Makes SOV-G11 required. |
| **SOV-725**<br>rework, 1 day | **k3s default moves to the 1.32 line**<br>The default of k3s_version in every provider module moves one minor, to the newest 1.32 patch. A kept cluster is raised by one commit through the version route of SOV-720. One kernel upgrade, scheduled by the foundation area's upgrade stream. | SOV-720 | stated in its ticket |
| **SOV-726**<br>rework, 1 day | **k3s default moves to the 1.33 line**<br>The default of k3s_version moves to the 1.33 line, at v1.33.10+k3s1 or later. This is the fleet's k3s line that SEC-170 and SEC-262 wait for. A kept cluster is raised by a second commit through the version route. | SOV-725 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SOV-720**: `platform/system-upgrade-controller/**`, `clusters/_template/bootstrap-kit/04a-bp-system-upgrade-controller.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `products/catalyst/bootstrap/api/internal/lifecycle/kubernetes_version.go`, `tests/acceptance/sovereign/internal/suite/check_kubernetes_upgrade.go`
- **SOV-725**: `infra/providers/*/variables.tf`, `docs/RUNBOOKS.md`
- **SOV-726**: `infra/providers/*/variables.tf`, `docs/RUNBOOKS.md`

</details>

## SEC Secrets and root of trust

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SEC-280**<br>lab test, 5 days | **Spike: database engine on CloudNativePG**<br>Behaviour of dynamic users and static roles beside the operator, across a region switch and an OpenBao outage. | nothing else | stated in its ticket |
| **SEC-271**<br>lab test, 3 days | **Spike: the policy engine verifies a signature with no public log**<br>A result record with YES or NO for three questions: the installed engine verifies a key-signed image with no route to a transparency log (and the field that switches the log check off); the engine can read signatures from the Sovereign's own registry; the closure copy carries the signature artifacts. It also records the list of image references. All YES leads to SEC-272. A NO on the first keeps the controller of kit slot 32 as the one verifier and drops SEC-272, SEC-273 and SEC-274. | FND-061 | stated in its ticket |
| **SEC-272**<br>new, 3 days | **ImageValidatingPolicy for platform images, in audit**<br>One ImageValidatingPolicy with action Audit that checks the image references recorded by SEC-271 against the release public key and asks for no transparency-log entry. Values block imageVerification of section 4.13. Guard G12 keeps the chart's key copy byte-equal to keys/release-cosign.pub. Holds lock policy-chart. | SEC-271, FND-011, FND-014, FND-031, FND-032, FND-052, FND-053, NET-030, CMP-014 | stated in its ticket |
| **SEC-274**<br>delete, 3 days | **Delete the second image verifier: kit slot 32 and platform/sigstore**<br>Kit slot 32 and the chart folder platform/sigstore are gone, with every reference to the release. Other files that name the slot are found by the ticket's precondition scan and added by the architect. | FND-091, SEC-272 | stated in its ticket |
| **SEC-180**<br>new, 3 days | **OpenBao snapshot backup and restore drill**<br>Six-hourly raft snapshot to object storage and an automated restore check. | SEC-162, FND-011 | Makes SEC-G10 required. |
| **SEC-190**<br>new, 5 days | **Mounts, templated policies and generic roles**<br>Mounts mounted/ and transit/; policies workload, eso-ns, tofu-state, tofu-state-backup, tofu-state-mint and sovereign-admin; the roles of section 4.6 marked SEC-190; key transit/keys/tofu-state created with exportable=true and allow_plaintext_backup=true; token role tofu-state; values block stateKey; runbook section 'Offline backup of the state key'. | SEC-162, FND-011 | stated in its ticket |
| **SEC-200**<br>new, 5 days | **Library chart and namespaced stores for platform namespaces**<br>Library templates secretStores, generatedSecrets, envFromFiles; peer stores for tier B; platform charts on namespaced stores. | SEC-080, SEC-140, SEC-190, FND-011 | stated in its ticket |
| **SEC-250**<br>new, 5 days | **PKI engine, internal issuer and mesh issuer**<br>One root in the primary region, one intermediate per region, ClusterIssuer openbao-internal, role mesh and ClusterIssuer sovereign-mesh-ca. The signing request of a secondary region travels over the north-south gateway. If case B of NET-120 fails, the package is re-planned to one authority in the first region. | NET-120, SEC-190, FND-011 | Makes SEC-G09 required. |
| **SEC-192**<br>new, 3 days | **Producer identities and the simulation back end's own-path role**<br>Producer identities and the simulation back end's own-path role. | SEC-190, SEC-211, FND-011 | stated in its ticket |
| **SEC-335**<br>lab test, 3 days | **Spike: injection route for each upstream chart**<br>Per class U entry: five answers, the route of 4.9 that works, pod-start result. | SEC-211 | stated in its ticket |
| **SEC-205**<br>new, 3 days | **Stores in Organization namespaces**<br>Service account eso-reader and the two namespaced stores in every Organization's host-applied tree. | CTL-201, FND-120, SEC-200 | stated in its ticket |
| **SEC-115**<br>document, 3 days | **SECURITY.md section 5 rewritten to the built state**<br>Section 5 (OpenBao) and the secret-flow text describe what is built: injection, seal, register. Parts not built carry the target marker. | SEC-006, SEC-110, SEC-162, SEC-213 | stated in its ticket |
| **SEC-273**<br>new, 3 days | **Image verification denies; check SEC-G14**<br>Default action Deny. Check SEC-G14 added and flipped to required. Precondition: the audit count of the policy is zero in the records of two consecutive trains. | FND-032, FND-062, FND-141, SEC-272, FND-011, NET-030 | Makes SEC-G14 required. |
| **SEC-290**<br>new, 5 days | **Database engine: mount, binder, static and dynamic roles**<br>Database mount, binder Job, static role for the proof Application and removal of its class U Secret, dynamic role contract with the ttl field, ConfigMap <cluster>-openbao-db-roles, and the assertion that the management role is not a superuser. | FND-120, SEC-213, SEC-231, SEC-280, FND-011 | Makes SEC-G13 required. |
| **SEC-300**<br>new, 5 days | **Dynamic database users for one first-party service**<br>Package baocreds and the billing engine on one-hour database users. | SEC-290, FND-011 | Makes SEC-G15 required. |
| **SEC-207**<br>fix, 1 day | **Fence the legacy cluster-wide store**<br>Namespace condition on the local cluster store and the second admission validation. | SEC-213, SEC-214, SEC-222, SEC-223, SEC-224, SEC-233, SEC-234, SEC-236, SEC-242, FND-011 | stated in its ticket |
| **SEC-170**<br>fix, 5 days | **Existing clusters: tokens, etcd encryption, seal migration, salt**<br>In-place migration script and runbook for the mothership and any kept cluster. | SEC-130, SEC-162, SOV-720, SOV-726 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SEC-280**: `docs/ledger/plan/spikes/`
- **SEC-271**: `docs/ledger/plan/spikes/`
- **SEC-272**: `platform/kyverno-policies/chart/templates/baseline/26-image-signature.yaml`, `platform/kyverno-policies/chart/values.yaml`, `platform/kyverno-policies/chart/files/release-cosign.pub`, `platform/kyverno-policies/chart/tests/`, `scripts/`, `.github/workflows/`
- **SEC-274**: `clusters/_template/bootstrap-kit/32-sigstore.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `platform/sigstore/`, `docs/ledger/deleted-paths.txt`
- **SEC-180**: `platform/openbao/chart/templates/backup-snapshot.yaml`, `platform/openbao/chart/templates/_config.tpl`, `platform/openbao/chart/values.yaml`, `platform/openbao/chart/tests/backup-render.sh`, `docs/RUNBOOKS.md`, `docs/ledger/gates.yaml`
- **SEC-190**: `platform/openbao/chart/templates/_config.tpl`, `platform/openbao/chart/values.yaml`, `platform/openbao/chart/templates/sso-configure-deployment.yaml`, `platform/openbao/chart/tests/config-desired-render.sh`, `platform/openbao/chart/tests/policy-templates-render.sh`, `tests/e2e/bp-openbao-config-live.sh`, `docs/RUNBOOKS.md`
- **SEC-200**: `platform/secrets-lib/chart/**`, `platform/secrets-lib/README.md`, `platform/external-secrets-stores/chart/templates/clustersecretstore-peers.yaml`, `platform/external-secrets-stores/chart/tests/peer-stores-render.sh`, `platform/external-secrets-stores/chart/values.yaml`, `platform/openbao/chart/templates/_config.tpl`, `scripts/secret-guard-baselines/check-reflector-scope.txt`
- **SEC-250**: `platform/openbao/chart/templates/_config.tpl`, `platform/openbao/chart/values.yaml`, `platform/openbao/chart/templates/pki-bootstrap-job.yaml`, `platform/openbao/chart/tests/pki-render.sh`, `platform/cert-manager-issuers/chart/templates/clusterissuer-openbao-internal.yaml`, `clusters/_template/bootstrap-kit/08-openbao.yaml`, `docs/ledger/gates.yaml`
- **SEC-192**: `platform/openbao/chart/values.yaml`, `platform/openbao/chart/templates/_config.tpl`, `platform/openbao/chart/tests/config-desired-render.sh`, `tests/e2e/bp-openbao-config-live.sh`
- **SEC-335**: `docs/ledger/plan/spikes/`
- **SEC-205**: `core/controllers/organization/internal/gitops/manifests.go`, `core/controllers/organization/internal/gitops/*_test.go`
- **SEC-115**: `docs/SECURITY.md`
- **SEC-273**: `platform/kyverno-policies/chart/values.yaml`, `platform/kyverno-policies/chart/tests/`, `docs/ledger/gates.yaml`
- **SEC-290**: `platform/openbao/chart/values.yaml`, `platform/openbao/chart/templates/_config.tpl`, `platform/cnpg-pair/chart/templates/openbao-db-bind-job.yaml`, `platform/postgres/chart/templates/openbao-db-bind-job.yaml`, `platform/cnpg-pair/chart/values.yaml`, `platform/postgres/chart/values.yaml`, `platform/wordpress-tenant/chart/**`, `scripts/secret-guard-baselines/secret-object-register.yaml`, `tests/acceptance/sovereign/internal/suite/sec_db.go`, `docs/ledger/gates.yaml`
- **SEC-300**: `products/chargeback/internal/baocreds/`, `products/chargeback/internal/store/store.go`, `products/chargeback/chart/templates/`, `products/chargeback/chart/values.yaml`
- **SEC-207**: `platform/external-secrets-stores/chart/templates/clustersecretstore-vault-region1.yaml`, `platform/external-secrets-stores/chart/templates/remote-key-scope-policy.yaml`, `platform/external-secrets-stores/chart/tests/remote-key-scope-render.sh`
- **SEC-170**: `docs/RUNBOOKS.md`, `scripts/sec-migrate-existing-sovereign.sh`, `scripts/check-live-trust-root.sh`

</details>

### Group SEC-160: EPIC: OpenBao 2.7 with the seal out of etcd

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SEC-150**<br>new, 5 days | **Seal-provider contract and the static node-file implementation**<br>Seal hook module per provider, 32-byte key on a dedicated volume of control-plane nodes, ConfigMap sovereign-seal-profile. | CLD-080, SEC-005, SEC-040, SEC-130, SEC-135 | stated in its ticket |
| **SEC-161**<br>rework, 3 days | **Reconciler: secrets mounts and accessor substitution**<br>Kind secrets-mount, accessor substitution, reconciler writes its own policy first, KV mount as an operation. | SEC-100, FND-011 | stated in its ticket |
| **SEC-162**<br>rework, 5 days | **OpenBao 2.7.2, static seal, self-initialisation and declared audit device, as one change**<br>No init Job, unseal Secret, unseal loop, root script or root token; auto-unseal from the seal profile; audit to standard output. | SEC-100, SEC-130, SEC-150, SEC-161, FND-011 | Makes SEC-G03, SEC-G05 required. |
| **SEC-155**<br>new, 5 days | **Key-service seal on the reference cloud (only if SEC-135 answered YES)**<br>Rank 1 seal for the reference pack: key in the cloud key service, plug-in loaded by the chart. | SEC-135, SEC-150, SEC-162, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SEC-150**: `infra/providers/PROVIDER-INTERFACE.md`, `infra/providers/hetzner/seal/main.tf`, `infra/providers/huawei/seal/main.tf`, `infra/providers/hetzner/main.tf`, `infra/providers/huawei/main.tf`, `infra/providers/hetzner/variables.tf`, `infra/providers/huawei/variables.tf`, `infra/providers/hetzner/outputs.tf`, `infra/providers/huawei/outputs.tf`, `infra/providers/hetzner/.terraform.lock.hcl`, `infra/providers/huawei/.terraform.lock.hcl`, `infra/providers/_shared/cloudinit-control-plane.tftpl` and 2 more
- **SEC-161**: `platform/openbao/chart/files/openbao-config-reconcile.sh`, `platform/openbao/chart/templates/_config.tpl`, `platform/openbao/chart/templates/config-reconciler.yaml`, `platform/openbao/chart/tests/config-reconcile-idempotence.sh`, `platform/openbao/chart/tests/config-desired-render.sh`, `tests/e2e/bp-openbao-config-live.sh`
- **SEC-162**: `platform/openbao/chart/Chart.yaml`, `platform/openbao/chart/values.yaml`, `platform/openbao/chart/templates/init-job.yaml`, `platform/openbao/chart/templates/auth-bootstrap-job.yaml`, `platform/openbao/chart/templates/recovery-seed-bootstrap.yaml`, `platform/openbao/chart/templates/unseal-reconciler.yaml`, `platform/openbao/chart/templates/auto-unseal-rbac.yaml`, `platform/openbao/chart/files/openbao-generate-root.sh`, `platform/openbao/chart/templates/seal-config.yaml`, `platform/openbao/chart/templates/_config.tpl`, `platform/openbao/chart/tests/`, `clusters/_template/bootstrap-kit/08-openbao.yaml` and 6 more
- **SEC-155**: `infra/providers/huawei/seal/main.tf`, `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/PROVIDER-INTERFACE.md`, `platform/openbao/chart/templates/seal-config.yaml`, `platform/openbao/chart/values.yaml`, `platform/openbao/chart/tests/seal-plugin-render.sh`

</details>

### Group SEC-260: EPIC: External Secrets upgrade

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SEC-261**<br>test, 3 days | **Conformance test for External Secrets against OpenBao**<br>Five-behaviour conformance test on kind. | SEC-162 | stated in its ticket |
| **SEC-262**<br>rework, 5 days | **External Secrets to 0.16.2 and every manifest to the v1 API**<br>Operator on 0.16.2; all manifests on v1. | SEC-261, SOV-720, SOV-726, FND-011 | stated in its ticket |
| **SEC-263**<br>rework, 3 days | **External Secrets to the current 2.x minor**<br>Operator on a supported 2.x minor. | SEC-262, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SEC-261**: `tests/e2e/eso-openbao-conformance.sh`, `.github/workflows/test-eso-openbao-conformance.yaml`
- **SEC-262**: `platform/external-secrets/chart/Chart.yaml`, `platform/external-secrets/chart/values.yaml`, `platform/secrets-lib/chart/templates/*.tpl`
- **SEC-263**: `platform/external-secrets/chart/Chart.yaml`, `platform/external-secrets/chart/values.yaml`

</details>

### Group SEC-210: EPIC: injection

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SEC-211**<br>new, 5 days | **Blueprint bp-secrets-store-csi and the injection negative check**<br>CSI driver with sync off in the kit; canary proof that a mounted value never becomes a Secret. | SEC-140, SEC-190, FND-011 | Makes SEC-G08 required. |
| **SEC-212**<br>new, 5 days | **First-party proof: the marketplace API reads its signing secret from a mounted file**<br>Package secretfile; one chart-generated Secret and its random site replaced by a mounted value. | SEC-200, SEC-211, FND-011 | stated in its ticket |
| **SEC-213**<br>new, 5 days | **First Application proof: WordPress runs with its database password injected**<br>WordPress pods read the database password from a mount; one registered class U Secret remains for the database operator until SEC-290. | FND-120, SEC-140, SEC-200, SEC-211, FND-011 | Makes SEC-G12 required. |
| **SEC-214**<br>fix, 5 days | **Marketplace Organization path, single-cluster databases: credentials injected, nothing in Git**<br>Generator emits generator, push and SecretProviderClass on the host side and CSI volumes in pods; password read-back from Git deleted. | CTL-201, SEC-071, SEC-140, SEC-205, SEC-213 | stated in its ticket |
| **SEC-215**<br>fix, 5 days | **Marketplace Organization path, database pair: credentials out of Git**<br>Pair HelmReleases reference a Secret by name; no password value in any rendered file. | FND-120, SEC-214 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SEC-211**: `platform/secrets-store-csi/**`, `clusters/_template/bootstrap-kit/15b-bp-secrets-store-csi.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `scripts/expected-bootstrap-deps.yaml`, `clusters/_template/bootstrap-kit/08-openbao.yaml`, `docs/ledger/gates.yaml`
- **SEC-212**: `core/marketplace-api/internal/secretfile/`, `core/marketplace-api/main.go`, `products/catalyst/chart/templates/marketplace-api/`, `products/catalyst/chart/Chart.yaml`, `products/catalyst/chart/values.yaml`, `scripts/secret-guard-baselines/*.txt`
- **SEC-213**: `platform/wordpress-tenant/chart/**`, `scripts/secret-guard-baselines/*.txt`, `scripts/secret-guard-baselines/secret-object-register.yaml`, `docs/ledger/gates.yaml`
- **SEC-214**: `core/services/provisioning/gitops/gitops.go`, `core/services/provisioning/gitops/perorg_apps_tree.go`, `core/services/provisioning/gitops/*_test.go`, `core/services/provisioning/gitops/testdata/secret-literal-baseline.txt`, `core/services/provisioning/handlers/consumer.go`, `scripts/secret-guard-baselines/check-no-secret-writers.txt`
- **SEC-215**: `core/services/provisioning/gitops/gitops.go`, `core/services/provisioning/gitops/*_test.go`, `core/services/provisioning/gitops/testdata/secret-literal-baseline.txt`, `scripts/secret-guard-baselines/secret-object-register.yaml`

</details>

### Group SEC-220: EPIC: roll-out, identity charts

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SEC-221**<br>rework, 5 days | **Roll-out: platform/keycloak**<br>Five derivation helpers and the salt replaced by generated secrets; realm import fed from them. | SEC-060, SEC-200, SEC-212, FND-011 | stated in its ticket |
| **SEC-222**<br>rework, 3 days | **Roll-out: platform/sso-bridge**<br>Session, cookie and crypto secrets generated independently, not hashed from the client secret. | SEC-221, FND-011 | stated in its ticket |
| **SEC-223**<br>rework, 1 day | **Roll-out: platform/oidc-gate**<br>Cookie secret from a generated secret. | SEC-222, FND-011 | stated in its ticket |
| **SEC-224**<br>rework, 5 days | **Roll-out: products/catalyst**<br>Catalyst chart random sites and Secret manifests migrated; catalyst-api identity-provider client secret mounted; mail relay credential mounted and its class T register entry removed. | SEC-223, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SEC-221**: `platform/keycloak/chart/**`, `infra/providers/_shared/cloudinit-control-plane.tftpl`, `scripts/secret-guard-baselines/*`
- **SEC-222**: `platform/sso-bridge/chart/**`, `scripts/secret-guard-baselines/*`
- **SEC-223**: `platform/oidc-gate/chart/**`, `scripts/secret-guard-baselines/*`
- **SEC-224**: `products/catalyst/chart/**`, `products/catalyst/bootstrap/api/internal/secretfile/`, `products/catalyst/bootstrap/api/cmd/api/main.go`, `scripts/secret-guard-baselines/*`

</details>

### Group SEC-230: EPIC: roll-out, data and platform service charts

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SEC-231**<br>rework, 3 days | **Roll-out: platform/postgres**<br>Role passwords born in OpenBao; role Secret kept as class U with removal SEC-290. | SEC-200, SEC-212, FND-011 | stated in its ticket |
| **SEC-232**<br>rework, 1 day | **Roll-out: platform/valkey**<br>Chart secrets by Procedure R. | SEC-200, SEC-212, FND-011 | stated in its ticket |
| **SEC-235**<br>rework, 3 days | **Roll-out: platform/powerdns**<br>Chart secrets by Procedure R. | SEC-200, SEC-212, FND-011 | stated in its ticket |
| **SEC-233**<br>rework, 3 days | **Roll-out: platform/gitea**<br>Chart secrets by Procedure R. | SEC-200, SEC-212, SEC-231, FND-011 | stated in its ticket |
| **SEC-234**<br>rework, 3 days | **Roll-out: platform/harbor**<br>Chart secrets by Procedure R. | SEC-200, SEC-212, SEC-231, FND-011 | stated in its ticket |
| **SEC-236**<br>rework, 3 days | **Roll-out: platform/powerdns-admin**<br>Chart secrets by Procedure R. | SEC-200, SEC-212, SEC-235, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SEC-231**: `platform/postgres/chart/**`, `scripts/secret-guard-baselines/*`
- **SEC-232**: `platform/valkey/chart/**`, `scripts/secret-guard-baselines/*`
- **SEC-235**: `platform/powerdns/chart/**`, `scripts/secret-guard-baselines/*`
- **SEC-233**: `platform/gitea/chart/**`, `scripts/secret-guard-baselines/*`
- **SEC-234**: `platform/harbor/chart/**`, `scripts/secret-guard-baselines/*`
- **SEC-236**: `platform/powerdns-admin/chart/**`, `scripts/secret-guard-baselines/*`

</details>

### Group SEC-240: EPIC: roll-out, remaining charts

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SEC-242**<br>rework, 3 days | **Roll-out: platform/stalwart-tenant**<br>Chart secrets by Procedure R. | SEC-200, SEC-212, FND-011 | stated in its ticket |
| **SEC-245**<br>rework, 1 day | **Roll-out: platform/guacamole**<br>Chart secrets by Procedure R; skipped if the access-plane area removed the slot. | ACC-020, SEC-200, SEC-212, FND-011 | stated in its ticket |
| **SEC-246**<br>rework, 3 days | **Roll-out: products/chargeback master key**<br>Key-generating Job deleted; master key mounted in the billing pod. | SEC-200, SEC-212, FND-011 | stated in its ticket |
| **SEC-247**<br>rework, 3 days | **Roll-out: seven driver charts with one credential Secret each**<br>Plain Secret manifests replaced by ExternalSecrets on the namespace store with class U register entries. | SEC-200, SEC-212, FND-011 | stated in its ticket |
| **SEC-244**<br>rework, 1 day | **Roll-out: products/agenity**<br>Chart secret by Procedure R. | AIP-155, SEC-200, SEC-212, FND-011 | stated in its ticket |
| **SEC-248**<br>guard, 1 day | **Census limit lowered after the chart roll-out**<br>Field allowedUnmatched set to the number of the newest gate record after the roll-out packages merged. The pull request lists every Secret still unmatched and the package that owns it. | SEC-096, SEC-221, SEC-222, SEC-223, SEC-224, SEC-231, SEC-232, SEC-233, SEC-234, SEC-235, SEC-236, SEC-242, SEC-246, SEC-247 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SEC-242**: `platform/stalwart-tenant/chart/**`, `scripts/secret-guard-baselines/*`
- **SEC-245**: `platform/guacamole/chart/**`, `scripts/secret-guard-baselines/*`
- **SEC-246**: `products/chargeback/chart/templates/app-key-job.yaml`, `products/chargeback/chart/**`, `scripts/secret-guard-baselines/*`
- **SEC-247**: `platform/mimir/chart/**`, `platform/velero/chart/**`, `platform/velero-hcs/chart/**`, `platform/cluster-autoscaler-hcloud/chart/**`, `platform/hcloud-csi/chart/**`, `platform/hcloud-ccm/chart/**`, `platform/huawei-evs-csi/chart/**`, `scripts/secret-guard-baselines/*`
- **SEC-244**: `products/agenity/chart/**`, `scripts/secret-guard-baselines/*`
- **SEC-248**: `scripts/secret-guard-baselines/secret-object-register.yaml`

</details>

### Group SEC-340: EPIC: upstream chart pods move to injection

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SEC-344**<br>rework, 3 days | **Upstream pods to injection: Grafana**<br>As SEC-341 for Grafana. | SEC-335, FND-011 | stated in its ticket |
| **SEC-341**<br>rework, 3 days | **Upstream pods to injection: Keycloak**<br>Class U entries with a working route become mounted; entries deleted. | SEC-221, SEC-335, FND-011 | stated in its ticket |
| **SEC-342**<br>rework, 3 days | **Upstream pods to injection: Gitea**<br>As SEC-341 for Gitea. | SEC-233, SEC-335, FND-011 | stated in its ticket |
| **SEC-343**<br>rework, 3 days | **Upstream pods to injection: Harbor**<br>As SEC-341 for Harbor. | SEC-234, SEC-335, FND-011 | stated in its ticket |
| **SEC-345**<br>rework, 3 days | **Upstream pods to injection: PowerDNS and its administration interface**<br>As SEC-341 for the two PowerDNS charts. | SEC-235, SEC-236, SEC-335, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SEC-344**: `platform/grafana/chart/**`, `scripts/secret-guard-baselines/secret-object-register.yaml`
- **SEC-341**: `platform/keycloak/chart/**`, `scripts/secret-guard-baselines/secret-object-register.yaml`
- **SEC-342**: `platform/gitea/chart/**`, `scripts/secret-guard-baselines/secret-object-register.yaml`
- **SEC-343**: `platform/harbor/chart/**`, `scripts/secret-guard-baselines/secret-object-register.yaml`
- **SEC-345**: `platform/powerdns/chart/**`, `platform/powerdns-admin/chart/**`, `scripts/secret-guard-baselines/secret-object-register.yaml`

</details>

## NET Network, zones and cluster classes

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-125**<br>rework, 3 days | **Mesh status read, and a mode switch, in the API**<br>observeClusterMesh and CATALYST_CLUSTERMESH_MODE (establish default, observe) | nothing else | stated in its ticket |
| **NET-100**<br>new, 3 days | **Network registry output and validator**<br>Output network_registry in both modules, computed pod supernet (max six regions), validator with named rules | CLD-080 | stated in its ticket |
| **NET-110**<br>rework, 5 days | **Cilium base values to target**<br>No literal API host or routing range in the shared chart; routing range from the module; Hubble metrics; stale mesh files deleted | NET-020, NET-100, FND-011, NET-031 | stated in its ticket |
| **NET-190**<br>fix, 3 days | **Route admission by owner**<br>Shared Gateway listeners admit a named list of kit namespaces; guard G4 extended | NET-060, NET-090 | stated in its ticket |
| **NET-170**<br>new, 5 days | **Organization fence and isolation proof**<br>Two host-side deny objects per Organization (org-fence, org-fence-api), API egress limited to vcluster pods, journey ISO-1 to ISO-9 | NET-080, NET-095, FND-011 | Makes NET-G07 required. |
| **NET-081**<br>delete, 3 days | **One producer of the Organization kubeconfig**<br>mirrorVClusterKubeconfig, its repair loop and its RBAC deleted; reflector path is the only producer | NET-170 | stated in its ticket |
| **NET-120**<br>test, 5 days | **Hermetic two-cluster mesh test**<br>Mesh test on the shared harness with three cases: shared authority, per-cluster intermediates under one root, authMode cluster; partition and resume | CLD-087, NET-110 | stated in its ticket |
| **NET-191**<br>fix, 3 days | **Organization listeners admit only their Organization**<br>Four code sites select the Organization's namespace by name | NET-170, NET-190 | Makes NET-G11 required. |
| **NET-055**<br>document, 3 days | **Canonical documents state the zone, firewall and default-deny design of record 0019**<br>Canonical documents state the zone, firewall and default-deny design of record 0019. | FND-104, NET-050 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **NET-125**: `products/catalyst/bootstrap/api/internal/handler/clustermesh.go`, `products/catalyst/bootstrap/api/internal/handler/clustermesh_test.go`
- **NET-100**: `infra/providers/huawei/main.tf`, `infra/providers/huawei/outputs.tf`, `infra/providers/huawei/variables.tf`, `infra/providers/huawei/tests/multi_region.tftest.hcl`, `infra/providers/hetzner/main.tf`, `infra/providers/hetzner/outputs.tf`, `infra/providers/PROVIDER-INTERFACE.md`, `scripts/check-network-registry.py`, `scripts/tests/check-network-registry/`
- **NET-110**: `platform/cilium/chart/values.yaml`, `platform/cilium/chart/values-clustermesh.yaml`, `platform/cilium/chart/templates/clustermesh-config.yaml`, `platform/cilium/chart/Chart.yaml`, `platform/cilium/chart/tests/`, `clusters/_template/bootstrap-kit/01-cilium.yaml`, `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/huawei/main.tf`, `infra/providers/hetzner/main.tf`
- **NET-190**: `clusters/_template/bootstrap-kit/01-cilium.yaml`, `scripts/check-network-guards.py`, `scripts/network-guards-allowlist.yaml`
- **NET-170**: `core/controllers/organization/internal/gitops/manifests.go`, `core/controllers/organization/cmd/main.go`, `products/catalyst/chart/templates/controllers/organization-controller-deployment.yaml`
- **NET-081**: `core/services/provisioning/handlers/handlers.go`, `core/services/provisioning/handlers/consumer.go`, `core/services/provisioning/handlers/kubeconfig_reconciler.go`, `core/services/provisioning/gitops/gitops.go`
- **NET-120**: `tests/hermetic/mesh/**`, `.github/workflows/test-mesh-hermetic.yaml`
- **NET-191**: `core/controllers/organization/internal/controller/tenant_console_tls.go`, `products/catalyst/bootstrap/api/internal/handler/org_console_tls.go`
- **NET-055**: `docs/ARCHITECTURE.md`, `docs/DOD.md`, `docs/GLOSSARY.md`, `docs/SECURITY.md`

</details>

### Group NET-130: Epic: declarative ClusterMesh

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-133**<br>fix, 3 days | **A local-affinity decision on every global Service**<br>Affinity local or the comment '# single-region backend' on each global Service by a fixed rule | NET-090 | stated in its ticket |
| **NET-134**<br>new, 3 days | **Mesh role on the PKI engine and the issuer sovereign-mesh-ca**<br>Mesh role on the PKI engine and the issuer sovereign-mesh-ca. | FND-032, NET-120, SEC-250, FND-011 | Makes NET-G16 required. |
| **NET-131**<br>new, 5 days | **Mesh layer from Git with the shared authority**<br>ConfigMap cilium-mesh-values via valuesFrom, no inline clustermesh key, three substitution keys for multi-region only, API set to observe | NET-010, NET-110, NET-120, NET-125, NET-134, FND-011 | Makes NET-G09 required. |
| **NET-132**<br>fix, 1 day | **Mesh authentication mode cluster**<br>CILIUM_MESH_AUTH_MODE set to cluster | NET-131 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **NET-134**: `platform/openbao/chart/values.yaml`, `docs/ledger/gates.yaml`
- **NET-131**: `clusters/_template/sovereign-tls/cilium-mesh-values.yaml`, `clusters/_template/sovereign-tls/kustomization.yaml`, `clusters/_template/bootstrap-kit/01-cilium.yaml`, `platform/cilium/chart/values.yaml`, `platform/cilium/chart/templates/clustermesh-proxy-daemonset.yaml`, `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/huawei/main.tf`, `products/catalyst/chart/values.yaml`, `products/catalyst/chart/templates/api-deployment.yaml`, `products/catalyst/chart/templates/api-deployment-kustomize.yaml`
- **NET-132**: `infra/providers/_shared/cloudinit-control-plane.tftpl`

</details>

### Group NET-160: Epic: default deny on every platform namespace

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-150**<br>new, 5 days | **Default-deny scaffolding and dial checks**<br>baseline-default-deny driven by enforcedNamespaces (empty), allow-file helper, legacy templates deleted, suite step POL with a dial table | NET-050, NET-095, FND-011, NET-030 | stated in its ticket |
| **NET-161**<br>test, 3 days | **Audit run and flow report**<br>24-hour audit-mode run on the gate environment and a summarised flow report per namespace | NET-131, NET-150 | stated in its ticket |
| **NET-162**<br>new, 5 days | **Allow rules and enforcement, ring 1 (observability and scanners)**<br>Eleven hand-written allow files (ring 1 without syft-grype), namespaces enforced, dial rows, plane-isolation entries removed | FND-193, NET-161, FND-011, NET-030 | stated in its ticket |
| **NET-163**<br>new, 5 days | **Allow rules and enforcement, ring 2 (platform services)**<br>Twelve allow files enforced with dial rows | NET-162, FND-011, NET-030 | stated in its ticket |
| **NET-164**<br>new, 5 days | **Allow rules and enforcement, ring 3 (identity, data, edge helpers)**<br>Eleven allow files enforced with dial rows; internet egress as DNS-name rules or cloud profile ranges | CLD-080, NET-163, SOV-110, FND-011, NET-030 | stated in its ticket |
| **NET-165**<br>new, 5 days | **Allow rules and enforcement, ring 4 (kernel)**<br>Eleven allow files enforced, kube-system last | NET-164, FND-011, NET-030 | stated in its ticket |
| **NET-166**<br>delete, 3 days | **Enforce everywhere and delete the old policy charts**<br>plane-isolation chart and slot, placeholder chart and replaced templates deleted; all dial rows mandatory | NET-165, FND-011, NET-030 | Makes NET-G06 required. |

<details><summary>Files each task of this group changes</summary>

- **NET-150**: `platform/network-policies/chart/templates/baseline-default-deny.yaml`, `platform/network-policies/chart/templates/allow/`, `platform/network-policies/chart/templates/default-deny.yaml`, `platform/network-policies/chart/templates/allow-system-namespaces.yaml`, `platform/network-policies/chart/templates/allow-dns.yaml`, `platform/network-policies/chart/values.yaml`, `tests/acceptance/sovereign/policy-dials.yaml`
- **NET-161**: `platform/network-policies/audit/flows.json`, `platform/network-policies/audit/README.md`, `scripts/summarise-audit-flows.py`
- **NET-162**: `platform/network-policies/chart/values.yaml`, `tests/acceptance/sovereign/policy-dials.yaml`, `platform/plane-isolation/chart/values.yaml`
- **NET-163**: `platform/network-policies/chart/values.yaml`, `tests/acceptance/sovereign/policy-dials.yaml`, `platform/plane-isolation/chart/values.yaml`
- **NET-164**: `platform/network-policies/chart/values.yaml`, `tests/acceptance/sovereign/policy-dials.yaml`, `platform/plane-isolation/chart/values.yaml`
- **NET-165**: `platform/network-policies/chart/values.yaml`, `tests/acceptance/sovereign/policy-dials.yaml`
- **NET-166**: `platform/plane-isolation/**`, `clusters/_template/bootstrap-kit/26b-bp-plane-isolation.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `products/catalyst/chart/templates/network-policies/**`, `platform/cilium-policies/**`, `tests/e2e/bootstrap-kit/main_test.go`, `scripts/expected-bootstrap-deps.yaml`, `tests/acceptance/sovereign/policy-dials.yaml`

</details>

## CTL Control plane

### Group CTL-200: Epic: one Organization pipeline

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CTL-201**<br>document, 3 days | **Parity file for the Organization pipelines**<br>One row per pipeline step, legacy overlay resource and provisioning-service cluster write, each mapped to the controller or marked missing. A first table names, for each entry point (console, storefront, internal route), the pipeline that serves it today, with file and function. | nothing else | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CTL-201**: `docs/ledger/org-pipeline-parity.md`, `scripts/check-org-pipeline-parity.py`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CTL-110**<br>new, 5 days | **Commit library gitwrite**<br>Package gitwrite exactly as contract 4.1 with fake, and gitea.Client.ChangeFiles | CTL-044, CTL-050 | stated in its ticket |
| **CTL-112**<br>new, 3 days | **Region scopes of the commit library**<br>Region scopes of the commit library. | CTL-044, CTL-110 | stated in its ticket |
| **CTL-620**<br>rework, 5 days | **Flux upgrade to the 2.10 line**<br>Flux 2.10 on fresh install and in place via 2.7 and flux migrate; Receiver resourceFilter; action pinned; watch label on the two settings ConfigMaps and on the mesh values ConfigMap of NET-131 when it exists. | CTL-040, FND-141, FND-011, NET-031 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CTL-110**: `core/controllers/pkg/gitwrite/**`, `core/controllers/pkg/gitea/client.go`, `core/controllers/pkg/gitea/client_test.go`
- **CTL-112**: `core/controllers/pkg/gitwrite/**`
- **CTL-620**: `infra/providers/_shared/cloudinit-control-plane.tftpl`, `platform/flux/chart/Chart.yaml`, `platform/flux/chart/values.yaml`, `platform/flux/chart/tests/version-pin-replay.sh`, `products/catalyst/bootstrap/api/internal/helmwatch/manage.go`, `.github/workflows/test-bootstrap-kit.yaml`, `.github/workflows/preflight-bootstrap-kit.yaml`, `clusters/_template/sovereign-region/base/receiver.yaml`, `docs/RUNBOOKS.md`

</details>

## BSS Commerce and billing

### Group BSS-140: Epic: secrets of this area

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **BSS-141**<br>fix, 3 days | **The cost-source credential is never a Kubernetes Secret**<br>Secret read and credentialRef removed from adapter, definition, sample and types | FND-051, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **BSS-141**: `products/chargeback/internal/adapter/openova/orgsync.go`, `products/catalyst/chart/crds/organization.yaml`, `products/catalyst/chart/crds/tests/organization-sample-valid.yaml`, `core/controllers/organization/internal/orgapi/types.go`, `scripts/baselines/bss.json`

</details>

## AIP Agent plane

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **AIP-120**<br>new, 5 days | **Agent tokens and the signer key set route**<br>Issue route on the existing TokenReview mechanism, key set route with key identifier, Organization confinement and deny-by-default mutation middleware for agent tokens | CTL-025, CTL-035, CTL-045, FND-032, FND-151, FND-011 | Makes AIP-G07 required. |
| **AIP-150**<br>fix, 3 days | **The tool server accepts agent tokens**<br>typ agent with audience platform-tools accepted and capped at developer; answers fitted and made inert; HTTP fallback bearer removed | AIP-120, FND-011 | stated in its ticket |
| **AIP-250**<br>lab test, 5 days | **Spike: agent workspace on the Organization's own sign-in with a short-lived tool identity**<br>Spike: agent workspace on the Organization's own sign-in with a short-lived tool identity. | AIP-120 | stated in its ticket |
| **AIP-155**<br>fix, 5 days | **Agent workspace: the Organization's own sign-in by mount and a short-lived tool identity, as the spike record says**<br>The workspace keeps its direct model path and its tool path. The credential seeded from the Sovereign is replaced by the Organization's own sign-in; the one-year bearer is replaced by a tool identity of at most one hour, renewed without a restart. No ExternalSecret and no Secret object remain in the chart. It writes no lifecycle verdict and no 'loss' statement, and does not point the workspace at the gateway. | AIP-150, AIP-250, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **AIP-120**: `products/catalyst/bootstrap/api/internal/agenttoken/**`, `products/catalyst/bootstrap/api/internal/sacaller/**`, `products/catalyst/bootstrap/api/internal/handler/internal_auth.go`, `products/catalyst/bootstrap/api/internal/handler/org_scope.go`, `products/catalyst/bootstrap/api/internal/handler/org_scope_test.go`, `products/catalyst/bootstrap/api/internal/auth/session.go`, `products/catalyst/bootstrap/api/internal/auth/session_test.go`, `products/catalyst/bootstrap/api/internal/handoverjwt/signer.go`, `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/bootstrap/api/cmd/api/routes*.go`, `products/catalyst/bootstrap/api/cmd/api/testdata/routes.golden`, `products/catalyst/chart/values.yaml` and 5 more
- **AIP-150**: `products/openova-mcp/internal/identity/**`, `products/openova-mcp/internal/fit/**`, `products/openova-mcp/internal/tools/**`, `products/openova-mcp/cmd/openova-mcp/http.go`, `products/openova-mcp/cmd/openova-mcp/main.go`, `products/openova-mcp/chart/templates/deployment.yaml`, `products/openova-mcp/chart/values.yaml`, `products/openova-mcp/README.md`
- **AIP-250**: `docs/ledger/plan/spikes/AIP-250.yaml`
- **AIP-155**: `products/agenity/chart/templates/externalsecret-mcp-bearer.yaml`, `products/agenity/chart/templates/externalsecret-anthropic.yaml`, `products/agenity/chart/templates/statefulset.yaml`, `products/agenity/chart/templates/cilium-egress-networkpolicy.yaml`, `products/agenity/chart/values.yaml`, `products/agenity/chart/tests/**`, `products/agenity/blueprint.yaml`, `products/agenity/README.md`, `scripts/agent-plane-baseline.yaml`

</details>

## INF Infrastructure plane

### Group INF-100: Epic: machine operations on the platform's own nodes (INF-110, 112, 120, 130, 132, 135, 140, 160, 145, 170)

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-110**<br>new, 5 days | **Kinds Machine and MachineSource; inventory controller with the node source**<br>Two definitions with the INF-110 fields of 4.5.1, the node source, pure rule functions with test tables, the controller binary | CTL-025, FND-052, FND-011 | stated in its ticket |
| **INF-112**<br>new, 3 days | **Chart templates and image for the inventory controller**<br>Inventory Deployment, RBAC without Secret verbs, the nodes source, the binary in the API image | FND-062, INF-110, FND-011 | stated in its ticket |
| **INF-120**<br>new, 5 days | **Runner image and node-mode tasks**<br>Pinned runner image (Ansible, Trivy, OpenSCAP, ComplianceAsCode 0.1.82), result document type, node-mode inventory and hardening tasks, evidence upload | FND-062, INF-010 | stated in its ticket |
| **INF-130**<br>new, 5 days | **Kinds ScanPolicy, HardeningBaseline, MachineRun; operations controller**<br>Three definitions with the INF-130 fields, timer-based scheduler, run reconciler creating node-mode Jobs, hardening report writer | INF-110, INF-120, FND-011 | stated in its ticket |
| **INF-132**<br>new, 3 days | **Chart templates for the operations controller; check INF-G02**<br>Operations Deployment and RBAC, default node ScanPolicy and HardeningBaseline, check INF-G02 as expected-fail | INF-112, INF-130, FND-011 | stated in its ticket |
| **INF-135**<br>guard, 1 day | **Lock stage machine-ops in audit**<br>One more binding of the intent lock for the Git-born kinds of machines.openova.io, in audit | CTL-150, INF-130, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **INF-110**: `platform/machine-operations/chart/Chart.yaml`, `platform/machine-operations/chart/values.yaml`, `platform/machine-operations/chart/crds/machines.yaml`, `platform/machine-operations/chart/crds/machinesources.yaml`, `platform/machine-operations/blueprint.yaml`, `platform/machine-operations/README.md`, `products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1/**`, `products/catalyst/bootstrap/api/internal/machines/source/**`, `products/catalyst/bootstrap/api/internal/machines/inventory/**`, `products/catalyst/bootstrap/api/cmd/machine-controller/main.go`, `products/catalyst/bootstrap/api/go.mod`, `products/catalyst/bootstrap/api/go.sum`
- **INF-112**: `platform/machine-operations/chart/values.yaml`, `platform/machine-operations/chart/templates/_helpers.tpl`, `platform/machine-operations/chart/templates/deployment-inventory.yaml`, `platform/machine-operations/chart/templates/rbac-inventory.yaml`, `platform/machine-operations/chart/templates/sources.yaml`, `platform/machine-operations/chart/tests/render.sh`, `products/catalyst/bootstrap/api/Containerfile`
- **INF-120**: `products/machine-runner/Containerfile`, `products/machine-runner/tools.lock.yaml`, `products/machine-runner/LICENSES.md`, `products/machine-runner/README.md`, `products/catalyst/bootstrap/api/cmd/machine-runner/main.go`, `products/catalyst/bootstrap/api/internal/machines/runner/**`, `products/catalyst/bootstrap/api/internal/machines/runresult/**`, `.github/workflows/build-machine-runner.yaml`
- **INF-130**: `platform/machine-operations/chart/crds/scanpolicies.yaml`, `platform/machine-operations/chart/crds/hardeningbaselines.yaml`, `platform/machine-operations/chart/crds/machineruns.yaml`, `platform/machine-operations/chart/crds/machines.yaml`, `products/catalyst/bootstrap/api/internal/machines/apis/v1alpha1/**`, `products/catalyst/bootstrap/api/internal/machines/operations/**`, `products/catalyst/bootstrap/api/cmd/machine-controller/main.go`
- **INF-132**: `platform/machine-operations/chart/values.yaml`, `platform/machine-operations/chart/templates/deployment-operations.yaml`, `platform/machine-operations/chart/templates/rbac-operations.yaml`, `platform/machine-operations/chart/templates/defaults.yaml`, `platform/machine-operations/chart/tests/render.sh`, `tests/acceptance/sovereign/internal/suite/check_inf_g02.go`, `docs/ledger/gates.yaml`
- **INF-135**: `products/catalyst/chart/templates/intent-lock/bindings.yaml`, `products/catalyst/chart/values.yaml`, `products/catalyst/chart/tests/intent-lock.sh`, `scripts/intent-lock-report.py`

</details>

