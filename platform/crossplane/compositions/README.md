# Catalyst Crossplane Compositions — canonical Hetzner XRDs

**XRD API group:** `compose.openova.io/v1alpha1`
(per `docs/BLUEPRINT-AUTHORING.md` §8 + `VALIDATION-LOG.md` Pass 42/48; **never** `catalyst.openova.io` — that is the Catalyst CRD group, not the Crossplane composite group.)

This directory contains the four canonical Hetzner-backed XRDs + their default Compositions that Catalyst uses to manage day-2 cloud infrastructure on a franchised Sovereign. After Phase 0 (`infra/hetzner/main.tf`) hands off to Phase 1, **all** further Hetzner resources — additional regions, attached volumes, additional firewalls, additional load balancers — go through these XRDs and are reconciled by Crossplane.

Per `docs/INVIOLABLE-PRINCIPLES.md` principle #3:

> Crossplane is the ONLY IaC after Phase 1 hand-off. Not direct provider SDKs. Not Terraform. Not the catalyst-api Go service calling cloud APIs.

## XRDs in this directory

| XRD | Wraps |
|---|---|
| `XHetznerNetwork` | `hcloud_network` + `hcloud_network_subnet` (provider-hcloud `Network` + `NetworkSubnet`) |
| `XHetznerFirewall` | `hcloud_firewall` (provider-hcloud `Firewall`) |
| `XHetznerServer` | `hcloud_server` (provider-hcloud `Server`) |
| `XHetznerLoadBalancer` | `hcloud_load_balancer` + targets + services (provider-hcloud `LoadBalancer` + `LoadBalancerTarget` + `LoadBalancerService`) |

Each `xrd-*.yaml` declares the OpenAPIv3 schema; each matching `composition-*.yaml` references the upstream `provider-hcloud` managed resources.

## Why these four

These mirror the four resource families OpenTofu provisions in `infra/hetzner/main.tf` Phase 0. After Phase 1 hand-off, Crossplane **adopts** the OpenTofu-created resources by `external-name` (the Hetzner numeric resource ID), and any further changes — adding a worker, opening a port, adding a region — are made by submitting an XR (claim) of the appropriate type instead of editing OpenTofu state.

## Provider configuration

The provider itself (`provider-hcloud`) and its `ProviderConfig` are installed by `platform/crossplane/chart/templates/provider-hcloud.yaml`, which is reconciled by Flux from the cluster directory. The Hetzner API token is mounted from a K8s `Secret` named `hcloud-credentials` in the `crossplane-system` namespace — that secret is created by the OpenTofu module's hand-off step.

## Adoption pattern

When OpenTofu creates a resource in Phase 0, the resource gets a label like:

```
catalyst.openova.io/sovereign: omantel.omani.works
catalyst.openova.io/role: control-plane
```

Phase 1 ingests these into Crossplane by creating an XR with `metadata.annotations[crossplane.io/external-name]` set to the Hetzner numeric ID. Crossplane then takes over the lifecycle — `kubectl delete xhetznerserver/cp1` after Phase 1 will deprovision the underlying Hetzner server, just like `tofu destroy` would have done in Phase 0. (See `clusters/<sovereign-fqdn>/infrastructure/adoption-claims.yaml` for the bootstrap claim manifests.)

## Authoring conventions

- Every XRD's `group` is `compose.openova.io` and `versions[0].name` is `v1alpha1`.
- Every XR's plural is `<kind-lowercase>s` (e.g. `xhetznerservers`).
- Every XRD declares a `claimNames` block so users can submit namespaced claims (`HetznerServer`) instead of cluster-scoped XRs (`XHetznerServer`).
- `defaultCompositionRef` points at the matching `composition-*.yaml` shipped here.
- Per principle #4 (no hardcoding): every cloud-specific value (region, server type, image) is a schema field, never a constant in the Composition.

## Adding a new XRD

1. Drop `xrd-<resource>.yaml` and `composition-<resource>.yaml` in this directory.
2. Reference the matching upstream provider-hcloud kind under `spec.resources[].base`.
3. Add the file to `kustomization.yaml`.
4. Bump `Chart.yaml` version of `bp-crossplane`.

The CI (`.github/workflows/blueprint-release.yaml`) re-publishes `bp-crossplane` to GHCR on the next push, and Flux reconciles the new XRDs into every Sovereign on its next pull.
