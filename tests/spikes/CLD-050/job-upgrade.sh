#!/usr/bin/env bash
# Spike CLD-050 — job "upgrade": E7. Do adoption Workspaces survive
# Crossplane 1.18 -> 1.19 -> 1.20 (+ Composition converted to Pipeline)
# -> 2.4, and at which step may provider-opentofu be raised to v1.1.11?
#
#   job-upgrade.sh chain   the whole path, provider raised last (E7)
#   job-upgrade.sh early   provider v1.1.11 directly on Crossplane 1.18 (E7b)
#
# Uses the repository's own bp-crossplane chart (only the dependency
# version changes per step, as CLD-061/062/065 do), the repository's
# provider-opentofu manifest, its ProviderConfig `default` (kubernetes back
# end) and the XCloudAdoption definition + Composition rendered from
# bp-crossplane-claims. The Composition's inline module is replaced by a
# cloud-free module with the same variables and outputs (plus anchor_id)
# because the real one looks up live cloud resources.
set -o pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

MODE=${1:-chain}
WSR=workspaces.opentofu.upbound.io
REPO_ROOT="$(cd "$SPIKE_DIR/../../.." && pwd)"
CHART="$REPO_ROOT/platform/crossplane/chart"
CLAIMS=(spike-lb spike-srv spike-net)
KINDS=(loadbalancer server network)
mkdir -p "$EV/E7"
PREV_REGION=r0
ALL_OK=yes

substitute_module() {
  cat <<'EOF'
variable "cloud"         { type = string }
variable "resource_kind" { type = string }
variable "resource_id"   { type = string }
variable "huawei_region" {
  type    = string
  default = ""
}
variable "huawei_access_key" {
  type    = string
  default = ""
}
variable "huawei_secret_key" {
  type    = string
  default = ""
}
variable "huawei_project_id" {
  type    = string
  default = ""
}
variable "huawei_insecure" {
  type    = string
  default = "true"
}
variable "hcloud_token" {
  type    = string
  default = ""
}
resource "terraform_data" "adoption_anchor" {
  input = "${var.resource_id}/${var.huawei_region}"
}
output "observed_id"      { value = var.resource_id }
output "observed_address" { value = "" }
output "adopted"          { value = true }
output "hw_api_region"    { value = var.huawei_region }
output "anchor_id"        { value = terraform_data.adoption_anchor.id }
EOF
}

core_install() {
  helm repo add crossplane-stable https://charts.crossplane.io/stable >/dev/null
  helm repo update >/dev/null
  helm dependency update "$CHART" || return 1
  helm install crossplane "$CHART" -n crossplane-system --create-namespace \
    --set crossplane.image.repository=docker.io/crossplane/crossplane --wait --timeout 10m || return 1
  measure E7 "core_image_${1}" "$(kubectl -n crossplane-system get deploy crossplane -o jsonpath='{.spec.template.spec.containers[0].image}')"
}

core_to() { # version label
  sed -i -E "s/^(    version: )\"[0-9]+\.[0-9]+\.[0-9]+\"/\1\"$1\"/" "$CHART/Chart.yaml"
  grep -n 'version: "' "$CHART/Chart.yaml"
  helm dependency update "$CHART" > "$EV/E7/helm-dep-$2.txt" 2>&1 || { cat "$EV/E7/helm-dep-$2.txt"; return 1; }
  if ! helm upgrade crossplane "$CHART" -n crossplane-system \
      --set crossplane.image.repository=docker.io/crossplane/crossplane --wait --timeout 10m > "$EV/E7/helm-upgrade-$2.txt" 2>&1; then
    cat "$EV/E7/helm-upgrade-$2.txt"; return 1
  fi
  kubectl -n crossplane-system rollout status deploy/crossplane --timeout=300s || return 1
  kubectl -n crossplane-system rollout status deploy/crossplane-rbac-manager --timeout=300s || true
  measure E7 "core_image_$2" "$(kubectl -n crossplane-system get deploy crossplane -o jsonpath='{.spec.template.spec.containers[0].image}')"
  sleep 30
}

record_state() { # label
  local f="$EV/E7/state-$1.txt"
  {
    echo "## $1"
    kubectl get providers.pkg.crossplane.io,providerrevisions.pkg.crossplane.io,functions.pkg.crossplane.io 2>&1
    kubectl get xrd,compositions,compositionrevisions 2>&1
    kubectl -n crossplane-system get cloudadoptions.compose.openova.io 2>&1
    kubectl get "$WSR" 2>&1
    kubectl api-resources --api-group=opentofu.upbound.io 2>&1
    kubectl get managedresourcedefinitions 2>&1 | head -5
  } > "$f"
  cat "$f"
}

