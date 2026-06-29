#!/usr/bin/env bash
# cloudinit-size-check.sh — dedicated, required-able byte-size gate for the
# rendered OpenTofu cloud-init user_data.
#
# WHY THIS EXISTS (#3724): Hetzner's `user_data` API has a HARD 32768-byte cap.
# The repo enforces a 32256-byte control-plane / 30720-byte worker guardrail via
# `precondition` blocks in infra/providers/hetzner/main.tf, but those only fire
# inside `tofu test`/`tofu plan`. On PR #3715 the rendered control-plane
# cloud-init tipped past the cap; the `tofu test` PR check DID go red but was
# (a) NOT a required/blocking check and (b) shared the ambiguous name
# `validate + fmt + test` with the passing huawei job, so the red was missed and
# the PR merged — silently freezing "Build & Deploy Catalyst" (G36 tofu test)
# on every subsequent main commit for hours.
#
# This script renders the REAL templates the providers render (the ONE shared
# cloud-agnostic control-plane template + each provider's worker template),
# applies the SAME comment-strip the provider applies before handing user_data
# to the cloud API, and asserts the byte size against the same guardrails — with
# a UNIQUE per-surface name so the corresponding GitHub check
# (`cloud-init-size / hetzner`, `cloud-init-size / huawei`) can be marked a
# required status check and will never collide with another job's name.
#
# It also emits GitHub-Actions workflow annotations:
#   ::error::    when a rendered surface EXCEEDS its cap (gate FAILS).
#   ::warning::  when a surface is >= ${HEADROOM_WARN_BYTES} (~93% of cap) — the
#                cliff is visible BEFORE a PR trips it.
#   ::notice::   the exact byte count + % of cap for every surface otherwise.
#
# It does NOT provision anything, touch the network, or modify any template.
# It only renders (via `templatefile()` — no providers/network needed) + measures.
#
# USAGE:
#   scripts/cloudinit-size-check.sh hetzner
#   scripts/cloudinit-size-check.sh huawei
#   scripts/cloudinit-size-check.sh both      # check both, aggregate exit status
#
# EXIT: 0 = every rendered surface for the target provider(s) is within cap.
#       1 = at least one surface EXCEEDS its cap (or a render failed).
#       2 = bad invocation / missing tool.
#
# REQUIREMENTS: tofu (templatefile rendering — no providers needed), python3.
set -u

# ─────────────────────────────────────────────────────────────────────────────
# Caps (must mirror the `precondition` guardrails in
# infra/providers/hetzner/main.tf — keep in lockstep). Hetzner hard cap is
# 32768; the CP guardrail keeps a 512 B safety buffer (32256), worker keeps more
# (30720). Huawei has no documented user_data cap, but we hold it to the SAME
# guardrails so a future move to a capped provider never surprises us, and so
# huawei gets byte coverage for the FIRST time (#3724).
# ─────────────────────────────────────────────────────────────────────────────
CAP_CP_BYTES="${CAP_CP_BYTES:-32256}"
CAP_WORKER_BYTES="${CAP_WORKER_BYTES:-30720}"
HEADROOM_WARN_BYTES="${HEADROOM_WARN_BYTES:-30000}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
PROVIDERS_DIR="${REPO_ROOT}/infra/providers"

TOFU_BIN="${TOFU_BIN:-tofu}"
PY_BIN="${PY_BIN:-python3}"

# The provider strips pure-comment lines from the rendered cloud-init before
# handing user_data to the cloud API (see the `replace(..., "/(?m)^[ ]*#( |$).*
# \n/", "")` in infra/providers/<p>/main.tf). We MUST apply the byte-identical
# strip here, else we'd over-count the raw template's ~44 KB of doc prose and
# the gate would be meaningless. This is the exact same regex, in Python (`re`):
#   ^[ ]*#( |$).*\n   multiline — a leading-whitespace `#` followed by a space
#                     (prose comment) OR end-of-line (separator `#`), whole line.
# Lines whose `#` is followed by any other char (`#!shebang`, `#cloud-config`,
# `#pragma`) are OPERATIVE and NOT stripped — same as the provider.
COMMENT_STRIP_PY='import re,sys; sys.stdout.write(re.sub(r"(?m)^[ ]*#( |$).*\n","",sys.stdin.read()))'

