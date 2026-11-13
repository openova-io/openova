#!/usr/bin/env bash
# Spike CLD-050 — job "git": E4 (module from the Sovereign's Git with a
# read-only .git-credentials file) and E8 (an oci:// module address).
# The Git server (Gitea, HTTPS) and the OCI registry (HTTPS) are Docker
# containers on the kind network, outside the cluster, with a spike CA the
# provider pod trusts through a mounted bundle.
set -o pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

GADM_PW="Spike-admin-0001"
GRD_PW="Spike-reader-0001"
NS=catalyst-infra
PACK_PATH="infra/providers/huawei/day2/nodepool"

drc_git() {
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
$(spike_host_aliases)
          containers:
            - name: package-runtime
              args: ["--debug"]
              env:
                - {name: AWS_ACCESS_KEY_ID, value: "$S3_ACCESS"}
                - {name: AWS_SECRET_ACCESS_KEY, value: "$S3_SECRET"}
                - {name: SSL_CERT_FILE, value: /etc/spike-ca/bundle.crt}
                - {name: GIT_SSL_CAINFO, value: /etc/spike-ca/bundle.crt}
              volumeMounts:
                - {name: spike-ca, mountPath: /etc/spike-ca, readOnly: true}
          volumes:
            - name: spike-ca
              configMap:
                name: spike-ca
EOF
}

