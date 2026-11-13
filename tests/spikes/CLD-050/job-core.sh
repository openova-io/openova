#!/usr/bin/env bash
# Spike CLD-050 — job "core": E1, E2, E3, E6, E13, then E15, E16, E17.
# Crossplane 2.4.3 + provider-opentofu v1.1.11 on kind; the object store
# (SeaweedFS, S3 API) and the scratch OpenBao of E13 run as Docker
# containers OUTSIDE the cluster; the "Sovereign's" OpenBao runs inside it.
set -o pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

NS_INFRA=catalyst-infra
TOKEN_DIR=/var/run/secrets/openova/tofu-state
CANARY_TEXT="CLD050-PLAINTEXT-CANARY"

bao() { kubectl -n openbao exec -i deploy/openbao -- bao "$@"; }

tf_encryption_hcl() {
  cat <<EOF
key_provider "openbao" "sovereign" {
  address  = "http://openbao.openbao.svc.cluster.local:8200"
  token    = trimspace(file("$TOKEN_DIR/token"))
  key_name = "sovereign-tofu-state"
}
method "aes_gcm" "sovereign" {
  keys = key_provider.openbao.sovereign
}
state {
  method   = method.aes_gcm.sovereign
  enforced = true
}
plan {
  method   = method.aes_gcm.sovereign
  enforced = true
}
EOF
}

drc_core() { # yes|no — with TF_ENCRYPTION or without
  cat <<EOF
apiVersion: pkg.crossplane.io/v1beta1
kind: DeploymentRuntimeConfig
metadata:
  name: provider-opentofu
spec:
  serviceAccountTemplate:
    metadata:
      name: provider-opentofu
  deploymentTemplate:
    spec:
      selector: {}
      template:
        spec:
          containers:
            - name: package-runtime
              args: ["--debug"]
              env:
                - name: AWS_ACCESS_KEY_ID
                  value: "$S3_ACCESS"
                - name: AWS_SECRET_ACCESS_KEY
                  value: "$S3_SECRET"
EOF
  if [[ $1 == yes ]]; then
    echo "                - name: TF_ENCRYPTION"
    echo "                  value: |"
    tf_encryption_hcl | indent 20
  fi
  cat <<EOF
              volumeMounts:
                - name: tofu-state-token
                  mountPath: $TOKEN_DIR
                  readOnly: true
          volumes:
            - name: tofu-state-token
              secret:
                secretName: sovereign-tofu-state-token
                optional: true
EOF
}

ws_inline() { # ns name providerconfig external-name module-file marker
  cat <<EOF
apiVersion: opentofu.m.upbound.io/v1beta1
kind: Workspace
metadata:
  name: $2
  namespace: $1
  annotations:
    crossplane.io/external-name: $4
spec:
  providerConfigRef:
    kind: ClusterProviderConfig
    name: $3
  forProvider:
    source: Inline
    module: |
$(indent 6 < "$5")
    vars:
      - key: marker
        value: "$6"
EOF
}

