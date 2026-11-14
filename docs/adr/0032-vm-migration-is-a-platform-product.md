# ADR-0032 — Migration is a platform product: brownfield virtual-machine migration is the optional Blueprint `bp-vm-migration`

Status: Accepted
Date: 2026-11-14

Written by work package MIG-010. It records that moving running virtual machines from VMware vSphere onto a Sovereign's cloud is software of the platform, in one optional Blueprint, and fixes its shape: the product, its resource kinds, how it reads the source, how the vendor disk library reaches it, how it writes the target, its routes, and the layer-2 extension. Rules of the build that do not change the shape (the worker Job, guest conversion, the cutover state machine, credentials, events, the order of proof) are written in the work packages of area MIG. The `VirtualMachine` contract, which a landed machine becomes at acceptance, is decided in ADR-0031.

Terms used here:

- **Source**: the vSphere environment a machine leaves. **Target**: the cloud where it lands.
- **Disk library**: the VMware virtual disk development kit (VDDK), a set of shared libraries that read virtual disks over the network.
- **Change tracking**: the vSphere feature that reports which disk areas changed since a named point.
- **Warm** migration: the machine keeps running while its disks are copied and synchronised again; only the last synchronisation needs it stopped.
- **Reference migrator**: the open-source Forklift project, the upstream of the Migration Toolkit for Virtualization.
- **Reference cloud**: the Huawei Cloud Stack installation that is the primary substrate (ADR-0024).

## Context

Measured on `main` at commit `7f928cc3f`, on 2026-11-14.

- The canonical documents said that migration is a service and not software. `docs/ARCHITECTURE.md` §3.5 said "Specter (AIOps agents) and Exodus (migration program) are NOT composite Blueprints", and the repository tree in `README.md` called exodus a deliverable service. Work package MIG-010 corrects both, together with this decision.
- The platform has no migration code. Outside `docs/ledger` and `docs/adr`, the only tracked file that names the disk library, nbdkit, `virt-v2v`, govmomi or Forklift is the guard `scripts/check-no-vendor-disk-library.sh`.
- `products/catalyst-migrator` is a runtime image for database schema-migration Jobs. It is not related to this product.
- The guard `scripts/check-no-vendor-disk-library.sh` (MIG-060) refuses the disk library in any tracked file, with no allow-list.
- Spike MIG-050 (`docs/ledger/plan/spikes/MIG-050.md`) measured a `virt-v2v-in-place` conversion and a boot of the converted disk on a standard CI runner: under five minutes, outcome D1.
- Contract C6 (`docs/contracts/c6-crossplane-conventions.md`) already names the migration controller, in namespace `vm-migration-system` only, as one creator of unit Workspaces.
- The Huawei module sets `source_dest_check = false` on the network interface of every node (`infra/providers/huawei/main.tf`), so traffic with foreign network addresses already crosses nodes there. Nothing shows that the reference cloud forwards frames with foreign hardware addresses.
- nbdkit's VDDK plug-in is deprecated upstream and will be removed in nbdkit 1.50. The disk library's licence does not permit redistribution.

## Decision

The decisions keep the numbers of the design of area MIG, which the work packages cite. M3, M6, M9, M10 and M12 to M16 are rules of the build packages and are not decided here.

