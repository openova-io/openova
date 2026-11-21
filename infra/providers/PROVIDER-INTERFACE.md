# OpenTofu provider module contract — `infra/providers/<name>/`

## Contract version 1: enforced by `TestProviderPacks`

This document states the contract that every provider module under `infra/providers/<name>/` meets. The conformance tests of section 6 enforce it on every pull request. Section 7 lists what was measured before the modules were brought into line. The decision record is [ADR-0016](../../docs/adr/0016-cloud-abstraction-contract.md).

Two work packages made it real. CLD-080 published the contract as a machine-readable file, `infra/providers/_contract/contract.json`. CLD-090 brought the modules into line, added the conformance tests of section 6 and changed the heading above. The file is the source and this document is its readable form. Where the two differ, the file wins.

A module receives three classes of variable.

1. **Contract variables.** The 33 of section 1. Every module declares every one.
2. **Engine extras.** Neutral variables that the provisioning engine writes today and that not every module declares. OpenTofu accepts an undeclared variable in a variables file with a warning, and the engine relies on that. The list is closed: `domain_mode`, `dynadot_key`, `dynadot_secret`, `ha_enabled`, `pool_domain`, `sovereign_subdomain`, `sovereign_deployment_id`, `parent_domains`, `qa_fixtures_namespace`, `qa_organization`, `qa_test_session_enabled`, `provider`, `region`, `control_plane_size`, `worker_size`, `worker_count`, `retry_attempt`, `object_storage_region`, `object_storage_access_key`, `object_storage_secret_key`, `object_storage_bucket_name`. A module may declare any of them. No entry is added without a change of the contract. This class is not a place for new variables.
3. **Adapter variables.** Variables whose name or meaning belongs to one cloud, such as `hcloud_token` or `huawei_access_key`. The Go adapter of that cloud supplies them.

### 1. Variables

Every module declares all 33 with exactly this type. "none" in the default column means the variable is required. "module's" means the module chooses its default. Any other default is the same in every module.

| Name | Type | Required | Contract default | Sensitive | Meaning |
|---|---|---|---|---|---|
| `deployment_id` | string | yes | none | no | 16 hexadecimal characters. The first 8 are part of the resource name prefix |
| `sovereign_fqdn` | string | yes | none | no | Fully qualified name of the Sovereign |
| `org_name` | string | yes | none | no | Name of the owning Organization |
| `org_email` | string | yes | none | no | Contact for certificates |
| `ssh_public_key` | string | yes | none | no | Break-glass key for nodes |
| `regions` | list(object), see below | yes | none | no | One entry per region. Index 0 is the primary |
| `parent_domains_yaml` | string | no | `""` | no | YAML list of parent domains |
| `gitops_repo_url` | string | yes | none | no | Git source for Flux |
| `gitops_branch` | string | no | `"main"` | no | Git ref that the Sovereign follows |
| `catalyst_api_url` | string | yes | none | no | Address of the provisioning API that receives call-backs |
| `handover_jwt_public_key` | string | no | `""` | yes | Key that the new Sovereign uses to verify the hand-over token |
| `kubeconfig_bearer_token` | string | no | `""` | yes | One-time token for the kubeconfig call-back |
| `ghcr_pull_token` | string | no | `""` | yes | Registry pull credential |
| `harbor_robot_token` | string | no | `""` | yes | Registry pull credential |
| `powerdns_api_key` | string | no | `""` | yes | DNS API key |
| `pdm_basic_auth_user` | string | no | `""` | yes | User of the pool domain manager |
| `pdm_basic_auth_pass` | string | no | `""` | yes | Password of the pool domain manager |
| `k3s_version` | string | no | module's | no | Version of the Kubernetes distribution |
| `default_storage_class` | string | yes | none | no | Written from the cloud profile |
| `variables` | map(string) | yes | none | no | The variables document of the region: exactly the keys of contract C5 (`docs/contracts/c5-upstream-variables.schema.json`). Its key `HANDOVER_AUTO` starts the hand-over flip |
| `provider_api_cidrs` | list(string) | no | `[]` | no | Address ranges of the cloud API that stay reachable during the egress proof |
| `ssh_allowed_cidrs` | list(string) | no | `[]` | no | Sources that may reach SSH on a node |
| `bcp_topology` | string | no | `"single-region"` | no | Topology choice |
| `cluster_mesh_name` | string | no | `""` | no | Mesh name of the cluster |
| `cluster_mesh_id` | number | no | `0` | no | Mesh identifier of the cluster |
| `console_isolation_enabled` | string | no | `"true"` | no | Separate front door for the console |
| `enable_fail2ban` | bool | no | `true` | no | Node hardening |
| `enable_unattended_upgrades` | bool | no | `true` | no | Node hardening |
| `enable_hot_standby` | string | no | `"false"` | no | Database option |
| `enable_shared_pg` | string | no | `"true"` | no | Database option |
| `marketplace_enabled` | string | no | `"true"` | no | Storefront switch |
| `qa_fixtures_enabled` | string | no | `"false"` | no | Switch for test fixtures |
| `wildcard_cert_use_staging` | string | no | `"false"` | no | Switch to the staging certificate authority |

