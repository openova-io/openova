terraform {
  required_providers {
    random = { source = "hashicorp/random", version = "~> 3.6" }
  }
}
variable "region_keys" { type = set(string) }
variable "seal_key_generation" {
  type    = number
  default = 1
}
resource "random_bytes" "seal" {
  for_each = var.region_keys
  length   = 32
  keepers  = { generation = var.seal_key_generation }
}
locals {
  key_id = { for k in var.region_keys : k => nonsensitive(substr(sha256(random_bytes.seal[k].base64), 0, 16)) }
}
output "kind" { value = "static-node-file" }
output "stanza_by_region" {
  value = { for k in var.region_keys : k => <<-EOT
    seal "static" {
      current_key_id = "${local.key_id[k]}"
      current_key    = "file:///openbao/seal/openbao-seal-${local.key_id[k]}.key"
    }
  EOT
  }
}
output "node_files_by_region" {
  sensitive = true
  value = { for k in var.region_keys : k => [{
    path        = "/var/lib/catalyst/seal/openbao-seal-${local.key_id[k]}.key"
    owner       = "100:1000"
    permissions = "0400"
    content_b64 = random_bytes.seal[k].base64
  }] }
}
output "volume_device" { value = "/dev/vdb" } # reference cloud. "" for a pack with no separate volume.
