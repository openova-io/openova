# Multi-region wiring tests for the Catalyst Hetzner Phase-0 module.
#
# Slice G1 (EPIC-0 #1095) wires every entry in var.regions[] end-to-end.
# These tests exercise the wiring shape WITHOUT touching real Hetzner —
# both providers are mocked so the test runs offline in CI on every PR
# touching infra/hetzner/**.
#
# Three scenarios:
#   1. Legacy single-region shape (var.regions = []) — every secondary
#      output is empty and the singular-path resources are unchanged.
#   2. Single-entry regions list (len(regions) == 1) — secondary
#      resources stay empty (regions[0] is owned by the singular path),
#      preserving the cost-zero shape for solo Sovereigns that pre-date
#      the per-region wizard payload.
#   3. Three-region shape (mgmt + fsn + hel — the EPIC-6 #1101
#      Continuum DR shape per docs/EPICS-1-6-unified-design.md §3.8 +
#      §11) — secondary resources fire for regions[1] and regions[2],
#      producing two extra subnets, two extra CPs, four extra workers
#      (two per region), and two extra LBs.

# ── Provider mocks ────────────────────────────────────────────────────────
# `mock_provider` short-circuits the apply path so `command = plan`
# does not need real credentials. Every resource the module declares
# gets a synthetic computed-value response.
#
# The hcloud provider returns integer IDs for hcloud_network +
# hcloud_load_balancer + hcloud_server (the schema declares them as
# `type: number`, not strings — `mock_provider`'s default string IDs
# fail the type-check). We override the defaults below so the plan
# graph wires up correctly.
#
# The minio provider needs minio_server (and friends) populated even
# under mocking — its provider-block validation runs before any
# resource is processed. We pass the same defaults the production
# config uses, computed from the same vars.

mock_provider "hcloud" {
  mock_resource "hcloud_network" {
    defaults = {
      id = "1"
    }
  }
  mock_resource "hcloud_network_subnet" {
    defaults = {
      id = "1"
    }
  }
  mock_resource "hcloud_load_balancer" {
    defaults = {
      id   = "1"
      ipv4 = "203.0.113.10"
    }
  }
  mock_resource "hcloud_load_balancer_network" {
    defaults = {
      id = "1"
    }
  }
  mock_resource "hcloud_load_balancer_target" {
    defaults = {
      id = "1"
    }
  }
  mock_resource "hcloud_load_balancer_service" {
    defaults = {
      id = "1"
    }
  }
  mock_resource "hcloud_server" {
    defaults = {
      id           = "1"
      ipv4_address = "203.0.113.20"
    }
  }
  mock_resource "hcloud_ssh_key" {
    defaults = {
      id = "1"
    }
  }
  mock_resource "hcloud_firewall" {
    defaults = {
      id = "1"
    }
  }
}

# The hashicorp/aws provider has Required attributes (region etc.) at
# the schema level. Under `tofu test`, OpenTofu still type-checks the
# provider block before any `mock_provider` rewriting fires, and the
# production provider config in versions.tf reads from
# `var.object_storage_region` which the test framework cannot supply
# pre-evaluation in 1.8.5. Workaround: bypass the provider entirely by
# overriding the two aws resources the module declares (the bucket +
# its ACL). The overrides return synthetic values without ever invoking
# the provider — same outcome as a mock but without the schema-validation
# race. Slice G1 doesn't touch these resources; the overrides are a
# test-harness concession only.
#
# History: pre-fix-#133 this file overrode `minio_s3_bucket.main` for
# the same reason against the aminueza/minio provider. The provider was
# swapped to hashicorp/aws to escape that provider's AccessDenied wedge
# on Hetzner Object Storage credentials (see versions.tf).
override_resource {
  target = aws_s3_bucket.main
  values = {
    id     = "catalyst-test-example-com"
    bucket = "catalyst-test-example-com"
  }
}

override_resource {
  target = aws_s3_bucket_acl.main
  values = {}
}

# ── Variables shared across scenarios ─────────────────────────────────────

variables {
  sovereign_fqdn      = "test.example.com"
  sovereign_subdomain = "test"
  org_name            = "Test Org"
  org_email           = "ops@example.com"
  hcloud_token        = "mock-hcloud-token"
  hcloud_project_id   = "12345"

  # Legacy singular path — regions[0] mirror.
  region             = "nbg1"
  control_plane_size = "cpx22"
  worker_size        = "cpx32"
  worker_count       = 2

  k3s_version    = "v1.31.4+k3s1"
  ssh_public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITESTKEYTESTKEYTESTKEYTESTKEYTESTKEY test@local"

  object_storage_region      = "nbg1"
  object_storage_access_key  = "TESTACCESSKEY1234567"
  object_storage_secret_key  = "TESTSECRETKEYTESTSECRETKEYTESTSE"
  object_storage_bucket_name = "catalyst-test-example-com"

  domain_mode = "byo"
}

