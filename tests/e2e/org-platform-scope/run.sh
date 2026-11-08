#!/usr/bin/env bash
# tests/e2e/org-platform-scope/run.sh — the #7067 per-Organization admission
# scopes, on a REAL apiserver (kind) running REAL Kyverno.
#
# The Kyverno CLI cannot see the failure that matters here: the in-tree
# Priority admission plugin stamps spec.priority before the webhook and is
# re-invoked after it, so a policy that only sets priorityClassName makes Pod
# CREATE fail ("the integer value of priority (0) must not be provided"). Only
# an apiserver shows that. This script applies the org-controller's OWN render
# (gitops.Render, written by TestPlatformScope_WriteRenderForAdmissionPreflight)
# and walks the rollout the gate is for:
#
#   1. legacy: an unscoped (pending) plan-quota and a platform pod admitted
#      before the Policy existed — no class;
#   2. the Policy + PriorityClass + platform-quota land; a customer pod is
#      still admitted (the pending plan-quota is unscoped and sized for all);
#   3. HAZARD: rescope plan-quota while the classless platform pod runs — the
#      next customer pod is refused (why the controller gates the rescope);
#   4. the platform pod is recreated -> stamped (class, priority 1000000,
#      preemptionPolicy Never), plan-quota usage drops, the customer pod fits;
#   5. a Helm-hook-shaped Job pod (keycloak-config-cli labels) is stamped and
#      charged to platform-quota; an openclaw per-user pod and the customer
#      pods stay in plan-quota; a vCluster-synced pod claiming the class is
#      refused.
#
# Needs: kubectl context on a disposable cluster with Kyverno installed, go.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
NS=acme
CLS=catalyst-org-platform
OUT="$(mktemp -d)"
fail=0
pass() { echo "PASS  $*"; }
bad() { echo "FAIL  $*"; fail=1; }

echo "== render (gitops.Render, plan m) -> $OUT"
(cd "$ROOT/core/controllers/organization" &&
  ORG_PLATFORM_SCOPE_RENDER_DIR="$OUT" go test ./internal/gitops/ -count=1 \
    -run '^TestPlatformScope_WriteRenderForAdmissionPreflight$' >/dev/null)
ls "$OUT/scoped" "$OUT/pending"

used() { # used <quota> <resource>
  kubectl -n "$NS" get resourcequota "$1" -o "jsonpath={.status.used['$2']}"
}
wait_used() { # wait_used <quota> <resource> <want>
  for _ in $(seq 1 60); do
    [ "$(used "$1" "$2")" = "$3" ] && return 0
    sleep 2
  done
  echo "    $1 $2 = $(used "$1" "$2"), wanted $3"
  return 1
}
pod() { # pod <name> <labels-json> <cpu-req> <mem-req> [priorityClassName]
  local pc=""
  [ -n "${5:-}" ] && pc="\"priorityClassName\": \"$5\","
  cat <<EOF
{"apiVersion":"v1","kind":"Pod","metadata":{"name":"$1","namespace":"$NS","labels":$2},
 "spec":{$pc "terminationGracePeriodSeconds":0,"containers":[{"name":"c","image":"registry.k8s.io/pause:3.9",
  "resources":{"requests":{"cpu":"$3","memory":"$4"},"limits":{"cpu":"$3","memory":"$4"}}}]}}
EOF
}
create() { pod "$@" | kubectl create -f - 2>&1; }
klass() { kubectl -n "$NS" get pod "$1" -o 'jsonpath={.spec.priorityClassName}/{.spec.priority}/{.spec.preemptionPolicy}'; }

KEYCLOAK='{"app.kubernetes.io/instance":"bp-keycloak","app.kubernetes.io/name":"keycloak"}'
CUSTOMER='{"app.kubernetes.io/instance":"bp-stalwart-tenant","app.kubernetes.io/name":"bp-stalwart-tenant"}'

echo "== 1. legacy namespace: pending (unscoped) plan-quota, no Policy yet"
kubectl apply -f "$OUT/pending/namespace.yaml" -f "$OUT/pending/resourcequota.yaml"
if kubectl -n "$NS" get resourcequota plan-quota -o jsonpath='{.spec.scopeSelector}' | grep -q PriorityClass; then
  bad "pending plan-quota rendered scoped"
else
  pass "pending plan-quota is unscoped: $(kubectl -n "$NS" get resourcequota plan-quota -o jsonpath='{.spec.hard}')"
fi
create legacy-keycloak "$KEYCLOAK" 1 1Gi >/dev/null
[ "$(klass legacy-keycloak)" = "/0/PreemptLowerPriority" ] && pass "legacy platform pod admitted classless ($(klass legacy-keycloak))" || bad "legacy pod: $(klass legacy-keycloak)"

echo "== 2. PriorityClass + Policy + platform-quota land"
kubectl apply -f "$OUT/scoped/priorityclass.yaml" -f "$OUT/scoped/platform-scope-policy.yaml" \
  -f "$OUT/scoped/platformquota.yaml" -f "$OUT/scoped/platform-scope-rbac.yaml"
