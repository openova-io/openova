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

  # Cloud-init for the control-plane node — installs k3s, then Flux, then
  # writes the Flux GitRepository + Kustomization that points at
  # clusters/<sovereign-fqdn>/ in the public OpenOva monorepo.
  control_plane_cloud_init = templatefile("${path.module}/cloudinit-control-plane.tftpl", {
    sovereign_fqdn             = var.sovereign_fqdn
    sovereign_subdomain        = var.sovereign_subdomain
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
  })

  worker_cloud_init = templatefile("${path.module}/cloudinit-worker.tftpl", {
    sovereign_fqdn             = var.sovereign_fqdn
    k3s_version                = var.k3s_version
    k3s_token                  = local.k3s_token
    cp_private_ip              = "10.0.1.2" # First static IP in the subnet — control plane
    enable_unattended_upgrades = var.enable_unattended_upgrades
    enable_fail2ban            = var.enable_fail2ban
  })
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

resource "hcloud_load_balancer_service" "http" {
  load_balancer_id = hcloud_load_balancer.main.id
  protocol         = "tcp"
  listen_port      = 80
  destination_port = 31080 # Cilium Gateway will bind this NodePort post-bootstrap
}

resource "hcloud_load_balancer_service" "https" {
  load_balancer_id = hcloud_load_balancer.main.id
  protocol         = "tcp"
  listen_port      = 443
  destination_port = 31443
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
