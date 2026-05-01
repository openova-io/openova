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

# ── GHCR pull token ───────────────────────────────────────────────────────
#
# Long-lived GHCR token (GitHub PAT or fine-grained token, scope
# `packages:read` on `openova-io`) that the new Sovereign's Flux
# source-controller uses to pull the private bp-* OCI artifacts from
# `ghcr.io/openova-io/`. Cloud-init writes this into the
# flux-system/ghcr-pull Secret on the freshly-installed k3s control
# plane BEFORE applying the GitRepository + Kustomization that wires up
# clusters/<sovereign-fqdn>/.
#
# Without this, every HelmRepository CR in
# clusters/<sovereign-fqdn>/bootstrap-kit/ (each carrying
# `secretRef: name: ghcr-pull`) errors with:
#   failed to get authentication secret 'flux-system/ghcr-pull':
#     secrets "ghcr-pull" not found
# Phase 1 stalls at bp-cilium and the bootstrap kit never lands. The
# operator-applied workaround (kubectl apply the secret by hand) is not
# durable across reprovisioning of the same Sovereign.
#
# Source: catalyst-api Pod mounts this from the
# `catalyst-ghcr-pull-token` Kubernetes Secret in the catalyst namespace
# as the env var CATALYST_GHCR_PULL_TOKEN. Rotation policy + storage:
# docs/SECRET-ROTATION.md.
variable "ghcr_pull_token" {
  type        = string
  description = <<-EOT
    GHCR pull token (GitHub PAT or fine-grained token, scope `packages:read`
    on openova-io). Written to flux-system/ghcr-pull at cloud-init time so
    Flux source-controller can pull private bp-* OCI artifacts.

    Empty default exists so the OpenTofu module renders for BYO
    catalyst-api Pods that have not yet adopted the
    `catalyst-ghcr-pull-token` Secret; provisioner.Validate() in
    products/catalyst/bootstrap/api/internal/provisioner enforces
    non-empty for managed-pool deployments where Phase 1 absolutely
    needs the token. Sensitive — never logged, never committed to git.

    Rotation policy: yearly, stored in 1Password — see
    docs/SECRET-ROTATION.md.
  EOT
  sensitive   = true
  default     = ""
}

# ── Cloud-init kubeconfig postback (issue #183, Option D) ────────────────

variable "deployment_id" {
  type        = string
  description = <<-EOT
    catalyst-api's per-deployment 16-char hex identifier. Templated
    into the new Sovereign's cloud-init runcmd so the new control
    plane PUTs its rewritten kubeconfig to the correct deployment
    record:

      PUT $${var.catalyst_api_url}/api/v1/deployments/$${var.deployment_id}/kubeconfig

    Empty when the catalyst-api caller is using the legacy
    out-of-band kubeconfig fetch path; cloud-init then skips the PUT
    runcmd entirely.
  EOT
  default     = ""
}

