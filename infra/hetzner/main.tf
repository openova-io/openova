# Catalyst Sovereign on Hetzner — canonical Phase 0 OpenTofu module.
#
# Per docs/ARCHITECTURE.md §10 + docs/SOVEREIGN-PROVISIONING.md §3-§4:
#   - This module provisions Phase 0 cloud resources on Hetzner.
#   - Cloud-init on the control-plane node installs k3s + bootstraps Flux +
#     installs Crossplane + provider-hcloud.
#   - Flux then takes over (Phase 1 hand-off): reconciles
#     clusters/<sovereign-fqdn>/ from the public OpenOva monorepo, installing
#     the 11-component bootstrap kit and bp-catalyst-platform umbrella.
#   - Crossplane adopts day-2 management of cloud resources after Phase 1.
#
# Per INVIOLABLE-PRINCIPLES.md:
#   - No hardcoded values (region, sizes, k3s flags all come from variables)
#   - No bespoke API calls (we use the canonical hcloud terraform provider)
#   - Phase 0 is OpenTofu, day-2 is Crossplane, GitOps is Flux, install unit is Blueprints

# ── Network: private 10.0.0.0/16 with control-plane subnet ────────────────

resource "hcloud_network" "main" {
  name     = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-net"
  ip_range = "10.0.0.0/16"
  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
  }
}

resource "hcloud_network_subnet" "main" {
  network_id   = hcloud_network.main.id
  type         = "cloud"
  network_zone = local.network_zone
  ip_range     = "10.0.1.0/24"
}

# ── Firewall: 80/443 + 6443 + ICMP open; 22 only when ssh_allowed_cidrs set ─

resource "hcloud_firewall" "main" {
  name = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-fw"

  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "80"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "6443"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
  rule {
    direction  = "in"
    protocol   = "icmp"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # DNS/53 — open to the world so the Sovereign's PowerDNS authoritative
  # server is reachable from Let's Encrypt resolvers (DNS-01 challenge) and
  # from the public internet for subdomain NS delegation. Both TCP and UDP
  # are required: TCP for zone transfers and large responses, UDP for
  # standard query traffic. The LB service (hcloud_load_balancer_service.dns)
  # forwards :53 → NodePort 30053 on the control-plane node where k3s exposes
  # the powerdns Service.
  rule {
    direction  = "in"
    protocol   = "tcp"
    port       = "53"
    source_ips = ["0.0.0.0/0", "::/0"]
  }
  rule {
    direction  = "in"
    protocol   = "udp"
    port       = "53"
    source_ips = ["0.0.0.0/0", "::/0"]
  }

  # SSH (22) is intentionally NOT open to the world. When ssh_allowed_cidrs is
  # set, we add a narrow rule for those operators only; otherwise the rule is
  # omitted entirely and break-glass is via Hetzner Console (out-of-band).
  # Operators tighten/widen this via Crossplane Composition once Phase 1
  # finishes — see infra/hetzner/README.md §"Firewall rules".
  dynamic "rule" {
    for_each = length(var.ssh_allowed_cidrs) > 0 ? [1] : []
    content {
      direction  = "in"
      protocol   = "tcp"
      port       = "22"
      source_ips = var.ssh_allowed_cidrs
    }
  }

  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
  }
}

# ── SSH key: from wizard input, never auto-generated ──────────────────────

resource "hcloud_ssh_key" "main" {
  name       = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}"
  public_key = var.ssh_public_key
  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
  }
}

# ── Control plane: 1 server (or 3 if ha_enabled), with k3s cloud-init ─────

