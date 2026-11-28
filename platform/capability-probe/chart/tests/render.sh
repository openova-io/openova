#!/usr/bin/env bash
# bp-capability-probe chart test: bash platform/capability-probe/chart/tests/render.sh <chart dir>
#
# C  The render (helm template, yq). A bound alias of object-storage v1 renders one
#    SecretProviderClass with the nine keys of the contract file as its objects, every one read
#    from the binding's path, no secretObjects and no Secret; the idle Deployment and the function
#    test mount it at /bindings/<alias>; the Deployment rolls with Recreate. The chart's defaults
#    (no bound alias, no image tag) render the ServiceAccount alone and no workload. The values
#    of the NodePort sweep (ci/nodeport-render-values.yaml) render the workload and no NodePort.
# F  The rendered function-test script, run with a stub in place of /capability-probe: the line
#    it ends with on a pass and on a failure of the probe.
set -euo pipefail
CHART=${1:?usage: render.sh <chart dir>}
CONTRACT=$CHART/../../_capabilities/object-storage.yaml
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
FAIL=0
pass() { echo "  PASS  $1"; }
check() { if "${@:2}"; then pass "$1"; else echo "  FAIL  $1"; FAIL=1; fi; }
q() { yq ea "[$1] | .[]" "$2"; }   # q <expression> <render>: every result, one document read as one list
count() { yq ea "[select(.kind == \"$2\")] | length" "$1"; }
P=mounted/data/acme/shared/binding-objects/e1
OBJECTS='{"objects":{"capability":"object-storage","contract":"v1","path":"'$P'","mountPath":"/bindings/objects"}}'
render() {  # render <out> <bindings JSON> [helm flags]
  local out=$1 b=$2
  shift 2
  helm template probe "$CHART" --namespace acme --set image.tag=abc1234 --set-json "bindings=$b" "$@" >"$out"
}
refused() {  # refused <message> <bindings JSON> [helm flags]
  local msg=$1 b=$2
  shift 2
  ! helm template probe "$CHART" --namespace acme --set image.tag=abc1234 --set-json "bindings=$b" "$@" >/dev/null 2>"$TMP/err" &&
    grep -q -F -- "$msg" "$TMP/err"
}

echo "C  render"
render "$TMP/one.yaml" "$OBJECTS"
check "C1 one bound alias renders one SecretProviderClass and no Secret" \
  test "$(count "$TMP/one.yaml" SecretProviderClass) $(count "$TMP/one.yaml" Secret)" = "1 0"
q 'select(.kind == "SecretProviderClass") | .spec.parameters.objects' "$TMP/one.yaml" >"$TMP/objects.yaml"
check "C1 its objects are the nine keys of contract v1 of the contract file, in its order" \
  test "$(yq '.[].objectName' "$TMP/objects.yaml" | tr '\n' ' ')" = "$(yq '.spec.contracts[] | select(.version == "v1") | .keys[].name' "$CONTRACT" | tr '\n' ' ')" \
  -a "$(yq '. | length' "$TMP/objects.yaml")" = 9
check "C1 each object reads its own key from the binding's path" \
  test "$(yq '[.[] | select(.secretPath == "'$P'" and .secretKey == .objectName)] | length' "$TMP/objects.yaml")" = 9
check "C1 no secretObjects: no key becomes a Secret object" \
  test "$(q 'select(.kind == "SecretProviderClass") | .spec | has("secretObjects")' "$TMP/one.yaml")" = false
check "C1 provider openbao, role workload, audience openbao" \
  test "$(q 'select(.kind == "SecretProviderClass") | .spec.provider + " " + .spec.parameters.roleName + " " + .spec.parameters.audience' "$TMP/one.yaml")" = "openbao workload openbao"
check "C2 one idle Deployment of the pinned image" \
  test "$(count "$TMP/one.yaml" Deployment) $(q 'select(.kind == "Deployment") | .spec.template.spec.containers[0].image + " " + .spec.template.spec.containers[0].args[0]' "$TMP/one.yaml")" = \
  "1 ghcr.io/openova-io/openova/capability-probe:abc1234 --idle"
check "C2 it mounts the binding read-only at /bindings/objects from that SecretProviderClass" \
  test "$(q 'select(.kind == "Deployment") | .spec.template.spec | (.containers[0].volumeMounts[0].mountPath + " " + (.containers[0].volumeMounts[0].readOnly | tostring) + " " + .volumes[0].csi.volumeAttributes.secretProviderClass + " " + (.volumes[0].csi.readOnly | tostring))' "$TMP/one.yaml")" = \
  "/bindings/objects true probe-objects true"
