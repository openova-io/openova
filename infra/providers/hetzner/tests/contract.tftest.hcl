# Contract test of the Hetzner module: the mocks of this module, the marker
# line, then infra/providers/_contract/contract.tftest.body.hcl byte for byte.
# The body sets the contract's variables. The module's own required variables
# (credentials, the primary location and the object storage) come from
# tests/contract.auto.tfvars.json, which `tofu test` reads by itself.

mock_provider "hcloud" {
  mock_resource "hcloud_network" {
    defaults = {
      id = "1"
    }
  }
  mock_resource "hcloud_network_subnet" {
    defaults = {
      id = "1"
    }
  }
  mock_resource "hcloud_load_balancer" {
    defaults = {
      id   = "1"
      ipv4 = "203.0.113.10"
    }
  }
  mock_resource "hcloud_load_balancer_network" {
    defaults = {
      id = "1"
    }
  }
  mock_resource "hcloud_load_balancer_target" {
    defaults = {
      id = "1"
    }
  }
  mock_resource "hcloud_load_balancer_service" {
    defaults = {
      id = "1"
    }
  }
  mock_resource "hcloud_server" {
    defaults = {
      id           = "1"
      ipv4_address = "203.0.113.20"
    }
  }
  mock_resource "hcloud_ssh_key" {
    defaults = {
      id = "1"
    }
  }
  mock_resource "hcloud_firewall" {
    defaults = {
      id = "1"
    }
  }
}

# The hashicorp/aws provider has Required attributes (region etc.) at
# the schema level. Under `tofu test`, OpenTofu still type-checks the
# provider block before any `mock_provider` rewriting fires, and the
# production provider config in versions.tf reads from
# `var.object_storage_region` which the test framework cannot supply
# pre-evaluation in 1.8.5. Workaround: bypass the provider entirely by
# overriding the two aws resources the module declares (the bucket +
# its ACL). The overrides return synthetic values without ever invoking
# the provider — same outcome as a mock but without the schema-validation
# race. The contract runs assert nothing on these resources; the
# overrides are a test-harness concession only.
#
# History: tests/multi_region.tftest.hcl once overrode
# `minio_s3_bucket.main` for the same reason against the aminueza/minio
# provider. The provider was swapped to hashicorp/aws to escape that
# provider's AccessDenied wedge on Hetzner Object Storage (see versions.tf).
override_resource {
  target = aws_s3_bucket.main
  values = {
    id = "catalyst-test-example-com"
    # `bucket` is a required configuration input on aws_s3_bucket, not a
    # computed attribute. tofu test refuses to override config inputs with
    # "Invalid mock/override field `bucket`". The bucket name is resolved
    # naturally from var.object_storage_bucket_name; overriding it here is
    # unnecessary and blocks the test from running.
  }
}

override_resource {
  target = aws_s3_bucket_acl.main
  values = {}
}

# SEC-020: the k3s tokens are random_password resources. Under
# `command = plan` their `result` is unknown, which would leave every
# rendered cloud-init unknown and every assertion on it unevaluated. These
# overrides give the tokens fixed values in the plan.
override_resource {
  target = random_password.k3s_server_token
  values = {
    result = "test-k3s-server-token"
  }
}

override_resource {
  target = random_password.k3s_agent_token
  values = {
    result = "test-k3s-agent-token"
  }
}

override_resource {
  target = random_password.keycloak_client_secret_salt
  values = {
    result = "SEC060saltSEC060saltSEC060saltSEC060saltSEC060sa"
  }
}

# --- contract body ---
# Contract version 1: the shared body of every module's tests/contract.tftest.hcl.
# A module's file holds its own mock_provider and override blocks, then the
# marker line "# --- contract body ---", then this text byte for byte.
# The fixtures differ only in "regions". Paths are relative to the module.

variables {
  deployment_id         = jsondecode(file("../_contract/fixtures/single-region.tfvars.json")).deployment_id
  sovereign_fqdn        = jsondecode(file("../_contract/fixtures/single-region.tfvars.json")).sovereign_fqdn
  org_name              = jsondecode(file("../_contract/fixtures/single-region.tfvars.json")).org_name
  org_email             = jsondecode(file("../_contract/fixtures/single-region.tfvars.json")).org_email
  ssh_public_key        = jsondecode(file("../_contract/fixtures/single-region.tfvars.json")).ssh_public_key
  gitops_repo_url       = jsondecode(file("../_contract/fixtures/single-region.tfvars.json")).gitops_repo_url
  catalyst_api_url      = jsondecode(file("../_contract/fixtures/single-region.tfvars.json")).catalyst_api_url
  default_storage_class = jsondecode(file("../_contract/fixtures/single-region.tfvars.json")).default_storage_class
  regions               = jsondecode(file("../_contract/fixtures/single-region.tfvars.json")).regions
  variables             = jsondecode(file("../_contract/fixtures/single-region.tfvars.json")).variables
}

run "contract_single_region" {
  command = plan
}

run "contract_two_regions" {
  command = plan

  variables {
    regions = jsondecode(file("../_contract/fixtures/two-region.tfvars.json")).regions
  }
}

run "contract_outputs" {
  command = plan

  variables {
    regions = jsondecode(file("../_contract/fixtures/two-region.tfvars.json")).regions
  }

  assert {
    condition     = output.contract_version == "1"
    error_message = "output contract_version must be \"1\"."
  }

  assert {
    condition     = sort(keys(output.control_plane_ips_per_region)) == sort([for r in var.regions : r.code])
    error_message = "output control_plane_ips_per_region must have one key per region code."
  }
}
