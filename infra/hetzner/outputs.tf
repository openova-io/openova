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
