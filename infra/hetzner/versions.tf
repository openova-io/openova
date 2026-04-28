terraform {
  required_version = ">= 1.6.0"

  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "~> 1.49"
    }
  }
}

# Provider configured from the hcloud_token variable. Per Catalyst the token
# comes from the wizard's StepCredentials, never from environment variables
# in the catalyst-api process — every Sovereign provisioning runs with the
# requesting customer's own token, never with a shared OpenOva token.
provider "hcloud" {
  token = var.hcloud_token
}