# ── Scenario 1: legacy single-region (var.regions = []) ──────────────────
# Confirms slice G1 is purely additive — when no per-region payload is
# supplied (every Sovereign provisioned before slice G1 + every wizard
# request body that omits the regions field), the multi-region overlay
# is a no-op and no secondary resources are planned.

run "legacy_no_regions_payload" {
  command = plan

  variables {
    regions = []
  }

  assert {
    condition     = length(output.secondary_region_keys) == 0
    error_message = "secondary_region_keys must be empty when var.regions=[] (legacy single-region apply path)."
  }

  assert {
    condition     = length(output.control_plane_ips_by_region) == 0
    error_message = "control_plane_ips_by_region must be empty when var.regions=[]."
  }

  assert {
    condition     = length(output.load_balancer_ips_by_region) == 0
    error_message = "load_balancer_ips_by_region must be empty when var.regions=[]."
  }
}

# ── Scenario 2: single-entry regions[] ────────────────────────────────────
# regions[0] is owned by the singular path so even with len(regions)==1
# no secondary resources are planned. This protects existing wizard
# payloads (every wizard run today emits 1 entry) from accidentally
# materialising duplicate primary-region resources.

run "single_region_entry_does_not_double_provision" {
  command = plan

  variables {
    regions = [
      {
        provider         = "hetzner"
        cloudRegion      = "nbg1"
        controlPlaneSize = "cpx22"
        workerSize       = "cpx32"
        workerCount      = 2
      }
    ]
  }

  assert {
    condition     = length(output.secondary_region_keys) == 0
    error_message = "regions[0] is owned by the singular path; secondary_region_keys must be empty for len(regions)==1."
  }
}

# ── Scenario 3: three-region EPIC-6 shape (mgmt + fsn + hel) ─────────────
# Mirrors docs/EPICS-1-6-unified-design.md §3.8: one mgmt cluster
# (regions[0], drives the singular path) + two data-plane clusters in
# fsn1 + hel1 (regions[1..2], drive the secondary overlay).
#
# The slice G1 success gate: secondary_region_keys carries exactly two
# entries, in stable insertion order, with the deterministic
# "{cloudRegion}-{index}" naming.

run "three_region_mgmt_fsn_hel" {
  command = plan

  variables {
    regions = [
      {
        provider         = "hetzner"
        cloudRegion      = "nbg1"
        controlPlaneSize = "cpx32"
        workerSize       = "cpx32"
        workerCount      = 1
      },
      {
        provider         = "hetzner"
        cloudRegion      = "fsn1"
        controlPlaneSize = "cpx32"
        workerSize       = "cpx32"
        workerCount      = 2
      },
      {
        provider         = "hetzner"
        cloudRegion      = "hel1"
        controlPlaneSize = "cpx32"
        workerSize       = "cpx32"
        workerCount      = 2
      },
    ]
  }

  assert {
    condition     = length(output.secondary_region_keys) == 2
    error_message = "Three-region payload (mgmt + fsn + hel) must produce exactly 2 secondary regions (regions[1..2])."
  }

  assert {
    condition     = contains(output.secondary_region_keys, "fsn1-1")
    error_message = "secondary_region_keys must contain the fsn1-1 key for regions[1] (Falkenstein data plane)."
  }

  assert {
    condition     = contains(output.secondary_region_keys, "hel1-2")
    error_message = "secondary_region_keys must contain the hel1-2 key for regions[2] (Helsinki data plane)."
  }
}

# ── Scenario 4: same-region duplicate ────────────────────────────────────
# Same cloudRegion appearing multiple times must produce distinct
# secondary keys (the index suffix is what makes them unique). Tests
# the deterministic naming rule that "{cloudRegion}-{index}" never
# collapses to a single key.

run "same_region_duplicates_produce_distinct_keys" {
  command = plan

  variables {
    regions = [
      {
        provider         = "hetzner"
        cloudRegion      = "nbg1"
        controlPlaneSize = "cpx22"
        workerSize       = "cpx32"
        workerCount      = 1
      },
      {
        provider         = "hetzner"
        cloudRegion      = "fsn1"
        controlPlaneSize = "cpx32"
        workerSize       = "cpx32"
        workerCount      = 1
      },
      {
        provider         = "hetzner"
        cloudRegion      = "fsn1"
        controlPlaneSize = "cpx32"
        workerSize       = "cpx32"
        workerCount      = 1
      },
    ]
  }

  assert {
    condition     = length(output.secondary_region_keys) == 2
    error_message = "Two fsn1 entries at indices 1 and 2 must produce 2 distinct secondary keys (fsn1-1 + fsn1-2)."
  }

  assert {
    condition     = contains(output.secondary_region_keys, "fsn1-1") && contains(output.secondary_region_keys, "fsn1-2")
    error_message = "Same-region duplicates must yield index-distinguished keys fsn1-1 and fsn1-2."
  }
}

