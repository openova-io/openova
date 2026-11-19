# Regression tests for cloudinit-control-plane.tftpl render integrity (#4513).
#
# Two bugs wedged the keystone fresh prov 25aadcfc at 0 HelmReleases:
#   1. `kubectl create namespace catalyst-system openbao` (two NAMEs) — kubectl
#      accepts exactly one — died on the first runcmd item, halting the whole
#      runcmd block (no bootstrap secrets, no flux-bootstrap apply).
#   2. `%{ endif ~}` on the infrastructure-config `path:` stripped the trailing
#      newline → `prune: true` jammed onto the path line → invalid YAML.
#
# These tests render the template (no apply, no creds) for both cloud branches
# and assert neither defect re-appears.

run "hetzner_render_is_valid" {
  command = plan

  variables {
    provider_name = "hetzner"
  }

  assert {
    condition     = output.path_line_jammed == false
    error_message = "#4513 regression: infrastructure-config `path:` and `prune:` are on the same line (an endif tilde newline-strip bug) — invalid YAML."
  }

  assert {
    condition     = output.prune_on_own_line == true
    error_message = "#4513: `prune: true` must render on its own line."
  }

  assert {
    condition     = output.multi_name_create == false
    error_message = "#4513 regression: `kubectl create namespace catalyst-system openbao` passes two NAMEs — kubectl accepts exactly one; the runcmd block will die on the first item."
  }

  # Hetzner has a hard 32256B user-data cap; the production module enforces this
  # via a precondition. Keep the standalone render comfortably under it so this
  # harness fails fast if the template balloons.
  assert {
    condition     = output.rendered_bytes < 32256
    error_message = "Rendered cloud-init exceeds the Hetzner 32256B guardrail."
  }

  # #4521 — the Crossplane Provider + ProviderConfig MUST NOT be batched in one
  # Flux Kustomization (atomic dry-run deadlock). LAYER 1
  # (`infrastructure-providers`) is a child of the root (SOV-154) and points at the
  # providers/ tree; LAYER 2 (`infrastructure-config`) ships as a COMMITTED
  # Flux Kustomization CR (kept off the byte-capped cloud-init) that
  # `dependsOn`s LAYER 1.
  assert {
    condition     = output.has_providers_kustomization == true
    error_message = "#4521 regression: the `infrastructure-providers` Flux Kustomization (Provider-install LAYER 1) under clusters/_template/root is missing — the Provider + ProviderConfig got re-batched into one Kustomization → atomic dry-run deadlock."
  }

  assert {
    condition     = output.root.names == ["sovereign-root"] && output.root.path == "clusters/_template/providers/hetzner/root/primary" && output.root.folder
    error_message = "SOV-154: flux-bootstrap.yaml must hold one Flux Kustomization, sovereign-root, with the path ./clusters/_template/providers/hetzner/root/primary, a folder of the repository with a kustomization.yaml."
  }

  assert {
    condition     = output.providers_path_correct == true
    error_message = "#4521, CLD-023: `infrastructure-providers` must point its `path` at ./clusters/_template/infrastructure/providers (the Provider-only tree), the same path on every cloud."
  }

  assert {
    condition     = output.config_not_inlined == true
    error_message = "#4521 / #1981 / #3884 byte-cap regression: the `infrastructure-config` LAYER-2 Kustomization must NOT be re-inlined into cloud-init — it ships as a committed Flux Kustomization CR. A third inline Kustomization pushed the 3-region Hetzner render past the 32256B cap."
  }

  assert {
    condition     = output.config_depends_on_providers == true
    error_message = "#4521 regression: the committed `infrastructure-config` Flux Kustomization CR must `dependsOn: [infrastructure-providers]` so the Provider CRDs register before the ProviderConfig is dry-run."
  }

  assert {
    condition     = output.config_paths_correct == true
    error_message = "#4521: the committed `infrastructure-config` CR must point at the config tree ./clusters/_template/infrastructure."
  }

  # #7922: no node reboots itself. The control plane switched on the automatic
  # reboot of unattended-upgrades at 02:30 node time and rebooted a region's only
  # control plane mid-cutover. kured's node-patch-config owns that configuration.
  assert {
    condition     = !strcontains(output.rendered_cloud_init, "Automatic-Reboot") && !strcontains(output.rendered_cloud_init, "/etc/apt/apt.conf.d/") && !strcontains(output.rendered_cloud_init, "enable --now unattended-upgrades") && strcontains(output.rendered_cloud_init, "\n  - unattended-upgrades\n")
    error_message = "#7922 (hetzner branch): the control-plane cloud-init must write no unattended-upgrade configuration (no apt.conf.d file, no Automatic-Reboot) and must not enable the service itself; bp-kured's node-patch-config owns the configuration. The package unattended-upgrades must still be installed."
  }

  # SEC-020 — the k3s server starts with the server token and hands workers a
  # separate agent token. Workers never receive the server token.
  assert {
    condition     = strcontains(output.rendered_cloud_init, "K3S_TOKEN=render-test-server-token ")
    error_message = "SEC-020: the k3s server line must set K3S_TOKEN to the server token (k3s_server_token)."
  }

  assert {
    condition     = strcontains(output.rendered_cloud_init, " --agent-token=render-test-agent-token ")
    error_message = "SEC-020: the k3s server line must pass --agent-token with the agent token (k3s_agent_token)."
  }

  # NET-020 — Cilium replaces kube-proxy, so the k3s server starts without it.
  assert {
    condition     = strcontains(output.rendered_cloud_init, " --disable-network-policy --disable-kube-proxy ")
    error_message = "NET-020: the k3s server line must pass --disable-kube-proxy (Cilium runs with kubeProxyReplacement: true)."
  }

  # SEC-030 — the k3s server writes Secrets to etcd as secretbox ciphertext.
  assert {
    condition     = strcontains(output.rendered_cloud_init, "INSTALL_K3S_EXEC=\"server --cluster-init --secrets-encryption --secrets-encryption-provider=secretbox ")
    error_message = "SEC-030: the k3s server line must pass --secrets-encryption --secrets-encryption-provider=secretbox (etcd encryption at rest)."
  }

  # SOV-130 — the substituted values live in ConfigMap
  # flux-system/sovereign-variables, which the root Kustomization reads
  # with postBuild.substituteFrom. No Kustomization keeps an inline
  # substitute map, and the DNS-01 API key is only in the Secret
  # flux-system/sovereign-tls-secrets.
  assert {
    condition     = strcontains(output.flux_bootstrap_yaml, "\nkind: ConfigMap\nmetadata:\n  name: sovereign-variables\n  namespace: flux-system\ndata:\n")
    error_message = "SOV-130: the ConfigMap flux-system/sovereign-variables is missing from flux-bootstrap.yaml."
  }

  assert {
    condition     = length(regexall("(?m)^        name: sovereign-variables$", output.flux_bootstrap_yaml)) == 1
    error_message = "SOV-130, SOV-154: the root Kustomization must list ConfigMap sovereign-variables under postBuild.substituteFrom."
  }

  assert {
    condition     = length(regexall("(?m)^[ ]*substitute:", output.flux_bootstrap_yaml)) == 0
    error_message = "SOV-130: an inline Flux Kustomization still carries a postBuild.substitute map; its values belong in ConfigMap sovereign-variables."
  }

  assert {
    condition     = !strcontains(output.flux_bootstrap_yaml, "POWERDNS_API_KEY") && length(regexall("POWERDNS_API_KEY:", output.rendered_cloud_init)) == 1 && strcontains(output.rendered_cloud_init, "        name: sovereign-tls-secrets\n        namespace: flux-system\n      type: Opaque\n      stringData:\n        POWERDNS_API_KEY: \"${base64encode("render-test-pdns-key")}\"\n")
    error_message = "SOV-130: POWERDNS_API_KEY must appear once, as the one key of the Secret flux-system/sovereign-tls-secrets, never in the ConfigMap sovereign-variables."
  }

  assert {
    condition     = strcontains(output.rendered_cloud_init, " -f cloud-credentials-secret.yaml -f sovereign-tls-secrets.yaml && shred -u ")
    error_message = "SOV-130: the Secret file sovereign-tls-secrets.yaml must be applied with the other bootstrap Secrets."
  }

  # SEC-040 — bootstrap secret hygiene on the node. The kubeconfig and
  # flux-bootstrap.yaml have mode 0600; the seven bootstrap Secret files are
  # shredded once their apply succeeded, flux-bootstrap.yaml after its
  # apply; the robot token is reflected into catalyst-system only; the
  # template creates no OpenBao recovery seed (the bp-openbao chart does).
  assert {
    condition     = strcontains(output.rendered_cloud_init, " --write-kubeconfig-mode=0600\" \\\n") && !strcontains(output.rendered_cloud_init, "--write-kubeconfig-mode=0644") && strcontains(output.rendered_cloud_init, "\n  - path: /var/lib/catalyst/flux-bootstrap.yaml\n    permissions: '0600'\n")
    error_message = "SEC-040: the k3s server line must pass --write-kubeconfig-mode=0600, and /var/lib/catalyst/flux-bootstrap.yaml must have mode 0600."
  }

  assert {
    condition     = strcontains(output.rendered_cloud_init, " -f sovereign-tls-secrets.yaml && shred -u ghcr-pull-secret.yaml harbor-robot-token-secret.yaml pdm-basicauth-secret.yaml handover-jwt-public-secret.yaml object-storage-secret.yaml cloud-credentials-secret.yaml sovereign-tls-secrets.yaml\n    (exit $?)\n") && length(regexall("kubectl apply -f /var/lib/catalyst/flux-bootstrap\\.yaml \\|\\| \\{[^\n]*exit 92; \\}\n    shred -u /var/lib/catalyst/flux-bootstrap\\.yaml\n", output.rendered_cloud_init)) == 1
    error_message = "SEC-040: the seven bootstrap Secret files must be shredded on the line of their apply once it succeeded, a failed apply must stop the script on the next line, (exit $?), and flux-bootstrap.yaml must be shredded on the line after its apply."
  }

  assert {
    condition     = strcontains(output.boot_text, "\n  name: harbor-robot-token\n  namespace: flux-system\n  annotations:\n    reflector.v1.k8s.emberstack.com/reflection-allowed: \"true\"\n    reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: \"catalyst-system\"\n    reflector.v1.k8s.emberstack.com/reflection-auto-enabled: \"true\"\n    reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: \"catalyst-system\"\n")
    error_message = "SEC-040: the Secret flux-system/harbor-robot-token must be reflected into catalyst-system and into no other namespace."
  }

  assert {
    condition     = !strcontains(output.boot_text, "openbao-recovery-seed")
    error_message = "SEC-040: cloud-init must not create the Secret openbao-recovery-seed; the bp-openbao chart creates it."
  }

  # SOV-140 — before the post-back, cloud-init creates
  # ServiceAccount kube-system/parent-bootstrap, its cluster-admin binding and
  # its token Secret, and writes the kubeconfig the post-back sends; right
  # after the post-back it deletes that file.
  assert {
    condition     = strcontains(output.rendered_cloud_init, "    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml N=parent-bootstrap S=kube-system K=/var/lib/catalyst/parent-bootstrap.kubeconfig\n") && strcontains(output.rendered_cloud_init, "    roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin}\n    subjects: [{kind: ServiceAccount, name: $N, namespace: $S}]\n") && strcontains(output.rendered_cloud_init, "    type: kubernetes.io/service-account-token\n    metadata: {name: $N-token, namespace: $S, annotations: {kubernetes.io/service-account.name: $N}}\n")
    error_message = "SOV-140: the ServiceAccount kube-system/parent-bootstrap, its cluster-admin binding or its token Secret parent-bootstrap-token is missing."
  }

  assert {
    condition     = length(regexall("(?s)K=/var/lib/catalyst/parent-bootstrap\\.kubeconfig\n.*config set-credentials default --token=.*\n  - rm -f /var/lib/catalyst/parent-bootstrap\\.kubeconfig\n", output.rendered_cloud_init)) == 1
    error_message = "SOV-140: the parent-bootstrap kubeconfig must be written before the post-back and deleted right after it."
  }

  # SOV-133 — the template takes the variables document as its input
  # `variables`; the kit key FIRE_CUTOVER_ON_HANDOVER takes the value of its
  # key HANDOVER_AUTO ("false" in render.tf).
  assert {
    condition     = strcontains(output.flux_bootstrap_yaml, "\n      FIRE_CUTOVER_ON_HANDOVER: \"false\"\n")
    error_message = "SOV-133: the kit key FIRE_CUTOVER_ON_HANDOVER must take the value of HANDOVER_AUTO of the template input variables."
  }

  # SOV-134 — the variables document (the template input `variables`)
  # is written packed to /var/lib/catalyst/variables.env, one KEY=value line
  # per key in key order, and ConfigMap flux-system/sovereign-variables is
  # created from it right before flux-bootstrap.yaml is applied, so the
  # ConfigMap holds the contract keys besides the kit keys.
  assert {
    condition     = output.variables_env_written == true
    error_message = "SOV-134: the write_files entry /var/lib/catalyst/variables.env (mode 0600, encoding gz+b64) must hold every key of the template input variables as KEY=value lines in key order."
  }

  assert {
    condition     = length(regexall("(?m)^[A-Z][A-Z0-9_]*=", output.variables_env)) == 39 && strcontains(output.variables_env, "\nHANDOVER_AUTO=false\n") && strcontains(output.variables_env, "\nLOCAL_REGISTRY_HOST=registry.render-test.example.test\n") && strcontains(output.variables_env, "\nSOVEREIGN_CONSUME=origin\n") && !strcontains(output.variables_env, "POWERDNS_API_KEY")
    error_message = "SOV-134: variables.env must carry the 39 keys of the variables document, among them HANDOVER_AUTO, LOCAL_REGISTRY_HOST and SOVEREIGN_CONSUME, and never POWERDNS_API_KEY."
  }

  assert {
    condition     = length(regexall("\n    kubectl -n flux-system create cm sovereign-variables --from-env-file=/var/lib/catalyst/variables\\.env && kubectl apply -f /var/lib/catalyst/flux-bootstrap\\.yaml \\|\\| \\{ ", output.rendered_cloud_init)) == 1 && length(regexall("kubectl apply -f /var/lib/catalyst/flux-bootstrap\\.yaml", output.rendered_cloud_init)) == 1
    error_message = "SOV-134: ConfigMap sovereign-variables must be created from variables.env right before the one apply of flux-bootstrap.yaml, under the same exit 92."
  }

  # SOV-142 — node artifacts come from the origin registry. The packed
  # helper /var/lib/catalyst/oci_fetch is written before runcmd and sets
  # NODE_ARTIFACT_FALLBACK=true once (SOV-510 changes it to false). The k3s
  # installer (tag: k3s_version with + as -), the Gateway API manifest and
  # the Flux install manifest are each fetched through it, with the upstream
  # address of each as its fallback, and none of the three is downloaded
  # directly any more.
  assert {
    condition     = length(regexall("(?s)\n  - path: /var/lib/catalyst/oci_fetch\n    permissions: '0700'\n    encoding: gz\\+b64\n    content: [A-Za-z0-9+/]+=*\n.*\nruncmd:\n", output.rendered_cloud_init)) == 1 && strcontains(output.boot_text, "\n#!/bin/sh\nNODE_ARTIFACT_FALLBACK=true\n") && length(regexall("NODE_ARTIFACT_FALLBACK=", output.boot_text)) == 1
    error_message = "SOV-142: the write_files entry /var/lib/catalyst/oci_fetch (mode 0700, encoding gz+b64) must be written once, before runcmd, and must set NODE_ARTIFACT_FALLBACK=true, once."
  }

  assert {
    condition     = length(regexall("\n      sh /var/lib/catalyst/oci_fetch k3s-install v1\\.31\\.4-k3s1 /var/lib/catalyst/k3s-install\\.sh https://[^ ]+ \\\\\n", output.rendered_cloud_init)) == 1 && length(regexall("; do sh /var/lib/catalyst/oci_fetch gateway-api-experimental-install v1\\.1\\.0 /var/lib/catalyst/gwapi\\.yaml https://[^ ]+/v1\\.1\\.0/experimental-install\\.yaml && kubectl apply -f /var/lib/catalyst/gwapi\\.yaml && break;", output.rendered_cloud_init)) == 1 && length(regexall("\n     sh /var/lib/catalyst/oci_fetch flux-install v2\\.4\\.0 /var/lib/catalyst/flux\\.yaml https://[^ ]+/v2\\.4\\.0/install\\.yaml && kubectl apply -f /var/lib/catalyst/flux\\.yaml \\|\\| true\n", output.rendered_cloud_init)) == 1 && length(regexall("sh /var/lib/catalyst/oci_fetch ", output.rendered_cloud_init)) == 3 && !strcontains(output.rendered_cloud_init, "curl -fsSL --max-time 60 https://") && !strcontains(output.rendered_cloud_init, "kubectl apply -f https://") && !strcontains(output.rendered_cloud_init, "/install.yaml | kubectl apply")
    error_message = "SOV-142: the k3s installer, the Gateway API manifest and the Flux install manifest must each be fetched with oci_fetch, once, with the upstream address as the fallback, and none of them directly."
  }

  # SEC-152 — the four actions of contract C8 (docs/contracts/c8-seal-provider.md).
  # The hetzner fixture names no seal volume. Action 1: the node file is staged
  # under /run with mode 0400 and encoding b64 and decodes to the key. Action 3:
  # the item before the k3s install starts with the seal directory, installs
  # the key at its path and removes the stage. Action 2 is absent: no exit 96,
  # no fstab line. Action 4: the seal profile is ConfigMap
  # flux-system/sovereign-seal-profile with the hook's kind and stanza, applied
  # with the bootstrap Secrets and not shredded.
  assert {
    condition     = output.seal.key == "render-test-seal-key-32-bytes-ok" && strcontains(output.rendered_cloud_init, "\n  - path: /run/catalyst-seal-stage/openbao-seal-0123456789abcdef.key\n    permissions: '0400'\n    encoding: b64\n")
    error_message = "SEC-152: the seal key file must be written to /run/catalyst-seal-stage/openbao-seal-0123456789abcdef.key, mode 0400, encoding b64, and decode to the key of the hook."
  }

  assert {
    condition     = length(regexall("(?s)\n  - \\|\n    install -d -m 0700 -o 100 -g 1000 /var/lib/catalyst/seal\n    install -m 0400 -o 100 -g 1000 /run/catalyst-seal-stage/openbao-seal-0123456789abcdef\\.key /var/lib/catalyst/seal/openbao-seal-0123456789abcdef\\.key\n    rm -rf /run/catalyst-seal-stage\n.*K3S_TOKEN=", output.rendered_cloud_init)) == 1 && !strcontains(output.rendered_cloud_init, "exit 96") && !strcontains(output.rendered_cloud_init, "catalyst-seal /etc/fstab")
    error_message = "SEC-152: without a seal volume, the runcmd item before the k3s install must create /var/lib/catalyst/seal (0700, 100:1000), install the staged key at its path and remove the stage, with no volume step."
  }

  assert {
    condition     = jsonencode(output.seal.profile) == jsonencode({ apiVersion = "v1", kind = "ConfigMap", metadata = { name = "sovereign-seal-profile", namespace = "flux-system" }, data = { kind = "static-node-file", stanza = "seal \"static\" {\n  current_key_id = \"0123456789abcdef\"\n  current_key    = \"file:///openbao/seal/openbao-seal-0123456789abcdef.key\"\n}\n" } }) && strcontains(output.rendered_cloud_init, "\n    kubectl apply --server-side -f sovereign-seal-profile.yaml -f ghcr-pull-secret.yaml ") && length(regexall("sovereign-seal-profile\\.yaml", output.rendered_cloud_init)) == 2
    error_message = "SEC-152: /var/lib/catalyst/sovereign-seal-profile.yaml must be ConfigMap flux-system/sovereign-seal-profile with data.kind and data.stanza of the hook, applied by the server-side apply of the bootstrap Secrets and not shredded."
  }
}

