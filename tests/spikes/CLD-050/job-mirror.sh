#!/usr/bin/env bash
# Spike CLD-050 — job "mirror": E5. Can provider binaries come only from
# the Sovereign's registry? The provider pod's /etc/hosts sends every
# public OpenTofu/HashiCorp/GitHub host to 127.0.0.1 (hostAliases), so a
# direct install fails; the providers are offered from an OCI registry
# (oci_mirror, OpenTofu >= 1.10) and from an HTTPS network mirror.
set -o pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

NS=catalyst-infra
BLOCKED_HOSTS='"registry.opentofu.org", "registry.terraform.io", "releases.hashicorp.com", "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com", "get.opentofu.org", "opentofu.org"'

drc_mirror() {
  cat <<EOF
apiVersion: pkg.crossplane.io/v1beta1
kind: DeploymentRuntimeConfig
metadata:
  name: provider-opentofu
spec:
  deploymentTemplate:
    spec:
      selector: {}
      template:
        spec:
          hostAliases:
            - ip: "127.0.0.1"
              hostnames: [$BLOCKED_HOSTS]
$(spike_host_aliases)
          containers:
            - name: package-runtime
              args: ["--debug"]
              env:
                - {name: AWS_ACCESS_KEY_ID, value: "$S3_ACCESS"}
                - {name: AWS_SECRET_ACCESS_KEY, value: "$S3_SECRET"}
                - {name: SSL_CERT_FILE, value: /etc/spike-ca/bundle.crt}
                - {name: TF_CLI_CONFIG_FILE, value: /etc/spike-tofu/oci.tofurc}
              volumeMounts:
                - {name: spike-ca, mountPath: /etc/spike-ca, readOnly: true}
                - {name: spike-tofu, mountPath: /etc/spike-tofu, readOnly: true}
          volumes:
            - name: spike-ca
              configMap:
                name: spike-ca
            - name: spike-tofu
              configMap:
                name: spike-tofu
EOF
}

pc_yaml() { # name [secret-with-.tofurc]
  cat <<EOF
apiVersion: opentofu.m.upbound.io/v1beta1
kind: ClusterProviderConfig
metadata:
  name: $1
spec:
  pluginCache: false
  credentials:
EOF
  if [[ -n ${2:-} ]]; then
    cat <<EOF
    - filename: .tofurc
      source: Secret
      secretRef:
        namespace: crossplane-system
        name: $2
        key: tofurc
EOF
  else
    cat <<EOF
    - filename: none.env
      source: None
EOF
  fi
  cat <<EOF
  configuration: |
$(s3_backend_hcl "http://$S3_IP:8333" | indent 4)
EOF
}

ws_random() { # name providerconfig [cli-config-file-for-workspace-env]
  cat <<EOF
apiVersion: opentofu.m.upbound.io/v1beta1
kind: Workspace
metadata:
  name: $1
  namespace: $NS
  annotations:
    crossplane.io/external-name: $NS-$1
spec:
  providerConfigRef:
    kind: ClusterProviderConfig
    name: $2
  forProvider:
    source: Inline
    module: |
$(indent 6 < "$WORK/e5.tf")
EOF
  if [[ -n ${3:-} ]]; then
    cat <<EOF
    env:
      - name: TF_CLI_CONFIG_FILE
        value: $3
EOF
  fi
}

