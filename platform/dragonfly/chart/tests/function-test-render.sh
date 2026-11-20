#!/usr/bin/env bash
# Render check of the function test of bp-dragonfly (templates/tests/function-test.yaml).
# The contract is docs/RUNBOOKS.md section 4.5; scripts/check-function-test.py checks it.
# CI runs every chart/tests/*.sh as `bash <script> <chart dir>`.
set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
CHECK="$(cd "$CHART_DIR/../../.." && pwd)/scripts/check-function-test.py"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $1" >&2; exit 1; }
TEST=templates/tests/function-test.yaml

# policy_names_manager <render> <release> <Deployment>: the test policy of the
# release lets the test Pod reach exactly the Pods of that one Deployment. Every
# label of the policy's manager rule is a label of the Deployment's Pods, and
# the namespace the rule names is the Deployment's namespace.
policy_names_manager() {
  python3 - "$@" <<'EOF'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
policies = [d for d in docs if d.get("kind") == "CiliumNetworkPolicy" and d["metadata"]["name"] == sys.argv[2] + "-function-test"]
rules = [r for p in policies for r in p["spec"]["egress"] if "component" in r["toEndpoints"][0]["matchLabels"]]
if len(policies) != 1 or len(rules) != 1:
    sys.exit(1)
labels = dict(rules[0]["toEndpoints"][0]["matchLabels"])
namespace = labels.pop("k8s:io.kubernetes.pod.namespace", None)
hits = [d["metadata"]["name"] for d in docs if d.get("kind") == "Deployment"
        and d["metadata"].get("namespace") == namespace
        and labels.items() <= d["spec"]["template"]["metadata"]["labels"].items()]
sys.exit(0 if hits == [sys.argv[3]] else 1)
EOF
}

python3 "$CHECK" --self-test > "$TMP/self-test.txt" || { cat "$TMP/self-test.txt" >&2; fail "check-function-test.py --self-test failed"; }

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[function-test] Case 1: default render asks the manager's health endpoint"
helm template dragonfly . --namespace dragonfly > "$TMP/default.yaml"
python3 "$CHECK" --chart bp-dragonfly --release dragonfly < "$TMP/default.yaml"
helm template dragonfly . --namespace dragonfly --show-only "$TEST" > "$TMP/default-test.yaml"
grep -q -F 'value: "http://dragonfly-manager.dragonfly.svc.cluster.local:8080/healthy"' "$TMP/default-test.yaml" || fail "the test does not ask /healthy of the Service dragonfly-manager"
grep -q -E '^  name: dragonfly-manager$' "$TMP/default.yaml" || fail "the subchart renders no Service dragonfly-manager"
grep -q -F 'targetPort: 8080' "$TMP/default.yaml" || fail "the manager Service no longer serves the REST port 8080"

echo "[function-test] Case 2: with cilium.io/v2 the test Pod may reach DNS and the manager Pods"
helm template dragonfly . --namespace dragonfly --api-versions cilium.io/v2 > "$TMP/cilium.yaml"
python3 "$CHECK" --chart bp-dragonfly --release dragonfly < "$TMP/cilium.yaml"
helm template dragonfly . --namespace dragonfly --api-versions cilium.io/v2 --show-only "$TEST" > "$TMP/cilium-test.yaml"
grep -q -F 'kind: CiliumNetworkPolicy' "$TMP/cilium-test.yaml" || fail "no CiliumNetworkPolicy for the test Pod"
grep -q -F 'app: "dragonfly"' "$TMP/cilium-test.yaml" || fail "the test policy does not name the manager Pods by app=dragonfly"
grep -q -F 'component: "manager"' "$TMP/cilium-test.yaml" || fail "the test policy does not name the manager Pods by component=manager"
grep -q -F 'k8s:io.kubernetes.pod.namespace: "dragonfly"' "$TMP/cilium-test.yaml" || fail "the test policy does not name the namespace of the manager Pods"
policy_names_manager "$TMP/cilium.yaml" dragonfly dragonfly-manager || fail "the test policy does not select the Pods of the Deployment dragonfly-manager"
if grep -q -F 'kind: CiliumNetworkPolicy' "$TMP/default-test.yaml"; then
  fail "a CiliumNetworkPolicy renders while cilium.io/v2 is not served"
fi

echo "[function-test] Case 3: another release name follows the subchart, in the address and in the policy"
helm template p2p . --namespace images --api-versions cilium.io/v2 > "$TMP/other.yaml"
python3 "$CHECK" --chart bp-dragonfly --release p2p < "$TMP/other.yaml"
grep -q -F 'value: "http://p2p-dragonfly-manager.images.svc.cluster.local:8080/healthy"' "$TMP/other.yaml" || fail "the test does not follow the release name"
grep -q -E '^  name: p2p-dragonfly-manager$' "$TMP/other.yaml" || fail "the subchart renders no Service p2p-dragonfly-manager"
grep -q -F 'app: "p2p-dragonfly"' "$TMP/other.yaml" || fail "the test policy does not follow the release name (app=p2p-dragonfly)"
policy_names_manager "$TMP/other.yaml" p2p p2p-dragonfly-manager || fail "the test policy does not select the Pods of the Deployment p2p-dragonfly-manager"