pc_yaml() { # name with-git-credentials(yes|no)
  cat <<EOF
apiVersion: opentofu.m.upbound.io/v1beta1
kind: ClusterProviderConfig
metadata:
  name: $1
spec:
  credentials:
EOF
  if [[ $2 == yes ]]; then
    cat <<EOF
    - filename: .git-credentials
      source: Secret
      secretRef:
        namespace: crossplane-system
        name: git-credentials
        key: .git-credentials
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

ws_remote() { # name providerconfig module-address
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
    source: Remote
    module: "$3"
    vars:
      - key: marker
        value: m0
EOF
}

gitapi() { # method path [json] — as the admin user
  curl -sS --cacert "$CA" -u "spikeadmin:$GADM_PW" -X "$1" -H 'Content-Type: application/json' \
    "https://127.0.0.1:3000/api/v1$2" ${3:+-d "$3"}
}

start_gitserver() {
  docker run -d --name gitserver --network kind -p 3000:3000 \
    -v "$WORK/certs:/certs:ro" \
    -e GITEA__security__INSTALL_LOCK=true \
    -e GITEA__database__DB_TYPE=sqlite3 \
    -e GITEA__server__PROTOCOL=https \
    -e GITEA__server__HTTP_PORT=3000 \
    -e GITEA__server__ROOT_URL="https://$GIT_HOST:3000/" \
    -e GITEA__server__CERT_FILE=/certs/tls.crt \
    -e GITEA__server__KEY_FILE=/certs/tls.key \
    -e GITEA__service__DISABLE_REGISTRATION=true \
    "$GITSERVER_IMAGE" >/dev/null
  local i
  for i in $(seq 1 60); do
    curl -fsS --cacert "$CA" https://127.0.0.1:3000/api/healthz >/dev/null 2>&1 && break
    sleep 3
  done
  curl -sS --cacert "$CA" https://127.0.0.1:3000/api/healthz; echo
  docker exec -u git gitserver gitea admin user create --admin --username spikeadmin \
    --password "$GADM_PW" --email admin@spike.test --must-change-password=false
  docker exec -u git gitserver gitea admin user create --username reader \
    --password "$GRD_PW" --email reader@spike.test --must-change-password=false
  gitapi POST /orgs '{"username":"openova"}' >/dev/null
  gitapi POST /orgs/openova/repos '{"name":"provider-pack","private":true}' | jq -c '{full_name, private}'
  gitapi PUT /repos/openova/provider-pack/collaborators/reader '{"permission":"read"}'
  READ_TOKEN=$(curl -sS --cacert "$CA" -u "reader:$GRD_PW" -X POST -H 'Content-Type: application/json' \
    https://127.0.0.1:3000/api/v1/users/reader/tokens -d '{"name":"provider-pack-ro","scopes":["read:repository"]}' | jq -r .sha1)
  [[ -n $READ_TOKEN && $READ_TOKEN != null ]] || return 1

  local p="$WORK/pack"
  mkdir -p "$p/$PACK_PATH"
  cat > "$p/$PACK_PATH/main.tf" <<'EOF'
variable "marker" {
  type    = string
  default = "m0"
}
resource "terraform_data" "unit" {
  input = var.marker
}
output "marker" {
  value = terraform_data.unit.output
}
output "source" {
  value = "provider-pack"
}
EOF
  (
    cd "$p" || exit 1
    git init -q -b main
    git add .
    git -c user.name=spike -c user.email=spike@spike.test commit -qm "provider pack v0.0.1"
    git tag v0.0.1
    git -c http.sslCAInfo="$CA" push -q "https://spikeadmin:$GADM_PW@127.0.0.1:3000/openova/provider-pack.git" main v0.0.1
  ) || return 1
  return 0
}

start_registry() {
  docker run -d --name registry --network kind -p 5000:5000 \
    -v "$WORK/certs:/certs:ro" \
    -e REGISTRY_HTTP_TLS_CERTIFICATE=/certs/tls.crt -e REGISTRY_HTTP_TLS_KEY=/certs/tls.key \
    "$REGISTRY_IMAGE" >/dev/null
  local i
  for i in $(seq 1 30); do
    curl -fsS --cacert "$CA" https://127.0.0.1:5000/v2/ >/dev/null 2>&1 && return 0
    sleep 2
  done
  return 1
}

setup() {
  kind_up cld050-git "$KIND_NODE_V2" || return 1
  kind_ips
  make_certs
  start_seaweedfs || return 1
  start_gitserver || { log "Git server setup failed"; docker logs gitserver 2>&1 | tail -30; return 1; }
  start_registry || { log "registry setup failed"; docker logs registry 2>&1 | tail -30; return 1; }
  install_crossplane_v2 || return 1
  ca_configmap crossplane-system
  kubectl create namespace "$NS"
  kubectl -n crossplane-system create secret generic git-credentials \
    --from-literal=.git-credentials="https://reader:$READ_TOKEN@$GIT_HOST:3000"
  drc_git | kubectl apply -f - || return 1
  provider_yaml "$PROVIDER_V1111" provider-opentofu | kubectl apply -f - || return 1
  wait_provider_healthy 600 || return 1
  wait_provider_pod "" 300 || return 1
  ensure_workspace_kinds || return 1
  local i
  for i in $(seq 1 30); do
    { pc_yaml git-none no; echo '---'; pc_yaml git-ro yes; } | kubectl apply -f - && break
    sleep 5
  done
}

# ------------------------------------------------------------------ E4
e4() {
  local d="$EV/E4"; mkdir -p "$d"
  log "E4: module from the Sovereign's Git with source Remote and a read-only .git-credentials file"
  local addr="git::https://$GIT_HOST:3000/openova/provider-pack.git//$PACK_PATH?ref=v0.0.1"
  measure E4 module_address "$addr"
  # The credential is read-only and the repository is private.
  if (cd "$WORK/pack" && git -c http.sslCAInfo="$CA" push -q "https://reader:$READ_TOKEN@127.0.0.1:3000/openova/provider-pack.git" main:refs/heads/ro-probe) > "$d/reader-push.txt" 2>&1; then
    measure E4 reader_credential_can_push yes
  else
    measure E4 reader_credential_can_push "no ($(tail -1 "$d/reader-push.txt" | cut -c1-150))"
  fi
  if GIT_TERMINAL_PROMPT=0 git -c http.sslCAInfo="$CA" ls-remote https://127.0.0.1:3000/openova/provider-pack.git > "$d/anon-ls-remote.txt" 2>&1; then
    measure E4 anonymous_read yes
  else
    measure E4 anonymous_read "no ($(tail -1 "$d/anon-ls-remote.txt" | cut -c1-150))"
  fi

  local pod
  pod=$(provider_pod)
  kubectl -n crossplane-system exec "$pod" -- sh -c 'echo "HOME=$HOME"; id; cat /.gitconfig; git --version' > "$d/provider-pod-git.txt" 2>&1
  measure E4 provider_pod_git "$(tr '\n' ' ' < "$d/provider-pod-git.txt" | cut -c1-250)"
  # 1. Without the credential: must fail (run first; see the GIT_CRED_DIR note below).
  ws_remote e4-nocreds git-none "$addr" > "$d/ws-e4-nocreds.yaml"
  kubectl apply -f "$d/ws-e4-nocreds.yaml"
  local neg="no error seen" pos=no
  ws_wait_settle "$NS" e4-nocreds 180
  if [[ $SETTLED == error ]]; then
    neg="Synced=False: $(ws_msg "$NS" e4-nocreds | tr '\n' ' ' | cut -c1-300)"
  elif [[ $SETTLED == ready ]]; then
    neg="Ready=True (the private repository was read without a credential)"
  fi
  measure E4 without_credential "$neg"
  ws_delete "$NS" e4-nocreds 60

  # 2. With the read-only credential through the ProviderConfig.
  ws_remote e4-git git-ro "$addr" > "$d/ws-e4-git.yaml"
  kubectl apply -f "$d/ws-e4-git.yaml"
  if ws_wait_ready "$NS" e4-git 300; then
    pos=yes
    measure E4 with_credential "Ready in ${WAITED}s; outputs marker=$(ws_out "$NS" e4-git marker) source=$(ws_out "$NS" e4-git source); remoteSource=$(ws_get "$NS" e4-git '{.status.atProvider.remoteSource}')"
    kubectl -n "$NS" patch "$WSR" e4-git --type=json -p '[{"op":"replace","path":"/spec/forProvider/vars/0/value","value":"m1"}]'
    if ws_wait_output "$NS" e4-git marker m1 300; then
      measure E17 remote_module_seconds_patch_to_applied "$WAITED (job git, Remote git module, re-download on every reconcile)"
    fi
  else
    measure E4 with_credential "not Ready after ${WAITED}s: $(ws_msg "$NS" e4-git | tr '\n' ' ' | cut -c1-400)"
  fi
  ws_dump "$NS" e4-git "$d/e4-git-status.yaml"

  # 3. Again without the credential, after a credentialed Workspace ran in the same pod.
  ws_remote e4-nocreds-2 git-none "$addr" > "$d/ws-e4-nocreds-2.yaml"
  kubectl apply -f "$d/ws-e4-nocreds-2.yaml"
  local leak="unknown"
  ws_wait_settle "$NS" e4-nocreds-2 150
  if [[ $SETTLED == ready ]]; then
    leak="yes: a ProviderConfig WITHOUT credentials read the private repository after another ProviderConfig's credential was written (process-wide GIT_CRED_DIR)"
  elif [[ $SETTLED == error ]]; then
    leak="no: $(ws_msg "$NS" e4-nocreds-2 | tr '\n' ' ' | cut -c1-200)"
  fi
  measure E4 credential_shared_across_providerconfigs "$leak"
  ws_delete "$NS" e4-nocreds-2 60
  if [[ $pos == yes && $neg == Synced=False* ]]; then
    verdict E4 PASS "source Remote with git::https://<git server>/openova/provider-pack.git//<path>?ref=<tag> and a read-only .git-credentials Secret file in the ProviderConfig: Ready; the same address without the credential fails"
  else
    verdict E4 FAIL "with credential ready=$pos; without credential: $neg"
  fi
}

# ------------------------------------------------------------------ E8
e8() {
  local d="$EV/E8"; mkdir -p "$d"
  log "E8: does the provider accept an oci:// module address"
  python3 "$SPIKE_DIR/oci_push.py" module https://127.0.0.1:5000 "$CA" openova/provider-pack/nodepool v0.0.1 \
    "$WORK/pack/$PACK_PATH" 2>&1 | tee "$d/push.txt"
  local oci="oci://$REG_HOST:5000/openova/provider-pack/nodepool?tag=v0.0.1" remote_res inline_res remote_ok=no inline_ok=no
  measure E8 module_address "$oci"
  # a. As the Workspace module address with source Remote (go-getter in the provider).
  ws_remote e8-remote git-none "$oci" > "$d/ws-e8-remote.yaml"
  kubectl apply -f "$d/ws-e8-remote.yaml"
  ws_wait_settle "$NS" e8-remote 180
  if [[ $SETTLED == ready ]]; then
    remote_ok=yes; remote_res="Ready in ${WAITED}s"
  else
    remote_res="not Ready after ${WAITED}s: Synced=$(ws_cond "$NS" e8-remote Synced) $(ws_msg "$NS" e8-remote | tr '\n' ' ' | cut -c1-300)"
  fi
  measure E8 remote_source_oci "$remote_res"
  ws_dump "$NS" e8-remote "$d/e8-remote-status.yaml"
  ws_delete "$NS" e8-remote 60
  # b. Inline wrapper whose module block names the oci:// source (OpenTofu installs it).
  cat > "$WORK/e8-inline.tf" <<EOF
module "unit" {
  source = "$oci"
}
output "marker" {
  value = module.unit.marker
}
EOF
  cat > "$d/ws-e8-inline.yaml" <<EOF
apiVersion: opentofu.m.upbound.io/v1beta1
kind: Workspace
metadata:
  name: e8-inline
  namespace: $NS
  annotations:
    crossplane.io/external-name: $NS-e8-inline
spec:
  providerConfigRef:
    kind: ClusterProviderConfig
    name: git-none
  forProvider:
    source: Inline
    module: |
$(indent 6 < "$WORK/e8-inline.tf")
EOF
  kubectl apply -f "$d/ws-e8-inline.yaml"
  if ws_wait_ready "$NS" e8-inline 240; then
    inline_ok=yes; inline_res="Ready in ${WAITED}s, output marker=$(ws_out "$NS" e8-inline marker)"
  else
    inline_res="not Ready after ${WAITED}s: $(ws_msg "$NS" e8-inline | tr '\n' ' ' | cut -c1-300)"
  fi
  measure E8 inline_wrapper_oci_module_block "$inline_res"
  ws_dump "$NS" e8-inline "$d/e8-inline-status.yaml"
  if [[ $remote_ok == yes ]]; then
    verdict E8 PASS "spec.forProvider.module accepts oci:// with source Remote"
  else
    verdict E8 FAIL "spec.forProvider.module with source Remote does not accept oci:// ($remote_res). Inline wrapper with a module block: $([[ $inline_ok == yes ]] && echo works || echo fails)"
  fi
}

[[ ${CLD050_SOURCE_ONLY:-} == 1 ]] && return 0  # lets a reviewer source the helpers without running
if ! setup; then
  log "SETUP FAILED"
  kubectl get pods -A -o wide 2>&1 | tail -40
  verdict E4 NOT-RUN "setup failed"; verdict E8 NOT-RUN "setup failed"
  summary git
  exit 1
fi
e4
e8
summary git