g6_check() { # label expected-region timeout -> 0 when CLD-G6 holds
  local s=$SECONDS t=${3:-600}
  while (( SECONDS - s < t )); do
    kubectl get "$WSR" -o json > "$WORK/ws.json" 2>/dev/null
    if python3 "$SPIKE_DIR/g6.py" "$WORK/ws.json" "$EV/E7/baseline-$MODE.json" "$2" > "$WORK/g6.txt" 2>&1; then
      cat "$WORK/g6.txt"
      measure E7 "g6_$1" "pass after $((SECONDS - s))s: $(tail -1 "$WORK/g6.txt")"
      return 0
    fi
    sleep 5
  done
  cat "$WORK/g6.txt"
  measure E7 "g6_$1" "FAIL after ${t}s: $(tail -1 "$WORK/g6.txt")"
  kubectl get "$WSR" -o yaml | yq '.items[] | {"name": .metadata.name, "conditions": .status.conditions}' 2>/dev/null | head -60
  provider_logs 40
  return 1
}

bump_region() { # region
  local c
  for c in "${CLAIMS[@]}"; do
    kubectl -n crossplane-system patch cloudadoptions.compose.openova.io "$c" --type=merge \
      -p "{\"spec\":{\"parameters\":{\"region\":\"$1\"}}}" >/dev/null
  done
}

check_step() { # label new-region
  local ok=yes
  g6_check "$1-steady" "$PREV_REGION" 300 || ok=no
  bump_region "$2"
  g6_check "$1-after-change" "$2" 600 || ok=no
  PREV_REGION=$2
  record_state "$1"
  [[ $ok == yes ]] || ALL_OK=no
  [[ $ok == yes ]]
}

setup_adoption() { # provider-package
  kubectl create namespace flux-system
  kubectl -n flux-system create secret generic cloud-credentials \
    --from-literal=hcloud-token=spike --from-literal=huawei-ak=spike --from-literal=huawei-sk=spike \
    --from-literal=huawei-project-id=spike --from-literal=huawei-region=spike
  sed -E "s#^(  package: ).*#\1$1#" "$REPO_ROOT/clusters/_template/infrastructure/providers/base/provider-opentofu.yaml" \
    > "$EV/E7/provider-opentofu.yaml"
  grep -n '^  package:' "$EV/E7/provider-opentofu.yaml"
  kubectl apply -f "$EV/E7/provider-opentofu.yaml" || return 1
  wait_provider_healthy 600 || return 1
  wait_provider_pod "" 300 || return 1
  local i
  for i in $(seq 1 30); do
    kubectl apply -f "$REPO_ROOT/clusters/_template/infrastructure/base/provider-config-opentofu.yaml" && break
    sleep 5
  done
  helm template cc "$REPO_ROOT/platform/crossplane-claims/chart" \
    --show-only templates/xrds/cloudadoption.yaml > "$WORK/xrd.yaml" || return 1
  helm template cc "$REPO_ROOT/platform/crossplane-claims/chart" \
    --show-only templates/compositions/cloudadoption.yaml > "$WORK/comp-native.yaml" || return 1
  MOD="$(substitute_module)" yq -i '.spec.resources[0].base.spec.forProvider.module = strenv(MOD)' "$WORK/comp-native.yaml"
  cp "$WORK/xrd.yaml" "$WORK/comp-native.yaml" "$EV/E7/"
  kubectl apply -f "$WORK/xrd.yaml" || return 1
  kubectl wait xrd/xcloudadoptions.compose.openova.io --for=condition=Established --timeout=180s || return 1
  kubectl wait xrd/xcloudadoptions.compose.openova.io --for=condition=Offered --timeout=180s || return 1
  kubectl apply -f "$WORK/comp-native.yaml" || return 1
  for i in 0 1 2; do
    kubectl apply -f - <<EOF
apiVersion: compose.openova.io/v1alpha1
kind: CloudAdoption
metadata:
  name: ${CLAIMS[$i]}
  namespace: crossplane-system
spec:
  parameters:
    cloud: huawei
    resourceKind: ${KINDS[$i]}
    resourceId: ${CLAIMS[$i]}-0001
    region: r0
    sovereignFQDN: t00.spike.test
    manage: false
  providerConfigRef:
    name: default
EOF
  done
  g6_check baseline r0 600
}

install_function() {
  local served api
  served=$(kubectl get crd functions.pkg.crossplane.io -o jsonpath='{.spec.versions[?(@.served==true)].name}')
  if [[ " $served " == *" v1 "* ]]; then api=pkg.crossplane.io/v1; else api=pkg.crossplane.io/v1beta1; fi
  measure E7 function_api "$api (served: $served)"
  kubectl apply -f - <<EOF
apiVersion: $api
kind: Function
metadata:
  name: function-patch-and-transform
spec:
  package: $FUNCTION_PT
EOF
  kubectl wait functions.pkg.crossplane.io/function-patch-and-transform --for=condition=Healthy --timeout=420s
}

