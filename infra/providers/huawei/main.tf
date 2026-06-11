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

  # Per-region VPC CIDR — second octet derived from deployment_id hash
  # so re-provisions of the same FQDN (or sibling Sovereigns sharing a
  # project) NEVER collide on VPC CIDR. Range 10.32-10.247 (216 buckets)
  # avoids the canonical 10.0/24 management range and the 10.250+ reserved
  # range. Each region gets `base + idx*10` (so a 2-region deploy occupies
  # two distinct /16s).
  #
  # Wave 5.27 (Refs #2191 follow-up): caught live on 22nd attempt
  # ac117d1e — VPC creation rejected because 19th/20th attempts left
  # orphaned VPCs at 10.20.0.0/16 + 10.30.0.0/16 in HCS. Per Principle
  # 14 (target-state, no workarounds), per-deployment CIDR is the
  # canonical fix: tofu state can be lost, manual SSH-debug can
  # de-sync state, orphan-purge sweeps can lag — fresh CIDR per prov
  # is the only collision-proof shape.
  #
  # Inter-VPC routing is via DMZ WireGuard over PUBLIC EIPs (per
  # docs/DOD.md A2), so a fresh CIDR per prov is transparent.
  cidr_base = 32 + (parseint(substr(sha256(var.deployment_id), 0, 2), 16) % 216)
  region_vpc_cidr = {
    for idx, r in var.regions :
    r.code => format("10.%d.0.0/16", local.cidr_base + idx * 10)
  }

  # Per-region subnet CIDR — uniform /24 inside each /16 since the
  # regions don't share a routing domain.
  region_subnet_cidr = {
    for idx, r in var.regions :
    r.code => format("10.%d.1.0/24", local.cidr_base + idx * 10)
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
  # Hetzner's `catalyst-<fqdn-slug>` BUT also includes the first 8 hex
  # chars of the deployment_id so re-provisioning the same FQDN never
  # collides on resource names (HCS rejects same-name KPS keypair / VPC /
  # OBS bucket / EIP / NAT with HTTP 409 "could not be processed due to
  # conflict in the request").
  #
  # Wave 5.26 (Refs #2191): caught live on 20th attempt 51fcc636d958c26a
  # — KPS keypair "catalyst-hw01-omani-works-key" collided with the
  # orphaned keypair from 19th attempt c5da3542 that was not cleaned up
  # because manual SSH-debug had broken tofu state on the prior prov.
  #
  # The orphan-purge sweep (Go-side ListServers / OBS bucket sweep) keys
  # off TMS tag `catalyst.openova.io/deployment-id`, not the name —
  # different per-prov names do NOT break orphan cleanup.
  name_prefix = "catalyst-${local.fqdn_slug}-${substr(var.deployment_id, 0, 8)}"

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
  # tags disabled Wave 5.4 (HCS tag API divergence).

  # HCS VPC read-back occasionally surfaces a synthesised `tags = []`
  # even when none were sent; ignore to keep plan clean.
  lifecycle {
    ignore_changes = [tags]
  }
}

# ── Cross-VPC peering for the multi-region mesh datapath ─────────────────
#
# kom4dc is ONE physical region; "multi-region" is mimicked via one VPC
# per RegionSpec. ClusterMesh CONTROL traffic dials public
# nodeEIP:NodePort (works without peering), but the POD DATAPATH —
# global-service backends, cnpg-pair WAL streaming — routes to peer NODE
# INTERNAL IPs, which are unroutable across unpeered VPCs. Verified live
# on hw128 (#3241 layer 6, 2026-06-11): with both regions 1/1 meshed the
# replica's pg_basebackup timed out against the primary-mesh global
# service for an hour; creating this exact peering + the two routes via
# the Huawei API unblocked it within one retry (and the region-kill walk
# then PASSED with a 3s promote). Intra-region VPC peering is free and
# consumes no EIP quota.
#
# Pairwise full mesh over the region list (kom4dc reality is 2 VPCs =
# one pair). Peering requester/accepter live in the same tenant, so the
# connection auto-activates — no accepter resource needed.
locals {
  vpc_peering_pairs = {
    for pair in flatten([
      for i, a in var.regions : [
        for j, b in var.regions : {
          key = "${a.code}--${b.code}"
          a   = a.code
          b   = b.code
        } if j > i
      ]
    ]) : pair.key => pair
  }
}

resource "huaweicloud_vpc_peering_connection" "mesh" {
  for_each = local.vpc_peering_pairs

  # Name capped at 64 chars (HCS limit): the raw region-pair key
  # overflows on long region codes (me-east-215-a--me-east-215-b), so
  # use a stable 8-hex digest of the pair key instead.
  name        = "${local.name_prefix}-peer-${substr(sha256(each.key), 0, 8)}"
  vpc_id      = huaweicloud_vpc.region[each.value.a].id
  peer_vpc_id = huaweicloud_vpc.region[each.value.b].id
}

resource "huaweicloud_vpc_route" "mesh_a_to_b" {
  for_each = local.vpc_peering_pairs

  vpc_id      = huaweicloud_vpc.region[each.value.a].id
  destination = local.region_vpc_cidr[each.value.b]
  type        = "peering"
  nexthop     = huaweicloud_vpc_peering_connection.mesh[each.key].id
}

resource "huaweicloud_vpc_route" "mesh_b_to_a" {
  for_each = local.vpc_peering_pairs

  vpc_id      = huaweicloud_vpc.region[each.value.b].id
  destination = local.region_vpc_cidr[each.value.a]
  type        = "peering"
  nexthop     = huaweicloud_vpc_peering_connection.mesh[each.key].id
}