echo "[function-test] Case 4: dragonfly.namespaceOverride moves the address and the policy to that namespace"
helm template dragonfly . --namespace dragonfly --api-versions cilium.io/v2 --set dragonfly.namespaceOverride=mesh > "$TMP/namespace.yaml"
python3 "$CHECK" --chart bp-dragonfly --release dragonfly < "$TMP/namespace.yaml"
grep -q -F 'value: "http://dragonfly-manager.mesh.svc.cluster.local:8080/healthy"' "$TMP/namespace.yaml" || fail "the test does not follow dragonfly.namespaceOverride"
grep -q -F 'k8s:io.kubernetes.pod.namespace: "mesh"' "$TMP/namespace.yaml" || fail "the test policy does not follow dragonfly.namespaceOverride"
policy_names_manager "$TMP/namespace.yaml" dragonfly dragonfly-manager || fail "the test policy does not select the manager Pods in the namespace mesh"

echo "[function-test] Case 5: no manager renders no test"
helm template dragonfly . --namespace dragonfly --set dragonfly.manager.enable=false > "$TMP/nomanager.yaml"
python3 "$CHECK" --chart bp-dragonfly --release dragonfly --absent < "$TMP/nomanager.yaml"


echo "[function-test] Against stand-ins: the rendered script waits for a service that comes up late and fails one that never does"
# pod_script <render>: the script of the test Pod as Kubernetes hands it to the shell. It is the block under args, with
# every $$ turned into $ (Kubernetes expands $(VAR) and $$ in args before the shell reads them), and the Pod's paths moved
# under $TMP. It runs against stand-in commands in $TMP/bin. The stand-in sleep returns at once, so the time a test
# spends is the shell's SECONDS, which bash takes from the environment.
pod_script() {
  awk '/^      args:$/{a=1; next} a && /^        - [|]$/{b=1; next} b && /^$/{print; next} b && /^          /{print substr($0, 11); next} b{exit}' "$1" \
    | sed -e 's/[$][$]/$/g' -e "s#/tmp/#$TMP/run/#g" -e "s#/etc/function-test/[a-z0-9]*/#$TMP/etc/#g" -e "s#/var/run/secrets/kubernetes.io/serviceaccount/#$TMP/etc/#g"
}
# runs <pass|fail> <what> <VAR=value>...: runs the script with the stand-ins and the variables given; it must end that way.
runs() {
  local want=$1 what=$2 rc=0 last
  shift 2
  rm -rf "$TMP/run" "$TMP/count"
  mkdir -p "$TMP/run" "$TMP/etc"
  env PATH="$TMP/bin:$PATH" COUNT="$TMP/count" "$@" "${RUN_SHELL:-bash}" "$TMP/run.sh" > "$TMP/run.out" 2>&1 || rc=$?
  last="$(tail -n 1 "$TMP/run.out")"
  case "$want:$rc:$last" in
    pass:0:"FUNCTION-TEST pass "* | fail:[1-9]*:"FUNCTION-TEST fail "*) ;;
    *) fail "$what: exit code $rc, last line: $last" ;;
  esac
}
# stub <name>: a stand-in command from standard input. Each stand-in counts its calls in $COUNT.
stub() { mkdir -p "$TMP/bin"; cat > "$TMP/bin/$1"; chmod +x "$TMP/bin/$1"; }
printf '#!/bin/sh\nexit 0\n' | stub sleep
helm template dragonfly . --namespace dragonfly --show-only "$TEST" > "$TMP/run.yaml"
pod_script "$TMP/run.yaml" > "$TMP/run.sh"
stub curl <<'EOF'
#!/bin/bash
# Refuses the first $REFUSE connections, then answers 200.
n=$(( $(cat "$COUNT" 2>/dev/null || echo 0) + 1 )); echo "$n" > "$COUNT"
if [ "$n" -le "$REFUSE" ]; then echo "curl: (7) Failed to connect" >&2; printf 000; exit 7; fi
printf 200
EOF
runs pass "the test does not wait for a manager that refuses 20 connections" REFUSE=20 SECONDS=0 HEALTH_URL=http://dragonfly-manager:8080/healthy
runs fail "the test passes against a manager that never answers" REFUSE=1000000 SECONDS=239 HEALTH_URL=http://dragonfly-manager:8080/healthy
grep -q -F 'within 240 seconds' "$TMP/run.out" || fail "without functionTest.timeoutSeconds the wait does not end at second 240 of 300"

echo "[function-test] Case 6: functionTest.timeoutSeconds is the bound T: the Pod ends at T and the wait 60 seconds before it"
grep -q -x '  activeDeadlineSeconds: 300' "$TMP/run.yaml" || fail "without functionTest.timeoutSeconds the test Pod does not end at 300 seconds"
helm template dragonfly . --namespace dragonfly --set functionTest.timeoutSeconds=900 --show-only "$TEST" > "$TMP/bound.yaml"
grep -q -x '  activeDeadlineSeconds: 900' "$TMP/bound.yaml" || fail "with functionTest.timeoutSeconds=900 the test Pod does not end at 900 seconds"
pod_script "$TMP/bound.yaml" > "$TMP/run.sh"
runs pass "under T 900 the test does not wait for a manager that comes up after second 600" REFUSE=20 SECONDS=600 HEALTH_URL=http://dragonfly-manager:8080/healthy
runs fail "under T 900 the test passes against a manager that never answers" REFUSE=1000000 SECONDS=839 HEALTH_URL=http://dragonfly-manager:8080/healthy
grep -q -F 'the manager did not answer GET http://dragonfly-manager:8080/healthy with 200 within 840 seconds' "$TMP/run.out" || fail "under T 900 the failure does not name the manager's /healthy and the wait of 840 seconds"

echo "function-test-render: all cases passed"