C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_YEL=$'\033[33m'; C_BLU=$'\033[34m'; C_RST=$'\033[0m'
if [ ! -t 1 ]; then C_RED=; C_GRN=; C_YEL=; C_BLU=; C_RST=; fi
say()  { printf '%s\n' "$*"; }
info() { printf '%s[cloudinit-size]%s %s\n' "${C_BLU}" "${C_RST}" "$*"; }
ok()   { printf '%s  PASS%s %s\n' "${C_GRN}" "${C_RST}" "$*"; }
warn() { printf '%s  WARN%s %s\n' "${C_YEL}" "${C_RST}" "$*"; }
err()  { printf '%s  FAIL%s %s\n' "${C_RED}" "${C_RST}" "$*"; }
die()  { printf '%s[cloudinit-size] FATAL%s %s\n' "${C_RED}" "${C_RST}" "$*" >&2; exit 2; }

# Only emit ::error:: / ::warning:: / ::notice:: workflow commands when running
# inside GitHub Actions (so local runs stay clean).
gha() { [ -n "${GITHUB_ACTIONS:-}" ] && printf '%s\n' "$*"; return 0; }

for t in "${TOFU_BIN}" "${PY_BIN}"; do
  command -v "${t}" >/dev/null 2>&1 || die "required tool not found on PATH: ${t}"
done

# ─────────────────────────────────────────────────────────────────────────────
# Fixture maps. The control-plane fixtures are intentionally byte-for-byte the
# SAME shape `scripts/lint-cloudinit.sh` uses (the two scripts render the same
# shared template) — keep them in lockstep with the main.tf templatefile() maps.
# ─────────────────────────────────────────────────────────────────────────────
fixture_cp_common() {
  cat <<'HCL'
    sovereign_fqdn                    = "t99.omani.works"
    sovereign_fqdn_slug               = "t99-omani-works"
    deployment_id                     = "test-dep-0001"
    org_name                          = "Test Org \"Quote\" & Co"
    org_email                         = "ops@t99.omani.works"
    region                            = "test-region-a"
    sovereign_region_key              = "test-region-a"
    region_canonical_label            = "xx-test-region-a-rtz-prod"
    primary_region_canonical_label    = "xx-test-region-a-rtz-prod"
    replica_region_canonical_label    = "xx-test-region-b-rtz-prod"
    cluster_mesh_name                 = "t99-omani-works-a"
    cluster_mesh_id                   = "1"
    k3s_version                       = "v1.31.5+k3s1"
    k3s_token                         = "0123456789abcdef0123456789abcdef"
    cp_private_ip                     = "10.0.1.2"
    cluster_cidr                      = "10.42.0.0/16"
    service_cidr                      = "10.96.0.0/16"
    gitops_repo_url                   = "https://github.com/openova-io/openova"
    gitops_branch                     = "main"
    ghcr_pull_username                = "openova-bot"
    ghcr_pull_token                   = "ghp_TESTtokenTESTtokenTESTtoken0001"
    ghcr_pull_auth_b64                = "b3Blbm92YS1ib3Q6Z2hwX1RFU1Q="
    harbor_robot_token                = "harborTESTrobotTESTtoken0001"
    handover_jwt_public_key           = "{\"kty\":\"RSA\",\"n\":\"test-modulus\",\"e\":\"AQAB\"}"
    powerdns_api_key                  = "pdnsTESTapikey0001"
    pdm_basic_auth_user               = "pdm-user"
    pdm_basic_auth_pass               = "pdmTESTpass0001"
    kubeconfig_bearer_token           = "kubeconfigTESTbearer0001"
    catalyst_api_url                  = "https://console.openova.io/sovereign"
    enable_unattended_upgrades        = true
    enable_fail2ban                   = true
    marketplace_enabled               = "false"
    qa_fixtures_enabled               = "false"
    console_isolation_enabled         = "true"
    continuum_enabled                 = "false"
    bcp_topology                      = "single-region"
    enable_hot_standby                = "false"
    enable_shared_pg                  = "false"
    default_storage_class             = "hcloud-volumes"
    sovereign_cnpg_instances          = "1"
    wildcard_cert_issuer              = "letsencrypt-dns01-prod-powerdns"
    parent_domains_yaml               = "[{name: \"t99.omani.works\", role: \"primary\"}]"
    sovereign_regions_json            = "[{\"provider\":\"test\",\"cloudRegion\":\"xx-test-region-a-rtz-prod\",\"controlPlaneSize\":\"cx41\",\"workerSize\":\"cx41\",\"workerCount\":2}]"
    sovereign_configured_regions_yaml = "[\"xx-test-region-a-rtz-prod\"]"
    object_storage_endpoint           = "https://fsn1.your-objectstorage.com"
    object_storage_region             = "fsn1"
    object_storage_bucket_name        = "t99-omani-works-sov"
    object_storage_access_key         = "OBSTESTACCESSKEY0001"
    object_storage_secret_key         = "OBSTESTsecretkey0001"
    load_balancer_ipv4                = "203.0.113.10"
    console_load_balancer_ipv4        = "203.0.113.11"
    pdns_api_host                     = "pdns.openova.io"
    sovereign_region_role             = "primary"
    node_external_ip_value            = "203.0.113.10"
HCL
}

