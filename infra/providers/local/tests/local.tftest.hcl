# Tests of the local pack beyond the contract: two regions give two clusters,
# the reduced kit and the pack substitutes reach the control-plane user-data,
# the load-balancer pool is never a machine's own address, the DNS server
# binds port 53 on the machine, and the network registry has its form.

mock_provider "docker" {
  mock_resource "docker_image" {
    defaults = {
      image_id = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
    }
  }
}

mock_provider "random" {}

override_resource {
  target = random_password.k3s_server_token
  values = {
    result = "local-test-server-token"
  }
}

override_resource {
  target = random_password.k3s_agent_token
  values = {
    result = "local-test-agent-token"
  }
}

variables {
  deployment_id         = jsondecode(file("../_contract/fixtures/two-region.tfvars.json")).deployment_id
  sovereign_fqdn        = jsondecode(file("../_contract/fixtures/two-region.tfvars.json")).sovereign_fqdn
  org_name              = jsondecode(file("../_contract/fixtures/two-region.tfvars.json")).org_name
  org_email             = jsondecode(file("../_contract/fixtures/two-region.tfvars.json")).org_email
  ssh_public_key        = jsondecode(file("../_contract/fixtures/two-region.tfvars.json")).ssh_public_key
  gitops_repo_url       = jsondecode(file("../_contract/fixtures/two-region.tfvars.json")).gitops_repo_url
  catalyst_api_url      = jsondecode(file("../_contract/fixtures/two-region.tfvars.json")).catalyst_api_url
  regions               = jsondecode(file("../_contract/fixtures/two-region.tfvars.json")).regions
  default_storage_class = "local-path"
  variables             = jsondecode(file("../_contract/fixtures/two-region.tfvars.json")).variables
}

run "two_regions_are_two_clusters" {
  command = plan

  assert {
    condition     = join(",", sort(keys(docker_container.control_plane))) == "region-a-cp1,region-b-cp1"
    error_message = "two regions must give one control-plane machine each."
  }

  assert {
    condition     = join(",", sort(keys(docker_container.worker))) == "region-a-w1,region-b-w1"
    error_message = "worker_count 1 in each region must give one worker machine each."
  }

  assert {
    condition     = [for n in docker_container.control_plane["region-a-cp1"].networks_advanced : n.ipv4_address] == ["172.30.1.10"] && [for n in docker_container.control_plane["region-b-cp1"].networks_advanced : n.ipv4_address] == ["172.30.2.10"]
    error_message = "the control-plane machines must have host 10 of the /24 of their region."
  }

  assert {
    condition     = alltrue([for m in merge(docker_container.control_plane, docker_container.worker) : m.restart == "unless-stopped" && m.privileged])
    error_message = "every machine must be privileged and restart after a reboot (restart unless-stopped)."
  }

  assert {
    condition     = output.control_plane_ips_per_region == { "region-a" = "172.30.1.10", "region-b" = "172.30.2.10" }
    error_message = "control_plane_ips_per_region must map each region to its control-plane machine."
  }

  assert {
    condition     = output.load_balancer_ip == "172.30.1.10" && output.control_plane_ip == "172.30.1.10" && output.console_load_balancer_ip == "172.30.1.10"
    error_message = "the front doors must be the primary control-plane machine's own address."
  }
}

