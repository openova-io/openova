# outputs.tf — operator-facing outputs.
#
# `worker_url` is THE value the operator pastes into the Continuum
# CR's `spec.leaseClient.config.baseURL`. The CFKVClient (K-Cont-3)
# constructs requests as `${baseURL}/lease/<slot-url-encoded>`.

output "worker_url" {
  description = <<-EOT
    HTTPS URL of the deployed lease-witness Worker. Set this on the
    Continuum CR via:

      spec:
        leaseClient:
          kind: cloudflare-kv
          config:
            baseURL: "<this output>"
            tokenSecretRef:
              name: continuum-cf-witness-tokens
              key: token
  EOT
  # Cloudflare's default Workers route format. The actual URL depends
  # on the operator's Workers subdomain (settable in the CF dashboard);
  # this output assumes the default `.workers.dev` subdomain. For a
  # custom domain route, override `spec.leaseClient.config.baseURL`
  # directly with the custom hostname.
  value = "https://${var.worker_name}.${var.cloudflare_account_id}.workers.dev"
}

output "kv_namespace_id" {
  description = "Cloudflare KV namespace ID — useful for ad-hoc CLI introspection (`wrangler kv:key list`)."
  value       = cloudflare_workers_kv_namespace.leases.id
}

output "worker_name" {
  description = "Worker script name as deployed."
  value       = cloudflare_workers_script.lease_witness.script_name
}
