#!/bin/sh
# SEC-228, fact X3 (README, "A Secret that Helm kept"): runs x3_test.go in the
# envtest suite of external-secrets at the version that
# platform/external-secrets/chart/Chart.yaml pins, with the envtest
# Kubernetes version of that release's Makefile. It builds external-secrets
# and downloads its modules and the envtest binaries, so no workflow runs
# it: run it again when the pin moves. Usage: run.sh <work directory>.
# The exit status is the test's.
set -eu
export KUBECONFIG=/dev/null
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../../../../.." && pwd)
WORK=${1:?usage: run.sh <work directory>}
VER=$(awk '/- name: external-secrets$/ { on = 1; next } on && /version:/ { gsub(/"/, "", $2); print $2; exit }' "$ROOT/platform/external-secrets/chart/Chart.yaml")
[ -n "$VER" ] || { echo "run.sh: no external-secrets version in platform/external-secrets/chart/Chart.yaml" >&2; exit 1; }
mkdir -p "$WORK"
SRC="$WORK/external-secrets-$VER"
[ -d "$SRC/.git" ] || git clone --quiet --depth 1 --branch "v$VER" https://github.com/external-secrets/external-secrets.git "$SRC"
K8S=$(awk '/^KUBERNETES_VERSION *:?=/ { sub(/^[^=]*= */, ""); print; exit }' "$SRC/Makefile")
[ -n "$K8S" ] || { echo "run.sh: no KUBERNETES_VERSION in the Makefile of external-secrets $VER" >&2; exit 1; }
cp "$HERE/x3_test.go" "$SRC/pkg/controllers/externalsecret/sec228_x3_test.go"
cd "$SRC"
ASSETS=$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@release-0.19 use "$K8S" -p path --bin-dir "$WORK/envtest")
echo "external-secrets $VER, envtest Kubernetes $K8S ($ASSETS)"
cd "$SRC/pkg/controllers/externalsecret"
# fail-on-empty: a filter that selects no spec fails, never passes.
KUBEBUILDER_ASSETS="$ASSETS" go test -v -count=1 -run TestAPIs . -args -ginkgo.label-filter=sec228 -ginkgo.fail-on-empty -ginkgo.no-color
