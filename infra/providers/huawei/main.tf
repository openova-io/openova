# Catalyst Sovereign on Huawei Cloud (Stack) — canonical Phase-0
# OpenTofu module (Wave 3 of the multi-cloud refactor, Refs #2140).
#
# Topology (operator-confirmed, Tier-B):
#   - 2 VPCs (one per fake-region) carrying isolated `10.20.0.0/16` +
#     `10.30.0.0/16`. NO peering between them — inter-region traffic
#     ALWAYS flows over DMZ-WG on PUBLIC EIPs per docs/DOD.md A2
#     ("private links between regions are forbidden, irrespective of
#     same/different provider").
#   - 1 subnet per VPC, anchored in `me-east-215a` (the only AZ in
#     region me-east-215; fake-regions overlay via VPC isolation).
#   - 1 security group per VPC. Ingress: TCP 443 (Gateway) + TCP 6443
#     (k3s apiserver, fail-closed via k8s RBAC), UDP 51820 (Cilium WG)
#     + TCP 2379 (ClusterMesh etcd) + TCP 4240 (Cilium health) all
#     restricted to the OTHER region's CP EIPs only.
#   - 3 CP ECS per region (s7n.large.4 — 2 vCPU / 8 GB) + 2 worker
#     ECS per region (m7n.xlarge.8 — 4 vCPU / 32 GB).
#   - 1 EIP per CP (3 per region; workers have no EIP — only
#     cluster-internal connectivity). Total = 6 EIPs across 2 regions
#     out of HCS quota 10.
#   - 1 OBS bucket per Sovereign for tofu remote state + Velero backups.
#
# Per-CP EIP is non-negotiable per DOD A2 (DMZ WireGuard over PUBLIC
# IPs, no VPC peering). Per docs/PRINCIPLES.md #4 nothing is hardcoded —
# every value is a variable.

# ── Per-region key set ────────────────────────────────────────────────────
locals {
  # Region keys preserve operator-supplied order. Index 0 = primary.
  region_keys = [for r in var.regions : r.code]

  # Per-region VPC CIDR — first region gets 10.20.0.0/16, second
  # gets 10.30.0.0/16, third gets 10.40.0.0/16, etc. Each region's
  # CIDR is isolated (no peering) — collisions across regions are
  # harmless because traffic never crosses VPC boundaries.
  region_vpc_cidr = {
    for idx, r in var.regions :
    r.code => format("10.%d.0.0/16", 20 + idx * 10)
  }

  # Per-region subnet CIDR — uniform /24 inside each /16 since the
  # regions don't share a routing domain.
  region_subnet_cidr = {
    for idx, r in var.regions :
    r.code => format("10.%d.1.0/24", 20 + idx * 10)
  }

  # Effective per-region SKU — operator-supplied wins; module defaults
  # back-fill empty entries (s7n.large.4 / m7n.xlarge.8).
  effective_cp_flavor = {
    for r in var.regions :
    r.code => r.control_plane_size != "" ? r.control_plane_size : var.default_control_plane_flavor
  }
  effective_worker_flavor = {
    for r in var.regions :
    r.code => r.worker_size != "" ? r.worker_size : var.default_worker_flavor
  }

  # FQDN slug (dots → dashes). Used in every resource name so a fresh
  # Sovereign provision never collides with sibling Sovereigns in the
  # same Huawei project.
  fqdn_slug = replace(var.sovereign_fqdn, ".", "-")

  # GHCR pull-token base64 for the dockerconfigjson Secret shape (same
  # pattern the Hetzner module uses — computed here so the cloud-init
  # template stays a pure string-interpolation).
  ghcr_pull_username = "openova-bot"
  ghcr_pull_auth_b64 = base64encode("${local.ghcr_pull_username}:${var.ghcr_pull_token}")

  # Canonical resource-name prefix that the orphan-purge sweep (Go-side
  # at internal/providers/huawei/ Wave 3) will look for. Pattern mirrors
  # Hetzner's `catalyst-<fqdn-slug>`.
  name_prefix = "catalyst-${local.fqdn_slug}"

  # Canonical TMS tags per PROVIDER-INTERFACE.md §3 (label conventions).
  # Same key shape as Hetzner; Huawei TMS happens to accept k=v lists.
  common_tags = {
    "catalyst.openova.io/sovereign"     = var.sovereign_fqdn
    "catalyst.openova.io/deployment-id" = var.deployment_id
    "catalyst.openova.io/managed-by"    = "catalyst-zero"
  }
}

# ── VPC per region ────────────────────────────────────────────────────────
resource "huaweicloud_vpc" "region" {
  for_each = { for r in var.regions : r.code => r }

  name = "${local.name_prefix}-${each.key}-vpc"
  cidr = local.region_vpc_cidr[each.key]
  # tags = merge(local.common_tags, {
  # "catalyst.openova.io/region" = each.key
  # })  # disabled Wave 5.4: HCS tag API divergence
}