The one `regions` type:

```hcl
variable "regions" {
  type = list(object({
    code               = string
    cloud_region       = string
    availability_zone  = optional(string, "")
    role               = string
    control_plane_size = string
    worker_size        = string
    worker_count       = number
  }))
  validation {
    condition     = length(var.regions) >= 1 && var.regions[0].role == "primary"
    error_message = "regions must have at least one entry and regions[0].role must be \"primary\"."
  }
  validation {
    condition     = alltrue([for r in var.regions : contains(["primary", "secondary"], r.role)])
    error_message = "regions[].role must be primary or secondary."
  }
  validation {
    condition     = length(distinct([for r in var.regions : r.code])) == length(var.regions)
    error_message = "regions[].code must be unique."
  }
}
```

A module whose cloud cannot span cloud regions adds its own validation that all `cloud_region` values are equal. Contract version 1 is one provider per Sovereign, so an entry has no `provider` field.

### 2. Outputs

Every module emits these seven. An output is in the contract only when shared code reads it.

| Name | Type | Meaning | Reader |
|---|---|---|---|
| `contract_version` | string | Always `"1"` | The conformance test. The engine refuses a module that reports another value |
| `control_plane_ip` | string | Public address of the primary control plane | The provisioning engine |
| `load_balancer_ip` | string | Public address of the primary gateway front door | The provisioning engine |
| `console_load_balancer_ip` | string | Address of the console front door, `""` when the console has no front door of its own | The provisioning engine |
| `control_plane_ips_per_region` | map(string) | Region code to control-plane address | The provisioning engine |
| `network_registry` | string (JSON) | The network registry of the Sovereign, section 2.1 | `scripts/check-network-registry.py` and the module tests |
| `seal_kind` | string | The kind of the pack's seal hook (contract C8): `module.seal.kind`, `static-node-file` or `cloud-key-service` | The conformance test. It equals `spec.seal.kind` of the pack's profile |

A further output, `day2_facts`, joins the contract in work package CLD-173, which adds its reader.

These names are reserved. Each joins the contract in the work package that adds its reader.

| Name | Kind | Type | Joins with |
|---|---|---|---|
| `edge_pool` | variable | object({size = string, count = number}) | NET-181 |
| `region_separation` | output | string | NET-210 |
| `edge_addresses` | output | map(string) | NET-184 |

Not in the contract: `sovereign_fqdn`, `console_url`, `gitops_repo_url`, `gitops_repo_url_out`, `kubeconfig_path`, `s3_buckets`, `region_primary`, `region_secondaries`, `worker_ips_per_region` and the object-storage outputs. Shared code reads none of them. A module may keep them as extras.

### 2.1 The network registry (`network_registry`)

The network registry is the record of every address, cluster name and cluster identifier of one Sovereign. The module is its one source. The output is the JSON text of one object. `scripts/check-network-registry.py <file>` checks it; `<file>` holds the object or the output of `tofu output -json network_registry`. The documents under `scripts/tests/check-network-registry/` are its test cases: `valid-*.json` pass, and `invalid/<rule>.json` fails with `<rule>`.

| Field | Type | Meaning |
|---|---|---|
| `podSupernet` | IPv4 CIDR | One range that holds every `podCIDR`: `10.42.0.0/15` for one or two regions, `10.40.0.0/13` for three to six |
| `mtu` | integer, 1280 to 9000 | The MTU given to Cilium (cilium-config `mtu`): the MTU of the node interface, `local.node_interface_mtu` of the module (1500 on Huawei and local, 1450 on Hetzner), never a value with an overhead taken off; Cilium takes the WireGuard overhead (80) off itself, so the pods get `mtu` - 80 |
| `regionSeparation` | `none`, `zone` or `site` | `site` when every region has a different `cloudRegion`; `zone` when cloud regions repeat but every pair of `cloudRegion` and `az` is different; `none` otherwise and for one region |
| `controlPlaneAccess.adminCIDRs`, `controlPlaneAccess.provisionerCIDRs` | list of CIDR, may be empty | Sources of the API port: the sovereign-admin source list and the provisioner source list. Empty until the module has these lists |
| `regions` | list, 1 to 6 entries | One entry per region, in the module's region order |
| `regions[].code` | string | The region code of the module |
| `regions[].cloudRegion`, `regions[].az` | string | Cloud region and zone of the region; `az` is `none` where the cloud has no zones |
| `regions[].clusterName` | string | Mesh name of the cluster: a lower-case label of at most 32 characters |
| `regions[].clusterID` | integer, 1 to 255 | Mesh identifier of the cluster |
| `regions[].nodeCIDR` | IPv4 CIDR | The node subnet |
| `regions[].podCIDR`, `regions[].serviceCIDR` | IPv4 CIDR, prefix length 16 | Pod range `10.(42+index).0.0/16`, service range `10.(96+index).0.0/16` |
| `regions[].meshEndpoint` | object `{ips, port}` | Present when there is more than one region: the addresses peers dial and the dial port |
| `regions[].edgeAddress` | IPv4 address | The public address clients reach in the region |
| `regions[].egressAddresses` | list of IPv4 address, at least one | Source addresses of traffic that leaves the region's nodes and pods |

