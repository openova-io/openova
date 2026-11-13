#!/usr/bin/env bash
# CMP-S1 lab test, engine half (questions 1 to 5, and the switch control
# for question 3). Runs only inside the spike-cmp-s1 workflow, against the
# throwaway kind cluster that job creates. Never run it against a real
# cluster: it installs an admission engine and creates test namespaces.
#
# Prints one "RESULT key=value" line per measured value, and writes raw
# evidence (reports, statuses, logs) under $OUT.
set -uo pipefail
export LC_ALL=C

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MAN="$HERE/manifests"
OUT="${OUT:-$PWD/evidence}"
CHART_VERSION="${CHART_VERSION:-3.9.2}"
EXC_NS="catalyst-policy-exceptions"
EXPIRY_AHEAD_S="${EXPIRY_AHEAD_S:-120}"
mkdir -p "$OUT"
: > "$OUT/results.txt"

ts() { date -u +%H:%M:%S.%3N; }
log() { printf '%s %s\n' "$(ts)" "$*"; }
now_ms() { date +%s%3N; }
result() { printf 'RESULT %s=%s\n' "$1" "$2" | tee -a "$OUT/results.txt"; }
section() { printf '\n%s ===== %s =====\n' "$(ts)" "$*"; }

VP_V=""; MP_V=""; PE_V=""; EXP=""; MSG="initial"; REPORT_RES=""

render() {
  sed -e "s/__VP__/${VP_V}/g" -e "s/__MP__/${MP_V}/g" -e "s/__PE__/${PE_V}/g" \
      -e "s/__EXP__/${EXP}/g" -e "s/__MSG__/${MSG}/g" "$MAN/$1"
}

# apply_file <manifest> : renders and applies, keeps stderr (warnings) as evidence
apply_file() {
  local f="$1" rc
  render "$f" > "$OUT/applied-$f"
  kubectl apply -f "$OUT/applied-$f" >"$OUT/apply-$f.out" 2>&1
  rc=$?
  log "apply $f (exit $rc): $(tr '\n' ' ' < "$OUT/apply-$f.out")"
  return $rc
}

deploy() { # ns name replicas
  cat <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $2
  namespace: $1
  labels:
    app: $2
spec:
  replicas: $3
  selector:
    matchLabels:
      app: $2
  template:
    metadata:
      labels:
        app: $2
    spec:
      containers:
        - name: pause
          image: registry.k8s.io/pause:3.10
EOF
}

ensure_ns() { kubectl create namespace "$1" --dry-run=client -o yaml | kubectl apply -f - >/dev/null; }

# probe <ns> <name> : server dry-run create of a 1-replica Deployment.
# Sets PROBE (admitted | denied | webhook-error | other) and PROBE_MSG.
PROBE=""; PROBE_MSG=""
probe() {
  local out rc
  out="$(deploy "$1" "$2" 1 | kubectl create --dry-run=server -f - 2>&1)"
  rc=$?
  PROBE_MSG="$(printf '%s' "$out" | tr '\n' ' ' | cut -c1-300)"
  if [ "$rc" -eq 0 ]; then PROBE=admitted
  elif printf '%s' "$out" | grep -qiE 'failed calling webhook|connection refused|no endpoints available|context deadline|i/o timeout'; then PROBE=webhook-error
  elif printf '%s' "$out" | grep -q 'denied the request'; then PROBE=denied
  else PROBE=other; fi
}

# poll_probe <ns> <name> <want> <timeout_s> : probes until PROBE=want.
# Sets POLL_MS (time to the wanted answer, or "timeout").
POLL_MS=""
poll_probe() {
  local t0 deadline
  t0=$(now_ms); deadline=$((t0 + $4 * 1000))
  while :; do
    probe "$1" "$2"
    if [ "$PROBE" = "$3" ]; then POLL_MS=$(( $(now_ms) - t0 )); return 0; fi
    if [ "$(now_ms)" -ge "$deadline" ]; then POLL_MS=timeout; return 1; fi
    sleep 1
  done
}

reports_json() { kubectl get "$REPORT_RES" -n "$1" -o json 2>/dev/null || echo '{"items":[]}'; }

