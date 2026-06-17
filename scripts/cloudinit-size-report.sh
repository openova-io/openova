#!/usr/bin/env bash
# cloudinit-size-report.sh — report the EXACT rendered cloud-init user_data byte
# count for a provider module, and emit a GitHub headroom annotation.
#
# WHY (issue #3724 / Refs #3720): the 32256-byte Hetzner user_data cap is a
# cliff. PR #3715 rendered the control-plane cloud-init at >32256 and froze
# catalyst-build for hours. The blocking gate (tests/cloudinit_size.tftest.hcl)
# fails a PR that crosses the cap — but by then you're already over. This script
# surfaces the *exact* byte count on EVERY run and WARNS once the render crosses
# ~93% of the cap (HEADROOM_WARN_BYTES), so the cliff is visible while there's
# still room to act.
#
# HOW the byte count is obtained with ZERO render drift:
# The only faithful source of the rendered size is the real module's own local
# (it threads the multi-KB provider-injected strings — registry mirror, cloud-
# credentials secret, crossplane provider yaml, kubeconfig PUT block — that a
# standalone templatefile() harness would omit and thus undercount by KBs). A
# `tofu test` assert can read that local directly. OpenTofu only renders an
# assert's error_message on FAILURE, so to print the count unconditionally we run
# a probe whose condition is deliberately false (`length(...) < 0`) and whose
# error_message is `CLOUDINIT_<KIND>_BYTES=${nonsensitive(length(<local>))}`.
# nonsensitive() is required because the local embeds secrets (same reason the
# production precondition at main.tf:956 wraps its length()); it leaks only the
# integer byte count, never any token bytes.
#
# The probe is generated into a throwaway module subdir and run via
# `tofu test -test-directory=<subdir>` (relative dir under the module — OpenTofu
# resolves test dirs relative to the module, absolute paths are NOT discovered).
# The committed tests/ suite is never touched, so the existing infra-*-tofu.yaml
# `tofu test` (which runs every file in tests/) stays green. The subdir is
# removed on exit.
#
# This script NEVER fails the job on the size cap — that is the blocking gate's
# job (tests/cloudinit_size.tftest.hcl, run separately). It only reports + warns.
# It exits non-zero only on its own operational failure (couldn't render).
#
# Usage:  scripts/cloudinit-size-report.sh <provider-module-dir> <provider>
#   <provider>: hetzner | huawei  (selects the right local name + probe vars)

set -euo pipefail

MODULE_DIR="${1:?usage: cloudinit-size-report.sh <module-dir> <hetzner|huawei>}"
PROVIDER="${2:?usage: cloudinit-size-report.sh <module-dir> <hetzner|huawei>}"

# Hetzner's hard cap is 32768; the production guardrail trips at 32256. We warn
# at HEADROOM_WARN_BYTES (~93% of 32256) so a PR sees the cliff coming. Override
# via env for local experimentation.
SIZE_CAP_BYTES="${SIZE_CAP_BYTES:-32256}"
HEADROOM_WARN_BYTES="${HEADROOM_WARN_BYTES:-30000}"

PROBE_DIR_NAME=".cloudinit-size-probe"
PROBE_DIR="${MODULE_DIR}/${PROBE_DIR_NAME}"
cleanup() { rm -rf "${PROBE_DIR}"; }
trap cleanup EXIT
mkdir -p "${PROBE_DIR}"

# Per-provider: the mock_provider + override + minimal variables block needed to
# materialise the cloud-init local, plus the local name to measure. Kept in
# lockstep with the committed tests/cloudinit_size.tftest.hcl of each provider.
case "${PROVIDER}" in
  hetzner)
    LOCAL_EXPR='length(local.control_plane_cloud_init)'
    cat > "${PROBE_DIR}/probe.tftest.hcl" <<'PROBE'
