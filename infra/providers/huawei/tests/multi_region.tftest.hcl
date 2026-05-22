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