locals {
  control_plane_count = var.ha_enabled ? 3 : 1

  # k3s deterministic bootstrap token derived from project ID + sovereign FQDN.
  # Workers join with this; k3s rotates it after first join.
  k3s_token = sha256("${var.hcloud_project_id}/${var.sovereign_fqdn}/k3s-bootstrap")

  # ── Hetzner location → network-zone lookup ───────────────────────────────
  # Hetzner Network Subnets are zoned (eu-central / us-east / us-west /
  # ap-southeast). The mapping is documented in the hcloud Terraform
  # provider's hcloud_network_subnet docs. Adding a new Hetzner location
  # means updating this lookup AND the var.region validation in
  # variables.tf in the same PR (so the multi-region for_each below cannot
  # land in a location whose zone we can't resolve).
  hetzner_network_zones = {
    fsn1 = "eu-central"
    nbg1 = "eu-central"
    hel1 = "eu-central"
    ash  = "us-east"
    hil  = "us-west"
    sin  = "ap-southeast"
  }

  # Legacy singular-region path's network zone — preserved as a separate
  # local so existing single-region applies (every Sovereign provisioned
  # before slice G1) keep producing identical plans. The multi-region
  # for_each path below uses the same lookup table per region entry.
  network_zone = lookup(local.hetzner_network_zones, var.region, "eu-central")

  # ── Multi-region overlay (slice G1, EPIC-0 #1095) ────────────────────────
  # Slice G1 wires every entry in var.regions[] end-to-end. The legacy
  # singular-region path (var.region + var.control_plane_size + var.worker_*
  # + count = local.control_plane_count) stays untouched so existing
  # Sovereign state (omantel, otech*) continues to plan-clean — those
  # resources keep their addresses (hcloud_server.control_plane[0],
  # hcloud_load_balancer.main, etc).
  #
  # New regions (regions[1+]) are realised via a parallel for_each set of
  # resources keyed by a deterministic per-region key. The legacy path
  # owns regions[0] semantically — when var.regions is non-empty,
  # provisioner.go writes regions[0]'s SKU/size into the singular fields
  # so the singular path drives regions[0] verbatim. Slice G1 layers in
  # the additional regions; slice G3 wires Cilium ClusterMesh between
  # them (out of scope here).
  #
  # Why a hybrid (singular path + for_each overlay) instead of a full
  # for_each refactor: a full refactor would change every existing
  # resource address from hcloud_server.control_plane[0] to
  # hcloud_server.control_plane["mgmt"], forcing every running Sovereign
  # to run `tofu state mv` for ~12 resources or face destructive
  # plan-time recreates. The brief explicitly bans that. Hybrid is
  # additive: secondary-region resources are NEW addresses that no
  # existing state carries, so legacy `tofu plan` outputs are unchanged
  # for any Sovereign whose request body has len(regions) ≤ 1.
  #
  # Key shape: "{cloudRegion}-{index}". Including the index protects
  # against same-region duplicates (e.g. fsn1 mgmt + fsn1 dataplane in
  # the same Sovereign — legal, even if uncommon). Index starts at 1
  # because regions[0] is owned by the singular path.
  secondary_regions = {
    for i, r in var.regions :
    "${r.cloudRegion}-${i}" => r
    if i > 0 && r.provider == "hetzner"
  }

  # Per-secondary-region subnet CIDR. The legacy singular subnet uses
  # 10.0.1.0/24 (cp at .2, workers at .10+). Secondary regions allocate
  # 10.0.<10+index>.0/24 so the slash-16 hcloud_network can hold up to
  # 245 secondary subnets without collision. Subnet allocation is
  # deterministic on the region's index in var.regions[] — re-running
  # `tofu apply` after an intra-list reorder would shift subnets, so
  # the catalyst-api MUST keep regions[] order stable across re-applies
  # of the same deployment (it does — the wizard's StepProvider emits
  # regions in the operator's selection order and provisioner.go never
  # reorders).
  secondary_region_subnets = {
    for k, r in local.secondary_regions :
    k => format("10.0.%d.0/24", 10 + index(keys(local.secondary_regions), k))
  }

  # Per-secondary-region Cilium ClusterMesh peer anchors (#1101 EPIC-6).
  # Auto-derive cluster.name as `<sovereign-stem>-<region-code-no-digits>`
  # (e.g. omantel + hel1 -> omantel-hel) when the operator left
  # var.cluster_mesh_name empty, OR honour the explicit override per
  # region (RegionSpec.cluster_mesh_name when present).  cluster.id is
  # derived as `var.cluster_mesh_id + 1 + index(secondary_regions, k)`
  # so the primary region keeps id=cluster_mesh_id and each peer claims
  # the next free id within the mesh — matching the registry convention
  # in docs/CLUSTERMESH-CLUSTER-IDS.md (mesh-omantel: fsn=1, hel=2, ...).
  # When var.cluster_mesh_name is empty AND there are no secondary
  # regions, the peer name remains empty (single-cluster Sovereign).
  # `coalesce` is intentionally NOT used here: it errors when every
  # argument is empty (e.g. tofu test mock with var.cluster_mesh_name="").
  # The conditional yields "" when both the per-region override AND the
  # umbrella var are empty (the not-in-mesh path).
  secondary_region_cluster_mesh_name = {
    for k, r in local.secondary_regions :
    k => try(r.clusterMeshName, "") != "" ? r.clusterMeshName : (
      var.cluster_mesh_name == "" ? "" : format(
        "%s-%s",
        split(".", var.sovereign_fqdn)[0],
        replace(r.cloudRegion, "/[0-9]+/", "")
      )
    )
  }
  secondary_region_cluster_mesh_id = {
    for k, _ in local.secondary_regions :
    k => var.cluster_mesh_id == 0 ? 0 : (
      var.cluster_mesh_id + 1 + index(keys(local.secondary_regions), k)
    )
  }

  # Per-secondary-region first-IP for control plane. Mirrors the legacy
  # singular path's "10.0.1.2" — first usable host of the subnet. Workers
  # in the same region count up from .10 within their own subnet.
  secondary_region_cp_ips = {
    for k, _ in local.secondary_regions :
    k => cidrhost(local.secondary_region_subnets[k], 2)
  }

  # GHCR pull token + the dockerconfigjson `auth` field, computed once here
  # so the cloud-init template stays a clean string-interpolation.
  #
  # The dockerconfigjson Secret format wants a top-level `auth` value of
  # base64("<username>:<token>"). Computing it inside the templatefile()
  # via OpenTofu's `base64encode()` would force the template to know about
  # OpenTofu functions; deriving it here keeps the template a pure heredoc
  # that emits valid YAML regardless of who renders it (production
  # provisioner, integration test harness, `tofu console`).
  #
  # `ghcr_pull_username` is the GHCR convention: the username is fixed for
  # token-based auth — GitHub validates the token, not the username. We use
  # `openova-bot` as a stable identity string so audit logs in CI / GHCR
  # pulls show a recognisable principal.
  ghcr_pull_username = "openova-bot"
  ghcr_pull_auth_b64 = base64encode("${local.ghcr_pull_username}:${var.ghcr_pull_token}")

  # Cloud-init for the control-plane node — installs k3s, then Flux, then
  # writes the Flux GitRepository + Kustomization that points at
  # clusters/<sovereign-fqdn>/ in the public OpenOva monorepo.
  # ── Hetzner Object Storage S3 endpoint (Phase 0b — issue #371) ──────────
  # Composed once here from the chosen region so the cloud-init template
  # and the Object Storage K8s Secret it writes both reference the same
  # canonical URL. Hetzner's public docs pin the format to
  # `https://<region>.your-objectstorage.com`. Per
  # docs/INVIOLABLE-PRINCIPLES.md #4 the URL is composed from the
  # operator's region choice, never hardcoded in cloudinit-control-plane.tftpl.
  object_storage_endpoint = "https://${var.object_storage_region}.your-objectstorage.com"

  # Worker cloud-init computed BEFORE the control-plane cloud-init so it
  # can be threaded into the CP template as `worker_cloud_init_b64`
  # (issue #921). The CP cloud-init writes the base64-encoded value
  # under the `hcloud-cloud-init` key of the canonical
  # `flux-system/cloud-credentials` Secret, which the bp-cluster-autoscaler-
  # hcloud HelmRelease lifts via Flux `valuesFrom` into
  # `clusterAutoscalerHcloud.cloudInit`. cluster-autoscaler 1.32.x's
  # Hetzner provider FATALs at startup ("`HCLOUD_CLUSTER_CONFIG` or
  # `HCLOUD_CLOUD_INIT` is not specified") without it. The autoscaler-
  # spawned worker uses the IDENTICAL bootstrap as Phase-0 workers so
  # k3s join token, control-plane address, hardening drop-ins are all
  # already aligned.
  worker_cloud_init = replace(templatefile("${path.module}/cloudinit-worker.tftpl", {
    sovereign_fqdn             = var.sovereign_fqdn
    k3s_version                = var.k3s_version
    k3s_token                  = local.k3s_token
    cp_private_ip              = "10.0.1.2" # First static IP in the subnet — control plane
    enable_unattended_upgrades = var.enable_unattended_upgrades
    enable_fail2ban            = var.enable_fail2ban
  }), "/(?m)^[ ]*#( |$).*\n/", "")

  # Strip ALL pure-comment lines (any indent) from the rendered cloud-init
  # before passing it to Hetzner (HARD 32 KiB user_data limit per the hcloud
  # API). The source template ships ~44 KB of documentation prose in comments
  # — explanatory text for future readers, not operationally meaningful at
  # boot. Comments live at indent-0/2 (template-level prose explaining the
  # tftpl itself) AND at indent-6+ INSIDE heredoc `content: |` blocks (YAML
  # comments inside flux-bootstrap.yaml, cloud-credentials-secret.yaml, etc.
  # — every write_files file we materialise is YAML, JSON, or a key=value
  # config; none are shell scripts). YAML/JSON/INI/conf parsers all ignore
  # `#`-prefixed comment lines so stripping them in Tofu loses nothing at
  # runtime. The regex `^[ ]*#( |$).*` matches a leading-whitespace `#`
  # followed by either a space (prose comment) OR end-of-line (separator
  # `#`). Lines whose `#` is followed by ANY OTHER char (e.g. `#!shebang`,
  # `#cloud-config` at line 1, `#pragma`) are NOT matched — they're operative.
  # Phase-8a-preflight bug #5 surfaced the 32 KiB cap initially; issue #966
  # raised it again on otech114 after #921 (cluster-autoscaler HCLOUD_CLOUD_INIT
  # b64) pushed rendered size past 36 KiB. The any-indent strip lands rendered
  # cloud-init at ~22 KB with ~10 KB of headroom for future additions.
  # Guardrail in this same module: see `validate_user_data_size` precondition
  # below — any future bloat that pushes user_data ≥ 30 KiB fails at plan-time.
  control_plane_cloud_init = replace(templatefile("${path.module}/cloudinit-control-plane.tftpl", {
    sovereign_fqdn          = var.sovereign_fqdn
    sovereign_subdomain     = var.sovereign_subdomain
    marketplace_enabled     = var.marketplace_enabled
    qa_fixtures_enabled       = var.qa_fixtures_enabled
    qa_test_session_enabled   = var.qa_test_session_enabled
    qa_fixtures_namespace     = var.qa_fixtures_namespace
    qa_organization           = var.qa_organization
    wildcard_cert_use_staging = var.wildcard_cert_use_staging
    cluster_mesh_name         = var.cluster_mesh_name
    cluster_mesh_id           = var.cluster_mesh_id

    # Multi-domain Sovereign (issue #827). When the wizard supplies an
    # explicit parent-domain list, use it verbatim. Otherwise default to a
    # single-zone array derived from sovereign_fqdn so legacy single-zone
    # provisioning paths render an identical Helm values shape (one zone,
    # one wildcard cert) — no special-casing in the chart templates.
    parent_domains_yaml = coalesce(
      var.parent_domains_yaml,
      format("[{name: \"%s\", role: \"primary\"}]", var.sovereign_fqdn)
    )
    org_name                   = var.org_name
    org_email                  = var.org_email
    region                     = var.region
    ha_enabled                 = var.ha_enabled
    worker_count               = var.worker_count
    k3s_version                = var.k3s_version
    k3s_token                  = local.k3s_token
    gitops_repo_url            = var.gitops_repo_url
    gitops_branch              = var.gitops_branch
    enable_unattended_upgrades = var.enable_unattended_upgrades
    enable_fail2ban            = var.enable_fail2ban
    ghcr_pull_username         = local.ghcr_pull_username
    ghcr_pull_token            = var.ghcr_pull_token
    ghcr_pull_auth_b64         = local.ghcr_pull_auth_b64

    # Object Storage credentials — interpolated into the Sovereign's
    # `object-storage` K8s Secret at cloud-init time so Harbor (#383)
    # and Velero (#384) HelmReleases find the credentials in the cluster
    # from Phase 1 onwards. Same pattern as ghcr_pull_token: never in
    # git, only in the encrypted per-deployment OpenTofu workdir + the
    # Sovereign's user_data, wiped on `tofu destroy`. Per #425 the K8s
    # Secret name is vendor-agnostic (`flux-system/object-storage`) —
    # no `hetzner-` prefix — so a future AWS / Azure / GCP / OCI
    # Sovereign reuses every existing chart without rename.
    object_storage_endpoint    = local.object_storage_endpoint
    object_storage_region      = var.object_storage_region
    object_storage_bucket_name = var.object_storage_bucket_name
    object_storage_access_key  = var.object_storage_access_key
    object_storage_secret_key  = var.object_storage_secret_key

    # OpenTofu→Crossplane handover (issue #425). The Hetzner Cloud API
    # token is interpolated into both the `flux-system/cloud-credentials`
    # K8s Secret AND the cloud-init's runcmd that applies the matching
    # Crossplane Provider+ProviderConfig. Once Crossplane core comes up
    # (via bp-crossplane) the Provider transitions Healthy=True and the
    # Sovereign is ready to accept Day-2 XRC writes — at which point
    # the catalyst-api's bespoke Hetzner-API hatching is retired in
    # favour of XRC writes per ADR-0001 §11.3 + INVIOLABLE-PRINCIPLES #3.
    hcloud_token = var.hcloud_token

    # Dynadot credentials — injected into cert-manager/dynadot-api-credentials
    # K8s Secret at cloud-init time so the bp-cert-manager-dynadot-webhook Pod
    # can start without a manual secret-creation step (issue #550 root-cause fix).
    # dynadot_managed_domains defaults to the parent zone of sovereign_fqdn when
    # the caller leaves it blank — e.g. "omani.works" for "console.otech22.omani.works".
    dynadot_key             = var.dynadot_key
    dynadot_secret          = var.dynadot_secret
    dynadot_managed_domains = coalesce(var.dynadot_managed_domains, join(".", slice(split(".", var.sovereign_fqdn), 1, length(split(".", var.sovereign_fqdn)))))

    # Cloud-init kubeconfig postback (issue #183, Option D). When
    # all three are non-empty, the template renders a runcmd that
    # rewrites k3s.yaml's 127.0.0.1:6443 to the LB's public IPv4
    # and PUTs the result to the catalyst-api with a Bearer header.
    # When any is empty (legacy out-of-band fetch path), the runcmd
    # is omitted entirely.
    #
    # load_balancer_ipv4 is interpolated from the hcloud_load_balancer
    # resource at apply time. Referencing it here implicitly forces
    # the LB to be created before the control-plane server boots —
    # which is exactly the ordering we want, because the new
    # Sovereign's curl PUT to catalyst-api needs to come from a
    # source IP the firewall accepts (any 0.0.0.0/0 → 443 outbound)
    # and arrive on a kubeconfig whose `server:` field is a
    # public-routable address.
    # Harbor pull-through mirror token (issue #557, Option A).
    # Passed into registries.yaml written at cloud-init time so containerd
    # authenticates against harbor.openova.io proxy-cache projects.
    harbor_robot_token = var.harbor_robot_token

    # Contabo PowerDNS API key (PR #686, F3 followup). Interpolated into
    # the Sovereign's cert-manager/powerdns-api-credentials Secret so
    # bp-cert-manager-powerdns-webhook can write DNS-01 challenge TXT
    # records to contabo's authoritative omani.works zone.
    powerdns_api_key = var.powerdns_api_key

    # PDM (Pool Domain Manager) basic-auth credentials (issue #879 Bug 2).
    # Interpolated into the Sovereign's `flux-system/pdm-basicauth` Secret
    # at cloud-init time so catalyst-api in catalyst-system can call PDM
    # at https://pool.openova.io with `Authorization: Basic …` for the
    # Day-2 multi-domain "Add another parent domain" flow. Reflector
    # auto-mirrors the Secret into `catalyst-system` (same canonical
    # pattern flux-system/ghcr-pull and flux-system/harbor-robot-token
    # already use). Sensitive — never logged, never committed.
    pdm_basic_auth_user = var.pdm_basic_auth_user
    pdm_basic_auth_pass = var.pdm_basic_auth_pass

    deployment_id           = var.deployment_id
    kubeconfig_bearer_token = var.kubeconfig_bearer_token
    catalyst_api_url        = var.catalyst_api_url
    handover_jwt_public_key = var.handover_jwt_public_key
    load_balancer_ipv4      = hcloud_load_balancer.main.ipv4
    # control_plane_ipv4 is NOT templated — it would create a dependency cycle
    # (cloud-init → control_plane.ipv4_address → control_plane.user_data → cloud-init).
    # The cloud-init runs ON the CP node, so it resolves its own public IP at boot
    # via Hetzner metadata service (169.254.169.254) — see cloudinit-control-plane.tftpl.

    # Issue #921 — base64-encoded worker cloud-init for the bp-cluster-
    # autoscaler-hcloud HelmRelease's HCLOUD_CLOUD_INIT env var. Same
    # bootstrap content the Phase-0 workers receive, so autoscaler-spawned
    # workers join the cluster identically.
    worker_cloud_init_b64 = base64encode(local.worker_cloud_init)
  }), "/(?m)^[ ]*#( |$).*\n/", "")
}

