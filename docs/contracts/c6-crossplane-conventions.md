# C6 — Crossplane 2 conventions (infrastructure part)

Contract C6 fixes how infrastructure is managed through Crossplane on a
Sovereign. Area CLD (cloud abstraction) owns this file. Area SVC (service
model) writes the service part of C6 to the same rules. The decision record
is [ADR-0016](../adr/0016-cloud-abstraction-contract.md), decisions K7 to K10
and K16. Contract C2, the cloud profile, is the pair
`infra/providers/_contract/cloudprofile.schema.json` and
`infra/providers/_contract/pack.schema.json`. They stay under
`infra/providers/` because the API image and the modules read them there.

This file states the target. The work packages named below build it. Until
they have merged, the platform does not do what a line says.

## Versions

- Crossplane 2, the highest 2.4 patch (2.4.3 on 2026-11-12).
- `provider-opentofu` v1.1.11.
- Function `function-patch-and-transform`, the highest release on the day
  CLD-065 runs, recorded in the slot file. No other function is used for
  infrastructure.

## Two classes of Workspace

OpenTofu provisions. At hand-over Crossplane takes ownership by running the
same OpenTofu modules inside the Sovereign as Workspaces. Every Workspace
carries the label `compose.openova.io/workspace-class` with the value `base`
or `unit`.

| | Base Workspace | Unit Workspace |
|---|---|---|
| Module | `infra/providers/<provider>`, the bootstrap module, unchanged | `infra/providers/<provider>/day2/<unit>` |
| Holds | Network, control-plane nodes, front doors, addresses, the bootstrap bucket, the bootstrap worker pool | One thing that changes day to day: an additional node pool first; a firewall rule set, an extra address or a bucket when each has a reader |
| How many | One per Sovereign | One per resource |
| Created by | The hand-over (area SOV), by moving the bootstrap state, never by a first apply | A Composition; the day-2 route of area SOV, from a pack template; the migration controller, in namespace `vm-migration-system` only; the provisioning engine, for a pack's `bootstrapUnits` |
| Starts as | `managementPolicies: ["Observe"]` | Managed |
| Promotion to managed | A Git commit, allowed only when the zero-change plan check of area SOV has a green record for this Sovereign and commit: `tofu plan -detailed-exitcode` exits 0 | Not applicable |
| When it may apply | Only while a maintenance window is declared (the object and its admission rule belong to area SOV) | Any time |
| Name | `sovereign-base` in namespace `catalyst-infra` | `<resource name>` in the namespace of its composite resource |
| Where its manifest lives in Git | Area SOV decides | The folder of the region whose cluster owns the resource. It carries no region field. Contract C4 names the path |

## Definitions

- `apiextensions.crossplane.io/v2` `CompositeResourceDefinition`, group
  `compose.openova.io`, `scope: Namespaced`, no claim names.
- A kind name carries no `X` prefix and no provider name.
- Definitions are shipped by the shared chart `bp-crossplane-claims`.
- A definition merges only in the pull request that also carries two
  Compositions for it.

## Namespaces

- Sovereign-level infrastructure resources live in namespace `catalyst-infra`.
- Organization-level resources live in the Organization's namespace.

## Compositions and selection

- `mode: Pipeline`, one step, `function-patch-and-transform`.
- A Composition is shipped only by a provider overlay, in
  `clusters/_template/providers/<name>/compositions/`.
- Its name is `<contract>-<implementation>`, for example `nodepool-native`.
  It never names a provider.
- Labels: `compose.openova.io/contract` and `compose.openova.io/implementation`.
- A Sovereign runs one provider (ADR-0016, K16). Only the installed pack's
  Compositions exist on it, so nothing selects by provider and no chart needs
  the provider name.
- A resource may set `spec.crossplane.compositionSelector.matchLabels` with
  `compose.openova.io/implementation`. Otherwise the definition's
  `spec.defaultCompositionRef` applies. The shared chart renders it from the
  value `defaults.<contract>`, default `<contract>-native` for an
  infrastructure contract.

## ProviderConfig