fixture_cp_hetzner() {
  fixture_cp_common
  cat <<'HCL'
    provider             = "hetzner"
    node_external_ip_cmd = "curl -fsSL --retry 30 --retry-delay 2 http://169.254.169.254/hetzner/v1/metadata/public-ipv4"
    node_ip_cmd          = "echo 10.0.1.2"
    k3s_extra_args       = "--kube-apiserver-arg=oidc-issuer-url=https://auth.t99.omani.works/realms/sovereign --disable-cloud-controller --node-taint node-role.kubernetes.io/control-plane=true:NoSchedule"
    registry_mirror_yaml = <<-MIRROR
      mirrors:
        "docker.io":
          endpoint:
            - "https://harbor.openova.io"
          rewrite:
            "(.*)": "proxy-dockerhub/$1"
      configs:
        "harbor.openova.io":
          auth:
            username: "robot$openova-bot"
            password: "harborTESTrobotTESTtoken0001"
    MIRROR
    cloud_credentials_secret_yaml = <<-CREDS
      apiVersion: v1
      kind: Secret
      metadata:
        name: cloud-credentials
        namespace: flux-system
      type: Opaque
      stringData:
        hcloud-token: hcloudTESTtoken0001
        hcloud-cloud-init: IyEvYmluL3NoCmVjaG8gd29ya2Vy
        hcloud-network-name: t99-omani-works-primary
    CREDS
    crossplane_provider_yaml = <<-XP
      ---
      apiVersion: pkg.crossplane.io/v1
      kind: Provider
      metadata:
        name: provider-hcloud
      spec:
        package: xpkg.upbound.io/crossplane-contrib/provider-hcloud:v0.4.0
    XP
    provider_id_cmd = <<-PID
      - 'HCLOUD_SERVER_ID=$(curl -fsSL http://169.254.169.254/hetzner/v1/metadata/instance-id) && NODE_NAME=$(hostname) && for i in $(seq 1 30); do kubectl --kubeconfig=/etc/rancher/k3s/k3s.yaml patch node "$NODE_NAME" --type=merge -p "{\"spec\":{\"providerID\":\"hcloud://$HCLOUD_SERVER_ID\"}}" && break || sleep 5; done || echo "non-fatal"'
    PID
    provider_prelude = <<-PRE
      - |
        EXPECTED_PRIVATE_IP="10.0.1.2"
        PRIMARY_NIC=$(ip -br link | awk '$1 == "eth0" {print $1; exit}')
        for i in $(seq 1 60); do
          if ip -4 addr show | grep -qE "inet $${EXPECTED_PRIVATE_IP}/"; then break; fi
          EXTRA=$(ip -br link | awk -v primary="$${PRIMARY_NIC}" '$1 != "lo" && $1 != primary {print $1; exit}')
          sleep 2
        done
    PRE
    kubeconfig_put_block = <<-PUT
      - install -m 0600 /dev/null /etc/rancher/k3s/k3s.yaml.public
      - 'CP_PUBLIC_IPV4=$(curl -fsSL http://169.254.169.254/hetzner/v1/metadata/public-ipv4) && sed "s|https://127.0.0.1:6443|https://$${CP_PUBLIC_IPV4}:6443|g" /etc/rancher/k3s/k3s.yaml > /etc/rancher/k3s/k3s.yaml.public'
      - |
        curl -fsSL --retry 60 -X PUT \
          -H "Authorization: Bearer kubeconfigTESTbearer0001" \
          --data-binary @/etc/rancher/k3s/k3s.yaml.public \
          "https://console.openova.io/sovereign/api/v1/deployments/test-dep-0001/kubeconfig"
      - rm -f /etc/rancher/k3s/k3s.yaml.public
    PUT
HCL
}

