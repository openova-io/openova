# Multi-region wiring tests for the Catalyst Hetzner Phase-0 module.
#
# Slice G1 (EPIC-0 #1095) wires every entry in var.regions[] end-to-end.
# These tests exercise the wiring shape WITHOUT touching real Hetzner —
# both providers are mocked so the test runs offline in CI on every PR
# touching infra/hetzner/**.
#
# var.regions has the type of contract version 1 (CLD-090): one entry per
# region, index 0 the primary. Three scenarios:
#   1. An empty regions list is refused: the contract needs the primary.
#   2. Single-entry regions list (len(regions) == 1) — secondary
#      resources stay empty (regions[0] is owned by the singular path),
#      preserving the cost-zero shape for solo Sovereigns.
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

# SEC-020: the k3s tokens are random_password resources. Under
# `command = plan` their `result` is unknown, which would leave every
# rendered cloud-init unknown and every assertion on it unevaluated. These
# overrides give the tokens fixed values in the plan.
override_resource {
  target = random_password.k3s_server_token
  values = {
    result = "test-k3s-server-token"
  }
}

override_resource {
  target = random_password.k3s_agent_token
  values = {
    result = "test-k3s-agent-token"
  }
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

  # CLD-090: contract version 1. regions is required and index 0 is the
  # primary region; a run with more regions sets its own list.
  regions = [
    {
      code               = "nbg1"
      cloud_region       = "nbg1"
      role               = "primary"
      control_plane_size = "cpx22"
      worker_size        = "cpx32"
      worker_count       = 2
    }
  ]

  # CLD-090: contract version 1 gives these four no default; catalyst-api
  # always writes them. The values are the defaults the module had before.
  # An empty deployment_id leaves the kubeconfig post-back out of the render;
  # the run post_back_render_fits_the_size_guards_with_room sets a real one.
  deployment_id         = ""
  gitops_repo_url       = "https://github.com/openova-io/openova"
  catalyst_api_url      = "https://console.openova.io/sovereign"
  default_storage_class = "hcloud-volumes"

  k3s_version    = "v1.31.4+k3s1"
  ssh_public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITESTKEYTESTKEYTESTKEYTESTKEYTESTKEY test@local"

  object_storage_region      = "nbg1"
  object_storage_access_key  = "TESTACCESSKEY1234567"
  object_storage_secret_key  = "TESTSECRETKEYTESTSECRETKEYTESTSE"
  object_storage_bucket_name = "catalyst-test-example-com"

  domain_mode = "byo"

  # SOV-131: the variables document (contract C5); HANDOVER_AUTO is "false"
  # so that variables_handover_auto_reaches_cloud_init sees where it goes.
  variables = {
    SOVEREIGN_MODE                     = "sovereign"
    SOVEREIGN_CONSUME                  = "origin"
    HANDOVER_AUTO                      = "false"
    HANDOVER_HOLD_SECONDS              = "600"
    REGISTRY_HOST                      = "registry.parent.example.test"
    REGISTRY_SCHEME                    = "https"
    LOCAL_REGISTRY_HOST                = "registry.sov.example.test"
    ORIGIN_REGISTRY_HOST               = "registry.parent.example.test"
    ORIGIN_REGISTRY_SCHEME             = "https"
    ORIGIN_REGISTRY_CA_B64             = ""
    ORIGIN_PROXY_ENDPOINTS_JSON        = "{}"
    REGISTRY_PULL_SECRET               = "ghcr-pull"
    REGISTRY_PATH_CHARTS               = "openova-io"
    REGISTRY_PATH_RELEASE              = "openova-io/release"
    REGISTRY_PATH_PROVIDER_PACKS       = "openova-io/provider-packs"
    REGISTRY_PATH_XPKG                 = "proxy-xpkg"
    REGISTRY_PATH_TOFU_PROVIDERS       = "tofu-providers"
    REGISTRY_PATH_NODE_ARTIFACTS       = "node-artifacts"
    REGISTRY_PATH_VULN_DB              = "proxy-vulndb/aquasecurity/trivy-db"
    REGISTRY_PROXY_PROJECTS_JSON       = "{\"docker.io\":\"proxy-dockerhub\",\"gcr.io\":\"proxy-gcr\",\"ghcr.io\":\"proxy-ghcr\",\"public.ecr.aws\":\"proxy-ecr\",\"quay.io\":\"proxy-quay\",\"registry-1.docker.io\":\"proxy-dockerhub\",\"registry.k8s.io\":\"proxy-k8s\",\"xpkg.upbound.io\":\"proxy-xpkg\"}"
    RELEASE_PUBLIC_KEY_B64             = ""
    RELEASE_CHANNEL                    = "stable"
    RELEASE_VERSION                    = ""
    RELEASE_AUTO                       = "false"
    RELEASE_SYNC_SCHEDULE              = "0 3 * * *"
    ORIGIN_PARENT_FQDN                 = "parent.example.test"
    ORIGIN_PARENT_API_URL              = "https://console.parent.example.test/sovereign"
    DNS_MODE                           = "delegated"
    DNS_UPSTREAM_API_URL               = ""
    MAIL_MODE                          = "local"
    MAIL_RELAY_HOST                    = ""
    MAIL_RELAY_PORT                    = "587"
    MAIL_FROM                          = ""
    MAIL_RELAY_SECRET                  = ""
    K3S_VERSION                        = "v1.31.8+k3s1"
    K3S_UPGRADE_CONCURRENCY            = "1"
    CLOUD_CREDENTIAL_MODE              = "supplied"
    CLOUD_CREDENTIAL_ROTATION_SCHEDULE = "0 2 1 * *"
    CLOUD_CREDENTIAL_ROTATION_NONCE    = ""
    GITOPS_REPO_URL                    = "http://gitea-http.gitea.svc.cluster.local:3000/openova/provider-pack"
  }
}

