# Inputs of the local provider pack.
#
# The variables up to and including the map `variables` are the 33 variables
# of contract version 1 (infra/providers/_contract/contract.json). The map
# `variables` is the variables document of contract C5, with the validation
# the other modules use. The variables after it belong to this pack alone.

variable "deployment_id" {
  type        = string
  description = "Deployment identifier: 16 hexadecimal characters. The first 8 are part of the name prefix of every machine, volume and network."
  validation {
    condition     = can(regex("^[0-9a-f]{16}$", var.deployment_id))
    error_message = "deployment_id must be 16 lower-case hexadecimal characters."
  }
}

variable "sovereign_fqdn" {
  type        = string
  description = "Fully qualified name of the Sovereign."
  validation {
    condition     = can(regex("^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$", var.sovereign_fqdn))
    error_message = "sovereign_fqdn must be a lower-case DNS name with at least two labels."
  }
}

variable "org_name" {
  type        = string
  description = "Name of the owning organisation."
}

variable "org_email" {
  type        = string
  description = "Contact address of the owning organisation."
  validation {
    condition     = can(regex("^[^@ ]+@[^@ ]+$", var.org_email))
    error_message = "org_email must be an e-mail address."
  }
}

variable "ssh_public_key" {
  type        = string
  description = "Break-glass public key. It is written into the meta-data of every machine."
  validation {
    condition     = can(regex("^ssh-[a-z0-9-]+ ", var.ssh_public_key))
    error_message = "ssh_public_key must be an OpenSSH public key."
  }
}

variable "regions" {
  type = list(object({
    code               = string
    cloud_region       = string
    availability_zone  = optional(string, "")
    role               = string
    control_plane_size = string
    worker_size        = string
    worker_count       = number
  }))
  description = "One entry per region; index 0 is the primary. Each region is one cluster: one control-plane machine and worker_count worker machines."
  validation {
    condition     = length(var.regions) >= 1 && var.regions[0].role == "primary"
    error_message = "regions must have at least one entry and regions[0].role must be \"primary\"."
  }
  validation {
    condition     = alltrue([for r in var.regions : contains(["primary", "secondary"], r.role)])
    error_message = "regions[].role must be primary or secondary."
  }
  validation {
    condition     = length(distinct([for r in var.regions : r.code])) == length(var.regions)
    error_message = "regions[].code must be unique."
  }
  # The pod supernet 10.40.0.0/13 holds the pod ranges of at most six regions.
  validation {
    condition     = length(var.regions) <= 6
    error_message = "At most six regions: the pod supernet holds the pod ranges 10.42.0.0/16 to 10.47.0.0/16."
  }
  # The profile says multiCloudRegion: false. Every machine runs on one host.
  validation {
    condition     = length(distinct([for r in var.regions : r.cloud_region])) == 1
    error_message = "Every regions[].cloud_region must be the same: the local pack runs every region on one host."
  }
  validation {
    condition     = alltrue([for r in var.regions : r.worker_count >= 0 && r.worker_count <= 9 && floor(r.worker_count) == r.worker_count])
    error_message = "regions[].worker_count must be a whole number from 0 to 9."
  }
}

variable "parent_domains_yaml" {
  type        = string
  description = "Parent domains as a YAML inline list. Empty: one entry, the Sovereign's own name, with role primary."
  default     = ""
}

variable "gitops_repo_url" {
  type        = string
  description = "Git source that Flux reads the kit from."
}

variable "gitops_branch" {
  type        = string
  description = "Branch of gitops_repo_url."
  default     = "main"
}

variable "catalyst_api_url" {
  type        = string
  description = "Address of the provisioning API that receives the call-backs of the boot script."
}

variable "handover_jwt_public_key" {
  type        = string
  description = "Key the new Sovereign uses to verify the hand-over token."
  default     = ""
  sensitive   = true
}

variable "kubeconfig_bearer_token" {
  type        = string
  description = "One-time token for the kubeconfig call-back."
  default     = ""
  sensitive   = true
}

variable "ghcr_pull_token" {
  type        = string
  description = "Registry pull credential."
  default     = ""
  sensitive   = true
}

variable "harbor_robot_token" {
  type        = string
  description = "Registry pull credential."
  default     = ""
  sensitive   = true
}

