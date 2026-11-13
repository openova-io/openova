#!/usr/bin/env bash
# Lab test CMP-S6. Question: on the installed Flux line, after a ConfigMap
# read through HelmRelease valuesFrom changes, does stamping the annotation
# reconcile.fluxcd.io/requestedAt on the HelmRelease make it re-read the
# values at once?
#
# Runs in a CI job against a throwaway kind cluster that already has Flux
# installed from the upstream install.yaml. Usage: measure.sh <leg-name>.
# Writes one JSON line per trial to $OUT_DIR/trials.jsonl and prints
# TRIAL / RESULT / OUTCOME lines to the job log.
set -euo pipefail

LEG="${1:?usage: measure.sh <leg-name>}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
OUT="${OUT_DIR:-$PWD/out}"
mkdir -p "$OUT"
RES="$OUT/trials.jsonl"
: > "$RES"
SUM="${GITHUB_STEP_SUMMARY:-/dev/null}"

FNS=flux-system
HR=bp-kyverno-policies
CM=compliance-policy-values
TNS=spike
RCM=cmp-s6-rendered
AT_ONCE=60   # pre-registered bound for "at once", in seconds (HelmRelease interval is 900 s; gate CMP-G08 allows 180 s)
HOLD=90      # seconds a trial without a stamp watches for an apply
PF=""

now()   { date +%s.%N; }
since() { awk -v a="$1" -v b="$(date +%s.%N)" 'BEGIN{printf "%.2f", b-a}'; }
over()  { awk -v e="$(since "$1")" -v t="$2" 'BEGIN{exit !(e>t)}'; }
token() { date -u +%Y-%m-%dT%H:%M:%S.%NZ; }
log()   { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }

rendered() { kubectl -n "$TNS" get cm "$RCM" -o jsonpath='{.data.action}' 2>/dev/null || true; }
rev()      { kubectl -n "$FNS" get hr "$HR" -o jsonpath='{.status.history[0].version}' 2>/dev/null || true; }
hrfield()  { kubectl -n "$FNS" get hr "$HR" -o jsonpath="{.status.$1}" 2>/dev/null || true; }

diag() {
  [[ -n "$PF" ]] && kill "$PF" 2>/dev/null || true
  kubectl -n "$FNS" get hr "$HR" -o yaml > "$OUT/helmrelease.yaml" 2>&1 || true
  kubectl -n "$FNS" get cm "$CM" -o yaml > "$OUT/values-configmap.yaml" 2>&1 || true
  kubectl -n "$FNS" get events --sort-by=.lastTimestamp > "$OUT/flux-system-events.txt" 2>&1 || true
  kubectl -n "$FNS" logs deploy/helm-controller > "$OUT/helm-controller.log" 2>&1 || true
  kubectl -n "$FNS" logs deploy/source-controller > "$OUT/source-controller.log" 2>&1 || true
  helm -n "$FNS" history kyverno-policies > "$OUT/helm-history.txt" 2>&1 || true
}
trap diag EXIT

# set_cm <Audit|Enforce|EMPTY> <watch-label 0|1>: the values document in the
# shape of the compliance design (labels, change-id, substitute disabled).
set_cm() {
  local action="$1" watch="$2" body label=""
  if [[ "$action" == EMPTY ]]; then
    body='    {}'
  else
    body=$'    compliancePolicies:\n      probesPresent:\n        action: '"$action"
  fi
  [[ "$watch" == 1 ]] && label=$'\n    reconcile.fluxcd.io/watch: Enabled'
  kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: ConfigMap
metadata:
  name: $CM
  namespace: $FNS
  labels:
    app.kubernetes.io/managed-by: flux$label
  annotations:
    catalyst.openova.io/change-id: "$(token)"
    kustomize.toolkit.fluxcd.io/substitute: disabled
data:
  values.yaml: |
$body
EOF
}

