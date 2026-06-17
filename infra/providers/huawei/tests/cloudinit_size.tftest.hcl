# Cloud-init user_data size gate for the Catalyst Huawei (HCS) Phase-0 module.
#
# WHY THIS FILE EXISTS (incident 2026-06-16, issue #3724 / Refs #3720):
# The 32256-byte regression that froze catalyst-build for hours (PR #3715) bloated
# infra/providers/_shared/cloudinit-control-plane.tftpl — the SAME template this
# Huawei module renders (see local.cp_cloud_init_by_region below). Hetzner caught
# it via its main.tf:949 precondition; Huawei caught NOTHING because this module
# has *no* user_data size precondition at all (see the comment at main.tf:625:
# "HCS does not document a hard cap ... but we keep the same conservative budget
# so the same template scales to fresh-prov against either cloud"). That budget
# was aspirational — nothing enforced it. This suite enforces it.
#
# A `_shared`-template bloat that happens to fit Huawei's render but overshoots
# Hetzner's would previously only fail on the Hetzner side; a Huawei-only bloat
# would be caught nowhere. This gate closes both gaps by asserting Huawei's REAL
# rendered cloud-init (rendered through the real module under mock_provider —
# zero render-harness drift) against the same conservative budget Hetzner enforces.
# The companion `.github/workflows/cloud-init-size.yaml` runs it under a UNIQUE,
# required-able check name (`cloud-init-size / huawei`) and emits a headroom
# WARNING once any region's render crosses ~93% of the cap.
#
# Budget (mirrors the Hetzner module's hard caps — the shared template targets
# the stricter of the two clouds so one template fresh-provs against either):
#   - cp_cloud_init_by_region[*]     <= 32256
#   - worker_cloud_init_by_region[*] <= 30720
#
# nonsensitive() wraps every length() because the rendered cloud-init embeds
# sensitive tokens (huawei AK/SK, harbor / ghcr creds); without it OpenTofu
# redacts the whole error_message and the operator can't see the byte overshoot.
# length() of a sensitive string is itself sensitive; nonsensitive(length(...))
# leaks only the integer byte count, never any token bytes.

mock_provider "huaweicloud" {
  mock_resource "huaweicloud_vpc" {
    defaults = { id = "vpc-mock-1" }
  }
  mock_resource "huaweicloud_vpc_subnet" {
    defaults = { id = "subnet-mock-1" }
  }
  mock_resource "huaweicloud_vpc_eip" {
    defaults = { id = "eip-mock-1", address = "203.0.113.10" }
  }
  mock_resource "huaweicloud_networking_secgroup" {
    defaults = { id = "sg-mock-1" }
  }
  mock_resource "huaweicloud_networking_secgroup_rule" {
    defaults = { id = "sgr-mock-1" }
  }
  mock_resource "huaweicloud_compute_instance" {
    defaults = { id = "ecs-mock-1", access_ip_v4 = "10.20.1.10" }
  }
  mock_resource "huaweicloud_kps_keypair" {
    defaults = { id = "kp-mock-1" }
  }
  mock_resource "huaweicloud_obs_bucket" {
    defaults = { id = "catalyst-test-bucket" }
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

  # Two-region Tier-B payload so BOTH the primary and the secondary per-region
  # cloud-init renders are materialised and size-asserted.
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

run "control_plane_cloud_init_under_32256" {
  command = plan
  assert {
    condition     = alltrue([for k, v in local.cp_cloud_init_by_region : nonsensitive(length(v)) <= 32256])
    error_message = "At least one region's control-plane cloud-init exceeds the 32256-byte user_data budget. Per-region sizes: ${jsonencode({ for k, v in local.cp_cloud_init_by_region : k => nonsensitive(length(v)) })}. Cull comments / move bloat out of infra/providers/_shared/cloudinit-control-plane.tftpl. See issue #3724."
  }
}

run "worker_cloud_init_under_30720" {
  command = plan
  assert {
    condition     = alltrue([for k, v in local.worker_cloud_init_by_region : nonsensitive(length(v)) <= 30720])
    error_message = "At least one region's worker cloud-init exceeds the 30720-byte user_data budget. Per-region sizes: ${jsonencode({ for k, v in local.worker_cloud_init_by_region : k => nonsensitive(length(v)) })}. Cull comments / move bloat out of infra/providers/huawei/cloudinit-worker.tftpl. See issue #3724."
  }
}