variable "powerdns_api_key" {
  type        = string
  description = "DNS API key."
  default     = ""
  sensitive   = true
}

variable "pdm_basic_auth_user" {
  type        = string
  description = "Domain pool manager user."
  default     = ""
  sensitive   = true
}

variable "pdm_basic_auth_pass" {
  type        = string
  description = "Domain pool manager password."
  default     = ""
  sensitive   = true
}

variable "k3s_version" {
  type        = string
  description = "k3s release that every machine installs."
  default     = "v1.31.8+k3s1"
  validation {
    condition     = can(regex("^v[0-9]+\\.[0-9]+\\.[0-9]+\\+k3s[0-9]+$", var.k3s_version))
    error_message = "k3s_version must look like v1.31.8+k3s1."
  }
}

variable "default_storage_class" {
  type        = string
  description = "Default storage class of the Sovereign, written from the profile. For this pack it is local-path, which the kit installs."
  validation {
    condition     = var.default_storage_class != ""
    error_message = "default_storage_class must not be empty."
  }
}

variable "provider_api_cidrs" {
  type        = list(string)
  description = "Cloud API address ranges allowed during the egress proof. The local pack has no cloud API."
  default     = []
}

variable "ssh_allowed_cidrs" {
  type        = list(string)
  description = "Sources allowed to reach node SSH. The machines sit on a private network of the host and have no firewall of their own."
  default     = []
}

variable "bcp_topology" {
  type        = string
  description = "Topology choice of the Sovereign."
  default     = "single-region"
}

variable "cluster_mesh_name" {
  type        = string
  description = "Mesh cluster name of the primary region. Empty: derived from the Sovereign's first label and the region code."
  default     = ""
}

variable "cluster_mesh_id" {
  type        = number
  description = "Mesh cluster identifier of the primary region. 0: 1. Region i gets the identifier of the primary plus i."
  default     = 0
  validation {
    condition     = var.cluster_mesh_id >= 0 && var.cluster_mesh_id <= 250
    error_message = "cluster_mesh_id must be from 0 to 250."
  }
}

variable "console_isolation_enabled" {
  type        = string
  description = "\"true\": the console has its own front door, on the console gateway ports of the primary control-plane machine."
  default     = "true"
  validation {
    condition     = contains(["true", "false"], var.console_isolation_enabled)
    error_message = "console_isolation_enabled must be \"true\" or \"false\"."
  }
}

variable "enable_fail2ban" {
  type        = bool
  description = "Node hardening on the control-plane machines."
  default     = true
}

variable "enable_unattended_upgrades" {
  type        = bool
  description = "Node hardening on the control-plane machines."
  default     = true
}

variable "enable_hot_standby" {
  type        = string
  description = "Database option."
  default     = "false"
  validation {
    condition     = contains(["true", "false"], var.enable_hot_standby)
    error_message = "enable_hot_standby must be \"true\" or \"false\"."
  }
}

variable "enable_shared_pg" {
  type        = string
  description = "Database option."
  default     = "true"
  validation {
    condition     = contains(["true", "false"], var.enable_shared_pg)
    error_message = "enable_shared_pg must be \"true\" or \"false\"."
  }
}

variable "marketplace_enabled" {
  type        = string
  description = "Storefront switch."
  default     = "true"
}

variable "qa_fixtures_enabled" {
  type        = string
  description = "Test fixtures switch."
  default     = "false"
  validation {
    condition     = contains(["true", "false"], var.qa_fixtures_enabled)
    error_message = "qa_fixtures_enabled must be \"true\" or \"false\"."
  }
}

variable "wildcard_cert_use_staging" {
  type        = string
  description = "\"true\": the wildcard certificate comes from the staging certificate authority."
  default     = "false"
}

