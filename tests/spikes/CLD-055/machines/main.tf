# CLD-055 spike, machine kind A: privileged containers that run systemd and
# cloud-init, started with the kreuzwerker/docker OpenTofu provider. Each
# machine boots the output of the shared template through cloud-init's NoCloud
# data source (the user-data is written into /var/lib/cloud/seed/nocloud).

terraform {
  required_version = ">= 1.8.0"
  required_providers {
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 3.6"
    }
  }
}

provider "docker" {
  host = "unix:///var/run/docker.sock"
}

variable "shape" {
  description = "single = one control plane (L1, L3, L4, L7); two-region = two control planes (L6); diag = one control plane whose external address is its own (root cause of run 43358110034)."
  type        = string
  default     = "single"
}

variable "image" {
  type    = string
  default = "cld055-machine:local"
}

variable "registry_mirror_yaml" {
  type = string
}

locals {
  subnet  = "172.30.0.0/24"
  gateway = "172.30.0.1"
  # ext: the address the Huawei branch treats as the node's EIP. Like an EIP
  # it is not on any interface of the machine (documentation range).
  machines = var.shape == "two-region" ? {
    "cp-a" = { ip = "172.30.0.10", ext = "198.51.100.10", region_index = 0 }
    "cp-b" = { ip = "172.30.0.20", ext = "198.51.100.20", region_index = 1 }
    } : var.shape == "diag" ? {
    "cp-a" = { ip = "172.30.0.10", ext = "172.30.0.10", region_index = -1 }
    } : {
    "cp-a" = { ip = "172.30.0.10", ext = "198.51.100.10", region_index = -1 }
  }
}

module "render" {
  source   = "../render"
  for_each = local.machines

  node_ip                 = each.value.ip
  external_ip             = each.value.ext
  two_region_index        = each.value.region_index
  object_storage_endpoint = "http://seaweedfs-s3.seaweedfs.svc.cluster.local:8333"
  catalyst_api_url        = "http://${local.gateway}:8089"
  registry_mirror_yaml    = var.registry_mirror_yaml
  pdns_api_host           = "powerdns.example.test"
}

resource "docker_network" "lab" {
  name = "cld055"
  ipam_config {
    subnet  = local.subnet
    gateway = local.gateway
  }
}

# containerd's overlay snapshotter cannot sit on the container's own overlay
# root, so the k3s and kubelet state live on volumes (as the k3s image does).
resource "docker_volume" "rancher" {
  for_each = local.machines
  name     = "cld055-${each.key}-rancher"
}

resource "docker_volume" "kubelet" {
  for_each = local.machines
  name     = "cld055-${each.key}-kubelet"
}

resource "docker_container" "machine" {
  for_each = local.machines

  name        = "cld055-${each.key}"
  hostname    = each.key
  image       = var.image
  privileged  = true
  restart     = "unless-stopped"
  stop_signal = "SIGRTMIN+3"
  env         = ["container=docker"]
  must_run    = true
  wait        = false

  tmpfs = {
    "/run"      = "rw,exec,mode=755"
    "/run/lock" = "rw,mode=1777"
  }

  networks_advanced {
    name         = docker_network.lab.name
    ipv4_address = each.value.ip
  }

  volumes {
    volume_name    = docker_volume.rancher[each.key].name
    container_path = "/var/lib/rancher"
  }

  volumes {
    volume_name    = docker_volume.kubelet[each.key].name
    container_path = "/var/lib/kubelet"
  }

  mounts {
    type      = "bind"
    source    = "/lib/modules"
    target    = "/lib/modules"
    read_only = true
  }

  # The machine driver's socket (the profile's runner_socket). L4 hands it on
  # to the provider pod through the runtime configuration.
  mounts {
    type   = "bind"
    source = "/var/run/docker.sock"
    target = "/opt/runner/docker.sock"
  }

  upload {
    file    = "/var/lib/cloud/seed/nocloud/user-data"
    content = module.render[each.key].user_data
  }

  upload {
    file    = "/var/lib/cloud/seed/nocloud/meta-data"
    content = "instance-id: cld055-${each.key}\nlocal-hostname: ${each.key}\n"
  }
}

output "machines" {
  value = { for k, v in local.machines : k => v.ip }
}

output "user_data_sha256" {
  value = { for k, m in module.render : k => m.user_data_sha256 }
}

output "user_data_bytes" {
  value = { for k, m in module.render : k => m.user_data_bytes }
}

output "user_data" {
  value = { for k, m in module.render : k => m.user_data }
}
