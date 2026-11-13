# ADR-0031 — Infrastructure plane: the platform's own nodes first, a virtual machine is an Application, an inventory of machines, one Job per target

Status: Accepted
Date: 2026-11-13

Written by work package INF-070. It records how the platform provisions virtual machines, keeps an inventory of the machines that exist, and patches, scans, hardens and tests them, starting with the platform's own nodes. Interactive access and session recording (ADR-0033), the migration of virtual machines, the Kubernetes upgrade controller (SOV-720), the deadlines of vulnerability findings and the compliance screens, and node pools (ADR-0016) are decided elsewhere.

Terms used here:

- **Machine**: a host with an operating system: a node of a Sovereign's cluster, a virtual machine the platform built, or a server the platform discovered.
- **Provider pack**: the module, overlay, adapter and profile of one cloud (ADR-0016). The `local` pack runs on one CI runner.
- **Runner**: a short-lived Job that acts on one machine with one credential issued for that run.
- **Bill of materials**: the list of software packages installed on a machine.

## Context

Measured on `main` at commit `96013c422`, on 2026-11-13.

- The platform has no virtual-machine capability. No file outside `docs/ledger` and `docs/adr` mentions KubeVirt. The provider modules under `infra/providers/` create servers only as Kubernetes nodes.
- The definition `XHetznerServer` (`platform/crossplane/compositions/xrd-server.yaml`) describes a server on one cloud only. Work package CLD-023 deletes it.
- The API writes claims in the group `infra.openova.io` that no manifest defines (`products/catalyst/bootstrap/api/internal/infrastructure/xrc.go`; ADR-0016 decision K6 deletes this write surface).
- Node patching differs by provider. The shared control-plane template `infra/providers/_shared/cloudinit-control-plane.tftpl` and the Hetzner worker template `infra/providers/hetzner/cloudinit-worker.tftpl` install security updates and reboot the node at 02:30, with no drain and no lock. The Huawei worker template `infra/providers/huawei/cloudinit-worker.tftpl` installs the update package and sets no reboot. The guard `scripts/check-machine-plane.py` counts these two reboot settings in `scripts/baselines/machine-plane.json` and lets the count only fall.
- No file outside `docs/ledger` and `docs/adr` mentions kured or `system-upgrade-controller`. Nothing coordinates node reboots.
- No host is scanned for vulnerabilities. The chart `platform/syft-grype` is in the default kit (`clusters/_template/bootstrap-kit/33-syft-grype.yaml`), and its default target is empty, so it scans nothing (`platform/syft-grype/chart/values.yaml`). The Trivy operator (`platform/trivy`, kit slot 30) checks container images and node configuration.
- The compliance view reads `PolicyReport` and `ClusterPolicyReport` of `wgpolicyk8s.io/v1alpha2` through the informer cache (`products/catalyst/bootstrap/api/internal/k8scache/kinds.go`).
- `ListServers` of the provider port lists the servers of one deployment (`products/catalyst/bootstrap/api/internal/providers/provider.go`). Nothing lists the machines of a cloud account.
- A kind `Runbook` exists in `catalyst.openova.io` (`products/catalyst/chart/crds/runbook.yaml`). Its schema describes an automatic rule whose executor is a future controller; no executor exists.
- Contract C4 (`docs/contracts/C4-git-layout-and-commit-library.md`) reserves the module `machine-ops` for intent that every region applies, and states that fleet intent under `intent/` is applied in the writer region only.

## Decision

