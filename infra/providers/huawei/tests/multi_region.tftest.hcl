# Multi-region wiring tests for the Catalyst Huawei Phase-0 module.
#
# Exercises the per-region shape WITHOUT touching real Huawei API —
# both providers are mocked so the test runs offline in CI on every PR
# touching infra/providers/huawei/**. Mirrors the structure of
# infra/providers/hetzner/tests/multi_region.tftest.hcl so future
# cross-provider tests can lift this pattern verbatim.

mock_provider "huaweicloud" {
  mock_resource "huaweicloud_vpc" {
    defaults = {
      id = "vpc-mock-1"
    }
  }
  mock_resource "huaweicloud_vpc_subnet" {
    defaults = {
      id = "subnet-mock-1"
    }
  }
  mock_resource "huaweicloud_vpc_eip" {
    defaults = {
      id      = "eip-mock-1"
      address = "203.0.113.10"
    }
  }
  mock_resource "huaweicloud_networking_secgroup" {
    defaults = {
      id = "sg-mock-1"
    }
  }
  mock_resource "huaweicloud_networking_secgroup_rule" {
    defaults = {
      id = "sgr-mock-1"
    }
  }
  mock_resource "huaweicloud_compute_instance" {
    defaults = {
      id           = "ecs-mock-1"
      access_ip_v4 = "10.20.1.10"
    }
  }
  mock_resource "huaweicloud_kps_keypair" {
    defaults = {
      id = "kp-mock-1"
    }
  }
  mock_resource "huaweicloud_obs_bucket" {
    defaults = {
      id = "catalyst-test-bucket"
    }
  }
}

variables {
  deployment_id       = "deadbeefcafef00d"
  sovereign_fqdn      = "t99.omani.works"
  huawei_access_key   = "AKIAEXAMPLEAKIAEXAMPLE"
  huawei_secret_key   = "secret-key-not-real-just-padding-32chars"
  huawei_project_id   = "0123456789abcdef0123456789abcdef"
  org_name            = "Acme Test"
  org_email           = "admin@example.com"
  obs_bucket_name     = "catalyst-t99-omani-works-deadbeef"
  ssh_public_key      = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForTest test@bastion"
  parent_domains_yaml = ""
}

