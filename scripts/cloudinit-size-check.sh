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
# WHY IT MEASURES A WORST CASE (#7893): until 14 Nov 2026 this script rendered
# the shared template with short reference values (t99.omani.works, 28-character
# tokens, a 47-character stand-in for the handover key, one region) and measured
# about 30,850 bytes for the Huawei control plane. A real two-region Huawei
# installation from the same commit rendered about 32,931 bytes, and the Huawei
# ECS API refused both control-plane servers: `the size of userdata [43908] is
# over quota limits [43692]` (43,692 is the base64 form of 32,769 bytes). The
# script passed because a real installation's values are about 2,080 bytes
# longer than the reference ones.
#
# So the gate is now the WORST-CASE render (scripts/cloudinit-size-check.py):
# for each provider the control plane of the primary and of the secondary
# region and the worker of both, rendered from the provider module's own
# expressions in infra/providers/<p>/main.tf with the longest values a real
# installation can carry: the longest FQDN the naming rules allow, two
# regions, every optional input set, tokens and keys of their real lengths.
# What each length stands for is written beside it: the lengths every provider
# shares in scripts/cloudinit-size-check.py, the rest and the limits in
# infra/providers/<p>/user_data_worst_case.py. The same worst-case variables
# are written into the module test (infra/providers/<p>/tests/
# multi_region.tftest.hcl, between its marker lines; refresh them with
# `scripts/cloudinit-size-check.py --write-fixtures`), and this script fails
# when the two differ. The values
# pass through the module's own variables.tf validation. Each surface is held
# to the limit a real installation meets, the cloud API's limit or the module's
# precondition when that is smaller:
#   huawei   control plane 32768, worker 32768 (the ECS API: 32 KiB of raw
#            user data, measured as above; the module has no precondition)
#   hetzner  primary control plane 32576, secondary control plane 32256,
#            worker 30720 (the preconditions in infra/providers/hetzner/main.tf,
#            all under the API's hard cap of 32768, issue #966)
# The reference render with the short values is still printed, for
# comparison, and is not gated.
#
# Every surface is rendered from the REAL templates (the ONE shared
# cloud-agnostic control-plane template + each provider's worker template),
# with the SAME comment-strip the provider applies before handing user_data to
# the cloud API, and with a UNIQUE per-surface name so the corresponding GitHub
# check (`cloud-init-size / hetzner`, `cloud-init-size / huawei`) can be marked
# a required status check and will never collide with another job's name.
#
# It also emits GitHub-Actions workflow annotations:
#   ::error::    when a worst-case surface EXCEEDS its limit (gate FAILS).
#   ::warning::  when a worst-case surface is within ${WARN_MARGIN_BYTES} bytes of
#                its limit — the cliff is visible BEFORE a PR trips it.
#   ::notice::   the exact byte count, limit and margin of every surface otherwise.
#
# It does NOT provision anything, touch the network, or modify any template.
# It only renders (via `templatefile()` and `tofu apply` of a configuration with
# no provider and no resource) + measures.
#
# USAGE:
#   scripts/cloudinit-size-check.sh hetzner
#   scripts/cloudinit-size-check.sh huawei
#   scripts/cloudinit-size-check.sh both      # check both, aggregate exit status
#
# EXIT: 0 = every worst-case surface for the target provider(s) is within its limit.
#       1 = at least one surface EXCEEDS its limit (or a render failed).
#       2 = bad invocation / missing tool.
#
# REQUIREMENTS: tofu (templatefile rendering — no providers needed), python3.
set -u