| # | Decision |
|---|---|
| I1 | **Three planes, separated by writer and by credential.** Provision is Crossplane running the provider pack's OpenTofu unit module in a Workspace. A Sovereign is started by OpenTofu and handed to Crossplane (ADR-0016 decision K7); a virtual machine is created after that hand-over, so its whole life is under Crossplane. Observe is an inventory controller that sends read requests only and writes `Machine` status. Act is short-lived runner Jobs that hold a per-run credential and nothing else. No component belongs to two planes. Limit stated: until the cloud can issue a read-only credential, the inventory controller reads the same mounted cloud credential that the provider pod uses, and a transport test fails any adapter request that is not a read. |
| I2 | **The platform's own nodes come first.** The first slice is coordinated node reboots by kured and one owner of the node patch configuration (INF-040, INF-050, INF-060). The next slice is host scans of the nodes in the compliance view, with no SSH (epic INF-100). The Kubernetes upgrade controller and its Plans stay with SOV-720. Reboots and upgrades are kept apart by two disjoint time windows. kured's pod selector is only a second, node-local guard: it looks at pods on the node it is about to reboot and nowhere else. |
| I3 | **A virtual machine is an Application.** A virtual machine is an Application of Blueprint `bp-virtual-machine`, class `infrastructure` (ADR-0020). The chart renders one `VirtualMachine` composite resource. It uses the Application's Git write path, Organization pipeline, catalogue, admission lock and metering. No second write path is built. |
| I4 | **The `VirtualMachine` contract.** Group `compose.openova.io`, version `v1alpha1`, namespaced, in the Crossplane 2 conventions of contract C6 (`docs/contracts/README.md`). It merges with two Compositions: one for the Huawei pack and one for the `local` pack. The region is not a field: a virtual machine belongs to the region of the cluster its resource lives in. |
| I5 | **KubeVirt is one more implementation, not the first.** A Composition `virtualmachine-kubevirt` is built only where spike INF-020 shows hardware virtualisation on worker nodes, or where a Sovereign runs on bare metal (epic INF-800). The version 1 fields are chosen so that each maps to one KubeVirt field. |
| I6 | **Related contracts stay small.** An image is an entry of a catalogue that is one data document per cluster, not a kind. Disks are inline in the virtual machine. A snapshot is a second composite kind, added with its first reader, the pre-patch snapshot (INF-470). A network attachment is not written: a machine attaches to its region's subnet with its own security group. |
| I7 | **The inventory is custom resources.** One `Machine` object per machine, written only by the inventory controller. One `MachineSource` per source, born in Git or in chart values. Summaries live in the object, with a bounded size. Bulk evidence (bills of materials, raw scan output) lives in the Sovereign's object store, reached through the `object-storage` capability binding (ADR-0020). There is no inventory database. |
| I8 | **The guard rules of the inventory are normative.** A machine becomes stale only by a complete enumeration of the same source. A sweep that would retire too many machines is held. A machine whose identity changes is quarantined. Reuse of an address by another machine is detected. Every machine is stamped with its ownership: `declared`, `adopted` or `unmanaged`. Each rule has a table-driven test before any run can target a machine. |
| I9 | **Region locality.** Every region's cluster runs its own controllers, holds the inventory of its own region and runs operations against it. Policy objects are the same in every region. Nothing in this plane calls another cluster. |
| I10 | **One new API group, owned by a Blueprint.** `machines.openova.io`. Its kinds are kinds of the Blueprint `bp-machine-operations`: they ship in that chart's `crds/` folder, exist only where the Blueprint is installed, and are not platform entities. Eight kinds in the first slices; a ninth, `MachineImage`, with epic INF-600. Every kind and every field enters the tree in the work package that carries its reader. A Blueprint's own kinds do not sit in a platform group. |
| I11 | **Policies are born in Git; runs are audited operations.** Patch, scan, hardening and test policies, maintenance windows and sources are desired state. They live in Git as intent that every region applies, module `machine-ops` of contract C4. A run is an operation record, `MachineRun`, created by the controller from a policy or by an audited route, and it can do nothing its policy does not allow. |
| I12 | **Execution by one Job per target.** A pinned runner image carries Ansible and the standard tools. Node mode mounts the node's file system read-only and uses no SSH. SSH mode signs a 30-minute certificate in OpenBao for each Job (INF-410 with SEC-310). Windows Server 2022 and later use the same certificate path; older Windows uses HTTPS remote management with a password that is rotated after every use. |
| I13 | **Results are report objects.** One `PolicyReport` per machine and per report type, in the version `wgpolicyk8s.io/v1alpha2` that the compliance view already reads. Vulnerabilities are matched centrally from a stored bill of materials, after every new bill of materials and once a day, by Trivy, the scanner the platform already runs. A Sovereign has one matcher and one vulnerability database. Deadlines and exceptions belong to the compliance plane. |
| I14 | **Standard engines replace custom code.** ComplianceAsCode content with OpenSCAP; Trivy for the bill of materials and the matching; Nuclei and the ZAP baseline scan; kured; Ansible. No port of a third-party engine, no second matcher, no nmap, no Packer 1.10 or later, no AWX, no second console. `scripts/check-machine-plane.py` refuses the forbidden tools in the machine plane. |
| I15 | **Light testing is scoped outside the tool.** A `TestScope` object, a generated egress policy on the runner, a time window, a kill switch that fails closed, and a human approval. Profiles are passive. An agent can request a test; it cannot start one. |
| I16 | **Patch path.** Replace the machine from a newer image where it is stateless. Patch in place, in rings, where it is not. A run never reports a success it did not verify. |
| I17 | **The native option.** The cloud's own host security and patch services are engines behind a capability of the provider port, declared per function and per operating-system family. A policy gains the field `engine` together with its second engine. Built after a live spike on the Huawei Cloud Stack installation (INF-710). |
| I18 | **Golden images are baked through the platform.** An image is built by creating a virtual machine, running the baseline on it, and capturing it through the provider pack. Built after spike INF-610. No external image tool is embedded. |
| I19 | **Order of delivery.** The platform's own nodes (INF-040 to INF-060), node scans (epic INF-100), virtual machines (epic INF-200), discovery and ownership (epic INF-300), operations over SSH (epic INF-400), light testing (epic INF-500), golden images (epic INF-600), the native engine (epic INF-700), KubeVirt (epic INF-800), Windows and hypervisor sources (epic INF-900). Each slice has its own gate check, INF-G01 to INF-G15. |

