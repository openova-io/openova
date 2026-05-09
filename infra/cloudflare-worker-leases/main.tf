# main.tf — deploys the OpenOva Continuum lease-witness Worker.
#
# Slice K-Cont-4 of EPIC-6 (#1101). The Worker source lives at
# `products/continuum/cloudflare-worker/`; this module is the operator-
# facing deploy surface that:
#   1. Creates a Cloudflare Workers KV namespace (`OPENOVA_LEASES`)
#   2. Uploads the bundled Worker script
#   3. Binds the KV namespace + env vars to the script
#   4. Optionally registers a custom subdomain route
#
# Per docs/INVIOLABLE-PRINCIPLES.md:
#   - #4: every value is a variable — account id, KV name, bearer tokens
#   - #5: bearer tokens come from a K8s SealedSecret (operator extracts
#     via TF_VAR_bearer_tokens_csv at apply time; never committed)
#
# `tofu apply` is OPERATOR-RUN (per K-Cont-4 brief, "DO NOT actually run
# tofu apply — module ships ready for operator action"). CI verifies via
# `tofu validate` + `tofu fmt -check` only.

provider "cloudflare" {
  api_token = var.cloudflare_api_token
}

# ─── KV namespace ──────────────────────────────────────────────────────
#
# Per the K-Cont-3 Cloudflare KV Worker contract, keys live under
# `lease:<slot-url-encoded>`. ONE namespace serves all Continuum CRs
# this Worker is responsible for; per-CR isolation is via the slot
# string, not via separate KV namespaces.
resource "cloudflare_workers_kv_namespace" "leases" {
  account_id = var.cloudflare_account_id
  title      = var.kv_namespace_title
}

# ─── Worker script ─────────────────────────────────────────────────────
#
# `cloudflare_workers_script` v5 expects `content` to be the bundled JS
# (the result of `wrangler deploy --dry-run --outdir=dist`). Operators
# build the bundle out-of-band from `products/continuum/cloudflare-worker/`
# and point `worker_script_path` at the result.
#
# The script binds:
#   - kv_namespace OPENOVA_LEASES → the namespace above
#   - plain_text_binding BEARER_TOKENS_CSV → operator-supplied
#   - plain_text_binding LOG_LEVEL → operator-supplied
#
# `compatibility_date` MUST match wrangler.toml's value to keep the
# runtime semantics identical between `wrangler dev` and the deployed
# script.
resource "cloudflare_workers_script" "lease_witness" {
  account_id         = var.cloudflare_account_id
  script_name        = var.worker_name
  content            = file(var.worker_script_path)
  main_module        = "index.js"
  compatibility_date = "2024-09-09"

  # Bindings — must match `wrangler.toml`'s `[[kv_namespaces]] binding`
  # + `[vars]` keys. Per the cloudflare/cloudflare v5 provider the
  # binding `type` enum is one of: `kv_namespace`, `plain_text`,
  # `secret_text`, ... (see `tofu providers schema`).
  #
  # On secret handling: `secret_text` is the v5 canonical seam for
  # Worker-bound secrets — encrypted at rest by Cloudflare, never
  # visible via the dashboard read API once written. (V4 had a
  # separate `cloudflare_workers_secret` resource; v5 collapsed both
  # into the inline binding.)  Per Inviolable Principle #5 the actual
  # value comes from a K8s SealedSecret extracted into
  # TF_VAR_bearer_tokens_csv at apply time — never inlined here.
  #
  # Rotation: replace TF_VAR_bearer_tokens_csv with `<new>,<old>`, run
  # `tofu apply`. The Worker now accepts both. After one renew cycle
  # (default 10s) update controller-side SealedSecret, then run a final
  # `tofu apply` with just `<new>` to retire the old token.
  bindings = [
    {
      name         = "OPENOVA_LEASES"
      type         = "kv_namespace"
      namespace_id = cloudflare_workers_kv_namespace.leases.id
    },
    {
      name = "LOG_LEVEL"
      type = "plain_text"
      text = var.log_level
    },
    {
      name = "BEARER_TOKENS_CSV"
      type = "secret_text"
      text = var.bearer_tokens_csv
    },
  ]
}