# Scenario 1 — Tier-B canonical two-region shape (primary + secondary)
# at the Wave 5.7 POC default (huawei_control_plane_count=1).
# Verifies the new HCS-quota-fit footprint: 2 VPCs + 2 subnets + 2 SGs
# + 2 EIPs (one per region — was 6 in the historical fixed-3 CP design)
# + 2 CP ECS + 4 worker ECS + 1 OBS bucket.
run "tier_b_two_regions_poc" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      },
      {
        code               = "me-east-215-b"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  # 2 VPCs (one per region)
  assert {
    condition     = length(huaweicloud_vpc.region) == 2
    error_message = "Tier-B must create one VPC per region (expected 2)."
  }

  # 2 subnets (one per VPC)
  assert {
    condition     = length(huaweicloud_vpc_subnet.region) == 2
    error_message = "Tier-B must create one subnet per VPC (expected 2)."
  }

  # 2 security groups (one per region)
  assert {
    condition     = length(huaweicloud_networking_secgroup.region) == 2
    error_message = "Tier-B must create one security group per region (expected 2)."
  }

  # #3504 (Refs #3375): per-region pod + service CIDRs must be DISTINCT
  # across regions (DoD gate D11 — non-overlapping ClusterMesh pod CIDRs).
  # Regression guard for the hardcoded 10.42.0.0/16 / 10.96.0.0/16 that
  # made both regions' cp1 advertise 10.42.0.0/24 → ClusterMesh PodCIDR
  # collision → cp1 datapath mis-route → region-kill blocked on hw135.
  assert {
    condition     = local.region_cluster_cidr["me-east-215-a"] == "10.42.0.0/16"
    error_message = "Primary region pod CIDR must be 10.42.0.0/16."
  }
  assert {
    condition     = local.region_cluster_cidr["me-east-215-b"] == "10.43.0.0/16"
    error_message = "Secondary region pod CIDR must be 10.43.0.0/16 (offset by region index — must NOT collide with the primary's 10.42.0.0/16)."
  }
  assert {
    condition     = local.region_cluster_cidr["me-east-215-a"] != local.region_cluster_cidr["me-east-215-b"]
    error_message = "Per-region pod CIDRs MUST be distinct across ClusterMesh peers (D11) — a collision mis-routes the cp1 apiserver→webhook datapath."
  }
  assert {
    condition     = local.region_service_cidr["me-east-215-a"] == "10.96.0.0/16" && local.region_service_cidr["me-east-215-b"] == "10.97.0.0/16"
    error_message = "Per-region service CIDRs must be 10.96.0.0/16 (primary) and 10.97.0.0/16 (secondary) — offset by region index."
  }

  # Wave 5.7: 2 EIPs (one per region) — was 6 (3 CP × 2 regions) in the
  # historical Hetzner-style fixed-3 design. The new per-region EIP keys
  # by region code (`cp["me-east-215-a"]`) rather than CP index.
  assert {
    condition     = length(huaweicloud_vpc_eip.cp) == 2
    error_message = "Wave 5.7 default (1 CP/region) must create 1 EIP per region (expected 2 for 2-region Tier-B)."
  }

  # Wave 5.7: 2 CP ECS (1 per region with huawei_control_plane_count=1).
  assert {
    condition     = length(huaweicloud_compute_instance.control_plane) == 2
    error_message = "Wave 5.7 POC default (huawei_control_plane_count=1) must create 1 CP ECS per region (expected 2 total)."
  }

  # 4 worker ECS (regions[].worker_count=2 × 2 regions).
  assert {
    condition     = length(huaweicloud_compute_instance.worker) == 4
    error_message = "Tier-B with worker_count=2 must create 2 worker ECS per region (expected 4 total)."
  }

  # 1 OBS bucket per Sovereign — Wave 5.5 switched from aws_s3_bucket
  # to native huaweicloud_obs_bucket (HCS OBS does not expose
  # GetBucketEncryption that the aws provider always probes).
  assert {
    condition     = huaweicloud_obs_bucket.main.bucket == "catalyst-t99-omani-works-deadbeef"
    error_message = "OBS bucket name must match the operator-supplied obs_bucket_name."
  }

  # #5244 — gateway ELB members span ALL regions' nodes (2 CP + 4 workers =
  # 6), not just the primary's. Primary-region-only membership black-holed
  # the gateway EIP for the whole region-a outage on hw275 G12: the health
  # monitors drained every member and nothing was left to serve.
  assert {
    condition     = length(huaweicloud_elb_member.https) == 6 && length(huaweicloud_elb_member.http) == 6
    error_message = "Gateway ELB pools must contain EVERY region's nodes (6 for 2×(1 CP + 2 workers)) so a region kill leaves the peer region serving the EIP (#5244)."
  }
  assert {
    condition     = length([for m in local.gateway_lb_members : m if !m.primary]) == 3
    error_message = "3 of the 6 gateway ELB members must be peer-region (region-b) nodes (#5244)."
  }
  # Peer-region members are cross-VPC IP-type members: subnet_id omitted.
  assert {
    condition     = length([for i, m in local.gateway_lb_members : i if !m.primary && huaweicloud_elb_member.https[i].subnet_id != null]) == 0
    error_message = "Peer-region gateway members must OMIT subnet_id (cross-VPC IP-type member — the peer node's IP is outside the ELB's VPC, #5244)."
  }
  # Cross-VPC (IP-as-backend) must be on for a multi-region prov, or the
  # peer-region member creates are rejected by the ELB API.
  assert {
    condition     = huaweicloud_elb_loadbalancer.primary.cross_vpc_backend == true
    error_message = "Multi-region provs must enable cross_vpc_backend on the gateway ELB (#5244)."
  }
  # §854 — the member ports stay the durable cilium-envoy hostNetwork host
  # ports; the k8s NodePort range must never appear.
  assert {
    condition     = length([for m in huaweicloud_elb_member.https : m if m.protocol_port >= 30000]) == 0 && length([for m in huaweicloud_elb_member.http : m if m.protocol_port >= 30000]) == 0
    error_message = "Gateway ELB member ports must stay below the k8s NodePort range — nodePorts are forbidden (§854)."
  }
}

