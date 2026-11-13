# CLD-055 spike: render the UNMODIFIED shared control-plane template
# (infra/providers/_shared/cloudinit-control-plane.tftpl) for one machine.
#
# Every value is the Huawei fixture of infra/providers/_shared/tests/render.tf,
# copied as it stands, except:
#   - values that name a host (an address, an endpoint or a mirror), which are
#     variables here because a machine on a CI runner has other hosts;
#   - for the two-region run (L6) only, the per-region values, derived the way
#     infra/providers/huawei/main.tf derives them for regions[0] and regions[1].
# The comment-strip is the one the provider modules apply.

variable "node_ip" {
  description = "The machine's own address. Feeds cp_private_ip, node_ip_cmd and load_balancer_ipv4 (the front door is the machine's own address)."
  type        = string
  default     = ""
}

# The Huawei branch builds the LB-IPAM pool from node_external_ip_value/32.
# On Huawei that is the EIP, which is NOT on the node (1:1 NAT). Run
# 43358110034 used the node's own address here: once bp-cilium applied the
# pool, the machine's own TCP egress and its apiserver stopped answering.
# Empty = the node's own address.
variable "external_ip" {
  description = "Feeds node_external_ip_cmd and node_external_ip_value."
  type        = string
  default     = ""
}

variable "object_storage_endpoint" {
  description = "S3 endpoint written into flux-system/object-storage (the in-kit object store)."
  type        = string
  default     = ""
}

variable "catalyst_api_url" {
  description = "Where the template's log helper sends cloud-init-output.log (a sink on the runner)."
  type        = string
  default     = ""
}

variable "registry_mirror_yaml" {
  description = "Body of /etc/rancher/k3s/registries.yaml."
  type        = string
  default     = ""
}

variable "pdns_api_host" {
  description = "PowerDNS API host substitute."
  type        = string
  default     = ""
}

# Proof that the fixture is copied exactly: with fixture_hosts = true every
# host value is render.tf's own, so user_data_sha256 must equal golden_huawei
# in infra/providers/_shared/tests/golden.auto.tfvars.json.
variable "fixture_hosts" {
  type    = bool
  default = false
}

# Two-region shape (L6). -1 means the single-region fixture.
variable "two_region_index" {
  description = "-1 = the single-region fixture as render.tf has it; 0 or 1 = that region of a two-region set."
  type        = number
  default     = -1
}