resource "hcloud_server" "control_plane" {
  count        = local.control_plane_count
  name         = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-cp${count.index + 1}"
  image        = "ubuntu-24.04"
  server_type  = var.control_plane_size
  location     = var.region
  ssh_keys     = [hcloud_ssh_key.main.id]
  firewall_ids = [hcloud_firewall.main.id]
  user_data    = local.control_plane_cloud_init

  network {
    network_id = hcloud_network.main.id
    ip         = "10.0.1.${count.index + 2}" # cp1=10.0.1.2, cp2=10.0.1.3, cp3=10.0.1.4
  }

  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
    "catalyst.openova.io/role"      = "control-plane"
  }

  # Issue #966 — Hetzner Cloud HARD limit on user_data is 32768 bytes.
  # Fail at plan-time (not at apply-time after the network/LB/firewall are
  # already created) if the rendered cloud-init exceeds 30720 bytes (30 KiB
  # = 32 KiB minus 10% future-additions buffer). Diagnosed live on otech114
  # deployment 5c3eea37d3aacda6 where #921's HCLOUD_CLOUD_INIT b64 + #827
  # multi-domain + earlier accumulation pushed rendered size to ~37 KB,
  # causing `tofu apply` to FATAL with `invalid input in field 'user_data'
  # [Length must be between 0 and 32768]` AFTER 30+ seconds of partial
  # provisioning. The any-indent comment-strip in `local.control_plane_cloud_init`
  # lands rendered size at ~22 KB; the 30 KiB precondition guards against
  # future bloat-creep silently re-eating that headroom.
  lifecycle {
    precondition {
      condition     = length(local.control_plane_cloud_init) <= 30720
      error_message = "Rendered control-plane cloud-init is ${length(local.control_plane_cloud_init)} bytes, exceeds 30720 (30 KiB) guardrail (Hetzner hard cap is 32768). Cull comments / move bloat out of cloudinit-control-plane.tftpl. See issue #966."
    }
    precondition {
      condition     = length(local.worker_cloud_init) <= 30720
      error_message = "Rendered worker cloud-init is ${length(local.worker_cloud_init)} bytes, exceeds 30720 (30 KiB) guardrail (Hetzner hard cap is 32768). Cull comments / move bloat out of cloudinit-worker.tftpl. See issue #966."
    }
  }

  depends_on = [hcloud_network_subnet.main]
}

