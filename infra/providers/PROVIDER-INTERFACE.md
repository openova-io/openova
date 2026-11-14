# OpenTofu Provider Interface — `infra/providers/<name>/`

**Status**: canon, introduced in the catalyst-api providers/ refactor
(Wave 2, Issue #1841 — DoD A6 multi-cloud canonical case).

Every cloud-provider OpenTofu module that catalyst-api drives MUST
satisfy the variable + output contract below. catalyst-api's
`internal/providers/<name>` Go adapter consumes the outputs and feeds
the variables; new providers are added by dropping a new module into
`infra/providers/<name>/` + an adapter package — no changes to the
catalyst-api handler call sites are needed.

This is the analogue of the Go-side `providers.CloudProvider` interface
at `products/catalyst/bootstrap/api/internal/providers/provider.go`. The
two contracts MUST stay in lockstep — bump them together.

## Required variables (every module MUST accept)

| Variable                          | Type            | Source              | Notes |
|-----------------------------------|-----------------|---------------------|-------|
| `deployment_id`                   | `string`        | catalyst-api        | Opaque 16-hex id; stamped as a label on every cloud resource so the orphan-purge sweep can scope its delete by deployment. |
| `sovereign_fqdn`                  | `string`        | catalyst-api        | FQDN of the Sovereign (`omantel.omani.works`). Used by cloud-init for DNS, LE cert issuance, kube-vip seed. |
| `regions`                         | `list(object)`  | catalyst-api        | `[{ code, role, control_plane_size, worker_size, worker_count }]`. `role` is `primary` or `secondary`; the primary region runs the control plane + the first CNPG cluster. |
| `parent_domains_yaml`             | `string` (YAML) | catalyst-api        | Per-domain {name, role, registrar} payload. Module decodes via `yamldecode(var.parent_domains_yaml)`. Empty → single primary from `sovereign_fqdn`. |
| `gitops_repo_url`                 | `string`        | catalyst-api env    | URL Flux on the new cluster watches. |
| `gitops_branch`                   | `string`        | catalyst-api env    | Default `main`. |
| `handover_jwt_public_key`         | `string` (JWK)  | catalyst-api        | RFC 7517 JWK JSON. Wired into cloud-init so the new Sovereign's `/auth/handover` endpoint can validate the bearer JWT. |

Per-provider additional variables (credentials, project IDs, region-
specific knobs) live alongside these and are documented in each
module's own `variables.tf`.

## Required outputs (every module MUST emit)

| Output                | Type     | Notes |
|-----------------------|----------|-------|
| `control_plane_ip`    | `string` | Public IPv4 of the primary-region control-plane node. Wizard's success-screen "Open kubeconfig" button + the Phase-1 watcher both depend on this. |
| `load_balancer_ip`    | `string` | Public IPv4 of the primary-region L4 LB the Cilium Gateway listens on. Cloud-init plants the corresponding HTTPRoute against this IP. |
| `console_url`         | `string` | Fully-qualified HTTPS URL the wizard hands to the operator on Phase-1 completion (`https://console.<sovereign_fqdn>/`). |
| `gitops_repo_url_out` | `string` | Echo of the input (the wizard renders it on the deployment success screen). |
| `kubeconfig_path`     | `string` | Empty at apply-time (cloud-init PUTs the file back via the bearer endpoint after k3s starts); declared here so the provisioner can read whatever the module sets if a future provider mints the kubeconfig server-side. |
| `s3_buckets`          | `list(string)` | Names of object-storage buckets the module created. Drives the wipe handler's per-deployment bucket purge. Empty when the provider has no object storage. |
| `region_primary`      | `string` | Code of the primary region (echo of `regions[0].code`). |
| `region_secondaries`  | `list(string)` | Codes of secondary regions in declared order. |

## Label conventions (every cloud-provider resource MUST carry)

| Label key                                  | Value                          |
|--------------------------------------------|--------------------------------|
| `catalyst.openova.io/sovereign`            | `var.sovereign_fqdn`           |
| `catalyst.openova.io/deployment-id`        | `var.deployment_id`            |
| `catalyst.openova.io/region`               | one of `var.regions[*].code`   |
| `catalyst.openova.io/managed-by`           | `catalyst-zero`                |

These labels are the key the wipe handler's orphan-purge sweep uses.
Per provider, the label-syntax differs (Hetzner: `label_selector` on
the Cloud API; Huawei: TMS tags; AWS: ResourceTagMappings) — the
adapter package is responsible for translating, but the *key shape*
is universal.

## Cloud-init contract

The cloud-init template (`cloudinit-control-plane.tftpl` +
`cloudinit-worker.tftpl`) is provider-specific (different metadata
service, different cloud-credentials secret shape, different CSI
driver) but every cloud-init MUST plant the same set of files on the
new node:

| Path                                              | Contents |
|---------------------------------------------------|----------|
| `/etc/rancher/k3s/config.yaml`                    | k3s server/agent config with the cluster name, token, primary-region LB IP. |
| `/etc/rancher/k3s/registries.yaml`                | Pre-pull credentials for `ghcr.io` (via `var.ghcr_pull_token`). |
| `/etc/catalyst/handover-jwt-public.jwk`           | The JWK from `var.handover_jwt_public_key`. |
| `/etc/catalyst/sovereign.env`                     | `SOVEREIGN_FQDN=...`, `MARKETPLACE_ENABLED=...`, `QA_FIXTURES_ENABLED=...`, etc. — consumed by the Flux Kustomization `postBuild.substitute`. |
| `/etc/catalyst/bootstrap-kit-cluster.yaml`        | The Flux source pointing at `<gitops_repo_url>@<gitops_branch>` so Flux can pull the bootstrap kit. |

Differences between providers (Hetzner uses k3s + hcloud-ccm; future
Huawei impl may use k3s + Huawei CCM; future AWS impl may swap to EKS
+ AWS LB controller) are absorbed inside each cloud-init template.

## Module-local tests

Each `infra/providers/<name>/` module MUST ship:

- `tests/` directory with `.tftest.hcl` files that exercise the full
  `tofu apply` against fake/local providers (no real cloud calls).
- `tofu fmt -check -recursive` cleanliness.
- `tofu validate` against an empty backend.

CI workflows under `.github/workflows/infra-*-tofu.yaml` enforce all
three on every PR that touches the module.

## Naming + path convention

- Module path: `infra/providers/<name>/` (e.g. `infra/providers/hetzner/`,
  `infra/providers/huawei/`).
- Adapter path: `products/catalyst/bootstrap/api/internal/providers/<name>/`.
- CI workflow path: `.github/workflows/infra-<name>-tofu.yaml`.
- Container default: `CATALYST_TOFU_MODULE_PATH=/infra/providers/<name>` (set per-deployment from `deployment.Provider`).

## Adding a new provider — checklist

1. Drop the OpenTofu module at `infra/providers/<name>/` with the
   variable + output contract above.
2. Add the Go adapter at
   `products/catalyst/bootstrap/api/internal/providers/<name>/provider.go`
   implementing `providers.CloudProvider`. Register in its `init()`.
3. Add `_ "…/providers/<name>"` to `providers/all/all.go`.
4. Add `infra/providers/<name>/**` to `.github/workflows/catalyst-build.yaml`
   path triggers.
5. Add `.github/workflows/infra-<name>-tofu.yaml` (copy from Hetzner).
6. Update the provider enum in
   `core/controllers/environment/api/v1/types.go` if not already
   present.
7. Add per-provider credential documentation in the adapter's package
   doc-comment.
