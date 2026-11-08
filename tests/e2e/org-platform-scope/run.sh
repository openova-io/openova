#!/usr/bin/env bash
# tests/e2e/org-platform-scope/run.sh — the #7067 per-Organization admission
# scopes, on a REAL apiserver (kind) running REAL Kyverno, for an Organization
# that STARTS over quota — the live hw307 acmewalk307 shape after #7078.
#
# The Kyverno CLI cannot see what matters here: the in-tree Priority plugin
# runs before the webhook (and on some apiservers again after it), and the
# ResourceQuota plugin charges by scope. Only an apiserver shows both. The
# script applies the org-controller's OWN render — gitops.Render for the
# static objects, and the controller's own credit computation run against this
# apiserver (TestE2E_ClasslessCreditRender) for plan-quota — and walks:
#
#   0. the live wedge: classless platform pods (3805m / 6736Mi) + a customer
#      over its plan (1400m / 3072Mi) under the unscoped #7078 plan-quota
#      (5479m / 9286Mi — memory already over); the keycloak-config-cli hook is
#      refused, exactly as on hw307;
#   1. RBAC missing (the read Role not yet bound): the controller cannot list
#      pods and its plan-quota never shrinks below the safe bound;
#   2. first real render: plan-quota scoped NotIn the class, hard = plan +
#      coredns + the classless credit; the refused hook Job's retry is admitted
#      and charged to platform-quota;
#   3. platform pods restarted one by one: each stamped replacement is
#      admitted; the credit shrinks every re-render and reaches zero;
#   4. the customer over its plan: existing pods keep running, a NEW customer
#      pod is refused (correct); an openclaw per-user pod is the customer's; a
#      synced pod claiming the class is refused.
#
# Needs: a disposable cluster (current kubectl context) with Kyverno, go.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
NS=acme
CLS=catalyst-org-platform
OUT="$(mktemp -d)"
KCFG="${KUBECONFIG:-$HOME/.kube/config}"
fail=0
pass() { echo "PASS  $*"; }
bad() { echo "FAIL  $*"; fail=1; }

echo "== render the static objects (gitops.Render, plan m) -> $OUT"
(cd "$ROOT/core/controllers/organization" &&
  ORG_PLATFORM_SCOPE_RENDER_DIR="$OUT" go test ./internal/gitops/ -count=1 \
    -run '^TestPlatformScope_WriteRenderForAdmissionPreflight$' >/dev/null)

# credit_render [impersonate] — the controller's credit computation against
# this apiserver, rendered into plan-quota, applied the way Flux's force
# annotation does it for an immutable scope (delete + create on change).
credit_render() {
  (cd "$ROOT/core/controllers/organization" &&
    ORG_PLATFORM_SCOPE_KUBECONFIG="$KCFG" ORG_PLATFORM_SCOPE_CREDIT_OUT="$OUT/plan-quota.yaml" \
      ORG_PLATFORM_SCOPE_IMPERSONATE="${1:-}" \
      go test ./internal/controller/ -count=1 -v -run '^TestE2E_ClasslessCreditRender$' | grep -E 'credit known=' | sed 's/^ *//')
}
apply_plan_quota() {
  kubectl -n "$NS" delete resourcequota plan-quota --ignore-not-found --wait >/dev/null
  kubectl apply -f "$OUT/plan-quota.yaml" >/dev/null
}
hard() { kubectl -n "$NS" get resourcequota "$1" -o "jsonpath={.spec.hard['${2//./\\.}']}"; }
used() { kubectl -n "$NS" get resourcequota "$1" -o "jsonpath={.status.used['${2//./\\.}']}"; }
milli() { # quantity -> millicores
  case "$1" in *m) echo "${1%m}" ;; *) echo $(($1 * 1000)) ;; esac
}
pod() { # pod <name> <labels-json> <cpu-req> <mem-req> <cpu-lim> <mem-lim> [priorityClassName]
  local pc=""
  [ -n "${7:-}" ] && pc="\"priorityClassName\": \"$7\","
  cat <<EOF
{"apiVersion":"v1","kind":"Pod","metadata":{"name":"$1","namespace":"$NS","labels":$2},
 "spec":{$pc "terminationGracePeriodSeconds":0,"containers":[{"name":"c","image":"registry.k8s.io/pause:3.9",
  "resources":{"requests":{"cpu":"$3","memory":"$4"},"limits":{"cpu":"$5","memory":"$6"}}}]}}
EOF
}
create() { pod "$@" | kubectl create -f - 2>&1; }
klass() { kubectl -n "$NS" get pod "$1" -o 'jsonpath={.spec.priorityClassName}/{.spec.priority}/{.spec.preemptionPolicy}'; }
# stamped: the class name is set; the computed figures are the class's own
# (Priority plugin re-invoked, as on hw307) or absent (not re-invoked, kind).
stamped() { case "$(klass "$1")" in "$CLS/1000000/Never" | "$CLS//") return 0 ;; *) return 1 ;; esac; }