## Alternatives rejected

**I1.** (a) One service that provisions, discovers and acts. Its one workload would hold every credential and be the single point to fence. (b) Crossplane for all three planes: one managed resource per discovered machine, operations as Crossplane functions. It cannot see machines that nobody declared, it cannot express work inside the guest, and Crossplane Operations are an alpha feature.

**I2.** (a) Start with the virtual-machine service. The platform's own nodes reboot themselves at one fixed minute with no drain, and the Huawei workers do not reboot at all; that is the first risk to close. (b) Add the upgrade controller here. SOV-720 owns it; two owners of one chart is the outcome to avoid.

**I3.** (a) New routes and a new Git path for virtual machines in the Organization repository. It needs a new path, a new role rule, a new lock stage, new metering and new screens, when the Application path gives all of them. (b) Commit the composite resource directly under the fleet intent path. That path is applied in the writer region only (contract C4), so a machine of another region would be managed from the wrong cluster and duplicated when the writer moves.

**I4.** (a) A `region` field. A resource in one cluster that targets another region keeps its state in the wrong region's bucket, and is orphaned or duplicated when the region moves. (b) Neutral `vcpu` and `memoryGiB` fields. The cloud implementations read a flavor name; a second pair of fields would have no reader. (c) One definition per cloud. That is the design of `XHetznerServer`, which CLD-023 deletes.

**I5.** KubeVirt first. The reference cluster class runs on cloud virtual machines, and hardware virtualisation there is untested. Spike INF-020 measures it on the Huawei Cloud Stack installation, which is the target. KubeVirt also needs a second network (Multus) beside Cilium to keep a machine's addresses.

**I6.** (a) Kinds for image, disk and network attachment now. None of them has a second implementation or a reader in this plan. (b) The image list inside the installation document of the cloud profile (ADR-0016 decision K3). That document changes only at provisioning or in a maintenance window, and images rotate weekly.

**I7.** (a) A document database. It adds a store, an API and a change feed, and it loses label selectors, Kubernetes access control and the path from the informer to the browser that the console already has. (b) Everything in the object. A software inventory is megabytes; the object keeps a pointer and a summary. Limit stated: this design is sized for up to 20,000 machines per region cluster; beyond that the store is decided again.

**I8.** Trust the last-seen time. It is a proxy that is wrong whenever an enumeration is partial, and a record whose address answers as another machine would then receive that machine's actions.

**I9.** One central inventory in the writer region that reaches every region. It needs credentials for other clusters and breaks when the writer moves. The console already joins the streams of each cluster.