setup() {
  kind_up cld050-core "$KIND_NODE_V2" || return 1
  kind_ips
  start_seaweedfs || return 1
  kubectl apply -f - <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: openbao
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: openbao
  namespace: openbao
spec:
  replicas: 1
  selector:
    matchLabels: {app: openbao}
  template:
    metadata:
      labels: {app: openbao}
    spec:
      containers:
        - name: openbao
          image: $OPENBAO_IMAGE
          args: ["server", "-dev", "-dev-root-token-id=spike-root", "-dev-listen-address=0.0.0.0:8200"]
          env:
            - {name: SKIP_SETCAP, value: "true"}
            - {name: BAO_ADDR, value: "http://127.0.0.1:8200"}
            - {name: BAO_TOKEN, value: "spike-root"}
          ports:
            - containerPort: 8200
          readinessProbe:
            httpGet: {path: /v1/sys/health, port: 8200}
---
apiVersion: v1
kind: Service
metadata:
  name: openbao
  namespace: openbao
spec:
  selector: {app: openbao}
  ports:
    - port: 8200
      targetPort: 8200
EOF
  install_crossplane_v2 || return 1
  kubectl create namespace "$NS_INFRA"
  kubectl create namespace cloud-sim
  kubectl create namespace e2
  kubectl create namespace e15
  kubectl apply -f - <<EOF
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: e15-cloud-sim
  namespace: cloud-sim
rules:
  - apiGroups: [""]
    resources: ["configmaps"]
    verbs: ["*"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: e15-cloud-sim
  namespace: cloud-sim
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: e15-cloud-sim
subjects:
  - kind: ServiceAccount
    name: provider-opentofu
    namespace: crossplane-system
EOF
  drc_core no | kubectl apply -f - || return 1
  provider_yaml "$PROVIDER_V1111" provider-opentofu | kubectl apply -f - || return 1
  wait_provider_healthy 600 || return 1
  wait_provider_pod "" 300 || return 1
  ensure_workspace_kinds || return 1
  kubectl -n openbao rollout status deploy/openbao --timeout=180s || return 1
  local cpc
  cpc=$(cat <<EOF
apiVersion: opentofu.m.upbound.io/v1beta1
kind: ClusterProviderConfig
metadata:
  name: cloud
spec:
  credentials:
    - filename: none.env
      source: None
  configuration: |
$(s3_backend_hcl "http://$S3_IP:8333" | indent 4)
---
apiVersion: opentofu.m.upbound.io/v1beta1
kind: ClusterProviderConfig
metadata:
  name: local-backend
spec:
  credentials:
    - filename: none.env
      source: None
EOF
)
  # The ClusterProviderConfig kind only exists once the provider is installed.
  local i
  for i in $(seq 1 30); do
    echo "$cpc" | kubectl apply -f - && break
    sleep 5
  done
  echo "$cpc" > "$EV/clusterproviderconfigs.yaml"
}

# ------------------------------------------------------------------ E1
e1() {
  local d="$EV/E1"; mkdir -p "$d"
  log "E1: namespaced kinds of provider-opentofu v1.1.11 on Crossplane $CROSSPLANE_V2"
  kubectl api-resources --api-group=opentofu.m.upbound.io -o wide 2>&1 | tee "$d/api-resources-opentofu.m.upbound.io.txt"
  kubectl api-resources --api-group=opentofu.upbound.io -o wide 2>&1 | tee "$d/api-resources-opentofu.upbound.io.txt"
  kubectl get crd -o custom-columns='NAME:.metadata.name,KIND:.spec.names.kind,SCOPE:.spec.scope,VERSIONS:.spec.versions[*].name' 2>&1 \
    | grep -E 'NAME|opentofu' | tee "$d/crds.txt"
  kubectl get managedresourcedefinitions 2>&1 | tee "$d/mrds.txt"
  kubectl get managedresourceactivationpolicies -o yaml > "$d/mraps.yaml" 2>&1
  local ws cpc pc served
  ws=$(kubectl get crd workspaces.opentofu.m.upbound.io -o jsonpath='{.spec.names.kind}/{.spec.scope}' 2>/dev/null)
  cpc=$(kubectl get crd clusterproviderconfigs.opentofu.m.upbound.io -o jsonpath='{.spec.names.kind}/{.spec.scope}' 2>/dev/null)
  pc=$(kubectl get crd providerconfigs.opentofu.m.upbound.io -o jsonpath='{.spec.names.kind}/{.spec.scope}' 2>/dev/null)
  served=$(kubectl api-resources --api-group=opentofu.m.upbound.io -o name 2>/dev/null | sort | tr '\n' ' ')
  measure E1 crossplane_version "$(kubectl -n crossplane-system get deploy crossplane -o jsonpath='{.spec.template.spec.containers[0].image}')"
  measure E1 provider_package "$PROVIDER_V1111"
  measure E1 kind_workspace "$ws"
  measure E1 kind_clusterproviderconfig "$cpc"
  measure E1 kind_providerconfig "$pc"
  measure E1 served_resources "$served"
  if [[ $ws == Workspace/Namespaced && $cpc == ClusterProviderConfig/Cluster && $served == *workspaces.opentofu.m.upbound.io* && $served == *clusterproviderconfigs.opentofu.m.upbound.io* ]]; then
    verdict E1 PASS "Workspace (Namespaced) and ClusterProviderConfig (Cluster) are served in opentofu.m.upbound.io/v1beta1 as section 4.7 names them"
  else
    verdict E1 FAIL "kinds differ: workspace=$ws clusterproviderconfig=$cpc served=[$served]"
  fi
}

# ------------------------------------------------------------------ E2
e2() {
  local d="$EV/E2"; mkdir -p "$d"
  log "E2: first observation with the s3 back end; state across a provider pod restart"
  tfdata_module "e2" > "$WORK/e2.tf"
  ws_inline e2 e2-local local-backend e2-local "$WORK/e2.tf" m0 > "$d/ws-e2-local.yaml"
  ws_inline e2 e2-s3 cloud e2-e2-s3 "$WORK/e2.tf" m0 > "$d/ws-e2-s3.yaml"
  kubectl apply -f "$d/ws-e2-local.yaml"
  local local_result s3_ok=no restart_ok=no id0 id1 obj marker_ok=no
  if ws_wait_error e2 e2-local 240; then
    local_result="Synced=False after ${WAITED}s: $(ws_msg e2 e2-local | tr '\n' ' ' | cut -c1-400)"
  elif [[ $(ws_cond e2 e2-local Ready) == True ]]; then
    local_result="Ready=True (no dead-lock)"
  else
    local_result="no verdict after 240s"
  fi
  ws_dump e2 e2-local "$d/e2-local-status.yaml"
  measure E2 local_backend_first_observation "$local_result"
  ws_delete e2 e2-local 60

  kubectl apply -f "$d/ws-e2-s3.yaml"
  if ws_wait_ready e2 e2-s3 300; then
    s3_ok=yes
    measure E2 s3_first_ready_seconds "$WAITED"
  else
    measure E2 s3_first_observation "not Ready after ${WAITED}s: $(ws_msg e2 e2-s3 | tr '\n' ' ' | cut -c1-400)"
  fi
  ws_dump e2 e2-s3 "$d/e2-s3-status-1.yaml"
  obj="tofu-state/e2-e2-s3/terraform.tfstate"
  s3 s3 ls "s3://$S3_BUCKET/tofu-state/" --recursive 2>&1 | tee "$d/bucket-listing-1.txt"
  if s3 s3 cp "s3://$S3_BUCKET/$obj" "$d/e2-state-object.json" >/dev/null 2>&1; then
    measure E2 state_object "$obj present; resources=$(jq '.resources | length' "$d/e2-state-object.json" 2>/dev/null) serial=$(jq '.serial' "$d/e2-state-object.json" 2>/dev/null)"
  else
    measure E2 state_object "$obj absent"
  fi
  measure E2 kubernetes_state_secrets "$(kubectl get secrets -A --no-headers 2>/dev/null | grep -c tfstate)"
  if [[ $s3_ok == yes ]]; then
    id0=$(ws_out e2 e2-s3 id)
    local old new
    old=$(provider_pod)
    log "deleting provider pod $old"
    kubectl -n crossplane-system delete pod "$old" --wait=true
    wait_provider_pod "" 300
    new=$(provider_pod)
    sleep 30
    measure E2 after_restart_conditions "Synced=$(ws_cond e2 e2-s3 Synced) Ready=$(ws_cond e2 e2-s3 Ready) (pod $old -> $new)"
    kubectl -n e2 patch "$WSR" e2-s3 --type=json -p '[{"op":"replace","path":"/spec/forProvider/vars/0/value","value":"m1"}]'
    if ws_wait_output e2 e2-s3 marker m1 300; then
      marker_ok=yes
      id1=$(ws_out e2 e2-s3 id)
      measure E2 resource_id_before_restart "$id0"
      measure E2 resource_id_after_restart_and_apply "$id1"
      [[ -n $id0 && $id0 == "$id1" ]] && restart_ok=yes
    else
      measure E2 apply_after_restart "not applied after ${WAITED}s: $(ws_msg e2 e2-s3 | cut -c1-300)"
    fi
    kubectl -n crossplane-system logs "$new" 2>&1 | grep -E 'e2-s3' | tail -20 > "$d/provider-log-after-restart.txt"
    measure E2 provider_log_lines_for_e2_s3_after_restart "$(wc -l < "$d/provider-log-after-restart.txt")"
    ws_dump e2 e2-s3 "$d/e2-s3-status-2.yaml"
  fi
  ws_delete e2 e2-s3 180
  if [[ $s3_ok == yes && $restart_ok == yes ]]; then
    verdict E2 PASS "s3 back end: first observation passes (Ready in $(grep s3_first_ready_seconds "$EV/measures.tsv" | cut -f3)s); after the provider pod was deleted, a forced apply kept the same resource id (state read from the bucket). Local back end: $local_result"
  else
    verdict E2 FAIL "s3_ready=$s3_ok marker_applied=$marker_ok same_id_after_restart=$restart_ok; local: $local_result"
  fi
}

# ------------------------------------------------------------------ E3
e3() {
  local d="$EV/E3"; mkdir -p "$d"
  log "E3: OpenBao key provider, token read from a file that is re-minted"
  bao secrets enable transit
  bao write -f transit/keys/sovereign-tofu-state
  bao policy write tofu-state - <<'EOF'
path "transit/datakey/plaintext/sovereign-tofu-state" { capabilities = ["update"] }
path "transit/decrypt/sovereign-tofu-state" { capabilities = ["update"] }
EOF
  bao write auth/token/roles/tofu-state allowed_policies=tofu-state orphan=true token_explicit_max_ttl=2h
  local t1 t2
  t1=$(bao token create -role=tofu-state -ttl=20m -field=token)
  [[ -n $t1 ]] || { verdict E3 FAIL "could not mint a token of role tofu-state"; return; }
  kubectl -n crossplane-system create secret generic sovereign-tofu-state-token --from-literal=token="$t1"
  tf_encryption_hcl > "$d/TF_ENCRYPTION.hcl"
  drc_core yes > "$d/deploymentruntimeconfig.yaml"
  kubectl apply -f "$d/deploymentruntimeconfig.yaml"
  if ! wait_provider_pod TF_ENCRYPTION 420; then
    verdict E3 FAIL "provider pod did not restart with TF_ENCRYPTION"; return
  fi
  local pod
  pod=$(provider_pod)
  measure E3 tofu_version_in_pod "$(kubectl -n crossplane-system exec "$pod" -- tofu version 2>&1 | head -2 | tr '\n' ' ')"
  local filetok
  filetok=$(kubectl -n crossplane-system exec "$pod" -- cat "$TOKEN_DIR/token" 2>/dev/null)
  measure E3 token_file_matches_first_token "$([[ $filetok == "$t1" ]] && echo yes || echo no)"

  tfdata_module "$CANARY_TEXT" > "$WORK/canary.tf"
  ws_inline "$NS_INFRA" state-canary cloud catalyst-infra-state-canary "$WORK/canary.tf" m0 > "$d/ws-state-canary.yaml"
  kubectl apply -f "$d/ws-state-canary.yaml"
  if ! ws_wait_ready "$NS_INFRA" state-canary 300; then
    ws_dump "$NS_INFRA" state-canary "$d/state-canary-status.yaml"
    provider_logs 60 > "$d/provider-log.txt"
    verdict E3 FAIL "Workspace with TF_ENCRYPTION (token from file) not Ready after ${WAITED}s: $(ws_msg "$NS_INFRA" state-canary | tr '\n' ' ' | cut -c1-500)"
    return
  fi
  measure E3 canary_ready_seconds "$WAITED"
  local obj="tofu-state/catalyst-infra-state-canary/terraform.tfstate" cipher=no
  s3 s3 cp "s3://$S3_BUCKET/$obj" "$d/state-object.json" >/dev/null 2>&1
  python3 - "$d/state-object.json" "$CANARY_TEXT" > "$d/ciphertext-check.txt" <<'EOF'
import json, sys
raw = open(sys.argv[1], 'rb').read()
doc = json.loads(raw)
keys = sorted(doc.keys())
print("object bytes:", len(raw))
print("top-level keys:", keys)
print("has encrypted_data:", "encrypted_data" in doc)
print("has resources (plaintext state):", "resources" in doc)
print("canary text present:", sys.argv[2].encode() in raw)
print("meta key providers:", list((doc.get("meta") or {}).keys()))
ok = "encrypted_data" in doc and "resources" not in doc and sys.argv[2].encode() not in raw
print("CIPHERTEXT", "yes" if ok else "no")
EOF
  cat "$d/ciphertext-check.txt"
  grep -q '^CIPHERTEXT yes' "$d/ciphertext-check.txt" && cipher=yes
  measure E3 state_object_is_ciphertext "$cipher ($(grep 'top-level keys' "$d/ciphertext-check.txt"))"
  head -c 300 "$d/state-object.json" > "$d/state-object-head.txt"; echo >> "$d/state-object-head.txt"
  measure E3 state_secrets_in_cluster "$(kubectl get secrets -A --no-headers | grep -c tfstate)"

  # Re-mint: a second token replaces the first in the Secret; the first is revoked.
  local rot=no s
  t2=$(bao token create -role=tofu-state -ttl=20m -field=token)
  kubectl -n crossplane-system create secret generic sovereign-tofu-state-token --from-literal=token="$t2" \
    --dry-run=client -o yaml | kubectl apply -f -
  bao token revoke "$t1"
  if bao token lookup "$t1" >/dev/null 2>&1; then
    measure E3 first_token_revoked no
  else
    measure E3 first_token_revoked yes
  fi
  s=$SECONDS
  while (( SECONDS - s < 240 )); do
    filetok=$(kubectl -n crossplane-system exec "$pod" -- cat "$TOKEN_DIR/token" 2>/dev/null)
    [[ $filetok == "$t2" ]] && break
    sleep 3
  done
  measure E3 seconds_until_file_shows_new_token "$((SECONDS - s)) ($([[ $filetok == "$t2" ]] && echo updated || echo NOT-updated))"
  kubectl -n "$NS_INFRA" patch "$WSR" state-canary --type=json -p '[{"op":"replace","path":"/spec/forProvider/vars/0/value","value":"m1"}]'
  if ws_wait_output "$NS_INFRA" state-canary marker m1 300; then
    rot=yes
    measure E3 apply_with_new_token_after_old_revoked "applied in ${WAITED}s"
  else
    measure E3 apply_with_new_token_after_old_revoked "failed after ${WAITED}s: $(ws_msg "$NS_INFRA" state-canary | cut -c1-300)"
  fi
  ws_dump "$NS_INFRA" state-canary "$d/state-canary-status.yaml"
  if [[ $cipher == yes && $rot == yes ]]; then
    verdict E3 PASS "OpenBao key provider in TF_ENCRYPTION (pod env) with token = file(...) of a Secret volume: Workspace Ready, bucket object is ciphertext, and after re-mint + revoke of the old token the next apply used the new file"
    E3_OPTION=B
  else
    verdict E3 FAIL "ciphertext=$cipher apply_after_remint=$rot"
    E3_OPTION=none
  fi
  measure E3 encryption_option "$E3_OPTION"
}

# ------------------------------------------------------------------ E6
e6() {
  local d="$EV/E6"; mkdir -p "$d"
  log "E6: varFiles from a ConfigMap key on the namespaced Workspace"
  kubectl -n "$NS_INFRA" create configmap infrastructure-facts \
    --from-literal=facts.auto.tfvars.json='{"region_code":"spike-r1","sovereign_fqdn":"t00.spike.test","deployment_id":"dep-cld050","name_prefix":"spk"}'
  cat > "$WORK/e6.tf" <<'EOF'
variable "region_code" { type = string }
variable "sovereign_fqdn" { type = string }
variable "deployment_id" { type = string }
variable "name_prefix" { type = string }
variable "marker" {
  type    = string
  default = "m0"
}
resource "terraform_data" "facts" {
  input = "${var.name_prefix}-${var.region_code}-${var.marker}"
}
output "region_code" { value = var.region_code }
output "sovereign_fqdn" { value = var.sovereign_fqdn }
output "deployment_id" { value = var.deployment_id }
output "name_prefix" { value = var.name_prefix }
EOF
  cat > "$d/ws-e6-facts.yaml" <<EOF
apiVersion: opentofu.m.upbound.io/v1beta1
kind: Workspace
metadata:
  name: e6-facts
  namespace: $NS_INFRA
  annotations:
    crossplane.io/external-name: catalyst-infra-e6-facts
spec:
  providerConfigRef:
    kind: ClusterProviderConfig
    name: cloud
  forProvider:
    source: Inline
    module: |
$(indent 6 < "$WORK/e6.tf")
    varFiles:
      - source: ConfigMapKey
        format: JSON
        configMapKeyRef:
          name: infrastructure-facts
          key: facts.auto.tfvars.json
EOF
  kubectl apply -f "$d/ws-e6-facts.yaml"
  if ws_wait_ready "$NS_INFRA" e6-facts 300; then
    local got
    got="$(ws_out "$NS_INFRA" e6-facts region_code)|$(ws_out "$NS_INFRA" e6-facts sovereign_fqdn)|$(ws_out "$NS_INFRA" e6-facts deployment_id)|$(ws_out "$NS_INFRA" e6-facts name_prefix)"
    measure E6 outputs "$got"
    if [[ $got == "spike-r1|t00.spike.test|dep-cld050|spk" ]]; then
      verdict E6 PASS "a JSON variables file from ConfigMap $NS_INFRA/infrastructure-facts key facts.auto.tfvars.json reached all four variables (Ready in ${WAITED}s)"
    else
      verdict E6 FAIL "outputs differ: $got"
    fi
  else
    verdict E6 FAIL "not Ready after ${WAITED}s: $(ws_msg "$NS_INFRA" e6-facts | cut -c1-300)"
  fi
  ws_dump "$NS_INFRA" e6-facts "$d/e6-facts-status.yaml"
}

# ------------------------------------------------------------------ E13
e13() {
  local d="$EV/E13"; mkdir -p "$d"
  log "E13: offline backup of the transit key decrypts the state without the Sovereign's OpenBao"
  local pod uid live live_count
  pod=$(provider_pod)
  uid=$(kubectl -n "$NS_INFRA" get "$WSR" state-canary -o jsonpath='{.metadata.uid}')
  live=$(kubectl -n crossplane-system exec "$pod" -- sh -c "cd /tofu/$uid && tofu state list" 2>&1)
  echo "$live" > "$d/live-state-list.txt"
  live_count=$(grep -c '^[a-z]' "$d/live-state-list.txt")
  measure E13 live_resource_count "$live_count ($(tr '\n' ' ' < "$d/live-state-list.txt"))"

  if bao read -field=backup transit/backup/sovereign-tofu-state > /dev/null 2> "$d/backup-before-config.err"; then
    measure E13 backup_without_key_config "allowed"
  else
    measure E13 backup_without_key_config "refused: $(tr '\n' ' ' < "$d/backup-before-config.err" | cut -c1-200)"
  fi
  bao write transit/keys/sovereign-tofu-state/config exportable=true allow_plaintext_backup=true
  bao read -field=backup transit/backup/sovereign-tofu-state > "$WORK/transit-backup.b64" 2> "$d/backup.err"
  measure E13 backup_bytes "$(wc -c < "$WORK/transit-backup.b64")"
  if [[ ! -s $WORK/transit-backup.b64 ]]; then
    verdict E13 FAIL "no backup produced: $(cut -c1-200 "$d/backup.err")"; return
  fi

  # The scratch OpenBao: a container on the runner, outside the cluster.
  docker run -d --name bao-scratch -p 8300:8200 -e SKIP_SETCAP=true "$OPENBAO_IMAGE" \
    server -dev -dev-root-token-id=scratch-root -dev-listen-address=0.0.0.0:8200 >/dev/null
  docker run -d --name bao-other -p 8301:8200 -e SKIP_SETCAP=true "$OPENBAO_IMAGE" \
    server -dev -dev-root-token-id=other-root -dev-listen-address=0.0.0.0:8200 >/dev/null
  local i
  for i in $(seq 1 30); do
    curl -fsS http://127.0.0.1:8300/v1/sys/health >/dev/null 2>&1 && curl -fsS http://127.0.0.1:8301/v1/sys/health >/dev/null 2>&1 && break
    sleep 2
  done
  docker exec -e BAO_ADDR=http://127.0.0.1:8200 -e BAO_TOKEN=scratch-root bao-scratch bao secrets enable transit
  docker exec -e BAO_ADDR=http://127.0.0.1:8200 -e BAO_TOKEN=scratch-root bao-scratch \
    bao write transit/restore/sovereign-tofu-state backup="$(cat "$WORK/transit-backup.b64")" 2>&1 | tee "$d/restore.txt"
  docker exec -e BAO_ADDR=http://127.0.0.1:8200 -e BAO_TOKEN=other-root bao-other bao secrets enable transit
  docker exec -e BAO_ADDR=http://127.0.0.1:8200 -e BAO_TOKEN=other-root bao-other bao write -f transit/keys/sovereign-tofu-state

  local cip
  cip=$(kubectl -n openbao get svc openbao -o jsonpath='{.spec.clusterIP}')
  if curl -sS -m 5 "http://$cip:8200/v1/sys/health" >/dev/null 2>&1; then
    measure E13 runner_reaches_cluster_openbao "yes ($cip)"
  else
    measure E13 runner_reaches_cluster_openbao "no ($cip unreachable from the runner)"
  fi

  local w="$WORK/drill"
  mkdir -p "$w"
  s3_backend_hcl "http://127.0.0.1:8333" > "$w/main.tf"
  local enc_scratch enc_other pulled_count=-1 other_rc
  enc_scratch='key_provider "openbao" "offline" {
  address  = "http://127.0.0.1:8300"
  token    = "scratch-root"
  key_name = "sovereign-tofu-state"
}
method "aes_gcm" "offline" {
  keys = key_provider.openbao.offline
}
state {
  method   = method.aes_gcm.offline
  enforced = true
}'
  enc_other="${enc_scratch//8300/8301}"
  enc_other="${enc_other//scratch-root/other-root}"
  (cd "$w" && TF_ENCRYPTION="$enc_scratch" TF_WORKSPACE=catalyst-infra-state-canary tofu init -input=false -no-color) > "$d/drill-init.txt" 2>&1
  if (cd "$w" && TF_ENCRYPTION="$enc_scratch" TF_WORKSPACE=catalyst-infra-state-canary tofu state pull) > "$WORK/pulled.json" 2> "$d/drill-pull.err"; then
    pulled_count=$(jq '[.resources[].instances[]] | length' "$WORK/pulled.json")
    jq '{serial, lineage, resources: [.resources[] | {type, name, instances: (.instances|length)}]}' "$WORK/pulled.json" > "$d/pulled-summary.json"
    cat "$d/pulled-summary.json"
    measure E13 pulled_resource_count_with_restored_key "$pulled_count"
  else
    measure E13 state_pull_with_restored_key "failed: $(tr '\n' ' ' < "$d/drill-pull.err" | cut -c1-300)"
  fi
  (cd "$w" && TF_ENCRYPTION="$enc_other" TF_WORKSPACE=catalyst-infra-state-canary tofu state pull) > /dev/null 2> "$d/negative-pull.err"
  other_rc=$?
  measure E13 state_pull_with_a_different_key "exit=$other_rc $(tr '\n' ' ' < "$d/negative-pull.err" | cut -c1-200)"
  if [[ $pulled_count -ge 1 && $pulled_count -eq $live_count && $other_rc -ne 0 ]]; then
    verdict E13 PASS "transit key backup restored into a scratch OpenBao outside the cluster decrypts the canary state (resources $pulled_count = live $live_count); a different key cannot. Backup needs exportable=true and allow_plaintext_backup=true on the key"
  else
    verdict E13 FAIL "pulled=$pulled_count live=$live_count negative_exit=$other_rc"
  fi
}