# A worst-case surface within this many bytes of its limit gets a warning.
WARN_MARGIN_BYTES="${WARN_MARGIN_BYTES:-1024}"

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
    k3s_server_token                  = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
    k3s_agent_token                   = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
    # SEC-152: the outputs of a static-node-file seal hook (contract C8).
    seal_kind                         = "static-node-file"
    seal_stanza                       = "seal \"static\" {\n  current_key_id = \"0123456789abcdef\"\n  current_key    = \"file:///openbao/seal/openbao-seal-0123456789abcdef.key\"\n}\n"
    seal_node_files                   = [{ path = "/var/lib/catalyst/seal/openbao-seal-0123456789abcdef.key", owner = "100:1000", permissions = "0400", content_b64 = "AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8=" }]
    cp_private_ip                     = "10.0.1.2"
    cluster_cidr                      = "10.42.0.0/16"
    pod_supernet                      = "10.42.0.0/15"
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
    # SOV-133: a complete variables document (contract C5) for this fixture,
    # with test domains only; the template renders its key HANDOVER_AUTO.
    variables = {
      SOVEREIGN_MODE                     = "sovereign"
      SOVEREIGN_CONSUME                  = "origin"
      HANDOVER_AUTO                      = "false"
      HANDOVER_HOLD_SECONDS              = "600"
      REGISTRY_HOST                      = "ghcr.io"
      REGISTRY_SCHEME                    = "https"
      LOCAL_REGISTRY_HOST                = "registry.t99.omani.works"
      ORIGIN_REGISTRY_HOST               = "ghcr.io"
      ORIGIN_REGISTRY_SCHEME             = "https"
      ORIGIN_REGISTRY_CA_B64             = ""
      ORIGIN_PROXY_ENDPOINTS_JSON        = "{}"
      REGISTRY_PULL_SECRET               = "ghcr-pull"
      REGISTRY_PATH_CHARTS               = "openova-io"
      REGISTRY_PATH_RELEASE              = "openova-io/release"
      REGISTRY_PATH_PROVIDER_PACKS       = "openova-io/provider-packs"
      REGISTRY_PATH_XPKG                 = "proxy-xpkg"
      REGISTRY_PATH_TOFU_PROVIDERS       = "tofu-providers"
      REGISTRY_PATH_NODE_ARTIFACTS       = "node-artifacts"
      REGISTRY_PATH_VULN_DB              = "proxy-vulndb/aquasecurity/trivy-db"
      REGISTRY_PROXY_PROJECTS_JSON       = "{\"docker.io\":\"proxy-dockerhub\",\"gcr.io\":\"proxy-gcr\",\"ghcr.io\":\"proxy-ghcr\",\"public.ecr.aws\":\"proxy-ecr\",\"quay.io\":\"proxy-quay\",\"registry-1.docker.io\":\"proxy-dockerhub\",\"registry.k8s.io\":\"proxy-k8s\",\"xpkg.upbound.io\":\"proxy-xpkg\"}"
      RELEASE_PUBLIC_KEY_B64             = ""
      RELEASE_CHANNEL                    = "stable"
      RELEASE_VERSION                    = ""
      RELEASE_AUTO                       = "false"
      RELEASE_SYNC_SCHEDULE              = "0 3 * * *"
      ORIGIN_PARENT_FQDN                 = "t98.omani.works"
      ORIGIN_PARENT_API_URL              = "https://console.t98.omani.works/sovereign"
      DNS_MODE                           = "delegated"
      DNS_UPSTREAM_API_URL               = ""
      MAIL_MODE                          = "local"
      MAIL_RELAY_HOST                    = ""
      MAIL_RELAY_PORT                    = "587"
      MAIL_FROM                          = ""
      MAIL_RELAY_SECRET                  = ""
      K3S_VERSION                        = "v1.31.5+k3s1"
      K3S_UPGRADE_CONCURRENCY            = "1"
      CLOUD_CREDENTIAL_MODE              = "supplied"
      CLOUD_CREDENTIAL_ROTATION_SCHEDULE = "0 2 1 * *"
      CLOUD_CREDENTIAL_ROTATION_NONCE    = ""
      GITOPS_REPO_URL                    = "http://gitea-http.gitea.svc.cluster.local:3000/openova/provider-pack"
    }
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
    sovereign_region_role             = "primary"
    # #5017 keystone — provider API CIDRs (inline-YAML array) for the step-08
    # deny-egress allow-list; every real templatefile() map supplies it.
    provider_api_cidrs_yaml           = "[]"
HCL
}

# CLD-131 — the input provider_substitutes_yaml as the pack's main.tf passes
# it: infra/providers/<pack>/provider-substitutes.yaml.tftpl rendered with
# the values that fixture_cp_common gives the same keys. The pack is the
# caller's name without the prefix fixture_cp_.
fixture_substitutes() {
  printf '    provider_substitutes_yaml = templatefile("%s/%s/provider-substitutes.yaml.tftpl", {\n' "${PROVIDERS_DIR}" "${FUNCNAME[1]#fixture_cp_}"
  fixture_cp_common | grep -E '^    (region|sovereign_fqdn_slug|default_storage_class|sovereign_region_role) +=' | sed 's/^/  /'
  # CLD-132: the shared template reads none of these three; only the
  # substitutes file of a pack does.
  printf '      node_external_ip_value = "203.0.113.10"\n      clustermesh_proxy_port = 12379\n      pdns_api_host          = "pdns.openova.io"\n'
  printf '    })\n'
  # CLD-132: the overlay folder of the pack, named like the substitutes file.
  printf '    provider_overlay_path = "./clusters/_template/providers/%s"\n' "${FUNCNAME[1]#fixture_cp_}"
}