resource "huaweicloud_vpc_subnet" "region" {
  for_each = { for r in var.regions : r.code => r }

  name              = "${local.name_prefix}-${each.key}-subnet"
  cidr              = local.region_subnet_cidr[each.key]
  gateway_ip        = cidrhost(local.region_subnet_cidr[each.key], 1)
  vpc_id            = huaweicloud_vpc.region[each.key].id
  availability_zone = var.huawei_az
  # tags = merge(local.common_tags, {
  # "catalyst.openova.io/region" = each.key
  # })  # disabled Wave 5.4: HCS tag API divergence
}

# ── Security group per region ─────────────────────────────────────────────
#
# Default-deny ingress with explicit allow rules for:
#   - TCP 443  — Cilium Gateway (HTTPS) from 0/0
#   - TCP 80   — Cilium Gateway (HTTP, ACME HTTP-01 + .well-known) from 0/0
#   - TCP 6443 — k3s apiserver from 0/0 (fail-closed by k8s RBAC,
#                not by firewall — same shape Hetzner module ships)
#   - UDP 51820 — Cilium WireGuard inter-region (DMZ-WG)
#   - TCP 30000-32767 — k8s NodePort range (clustermesh-apiserver +
#                DMZ-WG bootstrapping)
#   - TCP 22  — only when ssh_allowed_cidrs is non-empty
#   - ICMP    — for path-MTU + ping diagnostics
#
# Egress is wide-open (default Huawei behavior); the Sovereign needs
# unconstrained egress to pull from ghcr.io/harbor.openova.io/github.com
# until bp-self-sovereign-cutover flips it.
resource "huaweicloud_networking_secgroup" "region" {
  for_each = { for r in var.regions : r.code => r }

  name        = "${local.name_prefix}-${each.key}-sg"
  description = "Catalyst Sovereign ${var.sovereign_fqdn} security group for region ${each.key}"
}

resource "huaweicloud_networking_secgroup_rule" "ingress_443" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 443
  port_range_max    = 443
  remote_ip_prefix  = "0.0.0.0/0"
  description       = "Cilium Gateway HTTPS"
}

resource "huaweicloud_networking_secgroup_rule" "ingress_80" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 80
  port_range_max    = 80
  remote_ip_prefix  = "0.0.0.0/0"
  description       = "Cilium Gateway HTTP (ACME + .well-known)"
}

resource "huaweicloud_networking_secgroup_rule" "ingress_6443" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 6443
  port_range_max    = 6443
  remote_ip_prefix  = "0.0.0.0/0"
  description       = "k3s apiserver (fail-closed via k8s RBAC, mTLS-protected)"
}

resource "huaweicloud_networking_secgroup_rule" "ingress_wireguard" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "udp"
  port_range_min    = 51820
  port_range_max    = 51820
  remote_ip_prefix  = "0.0.0.0/0"
  description       = "Cilium WireGuard inter-region (DMZ-WG, static-key crypto is the security boundary)"
}

resource "huaweicloud_networking_secgroup_rule" "ingress_nodeport" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 30000
  port_range_max    = 32767
  remote_ip_prefix  = "0.0.0.0/0"
  description       = "k8s NodePort range (clustermesh-apiserver mTLS LB, cross-region traffic)"
}

resource "huaweicloud_networking_secgroup_rule" "ingress_icmp" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "icmp"
  remote_ip_prefix  = "0.0.0.0/0"
  description       = "ICMP (path-MTU + ping diagnostics)"
}

# SSH — narrow rule per ssh_allowed_cidrs. When the list is empty no
# rule lands; break-glass is via HCS console (out-of-band).
resource "huaweicloud_networking_secgroup_rule" "ingress_ssh" {
  for_each = {
    for pair in setproduct(local.region_keys, var.ssh_allowed_cidrs) :
    "${pair[0]}|${pair[1]}" => { region = pair[0], cidr = pair[1] }
  }
  security_group_id = huaweicloud_networking_secgroup.region[each.value.region].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_ip_prefix  = each.value.cidr
  description       = "SSH from operator CIDR ${each.value.cidr}"
}

# ── Per-CP EIP (one per CP node) ──────────────────────────────────────────
#
# Workers have no EIP — they only need cluster-internal connectivity.
# Tier-B requirement: 3 CP per region × N regions × 1 EIP each. With
# the canonical 2-region Tier-B that's 6 EIPs of the 10 HCS quota.
#
# `huaweicloud_vpc_eip` carries no VPC binding by itself — it's a
# floating public IPv4 that we attach to the ECS instance via the
# instance's eip_id field (huaweicloud_compute_instance below).