# ── Workers: variable count ───────────────────────────────────────────────

resource "hcloud_server" "worker" {
  count        = var.worker_count
  name         = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-w${count.index + 1}"
  image        = "ubuntu-24.04"
  server_type  = var.worker_size
  location     = var.region
  ssh_keys     = [hcloud_ssh_key.main.id]
  firewall_ids = [hcloud_firewall.main.id]
  user_data    = local.worker_cloud_init

  network {
    network_id = hcloud_network.main.id
    ip         = "10.0.1.${count.index + 10}" # workers start at .10
  }

  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
    "catalyst.openova.io/role"      = "worker"
  }

  # Issue #966 — same 32 KiB user_data hard cap applies to workers. The
  # precondition on hcloud_server.control_plane already guards both rendered
  # cloud-inits, but mirror it here so a worker-only future change can't
  # bypass the gate by editing only cloudinit-worker.tftpl.
  lifecycle {
    precondition {
      condition     = length(local.worker_cloud_init) <= 30720
      error_message = "Rendered worker cloud-init is ${length(local.worker_cloud_init)} bytes, exceeds 30720 (30 KiB) guardrail (Hetzner hard cap is 32768). Cull comments / move bloat out of cloudinit-worker.tftpl. See issue #966."
    }
  }

  depends_on = [hcloud_server.control_plane]
}