resource "huaweicloud_vpc_subnet" "region" {
  for_each = { for r in var.regions : r.code => r }

  name              = "${local.name_prefix}-${each.key}-subnet"
  cidr              = local.region_subnet_cidr[each.key]
  gateway_ip        = cidrhost(local.region_subnet_cidr[each.key], 1)
  vpc_id            = huaweicloud_vpc.region[each.key].id
  availability_zone = var.huawei_az
  # tags disabled Wave 5.4 (HCS tag API divergence).

  # HCS subnet read-back surfaces `dns_list`, `dhcp_lease_time`,
  # `primary_dns`, `secondary_dns`, `tags` synthesised from the VPC
  # default — ignore so subsequent plans don't churn.
  lifecycle {
    ignore_changes = [tags, dns_list, primary_dns, secondary_dns, dhcp_lease_time]
  }
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

# Wave 5.82 (Refs #2419): optional 22/tcp ingress for recovery-script SSH.
# Off by default (canonical posture). Toggle via `debug_ssh_enabled = true`
# in tfvars when running Wave 5.75 recovery-script flow; toggle back when
# done. Keeps secgroup-rule lifecycle in tofu state instead of drifting
# via manual Huawei console fiddling.
resource "huaweicloud_networking_secgroup_rule" "ingress_22_debug" {
  for_each = var.debug_ssh_enabled ? { for r in var.regions : r.code => r } : {}

  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 22
  port_range_max    = 22
  remote_ip_prefix  = var.debug_ssh_remote_cidr
  description       = "SSH (debug-ssh-toggle ON via debug_ssh_enabled=true)"
}

# Wave 5.30 (Refs #2163): Cilium tunnel/health/encryption ports — node↔node
# encap and cilium-health checks must be reachable for pod-to-pod traffic
# across nodes. Without these rules, Cilium VXLAN encap packets (or
# alternative GENEVE/WG-encrypted) get silently dropped by the HCS SDN
# even when source_dest_check is off. Per Huawei UCS Cilium prerequisites:
# https://support.huaweicloud.com/intl/en-us/usermanual-ucs/ucs_01_0204.html

# Cilium VXLAN tunnel default port (8472/UDP — NOT 4789).
resource "huaweicloud_networking_secgroup_rule" "ingress_cilium_vxlan" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "udp"
  port_range_min    = 8472
  port_range_max    = 8472
  remote_ip_prefix  = local.region_subnet_cidr[each.key]
  description       = "Cilium VXLAN tunnel (intra-region pod-to-pod encap)"
}

# Cilium GENEVE alternative encap (6081/UDP).
resource "huaweicloud_networking_secgroup_rule" "ingress_cilium_geneve" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "udp"
  port_range_min    = 6081
  port_range_max    = 6081
  remote_ip_prefix  = local.region_subnet_cidr[each.key]
  description       = "Cilium GENEVE tunnel (alternative encap mode)"
}

# Cilium WireGuard transparent encryption (51871/UDP — Cilium uses
# this, NOT the kernel-WG default 51820 that ingress_wireguard above
# opens for DMZ-WG inter-region).
resource "huaweicloud_networking_secgroup_rule" "ingress_cilium_wireguard" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "udp"
  port_range_min    = 51871
  port_range_max    = 51871
  remote_ip_prefix  = "0.0.0.0/0"
  description       = "Cilium WireGuard transparent encryption (port 51871, not 51820)"
}

# Cilium health probes (4240/TCP — node-to-node L3 reachability).
resource "huaweicloud_networking_secgroup_rule" "ingress_cilium_health" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 4240
  port_range_max    = 4240
  remote_ip_prefix  = local.region_subnet_cidr[each.key]
  description       = "Cilium cilium-health (node reachability probes)"
}

