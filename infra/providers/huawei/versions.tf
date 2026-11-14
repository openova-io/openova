# Huawei Cloud Stack (HCS) — pinned provider versions.
#
# We use the upstream `huaweicloud/huaweicloud` Terraform provider
# (OpenTofu-compatible). The provider talks to the public Huawei Cloud
# API by default; for the on-premises / sovereign-cloud HCS endpoint
# pattern (`https://<service>.me-east-215.kom4dc.nationalcloud.om`) we
# override per-service endpoints via the `endpoints {}` block in
# main.tf — same pattern Hetzner Object Storage uses to point the
# `hashicorp/aws` S3 client at `your-objectstorage.com`.
#
# OBS bucket creation reuses `hashicorp/aws` (S3-protocol) like Hetzner
# does, because Huawei OBS is S3-compatible and the canonical
# CreateBucket path is the simpler of the two. Avoiding the native OBS
# resource keeps the bucket-purge code path uniform across providers
# (the minio-go client at internal/hetzner/buckets.go works against
# any S3 endpoint — Wave 4 may extract this into a shared
# internal/objectstorage/s3/ package).

terraform {
  required_version = ">= 1.6.0"

  required_providers {
    huaweicloud = {
      source  = "huaweicloud/huaweicloud"
      version = "~> 1.65"
    }

    # S3-compatible bucket layer for OBS. Same rationale as the Hetzner
    # module's aws provider: a vanilla S3 client against the cloud's
    # S3-protocol endpoint avoids the cloud-specific Terraform-resource
    # path entirely and keeps bucket purge cross-cloud.
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }

    time = {
      source  = "hashicorp/time"
      version = "~> 0.10"
    }
  }
}

# ── Huawei Cloud Stack provider ───────────────────────────────────────────
#
# The on-premises HCS deployment exposes per-service endpoints under
# `https://<service>.me-east-215.kom4dc.nationalcloud.om` (region:
# me-east-215, AZ: me-east-215a — single-AZ region with VPC isolation
# used as the fake-region pattern). Credentials are operator-supplied
# IAM AK/SK + project_id, NEVER baked into the module's defaults.
#
# `insecure = true` reflects that the on-prem HCS CA isn't in the
# standard trust store. For public Huawei Cloud the same module flips
# to `insecure = false` via a variable override.
provider "huaweicloud" {
  region     = var.huawei_region
  access_key = var.huawei_access_key
  secret_key = var.huawei_secret_key
  project_id = var.huawei_project_id

  endpoints = {
    iam = "https://iam.${var.huawei_region}.kom4dc.nationalcloud.om"
    ecs = "https://ecs.${var.huawei_region}.kom4dc.nationalcloud.om"
    vpc = "https://vpc.${var.huawei_region}.kom4dc.nationalcloud.om"
    evs = "https://evs.${var.huawei_region}.kom4dc.nationalcloud.om"
    ims = "https://ims.${var.huawei_region}.kom4dc.nationalcloud.om"
    obs = "https://obs.${var.huawei_region}.kom4dc.nationalcloud.om"
    elb = "https://elb.${var.huawei_region}.kom4dc.nationalcloud.om"
    tms = "https://tms.${var.huawei_region}.kom4dc.nationalcloud.om"

    # KMS / KPS (Key Pair Service) — needed for huaweicloud_kps_keypair
    # which registers the operator-supplied SSH key for ECS attachment.
    # Without this override the provider falls back to
    # kms.<region>.myhuaweicloud.com (public Huawei Cloud) which the
    # on-prem HCS deployment can't reach. Caught live on 8f711cae
    # 2026-05-22T19:52Z: `dial tcp: lookup kms.me-east-215.
    # myhuaweicloud.com: no such host`.
    kms = "https://kms.${var.huawei_region}.kom4dc.nationalcloud.om"
    # NAT Gateway (Wave 5.21, Refs #2140) — workers need internet egress
    # for apt-install + k3s installer. SNAT rule + EIP gives transparent
    # egress without per-worker EIP.
    nat = "https://nat.${var.huawei_region}.kom4dc.nationalcloud.om"
  }

  insecure = var.huawei_insecure
}

# Huawei OBS — S3-protocol layer (same `hashicorp/aws` client pattern
# the Hetzner module uses against Hetzner Object Storage). Path-style
# addressing is mandatory; the four `skip_*` flags disable AWS-specific
# preflight calls (STS GetCallerIdentity, IMDS metadata discovery) that
# HCS does not implement.
provider "aws" {
  region                      = var.huawei_region
  access_key                  = var.huawei_access_key
  secret_key                  = var.huawei_secret_key
  skip_credentials_validation = true
  skip_metadata_api_check     = true
  skip_requesting_account_id  = true
  skip_region_validation      = true
  s3_use_path_style           = true

  endpoints {
    s3 = "https://obs.${var.huawei_region}.kom4dc.nationalcloud.om"
  }
}