fixture_cp_huawei() {
  fixture_cp_common
  cat <<'HCL'
    provider             = "huawei"
    node_external_ip_cmd = "printf '%s' '203.0.113.10'"
    node_ip_cmd          = "ip -4 -o addr show dev eth0 | awk '{print $4}' | cut -d/ -f1"
    k3s_extra_args       = "--node-label=topology.kubernetes.io/region=test-region-a --node-label=catalyst.openova.io/provider=huawei --kube-apiserver-arg=default-watch-cache-size=200 --kube-apiserver-arg=watch-cache-sizes=secrets#500,configmaps#500 --etcd-snapshot-retention=5"
    registry_mirror_yaml = <<-MIRROR
      mirrors:
        "harbor.openova.io":
          endpoint:
            - "http://212.72.24.20:5000"
        "docker.io":
          endpoint:
            - "http://212.72.24.20:5002"
      configs:
        "212.72.24.20:5000":
          tls:
            insecure_skip_verify: true
        "harbor.openova.io":
          auth:
            username: "robot$openova-bot"
            password: "harborTESTrobotTESTtoken0001"
    MIRROR
    cloud_credentials_secret_yaml = <<-CREDS
      apiVersion: v1
      kind: Secret
      metadata:
        name: cloud-credentials
        namespace: flux-system
      type: Opaque
      stringData:
        huawei-ak: OBSTESTACCESSKEY0001
        huawei-sk: OBSTESTsecretkey0001
        huawei-project-id: me-east-215
        huawei-region: me-east-215
    CREDS
    crossplane_provider_yaml = ""
    provider_id_cmd          = ""
    provider_prelude = <<-PRE
      - ["bash", "-c", "iptables -t nat -A PREROUTING -p tcp --dport 443 -j REDIRECT --to-port 30443 || true"]
      - ["bash", "-c", "iptables -t nat -A PREROUTING -p tcp --dport 80 -j REDIRECT --to-port 30080 || true"]
      - ["bash", "-c", "mkdir -p /etc/iptables && iptables-save > /etc/iptables/rules.v4 2>/dev/null || true"]
      - |
        if [ -n "test-dep-0001" ] && [ -n "kubeconfigTESTbearer0001" ]; then
          nohup sh -c '
            n=0
            while [ "$n" -lt 40 ]; do
              curl -sk -X PUT \
                -H "Authorization: Bearer kubeconfigTESTbearer0001" \
                --data-binary @/var/log/cloud-init-output.log \
                --max-time 20 \
                "https://console.openova.io/sovereign/api/v1/deployments/test-dep-0001/cloudinit-log" >/dev/null 2>&1 || true
              n=$((n+1))
              sleep 30
            done
          ' >/dev/null 2>&1 &
        fi
    PRE
    kubeconfig_put_block = <<-PUT
      - |
        HOSTNAME=$(hostname)
        if [ -n "test-dep-0001" ] && [ -n "kubeconfigTESTbearer0001" ]; then
          sed "s|server: https://127.0.0.1:6443|server: https://203.0.113.10:6443|" /etc/rancher/k3s/k3s.yaml > /tmp/kubeconfig-rewritten.yaml
          for attempt in 1 2 3 4 5 6; do
            HTTP_CODE=$(curl -sk -o /dev/null -w '%%{http_code}' -X PUT \
              -H "Authorization: Bearer kubeconfigTESTbearer0001" \
              --data-binary @/tmp/kubeconfig-rewritten.yaml \
              --max-time 15 \
              "https://console.openova.io/sovereign/api/v1/deployments/test-dep-0001/kubeconfig" || echo "000")
            echo "kubeconfig PUT-back attempt $attempt -> HTTP $HTTP_CODE"
            if [ "$HTTP_CODE" = "204" ] || [ "$HTTP_CODE" = "200" ]; then break; fi
            sleep 30
          done
        fi
    PUT
HCL
}

