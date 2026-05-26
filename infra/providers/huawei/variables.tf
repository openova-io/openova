# Catalyst Sovereign on Huawei Cloud (Stack) — Phase-0 OpenTofu variables.
#
# Honours the canonical cross-provider contract at
# infra/providers/PROVIDER-INTERFACE.md. Variable names mirror
# infra/providers/hetzner/variables.tf one-to-one for the shared
# inputs (sovereign_fqdn, deployment_id, regions, parent_domains_yaml,
# gitops_repo_url, gitops_branch, handover_jwt_public_key, ghcr_pull_token).
# Provider-specific knobs (huawei_access_key / huawei_secret_key /
# huawei_project_id / huawei_region / image_id / control-plane + worker
# flavors) follow.
#
# Per docs/PRINCIPLES.md #4 nothing is hardcoded — every value is a
# variable here; the catalyst-api provisioner writes tofu.auto.tfvars.json
# from the operator's wizard input.

# ── Cross-provider canonical inputs (PROVIDER-INTERFACE.md §1) ───────────

variable "deployment_id" {
  type        = string
  description = "catalyst-api opaque 16-hex deployment identifier. Stamped on every cloud resource (TMS tag `catalyst.openova.io/deployment-id`) so orphan-purge can scope its delete."
}

variable "sovereign_fqdn" {
  type        = string
  description = "Fully-qualified Sovereign domain (e.g. `omantel.omani.works`). Drives cloud-init DNS + LE cert issuance."
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]*(\\.[a-z][a-z0-9-]*)+$", var.sovereign_fqdn))
    error_message = "Sovereign FQDN must be a valid lowercase domain (RFC 1035)."
  }
}

variable "regions" {
  type = list(object({
    code               = string
    role               = string
    control_plane_size = string
    worker_size        = string
    worker_count       = number
  }))
  description = <<-EOT
    Per-region sizing + role assignment per PROVIDER-INTERFACE.md §1.
    Length ≥ 1. Index 0 = primary (runs Catalyst control plane + first
    CNPG cluster); indexes 1..N = secondaries (carry vClusters + CNPG
    ReplicaCluster mirrors).

    `code` is the operator-supplied fake-region tag (e.g. `me-east-215-a`,
    `me-east-215-b`). HCS exposes one physical region (me-east-215) so
    fake-regions are VPC-isolated overlays within the same physical
    region — the Sovereign multi-region DR contract is still satisfied
    because inter-region traffic flows over DMZ-WG on public EIPs.

    `control_plane_size` defaults at the Go provisioner layer to
    `s7n.large.4` (2 vCPU / 8 GB) and `worker_size` to `m7n.xlarge.8`
    (4 vCPU / 32 GB) per the operator-confirmed Tier-B sizing.
  EOT
  validation {
    condition     = length(var.regions) >= 1
    error_message = "Tier-B Huawei Sovereign requires at least 1 region (primary)."
  }
  validation {
    condition = alltrue([
      for r in var.regions :
      contains(["primary", "secondary"], r.role)
    ])
    error_message = "Each regions[].role must be 'primary' or 'secondary'."
  }
}

variable "parent_domains_yaml" {
  type        = string
  description = "Parent-domain list as a YAML inline-array literal. Each entry: `{name: <apex>, role: <primary|sme-pool>, ...}`. Empty = single-zone fallback derived from sovereign_fqdn."
  default     = ""
}

variable "gitops_repo_url" {
  type        = string
  description = "Git URL Flux on the new cluster watches for clusters/<sovereign-fqdn>/."
  default     = "https://github.com/openova-io/openova"
}

variable "gitops_branch" {
  type        = string
  description = "Branch Flux watches."
  default     = "main"
}

variable "handover_jwt_public_key" {
  type        = string
  description = "RFC 7517 JWK JSON of the Catalyst-Zero RS256 handover-JWT public key. Written to /var/lib/catalyst/handover-jwt-public.jwk on the new Sovereign by cloud-init."
  sensitive   = true
  default     = ""
}