run "huawei_render_is_valid" {
  command = plan

  variables {
    provider_name = "huawei"
  }

  assert {
    condition     = output.path_line_jammed == false
    error_message = "#4513 regression (huawei branch): infrastructure-config `path:`/`prune:` jammed onto one line."
  }

  assert {
    condition     = output.prune_on_own_line == true
    error_message = "#4513 (huawei branch): `prune: true` must render on its own line."
  }

  assert {
    condition     = output.multi_name_create == false
    error_message = "#4513 regression (huawei branch): two-NAME `create namespace` will die on the first runcmd item."
  }

  # #4521 — Huawei is the branch the keystone walk (dep 25aadcfc) hit the
  # deadlock on. The LAYER-1 Kustomization + the committed LAYER-2 CRs'
  # dependsOn ordering must hold on the Huawei branch too.
  assert {
    condition     = output.has_providers_kustomization == true
    error_message = "#4521 regression (huawei branch): `infrastructure-providers` Kustomization missing under clusters/_template/root — provider-opentofu will never install (the live 25aadcfc fault)."
  }

  assert {
    condition     = output.root.names == ["sovereign-root"] && output.root.path == "clusters/_template/root/base-primary" && output.root.folder
    error_message = "SOV-154 (huawei branch): flux-bootstrap.yaml must hold one Flux Kustomization, sovereign-root, with the path ./clusters/_template/root/base-primary, a folder of the repository with a kustomization.yaml."
  }

  assert {
    condition     = output.config_not_inlined == true
    error_message = "#4521 (huawei branch): `infrastructure-config` LAYER 2 must NOT be re-inlined into cloud-init (it is a committed Flux Kustomization CR)."
  }

  assert {
    condition     = output.config_depends_on_providers == true
    error_message = "#4521 regression (huawei branch): the committed `infrastructure-config` CR must `dependsOn: [infrastructure-providers]`."
  }

  # SEC-020 (huawei branch) — the server token and the separate agent token.
  assert {
    condition     = strcontains(output.rendered_cloud_init, "K3S_TOKEN=render-test-server-token ")
    error_message = "SEC-020 (huawei branch): the k3s server line must set K3S_TOKEN to the server token (k3s_server_token)."
  }

  assert {
    condition     = strcontains(output.rendered_cloud_init, " --agent-token=render-test-agent-token ")
    error_message = "SEC-020 (huawei branch): the k3s server line must pass --agent-token with the agent token (k3s_agent_token)."
  }

  # NET-020 (huawei branch) — the k3s server starts without kube-proxy.
  assert {
    condition     = strcontains(output.rendered_cloud_init, " --disable-network-policy --disable-kube-proxy ")
    error_message = "NET-020 (huawei branch): the k3s server line must pass --disable-kube-proxy (Cilium runs with kubeProxyReplacement: true)."
  }

  # SEC-030 (huawei branch) — etcd encryption with the secretbox provider.
  assert {
    condition     = strcontains(output.rendered_cloud_init, "INSTALL_K3S_EXEC=\"server --cluster-init --secrets-encryption --secrets-encryption-provider=secretbox ")
    error_message = "SEC-030 (huawei branch): the k3s server line must pass --secrets-encryption --secrets-encryption-provider=secretbox (etcd encryption at rest)."
  }

  # SOV-130 (huawei branch) — the substituted values live in ConfigMap
  # flux-system/sovereign-variables, which the root Kustomization reads
  # with postBuild.substituteFrom. No Kustomization keeps an inline
  # substitute map, and the DNS-01 API key is only in the Secret
  # flux-system/sovereign-tls-secrets.
  assert {
    condition     = strcontains(output.flux_bootstrap_yaml, "\nkind: ConfigMap\nmetadata:\n  name: sovereign-variables\n  namespace: flux-system\ndata:\n")
    error_message = "SOV-130 (huawei branch): the ConfigMap flux-system/sovereign-variables is missing from flux-bootstrap.yaml."
  }

  assert {
    condition     = length(regexall("(?m)^        name: sovereign-variables$", output.flux_bootstrap_yaml)) == 1
    error_message = "SOV-130, SOV-154 (huawei branch): the root Kustomization must list ConfigMap sovereign-variables under postBuild.substituteFrom."
  }

  assert {
    condition     = length(regexall("(?m)^[ ]*substitute:", output.flux_bootstrap_yaml)) == 0
    error_message = "SOV-130 (huawei branch): an inline Flux Kustomization still carries a postBuild.substitute map; its values belong in ConfigMap sovereign-variables."
  }

  assert {
    condition     = !strcontains(output.flux_bootstrap_yaml, "POWERDNS_API_KEY") && length(regexall("POWERDNS_API_KEY:", output.rendered_cloud_init)) == 1 && strcontains(output.rendered_cloud_init, "        name: sovereign-tls-secrets\n        namespace: flux-system\n      type: Opaque\n      stringData:\n        POWERDNS_API_KEY: \"${base64encode("render-test-pdns-key")}\"\n")
    error_message = "SOV-130 (huawei branch): POWERDNS_API_KEY must appear once, as the one key of the Secret flux-system/sovereign-tls-secrets, never in the ConfigMap sovereign-variables."
  }

  assert {
    condition     = strcontains(output.rendered_cloud_init, " -f cloud-credentials-secret.yaml -f sovereign-tls-secrets.yaml && shred -u ")
    error_message = "SOV-130 (huawei branch): the Secret file sovereign-tls-secrets.yaml must be applied with the other bootstrap Secrets."
  }

  # SEC-040 — bootstrap secret hygiene on the node. The kubeconfig and
  # flux-bootstrap.yaml have mode 0600; the seven bootstrap Secret files are
  # shredded once their apply succeeded, flux-bootstrap.yaml after its
  # apply; the robot token is reflected into catalyst-system only; the
  # template creates no OpenBao recovery seed (the bp-openbao chart does).
  assert {
    condition     = strcontains(output.rendered_cloud_init, " --write-kubeconfig-mode=0600\" \\\n") && !strcontains(output.rendered_cloud_init, "--write-kubeconfig-mode=0644") && strcontains(output.rendered_cloud_init, "\n  - path: /var/lib/catalyst/flux-bootstrap.yaml\n    permissions: '0600'\n")
    error_message = "SEC-040 (huawei branch): the k3s server line must pass --write-kubeconfig-mode=0600, and /var/lib/catalyst/flux-bootstrap.yaml must have mode 0600."
  }

  assert {
    condition     = strcontains(output.rendered_cloud_init, " -f sovereign-tls-secrets.yaml && shred -u ghcr-pull-secret.yaml harbor-robot-token-secret.yaml pdm-basicauth-secret.yaml handover-jwt-public-secret.yaml object-storage-secret.yaml cloud-credentials-secret.yaml sovereign-tls-secrets.yaml\n    (exit $?)\n") && length(regexall("kubectl apply -f /var/lib/catalyst/flux-bootstrap\\.yaml \\|\\| \\{[^\n]*exit 92; \\}\n    shred -u /var/lib/catalyst/flux-bootstrap\\.yaml\n", output.rendered_cloud_init)) == 1
    error_message = "SEC-040 (huawei branch): the seven bootstrap Secret files must be shredded on the line of their apply once it succeeded, a failed apply must stop the script on the next line, (exit $?), and flux-bootstrap.yaml must be shredded on the line after its apply."
  }

  assert {
    condition     = strcontains(output.boot_text, "\n  name: harbor-robot-token\n  namespace: flux-system\n  annotations:\n    reflector.v1.k8s.emberstack.com/reflection-allowed: \"true\"\n    reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: \"catalyst-system\"\n    reflector.v1.k8s.emberstack.com/reflection-auto-enabled: \"true\"\n    reflector.v1.k8s.emberstack.com/reflection-auto-namespaces: \"catalyst-system\"\n")
    error_message = "SEC-040 (huawei branch): the Secret flux-system/harbor-robot-token must be reflected into catalyst-system and into no other namespace."
  }

  assert {
    condition     = !strcontains(output.boot_text, "openbao-recovery-seed")
    error_message = "SEC-040 (huawei branch): cloud-init must not create the Secret openbao-recovery-seed; the bp-openbao chart creates it."
  }

  # SOV-140 (huawei branch) — before the post-back, cloud-init creates
  # ServiceAccount kube-system/parent-bootstrap, its cluster-admin binding and
  # its token Secret, and writes the kubeconfig the post-back sends; right
  # after the post-back it deletes that file.
  assert {
    condition     = strcontains(output.rendered_cloud_init, "    export KUBECONFIG=/etc/rancher/k3s/k3s.yaml N=parent-bootstrap S=kube-system K=/var/lib/catalyst/parent-bootstrap.kubeconfig\n") && strcontains(output.rendered_cloud_init, "    roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: cluster-admin}\n    subjects: [{kind: ServiceAccount, name: $N, namespace: $S}]\n") && strcontains(output.rendered_cloud_init, "    type: kubernetes.io/service-account-token\n    metadata: {name: $N-token, namespace: $S, annotations: {kubernetes.io/service-account.name: $N}}\n")
    error_message = "SOV-140 (huawei branch): the ServiceAccount kube-system/parent-bootstrap, its cluster-admin binding or its token Secret parent-bootstrap-token is missing."
  }

  assert {
    condition     = length(regexall("(?s)K=/var/lib/catalyst/parent-bootstrap\\.kubeconfig\n.*config set-credentials default --token=.*\n  - rm -f /var/lib/catalyst/parent-bootstrap\\.kubeconfig\n", output.rendered_cloud_init)) == 1
    error_message = "SOV-140 (huawei branch): the parent-bootstrap kubeconfig must be written before the post-back and deleted right after it."
  }

  # SOV-133 (huawei branch) — the template takes the variables document as its input
  # `variables`; the kit key FIRE_CUTOVER_ON_HANDOVER takes the value of its
  # key HANDOVER_AUTO ("false" in render.tf).
  assert {
    condition     = strcontains(output.flux_bootstrap_yaml, "\n      FIRE_CUTOVER_ON_HANDOVER: \"false\"\n")
    error_message = "SOV-133 (huawei branch): the kit key FIRE_CUTOVER_ON_HANDOVER must take the value of HANDOVER_AUTO of the template input variables."
  }

  # SOV-134 (huawei branch) — the variables document (the template input `variables`)
  # is written packed to /var/lib/catalyst/variables.env, one KEY=value line
  # per key in key order, and ConfigMap flux-system/sovereign-variables is
  # created from it right before flux-bootstrap.yaml is applied, so the
  # ConfigMap holds the contract keys besides the kit keys.
  assert {
    condition     = output.variables_env_written == true
    error_message = "SOV-134 (huawei branch): the write_files entry /var/lib/catalyst/variables.env (mode 0600, encoding gz+b64) must hold every key of the template input variables as KEY=value lines in key order."
  }

  assert {
    condition     = length(regexall("(?m)^[A-Z][A-Z0-9_]*=", output.variables_env)) == 39 && strcontains(output.variables_env, "\nHANDOVER_AUTO=false\n") && strcontains(output.variables_env, "\nLOCAL_REGISTRY_HOST=registry.render-test.example.test\n") && strcontains(output.variables_env, "\nSOVEREIGN_CONSUME=origin\n") && !strcontains(output.variables_env, "POWERDNS_API_KEY")
    error_message = "SOV-134 (huawei branch): variables.env must carry the 39 keys of the variables document, among them HANDOVER_AUTO, LOCAL_REGISTRY_HOST and SOVEREIGN_CONSUME, and never POWERDNS_API_KEY."
  }

  assert {
    condition     = length(regexall("\n    kubectl -n flux-system create cm sovereign-variables --from-env-file=/var/lib/catalyst/variables\\.env && kubectl apply -f /var/lib/catalyst/flux-bootstrap\\.yaml \\|\\| \\{ ", output.rendered_cloud_init)) == 1 && length(regexall("kubectl apply -f /var/lib/catalyst/flux-bootstrap\\.yaml", output.rendered_cloud_init)) == 1
    error_message = "SOV-134 (huawei branch): ConfigMap sovereign-variables must be created from variables.env right before the one apply of flux-bootstrap.yaml, under the same exit 92."
  }

  # SOV-142 (huawei branch) — node artifacts come from the origin registry. The packed
  # helper /var/lib/catalyst/oci_fetch is written before runcmd and sets
  # NODE_ARTIFACT_FALLBACK=true once (SOV-510 changes it to false). The k3s
  # installer (tag: k3s_version with + as -), the Gateway API manifest and
  # the Flux install manifest are each fetched through it, with the upstream
  # address of each as its fallback, and none of the three is downloaded
  # directly any more.
  assert {
    condition     = length(regexall("(?s)\n  - path: /var/lib/catalyst/oci_fetch\n    permissions: '0700'\n    encoding: gz\\+b64\n    content: [A-Za-z0-9+/]+=*\n.*\nruncmd:\n", output.rendered_cloud_init)) == 1 && strcontains(output.boot_text, "\n#!/bin/sh\nNODE_ARTIFACT_FALLBACK=true\n") && length(regexall("NODE_ARTIFACT_FALLBACK=", output.boot_text)) == 1
    error_message = "SOV-142 (huawei branch): the write_files entry /var/lib/catalyst/oci_fetch (mode 0700, encoding gz+b64) must be written once, before runcmd, and must set NODE_ARTIFACT_FALLBACK=true, once."
  }

  assert {
    condition     = length(regexall("\n      sh /var/lib/catalyst/oci_fetch k3s-install v1\\.31\\.4-k3s1 /var/lib/catalyst/k3s-install\\.sh https://[^ ]+ \\\\\n", output.rendered_cloud_init)) == 1 && length(regexall("; do sh /var/lib/catalyst/oci_fetch gateway-api-experimental-install v1\\.1\\.0 /var/lib/catalyst/gwapi\\.yaml https://[^ ]+/v1\\.1\\.0/experimental-install\\.yaml && kubectl apply -f /var/lib/catalyst/gwapi\\.yaml && break;", output.rendered_cloud_init)) == 1 && length(regexall("\n     sh /var/lib/catalyst/oci_fetch flux-install v2\\.4\\.0 /var/lib/catalyst/flux\\.yaml https://[^ ]+/v2\\.4\\.0/install\\.yaml && kubectl apply -f /var/lib/catalyst/flux\\.yaml \\|\\| true\n", output.rendered_cloud_init)) == 1 && length(regexall("sh /var/lib/catalyst/oci_fetch ", output.rendered_cloud_init)) == 3 && !strcontains(output.rendered_cloud_init, "curl -fsSL --max-time 60 https://") && !strcontains(output.rendered_cloud_init, "kubectl apply -f https://") && !strcontains(output.rendered_cloud_init, "/install.yaml | kubectl apply")
    error_message = "SOV-142 (huawei branch): the k3s installer, the Gateway API manifest and the Flux install manifest must each be fetched with oci_fetch, once, with the upstream address as the fallback, and none of them directly."
  }

  # #7922: no node reboots itself. The control plane switched on the automatic
  # reboot of unattended-upgrades at 02:30 node time and rebooted a region's only
  # control plane mid-cutover. kured's node-patch-config owns that configuration.
  assert {
    condition     = !strcontains(output.rendered_cloud_init, "Automatic-Reboot") && !strcontains(output.rendered_cloud_init, "/etc/apt/apt.conf.d/") && !strcontains(output.rendered_cloud_init, "enable --now unattended-upgrades") && strcontains(output.rendered_cloud_init, "\n  - unattended-upgrades\n")
    error_message = "#7922 (huawei branch): the control-plane cloud-init must write no unattended-upgrade configuration (no apt.conf.d file, no Automatic-Reboot) and must not enable the service itself; bp-kured's node-patch-config owns the configuration. The package unattended-upgrades must still be installed."
  }

  # SEC-152 (huawei branch) — contract C8, action 2: this fixture names the seal
  # volume /dev/vdb. The item before the k3s install stops with code 96 when
  # the device is missing, formats an empty device once with the label
  # catalyst-seal, adds the fstab line once and mounts it, then installs the
  # key (action 3). The key file and the seal profile are written as in the
  # hetzner branch.
  assert {
    condition     = length(regexall("(?s)\n  - \\|\n    \\[ -b /dev/vdb \\] \\|\\| exit 96\n    blkid /dev/vdb >/dev/null 2>&1 \\|\\| mkfs\\.ext4 -q -L catalyst-seal /dev/vdb\n    grep -q catalyst-seal /etc/fstab \\|\\| echo 'LABEL=catalyst-seal /var/lib/catalyst/seal ext4 defaults,nodev,nosuid,noexec 0 2' >>/etc/fstab\n    mkdir -p /var/lib/catalyst/seal && mount /var/lib/catalyst/seal\n    install -d -m 0700 -o 100 -g 1000 /var/lib/catalyst/seal\n    install -m 0400 -o 100 -g 1000 /run/catalyst-seal-stage/openbao-seal-0123456789abcdef\\.key /var/lib/catalyst/seal/openbao-seal-0123456789abcdef\\.key\n    rm -rf /run/catalyst-seal-stage\n.*K3S_TOKEN=", output.rendered_cloud_init)) == 1 && length(regexall("exit 96", output.rendered_cloud_init)) == 1
    error_message = "SEC-152 (huawei branch): with the seal volume /dev/vdb, the runcmd item before the k3s install must check the device (exit 96), format it once (label catalyst-seal), add the fstab line once, mount it at /var/lib/catalyst/seal, then install the staged key and remove the stage."
  }

  assert {
    condition     = output.seal.key == "render-test-seal-key-32-bytes-ok" && output.seal.profile.data.kind == "static-node-file" && strcontains(output.rendered_cloud_init, "\n    kubectl apply --server-side -f sovereign-seal-profile.yaml -f ghcr-pull-secret.yaml ")
    error_message = "SEC-152 (huawei branch): the seal key file and the seal profile must be written and applied as in the hetzner branch."
  }
}

