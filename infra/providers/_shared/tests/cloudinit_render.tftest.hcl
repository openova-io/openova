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
}