stamp_req()   { kubectl -n "$FNS" annotate hr "$HR" --overwrite "reconcile.fluxcd.io/requestedAt=$1" >/dev/null; }
stamp_force() { kubectl -n "$FNS" annotate hr "$HR" --overwrite "reconcile.fluxcd.io/forceAt=$1" >/dev/null; }
stamp_both()  { kubectl -n "$FNS" annotate hr "$HR" --overwrite "reconcile.fluxcd.io/requestedAt=$1" "reconcile.fluxcd.io/forceAt=$1" >/dev/null; }

# wait_rendered <value> <t0> <timeout>: seconds from t0 until the chart's
# rendered ConfigMap shows <value>, or "none".
wait_rendered() {
  local want="$1" t0="$2" to="$3"
  while :; do
    if [[ "$(rendered)" == "$want" ]]; then since "$t0"; return 0; fi
    if over "$t0" "$to"; then echo none; return 0; fi
    sleep 0.25
  done
}
# wait_field <status-field> <value> <timeout>
wait_field() {
  local f="$1" want="$2" to="$3" t0
  t0=$(now)
  while :; do
    if [[ "$(hrfield "$f")" == "$want" ]]; then since "$t0"; return 0; fi
    if over "$t0" "$to"; then echo none; return 0; fi
    sleep 0.25
  done
}
# judge <latency>: yes (within AT_ONCE), late (later), no (never)
judge() {
  if [[ "$1" == none ]]; then echo no
  elif awk -v l="$1" -v t="$AT_ONCE" 'BEGIN{exit !(l<=t)}'; then echo yes
  else echo late; fi
}

record() { # trial what expected applied latency rev_before rev_after note
  jq -cn --arg leg "$LEG" --arg trial "$1" --arg what "$2" --arg expected "$3" \
    --arg applied "$4" --arg latency "$5" --arg rb "$6" --arg ra "$7" --arg note "$8" \
    --arg handled "$(hrfield lastHandledReconcileAt)" --arg forced "$(hrfield lastHandledForceAt)" \
    '{leg:$leg, trial:$trial, what:$what, expected:$expected, applied:$applied,
      latency_s:$latency, revision_before:$rb, revision_after:$ra,
      lastHandledReconcileAt:$handled, lastHandledForceAt:$forced, note:$note}' >> "$RES"
  printf 'TRIAL %-4s applied=%-4s latency_s=%-7s revision %s->%s | %s | expected: %s %s\n' \
    "$1" "$4" "$5" "$6" "$7" "$2" "$3" "$8"
}

# ---------------------------------------------------------------- setup
HC_IMAGE=$(kubectl -n "$FNS" get deploy helm-controller -o jsonpath='{.spec.template.spec.containers[0].image}')
SC_IMAGE=$(kubectl -n "$FNS" get deploy source-controller -o jsonpath='{.spec.template.spec.containers[0].image}')
K8S=$(kubectl version -o json | jq -r .serverVersion.gitVersion)
log "leg=$LEG helm-controller=$HC_IMAGE source-controller=$SC_IMAGE kubernetes=$K8S"

log "setup: in-cluster OCI registry, chart push"
kubectl apply -f "$HERE/manifests/registry.yaml" >/dev/null
kubectl -n spike-registry rollout status deploy/registry --timeout=180s
kubectl -n spike-registry port-forward svc/registry 5000:5000 > "$OUT/port-forward.log" 2>&1 &
PF=$!
for _ in $(seq 1 80); do curl -fsS http://127.0.0.1:5000/v2/ >/dev/null 2>&1 && break; sleep 0.25; done
helm package "$HERE/chart" -d "$OUT" >/dev/null
helm push "$OUT/cmp-s6-probe-0.1.0.tgz" oci://127.0.0.1:5000/charts --plain-http
kill "$PF" 2>/dev/null || true
PF=""

log "setup: HelmRepository + HelmRelease, values ConfigMap absent (optional: true)"
kubectl -n "$FNS" delete cm "$CM" --ignore-not-found >/dev/null
kubectl apply -f "$HERE/manifests/release.yaml" >/dev/null
t0=$(now)
kubectl -n "$FNS" wait "hr/$HR" --for=condition=Ready --timeout=300s
lat=$(wait_rendered Audit "$t0" 60)
record S0 "install with the values ConfigMap absent" "Ready, chart default Audit rendered" \
  "$([[ "$(rendered)" == Audit ]] && echo yes || echo no)" "$(since "$t0")" "-" "$(rev)" "install"