# Scenario 2 — single-region shape (1 CP region, no secondary). Verifies
# Wave 4 single-region provisioning still satisfies the contract.
run "single_region" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = length(huaweicloud_vpc.region) == 1
    error_message = "Single-region must create exactly 1 VPC."
  }
  # Wave 5.7: 1 EIP per region.
  assert {
    condition     = length(huaweicloud_vpc_eip.cp) == 1
    error_message = "Single-region (Wave 5.7) must create 1 CP EIP."
  }
  # Wave 5.7: 1 CP per region (was 3).
  assert {
    condition     = length(huaweicloud_compute_instance.control_plane) == 1
    error_message = "Single-region (Wave 5.7) must create 1 CP ECS (huawei_control_plane_count=1)."
  }
  assert {
    condition     = length(huaweicloud_compute_instance.worker) == 2
    error_message = "Single-region must create 2 worker ECS."
  }
  # #5244 — single-region keeps the pre-#5244 member shape: all members are
  # same-VPC (subnet_id set), and cross_vpc_backend stays provider-default.
  assert {
    condition     = length(huaweicloud_elb_member.https) == 3 && length([for m in local.gateway_lb_members : m if !m.primary]) == 0
    error_message = "Single-region gateway ELB members must be exactly the region's own 3 nodes, all primary (no cross-VPC members, #5244)."
  }
}

# Scenario 4 (#3110) — the rendered primary-CP cloud-init carries the
# canonical SOVEREIGN_REGIONS_JSON substitute populated from var.regions.
# Pre-#3110 the HCS port omitted this key entirely, so the bootstrap-kit
# Kustomization's `$${SOVEREIGN_REGIONS_JSON:-}` resolved empty → the
# bp-catalyst-platform chart shipped `regionsJson=[]` in the sovereign-
# fqdn ConfigMap on every HCS Sovereign (caught live on hw101), forcing
# #3109's discrete primary/replica fallback. These asserts pin the
# canonical fix: regionsJson is non-empty, re-keyed into the Go RegionSpec
# JSON shape (cloudRegion, NOT the HCS-native `code`), and uses the
# 4-segment canonical region label so it matches the discrete
# SOVEREIGN_PRIMARY_REGION / SOVEREIGN_REPLICA_REGION format.
run "regions_json_populated_3110" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      },
      {
        code               = "me-east-215-b"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 3
      }
    ]
  }

  # SOVEREIGN_REGIONS_JSON key is present and NON-empty (`[]` is the bug).
  assert {
    condition = strcontains(
      local.cp_cloud_init_by_region["me-east-215-a"],
      "SOVEREIGN_REGIONS_JSON:"
    )
    error_message = "Primary-CP cloud-init must emit the SOVEREIGN_REGIONS_JSON substitute key (#3110)."
  }
  # The empty-array regression must NOT appear (single-quoted JSON-flow).
  assert {
    condition = !strcontains(
      local.cp_cloud_init_by_region["me-east-215-a"],
      "SOVEREIGN_REGIONS_JSON: '[]'"
    )
    error_message = "SOVEREIGN_REGIONS_JSON must be populated, not the empty-array `[]` regression (#3110/#3106)."
  }
  # Both regions appear in the JSON, in the canonical 4-segment label form
  # (cloudRegion=hw-<code>-rtz-prod), matching the discrete primary/replica
  # substitutes — NOT the bare HCS `code`.
  assert {
    condition = strcontains(
      local.cp_cloud_init_by_region["me-east-215-a"],
      "hw-me-east-215-a-rtz-prod"
    )
    error_message = "regionsJson must carry the primary region's canonical label hw-me-east-215-a-rtz-prod (#3110)."
  }
  assert {
    condition = strcontains(
      local.cp_cloud_init_by_region["me-east-215-a"],
      "hw-me-east-215-b-rtz-prod"
    )
    error_message = "regionsJson must carry the replica region's canonical label hw-me-east-215-b-rtz-prod (#3110)."
  }
  # The Go RegionSpec field name `cloudRegion` must be present — proves the
  # HCS-native `code`/`control_plane_size` keys were re-mapped so
  # catalyst-api's RegionSpec json.Unmarshal can read it.
  assert {
    condition = strcontains(
      local.cp_cloud_init_by_region["me-east-215-a"],
      "\"cloudRegion\""
    )
    error_message = "regionsJson must use the Go RegionSpec field name cloudRegion (re-keyed from HCS `code`) so catalyst-api can unmarshal it (#3110)."
  }
  # Per-region SKU detail (worker_count) is threaded through, so the full
  # RegionSpec[] (not just region names) reaches the chroot.
  assert {
    condition = strcontains(
      local.cp_cloud_init_by_region["me-east-215-a"],
      "\"workerCount\":3"
    )
    error_message = "regionsJson must carry per-region SKU detail (replica workerCount=3) — full RegionSpec[], not just region names (#3110)."
  }
  # Companion key — configuredRegions list also populated with canonical
  # labels for the Dashboard SovereignCard + ClusterMesh chips.
  assert {
    condition = strcontains(
      local.cp_cloud_init_by_region["me-east-215-a"],
      "SOVEREIGN_CONFIGURED_REGIONS_YAML:"
    )
    error_message = "Primary-CP cloud-init must emit SOVEREIGN_CONFIGURED_REGIONS_YAML (#3110)."
  }
}

