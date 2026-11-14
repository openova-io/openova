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