The rules. A violation prints one line that starts with the rule name.

| Rule | A document passes when |
|---|---|
| `document-shape` | every field above is present with its type and range, `meshEndpoint` is the only optional field, and no other field exists |
| `unique-cluster-id` | no two regions share `clusterID` |
| `unique-cluster-name` | no two regions share `clusterName` |
| `unique-ranges` | no two of all `nodeCIDR`, `podCIDR` and `serviceCIDR` values overlap |
| `supernet-covers-pods` | every `podCIDR` lies inside `podSupernet` |
| `supernet-excludes-nodes-and-services` | no `nodeCIDR` or `serviceCIDR` intersects `podSupernet` |
| `mesh-endpoint-when-multi-region` | with more than one region, every region has `meshEndpoint` |
| `no-any-source` | no access list holds a range of prefix length 0 (`0.0.0.0/0`, `::/0`) |

### 3. Names, tags and labels

- Every named cloud resource starts with `catalyst-<fqdn with dots as dashes>-<first 8 characters of deployment_id>`. The orphan sweep, the server list and the liveness probe rely on this prefix and on nothing else.
- Tags and labels are not part of contract version 1. No shared code reads a tag. A pack may tag its resources as it likes.

### 4. Shared bootstrap template (cloud-init contract)

Every module renders the control-plane boot data with the one shared template, `infra/providers/_shared/cloudinit-control-plane.tftpl`. The template has no test on the provider name. Each module passes these 18 inputs. The first eight exist today. The next six are new: later work packages of area CLD add them. The last four are the outputs of the module's seal hook for the region of the render (contract C8, `docs/contracts/c8-seal-provider.md`; SEC-152).

| Input | Type | Meaning |
|---|---|---|
| `node_external_ip_cmd` | string | Shell that prints the public address of the node |
| `provider_id_cmd` | string | Shell that sets the provider identifier of the node, or `""` |
| `registry_mirror_yaml` | string | Body of the k3s registries file |
| `k3s_extra_args` | string | Extra flags for the k3s server |
| `provider_prelude` | string | Commands that run before k3s is installed |
| `cloud_credentials_secret_yaml` | string | Body of the Secret `flux-system/cloud-credentials` |
| `crossplane_provider_yaml` | string | Extra Crossplane manifests, or `""` |
| `kubeconfig_put_block` | string | Commands that send the kubeconfig back |
| `provider_overlay_path` | string | New. `./clusters/_template/providers/<name>` |
| `provider_substitutes_yaml` | string | New. YAML mapping of extra Flux substitute keys to string values |
| `node_ipv4_only` | bool | New. `true` renders the steps that a node without IPv6 needs |
| `fail_mark_cmd` | string | New. Command prefix that records a failure marker. The template appends the marker name |
| `render_region_kustomization` | bool | New. `false` only where the size limit of the boot data forbids it |
| `gateway_host_network` | bool | New. `true` when the gateway is served on host ports of the nodes |
| `seal_kind` | string | `module.seal.kind` |
| `seal_stanza` | string | `module.seal.stanza_by_region[<region key>]` |
| `seal_node_files` | list of objects | `module.seal.node_files_by_region[<region key>]` (sensitive) |
| `seal_volume_device` | string | `module.seal.volume_device` |

### 5. Module rules