# ── Scenario 5: non-Hetzner regions are skipped by the Hetzner module ────
# A regions[] payload may carry entries for other providers (oci, aws,
# huawei) when the operator chose multi-cloud at signup. The Hetzner
# module's overlay filters by `r.provider == "hetzner"` so non-Hetzner
# entries are quietly ignored here — sister provider modules (slice G2,
# G4, …) own their own iteration. This test pins that contract so a
# regression silently materialising a non-Hetzner row in the Hetzner
# overlay fails fast.

run "non_hetzner_regions_are_filtered_out" {
  command = plan

  variables {
    regions = [
      {
        provider         = "hetzner"
        cloudRegion      = "nbg1"
        controlPlaneSize = "cpx22"
        workerSize       = "cpx32"
        workerCount      = 1
      },
      {
        provider         = "hetzner"
        cloudRegion      = "fsn1"
        controlPlaneSize = "cpx32"
        workerSize       = "cpx32"
        workerCount      = 2
      },
      {
        provider         = "oci"
        cloudRegion      = "fra"
        controlPlaneSize = "VM.Standard.E5.Flex.4.32"
        workerSize       = "VM.Standard.E5.Flex.4.32"
        workerCount      = 2
      },
    ]
  }

  assert {
    condition     = length(output.secondary_region_keys) == 1
    error_message = "OCI region at index 2 must be filtered out by the Hetzner overlay; only fsn1-1 should remain in secondary_region_keys."
  }

  assert {
    condition     = contains(output.secondary_region_keys, "fsn1-1")
    error_message = "fsn1-1 (regions[1], hetzner) must be present after filtering."
  }
}

# ── Scenario 6: QA-mode auto-flips to bigger SKUs (Fix #157) ─────────────
# Customer Sovereigns (qa_fixtures_enabled='false') keep the cpx22 CP /
# cpx32 worker production defaults. QA Sovereigns (qa_fixtures_enabled=
# 'true') auto-flip to qa_control_plane_size (cpx32 default) and
# qa_worker_size (cpx42 default) so the bp-keycloak/harbor/cnpg/openbao +
# qaFixtures Continuum + status-seeder Jobs race doesn't OOM-cascade on
# the production-tier 4GB/8GB envelope (validated in 2026-05-10 bounded-
# cycle session, 12 of 12 fresh provisions wedged with the production
# defaults). The wiring lives in locals.effective_cp_size /
# locals.effective_worker_size which the singular-path hcloud_server
# resources read.

run "qa_mode_off_keeps_production_defaults" {
  command = plan

  variables {
    qa_fixtures_enabled = "false"
  }

  assert {
    condition     = hcloud_server.control_plane[0].server_type == "cpx22"
    error_message = "qa_fixtures_enabled='false' must NOT alter the production cpx22 CP default (customer Sovereign path)."
  }

  assert {
    condition     = hcloud_server.worker[0].server_type == "cpx32"
    error_message = "qa_fixtures_enabled='false' must NOT alter the production cpx32 worker default (customer Sovereign path)."
  }
}

run "qa_mode_on_flips_to_bigger_skus" {
  command = plan

  variables {
    qa_fixtures_enabled = "true"
  }

  assert {
    condition     = hcloud_server.control_plane[0].server_type == "cpx32"
    error_message = "qa_fixtures_enabled='true' must auto-flip CP to qa_control_plane_size default 'cpx32' (Fix #157 — eliminates cpx22 CP OOM-cascade root cause)."
  }

  assert {
    condition     = hcloud_server.worker[0].server_type == "cpx42"
    error_message = "qa_fixtures_enabled='true' must auto-flip workers to qa_worker_size default 'cpx42' (Fix #157 — eliminates cpx32 worker OOM-cascade root cause)."
  }
}

run "qa_mode_on_respects_explicit_overrides" {
  command = plan

  variables {
    qa_fixtures_enabled   = "true"
    qa_control_plane_size = "cpx42"
    qa_worker_size        = "ccx33"
  }

  assert {
    condition     = hcloud_server.control_plane[0].server_type == "cpx42"
    error_message = "QA-mode CP SKU must follow operator-supplied qa_control_plane_size verbatim (no hardcoded override)."
  }

  assert {
    condition     = hcloud_server.worker[0].server_type == "ccx33"
    error_message = "QA-mode worker SKU must follow operator-supplied qa_worker_size verbatim (no hardcoded override)."
  }
}
