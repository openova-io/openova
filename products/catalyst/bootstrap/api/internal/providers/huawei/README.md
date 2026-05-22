# Huawei Cloud provider — STUB (Wave 2)

**Status**: STUB ONLY. Every `providers.CloudProvider` method returns
`errors.New("huawei provider: not implemented yet — Wave 3 (Issue #1841 — DoD A6 provider-mix)")`.

**Tracking**: [Issue #1841](https://github.com/openova-io/openova/issues/1841) — DoD A6 — Provider-mix is the canonical case (1 region Hetzner, 1 AWS, 1 Huawei).

## Why a stub now (Wave 2)?

The catalyst-api `providers.CloudProvider` interface and registry
landed in Wave 2 (this refactor). With the seam in place, future
handlers can call `providers.Get(deployment.Provider)` to dispatch
without `if dep.Provider == "hetzner" { ... } else { ... }` branches.
The Huawei stub means:

- `providers.Get("huawei")` returns a real, non-nil `CloudProvider`.
- The platform code that dispatches by provider name compiles + runs
  end-to-end. A huawei-typed deployment fails LOUDLY with a clear
  error message that points to the tracking issue — rather than
  silently going down the Hetzner code path.
- Wave 3's job becomes "fill in the four methods" against a fixed,
  well-typed API surface — no interface-shape negotiations needed.

## Planned Wave 3 implementation

- **Credentials shape** (`providers.ProviderCreds.Raw` keys):
  - `ak` — Huawei IAM access key
  - `sk` — Huawei IAM secret key
  - `project_id` — Huawei project ID (region-scoped)
  - `region` — Huawei region code (e.g. `cn-north-4`)

- **OpenTofu module**: drop at `infra/providers/huawei/` honouring
  `infra/providers/PROVIDER-INTERFACE.md`. Use the
  [huaweicloud/huaweicloud](https://registry.terraform.io/providers/huaweicloud/huaweicloud) Terraform provider (compatible with OpenTofu 1.6+).

- **Cloud-init**: derive from
  `infra/providers/hetzner/cloudinit-control-plane.tftpl` — replace
  hcloud-ccm with Huawei CCE / CCM + Huawei EVS CSI driver. Keep all
  Catalyst-zero config-file paths identical (see canon
  `infra/providers/PROVIDER-INTERFACE.md` §Cloud-init contract).

- **Orphan-purge** (analogue of `internal/hetzner/purge.go`): query
  Huawei TMS (Tag Management Service) for resources with
  `catalyst.openova.io/sovereign=<fqdn>` and delete in dependency order
  (ELB → ECS → SecurityGroup → VPC).

- **Object-storage**: Huawei OBS supports the S3 protocol; reuse the
  minio-go client at `internal/hetzner/buckets.go` with the OBS S3
  endpoint. Or use the native Huawei OBS SDK if more idiomatic.

- **Credentials validation**: issue an IAM token-mint against the
  project to confirm AK/SK pair.

## Why the package exists in Wave 2 (rather than Wave 3 inline)

Per `docs/PRINCIPLES.md` #14 (target-state shape, no workarounds): if
the platform is to be multi-cloud, the abstraction-with-stubs IS the
target-state shape. Punting the Huawei stub to Wave 3 would mean Wave
2 lands a single-impl interface that doesn't prove the interface is
actually polymorphic — defeating the point of the refactor.
