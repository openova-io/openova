# ADR-0011 — OpenTofu→Crossplane adoption seam (provider-opentofu Workspace)

**Status:** Accepted
**Date:** 2026-06-21
**Refs:** #4002 (the seam-never-implemented gap), #3687 (IaC two-way spine), #3998 (cloud-view 0-LB symptom). Builds on Inviolable Principle #3 (Crossplane = the only day-2 cloud IaC) and ADR-0002 (cutover provider rotation).

## Context

The documented IaC layering (the founder's foundational principle):

- **OpenTofu = Phase-0 bootstrap ONLY** (`docs/ARCHITECTURE.md` §10). It creates the
  Sovereign's cloud substrate (ELB, ECS/servers, VPC, EIPs, firewall, peering) once.
- **Crossplane ADOPTS** those OpenTofu-created resources at Phase-1 hand-off via
  `crossplane.io/external-name` (the cloud resource ID), then owns **all non-Kubernetes
  day-2 infra** (`platform/crossplane/README.md`, `compositions/README.md` §Adoption pattern).
- **Flux = all Kubernetes-internal** (`docs/ARCHITECTURE.md` §K8s reconciliation).

**The violation (#4002, verified live on hw173 2026-06-21):** Crossplane owned **zero**
cloud resources on every Sovereign. On hw173's mgmt cluster `kubectl get managed`
returned `the server doesn't have a resource type "managed"` and `kubectl get
providers.pkg.crossplane.io` returned `No resources found` — Crossplane core was Running
but **no provider was installed** and **nothing ever handed it the OpenTofu state**. The
day-2 IaC layer was inert: the platform could not add/modify a cluster, node pool,
firewall, region, load balancer or any non-K8s resource through its own path.

Three concrete breaks:

1. **No adoption-claim manifests.** `compositions/README.md` pointed at
   `clusters/<fqdn>/infrastructure/adoption-claims.yaml`; a repo-wide search returned
   nothing — no file, no template, no generator.
2. **No producer.** `products/catalyst/bootstrap/api/internal/provisioner/provisioner.go`
   only *commented* `// Crossplane adoption (Phase 1 hand-off)`; no code wrote an XR with
   `crossplane.io/external-name` from the tofu outputs.
3. **Hetzner-only compositions.** The `compose.openova.io` XRDs/Compositions targeted
   `provider-hcloud` native MRs. hw173 is **Huawei** — there was no Huawei provider and no
   Huawei composition, so adoption was impossible on the live env.

## Decision

**Adopt the existing OpenTofu state into Crossplane via `provider-opentofu`'s `Workspace`
managed resource, observe-first, one mechanism for every cloud.** This is the cross-cloud
adoption seam; the native `compose.openova.io` Hetzner XRDs remain for native day-2 CRUD.

### Why provider-opentofu (Option B) over native upjet providers (Option A)

Two options were evaluated against the requirement: *Crossplane owns day-2 on BOTH Hetzner
and Huawei, adopts the existing OpenTofu infra without re-provisioning, maintainable.*

**Option A — native upjet providers (`provider-hcloud` + `provider-huaweicloud`).** Adopt
each cloud resource as a native MR by external-name. Matches the existing Hetzner
compositions. **Rejected for the Huawei half** because:

- `provider-huaweicloud` is at **v0.0.10** (brand-new, ~205 generated CRDs, no maturity
  track record).
- More fatally: the Huawei substrate is **on-prem Huawei Cloud Stack (HCS)** reached at
  custom per-service endpoints (`https://<svc>.me-east-215.kom4dc.nationalcloud.om`) with
  `insecure = true` (the on-prem CA is not in the public trust store). The native provider's
  `ProviderConfig` exposes no evidence it supports the full per-service `endpoints{}`
  override + insecure-TLS shape that `infra/providers/huawei/versions.tf` already encodes.
  A native MR provider that cannot reach the HCS endpoints cannot adopt anything.
- It would also duplicate, in two diverging schemas (HCL provider + Crossplane provider),
  the same resource model the tofu module already maintains correctly.

**Option B — `provider-opentofu` Workspace (CHOSEN).** A `Workspace` MR wraps an OpenTofu
module. We point it at the **already-built, already-endpoint-configured**
`infra/providers/{hetzner,huawei}` modules, which already solve the HCS custom-endpoint +
insecure quirks. Decisive advantages:

- **One mechanism for every cloud.** The same Workspace machinery adopts Hetzner and Huawei
  (and any future provider) with no per-cloud Crossplane provider to vet.
- **Reuses the maintained modules.** No second source of truth; the tofu provider config we
  already debugged (KMS/NAT/ELB endpoint overrides, `insecure`, S3-protocol OBS) is reused
  verbatim.
- **OpenTofu-license-clean.** `provider-opentofu` (upbound, v1.1.x) tracks the OpenTofu fork
  — unlike `provider-terraform`, which is frozen at Terraform 1.5.7 under the BSL license.
  Catalyst is an OpenTofu shop (`required_version >= 1.6.0`), so this is the native fit.
- **Observe-safe by construction.** With `managementPolicies: [Observe]`, Crossplane reads
  the workspace/data-source state and **never** runs a mutating apply — the live ELB/nodes
  are never re-provisioned or deleted.
- **Real outputs surface to status.** Non-sensitive Workspace outputs land in
  `status.atProvider.outputs`, so the adopted resource IDs (the real ELB id, EIPs, server
  ids, VPC id) become readable Kubernetes object fields — which is exactly what the console
  cloud-view needs (the #3998 root-fix, not a view band-aid).

### Adoption shape

Two layers, both shipped here:

1. **Native `compose.openova.io` XRDs/Compositions** (Hetzner, pre-existing) stay as the
   day-2 **CRUD** path for native Hetzner resources.
2. **`XCloudAdoption` / `CloudAdoption`** (new, `compose.openova.io/v1alpha1`) — a generic,
   cloud-agnostic adoption composite backed by a provider-opentofu `Workspace`. Its
   composition renders an **Observe-only** Workspace whose inline module runs a `data`
   lookup keyed by the real cloud resource id (carried in
   `crossplane.io/external-name`), surfacing the resource's live attributes as outputs. This
   binds Crossplane to the real OpenTofu-created resource **without** owning a second copy of
   the apply graph — the truest "observe the resource by its id" adoption.

### The producer (the missing seam)

The provisioner gains a post-`tofu apply` **adoption-claim generator**
(`internal/provisioner/adoption.go`): it reads the tofu **state/outputs**, extracts each
resource's cloud id + key attributes, and renders
`clusters/<fqdn>/infrastructure/adoption-claims.yaml` — one `CloudAdoption` per
{loadbalancer, control-plane node, network/VPC} (extensible to firewall/peering/region),
each annotated `crossplane.io/external-name=<cloud id>` and `managementPolicies: [Observe]`.
Flux reconciles the cluster's `infrastructure/` directory, so the claims apply at Phase-1
hand-off with no extra control-plane process. The generator is wired but produces the file
deterministically from state, so it is also runnable standalone for backfilling an
already-provisioned Sovereign (used for the hw173 live proof).

## Consequences

- **Principle #3 restored.** Crossplane now observes/owns real cloud resources on a Sovereign;
  `kubectl get managed` is no longer empty.
- **Observe-first is the default; full-manage is an explicit, later flip.** Adoption never
  mutates the live substrate. Promoting a `CloudAdoption` to `[Observe, Create, Update,
  Delete]` is a deliberate per-resource action taken only after the external-name binding is
  confirmed `Synced=True`.
- **Cross-cloud uniformity.** Adding a new cloud is "write the tofu module + reuse the
  Workspace adoption composition", not "vet and pin a new Crossplane provider".
- **Native Hetzner CRUD unaffected.** The existing `compose.openova.io` Hetzner XRDs and
  `provider-hcloud` install are untouched; adoption is additive.
- **Trade-off accepted:** a Workspace-backed adoption observes via OpenTofu data-sources
  rather than as a first-class native MR, so per-field drift detection is coarser than a
  native provider would give. For the day-2 contract (own the resource, read its live state,
  be able to mutate it through one IaC path) this is the correct altitude; native MRs can be
  layered in later for any cloud whose Crossplane provider matures, without changing the seam.

## Shipped-via

- `docs/adr/0011-opentofu-crossplane-adoption-seam.md` (this ADR)
- `clusters/_template/infrastructure/provider-opentofu.yaml` — Provider + DeploymentRuntimeConfig
- `clusters/_template/infrastructure/provider-config-opentofu.yaml` — ProviderConfig (per-cloud creds wiring)
- `platform/crossplane-claims/chart/templates/{xrds,compositions}/cloudadoption.yaml` — the
  generic `XCloudAdoption` composite + Observe-only Workspace composition
- `products/catalyst/bootstrap/api/internal/provisioner/adoption.go` — the adoption-claim
  generator (tofu state → `adoption-claims.yaml`)
- `clusters/_template/infrastructure/adoption-claims.yaml` — the bootstrap claim manifest the
  README promised (envsubst placeholders, generator-overwritten per Sovereign)