locals {
  # Flatten per-region CP indexes into a single ordered list so the
  # count-based EIP + ECS resources stay in lockstep. Each entry maps
  # to one CP node across all regions.
  cp_nodes = flatten([
    for r in var.regions : [
      for i in range(3) : {
        region = r.code
        index  = i
        flavor = local.effective_cp_flavor[r.code]
      }
    ]
  ])

  # Same shape for workers — 2 per region per Tier-B sizing.
  worker_nodes = flatten([
    for r in var.regions : [
      for i in range(2) : {
        region = r.code
        index  = i
        flavor = local.effective_worker_flavor[r.code]
      }
    ]
  ])

  # Worker map keyed by `<region>-<index>` so we can derive a stable
  # for_each key without relying on order. Used by outputs.tf to
  # filter workers by region.
  worker_map = {
    for w in local.worker_nodes :
    "${w.region}-${w.index}" => w
  }
}

resource "huaweicloud_vpc_eip" "cp" {
  count = length(local.cp_nodes)

  publicip {
    type = "5_bgp"
  }
  bandwidth {
    name        = "${local.name_prefix}-${local.cp_nodes[count.index].region}-cp${local.cp_nodes[count.index].index + 1}-bw"
    size        = 100
    share_type  = "PER"
    charge_mode = "traffic"
  }
  # tags = merge(local.common_tags, {
  # "catalyst.openova.io/region" = local.cp_nodes[count.index].region
  #     "catalyst.openova.io/role"   = "control-plane"
  # })  # disabled Wave 5.4: HCS tag API divergence
}

# ── Cloud-init render (control-plane + worker) ────────────────────────────
#
# Same comment-stripping regex the Hetzner module uses to land rendered
# user-data under cloud-provider user-data size caps. HCS does not
# document a hard cap on user_data the way Hetzner does (32 KiB) but
# we keep the same conservative budget so the same template scales to
# fresh-prov against either cloud without per-provider trimming.

locals {
  worker_cloud_init = replace(templatefile("${path.module}/cloudinit-worker.tftpl", {
    sovereign_fqdn             = var.sovereign_fqdn
    k3s_version                = var.k3s_version
    k3s_token                  = sha256("${var.huawei_project_id}/${var.sovereign_fqdn}/k3s-bootstrap")
    cp_private_ip              = cidrhost(local.region_subnet_cidr[local.region_keys[0]], 2)
    enable_unattended_upgrades = var.enable_unattended_upgrades
    enable_fail2ban            = var.enable_fail2ban
  }), "/(?m)^[ ]*#( |$).*\n/", "")

  control_plane_cloud_init = replace(templatefile("${path.module}/cloudinit-control-plane.tftpl", {
    sovereign_fqdn      = var.sovereign_fqdn
    sovereign_fqdn_slug = local.fqdn_slug
    deployment_id       = var.deployment_id
    org_name            = var.org_name
    org_email           = var.org_email
    region              = var.regions[0].code
    huawei_region       = var.huawei_region
    huawei_az           = var.huawei_az
    k3s_version         = var.k3s_version
    k3s_token           = sha256("${var.huawei_project_id}/${var.sovereign_fqdn}/k3s-bootstrap")
    cp_private_ip       = cidrhost(local.region_subnet_cidr[local.region_keys[0]], 2)
    cluster_cidr        = "10.42.0.0/16"
    service_cidr        = "10.96.0.0/16"
    gitops_repo_url     = var.gitops_repo_url
    gitops_branch       = var.gitops_branch
    parent_domains_yaml = coalesce(
      var.parent_domains_yaml,
      format("[{name: \"%s\", role: \"primary\"}]", var.sovereign_fqdn)
    )
    ghcr_pull_username         = local.ghcr_pull_username
    ghcr_pull_token            = var.ghcr_pull_token
    ghcr_pull_auth_b64         = local.ghcr_pull_auth_b64
    obs_endpoint               = "https://obs.${var.huawei_region}.kom4dc.nationalcloud.om"
    obs_region                 = var.huawei_region
    obs_bucket_name            = var.obs_bucket_name
    obs_access_key             = var.huawei_access_key
    obs_secret_key             = var.huawei_secret_key
    handover_jwt_public_key    = var.handover_jwt_public_key
    kubeconfig_bearer_token    = var.kubeconfig_bearer_token
    catalyst_api_url           = var.catalyst_api_url
    enable_unattended_upgrades = var.enable_unattended_upgrades
    enable_fail2ban            = var.enable_fail2ban
    worker_cloud_init_b64      = base64encode(local.worker_cloud_init)
  }), "/(?m)^[ ]*#( |$).*\n/", "")
}