convert_composition() {
  local how
  if [[ -x $HOME/bin/crank ]] && crank beta convert pipeline-composition "$WORK/comp-native.yaml" \
       -o "$WORK/comp-pipeline.yaml" --function-patch-and-transform-ref=function-patch-and-transform > "$EV/E7/convert.txt" 2>&1; then
    how="crossplane CLI $(crank version --client 2>/dev/null | head -1) beta convert pipeline-composition"
  else
    cat "$EV/E7/convert.txt" 2>/dev/null
    python3 "$SPIKE_DIR/convert.py" "$WORK/comp-native.yaml" "$WORK/comp-pipeline.yaml" || return 1
    how="scripted conversion (convert.py)"
  fi
  measure E7 composition_conversion "$how"
  cp "$WORK/comp-pipeline.yaml" "$EV/E7/"
  yq '.spec | {"mode": .mode, "steps": [.pipeline[].step], "function": [.pipeline[].functionRef.name], "resources": [.pipeline[].input.resources[].name]}' "$WORK/comp-pipeline.yaml"
  kubectl apply -f "$WORK/comp-pipeline.yaml" || return 1
  sleep 20
  measure E7 composition_mode_live "$(kubectl get composition opentofu-cloud-adoption.compose.openova.io -o jsonpath='{.spec.mode}')"
  measure E7 composite_revisions "$(kubectl get xcloudadoptions.compose.openova.io -o jsonpath='{range .items[*]}{.metadata.name}={.spec.compositionRevisionRef.name} {end}')"
}

raise_provider() { # package
  kubectl patch providers.pkg.crossplane.io provider-opentofu --type=merge -p "{\"spec\":{\"package\":\"$1\"}}" || return 1
  local s=$SECONDS rev="" cur="" healthy="" ok=no
  while (( SECONDS - s < 600 )); do
    rev=$(kubectl get providerrevisions.pkg.crossplane.io -o json | jq -r '.items[] | select(tostring | contains("b4690e8ffe0a")) | select(.spec.desiredState=="Active") | .metadata.name' | head -1)
    cur=$(kubectl get providers.pkg.crossplane.io provider-opentofu -o jsonpath='{.status.currentRevision}')
    healthy=$(kubectl get providers.pkg.crossplane.io provider-opentofu -o jsonpath='{.status.conditions[?(@.type=="Healthy")].status}')
    if [[ -n $rev && $rev == "$cur" && $healthy == True ]]; then ok=yes; break; fi
    sleep 5
  done
  measure E7 provider_v1111_revision "revision=$rev current=$cur healthy=$healthy after $((SECONDS - s))s"
  kubectl get providers.pkg.crossplane.io,providerrevisions.pkg.crossplane.io -o wide
  [[ $ok == yes ]] || return 1
  wait_provider_pod "" 300
  sleep 20
}

chain() {
  kind_up cld050-upgrade "$KIND_NODE_V1" || return 1
  core_install 1.18.0 || { verdict E7 NOT-RUN "Crossplane 1.18 install failed"; return 1; }
  if ! setup_adoption "$PROVIDER_V113"; then
    verdict E7 NOT-RUN "adoption baseline on 1.18 + provider v1.1.3 did not converge"; return 1
  fi
  record_state s0-1.18
  core_to 1.19.3 s1-1.19 && check_step s1-1.19 r1 || ALL_OK=no
  core_to 1.20.14 s2-1.20 && check_step s2-1.20 r2 || ALL_OK=no
  if install_function && convert_composition; then
    check_step s2c-1.20-pipeline r2c || ALL_OK=no
  else
    ALL_OK=no; measure E7 conversion "failed"
  fi
  core_to 2.4.3 s3-2.4 && check_step s3-2.4 r3 || ALL_OK=no
  if raise_provider "$PROVIDER_V1111"; then
    check_step s4-provider-v1.1.11 r4 || ALL_OK=no
  else
    ALL_OK=no
  fi
  measure E7 cluster_scoped_workspace_kind_after_all_steps "$(kubectl get crd workspaces.opentofu.upbound.io -o jsonpath='{.spec.names.kind}/{.spec.scope}' 2>/dev/null)"
  if [[ $ALL_OK == yes ]]; then
    verdict E7 PASS "3 adoption Workspaces kept count, UID, resource id and Synced/Ready through 1.18->1.19->1.20, the Pipeline conversion, 2.4.3 and provider v1.1.11; each step also applied a claim change end to end"
  else
    verdict E7 FAIL "see the g6_* measurements for the step that broke"
  fi
}

early() {
  kind_up cld050-upgrade-early "$KIND_NODE_V1" || return 1
  core_install 1.18.0-early || { verdict E7b NOT-RUN "Crossplane 1.18 install failed"; return 1; }
  if setup_adoption "$PROVIDER_V1111" && check_step early-1.18-provider-v1.1.11 r1; then
    verdict E7b PASS "provider-opentofu v1.1.11 installs and serves the adoption Workspaces on Crossplane 1.18 (it can be raised before the core upgrade)"
  else
    verdict E7b FAIL "provider-opentofu v1.1.11 on Crossplane 1.18 did not serve the adoption Workspaces"
  fi
  record_state early
}

[[ ${CLD050_SOURCE_ONLY:-} == 1 ]] && return 0  # lets a reviewer source the helpers without running
if [[ $MODE == chain ]]; then
  chain
else
  early
fi
summary "upgrade-$MODE"
