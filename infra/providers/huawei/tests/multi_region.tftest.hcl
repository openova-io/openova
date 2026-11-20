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

# SEC-152: the control-plane cloud-init carries the seal key of the seal
# hook (module.seal, contract C8). Its random_bytes are unknown under
# `command = plan` too; this override fixes the key to 32 known bytes, so the
# key id of every region is 493ee6f33a535b93 (the first 16 hex characters of
# the SHA-256 of the base64 text).
override_resource {
  target = module.seal.random_bytes.seal
  values = {
    base64 = "dGVzdC1zZWFsLWtleS0wMTIzNDU2Nzg5YWJjZGVmZ2g="
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

  # CLD-090: contract version 1 gives these three no default; catalyst-api
  # always writes them. The values are the defaults the module had before.
  gitops_repo_url       = "https://github.com/openova-io/openova"
  catalyst_api_url      = "https://console.openova.io/sovereign"
  default_storage_class = "evs-ssd"

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
    ORIGIN_REGISTRY_HOST               = "ghcr.io"
    ORIGIN_REGISTRY_SCHEME             = "https"
    ORIGIN_REGISTRY_CA_B64             = ""
    ORIGIN_PROXY_ENDPOINTS_JSON        = "{\"docker.io\":\"http://registry.parent.example.test:5000\",\"gcr.io\":\"http://registry.parent.example.test:5000\",\"ghcr.io\":\"http://registry.parent.example.test:5000\",\"harbor.openova.io\":\"http://registry.parent.example.test:5000\",\"quay.io\":\"http://registry.parent.example.test:5000\",\"registry-1.docker.io\":\"http://registry.parent.example.test:5000\",\"registry.k8s.io\":\"http://registry.parent.example.test:5000\",\"xpkg.upbound.io\":\"https://xpkg.example.test\"}"
    REGISTRY_PULL_SECRET               = "ghcr-pull"
    REGISTRY_PATH_CHARTS               = "openova-io"
    REGISTRY_PATH_RELEASE              = "openova-io/release"
    REGISTRY_PATH_PROVIDER_PACKS       = "openova-io/provider-packs"
    REGISTRY_PATH_XPKG                 = "proxy-xpkg"
    REGISTRY_PATH_TOFU_PROVIDERS       = "tofu-providers"
    REGISTRY_PATH_NODE_ARTIFACTS       = "node-artifacts"
    REGISTRY_PATH_VULN_DB              = "proxy-vulndb/aquasecurity/trivy-db"
    REGISTRY_PROXY_PROJECTS_JSON       = "{\"docker.io\":\"proxy-dockerhub\",\"gcr.io\":\"proxy-gcr\",\"ghcr.io\":\"proxy-ghcr\",\"harbor.openova.io\":\"\",\"quay.io\":\"proxy-quay\",\"registry-1.docker.io\":\"proxy-dockerhub\",\"registry.k8s.io\":\"proxy-k8s\",\"xpkg.upbound.io\":\"\"}"
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
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      },
      {
        code               = "me-east-215-b"
        cloud_region       = "me-east-215"
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
        cloud_region       = "me-east-215"
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
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      },
      {
        code               = "me-east-215-b"
        cloud_region       = "me-east-215"
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
      local.cp_flux_bootstrap_by_region["me-east-215-a"],
      "SOVEREIGN_REGIONS_JSON:"
    )
    error_message = "Primary-CP cloud-init must emit the SOVEREIGN_REGIONS_JSON substitute key (#3110)."
  }
  # The empty-array regression must NOT appear (single-quoted JSON-flow).
  assert {
    condition = !strcontains(
      local.cp_flux_bootstrap_by_region["me-east-215-a"],
      "SOVEREIGN_REGIONS_JSON: '[]'"
    )
    error_message = "SOVEREIGN_REGIONS_JSON must be populated, not the empty-array `[]` regression (#3110/#3106)."
  }
  # Both regions appear in the JSON, in the canonical 4-segment label form
  # (cloudRegion=hw-<code>-rtz-prod), matching the discrete primary/replica
  # substitutes — NOT the bare HCS `code`.
  assert {
    condition = strcontains(
      local.cp_flux_bootstrap_by_region["me-east-215-a"],
      "hw-me-east-215-a-rtz-prod"
    )
    error_message = "regionsJson must carry the primary region's canonical label hw-me-east-215-a-rtz-prod (#3110)."
  }
  assert {
    condition = strcontains(
      local.cp_flux_bootstrap_by_region["me-east-215-a"],
      "hw-me-east-215-b-rtz-prod"
    )
    error_message = "regionsJson must carry the replica region's canonical label hw-me-east-215-b-rtz-prod (#3110)."
  }
  # The Go RegionSpec field name `cloudRegion` must be present — proves the
  # HCS-native `code`/`control_plane_size` keys were re-mapped so
  # catalyst-api's RegionSpec json.Unmarshal can read it.
  assert {
    condition = strcontains(
      local.cp_flux_bootstrap_by_region["me-east-215-a"],
      "\"cloudRegion\""
    )
    error_message = "regionsJson must use the Go RegionSpec field name cloudRegion (re-keyed from HCS `code`) so catalyst-api can unmarshal it (#3110)."
  }
  # Per-region SKU detail (worker_count) is threaded through, so the full
  # RegionSpec[] (not just region names) reaches the chroot.
  assert {
    condition = strcontains(
      local.cp_flux_bootstrap_by_region["me-east-215-a"],
      "\"workerCount\":3"
    )
    error_message = "regionsJson must carry per-region SKU detail (replica workerCount=3) — full RegionSpec[], not just region names (#3110)."
  }
  # Companion key — configuredRegions list also populated with canonical
  # labels for the Dashboard SovereignCard + ClusterMesh chips.
  assert {
    condition = strcontains(
      local.cp_flux_bootstrap_by_region["me-east-215-a"],
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
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition = !strcontains(
      local.cp_flux_bootstrap_by_region["me-east-215-a"],
      "SOVEREIGN_REGIONS_JSON: '[]'"
    )
    error_message = "Single-region HCS prov must still emit a populated regionsJson, not `[]` (#3110)."
  }
  assert {
    condition = strcontains(
      local.cp_flux_bootstrap_by_region["me-east-215-a"],
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
        cloud_region       = "me-east-215"
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
run "default_storage_class_evs_ssd_renders" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
    # default_storage_class is the file-level value `evs-ssd`: the class
    # catalyst-api writes when the sovereign-admin chose none (the
    # per-provider Huawei EVS CSI durable class). The module has no default.
  }

  assert {
    condition = strcontains(
      local.cp_flux_bootstrap_by_region["me-east-215-a"],
      "SOVEREIGN_CNPG_STORAGE_CLASS: \"evs-ssd\""
    )
    error_message = "Primary-CP cloud-init must render SOVEREIGN_CNPG_STORAGE_CLASS=\"evs-ssd\" when default_storage_class is \"evs-ssd\" (the per-provider Huawei EVS CSI class catalyst-api writes by default). #4057"
  }

  assert {
    condition = !strcontains(
      local.cp_flux_bootstrap_by_region["me-east-215-a"],
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
        cloud_region       = "me-east-215"
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
      local.cp_flux_bootstrap_by_region["me-east-215-a"],
      "SOVEREIGN_CNPG_STORAGE_CLASS: \"evs-sas\""
    )
    error_message = "Primary-CP cloud-init must render the operator-chosen default_storage_class verbatim into SOVEREIGN_CNPG_STORAGE_CLASS. #4057"
  }
}

# ── SEC-020: a server token for the control plane, an agent token for workers
# The control plane starts k3s with K3S_TOKEN set to the server token and
# --agent-token set to the agent token. Workers join with the agent token and
# never receive the server token.
run "k3s_server_and_agent_tokens" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = strcontains(local.cp_cloud_init_by_region["me-east-215-a"], "K3S_TOKEN=test-k3s-server-token ")
    error_message = "SEC-020: the control-plane cloud-init must start k3s with K3S_TOKEN set to random_password.k3s_server_token."
  }

  assert {
    condition     = strcontains(local.cp_cloud_init_by_region["me-east-215-a"], " --agent-token=test-k3s-agent-token ")
    error_message = "SEC-020: the control-plane cloud-init must pass --agent-token set to random_password.k3s_agent_token."
  }

  assert {
    condition     = strcontains(local.worker_cloud_init_by_region["me-east-215-a"], "K3S_TOKEN=test-k3s-agent-token ")
    error_message = "SEC-020: the worker cloud-init must join with K3S_TOKEN set to random_password.k3s_agent_token."
  }

  assert {
    condition     = !strcontains(local.worker_cloud_init_by_region["me-east-215-a"], "test-k3s-server-token")
    error_message = "SEC-020: the worker cloud-init must not carry the k3s server token."
  }
}

