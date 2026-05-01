terraform {
  required_version = ">= 1.6.0"

  required_providers {
    hcloud = {
      source  = "hetznercloud/hcloud"
      version = "~> 1.49"
    }

    # Hetzner's own Cloud API does NOT expose Object Storage as a managed
    # resource (the upstream `hetznercloud/terraform-provider-hcloud` v1.49
    # supports certificate, datacenter, firewall, floatingip, image,
    # loadbalancer, network, primaryip, server, sshkey, storagebox, volume,
    # zone — but no object_storage / bucket / s3 resource). Hetzner ALSO
    # exposes no API to mint S3 access keys programmatically — credentials
    # are operator-issued exactly once via the Hetzner Console.
    #
    # Buckets, however, ARE creatable via standard S3 once credentials
    # exist. Hetzner officially recommends the `aminueza/minio` Terraform
    # provider for this path (https://docs.hetzner.com/storage/object-storage/
    # getting-started/creating-a-bucket-minio-terraform/). The provider
    # speaks both the MinIO admin API (irrelevant to us) AND the underlying
    # S3 bucket API (which is what Hetzner's Object Storage exposes).
    #
    # The bucket is a per-Sovereign Phase-0 resource — Harbor and Velero
    # consume the `s3-*` keys of the K8s Secret cloud-init writes into the
    # cluster. Without this provider declared here, OpenTofu's plan stage
    # cannot create the bucket and the operator would be forced to bring
    # up the Sovereign and then create the bucket out-of-band — violating
    # the principle that Phase 0 is end-to-end declarative.
    minio = {
      source  = "aminueza/minio"
      version = "~> 3.5"
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

# Hetzner Object Storage — S3-compatible bucket layer. The endpoint format is
# `<region>.your-objectstorage.com` (region ∈ fsn1 / nbg1 / hel1 — Object
# Storage availability is European-only as of 2026-04, see
# https://docs.hetzner.com/storage/object-storage/overview/). For Sovereigns
# whose compute region is ash/hil (USA), the operator selects a European
# Object Storage region in the wizard; Velero/Harbor backup latency is
# acceptable for the use case (asynchronous tarball push).
#
# `minio_ssl = true` is mandatory — Hetzner Object Storage rejects plaintext
# HTTP. The minio provider uses the AWS S3 SDK under the hood and works
# against any S3-compatible service whose region+endpoint pair is set.
#
# Credentials come from the wizard's StepCredentials object-storage section,
# operator-issued in the Hetzner Console (Console → Object Storage →
# Manage Credentials). Like hcloud_token, they never live in environment
# variables on the catalyst-api process — every Sovereign apply uses its
# own tenant's credentials.
provider "minio" {
  minio_server   = "${var.object_storage_region}.your-objectstorage.com"
  minio_user     = var.object_storage_access_key
  minio_password = var.object_storage_secret_key
  minio_region   = var.object_storage_region
  minio_ssl      = true
}