# ── Control-plane ECS instances ───────────────────────────────────────────

resource "huaweicloud_compute_instance" "control_plane" {
  count = length(local.cp_nodes)

  name              = "${local.name_prefix}-${local.cp_nodes[count.index].region}-cp${local.cp_nodes[count.index].index + 1}"
  image_id          = var.image_id
  flavor_id         = local.cp_nodes[count.index].flavor
  availability_zone = var.huawei_az
  security_groups   = [huaweicloud_networking_secgroup.region[local.cp_nodes[count.index].region].name]

  network {
    uuid = huaweicloud_vpc_subnet.region[local.cp_nodes[count.index].region].id
  }

  # Attach the per-node EIP. The provider supports `eip_id` on the
  # instance resource for direct binding without a separate
  # `huaweicloud_compute_eip_associate`.
  eip_id = huaweicloud_vpc_eip.cp[count.index].id

  user_data = local.control_plane_cloud_init

  key_pair = huaweicloud_kps_keypair.main.name

  # System disk type — Wave 5.6 (Refs #2140). The huaweicloud provider
  # defaults to GPSSD which is a public Huawei Cloud SKU; HCS me-east-215
  # exposes only `SSD` and `SAS` (queried via EVS cinder list-volume-types
  # 2026-05-22T20:21Z). Pin to SSD for production-appropriate IOPS.
  # Caught live on c928d81d3256535c: `Ecs.0005 rootVolume type[GPSSD]
  # is not exist`.
  system_disk_type = "SSD"
  system_disk_size = 40

  # tags = merge(local.common_tags, {

  # "catalyst.openova.io/region" = local.cp_nodes[count.index].region

  #     "catalyst.openova.io/role"   = "control-plane"

  # })  # disabled Wave 5.4: HCS tag API divergence
}

# ── Worker ECS instances ──────────────────────────────────────────────────
#
# Workers have NO EIP — only cluster-internal connectivity. They join
# k3s via the CP's private 10.x.1.2 IP inside the region's VPC.

resource "huaweicloud_compute_instance" "worker" {
  for_each = local.worker_map

  name              = "${local.name_prefix}-${each.value.region}-w${each.value.index + 1}"
  image_id          = var.image_id
  flavor_id         = each.value.flavor
  availability_zone = var.huawei_az
  security_groups   = [huaweicloud_networking_secgroup.region[each.value.region].name]

  network {
    uuid = huaweicloud_vpc_subnet.region[each.value.region].id
  }

  user_data = local.worker_cloud_init

  key_pair = huaweicloud_kps_keypair.main.name

  # System disk type — see CP block comment above (Wave 5.6 / Refs #2140).
  system_disk_type = "SSD"
  system_disk_size = 40

  # tags = merge(local.common_tags, {

  # "catalyst.openova.io/region" = each.value.region

  #     "catalyst.openova.io/role"   = "worker"

  # })  # disabled Wave 5.4: HCS tag API divergence
}

# ── SSH key (KPS keypair) ─────────────────────────────────────────────────

resource "huaweicloud_kps_keypair" "main" {
  name       = "${local.name_prefix}-key"
  public_key = var.ssh_public_key
}

# ── OBS bucket (S3-protocol, mirrors Hetzner Object Storage shape) ────────
#
# Same `hashicorp/aws` S3-protocol pattern the Hetzner module uses:
# create + acl + tagging via vanilla S3 against the HCS OBS endpoint
# (`obs.<region>.kom4dc.nationalcloud.om`). The Huawei-native
# `huaweicloud_obs_bucket` resource exists in the provider but it
# requires extra OBS-specific endpoint configuration that diverges from
# the cross-cloud "treat object storage as a vanilla S3 endpoint"
# pattern Wave 4 plans to extract. Keeping the aws-provider path keeps
# the bucket purge code one shape across providers.

# OBS bucket via the native `huaweicloud_obs_bucket` resource.
#
# Wave 5.5 (Refs #2140) — switched from `aws_s3_bucket` because the aws
# provider's auto-refresh always issues GetBucketEncryption against the
# bucket, and HCS OBS does not expose that API (returns 404
# NoSuchEncryptionConfiguration on deployment 0bbf240540c9351b
# 2026-05-22T20:08Z). The Huawei-native resource doesn't call any
# SSE-config side-read, so the bucket creates cleanly. Cross-cloud
# bucket-purge symmetry stays intact — the Go-side purge talks vanilla
# S3 protocol against the same OBS endpoint regardless of which
# Terraform resource minted the bucket.
resource "huaweicloud_obs_bucket" "main" {
  bucket = var.obs_bucket_name
  acl    = "private"

  # tags disabled per Wave 5.4 — HCS tag-API divergence
}
