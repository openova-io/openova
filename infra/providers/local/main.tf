# The local provider pack: contract version 1 on one host, with no cloud.
#
# For each entry of var.regions the module starts one control-plane machine
# and worker_count worker machines. A machine is a privileged container that
# runs systemd and cloud-init; cloud-init reads its user-data from the NoCloud
# seed directory, which the module fills before the container starts. The
# control-plane machine boots the shared template
# infra/providers/_shared/cloudinit-control-plane.tftpl, unmodified; the
# workers boot cloudinit-worker.tftpl of this module.
#
# Two values of the shared render are set here after the render, each at a
# text that must occur exactly once (the precondition on the control-plane
# machine stops the plan otherwise):
#   - the path of the Flux Kustomization bootstrap-kit becomes the reduced kit
#     clusters/_template/providers/local/kit (scripts/generators/local-kit.py);
#   - the substitutes of this pack (local.pack_substitutes) are written into
#     the ConfigMap flux-system/sovereign-variables, before the key
#     WILDCARD_CERT_ISSUER.
#
# Front doors are the control-plane machine's own address. The gateway and
# the DNS server bind host ports on it (443, 80, the console ports, 53);
# nothing uses a node port. The load-balancer address pool of a region is one
# address of var.external_cidr, which is on no machine: a pool that holds the
# machine's own address takes that address away from the machine.

locals {
  fqdn_slug   = replace(var.sovereign_fqdn, ".", "-")
  name_prefix = "catalyst-${local.fqdn_slug}-${substr(var.deployment_id, 0, 8)}"
  first_label = split(".", var.sovereign_fqdn)[0]

  region_keys = [for r in var.regions : r.code]

  # The cluster region id: {provider}-{region}-{building block}-{environment}.
  region_label = { for r in var.regions : r.code => "local-${r.code}-rtz-prod" }

  # Addresses. Region i uses the /24 number i + 1 of var.machine_cidr.
  machine_gateway     = cidrhost(var.machine_cidr, 1)
  region_node_cidr    = { for idx, r in var.regions : r.code => cidrsubnet(var.machine_cidr, 24 - tonumber(split("/", var.machine_cidr)[1]), idx + 1) }
  control_plane_ip    = { for r in var.regions : r.code => cidrhost(local.region_node_cidr[r.code], 10) }
  external_ip         = { for idx, r in var.regions : r.code => cidrhost(var.external_cidr, 10 + idx) }
  region_pod_cidr     = { for idx, r in var.regions : r.code => format("10.%d.0.0/16", 42 + idx) }
  region_service_cidr = { for idx, r in var.regions : r.code => format("10.%d.0.0/16", 96 + idx) }
  pod_supernet        = length(var.regions) <= 2 ? "10.42.0.0/15" : "10.40.0.0/13"

  # Mesh names and identifiers, as the other packs derive them.
  primary_mesh_id = var.cluster_mesh_id > 0 ? var.cluster_mesh_id : 1
  mesh_name = { for idx, r in var.regions : r.code => (
    idx == 0 && var.cluster_mesh_name != "" ? var.cluster_mesh_name :
    trim(substr(lower("${local.first_label}-${r.code}"), 0, 32), "-")
  ) }
  mesh_id = { for idx, r in var.regions : r.code => local.primary_mesh_id + idx }

  # Machines. One control plane per region (contract version 1), then the
  # workers of the region.
  control_planes = { for r in var.regions : "${r.code}-cp1" => {
    region   = r.code
    hostname = "${r.code}-cp1"
    ip       = local.control_plane_ip[r.code]
  } }
  workers = merge([for r in var.regions : { for i in range(r.worker_count) : "${r.code}-w${i + 1}" => {
    region   = r.code
    hostname = "${r.code}-w${i + 1}"
    ip       = cidrhost(local.region_node_cidr[r.code], 20 + i)
  } }]...)

  ghcr_pull_username = "openova-bot"
  ghcr_pull_auth_b64 = base64encode("${local.ghcr_pull_username}:${var.ghcr_pull_token}")

  parent_domains_yaml = coalesce(
    var.parent_domains_yaml,
    format("[{name: \"%s\", role: \"primary\"}]", var.sovereign_fqdn),
  )

  # The object store of this pack is the one the kit installs (slot 18).
  object_storage_endpoint = "http://seaweedfs-s3.seaweedfs.svc.cluster.local:8333"

  # The substitutes of this pack, per region. Every key is a kit key
  # (docs/contracts/c5-kit-keys.txt).
  pack_substitutes = { for r in var.regions : r.code => {
    SOVEREIGN_CNPG_STORAGE_CLASS       = var.default_storage_class
    HCLOUD_HR_SUSPEND                  = "true"
    HUAWEI_HR_SUSPEND                  = "true"
    HCLOUD_CSI_ENABLED                 = "false"
    EVS_CSI_ENABLED                    = "false"
    HUAWEI_VPC_ROUTES_ENABLED          = "false"
    SECONDARY_HR_SUSPEND               = r.role == "primary" ? "false" : "true"
    SECONDARY_EDGE_SUSPEND             = r.role == "primary" ? "true" : "false"
    CILIUM_LB_IPAM_ENABLED             = "true"
    CILIUM_LB_IPAM_CIDRS               = "${local.external_ip[r.code]}/32"
    CILIUM_CLUSTERMESH_PROXY_ENABLED   = "true"
    CILIUM_CLUSTERMESH_PROXY_PORT      = tostring(var.clustermesh_proxy_port)
    CLUSTERMESH_APISERVER_DIAL_PORT    = tostring(var.clustermesh_proxy_port)
    CILIUM_GATEWAY_HOSTNETWORK_ENABLED = "true"
    SOVEREIGN_GATEWAY_HTTPS_PORT       = "443"
    SOVEREIGN_GATEWAY_HTTP_PORT        = "80"
    CONSOLE_GATEWAY_HTTPS_PORT         = "8443"
    CONSOLE_GATEWAY_HTTP_PORT          = "8080"
    POWERDNS_DNSDIST_HOSTPORT          = "true"
  } }

  # The two texts of the shared render that this module changes.
  kit_path_shared   = "\n        path: ./clusters/_template/bootstrap-kit\n"
  kit_path_local    = "\n        path: ./clusters/_template/providers/local/kit\n"
  substitute_anchor = "\n            WILDCARD_CERT_ISSUER: "

  substitute_lines = { for code, subs in local.pack_substitutes : code => join("", [
    for k in sort(keys(subs)) : "\n            ${k}: ${jsonencode(subs[k])}"
  ]) }
}