- A `ClusterProviderConfig` named `cloud` in group `opentofu.m.upbound.io`
  holds the state back end and the reference to the Sovereign's cloud
  credential.
- The credential is a mounted file: source `Filesystem`, path
  `/var/run/secrets/openova/cloud-credential`, delivered into the provider pod
  by the injection of area SEC. The provider pod does not read the Secret
  `cloud-credentials` through `envFrom`.
- The existing cluster-scoped `ProviderConfig` named `default` stays for
  `XCloudAdoption` only and is deleted with it.

## Workspace object

- Kind `Workspace` in `opentofu.m.upbound.io`.
- `spec.forProvider.source: Remote`.
- `spec.forProvider.module: git::${GITOPS_REPO_URL}//<module path>?ref=${GITOPS_REVISION}`.
  `GITOPS_REPO_URL` is a key of the Sovereign's variables document (contract
  C5) and names the repository `openova/provider-pack` in the region's own Git
  server. `GITOPS_REVISION` is the tag `v<release>` in the ConfigMap
  `flux-system/sovereign-release`. That repository is filled from the pack
  artifact by the closure copy of area SOV (SOV-230), and by nothing else.
  The first package that commits a remote module (CLD-174) depends on SOV-230.
- `spec.providerConfigRef: {kind: ClusterProviderConfig, name: cloud}`.
- External name: `<namespace>-<resource name>`; `sovereign-base` for the base.
- `writeConnectionSecretToRef` is never set.

## Admission rule on the module address

- Outside namespace `catalyst-infra`, a Workspace is admitted only when
  `spec.forProvider.source` is `Remote` and its module address starts with
  `git::${GITOPS_REPO_URL}//infra/providers/` and contains `/day2/` (CLD-177).
- Inside `catalyst-infra` two more forms are admitted: the base module (name
  `sovereign-base`) and the inline canary below.

## Canary

One permanent unit Workspace, `catalyst-infra/state-canary`, is part of the
shared infrastructure base. Its module is inline and holds one
`terraform_data` resource, so it costs nothing in the cloud. It gives gate
check CLD-G05 a state object to read on every Sovereign before any real unit
exists.

## Inputs

- Values from the composite resource go in `spec.forProvider.vars`.
- Sovereign facts come from `spec.forProvider.varFiles` with source
  `ConfigMapKey`: ConfigMap `catalyst-infra/infrastructure-facts`, key
  `facts.auto.tfvars.json`. It holds the JSON of the bootstrap output
  `day2_facts` (CLD-173) plus `sovereign_fqdn`, `deployment_id` and
  `name_prefix`.
- Until the base Workspace exists, that ConfigMap is applied by the same
  post-hand-over step that applies adoption claims today. After that the
  hand-over writes it.

## State

- Back end `s3`. Bucket, endpoint and region are the Flux substitutes
  `OBJECT_STORAGE_BUCKET`, `OBJECT_STORAGE_ENDPOINT` and
  `OBJECT_STORAGE_REGION` (added by CLD-171).
- Key `terraform.tfstate` with `workspace_key_prefix = "tofu-state"`, so each
  Workspace's object is `tofu-state/<external name>/terraform.tfstate`.
- No state in a Kubernetes Secret, and none on the provider pod's own disk:
  that disk is lost at every restart.

## State protection

- The provider pod carries the environment variable `TF_ENCRYPTION`, set once
  in its `DeploymentRuntimeConfig`, so no Workspace can opt out.
- It enforces encryption of state and plan with the OpenBao key provider and
  the transit key `tofu-state` (path `transit/keys/tofu-state`), the spelling
  of [ADR-0018](../adr/0018-secrets-and-trust.md), decision S21.
- The pod reaches the key with a token of the OpenBao role `tofu-state`,
  minted again every 15 minutes into the Secret
  `crossplane-system/tofu-state-token`, key `token`, which the pod reads as a
  file (SEC-190). There is no long-lived token.

## Offline key

- The second key is an offline backup of the same transit key, taken with
  OpenBao's transit key backup, wrapped and handed to the sovereign-admin.
  Area SEC owns the backup and its custody.