# ── AIP-201: the Pod Certificate gates follow the k3s line ───────────────
# From k3s 1.35 on, the control plane starts k3s with the feature gates
# ClusterTrustBundle, ClusterTrustBundleProjection and PodCertificateRequest on
# the API server, controller manager, scheduler and kubelet, and with
# certificates.k8s.io/v1beta1 served; every worker's kubelet gets the gates.
# Below 1.35 a server with these gates does not start, so nothing is added.
run "pod_certificate_gates_absent_below_k3s_1_35" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = !strcontains(local.cp_cloud_init_by_region["me-east-215-a"], "feature-gates")
    error_message = "AIP-201: below k3s 1.35 the control-plane cloud-init must carry no feature gate."
  }

  assert {
    condition     = !strcontains(local.worker_cloud_init_by_region["me-east-215-a"], "feature-gates")
    error_message = "AIP-201: below k3s 1.35 the worker cloud-init must carry no feature gate."
  }
}

run "pod_certificate_gates_from_k3s_1_35" {
  command = plan

  variables {
    k3s_version = "v1.35.10+k3s1"
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = strcontains(local.cp_cloud_init_by_region["me-east-215-a"], " --kube-apiserver-arg=feature-gates=ClusterTrustBundle=true,ClusterTrustBundleProjection=true,PodCertificateRequest=true --kube-apiserver-arg=runtime-config=certificates.k8s.io/v1beta1=true --kube-controller-manager-arg=feature-gates=ClusterTrustBundle=true,ClusterTrustBundleProjection=true,PodCertificateRequest=true --kube-scheduler-arg=feature-gates=ClusterTrustBundle=true,ClusterTrustBundleProjection=true,PodCertificateRequest=true --kubelet-arg=feature-gates=ClusterTrustBundle=true,ClusterTrustBundleProjection=true,PodCertificateRequest=true --write-kubeconfig-mode=0600")
    error_message = "AIP-201: on k3s 1.35 the control-plane cloud-init must start k3s with the five gate flags after k3s_extra_args."
  }

  assert {
    condition     = strcontains(local.worker_cloud_init_by_region["me-east-215-a"], " --kubelet-arg=feature-gates=ClusterTrustBundle=true,ClusterTrustBundleProjection=true,PodCertificateRequest=true\"")
    error_message = "AIP-201: on k3s 1.35 the worker cloud-init must start the kubelet with the gates."
  }
}

# ── SOV-140: the post-back sends the parent-bootstrap kubeconfig ──────────
# The kubeconfig post-back of every region's first control plane reads
# /var/lib/catalyst/parent-bootstrap.kubeconfig: the kubeconfig of
# ServiceAccount kube-system/parent-bootstrap that the shared template writes
# just before the post-back and deletes just after it. It never reads the
# node's administrator file /etc/rancher/k3s/k3s.yaml, and it deletes its
# /tmp copy when it is done.
run "post_back_sends_parent_bootstrap_kubeconfig" {
  command = plan

  variables {
    kubeconfig_bearer_token = "test-bearer"
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      },
      {
        code               = "me-east-215-b"
        cloud_region       = "me-east-215"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = length(regexall("(?s)config set-credentials default --token=.*/var/lib/catalyst/parent-bootstrap\\.kubeconfig > /tmp/kubeconfig-rewritten\\.yaml\n.*\n      rm -f /tmp/kubeconfig-rewritten\\.yaml\n.*\n  - rm -f /var/lib/catalyst/parent-bootstrap\\.kubeconfig\n", local.cp_cloud_init_by_region["me-east-215-a"])) == 1 && !strcontains(local.cp_cloud_init_by_region["me-east-215-a"], "/etc/rancher/k3s/k3s.yaml > /tmp/")
    error_message = "SOV-140: the primary region's post-back must read /var/lib/catalyst/parent-bootstrap.kubeconfig, between the step that writes it and the step that deletes it, delete its /tmp copy, and never read /etc/rancher/k3s/k3s.yaml."
  }

  assert {
    condition     = length(regexall("(?s)config set-credentials default --token=.*/var/lib/catalyst/parent-bootstrap\\.kubeconfig > /tmp/kubeconfig-secondary\\.yaml\n.*\n      rm -f /tmp/secondary-kubeconfig-body\\.json /tmp/kubeconfig-secondary\\.yaml\n.*\n  - rm -f /var/lib/catalyst/parent-bootstrap\\.kubeconfig\n", local.cp_cloud_init_by_region["me-east-215-b"])) == 1 && !strcontains(local.cp_cloud_init_by_region["me-east-215-b"], "/etc/rancher/k3s/k3s.yaml > /tmp/")
    error_message = "SOV-140: the secondary region's post-back must read /var/lib/catalyst/parent-bootstrap.kubeconfig, between the step that writes it and the step that deletes it, delete its /tmp copy, and never read /etc/rancher/k3s/k3s.yaml."
  }
}

# SOV-141 — the peer token. In a region that is not the primary, cloud-init
# creates ServiceAccount kube-system/handover-peer with its cluster-admin
# binding and token Secret, writes /var/lib/catalyst/handover-peer.kubeconfig
# before the post-back, sends it in the post-back body under the key
# peerKubeconfig with the server set to the region's address, and deletes it
# after the post-back. The primary region has no handover-peer at all.
run "post_back_sends_handover_peer_kubeconfig" {
  command = plan

  variables {
    kubeconfig_bearer_token = "test-bearer"
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      },
      {
        code               = "me-east-215-b"
        cloud_region       = "me-east-215"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = length(regexall("(?s)\n    export KUBECONFIG=/etc/rancher/k3s/k3s\\.yaml N=handover-peer S=kube-system K=/var/lib/catalyst/handover-peer\\.kubeconfig\n.*exit 97; }\n.*sed -i \"s\\|server: https://127\\.0\\.0\\.1:6443\\|server: https://\\$\\{MY_EIP\\}:6443\\|\" /var/lib/catalyst/handover-peer\\.kubeconfig\n.*'peerKubeconfig':open\\('/var/lib/catalyst/handover-peer\\.kubeconfig'\\)\\.read\\(\\).*\n  - rm -f /var/lib/catalyst/parent-bootstrap\\.kubeconfig\n  - rm -f /var/lib/catalyst/handover-peer\\.kubeconfig\n", local.cp_cloud_init_by_region["me-east-215-b"])) == 1 && length(regexall("    type: kubernetes\\.io/service-account-token\n", local.cp_cloud_init_by_region["me-east-215-b"])) == 2
    error_message = "SOV-141: the secondary region must create the handover-peer account and its token Secret, write /var/lib/catalyst/handover-peer.kubeconfig before the post-back, send it under peerKubeconfig with its server set to the region's address, and delete it after the post-back."
  }

  assert {
    condition     = !strcontains(local.cp_cloud_init_by_region["me-east-215-a"], "handover-peer") && !strcontains(local.cp_cloud_init_by_region["me-east-215-a"], "exit 97")
    error_message = "SOV-141: the primary region must not create a handover-peer account or send a peer kubeconfig."
  }
}