- **Boot data never replaces a node.** Every compute resource that sets boot data (`user_data`) lists it in `lifecycle.ignore_changes`. A bootstrap template that renders differently must never plan the replacement of a node.
- **No node port.** A front door reaches a node on a host port: 443 and 80, and 53 (TCP and UDP) for the DNS front end. A module never declares a load-balancer target or a firewall rule in the range 30000 to 32767.
- **One control-plane node per region.** A second or third control-plane node per region is a change of the contract, with a work package of its own.
- **Random cluster tokens.** Every module declares `random_password.k3s_server_token` and `random_password.k3s_agent_token`, each with `for_each` over the region keys, `length = 64` and `special = false`. It passes the server token to the shared template as `k3s_server_token` and the agent token as `k3s_agent_token`. Its workers join with the agent token. No token is derived from an identifier such as the project or the domain name.
- **Seal hook.** Every module carries the seal hook of contract C8 (`docs/contracts/c8-seal-provider.md`) as the child module `infra/providers/<name>/seal/main.tf`. The file is the text of the contract; only the value of its output `volume_device` differs per pack: `"/dev/vdb"` in the pack `huawei`, `""` in the packs `hetzner` and `local`. The module calls the hook as `module "seal"` with `source = "./seal"`, with `region_keys` set to the region keys of its k3s tokens and `seal_key_generation` set to its own variable `seal_key_generation` (number, default `1`), and it emits the hook's `kind` as the output `seal_kind`. The variable `seal_key_generation` is the input of the same name of contract C8. It is in none of the three classes of variable at the head of this document: the provisioning engine does not write it and no adapter supplies it, so its default `1` applies. Raising it is a key rotation, and contract C8 leaves the rotation form of the hook to the first work package that performs a rotation. The provisioning engine copies the directory `seal` into the directory it runs OpenTofu in, and no other directory of the module: a module calls no other child module by a local path. Every call of the shared template passes the hook's outputs for its region as the four `seal_*` inputs of section 4 (SEC-152). When `volume_device` is not empty, each control-plane instance of the pack carries one data disk of the smallest size the cloud sells, deleted with the instance; in the pack `huawei` that is a 10 GB `SSD` disk, and `data_disks` and `delete_disks_on_termination` are in `lifecycle.ignore_changes`, because the provider replaces an instance when `data_disks` changes.

### 6. Conformance tests (added by CLD-090)

1. A Go test, `TestProviderPacks` (`products/catalyst/bootstrap/api/internal/providers/conformance`), lists the directories under `infra/providers/` whose name does not start with `_`. For each it compares the declared variables and outputs with `contract.json`, compares the OpenTofu providers of the module with `pack.yaml`, and asserts the first two rules of section 5. `scripts/check-provider-contract.sh` runs it; its `--self-test` proves that each rule can fail. Three of its checks are written and skipped until the work package named in their skip line makes them true for every pack: the adapter (CLD-102), the template inputs (CLD-132) and the overlay (CLD-144).
2. Each module has `tests/contract.tftest.hcl`. Its body is the same in every module (`infra/providers/_contract/contract.tftest.body.hcl`). It plans the module for one region and for two regions and asserts `contract_version` and the keys of `control_plane_ips_per_region`. A module's own required variables, such as its credentials, come from `tests/contract.auto.tfvars.json`.
3. CI finds the modules by directory. No workflow holds a list of provider names.
4. `TestProviderPacks` checks the seal hook of contract C8 (`docs/contracts/c8-seal-provider.md`) in every pack (added by CLD-092). The directory `seal/` declares exactly the inputs and the outputs of the contract, with their types, defaults and sensitive flags. Its output `kind` is a literal `static-node-file` or `cloud-key-service` and equals `spec.seal.kind` of the pack's profile, which the profile schema requires. The module calls the hook once as `module "seal"` with `source = "./seal"`, emits `seal_kind` as `module.seal.kind`, and declares `random_password.k3s_server_token` and `random_password.k3s_agent_token`, each with `for_each`, `length = 64` and `special = false`.

### 7. What the two modules do today

Measured on `main` at commit `1de1896b3`, on 2026-11-13, in `infra/providers/huawei/` and `infra/providers/hetzner/`. Later work packages change these facts; this section is not updated for each of them.

- Both modules declare all 33 contract variables. 32 have the contract type in both. `regions` has it in neither: the Huawei type lacks `cloud_region` and `availability_zone`, and the Hetzner type has the fields `provider`, `cloudRegion`, `controlPlaneSize`, `workerSize` and `workerCount`.
- `gitops_repo_url`, `catalyst_api_url` and `default_storage_class` have a default in both modules. The contract gives them none. In the Hetzner module `deployment_id` and `regions` also have a default. In the Huawei module `provider_api_cidrs` has a default that is not empty.
- Both modules emit four of the five outputs. Neither emits `contract_version`.
- The Huawei module uses the name prefix of section 3. The Hetzner module names its resources without the part taken from `deployment_id`.
- Both modules pass the first eight template inputs. The last six exist nowhere. The shared template tests the provider name on 23 lines.
- No compute resource of either module lists `user_data` in `lifecycle.ignore_changes`.
- No conformance test exists, and `contract.json` does not exist.