# ── Load balancer: lb11, 80/443 → control plane NodePorts 31080/31443 ─────

resource "hcloud_load_balancer" "main" {
  name               = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-lb"
  load_balancer_type = "lb11"
  location           = var.region
  algorithm {
    type = "round_robin"
  }
  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
  }
}

resource "hcloud_load_balancer_network" "main" {
  load_balancer_id = hcloud_load_balancer.main.id
  network_id       = hcloud_network.main.id
}

resource "hcloud_load_balancer_target" "control_plane" {
  count            = local.control_plane_count
  type             = "server"
  load_balancer_id = hcloud_load_balancer.main.id
  server_id        = hcloud_server.control_plane[count.index].id
  use_private_ip   = true

  depends_on = [hcloud_load_balancer_network.main]
}

# ── LB targets: workers ────────────────────────────────────────────────
# Cilium Gateway runs as a DaemonSet on every node
# (clusters/_template/sovereign-tls/cilium-gateway.yaml), so any node can
# serve ingress traffic on its NodePort. Adding workers as LB targets
# gives the Hetzner LB N+1 healthy endpoints (1 CP + N workers) for the
# public 80/443/53 services — node failure on any single node no longer
# breaks the front door, and inbound traffic is round-robin'd across
# every node for genuine horizontal scale (issue #733). use_private_ip
# routes through the 10.0.1.0/24 subnet; the worker's public IP is not
# used for this path. Worker count > 0 means at least one extra LB
# endpoint; worker_count=0 (solo dev/POC) leaves only the CP target.
resource "hcloud_load_balancer_target" "workers" {
  count            = var.worker_count
  type             = "server"
  load_balancer_id = hcloud_load_balancer.main.id
  server_id        = hcloud_server.worker[count.index].id
  use_private_ip   = true

  depends_on = [
    hcloud_load_balancer_network.main,
    hcloud_server.worker,
  ]
}

resource "hcloud_load_balancer_service" "http" {
  load_balancer_id = hcloud_load_balancer.main.id
  protocol         = "tcp"
  listen_port      = 80
  # destination_port=30080 — Cilium Gateway listens on a high port
  # (clusters/_template/sovereign-tls/cilium-gateway.yaml) because even
  # with hostNetwork=true + privileged=true + NET_BIND_SERVICE +
  # envoy-keep-cap-netbindservice=true, cilium-envoy still gets
  # "Permission denied" binding 0.0.0.0:80 on the host. The bind is
  # intercepted by cilium-agent's BPF socket-LB program in a way that
  # is not resolvable via container caps. High ports work without
  # privileged binding (verified on otech47 after iterating through
  # the privileged-bind chain). Hetzner LB translates the public 80→
  # node:30080 so the operator-facing URL stays `http://console.<fqdn>/`.
  destination_port = 30080
}

resource "hcloud_load_balancer_service" "https" {
  load_balancer_id = hcloud_load_balancer.main.id
  protocol         = "tcp"
  listen_port      = 443
  # destination_port=30443 — see http service comment above. The
  # cilium-gateway HTTPS listener binds 30443 (not 443) because
  # privileged-port bind through cilium-agent's BPF intercept fails
  # regardless of capability configuration. HCLB does the listener-side
  # port translation so external users still hit `https://console.<fqdn>/`.
  destination_port = 30443
}

resource "hcloud_load_balancer_service" "dns" {
  load_balancer_id = hcloud_load_balancer.main.id
  protocol         = "tcp"
  listen_port      = 53
  # NodePort 30053 — the powerdns Service exposes DNS on this NodePort via
  # the anycast-endpoint ServiceType=NodePort overlay in 11-powerdns.yaml.
  # lb11 supports TCP only; UDP :53 is handled via the Hetzner Firewall
  # opening UDP/53 directly to the node's public IP (k3s NodePort handles
  # UDP natively via iptables DNAT). The LB TCP path handles zone transfers
  # and ACME challenge TXT queries; UDP is used for regular resolution.
  destination_port = 30053
  health_check {
    protocol = "tcp"
    port     = 30053
    interval = 15
    timeout  = 10
    retries  = 3
  }
}