run "control_plane_user_data_holds_the_kit_and_the_substitutes" {
  command = plan

  assert {
    condition     = alltrue([for k, m in docker_container.control_plane : can(yamldecode(nonsensitive(one([for u in m.upload : u.content if u.file == "/var/lib/cloud/seed/nocloud/user-data"]))))])
    error_message = "the control-plane user-data must stay valid YAML after the kit path and the substitutes are set."
  }

  assert {
    condition     = alltrue([for k, m in docker_container.control_plane : length(regexall("(?m)^ +path: \\./clusters/_template/providers/local/kit$", nonsensitive(one([for u in m.upload : u.content if u.file == "/var/lib/cloud/seed/nocloud/user-data"])))) == 1])
    error_message = "the Flux Kustomization bootstrap-kit must point at the reduced kit."
  }

  assert {
    condition     = alltrue([for k, m in docker_container.control_plane : length(regexall("(?m)^ +path: \\./clusters/_template/bootstrap-kit$", nonsensitive(one([for u in m.upload : u.content if u.file == "/var/lib/cloud/seed/nocloud/user-data"])))) == 0])
    error_message = "no Flux Kustomization may point at the whole kit."
  }

  assert {
    condition     = alltrue([for k, m in docker_container.control_plane : strcontains(nonsensitive(one([for u in m.upload : u.content if u.file == "/var/lib/cloud/seed/nocloud/user-data"])), "\n            SOVEREIGN_CNPG_STORAGE_CLASS: \"local-path\"\n")])
    error_message = "the ConfigMap sovereign-variables must name the default storage class (the storage gate waits for it)."
  }

  assert {
    condition     = alltrue([for k, m in docker_container.control_plane : strcontains(nonsensitive(one([for u in m.upload : u.content if u.file == "/var/lib/cloud/seed/nocloud/user-data"])), "\n            POWERDNS_DNSDIST_HOSTPORT: \"true\"\n")])
    error_message = "the DNS server must bind port 53 on the machine's own address."
  }

  assert {
    condition     = strcontains(nonsensitive(one([for u in docker_container.control_plane["region-a-cp1"].upload : u.content if u.file == "/var/lib/cloud/seed/nocloud/user-data"])), "\n            CILIUM_LB_IPAM_CIDRS: \"198.51.100.10/32\"\n") && strcontains(nonsensitive(one([for u in docker_container.control_plane["region-b-cp1"].upload : u.content if u.file == "/var/lib/cloud/seed/nocloud/user-data"])), "\n            CILIUM_LB_IPAM_CIDRS: \"198.51.100.11/32\"\n")
    error_message = "the load-balancer pool of each region must be one address of external_cidr, never the machine's own address."
  }

  assert {
    condition     = strcontains(nonsensitive(one([for u in docker_container.control_plane["region-a-cp1"].upload : u.content if u.file == "/var/lib/cloud/seed/nocloud/user-data"])), "\n            SECONDARY_HR_SUSPEND: \"false\"\n") && strcontains(nonsensitive(one([for u in docker_container.control_plane["region-b-cp1"].upload : u.content if u.file == "/var/lib/cloud/seed/nocloud/user-data"])), "\n            SECONDARY_HR_SUSPEND: \"true\"\n")
    error_message = "the primary-only releases must run in the primary region and be suspended in the secondary."
  }

  assert {
    condition     = alltrue([for k, m in docker_container.control_plane : strcontains(nonsensitive(one([for u in m.upload : u.content if u.file == "/var/lib/cloud/seed/nocloud/user-data"])), "\n            PROVIDER: local\n")])
    error_message = "the kit key PROVIDER must be local."
  }
}

run "workers_join_their_region" {
  command = plan

  assert {
    condition     = strcontains(nonsensitive(one([for u in docker_container.worker["region-b-w1"].upload : u.content if u.file == "/var/lib/cloud/seed/nocloud/user-data"])), "server: https://172.30.2.10:6443\n")
    error_message = "a worker must join the control-plane machine of its own region."
  }

  assert {
    condition     = [for n in docker_container.worker["region-b-w1"].networks_advanced : n.ipv4_address] == ["172.30.2.20"]
    error_message = "the first worker of a region must have host 20 of the /24 of its region."
  }
}

run "network_registry_has_its_form" {
  command = plan

  assert {
    condition     = jsondecode(output.network_registry).podSupernet == "10.42.0.0/15" && jsondecode(output.network_registry).regionSeparation == "none"
    error_message = "two regions must give the pod supernet 10.42.0.0/15 and region separation none."
  }

  assert {
    condition     = [for r in jsondecode(output.network_registry).regions : r.meshEndpoint.ips[0]] == ["172.30.1.10", "172.30.2.10"]
    error_message = "with two regions every region must have a mesh endpoint on its control-plane machine."
  }

  assert {
    condition     = [for r in jsondecode(output.network_registry).regions : r.clusterID] == [1, 2] && length(distinct([for r in jsondecode(output.network_registry).regions : r.clusterName])) == 2
    error_message = "the regions must have distinct mesh cluster names and the identifiers 1 and 2."
  }
}

run "one_region_has_no_mesh_endpoint" {
  command = plan

  variables {
    regions = jsondecode(file("../_contract/fixtures/single-region.tfvars.json")).regions
  }

  assert {
    condition     = length(jsondecode(output.network_registry).regions) == 1 && !contains(keys(jsondecode(output.network_registry).regions[0]), "meshEndpoint")
    error_message = "one region must give one registry entry without a mesh endpoint."
  }
}

run "a_given_machine_image_is_used_and_nothing_is_built" {
  command = plan

  variables {
    machine_image = "registry.example.test/machine@sha256:1111111111111111111111111111111111111111111111111111111111111111"
  }

  assert {
    condition     = length(docker_image.machine) == 0
    error_message = "with machine_image set the module must not build an image."
  }

  assert {
    condition     = alltrue([for m in merge(docker_container.control_plane, docker_container.worker) : m.image == "registry.example.test/machine@sha256:1111111111111111111111111111111111111111111111111111111111111111"])
    error_message = "every machine must run the given machine_image."
  }
}
