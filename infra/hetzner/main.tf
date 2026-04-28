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

# ── Firewall: 80/443 + 6443 + 22 (locked to operator IPs) + ICMP ─────────

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
  # SSH (22) is intentionally NOT opened by default. Operators add a sovereign-
  # specific source-CIDR rule via Crossplane Composition once the cluster is up.

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
    sovereign_fqdn      = var.sovereign_fqdn
    sovereign_subdomain = var.sovereign_subdomain
    org_name            = var.org_name
    org_email           = var.org_email
    region              = var.region
    ha_enabled          = var.ha_enabled
    worker_count        = var.worker_count
    k3s_token           = local.k3s_token
    gitops_repo_url     = var.gitops_repo_url
    gitops_branch       = var.gitops_branch
  })

  worker_cloud_init = templatefile("${path.module}/cloudinit-worker.tftpl", {
    sovereign_fqdn = var.sovereign_fqdn
    k3s_token      = local.k3s_token
    cp_private_ip  = "10.0.1.2" # First static IP in the subnet — control plane
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

# ── DNS: managed pool only (BYO Sovereigns: customer points own CNAME) ────
#
# When domain_mode=pool and dynadot creds provided, we shell out to a
# helper Go binary the catalyst-api ships (so the OpenTofu module doesn't
# embed the Dynadot HTTP client). The binary is `/usr/local/bin/catalyst-dns`
# inside the catalyst-api container; tofu invokes it via local-exec.
#
# Records written:
#   *.<subdomain>.<pool-domain>     A → load balancer IP
#   console.<subdomain>.<pool-domain>  A → load balancer IP
#   gitea.<subdomain>.<pool-domain>    A → load balancer IP
#   harbor.<subdomain>.<pool-domain>   A → load balancer IP
#   admin.<subdomain>.<pool-domain>    A → load balancer IP
#   api.<subdomain>.<pool-domain>      A → load balancer IP

resource "null_resource" "dns_pool" {
  count = var.domain_mode == "pool" ? 1 : 0

  triggers = {
    lb_ip       = hcloud_load_balancer.main.ipv4
    sovereign   = var.sovereign_fqdn
    pool_domain = var.pool_domain
    subdomain   = var.sovereign_subdomain
  }

  provisioner "local-exec" {
    command = "/usr/local/bin/catalyst-dns"
    environment = {
      DYNADOT_API_KEY    = var.dynadot_key
      DYNADOT_API_SECRET = var.dynadot_secret
      DOMAIN             = var.pool_domain
      SUBDOMAIN          = var.sovereign_subdomain
      LB_IP              = hcloud_load_balancer.main.ipv4
    }
  }

  depends_on = [hcloud_load_balancer.main]
}
