# SOV-148 lab only, never merged: each control plane boots from a fetched,
# checksum-pinned bundle of its own runcmd.
locals {
  lab_origin_ip = cidrhost(var.machine_cidr, 250)
  lab_parts     = { for code, ud in local.control_plane_user_data : code => split("\nruncmd:\n", ud) }
  lab_tail      = { for code, p in local.lab_parts : code => split("\nfinal_message:", p[1]) }
  lab_items     = { for code, t in local.lab_tail : code => yamldecode("runcmd:\n${t[0]}").runcmd }
  # cloud-init's own shellify of runcmd: "#!/bin/sh", then each item and a newline.
  lab_script = { for code, it in local.lab_items : code => join("", concat(["#!/bin/sh\n"], [for x in it : "${x}\n"])) }

  lab_values = { for r in var.regions : r.code => [
    ["V_K3S_TOKEN", random_password.k3s_server_token[r.code].result, "$${V_K3S_TOKEN}", random_password.k3s_server_token[r.code].result],
    ["V_K3S_AGENT_TOKEN", random_password.k3s_agent_token[r.code].result, "$${V_K3S_AGENT_TOKEN}", random_password.k3s_agent_token[r.code].result],
    ["V_NODE_IP", local.control_plane_ip[r.code], "printf '%s' \"$V_NODE_IP\"", "printf '%s' '${local.control_plane_ip[r.code]}'"],
    ["V_NODE_EXTERNAL_IP", local.external_ip[r.code], "printf '%s' \"$V_NODE_EXTERNAL_IP\"", "printf '%s' '${local.external_ip[r.code]}'"],
    ["V_K3S_EXTRA_ARGS", "--node-label=topology.kubernetes.io/region=${r.code} --node-label=topology.kubernetes.io/zone=${r.code}", "$${V_K3S_EXTRA_ARGS}", "--node-label=topology.kubernetes.io/region=${r.code} --node-label=topology.kubernetes.io/zone=${r.code}"],
    ["V_REGION_LABEL", local.region_label[r.code], "$${V_REGION_LABEL}", local.region_label[r.code]],
    ["V_CLUSTER_CIDR", local.region_pod_cidr[r.code], "$${V_CLUSTER_CIDR}", local.region_pod_cidr[r.code]],
    ["V_SERVICE_CIDR", local.region_service_cidr[r.code], "$${V_SERVICE_CIDR}", local.region_service_cidr[r.code]],
    ["V_SOVEREIGN_FQDN", var.sovereign_fqdn, "$${V_SOVEREIGN_FQDN}", var.sovereign_fqdn],
    ["V_K3S_TAG", replace(var.k3s_version, "+", "-"), "$${V_K3S_TAG}", replace(var.k3s_version, "+", "-")],
    ["V_K3S_VERSION", var.k3s_version, "$${V_K3S_VERSION}", var.k3s_version],
    ["V_GITOPS_REPO_URL", var.gitops_repo_url, "$${V_GITOPS_REPO_URL}", var.gitops_repo_url],
    ["V_GITOPS_BRANCH", var.gitops_branch, "$${V_GITOPS_BRANCH}", var.gitops_branch],
    ["V_GHCR_USER", local.ghcr_pull_username, "$${V_GHCR_USER}", local.ghcr_pull_username],
    ["V_GHCR_TOKEN", var.ghcr_pull_token, "$${V_GHCR_TOKEN}", var.ghcr_pull_token],
  ] }
  # the text each value is found as (the value itself, or the provider command that prints it)
  lab_find = { for code, vs in local.lab_values : code => [for v in vs : v[3]] }
  lab_s0 = { for code, s in local.lab_script : code => local.lab_values[code][0][1] == "" ? s : replace(s, local.lab_find[code][0], local.lab_values[code][0][2]) }
  lab_s1 = { for code, s in local.lab_s0 : code => local.lab_values[code][1][1] == "" ? s : replace(s, local.lab_find[code][1], local.lab_values[code][1][2]) }
  lab_s2 = { for code, s in local.lab_s1 : code => local.lab_values[code][2][1] == "" ? s : replace(s, local.lab_find[code][2], local.lab_values[code][2][2]) }
  lab_s3 = { for code, s in local.lab_s2 : code => local.lab_values[code][3][1] == "" ? s : replace(s, local.lab_find[code][3], local.lab_values[code][3][2]) }
  lab_s4 = { for code, s in local.lab_s3 : code => local.lab_values[code][4][1] == "" ? s : replace(s, local.lab_find[code][4], local.lab_values[code][4][2]) }
  lab_s5 = { for code, s in local.lab_s4 : code => local.lab_values[code][5][1] == "" ? s : replace(s, local.lab_find[code][5], local.lab_values[code][5][2]) }
  lab_s6 = { for code, s in local.lab_s5 : code => local.lab_values[code][6][1] == "" ? s : replace(s, local.lab_find[code][6], local.lab_values[code][6][2]) }
  lab_s7 = { for code, s in local.lab_s6 : code => local.lab_values[code][7][1] == "" ? s : replace(s, local.lab_find[code][7], local.lab_values[code][7][2]) }
  lab_s8 = { for code, s in local.lab_s7 : code => local.lab_values[code][8][1] == "" ? s : replace(s, local.lab_find[code][8], local.lab_values[code][8][2]) }
  lab_s9 = { for code, s in local.lab_s8 : code => local.lab_values[code][9][1] == "" ? s : replace(s, local.lab_find[code][9], local.lab_values[code][9][2]) }
  lab_s10 = { for code, s in local.lab_s9 : code => local.lab_values[code][10][1] == "" ? s : replace(s, local.lab_find[code][10], local.lab_values[code][10][2]) }
  lab_s11 = { for code, s in local.lab_s10 : code => local.lab_values[code][11][1] == "" ? s : replace(s, local.lab_find[code][11], local.lab_values[code][11][2]) }
  lab_s12 = { for code, s in local.lab_s11 : code => local.lab_values[code][12][1] == "" ? s : replace(s, local.lab_find[code][12], local.lab_values[code][12][2]) }
  lab_s13 = { for code, s in local.lab_s12 : code => local.lab_values[code][13][1] == "" ? s : replace(s, local.lab_find[code][13], local.lab_values[code][13][2]) }
  lab_s14 = { for code, s in local.lab_s13 : code => local.lab_values[code][14][1] == "" ? s : replace(s, local.lab_find[code][14], local.lab_values[code][14][2]) }
  lab_bundle = local.lab_s14
  lab_env = { for code, vs in local.lab_values : code => join("", [for v in vs : "${v[0]}='${v[1]}'\n"]) }
  lab_sha = { for code, b in local.lab_bundle : code => sha256(b) }
  lab_tag = { for code, s in local.lab_sha : code => substr(s, 0, 12) }
  lab_stub = { for code, b in local.lab_bundle : code => join("", [
    "echo \"SOV148-T $(date +%s.%N) STUB\"\n",
    "umask 077; cat >/var/lib/catalyst/boot.env <<'BOOTENV'\n", local.lab_env[code], "BOOTENV\n", "umask 022\n",
    "sed 's|^ORIGIN_REGISTRY_HOST=.*|ORIGIN_REGISTRY_HOST=${local.lab_origin_ip}:5000|;s|^ORIGIN_REGISTRY_SCHEME=.*|ORIGIN_REGISTRY_SCHEME=http|' /var/lib/catalyst/variables.env >/var/lib/catalyst/lab-origin.env\n",
    "sed 's|/var/lib/catalyst/variables.env|/var/lib/catalyst/lab-origin.env|' /var/lib/catalyst/oci_fetch >/var/lib/catalyst/lab-oci_fetch\n",
    "cp /etc/hosts /var/lib/catalyst/hosts.before\n",
    "echo '0.0.0.0 raw.githubusercontent.com github.com codeload.github.com objects.githubusercontent.com get.k3s.io' >>/etc/hosts\n",
    "f=/var/lib/catalyst/cp-boot.sh\n",
    "for i in 1 2 3 4 5; do sh /var/lib/catalyst/lab-oci_fetch cp-boot ${local.lab_tag[code]} $f https://raw.githubusercontent.com/openova-io/openova/1af34ee3c13513685e500710470da7410f61e6f5/infra/providers/local/cp-boot.sh && echo \"${local.lab_sha[code]}  $f\" | sha256sum -c - && break; rm -f $f; [ \"$i\" = 5 ] && { touch /var/lib/catalyst/cp-boot-FAILED; exit 80; }; sleep $((i*5)); done\n",
    "cp /var/lib/catalyst/hosts.before /etc/hosts\n",
    "echo \"SOV148-T $(date +%s.%N) FETCHED\"\n",
    "set -a; . /var/lib/catalyst/boot.env; set +a\n",
    ". $f\n",
  ]) }
  lab_user_data = { for code, p in local.lab_parts : code => join("", [
    p[0], "\nruncmd:\n  - |\n    ", replace(trimsuffix(local.lab_stub[code], "\n"), "\n", "\n    "), "\n",
    "\nfinal_message:", local.lab_tail[code][1],
  ]) }
  lab_by_sha = { for s in distinct(values(local.lab_sha)) : s => [for c, b in local.lab_bundle : b if local.lab_sha[c] == s][0] }
  lab_objects = merge(
    { for s, b in local.lab_by_sha : "/srv/blobs/${s}" => b },
    { for s, b in local.lab_by_sha : "/srv/manifests/cp-boot/${substr(s, 0, 12)}" => jsonencode({
      schemaVersion = 2
      mediaType     = "application/vnd.oci.image.manifest.v1+json"
      config        = { mediaType = "application/vnd.oci.empty.v1+json", digest = "sha256:44136fa355b3678a1146ad16f7e8649e94fb4fc21fe77e8310c060f61caaff8a", size = 2 }
      layers        = [{ mediaType = "application/vnd.oci.image.layer.v1.tar", digest = "sha256:${s}", size = length(b) }]
    }) },
  )
}

resource "docker_container" "lab_origin" {
  name     = "${local.name_prefix}-lab-origin"
  image    = local.machine_image
  command  = ["python3", "/srv/origin.py"]
  must_run = true
  wait     = false

  networks_advanced {
    name         = docker_network.machines.name
    ipv4_address = local.lab_origin_ip
  }

  upload {
    file    = "/srv/origin.py"
    content = file("${path.module}/lab/origin.py")
  }

  dynamic "upload" {
    for_each = local.lab_objects
    content {
      file    = upload.key
      content = upload.value
    }
  }
}