# ------------------------------------------------------------------ E15
e15() {
  local d="$EV/E15"; mkdir -p "$d"
  log "E15: observe, then delete, leaves the cloud resources"
  cat > "$WORK/e15.tf" <<'EOF'
terraform {
  required_providers {
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "2.38.0"
    }
  }
}
provider "kubernetes" {}
variable "marker" {
  type    = string
  default = "m0"
}
variable "name" { type = string }
resource "kubernetes_config_map_v1" "sim" {
  metadata {
    name      = var.name
    namespace = "cloud-sim"
  }
  data = { owner = "cld-050-e15" }
}
output "cm" { value = kubernetes_config_map_v1.sim.metadata[0].name }
EOF
  mk() { # name external-name policy-json
    cat <<EOF
apiVersion: opentofu.m.upbound.io/v1beta1
kind: Workspace
metadata:
  name: $1
  namespace: e15
  annotations:
    crossplane.io/external-name: $2
spec:
  managementPolicies: $3
  providerConfigRef:
    kind: ClusterProviderConfig
    name: cloud
  forProvider:
    source: Inline
    module: |
$(indent 6 < "$WORK/e15.tf")
    vars:
      - key: marker
        value: m0
      - key: name
        value: $2
EOF
  }
  cm_exists() { kubectl -n cloud-sim get configmap "$1" >/dev/null 2>&1 && echo yes || echo no; }
  local a_ok=no b_ok=no c_ok=no
  mk e15-a e15-a '["*"]' > "$d/ws-e15-a.yaml"
  kubectl apply -f "$d/ws-e15-a.yaml"
  if ws_wait_ready e15 e15-a 420; then
    measure E15 a_created "configmap cloud-sim/e15-a exists=$(cm_exists e15-a) after ${WAITED}s"
    kubectl -n e15 patch "$WSR" e15-a --type=merge -p '{"spec":{"managementPolicies":["Observe"]}}'
    sleep 20
    measure E15 a_after_switch_to_observe "Synced=$(ws_cond e15 e15-a Synced) Ready=$(ws_cond e15 e15-a Ready)"
    ws_delete e15 e15-a 180
    measure E15 a_deleted_object_gone "$(kubectl -n e15 get "$WSR" e15-a >/dev/null 2>&1 && echo no || echo yes)"
    measure E15 a_configmap_after_delete "$(cm_exists e15-a)"
    if s3 s3api head-object --bucket "$S3_BUCKET" --key tofu-state/e15-a/terraform.tfstate >/dev/null 2>&1; then
      measure E15 a_state_object_after_delete present
    else
      measure E15 a_state_object_after_delete absent
    fi
    [[ $(cm_exists e15-a) == yes ]] && a_ok=yes

    mk e15-b e15-a '["Observe"]' > "$d/ws-e15-b.yaml"
    kubectl apply -f "$d/ws-e15-b.yaml"
    if ws_wait_ready e15 e15-b 300; then
      measure E15 b_observe_only_over_existing_state "Ready in ${WAITED}s, output cm=$(ws_out e15 e15-b cm)"
    else
      measure E15 b_observe_only_over_existing_state "not Ready after ${WAITED}s: $(ws_msg e15 e15-b | cut -c1-300)"
    fi
    ws_dump e15 e15-b "$d/e15-b-status.yaml"
    ws_delete e15 e15-b 180
    measure E15 b_configmap_after_delete "$(cm_exists e15-a)"
    [[ $(cm_exists e15-a) == yes ]] && b_ok=yes
  else
    measure E15 a_created "not Ready after ${WAITED}s: $(ws_msg e15 e15-a | cut -c1-300)"
    ws_dump e15 e15-a "$d/e15-a-status.yaml"
  fi

  mk e15-c e15-c '["*"]' > "$d/ws-e15-c.yaml"
  kubectl apply -f "$d/ws-e15-c.yaml"
  if ws_wait_ready e15 e15-c 300; then
    local before
    before=$(cm_exists e15-c)
    ws_delete e15 e15-c 240
    measure E15 c_control_managed_delete "configmap before=$before after=$(cm_exists e15-c)"
    [[ $before == yes && $(cm_exists e15-c) == no ]] && c_ok=yes
  else
    measure E15 c_control_managed_delete "not Ready: $(ws_msg e15 e15-c | cut -c1-300)"
  fi
  if [[ $a_ok == yes && $b_ok == yes && $c_ok == yes ]]; then
    verdict E15 PASS "a Workspace switched to Observe and then deleted, and an Observe-only Workspace over the same state then deleted, both left the resource; the control with full policies deleted it"
  else
    verdict E15 FAIL "observe-then-delete kept=$a_ok observe-only-delete kept=$b_ok control-removed=$c_ok"
  fi
}

