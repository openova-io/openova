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
    error_message = "#4521: `infrastructure-providers` must point its `path` at ./clusters/_template/infrastructure/providers (the Provider-only tree)."
  }

  assert {
    condition     = output.config_not_inlined == true
    error_message = "#4521 / #1981 / #3884 byte-cap regression: the `infrastructure-config` LAYER-2 Kustomization must NOT be re-inlined into cloud-init — it ships as a committed Flux Kustomization CR. A third inline Kustomization pushed the 3-region Hetzner render past the 32256B cap."
  }

  assert {
    condition     = output.config_depends_on_providers == true
    error_message = "#4521 regression: the committed `infrastructure-config` Flux Kustomization CRs (base + hetzner) must `dependsOn: [infrastructure-providers]` so the Provider CRDs register before the ProviderConfig is dry-run."
  }

  assert {
    condition     = output.config_paths_correct == true
    error_message = "#4521: the committed `infrastructure-config` CRs must point at the correct cloud config tree (base → ./clusters/_template/infrastructure ; hetzner → .../infrastructure/hetzner)."
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
    error_message = "#4521 regression (huawei branch): the committed `infrastructure-config` CRs must `dependsOn: [infrastructure-providers]`."
  }
}
