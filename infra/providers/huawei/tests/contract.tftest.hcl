# Contract test of the Huawei module: the mocks of this module, the marker
# line, then infra/providers/_contract/contract.tftest.body.hcl byte for byte.
# The body sets the contract's variables. The module's own required variables
# (credentials and bucket name) come from tests/contract.auto.tfvars.json,
# which `tofu test` reads by itself.

mock_provider "huaweicloud" {
  mock_resource "huaweicloud_vpc" {
    defaults = {
      id = "vpc-mock-1"
    }
  }
  mock_resource "huaweicloud_vpc_subnet" {
    defaults = {
      id = "subnet-mock-1"
    }
  }
  mock_resource "huaweicloud_vpc_eip" {
    defaults = {
      id      = "eip-mock-1"
      address = "203.0.113.10"
    }
  }
  mock_resource "huaweicloud_networking_secgroup" {
    defaults = {
      id = "sg-mock-1"
    }
  }
  mock_resource "huaweicloud_networking_secgroup_rule" {
    defaults = {
      id = "sgr-mock-1"
    }
  }
  mock_resource "huaweicloud_compute_instance" {
    defaults = {
      id           = "ecs-mock-1"
      access_ip_v4 = "10.20.1.10"
    }
  }
  mock_resource "huaweicloud_kps_keypair" {
    defaults = {
      id = "kp-mock-1"
    }
  }
  mock_resource "huaweicloud_obs_bucket" {
    defaults = {
      id = "catalyst-test-bucket"
    }
  }
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
