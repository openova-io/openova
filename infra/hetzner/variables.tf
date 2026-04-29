# All wizard inputs, as OpenTofu variables. The catalyst-api provisioner
# package writes these as tofu.auto.tfvars.json before running tofu apply.
#
# Per docs/INVIOLABLE-PRINCIPLES.md principle #4: nothing is hardcoded. Every
# value the wizard captures or the operator chose at provisioning time is a
# variable here. Defaults below describe the COMMON case (solo Sovereign on
# Hetzner) — see infra/hetzner/README.md for the rationale behind each default.

# ── Identity ──────────────────────────────────────────────────────────────

variable "sovereign_fqdn" {
  type        = string
  description = "Fully-qualified domain for this Sovereign — e.g. omantel.omani.works"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]*(\\.[a-z][a-z0-9-]*)+$", var.sovereign_fqdn))
    error_message = "Sovereign FQDN must be a valid lowercase domain (RFC 1035)."
  }
}

variable "sovereign_subdomain" {
  type        = string
  description = "Subdomain portion when domain_mode=pool — e.g. 'omantel' for omantel.omani.works. Empty when BYO."
  default     = ""
}

variable "org_name" {
  type        = string
  description = "Organisation name for resource labels + initial sovereign-admin Org name"
}

variable "org_email" {
  type        = string
  description = "Initial sovereign-admin email — becomes the first user in Keycloak's catalyst-admin realm"
  validation {
    condition     = can(regex("^[^@]+@[^@]+\\.[^@]+$", var.org_email))
    error_message = "Email must be a syntactically valid address."
  }
}

# ── Hetzner ───────────────────────────────────────────────────────────────

variable "hcloud_token" {
  type        = string
  description = "Hetzner Cloud API token (read+write). Never logged. Never committed to git."
  sensitive   = true
}

variable "hcloud_project_id" {
  type        = string
  description = "Hetzner project ID for resource attribution + audit log"
}

variable "region" {
  type        = string
  description = "Hetzner location (region). Runtime parameter — never hardcoded."
  validation {
    # Authoritative list of Hetzner Cloud locations as of 2026-04-28.
    # Update when Hetzner adds a new location AND the operator wants to
    # provision there. The local.network_zone lookup in main.tf must be
    # updated in the same PR.
    condition     = contains(["fsn1", "nbg1", "hel1", "ash", "hil"], var.region)
    error_message = "Region must be a valid Hetzner location: fsn1 (Falkenstein), nbg1 (Nuremberg), hel1 (Helsinki), ash (Ashburn), hil (Hillsboro)."
  }
}

# ── Topology ──────────────────────────────────────────────────────────────

variable "control_plane_size" {
  type        = string
  description = <<-EOT
    Hetzner server type for the control plane node.

    Default cx42 (16 GB / 8 vCPU) is the SMALLEST viable size for a solo
    Sovereign per docs/PLATFORM-TECH-STACK.md §7.1: ~11.3 GB Catalyst
    control-plane RAM + ~8.8 GB per-host-cluster overhead = ~20 GB
    minimum. cx32 (8 GB) is INSUFFICIENT and will OOM during the bootstrap
    kit install. See infra/hetzner/README.md §"Sizing rationale" for the
    full breakdown and the upgrade path to cax41/ccx33 for production.
  EOT
  default     = "cx42"
  validation {
    # Accepted families per Hetzner Cloud (https://www.hetzner.com/cloud/):
    #   cx*   — shared-vCPU Intel
    #   cpx*  — shared-vCPU AMD (the wizard's recommended CPX32 is here)
    #   ccx*  — dedicated-vCPU Intel
    #   cax*  — Ampere Arm
    # Earlier rule omitted the CPX family entirely, which rejected the
    # wizard's default selection at plan-time before the operator could
    # ever provision.
    condition     = can(regex("^(cx[0-9]+|cpx[0-9]+|ccx[0-9]+|cax[0-9]+)$", var.control_plane_size))
    error_message = "control_plane_size must match Hetzner server-type naming (cxNN | cpxNN | ccxNN | caxNN). Minimum recommended: cpx32 (8 GB AMD) or cx42 (16 GB Intel) for solo Sovereign."
  }
}