# ── DNS: deliberately NOT a tofu concern ──────────────────────────────────
#
# Per the PDM (pool-domain-manager) ownership boundary set at #168, ALL
# Dynadot writes for managed pool subdomains flow through the central
# pool-domain-manager service. The lifecycle is:
#
#   1. catalyst-api receives POST /v1/deployments. Before launching
#      `tofu apply`, it calls PDM /reserve to put the subdomain on hold
#      with a TTL. (See deployments.go:127.)
#   2. `tofu apply` runs THIS module — provisioning Hetzner network,
#      firewall, server, load balancer. NO DNS writes here.
#   3. catalyst-api reads the LB IP from the tofu outputs and calls PDM
#      /commit (deployments.go:247). PDM writes the canonical record set
#      via the Dynadot API.
#   4. On any tofu failure, catalyst-api calls PDM /release so the
#      subdomain returns to the available pool.
#
# A previous revision of this module also wrote DNS via a `null_resource`
# with a `local-exec` provisioner shelling out to `/usr/local/bin/catalyst-dns`.
# That created a dual-ownership pattern — both tofu AND PDM writing
# Dynadot — which (a) duplicated work, (b) put credentials in two places,
# and (c) failed on every Launch with an opaque "Invalid field in API
# request" Dynadot error. The null_resource was removed in this commit;
# DNS is now a single-owner concern (PDM) end-to-end.
#
# BYO Sovereigns continue to own their own DNS — the customer points their
# CNAME at the LB IP shown on the success screen.

# ── Hetzner Object Storage bucket (Phase 0b — issue #371) ─────────────────
#
# This is the Sovereign's S3 bucket for Velero (cluster-state backup) and
# Harbor (container-image registry storage). Both Blueprints consume the
# `flux-system/object-storage` K8s Secret cloud-init writes into the Sovereign
# the bucket itself MUST exist before those Blueprints reconcile their first
# HelmRelease, otherwise their startup probes fail with NoSuchBucket and
# Phase 1 stalls.
#
# Per docs/INVIOLABLE-PRINCIPLES.md #3, day-2 cloud resource mutation is
# Crossplane's job. THIS resource is Phase 0 — created exactly once at
# Sovereign provisioning time, never mutated afterwards. If a Sovereign
# operator wants to add a second bucket post-handover (for an analytics
# product, for example), that is a Crossplane-managed XR/XRC, not a
# rerun of this OpenTofu module.
#
# The aminueza/minio provider's `minio_s3_bucket` resource is idempotent:
# applying twice against the same name returns the existing bucket without
# error. This is critical because:
#   - re-running `tofu apply` (e.g. operator changed worker count) must
#     not bounce off the bucket with AlreadyExists
#   - the wipe + re-provision flow (issue #318) destroys the Sovereign
#     servers but does NOT destroy the bucket — Velero backup data must
#     survive a control-plane reinstall
#
# We deliberately do NOT set `force_destroy = true`: a `tofu destroy` of
# this module must NOT take the Velero archive with it. The operator
# performs explicit bucket deletion via the Hetzner Console as a
# separate, auditable step when a Sovereign is decommissioned.
resource "minio_s3_bucket" "main" {
  bucket = var.object_storage_bucket_name
  acl    = "private"

  # No `force_destroy` — see comment block above.

  # Object lock disabled: Velero relies on standard S3 versioning + the
  # operator's retention policy, not on WORM semantics. Harbor stores
  # immutable image layers but doesn't require object lock — the layer
  # content-addressed digest IS the immutability guarantee.
  object_locking = false
}

# ── Multi-region overlay (slice G1, EPIC-0 #1095) ─────────────────────────
#
# Realises every var.regions[1+] entry as a parallel set of Hetzner
# resources keyed off local.secondary_regions. Slice G3 wires Cilium
# ClusterMesh between these and the primary region; this slice only
# provisions the cloud substrate so the network + compute exist for G3
# to connect.
#
# Architectural decision (slice G1): hybrid singular-path-plus-overlay,
# NOT a wholesale refactor of every existing resource into a `for_each`.
# The hybrid is purely additive — every new resource address below
# (`hcloud_network_subnet.secondary`, `hcloud_server.secondary_control_plane`,
# `hcloud_load_balancer.secondary`, …) is keyed on `for_each =
# local.secondary_regions` and therefore shares NO address space with
# the legacy singular resources above. Existing Sovereign state that
# was provisioned with var.regions = [] or len(var.regions) == 1
# carries no entries in `local.secondary_regions` (the iteration filter
# `if i > 0` excludes regions[0]; regions[0] is owned by the singular
# path) and thus produces a no-op plan diff for the entire overlay
# block. No `tofu state mv` runbook is required for any pre-G1 state.
#
# When a new Sovereign request body carries len(regions) ≥ 2 (the
# EPIC-6 mgmt + fsn + hel shape per docs/EPICS-1-6-unified-design.md
# §3.8 + §11), the for_each fires and creates one network subnet, one
# CP server, N worker servers, and one LB per secondary region — all
# wrapped under the same hcloud_network.main and hcloud_firewall.main
# the primary region uses (Hetzner Networks span multiple network
# zones; one Network + one Firewall per Sovereign is the canonical
# tenant boundary).

# Per-secondary-region subnet — separate /24 inside the shared /16
# hcloud_network.main. Sub-zoning is allocated deterministically off
# the region's index in var.regions[] (see local.secondary_region_subnets
# above). Subnets in different network_zones inside the same Network
# are supported by Hetzner; cross-zone routing is what Cilium ClusterMesh
# (slice G3) consumes.
resource "hcloud_network_subnet" "secondary" {
  for_each = local.secondary_regions

  network_id   = hcloud_network.main.id
  type         = "cloud"
  network_zone = lookup(local.hetzner_network_zones, each.value.cloudRegion, "eu-central")
  ip_range     = local.secondary_region_subnets[each.key]
}

