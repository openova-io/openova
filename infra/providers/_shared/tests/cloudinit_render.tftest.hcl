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
  # Flux Kustomization (atomic dry-run deadlock). Both layers must render, the
  # provider layer must point at the providers/ tree, and the config layer must
  # `dependsOn` the provider layer with the provider healthCheck gate.
  assert {
    condition     = output.has_providers_kustomization == true
    error_message = "#4521 regression: the `infrastructure-providers` Flux Kustomization (Provider-install LAYER 1) is missing — the Provider + ProviderConfig got re-batched into one Kustomization → atomic dry-run deadlock."
  }

  assert {
    condition     = output.has_config_kustomization == true
    error_message = "#4521: the `infrastructure-config` Flux Kustomization (config LAYER 2) is missing."
  }

  assert {
    condition     = output.providers_path_correct == true
    error_message = "#4521: `infrastructure-providers` must point its `path` at ./clusters/_template/infrastructure/providers (the Provider-only tree)."
  }

  assert {
    condition     = output.config_depends_on_providers == true
    error_message = "#4521 regression: `infrastructure-config` must `dependsOn: [infrastructure-providers]` so the Provider CRDs register before the ProviderConfig is dry-run."
  }

  assert {
    condition     = output.providers_healthcheck == true
    error_message = "#4521: `infrastructure-providers` must carry a `healthChecks` gate on `kind: Provider` so its dependsOn only releases once the provider is Healthy (CRDs registered)."
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
  # deadlock on. Both Kustomization layers + the dependsOn ordering + the
  # provider healthCheck must render on the Huawei branch too.
  assert {
    condition     = output.has_providers_kustomization == true
    error_message = "#4521 regression (huawei branch): `infrastructure-providers` Kustomization missing — provider-opentofu will never install (the live 25aadcfc fault)."
  }

  assert {
    condition     = output.has_config_kustomization == true
    error_message = "#4521 (huawei branch): `infrastructure-config` Kustomization missing."
  }

  assert {
    condition     = output.config_depends_on_providers == true
    error_message = "#4521 regression (huawei branch): `infrastructure-config` must `dependsOn: [infrastructure-providers]`."
  }

  assert {
    condition     = output.providers_healthcheck == true
    error_message = "#4521 (huawei branch): `infrastructure-providers` must carry the `kind: Provider` healthCheck gate."
  }
}