variable "worker_size" {
  type        = string
  description = <<-EOT
    Hetzner server type for worker nodes.

    Default cx32 (8 GB / 4 vCPU). Workers run only application Blueprints
    and per-host-cluster infra (~8.8 GB nominal, but per-host overhead
    is amortised across nodes once you have 3+ workers). Solo Sovereigns
    use worker_count=0 and run all workloads on the control plane —
    in that mode this variable is unused.
  EOT
  default     = "cx32"
  validation {
    # Empty string is valid — solo Sovereigns set worker_count = 0 and
    # never read worker_size; the wizard surfaces the empty-SKU state as
    # "no workers" in the review screen. Non-empty values must match the
    # same Hetzner server-type families control_plane_size accepts.
    condition     = var.worker_size == "" || can(regex("^(cx[0-9]+|cpx[0-9]+|ccx[0-9]+|cax[0-9]+)$", var.worker_size))
    error_message = "worker_size must be empty (solo Sovereign, worker_count=0) or match Hetzner server-type naming (cxNN | cpxNN | ccxNN | caxNN)."
  }
}

variable "worker_count" {
  type        = number
  description = "Number of worker nodes. 0 = single-node solo Sovereign (control plane handles all workloads)."
  default     = 0
  validation {
    condition     = var.worker_count >= 0 && var.worker_count <= 50
    error_message = "Worker count must be between 0 and 50."
  }
}

variable "ha_enabled" {
  type        = bool
  description = "When true, provisions 3 control-plane nodes for HA. When false, single control-plane node."
  default     = false
}

# ── Per-region SKU payload ────────────────────────────────────────────────
#
# The wizard captures sizing per-region (each region has its own provider,
# its own cloud-region, and its own control-plane + worker SKUs). The
# canonical request shape carries one entry per topology slot via this
# variable; the legacy singular control_plane_size / worker_size /
# worker_count above mirror regions[0] for the single-region apply path
# main.tf currently drives.
#
# Multi-region tofu wiring is structural-correct (variables.tf accepts the
# list, the catalyst-api provisioner emits it to tofu.auto.tfvars.json),
# but only regions[0] is end-to-end exercised today against a real Hetzner
# project. The for_each iteration that activates the rest will replace
# main.tf's single-server hcloud_server resources with one per-region
# block — at that point this variable becomes the source of truth and the
# legacy singular fields drop out. The door is open structurally so that
# activation is a follow-up commit, not a redesign.
variable "regions" {
  type = list(object({
    provider         = string
    cloudRegion      = string
    controlPlaneSize = string
    workerSize       = string
    workerCount      = number
  }))
  description = <<-EOT
    Per-region SKU payload from the wizard's StepProvider. One entry per
    topology slot (plus 1 for AIR-GAP when enabled). SKU strings are the
    provider's NATIVE instance-type identifier (cx32, m6i.xlarge,
    Standard_D4s_v5, ...) — passed verbatim to that provider's API.

    When empty, main.tf falls back to the singular control_plane_size /
    worker_size / worker_count variables (the back-compat path used by
    handler/load_test.go and any pre-rework wizard payload).
  EOT
  default = []
  validation {
    condition = alltrue([
      for r in var.regions :
      contains(["hetzner", "huawei", "oci", "aws", "azure"], r.provider)
    ])
    error_message = "Each regions[].provider must be one of: hetzner, huawei, oci, aws, azure."
  }
}

# ── k3s ───────────────────────────────────────────────────────────────────

