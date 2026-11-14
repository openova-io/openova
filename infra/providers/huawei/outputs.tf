# Outputs the catalyst-api provisioner reads after `tofu apply` against
# the Huawei Cloud (Stack) module. Variable + output names mirror the
# canonical contract in infra/providers/PROVIDER-INTERFACE.md §2 so the
# Go-side provisioner.readOutputs() is provider-agnostic.

output "control_plane_ip" {
  description = "Public EIP of the primary-region's primary CP node. On Huawei the EIP IS the entry point (no cloud LB in Tier-B). Wave 5.7: keyed by region code."
  value       = length(var.regions) > 0 ? huaweicloud_vpc_eip.cp[var.regions[0].code].address : ""
}

output "load_balancer_ip" {
  description = "Public EIP the wildcard *.<fqdn> + app DNS A-records point at. #4690 / #4686 foundation-fix — this is the RESTORED gateway ELB's OWN routable EIP (huaweicloud_vpc_eip.elb_primary). The ELB does TCP passthrough public :443/:80 → node:443/:80, the durable cilium-envoy hostNetwork host ports (§854 — NodePorts FORBIDDEN; the former ELB→nodePort shape of #4690/#4691 was retired by the cilium 1.19.3 bump). It is DISTINCT from control_plane_ip above (the CP-node EIP, a Huawei 1:1 NAT that is externally unreachable — the #4687 option-B wrongly pointed DNS there, verified hw208). tofu seeds correct members at Phase-0; catalyst-api only heals member-set drift from node churn post-convergence (post_handover_gateway_elb.go) — ports never change."
  value       = huaweicloud_vpc_eip.elb_primary.publicip.0.ip_address
}

# #4053 — public EIP of the dedicated CONSOLE ELB. console./api.<fqdn> point
# HERE (to the isolated cilium-gateway-console LoadBalancer Service; #4682);
# the wildcard *.<fqdn> points at load_balancer_ip above (the RESTORED gateway ELB, #4690).
# catalyst-api threads this to pool-domain-manager /commit as
# consoleLoadBalancerIP. Empty-safe: a pre-#4053 consumer ignores it and PDM
# falls back to load_balancer_ip for every record.
output "console_load_balancer_ip" {
  description = "Public EIP of the dedicated console ELB (console./api.<fqdn> point here; #4053 gateway isolation). EMPTY when console_isolation_enabled=false — the catalyst-api DNS-writer then collapses console/api/marketplace onto load_balancer_ip (sovereign_dns_records.go, test-covered)."
  value       = length(huaweicloud_vpc_eip.elb_console) > 0 ? huaweicloud_vpc_eip.elb_console[0].publicip.0.ip_address : ""
}

output "sovereign_fqdn" {
  description = "Echo back the FQDN this Sovereign was provisioned for."
  value       = var.sovereign_fqdn
}

output "console_url" {
  description = "URL where the new Sovereign's Catalyst console is reachable once Flux finishes bootstrapping."
  value       = "https://console.${var.sovereign_fqdn}"
}

output "gitops_repo_url" {
  description = "Git URL Flux on the new cluster watches."
  value       = "${var.gitops_repo_url}//clusters/${var.sovereign_fqdn}"
}

# Echo of the input — PROVIDER-INTERFACE.md §2 mandates this even when
# the Go-side just round-trips the value, so the wizard's success-screen
# code path is uniform across providers.
output "gitops_repo_url_out" {
  description = "Echo of the input gitops_repo_url + branch as a fully-qualified clone URL."
  value       = "${var.gitops_repo_url}//clusters/${var.sovereign_fqdn}"
}

output "kubeconfig_path" {
  description = "Empty at apply-time; cloud-init PUTs the kubeconfig back via the bearer endpoint once k3s starts."
  value       = ""
}

# ── OBS bucket outputs (analogue of object_storage_* on Hetzner) ─────────

output "obs_endpoint" {
  description = "S3-protocol OBS endpoint URL the Sovereign's Harbor + Velero point at."
  value       = "https://obs.${var.huawei_region}.kom4dc.nationalcloud.om"
}

output "obs_region" {
  description = "Huawei region the OBS bucket lives in."
  value       = var.huawei_region
}

output "obs_bucket" {
  description = "Per-Sovereign OBS bucket name."
  value       = huaweicloud_obs_bucket.main.bucket
}

# PROVIDER-INTERFACE.md §2 — canonical s3_buckets list (drives the wipe
# handler's per-deployment bucket purge).
output "s3_buckets" {
  description = "Names of object-storage buckets the module created. Drives wipe-handler bucket purge."
  value       = [huaweicloud_obs_bucket.main.bucket]
}

# ── Per-region structured outputs ────────────────────────────────────────
# PROVIDER-INTERFACE.md §2 region_primary + region_secondaries. Same
# shape the Hetzner module emits via control_plane_ips_by_region etc.,
# but keyed by `<code>` directly rather than the `<region>-<index>`
# composite (Huawei has no shared/legacy singular path to preserve).

output "region_primary" {
  description = "Code of the primary region (echo of regions[0].code)."
  value       = length(var.regions) > 0 ? var.regions[0].code : ""
}

output "region_secondaries" {
  description = "Codes of secondary regions in declared order."
  value = [
    for r in var.regions : r.code
    if r.role == "secondary"
  ]
}

output "control_plane_ips_per_region" {
  description = "Public EIPs of each region's primary CP node, keyed by region code. Wave 5.7: 1 EIP per region (was 1-per-CP)."
  value = {
    for r in var.regions :
    r.code => huaweicloud_vpc_eip.cp[r.code].address
  }
}

output "worker_ips_per_region" {
  description = "Private IPs of each region's worker nodes, keyed by region code. Workers have no EIP — only cluster-internal connectivity."
  value = {
    for idx, r in var.regions :
    r.code => [
      for k, ws in huaweicloud_compute_instance.worker :
      ws.access_ip_v4
      if startswith(k, "${r.code}-")
    ]
  }
}

output "clustermesh_endpoint_per_region" {
  description = "Public clustermesh-apiserver endpoint per region (CP EIP + :2379 VIP). #4765 — peers dial the sovereign-vip LB-IPAM VIP on :2379 over DMZ-WG/mTLS; NodePorts are FORBIDDEN."
  value = {
    for r in var.regions :
    r.code => "${huaweicloud_vpc_eip.cp[r.code].address}:2379"
  }
}

output "s3_bucket_per_region" {
  description = "OBS bucket name per region. Tier-B uses ONE bucket for the Sovereign; emitted as a map for cross-provider uniformity."
  value = {
    for r in var.regions :
    r.code => huaweicloud_obs_bucket.main.bucket
  }
}
