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

  # Network zone derived from the Hetzner region — required by hcloud_network_subnet.
  network_zone = lookup({
    fsn1 = "eu-central"
    nbg1 = "eu-central"
    hel1 = "eu-central"
    ash  = "us-east"
    hil  = "us-west"
  }, var.region, "eu-central")

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
  ghcr_pull_username  = "openova-bot"
  ghcr_pull_auth_b64  = base64encode("${local.ghcr_pull_username}:${var.ghcr_pull_token}")

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

  # Strip indent-0 and indent-2 YAML-block comment lines from the rendered
  # cloud-init before passing it to Hetzner (32 KiB user_data limit per the
  # hcloud API). The source template ships ~16 KB of documentation prose in
  # comments — explanatory text for future readers, not operationally
  # meaningful at boot. Indent-4+ comments live INSIDE heredoc `content: |`
  # blocks (embedded shell scripts, kubeconfig fragments, etc.) and MUST
  # be preserved. The RE2 regex below matches lines whose first 0-2 chars
  # are spaces followed by `#` followed by anything-but-`!` (preserves
  # shebangs in case they ever appear at indent 0-2). Phase-8a-preflight
  # bug #5 surfaced the 32 KiB cap.
  control_plane_cloud_init = replace(templatefile("${path.module}/cloudinit-control-plane.tftpl", {
    sovereign_fqdn             = var.sovereign_fqdn
    sovereign_subdomain        = var.sovereign_subdomain
    marketplace_enabled        = var.marketplace_enabled

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
    hcloud_token               = var.hcloud_token

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
    harbor_robot_token      = var.harbor_robot_token

    # Contabo PowerDNS API key (PR #686, F3 followup). Interpolated into
    # the Sovereign's cert-manager/powerdns-api-credentials Secret so
    # bp-cert-manager-powerdns-webhook can write DNS-01 challenge TXT
    # records to contabo's authoritative omani.works zone.
    powerdns_api_key        = var.powerdns_api_key

    deployment_id           = var.deployment_id
    kubeconfig_bearer_token = var.kubeconfig_bearer_token
    catalyst_api_url        = var.catalyst_api_url
    handover_jwt_public_key = var.handover_jwt_public_key
    load_balancer_ipv4      = hcloud_load_balancer.main.ipv4
    # control_plane_ipv4 is NOT templated — it would create a dependency cycle
    # (cloud-init → control_plane.ipv4_address → control_plane.user_data → cloud-init).
    # The cloud-init runs ON the CP node, so it resolves its own public IP at boot
    # via Hetzner metadata service (169.254.169.254) — see cloudinit-control-plane.tftpl.
  }), "/(?m)^[ ]{0,2}# .*\n/", "")

  worker_cloud_init = replace(templatefile("${path.module}/cloudinit-worker.tftpl", {
    sovereign_fqdn             = var.sovereign_fqdn
    k3s_version                = var.k3s_version
    k3s_token                  = local.k3s_token
    cp_private_ip              = "10.0.1.2" # First static IP in the subnet — control plane
    enable_unattended_upgrades = var.enable_unattended_upgrades
    enable_fail2ban            = var.enable_fail2ban
  }), "/(?m)^[ ]{0,2}# .*\n/", "")
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