# The live acmewalk307 platform pods (all classless) and customer pods.
PLATFORM=(
  'vcluster-0|{"app":"vcluster"}|500m|1Gi|500m|1Gi'
  'bp-keycloak-0|{"app.kubernetes.io/instance":"bp-keycloak","app.kubernetes.io/name":"keycloak"}|1|2Gi|1|2Gi'
  'bp-keycloak-postgresql-0|{"app.kubernetes.io/instance":"bp-keycloak","app.kubernetes.io/name":"postgresql"}|500m|512Mi|500m|512Mi'
  'bp-newapi-newapi-pg-1|{"cnpg.io/cluster":"bp-newapi-newapi-pg"}|500m|512Mi|500m|512Mi'
  'bp-openclaw-controller|{"app.kubernetes.io/instance":"bp-openclaw","app.kubernetes.io/name":"bp-openclaw"}|250m|512Mi|250m|512Mi'
  'bp-agenity-0|{"app.kubernetes.io/instance":"bp-agenity","app.kubernetes.io/name":"bp-agenity"}|1005m|2064Mi|1050m|2112Mi'
  'oidc-gate-agenity|{"app.kubernetes.io/name":"bp-oidc-gate"}|50m|64Mi|50m|64Mi'
)
CUSTOMER=(
  'bookstack-x-acme-x-vcluster|{"app":"bookstack","vcluster.loft.sh/managed-by":"vcluster"}|100m|256Mi|100m|256Mi'
  'bp-stalwart-tenant-0|{"app.kubernetes.io/instance":"bp-stalwart-tenant"}|500m|1Gi|500m|1Gi'
  'bp-stalwart-tenant-webmail|{"app.kubernetes.io/instance":"bp-stalwart-tenant"}|500m|1Gi|500m|1Gi'
  'bp-wordpress-tenant|{"app.kubernetes.io/instance":"bp-wordpress-tenant"}|100m|256Mi|500m|512Mi'
  'wordpress-db-1|{"cnpg.io/cluster":"wordpress-db"}|100m|256Mi|500m|512Mi'
  'wordpress-db-replica-1|{"cnpg.io/cluster":"wordpress-db-replica"}|100m|256Mi|250m|512Mi'
)
mk() { local IFS='|'; set -- $1; create "$1" "$2" "$3" "$4" "$5" "$6" "${CLASS:-}"; }

echo "== 0. the live wedge (hw307 acmewalk307 after #7078)"
kubectl apply -f "$OUT/scoped/namespace.yaml" >/dev/null
# The #7078 transitional plan-quota (unscoped). Created roomy so the pods
# admit, then set to the live hard — memory is then already over, as live.
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: ResourceQuota
metadata: {name: plan-quota, namespace: $NS}
spec: {hard: {requests.cpu: "20", requests.memory: 40Gi, limits.cpu: "40", limits.memory: 80Gi}}
EOF
for p in "${PLATFORM[@]}" "${CUSTOMER[@]}"; do mk "$p" >/dev/null; done
kubectl -n "$NS" patch resourcequota plan-quota --type merge -p \
  '{"spec":{"hard":{"requests.cpu":"5479m","requests.memory":"9286Mi","limits.cpu":"9500m","limits.memory":"14122Mi"}}}' >/dev/null