# result_of <json> <deployment> <policy> -> "result|process|message" or empty
result_of() {
  jq -r --arg r "$2" --arg p "$3" '
    [ .items[] | select(.scope.kind == "Deployment" and .scope.name == $r) | .results[]?
      | select(.policy == $p or ((.policy | tostring) | endswith("/" + $p))) ]
    | first // empty
    | [ .result, (.properties.process // "-"), ((.message // "-") | gsub("\n"; " ")) ] | join("|")' <<<"$1"
}

# wait_result <ns> <deployment> <policy> <want-result> <timeout_s>
# Sets WAIT_MS and WAIT_LAST (last value seen).
WAIT_MS=""; WAIT_LAST=""
wait_result() {
  local t0 deadline j v
  t0=$(now_ms); deadline=$((t0 + $5 * 1000))
  while :; do
    j="$(reports_json "$1")"
    v="$(result_of "$j" "$2" "$3")"
    WAIT_LAST="${v:-none}"
    if [ "${v%%|*}" = "$4" ]; then WAIT_MS=$(( $(now_ms) - t0 )); return 0; fi
    if [ "$(now_ms)" -ge "$deadline" ]; then WAIT_MS=timeout; return 1; fi
    sleep 1
  done
}

# wait_both <ns> <deployment> <want-result> <timeout_s> <t0_ms>
# Times, from t0, until legacy-multi-replica and new-multi-replica both show want.
LEG_MS=""; NEW_MS=""
wait_both() {
  local deadline j a b
  LEG_MS=""; NEW_MS=""
  deadline=$(( $5 + $4 * 1000 ))
  while :; do
    j="$(reports_json "$1")"
    if [ -z "$LEG_MS" ]; then a="$(result_of "$j" "$2" legacy-multi-replica)"; [ "${a%%|*}" = "$3" ] && LEG_MS=$(( $(now_ms) - $5 )); fi
    if [ -z "$NEW_MS" ]; then b="$(result_of "$j" "$2" new-multi-replica)"; [ "${b%%|*}" = "$3" ] && NEW_MS=$(( $(now_ms) - $5 )); fi
    if [ -n "$LEG_MS" ] && [ -n "$NEW_MS" ]; then return 0; fi
    if [ "$(now_ms)" -ge "$deadline" ]; then LEG_MS="${LEG_MS:-timeout}"; NEW_MS="${NEW_MS:-timeout}"; return 1; fi
    sleep 0.5
  done
}

wait_vp_status() { # <resource> <name> : waits up to 90 s for a Ready-like status, saves it
  local i
  for i in $(seq 1 45); do
    kubectl get "$1" "$2" -o json > "$OUT/status-$2.json" 2>/dev/null
    if jq -e '(.status.conditionStatus.ready == true)
              or ([.status.conditions[]?, .status.conditionStatus.conditions[]? | select(.type == "Ready" and .status == "True")] | length > 0)' \
         "$OUT/status-$2.json" >/dev/null 2>&1; then
      log "$2 ready after ~$((i * 2)) s"; return 0
    fi
    sleep 2
  done
  log "$2 has no Ready status after 90 s: $(jq -c .status "$OUT/status-$2.json" 2>/dev/null)"
  return 1
}

flags_dump() { # <label>
  kubectl -n kyverno get deploy -o json | jq -r '.items[] | "\(.metadata.name): \([.spec.template.spec.containers[].args[]? | select(test("PolicyException|exceptionNamespace|AdmissionPolicy|backgroundScan"))] | join(" "))"' \
    | tee "$OUT/flags-$1.txt"
}

dump_logs() {
  local d
  for d in $(kubectl -n kyverno get deploy -o name); do
    kubectl -n kyverno logs "$d" --all-containers --tail=600 > "$OUT/log-${d#deployment.apps/}.txt" 2>&1
  done
}

# ---------------------------------------------------------------- install
section "Install upstream chart kyverno $CHART_VERSION"
kubectl version -o json | jq -r '"server " + .serverVersion.gitVersion' | tee "$OUT/k8s-version.txt"
result k8s_server_version "$(kubectl version -o json | jq -r .serverVersion.gitVersion)"
helm repo add kyverno https://kyverno.github.io/kyverno/ >/dev/null
helm repo update kyverno >/dev/null
helm show chart kyverno/kyverno --version "$CHART_VERSION" | tee "$OUT/chart.txt"
helm show values kyverno/kyverno --version "$CHART_VERSION" > "$OUT/chart-values.yaml"
echo "--- upstream default of features.policyExceptions:"
yq '.features.policyExceptions' "$OUT/chart-values.yaml"
result chart_app_version "$(grep '^appVersion:' "$OUT/chart.txt" | awk '{print $2}')"
result Q3_default_features.policyExceptions.enabled "$(yq '.features.policyExceptions.enabled' "$OUT/chart-values.yaml")"
result Q3_default_features.policyExceptions.namespace "'$(yq '.features.policyExceptions.namespace' "$OUT/chart-values.yaml")'"
result default_features.generateValidatingAdmissionPolicy.enabled "$(yq '.features.generateValidatingAdmissionPolicy.enabled' "$OUT/chart-values.yaml")"
result default_features.backgroundScan.backgroundScanInterval "$(yq '.features.backgroundScan.backgroundScanInterval' "$OUT/chart-values.yaml")"
result default_openreports.enabled "$(yq '.openreports.enabled' "$OUT/chart-values.yaml")"

t0=$(now_ms)
helm install kyverno kyverno/kyverno --version "$CHART_VERSION" -n kyverno --create-namespace \
  -f "$MAN/install-values.yaml" --wait --timeout 10m > "$OUT/helm-install.txt" 2>&1
rc=$?
tail -5 "$OUT/helm-install.txt"
if [ "$rc" -ne 0 ]; then
  result install "failed exit=$rc"
  kubectl get pods -A -o wide
  kubectl -n kyverno get events --sort-by=.lastTimestamp | tail -40
  dump_logs
  exit 1
fi
kubectl -n kyverno wait deploy --all --for=condition=Available --timeout=300s
result install_ms "$(( $(now_ms) - t0 ))"
kubectl -n kyverno get deploy -o json | jq -r '.items[] | "\(.metadata.name) \([.spec.template.spec.containers[].image] | join(","))"' | tee "$OUT/images.txt"
result engine_images "$(awk '{print $2}' "$OUT/images.txt" | tr ',' '\n' | sed 's#.*/##' | sort -u | tr '\n' ' ')"
echo "--- exception-related flags rendered into the controllers:"
flags_dump on
result Q3_flags_admission_controller "$(grep '^kyverno-admission-controller:' "$OUT/flags-on.txt" | cut -d: -f2- | xargs)"
result Q3_flags_reports_controller "$(grep '^kyverno-reports-controller:' "$OUT/flags-on.txt" | cut -d: -f2- | xargs)"
result Q3_flags_background_controller "$(grep '^kyverno-background-controller:' "$OUT/flags-on.txt" | cut -d: -f2- | xargs)"

# ---------------------------------------------------------------- Q1
section "Q1 served versions in group policies.kyverno.io"
kubectl get crd -o json | jq -r '.items[] | select(.spec.group == "policies.kyverno.io") | .metadata.name as $n | .spec.versions[]
  | "\($n) \(.name) served=\(.served) storage=\(.storage) deprecated=\(.deprecated // false)"' | tee "$OUT/q1-crd-versions.txt"
kubectl get --raw /apis/policies.kyverno.io > "$OUT/q1-group.json"
jq -c '{versions: [.versions[].version], preferred: .preferredVersion.version}' "$OUT/q1-group.json"
served() { kubectl get crd "$1" -o json | jq -r '[.spec.versions[] | select(.served) | .name] | join(",")'; }
storage() { kubectl get crd "$1" -o json | jq -r '.spec.versions[] | select(.storage) | .name'; }
pick() { if served "$1" | tr ',' '\n' | grep -qx v1; then echo v1; else storage "$1"; fi; }
for c in validatingpolicies mutatingpolicies policyexceptions; do
  result "Q1_${c}_served" "$(served "$c.policies.kyverno.io")"
  result "Q1_${c}_storage" "$(storage "$c.policies.kyverno.io")"
  result "Q1_${c}_deprecated_versions" "$(kubectl get crd "$c.policies.kyverno.io" -o json | jq -r '[.spec.versions[] | select(.deprecated == true) | .name] | join(",")')"
done
result Q1_group_preferred_version "$(jq -r .preferredVersion.version "$OUT/q1-group.json")"
VP_V="$(pick validatingpolicies.policies.kyverno.io)"
MP_V="$(pick mutatingpolicies.policies.kyverno.io)"
PE_V="$(pick policyexceptions.policies.kyverno.io)"
result used_versions "ValidatingPolicy=$VP_V MutatingPolicy=$MP_V PolicyException=$PE_V"
PE_FIELDS="$(kubectl get crd policyexceptions.policies.kyverno.io -o json | jq -r --arg v "$PE_V" '.spec.versions[] | select(.name == $v) | .schema.openAPIV3Schema.properties.spec.properties | keys | join(",")')"
result policyexception_spec_fields "$PE_FIELDS"
HAS_EXPIRES=no; printf '%s' "$PE_FIELDS" | tr ',' '\n' | grep -qx expiresAt && HAS_EXPIRES=yes
kubectl get crd policyexceptions.policies.kyverno.io -o json \
  | jq -r --arg v "$PE_V" '.spec.versions[] | select(.name == $v) | .schema.openAPIV3Schema.properties.spec.properties.expiresAt // "absent"' \
  | tee "$OUT/q4-expiresAt-schema.json"
kubectl get crd -o name | grep -iE 'report' | tee "$OUT/report-crds.txt"
if kubectl get crd policyreports.wgpolicyk8s.io >/dev/null 2>&1; then REPORT_RES=policyreports.wgpolicyk8s.io
elif kubectl get crd reports.openreports.io >/dev/null 2>&1; then REPORT_RES=reports.openreports.io; fi
result report_resource "${REPORT_RES:-none}"

# ---------------------------------------------------------------- Q2
section "Q2 background scan: legacy kind and new kind, same rule, different names"
ensure_ns spike-rep; ensure_ns spike-lat
deploy spike-rep rep-one 1 | kubectl apply -f - >/dev/null
deploy spike-rep rep-three 3 | kubectl apply -f - >/dev/null
log "resources created before any policy exists, so only a background scan can judge them"
apply_file legacy-validate.yaml
apply_file new-validate.yaml
t_pol=$(now_ms)
kubectl wait clusterpolicies.kyverno.io/legacy-multi-replica --for=condition=Ready --timeout=120s
wait_vp_status validatingpolicies.policies.kyverno.io new-multi-replica
deadline=$((t_pol + 300000))
while :; do
  J="$(reports_json spike-rep)"
  a="$(result_of "$J" rep-one legacy-multi-replica)"; b="$(result_of "$J" rep-one new-multi-replica)"
  c="$(result_of "$J" rep-three legacy-multi-replica)"; d="$(result_of "$J" rep-three new-multi-replica)"
  if [ -n "$a" ] && [ -n "$b" ] && [ -n "$c" ] && [ -n "$d" ]; then break; fi
  if [ "$(now_ms)" -ge "$deadline" ]; then log "timeout waiting for all four results"; break; fi
  sleep 1
done
result Q2_policy_apply_to_all_results_ms "$(( $(now_ms) - t_pol ))"
echo "$J" > "$OUT/q2-reports.json"
echo "--- report results in spike-rep (resource, policy, rule, result, severity, category, source, scored, properties, message):"
jq -r '.items[] | .scope.name as $r | .results[]? | [$r, .policy, (.rule // "-"), .result, (.severity // "-"), (.category // "-"), (.source // "-"), (.scored | tostring), ((.properties // {}) | tostring), (.message // "-")] | @tsv' <<<"$J" | tee "$OUT/q2-results.tsv"
result Q2_rep-one_legacy "${a:-none}"
result Q2_rep-one_new "${b:-none}"
result Q2_rep-three_legacy "${c:-none}"
result Q2_rep-three_new "${d:-none}"
if [ -n "$a" ] && [ "${a%%|*}" = "${b%%|*}" ] && [ -n "$c" ] && [ "${c%%|*}" = "${d%%|*}" ] && [ "${a%%|*}" = fail ] && [ "${c%%|*}" = pass ]; then
  result Q2_same_verdicts yes
else
  result Q2_same_verdicts no
fi
for r in rep-one rep-three; do
  result "Q2_${r}_fields_that_differ" "$(jq -r --arg r "$r" '
    [ .items[] | select(.scope.name == $r) | .results[]? ] as $rs
    | ($rs | map(select(.policy == "legacy-multi-replica")) | first) as $l
    | ($rs | map(select(.policy == "new-multi-replica")) | first) as $n
    | if ($l == null or $n == null) then "missing"
      else [ "result", "severity", "category", "source", "scored", "rule", "message", "properties" ]
           | map(select(($l[.] | tostring) != ($n[.] | tostring))
                 | . + "(legacy=" + ($l[.] | tostring) + " new=" + ($n[.] | tostring) + ")")
           | if length == 0 then "none" else join("; ") end
      end' <<<"$J")"
done
kubectl get validatingadmissionpolicies -o name > "$OUT/vap-after-q2.txt" 2>/dev/null
result generated_native_admission_policies_after_q2 "$(grep -c . "$OUT/vap-after-q2.txt")"

# ---------------------------------------------------------------- Q5
section "Q5 time from a resource change to its report"
for i in 1 2 3; do
  n="lat-$i"
  deploy spike-lat "$n" 1 | kubectl apply -f - >/dev/null; t0=$(now_ms)
  wait_both spike-lat "$n" fail 180 "$t0"
  result "Q5_round${i}_create_to_fail_legacy_ms" "$LEG_MS"
  result "Q5_round${i}_create_to_fail_new_ms" "$NEW_MS"
  kubectl -n spike-lat patch deploy "$n" --type merge -p '{"spec":{"replicas":3}}' >/dev/null; t0=$(now_ms)
  wait_both spike-lat "$n" pass 180 "$t0"
  result "Q5_round${i}_change_to_pass_legacy_ms" "$LEG_MS"
  result "Q5_round${i}_change_to_pass_new_ms" "$NEW_MS"
done
reports_json spike-lat > "$OUT/q5-reports.json"

# ---------------------------------------------------------------- mutate
section "Mutate: one policy of each kind"
ensure_ns spike-mut
apply_file legacy-mutate.yaml
apply_file new-mutate.yaml
kubectl wait clusterpolicies.kyverno.io/legacy-add-label --for=condition=Ready --timeout=120s
wait_vp_status mutatingpolicies.policies.kyverno.io new-add-label
t0=$(now_ms); deadline=$((t0 + 120000)); labels="{}"
while :; do
  labels="$(kubectl -n spike-mut create configmap probe-cm --from-literal=k=v --dry-run=server -o json 2>/dev/null | jq -c '.metadata.labels // {}')"
  [ -n "$labels" ] || labels='{}'
  if jq -e '."spike.openova.io/legacy-mutated" == "true" and ."spike.openova.io/new-mutated" == "true"' <<<"$labels" >/dev/null 2>&1; then break; fi
  if [ "$(now_ms)" -ge "$deadline" ]; then break; fi
  sleep 1
done
result mutate_ready_ms "$(( $(now_ms) - t0 ))"
kubectl -n spike-mut create configmap cm-1 --from-literal=k=v >/dev/null 2>&1
labels="$(kubectl -n spike-mut get configmap cm-1 -o json | jq -c '.metadata.labels // {}')"
log "cm-1 labels: $labels"
result mutate_legacy_label_added "$(jq -r 'if ."spike.openova.io/legacy-mutated" == "true" then "yes" else "no" end' <<<"$labels")"
result mutate_new_label_added "$(jq -r 'if ."spike.openova.io/new-mutated" == "true" then "yes" else "no" end' <<<"$labels")"

# ---------------------------------------------------------------- Q4
section "Q4 time condition in an exception's match condition"
for ns in "$EXC_NS" spike-exc spike-exc2 spike-exp spike-off; do ensure_ns "$ns"; done
deploy spike-exc exc-pre 1 | kubectl apply -f - >/dev/null
deploy spike-exp exp-pre 1 | kubectl apply -f - >/dev/null
log "exc-pre and exp-pre created before the enforcing policy: only a background scan judges them"
MSG="initial"; apply_file exc-validate.yaml
wait_vp_status validatingpolicies.policies.kyverno.io exc-replicas
poll_probe spike-exc exc-ctl denied 120
result Q4_control_no_exception "$PROBE after_ms=$POLL_MS"
log "denial text: $PROBE_MSG"
wait_result spike-exc exc-pre exc-replicas fail 120
result Q4_control_background_no_exception "$WAIT_LAST after_ms=$WAIT_MS"

EXP="$(date -u -d "+${EXPIRY_AHEAD_S} seconds" +%Y-%m-%dT%H:%M:%SZ)"
EXP_S="$(date -u -d "$EXP" +%s)"
# 4a: the time condition exactly as the waiver renders it, in both served
# non-deprecated versions of the exception kind.
for v in "$PE_V" v1beta1; do
  PE_SAVE="$PE_V"; PE_V="$v"
  apply_file exception-time.yaml; rc=$?
  cp "$OUT/apply-exception-time.yaml.out" "$OUT/apply-exception-time-$v.out"
  result "Q4_time_exception_apply_$v" "exit=$rc $(grep -o "undeclared reference to '[a-z]*'" "$OUT/apply-exception-time-$v.out" | sort -u | tr '\n' ' ')"
  PE_V="$PE_SAVE"
done
if kubectl -n "$EXC_NS" get policyexceptions.policies.kyverno.io spike-time-exc >/dev/null 2>&1; then
  result Q4_time_exception_stored yes
else
  result Q4_time_exception_stored no
fi

# 4b: the same waiver without the time condition (the residual design), the
# native expiry field, and the two controls.
apply_file exception-waiver.yaml
t_exc=$(now_ms)
if [ "$HAS_EXPIRES" = yes ]; then apply_file exception-expiresat.yaml; fi
apply_file exception-wrong-ns.yaml
apply_file exception-off.yaml
result Q4x_expiresAt_value "$EXP"

poll_probe spike-exc exc-a admitted 60
result Q4r_waiver_admission "$PROBE after_ms=$POLL_MS"
if [ "$PROBE" = admitted ]; then deploy spike-exc exc-a 1 | kubectl apply -f - >/dev/null 2>&1; fi
probe spike-exc2 x2
result Q4_control_exception_outside_namespace "$PROBE"
probe spike-off x3
result Q3_switch_on_exception_without_time_condition "$PROBE"
if [ "$HAS_EXPIRES" = yes ]; then probe spike-exp x4; result Q4x_expiresAt_admission_before_expiry "$PROBE"; fi

# Background under a live exception, with no trigger but the exception itself.
wait_result spike-exc exc-pre exc-replicas skip 100
since="none"; [ "$WAIT_MS" != timeout ] && since=$(( $(now_ms) - t_exc ))
result Q4r_waiver_background_exc-pre "$WAIT_LAST since_exception_ms=$since"
wait_result spike-exc exc-a exc-replicas skip 60
since="none"; [ "$WAIT_MS" != timeout ] && since=$(( $(now_ms) - t_exc ))
result Q4r_waiver_report_exc-a "$WAIT_LAST since_exception_ms=$since"
if [ "$HAS_EXPIRES" = yes ]; then
  left=$(( EXP_S - $(date +%s) - 3 )); [ "$left" -lt 3 ] && left=3
  wait_result spike-exp exp-pre exc-replicas skip "$left"
  since="none"; [ "$WAIT_MS" != timeout ] && since=$(( $(now_ms) - t_exc ))
  result Q4x_expiresAt_background_before_expiry "$WAIT_LAST since_exception_ms=$since"
fi
for ns in spike-exc spike-exp; do reports_json "$ns" > "$OUT/q4-reports-$ns-live-exception.json"; done
echo "--- results of excepted resources (resource, policy, result, properties):"
cat "$OUT"/q4-reports-spike-*-live-exception.json | jq -r '.items[] | .scope.name as $r | .results[]? | [$r, .policy, .result, ((.properties // {}) | tostring)] | @tsv'

# 4c: the native expiry field after its time, with no trigger.
if [ "$HAS_EXPIRES" = yes ]; then
  while [ "$(date +%s)" -lt $((EXP_S + 2)) ]; do sleep 1; done
  t_x=$(now_ms); adm_ms=""; bg_ms=""; deadline=$((t_x + 300000)); v=""
  while [ "$(now_ms)" -lt "$deadline" ]; do
    if [ -z "$adm_ms" ]; then probe spike-exp x5; [ "$PROBE" = denied ] && adm_ms=$(( $(now_ms) - t_x )); fi
    if [ -z "$bg_ms" ]; then v="$(result_of "$(reports_json spike-exp)" exp-pre exc-replicas)"; [ "${v%%|*}" = fail ] && bg_ms=$(( $(now_ms) - t_x )); fi
    if [ -n "$adm_ms" ] && [ -n "$bg_ms" ]; then break; fi
    sleep 3
  done
  result Q4x_expiresAt_admission_after_expiry "denied_after_ms=${adm_ms:-not-within-300s} last=$PROBE"
  result Q4x_expiresAt_background_after_expiry "fail_after_ms=${bg_ms:-not-within-300s} last=${v:-none}"
fi

# 4d: the residual path: delete the waiver, as the expiry commit does.
kubectl -n "$EXC_NS" delete policyexceptions.policies.kyverno.io spike-waiver
t_del=$(now_ms)
poll_probe spike-exc exc-b denied 90
result Q4r_after_delete_admission "$PROBE after_ms=$POLL_MS"
wait_result spike-exc exc-pre exc-replicas fail 240
since="none"; [ "$WAIT_MS" != timeout ] && since=$(( $(now_ms) - t_del ))
result Q4r_after_delete_background_exc-pre "$WAIT_LAST since_delete_ms=$since"
wait_result spike-exc exc-a exc-replicas fail 60
since="none"; [ "$WAIT_MS" != timeout ] && since=$(( $(now_ms) - t_del ))
result Q4r_after_delete_report_exc-a "$WAIT_LAST since_delete_ms=$since"
reports_json spike-exc > "$OUT/q4-reports-spike-exc-after-delete.json"
kubectl -n "$EXC_NS" get policyexceptions.policies.kyverno.io -o yaml > "$OUT/q4-exceptions.yaml" 2>&1

# ---------------------------------------------------------------- Q3 switch control
section "Q3 control: turn the exception switch off, read the same exception again"
probe spike-off x6
result Q3_switch_on_recheck "$PROBE"
t0=$(now_ms)
helm upgrade kyverno kyverno/kyverno --version "$CHART_VERSION" -n kyverno --reuse-values \
  --set features.policyExceptions.enabled=false --wait --timeout 10m > "$OUT/helm-upgrade.txt" 2>&1
log "helm upgrade exit $?"
kubectl -n kyverno rollout status deploy/kyverno-admission-controller --timeout=300s
flags_dump off
result Q3_flags_admission_controller_switch_off "$(grep '^kyverno-admission-controller:' "$OUT/flags-off.txt" | cut -d: -f2- | xargs)"
deadline=$(( $(now_ms) + 90000 )); seen=""
while [ "$(now_ms)" -lt "$deadline" ]; do
  probe spike-off x7
  seen="$seen $PROBE"
  [ "$PROBE" = denied ] && break
  sleep 2
done
result Q3_switch_off_same_exception "$PROBE after_upgrade_ms=$(( $(now_ms) - t0 ))"
log "probes seen after the upgrade:$seen"
log "message: $PROBE_MSG"

# ---------------------------------------------------------------- evidence
section "Evidence"
kubectl get validatingadmissionpolicies,validatingadmissionpolicybindings -o name > "$OUT/vap-final.txt" 2>/dev/null
cat "$OUT/vap-final.txt"
result generated_native_admission_policies_final "$(grep -c '^validatingadmissionpolicy\.' "$OUT/vap-final.txt")"
kubectl get validatingpolicies.policies.kyverno.io,mutatingpolicies.policies.kyverno.io -o yaml > "$OUT/new-kind-policies.yaml" 2>&1
kubectl get clusterpolicies.kyverno.io -o yaml > "$OUT/legacy-policies.yaml" 2>&1
dump_logs
echo "--- engine log lines that name exceptions or CEL errors:"
grep -iE 'exception|undeclared|no such overload|compile' "$OUT"/log-*.txt 2>/dev/null | tail -30 || true

section "Summary"
cat "$OUT/results.txt"
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  { echo "### CMP-S1 engine half ($(cat "$OUT/k8s-version.txt"))"; echo '```'; cat "$OUT/results.txt"; echo '```'; } >> "$GITHUB_STEP_SUMMARY"
fi
exit 0
