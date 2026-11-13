# ADR-0020 — Service model: classes and capability contracts

Status: Accepted
Date: 2026-11-13
Supersedes: ADR-0010 in part: how the connection keys reach the consumer, and the unused field `spec.backingServices` with its generator (the rule that in-cluster services use no custom controller and no Crossplane stands)
Unfreezes: topology-continuum
Unfreezes: application-controller

Written by work package SVC-010. The decision was taken on 2026-11-12. The points of ADR-0010 that it replaces are listed under "What changes in ADR-0010". The two `Unfreezes:` lines are explained under "What the Unfreezes lines permit".

## Context

Measured on `main` at commit `05b29b4e5`, on 2026-11-13.

- Installable software has two kinds: Blueprint, the definition, and Application, the installed thing. Both are defined in `products/catalyst/chart/crds/`. The Blueprint definition serves `v1alpha1` and `v1`.
- There are 102 `blueprint.yaml` files: 97 under `platform/` and 5 under `products/`. 60 declare `catalyst.openova.io/v1alpha1` and 42 declare `catalyst.openova.io/v1`.
- A Blueprint has no field that says what kind of thing it is, how far it is proven, or which capability it offers or needs. Five free-text classifiers stand in: `spec.card.category` (42 files, 16 values), `spec.card.family` (36 files), and the labels `catalyst.openova.io/section` (102 files, 24 values), `catalyst.openova.io/category` (30 files) and `catalyst.openova.io/tier` (20 files). `spec.visibility` is unset in 30 files, `listed` in 23 and `unlisted` in 49.
- A consumer names the Blueprint it needs exactly. The link from a consumer to a shared provider is a Context: an entry of the consumer Application's `spec.dependsOn[]` names the provider Application and the Context it occupies there. Two Blueprints are shareable, `platform/postgres` and `platform/valkey`. Under ADR-0010 the connection keys reach the consumer as a Kubernetes Secret object that is reflected into its namespace (`platform/postgres/chart/templates/role-secrets.yaml`).
- ADR-0010 also defined the field `spec.backingServices` and a generator for it, `core/controllers/pkg/backingservice/`. No `blueprint.yaml` declares the field.
- The catalogue is kept in several places. The cluster seed, `products/catalyst/chart/templates/catalog-seed/blueprints.yaml`, is a hand-written Helm template of 6,299 lines. A second generator, `products/catalyst/bootstrap/ui/scripts/build-catalog.mjs`, writes the console's copy and the API's embedded copy.
- The Blueprint controller, `core/controllers/blueprint`, is switched off by default (`enabled: false` in `products/catalyst/chart/values.yaml`).
- The bootstrap kit is one Flux Kustomization over `clusters/_template/bootstrap-kit`, with 68 HelmReleases and `prune: true`. The shared bootstrap template `infra/providers/_shared/cloudinit-control-plane.tftpl` writes that object once, at first boot. It is not in Git.
- No kit slot enables Helm tests, and one chart ships a test hook (`platform/cnpg-pair`). Nothing proves that an installed Blueprint works.
- Crossplane core is version 1.18.0 (`platform/crossplane/chart/Chart.yaml`).
- `docs/GLOSSARY.md` bans "Instance" as a user-facing object. The text "+ New instance" is on 15 lines of five non-test console files, and the API route is `/catalyst/v1/apps/instances`.

So the platform cannot say what a Blueprint is, cannot offer two implementations of one need, and cannot show that a Blueprint works.

## Decision

The decisions are numbered S1 to S12.

**S1 Keep the kinds.** Blueprint stays the definition and Application stays the installed thing. No kind is renamed, split or added for installing. Everything below is added fields, files or labels.

**S2 Class, lifecycle, provides, requires.** `spec.class` has six values: `application`, `data-service`, `network-security-service`, `ai-service`, `infrastructure` and `platform-component`. `spec.lifecycle` has three values: `experimental`, `supported` and `deprecated`. Both fields land in one scripted change over every `blueprint.yaml`, in the same pull request as the code that reads them (work package SVC-170). The same change deletes the five free-text classifiers named in the section "Context"; `spec.card.tags` stays, for search only. `spec.provides` and `spec.requires` land later, with the first capability that has two implementations (work package SVC-260). `spec.requires` is the list of capability bindings of a consumer. No other field is added to the Blueprint by this decision.

**S3 Where the choice of implementation lives.** The choice lives at the Blueprint level: several Blueprints may provide one capability. A Crossplane composite resource definition exists only for an implementation that is a cloud managed service. In-cluster implementations stay Helm charts with nothing from Crossplane in front of them. ADR-0010 stands: in-cluster services use no Crossplane and no binding controller. A cloud managed service is cloud infrastructure and uses Crossplane.

