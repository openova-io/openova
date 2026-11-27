#!/usr/bin/env bash
# bp-catalyst-platform — the operator's cloud credentials are mounted files (CLD-106).
#
# catalyst-api reads the operator's own cloud credentials from the files
# cloud-credential.<provider>.<key> of its flag --cloud-credentials-dir,
# default /var/run/secrets/openova. The Secrets Store CSI driver mounts them
# there from OpenBao. No Secret volume and no environment variable carries
# them: the variables CATALYST_HUAWEI_ACCESS_KEY / _SECRET_KEY / _PROJECT_ID /
# _REGION and the Secret huawei-operator-creds are gone.
#
# Cases, each on the values the clause names:
#   1. the binary's default directory, read from the Go source, is
#      /var/run/secrets/openova;
#   2. the Helm render with the default values: no such variable, no reference
#      to huawei-operator-creds, and no cloud-credentials volume or mount
#      ("" mounts nothing: a Sovereign provisions with no operator credential);
#   3. the Helm render with catalystApi.cloudCredentials.secretProviderClass
#      set: a CSI volume of driver secrets-store.csi.k8s.io naming that class,
#      mounted read-only at the default directory;
#   4. the literal manifest api-deployment-kustomize.yaml (the mothership):
#      the same CSI volume, naming catalyst-api-cloud-credentials.
# Every case first finds the catalyst-api container by one variable it must
# carry, so an absent container or env list is a failure, never a pass.
#
# Runs under helm and yq as installed by blueprint-release.yaml (yq v4.44.3):
# plain index access and select only, no slices and no `//` alternative.
#
# Usage: bash tests/operator-cloud-credentials-mount.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
# The chart depends on platform/secrets-lib (SEC-216): build it into charts/ before the first render.
helm dependency build --skip-refresh "$CHART_DIR" > /dev/null
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"

DIR="/var/run/secrets/openova"
SPC="catalyst-api-cloud-credentials"
GO_SOURCE="../bootstrap/api/internal/provisioner/provisioner.go"
fail=0

expect() { # label got want
  if [ "$2" = "$3" ]; then
    echo "  PASS $1 = $3"
  else
    echo "FAIL: $1 = '$2', want '$3'" >&2
    fail=1
  fi
}

# pod_spec FILE OUT — writes the pod spec of Deployment catalyst-api in FILE to OUT.
pod_spec() {
  yq 'select(.kind == "Deployment" and .metadata.name == "catalyst-api") | .spec.template.spec' "$1" > "$2"
}

# no_env_credentials SPEC RENDER LABEL — the container carries its other
# variables and none of the four retired ones; RENDER names no such Secret.
no_env_credentials() {
  yq '.containers[] | select(.name == "catalyst-api") | .env[] | .name' "$1" > "$TMP/env-names"
  expect "$3: catalyst-api carries CATALYST_HARBOR_ROBOT_TOKEN" "$(grep -cx 'CATALYST_HARBOR_ROBOT_TOKEN' "$TMP/env-names" || true)" "1"
  expect "$3: retired CATALYST_HUAWEI_ variables" "$(grep -cE '^CATALYST_HUAWEI_(ACCESS_KEY|SECRET_KEY|PROJECT_ID|REGION)$' "$TMP/env-names" || true)" "0"
  expect "$3: references to Secret huawei-operator-creds" "$(grep -c 'huawei-operator-creds' "$2" || true)" "0"
}

# csi_mount SPEC LABEL CLASS — the cloud-credentials CSI volume and its mount.
csi_mount() {
  expect "$2: volume cloud-credentials csi.driver" "$(yq '.volumes[] | select(.name == "cloud-credentials") | .csi.driver' "$1")" "secrets-store.csi.k8s.io"
  expect "$2: volume cloud-credentials csi.readOnly" "$(yq '.volumes[] | select(.name == "cloud-credentials") | .csi.readOnly' "$1")" "true"
  expect "$2: volume cloud-credentials secretProviderClass" "$(yq '.volumes[] | select(.name == "cloud-credentials") | .csi.volumeAttributes.secretProviderClass' "$1")" "$3"
  expect "$2: volume cloud-credentials keys" "$(yq '.volumes[] | select(.name == "cloud-credentials") | keys | .[] ' "$1" | sort | tr '\n' ' ')" "csi name "
  expect "$2: catalyst-api mount cloud-credentials mountPath" "$(yq '.containers[] | select(.name == "catalyst-api") | .volumeMounts[] | select(.name == "cloud-credentials") | .mountPath' "$1")" "$DIR"
  expect "$2: catalyst-api mount cloud-credentials readOnly" "$(yq '.containers[] | select(.name == "catalyst-api") | .volumeMounts[] | select(.name == "cloud-credentials") | .readOnly' "$1")" "true"
}

echo "[cloud-credentials] Case 1: the default of --cloud-credentials-dir in the binary"
expect "DefaultCloudCredentialsDir in $GO_SOURCE" \
  "$(sed -n 's/^const DefaultCloudCredentialsDir = "\([^"]*\)".*$/\1/p' "$GO_SOURCE")" "$DIR"

echo "[cloud-credentials] Case 2: Helm render, default values"
helm template smoke . --show-only templates/api-deployment.yaml > "$TMP/default.yaml"
pod_spec "$TMP/default.yaml" "$TMP/default-spec.yaml"
no_env_credentials "$TMP/default-spec.yaml" "$TMP/default.yaml" "default"
expect "default: volumes named cloud-credentials" "$(yq '.volumes[] | select(.name == "cloud-credentials") | .name' "$TMP/default-spec.yaml" | grep -c . || true)" "0"
expect "default: mounts at $DIR" "$(yq '.containers[] | select(.name == "catalyst-api") | .volumeMounts[] | .mountPath' "$TMP/default-spec.yaml" | grep -cx "$DIR" || true)" "0"
expect "default: CSI volumes" "$(yq '.volumes[] | select(has("csi")) | .name' "$TMP/default-spec.yaml" | grep -c . || true)" "0"

echo "[cloud-credentials] Case 3: Helm render, catalystApi.cloudCredentials.secretProviderClass=$SPC-x"
helm template smoke . --show-only templates/api-deployment.yaml \
  --set catalystApi.cloudCredentials.secretProviderClass="$SPC-x" > "$TMP/set.yaml"
pod_spec "$TMP/set.yaml" "$TMP/set-spec.yaml"
no_env_credentials "$TMP/set-spec.yaml" "$TMP/set.yaml" "set"
csi_mount "$TMP/set-spec.yaml" "set" "$SPC-x"

echo "[cloud-credentials] Case 4: the literal manifest templates/api-deployment-kustomize.yaml"
pod_spec templates/api-deployment-kustomize.yaml "$TMP/kustomize-spec.yaml"
no_env_credentials "$TMP/kustomize-spec.yaml" templates/api-deployment-kustomize.yaml "kustomize"
csi_mount "$TMP/kustomize-spec.yaml" "kustomize" "$SPC"

if [ "$fail" -ne 0 ]; then
  echo "FAIL: the operator's cloud credentials are not delivered as mounted files only (CLD-106)" >&2
  exit 1
fi
echo "PASS: operator cloud credentials are CSI-mounted files at $DIR, never variables or a Secret (CLD-106)"