resource "random_password" "k3s_server_token" {
  for_each = toset(local.region_keys)
  length   = 64
  special  = false
}

resource "random_password" "k3s_agent_token" {
  for_each = toset(local.region_keys)
  length   = 64
  special  = false
}

locals {
  control_plane_render_raw = { for r in var.regions : r.code => templatefile("${path.module}/../_shared/cloudinit-control-plane.tftpl", {
    provider = "local"

    sovereign_fqdn        = var.sovereign_fqdn
    sovereign_fqdn_slug   = local.fqdn_slug
    deployment_id         = var.deployment_id
    org_name              = var.org_name
    org_email             = var.org_email
    region                = r.code
    sovereign_region_key  = local.region_label[r.code]
    sovereign_region_role = r.role
    cluster_mesh_name     = local.mesh_name[r.code]
    cluster_mesh_id       = tostring(local.mesh_id[r.code])
    k3s_version           = var.k3s_version
    k3s_server_token      = random_password.k3s_server_token[r.code].result
    k3s_agent_token       = random_password.k3s_agent_token[r.code].result
    cp_private_ip         = local.control_plane_ip[r.code]
    cluster_cidr          = local.region_pod_cidr[r.code]
    service_cidr          = local.region_service_cidr[r.code]

    region_canonical_label         = local.region_label[r.code]
    primary_region_canonical_label = local.region_label[local.region_keys[0]]
    replica_region_canonical_label = length(var.regions) > 1 ? local.region_label[local.region_keys[1]] : ""

    pdns_api_host           = "powerdns.${var.sovereign_fqdn}"
    provider_api_cidrs_yaml = jsonencode(var.provider_api_cidrs)
    gitops_repo_url         = var.gitops_repo_url
    gitops_branch           = var.gitops_branch
    parent_domains_yaml     = local.parent_domains_yaml
    sovereign_regions_json = jsonencode([for rr in var.regions : {
      provider         = "local"
      cloudRegion      = local.region_label[rr.code]
      controlPlaneSize = rr.control_plane_size
      workerSize       = rr.worker_size
      workerCount      = rr.worker_count
    }])
    sovereign_configured_regions_yaml = jsonencode([for rr in var.regions : local.region_label[rr.code]])

    bcp_topology             = var.bcp_topology
    enable_hot_standby       = var.enable_hot_standby
    enable_shared_pg         = var.enable_shared_pg
    default_storage_class    = var.default_storage_class
    sovereign_cnpg_instances = length(var.regions) > 1 ? "2" : "1"
    continuum_enabled        = length(var.regions) > 1 ? "true" : "false"
    marketplace_enabled      = var.marketplace_enabled
    qa_fixtures_enabled      = var.qa_fixtures_enabled
    # The variables document of the region (contract C5); the template reads
    # the hand-over switch HANDOVER_AUTO from it.
    variables                 = var.variables
    console_isolation_enabled = var.console_isolation_enabled
    wildcard_cert_issuer      = var.wildcard_cert_use_staging == "true" ? "letsencrypt-dns01-staging-powerdns" : "letsencrypt-dns01-prod-powerdns"

    ghcr_pull_username         = local.ghcr_pull_username
    ghcr_pull_token            = var.ghcr_pull_token
    ghcr_pull_auth_b64         = local.ghcr_pull_auth_b64
    object_storage_endpoint    = local.object_storage_endpoint
    object_storage_region      = "us-east-1"
    object_storage_bucket_name = local.name_prefix
    object_storage_access_key  = var.object_storage_access_key
    object_storage_secret_key  = var.object_storage_secret_key
    handover_jwt_public_key    = var.handover_jwt_public_key
    kubeconfig_bearer_token    = var.kubeconfig_bearer_token
    catalyst_api_url           = var.catalyst_api_url
    enable_unattended_upgrades = var.enable_unattended_upgrades
    enable_fail2ban            = var.enable_fail2ban
    powerdns_api_key           = var.powerdns_api_key
    pdm_basic_auth_user        = var.pdm_basic_auth_user
    pdm_basic_auth_pass        = var.pdm_basic_auth_pass
    harbor_robot_token         = var.harbor_robot_token

    # Front doors: the control-plane machine's own address.
    load_balancer_ipv4         = local.control_plane_ip[local.region_keys[0]]
    console_load_balancer_ipv4 = var.console_isolation_enabled == "true" ? local.control_plane_ip[local.region_keys[0]] : ""

    registry_mirror_yaml          = var.registry_mirror_yaml
    cloud_credentials_secret_yaml = "apiVersion: v1\nkind: Secret\nmetadata:\n  name: cloud-credentials\n  namespace: flux-system\ntype: Opaque\ndata: {}"
    crossplane_provider_yaml      = ""
    provider_id_cmd               = ""
    provider_prelude              = ""
    kubeconfig_put_block          = ""
    k3s_extra_args                = "--node-label=topology.kubernetes.io/region=${r.code} --node-label=topology.kubernetes.io/zone=${r.code}"
    node_ip_cmd                   = "printf '%s' '${local.control_plane_ip[r.code]}'"
    node_external_ip_cmd          = "printf '%s' '${local.external_ip[r.code]}'"
    node_external_ip_value        = local.external_ip[r.code]
    clustermesh_proxy_port        = var.clustermesh_proxy_port
  }) }

  # The kit path and the pack substitutes, then the comment strip that the
  # other modules apply.
  control_plane_user_data = { for code, raw in local.control_plane_render_raw : code => replace(
    replace(
      replace(raw, local.kit_path_shared, local.kit_path_local),
      local.substitute_anchor, "${local.substitute_lines[code]}${local.substitute_anchor}",
    ),
    "/(?m)^[ ]*#( |$).*\n/", "",
  ) }

  # Each changed text occurs exactly once in each render.
  render_texts_found = alltrue([for code, raw in local.control_plane_render_raw :
    length(split(local.kit_path_shared, raw)) == 2 && length(split(local.substitute_anchor, raw)) == 2
  ])

  worker_user_data = { for name, m in local.workers : name => templatefile("${path.module}/cloudinit-worker.tftpl", {
    cp_private_ip        = local.control_plane_ip[m.region]
    node_ip              = m.ip
    region               = m.region
    k3s_token            = random_password.k3s_agent_token[m.region].result
    k3s_release_url      = "https://github.com/k3s-io/k3s/releases/download/${urlencode(var.k3s_version)}"
    registry_mirror_yaml = var.registry_mirror_yaml
  }) }
}