variable "variables" {
  type        = map(string)
  description = "The variables document of this region (contract C5): every key of docs/contracts/c5-upstream-variables.schema.json and no other key. HANDOVER_AUTO ('true' or 'false') decides whether the Sovereign starts the hand-over flip by itself."
  validation {
    condition = keys(var.variables) == sort([
      "SOVEREIGN_MODE", "SOVEREIGN_CONSUME", "HANDOVER_AUTO", "HANDOVER_HOLD_SECONDS",
      "REGISTRY_HOST", "REGISTRY_SCHEME", "LOCAL_REGISTRY_HOST",
      "ORIGIN_REGISTRY_HOST", "ORIGIN_REGISTRY_SCHEME", "ORIGIN_REGISTRY_CA_B64", "ORIGIN_PROXY_ENDPOINTS_JSON",
      "REGISTRY_PULL_SECRET", "REGISTRY_PATH_CHARTS", "REGISTRY_PATH_RELEASE", "REGISTRY_PATH_PROVIDER_PACKS",
      "REGISTRY_PATH_XPKG", "REGISTRY_PATH_TOFU_PROVIDERS", "REGISTRY_PATH_NODE_ARTIFACTS", "REGISTRY_PROXY_PROJECTS_JSON",
      "REGISTRY_PATH_VULN_DB",
      "RELEASE_PUBLIC_KEY_B64", "RELEASE_CHANNEL", "RELEASE_VERSION", "RELEASE_AUTO", "RELEASE_SYNC_SCHEDULE",
      "ORIGIN_PARENT_FQDN", "ORIGIN_PARENT_API_URL", "DNS_MODE", "DNS_UPSTREAM_API_URL",
      "MAIL_MODE", "MAIL_RELAY_HOST", "MAIL_RELAY_PORT", "MAIL_FROM", "MAIL_RELAY_SECRET",
      "K3S_VERSION", "K3S_UPGRADE_CONCURRENCY", "CLOUD_CREDENTIAL_MODE", "CLOUD_CREDENTIAL_ROTATION_SCHEDULE",
      "CLOUD_CREDENTIAL_ROTATION_NONCE", "GITOPS_REPO_URL",
    ])
    error_message = "variables must hold exactly the keys of contract C5 (docs/contracts/c5-upstream-variables.schema.json), no more and no fewer."
  }
  validation {
    condition     = contains(["true", "false"], lookup(var.variables, "HANDOVER_AUTO", ""))
    error_message = "variables[\"HANDOVER_AUTO\"] must be the string 'true' or 'false'."
  }
}

# ── Inputs of this pack alone ────────────────────────────────────────────────

variable "runner_socket" {
  type        = string
  description = "Address of the container engine that runs the machines (the profile's credential field runner_socket)."
  default     = "unix:///var/run/docker.sock"
}

variable "machine_image" {
  type        = string
  description = "Image of every machine. Empty: the module builds the image from the folder image/ of this module."
  default     = ""
}

variable "machine_cidr" {
  type        = string
  description = "Address range of the machine network. Region i uses the /24 with number i + 1 inside it; its control-plane machine has host 10 and its workers hosts 20 and up."
  default     = "172.30.0.0/16"
  validation {
    condition     = can(cidrhost(var.machine_cidr, 0)) && tonumber(split("/", var.machine_cidr)[1]) <= 20
    error_message = "machine_cidr must be an IPv4 range of prefix length 20 or less."
  }
}

variable "external_cidr" {
  type        = string
  description = "Range of the addresses that stand in for a public address that is not on the machine. Region i gets host 10 + i. The load-balancer address pool of the region is that one address; it must never be the machine's own address."
  default     = "198.51.100.0/24"
  validation {
    condition     = can(cidrhost(var.external_cidr, 16))
    error_message = "external_cidr must be an IPv4 range with at least 17 addresses."
  }
}

variable "clustermesh_proxy_port" {
  type        = number
  description = "Host port of the mesh proxy on every control-plane machine. Below the node-port range, and not 2379 or 2380."
  default     = 12379
  validation {
    condition     = var.clustermesh_proxy_port >= 1 && var.clustermesh_proxy_port < 30000 && var.clustermesh_proxy_port != 2379 && var.clustermesh_proxy_port != 2380
    error_message = "clustermesh_proxy_port must be below 30000 (node ports are forbidden) and not 2379 or 2380."
  }
}

variable "registry_mirror_yaml" {
  type        = string
  description = "Body of /etc/rancher/k3s/registries.yaml on every machine. The default sends every pull to its own registry."
  default     = "mirrors: {}"
}

variable "object_storage_access_key" {
  type        = string
  description = "Access key of the in-kit object store, when it is known before the machines start. Empty: it is written after the object store has started."
  default     = ""
  sensitive   = true
}

variable "object_storage_secret_key" {
  type        = string
  description = "Secret key of the in-kit object store, when it is known before the machines start."
  default     = ""
  sensitive   = true
}