# Golden render (CLD-040). The SHA-256 of each fixture's rendered cloud-init
# (comment lines stripped, as the provider modules strip them) must equal the
# value stored in golden.auto.tfvars.json. A change to the shared template or
# to a fixture value in render.tf changes a hash and fails this run. If the
# change to the render is intended, run scripts/update-cloudinit-golden.sh,
# commit the golden file with the change, and say in the commit message why
# the render changed.
run "golden" {
  command = plan

  assert {
    condition     = output.sha256_hetzner == var.golden_hetzner
    error_message = "Golden render: the hetzner fixture renders to SHA-256 ${output.sha256_hetzner}, but golden.auto.tfvars.json holds \"${var.golden_hetzner}\". If the change to the rendered cloud-init is intended, run scripts/update-cloudinit-golden.sh, commit the golden file, and say in the commit message why the render changed."
  }

  assert {
    condition     = output.sha256_huawei == var.golden_huawei
    error_message = "Golden render: the huawei fixture renders to SHA-256 ${output.sha256_huawei}, but golden.auto.tfvars.json holds \"${var.golden_huawei}\". If the change to the rendered cloud-init is intended, run scripts/update-cloudinit-golden.sh, commit the golden file, and say in the commit message why the render changed."
  }

  # #7893 — golden files. Every file cloud-init writes from each fixture,
  # a packed (gz+b64) entry decoded as cloud-init decodes it, must have the
  # SHA-256 stored in golden.auto.tfvars.json. A change that only alters how
  # the render carries a file (packing it) leaves these values as they are;
  # a change to what a node finds on its disk fails here.
  assert {
    condition     = jsonencode(output.files_sha256_hetzner) == jsonencode(var.golden_files_hetzner)
    error_message = "Golden files: a file cloud-init writes from the hetzner fixture differs from golden.auto.tfvars.json: ${join(", ", [for p in sort(setunion([for p, h in output.files_sha256_hetzner : p if lookup(var.golden_files_hetzner, p, "") != h], [for p, h in var.golden_files_hetzner : p if lookup(output.files_sha256_hetzner, p, "") != h])) : "hetzner:${p}"])}. If the change is intended, run scripts/update-cloudinit-golden.sh and say in the commit message which file changed and why."
  }

  assert {
    condition     = jsonencode(output.files_sha256_huawei) == jsonencode(var.golden_files_huawei)
    error_message = "Golden files: a file cloud-init writes from the huawei fixture differs from golden.auto.tfvars.json: ${join(", ", [for p in sort(setunion([for p, h in output.files_sha256_huawei : p if lookup(var.golden_files_huawei, p, "") != h], [for p, h in var.golden_files_huawei : p if lookup(output.files_sha256_huawei, p, "") != h])) : "huawei:${p}"])}. If the change is intended, run scripts/update-cloudinit-golden.sh and say in the commit message which file changed and why."
  }
}