**I10.** (a) The kinds in `catalyst.openova.io`. That group holds the platform's own kinds, and access rules, the lock stage and the Organization role are all written per group. (b) Reuse `infra.openova.io`. It is the group of the write surface that ADR-0016 deletes. (c) Reuse the `Runbook` kind for runs. It is an automatic rule inside the cluster, and its executor was never built.

**I11.** (a) A chart value that carries whole policy objects beside Git. Two ways to deliver one object need a rule for when they conflict. (b) Every run as a file in Git. Scheduled runs would make the controller the Git author of thousands of files that must then be pruned, and a run is not desired state. The audit stream records operations; Git records policy. An approval is recorded on the run by an audited route that refuses the requester as approver.

**I12.** (a) AWX. It has had no release since July 2024. (b) Semaphore. It brings a second console, a second database and a second access model. (c) A long-lived executor service fed by a queue. A standing service holds standing credentials and needs its own caller policy; a Job per target needs neither. (d) An agent on every machine. Rejected as the base; it may come later as an optional Blueprint.

**I13.** (a) A new report kind. The compliance view reads `wgpolicyk8s.io/v1alpha2` reports today. (b) OpenReports now. The schema is young; this plane follows the compliance plane when it moves. (c) OpenSCAP as the vulnerability matcher. Red Hat publishes no OVAL data from RHEL 10 on. (d) Syft with Grype as a second matcher. The platform keeps Trivy as its one vulnerability source, and epic FND-190 deletes the idle Syft and Grype chart. A second matcher would mean a second database to mirror into a Sovereign whose egress is closed, and two severity vocabularies in one view. Cost accepted: the reports carry no exploit-probability score.

**I14.** Write or port a scanner and a benchmark engine. A port of a third-party engine is a derived work under that engine's licence and must follow every upstream release. nmap is under a licence that restricts its use in proprietary products. Packer is not open source from version 1.10.

**I15.** Scope in the agent's prompt or in a document, or a scope that a switch can override while the test runs. A reviewed scope that can be overridden at run time is not a control, and a kill switch that depends on a store it may not reach fails open. Command-and-control tooling is excluded from every profile.

**I16.** (a) In-place patching only. It gives no rollback and no reproducible state. (b) Image rotation only. Stateful machines and discovered machines cannot be replaced.

**I17.** One `native` flag per provider. A cloud's scan service and its patch service do not cover the same operating-system families, so one flag cannot say what is native.

**I18.** (a) Packer. Its licence (I14). (b) The Kubernetes image-builder on Packer 1.9.5. It freezes a tool of 2023; it stays the fallback if spike INF-610 fails. Limit stated: baking through the platform has no upstream implementation to follow, hence the spike.

**I19.** (a) Virtual machines first: rejected under I2. (b) KubeVirt first: rejected under I5.

## Consequences

- The reboot settings leave the cloud-init templates once kured owns reboots (INF-060). The update package stays on every node.
- The kinds of `machines.openova.io` arrive with `bp-machine-operations` (epic INF-100). An installation without the Blueprint has none of them.
- No vulnerability matcher is added; Trivy is the one matcher, and epic FND-190 removes Syft and Grype.
- The `VirtualMachine` definition is written in the Crossplane 2 form of ADR-0016 decision K8, and it merges after the `NodePool` definition of CLD-175 (INF-230).
- A machine credential is never a field of a resource, and every runner tool is pinned; `scripts/check-machine-plane.py` holds both.
- Limits accepted now: the inventory controller reads with a write-capable cloud credential until the cloud issues a read-only one; vulnerabilities are re-matched once a day; the reports carry no exploit-probability score.

## Shipped via

Work packages of area INF in `docs/ledger/plan.yaml`: INF-010 (the guard), INF-020 and INF-030 (spikes), INF-040, INF-050 and INF-060 (coordinated reboots), INF-070 (ADR-0031), and the epics INF-100 (machine operations on the platform's own nodes), INF-200 (virtual machines), INF-300 (discovery and ownership), INF-400 (operations over SSH), INF-500 (light testing), INF-600 (golden images and rotation), INF-700 (native engine), INF-800 (KubeVirt, only where INF-020 says yes) and INF-900 (Windows and hypervisor sources).