# NET-010 — the peer ports are closed to the internet. The mesh dial port
# (var.clustermesh_proxy_port, 12379) admits only the Sovereign's own public
# addresses (every region's control-plane and NAT EIP, as /32) and every
# region's node subnet and pod range. Cilium WireGuard (51871) admits only the
# node subnets and pod ranges. The rules for 2379, 51820, 8472 and 6081 are
# deleted; ICMP is unchanged. No rule of these two families is 0.0.0.0/0.
run "peer_ports_closed" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      },
      {
        code               = "me-east-215-b"
        cloud_region       = "me-east-215"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  # 2 target regions x (4 public addresses + 4 node/pod ranges).
  assert {
    condition     = length(huaweicloud_networking_secgroup_rule.ingress_clustermesh_proxy) == 16
    error_message = "The mesh dial port must have one rule per target region and per source: 2 x (4 public addresses + 4 node/pod ranges) = 16."
  }
  assert {
    condition = alltrue([for k in ["cp|me-east-215-a", "cp|me-east-215-b", "nat|me-east-215-a", "nat|me-east-215-b"] :
    contains(keys(huaweicloud_networking_secgroup_rule.ingress_clustermesh_proxy), "me-east-215-a|${k}") && contains(keys(huaweicloud_networking_secgroup_rule.ingress_clustermesh_proxy), "me-east-215-b|${k}")])
    error_message = "Each region's mesh dial port must admit every region's control-plane and NAT address."
  }
  assert {
    condition     = alltrue([for c in local.mesh_ingress_cidrs : contains(keys(huaweicloud_networking_secgroup_rule.ingress_clustermesh_proxy), "me-east-215-b|${c}")])
    error_message = "The mesh dial port must admit every region's node subnet and pod range."
  }
  assert {
    condition     = length(local.sovereign_public_sources) == 4 && alltrue([for v in values(local.sovereign_public_sources) : endswith(v, "/32")])
    error_message = "The Sovereign's own public sources must be the 2 control-plane and 2 NAT addresses, each as /32."
  }
  assert {
    condition     = alltrue([for r in huaweicloud_networking_secgroup_rule.ingress_clustermesh_proxy : r.port_range_min == 12379 && r.port_range_max == 12379 && r.protocol == "tcp"])
    error_message = "Every mesh dial rule must be TCP 12379."
  }
  assert {
    condition     = alltrue([for r in huaweicloud_networking_secgroup_rule.ingress_clustermesh_proxy : r.remote_ip_prefix != "0.0.0.0/0"])
    error_message = "No rule for the mesh dial port 12379 may have the source 0.0.0.0/0."
  }

  # 2 target regions x 4 node/pod ranges.
  assert {
    condition     = length(huaweicloud_networking_secgroup_rule.ingress_cilium_wireguard) == 8
    error_message = "Cilium WireGuard (51871) must have one rule per target region and per node/pod range: 2 x 4 = 8."
  }
  assert {
    condition     = alltrue([for r in huaweicloud_networking_secgroup_rule.ingress_cilium_wireguard : r.port_range_min == 51871 && r.remote_ip_prefix != "0.0.0.0/0" && contains(local.mesh_ingress_cidrs, r.remote_ip_prefix)])
    error_message = "Every Cilium WireGuard (51871) rule must take its source from the node/pod ranges, never 0.0.0.0/0."
  }

  # ICMP is unchanged: one rule per region, from any source.
  assert {
    condition     = length(huaweicloud_networking_secgroup_rule.ingress_icmp) == 2 && alltrue([for r in huaweicloud_networking_secgroup_rule.ingress_icmp : r.remote_ip_prefix == "0.0.0.0/0"])
    error_message = "ICMP must stay one rule per region from 0.0.0.0/0 (path-MTU discovery)."
  }
}

# NET-010 — on a single region the same rules collapse to the region's own
# addresses and ranges: 2 public addresses + 2 node/pod ranges for the mesh
# dial port, 2 node/pod ranges for Cilium WireGuard.
run "peer_ports_closed_single_region" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = length(huaweicloud_networking_secgroup_rule.ingress_clustermesh_proxy) == 4 && length(huaweicloud_networking_secgroup_rule.ingress_cilium_wireguard) == 2
    error_message = "Single region: the mesh dial port must have 4 rules (2 public addresses + 2 node/pod ranges) and Cilium WireGuard 2 rules."
  }
  assert {
    condition     = alltrue([for r in concat(values(huaweicloud_networking_secgroup_rule.ingress_clustermesh_proxy), values(huaweicloud_networking_secgroup_rule.ingress_cilium_wireguard)) : r.remote_ip_prefix != "0.0.0.0/0"])
    error_message = "Single region: no rule for 12379 or 51871 may have the source 0.0.0.0/0."
  }
}

