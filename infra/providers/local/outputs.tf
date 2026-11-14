# Outputs of contract version 1 (infra/providers/_contract/contract.json).
# The front doors of the local pack are the control-plane machines' own
# addresses on the machine network.

output "contract_version" {
  description = "Version of the module contract this module meets."
  value       = "1"
}

output "control_plane_ip" {
  description = "Address of the primary region's control-plane machine."
  value       = local.control_plane_ip[local.region_keys[0]]
}

output "load_balancer_ip" {
  description = "Front door of the primary region: its control-plane machine, where the gateway binds the host ports 443 and 80."
  value       = local.control_plane_ip[local.region_keys[0]]
}

output "console_load_balancer_ip" {
  description = "Console front door: the primary control-plane machine when console_isolation_enabled is \"true\", else empty."
  value       = var.console_isolation_enabled == "true" ? local.control_plane_ip[local.region_keys[0]] : ""
}

output "control_plane_ips_per_region" {
  description = "Region code to the address of its control-plane machine."
  value       = local.control_plane_ip
}

output "network_registry" {
  description = "Network registry (JSON): pod supernet, MTU, region separation, control-plane access lists, and per region the cluster name and identifier, node, pod and service ranges, mesh endpoint, edge address and egress addresses."
  value       = jsonencode(local.network_registry)
}