# ── Machine image ────────────────────────────────────────────────────────────

locals {
  image_files   = sort(fileset("${path.module}/image", "*"))
  image_digest  = sha256(join("", [for f in local.image_files : filesha256("${path.module}/image/${f}")]))
  machine_image = var.machine_image != "" ? var.machine_image : one(docker_image.machine[*].image_id)
}

resource "docker_image" "machine" {
  count        = var.machine_image == "" ? 1 : 0
  name         = "catalyst-local-machine:${substr(local.image_digest, 0, 16)}"
  keep_locally = true

  build {
    context    = "${path.module}/image"
    dockerfile = "Containerfile"
  }

  triggers = {
    image_digest = local.image_digest
  }
}

# ── Network and state volumes ────────────────────────────────────────────────

resource "docker_network" "machines" {
  name = local.name_prefix

  ipam_config {
    subnet  = var.machine_cidr
    gateway = local.machine_gateway
  }
}

# containerd's overlay snapshotter cannot sit on the container's own overlay
# root, so the k3s and kubelet state of every machine live on volumes.
resource "docker_volume" "rancher" {
  for_each = merge(local.control_planes, local.workers)
  name     = "${local.name_prefix}-${each.key}-rancher"
}

resource "docker_volume" "kubelet" {
  for_each = merge(local.control_planes, local.workers)
  name     = "${local.name_prefix}-${each.key}-kubelet"
}

