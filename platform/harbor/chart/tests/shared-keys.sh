#!/usr/bin/env bash
# bp-harbor — one upload-state secret and one CSRF key for the Sovereign.
#
# The registry signs the state of a chunked blob upload with
# REGISTRY_HTTP_SECRET, the core signs its CSRF cookie with CSRF_KEY. The
# upstream chart generates both per cluster, while the registry's public name
# sends one client's requests to both regions: an upload begun in one region
# is refused in the other, and so is a write whose CSRF token the other region
# issued. templates/shared-keys-secret.yaml holds both in one Secret, written
# by the primary region only; the parent copies it to a follower
# (crossregion.CopiedSecrets, TestInputSync_OneUploadSecretAndCSRFKeyForTheSovereign).
#
# Cases (bash, helm and yq; no cluster):
#   1. A primary render writes harbor-shared-keys with a 16-character
#      REGISTRY_HTTP_SECRET and a 32-character CSRF_KEY (the lengths the
#      upstream chart generates; the core refuses another CSRF length).
#   2. Both registry containers read REGISTRY_HTTP_SECRET and the core reads
#      CSRF_KEY from that Secret, and the upstream Secrets carry neither.
#   3. A follower render writes no such Secret, and its registry and core
#      read the same Secret (the primary's copy).
#   4. Two names that differ fail the render.
set -euo pipefail

chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
helm="${HELM_BIN:-helm}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

render() { "$helm" template harbor "$chart_dir" --namespace harbor --set harbor.persistence.imageChartStorage.type=s3 "$@"; }
# doc <render> <kind> <name>: one document of the render.
doc() { K="$2" N="$3" yq ea -N 'select(.kind == strenv(K) and .metadata.name == strenv(N))' "$1"; }
# reads <render> <Deployment> <container> <env>: the Secret and key the env reads.
reads() {
  doc "$1" Deployment "$2" > "$TMP/d.yaml"
  C="$3" E="$4" yq '.spec.template.spec.containers[] | select(.name == strenv(C)) | .env[] | select(.name == strenv(E)) | .valueFrom.secretKeyRef.name + "/" + .valueFrom.secretKeyRef.key' "$TMP/d.yaml"
}
check_readers() {
  local r="$1" got
  for pair in harbor-registry:registry harbor-registry:registryctl; do
    got="$(reads "$r" "${pair%%:*}" "${pair##*:}" REGISTRY_HTTP_SECRET)"
    [ "$got" = harbor-shared-keys/REGISTRY_HTTP_SECRET ] \
      || fail "$2: container ${pair##*:} of ${pair%%:*} reads REGISTRY_HTTP_SECRET from '${got}', not harbor-shared-keys: each region signs upload state with its own secret"
  done
  got="$(reads "$r" harbor-core core CSRF_KEY)"
  [ "$got" = harbor-shared-keys/CSRF_KEY ] \
    || fail "$2: the core reads CSRF_KEY from '${got}', not harbor-shared-keys: each region checks CSRF tokens with its own key"
}

render > "$TMP/primary.yaml" || fail "the primary render errored"
render --set crossRegion.role=secondary > "$TMP/follower.yaml" || fail "the follower render errored"

echo "[shared-keys] Case 1: the primary writes harbor-shared-keys"
doc "$TMP/primary.yaml" Secret harbor-shared-keys > "$TMP/s.yaml"
[ -s "$TMP/s.yaml" ] || fail "case 1: the primary render holds no Secret harbor-shared-keys"
for kv in REGISTRY_HTTP_SECRET:16 CSRF_KEY:32; do
  v="$(K="${kv%%:*}" yq '.data[strenv(K)] // ""' "$TMP/s.yaml" | base64 -d)"
  [ "${#v}" = "${kv##*:}" ] || fail "case 1: ${kv%%:*} holds ${#v} characters, want ${kv##*:}"
done
[ "$(yq '.metadata.annotations["helm.sh/resource-policy"]' "$TMP/s.yaml")" = keep ] \
  || fail "case 1: harbor-shared-keys is not kept on uninstall: a reinstall would split the regions again"
echo "  PASS"

echo "[shared-keys] Case 2: the registry and the core read it; the upstream Secrets carry neither value"
check_readers "$TMP/primary.yaml" "case 2"
[ "$(doc "$TMP/primary.yaml" Secret harbor-registry | yq '.data | has("REGISTRY_HTTP_SECRET")')" = false ] \
  || fail "case 2: the upstream Secret harbor-registry still carries REGISTRY_HTTP_SECRET"
[ "$(doc "$TMP/primary.yaml" Secret harbor-core | yq '.data | has("CSRF_KEY")')" = false ] \
  || fail "case 2: the upstream Secret harbor-core still carries CSRF_KEY"
echo "  PASS"

echo "[shared-keys] Case 3: a follower writes none and reads the primary's copy"
[ -z "$(doc "$TMP/follower.yaml" Secret harbor-shared-keys)" ] \
  || fail "case 3: a follower render writes harbor-shared-keys: it would make values of its own"
check_readers "$TMP/follower.yaml" "case 3"
echo "  PASS"

echo "[shared-keys] Case 4: two different names fail the render"
if render --set harbor.core.existingXsrfSecret=other > /dev/null 2>&1; then
  fail "case 4: a render whose core reads another Secret than the registry did not fail"
fi
echo "  PASS"

echo "[shared-keys] all cases PASS"