mock_provider "hcloud" {
  mock_resource "hcloud_network" {
    defaults = { id = "1" }
  }
  mock_resource "hcloud_network_subnet" {
    defaults = { id = "1" }
  }
  mock_resource "hcloud_load_balancer" {
    defaults = { id = "1", ipv4 = "203.0.113.10" }
  }
  mock_resource "hcloud_load_balancer_network" {
    defaults = { id = "1" }
  }
  mock_resource "hcloud_load_balancer_target" {
    defaults = { id = "1" }
  }
  mock_resource "hcloud_load_balancer_service" {
    defaults = { id = "1" }
  }
  mock_resource "hcloud_server" {
    defaults = { id = "1", ipv4_address = "203.0.113.20" }
  }
  mock_resource "hcloud_ssh_key" {
    defaults = { id = "1" }
  }
  mock_resource "hcloud_firewall" {
    defaults = { id = "1" }
  }
}
override_resource {
  target = aws_s3_bucket.main
  values = { id = "catalyst-test-example-com" }
}
override_resource {
  target = aws_s3_bucket_acl.main
  values = {}
}
variables {
  sovereign_fqdn             = "test.example.com"
  sovereign_subdomain        = "test"
  org_name                   = "Test Org"
  org_email                  = "ops@example.com"
  hcloud_token               = "mock-hcloud-token"
  hcloud_project_id          = "12345"
  region                     = "hel1"
  control_plane_size         = "cpx22"
  worker_size                = "cpx32"
  worker_count               = 2
  k3s_version                = "v1.31.4+k3s1"
  ssh_public_key             = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITESTKEYTESTKEYTESTKEYTESTKEYTESTKEY test@local"
  object_storage_region      = "nbg1"
  object_storage_access_key  = "TESTACCESSKEY1234567"
  object_storage_secret_key  = "TESTSECRETKEYTESTSECRETKEYTESTSE"
  object_storage_bucket_name = "catalyst-test-example-com"
  domain_mode                = "byo"
}
run "emit_cp_bytes" {
  command = plan
  assert {
    condition     = nonsensitive(length(local.control_plane_cloud_init)) < 0
    error_message = "CLOUDINIT_CP_BYTES=${nonsensitive(length(local.control_plane_cloud_init))}"
  }
}
PROBE
    ;;
  huawei)
    LOCAL_EXPR='max([for v in values(local.cp_cloud_init_by_region) : length(v)]...)'
    cat > "${PROBE_DIR}/probe.tftest.hcl" <<'PROBE'
mock_provider "huaweicloud" {
  mock_resource "huaweicloud_vpc" {
    defaults = { id = "vpc-mock-1" }
  }
  mock_resource "huaweicloud_vpc_subnet" {
    defaults = { id = "subnet-mock-1" }
  }
  mock_resource "huaweicloud_vpc_eip" {
    defaults = { id = "eip-mock-1", address = "203.0.113.10" }
  }
  mock_resource "huaweicloud_networking_secgroup" {
    defaults = { id = "sg-mock-1" }
  }
  mock_resource "huaweicloud_networking_secgroup_rule" {
    defaults = { id = "sgr-mock-1" }
  }
  mock_resource "huaweicloud_compute_instance" {
    defaults = { id = "ecs-mock-1", access_ip_v4 = "10.20.1.10" }
  }
  mock_resource "huaweicloud_kps_keypair" {
    defaults = { id = "kp-mock-1" }
  }
  mock_resource "huaweicloud_obs_bucket" {
    defaults = { id = "catalyst-test-bucket" }
  }
}
variables {
  deployment_id       = "deadbeefcafef00d"
  sovereign_fqdn      = "t99.omani.works"
  huawei_access_key   = "AKIAEXAMPLEAKIAEXAMPLE"
  huawei_secret_key   = "secret-key-not-real-just-padding-32chars"
  huawei_project_id   = "0123456789abcdef0123456789abcdef"
  org_name            = "Acme Test"
  org_email           = "admin@example.com"
  obs_bucket_name     = "catalyst-t99-omani-works-deadbeef"
  ssh_public_key      = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFakeKeyForTest test@bastion"
  parent_domains_yaml = ""
  regions = [
    {
      code               = "me-east-215-a"
      role               = "primary"
      control_plane_size = "s7n.large.4"
      worker_size        = "m7n.xlarge.8"
      worker_count       = 2
    }
  ]
}
run "emit_cp_bytes" {
  command = plan
  assert {
    condition     = nonsensitive(max([for v in values(local.cp_cloud_init_by_region) : length(v)]...)) < 0
    error_message = "CLOUDINIT_CP_BYTES=${nonsensitive(max([for v in values(local.cp_cloud_init_by_region) : length(v)]...))}"
  }
}
PROBE
    ;;
  *)
    echo "::error::cloudinit-size-report: unknown provider '${PROVIDER}' (want hetzner|huawei)"
    exit 2
    ;;
