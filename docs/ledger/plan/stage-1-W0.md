# Stage 1 (W0): Secure and measure

Rails, a baseline measurement, deletions, security fixes, truth in documents. No new architecture.

202 tasks. One task is one pull request. Rows are in the order the tasks can run. "Waits for" lists the tasks that must be merged first; every task also waits for the first two tasks of the plan, `FND-021` and `FND-022`.

[Back to the overview](README.md)

## Areas in this stage

- [FND: Foundation gate and execution rules](#fnd-foundation-gate-and-execution-rules) — 53 tasks
- [CLD: Cloud abstraction](#cld-cloud-abstraction) — 14 tasks
- [SOV: Sovereign lifecycle](#sov-sovereign-lifecycle) — 10 tasks
- [SEC: Secrets and root of trust](#sec-secrets-and-root-of-trust) — 21 tasks
- [NET: Network, zones and cluster classes](#net-network-zones-and-cluster-classes) — 24 tasks
- [SVC: Service model](#svc-service-model) — 17 tasks
- [CTL: Control plane](#ctl-control-plane) — 13 tasks
- [CMP: Compliance and posture](#cmp-compliance-and-posture) — 11 tasks
- [BSS: Commerce and billing](#bss-commerce-and-billing) — 10 tasks
- [AIP: Agent plane](#aip-agent-plane) — 7 tasks
- [INF: Infrastructure plane](#inf-infrastructure-plane) — 7 tasks
- [MIG: Migration from VMware](#mig-migration-from-vmware) — 6 tasks
- [ACC: Access plane](#acc-access-plane) — 6 tasks
- [PRD: Later products](#prd-later-products) — 3 tasks

## FND Foundation gate and execution rules

### Group FND-020: Plan as data and the executor protocol

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-021**<br>guard, 5 days<br>ticket ready<br>[ticket](tickets/FND-021.md) | **Plan file and plan tool**<br>A tool reads the plan file and says which package is ready and which packages may run together, and a check keeps the plan file valid. | nothing else | 5 commands that CI runs on the pull request |
| **FND-022**<br>guard, 5 days<br>ticket ready<br>[ticket](tickets/FND-022.md) | **Scope fence, dependency gate and CI-run acceptance**<br>CI fails a pull request that leaves the paths of its package or whose dependencies are not merged and green, and CI runs the precondition and the acceptance commands of the package. | FND-021 | 5 commands that CI runs on the pull request |
| **FND-023**<br>document, 3 days | **Ticket template, linter and the executor rules in PROTOCOL.md**<br>The ticket template and its linter exist, and PROTOCOL.md carries the executor rules. | nothing else | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **FND-021**: `scripts/plan.py`, `.github/workflows/plan-validate.yaml`
- **FND-022**: `scripts/plan.py`, `.github/workflows/work-package.yaml`
- **FND-023**: `.github/ISSUE_TEMPLATE/dispatch.md`, `scripts/ticket-lint.py`, `docs/PROTOCOL.md`, `scripts/plan.py`

</details>

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-001**<br>lab test, 3 days | **Spike: the merge queue under release-writer pushes**<br>A result record that states how the merge queue behaves when the release writer pushes, and which identity FND-017 uses. | nothing else | stated in its ticket |
| **FND-002**<br>lab test, 1 day | **Spike: the gate runner reaches every region**<br>A result record that says whether a self-hosted runner reaches the API servers of every region, and the runner label FND-035 names. | nothing else | stated in its ticket |
| **FND-040**<br>guard, 3 days | **Stability register and the frozen-path check**<br>One register names every surface with its class, and CI fails a change to a closed or frozen path. | nothing else | stated in its ticket |
| **FND-110**<br>guard, 1 day | **Guard: no Kyverno engine at 1.20 while a legacy policy exists**<br>CI fails a Kyverno engine upgrade to 1.20 while a legacy policy kind is still rendered. | FND-091 | stated in its ticket |
| **FND-900**<br>fix, 5 days | **Topology soak fixes (standing package)**<br>Fixes of the sibling topology work enter the closed topology surface under the same fence as every other change. | FND-040, FND-011 | stated in its ticket |
| **FND-910**<br>fix, 3 days | **Dependency updates (standing package)**<br>A pull request of the dependency bot is checked by the scope fence like any other. | FND-052, FND-056 | stated in its ticket |
| **FND-071**<br>guard, 3 days | **Flight checklist: pre-flight gate before every fresh installation, leftover sweep after every wipe**<br>Every fresh installation runs the repository's pre-flight gate before the fire and a sweep of the cloud account after the wipe. The sweep lists every kind of resource the platform creates and must find none before the next fire. The measured quota numbers go into the gate record. | FND-035 | stated in its ticket |
| **FND-073**<br>test, 3 days | **The gate runs against the standing environment after each candidate, with no wipe**<br>After a candidate is cut, the standing environment is pointed at it and updated in place. When it has converged, the gate runs against it twice and both records are committed. Nothing is wiped and nothing is installed fresh. | FND-014, FND-015, FND-035 | stated in its ticket |
| **FND-070**<br>test, 3 days | **Baseline: one fresh provision of `main` as it stands**<br>One gate record of a fresh two-region provision of main as it stands, and a file with the measured numbers and the defects. | FND-014, FND-015, FND-033, FND-034, FND-035, FND-071, FND-036, CMP-010 | stated in its ticket |
| **FND-080**<br>guard, 1 day | **Set the manifest from the baseline**<br>Every FND check that passed the baseline is required, and every failed one has a fix package that will flip it. | FND-070 | Makes FND-G01 to FND-G12 (12 checks) required. |
| **FND-120**<br>test, 3 days | **W0 exit: two consecutive records on one candidate; freeze the topology surface**<br>Two consecutive gate records on one candidate pass every W0 check, and the topology surface is frozen. | FND-080, FND-054, FND-091, FND-092, FND-093, FND-094, FND-095, FND-096, FND-097, FND-098, FND-099, FND-171, FND-173, SOV-035 | Makes FND-G13 required. |

<details><summary>Files each task of this group changes</summary>

- **FND-001**: `docs/ledger/plan/spikes/FND-001-merge-queue.md`
- **FND-002**: `docs/ledger/plan/spikes/FND-002-gate-runner.md`
- **FND-040**: `docs/ledger/stability.yaml`, `scripts/check-frozen-paths.py`, `.github/workflows/work-package.yaml`, `scripts/plan.py`
- **FND-110**: `scripts/check-kyverno-legacy-policies.sh`, `.github/workflows/check-repo-hygiene.yaml`
- **FND-900**: `core/controllers/pkg/regionfile/**`, `platform/topology-lib/**`, `products/continuum/chart/**`, `core/controllers/continuum/**`, `scripts/check-continuum-no-workload-mutation.sh`, `clusters/_template/sovereign-region/**`, `core/controllers/organization/internal/controller/per_org_flux.go`, `core/controllers/pkg/regions/**`, `scripts/check-region-vocabulary.sh`, `tests/acceptance/sovereign/internal/suite/journeys.go`, `tests/acceptance/sovereign/internal/suite/match.go`, `core/controllers/application/**`
- **FND-910**: `**/go.mod`, `**/go.sum`, `**/package.json`, `**/package-lock.json`
- **FND-071**: `scripts/flight-sweep.sh`, `scripts/sovereign-lifecycle.sh`, `docs/RUNBOOKS.md`
- **FND-073**: `.github/workflows/foundation-gate-standing.yaml`
- **FND-070**: `docs/ledger/plan/spikes/FND-070-baseline.md`
- **FND-080**: `docs/ledger/gates.yaml`
- **FND-120**: `docs/ledger/stability.yaml`, `docs/ledger/gates.yaml`

</details>

### Group FND-010: Release train rails

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-014**<br>fix, 1 day<br>ticket ready<br>[ticket](tickets/FND-014.md) | **A deployment may name the Git ref it follows**<br>A deployment request may name the Git ref the new Sovereign follows, which is main or a release-candidate ref. | nothing else | 9 commands that CI runs on the pull request |
| **FND-011**<br>guard, 3 days | **CI bumps chart versions; pull requests may not**<br>CI is the only writer of chart versions, and a pull request that edits a version or pin value fails. | FND-001 | stated in its ticket |
| **FND-012**<br>delete, 1 day | **Stop the tracker commits**<br>The 15-minute tracker commit stops and the file no longer prints a false total. | FND-011 | stated in its ticket |
| **FND-013**<br>guard, 3 days | **Merge queue: aggregate check, queue triggers, rulesets and code owners as files**<br>The merge queue, its required checks and the code owners exist as files, and every unfiltered pull-request workflow also runs on a merge group. | FND-011, FND-098 | stated in its ticket |
| **FND-015**<br>guard, 3 days | **Candidate tool and cut workflow**<br>One command cuts an immutable candidate ref from a coherent main commit and refuses more than one kernel upgrade per candidate. | FND-013, FND-014 | stated in its ticket |
| **FND-017**<br>guard, 3 days | **Every bot writer pushes under the release-writer identity; the queue is switched on**<br>Every bot push to main uses one release-writer identity, and the merge-queue ruleset can be applied without stopping a deploy bump. | FND-013, FND-011, FND-001, FND-091, FND-031 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **FND-014**: `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`, `products/catalyst/bootstrap/api/internal/provisioner/gitops_branch_test.go`, `scripts/sovereign-lifecycle.sh`
- **FND-011**: `scripts/check-release-owned-lines.py`, `.github/workflows/check-release-owned-lines.yaml`, `.github/workflows/chart-auto-bump.yaml`, `.github/workflows/check-chart-version-bumped.yaml`, `.github/workflows/check-chart-version-claim.yaml`, `docs/RUNBOOKS.md`
- **FND-012**: `docs/ledger/TRACKER.md`
- **FND-013**: `.github/workflows/ci-gate.yaml`, `.github/rulesets/main.json`, `.github/rulesets/rc.json`, `.github/CODEOWNERS`, `scripts/check-ruleset-drift.py`, `.github/workflows/check-ruleset-drift.yaml`, `.github/workflows/banned-words-validate.yaml`, `.github/workflows/check-no-conflict-markers.yaml`, `.github/workflows/naming-guard.yaml`, `.github/workflows/pr-body-validate.yaml`, `.github/workflows/redact-guard.yml`, `.github/workflows/rt11-cutover-hold.yaml`
- **FND-015**: `scripts/release-candidate.sh`, `scripts/test-release-candidate.sh`, `.github/workflows/cut-release-candidate.yaml`
- **FND-017**: `scripts/ci/use-release-writer.sh`, `scripts/check-bot-writers.py`, `.github/actions/deploy-bump/action.yaml`, `.github/workflows/blueprint-release.yaml`, `.github/workflows/catalyst-build.yaml`, `.github/workflows/services-build.yaml`, `.github/workflows/check-sovereign-daytwo-bootstrap-refuse.yaml`, `.github/workflows/sso-uat-flip.yaml`, `.github/workflows/acceptance-sovereign.yaml`, `.github/workflows/foundation-gate.yaml`, `.github/workflows/chart-auto-bump.yaml`, `.github/workflows/check-ruleset-drift.yaml` and 1 more

</details>

### Group FND-030: Gate manifest, record and checks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-031**<br>guard, 5 days<br>ticket ready<br>[ticket](tickets/FND-031.md) | **Gate manifest and verdict tool; the mode and the override go**<br>The gate manifest moves forward only, one tool gives the verdict by the pass rule, the gate mode and the override are gone, and the contract index exists. | nothing else | 9 commands that CI runs on the pull request |
| **FND-032**<br>test, 5 days<br>ticket ready<br>[ticket](tickets/FND-032.md) | **Record version 2 and the check registry**<br>The suite writes version 2 records and runs registered checks selected from the manifest. | FND-031 | 11 commands that CI runs on the pull request |
| **FND-033**<br>test, 5 days | **Checks: candidate identity, zero touch, nodes, releases and pods**<br>Four checks exist in the suite and run as expected-fail: candidate identity, zero touch, nodes, releases and pods. | FND-032 | Adds the check FND-G01, FND-G02, FND-G03, FND-G04. |
| **FND-034**<br>test, 5 days | **Checks: NodePort, front doors, mesh, encryption, clean wipe; journeys as checks**<br>Five more checks run as expected-fail, and the mesh step and the five journeys are reported as checks. | FND-032, FND-033 | Adds the check FND-G05, FND-G06, FND-G08, FND-G09, FND-G10, FND-G11, FND-G12, FND-G13. |
| **FND-035**<br>test, 3 days | **The gate workflow**<br>A workflow runs the suite against a Sovereign, validates the record and commits it. | FND-032, FND-015, FND-002 | stated in its ticket |
| **FND-036**<br>test, 3 days | **Sign-in as a gate check**<br>The sign-in probe runs in the gate workflow and its result is a check of the record. | FND-035 | Adds the check FND-G07. |
| **FND-037**<br>delete, 3 days | **Retire the shell verifier**<br>The shell verifier is gone and the documents point at the gate checks. | FND-080, FND-091, NET-033, FND-120 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **FND-031**: `scripts/acceptance-gate.py`, `.github/workflows/catalyst-build.yaml`, `.github/workflows/gate-manifest.yaml`, `docs/contracts/README.md`, `tests/acceptance/sovereign/README.md`
- **FND-032**: `tests/acceptance/sovereign/internal/suite/registry.go`, `tests/acceptance/sovereign/internal/suite/registry_test.go`, `tests/acceptance/sovereign/internal/suite/result.go`, `tests/acceptance/sovereign/internal/suite/runner.go`, `tests/acceptance/sovereign/internal/suite/config.go`, `tests/acceptance/sovereign/cmd/sovereign-acceptance/main.go`, `tests/acceptance/sovereign/README.md`, `scripts/acceptance_record.py`, `scripts/acceptance-gate.py`
- **FND-033**: `tests/acceptance/sovereign/internal/suite/check_candidate*.go`, `tests/acceptance/sovereign/internal/suite/check_zero_touch*.go`, `tests/acceptance/sovereign/internal/suite/check_nodes*.go`, `tests/acceptance/sovereign/internal/suite/check_releases*.go`, `tests/acceptance/sovereign/internal/kube/kube.go`, `tests/acceptance/sovereign/internal/suite/result.go`, `scripts/acceptance-gate.py`, `docs/ledger/gates.yaml`
- **FND-034**: `tests/acceptance/sovereign/internal/suite/check_nodeport*.go`, `tests/acceptance/sovereign/internal/suite/check_front_doors*.go`, `tests/acceptance/sovereign/internal/suite/check_mesh*.go`, `tests/acceptance/sovereign/internal/suite/check_encryption*.go`, `tests/acceptance/sovereign/internal/suite/check_wipe*.go`, `tests/acceptance/sovereign/internal/suite/check_journeys*.go`, `tests/acceptance/sovereign/internal/kube/kube.go`, `tests/acceptance/sovereign/internal/suite/result.go`, `tests/acceptance/sovereign/internal/suite/config.go`, `tests/acceptance/sovereign/cmd/sovereign-acceptance/main.go`, `scripts/acceptance-gate.py`, `docs/ledger/gates.yaml`
- **FND-035**: `.github/workflows/foundation-gate.yaml`, `.github/workflows/acceptance-sovereign.yaml`, `scripts/acceptance-sovereign.sh`
- **FND-036**: `scripts/sso-zero-click-probe.mjs`, `.github/workflows/foundation-gate.yaml`, `.github/workflows/sso-probe.yaml`, `scripts/acceptance-gate.py`, `docs/ledger/gates.yaml`
- **FND-037**: `scripts/sovereign-dod-verify.sh`, `scripts/sovereign-3x-zero-touch-cycle.sh`, `docs/DOD.md`, `docs/RUNBOOKS.md`, `clusters/_template/bootstrap-kit/34-velero.yaml`, `clusters/_template/bootstrap-kit/34a-bp-velero-hcs.yaml`, `clusters/_template/bootstrap-kit/50-cluster-autoscaler.yaml`, `clusters/_template/bootstrap-kit/55-bp-hcloud-ccm.yaml`, `docs/ledger/deleted-paths.txt`

</details>

### Group FND-050: Test rails

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-051**<br>fix, 1 day<br>ticket ready<br>[ticket](tickets/FND-051.md) | **Billing-engine and tool-server tests run on pull requests**<br>The billing engine's and the tool server's tests run on every pull request that changes them. | nothing else | 3 commands that CI runs on the pull request |
| **FND-053**<br>guard, 3 days<br>ticket ready<br>[ticket](tickets/FND-053.md) | **Go lint ratchet**<br>A pull request fails on a new Go lint finding and never on an old one. | nothing else | 6 commands that CI runs on the pull request |
| **FND-055**<br>guard, 3 days | **Vulnerability and secret scans, new findings only**<br>A pull request fails on a new vulnerability or secret finding, with Trivy as the one image and file-system matcher. | nothing else | stated in its ticket |
| **FND-052**<br>fix, 1 day<br>ticket ready<br>[ticket](tickets/FND-052.md) | **Tests that run nowhere join the matrix**<br>Every Go module is tested on pull requests or is on a list that may only shrink. | FND-051 | 7 commands that CI runs on the pull request |
| **FND-054**<br>guard, 3 days | **Console lint ratchet; the switched-off suite is deleted**<br>Console lint warnings may only fall, and the switched-off console suite is gone. | FND-091 | stated in its ticket |
| **FND-056**<br>guard, 3 days | **Pin the two moving actions; ratchets for actions and base images**<br>No action floats on a branch, and the counts of unpinned actions and base images may only fall. | FND-013, FND-052, FND-053 | stated in its ticket |
| **FND-057**<br>guard, 1 day | **The privacy guard fails closed and runs on every branch**<br>The privacy guard fails when its pattern is missing and runs on every branch. | FND-013 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **FND-051**: `.github/workflows/build-chargeback.yaml`, `.github/workflows/build-openova-mcp.yaml`
- **FND-053**: `.golangci.yml`, `.github/workflows/go-lint.yaml`
- **FND-055**: `.github/workflows/security-scan.yaml`, `scripts/scan-baseline.py`, `docs/ledger/scan-baseline.json`
- **FND-052**: `.github/workflows/test-ungated-trees.yaml`, `.github/workflows/test-billing-integration.yaml`, `scripts/check-modules-tested.py`, `docs/ledger/untested-modules.txt`
- **FND-054**: `.github/workflows/console-lint.yaml`, `products/catalyst/bootstrap/ui/eslint-max-warnings.txt`, `.github/workflows/cosmetic-guards.yaml`, `products/catalyst/bootstrap/ui/e2e/cosmetic-guards.spec.ts`, `docs/ledger/deleted-paths.txt`
- **FND-056**: `.github/workflows/test-bootstrap-kit.yaml`, `.github/workflows/preflight-bootstrap-kit.yaml`, `scripts/check-pins.py`, `docs/ledger/pin-baseline.json`, `.github/workflows/check-pins.yaml`, `.github/dependabot.yml`
- **FND-057**: `.github/workflows/redact-guard.yml`

</details>

### Group FND-090: Delete train, this area's items

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-091**<br>delete, 3 days | **Remove committed binaries and root clutter; add the hygiene guard**<br>The repository holds no committed binary and a guard keeps it so. | nothing else | stated in its ticket |
| **FND-097**<br>delete, 3 days | **Remove the unshipped console, the one job that drives it and the tests that only target it**<br>The unshipped console is gone, and the 21 specifications of the sovereign-admin console still run on every pull request. | FND-091 | stated in its ticket |
| **FND-173**<br>delete, 1 day | **Remove the unimported type mirror**<br>The type mirror that no module imports is gone, and the project instructions point at the copy that is compiled. | FND-091 | stated in its ticket |
| **FND-092**<br>delete, 1 day | **Stop building the Sandbox images**<br>No workflow builds a Sandbox image, so no bot commit bumps one. | FND-091, FND-052, FND-106 | stated in its ticket |
| **FND-098**<br>delete, 1 day | **Remove two retired chart folders and one dead harness**<br>Two retired chart folders, a dead harness and its workflow are gone. | FND-091, FND-011 | stated in its ticket |
| **FND-099**<br>delete, 1 day | **Remove two custom resources that nothing reads**<br>The Runbook and SecretPolicy definitions, which nothing reads, are gone. | FND-091, FND-011 | stated in its ticket |
| **FND-093**<br>delete, 5 days | **Remove the Sandbox surface of the API**<br>The API serves no Sandbox route and holds no Sandbox handler. | FND-092 | stated in its ticket |
| **FND-094**<br>delete, 3 days | **Remove the Sandbox pages of the console**<br>The console has no Sandbox page or route, and the agent workspace is still reached through its Blueprint entry. | FND-092 | stated in its ticket |
| **FND-095**<br>delete, 3 days | **Remove the Sandbox orchestrator, grants and cutover entries**<br>Nothing creates, grants or pins a Sandbox object any more. | FND-093, FND-094, FND-011 | stated in its ticket |
| **FND-171**<br>delete, 3 days | **Remove the fabricated playback route and its preflight route**<br>The API no longer reports a switchover it never performs. | FND-091, FND-093 | stated in its ticket |
| **FND-096**<br>delete, 3 days | **Remove the Sandbox sources**<br>The Sandbox sources, chart and custom resource are gone, and a guard counts the remaining mentions down. | FND-095, SOV-035, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **FND-091**: `core/services/gateway/gateway`, `platform/openclaw/controller/controller`, `platform/openclaw/runtime/runtime`, `hw225-*.snapshot.md`, `graphify-out/**`, `products/catalyst/bootstrap/ui/.pr-evidence/**`, `scripts/check-repo-hygiene.py`, `docs/ledger/deleted-paths.txt`, `docs/ledger/sessions-baseline.txt`, `.github/workflows/check-repo-hygiene.yaml`, `.gitignore`
- **FND-097**: `products/catalyst/console/**`, `.github/workflows/playwright-e2e.yml`, `.github/workflows/playwright-smoke.yaml`, `tests/e2e/playwright/tests/g117-blueprint-admission.spec.ts`, `tests/e2e/playwright/tests/g117-launch-silent-sso.spec.ts`, `tests/e2e/playwright/tests/g117-multi-instance-create.spec.ts`, `core/controllers/blueprint/internal/validate/placement_vocabulary_drift_test.go`, `docs/ledger/deleted-paths.txt`, `CLAUDE.md`
- **FND-173**: `core/pkg/apis/**`, `CLAUDE.md`, `docs/ledger/deleted-paths.txt`
- **FND-092**: `.github/workflows/build-sandbox-controller.yaml`, `.github/workflows/build-sandbox-mcp-server.yaml`, `.github/workflows/build-sandbox-pty-server.yaml`, `docs/ledger/deleted-paths.txt`
- **FND-098**: `platform/cert-manager-dynadot-webhook/**`, `platform/stalwart-sovereign/**`, `tests/e2e/hetzner-provisioning/**`, `.github/workflows/test-hetzner-e2e.yaml`, `scripts/expected-unseeded-blueprints.yaml`, `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts`, `docs/ledger/deleted-paths.txt`
- **FND-099**: `products/catalyst/chart/crds/runbook.yaml`, `products/catalyst/chart/crds/secretpolicy.yaml`, `products/catalyst/chart/CRDS.md`, `docs/ledger/deleted-paths.txt`
- **FND-093**: `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/bootstrap/api/internal/handler/handler.go`, `products/catalyst/bootstrap/api/internal/handler/org_scope.go`, `products/catalyst/bootstrap/api/internal/handler/org_scope_test.go`, `products/catalyst/bootstrap/api/internal/handler/sandbox_*.go`, `products/catalyst/bootstrap/api/internal/handler/byos_claude_code.go`, `products/catalyst/bootstrap/api/internal/natspub/sandbox_publisher.go`, `products/catalyst/bootstrap/api/internal/natspub/sandbox_publisher_test.go`, `docs/ledger/deleted-paths.txt`
- **FND-094**: `products/catalyst/bootstrap/ui/src/pages/sovereign/sandbox/**`, `products/catalyst/bootstrap/ui/src/lib/sandbox.api.ts`, `products/catalyst/bootstrap/ui/e2e/sandbox.spec.ts`, `products/catalyst/bootstrap/ui/src/app/router.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/SovereignSidebar.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/SovereignSidebar.menu-mapping.test.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/SovereignSidebar.orgscope.test.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/sovereignNav.ts`, `products/catalyst/bootstrap/ui/src/app/layouts/SovereignConsoleLayout.tsx`, `.github/workflows/playwright-e2e.yml`, `docs/ledger/deleted-paths.txt`
- **FND-095**: `core/services/tenant/handlers/sandbox_consumer.go`, `core/services/tenant/handlers/sandbox_consumer_test.go`, `core/services/tenant/handlers/sandbox_consumer_integration_test.go`, `core/services/tenant/handlers/handlers.go`, `core/services/tenant/handlers/funnel_cart_install_test.go`, `core/services/tenant/handlers/launch_gate_test.go`, `core/services/tenant/main.go`, `core/services/tenant/store/store.go`, `platform/self-sovereign-cutover/chart/values.yaml`, `platform/self-sovereign-cutover/chart/tests/cutover-contract.sh`, `products/catalyst/chart/templates/clusterrole-cutover-driver.yaml`, `products/catalyst/chart/templates/org-services/serviceaccounts.yaml` and 1 more
- **FND-171**: `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/bootstrap/api/internal/handler/continuum_dr_extras.go`, `products/catalyst/bootstrap/api/internal/handler/continuum_dr_preflight_test.go`, `products/catalyst/bootstrap/api/internal/handler/continuum_dr_preflight02_6156_test.go`, `products/catalyst/bootstrap/api/internal/handler/continuum_dr_preflight02_callsite_6156_test.go`, `docs/ledger/deleted-paths.txt`
- **FND-096**: `products/catalyst/chart/crds/sandbox.yaml`, `core/controllers/sandbox/**`, `products/sandbox/**`, `platform/sandbox/**`, `tests/e2e/sandbox-mcp-contract.sh`, `scripts/expected-unseeded-blueprints.yaml`, `scripts/check-sandbox-word.py`, `docs/ledger/sandbox-word-baseline.txt`, `.github/workflows/check-repo-hygiene.yaml`, `docs/ledger/deleted-paths.txt`, `CLAUDE.md`, `products/catalyst/chart/templates/catalog-seed/blueprints.yaml` and 2 more

</details>

### Group FND-100: Documentation truth

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-101**<br>document, 3 days | **Truth reset of the status surfaces**<br>The status surfaces stop claiming a proof that does not exist, and the ledger is titled a regression list. | nothing else | stated in its ticket |
| **FND-102**<br>document, 5 days | **Inventory generators and the drift check**<br>Three inventories are generated from the tree, and one workflow fails a pull request whose committed output is stale. | nothing else | stated in its ticket |
| **FND-106**<br>decision, 3 days | **Decision records 0022, 0023 and 0025; index and header corrections**<br>Three missing decision records exist, and the index carries the allocation table. | nothing else | stated in its ticket |
| **FND-103**<br>guard, 3 days | **State markers and their ratchet**<br>Unmarked sections of the five canonical documents and citations of missing headings may only fall. | FND-031, FND-102 | stated in its ticket |
| **FND-105**<br>document, 3 days | **Security control matrix: format and generator**<br>The security table is generated from a control file, and a control prints as built only with a required gate check. | FND-102, FND-103 | stated in its ticket |
| **FND-104**<br>document, 3 days | **Truth patch of the canonical documents**<br>The canonical documents link to generated inventories, carry state markers, and no longer state the claims the audits found false. | FND-102, FND-103, FND-096, FND-097, FND-101, FND-105 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **FND-101**: `docs/ledger/UAT.md`, `docs/ledger/uat-retirements.csv`, `docs/ledger/UAT-FINAL.md`, `docs/ledger/UAT-CONFIDENCE.md`, `docs/ledger/PATH-TO-100.md`, `docs/ledger/PLAN-TO-100.md`, `docs/ledger/WBS-TO-100.md`, `docs/ledger/TRUST.md`, `docs/ledger/PROMOTION-SPEC-repo-rows.md`, `docs/STATUS.md`, `scripts/uat-tally.py`
- **FND-102**: `scripts/generators/slots.py`, `scripts/generators/components.py`, `scripts/generators/custom-resources.py`, `docs/generated/bootstrap-slots.md`, `docs/generated/components.md`, `docs/generated/custom-resources.md`, `.github/workflows/check-generated-docs.yaml`
- **FND-106**: `docs/adr/0022-foundation-gate-release-train-and-execution-protocol.md`, `docs/adr/0023-sandbox-withdrawn.md`, `docs/adr/0025-organization-vcluster-tier-gate-added-and-removed.md`, `docs/adr/README.md`, `docs/adr/0001-catalyst-control-plane-architecture.md`, `docs/adr/0002-post-handover-sovereignty-cutover.md`
- **FND-103**: `scripts/check-doc-state.py`, `docs/ledger/doc-state-baseline.txt`, `.github/workflows/check-generated-docs.yaml`
- **FND-105**: `docs/ledger/controls.yaml`, `scripts/generators/controls.py`, `docs/generated/security-controls.md`, `docs/SECURITY.md`
- **FND-104**: `docs/ARCHITECTURE.md`, `docs/STATUS.md`, `docs/SECURITY.md`, `docs/SRE.md`, `docs/DOD.md`, `README.md`, `core/README.md`, `docs/ledger/doc-state-baseline.txt`

</details>

### Group FND-060: Signing

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **FND-061**<br>guard, 3 days | **One signing action, used by the control-plane images first**<br>One shared action signs and attests an image with the release key, and the two control-plane images use it. | FND-051, FND-031 | stated in its ticket |
| **FND-062**<br>rework, 5 days | **Every image and chart build signs through the action**<br>Every image and chart build signs and attests through the shared action, and a guard fails a build that does not. | FND-061, FND-056, FND-092 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **FND-061**: `.github/actions/sign-and-attest/action.yaml`, `keys/release-cosign.pub`, `.github/workflows/catalyst-build.yaml`
- **FND-062**: `.github/workflows/admin-build.yaml`, `.github/workflows/agenity-build.yaml`, `.github/workflows/axon-build.yaml`, `.github/workflows/build-application-controller.yaml`, `.github/workflows/build-blueprint-controller.yaml`, `.github/workflows/build-bp-huawei-evs-csi.yaml`, `.github/workflows/build-bp-newapi-sandbox-bridge.yaml`, `.github/workflows/build-bp-wordpress-tenant-pg.yaml`, `.github/workflows/build-catalyst-migrator.yaml`, `.github/workflows/build-cert-manager-dynadot-webhook.yaml`, `.github/workflows/build-chargeback.yaml`, `.github/workflows/build-continuum-controller.yaml` and 21 more

</details>

## CLD Cloud abstraction

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-050**<br>lab test, 3 days | **Spike: Workspace mechanics on Crossplane 2 with bucket state, encryption, mirrors and the offline key**<br>Verdicts for E1 to E7, E13, E8, E15, E16, E17. E3 tests one form only: the OpenBao key provider with a token read from a file that is re-minted every 15 minutes. E13 has no passphrase fallback: on failure CLD-171 does not start. E8: does the provider accept an oci:// module address. E15: observe, then delete, leaves the cloud resources. E16: a patched Workspace applies at once. E17: seconds of one such apply. The four added rows run last. | nothing else | stated in its ticket |
| **CLD-063**<br>fix, 3 days | **Mirror the Crossplane packages**<br>Mirror workflow only: provider-opentofu v1.1.3 and v1.1.11 and the function package copied by digest into the mothership registry. The package line in the manifest is not changed. | nothing else | stated in its ticket |
| **CLD-070**<br>decision, 3 days | **Decision record and truthful contract documents**<br>Decision record and truthful contract documents. | nothing else | stated in its ticket |
| **CLD-071**<br>decision, 1 day | **Decision record 0024: the primary substrate moved on 2026-05-22; status of the earlier substrate**<br>Decision record 0024: the primary substrate moved on 2026-05-22; status of the earlier substrate. | nothing else | stated in its ticket |
| **CLD-010**<br>guard, 3 days | **Provider-leak scanner with a falling baseline**<br>Scanner to section 4.10 with check, update, report, self-test and a known-answer CI job. Flips CLD-G02, which now covers the leak scanner only. The allow-list keeps products/chargeback/internal/collector/<name>/. | FND-031 | Makes CLD-G02 required. |
| **CLD-040**<br>test, 3 days | **Run the shared render test in CI and pin a golden render**<br>Golden hash per fixture and a golden run executed in CI | NET-031, FND-031 | stated in its ticket |
| **CLD-030**<br>fix, 3 days | **Per-Organization databases use the Sovereign's storage class**<br>global.defaultStorageClass from SOVEREIGN_CNPG_STORAGE_CLASS; provider-to-class switch deleted | FND-032, FND-011 | Makes CLD-G09 required. |
| **CLD-061**<br>fix, 1 day | **Crossplane 1.18 to 1.19**<br>Core at highest 1.19 patch | CLD-023, CLD-050, FND-032, FND-011 | Makes CLD-G06 required. |
| **CLD-062**<br>fix, 1 day | **Crossplane 1.19 to 1.20**<br>Core at highest 1.20 patch | CLD-061, FND-011 | stated in its ticket |
| **CLD-064**<br>fix, 3 days | **Convert the adoption Composition to pipeline mode**<br>Function package installed with its upstream address as it is; Composition in Pipeline mode with unchanged resources. | CLD-062, CLD-063, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-063**: `.github/workflows/mirror-crossplane-packages.yaml`
- **CLD-070**: `docs/adr/README.md`, `infra/providers/PROVIDER-INTERFACE.md`, `infra/providers/hetzner/PROVIDER-INTERFACE.md`, `platform/opentofu/README.md`, `platform/crossplane/README.md`, `docs/adr/0016-cloud-abstraction-contract.md`
- **CLD-071**: `docs/adr/0024-primary-substrate.md`, `docs/adr/README.md`
- **CLD-010**: `scripts/check-provider-leaks.py`, `scripts/provider-leak-baseline.tsv`, `.github/workflows/check-provider-leaks.yaml`
- **CLD-040**: `infra/providers/_shared/tests/render.tf`, `infra/providers/_shared/tests/cloudinit_render.tftest.hcl`, `infra/providers/_shared/tests/golden.auto.tfvars.json`, `scripts/update-cloudinit-golden.sh`, `.github/workflows/catalyst-build.yaml`
- **CLD-030**: `clusters/_template/bootstrap-kit/13-bp-catalyst-platform.yaml`, `products/catalyst/chart/values.yaml`, `products/catalyst/chart/templates/org-services/provisioning.yaml`, `core/services/provisioning/main.go`, `core/services/provisioning/gitops/gitops.go`, `tests/acceptance/sovereign/internal/suite/check_*.go`
- **CLD-061**: `platform/crossplane/chart/Chart.yaml`, `platform/crossplane/chart/values.yaml`, `tests/acceptance/sovereign/internal/suite/check_*.go`
- **CLD-062**: `platform/crossplane/chart/Chart.yaml`, `platform/crossplane/chart/values.yaml`
- **CLD-064**: `platform/crossplane-claims/chart/templates/compositions/cloudadoption.yaml`, `clusters/_template/infrastructure/providers/base/function-patch-and-transform.yaml`, `clusters/_template/infrastructure/providers/base/kustomization.yaml`

</details>

### Group CLD-085: Epic: the local provider pack

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-055**<br>lab test, 3 days | **Spike: machine kind and reduced kit for the local pack**<br>Verdicts for L1 to L7 and the machine kind. L6: two clusters of the chosen kind plus the console's closure converge on the runner inside the time limit; the record lists that closure. L7: a machine answers on TCP 22 from a pod and restarts after an in-guest reboot. | nothing else | stated in its ticket |

### Group CLD-020: Epic: delete the dead infrastructure write facade and Hetzner-only Crossplane set

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CLD-022**<br>delete, 3 days | **Delete the infrastructure mutation dialogs from the console**<br>Only deployment delete and wipe dialogs remain; mutation library gone | FND-091, FND-094 | stated in its ticket |
| **CLD-021**<br>delete, 3 days | **Delete the 24 infrastructure mutation routes and the claim writer**<br>Routes at main.go:1654-1677, their handlers, xrc.go and the mutation bridge removed | CLD-022, CTL-030 | stated in its ticket |
| **CLD-023**<br>delete, 3 days | **Delete the Hetzner-only Compositions, their definitions and orphan manifests**<br>One definition and one Composition (cloud adoption) remain; template line 1245 fixed path | CLD-021, CLD-040, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CLD-022**: `products/catalyst/bootstrap/ui/src/components/CrudModals/**`, `products/catalyst/bootstrap/ui/src/lib/infrastructure-crud.ts`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-compute/*.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-storage/*.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-network/LoadBalancersPage.tsx`, `products/catalyst/bootstrap/ui/src/widgets/architecture-graph/ArchitectureGraphPage.tsx`, `docs/ledger/deleted-paths.txt`
- **CLD-021**: `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/bootstrap/api/internal/handler/infrastructure.go`, `products/catalyst/bootstrap/api/internal/infrastructure/xrc.go`, `products/catalyst/bootstrap/api/internal/jobs/mutation_bridge.go`, `docs/ledger/deleted-paths.txt`
- **CLD-023**: `platform/crossplane/compositions/**`, `platform/powerdns/chart/templates/crossplane-floatingip.yaml`, `clusters/_template/infrastructure/hetzner/**`, `clusters/_template/infrastructure/providers/hetzner/**`, `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/hetzner/main.tf`, `.github/workflows/preflight-crossplane-hcloud.yaml`, `docs/ledger/deleted-paths.txt`

</details>

## SOV Sovereign lifecycle

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SOV-030**<br>fix, 3 days | **Wipe refuses a handed-over Sovereign without confirmation**<br>As in the body, plus: the confirmation text says that a wipe also removes the virtual machines the Sovereign created. New logic lives in internal/lifecycle; wipe.go keeps only the call. | nothing else | stated in its ticket |
| **SOV-040**<br>guard, 3 days | **Guard G1: source-address ratchet**<br>Six-scope count of company and public source addresses with baseline and --list option | nothing else | stated in its ticket |
| **SOV-050**<br>guard, 1 day | **Guards G2, G3, G4, G7: shrink ratchets**<br>Ratchets on chart lines, kubectl mutation lines, git-push lines, sequencer lines, child client sites, twin parity | nothing else | stated in its ticket |
| **SOV-060**<br>lab test, 3 days | **Spike: born-local node registry list**<br>Seven yes/no rows on fall-through, wildcard refusal, Dragonfly order and live reload | nothing else | stated in its ticket |
| **SOV-070**<br>lab test, 3 days | **Spike: a scoped cloud identity per Sovereign on the reference cloud**<br>Six yes/no rows on minting, scoping and self-rotation; sets spec.identity.mode | nothing else | stated in its ticket |
| **SOV-090**<br>lab test, 3 days | **Spike: authoritative DNS and outbound mail on the reference cloud**<br>Four yes/no rows; sets DNS_MODE and MAIL_MODE defaults per cloud | nothing else | stated in its ticket |
| **SOV-080**<br>lab test, 3 days | **Spike: whole-state move, encrypted object-store state, zero-change plan, offline restore**<br>Seven yes/no rows: encrypted push, reference-cloud store, observe-only, zero-change plan with real state and scoped key, plan duration, offline restore | CLD-050 | stated in its ticket |
| **SOV-020**<br>fix, 1 day | **One default for the zero-touch switch**<br>All three fallbacks of the auto-fire switch agree on true | FND-011 | stated in its ticket |
| **SOV-035**<br>delete, 1 day | **Delete the ProvisioningState kind and its unwired store**<br>Dead kind and store removed (FND delete list item 22) | FND-011 | stated in its ticket |
| **SOV-010**<br>document, 1 day | **Record the cutover result of the baseline provision and correct the lifecycle documents**<br>Dated note in ADR-0002, corrected cutover line in STATUS.md, step list in label order | FND-070 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **SOV-030**: `products/catalyst/bootstrap/api/internal/handler/wipe.go`, `products/catalyst/bootstrap/api/internal/handler/wipe_test.go`, `products/catalyst/bootstrap/api/internal/handler/phase1_watch.go`, `products/catalyst/bootstrap/api/internal/lifecycle/**`
- **SOV-040**: `scripts/check-upstream-literals.sh`, `scripts/upstream-literals-baseline.txt`, `.github/workflows/check-upstream-literals.yaml`
- **SOV-050**: `scripts/check-lifecycle-ratchets.sh`, `scripts/lifecycle-ratchets-baseline.txt`, `.github/workflows/check-lifecycle-ratchets.yaml`
- **SOV-060**: `docs/ledger/plan/spikes/SOV-060-node-registry-list.md`
- **SOV-070**: `docs/ledger/plan/spikes/SOV-070-scoped-identity.md`
- **SOV-090**: `docs/ledger/plan/spikes/SOV-090-dns-mail.md`
- **SOV-080**: `docs/ledger/plan/spikes/SOV-080-state-move.md`
- **SOV-020**: `clusters/_template/bootstrap-kit/06a-bp-self-sovereign-cutover.yaml`, `platform/self-sovereign-cutover/chart/values.yaml`, `infra/providers/huawei/variables.tf`, `infra/providers/hetzner/variables.tf`
- **SOV-035**: `products/catalyst/bootstrap/api/internal/store/crd_store.go`, `products/catalyst/bootstrap/api/internal/store/crd_store_test.go`, `products/catalyst/chart/crds/provisioningstate.yaml`, `products/catalyst/chart/CRDS.md`, `products/catalyst/chart/values.yaml`, `products/catalyst/chart/Chart.yaml`, `docs/ledger/deleted-paths.txt`
- **SOV-010**: `docs/adr/0002-post-handover-sovereignty-cutover.md`, `docs/STATUS.md`, `CLAUDE.md`

</details>

## SEC Secrets and root of trust

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SEC-071**<br>test, 3 days | **Ratchet on passwords the provisioning service writes to Git**<br>Sentinel test and baseline listing every output file that carries the password; count may only fall. | nothing else | stated in its ticket |
| **SEC-125**<br>test, 3 days | **k3s bootstrap flags proven on a CI runner**<br>CI proof that three servers and one agent start with encryption on and separate tokens. | nothing else | stated in its ticket |
| **SEC-130**<br>lab test, 5 days | **Spike: OpenBao 2.7, static seal, self-initialisation, declared audit device**<br>Recorded chart values, unseal time, upgrade and seal-migration path, templated-policy proof. | nothing else | stated in its ticket |
| **SEC-135**<br>lab test, 5 days | **Spike: key-service seal on the reference cloud**<br>YES or NO on sealing OpenBao with the cloud key service, what the vendor must allow, and the data-disk device name. | nothing else | stated in its ticket |
| **SEC-140**<br>lab test, 5 days | **Spike: injection into an Organization's virtual cluster**<br>As before, plus two recorded results: the CSI mount in a pod with automountServiceAccountToken false; one Job created at run time inside an Organization's virtual cluster with one CSI object. | nothing else | stated in its ticket |
| **SEC-005**<br>document, 1 day | **Publish contract C8: seal provider and k3s bootstrap**<br>The file docs/contracts/c8-seal-provider.md with sections 4.1 and 4.2 of the record word for word. The contract index names it for C8. | FND-031 | stated in its ticket |
| **SEC-006**<br>decision, 3 days | **Decision record 0018: secrets and trust**<br>One accepted decision record with decisions S1 to S21 of section 1: injection, tiers, seal, state key, producer identities, verification at admission. | FND-106 | stated in its ticket |
| **SEC-010**<br>fix, 3 days | **Security fix. Its text is published with its ticket.**<br>Security fix. Its text is published with its ticket. | FND-032, FND-011 | Makes SEC-G04 required. |
| **SEC-020**<br>fix, 3 days | **Security fix. Its text is published with its ticket.**<br>Security fix. Its text is published with its ticket. | CLD-040, SEC-125 | stated in its ticket |
| **SEC-080**<br>fix, 5 days | **Security fix. Its text is published with its ticket.**<br>Security fix. Its text is published with its ticket. | FND-032, FND-011 | Makes SEC-G07 required. |
| **SEC-105**<br>delete, 3 days | **Delete Sealed Secrets**<br>Slot 05, the chart folder and the two placeholder templates removed with every dependant reference. | FND-091, FND-011 | stated in its ticket |
| **SEC-120**<br>test, 3 days | **etcd dump check and node hygiene check, as check files**<br>Check files for SEC-G01 and SEC-G02, entered in the manifest as expected-fail. | FND-031, FND-032 | Adds the check SEC-G01, SEC-G02. |
| **SEC-030**<br>fix, 3 days | **Security fix. Its text is published with its ticket.**<br>Security fix. Its text is published with its ticket. | SEC-020, SEC-120 | Makes SEC-G01 required. |
| **SEC-110**<br>document, 3 days | **Truth in the security documents**<br>False claims replaced by present facts; target sections marked; threat-model table inserted between markers; values of docs/ledger/controls.yaml filled. The claims inside the old threat table are not edited, because FND-105 replaces that table. | FND-105 | stated in its ticket |
| **SEC-040**<br>fix, 3 days | **Security fix. Its text is published with its ticket.**<br>Security fix. Its text is published with its ticket. | SEC-030 | Makes SEC-G02 required. |
| **SEC-090**<br>guard, 5 days | **Guards**<br>Guards G1 to G7 and G9 with shrinking allow-lists at today's values. | FND-013 | stated in its ticket |
| **SEC-100**<br>delete, 3 days | **Delete the abandoned OpenBao multi-region mechanisms**<br>Stretched-raft helper, snapshot replication and OpenBao Continuum resource removed; peer push untouched. | FND-013, FND-091, FND-011 | stated in its ticket |
| **SEC-073**<br>fix, 1 day | **Security fix. Its text is published with its ticket.**<br>Security fix. Its text is published with its ticket. | FND-091, SEC-090 | stated in its ticket |
| **SEC-095**<br>guard, 5 days | **Exception register, source guard and Secret census**<br>Register with class K, U and T entries of section 4.11, including the entries other areas asked for and an empty metadataAccess list; guards G10 and G11; the census check file, entered in the manifest as expected-fail. The removal of a reflected binding Secret names the consumer chart's roll-out package. Only SEC-096 and SEC-248 edit allowedUnmatched. | FND-032, FND-070, SEC-090 | stated in its ticket |
| **SEC-060**<br>fix, 5 days | **Security fix. Its text is published with its ticket.**<br>Security fix. Its text is published with its ticket. | FND-036, FND-080, SEC-040, FND-011 | Makes SEC-G06 required. |
| **SEC-096**<br>guard, 1 day | **Census limit set from the baseline; SEC-G11 required**<br>Field allowedUnmatched set to the number the baseline gate record measured. SEC-G11 flipped from expected-fail to required. | FND-070, SEC-095 | Makes SEC-G11 required. |

<details><summary>Files each task of this group changes</summary>

- **SEC-071**: `core/services/provisioning/gitops/secret_literal_ratchet_test.go`, `core/services/provisioning/gitops/testdata/secret-literal-baseline.txt`
- **SEC-125**: `tests/e2e/k3s-bootstrap-flags.sh`, `.github/workflows/test-k3s-bootstrap-flags.yaml`
- **SEC-130**: `docs/ledger/plan/spikes/`
- **SEC-135**: `docs/ledger/plan/spikes/`
- **SEC-140**: `docs/ledger/plan/spikes/`
- **SEC-005**: `docs/contracts/c8-seal-provider.md`, `docs/contracts/README.md`
- **SEC-006**: `docs/adr/0018-*.md`, `docs/adr/README.md`
- **SEC-105**: `clusters/_template/bootstrap-kit/05-sealed-secrets.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `clusters/_template/bootstrap-kit/51-bp-k8s-ws-proxy.yaml`, `clusters/_template/bootstrap-kit/52-bp-guacamole.yaml`, `scripts/expected-bootstrap-deps.yaml`, `scripts/sovereign-dod-verify.sh`, `scripts/check-no-nodeports.sh`, `platform/sealed-secrets/`, `platform/guacamole/chart/templates/keycloak-client-secret.yaml`, `platform/guacamole/chart/values.yaml`, `platform/guacamole/chart/tests/g117-w3d1-sso-secret.sh` and 3 more
- **SEC-120**: `tests/acceptance/sovereign/internal/suite/sec_etcd_job.yaml`, `docs/ledger/gates.yaml`
- **SEC-110**: `docs/SECURITY.md`, `platform/openbao/README.md`, `platform/external-secrets/README.md`, `docs/STATUS.md`, `docs/ledger/controls.yaml`
- **SEC-090**: `scripts/check-no-secret-values-in-git.sh`, `scripts/check-no-chart-random-secrets.sh`, `scripts/check-no-plain-secret-manifests.sh`, `scripts/check-no-derived-credentials.sh`, `scripts/check-no-secret-writers.sh`, `scripts/check-reflector-scope.sh`, `scripts/check-openbao-no-unseal-secret.sh`, `scripts/check-secret-delivery-lint.sh`, `scripts/secret-guard-baselines/`, `​.gitleaks.toml`, `.github/workflows/secret-guards.yaml`, `.github/workflows/check-guard-selftests.yaml`
- **SEC-100**: `platform/openbao/chart/templates/cross-region-raft-config.yaml`, `platform/openbao/chart/templates/cross-region-mesh-service.yaml`, `platform/openbao/chart/templates/continuum.yaml`, `platform/openbao/chart/templates/snapshot-replication.yaml`, `platform/openbao/chart/templates/_helpers.tpl`, `platform/openbao/chart/templates/_config.tpl`, `platform/openbao/chart/values.yaml`, `platform/openbao/chart/tests/`, `clusters/_template/bootstrap-kit/08-openbao.yaml`, `docs/ledger/deleted-paths.txt`
- **SEC-095**: `scripts/secret-guard-baselines/secret-object-register.yaml`, `scripts/secret-guard-baselines/threat-model.md`, `scripts/check-secret-object-register.sh`, `.github/workflows/secret-guards.yaml`, `docs/ledger/gates.yaml`
- **SEC-096**: `scripts/secret-guard-baselines/secret-object-register.yaml`, `docs/ledger/gates.yaml`

</details>

## NET Network, zones and cluster classes

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-031**<br>delete, 1 day<br>ticket ready<br>[ticket](tickets/NET-031.md) | **Stop reflecting the object-storage Secret to retired namespaces**<br>The shared cloud-init file no longer asks the reflector to copy the Secret object-storage of namespace flux-system into the retired namespaces mgmt, dmz and rtz. | nothing else | 6 commands that CI runs on the pull request |
| **NET-033**<br>fix, 1 day | **The definition-of-done verifier stops requiring retired releases**<br>Three retired release names removed from REQUIRED_HRS | nothing else | stated in its ticket |
| **NET-041**<br>delete, 3 days | **Remove the firewall claims from the storefront and the console**<br>No screen or catalogue entry offers or describes a firewall that does not exist | nothing else | stated in its ticket |
| **NET-042**<br>delete, 1 day | **Retire the two legacy per-Sovereign overlays**<br>Both overlay directories deleted (they install slots 06, 35 and 54) | nothing else | stated in its ticket |
| **NET-090**<br>guard, 3 days | **Network guards in CI**<br>Seven guards G1 to G7 with a self-written baseline allow-list that may only shrink, and for guard G2 a separate list of named exceptions with owner and reason. | nothing else | stated in its ticket |
| **NET-290**<br>lab test, 5 days | **Spike: platform-owned Cilium on the National Cloud's managed Kubernetes**<br>Outcome POSSIBLE, POSSIBLE-WITH-LIMITS or NOT-POSSIBLE; measured flags; list of what the vendor must allow | nothing else | stated in its ticket |
| **NET-300**<br>lab test, 1 day | **Spike: region separation on the reference cloud**<br>Outcome ZONE, SITE or NONE from the operator's written answer | nothing else | stated in its ticket |
| **NET-050**<br>decision, 3 days | **Zone decision record**<br>Accepted decision record with the seven statements of section 4.11 and the rejected alternatives | FND-106 | stated in its ticket |
| **NET-052**<br>decision, 1 day | **Decision record 0026: NodePort ban and the gateway serving model**<br>Accepted record 0026 with the plain header lines. It states the ban (no NodePort Service, no reliance on allocated node ports, nodePort 0 stated explicitly), the serving model of today (host ports on the host-network gateway pods, or a shared load-balancer address) and the target model after epic NET-180 (host ports on edge nodes behind a load balancer per region). It names the two existing guard scripts and check FND-G05. | FND-106 | stated in its ticket |
| **NET-270**<br>lab test, 3 days | **Spike: firewall attachment**<br>Outcome ATTACHED, SECOND-TIER or NONE with commands and output | CLD-055 | stated in its ticket |
| **NET-010**<br>fix, 3 days | **Security fix. Its text is published with its ticket.**<br>Security fix. Its text is published with its ticket. | FND-032 | Makes NET-G01 required. |
| **NET-020**<br>fix, 1 day | **Disable the k3s kube-proxy**<br>--disable-kube-proxy on the server install line with a render test | FND-032, NET-031 | Makes NET-G03 required. |
| **NET-030**<br>delete, 1 day<br>ticket ready<br>[ticket](tickets/NET-030.md) | **Retired names lose their policy privileges**<br>The Kyverno policy chart exempts no retired namespace and lists no retired chart name as substrate, and the two network policy charts allow no traffic for the namespace sandbox. | FND-011 | 12 commands that CI runs on the pull request |
| **NET-032**<br>fix, 3 days | **Reserve platform namespace names as Organization slugs**<br>Reserved slug list covering every kit namespace in the kind and in the tenant service, with a guard that keeps them in step with placement.yaml | FND-011 | stated in its ticket |
| **NET-060**<br>test, 3 days | **Read-only census of the network posture**<br>Discovery steps D1.1 to D1.8 recording kube-proxy, Cilium config, policies, metadata dial, listeners, nodes | FND-032 | Makes NET-G04 required. |
| **NET-070**<br>delete, 3 days | **Wizard shows what the provisioner builds**<br>Five zone topology cards replaced by two region-count choices | NET-050 | stated in its ticket |
| **NET-072**<br>delete, 1 day | **Placement selectors offer the host only**<br>Fallback mgmt becomes host; selector offers host only | NET-050 | stated in its ticket |
| **NET-080**<br>fix, 3 days | **Pin the Organization virtual cluster chart**<br>Exact vcluster chart version (highest 0.33.N from the chart index) in controller default, chart value and cutover mirror list | FND-011 | stated in its ticket |
| **NET-011**<br>fix, 5 days | **Security fix. Its text is published with its ticket.**<br>Security fix. Its text is published with its ticket. | FND-032, NET-010, FND-011, FND-014 | Makes NET-G02 required. |
| **NET-040**<br>delete, 5 days | **Remove the unwired firewall and the unused SPIRE chart**<br>Slot 35, both chart folders, seed and list entries removed; WireGuard no longer called mTLS | NET-030, FND-011, FND-031 | stated in its ticket |
| **NET-095**<br>fix, 3 days | **Security fix. Its text is published with its ticket.**<br>Security fix. Its text is published with its ticket. | NET-060, FND-011, NET-030 | Makes NET-G10 required. |
| **NET-012**<br>fix, 1 day | **Security fix. Its text is published with its ticket.**<br>Security fix. Its text is published with its ticket. | NET-011 | stated in its ticket |
| **NET-071**<br>delete, 5 days | **Remove the zone virtual-cluster dialog, tab and stub kind**<br>Dead dialog, DMZ panels, two routes and the stub kind removed; page lists Organizations | CLD-021, CLD-022, NET-050, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **NET-031**: `infra/providers/_shared/cloudinit-control-plane.tftpl`
- **NET-033**: `scripts/sovereign-dod-verify.sh`
- **NET-041**: `core/marketplace/src/components/AddonsStep.svelte`, `core/marketplace/src/lib/packages.test.ts`, `core/marketplace/src/lib/packagesLadder.test.ts`, `core/marketplace/src/lib/api.ts`, `core/services/catalog/handlers/seed.go`, `core/services/catalog/store/store.go`, `products/catalyst/bootstrap/ui/src/pages/marketplace/marketplaceCopy.ts`, `products/catalyst/bootstrap/ui/src/entities/deployment/model.ts`
- **NET-042**: `clusters/omantel.omani.works/**`, `clusters/otech.omani.works/**`
- **NET-090**: `scripts/check-network-guards.py`, `scripts/network-guards-allowlist.yaml`, `scripts/tests/check-network-guards/`, `.github/workflows/check-network-guards.yaml`
- **NET-290**: `docs/sessions/`
- **NET-300**: `docs/sessions/`
- **NET-050**: `docs/adr/README.md`, `platform/plane-isolation/README.md`, `scripts/check-live-node-encryption.sh`
- **NET-052**: `docs/adr/README.md`
- **NET-270**: `docs/sessions/`
- **NET-020**: `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/_shared/tests/cloudinit_render.tftest.hcl`, `infra/providers/_shared/tests/render.tf`, `.github/workflows/preflight-cilium-httproute.yaml`
- **NET-030**: `platform/kyverno-policies/chart/values.yaml`, `platform/plane-isolation/chart/values.yaml`, `platform/network-policies/chart/values.yaml`, `tests/e2e/bootstrap-kit/main_test.go`
- **NET-032**: `products/catalyst/chart/crds/organization.yaml`, `products/catalyst/chart/crds/tests/`, `core/services/tenant/handlers/handlers.go`, `scripts/check-reserved-slugs.py`, `.github/workflows/check-reserved-slugs.yaml`
- **NET-060**: `tests/acceptance/sovereign/internal/suite/discover.go`
- **NET-070**: `products/catalyst/bootstrap/ui/src/pages/wizard/steps/StepTopology.tsx`, `products/catalyst/bootstrap/ui/src/entities/deployment/model.ts`
- **NET-072**: `products/catalyst/bootstrap/ui/src/shared/lib/placement.ts`
- **NET-080**: `core/controllers/organization/cmd/main.go`, `products/catalyst/chart/templates/controllers/organization-controller-deployment.yaml`, `products/catalyst/chart/values.yaml`, `platform/self-sovereign-cutover/chart/values.yaml`
- **NET-040**: `clusters/_template/bootstrap-kit/35-coraza.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `clusters/_template/bootstrap-kit/placement.yaml`, `clusters/_template/bootstrap-kit/22-loki.yaml`, `platform/coraza/**`, `platform/spire/**`, `platform/plane-isolation/chart/values.yaml`, `platform/network-policies/chart/values.yaml`, `platform/openbao/blueprint.yaml`, `products/catalyst/chart/templates/catalog-seed/blueprints.yaml`, `scripts/expected-bootstrap-deps.yaml`, `scripts/expected-unseeded-blueprints.yaml` and 7 more
- **NET-071**: `products/catalyst/bootstrap/ui/src/pages/sovereign/cloud-compute/VClustersPage.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/networking/NetworkingPage.tsx`, `products/catalyst/bootstrap/api/internal/handler/networking.go`, `products/catalyst/bootstrap/api/cmd/api/main.go`, `platform/bp-vcluster-helmrepo/chart/templates/vcluster-crd.yaml`

</details>

### Group NET-220: Epic: placement contract without tiers

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **NET-221**<br>delete, 3 days | **Delete the four retired charts and their seed entries**<br>Four dead chart folders, three seed documents and their CI list entries removed | FND-096, NET-030, NET-042, NET-050, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **NET-221**: `platform/bp-dmz-vcluster/**`, `platform/bp-mgmt-vcluster/**`, `platform/bp-rtz-vcluster/**`, `products/dmz-vcluster/**`, `products/catalyst/chart/templates/catalog-seed/blueprints.yaml`, `scripts/check-kyverno-proxy-images.sh`, `scripts/check-no-nodeports.sh`, `scripts/check-netpol-egress-completeness.py`, `scripts/expected-bootstrap-deps.yaml`, `scripts/expected-unseeded-blueprints.yaml`

</details>

## SVC Service model

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SVC-010**<br>decision, 3 days | **Decision record and glossary edits**<br>Decision record and glossary edits. | nothing else | stated in its ticket |
| **SVC-020**<br>guard, 3 days | **Service-model ratchet guard**<br>Rules G1 to G9 with baselines that may only fall and a self-test per rule. The storefront numbers of rules G3 and G9 only stop a rise; no package of this area lowers them. | nothing else | stated in its ticket |
| **SVC-030**<br>fix, 3 days | **The Catalog page honours visibility and refuses what has no chart**<br>Show-unlisted toggle (default off); New instance disabled for version 0.0.0 | nothing else | stated in its ticket |
| **SVC-210**<br>lab test, 3 days | **Spike: the ring switch on the pinned Flux**<br>Spike: the ring switch on the pinned Flux. | nothing else | stated in its ticket |
| **SVC-190**<br>delete, 3 days | **Delete the Blueprint controller**<br>Dormant controller, its workflow, values block and templates removed | FND-091, FND-011 | stated in its ticket |
| **SVC-040**<br>fix, 3 days | **Normalise names, API version and visibility**<br>24 names prefixed bp-<folder>; every file on apiVersion v1 with explicit visibility; two missing blueprint.yaml files added; the two regenerated catalogue files committed in the same pull request (they are no longer discarded). | FND-011, SVC-020, FND-014, NET-030 | stated in its ticket |
| **SVC-150**<br>rework, 5 days | **One generator, part 1: embedded and console catalogue**<br>Python generator for outputs 1 and 2: no flag writes in place, --check, --dry-run for tests. The pull request commits the regenerated outputs. The drift workflow runs --check on pull requests, merge groups and main. Node generator deleted; summary defect fixed. | FND-011, FND-091, SVC-040 | stated in its ticket |
| **SVC-160**<br>rework, 5 days | **One generator, part 2: the seed becomes generated**<br>The seed is generated at the same path and committed by the pull request; five ghost objects gone; two hand registers and the sync script deleted; the release writer runs the generator after it has written versions; the release-owned list is changed for site 3. Implements SVC-G01 and sets it to expected-fail. | FND-031, FND-032, FND-091, SVC-150, FND-011 | Adds the check SVC-G01. |

<details><summary>Files each task of this group changes</summary>

- **SVC-010**: `docs/adr/README.md`, `docs/GLOSSARY.md`, `docs/adr/0020-service-model-classes-and-capability-contracts.md`
- **SVC-020**: `scripts/check-service-model.py`, `scripts/service-model-baseline.yaml`, `scripts/testdata/service-model/`, `​.github/workflows/check-service-model.yaml`
- **SVC-030**: `products/catalyst/bootstrap/ui/src/pages/sovereign/CatalogPage.tsx`, `products/catalyst/bootstrap/ui/src/pages/sovereign/CatalogDetail.tsx`
- **SVC-190**: `core/controllers/blueprint/**`, `​.github/workflows/build-blueprint-controller.yaml`, `products/catalyst/chart/values.yaml`, `docs/ledger/deleted-paths.txt`, `products/catalyst/chart/templates/api-deployment.yaml`, `products/catalyst/chart/templates/api-deployment-kustomize.yaml`
- **SVC-040**: `platform/*/blueprint.yaml`, `products/*/blueprint.yaml`, `products/*/chart/blueprint.yaml`, `platform/openova-flow-server/blueprint.yaml`, `platform/openova-flow-emitter/blueprint.yaml`, `scripts/service-model-baseline.yaml`, `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts`
- **SVC-150**: `scripts/generate-catalog.py`, `scripts/tests/test_generate_catalog.py`, `products/catalyst/bootstrap/ui/package.json`, `products/catalyst/bootstrap/ui/Containerfile`, `products/catalyst/bootstrap/ui/vite.config.ts`, `​.github/workflows/check-catalog-generated-drift.yaml`, `​.github/workflows/blueprint-release.yaml`, `​.github/workflows/check-release-lockstep-writer.yaml`, `scripts/check-release-lockstep-writer.py`, `scripts/check-train-coherent.sh`, `products/catalyst/bootstrap/api/internal/catalog/blueprints.json`, `products/catalyst/bootstrap/ui/src/shared/constants/catalog.generated.ts` and 1 more
- **SVC-160**: `scripts/generate-catalog.py`, `platform/_catalog/seed-exclusions.yaml`, `products/catalyst/chart/templates/catalog-seed/_README.txt`, `​.github/workflows/blueprint-release.yaml`, `​.github/workflows/check-catalog-seed-lockstep.yaml`, `scripts/check-catalog-seed-lockstep.sh`, `tests/acceptance/sovereign/internal/suite/check_svc_catalogue.go`, `scripts/check-release-owned-lines.py`, `products/catalyst/chart/templates/catalog-seed/blueprints.yaml`, `docs/ledger/gates.yaml`, `docs/ledger/deleted-paths.txt`

</details>

### Group SVC-050: Epic: function tests for the foundation ring

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **SVC-060**<br>test, 5 days | **Function-test convention and the data charts**<br>Runbook section and five function tests with fixed assertions | nothing else | stated in its ticket |
| **SVC-120**<br>lab test, 1 day | **Spike: Flux records test hooks and leaves Ready alone**<br>Spike: Flux records test hooks and leaves Ready alone. | nothing else | stated in its ticket |
| **SVC-070**<br>test, 5 days | **Function tests: identity and secrets charts**<br>Six function tests with fixed assertions | SVC-060 | stated in its ticket |
| **SVC-080**<br>test, 5 days | **Function tests: GitOps, registry and certificate charts**<br>Seven function tests with fixed assertions | SVC-060 | stated in its ticket |
| **SVC-090**<br>test, 5 days | **Function tests: network, names and edge charts**<br>Eight function tests with fixed assertions | SVC-060 | stated in its ticket |
| **SVC-100**<br>test, 5 days | **Function tests: admission, infrastructure and control-plane charts**<br>Seven function tests with fixed assertions | SVC-060, FND-011 | stated in its ticket |
| **SVC-125**<br>test, 3 days | **Blueprint proofs check: the function-test result in the gate record**<br>Check SVC-G06 in one new file with RegisterCheck. It fills blueprint and functionTest (pass, fail, none) on every artifacts.charts[] entry and fails when a foundation ring release that is not on the exemption list of rule G6 is not pass. The record schema gains the two fields in the same pull request. Implements SVC-G06 and sets it from not-built to expected-fail; it does not set required. | FND-032, FND-033, SVC-120 | stated in its ticket |
| **SVC-130**<br>test, 3 days | **Enable function tests on the foundation slots**<br>test.enable and ignoreFailures on every foundation slot file that exists (40 at 2c1f5b0c0); exemption list for charts without a test. It moves no gate check. It stops if the pinned Flux version differs from the record of SVC-120. | FND-031, FND-070, SVC-060, SVC-070, SVC-080, SVC-090, SVC-100, SVC-120, SVC-125 | stated in its ticket |
| **SVC-135**<br>test, 1 day | **The Blueprint proofs check becomes required**<br>One line: SVC-G06 from expected-fail to required. Precondition: python3 scripts/acceptance-gate.py verdict --check SVC-G06 exits 0. If it does not, stop and report the failing releases. | SVC-125, SVC-130 | Makes SVC-G06 required. |

<details><summary>Files each task of this group changes</summary>

- **SVC-060**: `docs/RUNBOOKS.md`
- **SVC-100**: `products/catalyst/chart/templates/tests/function-test.yaml`
- **SVC-125**: `tests/acceptance/sovereign/internal/suite/check_svc_proofs.go`, `tests/acceptance/sovereign/internal/suite/check_svc_proofs_test.go`, `scripts/acceptance-gate.py`, `docs/ledger/gates.yaml`
- **SVC-130**: `scripts/service-model-baseline.yaml`
- **SVC-135**: `docs/ledger/gates.yaml`

</details>

## CTL Control plane

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CTL-010**<br>guard, 3 days | **Guard: no new direct cluster write**<br>Ratchet on Kubernetes write call sites (metric K) and raw REST writes (metric R) in catalyst-api and core/services, with baseline file and self-test | nothing else | stated in its ticket |
| **CTL-020**<br>guard, 3 days | **Guard: no new polling loop**<br>Guard: no new polling loop. | nothing else | stated in its ticket |
| **CTL-025**<br>guard, 1 day | **Guard: the handler package and main may only shrink**<br>Ratchet on handler_lines, handler_methods, main_lines and the list of files in internal/handler | nothing else | stated in its ticket |
| **CTL-050**<br>lab test, 3 days | **Spike: push trigger, batch commit and lock on the pinned versions**<br>Recorded results for S1 (Receiver and system webhook on Flux 2.4 and Gitea), S2 (atomic batch commit), S3 (lock expressions, metric, break-glass), S4 (adoption without change), S6 (who creates Environments) | nothing else | stated in its ticket |
| **CTL-055**<br>lab test, 1 day | **Spike S10: Gitea push mirror between two Giteas on one database**<br>Spike S10: Gitea push mirror between two Giteas on one database. | nothing else | stated in its ticket |
| **CTL-044**<br>document, 3 days | **Publish contracts C3 and C4**<br>Publish contracts C3 and C4. | FND-031 | stated in its ticket |
| **CTL-060**<br>decision, 1 day | **Decision record for the write path, the lock and the backbone**<br>Decision record 0021 recording C1 to C21; supersedes ADR-0009. It carries one Unfreezes line for each topology entry touched by CTL-120, CTL-122, CTL-130, CTL-132, CTL-172 to CTL-176, CTL-260, CTL-410 and CTL-415. | CTL-050 | stated in its ticket |
| **CTL-040**<br>rework, 3 days | **Flux manifests to stable API versions**<br>HelmRepository documents and templates at source.toolkit.fluxcd.io/v1; README examples at helm v2 | FND-011 | stated in its ticket |
| **CTL-150**<br>new, 3 days | **Intent lock in audit mode, with its report**<br>ValidatingAdmissionPolicy with one binding per stage in audit, the burn-down report, guard G6, and the check file for CTL-G04 as expected-fail. The stage list is chart data: another area adds a stage or a resource with one values entry, one rule and one baseline line in its own package. | CTL-050, FND-032, FND-011 | Adds the check CTL-G04. |
| **CTL-030**<br>delete, 3 days | **Delete the endpoint pipeline and the two duplicate registrations**<br>Three endpoint routes and their handlers gone; duplicate PUT registrations removed after a test proves which handler serves | FND-171 | stated in its ticket |
| **CTL-035**<br>rework, 5 days | **Route registration leaves main()**<br>One register function per concern in cmd/api, built by buildRouter; route table and handlers unchanged, proven by a golden file generated before the move | CLD-021, CTL-025, CTL-030 | stated in its ticket |
| **CTL-045**<br>document, 3 days | **Publish contract C9 and its tool**<br>Contract file C9 with its two checks (guard G7): every mutating route classified. The tool is scripts/generators/mutating-routes.py with --check. The operation values are a list inside the contract file, so a package that adds an operation edits the file only. C3 and C4 moved to CTL-044. | CTL-030, CTL-035, CTL-044 | stated in its ticket |
| **CTL-032**<br>delete, 1 day | **Remove kubectl and helm from the API image**<br>The API image no longer carries the two binaries (deletion list item 31). Precondition: no non-test Go file under products/catalyst/bootstrap/api runs either binary; if one does, stop and report. | FND-037 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CTL-010**: `scripts/check-no-direct-cluster-write.py`, `scripts/baselines/direct-cluster-writes.json`, `.github/workflows/check-no-direct-cluster-write.yaml`
- **CTL-020**: `scripts/check-no-new-polling.py`, `scripts/baselines/polling.json`, `.github/workflows/check-no-new-polling.yaml`
- **CTL-025**: `scripts/check-api-structure.py`, `scripts/baselines/api-structure.json`, `.github/workflows/check-api-structure.yaml`
- **CTL-050**: `docs/ledger/plan/spikes/CTL-050.md`
- **CTL-055**: `docs/ledger/plan/spikes/CTL-055.md`
- **CTL-044**: `docs/contracts/C3-event-subjects.yaml`, `docs/contracts/C4-git-layout-and-commit-library.md`, `docs/contracts/C3-alert-changed-v1.schema.json`, `docs/contracts/README.md`, `scripts/check-event-subjects.py`, `scripts/baselines/streams.json`, `.github/workflows/check-contracts-ctl.yaml`
- **CTL-060**: `docs/adr/README.md`, `docs/adr/0009-per-org-iac-repo-bootstrap.md`, `docs/adr/0021-git-first-write-path-and-event-backbone.md`
- **CTL-040**: `platform/falco/README.md`
- **CTL-150**: `products/catalyst/chart/templates/intent-lock/policy.yaml`, `products/catalyst/chart/templates/intent-lock/bindings.yaml`, `products/catalyst/chart/values.yaml`, `products/catalyst/chart/tests/intent-lock.sh`, `scripts/intent-lock-report.py`, `scripts/check-lock-stages.py`, `scripts/baselines/lock-stages.json`, `tests/acceptance/sovereign/internal/suite/check_ctl_lock.go`, `tests/acceptance/sovereign/internal/suite/check_ctl_lock_test.go`, `docs/ledger/gates.yaml`
- **CTL-030**: `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/bootstrap/api/internal/handler/endpoint_handler.go`, `products/catalyst/bootstrap/api/cmd/api/main_test.go`, `scripts/baselines/direct-cluster-writes.json`, `scripts/baselines/api-structure.json`, `docs/ledger/deleted-paths.txt`
- **CTL-035**: `products/catalyst/bootstrap/api/cmd/api/main.go`, `products/catalyst/bootstrap/api/cmd/api/routes*.go`, `products/catalyst/bootstrap/api/cmd/api/routes_golden_test.go`, `products/catalyst/bootstrap/api/cmd/api/testdata/routes.golden`, `scripts/baselines/api-structure.json`
- **CTL-045**: `docs/contracts/C9-mutating-routes.yaml`, `products/catalyst/bootstrap/api/cmd/api/routes_contract_test.go`, `.github/workflows/check-contracts-ctl.yaml`, `scripts/generators/mutating-routes.py`
- **CTL-032**: `products/catalyst/bootstrap/api/Containerfile`, `docs/ledger/deleted-paths.txt`

</details>

## CMP Compliance and posture

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **CMP-005**<br>decision, 1 day | **Decision record: the compliance plane**<br>Accepted decision record 0029 with K1 to K14 and rejected alternatives, the module kinds in group compliance.openova.io, and the statement that this area names no guarded change class by default; text supplied in the ticket | nothing else | stated in its ticket |
| **CMP-014**<br>test, 3 days<br>ticket ready<br>[ticket](tickets/CMP-014.md) | **Run the policy chart's tests on every pull request**<br>A pull request that changes the policy chart or the policy engine chart runs the four policy suites and the two shell tests of the policy chart, with a tool version equal to the appVersion of the engine chart. | nothing else | 6 commands that CI runs on the pull request |
| **CMP-016**<br>fix, 3 days | **An optional informer kind starts when its definition arrives**<br>Skipped optional kinds are recorded and started on the customresourcedefinition event, with no timer | nothing else | stated in its ticket |
| **CMP-S1**<br>lab test, 3 days | **Spike: new policy kinds, exceptions, time conditions and the test tool on the 1.19 engine line**<br>Answers to six questions that fix the template version, exception values and the migration method | nothing else | stated in its ticket |
| **CMP-S2**<br>lab test, 1 day | **Spike: side-car publish to NATS, payload shape, heartbeat**<br>Captured payload pinned into the schema file; yes or no on stream capture and heartbeat | nothing else | stated in its ticket |
| **CMP-S3**<br>lab test, 1 day | **Spike: scanner report labels, publication date, benchmark report names, report volume**<br>Names and facts that CMP-091, CMP-088 and CMP-121 take | nothing else | stated in its ticket |
| **CMP-S4**<br>lab test, 1 day | **Spike: etcd load from report objects**<br>Object count and bytes per report kind against the trigger of 20,000 objects or 200 MB | nothing else | stated in its ticket |
| **CMP-S5**<br>lab test, 1 day | **Spike: do Organization workloads pass the baseline pod-security level**<br>Yes, or the named failing workload and control | nothing else | stated in its ticket |
| **CMP-S6**<br>lab test, 1 day | **Spike: does a requestedAt stamp make a HelmRelease re-read valuesFrom on the installed Flux line**<br>Yes, or the fallback annotation, or a dependency of CMP-072 on CTL-620 | nothing else | stated in its ticket |
| **CMP-012**<br>guard, 1 day | **Guard: legacy policy count may only fall; the upgrade acknowledgement value is forbidden**<br>Ratchet at 47 legacy policy objects in 29 files and a ban on acknowledgeLegacyPoliciesNotEnforced | FND-110 | stated in its ticket |
| **CMP-010**<br>test, 3 days | **Gate checks: the score moves by push; the shown mode is the engine's mode**<br>Implements CMP-G01 and CMP-G02 as expected-fail with a seeded failing workload | FND-032, FND-033 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **CMP-005**: `docs/adr/README.md`, `docs/adr/0029-compliance-plane.md`
- **CMP-014**: `.github/workflows/policy-chart-tests.yaml`, `scripts/run-policy-chart-tests.sh`
- **CMP-016**: `products/catalyst/bootstrap/api/internal/k8scache/factory.go`, `products/catalyst/bootstrap/api/internal/k8scache/factory_optional_test.go`
- **CMP-S1**: `docs/ledger/plan/spikes/CMP-S1.md`
- **CMP-S2**: `docs/ledger/plan/spikes/CMP-S2.md`
- **CMP-S3**: `docs/ledger/plan/spikes/CMP-S3.md`
- **CMP-S4**: `docs/ledger/plan/spikes/CMP-S4.md`
- **CMP-S5**: `docs/ledger/plan/spikes/CMP-S5.md`
- **CMP-S6**: `docs/ledger/plan/spikes/CMP-S6.md`
- **CMP-012**: `scripts/check-kyverno-legacy-policies.sh`, `scripts/baselines/kyverno-legacy.json`
- **CMP-010**: `tests/acceptance/sovereign/internal/suite/check_compliance_push.go`, `tests/acceptance/sovereign/internal/suite/check_compliance_mode.go`, `tests/acceptance/sovereign/internal/suite/check_compliance_test.go`, `docs/ledger/gates.yaml`

</details>

## BSS Commerce and billing

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **BSS-035**<br>fix, 3 days | **The storefront says when prices are unavailable**<br>Tagged result ok/none/unavailable; 404 and empty are none; unavailable shows a message and disables the plan step | nothing else | stated in its ticket |
| **BSS-050**<br>guard, 3 days | **Ratchets of this area**<br>Ten guards with baselines measured at 2c1f5b0c0 and a self-test | nothing else | stated in its ticket |
| **BSS-S2**<br>lab test, 1 day | **Spike: does the operator-hosted cloud export a FOCUS cost file**<br>Yes or no with the observed columns; no code depends on it | nothing else | stated in its ticket |
| **BSS-010**<br>decision, 3 days | **Decision record: commerce and billing**<br>Decision record: commerce and billing. | FND-106 | stated in its ticket |
| **BSS-020**<br>document, 1 day | **Truth in the status document and the engine's README**<br>One truthful status row (or none if the truth reset removed them); README points at the new record | BSS-010, FND-101, FND-102 | stated in its ticket |
| **BSS-030**<br>fix, 3 days | **Order capture never prices from a second list**<br>ErrNoPackages (404 or empty document); refusal 409 package-required at the top of priceFromCatalog when the document has packages; 503 on any other read error; check BSS-G02 | FND-031, FND-033, FND-052 | Makes BSS-G02 required. |
| **BSS-S1**<br>lab test, 3 days | **Spike: suite sign-in, suite access to the engine API, and the price list a fresh Sovereign shows**<br>Result record with the exact calls for (a) storefront customer sign-in, (b) engine API with sovereign scope, (c) measured price lists before and after the first Organization | FND-035 | stated in its ticket |
| **BSS-060**<br>test, 5 days | **Gate check: self-service onboarding**<br>Voucher, sign-in, order, serving application journey with no sovereign-admin action; memoised onboardedOrg; published price-list state recorded | BSS-030, BSS-S1, FND-031, FND-033 | Adds the check BSS-G01. |
| **BSS-040**<br>delete, 3 days | **Delete core/marketplace-api**<br>Dead service, its chart templates, workflow, image key and matrix legs removed | CTL-045, FND-091, FND-011 | stated in its ticket |
| **BSS-070**<br>test, 1 day | **BSS-G01 becomes required**<br>BSS-G01 required after two passing records that both show price_list_state published | BSS-035, BSS-060, FND-080 | Makes BSS-G01 required. |

<details><summary>Files each task of this group changes</summary>

- **BSS-035**: `core/marketplace/src/lib/packages.ts`, `core/marketplace/src/lib/packages.test.ts`, `core/marketplace/src/components/*.svelte`
- **BSS-050**: `scripts/check-bss-ratchets.py`, `scripts/baselines/bss.json`, `.github/workflows/bss-ratchets.yaml`
- **BSS-S2**: `docs/ledger/plan/spikes/BSS-S2.md`
- **BSS-010**: `docs/adr/0014-chargeback-usage-ledger-and-split-deployment.md`, `docs/adr/0001-catalyst-control-plane-architecture.md`, `docs/adr/README.md`, `docs/adr/0028-commerce-and-billing.md`
- **BSS-020**: `docs/STATUS.md`, `products/chargeback/README.md`
- **BSS-030**: `core/services/billing/packages/packages.go`, `core/services/billing/packages/packages_test.go`, `core/services/billing/handlers/handlers.go`, `core/services/billing/handlers/checkout_bss_test.go`, `tests/acceptance/sovereign/internal/suite/check_bss_price_list.go`, `docs/ledger/gates.yaml`
- **BSS-S1**: `docs/ledger/plan/spikes/BSS-S1.md`
- **BSS-060**: `tests/acceptance/sovereign/internal/suite/check_bss_onboarding.go`, `tests/acceptance/sovereign/internal/suite/bss_org.go`, `tests/acceptance/sovereign/internal/storefront/client.go`, `tests/acceptance/sovereign/internal/suite/runner.go`, `docs/ledger/gates.yaml`
- **BSS-040**: `core/marketplace-api/**`, `products/catalyst/chart/templates/marketplace-api/**`, `products/catalyst/chart/.helmignore`, `products/catalyst/chart/values.yaml`, `.github/workflows/marketplace-api-build.yaml`, `.github/workflows/catalyst-build.yaml`, `.github/workflows/test-ungated-trees.yaml`, `docs/contracts/C9-mutating-routes.yaml`, `docs/ledger/deleted-paths.txt`
- **BSS-070**: `docs/ledger/gates.yaml`

</details>

## AIP Agent plane

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **AIP-010**<br>lab test, 5 days | **Spike: gateway proof (agentgateway, tests T1 to T8)**<br>As before, plus two informative tests outside the pass rule: the three task paths are routed to a stub with the credential check and header removal; owned_by can be set per model object of GET /v1/models. The spike record gains the keys taskRoutes and ownedBy. | nothing else | stated in its ticket |
| **AIP-020**<br>lab test, 5 days | **Spike: simulation back end proof (agent kit turned inside out)**<br>Spike record: tool pass-through, 600-second parked handler, child authentication, usage in raw stream events | nothing else | stated in its ticket |
| **AIP-060**<br>decision, 1 day | **Decision record: the model plane**<br>Accepted decision record 0030 superseding ADR-0003, and the published model contract file. It states AIP-D6 as amended: the agent workspace stays on each Organization's own sign-in beside kagent and is not placed behind the gateway; the Anthropic Messages route is not offered. | nothing else | stated in its ticket |
| **AIP-030**<br>lab test, 5 days | **Spike: agent runtime proof (kagent 1.x on k3s, Cilium, vcluster)**<br>Spike record with versions, node preparation, placement and A2A address template | AIP-010 | stated in its ticket |
| **AIP-040**<br>fix, 1 day | **Security fix. Its text is published with its ticket.**<br>Security fix. Its text is published with its ticket. | FND-031, FND-032, FND-011 | Makes AIP-G01 required. |
| **AIP-050**<br>guard, 3 days | **Agent-plane guards**<br>Seven ratchet rules with measured baselines and a self-test | FND-011 | stated in its ticket |
| **AIP-070**<br>delete, 1 day | **Delete the hollow gateway charts**<br>Two hollow charts removed and guarded against return | AIP-010, SVC-160, FND-011 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **AIP-010**: `docs/ledger/plan/spikes/AIP-010.yaml`
- **AIP-020**: `docs/ledger/plan/spikes/AIP-020.yaml`
- **AIP-060**: `docs/adr/README.md`, `docs/adr/0003-rbac-newapi-user-create-hook.md`, `docs/contracts/model-contract.md`, `docs/adr/0030-model-plane.md`
- **AIP-030**: `docs/ledger/plan/spikes/AIP-030.yaml`
- **AIP-050**: `scripts/check-agent-plane.py`, `scripts/agent-plane-baseline.yaml`, `.github/workflows/agent-plane-guards.yaml`
- **AIP-070**: `platform/llm-gateway/**`, `platform/anthropic-adapter/**`, `scripts/expected-unseeded-blueprints.yaml`, `docs/ledger/deleted-paths.txt`, `scripts/agent-plane-baseline.yaml`

</details>

## INF Infrastructure plane

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **INF-030**<br>lab test, 3 days | **Spike: kured beside the upgrade controller on k3s**<br>Six yes/no answers: serial reboots, lock annotation, upgrade Pod label, kured waits on the upgrading node, a closed Plan window holds an upgrade, a server node reboots cleanly | nothing else | stated in its ticket |
| **INF-070**<br>decision, 1 day | **Decision record for the infrastructure plane**<br>Decisions I1 to I19 with rejected alternatives, no annex references | nothing else | stated in its ticket |
| **INF-010**<br>guard, 3 days | **Guard for the machine plane**<br>One script with seven metrics, a self-test and a baseline file | CTL-010 | stated in its ticket |
| **INF-040**<br>new, 3 days | **Chart bp-kured with the node patch configuration**<br>Wrapper chart for kured plus the node-patch-config DaemonSet that owns the two apt files | INF-030, FND-011 | stated in its ticket |
| **INF-050**<br>new, 3 days | **Install kured; check INF-G01**<br>Kit slot 04b and the destructive cloud check for coordinated reboots with the Plan-window assertion | INF-040 | Makes INF-G01 required. |
| **INF-060**<br>delete, 1 day | **Remove the reboot settings from cloud-init**<br>The two unattended-upgrade write_files blocks leave both templates; kured's DaemonSet is the only owner | INF-050, NET-031 | stated in its ticket |
| **INF-020**<br>lab test, 1 day | **Spike: hardware virtualisation on worker nodes**<br>Per pack: flavor and whether /dev/kvm exists on a worker of the operator-hosted stack | FND-070 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **INF-030**: `docs/ledger/plan/spikes/INF-030.md`
- **INF-070**: `docs/adr/0031-infrastructure-plane.md`
- **INF-010**: `scripts/check-machine-plane.py`, `scripts/baselines/machine-plane.json`, `.github/workflows/check-machine-plane.yaml`
- **INF-040**: `platform/kured/chart/**`, `platform/kured/blueprint.yaml`, `platform/kured/README.md`
- **INF-050**: `clusters/_template/bootstrap-kit/04b-bp-kured.yaml`, `clusters/_template/bootstrap-kit/kustomization.yaml`, `scripts/expected-bootstrap-deps.yaml`, `tests/acceptance/sovereign/internal/suite/check_inf_g01.go`, `docs/ledger/gates.yaml`
- **INF-060**: `infra/providers/_shared/cloudinit-control-plane.tftpl`, `infra/providers/hetzner/cloudinit-worker.tftpl`, `infra/providers/_shared/tests/**`, `scripts/baselines/machine-plane.json`, `docs/ledger/deleted-paths.txt`
- **INF-020**: `docs/ledger/plan/spikes/INF-020.md`

</details>

## MIG Migration from VMware

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **MIG-030**<br>lab test, 3 days | **Spike: a written volume boots on the reference cloud, through a unit Workspace**<br>A result record says which boot mode turns a written volume into a running server on the reference cloud through the pack's module, with the limits and the seconds one landing apply takes. | nothing else | stated in its ticket |
| **MIG-050**<br>lab test, 1 day | **Spike: hermetic conversion and boot on a standard CI runner**<br>A result record says how long a conversion and a boot take on a hosted CI runner, and so how often the hermetic test can run. | nothing else | stated in its ticket |
| **MIG-040**<br>lab test, 3 days | **Spike: address preservation while a subnet is split**<br>A result record says whether addresses are preserved while a subnet is split on the reference cloud, and names the relay variant and the foreign-address mechanism that worked. | MIG-030 | stated in its ticket |
| **MIG-060**<br>guard, 1 day | **Guard: no vendor disk library in the repository or in public images**<br>A required CI check fails any pull request that puts the vendor disk library into the repository, and later into a public image. | FND-013 | stated in its ticket |
| **MIG-020**<br>lab test, 3 days | **Spike: the disk-library read path and change tracking**<br>A result record says, with measured numbers, whether the disk-library read path with change tracking works, and fixes the nbdkit tag, the tested library versions and the connections per host. | MIG-060 | stated in its ticket |
| **MIG-010**<br>decision, 1 day | **Decision record: migration is a platform product; correct the documents**<br>A decision record states that brownfield virtual-machine migration is the optional Blueprint `bp-vm-migration`, and the documents stop saying it is not a Blueprint. | FND-104, FND-106 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **MIG-030**: `docs/ledger/plan/spikes/MIG-030-target-writer.md`
- **MIG-050**: `docs/ledger/plan/spikes/MIG-050-hermetic.md`
- **MIG-040**: `docs/ledger/plan/spikes/MIG-040-address-preservation.md`
- **MIG-060**: `scripts/check-no-vendor-disk-library.sh`, `.github/workflows/check-no-vendor-disk-library.yaml`, `.github/rulesets/main.json`
- **MIG-020**: `docs/ledger/plan/spikes/MIG-020-read-path.md`
- **MIG-010**: `docs/adr/0032-vm-migration-is-a-platform-product.md`

</details>

## ACC Access plane

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **ACC-020**<br>lab test, 5 days | **Spike: guacd 1.6.0 driven without the web application**<br>PASS or FAIL with exact parameter names for items (a) to (l): handshake, SSH certificate parameter, kubernetes protocol through the exec proxy, VNC, recording flush, piecewise replay, tunnel frames, clipboard controls, FIFO, resource use, start time, provenance of the browser library. | nothing else | stated in its ticket |
| **ACC-030**<br>lab test, 5 days | **Spike: per-session credentials through the mount; object lock**<br>PASS or FAIL and attribute names for items (a) to (i): ssh/issue, pki/issue with templated URI names, db/creds, mount without token, virtual-cluster identity, proxy trust file, SeaweedFS object lock, transit ed25519, behaviour under CSI rotation. | nothing else | stated in its ticket |
| **ACC-060**<br>decision, 3 days | **Decision record for the access plane**<br>Accepted decision record with decisions A1 to A21 and rejected alternatives; index line. | FND-106 | stated in its ticket |
| **ACC-050**<br>document, 3 days | **Truth in the access documents**<br>README, chart README and Blueprint card no longer state session recording as present; one pointer to the decision record; catalogue regenerated. | FND-102, FND-011 | stated in its ticket |
| **ACC-010**<br>fix, 3 days | **Security fix. Its text is published with its ticket.**<br>Security fix. Its text is published with its ticket. | FND-034, FND-080, FND-011 | Makes ACC-G01 required. |
| **ACC-040**<br>guard, 3 days | **Access guards with their baselines**<br>Guards ACC-GD1 to ACC-GD8 with self-test and baseline ratchet, run on pull_request and merge_group. | CTL-045, FND-017 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **ACC-020**: `docs/ledger/plan/spikes/ACC-020.md`
- **ACC-030**: `docs/ledger/plan/spikes/ACC-030.md`
- **ACC-060**: `docs/adr/README.md`, `docs/adr/0033-access-plane.md`
- **ACC-050**: `platform/guacamole/README.md`, `platform/guacamole/chart/README.md`, `platform/guacamole/blueprint.yaml`
- **ACC-040**: `scripts/check-access-guards.sh`, `scripts/baselines/access.json`, `.github/workflows/access-guards.yaml`

</details>

## PRD Later products

### Single tasks

| Task | What it does | Waits for | How it is proven |
|---|---|---|---|
| **PRD-010**<br>document, 1 day | **Remove the unbacked open-banking claims from the Keycloak README**<br>README without FAPI, eIDAS, TPP and PSD2 claims; one line saying open banking is not built. | nothing else | stated in its ticket |
| **PRD-030**<br>guard, 3 days | **Product guard at today's values (rules G-P1 to G-P3)**<br>One guard script with three rules, self-test fixtures and a baseline of 13 secretKeyRef lines; later rules are added by the packages that create their surface. | nothing else | stated in its ticket |
| **PRD-020**<br>decision, 3 days | **Decision record: later products**<br>Accepted decision record with the 20 decisions, alternatives, context (section 2 lines 1 to 25) and consequences, client-neutral. | FND-106 | stated in its ticket |

<details><summary>Files each task of this group changes</summary>

- **PRD-010**: `platform/keycloak/README.md`
- **PRD-030**: `scripts/check-products.py`, `scripts/baselines/products.json`, `.github/workflows/check-products.yaml`
- **PRD-020**: `docs/adr/README.md`, `docs/adr/0034-later-products-ai-hub-and-open-banking.md`

</details>