# ── Huawei Cloud (Stack) credentials ─────────────────────────────────────

variable "huawei_access_key" {
  type        = string
  description = "Huawei Cloud IAM access key (AK). Operator-issued; never logged."
  sensitive   = true
}

variable "huawei_secret_key" {
  type        = string
  description = "Huawei Cloud IAM secret key (SK). Operator-issued; never logged."
  sensitive   = true
}

variable "huawei_project_id" {
  type        = string
  description = "Huawei project ID (region-scoped). Operator-supplied at wizard StepCredentials; NOT a module default."
}

variable "huawei_region" {
  type        = string
  description = "Huawei region code. Defaults to the operator-confirmed HCS region `me-east-215`."
  default     = "me-east-215"
}

variable "huawei_az" {
  type        = string
  description = "Availability zone within the region. HCS region me-east-215 exposes only `me-east-215a` (single-AZ); fake-regions overlay via VPC isolation."
  default     = "me-east-215a"
}

variable "huawei_insecure" {
  type        = bool
  description = "When true, skip TLS verify against the HCS endpoint (the on-prem HCS CA is not in the standard trust store). Public Huawei Cloud sets this false."
  default     = true
}

# ── Image + flavors (operator-confirmed live-API values) ─────────────────

variable "image_id" {
  type        = string
  description = <<-EOT
    Huawei IMS image ID. Default is the operator-confirmed live
    `Ubuntu 22.04 server 64bit (40 GB)` image at HCS me-east-215
    (queried via IMS API 2026-05-22T20:08Z, gold imagetype). Public
    Huawei Cloud or rotated HCS images override via wizard input.

    catalyst-api writes the resolved value into tofu.auto.tfvars.json
    at provision time so a stale baked-in default never blocks a fresh
    image rotation. The default below is the live ID — NOT a placeholder.

    Wave 5.5 (Refs #2140): the prior placeholder
    `ec509d3b-0000-0000-0000-000000000000` triggered ECS create failure
    `Ecs.0304 No image found with ID ec509d3b-0000-0000-0000-000000000000`
    on deployment 0bbf240540c9351b. Real UUID baked here.
  EOT
  default     = "ec509d3b-e2c5-40b8-987b-ce9623d67a88"
}

variable "default_control_plane_flavor" {
  type        = string
  description = "Default ECS flavor for control-plane nodes when regions[].control_plane_size is empty. `s7n.large.4` = 2 vCPU / 8 GB."
  default     = "s7n.large.4"
}

variable "default_worker_flavor" {
  type        = string
  description = "Default ECS flavor for worker nodes when regions[].worker_size is empty. `m7n.xlarge.8` = 4 vCPU / 32 GB."
  default     = "m7n.xlarge.8"
}

variable "huawei_control_plane_count" {
  type        = number
  description = <<-EOT
    Number of control-plane ECS instances per region. Wave 5.7 (Refs #2140)
    default is 1, sized for the HCS me-east-215 publicIp quota (10) — the
    earlier Hetzner-style fixed-3 design consumed 6 EIPs for a 2-region
    Tier-B Sovereign and left no headroom for mothership + a second
    Sovereign on the same tenancy (`eip:ListQuotas` confirmed
    quota=10 used=10 on the first Wave 5 apply attempts 2026-05-22T20:35Z).

    Set to 3 for HA control plane on a tenancy with sufficient EIP
    headroom (public Huawei Cloud or HCS POD enterprise tier).
  EOT
  default     = 1
  validation {
    condition     = var.huawei_control_plane_count == 1 || var.huawei_control_plane_count == 3
    error_message = "huawei_control_plane_count must be 1 (POC) or 3 (HA)."
  }
}

# ── SSH ──────────────────────────────────────────────────────────────────

variable "ssh_public_key" {
  type        = string
  description = "Public SSH key (OpenSSH format) attached to every ECS instance for break-glass access."
  validation {
    condition     = can(regex("^(ssh-rsa|ssh-ed25519|ecdsa-sha2-nistp256) ", var.ssh_public_key))
    error_message = "SSH public key must be in OpenSSH format starting with ssh-rsa, ssh-ed25519, or ecdsa-sha2-nistp256."
  }
}

variable "ssh_allowed_cidrs" {
  type        = list(string)
  description = "Source CIDRs allowed to reach SSH (port 22). Empty = SSH closed at the security group; break-glass via HCS console."
  default     = []
}

# ── Cloud-init handover postback + identity (mirrors Hetzner module) ──────

variable "org_name" {
  type        = string
  description = "Organisation name for resource tags + initial sovereign-admin Org name."
}

variable "org_email" {
  type        = string
  description = "Initial sovereign-admin email — becomes the first user in Keycloak's catalyst-admin realm."
  validation {
    condition     = can(regex("^[^@]+@[^@]+\\.[^@]+$", var.org_email))
    error_message = "Email must be a syntactically valid address."
  }
}

variable "k3s_version" {
  type        = string
  description = "k3s release pinned for CP + workers (INSTALL_K3S_VERSION format)."
  default     = "v1.31.4+k3s1"
  validation {
    condition     = can(regex("^v[0-9]+\\.[0-9]+\\.[0-9]+\\+k3s[0-9]+$", var.k3s_version))
    error_message = "k3s_version must match vMAJOR.MINOR.PATCH+k3sN."
  }
}

variable "ghcr_pull_token" {
  type        = string
  description = "GHCR pull token (PAT, scope packages:read on openova-io). Written to flux-system/ghcr-pull at cloud-init time."
  sensitive   = true
  default     = ""
}

variable "kubeconfig_bearer_token" {
  type        = string
  description = "32-byte bearer token the new Sovereign cloud-init attaches when PUTting back its kubeconfig (issue #183 Option D pattern)."
  sensitive   = true
  default     = ""
}

variable "catalyst_api_url" {
  type        = string
  description = "Public origin the new Sovereign's cloud-init PUTs its kubeconfig back to."
  default     = "https://console.openova.io/sovereign"
}

# ── OS hardening (mirrors Hetzner module) ────────────────────────────────

variable "enable_unattended_upgrades" {
  type        = bool
  description = "Install + enable unattended-upgrades. Default true; disable only for short-lived test sovereigns."
  default     = true
}

variable "enable_fail2ban" {
  type        = bool
  description = "Install + enable fail2ban with sshd jail."
  default     = true
}

# ── Wave 5.34 (Refs #2208): Sovereign-side Secret seeds ─────────────────
# Mirror Hetzner cloud-init pattern. Provisioner (provisioner.go) writes
# these into tofu.auto.tfvars.json regardless of provider; without these
# variable declarations the Huawei cloudinit-control-plane.tftpl could not
# reference ${powerdns_api_key} et al.

variable "harbor_robot_token" {
  type        = string
  sensitive   = true
  default     = ""
  description = <<-EOT
    Wave 5.118 (#2462): Harbor robot token for `robot$openova-bot` on
    harbor.openova.io. Seeded into the Sovereign's flux-system/harbor-
    robot-token Secret (mirrored to catalyst-system by bp-reflector) so
    catalyst-api's REQUIRED CATALYST_HARBOR_ROBOT_TOKEN secretKeyRef
    resolves. Without this, catalyst-api Pod stays in
    CreateContainerConfigError indefinitely → api.<sov> HTTP 503 → no
    sign-in PIN. Hetzner provider has the same variable (lines 793+).
  EOT
}

variable "powerdns_api_key" {
  type        = string
  sensitive   = true
  description = <<-EOT
    Contabo central PowerDNS API key. Seeded into the Sovereign's
    cert-manager/powerdns-api-credentials Secret so
    bp-cert-manager-powerdns-webhook can write DNS-01 challenge TXT
    records to contabo's authoritative omani.works zone. Empty = wildcard
    cert never issues (HTTPS listener stays Programmed=False).
  EOT
  default     = ""
}

variable "pdm_basic_auth_user" {
  type        = string
  sensitive   = true
  description = <<-EOT
    Pool-Domain-Manager basic-auth username. Seeded into the Sovereign's
    flux-system/pdm-basicauth Secret; Reflector mirrors to catalyst-system.
    catalyst-api mounts via secretKeyRef for Day-2 add-parent-domain calls.
  EOT
  default     = ""
}

variable "pdm_basic_auth_pass" {
  type        = string
  sensitive   = true
  description = "PDM basic-auth password. Paired with pdm_basic_auth_user."
  default     = ""
}

# ── OBS bucket (S3-protocol; mirrors Hetzner object-storage_* triplet) ───

variable "obs_bucket_name" {
  type        = string
  description = <<-EOT
    Huawei OBS bucket name. Deterministic per-Sovereign per-deployment
    name derived by catalyst-api as `catalyst-<fqdn-dashed>-<dep-id-prefix>`
    — same shape Hetzner Object Storage uses. The bucket is created
    idempotently via the hashicorp/aws S3 provider pointed at the HCS OBS
    endpoint.
  EOT
  validation {
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.obs_bucket_name))
    error_message = "OBS bucket name must be 3-63 chars, lowercase alphanumeric + hyphens, starting + ending with alphanumeric."
  }
}

# ── Debug SSH toggle (Wave 5.82 #2419, recovery-script unblock) ──────────
# Default `false`: the canonical Sovereign posture has NO port 22 ingress
# on the per-region security group (operator never needs SSH in steady
# state — k3s + Cilium handle everything inside the cluster, mothership
# observability covers Day-2). When a recovery flow needs SSH (Wave 5.75
# manual secondary-kubeconfig PUT-back via `scripts/hw01-recover-
# secondary-kubeconfig.sh`), the operator flips this to `true` + runs
# `tofu apply`, runs the script, then flips back + re-applies. The
# secgroup-rule lifecycle stays in tofu state — no manual Huawei console
# fiddling that would drift state.

variable "debug_ssh_enabled" {
  type        = bool
  default     = false
  description = <<-EOT
    When true, opens 22/tcp ingress on every region's security group
    from `debug_ssh_remote_cidr`. Default false (canonical posture).
    Flip true to enable recovery-script SSH; flip back after recovery.
  EOT
}

variable "debug_ssh_remote_cidr" {
  type        = string
  default     = "0.0.0.0/0"
  description = <<-EOT
    CIDR allowed to reach 22/tcp when `debug_ssh_enabled = true`.
    Default 0.0.0.0/0 (broad — relies on key-only SSH auth). Tighten
    to the bastion /32 in tfvars when running recovery from a known IP.
  EOT
}

# ── Marketplace toggle (Wave 5.88 #2432 — sovereign-tls Kustomization) ──
variable "marketplace_enabled" {
  type        = string
  default     = "true"
  description = <<-EOT
    Stringified bool ("true" or "false"). Threaded into sovereign-tls
    Kustomization's MARKETPLACE_ENABLED postBuild.substitute so
    bp-catalyst-platform's marketplace + tenant-wildcard HTTPRoutes
    render on a Sovereign that opts in. Defaults true per the canonical
    Sovereign posture.
  EOT
}

variable "wildcard_cert_use_staging" {
  type        = string
  default     = "false"
  description = <<-EOT
    Stringified bool. When "true" the sovereign-tls Kustomization's
    Certificate routes to LE staging (no 5/168h rate-limit per
    identifier-set; useful when iterating on the same Sovereign FQDN).
    Default "false" → real-trusted production cert.
  EOT
}
