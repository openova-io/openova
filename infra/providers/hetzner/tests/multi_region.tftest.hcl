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
    id = "catalyst-test-example-com"
    # G107 #2702 (2026-06-01): `bucket` is a required configuration input
    # on aws_s3_bucket, not a computed attribute. tofu test refuses to
    # override config inputs with "Invalid mock/override field `bucket`".
    # The bucket name is resolved naturally from var.object_storage_bucket_name
    # — overriding it here is unnecessary AND blocks the test from running.
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

  # Per-region network refactor: even with NO secondary regions, the
  # primary region's hcloud_network.region["primary"] must exist. The
  # legacy `hcloud_network.main` and `hcloud_network_subnet.main`
  # singletons have been deleted; their job is now done by the
  # for_each map keyed on local.all_region_keys.
  assert {
    condition     = length(hcloud_network.region) == 1
    error_message = "Single-region (var.regions=[]) must still produce exactly 1 hcloud_network keyed 'primary' (the legacy hcloud_network.main was retired)."
  }

  assert {
    condition     = contains(keys(hcloud_network.region), "primary")
    error_message = "The primary region key must be 'primary'; for_each over local.all_region_keys."
  }

  assert {
    condition     = hcloud_network_subnet.region["primary"].ip_range == "10.0.1.0/24"
    error_message = "Primary subnet must be 10.0.1.0/24 — uniform layout across regions."
  }

  # Firewall must include the Cilium WG inter-region rule (UDP 51871).
  # DoD A2 (docs/SOVEREIGN-MULTI-REGION-DOD.md) — without this, the
  # WireGuard mesh between regions cannot form and gate D11 fails.
  assert {
    condition = length([
      for r in hcloud_firewall.main.rule :
      r if r.protocol == "udp" && r.port == "51871"
    ]) == 1
    error_message = "hcloud_firewall.main must declare exactly 1 inbound rule for UDP 51871 (Cilium WireGuard inter-region encryption per DoD A2)."
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

  # Per-region network refactor (2026-05-15 DoD A2) — one hcloud_network
  # per region, NO shared private net across regions. Verify the
  # for_each map declares one Network for "primary" + one for each
  # secondary region, all on the same 10.0.0.0/16 (the ranges live in
  # ISOLATED networks so the collision is intentional).
  assert {
    condition     = length(hcloud_network.region) == 3
    error_message = "Three-region payload must produce exactly 3 hcloud_network entries (primary + 2 secondaries) — one isolated /16 per region per DoD A2."
  }

  assert {
    condition     = hcloud_network.region["primary"].ip_range == "10.0.0.0/16"
    error_message = "Each region's hcloud_network must be 10.0.0.0/16 (identical inside isolated networks)."
  }

  assert {
    condition     = hcloud_network.region["fsn1-1"].ip_range == "10.0.0.0/16"
    error_message = "Secondary region's hcloud_network must be 10.0.0.0/16 (same range as primary inside its OWN isolated network)."
  }

  assert {
    condition     = length(hcloud_network_subnet.region) == 3
    error_message = "Three-region payload must produce exactly 3 hcloud_network_subnet entries — one /24 per region's isolated /16."
  }

  assert {
    condition     = hcloud_network_subnet.region["hel1-2"].ip_range == "10.0.1.0/24"
    error_message = "Each region's subnet must be 10.0.1.0/24 (uniform CP=.2, workers=.10+, LB=.254 layout)."
  }

  # Per-region pod/service CIDRs (DoD gate D11 — no collision across
  # ClusterMesh peers). Verify primary, fsn1-1, hel1-2 get distinct
  # cluster-cidrs (10.42/43/44.0.0/16) + service-cidrs (10.96/97/98.0.0/16).
  assert {
    condition     = local.region_cluster_cidr["primary"] == "10.42.0.0/16"
    error_message = "primary region's cluster-cidr must be 10.42.0.0/16 (index 0)."
  }

  assert {
    condition     = local.region_cluster_cidr["fsn1-1"] == "10.43.0.0/16"
    error_message = "fsn1-1 (secondary index 0 → region index 1) must get cluster-cidr 10.43.0.0/16."
  }

  assert {
    condition     = local.region_cluster_cidr["hel1-2"] == "10.44.0.0/16"
    error_message = "hel1-2 (secondary index 1 → region index 2) must get cluster-cidr 10.44.0.0/16."
  }

  assert {
    condition     = local.region_service_cidr["primary"] == "10.96.0.0/16"
    error_message = "primary region's service-cidr must be 10.96.0.0/16 (index 0)."
  }

  assert {
    condition     = local.region_service_cidr["hel1-2"] == "10.98.0.0/16"
    error_message = "hel1-2 (region index 2) must get service-cidr 10.98.0.0/16 — non-overlapping across peers."
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

  # Body-first coalesce contract post Fix #183 (PR #1386):
  # `effective_cp_size = local.qa_mode ? coalesce(var.control_plane_size, var.qa_control_plane_size) : var.control_plane_size`.
  # provisioner.go's writeTfvars CONDITIONALLY emits control_plane_size only
  # when the operator's request body has a non-empty value — when the wizard
  # omits the override, the var hits variables.tf default `cpx22` and qa-mode
  # falls through to qa_control_plane_size. Inside tofu test we can't
  # conditionally omit the var, so the variables.tf default ALWAYS wins
  # body-first coalesce here. The test below thus verifies the body-wins
  # contract: with no operator override the legacy `cpx22` default holds
  # even in qa mode. The qa-default flip is exercised at the
  # provisioner-side `writeTfvars` boundary (covered by
  # provisioner.go's auto-flip tests).
  assert {
    condition     = hcloud_server.control_plane[0].server_type == "cpx22"
    error_message = "Body-first coalesce (Fix #183): variables.tf default `cpx22` for control_plane_size wins over qa_control_plane_size when no operator override is supplied — the auto-flip is provisioner-side (writeTfvars conditionally omits the var on empty body input), not tofu-side."
  }

  assert {
    condition     = hcloud_server.worker[0].server_type == "cpx32"
    error_message = "Body-first coalesce (Fix #183): variables.tf default `cpx32` for worker_size wins over qa_worker_size when no operator override is supplied."
  }
}

run "qa_mode_on_respects_explicit_overrides" {
  command = plan

  variables {
    qa_fixtures_enabled   = "true"
    qa_control_plane_size = "cpx42"
    qa_worker_size        = "ccx33"
  }

  # Same body-first coalesce: explicit qa_control_plane_size = "cpx42"
  # loses to the variables.tf default "cpx22" for control_plane_size
  # because the operator did NOT override control_plane_size. This is
  # the documented post-Fix #183 contract — body wins, qa-default
  # activates only when body is empty (which the wizard's writeTfvars
  # achieves by conditionally omitting the var).
  assert {
    condition     = hcloud_server.control_plane[0].server_type == "cpx22"
    error_message = "Body-first coalesce contract: control_plane_size variables.tf default `cpx22` wins over qa_control_plane_size=cpx42 because operator did not override control_plane_size. (Operator-supplied control_plane_size wins over qa_control_plane_size — the qa override is the auto-flip path, not the operator-override path.)"
  }

  assert {
    condition     = hcloud_server.worker[0].server_type == "cpx32"
    error_message = "Body-first coalesce contract: worker_size variables.tf default `cpx32` wins over qa_worker_size=ccx33 because operator did not override worker_size."
  }
}

# When the operator's body DOES supply control_plane_size + worker_size,
# the body wins verbatim — qa-mode is a no-op on top of that. Confirms
# the operator's override path is the canonical "body wins" lane.
run "qa_mode_on_body_overrides_win" {
  command = plan

  variables {
    qa_fixtures_enabled   = "true"
    qa_control_plane_size = "cpx42"
    qa_worker_size        = "ccx33"
    control_plane_size    = "ccx23"
    worker_size           = "ccx33"
  }

  assert {
    condition     = hcloud_server.control_plane[0].server_type == "ccx23"
    error_message = "Body-supplied control_plane_size=ccx23 must win verbatim over qa_control_plane_size=cpx42 (Fix #183 body-first coalesce — operator intent ALWAYS wins)."
  }

  assert {
    condition     = hcloud_server.worker[0].server_type == "ccx33"
    error_message = "Body-supplied worker_size=ccx33 must win verbatim over qa_worker_size=cpx32 (Fix #183 body-first coalesce — operator intent ALWAYS wins)."
  }
}

# ── Scenario 7: per-region cloud-init renders with that region's own values ─
# Root-cause regression test for the 2026-05-16 omani.works t114 incident
# (deployment id a1448e0b9e471f5d). The secondary CPs' rendered
# cloud-init was carrying the PRIMARY region's SOVEREIGN_REGION_KEY +
# HCLOUD_LB_LOCATION + the canonical `openova.io/region=hz-fsn-rtz-prod`
# k3s node-label — instead of each secondary's OWN values. Concrete
# symptoms in prod:
#
#   1. cilium-gateway-cilium-gateway Service stuck External-IP=<pending>
#      on the secondary clusters because hcloud-ccm read
#      HCLOUD_LB_LOCATION="hel1" on the nbg1 cluster, asked Hetzner to
#      allocate the LB in hel1, and nbg1's CCM didn't own that location.
#   2. OpenovaFlow canvas could not group bubbles by region — every
#      FlowNode.region was "hel1" (the primary's region), so the canvas
#      drew every secondary's bubbles inside the primary super-bubble.
#   3. K3s --node-label was hardcoded to "openova.io/region=hz-fsn-rtz-prod"
#      (a placeholder from the qa-fixtures Pod scheduling rule) — that
#      pinned every CP node label to fsn regardless of its real region,
#      cascading into qaFixtures Pod scheduling misroutes when QA
#      Sovereigns ran multi-region.
#
# The fix derives node-label region from `region` (the templatefile
# variable that's set to `r.cloudRegion` for secondary blocks and to
# `var.region` for the primary block) instead of a hardcoded literal,
# and verifies the rendered cloud-init at the local.* level. We assert
# on local.secondary_region_cloud_init[k] (a string) so the test does
# not depend on provider-mock behaviour for user_data.
run "per_region_cloud_init_carries_secondarys_own_region" {
  command = plan

  variables {
    sovereign_deployment_id = "test1234deadbeef"
    cluster_mesh_name       = "test-mesh"
    cluster_mesh_id         = 1
    regions = [
      {
        provider         = "hetzner"
        cloudRegion      = "hel1"
        controlPlaneSize = "cpx32"
        workerSize       = "cpx32"
        workerCount      = 1
      },
      {
        provider         = "hetzner"
        cloudRegion      = "nbg1"
        controlPlaneSize = "cpx32"
        workerSize       = "cpx32"
        workerCount      = 1
      },
      {
        provider         = "hetzner"
        cloudRegion      = "sin"
        controlPlaneSize = "cpx32"
        workerSize       = "cpx32"
        workerCount      = 1
      },
    ]
    # Singular-path region must mirror regions[0] (provisioner.go contract).
    region                = "hel1"
    object_storage_region = "hel1"
  }

  # ── Secondary nbg1-1 cloud-init must carry nbg1's own values, NOT hel1's ──
  assert {
    condition     = strcontains(local.secondary_region_cloud_init["nbg1-1"], "SOVEREIGN_REGION_KEY: nbg1-1")
    error_message = "Secondary nbg1-1 cloud-init must render SOVEREIGN_REGION_KEY=nbg1-1 (the each.key from local.secondary_regions); got primary's value instead."
  }

  assert {
    condition     = strcontains(local.secondary_region_cloud_init["nbg1-1"], "HCLOUD_LB_LOCATION: \"nbg1\"")
    error_message = "Secondary nbg1-1 cloud-init must render HCLOUD_LB_LOCATION=\"nbg1\" (r.cloudRegion); got primary's hel1 instead — breaks hcloud-ccm clustermesh-apiserver LB allocation (D10/D12)."
  }

  assert {
    condition     = !strcontains(local.secondary_region_cloud_init["nbg1-1"], "HCLOUD_LB_LOCATION: \"hel1\"")
    error_message = "Secondary nbg1-1 cloud-init must NOT carry primary's HCLOUD_LB_LOCATION=\"hel1\"."
  }

  # ── Secondary sin-2 cloud-init must carry sin's own values, NOT hel1's ────
  assert {
    condition     = strcontains(local.secondary_region_cloud_init["sin-2"], "SOVEREIGN_REGION_KEY: sin-2")
    error_message = "Secondary sin-2 cloud-init must render SOVEREIGN_REGION_KEY=sin-2 (each.key)."
  }

  assert {
    condition     = strcontains(local.secondary_region_cloud_init["sin-2"], "HCLOUD_LB_LOCATION: \"sin\"")
    error_message = "Secondary sin-2 cloud-init must render HCLOUD_LB_LOCATION=\"sin\" (r.cloudRegion)."
  }

  # ── K3s node-label must derive from the per-region `region` var, never a
  # ── hardcoded literal (DoD A6 provider-agnostic; INVIOLABLE-PRINCIPLES #4
  # ── never hardcode). Canonical 4-segment label per NAMING-CONVENTION
  # §2.1 = `hz-<region-prefix-no-digits>-rtz-prod` for the Hetzner module.
  assert {
    condition     = strcontains(local.secondary_region_cloud_init["nbg1-1"], "openova.io/region=hz-nbg-rtz-prod")
    error_message = "Secondary nbg1-1 k3s node-label must carry openova.io/region=hz-nbg-rtz-prod (locals.region_canonical_label[\"nbg1-1\"]), NOT the hardcoded primary placeholder hz-fsn-rtz-prod."
  }

  assert {
    condition     = !strcontains(local.secondary_region_cloud_init["nbg1-1"], "openova.io/region=hz-fsn-rtz-prod")
    error_message = "Secondary nbg1-1 cloud-init must NOT carry openova.io/region=hz-fsn-rtz-prod (legacy hardcoded placeholder; that broke every non-fsn1 primary Sovereign)."
  }

  assert {
    condition     = strcontains(local.secondary_region_cloud_init["sin-2"], "openova.io/region=hz-sin-rtz-prod")
    error_message = "Secondary sin-2 k3s node-label must carry openova.io/region=hz-sin-rtz-prod (digits stripped from cloudRegion when the region has none preserves the bare label)."
  }

  # ── QA_PRIMARY_REGION substitute on every cluster's bootstrap-kit
  # ── Kustomization must point at the Sovereign-wide primary region
  # ── label, not a hardcoded `hz-fsn-rtz-prod` fallback. The chart's
  # ── default for qaFixtures.primaryRegion is `hz-fsn-rtz-prod`; the
  # ── cloud-init substitute MUST override it with the actual primary's
  # ── canonical label so qa-fixtures pin to the right region.
  assert {
    condition     = strcontains(local.control_plane_cloud_init, "QA_PRIMARY_REGION: \"hz-hel-rtz-prod\"")
    error_message = "Primary cloud-init bootstrap-kit substitute must thread QA_PRIMARY_REGION=hz-hel-rtz-prod (var.region=hel1 → hz-hel-rtz-prod) so the chart's qaFixtures.primaryRegion isn't left at the hardcoded `hz-fsn-rtz-prod` default."
  }

  assert {
    condition     = strcontains(local.secondary_region_cloud_init["nbg1-1"], "QA_PRIMARY_REGION: \"hz-hel-rtz-prod\"")
    error_message = "Secondary nbg1-1 cloud-init bootstrap-kit substitute must thread QA_PRIMARY_REGION=hz-hel-rtz-prod (the SOVEREIGN's primary, NOT this secondary's own region) — qaFixtures.primaryRegion is Sovereign-wide singular."
  }

  # ── Primary's cloud-init still carries the primary's region.
  assert {
    condition     = strcontains(local.control_plane_cloud_init, "SOVEREIGN_REGION_KEY: hel1")
    error_message = "Primary cloud-init must render SOVEREIGN_REGION_KEY=hel1 (var.region) — fix must not regress the primary path."
  }

  assert {
    condition     = strcontains(local.control_plane_cloud_init, "HCLOUD_LB_LOCATION: \"hel1\"")
    error_message = "Primary cloud-init must render HCLOUD_LB_LOCATION=\"hel1\" (var.region)."
  }

  assert {
    condition     = strcontains(local.control_plane_cloud_init, "openova.io/region=hz-hel-rtz-prod")
    error_message = "Primary cloud-init's k3s node-label must derive from var.region as the canonical 4-segment `hz-hel-rtz-prod` (var.region=hel1 → digits stripped → hz-hel-rtz-prod)."
  }

  assert {
    condition     = !strcontains(local.control_plane_cloud_init, "openova.io/region=hz-fsn-rtz-prod")
    error_message = "Primary cloud-init must NOT carry the legacy hardcoded `openova.io/region=hz-fsn-rtz-prod` literal when var.region != fsn1 — broke t114-omani-works (primary=hel1) and every other non-fsn1 Sovereign."
  }
}