# Wave 5.30 (Refs #2163): pod CIDR allow-all from same-region subnet so
# any pod-to-pod traffic that escapes encap (e.g. host-gw / native routing
# fallback) is also permitted. Defence-in-depth alongside source_dest_check=false.
# HCS rejects "any" protocol — list specific protocols Cilium pod-to-pod uses.
resource "huaweicloud_networking_secgroup_rule" "ingress_pod_cidr_tcp" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  remote_ip_prefix  = "10.42.0.0/16"
  description       = "Pod CIDR TCP all-ports (Cilium pod-to-pod fallback path)"
}
resource "huaweicloud_networking_secgroup_rule" "ingress_pod_cidr_udp" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "udp"
  remote_ip_prefix  = "10.42.0.0/16"
  description       = "Pod CIDR UDP all-ports (Cilium pod-to-pod fallback path)"
}
resource "huaweicloud_networking_secgroup_rule" "ingress_pod_cidr_icmp" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "icmp"
  remote_ip_prefix  = "10.42.0.0/16"
  description       = "Pod CIDR ICMP (Cilium pod-to-pod diagnostics)"
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
  #
  # Wave 5.7 (Refs #2140): per-region CP count now comes from
  # `huawei_control_plane_count` (default 1 for HCS me-east-215). The
  # historical fixed-3 layout (Hetzner-style HA per region) consumed
  # 3 EIPs × N regions = 6 EIPs for a 2-region Tier-B Sovereign, which
  # exhausted the HCS publicIp quota (10) and left no headroom for
  # the mothership + a second Sovereign on the same HCS tenancy.
  # Live evidence 2026-05-22T20:35Z: `error allocating EIP: The request
  # could not be processed due to conflict in the request` was the
  # provider's translation of the publicIp quota=10 used=10 error
  # (verified via `eip:ListQuotas`).
  cp_nodes = flatten([
    for r in var.regions : [
      for i in range(var.huawei_control_plane_count) : {
        region = r.code
        index  = i
        flavor = local.effective_cp_flavor[r.code]
      }
    ]
  ])

  # Per-region worker count comes from `regions[].worker_count` (the
  # cross-provider PROVIDER-INTERFACE.md §1 shape — set by the wizard
  # / Go provisioner). Default 3 if zero. Wave 5.108 (#2462) had bumped
  # 3→4 for k3s-agent install reliability, but cloud-init now ships 10×
  # retry on the get.k3s.io curl so the +1 buffer is no longer required.
  # Founder direction 2026-05-28 (Refs #2536): a 2-region Sovereign with
  # 3 vClusters per region (MGMT+RTZ+DMZ, Refs #2537) needs ~6 ECS
  # workers total, not 16. Tier-B HCS cost-fit. Walks pass on 3 workers.
  worker_nodes = flatten([
    for r in var.regions : [
      for i in range(r.worker_count > 0 ? r.worker_count : 3) : {
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

  # Wave 5.7: one EIP per region, attached to that region's primary CP
  # (index 0). Reduces the per-Sovereign EIP footprint from N_regions ×
  # N_cps to N_regions (2 for the canonical 2-region Tier-B), fitting
  # inside the HCS publicIp quota=10 with mothership headroom. The
  # primary CP IS the WireGuard DMZ endpoint for its region (DOD A2 —
  # inter-region traffic flows over public EIPs), and ClusterMesh
  # apiserver peers dial the OTHER region's primary CP EIP. Failure of
  # a primary CP demotes its region (k3s replica CPs continue serving
  # the region from private IPs; cluster-internal load-balancing via
  # Cilium); cross-region HA is preserved at the region granularity
  # which IS the DR contract.
  cp_eip_regions = [for r in var.regions : r.code]
}

resource "huaweicloud_vpc_eip" "cp" {
  for_each = toset(local.cp_eip_regions)

  publicip {
    type = "5_bgp"
  }
  bandwidth {
    name        = "${local.name_prefix}-${each.key}-cp1-bw"
    size        = 100
    share_type  = "PER"
    charge_mode = "traffic"
  }
  # tags disabled Wave 5.4 (HCS tag-API divergence; restored once HCS TMS endpoint contract stabilises).

  # HCS EIP read-back occasionally surfaces a `tags` field even when
  # none were sent on create — pin to ignore to prevent perpetual diff.
  lifecycle {
    ignore_changes = [tags]
  }
}

# ── NAT Gateway for worker egress (Wave 5.21, Refs #2140) ─────────────────
#
# Workers (no EIP per A2) need internet to apt-install + curl get.k3s.io
# during cloud-init. Without NAT, workers' apt update times out + k3s
# install never runs → only the CP joins the cluster, all Pods Pending
# Insufficient CPU. Caught live on 18th deployment 57f82ce11ed0c899:
# worker SSH revealed "curl get.k3s.io: Could not resolve host" + apt
# halt. One NAT Gateway + one SNAT rule per VPC gives transparent egress
# for the entire region subnet (workers + CP). Egress flows: worker →
# NAT GW → NAT EIP → internet. No per-worker EIP needed (keeps HCS
# publicIp quota=10 comfortable: 2 CP + 2 NAT = 4 EIPs).
resource "huaweicloud_nat_gateway" "region" {
  for_each = toset(local.region_keys)

  name        = "${local.name_prefix}-${each.key}-nat"
  spec        = "1" # small — up to 10K connections, sufficient for POC
  vpc_id      = huaweicloud_vpc.region[each.key].id
  subnet_id   = huaweicloud_vpc_subnet.region[each.key].id
  description = "Catalyst worker egress for region ${each.key}"

  lifecycle {
    ignore_changes = [tags]
  }
}

resource "huaweicloud_vpc_eip" "nat" {
  for_each = toset(local.region_keys)

  publicip {
    type = "5_bgp"
  }
  bandwidth {
    name        = "${local.name_prefix}-${each.key}-nat-bw"
    size        = 100
    share_type  = "PER"
    charge_mode = "traffic"
  }

  lifecycle {
    ignore_changes = [tags]
  }
}

# HCS NAT plane has eventual consistency with the EIP service — the EIP
# registers in the VPC plane immediately but the NAT API takes up to 15s
# to propagate visibility. Creating the SNAT rule without waiting returns
# VPC.2030 "Floating IP not found". Witnessed live on hw36 d26632c3e536c2f9
# (2026-05-28). 15s is empirically sufficient; the HCS docs mention a
# "max 30s" propagation window for EIP state changes.
resource "time_sleep" "nat_eip_propagation" {
  for_each = toset(local.region_keys)

  depends_on      = [huaweicloud_vpc_eip.nat]
  create_duration = "15s"
}

resource "huaweicloud_nat_snat_rule" "region" {
  for_each = toset(local.region_keys)

  nat_gateway_id = huaweicloud_nat_gateway.region[each.key].id
  floating_ip_id = huaweicloud_vpc_eip.nat[each.key].id
  subnet_id      = huaweicloud_vpc_subnet.region[each.key].id

  depends_on = [time_sleep.nat_eip_propagation]
}

# ── Cloud-init render (control-plane + worker) ────────────────────────────
#
# Same comment-stripping regex the Hetzner module uses to land rendered
# user-data under cloud-provider user-data size caps. HCS does not
# document a hard cap on user_data the way Hetzner does (32 KiB) but
# we keep the same conservative budget so the same template scales to
# fresh-prov against either cloud without per-provider trimming.

locals {
  # ── #3145 cloud-agnostic bootstrap: Huawei provider-injected strings ───
  # The Kubernetes bootstrap lives in infra/providers/_shared/cloudinit-
  # control-plane.tftpl (ONE template, every cloud). These locals carry the
  # ONLY Huawei-specific pieces (the "hard dependency" exceptions, plan §5):
  # bastion registry mirror (kom4dc NAT bypass), the iptables DNAT +
  # console-less log self-upload prelude (#3132), huawei-ak/sk creds, and the
  # HCS-specific k3s flags (watch-cache tuning + topology labels). The EIP is
  # per-region so node_external_ip_cmd / kubeconfig_put_block are built inline
  # in the cp_cloud_init_by_region loop below.

  # registry mirror — Huawei routes pulls through the bastion-openova
  # registry:2 caches on direct EIP 212.72.24.20 (kom4dc NAT blocklist bypass,
  # Wave 5.106 #2462). ghcr/quay/kyverno pull DIRECT (bastion is anon-only, can't
  # auth private ghcr); harbor + docker.io mirror via :5000 / :5002 (#2842).
  registry_mirror_yaml_huawei = <<-EOT
    mirrors:
      "harbor.openova.io":
        endpoint:
          - "http://212.72.24.20:5000"
      "docker.io":
        endpoint:
          - "http://212.72.24.20:5002"
      "registry-1.docker.io":
        endpoint:
          - "http://212.72.24.20:5002"
    configs:
      "212.72.24.20:5000":
        tls:
          insecure_skip_verify: true
      "212.72.24.20:5002":
        tls:
          insecure_skip_verify: true
      "harbor.openova.io":
        auth:
          username: "robot$openova-bot"
          password: "${var.harbor_robot_token}"
  EOT

  # cloud-credentials Secret — Huawei AK/SK (#425 vendor-agnostic name).
  cloud_credentials_secret_yaml_huawei = <<-EOT
    apiVersion: v1
    kind: Secret
    metadata:
      name: cloud-credentials
      namespace: flux-system
    type: Opaque
    stringData:
      huawei-ak: "${var.huawei_access_key}"
      huawei-sk: "${var.huawei_secret_key}"
      huawei-project-id: "${var.huawei_region}"
      huawei-region: "${var.huawei_region}"
  EOT

  # provider prelude — Huawei. Runs BEFORE k3s (runcmd 0-indent items; the
  # shared template re-indents via indent(2,…)). Two hard dependencies:
  #  (1) iptables DNAT 443→30443 + 80→30080: Cilium Gateway on host-network
  #      binds envoy to high NodePorts; the tofu HCS ELB targets those, so the
  #      CP must REDIRECT privileged ports (Wave 5.96 #TBD-W5.96).
  #  (2) #3132 console-less log self-upload: kom4dc CPs have NO ECS console-
  #      output API + no reachable sshd, so /var/log/cloud-init-output.log can
  #      only be PUSHED. Backgrounded EARLY so it keeps uploading even if a
  #      later step aborts cloud-init — the founder's capture-before-wipe guard.
  # Plain POSIX (`nohup sh -c`, not bash; dash -n clean — no $(( )) here).
  provider_prelude_huawei = <<-EOT
    - ["bash", "-c", "iptables -t nat -A PREROUTING -p tcp --dport 443 -j REDIRECT --to-port 30443 || true"]
    - ["bash", "-c", "iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 30080 || true"]
    - ["bash", "-c", "iptables -t nat -A OUTPUT -p tcp -d 127.0.0.0/8 -j RETURN || true"]
    - ["bash", "-c", "mkdir -p /etc/iptables && iptables-save > /etc/iptables/rules.v4 2>/dev/null || true"]
    - |
      if [ -n "${var.deployment_id}" ] && [ -n "${var.kubeconfig_bearer_token}" ] && [ -n "${var.catalyst_api_url}" ]; then
        nohup sh -c '
          n=0
          while [ "$${n}" -lt 40 ]; do
            curl -sk -X PUT \
              -H "Authorization: Bearer ${var.kubeconfig_bearer_token}" \
              -H "Content-Type: text/plain" \
              --data-binary @/var/log/cloud-init-output.log \
              --max-time 20 \
              "${var.catalyst_api_url}/api/v1/deployments/${var.deployment_id}/cloudinit-log" >/dev/null 2>&1 || true
            n=$$((n+1))
            sleep 30
          done
        ' >/dev/null 2>&1 &
      fi
  EOT

  # HCS-specific k3s flags. NO OIDC (HCS Sovereigns don't wire kubectl-SSO at
  # bootstrap), but topology labels (CCM doesn't stamp them) + apiserver watch-
  # cache tuning (Wave 5.125 #2472 — IP recycling floods the default 100-entry
  # window → 410-Expired CNPG/cert-manager wedges) + etcd snapshot retention.
  k3s_extra_args_huawei = "--node-label=topology.kubernetes.io/region=REGION_PLACEHOLDER --node-label=topology.kubernetes.io/zone=REGION_PLACEHOLDER --node-label=catalyst.openova.io/provider=huawei --kube-apiserver-arg=default-watch-cache-size=200 --kube-apiserver-arg=watch-cache-sizes=secrets#500,configmaps#500 --etcd-snapshot-retention=5"

  # Wave 5.13 (Refs #2140): per-region worker cloud-init render with the
  # ACTUAL CP private IP per region (not the wrong `cidrhost(subnet,2)`
  # assumption). HCS DHCP does not assign deterministic .2 addresses —
  # workers on 974113d27f0e3666 tried to join 10.20.1.2 but the CP was
  # at a DHCP-assigned address. Only 1 node ever joined (the CP itself)
  # → Pods Pending with "Insufficient cpu" because the single CP had
  # no worker capacity. Use the resource attribute `access_ip_v4` for
  # the deterministic correct IP. Tofu DAG creates CPs first, then
  # renders worker cloud-init, then creates workers.
  #
  # Map keyed by region code so the for_each worker resource can pick
  # its OWN region's CP IP.
  cp_primary_private_ip_by_region = {
    for r in var.regions :
    r.code => huaweicloud_compute_instance.control_plane[
      index([for n in local.cp_nodes : "${n.region}-${n.index}"], "${r.code}-0")
    ].access_ip_v4
  }

  worker_cloud_init_by_region = {
    for r in var.regions :
    r.code => replace(templatefile("${path.module}/cloudinit-worker.tftpl", {
      sovereign_fqdn             = var.sovereign_fqdn
      k3s_version                = var.k3s_version
      k3s_token                  = sha256("${var.huawei_project_id}/${var.sovereign_fqdn}/k3s-bootstrap")
      cp_private_ip              = local.cp_primary_private_ip_by_region[r.code]
      enable_unattended_upgrades = var.enable_unattended_upgrades
      enable_fail2ban            = var.enable_fail2ban
      # G87 #2634 (2026-05-31): per-worker region key for K8s
      # topology labels (`topology.kubernetes.io/region` +
      # `openova.io/region`). Without these, the K8s scheduler's
      # topology spread + CNPG affinity + dashboard region grouping
      # all collapse to single-region. Huawei CCM doesn't stamp
      # standard topology labels by default; cloudinit fills the gap.
      region = r.code
      # #2842 (2026-06-03): harbor robot token for the registries.yaml
      # `configs.harbor.openova.io.auth` block — without this, every
      # uncached pull of an upstream image referencing harbor.openova.io
      # 401s and containerd loops on stale-ingest-locks. Mirrors the CP
      # template binding (line 788).
      harbor_robot_token = var.harbor_robot_token
    }), "/(?m)^[ ]*#( |$).*\n/", "")
  }

  # ── Per-region Cilium ClusterMesh anchors (Refs #2535 — G4) ───────────
  # Mirror Hetzner main.tf: primary inherits var.cluster_mesh_{name,id};
  # secondaries derive name = "<sovereign-stem>-<region-stem-no-digits>"
  # and id = primary + 1 + secondary-index. Auto-derive when var is empty.
  primary_cluster_mesh_name_effective = (
    var.cluster_mesh_name != "" ? var.cluster_mesh_name :
    "${split(".", var.sovereign_fqdn)[0]}-${replace(local.region_keys[0], "/[0-9]+/", "")}"
  )
  primary_cluster_mesh_id_effective = (
    var.cluster_mesh_id > 0 ? var.cluster_mesh_id : 1
  )
  cluster_mesh_name_by_region = {
    for idx, r in var.regions :
    r.code => (
      idx == 0 ? local.primary_cluster_mesh_name_effective :
      "${split(".", var.sovereign_fqdn)[0]}-${replace(r.code, "/[0-9]+/", "")}"
    )
  }
  cluster_mesh_id_by_region = {
    for idx, r in var.regions :
    r.code => (idx == 0 ? local.primary_cluster_mesh_id_effective : local.primary_cluster_mesh_id_effective + idx)
  }
  region_role_by_region = {
    for idx, r in var.regions :
    r.code => (idx == 0 ? "primary" : "secondary")
  }

  # ── Per-region control-plane cloud-init (Refs #2533 — G1) ────────────
  # Each CP gets its OWN region's cloud-init render so per-region node
  # labels, ClusterMesh anchors, cp_private_ip, and region role are
  # baked into the bootstrap. Mirrors Hetzner's
  # local.secondary_region_cloud_init pattern, but as a single map keyed
  # by region (the count-based control_plane resource looks up by
  # local.cp_nodes[count.index].region).
  # G76+G82 #2623/#2629 (2026-05-31): per-region cloud-init must know
  # its OWN index in var.regions so it can synthesise the canonical
  # `<cloudRegion>-<idx>` regionKey when PUTting back its kubeconfig
  # to mothership /api/v1/sovereign/secondary-kubeconfig. Without the
  # index suffix, mothership's d16-export waiter (`regionKeysForExport`
  # in deployment_handover_export.go) keeps waiting forever on a file
  # at `<depID>-<cloudRegion>-<idx>.yaml` that never appears (Hetzner
  # tofu uses `secondary_regions = { for i, r in var.regions :
  # "${r.cloudRegion}-${i}" => r if i > 0 }` — Huawei was previously
  # missing the index suffix and posted plain `r.code` as regionKey).
  # Net effect on every HCS multi-region prov: chroot never received
  # secondary kubeconfig → k8sCache only had primary → CNPG topology
  # showed single-region (#2629) + Jobs view missed region-B (#2623).
  # Verified live hw86 afc8800bc03751c6 2026-05-31: file landed at
  # `afc8800bc03751c6-me-east-215-b.yaml` while d16-export waited on
  # `afc8800bc03751c6-me-east-215-b-1.yaml` (12+min later still
  # waiting). Fix: index-aware for_each + indexed region key per
  # Hetzner convention.
  cp_cloud_init_by_region = {
    for idx, r in var.regions :
    r.code => replace(templatefile("${path.module}/../_shared/cloudinit-control-plane.tftpl", {
      # ── #3145: render the ONE cloud-agnostic bootstrap. Huawei-specific
      # pieces are injected as the *_huawei locals + per-region inline strings
      # (the "hard dependency" exceptions). Common vars identical to every cloud.
      provider = "huawei"

      sovereign_fqdn      = var.sovereign_fqdn
      sovereign_fqdn_slug = local.fqdn_slug
      deployment_id       = var.deployment_id
      org_name            = var.org_name
      org_email           = var.org_email
      # Refs #2533 — G1: per-CP region (not always primary).
      region = r.code
      # SOVEREIGN_REGION_KEY — Huawei passes the canonical label (preserves the
      # pre-#3145 Huawei template's `SOVEREIGN_REGION_KEY: ${region_canonical_label}`).
      sovereign_region_key  = "hw-${r.code}-rtz-prod"
      sovereign_region_role = local.region_role_by_region[r.code]
      cluster_mesh_name     = local.cluster_mesh_name_by_region[r.code]
      cluster_mesh_id       = tostring(local.cluster_mesh_id_by_region[r.code])
      k3s_version           = var.k3s_version
      k3s_token             = sha256("${var.huawei_project_id}/${var.sovereign_fqdn}/k3s-bootstrap")
      # Per-CP region's own subnet first-IP.
      cp_private_ip = cidrhost(local.region_subnet_cidr[r.code], 2)
      cluster_cidr  = "10.42.0.0/16"
      service_cidr  = "10.96.0.0/16"
      # Canonical region labels (G84 #2631: `hw` prefix, NOT `hu`).
      region_canonical_label         = "hw-${r.code}-rtz-prod"
      primary_region_canonical_label = "hw-${var.regions[0].code}-rtz-prod"
      replica_region_canonical_label = length(var.regions) > 1 ? "hw-${var.regions[1].code}-rtz-prod" : ""
      # #2940: per-Sovereign PDNS endpoint override (bootstrap-kit substitute).
      pdns_api_host   = var.pdns_api_host
      gitops_repo_url = var.gitops_repo_url
      gitops_branch   = var.gitops_branch
      parent_domains_yaml = coalesce(
        var.parent_domains_yaml,
        format("[{name: \"%s\", role: \"primary\"}]", var.sovereign_fqdn)
      )
      # #3110 — canonical RegionSpec[] JSON (re-key HCS var.regions to the Go
      # RegionSpec field names: cloudRegion=hw-<code>-rtz-prod, provider=huawei).
      sovereign_regions_json = jsonencode([
        for rr in var.regions : {
          provider         = "huawei"
          cloudRegion      = "hw-${rr.code}-rtz-prod"
          controlPlaneSize = rr.control_plane_size
          workerSize       = rr.worker_size
          workerCount      = rr.worker_count
        }
      ])
      sovereign_configured_regions_yaml = jsonencode([
        for rr in var.regions : "hw-${rr.code}-rtz-prod"
      ])
      bcp_topology             = var.bcp_topology
      enable_hot_standby       = var.enable_hot_standby
      enable_shared_pg         = var.enable_shared_pg
      sovereign_cnpg_instances = length(var.regions) > 1 ? "2" : "1"
      continuum_enabled        = length(var.regions) > 1 ? "true" : "false"
      marketplace_enabled      = var.marketplace_enabled
      wildcard_cert_issuer     = var.wildcard_cert_use_staging == "true" ? "letsencrypt-dns01-staging-powerdns" : "letsencrypt-dns01-prod-powerdns"

      ghcr_pull_username = local.ghcr_pull_username
      ghcr_pull_token    = var.ghcr_pull_token
      ghcr_pull_auth_b64 = local.ghcr_pull_auth_b64
      # Object storage — Huawei OBS via S3-compatible endpoint. Shared template
      # uses the vendor-agnostic object_storage_* contract.
      object_storage_endpoint    = "https://obs.${var.huawei_region}.kom4dc.nationalcloud.om"
      object_storage_region      = var.huawei_region
      object_storage_bucket_name = var.obs_bucket_name
      object_storage_access_key  = var.huawei_access_key
      object_storage_secret_key  = var.huawei_secret_key
      handover_jwt_public_key    = var.handover_jwt_public_key
      kubeconfig_bearer_token    = var.kubeconfig_bearer_token
      catalyst_api_url           = var.catalyst_api_url
      enable_unattended_upgrades = var.enable_unattended_upgrades
      enable_fail2ban            = var.enable_fail2ban
      powerdns_api_key           = var.powerdns_api_key
      pdm_basic_auth_user        = var.pdm_basic_auth_user
      pdm_basic_auth_pass        = var.pdm_basic_auth_pass
      harbor_robot_token         = var.harbor_robot_token
      # SOVEREIGN_LB_IP for the sovereign-tls Kustomization → the HCS ELB EIP
      # (the FQDN A-record points HERE, not the CP EIP).
      load_balancer_ipv4 = huaweicloud_vpc_eip.elb_primary.publicip.0.ip_address

      # ── Provider-injected strings (the §5 hard-dependency exceptions) ──
      registry_mirror_yaml          = local.registry_mirror_yaml_huawei
      cloud_credentials_secret_yaml = local.cloud_credentials_secret_yaml_huawei
      crossplane_provider_yaml      = "" # no Huawei Crossplane provider package yet
      provider_id_cmd               = "" # HCS uses native node labels, no providerID patch
      provider_prelude              = local.provider_prelude_huawei
      # Per-region k3s topology labels (this region's code).
      k3s_extra_args = "--node-label=topology.kubernetes.io/region=${r.code} --node-label=topology.kubernetes.io/zone=${r.code} --node-label=catalyst.openova.io/provider=huawei --kube-apiserver-arg=default-watch-cache-size=200 --kube-apiserver-arg=watch-cache-sizes=secrets#500,configmaps#500 --etcd-snapshot-retention=5"
      # node external IP — HCS metadata doesn't populate it, so use this
      # region's tofu-known CP EIP (printf, NOT curl). Hard dependency (plan §5).
      node_external_ip_cmd   = "printf '%s' '${huaweicloud_vpc_eip.cp[r.code].publicip.0.ip_address}'"
      node_external_ip_value = huaweicloud_vpc_eip.cp[r.code].publicip.0.ip_address
      # node PRIVATE IP — HCS DHCP is non-deterministic (Wave 5.29 #2163:
      # cidrhost(subnet,2) is wrong), so detect the real eth0 address at
      # runtime. Used for k3s --node-ip + cilium k8sServiceHost substitution.
      node_ip_cmd = "ip -4 -o addr show dev eth0 | awk '{print $4}' | cut -d/ -f1"

      # kubeconfig PUT-back — Huawei. Each CP decides at RUNTIME by hostname:
      #   primary CP (hostname == primary_cp_hostname) → PUT to /kubeconfig.
      #   secondary CP                                 → POST to /sovereign/secondary-kubeconfig.
      # Server URL rewritten to this region's EIP (cross-cloud reachability).
      # Both gated so only the right CP fires; runcmd 0-indent items.
      kubeconfig_put_block = <<-PUT
        - |
          HOSTNAME=$(hostname)
          PRIMARY_HOST='${local.name_prefix}-${local.region_keys[0]}-cp1-${substr(sha256("${var.deployment_id}-${local.region_keys[0]}-cp0-${var.retry_attempt}"), 0, 6)}'
          if [ -n "${var.deployment_id}" ] && [ -n "${var.kubeconfig_bearer_token}" ] && [ "$${HOSTNAME}" = "$${PRIMARY_HOST}" ]; then
            sed "s|server: https://127.0.0.1:6443|server: https://${huaweicloud_vpc_eip.cp[local.region_keys[0]].publicip.0.ip_address}:6443|" /etc/rancher/k3s/k3s.yaml > /tmp/kubeconfig-rewritten.yaml
            for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
              HTTP_CODE=$(curl -sk -o /dev/null -w '%%{http_code}' -X PUT \
                -H "Authorization: Bearer ${var.kubeconfig_bearer_token}" \
                -H "Content-Type: application/x-yaml" \
                --data-binary @/tmp/kubeconfig-rewritten.yaml \
                --max-time 15 \
                "${var.catalyst_api_url}/api/v1/deployments/${var.deployment_id}/kubeconfig" || echo "000")
              echo "primary kubeconfig PUT-back attempt $${attempt} -> HTTP $${HTTP_CODE}"
              if [ "$${HTTP_CODE}" = "204" ] || [ "$${HTTP_CODE}" = "200" ]; then break; fi
              sleep 30
            done
          fi
        - |
          HOSTNAME=$(hostname)
          PRIMARY_HOST='${local.name_prefix}-${local.region_keys[0]}-cp1-${substr(sha256("${var.deployment_id}-${local.region_keys[0]}-cp0-${var.retry_attempt}"), 0, 6)}'
          MY_REGION='${idx == 0 ? r.code : "${r.code}-${idx}"}'
          MY_EIP='${huaweicloud_vpc_eip.cp[r.code].publicip.0.ip_address}'
          if [ -n "${var.deployment_id}" ] && [ -n "${var.kubeconfig_bearer_token}" ] && [ "$${HOSTNAME}" != "$${PRIMARY_HOST}" ] && [ -n "$${MY_REGION}" ] && [ -n "$${MY_EIP}" ]; then
            sed "s|server: https://127.0.0.1:6443|server: https://$${MY_EIP}:6443|" /etc/rancher/k3s/k3s.yaml > /tmp/kubeconfig-secondary.yaml
            python3 -c "import json; print(json.dumps({'deploymentId':'${var.deployment_id}','regionKey':'$${MY_REGION}','kubeconfigYaml':open('/tmp/kubeconfig-secondary.yaml').read()}))" > /tmp/secondary-kubeconfig-body.json
            for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
              HTTP_CODE=$(curl -sk -o /dev/null -w '%%{http_code}' -X POST \
                -H "Authorization: Bearer ${var.kubeconfig_bearer_token}" \
                -H "Content-Type: application/json" \
                --data-binary @/tmp/secondary-kubeconfig-body.json \
                --max-time 15 \
                "${var.catalyst_api_url}/api/v1/sovereign/secondary-kubeconfig" || echo "000")
              echo "secondary kubeconfig PUT-back attempt $${attempt} -> HTTP $${HTTP_CODE}"
              if [ "$${HTTP_CODE}" = "204" ] || [ "$${HTTP_CODE}" = "200" ]; then break; fi
              sleep 30
            done
            rm -f /tmp/secondary-kubeconfig-body.json
          fi
      PUT
    }), "/(?m)^[ ]*#( |$).*\n/", "")
  }
}

# ── Control-plane ECS instances ───────────────────────────────────────────

resource "huaweicloud_compute_instance" "control_plane" {
  count = length(local.cp_nodes)

  # Wave 5.144 (hw30 #20 fix-forward 2026-05-27): CP name also carries
  # the retry_attempt salt. Without it, Wave 5.139 only randomized
  # WORKER names; the CP name stayed static across retries so HCS
  # scheduler kept picking the same broken cell on every retry. hw30
  # #20 hit Common.0021 on CP across both attempts at 04:20Z + 04:23Z
  # with parallelism=2 — same cell, same failure. The 6hex salt
  # tail `cp<count+1>-<6hex>` differs per retry, so HCS picks fresh
  # cells. lifecycle.ignore_changes=[name] below protects an already-
  # ACTIVE CP from being destroyed when the formula changes.
  name              = "${local.name_prefix}-${local.cp_nodes[count.index].region}-cp${local.cp_nodes[count.index].index + 1}-${substr(sha256("${var.deployment_id}-${local.cp_nodes[count.index].region}-cp${local.cp_nodes[count.index].index}-${var.retry_attempt}"), 0, 6)}"
  image_id          = var.image_id
  flavor_id         = local.cp_nodes[count.index].flavor
  availability_zone = var.huawei_az
  security_groups   = [huaweicloud_networking_secgroup.region[local.cp_nodes[count.index].region].name]

  network {
    uuid = huaweicloud_vpc_subnet.region[local.cp_nodes[count.index].region].id
    # Wave 5.30 (Refs #2163): HCS default port_security_enabled=true drops
    # any packet whose source IP is not the port's primary fixed IP. Pod
    # CIDR packets (10.42.x.y src) are silently dropped → Cilium/Flannel
    # cross-node networking fails entirely. Disable port_security on every
    # ECS NIC so pod-CIDR packets transit. Documented in Huawei UCS Cilium
    # prerequisites: https://support.huaweicloud.com/intl/en-us/usermanual-ucs/ucs_01_0204.html
    source_dest_check = false
  }

  # Wave 5.7 (Refs #2140): only the index-0 (primary) CP of each region
  # gets an EIP; replica CPs (index > 0 — only when
  # huawei_control_plane_count > 1) run with private IPs only and
  # peer ClusterMesh via the primary CP's EIP. HCS publicIp quota=10
  # vs Hetzner-style 1-per-CP design = irreconcilable for any tenancy
  # carrying mothership + ≥1 Sovereign.
  eip_id = local.cp_nodes[count.index].index == 0 ? huaweicloud_vpc_eip.cp[local.cp_nodes[count.index].region].id : null

  user_data = local.cp_cloud_init_by_region[local.cp_nodes[count.index].region]

  key_pair = huaweicloud_kps_keypair.main.name

  # System disk type — Wave 5.6 (Refs #2140). The huaweicloud provider
  # defaults to GPSSD which is a public Huawei Cloud SKU; HCS me-east-215
  # exposes only `SSD` and `SAS` (queried via EVS cinder list-volume-types
  # 2026-05-22T20:21Z). Pin to SSD for production-appropriate IOPS.
  # Caught live on c928d81d3256535c: `Ecs.0005 rootVolume type[GPSSD]
  # is not exist`.
  system_disk_type = "SSD"
  system_disk_size = 40

  # HCS ECS read-back occasionally returns synthesised values for tags
  # + metadata.os_type + system_disk_id; ignore so plan stays clean.
  # Wave 5.144: also ignore `name` so an ACTIVE CP doesn't get
  # destroyed when the retry-loop bumps var.retry_attempt and
  # changes its formula-computed name.
  lifecycle {
    ignore_changes = [tags, metadata, name]
  }
}

# ── Worker ECS instances ──────────────────────────────────────────────────
#
# Workers have NO EIP — only cluster-internal connectivity. They join
# k3s via the CP's private 10.x.1.2 IP inside the region's VPC.

resource "huaweicloud_compute_instance" "worker" {
  for_each = local.worker_map

  # Wave 5.134 (hw30 fix-forward 2026-05-27): worker name carries a hash
  # of (deployment_id + region + index) instead of plain `w<idx>`. HCS
  # scheduler exhibited persistent Common.0021 (CollectInfoTask-fail)
  # on the SAME numerical indices (w3, w5, w6) across 9 attempts on
  # hw30. Switching to a non-numerical suffix breaks the scheduler-cell
  # affinity heuristic that appeared to pin those names to specific
  # broken compute cells. Per docs/PRINCIPLES.md #4 (never hardcode),
  # the hash is deterministic per (deployment, index) so re-prov of
  # same deployment_id reuses same names (idempotency preserved).
  #
  # Format: <name_prefix>-<region>-w<6hex> where 6hex =
  # substr(sha256(deployment_id + region + idx + retry_attempt), 0, 6).
  # Collision probability across 8 workers in one Sovereign: ~8²/2/16⁶ ≈ 1.9e-6.
  #
  # Wave 5.139 (hw30 #15 fix-forward 2026-05-27): extend the hash input
  # with var.retry_attempt. Within a single prov, the catalyst-api
  # Provision retry loop (provisioner.go:1220) bumps retry_attempt before
  # each retry's plan; tofu sees a NEW name for any worker not already
  # in state → HCS scheduler picks a fresh cell, dodging the bad cell
  # that returned Common.0021 on the prior attempt. EXISTING ACTIVE
  # workers are protected by lifecycle.ignore_changes=[name] below so
  # their already-created names persist across retries.
  name              = "${local.name_prefix}-${each.value.region}-w${substr(sha256("${var.deployment_id}-${each.value.region}-${each.value.index}-${var.retry_attempt}"), 0, 6)}"
  image_id          = var.image_id
  flavor_id         = each.value.flavor
  availability_zone = var.huawei_az
  security_groups   = [huaweicloud_networking_secgroup.region[each.value.region].name]

  network {
    uuid = huaweicloud_vpc_subnet.region[each.value.region].id
    # Wave 5.30 (Refs #2163): see CP block — disable port_security so pod
    # CIDR packets transit the HCS SDN layer.
    source_dest_check = false
  }

  # Wave 5.13 (Refs #2140): per-region cloud-init render with the ACTUAL
  # CP private IP for THIS region (not the wrong subnet+2 assumption).
  user_data = local.worker_cloud_init_by_region[each.value.region]

  key_pair = huaweicloud_kps_keypair.main.name

  # System disk type — see CP block comment above (Wave 5.6 / Refs #2140).
  system_disk_type = "SSD"
  system_disk_size = 40

  # HCS read-back divergence — see CP block lifecycle comment.
  # Wave 5.139: also ignore `name` so already-created ACTIVE workers
  # don't get destroyed when the retry-loop bumps var.retry_attempt
  # (which changes the formula-computed name for not-yet-created
  # workers but must NOT trigger a destroy of the alive ones).
  lifecycle {
    ignore_changes = [tags, metadata, name]
  }
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

# ── Wave 5.98 (#2447) — Huawei ELB v3: public 443→30443 + 80→30080 ──────
# Public ELB that terminates 80/443 from the operator-facing FQDN and
# forwards to cilium-envoy on the host NodePorts (30080/30443) on the
# primary-region CP1. The Cilium Gateway-API hostnetwork mode binds
# cilium-envoy to high ports because cilium-agent's BPF socket-LB
# program rejects privileged-port bind() syscalls (verified on otech45-47
# Hetzner port — see clusters/_template/sovereign-tls/cilium-gateway.yaml
# line ~217 comment). Hetzner provider solves this with hcloud_load_
# balancer_service 443→30443; this is the Huawei-port equivalent.
#
# Sovereign FQDN A-record points at this ELB's EIP (NOT the CP EIP).
# Phase 1 mothership-side handover writes the A record via PowerDNS.

resource "huaweicloud_vpc_eip" "elb_primary" {
  publicip {
    type = "5_bgp"
  }
  bandwidth {
    share_type  = "PER"
    name        = "${local.name_prefix}-elb-primary-bw"
    size        = 300
    charge_mode = "traffic"
  }
}

resource "huaweicloud_elb_loadbalancer" "primary" {
  name              = "${local.name_prefix}-elb-primary"
  vpc_id            = huaweicloud_vpc.region[local.region_keys[0]].id
  ipv4_subnet_id    = huaweicloud_vpc_subnet.region[local.region_keys[0]].ipv4_subnet_id
  ipv4_eip_id       = huaweicloud_vpc_eip.elb_primary.id
  availability_zone = [var.huawei_az]
  description       = "Catalyst Sovereign ${var.sovereign_fqdn} — Wave 5.98 Cilium-Gateway-API 443→30443 + 80→30080 routing"

  # Wave 5.128 (hw30 fix-forward 2026-05-27): ignore_changes on flavor IDs.
  # The huaweicloud provider sends `l4_flavor_id: null` + `l7_flavor_id: null`
  # on PUT when neither is explicitly set in the resource block (HCS auto-
  # picks flavors at create-time, and the provider's update path doesn't
  # round-trip them on subsequent plans). HCS rejects with ELB.8959 "Not
  # allowed to update both l4_flavor_id and l7_flavor_id to null or empty".
  # Caught on hw30 #4 + hw29 Wave 5.122 — every tofu apply that touches
  # any child of the LB (member port change, listener update) cascades to
  # an attempted LB PUT that fails the apply.
  #
  # Flavor IDs are immutable post-create — ignore them so subsequent plans
  # don't churn. If we ever need to resize the LB, the canonical path is
  # tofu taint + recreate, not in-place flavor update.
  lifecycle {
    ignore_changes = [
      l4_flavor_id,
      l7_flavor_id,
      # Wave 5.131 (hw30 fix-forward 2026-05-27): provider also sends
      # null for vpc_id + ipv4_eip_id on subsequent plans (same shape
      # as the flavor-id bug — HCS auto-pins both at create, provider's
      # update path doesn't round-trip them). Apply errors:
      #   * vpc_id can't be updated, <existing> -> ""
      #   * ipv4_eip_id can't be updated, <existing> -> ""
      # Both are immutable post-create; ignore them.
      vpc_id,
      ipv4_eip_id,
    ]
  }
}

resource "huaweicloud_elb_pool" "https" {
  name            = "${local.name_prefix}-elb-pool-https"
  protocol        = "TCP"
  lb_method       = "ROUND_ROBIN"
  loadbalancer_id = huaweicloud_elb_loadbalancer.primary.id
  description     = "cilium-envoy targets on port 30443"
}

resource "huaweicloud_elb_pool" "http" {
  name            = "${local.name_prefix}-elb-pool-http"
  protocol        = "TCP"
  lb_method       = "ROUND_ROBIN"
  loadbalancer_id = huaweicloud_elb_loadbalancer.primary.id
  description     = "cilium-envoy targets on port 30080"
}

resource "huaweicloud_elb_listener" "https" {
  loadbalancer_id = huaweicloud_elb_loadbalancer.primary.id
  name            = "${local.name_prefix}-elb-listener-https"
  protocol        = "TCP"
  protocol_port   = 443
  default_pool_id = huaweicloud_elb_pool.https.id
  idle_timeout    = 60
  description     = "TCP passthrough — cilium-envoy terminates TLS at 30443"
}

resource "huaweicloud_elb_listener" "http" {
  loadbalancer_id = huaweicloud_elb_loadbalancer.primary.id
  name            = "${local.name_prefix}-elb-listener-http"
  protocol        = "TCP"
  protocol_port   = 80
  default_pool_id = huaweicloud_elb_pool.http.id
  idle_timeout    = 60
  description     = "TCP passthrough — ACME HTTP-01 + .well-known"
}

# Pool members: primary-region nodes (CPs + workers). Cilium-envoy DS runs
# on every node, so every node serves 30443/30080. local.primary_lb_node_ips
# collects the private IPs.

locals {
  # CPs are `count`-based — index by tofu's list-indexed array.
  # Workers are `for_each`-based on local.worker_map (key=<region>-<index>).
  primary_lb_node_ips = concat(
    [for idx, n in local.cp_nodes :
      huaweicloud_compute_instance.control_plane[idx].access_ip_v4
      if n.region == local.region_keys[0]
    ],
    [for w in local.worker_nodes :
      huaweicloud_compute_instance.worker["${w.region}-${w.index}"].access_ip_v4
      if w.region == local.region_keys[0]
    ],
  )
}

resource "huaweicloud_elb_member" "https" {
  count   = length(local.primary_lb_node_ips)
  pool_id = huaweicloud_elb_pool.https.id
  address = local.primary_lb_node_ips[count.index]
  # Wave 5.124 (Refs hw29 fix-forward 2026-05-27): revert Wave 5.122.
  # Cilium gateway-api `hostnetwork-enabled=true` binds envoy to whatever
  # ports the Gateway spec declares — NOT the canonical web ports.
  # bp-catalyst-platform's Gateway listeners are 30443 (HTTPS) + 30080
  # (HTTP) per sovereign-tls-vars-cm.yaml. ELB does public 443→30443 and
  # 80→30080 NAT — listeners stay at 443/80, members target 30443/30080.
  protocol_port = 30443
  subnet_id     = huaweicloud_vpc_subnet.region[local.region_keys[0]].ipv4_subnet_id
}

resource "huaweicloud_elb_member" "http" {
  count         = length(local.primary_lb_node_ips)
  pool_id       = huaweicloud_elb_pool.http.id
  address       = local.primary_lb_node_ips[count.index]
  protocol_port = 30080 # Wave 5.124 (hw29 fix-forward) — Gateway listener
  subnet_id     = huaweicloud_vpc_subnet.region[local.region_keys[0]].ipv4_subnet_id
}

resource "huaweicloud_elb_monitor" "https" {
  pool_id     = huaweicloud_elb_pool.https.id
  protocol    = "TCP"
  port        = 30443 # Wave 5.124 (hw29 fix-forward) — Gateway listener
  interval    = 10
  timeout     = 5
  max_retries = 3
}

resource "huaweicloud_elb_monitor" "http" {
  pool_id     = huaweicloud_elb_pool.http.id
  protocol    = "TCP"
  port        = 30080 # Wave 5.124 (hw29 fix-forward) — Gateway listener
  interval    = 10
  timeout     = 5
  max_retries = 3
}