# Worker templates are per-provider and take a much smaller var set (see the
# `worker_cloud_init = templatefile(...)` call in infra/providers/<p>/main.tf).
fixture_worker_hetzner() {
  cat <<'HCL'
    sovereign_fqdn             = "t99.omani.works"
    k3s_version                = "v1.31.5+k3s1"
    k3s_token                  = "0123456789abcdef0123456789abcdef"
    cp_private_ip              = "10.0.1.2"
    enable_unattended_upgrades = true
    enable_fail2ban            = true
    harbor_robot_token         = "harborTESTrobotTESTtoken0001"
HCL
}

fixture_worker_huawei() {
  cat <<'HCL'
    sovereign_fqdn             = "t99.omani.works"
    k3s_version                = "v1.31.5+k3s1"
    k3s_token                  = "0123456789abcdef0123456789abcdef"
    cp_private_ip              = "10.0.1.2"
    region                     = "me-east-215-a"
    enable_unattended_upgrades = true
    enable_fail2ban            = true
    harbor_robot_token         = "harborTESTrobotTESTtoken0001"
HCL
}

# ─────────────────────────────────────────────────────────────────────────────
# render <tftpl_abs_path> <fixture_fn> <out_file>
#   Throwaway tofu config that renders the template via templatefile(), runs
#   init+apply, captures `tofu output -raw rendered`. Returns non-zero on error.
# ─────────────────────────────────────────────────────────────────────────────
render() {
  tftpl="$1"; fixture_fn="$2"; out_file="$3"
  [ -f "${tftpl}" ] || { err "template not found: ${tftpl}"; return 2; }
  workdir="$(mktemp -d "${TMPDIR:-/tmp}/cloudinit-size.XXXXXX")"
  {
    printf 'output "rendered" {\n'
    printf '  value = templatefile("%s", {\n' "${tftpl}"
    "${fixture_fn}"
    printf '  })\n'
    printf '}\n'
  } > "${workdir}/main.tf"
  if ! "${TOFU_BIN}" -chdir="${workdir}" init -input=false -no-color >"${workdir}/init.log" 2>&1; then
    err "tofu init failed"; sed 's/^/      /' "${workdir}/init.log" >&2; rm -rf "${workdir}"; return 1
  fi
  if ! "${TOFU_BIN}" -chdir="${workdir}" apply -auto-approve -input=false -no-color >"${workdir}/apply.log" 2>&1; then
    err "tofu apply (render) failed"; sed 's/^/      /' "${workdir}/apply.log" >&2; rm -rf "${workdir}"; return 1
  fi
  if ! "${TOFU_BIN}" -chdir="${workdir}" output -raw -no-color rendered >"${out_file}" 2>"${workdir}/output.log"; then
    err "tofu output -raw rendered failed"; sed 's/^/      /' "${workdir}/output.log" >&2; rm -rf "${workdir}"; return 1
  fi
  rm -rf "${workdir}"; return 0
}

