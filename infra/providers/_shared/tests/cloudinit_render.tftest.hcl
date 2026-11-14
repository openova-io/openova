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
  # (`infrastructure-providers`) is inlined in cloud-init and points at the
  # providers/ tree; LAYER 2 (`infrastructure-config`) ships as a COMMITTED
  # Flux Kustomization CR (kept off the byte-capped cloud-init) that
  # `dependsOn`s LAYER 1.
  assert {
    condition     = output.has_providers_kustomization == true
    error_message = "#4521 regression: the inline `infrastructure-providers` Flux Kustomization (Provider-install LAYER 1) is missing — the Provider + ProviderConfig got re-batched into one Kustomization → atomic dry-run deadlock."
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
  # flux-system/sovereign-variables, which every inline Kustomization reads
  # with postBuild.substituteFrom. No Kustomization keeps an inline
  # substitute map, and the DNS-01 API key is only in the Secret
  # flux-system/sovereign-tls-secrets.
  assert {
    condition     = strcontains(output.rendered_cloud_init, "      kind: ConfigMap\n      metadata:\n        name: sovereign-variables\n        namespace: flux-system\n      data:\n")
    error_message = "SOV-130: the ConfigMap flux-system/sovereign-variables is missing from flux-bootstrap.yaml."
  }

  assert {
    condition     = length(regexall("(?m)^              name: sovereign-variables$", output.rendered_cloud_init)) == 4
    error_message = "SOV-130: every inline Flux Kustomization (4 in this fixture) must list ConfigMap sovereign-variables under postBuild.substituteFrom."
  }

  assert {
    condition     = !strcontains(output.rendered_cloud_init, "          substitute:\n")
    error_message = "SOV-130: an inline Flux Kustomization still carries a postBuild.substitute map; its values belong in ConfigMap sovereign-variables."
  }

  assert {
    condition     = length(regexall("POWERDNS_API_KEY:", output.rendered_cloud_init)) == 1 && strcontains(output.rendered_cloud_init, "        name: sovereign-tls-secrets\n        namespace: flux-system\n      type: Opaque\n      stringData:\n        POWERDNS_API_KEY: \"${base64encode("render-test-pdns-key")}\"\n")
    error_message = "SOV-130: POWERDNS_API_KEY must appear once, as the one key of the Secret flux-system/sovereign-tls-secrets, never in the ConfigMap sovereign-variables."
  }

  assert {
    condition     = strcontains(output.rendered_cloud_init, " -f cloud-credentials-secret.yaml -f sovereign-tls-secrets.yaml\n")
    error_message = "SOV-130: the Secret file sovereign-tls-secrets.yaml must be applied with the other bootstrap Secrets."
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
    condition     = strcontains(output.rendered_cloud_init, "\n            FIRE_CUTOVER_ON_HANDOVER: \"false\"\n")
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
  # deadlock on. The inline LAYER-1 Kustomization + the committed LAYER-2 CRs'
  # dependsOn ordering must hold on the Huawei branch too.
  assert {
    condition     = output.has_providers_kustomization == true
    error_message = "#4521 regression (huawei branch): inline `infrastructure-providers` Kustomization missing — provider-opentofu will never install (the live 25aadcfc fault)."
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
  # flux-system/sovereign-variables, which every inline Kustomization reads
  # with postBuild.substituteFrom. No Kustomization keeps an inline
  # substitute map, and the DNS-01 API key is only in the Secret
  # flux-system/sovereign-tls-secrets.
  assert {
    condition     = strcontains(output.rendered_cloud_init, "      kind: ConfigMap\n      metadata:\n        name: sovereign-variables\n        namespace: flux-system\n      data:\n")
    error_message = "SOV-130 (huawei branch): the ConfigMap flux-system/sovereign-variables is missing from flux-bootstrap.yaml."
  }

  assert {
    condition     = length(regexall("(?m)^              name: sovereign-variables$", output.rendered_cloud_init)) == 5
    error_message = "SOV-130 (huawei branch): every inline Flux Kustomization (5 in this fixture) must list ConfigMap sovereign-variables under postBuild.substituteFrom."
  }

  assert {
    condition     = !strcontains(output.rendered_cloud_init, "          substitute:\n")
    error_message = "SOV-130 (huawei branch): an inline Flux Kustomization still carries a postBuild.substitute map; its values belong in ConfigMap sovereign-variables."
  }

  assert {
    condition     = length(regexall("POWERDNS_API_KEY:", output.rendered_cloud_init)) == 1 && strcontains(output.rendered_cloud_init, "        name: sovereign-tls-secrets\n        namespace: flux-system\n      type: Opaque\n      stringData:\n        POWERDNS_API_KEY: \"${base64encode("render-test-pdns-key")}\"\n")
    error_message = "SOV-130 (huawei branch): POWERDNS_API_KEY must appear once, as the one key of the Secret flux-system/sovereign-tls-secrets, never in the ConfigMap sovereign-variables."
  }

  assert {
    condition     = strcontains(output.rendered_cloud_init, " -f cloud-credentials-secret.yaml -f sovereign-tls-secrets.yaml\n")
    error_message = "SOV-130 (huawei branch): the Secret file sovereign-tls-secrets.yaml must be applied with the other bootstrap Secrets."
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
    condition     = strcontains(output.rendered_cloud_init, "\n            FIRE_CUTOVER_ON_HANDOVER: \"false\"\n")
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
}
