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
  # OVERFLOW FIX (2026-07-09, hw230 dep bc64c0e8): the modulo MUST leave
  # headroom for the per-region `+idx*10` offset, else a high base pushes
  # the LAST region past the second octet's 255 ceiling — hw230 hashed to
  # base=247, so region-b (idx 1) rendered `10.257.0.0/16`, an INVALID CIDR
  # that failed `tofu plan` before any resource was created. Subtract the
  # max region offset `(N-1)*10` from the 216-bucket span so every region
  # stays within [32, 247]: 2 regions → base∈[32,237], region-b∈[42,247].
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
  cidr_base = 32 + (parseint(substr(sha256(var.deployment_id), 0, 2), 16) % (216 - (length(var.regions) - 1) * 10))
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

  # #3504 (Refs #3375) — per-region k3s pod + service CIDRs.
  #
  # The k3s --cluster-cidr (pod CIDR) and --service-cidr MUST be
  # non-overlapping across ClusterMesh peers (DoD gate D11). Previously
  # both were hardcoded to 10.42.0.0/16 + 10.96.0.0/16 for EVERY region,
  # so region-a cp1 and region-b cp1 both advertised 10.42.0.0/24. Across
  # the mesh the region-a apiserver's pod-network source IP (10.42.0.x)
  # then resolved AMBIGUOUSLY — a peer's ipcache mapped it to region-b's
  # cp1 instead of region-a's — so every apiserver→webhook RESPONSE
  # tunnelled to the wrong region and never returned (10s timeout). That
  # mis-route broke the cp1 datapath (Cluster health 1/4 reachable),
  # wedging the cnpg-pair initdb + cert-manager/kyverno webhooks → no
  # region-b standby → #3375 region-kill impossible (root-caused on hw135).
  #
  # Mirror the Hetzner module (infra/providers/hetzner/main.tf
  # region_cluster_cidr / region_service_cidr): offset each region's /16
  # by its index. Pod CIDR 10.(42+idx).0.0/16, service CIDR
  # 10.(96+idx).0.0/16 — primary=10.42/10.96, region-b=10.43/10.97, etc.
  # These are the k3s overlay's INTERNAL pod/service ranges (independent
  # of the per-prov region_vpc_cidr above, which addresses the nodes); the
  # +42/+96 bases match Hetzner so both providers share the same D11 shape.
  region_cluster_cidr = {
    for idx, r in var.regions :
    r.code => format("10.%d.0.0/16", 42 + idx)
  }
  region_service_cidr = {
    for idx, r in var.regions :
    r.code => format("10.%d.0.0/16", 96 + idx)
  }

  # #4942 — mesh-wide ingress source CIDRs for the cilium-health (4240) and
  # pod-CIDR security-group rules.
  #
  # On a multi-region Sovereign the primary CP node does NOT participate in
  # node-to-node WireGuard the way workers do: its cilium ipcache entry carries
  # `encryptkey=0` (proven live hw234 — worker peer nodes read `encryptkey=255`).
  # So the CP's CROSS-REGION cilium-health TCP:4240 probes and host→pod traffic
  # travel NATIVE (unencrypted) over the VPC peering, whereas worker traffic is
  # WireGuard-wrapped and rides UDP:51871 (opened 0.0.0.0/0 by
  # ingress_cilium_wireguard) — bypassing these L3/L4 rules entirely.
  #
  # The two rule families below were scoped to the node's OWN region only
  # (region_subnet_cidr / region_cluster_cidr), so the peer region's native
  # CP traffic was dropped: cilium-health read the peer CP 0/1 on BOTH the host
  # (node:4240) and endpoint (pod:4240) paths — every worker stayed 1/1 — which
  # flipped the console to a FALSE "region Degraded" even though the pod
  # datapath was healthy (cross-region ICMP + CNPG replica streaming, RPO=0).
  # ICMP survived because ingress_icmp is already 0.0.0.0/0.
  #
  # Permit every mesh region's node subnet + pod CIDR (PRIVATE ranges only,
  # never 0.0.0.0/0) so the native CP path is delivered. On a single-region
  # prov this collapses to the node's own subnet + own pod CIDR — no behaviour
  # change. Workers are unaffected (their traffic already bypasses the SG via
  # WireGuard); this rule only makes the native CP path reachable.
  mesh_ingress_cidrs = distinct(concat(
    [for c in local.region_keys : local.region_subnet_cidr[c]],
    [for c in local.region_keys : local.region_cluster_cidr[c]],
  ))

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

# ── #4656: POD-CIDR peering routes (the native-routing complement) ──────────
# The VPC-CIDR routes above carry NODE-to-NODE traffic — sufficient under the
# old VXLAN tunnel (pod packets were encapsulated over node IPs). PR #4763
# switches cilium to NATIVE routing (routingMode: native) to end the
# VXLAN-over-WireGuard MTU DF-drops, so cross-cluster pod-to-pod now routes
# pod-IP->pod-IP (region-a 10.42.x -> region-b 10.43.x) with NO encapsulation.
# Those pod CIDRs (local.region_cluster_cidr, offset per region at ~L83) are
# NOT in the VPC route tables, so the peer VPC has no route back to them -> the
# region-A<->B POD datapath black-holes (node reachable, pod times out = the
# EXACT #4656 signature; live hw225: node WG OK, 56-byte pod ping 100% loss
# cross-region). Add pod-CIDR routes over the SAME peering so native
# cross-cluster pod traffic has a return path.
resource "huaweicloud_vpc_route" "mesh_pod_a_to_b" {
  for_each = local.vpc_peering_pairs

  vpc_id      = huaweicloud_vpc.region[each.value.a].id
  destination = local.region_cluster_cidr[each.value.b]
  type        = "peering"
  nexthop     = huaweicloud_vpc_peering_connection.mesh[each.key].id
}