# Per-secondary-region cloud-init — same template as the primary CP,
# parameterised with the secondary region's LB IPv4 and CP private IP
# in its own subnet. Sovereign FQDN, Org name, GitOps repo, secrets,
# region label etc. are SHARED with the primary so the secondary CP
# bootstraps as another node in the same logical Sovereign.
#
# Note for slice G3: a future per-cluster GitOps path differentiation
# (clusters/<provider>-<region>-<bb>-<env>/ per docs/NAMING-CONVENTION.md
# §4.1) will require a per-region `cluster_name` template knob; that
# is intentionally NOT introduced in slice G1 to keep this slice purely
# additive. Today every secondary CP renders an identical Flux
# Kustomization pointed at clusters/<sovereign_fqdn>/ — functionally
# valid k3s clusters for the EPIC-6 #1101 cloud-substrate gate; G3
# refines path separation when ClusterMesh + per-cluster Flux land.
locals {
  secondary_region_cloud_init = {
    for k, r in local.secondary_regions :
    k => replace(templatefile("${path.module}/cloudinit-control-plane.tftpl", {
      sovereign_fqdn          = var.sovereign_fqdn
      sovereign_subdomain     = var.sovereign_subdomain
      marketplace_enabled     = var.marketplace_enabled
      qa_fixtures_enabled       = var.qa_fixtures_enabled
      qa_test_session_enabled   = var.qa_test_session_enabled
      qa_fixtures_namespace     = var.qa_fixtures_namespace
      qa_organization           = var.qa_organization
      wildcard_cert_use_staging = var.wildcard_cert_use_staging
      # Per-secondary-region ClusterMesh anchors. id is incremented per
      # peer index so each secondary region gets a unique slot in the
      # mesh registry; primary region keeps var.cluster_mesh_id.
      cluster_mesh_name = local.secondary_region_cluster_mesh_name[k]
      cluster_mesh_id   = local.secondary_region_cluster_mesh_id[k]
      parent_domains_yaml = coalesce(
        var.parent_domains_yaml,
        format("[{name: \"%s\", role: \"primary\"}]", var.sovereign_fqdn)
      )
      org_name                   = var.org_name
      org_email                  = var.org_email
      region                     = r.cloudRegion
      ha_enabled                 = false # secondary regions land single-CP in slice G1; G3 introduces per-region HA
      worker_count               = r.workerCount
      k3s_version                = var.k3s_version
      k3s_token                  = local.k3s_token
      gitops_repo_url            = var.gitops_repo_url
      gitops_branch              = var.gitops_branch
      enable_unattended_upgrades = var.enable_unattended_upgrades
      enable_fail2ban            = var.enable_fail2ban
      ghcr_pull_username         = local.ghcr_pull_username
      ghcr_pull_token            = var.ghcr_pull_token
      ghcr_pull_auth_b64         = local.ghcr_pull_auth_b64
      object_storage_endpoint    = local.object_storage_endpoint
      object_storage_region      = var.object_storage_region
      object_storage_bucket_name = var.object_storage_bucket_name
      object_storage_access_key  = var.object_storage_access_key
      object_storage_secret_key  = var.object_storage_secret_key
      hcloud_token               = var.hcloud_token
      dynadot_key                = var.dynadot_key
      dynadot_secret             = var.dynadot_secret
      dynadot_managed_domains    = coalesce(var.dynadot_managed_domains, join(".", slice(split(".", var.sovereign_fqdn), 1, length(split(".", var.sovereign_fqdn)))))
      harbor_robot_token         = var.harbor_robot_token
      powerdns_api_key           = var.powerdns_api_key
      pdm_basic_auth_user        = var.pdm_basic_auth_user
      pdm_basic_auth_pass        = var.pdm_basic_auth_pass
      deployment_id              = var.deployment_id
      kubeconfig_bearer_token    = var.kubeconfig_bearer_token
      catalyst_api_url           = var.catalyst_api_url
      handover_jwt_public_key    = var.handover_jwt_public_key
      load_balancer_ipv4         = hcloud_load_balancer.secondary[k].ipv4
      worker_cloud_init_b64      = base64encode(local.secondary_region_worker_cloud_init[k])
    }), "/(?m)^[ ]*#( |$).*\n/", "")
  }

  # Per-secondary-region worker cloud-init — joins the secondary region's
  # own k3s CP via the secondary subnet's first usable IP (cidrhost(.../24, 2)).
  # Phase-0 workers use this; cluster-autoscaler-spawned workers in the
  # secondary region use the b64 form threaded into the secondary CP's
  # cloudinit (HCLOUD_CLOUD_INIT env var on bp-cluster-autoscaler-hcloud,
  # per issue #921).
  secondary_region_worker_cloud_init = {
    for k, r in local.secondary_regions :
    k => replace(templatefile("${path.module}/cloudinit-worker.tftpl", {
      sovereign_fqdn             = var.sovereign_fqdn
      k3s_version                = var.k3s_version
      k3s_token                  = local.k3s_token
      cp_private_ip              = local.secondary_region_cp_ips[k]
      enable_unattended_upgrades = var.enable_unattended_upgrades
      enable_fail2ban            = var.enable_fail2ban
    }), "/(?m)^[ ]*#( |$).*\n/", "")
  }
}