variable "k3s_version" {
  type        = string
  description = <<-EOT
    k3s release pinned for both control-plane and workers. Must match the
    INSTALL_K3S_VERSION format (e.g. v1.31.4+k3s1). Pinned so a Sovereign
    provisioned today and one provisioned next month land on the same
    Kubernetes minor — required for blueprint compatibility guarantees
    documented in docs/PLATFORM-TECH-STACK.md §8.1.
  EOT
  default     = "v1.31.4+k3s1"
  validation {
    condition     = can(regex("^v[0-9]+\\.[0-9]+\\.[0-9]+\\+k3s[0-9]+$", var.k3s_version))
    error_message = "k3s_version must match the INSTALL_K3S_VERSION format vMAJOR.MINOR.PATCH+k3sN (e.g. v1.31.4+k3s1)."
  }
}

# ── SSH ───────────────────────────────────────────────────────────────────

variable "ssh_public_key" {
  type        = string
  description = <<-EOT
    Public SSH key (OpenSSH format) attached to all servers for
    sovereign-admin break-glass access.

    The key MUST come from the operator's Hetzner project / SSO-linked
    identity — never auto-generated by this module. See
    infra/hetzner/README.md §"SSH key management" for why ephemeral keys
    are rejected (break-glass + audit-trail requirements).
  EOT
  validation {
    condition     = can(regex("^(ssh-rsa|ssh-ed25519|ecdsa-sha2-nistp256) ", var.ssh_public_key))
    error_message = "SSH public key must be in OpenSSH format starting with ssh-rsa, ssh-ed25519, or ecdsa-sha2-nistp256."
  }
}

# ── DNS ───────────────────────────────────────────────────────────────────

variable "domain_mode" {
  type        = string
  description = "How DNS is managed: 'pool' (Catalyst writes records via Dynadot), 'byo' (customer manages own DNS)"
  default     = "pool"
  validation {
    condition     = contains(["pool", "byo"], var.domain_mode)
    error_message = "Domain mode must be 'pool' or 'byo'."
  }
}

variable "pool_domain" {
  type        = string
  description = "Pool domain when domain_mode=pool — e.g. 'omani.works'"
  default     = ""
}

variable "dynadot_key" {
  type        = string
  description = "Dynadot API key (required when domain_mode=pool)"
  default     = ""
  sensitive   = true
}

variable "dynadot_secret" {
  type        = string
  description = "Dynadot API secret (required when domain_mode=pool)"
  default     = ""
  sensitive   = true
}

# ── GitOps source for Flux bootstrap ──────────────────────────────────────

variable "gitops_repo_url" {
  type        = string
  description = "Git URL Flux on the new cluster watches for clusters/<sovereign-fqdn>/. Defaults to public OpenOva monorepo."
  default     = "https://github.com/openova-io/openova"
}

variable "gitops_branch" {
  type        = string
  description = "Branch Flux watches"
  default     = "main"
}

# ── OS hardening ──────────────────────────────────────────────────────────

variable "ssh_allowed_cidrs" {
  type        = list(string)
  description = <<-EOT
    Source CIDRs allowed to reach SSH (port 22). Default empty list = SSH
    is NOT exposed at the firewall and break-glass requires an out-of-band
    path (Hetzner console / VNC). Operators tighten/widen this via
    Crossplane Composition once the cluster is up; the firewall rule below
    is the Phase 0 fallback only.
  EOT
  default     = []
  validation {
    condition     = alltrue([for c in var.ssh_allowed_cidrs : can(cidrnetmask(c))])
    error_message = "Each entry in ssh_allowed_cidrs must be a valid CIDR (e.g. 203.0.113.7/32)."
  }
}

variable "enable_unattended_upgrades" {
  type        = bool
  description = "Install + enable unattended-upgrades for security patches on Ubuntu. Default true; disable only for short-lived test sovereigns."
  default     = true
}

variable "enable_fail2ban" {
  type        = bool
  description = "Install + enable fail2ban with the sshd jail. Default true; disable only when an upstream WAF/IDS already covers the same surface."
  default     = true
}