kubectl -n "$NS" wait --for=condition=Ready "policy.kyverno.io/org-platform-scope" --timeout=120s
sleep 5 # webhook configuration reconcile
out="$(create cust-a "$CUSTOMER" 84m 342Mi)"
echo "$out" | grep -q created && pass "customer pod admitted under the pending quota" || bad "cust-a: $out"

echo "== 3. HAZARD: rescope plan-quota while a classless platform pod runs"
kubectl -n "$NS" delete resourcequota plan-quota --wait
kubectl apply -f "$OUT/scoped/resourcequota.yaml"
wait_used plan-quota requests.cpu 1084m && pass "scoped plan-quota charges the classless platform pod: used requests.cpu 1084m of $(kubectl -n "$NS" get resourcequota plan-quota -o "jsonpath={.spec.hard['requests\.cpu']}")" || bad "plan-quota usage"
out="$(create cust-b "$CUSTOMER" 84m 342Mi || true)"
echo "$out" | grep -q "exceeded quota: plan-quota" && pass "rescoping early REFUSES the next customer pod: $(echo "$out" | head -1 | cut -c1-160)" || bad "cust-b should have been refused: $out"

echo "== 4. platform pod recreated -> stamped by the Policy"
kubectl -n "$NS" delete pod legacy-keycloak --wait
out="$(create legacy-keycloak "$KEYCLOAK" 1 1Gi)"
echo "$out" | grep -q created && pass "platform pod admitted through the Policy (no 'priority must not be provided' refusal)" || bad "platform pod refused: $out"
[ "$(klass legacy-keycloak)" = "$CLS/1000000/Never" ] && pass "platform pod carries $(klass legacy-keycloak)" || bad "platform pod: $(klass legacy-keycloak)"
wait_used plan-quota requests.cpu 84m && pass "plan-quota usage back to the customer's 84m" || bad "plan-quota after restamp"
out="$(create cust-b "$CUSTOMER" 84m 342Mi)"
echo "$out" | grep -q created && pass "customer pod now fits plan-quota" || bad "cust-b: $out"

echo "== 5. hook Job, per-user pod, synced claim"
cat <<EOF | kubectl create -f -
apiVersion: batch/v1
kind: Job
metadata:
  name: bp-keycloak-keycloak-config-cli
  namespace: $NS
  annotations: {"helm.sh/hook": "post-install,post-upgrade"}
spec:
  backoffLimit: 0
  template:
    metadata:
      labels:
        app.kubernetes.io/instance: bp-keycloak
        app.kubernetes.io/name: keycloak
        app.kubernetes.io/component: keycloak-config-cli
    spec:
      restartPolicy: Never
      containers:
        - name: keycloak-config-cli
          image: registry.k8s.io/pause:3.9
          resources:
            requests: {cpu: 250m, memory: 256Mi}
            limits: {cpu: 250m, memory: 256Mi}
EOF
hookpod=""
for _ in $(seq 1 60); do
  hookpod="$(kubectl -n "$NS" get pod -l job-name=bp-keycloak-keycloak-config-cli -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  [ -n "$hookpod" ] && break
  sleep 2
done
[ -n "$hookpod" ] && [ "$(klass "$hookpod")" = "$CLS/1000000/Never" ] && pass "Helm-hook-shaped Job pod $hookpod admitted and stamped ($(klass "$hookpod"))" || bad "hook pod: '${hookpod}' $(kubectl -n "$NS" describe job bp-keycloak-keycloak-config-cli | tail -5)"
create openclaw-user-u1 '{"app.kubernetes.io/name":"bp-openclaw","app.kubernetes.io/component":"per-user-pod"}' 50m 64Mi >/dev/null
[ "$(klass openclaw-user-u1 | cut -d/ -f1)" = "" ] && pass "openclaw per-user pod stays customer scope" || bad "per-user pod: $(klass openclaw-user-u1)"
out="$(create sneaky-x-acme-x-vcluster '{"app.kubernetes.io/instance":"bp-keycloak","vcluster.loft.sh/managed-by":"vcluster"}' 10m 16Mi "$CLS" || true)"
echo "$out" | grep -q "synced-pods-stay-customer-scope" && pass "synced pod claiming the class refused" || bad "synced claim: $out"

echo "== quota usage"
wait_used platform-quota requests.cpu 1250m && pass "platform-quota used requests.cpu 1250m (keycloak 1 + hook 250m)" || bad "platform-quota usage"
wait_used plan-quota requests.cpu 218m && pass "plan-quota used requests.cpu 218m (2 x 84m customer + 50m per-user)" || bad "plan-quota usage"
kubectl -n "$NS" get resourcequota
kubectl -n "$NS" get pods -o custom-columns=NAME:.metadata.name,CLASS:.spec.priorityClassName,PRIORITY:.spec.priority,PREEMPT:.spec.preemptionPolicy

if [ "$fail" -ne 0 ]; then
  echo "== FAIL: the per-Organization admission scopes do not hold on a real apiserver"
  exit 1
fi
echo "== PASS: every assertion held on a real apiserver"