# Per-secondary-region control-plane node — slice G1 lands single-CP per
# secondary region (HA in secondaries comes with G3 alongside ClusterMesh
# wiring). Each secondary CP shares the same hcloud_ssh_key.main and
# hcloud_firewall.main as the primary; only the network subnet, location,
# and cloud-init differ.
resource "hcloud_server" "secondary_control_plane" {
  for_each = local.secondary_regions

  name         = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-${each.key}-cp1"
  image        = "ubuntu-24.04"
  server_type  = each.value.controlPlaneSize
  location     = each.value.cloudRegion
  ssh_keys     = [hcloud_ssh_key.main.id]
  firewall_ids = [hcloud_firewall.main.id]
  user_data    = local.secondary_region_cloud_init[each.key]

  network {
    network_id = hcloud_network.main.id
    ip         = local.secondary_region_cp_ips[each.key]
  }

  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
    "catalyst.openova.io/role"      = "control-plane"
    "catalyst.openova.io/region"    = each.value.cloudRegion
    "catalyst.openova.io/region-id" = each.key
  }

  # Same 32 KiB user_data hard cap applies to secondary regions —
  # mirror the precondition from the primary CP (issue #966).
  lifecycle {
    precondition {
      condition     = length(local.secondary_region_cloud_init[each.key]) <= 30720
      error_message = "Rendered control-plane cloud-init for secondary region ${each.key} is ${length(local.secondary_region_cloud_init[each.key])} bytes, exceeds 30720 (30 KiB) guardrail (Hetzner hard cap is 32768)."
    }
    precondition {
      condition     = length(local.secondary_region_worker_cloud_init[each.key]) <= 30720
      error_message = "Rendered worker cloud-init for secondary region ${each.key} is ${length(local.secondary_region_worker_cloud_init[each.key])} bytes, exceeds 30720 (30 KiB) guardrail."
    }
  }

  depends_on = [hcloud_network_subnet.secondary]
}

# Per-secondary-region workers. Hetzner's `count` semantics inside a
# `for_each` map require flattening — we expand the (region, worker-index)
# product into a single map keyed on "{region-key}-w{index}".
locals {
  secondary_workers = {
    for pair in flatten([
      for k, r in local.secondary_regions : [
        for i in range(r.workerCount) : {
          key        = "${k}-w${i + 1}"
          region_key = k
          region     = r
          worker_idx = i
          private_ip = cidrhost(local.secondary_region_subnets[k], 10 + i)
        }
      ]
    ]) :
    pair.key => pair
  }
}

resource "hcloud_server" "secondary_worker" {
  for_each = local.secondary_workers

  name         = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-${each.value.region_key}-w${each.value.worker_idx + 1}"
  image        = "ubuntu-24.04"
  server_type  = each.value.region.workerSize
  location     = each.value.region.cloudRegion
  ssh_keys     = [hcloud_ssh_key.main.id]
  firewall_ids = [hcloud_firewall.main.id]
  user_data    = local.secondary_region_worker_cloud_init[each.value.region_key]

  network {
    network_id = hcloud_network.main.id
    ip         = each.value.private_ip
  }

  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
    "catalyst.openova.io/role"      = "worker"
    "catalyst.openova.io/region"    = each.value.region.cloudRegion
    "catalyst.openova.io/region-id" = each.value.region_key
  }

  lifecycle {
    precondition {
      condition     = length(local.secondary_region_worker_cloud_init[each.value.region_key]) <= 30720
      error_message = "Rendered worker cloud-init for secondary region ${each.value.region_key} is ${length(local.secondary_region_worker_cloud_init[each.value.region_key])} bytes, exceeds 30720 (30 KiB) guardrail."
    }
  }

  depends_on = [hcloud_server.secondary_control_plane]
}

# Per-secondary-region load balancer — separate lb11 in each region's
# location so traffic terminates region-locally. PowerDNS lua-records
# (`ifurlup` health probes per docs/MULTI-REGION-DNS.md) point a single
# FQDN at the union of LB IPs and steer per-client based on geographic
# proximity + liveness.
resource "hcloud_load_balancer" "secondary" {
  for_each = local.secondary_regions

  name               = "catalyst-${replace(var.sovereign_fqdn, ".", "-")}-${each.key}-lb"
  load_balancer_type = "lb11"
  location           = each.value.cloudRegion
  algorithm {
    type = "round_robin"
  }
  labels = {
    "catalyst.openova.io/sovereign" = var.sovereign_fqdn
    "catalyst.openova.io/region"    = each.value.cloudRegion
    "catalyst.openova.io/region-id" = each.key
  }
}

resource "hcloud_load_balancer_network" "secondary" {
  for_each = local.secondary_regions

  load_balancer_id = hcloud_load_balancer.secondary[each.key].id
  network_id       = hcloud_network.main.id
}

resource "hcloud_load_balancer_target" "secondary_control_plane" {
  for_each = local.secondary_regions

  type             = "server"
  load_balancer_id = hcloud_load_balancer.secondary[each.key].id
  server_id        = hcloud_server.secondary_control_plane[each.key].id
  use_private_ip   = true

  depends_on = [hcloud_load_balancer_network.secondary]
}

resource "hcloud_load_balancer_target" "secondary_workers" {
  for_each = local.secondary_workers

  type             = "server"
  load_balancer_id = hcloud_load_balancer.secondary[each.value.region_key].id
  server_id        = hcloud_server.secondary_worker[each.key].id
  use_private_ip   = true

  depends_on = [
    hcloud_load_balancer_network.secondary,
    hcloud_server.secondary_worker,
  ]
}

resource "hcloud_load_balancer_service" "secondary_http" {
  for_each = local.secondary_regions

  load_balancer_id = hcloud_load_balancer.secondary[each.key].id
  protocol         = "tcp"
  listen_port      = 80
  destination_port = 30080
}

resource "hcloud_load_balancer_service" "secondary_https" {
  for_each = local.secondary_regions

  load_balancer_id = hcloud_load_balancer.secondary[each.key].id
  protocol         = "tcp"
  listen_port      = 443
  destination_port = 30443
}

resource "hcloud_load_balancer_service" "secondary_dns" {
  for_each = local.secondary_regions

  load_balancer_id = hcloud_load_balancer.secondary[each.key].id
  protocol         = "tcp"
  listen_port      = 53
  destination_port = 30053
  health_check {
    protocol = "tcp"
    port     = 30053
    interval = 15
    timeout  = 10
    retries  = 3
  }
}