sleep 5   # let the post-install status settle before the first trial

# ---------------------------------------------------------------- trials
# T1: change without a stamp. The installed line should not notice it.
rb=$(rev); t0=$(now); set_cm Enforce 0
lat=$(wait_rendered Enforce "$t0" "$HOLD")
record T1 "ConfigMap created (action Enforce), no stamp, no watch label, watched ${HOLD}s" \
  "no" "$([[ $lat == none ]] && echo no || echo yes)" "$lat" "$rb" "$(rev)" ""

# T2: the stamp the design relies on.
rb=$(rev); tok=$(token); t0=$(now); stamp_req "$tok"
lat=$(wait_rendered Enforce "$t0" 180)
h=$(wait_field lastHandledReconcileAt "$tok" 30)
record T2 "stamp requestedAt after the ConfigMap change" "yes, at once" "$(judge "$lat")" "$lat" "$rb" "$(rev)" "handled_after_s=$h"

# T3: a stamp with nothing changed. Does it cut a needless revision?
rb=$(rev); tok=$(token); t0=$(now); stamp_req "$tok"
h=$(wait_field lastHandledReconcileAt "$tok" 60); sleep 5; ra=$(rev)
record T3 "stamp requestedAt with the values unchanged" "handled, no new revision" \
  "$([[ "$rb" == "$ra" ]] && echo no-new-revision || echo new-revision)" "$h" "$rb" "$ra" "handled_after_s=$h"

# T4: the stamp lands before the ConfigMap changes (the order a route gets
# when it stamps right after the commit, before the region applies the file).
rb=$(rev); tok=$(token); stamp_req "$tok"
h=$(wait_field lastHandledReconcileAt "$tok" 60)
t0=$(now); set_cm Audit 0
lat=$(wait_rendered Audit "$t0" 60)
record T4 "stamp handled first, ConfigMap changed to Audit afterwards, watched 60s" "no" \
  "$([[ $lat == none ]] && echo no || echo yes)" "$lat" "$rb" "$(rev)" "stamp_handled_after_s=$h"

# T5: forceAt alone (requestedAt left as it was).
rb=$(rev); tok=$(token); t0=$(now); stamp_force "$tok"
lat=$(wait_rendered Audit "$t0" 60)
record T5 "forceAt alone on a pending change, watched 60s" "documented to need requestedAt too" \
  "$([[ $lat == none ]] && echo no || judge "$lat")" "$lat" "$rb" "$(rev)" ""

# T6: forceAt together with requestedAt, same token (what 'flux reconcile --force' writes).
if [[ "$(rendered)" == Audit ]]; then set_cm Enforce 0; want=Enforce; else want=Audit; fi
rb=$(rev); tok=$(token); t0=$(now); stamp_both "$tok"
lat=$(wait_rendered "$want" "$t0" 180)
hf=$(wait_field lastHandledForceAt "$tok" 30)
record T6 "forceAt plus requestedAt (same token) on a pending change to $want" "yes" \
  "$(judge "$lat")" "$lat" "$rb" "$(rev)" "force_handled_after_s=$hf"

# R1..R5: repeat the design's sequence (ConfigMap written, then stamp) for timing.
for i in 1 2 3 4 5; do
  if [[ "$(rendered)" == Audit ]]; then want=Enforce; else want=Audit; fi
  set_cm "$want" 0
  rb=$(rev); tok=$(token); t0=$(now); stamp_req "$tok"
  lat=$(wait_rendered "$want" "$t0" 180)
  record "R$i" "ConfigMap changed to $want, then stamp requestedAt" "yes, at once" \
    "$(judge "$lat")" "$lat" "$rb" "$(rev)" ""
done

# T7: the empty document. A key removed from valuesFrom must fall back to the chart default.
if [[ "$(rendered)" != Enforce ]]; then
  set_cm Enforce 0; tok=$(token); t0=$(now); stamp_req "$tok"; wait_rendered Enforce "$t0" 180 >/dev/null
