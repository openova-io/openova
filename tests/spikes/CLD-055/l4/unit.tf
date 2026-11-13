# CLD-055 spike, L4: the one-machine unit module that a Workspace inside the
# local cluster runs. It drives the runner's container engine through the
# socket that the runtime configuration mounts into the provider pod, and
# starts `replicas` worker machines of kind A that join with the agent token.
terraform {
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

variable "user_data_b64" {
  type = string
}

variable "replicas" {
  type    = number
  default = 1
}

resource "docker_volume" "rancher" {
  count = var.replicas
  name  = "cld055-w${count.index + 1}-rancher"
}

resource "docker_volume" "kubelet" {
  count = var.replicas
  name  = "cld055-w${count.index + 1}-kubelet"
}

resource "docker_container" "worker" {
  count = var.replicas

  name        = "cld055-w${count.index + 1}"
  hostname    = "w${count.index + 1}"
  image       = "cld055-machine:local"
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
    name         = "cld055"
    ipv4_address = "172.30.0.${11 + count.index}"
  }

  volumes {
    volume_name    = docker_volume.rancher[count.index].name
    container_path = "/var/lib/rancher"
  }

  volumes {
    volume_name    = docker_volume.kubelet[count.index].name
    container_path = "/var/lib/kubelet"
  }

  mounts {
    type      = "bind"
    source    = "/lib/modules"
    target    = "/lib/modules"
    read_only = true
  }

  upload {
    file    = "/var/lib/cloud/seed/nocloud/user-data"
    content = replace(base64decode(var.user_data_b64), "WORKER_IP", "172.30.0.${11 + count.index}")
  }

  upload {
    file    = "/var/lib/cloud/seed/nocloud/meta-data"
    content = "instance-id: cld055-w${count.index + 1}\nlocal-hostname: w${count.index + 1}\n"
  }
}
