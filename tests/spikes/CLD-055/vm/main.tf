# CLD-055 spike, machine kind B: a KVM virtual machine from the Ubuntu 22.04
# cloud image, started with the dmacvicar/libvirt OpenTofu provider, booting
# the same render of the shared template through a NoCloud seed disk.

terraform {
  required_version = ">= 1.8.0"
  required_providers {
    libvirt = {
      source  = "dmacvicar/libvirt"
      version = "0.8.3"
    }
  }
}

provider "libvirt" {
  uri = "qemu:///system"
}

variable "registry_mirror_yaml" {
  type = string
}

variable "base_image_path" {
  description = "Local path of the Ubuntu cloud image (downloaded by the job)."
  type        = string
}

locals {
  ip      = "192.168.150.10"
  gateway = "192.168.150.1"
}

module "render" {
  source = "../render"

  node_ip                 = local.ip
  object_storage_endpoint = "http://seaweedfs-s3.seaweedfs.svc.cluster.local:8333"
  catalyst_api_url        = "http://${local.gateway}:8089"
  registry_mirror_yaml    = var.registry_mirror_yaml
  pdns_api_host           = "powerdns.example.test"
}

resource "libvirt_network" "lab" {
  name      = "cld055"
  mode      = "nat"
  addresses = ["192.168.150.0/24"]
  autostart = true
  dhcp {
    enabled = true
  }
  dns {
    enabled = true
  }
}

resource "libvirt_volume" "base" {
  name   = "cld055-jammy-base.qcow2"
  pool   = "default"
  source = var.base_image_path
  format = "qcow2"
}

resource "libvirt_volume" "root" {
  name           = "cld055-cp-a.qcow2"
  pool           = "default"
  base_volume_id = libvirt_volume.base.id
  size           = 40 * 1024 * 1024 * 1024
}

resource "libvirt_cloudinit_disk" "seed" {
  name      = "cld055-cp-a-seed.iso"
  pool      = "default"
  user_data = module.render.user_data
  meta_data = "instance-id: cld055-cp-a\nlocal-hostname: cp-a\n"
}

resource "libvirt_domain" "cp" {
  name      = "cld055-cp-a"
  memory    = 8192
  vcpu      = 4
  autostart = true
  cloudinit = libvirt_cloudinit_disk.seed.id

  cpu {
    mode = "host-passthrough"
  }

  network_interface {
    network_id     = libvirt_network.lab.id
    hostname       = "cp-a"
    addresses      = [local.ip]
    wait_for_lease = false
  }

  disk {
    volume_id = libvirt_volume.root.id
  }

  # The provider adds a SPICE display when none is given; the runner's QEMU
  # (10.2.1 on Ubuntu 26.04) is built without SPICE (run 43357201647).
  graphics {
    type           = "vnc"
    listen_type    = "address"
    listen_address = "127.0.0.1"
  }

  console {
    type        = "file"
    target_port = "0"
    target_type = "serial"
    source_path = "/var/log/libvirt/qemu/cld055-cp-a-serial.log"
  }
}

output "ip" {
  value = local.ip
}

output "user_data_sha256" {
  value = module.render.user_data_sha256
}