variable "kubeconfig_bearer_token" {
  type        = string
  description = <<-EOT
    32-byte cryptographic-random bearer token the new Sovereign's
    cloud-init attaches as `Authorization: Bearer <token>` when
    PUTting back its kubeconfig (issue #183, Option D). Consumed
    once. The catalyst-api persists ONLY the SHA-256 hash on the
    deployment record; the plaintext lives in this tfvars file
    (file mode 0600 on the catalyst-api PVC) until `tofu destroy`
    removes the workdir.

    Empty when deployment_id is empty (legacy out-of-band fetch
    path); cloud-init then skips the PUT runcmd. Sensitive — never
    logged by OpenTofu, never committed to git.
  EOT
  sensitive   = true
  default     = ""
}

variable "catalyst_api_url" {
  type        = string
  description = <<-EOT
    Public origin the new Sovereign's cloud-init PUTs its kubeconfig
    back to. The full URL is

      $${var.catalyst_api_url}/api/v1/deployments/$${var.deployment_id}/kubeconfig

    Defaults to the OpenOva-hosted franchise console; air-gapped
    franchises override this with their own catalyst-api ingress
    via the CATALYST_API_PUBLIC_URL env var on the catalyst-api
    Pod. Per docs/INVIOLABLE-PRINCIPLES.md #4 this is runtime
    configuration, not code.
  EOT
  default     = "https://console.openova.io/sovereign"
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

# ── Hetzner Object Storage (Phase 0b — issue #371) ────────────────────────
#
# Hetzner Object Storage is the canonical S3 backing for Harbor (#383) and
# Velero (#384) on Hetzner Sovereigns per the omantel handover WBS §3 and
# the ADR-0001-derived "S3 vs SeaweedFS" rule (S3-aware apps write to the
# cloud-provider's native S3; only POSIX-only apps go through SeaweedFS as
# a buffer). For Hetzner that native S3 is Object Storage.
#
# Constraints baked into the rest of this module:
#   1. No native `hcloud_object_storage_*` Terraform resource exists today
#      (see versions.tf for the upstream provider audit). Bucket creation
#      is delegated to the `aminueza/minio` provider, which speaks the
#      S3 bucket API against `<region>.your-objectstorage.com`.
#   2. Hetzner does NOT expose a Cloud API to create S3 access keys
#      programmatically — the operator issues them once in the Hetzner
#      Console (Object Storage → Manage Credentials, secret half shown
#      exactly once and irretrievable thereafter). The wizard collects
#      both halves; the catalyst-api validates them via S3 ListBuckets;
#      this module receives them as variables and uses them for both
#      bucket creation AND interpolation into the Sovereign cloud-init's
#      `hetzner-object-storage` Kubernetes Secret.
#   3. Object Storage is available only in fsn1/nbg1/hel1 today. For
#      ash/hil compute Sovereigns the operator picks a European Object
#      Storage region — Velero/Harbor are latency-tolerant and the
#      backup path is asynchronous.

variable "object_storage_region" {
  type        = string
  description = <<-EOT
    Hetzner Object Storage region — one of fsn1 / nbg1 / hel1 (the
    European-only availability zones for Object Storage as of 2026-04).
    The endpoint URL is derived as `<region>.your-objectstorage.com` per
    https://docs.hetzner.com/storage/object-storage/getting-started/
    using-s3-api-tools/. Per docs/INVIOLABLE-PRINCIPLES.md #4 this is a
    runtime variable, never hardcoded — every Sovereign picks its own
    Object Storage region in the wizard.
  EOT
  validation {
    # Authoritative list of Hetzner Object Storage regions as of 2026-04-30.
    # Update when Hetzner adds a new Object Storage region (NOT the same
    # as Cloud regions — Cloud has ash/hil but Object Storage does not).
    condition     = contains(["fsn1", "nbg1", "hel1"], var.object_storage_region)
    error_message = "Object Storage region must be one of: fsn1 (Falkenstein), nbg1 (Nuremberg), hel1 (Helsinki). Object Storage is European-only as of 2026-04."
  }
}

variable "object_storage_access_key" {
  type        = string
  description = <<-EOT
    Hetzner Object Storage S3 access key — operator-issued once in the
    Hetzner Console (Object Storage → Manage Credentials). The
    catalyst-api validates this against the chosen region's S3 endpoint
    via ListBuckets BEFORE `tofu apply` runs, so a typo'd key surfaces
    at the wizard credential step, not 5 minutes into provisioning.
    Sensitive — never logged. Lives only in the per-deployment OpenTofu
    workdir (encrypted PVC, mode 0600) and in the Sovereign's cloud-init
    user_data; wiped on `tofu destroy`.
  EOT
  sensitive   = true
  validation {
    # Hetzner S3 access keys are 20-character ASCII per the AWS S3 v4
    # signing convention they emulate. We accept the broad shape rather
    # than the precise length so future Hetzner format changes don't
    # bounce off this validator with a stale literal.
    condition     = length(var.object_storage_access_key) >= 16 && length(var.object_storage_access_key) <= 64
    error_message = "Object Storage access key must be 16–64 characters."
  }
}

variable "object_storage_secret_key" {
  type        = string
  description = <<-EOT
    Hetzner Object Storage S3 secret key — operator-issued alongside the
    access key in the Hetzner Console. Per Hetzner's docs the secret is
    shown EXACTLY ONCE at issue time; if the operator loses it they must
    rotate. Sensitive — never logged. Same persistence boundary as the
    access key: per-deployment encrypted workdir + Sovereign cloud-init
    only; wiped on `tofu destroy`.
  EOT
  sensitive   = true
  validation {
    # Hetzner S3 secret keys are typically 40 base64 characters (AWS-style)
    # but the public spec does not pin a length and rotations may emit
    # different lengths in the future. 32–128 is the resilient range.
    condition     = length(var.object_storage_secret_key) >= 32 && length(var.object_storage_secret_key) <= 128
    error_message = "Object Storage secret key must be 32–128 characters."
  }
}

variable "object_storage_bucket_name" {
  type        = string
  description = <<-EOT
    Hetzner Object Storage bucket name. Bucket names share a global
    namespace across ALL Hetzner Object Storage tenants per
    https://docs.hetzner.com/storage/object-storage/getting-started/
    creating-a-bucket/, so we derive a deterministic per-Sovereign name
    from the FQDN slug (catalyst-api computes this; the wizard never
    surfaces a free-form bucket-name input to the operator). Pattern:
    `catalyst-<sovereign-fqdn-with-dots-replaced-by-dashes>`.

    The bucket is created idempotently via the `aminueza/minio` provider
    in main.tf. Existing buckets with a matching name are adopted (the
    minio_s3_bucket resource is idempotent on Create when the bucket
    already exists in the same tenant — re-running `tofu apply` against
    a previously-provisioned Sovereign is a no-op, never an error).
  EOT
  validation {
    # S3 bucket naming rules:
    #   - 3-63 chars
    #   - lowercase letters, digits, hyphens
    #   - must start and end with alphanumeric
    condition     = can(regex("^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$", var.object_storage_bucket_name))
    error_message = "Object Storage bucket name must be 3-63 chars, lowercase alphanumeric + hyphens, starting and ending with alphanumeric (RFC-compliant S3 bucket naming)."
  }
}