for _ in $(seq 1 30); do [ "$(used plan-quota requests.memory)" = "9808Mi" ] && break; sleep 2; done
[ "$(used plan-quota requests.cpu)" = "5205m" ] && [ "$(used plan-quota requests.memory)" = "9808Mi" ] &&
  pass "starts over quota like live: used $(used plan-quota requests.cpu) / $(used plan-quota requests.memory) of 5479m / 9286Mi" ||
  bad "fixture used $(used plan-quota requests.cpu) / $(used plan-quota requests.memory)"
kubectl apply -f "$OUT/scoped/priorityclass.yaml" -f "$OUT/scoped/platform-scope-policy.yaml" -f "$OUT/scoped/platformquota.yaml" >/dev/null
kubectl -n "$NS" wait --for=condition=Ready policy.kyverno.io/org-platform-scope --timeout=120s >/dev/null
sleep 5
cat <<EOF | kubectl create -f - >/dev/null
apiVersion: batch/v1
kind: Job
metadata:
  name: bp-keycloak-keycloak-config-cli
  namespace: $NS
  annotations: {"helm.sh/hook": "post-install,post-upgrade"}
spec:
  backoffLimit: 6
  template:
    metadata:
      labels: {app.kubernetes.io/instance: bp-keycloak, app.kubernetes.io/name: keycloak, app.kubernetes.io/component: keycloak-config-cli}
    spec:
      restartPolicy: Never
      containers:
        - name: keycloak-config-cli
          image: registry.k8s.io/pause:3.9
          resources: {requests: {cpu: 250m, memory: 256Mi}, limits: {cpu: 250m, memory: 256Mi}}
EOF
ev=""
for _ in $(seq 1 30); do
  ev="$(kubectl -n "$NS" get events --field-selector involvedObject.name=bp-keycloak-keycloak-config-cli -o jsonpath='{.items[*].message}' 2>/dev/null)"
  echo "$ev" | grep -q "exceeded quota: plan-quota" && break
  sleep 2
done
echo "$ev" | grep -q "exceeded quota: plan-quota" && pass "wedge reproduced: the hook is refused by the unscoped plan-quota ($(echo "$ev" | grep -o 'exceeded quota: plan-quota[^,]*, requested: [^ ]*' | head -1))" || bad "hook was not refused under the #7078 quota: $ev"

echo "== 1. RBAC missing: the controller cannot list pods -> never below the safe bound"
kubectl apply -f "$OUT/scoped/platform-scope-rbac.yaml" >/dev/null
kubectl -n "$NS" delete rolebinding org-controller-platform-scope-read >/dev/null
log="$(credit_render system:serviceaccount:catalyst-system:catalyst-organization-controller)"
echo "    $log" | cut -c1-200
echo "$log" | grep -q "known=false" && pass "pod list forbidden -> credit unknown" || bad "credit was computed without the read RBAC: $log"
apply_plan_quota
for k in requests.cpu:5479m requests.memory:9286Mi limits.cpu:9500m limits.memory:14122Mi; do
  [ "$(hard plan-quota "${k%%:*}")" = "${k#*:}" ] && pass "unknown credit keeps ${k%%:*} at the live ${k#*:} (>= static bound)" || bad "unknown credit ${k%%:*} = $(hard plan-quota "${k%%:*}"), want ${k#*:}"
done
kubectl -n "$NS" get resourcequota plan-quota -o jsonpath='{.spec.scopeSelector.matchExpressions[0].operator}' | grep -q NotIn && pass "plan-quota scoped NotIn even with the credit unknown" || bad "plan-quota not scoped"

