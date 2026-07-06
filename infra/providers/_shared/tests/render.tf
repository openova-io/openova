# Standalone render harness for cloudinit-control-plane.tftpl (#4513).
#
# This module exists ONLY to render the shared cloud-init template with a
# complete dummy var map so a tofu test can assert the rendered YAML is
# structurally valid — without standing up a full provider module and without
# exposing the production (secret-bearing) render. Every value here is a
# throwaway literal; nothing is applied.
#
# The render is exposed as a NON-sensitive output deliberately: all inputs are
# dummies, so there is no secret to leak. Production renders keep the cloud-init
# inside a `local` (never an output) — see hetzner/main.tf:853.

variable "provider_name" {
  description = "Which cloud branch to exercise (hetzner | huawei)."
  type        = string
  default     = "hetzner"
}

locals {
  rendered_raw = templatefile("${path.module}/../cloudinit-control-plane.tftpl", {
    provider                          = var.provider_name
    cp_private_ip                     = "10.0.1.2"
    cluster_cidr                      = "10.42.0.0/16"
    service_cidr                      = "10.43.0.0/16"
    sovereign_fqdn                    = "render-test.example.test"
    sovereign_fqdn_slug               = "render-test-example-test"
    deployment_id                     = "deadbeefdeadbeef"
    org_name                          = "Render-Test"
    org_email                         = "render-test@example.test"
    region                            = var.provider_name == "huawei" ? "me-east-215-a" : "fsn1"
    sovereign_region_key              = var.provider_name == "huawei" ? "me-east-215-a" : "fsn1"
    sovereign_region_role             = "primary"
    region_canonical_label            = "primary"
    primary_region_canonical_label    = "primary"
    replica_region_canonical_label    = ""
    cluster_mesh_name                 = "render-test"
    cluster_mesh_id                   = "1"
    k3s_version                       = "v1.31.4+k3s1"
    k3s_token                         = "render-test-token"
    gitops_repo_url                   = "https://github.com/openova-io/openova"
    gitops_branch                     = "main"
    marketplace_enabled               = "true"
    qa_fixtures_enabled               = "true"
    fire_cutover_on_handover          = "true"
    console_isolation_enabled         = "true"
    wildcard_cert_issuer              = "wildcard-issuer"
    bcp_topology                      = "single-region"
    enable_hot_standby                = "false"
    enable_shared_pg                  = "true"
    default_storage_class             = var.provider_name == "huawei" ? "evs-ssd" : "hcloud-volumes"
    sovereign_cnpg_instances          = "1"
    continuum_enabled                 = "false"
    parent_domains_yaml               = "[{name: \"example.test\", role: \"primary\"}]"
    sovereign_regions_json            = jsonencode([{ region = "fsn1", primary = true }])
    sovereign_configured_regions_yaml = jsonencode(["fsn1"])
    enable_unattended_upgrades        = "true"
    enable_fail2ban                   = "true"
    distro_id                         = "ubuntu"
    distro_codename                   = "jammy"
    load_balancer_ipv4                = "203.0.113.10"
    console_load_balancer_ipv4        = ""
    handover_jwt_public_key           = "RENDER_TEST_JWK"
    ghcr_pull_username                = "render-test"
    ghcr_pull_token                   = "render-test-token"
    object_storage_access_key         = "render-test-ak"
    object_storage_secret_key         = "render-test-sk"
    object_storage_bucket_name        = "render-test-bucket"
    object_storage_endpoint           = "https://s3.example.test"
    object_storage_region             = "us-east-1"
    pdns_api_host                     = "powerdns.example.test"
    node_ip_cmd                       = "echo 10.0.1.2"
    node_external_ip_cmd              = "echo 203.0.113.10"
    node_external_ip_value            = "203.0.113.10"
    clustermesh_proxy_port            = 12379
    k3s_extra_args                    = ""
    registry_mirror_yaml              = "mirrors: {}"
    catalyst_api_url                  = "https://console.example.test"
    ghcr_pull_auth_b64                = "cmVuZGVyLXRlc3Q6cmVuZGVyLXRlc3Q="
    harbor_robot_token                = "render-test-harbor-token"
    kubeconfig_bearer_token           = "render-test-bearer"
    pdm_basic_auth_user               = "render-test"
    pdm_basic_auth_pass               = "render-test-pass"
    powerdns_api_key                  = "render-test-pdns-key"
    cloud_credentials_secret_yaml     = "apiVersion: v1\nkind: Secret\nmetadata:\n  name: cloud-credentials\n  namespace: flux-system\ndata: {}"
    # Provider-injected runcmd/file blocks: empty on this render branch (the
    # conditional if-not-empty guards short-circuit them), which is enough to
    # exercise the infrastructure-config + namespace-create + flux-bootstrap
    # sites this regression targets.
    provider_prelude         = ""
    kubeconfig_put_block     = ""
    provider_id_cmd          = ""
    crossplane_provider_yaml = ""
  })

  # Mirror the production comment-strip (hetzner/main.tf:853 — `^[ ]*#( |$).*`)
  # so the regexes + byte budget below run against what actually boots, not the
  # ~44 KB of doc prose the template ships for human readers.
  rendered_cloud_init = replace(local.rendered_raw, "/(?m)^[ ]*#( |$).*\n/", "")

  # The infrastructure-config Kustomization spec: `path:` and `prune:` MUST be
  # on separate lines (#4513 regression — an endif tilde collapsed them).
  path_line_jammed  = length(regexall("infrastructure(/hetzner)?[ ]+prune:", local.rendered_cloud_init)) > 0
  prune_on_own_line = length(regexall("(?m)^[ ]+prune: true$", local.rendered_cloud_init)) > 0

  # The namespace-create loop must NOT pass two NAMEs to a single
  # `create namespace` (#4513 — "exactly one NAME is required, got 2").
  multi_name_create = length(regexall("create namespace catalyst-system openbao", local.rendered_cloud_init)) > 0

  # #4521 — the two-layer Crossplane bootstrap. The Provider CR and its
  # CRD-CONSUMING ProviderConfig MUST be applied by SEPARATE Flux
  # Kustomizations so Flux's atomic server-side dry-run cannot reject the
  # ProviderConfig (`no matches for kind "ProviderConfig" in version
  # "tf.upbound.io/v1beta1"`) and thereby block the Provider CR from ever
  # applying.
  #
  # LAYER 1 (`infrastructure-providers`, the Provider-install) is the ONLY
  # crossplane Flux Kustomization INLINED in cloud-init — it points at the
  # providers/ tree (Provider package CRs only). LAYER 2
  # (`infrastructure-config`, the ProviderConfig + CloudAdoption claims) is a
  # COMMITTED Flux Kustomization CR in the providers/ tree (kept OFF the
  # byte-capped cloud-init), asserted separately below against the repo file.
  has_providers_kustomization = length(regexall("(?m)^[ ]+name: infrastructure-providers$", local.rendered_cloud_init)) > 0

  # The inline Provider-install layer must point at the providers/ tree (NOT
  # the config tree) so it carries ONLY the Provider package CRs.
  providers_path_correct = length(regexall("path: ./clusters/_template/infrastructure/providers", local.rendered_cloud_init)) > 0

  # LAYER 2 must NOT be re-inlined into cloud-init (the #1981/#3884 byte-cap
  # regression class): no operative `name: infrastructure-config` Kustomization
  # line in the rendered cloud-init.
  config_not_inlined = length(regexall("(?m)^[ ]+name: infrastructure-config$", local.rendered_cloud_init)) == 0

  # The committed LAYER-2 Flux Kustomization CR (both cloud variants) must
  # exist and `dependsOn: [infrastructure-providers]` — the ordering gate that
  # breaks the #4521 deadlock. Read the repo files directly (not the render).
  config_ks_base    = file("${path.module}/../../../../clusters/_template/infrastructure/providers/infrastructure-config-kustomization.yaml")
  config_ks_hetzner = file("${path.module}/../../../../clusters/_template/infrastructure/providers/hetzner/infrastructure-config-kustomization.yaml")
  config_depends_on_providers = (
    length(regexall("(?s)name: infrastructure-config.*?dependsOn:.*?- name: infrastructure-providers", local.config_ks_base)) > 0 &&
    length(regexall("(?s)name: infrastructure-config.*?dependsOn:.*?- name: infrastructure-providers", local.config_ks_hetzner)) > 0
  )

  # The two committed LAYER-2 CRs must point at the correct cloud config tree:
  # base → ./clusters/_template/infrastructure ; hetzner → .../infrastructure/hetzner.
  config_paths_correct = (
    length(regexall("path: ./clusters/_template/infrastructure\n", local.config_ks_base)) > 0 &&
    length(regexall("path: ./clusters/_template/infrastructure/hetzner\n", local.config_ks_hetzner)) > 0
  )
}

output "rendered_cloud_init" {
  value     = local.rendered_cloud_init
  sensitive = false
}

output "path_line_jammed" {
  value = local.path_line_jammed
}

output "prune_on_own_line" {
  value = local.prune_on_own_line
}

output "multi_name_create" {
  value = local.multi_name_create
}

output "rendered_bytes" {
  value = length(local.rendered_cloud_init)
}

output "has_providers_kustomization" {
  value = local.has_providers_kustomization
}

output "providers_path_correct" {
  value = local.providers_path_correct
}

output "config_not_inlined" {
  value = local.config_not_inlined
}

output "config_depends_on_providers" {
  value = local.config_depends_on_providers
}

output "config_paths_correct" {
  value = local.config_paths_correct
}
