# Outputs the catalyst-api provisioner reads after `tofu apply` completes
# and surfaces back to the wizard's success screen.

output "control_plane_ip" {
  description = "Public IPv4 of the first control-plane node"
  value       = hcloud_server.control_plane[0].ipv4_address
}

output "load_balancer_ip" {
  description = "Public IPv4 of the Hetzner load balancer (the address DNS A records point at)"
  value       = hcloud_load_balancer.main.ipv4
}

output "sovereign_fqdn" {
  description = "Echo back the FQDN this Sovereign was provisioned for"
  value       = var.sovereign_fqdn
}

output "console_url" {
  description = "URL where the new Sovereign's Catalyst console is reachable once Flux finishes bootstrapping"
  value       = "https://console.${var.sovereign_fqdn}"
}

output "gitops_repo_url" {
  description = "Git URL Flux on the new cluster watches"
  value       = "${var.gitops_repo_url}//clusters/${var.sovereign_fqdn}"
}

# ── Hetzner Object Storage outputs (Phase 0b — issue #371) ────────────────
#
# Surfaced back to the catalyst-api after `tofu apply`. The catalyst-api
# logs them at info level (NEVER the credentials themselves) so an
# operator inspecting the deployment record can confirm Phase 0b
# completed and which bucket Harbor + Velero will land in once Phase 1
# finishes. The wizard's success screen does NOT render these — the
# bucket is plumbing, not user-facing.

output "object_storage_endpoint" {
  description = "S3 endpoint URL the Sovereign's Harbor + Velero point at"
  value       = local.object_storage_endpoint
}

output "object_storage_region" {
  description = "Hetzner Object Storage region the bucket lives in"
  value       = var.object_storage_region
}

output "object_storage_bucket" {
  description = "Per-Sovereign Object Storage bucket name (catalyst-<sovereign-slug>)"
  value       = minio_s3_bucket.main.bucket
}