esac

echo "Rendering ${PROVIDER} cloud-init to measure user_data size (${LOCAL_EXPR})..."

# Run the probe (it is DESIGNED to fail — its condition is always false — so the
# byte-count error_message renders). We tolerate the non-zero test exit; what
# matters is whether the count line was emitted.
PROBE_OUT="$(cd "${MODULE_DIR}" && tofu test -no-color -test-directory="${PROBE_DIR_NAME}" 2>&1 || true)"

# Normal (under-cap) path: our probe's error_message prints CLOUDINIT_CP_BYTES=N.
BYTES="$(printf '%s\n' "${PROBE_OUT}" | grep -oE 'CLOUDINIT_CP_BYTES=[0-9]+' | head -1 | cut -d= -f2 || true)"

# Over-cap fallback: when the render is already over the guardrail, the module's
# OWN production precondition (hcloud_server.control_plane lifecycle, main.tf:949)
# trips during `plan` and aborts the run BEFORE our probe assert renders. That
# precondition's message states the exact size ("... cloud-init is NNNNN bytes,
# exceeds 32256 ..."), so we parse it as the count source. This keeps the number
# visible (and lets us emit the over-cap ::error::) even past the cliff.
if [ -z "${BYTES:-}" ]; then
  BYTES="$(printf '%s\n' "${PROBE_OUT}" | grep -oE 'cloud-init (for secondary region [^ ]+ )?is [0-9]+ bytes' | grep -oE '[0-9]+' | sort -nr | head -1 || true)"
fi

if [ -z "${BYTES:-}" ]; then
  echo "::error::cloudinit-size-report: could not extract rendered byte count for ${PROVIDER}. Probe output follows:"
  printf '%s\n' "${PROBE_OUT}"
  exit 1
fi

PCT=$(( BYTES * 100 / SIZE_CAP_BYTES ))
HEADROOM=$(( SIZE_CAP_BYTES - BYTES ))

SUMMARY_LINE="${PROVIDER}: rendered control-plane cloud-init = ${BYTES} bytes (${PCT}% of the ${SIZE_CAP_BYTES}-byte guardrail; ${HEADROOM} bytes headroom before the cap)."
echo "${SUMMARY_LINE}"

# GitHub Step Summary (visible on the run page).
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  {
    echo "### cloud-init size — ${PROVIDER}"
    echo ""
    echo "- Rendered control-plane cloud-init: **${BYTES} bytes** (${PCT}% of ${SIZE_CAP_BYTES} guardrail)"
    echo "- Headroom before guardrail: **${HEADROOM} bytes**"
    echo "- Hetzner hard \`user_data\` cap: 32768 bytes"
  } >> "${GITHUB_STEP_SUMMARY}"
fi

if [ "${BYTES}" -gt "${SIZE_CAP_BYTES}" ]; then
  # Over the guardrail — the blocking gate (tests/cloudinit_size.tftest.hcl) will
  # already have failed the job; emit an error annotation too so the number is
  # unmissable.
  echo "::error title=cloud-init OVER cap (${PROVIDER})::${SUMMARY_LINE} This exceeds the guardrail — the size gate fails. Cull comments / move bloat out of infra/providers/_shared/cloudinit-control-plane.tftpl."
elif [ "${BYTES}" -ge "${HEADROOM_WARN_BYTES}" ]; then
  echo "::warning title=cloud-init headroom low (${PROVIDER})::${SUMMARY_LINE} Only ${HEADROOM} bytes left before the ${SIZE_CAP_BYTES}-byte guardrail (warn threshold ${HEADROOM_WARN_BYTES}). Trim the shared cloud-init template before adding more."
else
  echo "::notice title=cloud-init size OK (${PROVIDER})::${SUMMARY_LINE}"
fi
