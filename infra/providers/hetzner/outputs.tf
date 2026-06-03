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
  value       = aws_s3_bucket.main.bucket
}

# ── Multi-region outputs (slice G1, EPIC-0 #1095) ─────────────────────────
#
# Surfaced back to the catalyst-api after `tofu apply` completes for
# Sovereigns whose request body carried len(regions) ≥ 2. The maps are
# keyed by the same `{cloudRegion}-{index}` key used internally
# (local.secondary_regions); the catalyst-api joins this with the
# original Request.Regions slice to project per-region IPs back into
# its deployment record. Empty when no secondary regions were
# requested — preserves the cost-zero shape for solo/legacy Sovereigns.
#
# The primary region's IPs are still exposed via the legacy
# `control_plane_ip` and `load_balancer_ip` outputs above, unchanged.
# This matches the catalyst-api's existing read paths and prevents any
# provisioner-side breakage on the singular path.

output "control_plane_ips_by_region" {
  description = "Public IPv4 of each secondary region's first control-plane node, keyed by '{cloudRegion}-{index}'. Empty for single-region Sovereigns. The primary region's CP IP is still in `control_plane_ip`."
  value = {
    for k, _ in local.secondary_regions :
    k => hcloud_server.secondary_control_plane[k].ipv4_address
  }
}

output "load_balancer_ips_by_region" {
  description = "Public IPv4 of each secondary region's load balancer, keyed by '{cloudRegion}-{index}'. PowerDNS lua-records aggregate these into the Sovereign FQDN's A-record set per docs/MULTI-REGION-DNS.md. Empty for single-region Sovereigns. The primary region's LB IP is still in `load_balancer_ip`."
  value = {
    for k, _ in local.secondary_regions :
    k => hcloud_load_balancer.secondary[k].ipv4
  }
}

output "secondary_region_keys" {
  description = "Stable keys of every secondary region the module provisioned, in deterministic insertion order. Joined with var.regions[1+] by the catalyst-api when projecting per-region status into the deployment record."
  value       = keys(local.secondary_regions)
}

# G117 #2840-followup (2026-06-03): unified per-region CP-IP map that
# INCLUDES the primary region, matching the Huawei provider's
# `control_plane_ips_per_region` shape. The catalyst-api provisioner's
# partial-region-materialisation defense (provisioner.go:1648) reads
# this exact key name to detect silently-degraded multi-region provs;
# without this output the Hetzner path skipped the check entirely
# (`len(out.ControlPlaneIPsPerRegion) > 0` evaluated false) and a
# multi-region Hetzner Sovereign that lost a region to a quota /
# capacity cascade would have passed the post-apply gate with the
# same silent single-region degradation that hit hw86 on HCS.
#
# Keyed by the operator-supplied `cloudRegion` value verbatim so the
# catalyst-api's region-code comparison works the same on both
# providers. The primary entry's value is identical to `control_plane_ip`
# above; secondary entries are pulled from the same hcloud_server
# resource the `_by_region` output already references.
output "control_plane_ips_per_region" {
  description = "Per-region primary control-plane public IPv4, keyed by the operator's cloudRegion value. Includes BOTH primary AND secondary regions. Mirrors the Huawei provider's same-named output so the catalyst-api partial-region detection works uniformly. Refs #2840."
  value = merge(
    {
      (var.region) = hcloud_server.control_plane[0].ipv4_address
    },
    {
      for k, v in local.secondary_regions :
      v.cloudRegion => hcloud_server.secondary_control_plane[k].ipv4_address
    }
  )
}