fixture_cp_hetzner() {
  fixture_cp_common
  fixture_substitutes
  cat <<'HCL'
    provider             = "hetzner"
    node_ipv4_only              = false
    fail_mark_cmd               = "touch /var/lib/catalyst/"
    render_region_kustomization = false
    gateway_host_network        = false
    seal_volume_device          = ""
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
  fixture_substitutes
  cat <<'HCL'
    provider             = "huawei"
    node_ipv4_only              = true
    fail_mark_cmd               = "sh /var/lib/catalyst/mark "
    render_region_kustomization = true
    gateway_host_network        = true
    seal_volume_device          = "/dev/vdb"
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
      - |
        install -m 700 /dev/null /var/lib/catalyst/mark
        printf '%s\n' 'touch "/var/lib/catalyst/$1"' 'echo "CLOUDINIT-SENTINEL: $1"' 'sleep 1' 'curl -4 -sk -X PUT -H "Authorization: Bearer kubeconfigTESTbearer0001" -H "Content-Type: text/plain" --data-binary @/var/log/cloud-init-output.log --max-time 20 "https://console.openova.io/sovereign/api/v1/deployments/test-dep-0001/cloudinit-log?node=$(hostname)" >/dev/null 2>&1' >/var/lib/catalyst/mark
    PRE
    # #6339 — MIRRORS the real primary-region block in
    # infra/providers/huawei/main.tf (the `%{ if idx == 0 }` arm). The previous
    # stub was ~1.2 KB SMALLER than the block the CP really carries, so every
    # byte measurement this gate has ever reported for Huawei was optimistic.
    # The primary arm is the larger of the two per-region arms, so using it for
    # both surfaces below keeps the measurement conservative.
    # scripts/check-cloudinit-primary-kubeconfig-capture.sh fails if this copy
    # loses the real block's load-bearing tokens.
    kubeconfig_put_block = <<-PUT
      - |
        HOSTNAME=$(hostname)
        PRIMARY_HOST='catalyst-t99-omani-works-testdep0-test-region-a-cp1-abc123'
        if [ -n "test-dep-0001" ] && [ -n "kubeconfigTESTbearer0001" ] && [ "$${HOSTNAME}" = "$${PRIMARY_HOST}" ]; then
          sed "s|server: https://127.0.0.1:6443|server: https://203.0.113.10:6443|" /etc/rancher/k3s/k3s.yaml > /tmp/kubeconfig-rewritten.yaml
          LAST_CODE=000
          for attempt in 1 2 3 4 5 6 7 8 9 10 11 12; do
            LAST_CODE=$(curl -sk -o /dev/null -w '%%{http_code}' -X PUT \
              -H "Authorization: Bearer kubeconfigTESTbearer0001" \
              -H "Content-Type: application/x-yaml" \
              --data-binary @/tmp/kubeconfig-rewritten.yaml \
              --max-time 15 \
              "https://console.openova.io/sovereign/api/v1/deployments/test-dep-0001/kubeconfig" || echo "000")
            echo "primary kubeconfig PUT-back attempt $${attempt} -> HTTP $${LAST_CODE}"
            case "$${LAST_CODE}" in 2*) break;; esac
            sleep 30
          done
          case "$${LAST_CODE}" in
            2*) ;;
            *)
              echo "PRIMARY-KUBECONFIG-CAPTURE-FAILED region=test-region-a lastHTTP=$${LAST_CODE} after 12 attempts; GET /api/v1/deployments/test-dep-0001/kubeconfig stays 409 until this is repaired"
              export MY_NODE_IP=$(ip -4 -o addr show dev eth0 | awk '{print $4}' | cut -d/ -f1)
              python3 -c "import json,os; print(json.dumps({'deploymentId':'test-dep-0001','regionKey':'test-region-a','kubeconfigYaml':open('/tmp/kubeconfig-rewritten.yaml').read(),'nodeInternalIp':os.environ.get('MY_NODE_IP','')}))" > /tmp/primary-kubeconfig-body.json
              FALLBACK_CODE=$(curl -sk -o /dev/null -w '%%{http_code}' -X POST \
                -H "Authorization: Bearer kubeconfigTESTbearer0001" \
                -H "Content-Type: application/json" \
                --data-binary @/tmp/primary-kubeconfig-body.json \
                --max-time 15 \
                "https://console.openova.io/sovereign/api/v1/sovereign/secondary-kubeconfig" || echo "000")
              echo "PRIMARY-KUBECONFIG-FALLBACK per-region-channel regionKey=test-region-a -> HTTP $${FALLBACK_CODE}"
              rm -f /tmp/primary-kubeconfig-body.json
              sh /var/lib/catalyst/mark kubeconfig-put-FAILED || true
              ;;
          esac
        fi
    PUT