**S4 The binding.** The Context stays the binding. A consumer declares `requires`. The install API resolves each requirement to one implementation, creates or reuses an Application of that implementation, and writes a Context on it. It writes the consumer's side as `bindings.<alias>` in the consumer's `spec.parameters`, which already flow to Helm values. `depends[]` stays, for hard dependencies on a named Blueprint. No field is added to the Application kind, and the Application controller is not changed for capabilities.

**S5 One catalogue source.** `blueprint.yaml` is the only hand-written description of a Blueprint. One generator, `scripts/generate-catalog.py`, writes every catalogue output: the cluster seed (at its present path), the API's embedded catalogue, the console catalogue, the wizard's kit list and the capability index. A pull request that changes a `blueprint.yaml`, a kit slot or a capability file runs the generator and commits its output. A drift check runs `python3 scripts/generate-catalog.py --check` on every pull request, merge group and push to `main`. The release writer runs the generator again after it has written versions; that commit changes version values only. No generated file is edited by hand.

**S6 Capability registry and the choice per Sovereign.** A capability is a contract file in `platform/_capabilities/`. The registry, from a capability to its implementations, is computed from `provides`; it is not stored. The implementations a Sovereign offers for a capability, and its default, are the block `spec.catalog.capabilities[]` on the existing Sovereign kind. There is no separate kind for this choice. The choice is committed to Git under `clusters/<fqdn>/intent/catalog/` of the fleet repository and never written to the cluster directly. The block is built only when a capability has two implementations on a Sovereign (work package SVC-300).

**S7 Connection contracts.** Each capability fixes the keys that every implementation must expose. Key names and the file layout follow the Service Binding Specification for Kubernetes. The keys reach the consumer as files mounted into the running workload from OpenBao. A binding never becomes a Kubernetes Secret object. A provider writes the keys through a write-only identity that the secrets area supplies; each package that adds a provider registers it there. The keys hold no composed `uri`: the consumer builds an address from the parts.

**S8 Crossplane shape and timing.** A capability definition uses the group `compose.openova.io` and the Crossplane 2 form that ADR-0016 fixes (decision K8): namespaced, no claims. Its kind has no `X` prefix. Its Composition is named `<contract>-native` (ADR-0016, decision K10) and carries only the labels `compose.openova.io/contract` and `compose.openova.io/implementation`. It runs the provider pack's OpenTofu unit module `infra/providers/<pack>/day2/<contract>`, and the default Composition is the definition's `defaultCompositionRef`. No chart receives a provider name. No capability definition is written before the move to Crossplane 2 (work package CLD-065).

**S9 First capabilities and their order.** The first capability is `object-storage`. Its in-cluster implementation is a small Blueprint, `bp-object-bucket`, that creates a bucket and a bucket-scoped key on the kit's SeaweedFS; its other implementation is the cloud's native bucket. The second is `postgres`, the in-cluster database and the cloud's managed database. Its work starts only when spike SVC-315 has shown that a managed database can be created, read and deleted through the cloud API. The web application firewall contract and both of its implementations belong to the network area. A `cache` contract is written only if spike SVC-340 shows that a native cache can be offered with rotated credentials. A platform component that needs a bucket gets it through the `object-storage` binding, like any other consumer.

**S10 Naming.** No kind is renamed. Screens group Blueprints by the six classes. "Instance" is allowed again in one narrow sense: in the phrase "instance of" followed by a Blueprint name, and on the action "+ New instance". The installed object is an Application. "Context" stays the word for a binding in code, screens and documents. No banned term is added.

**S11 Proof that travels.** Every chart ships one function test as a Helm test hook. Flux runs it after every install and upgrade. On a gate run, the gate check SVC-G06 reads the result from each release's history and writes it into the gate record (`docs/ledger/acceptance/`), as the field `functionTest` of each entry of `artifacts.charts[]`. The gate record is the one store of proof: there is no per-Blueprint proof file, no proof route and no proof event. A Blueprint may say `supported` only while the newest counting gate record shows its test passed; the generator fails otherwise. A failed test is recorded and does not fail the release for now.

**S12 Blueprint controller and kit rings.** The Blueprint controller is deleted, not enabled (work package SVC-190). Validation runs in CI, at admission and in the API before a commit. The kit gets rings: every kit slot carries the label `catalyst.openova.io/ring` with one of the names `foundation`, `provider`, `observability`, `security-scanning`, `access`, `ai`, `commerce` and `operations`. `foundation` is never switched off, and `provider` is not a switch. A ring is switched off by one delete patch in the kit's Flux Kustomization object, chosen by a Git commit. The slot list, the slot files and the path that existing Sovereigns reconcile do not change. Until the kit's Kustomization object is in the Sovereign's repository, every ring is on.