# ── Scenario 1: an empty regions list is refused ─────────────────────────
# Contract version 1: regions has at least one entry and regions[0] is the
# primary region. catalyst-api writes one entry for a request that names no
# regions, so the module never sees an empty list.

run "empty_regions_list_is_refused" {
  command = plan

  variables {
    regions = []
  }

  expect_failures = [var.regions]
}

run "first_region_that_is_not_primary_is_refused" {
  command = plan

  variables {
    regions = [
      {
        code               = "nbg1"
        cloud_region       = "nbg1"
        role               = "secondary"
        control_plane_size = "cpx22"
        worker_size        = "cpx32"
        worker_count       = 2
      }
    ]
  }

  expect_failures = [var.regions]
}

# ── Scenario 2: single-entry regions[] ────────────────────────────────────
# regions[0] is owned by the singular path so with len(regions)==1 no
# secondary resources are planned: the multi-region overlay is a no-op.
# This protects single-region Sovereigns from materialising duplicate
# primary-region resources.

run "single_region_entry_does_not_double_provision" {
  command = plan

  variables {
    regions = [
      {
        code               = "nbg1"
        cloud_region       = "nbg1"
        role               = "primary"
        control_plane_size = "cpx22"
        worker_size        = "cpx32"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = length(output.secondary_region_keys) == 0
    error_message = "regions[0] is owned by the singular path; secondary_region_keys must be empty for len(regions)==1."
  }

  assert {
    condition     = length(output.control_plane_ips_by_region) == 0
    error_message = "control_plane_ips_by_region must be empty for len(regions)==1."
  }

  assert {
    condition     = length(output.load_balancer_ips_by_region) == 0
    error_message = "load_balancer_ips_by_region must be empty for len(regions)==1."
  }

  # CLD-090: the contract outputs. One key per region, the region code.
  assert {
    condition     = output.contract_version == "1"
    error_message = "CLD-090: output contract_version must be \"1\"."
  }

  assert {
    condition     = sort(keys(output.control_plane_ips_per_region)) == tolist(["nbg1"])
    error_message = "CLD-090: control_plane_ips_per_region must have exactly the key of regions[0].code."
  }

  # Per-region network refactor: even with NO secondary regions, the
  # primary region's hcloud_network.region["primary"] must exist. The
  # legacy `hcloud_network.main` and `hcloud_network_subnet.main`
  # singletons have been deleted; their job is now done by the
  # for_each map keyed on local.all_region_keys.
  assert {
    condition     = length(hcloud_network.region) == 1
    error_message = "A single region must still produce exactly 1 hcloud_network keyed 'primary' (the legacy hcloud_network.main was retired)."
  }

  assert {
    condition     = contains(keys(hcloud_network.region), "primary")
    error_message = "The primary region key must be 'primary'; for_each over local.all_region_keys."
  }

  assert {
    condition     = hcloud_network_subnet.region["primary"].ip_range == "10.0.1.0/24"
    error_message = "Primary subnet must be 10.0.1.0/24 — uniform layout across regions."
  }

  # NET-012 — only TCP 80, 443, 53, UDP 53 and ICMP are open to any source; no
  # rule opens 6443 without the two lists, and none opens 2379 or 51871.
  assert {
    condition = alltrue([for r in hcloud_firewall.main.rule : contains(["tcp 80", "tcp 443", "tcp 53", "udp 53", "icmp "], "${r.protocol} ${r.port == null ? "" : r.port}")
    if contains(r.source_ips, "0.0.0.0/0") || contains(r.source_ips, "::/0")]) && length([for r in hcloud_firewall.main.rule : r if contains(["6443", "2379", "51871"], r.port == null ? "" : r.port)]) == 0
    error_message = "NET-012: only TCP 80, 443, 53, UDP 53 and ICMP may be open to any source; no rule for 6443 without the lists, none for 2379 or 51871."
  }
}

run "api_port_limited" {
  command = plan
  variables {
    admin_cidrs       = ["198.51.100.0/28"]
    provisioner_cidrs = ["192.0.2.10/32", "198.51.100.0/28"]
  }
  assert {
    condition     = jsonencode([for r in hcloud_firewall.main.rule : [r.protocol, sort(r.source_ips)] if r.port == "6443"]) == jsonencode([["tcp", ["192.0.2.10/32", "198.51.100.0/28"]]])
    error_message = "NET-012: exactly one rule must open TCP 6443, with the sources 192.0.2.10/32 and 198.51.100.0/28 only."
  }
  assert {
    condition     = jsondecode(output.network_registry).controlPlaneAccess == { adminCIDRs = ["198.51.100.0/28"], provisionerCIDRs = ["192.0.2.10/32", "198.51.100.0/28"] }
    error_message = "NET-012: the network registry must carry admin_cidrs and provisioner_cidrs as controlPlaneAccess."
  }
}

run "api_port_lists_refuse_any_source" {
  command = plan
  variables {
    admin_cidrs       = ["0.0.0.0/0"]
    provisioner_cidrs = ["192.0.2.10/24"]
  }
  expect_failures = [var.admin_cidrs, var.provisioner_cidrs]
}

# ── Scenario 3: three-region EPIC-6 shape (mgmt + fsn + hel) ─────────────
# Mirrors docs/EPICS-1-6-unified-design.md §3.8: one mgmt cluster
# (regions[0], drives the singular path) + two data-plane clusters in
# fsn1 + hel1 (regions[1..2], drive the secondary overlay).
#
# The slice G1 success gate: secondary_region_keys carries exactly two
# entries, in stable insertion order, with the deterministic
# "{cloud_region}-{index}" naming.

run "three_region_mgmt_fsn_hel" {
  command = plan

  variables {
    regions = [
      {
        code               = "nbg1"
        cloud_region       = "nbg1"
        role               = "primary"
        control_plane_size = "cpx32"
        worker_size        = "cpx32"
        worker_count       = 1
      },
      {
        code               = "fsn1"
        cloud_region       = "fsn1"
        role               = "secondary"
        control_plane_size = "cpx32"
        worker_size        = "cpx32"
        worker_count       = 2
      },
      {
        code               = "hel1"
        cloud_region       = "hel1"
        role               = "secondary"
        control_plane_size = "cpx32"
        worker_size        = "cpx32"
        worker_count       = 2
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

  # CLD-090: the contract output and the network registry are keyed by
  # region code, in the order of var.regions.
  assert {
    condition     = sort(keys(output.control_plane_ips_per_region)) == tolist(["fsn1", "hel1", "nbg1"])
    error_message = "CLD-090: control_plane_ips_per_region must have one key per region code."
  }

  assert {
    condition     = [for r in jsondecode(output.network_registry).regions : r.code] == ["nbg1", "fsn1", "hel1"]
    error_message = "CLD-090: the network registry must name each region by its code, in the order of var.regions."
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

# ── Scenario 4: two regions in one location ──────────────────────────────
# The same cloud_region may appear more than once when every entry has its
# own region code. The module's internal keys stay distinct (the index
# suffix of "{cloud_region}-{index}"), and the contract output is keyed by
# the codes.

run "same_region_duplicates_produce_distinct_keys" {
  command = plan

  variables {
    regions = [
      {
        code               = "nbg1"
        cloud_region       = "nbg1"
        role               = "primary"
        control_plane_size = "cpx22"
        worker_size        = "cpx32"
        worker_count       = 1
      },
      {
        code               = "fsn1-a"
        cloud_region       = "fsn1"
        role               = "secondary"
        control_plane_size = "cpx32"
        worker_size        = "cpx32"
        worker_count       = 1
      },
      {
        code               = "fsn1-b"
        cloud_region       = "fsn1"
        role               = "secondary"
        control_plane_size = "cpx32"
        worker_size        = "cpx32"
        worker_count       = 1
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

  assert {
    condition     = sort(keys(output.control_plane_ips_per_region)) == tolist(["fsn1-a", "fsn1-b", "nbg1"])
    error_message = "CLD-090: control_plane_ips_per_region must be keyed by the three region codes."
  }
}

# ── Scenario 5: a repeated region code is refused ────────────────────────
# Contract version 1: regions[].code is unique. Two regions in one location
# with the same code would share one key in control_plane_ips_per_region.
# (Before contract version 1 this scenario pinned the filter by
# regions[].provider; the contract's type has no such field, so every
# regions[1+] entry is a region of this module.)

run "repeated_region_code_is_refused" {
  command = plan

  variables {
    regions = [
      {
        code               = "nbg1"
        cloud_region       = "nbg1"
        role               = "primary"
        control_plane_size = "cpx22"
        worker_size        = "cpx32"
        worker_count       = 1
      },
      {
        code               = "fsn1"
        cloud_region       = "fsn1"
        role               = "secondary"
        control_plane_size = "cpx32"
        worker_size        = "cpx32"
        worker_count       = 2
      },
      {
        code               = "fsn1"
        cloud_region       = "fsn1"
        role               = "secondary"
        control_plane_size = "cpx32"
        worker_size        = "cpx32"
        worker_count       = 2
      },
    ]
  }

  expect_failures = [var.regions]
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
# variable that's set to `r.cloud_region` for secondary blocks and to
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
        code               = "hel1"
        cloud_region       = "hel1"
        role               = "primary"
        control_plane_size = "cpx32"
        worker_size        = "cpx32"
        worker_count       = 1
      },
      {
        code               = "nbg1"
        cloud_region       = "nbg1"
        role               = "secondary"
        control_plane_size = "cpx32"
        worker_size        = "cpx32"
        worker_count       = 1
      },
      {
        code               = "sin"
        cloud_region       = "sin"
        role               = "secondary"
        control_plane_size = "cpx32"
        worker_size        = "cpx32"
        worker_count       = 1
      },
    ]
    # Singular-path region must mirror regions[0] (provisioner.go contract).
    region                = "hel1"
    object_storage_region = "hel1"
  }

  # ── Secondary nbg1-1 cloud-init must carry nbg1's own values, NOT hel1's ──
  assert {
    condition     = strcontains(local.secondary_region_flux_bootstrap["nbg1-1"], "SOVEREIGN_REGION_KEY: nbg1-1")
    error_message = "Secondary nbg1-1 cloud-init must render SOVEREIGN_REGION_KEY=nbg1-1 (the each.key from local.secondary_regions); got primary's value instead."
  }

  assert {
    condition     = strcontains(local.secondary_region_flux_bootstrap["nbg1-1"], "HCLOUD_LB_LOCATION: \"nbg1\"")
    error_message = "Secondary nbg1-1 cloud-init must render HCLOUD_LB_LOCATION=\"nbg1\" (r.cloud_region); got primary's hel1 instead — breaks hcloud-ccm clustermesh-apiserver LB allocation (D10/D12)."
  }

  assert {
    condition     = !strcontains(local.secondary_region_flux_bootstrap["nbg1-1"], "HCLOUD_LB_LOCATION: \"hel1\"")
    error_message = "Secondary nbg1-1 cloud-init must NOT carry primary's HCLOUD_LB_LOCATION=\"hel1\"."
  }

  # ── Secondary sin-2 cloud-init must carry sin's own values, NOT hel1's ────
  assert {
    condition     = strcontains(local.secondary_region_flux_bootstrap["sin-2"], "SOVEREIGN_REGION_KEY: sin-2")
    error_message = "Secondary sin-2 cloud-init must render SOVEREIGN_REGION_KEY=sin-2 (each.key)."
  }

  assert {
    condition     = strcontains(local.secondary_region_flux_bootstrap["sin-2"], "HCLOUD_LB_LOCATION: \"sin\"")
    error_message = "Secondary sin-2 cloud-init must render HCLOUD_LB_LOCATION=\"sin\" (r.cloud_region)."
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
    error_message = "Secondary sin-2 k3s node-label must carry openova.io/region=hz-sin-rtz-prod (digits stripped from cloud_region when the region has none preserves the bare label)."
  }

  # ── QA_PRIMARY_REGION substitute on every cluster's bootstrap-kit
  # ── Kustomization must point at the Sovereign-wide primary region
  # ── label, not a hardcoded `hz-fsn-rtz-prod` fallback. The chart's
  # ── default for qaFixtures.primaryRegion is `hz-fsn-rtz-prod`; the
  # ── cloud-init substitute MUST override it with the actual primary's
  # ── canonical label so qa-fixtures pin to the right region.
  assert {
    condition     = strcontains(local.control_plane_flux_bootstrap, "QA_PRIMARY_REGION: \"hz-hel-rtz-prod\"")
    error_message = "Primary cloud-init bootstrap-kit substitute must thread QA_PRIMARY_REGION=hz-hel-rtz-prod (var.region=hel1 → hz-hel-rtz-prod) so the chart's qaFixtures.primaryRegion isn't left at the hardcoded `hz-fsn-rtz-prod` default."
  }

  assert {
    condition     = strcontains(local.secondary_region_flux_bootstrap["nbg1-1"], "QA_PRIMARY_REGION: \"hz-hel-rtz-prod\"")
    error_message = "Secondary nbg1-1 cloud-init bootstrap-kit substitute must thread QA_PRIMARY_REGION=hz-hel-rtz-prod (the SOVEREIGN's primary, NOT this secondary's own region) — qaFixtures.primaryRegion is Sovereign-wide singular."
  }

  # ── CLD-090: SOVEREIGN_REGIONS_JSON keeps the field names the Sovereign
  # ── reads (core/controllers/pkg/regions), not the contract's names.
  assert {
    condition     = strcontains(local.control_plane_flux_bootstrap, "SOVEREIGN_REGIONS_JSON: '${jsonencode([for r in ["hel1", "nbg1", "sin"] : { provider = "hetzner", cloudRegion = r, controlPlaneSize = "cpx32", workerSize = "cpx32", workerCount = 1 }])}'")
    error_message = "CLD-090: the Flux bootstrap document of the primary cloud-init must carry SOVEREIGN_REGIONS_JSON with the fields provider, cloudRegion, controlPlaneSize, workerSize and workerCount."
  }

  # ── Primary's cloud-init still carries the primary's region.
  assert {
    condition     = strcontains(local.control_plane_flux_bootstrap, "SOVEREIGN_REGION_KEY: hel1")
    error_message = "Primary cloud-init must render SOVEREIGN_REGION_KEY=hel1 (var.region) — fix must not regress the primary path."
  }

  assert {
    condition     = strcontains(local.control_plane_flux_bootstrap, "HCLOUD_LB_LOCATION: \"hel1\"")
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

# ── #4057: SOVEREIGN_CNPG_STORAGE_CLASS reflects the operator's
# ── default_storage_class tofu var (no longer a hardcoded literal) ──────────
# The catalyst-api Request.StorageClass → var.default_storage_class → the
# cloud-init Kustomization postBuild.substitute SOVEREIGN_CNPG_STORAGE_CLASS →
# the host-shared CNPG Cluster CRs (slots 10/19/52) + the bp-cnpg/bp-mgmt-
# vcluster default class. local-path is FORBIDDEN; the var validation rejects
# "" and "local-path" so this substitute can never render ephemeral storage.
run "default_storage_class_hcloud_volumes_renders" {
  command = plan

  variables {
    region = "hel1"
    # default_storage_class is the file-level value `hcloud-volumes`: the
    # class catalyst-api writes when the sovereign-admin chose none (the
    # per-provider Hetzner CSI durable class). The module has no default.
  }

  assert {
    condition     = strcontains(local.control_plane_flux_bootstrap, "SOVEREIGN_CNPG_STORAGE_CLASS: \"hcloud-volumes\"")
    error_message = "Primary cloud-init must render SOVEREIGN_CNPG_STORAGE_CLASS=\"hcloud-volumes\" when default_storage_class is \"hcloud-volumes\" (the per-provider Hetzner CSI class catalyst-api writes by default). #4057"
  }

  assert {
    condition     = !strcontains(local.control_plane_flux_bootstrap, "SOVEREIGN_CNPG_STORAGE_CLASS: \"local-path\"")
    error_message = "Primary cloud-init must NEVER render SOVEREIGN_CNPG_STORAGE_CLASS=\"local-path\" — local-path is FORBIDDEN (#3971/#892)."
  }
}

run "default_storage_class_operator_override_renders" {
  command = plan

  variables {
    region                = "hel1"
    default_storage_class = "hcloud-volumes-xfs"
  }

  assert {
    condition     = strcontains(local.control_plane_flux_bootstrap, "SOVEREIGN_CNPG_STORAGE_CLASS: \"hcloud-volumes-xfs\"")
    error_message = "Primary cloud-init must render the operator-chosen default_storage_class verbatim into SOVEREIGN_CNPG_STORAGE_CLASS. #4057 — founder point #1 (storage class is a user input)."
  }

  assert {
    condition     = !strcontains(local.control_plane_flux_bootstrap, "SOVEREIGN_CNPG_STORAGE_CLASS: \"hcloud-volumes\"")
    error_message = "When the operator overrides default_storage_class, the cloud-init must carry ONLY the chosen class, not the default literal."
  }
}

# ── SEC-020: a server token for the control plane, an agent token for workers
# The control plane starts k3s with K3S_TOKEN set to the server token and
# --agent-token set to the agent token. Workers join with the agent token and
# never receive the server token.
run "k3s_server_and_agent_tokens" {
  command = plan

  assert {
    condition     = strcontains(local.control_plane_cloud_init, "K3S_TOKEN=test-k3s-server-token ")
    error_message = "SEC-020: the control-plane cloud-init must start k3s with K3S_TOKEN set to random_password.k3s_server_token."
  }

  assert {
    condition     = strcontains(local.control_plane_cloud_init, " --agent-token=test-k3s-agent-token ")
    error_message = "SEC-020: the control-plane cloud-init must pass --agent-token set to random_password.k3s_agent_token."
  }

  assert {
    condition     = strcontains(local.worker_cloud_init, "K3S_TOKEN=test-k3s-agent-token ")
    error_message = "SEC-020: the worker cloud-init must join with K3S_TOKEN set to random_password.k3s_agent_token."
  }

  assert {
    condition     = !strcontains(local.worker_cloud_init, "test-k3s-server-token")
    error_message = "SEC-020: the worker cloud-init must not carry the k3s server token."
  }
}

# ── SOV-131: HANDOVER_AUTO reaches FIRE_CUTOVER_ON_HANDOVER; a document
# without exactly the keys of contract C5 is refused at plan time.
run "variables_handover_auto_reaches_cloud_init" {
  command = plan

  assert {
    condition     = strcontains(local.control_plane_flux_bootstrap, "FIRE_CUTOVER_ON_HANDOVER: \"false\"")
    error_message = "SOV-131: variables[\"HANDOVER_AUTO\"] must reach the substitute FIRE_CUTOVER_ON_HANDOVER of the control-plane cloud-init."
  }
}

run "variables_refuses_a_document_without_the_contract_keys" {
  command = plan

  variables {
    variables = { HANDOVER_AUTO = "true", NOT_A_CONTRACT_KEY = "x" }
  }

  expect_failures = [var.variables]
}

# ── CLD-185, #7893: the worst-case render fits the size guards.
# The runs above use short values and an empty deployment_id, so their
# renders are smaller than the render of a real Sovereign. This run renders
# the module with the worst-case variables that
# scripts/cloudinit-size-check.py measures (it writes them below with
# --write-fixtures and says what each length stands for): the longest FQDN the bucket-name rule allows, two
# regions, every optional input set, tokens and keys of their real lengths.
# The k3s tokens and the load-balancer addresses below have their real
# lengths too (64 characters; 15, the longest IPv4 form). The plan fails by
# itself when a render is over its guard (32576 bytes for the primary, 32256
# for a secondary region), and the asserts state the same bound. On main
# before #7893 this render was about 34100 and 35300 bytes; with the Flux
# bootstrap document and the Cilium values packed it fits. The fixed Secrets
# and the provider-injected files travel packed (gz+b64); the last assert
# shows that the packed cloud-credentials file is the provider's Secret body
# without its comment lines, as the plain file was.
run "post_back_render_fits_the_size_guards_with_room" {
  command = plan

  # BEGIN worst-case variables, written by scripts/cloudinit-size-check.py --write-fixtures
  variables {
    bcp_topology               = "active-hotstandby"
    catalyst_api_url           = "https://console.vhwtxhpiag5nlbjhyfkfz36kcox31rsv6.omantel.biz/sovereign"
    cluster_mesh_id            = 1
    cluster_mesh_name          = "ae1r3b3eqk10j5wbg8ulhppktid7yj0g9-mesh"
    console_isolation_enabled  = "true"
    control_plane_size         = "ccx63"
    default_storage_class      = "hcloud-volumes"
    deployment_id              = "63ccf96f83c59e51"
    enable_hot_standby         = "true"
    enable_shared_pg           = "true"
    ghcr_pull_token            = "ghcr_filler_JKoBErUo62nLAJK5Keh4Biwuz53hoMGaBPB6HXU3W5gocWjikuaW8mPH1UVL6h8hS4DEzcnPVUnhHo0cS"
    gitops_repo_url            = "http://gitea-http.gitea.svc.cluster.local:3000/openova/provider-pack"
    handover_jwt_public_key    = "{\"alg\":\"RS256\",\"e\":\"AQAB\",\"kty\":\"RSA\",\"n\":\"jFfiWIe242hUG0FlefX2yClTWSyh-4cTacQBjPJuGLoXnxhR4t0_0WZeah0ImeJsw9HMrloVq7ooT76jrcXebjmIEScBf0v7CN4Kj4bkkNUJRpLMDY56nzOPqALld61c8K3IDDdoAUR4tNoYEABKlWT8XD3oQMINzGg-x7kUsudqkheDYqziWbwd9EsWw2evx8l900IvGvXCfDEOAPe-YQBM5ENUdbbJyqKVl9BrQFVBOPO5c7fAI4uCeVNVzvIrl4IQ7i50i1kA5DS5YvCFSaPlXHxwDrZsn9TLuChC90XeLo_jcyfr_08yxGV5nUMcim2uYB8wNRzSA-xUeCZG8x\",\"use\":\"sig\"}"
    harbor_robot_token         = "KCVhNCbzrrWzD3iyA5c4cVWoqF25f5j6"
    hcloud_project_id          = "12345678"
    hcloud_token               = "VrHjdQ6Br6jtsy9QYD96qbwsu7wmCVEdQsgY5UBLaGtdBFGDd5p9kHgzUDzBc4lM"
    kubeconfig_bearer_token    = "a5936414e8f1193d572074048696a49118ca3947da6121e8aec6beae72acd741"
    marketplace_enabled        = "true"
    object_storage_access_key  = "OcvPSam7irx2ENJJlW0xDRQriqh1pSCtk7nvd0MBE8FPej5NhuZ7Gpq3lYP1M6iU"
    object_storage_bucket_name = "catalyst-ae1r3b3eqk10j5wbg8ulhppktid7yj0g9-omani-works-63ccf96f"
    object_storage_region      = "fsn1"
    object_storage_secret_key  = "FRAYmfEVvKNA84Muk8cnJImAExyiVaqOarh17QXKcPiz2TQTLkJyBBQF3SW2vT6mh7NB44EuCZ9iOFkT9Lful9V1t6HqWEY9pDh6BZAlBlKVMrbV08Lxuj6ZdfIzlD1i"
    org_email                  = "bkzitohxvpztjrswdqdfeabizcvnzixuevmihzkweoxgwumfqanrpcrfsbqvadii@puzeqhhzsnzqdxw9e76xanom0auqgruadge9gj3m5swml9pin0ctkj4pyxzwwoj.vskqogyim6cpkz3kygtk4sb9d5n73ierktp1yh3fixs06u61qzo5ga6nyoa47nl.jpvqlp3kcdxlicem3icpgrdvhkq3gbvq0afmz4y7czjaksfvbhgjxvj89o.om"
    org_name                   = "Omani National Sovereign Cloud Services & Data Platforms Company (Closed Joint Stock) & Partners LLC"
    parent_domains_yaml        = "[{\"name\":\"ae1r3b3eqk10j5wbg8ulhppktid7yj0g9.omani.works\",\"role\":\"primary\"},{\"name\":\"omani.homes\",\"role\":\"org-pool\"},{\"name\":\"omani.rest\",\"role\":\"org-pool\"},{\"name\":\"omani.trade\",\"role\":\"org-pool\"}]"
    pdm_basic_auth_pass        = "fOxOud9JoeoSGJjfNZbUSGrONfhEzatlUijrH0u89WedgLRW8pfiDsNrOUvDba2U"
    pdm_basic_auth_user        = "ZMsusu7pFBC5ulGjXvckKA4VmcIdqn6A"
    powerdns_api_key           = "qG5I3mpjZOOQFPsRBhyYUUFybJWNzvTY"
    provider_api_cidrs = [
      "149.104.149.0/24",
      "141.131.193.0/24",
      "117.133.108.0/24",
      "171.108.110.0/24",
    ]
    qa_fixtures_enabled = "true"
    region              = "fsn1"
    regions = [
      {
        "cloud_region"       = "fsn1"
        "code"               = "fsn1"
        "control_plane_size" = "ccx63"
        "role"               = "primary"
        "worker_count"       = 50
        "worker_size"        = "ccx63"
      },
      {
        "cloud_region"       = "nbg1"
        "code"               = "nbg1"
        "control_plane_size" = "ccx63"
        "role"               = "secondary"
        "worker_count"       = 50
        "worker_size"        = "ccx63"
      },
    ]
    sovereign_deployment_id = "63ccf96f83c59e51"
    sovereign_fqdn          = "ae1r3b3eqk10j5wbg8ulhppktid7yj0g9.omani.works"
    sovereign_subdomain     = "ae1r3b3eqk10j5wbg8ulhppktid7yj0g9"
    ssh_public_key          = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIfxPSrXwdxgFhS9hMbVHdAee21BJZFReoM9QleQIH1OM sovereign-admin"
    variables = {
      "CLOUD_CREDENTIAL_MODE"              = "supplied"
      "CLOUD_CREDENTIAL_ROTATION_NONCE"    = ""
      "CLOUD_CREDENTIAL_ROTATION_SCHEDULE" = "0 2 1 * *"
      "DNS_MODE"                           = "upstream-managed"
      "DNS_UPSTREAM_API_URL"               = ""
      "GITOPS_REPO_URL"                    = "http://gitea-http.gitea.svc.cluster.local:3000/openova/provider-pack"
      "HANDOVER_AUTO"                      = "true"
      "HANDOVER_HOLD_SECONDS"              = "600"
      "K3S_UPGRADE_CONCURRENCY"            = "1"
      "K3S_VERSION"                        = "v1.31.8+k3s1"
      "LOCAL_REGISTRY_HOST"                = "registry.ae1r3b3eqk10j5wbg8ulhppktid7yj0g9.omani.works"
      "MAIL_FROM"                          = ""
      "MAIL_MODE"                          = "local"
      "MAIL_RELAY_HOST"                    = ""
      "MAIL_RELAY_PORT"                    = "587"
      "MAIL_RELAY_SECRET"                  = ""
      "ORIGIN_PARENT_API_URL"              = "https://console.vhwtxhpiag5nlbjhyfkfz36kcox31rsv6.omantel.biz/sovereign"
      "ORIGIN_PARENT_FQDN"                 = "vhwtxhpiag5nlbjhyfkfz36kcox31rsv6.omantel.biz"
      "ORIGIN_PROXY_ENDPOINTS_JSON"        = "{}"
      "ORIGIN_REGISTRY_CA_B64"             = ""
      "ORIGIN_REGISTRY_HOST"               = "registry.vhwtxhpiag5nlbjhyfkfz36kcox31rsv6.omantel.biz"
      "ORIGIN_REGISTRY_SCHEME"             = "https"
      "REGISTRY_HOST"                      = "registry.vhwtxhpiag5nlbjhyfkfz36kcox31rsv6.omantel.biz"
      "REGISTRY_PATH_CHARTS"               = "openova-io"
      "REGISTRY_PATH_NODE_ARTIFACTS"       = "node-artifacts"
      "REGISTRY_PATH_PROVIDER_PACKS"       = "openova-io/provider-packs"
      "REGISTRY_PATH_RELEASE"              = "openova-io/release"
      "REGISTRY_PATH_TOFU_PROVIDERS"       = "tofu-providers"
      "REGISTRY_PATH_VULN_DB"              = "proxy-vulndb/proxy-vulndb/aquasecurity/trivy-db"
      "REGISTRY_PATH_XPKG"                 = "proxy-xpkg"
      "REGISTRY_PROXY_PROJECTS_JSON"       = "{\"docker.io\":\"proxy-dockerhub\",\"gcr.io\":\"proxy-gcr\",\"ghcr.io\":\"proxy-ghcr\",\"public.ecr.aws\":\"proxy-ecr\",\"quay.io\":\"proxy-quay\",\"registry-1.docker.io\":\"proxy-dockerhub\",\"registry.k8s.io\":\"proxy-k8s\",\"xpkg.upbound.io\":\"proxy-xpkg\"}"
      "REGISTRY_PULL_SECRET"               = "ghcr-pull"
      "REGISTRY_SCHEME"                    = "https"
      "RELEASE_AUTO"                       = "false"
      "RELEASE_CHANNEL"                    = "candidate"
      "RELEASE_PUBLIC_KEY_B64"             = "LS0tLS1CRUdJTiBQVUJMSUMgS0VZLS0tLS0KNVdqS2Z0dVR5eGRwUnZ3Z2xPVFc2N0VhTTF1ZGZXbE9kUHg4ZTk2WXREeFR5NnpRT2w2RVgwWXlGUmQydFJIZgpPTFQ2dDVwWm1CSDFDaXJGMmtldklJNUJMU2YwaG9nRm03aU0wNEFqQUhERXdmU2FCcHR0Q2gwaFl6WDA9PQotLS0tLUVORCBQVUJMSUMgS0VZLS0tLS0K"
      "RELEASE_SYNC_SCHEDULE"              = "0 3 * * *"
      "RELEASE_VERSION"                    = ""
      "SOVEREIGN_CONSUME"                  = "origin"
      "SOVEREIGN_MODE"                     = "sovereign"
    }
    wildcard_cert_use_staging = "true"
    worker_count              = 50
    worker_size               = "ccx63"
  }
  # END worst-case variables

  override_resource {
    target = random_password.k3s_server_token
    values = {
      result = "FiLLeRk3sServer0FiLLeRk3sServer0FiLLeRk3sServer0FiLLeRk3sServer0"
    }
  }

  override_resource {
    target = random_password.k3s_agent_token
    values = {
      result = "FiLLeRk3sAgent00FiLLeRk3sAgent00FiLLeRk3sAgent00FiLLeRk3sAgent00"
    }
  }

  override_resource {
    target = hcloud_load_balancer.main
    values = {
      id   = "1"
      ipv4 = "198.151.113.210"
    }
  }

  override_resource {
    target = hcloud_load_balancer.console
    values = {
      id   = "1"
      ipv4 = "198.151.113.211"
    }
  }

  override_resource {
    target = hcloud_load_balancer.secondary
    values = {
      id   = "1"
      ipv4 = "198.151.113.212"
    }
  }

  assert {
    condition     = strcontains(local.control_plane_cloud_init, "--data-binary @/var/lib/catalyst/parent-bootstrap.kubeconfig")
    error_message = "CLD-185: the primary render must carry the post-back, which reads /var/lib/catalyst/parent-bootstrap.kubeconfig."
  }

  assert {
    condition     = strcontains(local.secondary_region_cloud_init["nbg1-1"], "--data-binary @/var/lib/catalyst/parent-bootstrap.kubeconfig")
    error_message = "CLD-185: the secondary render must carry the post-back, which reads /var/lib/catalyst/parent-bootstrap.kubeconfig."
  }

  assert {
    condition     = length(local.control_plane_cloud_init) <= 32576
    error_message = "CLD-185, #7893: the worst-case primary render is ${nonsensitive(length(local.control_plane_cloud_init))} bytes; it must stay at most 32576, its guard."
  }

  assert {
    condition     = length(local.secondary_region_cloud_init["nbg1-1"]) <= 32256
    error_message = "CLD-185, #7893: the worst-case secondary render is ${nonsensitive(length(local.secondary_region_cloud_init["nbg1-1"]))} bytes; it must stay at most 32256, its guard."
  }

  assert {
    condition     = strcontains(local.control_plane_cloud_init, "    encoding: gz+b64\n    content: ${base64gzip(format("%s\n", chomp(replace(local.cloud_credentials_secret_yaml_hetzner_primary, "/(?m)^[ ]*#( |$).*\n/", ""))))}\n")
    error_message = "CLD-185: the packed cloud-credentials file of the primary render must be the provider's Secret body without its comment lines."
  }
}

# ── SEC-150: the module calls the seal hook with its own region keys ──────
# Contract C8 (docs/contracts/c8-seal-provider.md): one seal key per region,
# for the same region keys as the k3s tokens. The hook itself is tested in
# tests/seal.tftest.hcl.
run "seal_hook_is_called_with_the_region_keys" {
  command = plan

  assert {
    condition     = output.seal_kind == "static-node-file"
    error_message = "SEC-150: output seal_kind must be the kind of this pack's seal hook, static-node-file."
  }

  assert {
    condition     = sort(keys(module.seal.stanza_by_region)) == sort(keys(random_password.k3s_server_token))
    error_message = "SEC-150: the seal hook must be called with the region keys of the k3s tokens, so that every region gets one seal key."
  }

  assert {
    condition     = module.seal.volume_device == ""
    error_message = "SEC-150: this pack has no separate seal volume, so the hook's volume_device must be empty."
  }
}
