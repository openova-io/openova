# All wizard inputs, as OpenTofu variables. The catalyst-api provisioner
# package writes these as tofu.auto.tfvars.json before running tofu apply.
#
# Per docs/INVIOLABLE-PRINCIPLES.md principle #4: nothing is hardcoded. Every
# value the wizard captures or the operator chose at provisioning time is a
# variable here.

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
    condition     = contains(["fsn1", "nbg1", "hel1", "ash", "hil"], var.region)
    error_message = "Region must be a valid Hetzner location: fsn1, nbg1, hel1, ash, hil."
  }
}

# ── Topology ──────────────────────────────────────────────────────────────

variable "control_plane_size" {
  type        = string
  description = "Hetzner server type for the control plane node — e.g. cx32, cx42"
  default     = "cx32"
}

variable "worker_size" {
  type        = string
  description = "Hetzner server type for worker nodes — e.g. cx32, cx42"
  default     = "cx32"
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

# ── SSH ───────────────────────────────────────────────────────────────────

variable "ssh_public_key" {
  type        = string
  description = "Public SSH key (OpenSSH format) attached to all servers for sovereign-admin break-glass access"
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