locals {
  external_ip = var.external_ip != "" ? var.external_ip : var.node_ip
  two_codes   = ["me-east-215-a", "me-east-215-b"]
  two         = var.two_region_index >= 0
  code        = local.two ? local.two_codes[var.two_region_index] : "me-east-215-a"
  label       = "hw-${local.code}-rtz-prod"

  # Single-region values: render.tf's Huawei fixture, verbatim.
  # Two-region values: huawei/main.tf lines 149-156 and 1117-1223.
  per_region = local.two ? {
    cluster_cidr                      = format("10.%d.0.0/16", 42 + var.two_region_index)
    service_cidr                      = format("10.%d.0.0/16", 96 + var.two_region_index)
    region                            = local.code
    sovereign_region_key              = local.label
    sovereign_region_role             = var.two_region_index == 0 ? "primary" : "secondary"
    region_canonical_label            = local.label
    primary_region_canonical_label    = "hw-${local.two_codes[0]}-rtz-prod"
    replica_region_canonical_label    = "hw-${local.two_codes[1]}-rtz-prod"
    cluster_mesh_name                 = var.two_region_index == 0 ? "render-test-me-east-a" : "render-test-me-east-b"
    cluster_mesh_id                   = tostring(1 + var.two_region_index)
    sovereign_cnpg_instances          = "2"
    continuum_enabled                 = "true"
    sovereign_regions_json            = jsonencode([for c in local.two_codes : { provider = "huawei", cloudRegion = "hw-${c}-rtz-prod", controlPlaneSize = "small", workerSize = "small", workerCount = 0 }])
    sovereign_configured_regions_yaml = jsonencode([for c in local.two_codes : "hw-${c}-rtz-prod"])
    bcp_topology                      = "active-passive"
    } : {
    cluster_cidr                      = "10.42.0.0/16"
    service_cidr                      = "10.43.0.0/16"
    region                            = "me-east-215-a"
    sovereign_region_key              = "me-east-215-a"
    sovereign_region_role             = "primary"
    region_canonical_label            = "primary"
    primary_region_canonical_label    = "primary"
    replica_region_canonical_label    = ""
    cluster_mesh_name                 = "render-test"
    cluster_mesh_id                   = "1"
    sovereign_cnpg_instances          = "1"
    continuum_enabled                 = "false"
    sovereign_regions_json            = jsonencode([{ region = "fsn1", primary = true }])
    sovereign_configured_regions_yaml = jsonencode(["fsn1"])
    bcp_topology                      = "single-region"
  }

  # The host-naming values: render.tf's own, or this machine's.
  hosts = var.fixture_hosts ? {
    cp_private_ip           = "10.0.1.2"
    load_balancer_ipv4      = "203.0.113.10"
    object_storage_endpoint = "https://s3.example.test"
    pdns_api_host           = "powerdns.example.test"
    node_ip_cmd             = "echo 10.0.1.2"
    node_external_ip_cmd    = "echo 203.0.113.10"
    node_external_ip_value  = "203.0.113.10"
    registry_mirror_yaml    = "mirrors: {}"
    catalyst_api_url        = "https://console.example.test"
    } : {
    cp_private_ip           = var.node_ip
    load_balancer_ipv4      = var.node_ip
    object_storage_endpoint = var.object_storage_endpoint
    pdns_api_host           = var.pdns_api_host
    node_ip_cmd             = "echo ${var.node_ip}"
    node_external_ip_cmd    = "echo ${local.external_ip}"
    node_external_ip_value  = local.external_ip
    registry_mirror_yaml    = var.registry_mirror_yaml
    catalyst_api_url        = var.catalyst_api_url
  }

  rendered_raw = templatefile("${path.module}/../../../../infra/providers/_shared/cloudinit-control-plane.tftpl", merge({
    provider                      = "huawei"
    sovereign_fqdn                = "render-test.example.test"
    sovereign_fqdn_slug           = "render-test-example-test"
    deployment_id                 = "deadbeefdeadbeef"
    org_name                      = "Render-Test"
    org_email                     = "render-test@example.test"
    k3s_version                   = "v1.31.4+k3s1"
    k3s_server_token              = "render-test-server-token"
    k3s_agent_token               = "render-test-agent-token"
    gitops_repo_url               = "https://github.com/openova-io/openova"
    gitops_branch                 = "main"
    marketplace_enabled           = "true"
    qa_fixtures_enabled           = "true"
    fire_cutover_on_handover      = "true"
    console_isolation_enabled     = "true"
    wildcard_cert_issuer          = "wildcard-issuer"
    enable_hot_standby            = "false"
    enable_shared_pg              = "true"
    default_storage_class         = "evs-ssd"
    parent_domains_yaml           = "[{name: \"example.test\", role: \"primary\"}]"
    enable_unattended_upgrades    = "true"
    enable_fail2ban               = "true"
    distro_id                     = "ubuntu"
    distro_codename               = "jammy"
    console_load_balancer_ipv4    = ""
    handover_jwt_public_key       = "RENDER_TEST_JWK"
    ghcr_pull_username            = "render-test"
    ghcr_pull_token               = "render-test-token"
    object_storage_access_key     = "render-test-ak"
    object_storage_secret_key     = "render-test-sk"
    object_storage_bucket_name    = "render-test-bucket"
    object_storage_region         = "us-east-1"
    clustermesh_proxy_port        = 12379
    provider_api_cidrs_yaml       = "[]"
    k3s_extra_args                = ""
    ghcr_pull_auth_b64            = "cmVuZGVyLXRlc3Q6cmVuZGVyLXRlc3Q="
    harbor_robot_token            = "render-test-harbor-token"
    kubeconfig_bearer_token       = "render-test-bearer"
    pdm_basic_auth_user           = "render-test"
    pdm_basic_auth_pass           = "render-test-pass"
    powerdns_api_key              = "render-test-pdns-key"
    cloud_credentials_secret_yaml = "apiVersion: v1\nkind: Secret\nmetadata:\n  name: cloud-credentials\n  namespace: flux-system\ndata: {}"
    provider_prelude              = ""
    kubeconfig_put_block          = ""
    provider_id_cmd               = ""
    crossplane_provider_yaml      = ""
  }, local.hosts, local.per_region))

  user_data = replace(local.rendered_raw, "/(?m)^[ ]*#( |$).*\n/", "")
}

output "user_data" {
  value = local.user_data
}

output "user_data_sha256" {
  value = sha256(local.user_data)
}

output "user_data_bytes" {
  value = length(local.user_data)
}