resource "huaweicloud_vpc_route" "mesh_pod_b_to_a" {
  for_each = local.vpc_peering_pairs

  vpc_id      = huaweicloud_vpc.region[each.value.b].id
  destination = local.region_cluster_cidr[each.value.a]
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
#   - TCP 2379  — Cilium clustermesh-apiserver VIP dial (mTLS-protected,
#                cross-region). #4765 ERADICATED the former 30000-32767
#                k8s NodePort-range rule; peers now dial the VIP :2379 directly.
#   - TCP 12379 — Cilium clustermesh-proxy host-socket dial (#4784) — a
#                hostNetwork socket BELOW the NodePort range that
#                TCP-passthroughs to the clustermesh-apiserver etcd, dodging
#                the CP-node k3s-etcd :2379 host collision.
#   §854 (NodePorts absolutely forbidden): NO 30000-32767 rule is opened
#                anywhere. The Sovereign Gateway uses cilium 1.19.3 gateway-api
#                hostNetwork mode — cilium-envoy binds node:443/:80 directly and
#                the gateway ELB targets those host ports (covered by the TCP
#                443/80 rules above). The 1.16.5-era ELB→node:<nodePort>
#                forwarding (#4690/#4691) is retired — no nodePort remains.
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

# #4706 — console gateway hostNetwork ports (8443/8080): the console ELB
# forwards public :443/:80 to these node host ports. Same 0/0 shape as the
# 443 rule above (traffic still fronts through the console ELB EIP).
resource "huaweicloud_networking_secgroup_rule" "ingress_8443" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 8443
  port_range_max    = 8443
  remote_ip_prefix  = "0.0.0.0/0"
  description       = "Cilium Gateway HTTPS"
}


resource "huaweicloud_networking_secgroup_rule" "ingress_8080" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 8080
  port_range_max    = 8080
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

resource "huaweicloud_networking_secgroup_rule" "ingress_clustermesh_2379" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  # #4765 (founder 2026-07-03, "FUCK THE NODEPORTS!!!") — the k8s NodePort
  # range (30000-32767) rule is ERADICATED. Cross-region ClusterMesh peers
  # now dial the clustermesh-apiserver VIP on :2379 directly (the single
  # sovereign-vip Cilium LB-IPAM pool serves it on the CP-node EIP; peers
  # reach it over the WG-encrypted node path). etcd mTLS is the security
  # boundary — same posture as the k3s apiserver :6443 rule above. The
  # Sovereign Gateway rides gateway-api hostNetwork (node:443/:80 direct,
  # opened by ingress_443 / ingress_80), so it needs NO NodePort either.
  port_range_min   = 2379
  port_range_max   = 2379
  remote_ip_prefix = "0.0.0.0/0"
  description      = "Cilium clustermesh-apiserver VIP dial :2379 (mTLS-protected; #4765 — replaces the forbidden NodePort range)"
}