# NET-100 — the network registry. Each run fixes the addresses of the three
# EIP resources and compares output network_registry with a document under
# scripts/tests/check-network-registry/. scripts/check-network-registry.py
# --self-test checks those documents against every rule of the registry, so a
# module change that breaks a rule fails here or there.
run "network_registry_one_region" {
  command = plan

  override_resource {
    target = huaweicloud_vpc_eip.elb_primary
    values = {
      address = "203.0.113.10"
    }
  }
  override_resource {
    target = huaweicloud_vpc_eip.cp
    values = {
      address = "203.0.113.11"
    }
  }
  override_resource {
    target = huaweicloud_vpc_eip.nat
    values = {
      address = "203.0.113.12"
    }
  }

  variables {
    huawei_az = "region-1a"
    regions = [
      {
        code               = "region-a"
        cloud_region       = "region-1"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = output.network_registry == jsonencode(jsondecode(file("../../../scripts/tests/check-network-registry/valid-one-region.json")))
    error_message = "NET-100: network_registry differs from scripts/tests/check-network-registry/valid-one-region.json: ${output.network_registry}"
  }
  assert {
    condition     = !strcontains(output.network_registry, "meshEndpoint")
    error_message = "NET-100: one region has no meshEndpoint."
  }
}

# Two regions: the CIDRs of the Tier-B fixture do not move (raw base 127 for
# deadbeefcafef00d), and every region carries its meshEndpoint.
run "network_registry_two_regions" {
  command = plan

  override_resource {
    target = huaweicloud_vpc_eip.elb_primary
    values = {
      address = "203.0.113.10"
    }
  }
  override_resource {
    target = huaweicloud_vpc_eip.cp
    values = {
      address = "203.0.113.11"
    }
  }
  override_resource {
    target = huaweicloud_vpc_eip.nat
    values = {
      address = "203.0.113.12"
    }
  }

  variables {
    huawei_az = "region-1a"
    regions = [
      {
        code               = "region-a"
        cloud_region       = "region-1"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      },
      {
        code               = "region-b"
        cloud_region       = "region-1"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = local.region_vpc_cidr["region-a"] == "10.127.0.0/16" && local.region_vpc_cidr["region-b"] == "10.137.0.0/16"
    error_message = "NET-100: the node VPC CIDRs of a two-region Sovereign must not move (10.127.0.0/16 and 10.137.0.0/16 for deadbeefcafef00d)."
  }
  assert {
    condition     = output.network_registry == jsonencode(jsondecode(file("../../../scripts/tests/check-network-registry/valid-two-regions.json")))
    error_message = "NET-100: network_registry differs from scripts/tests/check-network-registry/valid-two-regions.json: ${output.network_registry}"
  }
}

# Three regions: the pod supernet is 10.40.0.0/13. 0123456789ef59d0 hashes to
# raw base 45, whose node VPC 10.45.0.0/16 lies in that supernet; the base
# moves up by the smallest free shift, 3, to 48.
run "network_registry_three_regions" {
  command = plan

  override_resource {
    target = huaweicloud_vpc_eip.elb_primary
    values = {
      address = "203.0.113.10"
    }
  }
  override_resource {
    target = huaweicloud_vpc_eip.cp
    values = {
      address = "203.0.113.11"
    }
  }
  override_resource {
    target = huaweicloud_vpc_eip.nat
    values = {
      address = "203.0.113.12"
    }
  }

  variables {
    deployment_id = "0123456789ef59d0"
    huawei_az     = "region-1a"
    regions = [
      {
        code               = "region-a"
        cloud_region       = "region-1"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      },
      {
        code               = "region-b"
        cloud_region       = "region-1"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      },
      {
        code               = "region-c"
        cloud_region       = "region-1"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      }
    ]
  }

  assert {
    condition     = local.cidr_base_raw == 45 && local.cidr_base == 48
    error_message = "NET-100: with three regions the raw base 45 must move to 48, out of the pod supernet 10.40.0.0/13."
  }
  assert {
    condition     = output.network_registry == jsonencode(jsondecode(file("../../../scripts/tests/check-network-registry/valid-three-regions.json")))
    error_message = "NET-100: network_registry differs from scripts/tests/check-network-registry/valid-three-regions.json: ${output.network_registry}"
  }
}

# More than six regions is refused: the pod supernet holds six pod CIDRs.
run "network_registry_seven_regions_refused" {
  command = plan

  variables {
    huawei_az = "region-1a"
    regions = [
      for c in ["a", "b", "c", "d", "e", "f", "g"] : {
        code               = "region-${c}"
        cloud_region       = "region-1"
        role               = c == "a" ? "primary" : "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      }
    ]
  }

  expect_failures = [var.regions]
}

# ── #7893: the worst-case render fits the limit of the ECS API.
# A fresh two-region installation from main of 14 Nov 2026 failed in apply:
# the API refused both control-plane servers, `the size of userdata [43908]
# is over quota limits [43692]` (43692 is the base64 form of 32769 bytes, so
# the limit is 32768 bytes of raw user data). This run renders the module
# with the worst-case variables that scripts/cloudinit-size-check.py
# measures (it writes them below with --write-fixtures and says what each
# length stands for), and k3s tokens of their real length (64 characters).
# The Elastic IP addresses are the mock's (shorter than the 15 characters of
# the longest IPv4 form); scripts/cloudinit-size-check.py measures with the
# longest. On main before #7893 this render was about 35300 bytes in both
# regions; with the Flux bootstrap document and the Cilium values packed it
# fits.
run "worst_case_user_data_fits_the_api_limit" {
  command = plan

  # BEGIN worst-case variables, written by scripts/cloudinit-size-check.py --write-fixtures
  variables {
    bcp_topology              = "active-hotstandby"
    catalyst_api_url          = "https://console.vhwtxhpiag5nlbjhyfkfz36kcox31rsv6.omantel.biz/sovereign"
    console_isolation_enabled = "true"
    default_storage_class     = "evs-ssd"
    deployment_id             = "63ccf96f83c59e51"
    enable_hot_standby        = "true"
    enable_shared_pg          = "true"
    ghcr_pull_token           = "ghcr_filler_JKoBErUo62nLAJK5Keh4Biwuz53hoMGaBPB6HXU3W5gocWjikuaW8mPH1UVL6h8hS4DEzcnPVUnhHo0cS"
    gitops_repo_url           = "http://gitea-http.gitea.svc.cluster.local:3000/openova/provider-pack"
    handover_jwt_public_key   = "{\"alg\":\"RS256\",\"e\":\"AQAB\",\"kty\":\"RSA\",\"n\":\"jFfiWIe242hUG0FlefX2yClTWSyh-4cTacQBjPJuGLoXnxhR4t0_0WZeah0ImeJsw9HMrloVq7ooT76jrcXebjmIEScBf0v7CN4Kj4bkkNUJRpLMDY56nzOPqALld61c8K3IDDdoAUR4tNoYEABKlWT8XD3oQMINzGg-x7kUsudqkheDYqziWbwd9EsWw2evx8l900IvGvXCfDEOAPe-YQBM5ENUdbbJyqKVl9BrQFVBOPO5c7fAI4uCeVNVzvIrl4IQ7i50i1kA5DS5YvCFSaPlXHxwDrZsn9TLuChC90XeLo_jcyfr_08yxGV5nUMcim2uYB8wNRzSA-xUeCZG8x\",\"use\":\"sig\"}"
    harbor_robot_token        = "KCVhNCbzrrWzD3iyA5c4cVWoqF25f5j6"
    huawei_access_key         = "HWAKFILLER7ELYYULOHV"
    huawei_project_id         = "f431d7d76e2616d0a27980d49a63fa4b"
    huawei_secret_key         = "DF1FpgLSkvsSzqgHycPF6vzOazJrYxss2rEw2eeQ"
    kubeconfig_bearer_token   = "a5936414e8f1193d572074048696a49118ca3947da6121e8aec6beae72acd741"
    marketplace_enabled       = "true"
    obs_bucket_name           = "catalyst-ae1r3b3eqk10j5wbg8ul-omani-works-63ccf96f"
    org_email                 = "bkzitohxvpztjrswdqdfeabizcvnzixuevmihzkweoxgwumfqanrpcrfsbqvadii@puzeqhhzsnzqdxw9e76xanom0auqgruadge9gj3m5swml9pin0ctkj4pyxzwwoj.vskqogyim6cpkz3kygtk4sb9d5n73ierktp1yh3fixs06u61qzo5ga6nyoa47nl.jpvqlp3kcdxlicem3icpgrdvhkq3gbvq0afmz4y7czjaksfvbhgjxvj89o.om"
    org_name                  = "Omani National Sovereign Cloud Services & Data Platforms Company (Closed Joint Stock) & Partners LLC"
    parent_domains_yaml       = "[{name: \"ae1r3b3eqk10j5wbg8ul.omani.works\", role: \"primary\"}, {name: \"omani.homes\", role: \"org-pool\"}, {name: \"omani.rest\", role: \"org-pool\"}, {name: \"omani.trade\", role: \"org-pool\"}]"
    pdm_basic_auth_pass       = "fOxOud9JoeoSGJjfNZbUSGrONfhEzatlUijrH0u89WedgLRW8pfiDsNrOUvDba2U"
    pdm_basic_auth_user       = "ZMsusu7pFBC5ulGjXvckKA4VmcIdqn6A"
    pdns_api_host             = "https://pdns.vhwtxhpiag5nlbjhyfkfz36kcox31rsv6.omantel.biz"
    powerdns_api_key          = "qG5I3mpjZOOQFPsRBhyYUUFybJWNzvTY"
    provider_api_cidrs = [
      "149.104.149.0/24",
      "141.131.193.0/24",
      "117.133.108.0/24",
      "171.108.110.0/24",
    ]
    qa_fixtures_enabled = "true"
    regions = [
      {
        "cloud_region"       = "me-east-215"
        "code"               = "me-east-215-a"
        "control_plane_size" = "s7n.large.4"
        "role"               = "primary"
        "worker_count"       = 50
        "worker_size"        = "m7n.xlarge.8"
      },
      {
        "cloud_region"       = "me-east-215"
        "code"               = "me-east-215-b"
        "control_plane_size" = "s7n.large.4"
        "role"               = "secondary"
        "worker_count"       = 50
        "worker_size"        = "m7n.xlarge.8"
      },
    ]
    retry_attempt  = 9
    sovereign_fqdn = "ae1r3b3eqk10j5wbg8ul.omani.works"
    ssh_public_key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIfxPSrXwdxgFhS9hMbVHdAee21BJZFReoM9QleQIH1OM sovereign-admin"
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
      "LOCAL_REGISTRY_HOST"                = "registry.ae1r3b3eqk10j5wbg8ul.omani.works"
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

  assert {
    condition     = alltrue([for code, ud in local.cp_cloud_init_by_region : length(ud) <= 32768]) && length(local.cp_cloud_init_by_region) == 2
    error_message = "#7893: the worst-case control-plane renders are ${join(", ", [for code, ud in local.cp_cloud_init_by_region : "${code} ${nonsensitive(length(ud))} bytes"])}; each must stay at most 32768, the limit of the ECS API."
  }

  assert {
    condition     = alltrue([for code, ud in local.worker_cloud_init_by_region : length(ud) <= 32768]) && length(local.worker_cloud_init_by_region) == 2
    error_message = "#7893: the worst-case worker renders are ${join(", ", [for code, ud in local.worker_cloud_init_by_region : "${code} ${nonsensitive(length(ud))} bytes"])}; each must stay at most 32768, the limit of the ECS API."
  }

  assert {
    condition     = strcontains(local.cp_flux_bootstrap_by_region["me-east-215-a"], "SOVEREIGN_FQDN: ${var.sovereign_fqdn}\n") && strcontains(local.cp_flux_bootstrap_by_region["me-east-215-b"], "SOVEREIGN_REGION_ROLE: \"secondary\"\n")
    error_message = "#7893: the packed Flux bootstrap document of each region must decode to that region's document."
  }

  # SOV-132: this document has no origin per name and its origin host is not a
  # proxy name: every name takes the origin registry, the origin host gets an
  # entry of its own (local first), and only it gets the robot login.
  assert {
    condition = (
      length(yamldecode(local.registry_mirror_yaml_huawei).mirrors) == 11
      && jsonencode(yamldecode(local.registry_mirror_yaml_huawei).mirrors[var.variables["ORIGIN_REGISTRY_HOST"]]) == jsonencode({ endpoint = ["https://${var.variables["LOCAL_REGISTRY_HOST"]}", "https://${var.variables["ORIGIN_REGISTRY_HOST"]}"] })
      && alltrue([for n, m in yamldecode(local.registry_mirror_yaml_huawei).mirrors : m.endpoint[1] == "https://${var.variables["ORIGIN_REGISTRY_HOST"]}" if length(m.endpoint) == 2])
      && jsonencode(keys(yamldecode(local.registry_mirror_yaml_huawei).configs)) == jsonencode([var.variables["ORIGIN_REGISTRY_HOST"]])
      && strcontains(local.worker_cloud_init_by_region["me-east-215-b"], "    content: |\n      ${local.registry_mirror_yaml_huawei}\n")
    )
    error_message = "SOV-132: with no origin per name, every name must take the origin registry, the origin host must have its own entry, and only it the login; the secondary worker must write the file too."
  }
}

# ── SOV-132: the node registry file follows the variables document ───────
# The document above is a child's: its origin host (ghcr.io) is a proxy name,
# so it gets no second entry; every name has its own origin; two names are
# served at the root (no rewrite); ghcr.io keeps the rewrite of the hosted
# project (#4049); the origin that serves no proxy project gets no login.
# Every server and agent starts k3s with the flag.
run "node_registry_file_follows_the_variables_document" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition = jsonencode(yamldecode(local.registry_mirror_yaml_huawei)) == jsonencode({
      configs = { "registry.parent.example.test:5000" = { auth = { username = "robot$openova-bot", password = var.harbor_robot_token } } }
      mirrors = {
        "*"                         = { endpoint = ["https://registry.sov.example.test"] }
        "docker.io"                 = { endpoint = ["https://registry.sov.example.test", "http://registry.parent.example.test:5000"], rewrite = { "(.*)" = "proxy-dockerhub/$1" } }
        "gcr.io"                    = { endpoint = ["https://registry.sov.example.test", "http://registry.parent.example.test:5000"], rewrite = { "(.*)" = "proxy-gcr/$1" } }
        "ghcr.io"                   = { endpoint = ["https://registry.sov.example.test", "http://registry.parent.example.test:5000"], rewrite = { "^openova-io/openova/(.*)" = "openova-io/openova/$1", "^(openova-io/(?:[^o]|o[^p]|op[^e]|ope[^n]|open[^o]|openo[^v]|openov[^a]|openova[^/]).*|(?:[^o]|o[^p]|op[^e]|ope[^n]|open[^o]|openo[^v]|openov[^a]|openova[^-]|openova-[^i]|openova-i[^o]).*)" = "proxy-ghcr/$0" } }
        "harbor.openova.io"         = { endpoint = ["https://registry.sov.example.test", "http://registry.parent.example.test:5000"] }
        "quay.io"                   = { endpoint = ["https://registry.sov.example.test", "http://registry.parent.example.test:5000"], rewrite = { "(.*)" = "proxy-quay/$1" } }
        "registry-1.docker.io"      = { endpoint = ["https://registry.sov.example.test", "http://registry.parent.example.test:5000"], rewrite = { "(.*)" = "proxy-dockerhub/$1" } }
        "registry.k8s.io"           = { endpoint = ["https://registry.sov.example.test", "http://registry.parent.example.test:5000"], rewrite = { "(.*)" = "proxy-k8s/$1" } }
        "registry.sov.example.test" = { endpoint = ["https://registry.sov.example.test"] }
        "xpkg.upbound.io"           = { endpoint = ["https://registry.sov.example.test", "https://xpkg.example.test"] }
      }
    })
    error_message = "SOV-132: the registry file must follow the rule of contract C5 for the document of this test, with the rewrite of the hosted project for ghcr.io."
  }

  assert {
    condition     = strcontains(local.cp_cloud_init_by_region["me-east-215-a"], "\n  - path: /etc/rancher/k3s/registries.yaml\n    permissions: '0600'\n    encoding: gz+b64\n    content: ${base64gzip(format("%s\n", local.registry_mirror_yaml_huawei))}\n") && strcontains(local.cp_cloud_init_by_region["me-east-215-a"], " --disable=local-storage --disable-default-registry-endpoint --cluster-cidr=")
    error_message = "SOV-132: the control plane must write the registry file and start k3s with --disable-default-registry-endpoint."
  }

  assert {
    condition     = strcontains(local.worker_cloud_init_by_region["me-east-215-a"], "\n  - path: /etc/rancher/k3s/registries.yaml\n    permissions: '0644'\n    content: |\n      ${local.registry_mirror_yaml_huawei}\n") && strcontains(local.worker_cloud_init_by_region["me-east-215-a"], "INSTALL_K3S_EXEC=\"agent --disable-default-registry-endpoint --kubelet-arg=")
    error_message = "SOV-132: every worker must write the control plane's registry file and start the k3s agent with --disable-default-registry-endpoint."
  }
}

# ── SOV-132: the file of a Sovereign that consumes only its own registry ──
# The document above with SOVEREIGN_CONSUME "local", and an origin host that
# is not a proxy name, so that the missing entry for ORIGIN_REGISTRY_HOST is
# the work of the local branch and not of the skip of SOV-135. Every name
# lists only the Sovereign's own registry, "*" too, and no pull login is
# written (upstream.Variables.RegistriesYAML renders none). ghcr.io keeps the
# rewrite of the hosted project (#4049).
run "node_registry_file_without_origin_when_consume_is_local" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
    variables = {
      SOVEREIGN_MODE                     = "sovereign"
      SOVEREIGN_CONSUME                  = "local"
      HANDOVER_AUTO                      = "false"
      HANDOVER_HOLD_SECONDS              = "600"
      REGISTRY_HOST                      = "registry.parent.example.test"
      REGISTRY_SCHEME                    = "https"
      LOCAL_REGISTRY_HOST                = "registry.sov.example.test"
      ORIGIN_REGISTRY_HOST               = "registry.parent.example.test"
      ORIGIN_REGISTRY_SCHEME             = "https"
      ORIGIN_REGISTRY_CA_B64             = ""
      ORIGIN_PROXY_ENDPOINTS_JSON        = "{\"docker.io\":\"http://registry.parent.example.test:5000\",\"gcr.io\":\"http://registry.parent.example.test:5000\",\"ghcr.io\":\"http://registry.parent.example.test:5000\",\"harbor.openova.io\":\"http://registry.parent.example.test:5000\",\"quay.io\":\"http://registry.parent.example.test:5000\",\"registry-1.docker.io\":\"http://registry.parent.example.test:5000\",\"registry.k8s.io\":\"http://registry.parent.example.test:5000\",\"xpkg.upbound.io\":\"https://xpkg.example.test\"}"
      REGISTRY_PULL_SECRET               = "ghcr-pull"
      REGISTRY_PATH_CHARTS               = "openova-io"
      REGISTRY_PATH_RELEASE              = "openova-io/release"
      REGISTRY_PATH_PROVIDER_PACKS       = "openova-io/provider-packs"
      REGISTRY_PATH_XPKG                 = "proxy-xpkg"
      REGISTRY_PATH_TOFU_PROVIDERS       = "tofu-providers"
      REGISTRY_PATH_NODE_ARTIFACTS       = "node-artifacts"
      REGISTRY_PATH_VULN_DB              = "proxy-vulndb/aquasecurity/trivy-db"
      REGISTRY_PROXY_PROJECTS_JSON       = "{\"docker.io\":\"proxy-dockerhub\",\"gcr.io\":\"proxy-gcr\",\"ghcr.io\":\"proxy-ghcr\",\"harbor.openova.io\":\"\",\"quay.io\":\"proxy-quay\",\"registry-1.docker.io\":\"proxy-dockerhub\",\"registry.k8s.io\":\"proxy-k8s\",\"xpkg.upbound.io\":\"\"}"
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

  assert {
    condition = jsonencode(yamldecode(local.registry_mirror_yaml_huawei)) == jsonencode({
      configs = {}
      mirrors = {
        "*"                         = { endpoint = ["https://registry.sov.example.test"] }
        "docker.io"                 = { endpoint = ["https://registry.sov.example.test"], rewrite = { "(.*)" = "proxy-dockerhub/$1" } }
        "gcr.io"                    = { endpoint = ["https://registry.sov.example.test"], rewrite = { "(.*)" = "proxy-gcr/$1" } }
        "ghcr.io"                   = { endpoint = ["https://registry.sov.example.test"], rewrite = { "^openova-io/openova/(.*)" = "openova-io/openova/$1", "^(openova-io/(?:[^o]|o[^p]|op[^e]|ope[^n]|open[^o]|openo[^v]|openov[^a]|openova[^/]).*|(?:[^o]|o[^p]|op[^e]|ope[^n]|open[^o]|openo[^v]|openov[^a]|openova[^-]|openova-[^i]|openova-i[^o]).*)" = "proxy-ghcr/$0" } }
        "harbor.openova.io"         = { endpoint = ["https://registry.sov.example.test"] }
        "quay.io"                   = { endpoint = ["https://registry.sov.example.test"], rewrite = { "(.*)" = "proxy-quay/$1" } }
        "registry-1.docker.io"      = { endpoint = ["https://registry.sov.example.test"], rewrite = { "(.*)" = "proxy-dockerhub/$1" } }
        "registry.k8s.io"           = { endpoint = ["https://registry.sov.example.test"], rewrite = { "(.*)" = "proxy-k8s/$1" } }
        "registry.sov.example.test" = { endpoint = ["https://registry.sov.example.test"] }
        "xpkg.upbound.io"           = { endpoint = ["https://registry.sov.example.test"] }
      }
    })
    error_message = "SOV-132: with SOVEREIGN_CONSUME local the registry file must follow the rule of contract C5: the Sovereign's own registry only, no origin."
  }

  assert {
    condition     = alltrue([for n, m in yamldecode(local.registry_mirror_yaml_huawei).mirrors : jsonencode(m.endpoint) == jsonencode(["https://registry.sov.example.test"])]) && !contains(keys(yamldecode(local.registry_mirror_yaml_huawei).mirrors), var.variables["ORIGIN_REGISTRY_HOST"]) && length(yamldecode(local.registry_mirror_yaml_huawei).configs) == 0
    error_message = "SOV-132: with SOVEREIGN_CONSUME local every entry, \"*\" included, must list only the local registry, ORIGIN_REGISTRY_HOST must have no entry and no pull login may be written."
  }
}

# ── SOV-131: HANDOVER_AUTO reaches FIRE_CUTOVER_ON_HANDOVER; a document
# without exactly the keys of contract C5 is refused at plan time.
run "variables_handover_auto_reaches_cloud_init" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = strcontains(local.cp_flux_bootstrap_by_region["me-east-215-a"], "FIRE_CUTOVER_ON_HANDOVER: \"false\"")
    error_message = "SOV-131: variables[\"HANDOVER_AUTO\"] must reach the substitute FIRE_CUTOVER_ON_HANDOVER of the control-plane cloud-init."
  }
}

run "variables_refuses_a_document_without_the_contract_keys" {
  command = plan

  variables {
    variables = { HANDOVER_AUTO = "true", NOT_A_CONTRACT_KEY = "x" }
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  expect_failures = [var.variables]
}

# NET-011 — the Kubernetes API port 6443 admits only var.admin_cidrs,
# var.provisioner_cidrs, every region's node subnet and pod range and the
# Sovereign's own public addresses. No rule is 0.0.0.0/0. The network
# registry carries the two lists as controlPlaneAccess.
run "api_port_limited" {
  command = plan

  variables {
    admin_cidrs       = ["198.51.100.0/28"]
    provisioner_cidrs = ["192.0.2.10/32"]
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      },
      {
        code               = "me-east-215-b"
        cloud_region       = "me-east-215"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  # 2 target regions x (4 public addresses + 1 sovereign-admin range + 1
  # provisioner range + 4 node/pod ranges).
  assert {
    condition     = length(huaweicloud_networking_secgroup_rule.ingress_6443) == 20
    error_message = "Port 6443 must have one rule per target region and per source: 2 x (4 public addresses + 1 sovereign-admin range + 1 provisioner range + 4 node/pod ranges) = 20."
  }
  assert {
    condition     = alltrue([for r in huaweicloud_networking_secgroup_rule.ingress_6443 : r.remote_ip_prefix != "0.0.0.0/0" && r.remote_ip_prefix != "::/0"])
    error_message = "No rule for port 6443 may have the source 0.0.0.0/0 or ::/0."
  }
  assert {
    condition     = alltrue([for r in huaweicloud_networking_secgroup_rule.ingress_6443 : r.port_range_min == 6443 && r.port_range_max == 6443 && r.protocol == "tcp"])
    error_message = "Every rule of ingress_6443 must be TCP 6443."
  }
  assert {
    condition = alltrue([for k in ["198.51.100.0/28", "192.0.2.10/32", "cp|me-east-215-a", "cp|me-east-215-b", "nat|me-east-215-a", "nat|me-east-215-b"] :
    contains(keys(huaweicloud_networking_secgroup_rule.ingress_6443), "me-east-215-a|${k}") && contains(keys(huaweicloud_networking_secgroup_rule.ingress_6443), "me-east-215-b|${k}")])
    error_message = "Each region's port 6443 must admit the sovereign-admin range, the provisioner range and every region's control-plane and NAT address."
  }
  assert {
    condition     = alltrue([for c in local.mesh_ingress_cidrs : contains(keys(huaweicloud_networking_secgroup_rule.ingress_6443), "me-east-215-a|${c}")])
    error_message = "Port 6443 must admit every region's node subnet and pod range."
  }
  assert {
    condition     = alltrue([for r in huaweicloud_networking_secgroup_rule.ingress_6443 : contains(concat(local.api_access_cidrs, values(local.sovereign_public_sources)), r.remote_ip_prefix)])
    error_message = "Every rule of port 6443 must take its source from the declared lists or the Sovereign's own addresses."
  }
  assert {
    condition     = jsondecode(output.network_registry).controlPlaneAccess.adminCIDRs == ["198.51.100.0/28"] && jsondecode(output.network_registry).controlPlaneAccess.provisionerCIDRs == ["192.0.2.10/32"]
    error_message = "The network registry must carry admin_cidrs as controlPlaneAccess.adminCIDRs and provisioner_cidrs as controlPlaneAccess.provisionerCIDRs."
  }
}

# NET-011 — a range named in two lists, or equal to a node range, gets one
# rule; one region without lists keeps only its own 2 addresses and 2 ranges.
run "api_port_limited_single_region" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = length(huaweicloud_networking_secgroup_rule.ingress_6443) == 4
    error_message = "Single region without lists: port 6443 must have 4 rules (2 public addresses + 2 node/pod ranges)."
  }
  assert {
    condition     = alltrue([for r in huaweicloud_networking_secgroup_rule.ingress_6443 : r.remote_ip_prefix != "0.0.0.0/0"])
    error_message = "Single region: no rule for port 6443 may have the source 0.0.0.0/0."
  }
}

run "api_port_limited_duplicate_range_one_rule" {
  command = plan

  variables {
    admin_cidrs       = ["192.0.2.10/32"]
    provisioner_cidrs = ["192.0.2.10/32"]
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = length(huaweicloud_networking_secgroup_rule.ingress_6443) == 5
    error_message = "A range in both lists must get one rule: 2 public addresses + 1 range + 2 node/pod ranges = 5."
  }
}

# NET-011 — the two lists refuse any source and malformed ranges at plan time.
run "admin_cidrs_refuses_any_source" {
  command = plan

  variables {
    admin_cidrs = ["0.0.0.0/0"]
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  expect_failures = [var.admin_cidrs]
}

run "provisioner_cidrs_refuses_a_range_with_host_bits" {
  command = plan

  variables {
    provisioner_cidrs = ["192.0.2.10/24"]
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  expect_failures = [var.provisioner_cidrs]
}

run "provisioner_cidrs_refuses_ipv6_any_source" {
  command = plan

  variables {
    provisioner_cidrs = ["::/0"]
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  expect_failures = [var.provisioner_cidrs]
}

# ── CLD-090: contract version 1. The cloud region and the zone come from
# var.regions: cloud_region is the region of the provider, of the service
# endpoints and of the bucket; availability_zone is the region's zone, and an
# empty one falls back to var.huawei_az.
run "cloud_region_and_zone_come_from_regions" {
  command = plan

  variables {
    huawei_az = "region-9a"
    regions = [
      {
        code               = "region-a"
        cloud_region       = "region-9"
        availability_zone  = "region-9z"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      },
      {
        code               = "region-b"
        cloud_region       = "region-9"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      }
    ]
  }

  assert {
    condition     = output.contract_version == "1"
    error_message = "CLD-090: output contract_version must be \"1\"."
  }
  assert {
    condition     = output.obs_region == "region-9" && startswith(output.obs_endpoint, "https://obs.region-9.")
    error_message = "CLD-090: the bucket's region and endpoint must come from regions[0].cloud_region."
  }
  assert {
    condition     = huaweicloud_vpc_subnet.region["region-a"].availability_zone == "region-9z" && huaweicloud_vpc_subnet.region["region-b"].availability_zone == "region-9a"
    error_message = "CLD-090: a region's subnet must be in its availability_zone, or in var.huawei_az when that is empty."
  }
  assert {
    condition     = alltrue([for i in huaweicloud_compute_instance.control_plane : i.availability_zone == (strcontains(i.name, "-region-a-") ? "region-9z" : "region-9a")])
    error_message = "CLD-090: a control-plane node must be in its region's zone."
  }
  assert {
    condition     = alltrue([for k, i in huaweicloud_compute_instance.worker : i.availability_zone == (startswith(k, "region-a-") ? "region-9z" : "region-9a")])
    error_message = "CLD-090: a worker node must be in its region's zone."
  }
  assert {
    condition     = [for r in jsondecode(output.network_registry).regions : "${r.code}/${r.cloudRegion}/${r.az}"] == ["region-a/region-9/region-9z", "region-b/region-9/region-9a"]
    error_message = "CLD-090: the network registry must carry each region's cloud region and zone."
  }
  assert {
    condition     = jsondecode(output.network_registry).regionSeparation == "zone"
    error_message = "CLD-090: two regions of one cloud region in two zones are separated by zone."
  }
}

# CLD-090: this cloud cannot span cloud regions (profile: multiCloudRegion
# false), so regions with two different cloud_region values are refused.
run "regions_refuses_two_cloud_regions" {
  command = plan

  variables {
    regions = [
      {
        code               = "region-a"
        cloud_region       = "region-9"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      },
      {
        code               = "region-b"
        cloud_region       = "region-8"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      }
    ]
  }

  expect_failures = [var.regions]
}

# CLD-090: the first region must be the primary one, and a region code must
# not repeat.
run "regions_refuses_a_first_region_that_is_not_primary" {
  command = plan

  variables {
    regions = [
      {
        code               = "region-a"
        cloud_region       = "region-9"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      }
    ]
  }

  expect_failures = [var.regions]
}

run "regions_refuses_a_repeated_code" {
  command = plan

  variables {
    regions = [
      {
        code               = "region-a"
        cloud_region       = "region-9"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      },
      {
        code               = "region-a"
        cloud_region       = "region-9"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      }
    ]
  }

  expect_failures = [var.regions]
}

# ── SEC-150: the module calls the seal hook with its own region keys ──────
# Contract C8 (docs/contracts/c8-seal-provider.md): one seal key per region,
# for the same region keys as the k3s tokens. The hook itself is tested in
# tests/seal.tftest.hcl.
run "seal_hook_is_called_with_the_region_keys" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      },
      {
        code               = "me-east-215-b"
        cloud_region       = "me-east-215"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = output.seal_kind == "static-node-file"
    error_message = "SEC-150: output seal_kind must be the kind of this pack's seal hook, static-node-file."
  }

  assert {
    condition     = sort(keys(module.seal.stanza_by_region)) == sort(keys(random_password.k3s_server_token))
    error_message = "SEC-150: the seal hook must be called with the region keys of the k3s tokens, so that every region gets one seal key."
  }

  assert {
    condition     = module.seal.volume_device == "/dev/vdb"
    error_message = "SEC-150: the hook's volume_device of this pack must be /dev/vdb."
  }

  # SEC-152, contract C8: every control-plane instance carries the seal volume
  # the hook names, one 10 GB SSD data disk deleted with the instance; a
  # worker carries none. The control-plane cloud-init of each region stages
  # the region's key under /run, stops with code 96 when /dev/vdb is missing,
  # installs the key under /var/lib/catalyst/seal and writes the seal profile;
  # a worker's cloud-init carries no key. The key is the override at the head
  # of this file.
  assert {
    condition     = length(huaweicloud_compute_instance.control_plane) == 2 && alltrue([for cp in huaweicloud_compute_instance.control_plane : length(cp.data_disks) == 1 && cp.data_disks[0].type == "SSD" && cp.data_disks[0].size == 10 && cp.delete_disks_on_termination == true]) && alltrue([for w in huaweicloud_compute_instance.worker : length(w.data_disks) == 0])
    error_message = "SEC-152: each control-plane instance must carry one data disk (type SSD, size 10) and delete_disks_on_termination = true; a worker must carry no data disk."
  }

  assert {
    condition = length(local.cp_cloud_init_by_region) == 2 && alltrue([for code, ud in local.cp_cloud_init_by_region :
      strcontains(ud, "\n  - path: /run/catalyst-seal-stage/openbao-seal-493ee6f33a535b93.key\n    permissions: '0400'\n    encoding: b64\n    content: dGVzdC1zZWFsLWtleS0wMTIzNDU2Nzg5YWJjZGVmZ2g=\n")
      && strcontains(ud, "\n    [ -b /dev/vdb ] || exit 96\n")
      && strcontains(ud, "\n    install -m 0400 -o 100 -g 1000 /run/catalyst-seal-stage/openbao-seal-493ee6f33a535b93.key /var/lib/catalyst/seal/openbao-seal-493ee6f33a535b93.key\n")
      && strcontains(ud, "\n        kind: static-node-file\n        stanza: |\n          seal \"static\" {\n            current_key_id = \"493ee6f33a535b93\"\n")
    ]) && alltrue([for code, ud in local.worker_cloud_init_by_region : !strcontains(ud, "dGVzdC1zZWFsLWtleS0wMTIzNDU2Nzg5YWJjZGVmZ2g=") && !strcontains(ud, "catalyst-seal")])
    error_message = "SEC-152: the control-plane cloud-init of each region must stage the seal key under /run (0400, b64), check /dev/vdb with exit 96, install the key at /var/lib/catalyst/seal/openbao-seal-<key id>.key and write the seal profile; a worker's cloud-init must carry no key and no seal step."
  }
}

# NET-210: output region_separation. Two regions of one cloud region in two
# zones: every subnet and node is planned in its region's zone, both load
# balancers stand in the primary region's zone, and the separation is zone.
# huawei_az is a zone that no region names, so a resource that still reads
# var.huawei_az fails the run.
run "region_separation_two_zones" {
  command = plan

  variables {
    huawei_az = "region-9z"
    regions = [
      {
        code               = "region-a"
        cloud_region       = "region-9"
        availability_zone  = "region-9a"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      },
      {
        code               = "region-b"
        cloud_region       = "region-9"
        availability_zone  = "region-9b"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = huaweicloud_vpc_subnet.region["region-a"].availability_zone == "region-9a" && huaweicloud_vpc_subnet.region["region-b"].availability_zone == "region-9b"
    error_message = "NET-210: each region's subnet must be in that region's zone."
  }
  assert {
    condition     = sort([for i in huaweicloud_compute_instance.control_plane : "${i.availability_zone}/${strcontains(i.name, "-region-a-cp") ? "region-a" : "region-b"}"]) == tolist(["region-9a/region-a", "region-9b/region-b"])
    error_message = "NET-210: each region's control-plane node must be in that region's zone."
  }
  assert {
    condition     = sort([for k, i in huaweicloud_compute_instance.worker : "${i.availability_zone}/${strcontains(i.name, "-region-a-w") ? "region-a" : "region-b"}"]) == tolist(["region-9a/region-a", "region-9a/region-a", "region-9b/region-b", "region-9b/region-b"])
    error_message = "NET-210: each region's workers must be in that region's zone."
  }
  assert {
    condition     = toset(huaweicloud_elb_loadbalancer.primary.availability_zone) == toset(["region-9a"]) && toset(huaweicloud_elb_loadbalancer.console[0].availability_zone) == toset(["region-9a"])
    error_message = "NET-210: both load balancers stand in the primary region and must be in its zone."
  }
  assert {
    condition     = output.region_separation == "zone"
    error_message = "NET-210: two regions of one cloud region in two zones must give region_separation zone."
  }
  assert {
    condition     = output.region_separation == jsondecode(output.network_registry).regionSeparation
    error_message = "NET-210: region_separation must equal regionSeparation of network_registry."
  }
}

# NET-210: the default two-region request sets no zone. Every subnet, node and
# load balancer stays in var.huawei_az, as before this package, and the
# separation is none.
run "region_separation_default_none" {
  command = plan

  variables {
    regions = [
      {
        code               = "me-east-215-a"
        cloud_region       = "me-east-215"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      },
      {
        code               = "me-east-215-b"
        cloud_region       = "me-east-215"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 2
      }
    ]
  }

  assert {
    condition     = distinct(concat([for s in huaweicloud_vpc_subnet.region : s.availability_zone], [for i in huaweicloud_compute_instance.control_plane : i.availability_zone], [for i in huaweicloud_compute_instance.worker : i.availability_zone])) == tolist([var.huawei_az])
    error_message = "NET-210: with no zone set, every subnet and node must be in var.huawei_az."
  }
  assert {
    condition     = toset(huaweicloud_elb_loadbalancer.primary.availability_zone) == toset([var.huawei_az]) && toset(huaweicloud_elb_loadbalancer.console[0].availability_zone) == toset([var.huawei_az])
    error_message = "NET-210: with no zone set, both load balancers must be in var.huawei_az."
  }
  assert {
    condition     = output.region_separation == "none" && jsondecode(output.network_registry).regionSeparation == "none"
    error_message = "NET-210: two regions in one zone must give region_separation none."
  }
}

# NET-210: two regions that name the same zone share one failure domain.
run "region_separation_same_zone_none" {
  command = plan

  variables {
    regions = [
      {
        code               = "region-a"
        cloud_region       = "region-9"
        availability_zone  = "region-9a"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      },
      {
        code               = "region-b"
        cloud_region       = "region-9"
        availability_zone  = "region-9a"
        role               = "secondary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      }
    ]
  }

  assert {
    condition     = output.region_separation == "none"
    error_message = "NET-210: two regions in the same zone must give region_separation none."
  }
}

# NET-210: one region has nothing to be separated from.
run "region_separation_one_region_none" {
  command = plan

  variables {
    regions = [
      {
        code               = "region-a"
        cloud_region       = "region-9"
        availability_zone  = "region-9b"
        role               = "primary"
        control_plane_size = "s7n.large.4"
        worker_size        = "m7n.xlarge.8"
        worker_count       = 1
      }
    ]
  }

  assert {
    condition     = output.region_separation == "none"
    error_message = "NET-210: one region must give region_separation none."
  }
}