## What changes in ADR-0010

ADR-0010 stays the record of the shareable backing service and of its rule: in-cluster services use no custom controller and no Crossplane. Two of its points change.

1. Delivery of the keys. Under ADR-0010 the consumer reads its connection keys from a Kubernetes Secret object reflected into its namespace. Under S7 the keys are files mounted from OpenBao, and no Secret object is made for a binding. The existing Secrets of existing consumers stay until the secrets area replaces them.
2. `spec.backingServices`. The field and its generator, `core/controllers/pkg/backingservice/`, are deleted (work package SVC-360). `requires` and the Context take their place (S4).

## What the Unfreezes lines permit

The stability register, `docs/ledger/stability.yaml` (work package FND-040 adds it), lists the topology surface, and work package FND-120 freezes it. A pull request that changes a frozen path must carry the trailer `Decision: ADR-NNNN`, naming an accepted record with an `Unfreezes:` line for the entry it touches. This record is cited for three changes only:

| Entry | Work package | The change |
|---|---|---|
| `topology-continuum` | SVC-110 | A function test for the Continuum chart, `products/continuum/chart/templates/tests/function-test.yaml`. No other template of that chart changes. |
| `application-controller` | SVC-140 | The Application controller sets `spec.test.enable: true` and `spec.test.ignoreFailures: true` on every HelmRelease it renders. |
| `application-controller` | SVC-360 | The Application controller's calls into the deleted package `core/controllers/pkg/backingservice/` are removed, with the two status fields they fill. |

Each of the three waits for the topology freeze (work package FND-120) and carries the trailer `Decision: ADR-0020`.

## Alternatives rejected

- **S1.** A second kind for data, network and AI services, or a rename of Blueprint to "Service". Rejected: two new definitions and a migration of every installed object, for no behaviour that the class field does not give. "Service" also collides with the Kubernetes Service.
- **S2.** The five classes without `platform-component`. Rejected: most kit releases are cluster plumbing, and none of the five fits them. Deriving the class from the `section` label was rejected too: it has 24 values that mix kinds.
- **S3.** One composite definition with one Composition per implementation, the in-cluster one included. Rejected: it breaks the rule of ADR-0010, puts Crossplane in front of the one proven install path, and makes the whole service model wait for the Crossplane upgrade.
- **S4.** A binding controller, the Service Binding runtime, or capability fields on `depends[]`. Rejected: ADR-0010 forbids the first, nothing here needs the second, and `depends[]` would become ambiguous.
- **S6.** A separate kind for the choice per Sovereign. Rejected: the platform's entities are fixed, and a need of one Sovereign is an optional field on an existing kind.
- **S7.** A Secret object per binding. Rejected: the credential of an Application never becomes a Secret object.
- **S10.** Keeping the total ban on "Instance". Rejected: enforcing it would change a route and fifteen lines of console text; narrowing it changes one glossary row.
- **S12.** Enabling the Blueprint controller. Rejected: its checks are covered in CI, at admission and in the API, and its mirror from the cluster to Git is a write path the platform removes. One Flux Kustomization per ring, or removing ring slots from the slot list, was rejected too: Flux would prune those releases on every existing Sovereign.

## Consequences

- A Blueprint says what it is (`class`), how far it is proven (`lifecycle`) and, from SVC-260 on, what it offers and needs (`provides`, `requires`).
- A consumer chart is the same whichever implementation serves it. The swap check SVC-G02 proves this for object storage.
- The catalogue has one hand-written source, and a drift check fails a pull request whose generated files are stale.
- A Blueprint is `supported` only on current proof from a gate record.
- A sovereign-admin can switch off a ring of the kit with a commit once the kit's Kustomization object is in the Sovereign's repository. Until then every ring is on.
- The words of the glossary match the console: "+ New instance" is allowed, and the installed object is still called an Application.

## Shipped via

Work packages of area SVC in `docs/ledger/plan.yaml`. The main ones: SVC-170 (class and lifecycle); SVC-150 and SVC-160 (one generator); SVC-190 (the Blueprint controller is deleted); SVC-220 (ring labels); SVC-360 (the backing-service generator is deleted); and the epics SVC-050 (function tests and the proof check), SVC-240 (object storage with two implementations, the registry and the choice per Sovereign) and SVC-310 (PostgreSQL as a capability).