- A restore drill proves it, as a gate check of area SOV: restore the key into
  a scratch OpenBao, point `TF_ENCRYPTION` at it, run `tofu state pull` for
  the base Workspace, and compare the resource count with the live one.
- This is the one offline key. There is no passphrase fallback.

## Zero-change plan before any managed change

A Workspace that takes over existing resources is seeded by moving or
importing state, and it is observed first. It becomes managed only after a
plan with zero changes. A first `apply` of a module that declares less than
what exists can delete what it does not declare; this rule makes that
impossible.

## Errors and polling

- A healthy Workspace is planned again at the poll interval, 10 minutes.
- A Workspace in error is retried on the controller's back-off, which is
  faster than the poll interval. So a unit module fails in variable
  validation, before it calls the cloud, whenever the fault is in its inputs.
- A gate check treats a Workspace that is not `Synced=True` for 30 minutes as
  a failure.

## Provider binaries

- The provider pod and the provisioning engine read an OpenTofu CLI
  configuration that installs providers from the mirror at
  `${REGISTRY_HOST}/${REGISTRY_PATH_TOFU_PROVIDERS}`, and from nowhere else.
- Both keys are contract C5 keys. Manifests write the two-key form, never one
  combined key.
- The mirror is filled from each pack's `spec.tofuProviders` (CLD-172).

## Packages

- `Provider.spec.package` is
  `${REGISTRY_HOST}/${REGISTRY_PATH_XPKG}/upbound/provider-opentofu@sha256:<digest>`.
- SOV-150 turns the package addresses into this form, together with every
  other source of the kit. Packages of area CLD that merge before SOV-150
  leave the upstream address as it is.
- A CI check fails when the pinned digest is absent from the parent registry.

## Unit module contract

- Each `day2/<unit>` module declares the variables its contract maps, the
  facts variables it needs, the provider installation variables, and the
  outputs its contract names.
- It declares no sensitive output and never declares a back end.
- It has a `tests/` directory with at least one `tofu test` plan run that uses
  mocks.
- A unit behind a capability contract of area SVC (`day2/objectbucket`,
  `day2/postgresinstance`) follows the same rules and one more: it writes
  exactly the contract keys to the OpenBao path it receives as
  `binding_path`, with the write-only producer identity of area SEC, and to
  nowhere else.

## Bootstrap units

- A unit named in `pack.yaml` under `spec.bootstrapUnits` is applied by the
  provisioning engine right after the bootstrap module, with its own state,
  key `tofu-state/<unit>/terraform.tfstate`.
- The ownership pivot of area SOV moves that state with the base state and
  commits the unit's Workspace as managed.
- The first one is `provisioner-access` (CLD-176): the rule that lets the
  provisioner's address range reach the API port. The hand-over empties its
  input, so the rule goes without a maintenance window on the base.

## Single writer

- A cloud attribute has one writer.
- The bootstrap worker pool belongs to the base Workspace. A `NodePool`
  resource is an additional pool and never touches the bootstrap pool.
- Load-balancer membership of the bootstrap nodes is declared by the
  bootstrap module. Membership of nodes that join later is written by the
  cloud controller (CLD-152) and by nothing else.

## Known behaviours of operator-hosted Huawei Cloud Stack installations

The Huawei unit modules respect these. Each is a test case of CLD-174.

1. An instance on a subnet whose outbound traffic goes through a NAT gateway
   cannot be given a public address directly. Unit nodes get no public
   address; they are reached through the front doors.
2. Traffic that enters through a destination-NAT rule arrives with a rewritten
   source address. A security-group rule that allows one source host does not
   match it. Unit nodes use the security groups in the facts and add no
   single-host rule.
3. Some machine images ship a host firewall that drops all inbound traffic.
   Unit nodes use exactly the image in the facts (`image_ref`), the one the
   bootstrap module uses. `NodePool` has no image field.
4. The native Crossplane provider for this cloud has reported a conflict
   between two versions of the security-group rule API on such installations.
   This is one more reason the Huawei implementation is the wrapped module.