fi
set_cm EMPTY 0
rb=$(rev); tok=$(token); t0=$(now); stamp_req "$tok"
lat=$(wait_rendered Audit "$t0" 180)
record T7 "ConfigMap set to the empty document {}, then stamp requestedAt" "yes, chart default Audit" \
  "$(judge "$lat")" "$lat" "$rb" "$(rev)" ""

# T8: the watch label (CTL-620's premise). First the label alone, then a change without a stamp.
rb=$(rev); set_cm EMPTY 1; sleep 10
record T8a "watch label reconcile.fluxcd.io/watch=Enabled added, values unchanged, 10s" "no new revision" \
  "$([[ "$rb" == "$(rev)" ]] && echo no-new-revision || echo new-revision)" "-" "$rb" "$(rev)" ""
rb=$(rev); t0=$(now); set_cm Enforce 1
lat=$(wait_rendered Enforce "$t0" "$HOLD")
record T8b "labelled ConfigMap changed to Enforce, no stamp, watched ${HOLD}s" "only on a line that honours the label" \
  "$([[ $lat == none ]] && echo no || judge "$lat")" "$lat" "$rb" "$(rev)" ""

# T9: liveness check after a 'no': a stamp must still apply, or the 'no' is not evidence.
if [[ "$lat" == none ]]; then
  rb=$(rev); tok=$(token); t0=$(now); stamp_req "$tok"
  lat=$(wait_rendered Enforce "$t0" 180)
  record T9 "stamp requestedAt after T8b (proves the release was still live)" "yes, at once" \
    "$(judge "$lat")" "$lat" "$rb" "$(rev)" ""
fi

# ---------------------------------------------------------------- verdict
stamp_trials='["T2","R1","R2","R3","R4","R5","T7"]'
n_stamp=$(jq -s --argjson ids "$stamp_trials" '[.[] | select(.trial as $t | $ids | index($t))] | length' "$RES")
n_yes=$(jq -s --argjson ids "$stamp_trials" '[.[] | select((.trial as $t | $ids | index($t)) and .applied=="yes")] | length' "$RES")
lat_list=$(jq -rs --argjson ids "$stamp_trials" '[.[] | select(.trial as $t | $ids | index($t)) | .latency_s] | join(",")' "$RES")
lat_max=$(jq -rs --argjson ids "$stamp_trials" '[.[] | select(.trial as $t | $ids | index($t)) | .latency_s | tonumber? // empty] | max // "none"' "$RES")
get() { jq -rs --arg t "$1" '.[] | select(.trial==$t) | .applied' "$RES"; }

if [[ "$n_stamp" -gt 0 && "$n_yes" == "$n_stamp" && "$(get T1)" == no ]]; then outcome=yes
elif [[ "$(get T6)" == yes ]]; then outcome=forceAt
else outcome=no; fi

echo "RESULT leg=$LEG helm-controller=$HC_IMAGE kubernetes=$K8S"
echo "RESULT stamp_applied=$n_yes/$n_stamp latencies_s=[$lat_list] max_s=$lat_max bound_s=$AT_ONCE"
echo "RESULT no_stamp_applied(T1)=$(get T1) noop_stamp(T3)=$(get T3) stamp_before_change(T4)=$(get T4)"
echo "RESULT forceAt_alone(T5)=$(get T5) forceAt_with_requestedAt(T6)=$(get T6) empty_document(T7)=$(get T7)"
echo "RESULT watch_label_alone(T8a)=$(get T8a) watch_label_change(T8b)=$(get T8b)"
echo "OUTCOME leg=$LEG $outcome"

{
  echo "### CMP-S6 — $LEG"
  echo
  echo "helm-controller \`$HC_IMAGE\`, source-controller \`$SC_IMAGE\`, Kubernetes \`$K8S\`. Outcome: **$outcome**"
  echo
  echo "| trial | what | expected | applied | latency s | revision |"
  echo "|---|---|---|---|---|---|"
  jq -r '"| \(.trial) | \(.what) | \(.expected) | \(.applied) | \(.latency_s) | \(.revision_before) → \(.revision_after) |"' "$RES"
} >> "$SUM"