setup() {
  kind_up cld050-mirror "$KIND_NODE_V2" || return 1
  kind_ips
  make_certs
  start_seaweedfs || return 1
  # Registry (OCI mirror) and nginx (network mirror), both HTTPS with the spike CA.
  docker run -d --name registry --network kind -p 5000:5000 \
    -v "$WORK/certs:/certs:ro" \
    -e REGISTRY_HTTP_TLS_CERTIFICATE=/certs/tls.crt -e REGISTRY_HTTP_TLS_KEY=/certs/tls.key \
    "$REGISTRY_IMAGE" >/dev/null
  cat > "$WORK/e5.tf" <<'EOF'
terraform {
  required_providers {
    random = {
      source  = "hashicorp/random"
      version = "3.6.3"
    }
  }
}
resource "random_id" "unit" {
  byte_length = 4
}
output "id" {
  value = random_id.unit.hex
}
EOF
  mkdir -p "$WORK/mirror-src"
  cp "$WORK/e5.tf" "$WORK/mirror-src/main.tf"
  (cd "$WORK/mirror-src" && tofu providers mirror -platform=linux_amd64 "$WORK/netmirror") || return 1
  find "$WORK/netmirror" -type f | tee "$EV/netmirror-files.txt"
  local zip
  zip=$(find "$WORK/netmirror" -name 'terraform-provider-random_3.6.3_linux_amd64.zip' | head -1)
  [[ -n $zip ]] || return 1
  local i
  for i in $(seq 1 30); do curl -fsS --cacert "$CA" https://127.0.0.1:5000/v2/ >/dev/null 2>&1 && break; sleep 2; done
  python3 "$SPIKE_DIR/oci_push.py" provider https://127.0.0.1:5000 "$CA" tofu-providers/hashicorp/random 3.6.3 "$zip" linux amd64 \
    | tee "$EV/oci-push.txt" || return 1
  cat > "$WORK/nginx.conf" <<'EOF'
server {
  listen 8443 ssl;
  ssl_certificate     /certs/tls.crt;
  ssl_certificate_key /certs/tls.key;
  root /mirror;
}
EOF
  chmod -R a+rX "$WORK/netmirror"
  docker run -d --name webmirror --network kind -p 8443:8443 \
    -v "$WORK/netmirror:/mirror:ro" -v "$WORK/certs:/certs:ro" \
    -v "$WORK/nginx.conf:/etc/nginx/conf.d/default.conf:ro" "$NGINX_IMAGE" >/dev/null
  for i in $(seq 1 30); do
    curl -fsS --cacert "$CA" https://127.0.0.1:8443/registry.opentofu.org/hashicorp/random/index.json && break
    sleep 2
  done
  echo
  install_crossplane_v2 || return 1
  ca_configmap crossplane-system
  cat > "$WORK/oci.tofurc" <<EOF
provider_installation {
  oci_mirror {
    repository_template = "$REG_HOST:5000/tofu-providers/\${namespace}/\${type}"
    include             = ["registry.opentofu.org/*/*"]
  }
}
EOF
  cat > "$WORK/net.tofurc" <<EOF
provider_installation {
  network_mirror {
    url = "https://$WEB_HOST:8443/"
  }
}
EOF
  : > "$WORK/empty.tofurc"
  cp "$WORK"/*.tofurc "$EV/"
  kubectl -n crossplane-system create configmap spike-tofu --from-file="$WORK/oci.tofurc" \
    --from-file="$WORK/net.tofurc" --from-file="$WORK/empty.tofurc"
  kubectl -n crossplane-system create secret generic tofurc-oci --from-file=tofurc="$WORK/oci.tofurc"
  kubectl -n crossplane-system create secret generic tofurc-net --from-file=tofurc="$WORK/net.tofurc"
  kubectl create namespace "$NS"
  drc_mirror | tee "$EV/deploymentruntimeconfig.yaml" | kubectl apply -f - || return 1
  provider_yaml "$PROVIDER_V1111" provider-opentofu | kubectl apply -f - || return 1
  wait_provider_healthy 600 || return 1
  wait_provider_pod "" 300 || return 1
  ensure_workspace_kinds || return 1
  for i in $(seq 1 30); do
    { pc_yaml mirror-none; echo '---'; pc_yaml mirror-oci tofurc-oci; echo '---'; pc_yaml mirror-net tofurc-net; } \
      | kubectl apply -f - && break
    sleep 5
  done
}

run_case() { # measure-key workspace providerconfig [workspace-env-cli-file] -> sets CASE
  local d="$EV/E5"
  ws_random "$2" "$3" "${4:-}" > "$d/ws-$2.yaml"
  kubectl apply -f "$d/ws-$2.yaml"
  ws_wait_settle "$NS" "$2" 300
  local first=$WAITED msg
  if [[ $SETTLED == error ]]; then
    # A first error can be transient: give the retry 120 s more before calling it.
    msg=$(ws_msg "$NS" "$2" | tr '\n' ' ')
    ws_wait_ready "$NS" "$2" 120 && SETTLED="ready-after-retry"
  fi
  case $SETTLED in
    ready|ready-after-retry) CASE=ready; measure E5 "$1" "Ready ($SETTLED) in $((first + WAITED))s, random_id=$(ws_out "$NS" "$2" id)" ;;
    error) CASE=error; measure E5 "$1" "Synced=False after ${first}s: $(echo "$msg" | grep -oE '(connection refused|Failed to [a-z ]+|could not [a-z ]+|no available [a-z ]+|x509: [a-z ]+|[a-z.]+:[0-9]+: connect)' | sort -u | head -6 | tr '\n' ';') | $(echo "$msg" | cut -c1-260)" ;;
    *) CASE=timeout; measure E5 "$1" "no result after ${WAITED}s" ;;
  esac
  ws_dump "$NS" "$2" "$d/$2-status.yaml" >/dev/null
  ws_delete "$NS" "$2" 120
}

e5() {
  local d="$EV/E5"; mkdir -p "$d"
  log "E5: provider binaries only from the Sovereign registry"
  local pod
  pod=$(provider_pod)
  kubectl -n crossplane-system exec "$pod" -- sh -c 'tofu version; cat /etc/hosts' > "$d/pod-tofu-and-hosts.txt" 2>&1
  measure E5 tofu_version_in_pod "$(head -1 "$d/pod-tofu-and-hosts.txt")"
  if kubectl -n crossplane-system exec "$pod" -- curl -sS -m 8 -o /dev/null https://registry.opentofu.org/.well-known/terraform.json > "$d/pod-public-probe.txt" 2>&1; then
    measure E5 public_registry_from_pod "reachable (block NOT effective)"
  else
    measure E5 public_registry_from_pod "blocked: $(tr '\n' ' ' < "$d/pod-public-probe.txt" | cut -c1-150)"
  fi
  measure E5 public_registry_from_runner "HTTP $(curl -s -m 8 -o /dev/null -w '%{http_code}' https://registry.opentofu.org/.well-known/terraform.json)"

  local neg podenv oci net wsenv
  run_case direct_install_negative_control e5-neg mirror-none /etc/spike-tofu/empty.tofurc; neg=$CASE
  run_case pod_env_TF_CLI_CONFIG_FILE e5-podenv mirror-none; podenv=$CASE
  run_case oci_mirror_via_providerconfig_tofurc e5-oci mirror-oci; oci=$CASE
  run_case network_mirror_via_providerconfig_tofurc e5-net mirror-net; net=$CASE
  run_case oci_mirror_via_workspace_env e5-wsenv mirror-none /etc/spike-tofu/oci.tofurc; wsenv=$CASE
  provider_logs 400 | grep -iE 'e5-(neg|podenv|oci|net|wsenv)' | tail -40 > "$d/provider-log.txt"
  if [[ $neg == error && ( $oci == ready || $net == ready ) ]]; then
    verdict E5 PASS "with public registries unreachable from the pod (direct install fails), tofu init installed hashicorp/random 3.6.3 from the OCI mirror: $oci, from the network mirror: $net. The CLI configuration must reach tofu as ./.tofurc (ProviderConfig credential file) or per-Workspace env: pod-level TF_CLI_CONFIG_FILE gave $podenv"
  else
    verdict E5 FAIL "negative=$neg oci=$oci network=$net podenv=$podenv wsenv=$wsenv"
  fi
}

[[ ${CLD050_SOURCE_ONLY:-} == 1 ]] && return 0  # lets a reviewer source the helpers without running
if ! setup; then
  log "SETUP FAILED"
  kubectl get pods -A -o wide 2>&1 | tail -40
  verdict E5 NOT-RUN "setup failed"
  summary mirror
  exit 1
fi
e5
summary mirror