# #4784 — clustermesh-proxy host-socket dial port. On no-CCM Huawei the shared
# Sovereign EIP is the CP-node IP, a 1:1 NAT whose host :2379 is ALREADY bound
# by k3s' own embedded etcd — so peers dialing <EIP>:2379 hit k3s etcd, never
# the Cilium clustermesh-apiserver (proven live hw225, #4784). The bp-cilium
# clustermesh-proxy DaemonSet binds a hostNetwork socket on this dedicated
# non-2379 port and TCP-passthroughs to the clustermesh-apiserver etcd;
# catalyst-api points peers here via CLUSTERMESH_APISERVER_DIAL_PORT. This is
# a hostNetwork bind, NOT a NodePort (the port is well outside the forbidden
# 30000-32767 range). etcd mTLS remains the security boundary.
resource "huaweicloud_networking_secgroup_rule" "ingress_clustermesh_proxy" {
  for_each          = { for r in var.regions : r.code => r }
  security_group_id = huaweicloud_networking_secgroup.region[each.key].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = var.clustermesh_proxy_port
  port_range_max    = var.clustermesh_proxy_port
  remote_ip_prefix  = "0.0.0.0/0"
  description       = "Cilium clustermesh-proxy host-socket dial (mTLS-protected; #4784 — non-2379 to dodge the CP-node k3s-etcd host collision)"
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
# #4942: fan out over local.mesh_ingress_cidrs so the peer region's native
# (encryptkey=0) CP health probes are permitted — not just the own region.
resource "huaweicloud_networking_secgroup_rule" "ingress_cilium_health" {
  for_each = {
    for pair in setproduct(local.region_keys, local.mesh_ingress_cidrs) :
    "${pair[0]}|${pair[1]}" => { region = pair[0], cidr = pair[1] }
  }
  security_group_id = huaweicloud_networking_secgroup.region[each.value.region].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  port_range_min    = 4240
  port_range_max    = 4240
  remote_ip_prefix  = each.value.cidr
  description       = "Cilium cilium-health (node reachability probes) — mesh src ${each.value.cidr}"
}

# Wave 5.30 (Refs #2163): pod CIDR allow-all from same-region subnet so
# any pod-to-pod traffic that escapes encap (e.g. host-gw / native routing
# fallback) is also permitted. Defence-in-depth alongside source_dest_check=false.
# HCS rejects "any" protocol — list specific protocols Cilium pod-to-pod uses.
# #3504 (Refs #3375): the allowed prefix WAS the region's OWN pod CIDR
# (was hardcoded 10.42.0.0/16; with per-region pod CIDRs region-b is now
# 10.43.0.0/16, so a hardcoded 10.42 rule would drop region-b's own
# pod-to-pod fallback traffic).
# #4942: fan out over local.mesh_ingress_cidrs (all regions' node subnets +
# pod CIDRs) so native (encryptkey=0) cross-region host→CP-pod traffic is
# also permitted. Worker-hosted pods are reachable already (WireGuard bypass);
# CP-hosted pods need this because the CP node egresses/ingresses native.
resource "huaweicloud_networking_secgroup_rule" "ingress_pod_cidr_tcp" {
  for_each = {
    for pair in setproduct(local.region_keys, local.mesh_ingress_cidrs) :
    "${pair[0]}|${pair[1]}" => { region = pair[0], cidr = pair[1] }
  }
  security_group_id = huaweicloud_networking_secgroup.region[each.value.region].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "tcp"
  remote_ip_prefix  = each.value.cidr
  description       = "Pod CIDR TCP all-ports (Cilium pod-to-pod / native CP path) — mesh src ${each.value.cidr}"
}
resource "huaweicloud_networking_secgroup_rule" "ingress_pod_cidr_udp" {
  for_each = {
    for pair in setproduct(local.region_keys, local.mesh_ingress_cidrs) :
    "${pair[0]}|${pair[1]}" => { region = pair[0], cidr = pair[1] }
  }
  security_group_id = huaweicloud_networking_secgroup.region[each.value.region].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "udp"
  remote_ip_prefix  = each.value.cidr
  description       = "Pod CIDR UDP all-ports (Cilium pod-to-pod / native CP path) — mesh src ${each.value.cidr}"
}
resource "huaweicloud_networking_secgroup_rule" "ingress_pod_cidr_icmp" {
  for_each = {
    for pair in setproduct(local.region_keys, local.mesh_ingress_cidrs) :
    "${pair[0]}|${pair[1]}" => { region = pair[0], cidr = pair[1] }
  }
  security_group_id = huaweicloud_networking_secgroup.region[each.value.region].id
  direction         = "ingress"
  ethertype         = "IPv4"
  protocol          = "icmp"
  remote_ip_prefix  = each.value.cidr
  description       = "Pod CIDR ICMP (Cilium pod-to-pod / native CP path) — mesh src ${each.value.cidr}"
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
  # Wave 5.106 #2462). harbor + docker.io mirror via :5000 / :5002 (#2842).
  #
  # #3913 (revert of regression 4049d61f4): the bastion :5000 cache is NOT a
  # harbor-only/docker-only front — it is the SAME Harbor proxy-cache as
  # harbor.openova.io, reachable over the LOCAL EIP 212.72.24.20 with NO NAT
  # egress. It already serves the proxy-ghcr / proxy-quay / proxy-gcr / proxy-k8s
  # projects (anon-pullable; verified live 2026-06-21: flux source-controller,
  # cnpg, external-secrets, openbao, oauth2-proxy, kube-apiserver, gcr distroless
  # all return manifest HTTP 200 via http://212.72.24.20:5000/v2/proxy-*/...).
  #
  # 4049d61f4 wrongly re-routed ghcr/quay/gcr/registry.k8s.io to the EXTERNAL
  # https://harbor.openova.io. On kom4dc, reaching harbor.openova.io needs NAT
  # egress → draws a reputation-blocklisted ("poisoned") EIP → cloud-init's
  # Flux/cilium/k8s image pulls stall → region-a never bootstraps → prov FAILS.
  # The bastion is the NAT-bypass local registry built exactly to avoid this.
  # Route ghcr/quay/gcr/registry.k8s.io through the bastion :5000 with the SAME
  # proxy-* rewrites — zero NAT egress, exactly like harbor + docker.io already do.
  #
  # #4600 — xpkg.upbound.io is NO LONGER mirrored through the bastion Harbor.
  # The Crossplane provider-opentofu package pulls DIRECTLY from xpkg.upbound.io
  # (the #4002/#4018 adoption seam). The former rewrite to
  # `harbor.openova.io/proxy-xpkg` (#4488/#4491/#4542) was an ARTIFICIAL bastion
  # tether: (a) the Crossplane xpkg fetcher (go-containerregistry K8sFetcher)
  # bypasses THIS containerd mirror entirely, so the rewrite was always inert
  # for the actual provider-package pull; and (b) the bastion `proxy-xpkg`
  # proxy-cache project never existed → `401 project proxy-xpkg not found` →
  # provider-opentofu Installed=False. Live-proven 2026-06-28: xpkg.upbound.io
  # answers (HTTP 401 anon-challenge) in 0.43s direct from the Huawei Sovereign,
  # so the "poisoned NAT" premise that justified the tether is false. public.ecr.aws
  # stays on https://harbor.openova.io (its proxy-ecr project is out of scope
  # here and unrelated to the Crossplane adoption seam). docker.io stays on the
  # bastion :5002 cache.
  #
  # #4049 (2026-06-21) — every image Huawei→Huawei, INCLUDING PRIVATE catalyst.
  # The ghcr.io mirror now carries TWO ordered rewrite rules (k3s/containerd
  # evaluates them first-match-wins):
  #   1. `openova-io/openova/(.*)` → `openova-io/openova/$1` — the PRIVATE
  #      catalyst image set (catalyst-api/ui, the *-controllers, marketplace-*,
  #      console, admin, the sme services-*). These are NOT anon-pullable, so
  #      the bastion's anonymous `proxy-ghcr` PROXY-CACHE 404s them (Harbor's
  #      github-ghcr adapter can't do ghcr's 2-step bearer-token flow, so it
  #      never caches a private blob). They are instead NATIVE-PUSHED into the
  #      HOSTED bastion project `openova-io` by the catalyst-build warmup step
  #      (.github/workflows/catalyst-build.yaml, mirrors the cutover
  #      03-harbor-prewarm Job) on EVERY catalyst build — catalyst changes every
  #      build, so the warmup re-pushes the latest tags. This rule routes the
  #      private namespace to that hosted project so nodes pull catalyst
  #      bastion-local (28 MB/s) with ZERO ghcr egress — killing the #4049
  #      15-min FirstSeenTimeout wedge (hw182 RCA: nodes cold-fetching private
  #      catalyst DIRECT from ghcr at 2.3 MB/s). This also replaces the former
  #      cloud-init `crictl pull --creds ghcr.io/openova-io/openova/*` direct-
  #      ghcr pre-pull block (now deleted in _shared/cloudinit-control-plane).
  #   2. `(.*)` → `proxy-ghcr/$1` — the PUBLIC ghcr images (fluxcd/*,
  #      cloudnative-pg/*, …) keep using the already-warm anon proxy-cache.
  # The hosted `openova-io` project is PRIVATE, so node pulls authenticate via
  # the `212.72.24.20:5000` configs.auth block (the same `robot$openova-bot`
  # Harbor robot the warmup pushes with). PREREQUISITE (orchestrator/founder):
  # the hosted `openova-io` project must exist on the bastion Harbor + the robot
  # must have pull on it (the warmup step ensures-creates the project via the
  # Harbor API). xpkg/ecr/docker.io routing is unchanged.
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
      "quay.io":
        endpoint:
          - "http://212.72.24.20:5000"
        rewrite:
          "(.*)": "proxy-quay/$1"
      "gcr.io":
        endpoint:
          - "http://212.72.24.20:5000"
        rewrite:
          "(.*)": "proxy-gcr/$1"
      "registry.k8s.io":
        endpoint:
          - "http://212.72.24.20:5000"
        rewrite:
          "(.*)": "proxy-k8s/$1"
      "ghcr.io":
        endpoint:
          - "http://212.72.24.20:5000"
        rewrite:
          # #4049 (hw182 live-proven 2026-06-22): containerd renders the YAML
          # `rewrite:` map into a hosts.toml `[host.<x>.rewrite]` TOML MAP and
          # iterates it in NON-DETERMINISTIC (Go map) order — file/list order is
          # NOT honored (proven: `openova-io` rule listed first STILL lost to the
          # catch-all). A plain `(.*)` catch-all therefore SHADOWS the specific
          # `openova-io/openova/...` rule ~half the time → the private catalyst
          # ref gets rewritten to `proxy-ghcr/openova-io/...` (404, anon cache
          # can't serve the private blob) → containerd falls back to the upstream
          # `server = https://ghcr.io` → 401. The two rules MUST be mutually
          # exclusive. RE2 has no negative-lookahead, so the catch-all is anchored
          # to NOT match the `openova-io/openova/` prefix via a first-mismatch
          # alternation. PRIVATE openova catalyst → hosted bastion project
          # `openova-io` (warmed by the catalyst-build job, served from the
          # bastion :5000 pull-through cache of the PUBLIC Contabo `openova-io`
          # project — proven 0.9s warm pull, 61.7 MiB, zero ghcr egress).
          # Everything else (fluxcd/*, cloudnative-pg/*, …) → the anon proxy-ghcr
          # cache (verified still routes correctly).
          "^openova-io/openova/(.*)": "openova-io/openova/$1"
          "^(openova-io/(?:[^o]|o[^p]|op[^e]|ope[^n]|open[^o]|openo[^v]|openov[^a]|openova[^/]).*|(?:[^o]|o[^p]|op[^e]|ope[^n]|open[^o]|openo[^v]|openov[^a]|openova[^-]|openova-[^i]|openova-i[^o]).*)": "proxy-ghcr/$0"
      # #4600 — xpkg.upbound.io is NOT mirrored through the bastion Harbor.
      # The Crossplane provider-opentofu package pulls DIRECTLY from
      # xpkg.upbound.io (live-proven reachable sub-second from the Huawei
      # Sovereign — see clusters/_template/.../base/provider-opentofu.yaml).
      # The Crossplane xpkg fetcher bypasses this containerd mirror anyway, so
      # the former `harbor.openova.io/proxy-xpkg` rewrite was both inert for
      # the fetcher AND an artificial bastion tether for node-level pulls.
      "public.ecr.aws":
        endpoint:
          - "https://harbor.openova.io"
        rewrite:
          "(.*)": "proxy-ecr/$1"
    configs:
      "212.72.24.20:5000":
        auth:
          username: "robot$openova-bot"
          password: "${var.harbor_robot_token}"
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
      # #3971 follow-up: was wrongly set to var.huawei_region (a latent bug
      # harmless until the EVS CSI — slot 55b — needed the REAL project-id for
      # the EVS API path https://evs.<region>.<cloud>/v3/<project-id>/...).
      # The huaweicloud-csi-driver cloud-config reads this as `project-id`.
      huawei-project-id: "${var.huawei_project_id}"
      huawei-region: "${var.huawei_region}"
      # #4620: the crossplane cloud-adoption Composition's Workspace env sets
      # HCLOUD_TOKEN from a REQUIRED secretKeyRef to key `hcloud-token` (the
      # provider-opentofu env schema has NO `optional` field — #4739). On a
      # Huawei Sovereign that key is absent, so EVERY adopt-* Workspace
      # hard-fails at env-resolution: "couldn't find key hcloud-token in Secret
      # flux-system/cloud-credentials" → 0/24 Synced (live hw227, 2026-07-05),
      # AFTER the #4808 structural fix cleared the "No state file" deadlock.
      # Plant an EMPTY placeholder so the ref resolves; the tofu hcloud
      # provider already no-ops on an empty token (chart try/dummy-token path).
      hcloud-token: ""
  EOT

  # provider prelude — Huawei. Runs BEFORE k3s (runcmd 0-indent items; the
  # shared template re-indents via indent(2,…)). One hard dependency:
  #  (1) #3132 console-less log self-upload: kom4dc CPs have NO ECS console-
  #      output API + no reachable sshd, so /var/log/cloud-init-output.log can
  #      only be PUSHED. Backgrounded EARLY so it keeps uploading even if a
  #      later step aborts cloud-init — the founder's capture-before-wipe guard.
  #
  # NOTE (#4682 / #4690): the former iptables DNAT (443→30443 / 80→30080) is
  # GONE. The Cilium Gateway is served as a Service type=LoadBalancer on the
  # standard :443/:80 (hostNetwork off, no NodePort). On the CCM-less Huawei
  # provider the RESTORED gateway ELB (elb_primary, its OWN routable EIP)
  # forwards public :443/:80 → node:<gateway-Service-nodePort>; a dedicated
  # CiliumLoadBalancerIPPool assigns the Service its ExternalIP so Cilium
  # programs the datapath + allocates the nodePort. NO host-level REDIRECT, NO
  # :30443. (The #4687/#4689 sharing-key option-B that co-located the gateway
  # VIP on this CP-node EIP is removed — that EIP is a 1:1 NAT, unreachable.)
  # Plain POSIX (`nohup sh -c`, not bash; dash -n clean — no $(( )) here).
  provider_prelude_huawei = <<-EOT
    - |
      if [ -n "${var.deployment_id}" ] && [ -n "${var.kubeconfig_bearer_token}" ] && [ -n "${var.catalyst_api_url}" ]; then
        nohup sh -c '
          n=0
          # #5042: -4 forces IPv4. This loop runs in the prelude BEFORE the
          # IPv4-pin step, so on the IPv4-only kom4dc VPC curl otherwise resolves
          # catalyst_api_url to AAAA, tries IPv6 first -> "network unreachable" ->
          # every push fails silently (|| true) -> a mid-run cloud-init death is
          # forensic-blind (hw247 GET cloudinit-log 404). 60 iters (~50min) covers
          # a slow 2-region bootstrap.
          while [ "$${n}" -lt 60 ]; do
            curl -4 -sk -X PUT \
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
    "${split(".", var.sovereign_fqdn)[0]}-${trim(replace(replace(local.region_keys[0], "/[0-9]+/", ""), "/-+/", "-"), "-")}"
  )
  primary_cluster_mesh_id_effective = (
    var.cluster_mesh_id > 0 ? var.cluster_mesh_id : 1
  )
  cluster_mesh_name_by_region = {
    for idx, r in var.regions :
    r.code => (
      idx == 0 ? local.primary_cluster_mesh_name_effective :
      "${split(".", var.sovereign_fqdn)[0]}-${trim(replace(replace(r.code, "/[0-9]+/", ""), "/-+/", "-"), "-")}"
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
      # #3504 (Refs #3375): per-region pod/service CIDRs (was hardcoded
      # 10.42.0.0/16 + 10.96.0.0/16 for every region → ClusterMesh
      # PodCIDR collision → cp1 datapath mis-route → region-kill blocked).
      cluster_cidr = local.region_cluster_cidr[r.code]
      service_cidr = local.region_service_cidr[r.code]
      # Canonical region labels (G84 #2631: `hw` prefix, NOT `hu`).
      region_canonical_label         = "hw-${r.code}-rtz-prod"
      primary_region_canonical_label = "hw-${var.regions[0].code}-rtz-prod"
      replica_region_canonical_label = length(var.regions) > 1 ? "hw-${var.regions[1].code}-rtz-prod" : ""
      # #2940: per-Sovereign PDNS endpoint override (bootstrap-kit substitute).
      pdns_api_host = var.pdns_api_host
      # #5017 keystone — kom4dc provider API /24 for the step-08 deny-egress
      # allow-list (egressTest.allowProviderCIDRs via PROVIDER_API_CIDRS_YAML).
      provider_api_cidrs_yaml = jsonencode(var.provider_api_cidrs)
      gitops_repo_url         = var.gitops_repo_url
      gitops_branch           = var.gitops_branch
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
      default_storage_class    = var.default_storage_class
      sovereign_cnpg_instances = length(var.regions) > 1 ? "2" : "1"
      continuum_enabled        = length(var.regions) > 1 ? "true" : "false"
      marketplace_enabled      = var.marketplace_enabled
      qa_fixtures_enabled      = var.qa_fixtures_enabled
      # North Star — decouple cutover auto-fire from qaTestEnabled. → shared
      # template's FIRE_CUTOVER_ON_HANDOVER substitute → slot-13
      # catalystApi.fireCutoverOnHandover (ORed with qaFixtures.enabled by the
      # chart, #4648). INDEPENDENT of qa_fixtures_enabled so a PROD-cert
      # Sovereign auto-fires the cutover on handover too.
      fire_cutover_on_handover = var.fire_cutover_on_handover
      # #4053 console-isolation toggle (Refs #4431 #4212). Threads into the
      # shared template's SOVEREIGN_CONSOLE_GATEWAY substitute → slot-13
      # ingress.gateway.parentRef.name. "true" → cilium-gateway-console
      # (default isolation); "false" → cilium-gateway (shared) so the console
      # resolves even when no dedicated console ELB exists.
      console_isolation_enabled = var.console_isolation_enabled
      wildcard_cert_issuer      = var.wildcard_cert_use_staging == "true" ? "letsencrypt-dns01-staging-powerdns" : "letsencrypt-dns01-prod-powerdns"

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
      # SOVEREIGN_LB_IP / SOVEREIGN_CONTROL_PLANE_IP → the RESTORED gateway ELB's
      # OWN routable EIP (#4690 / #4686 foundation-fix). The wildcard *.<fqdn>
      # A-record (and thus console.<fqdn> when console_isolation is off) points at
      # THIS ELB EIP — NOT the CP-node EIP, which is a Huawei 1:1 NAT and
      # externally unreachable (the #4687 option-B pointed it at the CP EIP; that
      # never produced a public front door, verified hw208). The ELB EIP is the
      # canonical operator-facing entry point (Settings-page controlPlaneIP). Both
      # the ELB EIP (here) and the per-region CP EIP (node_external_ip_value, used
      # for LB-IPAM/control-plane) are known at tofu-apply time on Huawei.
      load_balancer_ipv4 = huaweicloud_vpc_eip.elb_primary.publicip.0.ip_address
      # #4236 — the dedicated console ELB EIP (#4053 isolation). Threads through
      # cloud-init → bootstrap-kit slot 13 → sovereign-fqdn ConfigMap `consoleLBIP`
      # → organization-controller so the per-Org pool-DNS `console.<slug>.<pool>`
      # A-record targets the console front door (the #4179 final layer).
      console_load_balancer_ipv4 = local.console_isolation_on ? huaweicloud_vpc_eip.elb_console[0].publicip.0.ip_address : ""

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
      # #4784 — clustermesh-proxy host-socket dial port (non-2379; dodges the
      # CP-node k3s-etcd :2379 collision). Drives CILIUM_CLUSTERMESH_PROXY_PORT
      # + CLUSTERMESH_APISERVER_DIAL_PORT; matches the ingress_clustermesh_proxy
      # SG rule + the bp-cilium clustermeshProxy.hostPort value.
      clustermesh_proxy_port = var.clustermesh_proxy_port
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
              case "$${HTTP_CODE}" in 2*) break;; esac
              sleep 30
            done
          fi
        - |
          HOSTNAME=$(hostname)
          PRIMARY_HOST='${local.name_prefix}-${local.region_keys[0]}-cp1-${substr(sha256("${var.deployment_id}-${local.region_keys[0]}-cp0-${var.retry_attempt}"), 0, 6)}'
          MY_REGION='${idx == 0 ? r.code : "${r.code}-${idx}"}'
          MY_EIP='${huaweicloud_vpc_eip.cp[r.code].publicip.0.ip_address}'
          # #3991 — this CP's PRIVATE eth0 IP (same detection as --node-ip).
          # The kubeconfig keeps MY_EIP as `server:` so the EXTERNAL mothership
          # can reach this region. We ALSO ship the private IP so the IN-CLUSTER
          # catalyst-api (chroot), which sits inside region-a's VPC and cannot
          # route to this region's DNAT'd EIP, can rewrite the server host to
          # this VPC-peered private IP it CAN reach (over the cross-VPC peering
          # routes provisioned above). The k3s apiserver lists NODE_IP as a TLS
          # SAN, so the pinned-CA handshake still validates after the swap.
          export MY_NODE_IP=$(ip -4 -o addr show dev eth0 | awk '{print $4}' | cut -d/ -f1)
          if [ -n "${var.deployment_id}" ] && [ -n "${var.kubeconfig_bearer_token}" ] && [ "$${HOSTNAME}" != "$${PRIMARY_HOST}" ] && [ -n "$${MY_REGION}" ] && [ -n "$${MY_EIP}" ]; then
            sed "s|server: https://127.0.0.1:6443|server: https://$${MY_EIP}:6443|" /etc/rancher/k3s/k3s.yaml > /tmp/kubeconfig-secondary.yaml
            python3 -c "import json,os; print(json.dumps({'deploymentId':'${var.deployment_id}','regionKey':'$${MY_REGION}','kubeconfigYaml':open('/tmp/kubeconfig-secondary.yaml').read(),'nodeInternalIp':os.environ.get('MY_NODE_IP','')}))" > /tmp/secondary-kubeconfig-body.json
            for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
              HTTP_CODE=$(curl -sk -o /dev/null -w '%%{http_code}' -X POST \
                -H "Authorization: Bearer ${var.kubeconfig_bearer_token}" \
                -H "Content-Type: application/json" \
                --data-binary @/tmp/secondary-kubeconfig-body.json \
                --max-time 15 \
                "${var.catalyst_api_url}/api/v1/sovereign/secondary-kubeconfig" || echo "000")
              echo "secondary kubeconfig PUT-back attempt $${attempt} -> HTTP $${HTTP_CODE}"
              # #5012 hw255: the secondary endpoint answers 201 Created — the old
              # 204|200-only check never broke, so every healthy secondary burned
              # 12 attempts x sleep 30 (~6 min) HERE, pushing flux-install past the
              # mothership's flux-CRD probe budget. Accept ANY 2xx.
              case "$${HTTP_CODE}" in 2*) break;; esac
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

# ── #4690 / #4686 foundation-fix — the dedicated GATEWAY ELB (elb_primary) ──
# is RESTORED (it was removed in #4687/252d20ddb by the broken "option-B").
#
# Why option-B was wrong: #4687 folded the Sovereign Gateway Service's
# ExternalIP onto the primary CP-node's OWN public EIP (co-located with the
# clustermesh Service via a Cilium LB-IPAM sharing-key). But that EIP is a
# Huawei 1:1 NAT — externally UNREACHABLE (verified live on hw208) — so the
# gateway never had a public front door. (The clustermesh path was unaffected
# because clustermesh peers reach the CP EIP over the WG-encrypted node path,
# not through the NAT.)
#
# Correct shape (this file, #4706 final form): a dedicated public Huawei ELB
# v3 with its OWN routable EIP does TCP passthrough public :443/:80 →
# member node:443/:80. With cilium 1.19.3's gateway-api hostNetwork mode
# (bp-cilium 1.4.8; the 1.16.x host-bind bug that forced the interim
# ELB→node:<nodePort> shape of #4690/#4691 is fixed), cilium-envoy — a
# hostNetwork DaemonSet — binds node:443/:80 DIRECTLY on every node. The
# member ports are durable and known at Phase-0 apply, so the front door is
# correct from first apply: no placeholder, no post-convergence nodePort
# discovery, no nodePort anywhere (§854 honored literally).
#
# catalyst-api (bootstrap/api post_handover_gateway_elb.go) only heals
# member-SET drift (node churn) at these same ports. The wildcard *.<fqdn>
# A-record points at THIS ELB's EIP (load_balancer_ip output +
# load_balancer_ipv4 cloud-init render var).
#
# The dedicated CONSOLE ELB (elb_console, #4053) below is unchanged.

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
  description       = "Catalyst Sovereign ${var.sovereign_fqdn} — #4690 gateway ELB, public 443/80 → node:443/:80 (cilium-envoy hostNetwork host ports, §854 — no nodePort)"

  # #5244 — IP-as-backend so PEER-REGION nodes can be pool members. The ELB
  # lives in the primary region's VPC; region-b nodes sit in region-b's VPC,
  # reachable only over the mesh VPC peering. Huawei ELB only accepts members
  # outside its own VPC as IP-type members with cross-VPC backend enabled.
  # Multi-region only: single-region provs keep the pre-#5244 shape (all
  # members are same-VPC, the flag stays provider-default).
  cross_vpc_backend = length(local.region_keys) > 1 ? true : null

  # Immutable-field churn guards (same as the console ELB): the huaweicloud
  # provider sends null/empty for l4_flavor_id/l7_flavor_id/vpc_id/ipv4_eip_id
  # on subsequent plans (HCS auto-pins them at create; the provider's update
  # path doesn't round-trip them) and HCS rejects the PUT (ELB.8959). They are
  # immutable post-create; ignore them.
  lifecycle {
    ignore_changes = [
      l4_flavor_id,
      l7_flavor_id,
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
  description     = "cilium-gateway members target node:443 — cilium-envoy hostNetwork host port, NOT a nodePort (§854 / #4765)"
}

resource "huaweicloud_elb_pool" "http" {
  name            = "${local.name_prefix}-elb-pool-http"
  protocol        = "TCP"
  lb_method       = "ROUND_ROBIN"
  loadbalancer_id = huaweicloud_elb_loadbalancer.primary.id
  description     = "cilium-gateway members target node:80 — cilium-envoy hostNetwork host port, NOT a nodePort (§854 / #4765)"
}

resource "huaweicloud_elb_listener" "https" {
  loadbalancer_id = huaweicloud_elb_loadbalancer.primary.id
  name            = "${local.name_prefix}-elb-listener-https"
  protocol        = "TCP"
  protocol_port   = 443
  default_pool_id = huaweicloud_elb_pool.https.id
  idle_timeout    = 60
  description     = "TCP passthrough — cilium-envoy (hostNetwork, node:443 direct) terminates TLS; no nodePort (§854 / #4765)"
}

resource "huaweicloud_elb_listener" "http" {
  loadbalancer_id = huaweicloud_elb_loadbalancer.primary.id
  name            = "${local.name_prefix}-elb-listener-http"
  protocol        = "TCP"
  protocol_port   = 80
  default_pool_id = huaweicloud_elb_pool.http.id
  idle_timeout    = 60
  description     = "TCP passthrough — ACME HTTP-01 + .well-known + HTTP→HTTPS redirect at the gateway"
}

# MESH-WIDE node private IPs — consumed by BOTH the gateway ELB (elb_primary)
# members here and the CONSOLE ELB (elb_console) members below. cilium-envoy
# (hostNetwork DaemonSet, gateway-api hostNetwork mode #4706) binds
# node:443/:80 on EVERY node in EVERY region, so every node is a valid member.
#
# #5244 region-kill failover: members were previously PRIMARY-REGION-ONLY, so
# on a region-a kill the ELB health monitors drained every member and the
# public EIP black-holed — externally unreachable for the whole outage — even
# though region-b's cilium-envoy was Running and serving node:443 (proven live
# on hw275 G12, dep 7886def2: all pool members 10.208.1.x/region-a, zero
# 10.218.1.x/region-b, ip_target_enable=false). The fix spans the member set
# across ALL regions' nodes: peer-region nodes are CROSS-VPC (IP-as-backend)
# members reaching the peer VPC over the existing mesh peering + routes
# (huaweicloud_vpc_peering_connection.mesh, ~L224 — the same path the pod
# datapath and the ELB health probes ride). The per-pool TCP health monitors
# (below, unchanged) drain the dead region's members within
# interval×max_retries (~30s) and the survivors carry the EIP. Ports stay the
# durable gateway host ports (443/80 gateway, 8443/8080 console) — hostNetwork
# cilium-envoy host ports, never a nodePort (§854).
locals {
  # CPs are `count`-based — index by tofu's list-indexed array.
  # Workers are `for_each`-based on local.worker_map (key=<region>-<index>).
  # `primary` marks nodes in the ELB's own VPC (region_keys[0]) — they carry
  # the primary subnet_id; peer-region nodes are IP-type members (no subnet,
  # requires cross_vpc_backend on the LB).
  gateway_lb_members = concat(
    [for idx, n in local.cp_nodes : {
      ip      = huaweicloud_compute_instance.control_plane[idx].access_ip_v4
      primary = n.region == local.region_keys[0]
    }],
    [for w in local.worker_nodes : {
      ip      = huaweicloud_compute_instance.worker["${w.region}-${w.index}"].access_ip_v4
      primary = w.region == local.region_keys[0]
    }],
  )

  # #4053 console-isolation gate (Refs #4431 #4212). When "true" (default) the
  # dedicated console ELB stack is created; "false" drops it to save one EIP on
  # a 3-EIP single-region validation prov. The member resources fan out per
  # node, so their count is the node count multiplied by the gate (0 → no
  # members when isolation is off).
  console_isolation_on = var.console_isolation_enabled == "true"
  console_member_count = local.console_isolation_on ? length(local.gateway_lb_members) : 0
}

# Gateway ELB members: ALL regions' nodes (CPs + workers). With cilium
# 1.19.3's gateway-api hostNetwork mode (#4706), cilium-envoy (a hostNetwork
# DaemonSet) binds node:443/:80 on EVERY node, so every node is a valid member
# at the DURABLE host ports — correct from Phase-0 apply, no placeholder, no
# post-convergence nodePort discovery, no nodePort anywhere (§854).
# #5244: peer-region nodes are cross-VPC IP-type members (subnet_id omitted —
# the ELB reaches them over the mesh VPC peering), so a region-a kill leaves
# the health-checked region-b members serving the EIP.
# catalyst-api (post_handover_gateway_elb.go) only heals member-SET drift
# (node churn from the autoscaler) at these same ports.
resource "huaweicloud_elb_member" "https" {
  count         = length(local.gateway_lb_members)
  pool_id       = huaweicloud_elb_pool.https.id
  address       = local.gateway_lb_members[count.index].ip
  protocol_port = var.gateway_member_port_https
  subnet_id     = local.gateway_lb_members[count.index].primary ? huaweicloud_vpc_subnet.region[local.region_keys[0]].ipv4_subnet_id : null
}

resource "huaweicloud_elb_member" "http" {
  count         = length(local.gateway_lb_members)
  pool_id       = huaweicloud_elb_pool.http.id
  address       = local.gateway_lb_members[count.index].ip
  protocol_port = var.gateway_member_port_http
  subnet_id     = local.gateway_lb_members[count.index].primary ? huaweicloud_vpc_subnet.region[local.region_keys[0]].ipv4_subnet_id : null
}

resource "huaweicloud_elb_monitor" "https" {
  pool_id     = huaweicloud_elb_pool.https.id
  protocol    = "TCP"
  port        = var.gateway_member_port_https
  interval    = 10
  timeout     = 5
  max_retries = 3
}

resource "huaweicloud_elb_monitor" "http" {
  pool_id     = huaweicloud_elb_pool.http.id
  protocol    = "TCP"
  port        = var.gateway_member_port_http
  interval    = 10
  timeout     = 5
  max_retries = 3
}

# ── #4053 — Dedicated CONSOLE ELB (poison-proof gateway isolation) ───────────
#
# Cilium Gateway API compiles every HTTPRoute + backend of a Gateway into ONE
# CiliumEnvoyConfig committed atomically; one unresolvable app backend fails the
# whole CEC and 404s the entire gateway (cilium#35728, unfixed on pinned 1.16.5;
# hit live on hw182). The console is isolated onto its OWN Cilium Gateway
# (`cilium-gateway-console` — its own Service type=LoadBalancer on :443/:80,
# hostNetwork off, no NodePort/host-port; #4682 —
# clusters/_template/sovereign-tls/cilium-gateway-console.yaml) with stable
# backends only.
#
# This ELB does TCP passthrough (public :443/:80 → console gateway Service
# nodePort). Because the ELB TCP listener is dumb L4 passthrough (no SNI —
# Huawei SNI requires terminating TLS at the ELB, which would break the
# in-cluster per-prov cert model), the console needs its OWN public EIP:
# console./api.<fqdn> A-records point at THIS ELB's EIP, while the wildcard
# *.<fqdn> points at the RESTORED gateway ELB EIP (elb_primary, #4690).
# DNS split is owned by core/pool-domain-manager (consoleLoadBalancerIP) +
# the catalyst-api handover. Byte-identical design to the Hetzner console LB
# (infra/providers/hetzner).
#
# NOTE (#4690): the console ELB members below still target the console gateway
# Service on :443/:80 — that is the SAME no-CCM-Huawei bug the gateway ELB just
# fixed (ELB→node:443 does not route without a CCM), so console isolation
# (console_isolation_enabled=true) currently needs the same nodePort-reconcile
# treatment. Out of scope for this gateway foundation-fix; tracked as a
# same-class follow-up. The common single-region validation path runs
# console_isolation_enabled=false (console collapses onto the gateway ELB via
# the pool-domain-manager DNS fallback), which is unaffected.

resource "huaweicloud_vpc_eip" "elb_console" {
  count = local.console_isolation_on ? 1 : 0
  publicip {
    type = "5_bgp"
  }
  bandwidth {
    share_type  = "PER"
    name        = "${local.name_prefix}-elb-console-bw"
    size        = 300
    charge_mode = "traffic"
  }
}

resource "huaweicloud_elb_loadbalancer" "console" {
  count             = local.console_isolation_on ? 1 : 0
  name              = "${local.name_prefix}-elb-console"
  vpc_id            = huaweicloud_vpc.region[local.region_keys[0]].id
  ipv4_subnet_id    = huaweicloud_vpc_subnet.region[local.region_keys[0]].ipv4_subnet_id
  ipv4_eip_id       = huaweicloud_vpc_eip.elb_console[0].id
  availability_zone = [var.huawei_az]
  description       = "Catalyst Sovereign ${var.sovereign_fqdn} — #4053 dedicated CONSOLE gateway 443→8443 + 80→8080 passthrough (#4682/#4715)"

  # #5244 — same cross-VPC backend rationale as elb_primary above: the console
  # EIP must keep serving from peer-region nodes when the primary region dies.
  cross_vpc_backend = length(local.region_keys) > 1 ? true : null

  # Immutable-field churn guards: the huaweicloud provider sends null/empty for
  # l4_flavor_id/l7_flavor_id/vpc_id/ipv4_eip_id on subsequent plans (HCS auto-
  # pins them at create; the provider's update path doesn't round-trip them) and
  # HCS rejects the PUT (ELB.8959). They are immutable post-create; ignore them.
  lifecycle {
    ignore_changes = [
      l4_flavor_id,
      l7_flavor_id,
      vpc_id,
      ipv4_eip_id,
    ]
  }
}

resource "huaweicloud_elb_pool" "console_https" {
  count           = local.console_isolation_on ? 1 : 0
  name            = "${local.name_prefix}-elb-pool-console-https"
  protocol        = "TCP"
  lb_method       = "ROUND_ROBIN"
  loadbalancer_id = huaweicloud_elb_loadbalancer.console[0].id
  description     = "cilium-gateway-console LoadBalancer Service targets on port 443"
}

resource "huaweicloud_elb_pool" "console_http" {
  count           = local.console_isolation_on ? 1 : 0
  name            = "${local.name_prefix}-elb-pool-console-http"
  protocol        = "TCP"
  lb_method       = "ROUND_ROBIN"
  loadbalancer_id = huaweicloud_elb_loadbalancer.console[0].id
  description     = "cilium-gateway-console LoadBalancer Service targets on port 80"
}

resource "huaweicloud_elb_listener" "console_https" {
  count           = local.console_isolation_on ? 1 : 0
  loadbalancer_id = huaweicloud_elb_loadbalancer.console[0].id
  name            = "${local.name_prefix}-elb-listener-console-https"
  protocol        = "TCP"
  protocol_port   = 443
  default_pool_id = huaweicloud_elb_pool.console_https[0].id
  idle_timeout    = 60
  description     = "TCP passthrough — cilium-gateway-console terminates TLS at :443"
}

resource "huaweicloud_elb_listener" "console_http" {
  count           = local.console_isolation_on ? 1 : 0
  loadbalancer_id = huaweicloud_elb_loadbalancer.console[0].id
  name            = "${local.name_prefix}-elb-listener-console-http"
  protocol        = "TCP"
  protocol_port   = 80
  default_pool_id = huaweicloud_elb_pool.console_http[0].id
  idle_timeout    = 60
  description     = "TCP passthrough — console HTTP→HTTPS redirect"
}

# Console ELB members: the SAME mesh-wide node set as the primary ELB
# (#5244 — the console EIP black-holed on region-kill for the same
# region-a-only-membership root cause). The console gateway binds
# node:8443/:8080 (host ports, NOT nodePorts — #4715) on every node in every
# region; peer-region nodes are cross-VPC IP-type members like the gateway's.
resource "huaweicloud_elb_member" "console_https" {
  count         = local.console_member_count
  pool_id       = huaweicloud_elb_pool.console_https[0].id
  address       = local.gateway_lb_members[count.index].ip
  protocol_port = 8443
  subnet_id     = local.gateway_lb_members[count.index].primary ? huaweicloud_vpc_subnet.region[local.region_keys[0]].ipv4_subnet_id : null
}

resource "huaweicloud_elb_member" "console_http" {
  count         = local.console_member_count
  pool_id       = huaweicloud_elb_pool.console_http[0].id
  address       = local.gateway_lb_members[count.index].ip
  protocol_port = 8080
  subnet_id     = local.gateway_lb_members[count.index].primary ? huaweicloud_vpc_subnet.region[local.region_keys[0]].ipv4_subnet_id : null
}

resource "huaweicloud_elb_monitor" "console_https" {
  count       = local.console_isolation_on ? 1 : 0
  pool_id     = huaweicloud_elb_pool.console_https[0].id
  protocol    = "TCP"
  port        = 8443
  interval    = 10
  timeout     = 5
  max_retries = 3
}

resource "huaweicloud_elb_monitor" "console_http" {
  count       = local.console_isolation_on ? 1 : 0
  pool_id     = huaweicloud_elb_pool.console_http[0].id
  protocol    = "TCP"
  port        = 8080
  interval    = 10
  timeout     = 5
  max_retries = 3
}