HCL
}

# Worker templates are per-provider and take a much smaller var set (see the
# `worker_cloud_init = templatefile(...)` call in infra/providers/<p>/main.tf).
# SOV-132: a worker writes the registry file the module builds as it is, so the
# reference render passes the file the module builds for the worst-case
# document (WORKER_REGISTRY_FILE, read by check_provider from the worst-case
# worker render), never an empty map that would leave its bytes out.
fixture_worker_hetzner() {
  cat <<'HCL'
    sovereign_fqdn             = "t99.omani.works"
    k3s_version                = "v1.31.5+k3s1"
    k3s_token                  = "0123456789abcdef0123456789abcdef"
    cp_private_ip              = "10.0.1.2"
    enable_unattended_upgrades = true
    enable_fail2ban            = true
HCL
  printf '    registry_mirror_yaml       = file("%s")\n' "${WORKER_REGISTRY_FILE}"
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
HCL
  printf '    registry_mirror_yaml       = file("%s")\n' "${WORKER_REGISTRY_FILE}"
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
# reference_surface <label> <tftpl> <fixture_fn>  -> 0 rendered / 1 render failed
#   Renders the short reference values, applies the provider comment-strip and
#   prints the size. Not gated: a real installation's values are longer (#7893).
# ─────────────────────────────────────────────────────────────────────────────
reference_surface() {
  label="$1"; tftpl="$2"; fixture_fn="$3"
  raw="$(mktemp "${TMPDIR:-/tmp}/cloudinit-size-raw.XXXXXX")"
  if ! render "${tftpl}" "${fixture_fn}" "${raw}"; then
    err "${label}: RENDER FAILED"
    gha "::error title=cloud-init-size ${label}::render failed — see job log"
    rm -f "${raw}"; return 1
  fi
  # Apply the byte-identical comment-strip the provider applies to user_data.
  size="$("${PY_BIN}" -c "${COMMENT_STRIP_PY}" < "${raw}" | wc -c | tr -d ' ')"
  rm -f "${raw}"
  say "  INFO ${label}: ${size} bytes (short reference values; not gated)"
  return 0
}

# ─────────────────────────────────────────────────────────────────────────────
# judge <provider> <label> <bytes> <limit>  -> 0 within / 1 over
#   One worst-case surface against the limit a real installation meets.
# ─────────────────────────────────────────────────────────────────────────────
judge() {
  provider="$1"; label="$2"; size="$3"; limit="$4"
  margin=$((limit - size))
  pct="$(awk -v s="${size}" -v c="${limit}" 'BEGIN{printf "%.1f", (s*100.0)/c}')"
  if [ "${size}" -gt "${limit}" ]; then
    err "${provider} ${label}: ${size} bytes EXCEEDS the limit ${limit} by $((-margin)) B (${pct}% of the limit)"
    gha "::error title=cloud-init-size ${provider} ${label} OVER LIMIT::${provider} ${label} rendered ${size} bytes, $((-margin)) B over the ${limit}-byte limit a real installation meets (${pct}%). Pack or move bytes out of the template. See #7893."
    return 1
  fi
  if [ "${margin}" -lt "${WARN_MARGIN_BYTES}" ]; then
    warn "${provider} ${label}: ${size} bytes, limit ${limit}, margin only ${margin} B (${pct}% of the limit)"
    gha "::warning title=cloud-init-size ${provider} ${label} LOW MARGIN::${provider} ${label} rendered ${size} bytes, only ${margin} B under the ${limit}-byte limit (${pct}%). The cliff is near. See #7893."
  else
    ok "${provider} ${label}: ${size} bytes, limit ${limit}, margin ${margin} B (${pct}% of the limit)"
    gha "::notice title=cloud-init-size ${provider} ${label}::${size} bytes / ${limit} limit (${pct}%, margin ${margin} B)"
  fi
  return 0
}

# ─────────────────────────────────────────────────────────────────────────────
# check_provider <provider> -> 0 if every worst-case surface is within its limit
# ─────────────────────────────────────────────────────────────────────────────
check_provider() {
  provider="$1"
  info "=== provider: ${provider} ==="
  shared_cp="${PROVIDERS_DIR}/_shared/cloudinit-control-plane.tftpl"
  worker_tftpl="${PROVIDERS_DIR}/${provider}/cloudinit-worker.tftpl"
  rc=0
  # The gate: the worst-case surfaces, rendered from the module's own
  # expressions (scripts/cloudinit-size-check.py). Each line is
  # "<label><TAB><bytes><TAB><limit>". DUMP_DIR, when set, keeps the renders.
  out="$(mktemp "${TMPDIR:-/tmp}/cloudinit-size-worst.XXXXXX")"
  dump="${DUMP_DIR:-$(mktemp -d "${TMPDIR:-/tmp}/cloudinit-size-dump.XXXXXX")}"
  if ! TOFU_BIN="${TOFU_BIN}" "${PY_BIN}" "${SCRIPT_DIR}/cloudinit-size-check.py" "${provider}" --dump "${dump}" > "${out}"; then
    err "${provider}: the worst-case render FAILED (see above)"
    gha "::error title=cloud-init-size ${provider}::the worst-case render failed — see job log"
    rm -f "${out}"; [ -n "${DUMP_DIR:-}" ] || rm -rf "${dump}"; return 1
  fi
  # SOV-132: the registry file the module builds for the worst-case document,
  # as the worst-case worker writes it, for the worker reference render.
  WORKER_REGISTRY_FILE="$(mktemp "${TMPDIR:-/tmp}/cloudinit-size-registries.XXXXXX")"
  awk '$0 == "  - path: /etc/rancher/k3s/registries.yaml" {f = 1; next}
       f == 1 && $0 == "    content: |" {f = 2; next}
       f == 2 && /^      / {print substr($0, 7); next}
       f == 2 {exit}' "${dump}/${provider}_worker_primary_region_worst_case.yaml" > "${WORKER_REGISTRY_FILE}"
  [ -n "${DUMP_DIR:-}" ] || rm -rf "${dump}"
  if [ ! -s "${WORKER_REGISTRY_FILE}" ]; then
    err "${provider}: the worst-case worker render writes no /etc/rancher/k3s/registries.yaml"
    rc=1
  fi
  case "${provider}" in
    hetzner)
      reference_surface "hetzner control-plane, reference" "${shared_cp}" fixture_cp_hetzner || rc=1
      reference_surface "hetzner worker, reference" "${worker_tftpl}" fixture_worker_hetzner || rc=1
      ;;
    huawei)
      reference_surface "huawei control-plane, reference" "${shared_cp}" fixture_cp_huawei || rc=1
      reference_surface "huawei worker, reference" "${worker_tftpl}" fixture_worker_huawei || rc=1
      ;;
    *) err "unknown provider: ${provider} (want hetzner|huawei)"; rm -f "${out}" "${WORKER_REGISTRY_FILE}"; return 2 ;;
  esac
  rm -f "${WORKER_REGISTRY_FILE}"
  surfaces=0
  tab="$(printf '\t')"
  while IFS="${tab}" read -r label size limit; do
    [ -n "${label}" ] || continue
    surfaces=$((surfaces + 1))
    judge "${provider}" "${label}" "${size}" "${limit}" || rc=1
  done < "${out}"
  rm -f "${out}"
  # Four surfaces per provider: the control plane and the worker of the
  # primary and of the secondary region. Fewer means something was not measured.
  if [ "${surfaces}" -ne 4 ]; then
    err "${provider}: the worst-case render gave ${surfaces} surfaces, not 4"
    rc=1
  fi
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
  info "${C_GRN}RESULT: every worst-case cloud-init surface is within its limit${C_RST}"
else
  info "${C_RED}RESULT: at least one worst-case cloud-init surface EXCEEDS its limit, or a render failed (see above)${C_RST}"
fi
exit "${rc_total}"
