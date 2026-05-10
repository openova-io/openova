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
    # exist. We use `hashicorp/aws` configured against Hetzner's S3
    # endpoint (`<region>.your-objectstorage.com`). The aws provider
    # talks vanilla S3 to any S3-compatible backend and — critically —
    # does NOT call DeleteBucketPolicy as part of its Create handler.
    #
    # Why we moved off `aminueza/minio`:
    #   `aminueza/minio v3.34.0`'s `minio_s3_bucket` Create handler calls
    #   `DeleteBucketPolicy` post-create as part of state normalization
    #   (the provider treats "no policy" as the canonical zero state and
    #   forcibly clears any inherited policy). Hetzner Object Storage's
    #   standard read/write credentials don't grant `s3:DeleteBucketPolicy`
    #   so this fails with AccessDenied EVERY TIME, even though the bucket
    #   itself is created on Hetzner's side. tofu marks the resource as
    #   failed and rolls back the apply, blocking every fresh Sovereign
    #   provision from reaching Phase 1. Provisions #13 / #17 both wedged
    #   on this in <2 min — the wedge is deterministic, not flaky.
    #
    # The aws provider does no such normalization: a successful CreateBucket
    # response is the terminal state for `aws_s3_bucket` Create. Hetzner
    # officially documents AWS CLI / SDK as a supported S3 client (see
    # https://docs.hetzner.com/storage/object-storage/getting-started/
    # using-s3-api-tools/) so this is the canonical-vendor path, not a
    # workaround.
    #
    # The bucket is a per-Sovereign Phase-0 resource — Harbor and Velero
    # consume the `s3-*` keys of the K8s Secret cloud-init writes into the
    # cluster. Without this provider declared here, OpenTofu's plan stage
    # cannot create the bucket and the operator would be forced to bring
    # up the Sovereign and then create the bucket out-of-band — violating
    # the principle that Phase 0 is end-to-end declarative.
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
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
# `s3_use_path_style = true` is mandatory — Hetzner Object Storage uses
# path-style addressing (https://endpoint/bucket), not virtual-host
# (https://bucket.endpoint). The four `skip_*` flags disable AWS-specific
# preflight calls (STS GetCallerIdentity, IMDS metadata discovery) that
# Hetzner does not implement and that would otherwise fail provider init.
#
# Credentials come from the wizard's StepCredentials object-storage section,
# operator-issued in the Hetzner Console (Console → Object Storage →
# Manage Credentials). Like hcloud_token, they never live in environment
# variables on the catalyst-api process — every Sovereign apply uses its
# own tenant's credentials.
provider "aws" {
  region                      = var.object_storage_region
  access_key                  = var.object_storage_access_key
  secret_key                  = var.object_storage_secret_key
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  s3_use_path_style           = true

  endpoints {
    s3 = "https://${var.object_storage_region}.your-objectstorage.com"
  }
}
