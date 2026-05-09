# variables.tf — operator-supplied inputs for the lease-witness Worker.
#
# Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode), every value
# this module needs comes from a variable — no inline account ids,
# zone ids, or KV namespace names. Per #5 (sealed secrets), the bearer
# token allow-list is sensitive and lives only in memory during apply.

# ─── Cloudflare account ───────────────────────────────────────────────

variable "cloudflare_account_id" {
  type        = string
  description = <<-EOT
    Cloudflare account id (32-char hex). Required for every CF
    Workers resource. Sourced from the operator's Cloudflare account
    dashboard (Right sidebar → Account ID). Never inlined; passed
    via tofu var (or a tfvars file outside git).
  EOT
  validation {
    condition     = can(regex("^[0-9a-f]{32}$", var.cloudflare_account_id))
    error_message = "cloudflare_account_id must be a 32-character hex string."
  }
}

variable "cloudflare_api_token" {
  type        = string
  sensitive   = true
  description = <<-EOT
    Cloudflare API token with permissions:
      - Account → Workers Scripts: Edit
      - Account → Workers KV Storage: Edit
    Generate via My Profile → API Tokens. Per Inviolable Principle
    #5 the token never lives in tofu-vars files committed to git;
    pass via TF_VAR_cloudflare_api_token at apply time.
  EOT
  validation {
    # CF tokens are 40-char base64-style strings. We don't pin a
    # precise format because CF may rotate the issuance shape.
    condition     = length(var.cloudflare_api_token) >= 32
    error_message = "cloudflare_api_token must be at least 32 characters."
  }
}

# ─── Worker identity ──────────────────────────────────────────────────

variable "worker_name" {
  type        = string
  description = <<-EOT
    Cloudflare Worker script name. Becomes part of the Worker URL:
    `https://<worker_name>.<account-subdomain>.workers.dev`. The
    Continuum controller reads this URL from the Continuum CR's
    `spec.leaseClient.config.baseURL`. Per Inviolable Principle #4
    runtime-configurable; the default is documented for the common
    case but every Sovereign can override it.
  EOT
  default     = "openova-continuum-lease-witness"
  validation {
    # Worker names must be lowercase RFC1035-compatible identifiers.
    condition     = can(regex("^[a-z][a-z0-9-]{0,62}$", var.worker_name))
    error_message = "worker_name must be a lowercase RFC1035 identifier (max 63 chars)."
  }
}

variable "kv_namespace_title" {
  type        = string
  description = <<-EOT
    Title (display name) of the KV namespace bound to the Worker as
    `OPENOVA_LEASES`. Per the K-Cont-3 contract (canonical-seams.md
    "Cloudflare KV Worker contract" entry) keys live under
    `lease:<slot-url-encoded>`. Defaults to a per-Sovereign-friendly
    name; operators may suffix the Sovereign FQDN to disambiguate
    when multiple Sovereigns share an account.
  EOT
  default     = "openova-continuum-leases"
  validation {
    condition     = length(var.kv_namespace_title) > 0 && length(var.kv_namespace_title) <= 64
    error_message = "kv_namespace_title must be 1-64 characters."
  }
}

# ─── Auth ─────────────────────────────────────────────────────────────

variable "bearer_tokens_csv" {
  type        = string
  sensitive   = true
  description = <<-EOT
    Comma-separated allow-list of valid bearer tokens. The Worker
    accepts `Authorization: Bearer <token>` IFF the token appears in
    this list. Per Inviolable Principle #5 sourced from a K8s
    SealedSecret in the Sovereign cluster (typically
    `catalyst-system/continuum-cf-witness-tokens`); the operator
    extracts the plaintext via `kubectl --kubeconfig` ONLY long
    enough to feed `TF_VAR_bearer_tokens_csv` to this module.

    Generate per Inviolable Principle #5: `openssl rand -hex 32`
    per token, 24+ chars, no dictionary words. Include 2 tokens
    when rotating: the old one stays valid for one renew cycle
    (default 10s) so the controller doesn't drop the lease during
    rollout.
  EOT
  validation {
    condition     = length(trim(var.bearer_tokens_csv, " ,")) > 0
    error_message = "bearer_tokens_csv must contain at least one non-empty token."
  }
}

variable "log_level" {
  type        = string
  description = <<-EOT
    Worker LOG_LEVEL — one of "error" / "info" / "debug". `info`
    logs request method + path + status; `debug` adds X-Holder +
    If-Match. The Authorization header value is NEVER logged. Per
    CLAUDE.md credential hygiene, prefer `info` in production;
    `debug` only during incident response.
  EOT
  default     = "info"
  validation {
    condition     = contains(["error", "info", "debug"], var.log_level)
    error_message = "log_level must be one of: error, info, debug."
  }
}

# ─── Worker source ─────────────────────────────────────────────────────

variable "worker_script_path" {
  type        = string
  description = <<-EOT
    Path (relative to this module dir, or absolute) to the bundled
    Worker script. Default expects the operator to have run
    `npm run build:dryrun` in `products/continuum/cloudflare-worker/`
    which produces `dist/index.js`. The build step is left out of
    the tofu module so the bundle is reviewable + reproducible from
    CI rather than synthesized at apply time.

    For a pre-deploy dry-run that only needs `tofu plan` to succeed
    with NO real Cloudflare credentials, point this at the source
    `src/index.ts` directly — `tofu validate` doesn't read the file.
  EOT
  default     = "../../products/continuum/cloudflare-worker/dist/index.js"
}
