# Hetzner provider — OpenTofu interface notes

This file documents how the Hetzner OpenTofu module satisfies
`infra/providers/PROVIDER-INTERFACE.md` and where the per-provider
variables / outputs / labels diverge from the canonical contract.

## Canonical contract — implementation map

| Contract variable           | Hetzner module variable / source             |
|-----------------------------|----------------------------------------------|
| `deployment_id`             | `var.deployment_id`                          |
| `sovereign_fqdn`            | `var.sovereign_fqdn`                         |
| `regions`                   | `var.regions` (list(object))                 |
| `parent_domains_yaml`       | `var.parent_domains_yaml`                    |
| `gitops_repo_url`           | `var.gitops_repo_url`                        |
| `gitops_branch`             | `var.gitops_branch`                          |
| `handover_jwt_public_key`   | `var.handover_jwt_public_key`                |

## Hetzner-specific variables (NOT in the canonical contract)

| Variable                  | Why Hetzner-specific |
|---------------------------|----------------------|
| `hcloud_token`            | Hetzner Cloud API token. Future Huawei impl uses `huawei_ak` + `huawei_sk` instead. |
| `hcloud_project_id`       | Hetzner Cloud project ID. Replaced by Huawei `project_id` (different shape). |
| `object_storage_access_key`, `object_storage_secret_key`, `object_storage_region`, `object_storage_bucket_name` | Hetzner Object Storage uses S3-compatible API credentials. Future providers may use a native blob-storage service. |
| `ghcr_pull_token`, `harbor_robot_token` | Same shape on every provider — wired through cloud-init unchanged. |
| `cluster_mesh_name`, `cluster_mesh_id` | Cilium ClusterMesh anchors — provider-agnostic but currently single-source-of-truth is `docs/CLUSTERMESH-CLUSTER-IDS.md`. |

## Canonical outputs — implementation

| Contract output       | Hetzner output         |
|-----------------------|------------------------|
| `control_plane_ip`    | `output.control_plane_ip`  (`hcloud_server.cp[0].ipv4_address`) |
| `load_balancer_ip`    | `output.load_balancer_ip`  (`hcloud_load_balancer.main.ipv4`) |
| `console_url`         | `output.console_url`       (`"https://console.${var.sovereign_fqdn}"`) |
| `gitops_repo_url_out` | `output.gitops_repo_url`   (echo of `var.gitops_repo_url`) |
| `kubeconfig_path`     | Empty (cloud-init PUTs file back via bearer endpoint) |
| `s3_buckets`          | `output.s3_buckets` — list with the `var.object_storage_bucket_name` entry |
| `region_primary`      | `output.region_primary`    (`var.regions[0].code`) |
| `region_secondaries`  | `output.region_secondaries` |

## Hetzner-specific label translation

Hetzner Cloud uses native resource `labels = { ... }` blocks on every
hcloud resource. The canonical contract labels map 1:1 to keys:

```hcl
labels = {
  "catalyst.openova.io/sovereign"     = var.sovereign_fqdn
  "catalyst.openova.io/deployment-id" = var.deployment_id
  "catalyst.openova.io/region"        = each.value.code
  "catalyst.openova.io/managed-by"    = "catalyst-zero"
}
```

The orphan-purge sweep in `products/catalyst/bootstrap/api/internal/hetzner/purge.go`
uses the Hetzner Cloud API `label_selector` query parameter
(`catalyst.openova.io/sovereign=<fqdn>`) to scope its sweep. Future
providers translate this label key into their own tag-system call
(Huawei TMS, AWS RGT, etc.).

## Cloud-init divergence

Hetzner's cloud-init bakes in:

- k3s 1.31 (latest stable as of 2026-05) with `--disable=servicelb,traefik`.
- `hcloud-cloud-controller-manager` as the CCM (loaded by the bootstrap-kit slot 55 chart).
- Hetzner Cloud Volume CSI driver.
- kube-vip for the API-server HA VIP across multi-region.

A future Huawei cloud-init will substitute the CCM + CSI + Cloud-Volume
references with Huawei equivalents but plant the same Catalyst-zero
config files at the same paths (see `infra/providers/PROVIDER-INTERFACE.md`
§Cloud-init contract).

## Tests

- Module-local tofu tests: `tests/` — exercised by `.github/workflows/infra-hetzner-tofu.yaml`.
- End-to-end provisioning test: `tests/e2e/hetzner-provisioning/` (cross-repo).