echo "== 2. first real render: scoped + classless credit"
kubectl apply -f "$OUT/scoped/platform-scope-rbac.yaml" >/dev/null
log="$(credit_render)"
echo "    $log" | cut -c1-200
echo "$log" | grep -q "known=true requests cpu=3805m memory=6736Mi; limits cpu=3850m memory=6784Mi; pods=7" && pass "controller credits the 7 classless platform pods (3805m / 6736Mi)" || bad "credit: $log"
apply_plan_quota
[ "$(hard plan-quota requests.cpu)" = "4159m" ] && [ "$(hard plan-quota requests.memory)" = "8166Mi" ] && pass "plan-quota hard = plan 334m/1366Mi + coredns 20m/64Mi + credit = 4159m / 8166Mi" || bad "plan-quota hard $(hard plan-quota requests.cpu) / $(hard plan-quota requests.memory)"
hookpod=""
for _ in $(seq 1 90); do
  hookpod="$(kubectl -n "$NS" get pod -l job-name=bp-keycloak-keycloak-config-cli -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  [ -n "$hookpod" ] && break
  sleep 2
done
[ -n "$hookpod" ] && stamped "$hookpod" && pass "the refused hook's retry is ADMITTED and stamped ($hookpod $(klass "$hookpod"))" || bad "hook still not admitted: '$hookpod'"
for _ in $(seq 1 30); do [ "$(used platform-quota requests.cpu)" = "250m" ] && break; sleep 2; done
[ "$(used platform-quota requests.cpu)" = "250m" ] && pass "hook charged to platform-quota (250m)" || bad "platform-quota used $(used platform-quota requests.cpu)"

echo "== 3. restart every platform pod; the credit decays to zero"
prev="$(milli "$(hard plan-quota requests.cpu)")"
for p in "${PLATFORM[@]}"; do
  name="${p%%|*}"
  kubectl -n "$NS" delete pod "$name" --wait >/dev/null
  out="$(CLASS="" mk "$p")"
  if echo "$out" | grep -q created && stamped "$name"; then pass "restarted $name admitted + stamped"; else bad "restart $name: $out $(klass "$name" 2>/dev/null)"; fi
  credit_render >/dev/null
  apply_plan_quota
  now="$(milli "$(hard plan-quota requests.cpu)")"
  [ "$now" -lt "$prev" ] && pass "credit shrank: plan-quota requests.cpu ${prev}m -> ${now}m" || bad "credit did not shrink after $name (${prev}m -> ${now}m)"
  prev="$now"
done
[ "$(hard plan-quota requests.cpu)" = "354m" ] && [ "$(hard plan-quota requests.memory)" = "1430Mi" ] && pass "credit reached zero: plan-quota = plan + coredns (354m / 1430Mi)" || bad "final plan-quota $(hard plan-quota requests.cpu) / $(hard plan-quota requests.memory)"
for _ in $(seq 1 30); do [ "$(used platform-quota requests.cpu)" = "4055m" ] && break; sleep 2; done
[ "$(used platform-quota requests.cpu)" = "4055m" ] && pass "platform-quota charges the stack + hook: 4055m of $(hard platform-quota requests.cpu)" || bad "platform-quota used $(used platform-quota requests.cpu)"

echo "== 4. the customer over its plan; per-user; synced claim"
out="$(create new-customer '{"app":"x"}' 10m 16Mi 10m 16Mi || true)"
echo "$out" | grep -q "exceeded quota: plan-quota" && pass "customer over plan: a NEW customer pod is refused (existing ones keep running)" || bad "new customer pod: $out"
[ "$(kubectl -n "$NS" get pod bp-stalwart-tenant-0 -o jsonpath='{.metadata.deletionTimestamp}')" = "" ] && pass "existing customer pods untouched" || bad "customer pod disturbed"
out="$(create sneaky-x-acme-x-vcluster '{"app.kubernetes.io/instance":"bp-keycloak","vcluster.loft.sh/managed-by":"vcluster"}' 10m 16Mi 10m 16Mi "$CLS" || true)"
echo "$out" | grep -q "synced-pods-stay-customer-scope" && pass "synced pod claiming the class refused" || bad "synced claim: $out"

kubectl -n "$NS" get resourcequota
kubectl -n "$NS" get pods -o custom-columns=NAME:.metadata.name,CLASS:.spec.priorityClassName,PRIORITY:.spec.priority

if [ "$fail" -ne 0 ]; then
  echo "== FAIL: the per-Organization admission scopes do not hold on a real apiserver"
  exit 1
fi
echo "== PASS: every assertion held on a real apiserver"