| # | Decision |
|---|---|
| M1 | **Product shape.** Migration is a platform product: one optional Blueprint `bp-vm-migration` in `products/vm-migration/`, class `infrastructure` (ADR-0020), installed by the sovereign-admin and never part of the bootstrap kit. The statement that migration is a deliverable service and not a Blueprint is withdrawn. |
| M2 | **Resource model.** Six kinds in a new group `migration.openova.io`, version `v1alpha1`: `SourceProvider`, `NetworkMap`, `StorageMap`, `MigrationPlan`, `Migration`, `MigrationWave`. Names and fields follow the reference migrator wherever the meaning is the same. Cutover, acceptance and rollback are fields of `Migration`, so each is a Git commit. These are the Blueprint's own kinds: they ship in its chart, arrive with the Blueprint and are absent without it. They are not platform entities, and the group is named after the product. |
| M4 | **Read path.** The primary and only built read path is the disk library, loaded at run time by nbdkit's VDDK plug-in, with vSphere change tracking for warm synchronisation. nbdkit is built from source at a pinned 1.48 tag inside the worker image, because upstream removes the plug-in in 1.50. A helper machine inside the source is recorded as a second path; it is not built. |
| M5 | **Supply of the disk library.** The library is never in a public image, chart, repository or release artifact. A sovereign-admin puts it into the Sovereign's own registry as a private image, in the layout the reference migrator already uses. `SourceProvider` names that image by digest, and an init container copies the library into the worker at start. A CI guard keeps public artifacts clean. |
| M7 | **Target writer.** The target writer is a contract on the provider pack's OpenTofu unit module, not Go code. The port and the server of a landed machine are created by the unit module `day2/virtualmachine` of the infrastructure plane (ADR-0031, INF-220), extended by landing variables. Crossplane runs it in a plain unit Workspace that the migration controller creates per machine in its own namespace, under an admission rule that limits the module address. The module makes the written system volume bootable in the pack's own way. The migration product makes no cloud call and holds no cloud credential. The first pack is the reference cloud, with the boot mode chosen by spike MIG-030. The `local` pack implements the same variables, so the contract never has one implementation only. |
| M8 | **Routes.** Three routes, one resource model. `cloud-server` (the platform's own thin controller, landing through the pack's unit module) is built first. `kubevirt` (the reference migrator, unchanged, driven by rendered objects) is built only after the infrastructure plane ships a KubeVirt implementation (ADR-0031 decision I5). `native-service` (a cloud's agent-based migration service) is reserved and built only for a cloud that publishes one; the reference cloud does not. An enum value enters the schema with its reader. |
| M11 | **Layer-2 extension.** One optional network capability, `NetworkExtension`, a Crossplane 2 composite in `compose.openova.io` under the conventions of contract C6. Three implementations are named: `relay` (a routed pair that answers address resolution for moved machines; built first), `native` (the cloud's own layer-2 gateway) and `bridge` (a true layer-2 tunnel, for KubeVirt targets). It is a time-boxed bridge with a mandatory removal date, not a permanent network. Its intent file lives in the folder of the region it serves, and the resource carries no region field. |

## Alternatives rejected

**M1.** (a) Keep migration as a services offer with private scripts. The work would stay outside the repository, unreviewed and untested, and every migration would start again from notes. (b) Put the engine in the bootstrap kit. Most Sovereigns never migrate a machine, and the kit is for the kernel of the platform.

**M2.** (a) Use the reference migrator's kinds directly for every route. Its controllers write only to KubeVirt, and its kinds carry no wave, no rollback verb and no network cutover. (b) Put the kinds in `catalyst.openova.io`. That group's definitions ship in the umbrella chart to every Sovereign; these kinds belong to an optional Blueprint and must be absent where it is not installed. (c) One large `Migration` kind with everything inline. The maps are shared by many plans of one source, and kinds that match the reference migrator make the KubeVirt route a copy of fields. (d) A separate `Hook` kind and a separate cutover kind. Two more kinds with one reader each; hooks are a list in the plan and cutover is a field.

**M4.** (a) The helper machine inside the source as the lead path. The disk library is the path every mature tool was built on, and it needs nothing deployed in the source. The helper path stays recorded because the plug-in has an end date. (b) Link the library directly from Go. It replaces a maintained, BSD-licensed plug-in and nbdkit's retry and read-ahead filters with our own code. It is the named fallback: nbdkit's plug-in interface is stable, so the last upstream plug-in source can be built out of tree against a newer nbdkit. (c) Track a distribution's nbdkit package. The package will drop the plug-in without notice. (d) A clean-room replacement library. Rejected as the default: it had one author and was weeks old when it was assessed.

**M5.** (a) Ship the library inside the worker image. Its licence forbids redistribution, and the image is public. (b) Download it at run time from the vendor. Public downloads have been withdrawn. (c) A host path on the migration nodes. A file placed by hand on a node is outside Git and is lost when the node is replaced.

**M7.** (a) A Go interface with one adapter per pack that calls the cloud directly. It is a second adapter registry beside the provider port (ADR-0016), it puts a cloud credential into the controller, and provisioning would not begin with OpenTofu. (b) The controller creates the `VirtualMachine` composite itself. Composites of `compose.openova.io` are desired state, and only Flux writes them (ADR-0021). (c) The landed machine as an Application committed before cutover. Every step would cross Git, Flux, the Application pipeline and Helm inside the downtime window, and the controller may not commit. The Application arrives at acceptance instead, through the adoption flow of the infrastructure plane (INF-465), which imports what the same module created and therefore plans zero changes. (d) A separate landing module. Two modules that describe one server cannot both plan zero changes. Costs accepted, and measured by spike MIG-030: one OpenTofu run sits inside the downtime window; and if only swapping the system disk works on a pack, that detach and re-attach cannot be written in a module, so one optional capability is then added to the provider port for those two calls and nothing else.

**M8.** (a) Fork the reference migrator and add cloud targets. Its destination is KubeVirt throughout its code; the fork would be permanent. (b) A cloud's native migration service only. It covers one cloud, needs an agent in every guest, and excludes domain controllers, clustered databases and encrypted disks; the reference cloud does not publish one. (c) Commercial engines. Their licences are per machine, or source-available terms that bar embedding.

**M11.** (a) Make address preservation depend on the layer-2 extension. A whole subnet moved in one window needs no extension; the extension covers only the time while a subnet is split. (b) A transparent layer-2 bridge as the first implementation. Nothing shows that the reference cloud forwards frames with foreign hardware addresses; the platform's own evidence is for foreign network addresses only. (c) The cloud's native layer-2 gateway first. It is not published on the reference cloud and has no resource in its OpenTofu provider. (d) A kind in `migration.openova.io` reconciled by the migration controller. The cloud half is infrastructure with one implementation per cloud, which is what a composite with one Composition per implementation is for (ADR-0016).

## Consequences

- The kinds of `migration.openova.io` arrive with `bp-vm-migration`. An installation without the Blueprint has none of them.
- The product's code, chart, Blueprint file and images live under `products/vm-migration/` (MIG-101, MIG-102). The repository tree in `README.md` gains the folder when the folder exists.
- The disk library is never in a public artifact. `scripts/check-no-vendor-disk-library.sh` (MIG-060) holds this for the repository.
- Nothing about migration is described as working, proven or piloted until the gate checks MIG-G04, MIG-G05 and MIG-G06 are `required` in `docs/ledger/gates.yaml`. Until then the canonical documents mark it target, not built.
- No build package starts before its spike has a result record under `docs/ledger/plan/spikes/`, and the build follows the `VirtualMachine` contract (INF-230).

## Shipped via

Work packages of area MIG in `docs/ledger/plan.yaml`: MIG-010 (ADR-0032), MIG-060 (the guard), MIG-020, MIG-030, MIG-040 and MIG-050 (spikes), the epics MIG-100 (hermetic engine), MIG-130 (landing on the reference cloud), MIG-140 (network extension), MIG-150 (write path and screens) and MIG-165 (adoption of a landed machine), MIG-170 (waves), MIG-180 (runbook and status), and the epic MIG-200 (route `kubevirt`, only after the infrastructure plane ships KubeVirt).