# ── Machines ─────────────────────────────────────────────────────────────────

resource "docker_container" "control_plane" {
  for_each = local.control_planes

  name        = "${local.name_prefix}-${each.key}"
  hostname    = each.value.hostname
  image       = local.machine_image
  privileged  = true
  restart     = "unless-stopped"
  stop_signal = "SIGRTMIN+3"
  env         = ["container=docker"]
  must_run    = true
  wait        = false

  tmpfs = {
    "/run"      = "rw,exec,mode=755"
    "/run/lock" = "rw,mode=1777"
  }

  networks_advanced {
    name         = docker_network.machines.name
    ipv4_address = each.value.ip
  }

  volumes {
    volume_name    = docker_volume.rancher[each.key].name
    container_path = "/var/lib/rancher"
  }

  volumes {
    volume_name    = docker_volume.kubelet[each.key].name
    container_path = "/var/lib/kubelet"
  }

  mounts {
    type      = "bind"
    source    = "/lib/modules"
    target    = "/lib/modules"
    read_only = true
  }

  upload {
    file    = "/var/lib/cloud/seed/nocloud/user-data"
    content = local.control_plane_user_data[each.value.region]
  }

  upload {
    file    = "/var/lib/cloud/seed/nocloud/meta-data"
    content = "instance-id: ${local.name_prefix}-${each.key}\nlocal-hostname: ${each.value.hostname}\npublic-keys:\n  - ${jsonencode(var.ssh_public_key)}\n"
  }

  lifecycle {
    # Boot data never replaces a machine.
    ignore_changes = [upload]

    precondition {
      condition     = local.render_texts_found
      error_message = "The shared template no longer holds exactly one line '        path: ./clusters/_template/bootstrap-kit' and exactly one line starting '            WILDCARD_CERT_ISSUER: '. The local module sets its kit path and its substitutes at these two texts."
    }
  }
}

resource "docker_container" "worker" {
  for_each = local.workers

  name        = "${local.name_prefix}-${each.key}"
  hostname    = each.value.hostname
  image       = local.machine_image
  privileged  = true
  restart     = "unless-stopped"
  stop_signal = "SIGRTMIN+3"
  env         = ["container=docker"]
  must_run    = true
  wait        = false

  tmpfs = {
    "/run"      = "rw,exec,mode=755"
    "/run/lock" = "rw,mode=1777"
  }

  networks_advanced {
    name         = docker_network.machines.name
    ipv4_address = each.value.ip
  }

  volumes {
    volume_name    = docker_volume.rancher[each.key].name
    container_path = "/var/lib/rancher"
  }

  volumes {
    volume_name    = docker_volume.kubelet[each.key].name
    container_path = "/var/lib/kubelet"
  }

  mounts {
    type      = "bind"
    source    = "/lib/modules"
    target    = "/lib/modules"
    read_only = true
  }

  upload {
    file    = "/var/lib/cloud/seed/nocloud/user-data"
    content = local.worker_user_data[each.key]
  }

  upload {
    file    = "/var/lib/cloud/seed/nocloud/meta-data"
    content = "instance-id: ${local.name_prefix}-${each.key}\nlocal-hostname: ${each.value.hostname}\npublic-keys:\n  - ${jsonencode(var.ssh_public_key)}\n"
  }

  lifecycle {
    # Boot data never replaces a machine.
    ignore_changes = [upload]
  }

  depends_on = [docker_container.control_plane]
}

# ── Network registry (contract output network_registry) ──────────────────────

locals {
  network_registry = {
    podSupernet      = local.pod_supernet
    mtu              = 1420
    regionSeparation = "none"
    controlPlaneAccess = {
      adminCIDRs       = []
      provisionerCIDRs = []
    }
    regions = [for r in var.regions : merge(
      {
        code            = r.code
        cloudRegion     = r.cloud_region
        az              = r.availability_zone != "" ? r.availability_zone : r.cloud_region
        clusterName     = local.mesh_name[r.code]
        clusterID       = local.mesh_id[r.code]
        nodeCIDR        = local.region_node_cidr[r.code]
        podCIDR         = local.region_pod_cidr[r.code]
        serviceCIDR     = local.region_service_cidr[r.code]
        edgeAddress     = local.control_plane_ip[r.code]
        egressAddresses = [local.control_plane_ip[r.code]]
      },
      { for k, v in {
        meshEndpoint = {
          ips  = [local.control_plane_ip[r.code]]
          port = var.clustermesh_proxy_port
        }
      } : k => v if length(var.regions) > 1 },
    )]
  }
}
