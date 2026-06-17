# Cloud-init user_data size gate for the Catalyst Hetzner Phase-0 module.
#
# WHY THIS FILE EXISTS (incident 2026-06-16, issue #3724 / Refs #3720):
# PR #3715 added +124 lines to infra/providers/_shared/cloudinit-control-
# plane.tftpl, pushing the *rendered* Hetzner control-plane cloud-init past
# the 32256-byte Hetzner `user_data` hard guardrail (main.tf:949 precondition,
# Hetzner's absolute cap is 32768). The PR-level `tofu test` in
# infra-hetzner-tofu.yaml DID fail on that PR — but the red check was not a
# required/blocking check and shared the ambiguous name `validate + fmt + test`
# with the huawei sibling, so it merged anyway. On `push: main` the catalyst-
# build "G36 — tofu test" step then failed on every commit → no catalyst-api
# image built for hours → the mothership silently froze 15 commits behind main
# and never received the #3717 fix. Reverted in #3720/#3721.
#
# This dedicated, single-purpose size suite asserts the REAL rendered cloud-init
# (rendered through the real module under mock_provider — zero render-harness
# drift) stays under the same guardrails the production preconditions enforce.
# The companion `.github/workflows/cloud-init-size.yaml` runs it under a UNIQUE,
# required-able check name (`cloud-init-size / hetzner`) and additionally emits a
# headroom WARNING once the rendered size crosses ~93% of the cap, so the cliff
# is visible before a PR trips it.
#
# These thresholds mirror infra/providers/hetzner/main.tf exactly:
#   - control_plane_cloud_init         <= 32256  (main.tf:949)
#   - worker_cloud_init                <= 30720  (main.tf:959)
#   - each secondary_region_cloud_init <= 32256  (main.tf:1428)
# Keep them in lockstep if the production preconditions ever change.
#
# nonsensitive() wraps every length() because local.*_cloud_init embeds
# sensitive tokens (ghcr / harbor / object-storage creds); without it OpenTofu
# redacts the whole error_message and the operator can't see the byte overshoot
# (same reason main.tf:956 wraps its precondition message). length() of a
# sensitive string is itself sensitive; nonsensitive(length(...)) leaks only the
# integer byte count, never any token bytes.

mock_provider "hcloud" {
  mock_resource "hcloud_network" {
    defaults = { id = "1" }
  }
  mock_resource "hcloud_network_subnet" {
    defaults = { id = "1" }
  }
  mock_resource "hcloud_load_balancer" {
    defaults = { id = "1", ipv4 = "203.0.113.10" }
  }
  mock_resource "hcloud_load_balancer_network" {
    defaults = { id = "1" }
  }
  mock_resource "hcloud_load_balancer_target" {
    defaults = { id = "1" }
  }
  mock_resource "hcloud_load_balancer_service" {
    defaults = { id = "1" }
  }
  mock_resource "hcloud_server" {
    defaults = { id = "1", ipv4_address = "203.0.113.20" }
  }
  mock_resource "hcloud_ssh_key" {
    defaults = { id = "1" }
  }
  mock_resource "hcloud_firewall" {
    defaults = { id = "1" }
  }
}

# Same aws (Object Storage) overrides the multi_region suite uses — bypass the
# provider's schema-validation race under `tofu test` (see multi_region.tftest.hcl
# for the full rationale). Size rendering doesn't touch these resources.
override_resource {
  target = aws_s3_bucket.main
  values = { id = "catalyst-test-example-com" }
}

override_resource {
  target = aws_s3_bucket_acl.main
  values = {}
}

variables {
  sovereign_fqdn             = "test.example.com"
  sovereign_subdomain        = "test"
  org_name                   = "Test Org"
  org_email                  = "ops@example.com"
  hcloud_token               = "mock-hcloud-token"
  hcloud_project_id          = "12345"
  region                     = "hel1"
  control_plane_size         = "cpx22"
  worker_size                = "cpx32"
  worker_count               = 2
  k3s_version                = "v1.31.4+k3s1"
  ssh_public_key             = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITESTKEYTESTKEYTESTKEYTESTKEYTESTKEY test@local"
  object_storage_region      = "nbg1"
  object_storage_access_key  = "TESTACCESSKEY1234567"
  object_storage_secret_key  = "TESTSECRETKEYTESTSECRETKEYTESTSE"
  object_storage_bucket_name = "catalyst-test-example-com"
  domain_mode                = "byo"

  # Two-region payload so the secondary-region cloud-init locals are
  # materialised and their size asserted (the secondary render carries its own
  # per-region substitutes and was the exact local that also overshot on #3715).
  regions = [
    { provider = "hetzner", cloudRegion = "hel1", controlPlaneSize = "cpx22", workerSize = "cpx32", workerCount = 2 },
    { provider = "hetzner", cloudRegion = "nbg1", controlPlaneSize = "cpx22", workerSize = "cpx32", workerCount = 2 }
  ]
}

run "control_plane_cloud_init_under_32256" {
  command = plan
  assert {
    condition     = nonsensitive(length(local.control_plane_cloud_init)) <= 32256
    error_message = "Primary control-plane cloud-init is ${nonsensitive(length(local.control_plane_cloud_init))} bytes, exceeds the 32256-byte Hetzner user_data guardrail (hard cap 32768). Cull comments / move bloat out of infra/providers/_shared/cloudinit-control-plane.tftpl. See issues #966 / #3724."
  }
}

run "worker_cloud_init_under_30720" {
  command = plan
  assert {
    condition     = nonsensitive(length(local.worker_cloud_init)) <= 30720
    error_message = "Worker cloud-init is ${nonsensitive(length(local.worker_cloud_init))} bytes, exceeds the 30720-byte worker guardrail (hard cap 32768). Cull comments / move bloat out of infra/providers/hetzner/cloudinit-worker.tftpl. See issue #966."
  }
}

run "secondary_region_cloud_init_under_32256" {
  command = plan
  assert {
    condition     = alltrue([for k, v in local.secondary_region_cloud_init : nonsensitive(length(v)) <= 32256])
    error_message = "At least one secondary-region control-plane cloud-init exceeds the 32256-byte Hetzner user_data guardrail. Per-region sizes: ${jsonencode({ for k, v in local.secondary_region_cloud_init : k => nonsensitive(length(v)) })}."
  }
}
