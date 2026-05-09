# versions.tf — provider pin for the lease-witness Worker module.
#
# Slice K-Cont-4 of EPIC-6 (#1101) — first Cloudflare-resource module
# in this repo. We pin the official cloudflare/cloudflare provider
# (https://registry.terraform.io/providers/cloudflare/cloudflare/) at
# v5+ which exposes:
#   - cloudflare_workers_script         (deploy the Worker source)
#   - cloudflare_workers_kv_namespace   (create the KV namespace)
#   - cloudflare_workers_secret         (env-bound bearer token list)
#
# Required version >= 1.6.0 to match the rest of `infra/`'s OpenTofu
# version floor (see infra/hetzner/versions.tf).

terraform {
  required_version = ">= 1.6.0"

  required_providers {
    cloudflare = {
      source = "cloudflare/cloudflare"
      # 5.x renamed several Workers resources; pinning <6 prevents an
      # accidental jump across a future breaking-change boundary. Bump
      # the upper bound deliberately when CF ships v6 + we audit the
      # rename diff.
      version = "~> 5.0"
    }
  }
}