# ─────────────────────────────────────────────────────────────────────────────
# check_surface <label> <tftpl> <fixture_fn> <cap_bytes>  -> 0 ok / 1 over
#   Renders, applies the provider comment-strip, measures, annotates.
# ─────────────────────────────────────────────────────────────────────────────
check_surface() {
  label="$1"; tftpl="$2"; fixture_fn="$3"; cap="$4"
  raw="$(mktemp "${TMPDIR:-/tmp}/cloudinit-size-raw.XXXXXX")"
  if ! render "${tftpl}" "${fixture_fn}" "${raw}"; then
    err "${label}: RENDER FAILED"
    gha "::error title=cloud-init-size ${label}::render failed — see job log"
    rm -f "${raw}"; return 1
  fi
  # Apply the byte-identical comment-strip the provider applies to user_data.
  size="$("${PY_BIN}" -c "${COMMENT_STRIP_PY}" < "${raw}" | wc -c | tr -d ' ')"
  rm -f "${raw}"
  pct="$(awk -v s="${size}" -v c="${cap}" 'BEGIN{printf "%.1f", (s*100.0)/c}')"
  if [ "${size}" -gt "${cap}" ]; then
    err "${label}: ${size} bytes EXCEEDS cap ${cap} (${pct}% of cap)"
    gha "::error title=cloud-init-size ${label} OVER CAP::${label} rendered ${size} bytes, exceeds the ${cap}-byte guardrail (${pct}% of cap). Cull comments / move bloat out of the template. See issue #966 / #3724."
    return 1
  fi
  if [ "${size}" -ge "${HEADROOM_WARN_BYTES}" ]; then
    warn "${label}: ${size} bytes — only $((cap - size)) B headroom under cap ${cap} (${pct}% of cap)"
    gha "::warning title=cloud-init-size ${label} LOW HEADROOM::${label} rendered ${size} bytes — only $((cap - size)) B under the ${cap}-byte cap (${pct}%). The cliff is near; thin the template before it trips a PR. See #3724."
  else
    ok "${label}: ${size} bytes within cap ${cap} ($((cap - size)) B headroom, ${pct}% of cap)"
    gha "::notice title=cloud-init-size ${label}::${label} rendered ${size} bytes / ${cap} cap (${pct}%, $((cap - size)) B headroom)"
  fi
  return 0
}

# ─────────────────────────────────────────────────────────────────────────────
# check_provider <provider> -> 0 if every surface within cap
# ─────────────────────────────────────────────────────────────────────────────
check_provider() {
  provider="$1"
  info "=== provider: ${provider} ==="
  shared_cp="${PROVIDERS_DIR}/_shared/cloudinit-control-plane.tftpl"
  worker_tftpl="${PROVIDERS_DIR}/${provider}/cloudinit-worker.tftpl"
  rc=0
  case "${provider}" in
    hetzner)
      check_surface "hetzner control-plane" "${shared_cp}" fixture_cp_hetzner "${CAP_CP_BYTES}" || rc=1
      # Secondary-region control-plane uses the SAME shared template + fixture
      # shape (region role differs but byte-footprint is equivalent); covering
      # it asserts the per-region render stays within cap too (#3724).
      check_surface "hetzner secondary-region control-plane" "${shared_cp}" fixture_cp_hetzner "${CAP_CP_BYTES}" || rc=1
      check_surface "hetzner worker" "${worker_tftpl}" fixture_worker_hetzner "${CAP_WORKER_BYTES}" || rc=1
      ;;
    huawei)
      check_surface "huawei control-plane" "${shared_cp}" fixture_cp_huawei "${CAP_CP_BYTES}" || rc=1
      check_surface "huawei secondary-region control-plane" "${shared_cp}" fixture_cp_huawei "${CAP_CP_BYTES}" || rc=1
      check_surface "huawei worker" "${worker_tftpl}" fixture_worker_huawei "${CAP_WORKER_BYTES}" || rc=1
      ;;
    *) err "unknown provider: ${provider} (want hetzner|huawei)"; return 2 ;;
  esac
  return "${rc}"
}

# ─────────────────────────────────────────────────────────────────────────────
# main
# ─────────────────────────────────────────────────────────────────────────────
usage() { say "usage: $(basename "$0") <hetzner|huawei|both>"; }

target="${1:-}"
[ -n "${target}" ] || { usage; exit 2; }

rc_total=0
case "${target}" in
  hetzner|huawei) check_provider "${target}" || rc_total=1 ;;
  both|all)
    check_provider hetzner || rc_total=1
    say ""
    check_provider huawei  || rc_total=1
    ;;
  -h|--help|help) usage; exit 0 ;;
  *) err "unknown target: ${target}"; usage; exit 2 ;;
esac

say ""
if [ "${rc_total}" -eq 0 ]; then
  info "${C_GRN}RESULT: every rendered cloud-init surface is within cap${C_RST}"
else
  info "${C_RED}RESULT: at least one cloud-init surface EXCEEDS its cap (see above)${C_RST}"
fi
exit "${rc_total}"