# ------------------------------------------------------------------ E16 + E17
e16_e17() {
  local d="$EV/E16"; mkdir -p "$d" "$EV/E17"
  log "E16/E17: a patched Workspace applies at once; seconds of one apply"
  local m all=() ok=yes id0 idn
  id0=$(ws_out "$NS_INFRA" state-canary id)
  for m in m2 m3 m4; do
    kubectl -n "$NS_INFRA" patch "$WSR" state-canary --type=json -p "[{\"op\":\"replace\",\"path\":\"/spec/forProvider/vars/0/value\",\"value\":\"$m\"}]"
    if ws_wait_output "$NS_INFRA" state-canary marker "$m" 600; then
      all+=("$WAITED")
      measure E17 "seconds_patch_to_applied_$m" "$WAITED"
    else
      ok=no
      all+=("timeout")
      measure E17 "seconds_patch_to_applied_$m" "not applied within ${WAITED}s"
    fi
  done
  idn=$(ws_out "$NS_INFRA" state-canary id)
  measure E16 poll_interval_of_provider "10m (provider default --poll)"
  measure E16 resource_id_unchanged "$([[ $id0 == "$idn" ]] && echo yes || echo no)"
  provider_logs 200 | grep -E 'state-canary' | tail -30 > "$d/provider-log.txt"
  local slow=no v
  for v in "${all[@]}"; do [[ $v == timeout || $v -ge 120 ]] && slow=yes; done
  if [[ $ok == yes && $slow == no ]]; then
    verdict E16 PASS "three patches of a var were each applied without waiting for the 10-minute poll (seconds: ${all[*]})"
  else
    verdict E16 FAIL "patch-to-applied seconds: ${all[*]}"
  fi
  local med
  med=$(printf '%s\n' "${all[@]}" | grep -E '^[0-9]+$' | sort -n | awk '{a[NR]=$1} END{if(NR) print a[int((NR+1)/2)]}')
  if [[ -n $med ]]; then
    verdict E17 MEASURED "seconds from patch to applied output, encrypted s3 state, inline one-resource module: ${all[*]} (median $med)"
  else
    verdict E17 FAIL "no apply completed"
  fi
}

[[ ${CLD050_SOURCE_ONLY:-} == 1 ]] && return 0  # lets a reviewer source the helpers without running
# ------------------------------------------------------------------ main
if ! setup; then
  log "SETUP FAILED"
  kubectl get pods -A -o wide 2>&1 | tail -40
  for r in E1 E2 E3 E6 E13 E15 E16 E17; do verdict "$r" NOT-RUN "setup failed (cluster, Crossplane or provider)"; done
  summary core
  exit 1
fi
E3_OPTION=none
e1
e2
e3
e6
if [[ $(ws_cond "$NS_INFRA" state-canary Ready) == True ]]; then
  e13
else
  verdict E13 NOT-RUN "no encrypted canary state (E3 failed)"
fi
e15
if [[ $(ws_cond "$NS_INFRA" state-canary Ready) == True ]]; then
  e16_e17
else
  verdict E16 NOT-RUN "canary Workspace not Ready"
  verdict E17 NOT-RUN "canary Workspace not Ready"
fi
echo "=== OUTCOME encryption option: $E3_OPTION"
summary core
