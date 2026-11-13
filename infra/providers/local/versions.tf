# The local provider pack: machines are privileged containers on the host
# that runs this module (a CI runner). They run systemd and cloud-init and
# boot from the NoCloud seed directory. No cloud account is used.

terraform {
  required_version = ">= 1.8.0"

  required_providers {
    docker = {
      source  = "kreuzwerker/docker"
      version = "~> 3.9"
    }

    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }
}

provider "docker" {
  host = var.runner_socket
}
