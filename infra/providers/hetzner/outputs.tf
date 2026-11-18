# Outputs the catalyst-api provisioner reads after `tofu apply` completes
# and surfaces back to the wizard's success screen.

output "contract_version" {
  description = "Version of the module contract this module meets (infra/providers/_contract/contract.json)."
  value       = "1"
}

output "control_plane_ip" {
  description = "Public IPv4 of the first control-plane node"
  value       = hcloud_server.control_plane[0].ipv4_address
}

output "load_balancer_ip" {
  description = "Public IPv4 of the Hetzner load balancer (the address the wildcard + app DNS A records point at)"
  value       = hcloud_load_balancer.main.ipv4
}

# #4053 — public IPv4 of the dedicated CONSOLE load balancer. The
# console./api.<fqdn> A-records point HERE (forwards 443→node:443 to the
# isolated cilium-gateway-console — see hcloud_load_balancer_service.console_https,
# listen_port=443 destination_port=443; the ":31443" this line used to name was a
# nodePort that §854/#4765 eradicated and no resource has referenced since),
# while the wildcard *.<fqdn> keeps pointing
# at load_balancer_ip above (the shared cilium-gateway). catalyst-api reads this
# and threads it to pool-domain-manager /commit as consoleLoadBalancerIP so a
# poisoned shared-gateway CEC can never 404 the console. Empty-safe: a consumer
# that pre-dates #4053 ignores this output and PDM falls back to
# load_balancer_ip for every record (byte-identical to pre-#4053 behaviour).
output "console_load_balancer_ip" {
  description = "Public IPv4 of the dedicated console load balancer (console./api.<fqdn> point here; #4053 gateway isolation). EMPTY when console_isolation_enabled=false — the catalyst-api DNS-writer then collapses console/api/marketplace onto load_balancer_ip (sovereign_dns_records.go, test-covered)."
  value       = length(hcloud_load_balancer.console) > 0 ? hcloud_load_balancer.console[0].ipv4 : ""
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
# keyed by the same `{cloud_region}-{index}` key used internally
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
  description = "Public IPv4 of each secondary region's first control-plane node, keyed by '{cloud_region}-{index}'. Empty for single-region Sovereigns. The primary region's CP IP is still in `control_plane_ip`."
  value = {
    for k, _ in local.secondary_regions :
    k => hcloud_server.secondary_control_plane[k].ipv4_address
  }
}

output "load_balancer_ips_by_region" {
  description = "Public IPv4 of each secondary region's load balancer, keyed by '{cloud_region}-{index}'. PowerDNS lua-records aggregate these into the Sovereign FQDN's A-record set per docs/MULTI-REGION-DNS.md. Empty for single-region Sovereigns. The primary region's LB IP is still in `load_balancer_ip`."
  value = {
    for k, _ in local.secondary_regions :
    k => hcloud_load_balancer.secondary[k].ipv4
  }
}

output "secondary_region_keys" {
  description = "Stable keys of every secondary region the module provisioned, in deterministic insertion order. Joined with var.regions[1+] by the catalyst-api when projecting per-region status into the deployment record."
  value       = keys(local.secondary_regions)
}

# The per-region control-plane address map of contract version 1
# (infra/providers/_contract/contract.json): one key per entry of
# var.regions, the region code. The catalyst-api provisioner's
# partial-region-materialisation defense (#2840) compares these keys with
# the declared region codes, so a multi-region Sovereign that lost a region
# to a quota or capacity cascade does not pass the post-apply gate.
#
# CLD-090: the keys were "primary" and "<cloudRegion>-<index>" before; they
# are the region codes now, as in every other module. Two regions in one
# Hetzner location need two different codes (the contract refuses a
# duplicate code). The other per-region outputs of this file keep the
# module's internal keys.
output "control_plane_ips_per_region" {
  description = "Per-region primary control-plane public IPv4, keyed by region code (var.regions[].code): contract version 1. Used by catalyst-api partial-region detection. Refs #2840."
  value = merge(
    {
      (local.region_code["primary"]) = hcloud_server.control_plane[0].ipv4_address
    },
    {
      for k, v in local.secondary_regions :
      local.region_code[k] => hcloud_server.secondary_control_plane[k].ipv4_address
    }
  )
}

# NET-100 — the network registry of this Sovereign as one JSON string (contract
# output network_registry; infra/providers/PROVIDER-INTERFACE.md section 2).
output "network_registry" {
  description = "Network registry (JSON): pod supernet, MTU, region separation, control-plane access lists, and per region the cluster name and identifier, node, pod and service ranges, edge address and egress addresses."
  value       = jsonencode(local.network_registry)
}

# Contract C8 (docs/contracts/c8-seal-provider.md): the kind of the seal hook
# of this pack, static-node-file or cloud-key-service.
output "seal_kind" {
  description = "Kind of the seal hook of this pack (contract C8): where the OpenBao seal key rests."
  value       = module.seal.kind
}