# Scenario 5 (#3110) — single-region Sovereign still emits a populated,
# 1-entry regionsJson (NOT empty), so a 1-region HCS prov also surfaces
# its region in /cloud without the discrete fallback.
run "regions_json_single_region_3110" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition = !strcontains(
      local.cp_cloud_init_by_region["me-east-215-a"],
      "SOVEREIGN_REGIONS_JSON: '[]'"
    )
    error_message = "Single-region HCS prov must still emit a populated regionsJson, not `[]` (#3110)."
  }
  assert {
    condition = strcontains(
      local.cp_cloud_init_by_region["me-east-215-a"],
      "hw-me-east-215-a-rtz-prod"
    )
    error_message = "Single-region regionsJson must carry the region's canonical label (#3110)."
  }
}

# Scenario 3 — HA topology (huawei_control_plane_count=3) for tenancies
# with adequate EIP headroom (public Huawei Cloud or HCS POD enterprise
# tier). Verifies replica CPs (index > 0) do NOT consume EIPs.
run "ha_three_cp_per_region" {
  command = plan

  variables {
    huawei_control_plane_count = 3
    regions = [
      {
        code               = "me-east-215-a"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  # 3 CP ECS (count=3).
  assert {
    condition     = length(huaweicloud_compute_instance.control_plane) == 3
    error_message = "HA topology must create 3 CP ECS per region."
  }
  # Wave 5.7: still 1 EIP per region — replica CPs (index 1, 2) carry
  # only private IPs. ClusterMesh peers dial the primary CP's EIP.
  assert {
    condition     = length(huaweicloud_vpc_eip.cp) == 1
    error_message = "Wave 5.7: even in HA (count=3) only the primary CP per region gets an EIP."
  }
}

# ── #4057: SOVEREIGN_CNPG_STORAGE_CLASS reflects the operator's
# ── default_storage_class tofu var (no longer a hardcoded `evs-ssd`) ────────
# The catalyst-api Request.StorageClass → var.default_storage_class → the
# cloud-init Kustomization postBuild.substitute SOVEREIGN_CNPG_STORAGE_CLASS →
# the host-shared CNPG Cluster CRs + the bp-cnpg/bp-mgmt-vcluster default class.
# On Huawei the per-provider durable default is `evs-ssd` (bp-huawei-evs-csi
# slot 55b). local-path is FORBIDDEN; the var validation rejects "" and
# "local-path".
run "default_storage_class_falls_back_to_evs_ssd" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
    # default_storage_class NOT set → the variables.tf default `evs-ssd`
    # applies (the per-provider Huawei EVS CSI durable class).
  }

  assert {
    condition = strcontains(
      local.cp_cloud_init_by_region["me-east-215-a"],
      "SOVEREIGN_CNPG_STORAGE_CLASS: \"evs-ssd\""
    )
    error_message = "Primary-CP cloud-init must render SOVEREIGN_CNPG_STORAGE_CLASS=\"evs-ssd\" when default_storage_class is unset (the per-provider Huawei EVS CSI default). #4057"
  }

  assert {
    condition = !strcontains(
      local.cp_cloud_init_by_region["me-east-215-a"],
      "SOVEREIGN_CNPG_STORAGE_CLASS: \"local-path\""
    )
    error_message = "Primary-CP cloud-init must NEVER render SOVEREIGN_CNPG_STORAGE_CLASS=\"local-path\" — local-path is FORBIDDEN (#3971/#892)."
  }
}

run "default_storage_class_operator_override_renders_huawei" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
    default_storage_class = "evs-sas"
  }

  assert {
    condition = strcontains(
      local.cp_cloud_init_by_region["me-east-215-a"],
      "SOVEREIGN_CNPG_STORAGE_CLASS: \"evs-sas\""
    )
    error_message = "Primary-CP cloud-init must render the operator-chosen default_storage_class verbatim into SOVEREIGN_CNPG_STORAGE_CLASS. #4057"
  }
}