check "C2 it is ready when the keys are present and well formed" \
  test "$(q 'select(.kind == "Deployment") | .spec.template.spec.containers[0].readinessProbe.exec.command[2]' "$TMP/one.yaml")" = \
  "/capability-probe --probe object-storage-v1 --binding-dir /bindings/objects --keys-only"
check "C3 the function test is a test hook that mounts the same binding" \
  test "$(q 'select(.kind == "Pod") | .metadata.annotations["helm.sh/hook"] + " " + .spec.volumes[0].csi.volumeAttributes.secretProviderClass + " " + .spec.containers[0].volumeMounts[0].mountPath' "$TMP/one.yaml")" = \
  "test probe-objects /bindings/objects"
check "C2 the one replica rolls with Recreate: it never needs room for a second Pod" \
  test "$(q 'select(.kind == "Deployment") | (.spec.replicas | tostring) + " " + .spec.strategy.type' "$TMP/one.yaml")" = "1 Recreate"
helm template probe "$CHART" --namespace acme >"$TMP/none.yaml"
check "C4 the defaults render the ServiceAccount alone and no workload" \
  test "$(yq ea '[select(. != null)] | length' "$TMP/none.yaml") $(count "$TMP/none.yaml" ServiceAccount) $(q 'select(.kind == "ServiceAccount") | .automountServiceAccountToken' "$TMP/none.yaml")" = "1 1 false"
helm template capability-probe "$CHART" -f "$CHART/ci/nodeport-render-values.yaml" >"$TMP/sweep.yaml"
check "C4 the values of the NodePort sweep render the workload and no NodePort" \
  test "$(count "$TMP/sweep.yaml" Deployment) $(count "$TMP/sweep.yaml" Pod) $(grep -c -i -E 'type: *NodePort|nodePort: *[1-9]' "$TMP/sweep.yaml" || true)" = "1 1 0"
check "C5 refuses a capability it has no probe for" refused 'bindings.objects: no probe for capability "postgres" contract "v1"' "${OBJECTS/object-storage/postgres}"
check "C5 refuses a path of another namespace" refused "bindings.objects: path \"mounted/data/other/shared/binding-objects/e1\" is not mounted/data/acme/shared/binding-objects/e<N>" \
  "${OBJECTS/data\/acme/data/other}"
check "C5 refuses a mount path that is not /bindings/<alias>" refused 'bindings.objects: mountPath "/data", want /bindings/objects' "${OBJECTS/\/bindings\/objects/\/data}"
check "C5 refuses to render a bound alias without a pinned image" refused 'image.tag is empty' "$OBJECTS" --set image.tag=

echo "F  the rendered function test against a stub probe"
q 'select(.kind == "Pod") | .spec.containers[0].args[0]' "$TMP/one.yaml" >"$TMP/test.sh"
check "F0 the test calls /capability-probe once, on /bindings/objects" \
  test "$(grep -c -F '/capability-probe --probe object-storage-v1 --binding-dir /bindings/objects)' "$TMP/test.sh")" = 1
sed "s#/capability-probe #$TMP/stub #" "$TMP/test.sh" >"$TMP/test-stub.sh"
printf '#!/bin/sh\necho "$STUB_LINE"\nexit "$STUB_EXIT"\n' >"$TMP/stub"
chmod +x "$TMP/stub"
STUB_LINE='PROBE pass object-storage-v1 put, got and deleted object capability-probe/0 in bucket b at http://s3.svc' STUB_EXIT=0 \
  sh "$TMP/test-stub.sh" >"$TMP/pass.out" || echo "exit $?" >>"$TMP/pass.out"
check "F1 a passing probe: the test exits 0 with the pass line last" \
  test "$(tail -n 1 "$TMP/pass.out")" = "FUNCTION-TEST pass bp-capability-probe the probe of each bound alias passed: objects"
STUB_LINE='PROBE fail object-storage-v1 key type missing' STUB_EXIT=1 sh "$TMP/test-stub.sh" >"$TMP/fail.out" && echo "exit 0" >>"$TMP/fail.out"
check "F2 a failing probe: the test fails with its FUNCTION-TEST fail line" \
  test "$(tail -n 1 "$TMP/fail.out")" = "FUNCTION-TEST fail bp-capability-probe binding objects key type missing"

if [ "$FAIL" -ne 0 ]; then
  for o in "$TMP"/*.out; do echo "── $(basename "$o")"; cat "$o"; done
  echo "bp-capability-probe chart test: FAIL"
  exit 1
fi
echo "bp-capability-probe chart test: PASS"
