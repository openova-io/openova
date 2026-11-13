#!/usr/bin/env bash
# Lab test SVC-210 (throwaway, never merged): the ring switch on the pinned Flux.
#
# Question: does a delete patch, targeted by the ring label and set in the
# spec.patches of a Flux Kustomization, remove exactly the labelled
# HelmReleases; does Flux then uninstall them; and does removing the patch
# reinstall them?
#
# Needs: a cluster with Flux installed from the pinned install.yaml, kubectl,
# helm, jq, yq. The Flux source is this branch on GitHub.
#
# Phases (each one applies a Kustomization object, as cloud-init does today):
#   p0  every ring on            (flux/ks-on.yaml)
#   p1  ring operations off      (flux/ks-off-operations.yaml)
#   p2  every ring on again      (flux/ks-on.yaml)
#   p3  three rings off, one of them without members (flux/ks-off-three.yaml)
#   p4  every ring on again      (flux/ks-on.yaml)
#   p5  a kit Kustomization object applied FROM GIT by a parent that runs
#       variable substitution: is the "$patch" line stored unchanged?
#
# The script measures and records. It does not stop at the first surprise.
set -uo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
OUT=${OUT_DIR:-$PWD/out}
mkdir -p "$OUT"
NS=svc210
KS=svc210-kit
LABEL=catalyst.openova.io/ring
HR=helmreleases.helm.toolkit.fluxcd.io
export PHASE_T0=0

log() { printf '%s  %s\n' "$(date -u +%H:%M:%S)" "$*"; }

# wait_until <timeout-seconds> <command...>
# Prints the seconds since PHASE_T0 at which the command first succeeded,
# or "timeout".
wait_until() {
  local to=$1; shift
  local start; start=$(date +%s)
  while :; do
    if "$@" >/dev/null 2>&1; then echo $(( $(date +%s) - PHASE_T0 )); return 0; fi
    if (( $(date +%s) - start >= to )); then echo timeout; return 1; fi
    sleep 2
  done
}

ks_observed() { # the controller has finished a reconcile of the current generation
  kubectl -n flux-system get kustomization "$1" -o json | jq -e '
    .metadata.generation == .status.observedGeneration and
    ([.status.conditions[]? | select(.type=="Ready" and (.status=="True" or .status=="False"))] | length == 1)'
}
gitrepo_ready() {
  kubectl -n flux-system get gitrepository svc210 -o json | jq -e '[.status.conditions[]? | select(.type=="Ready" and .status=="True")] | length == 1'
}
hr_set()       { kubectl -n $NS get $HR -o json | jq -r '[.items[].metadata.name] | sort | join(",")'; }
hr_ready_set() { kubectl -n $NS get $HR -o json | jq -r '[.items[] | select(.metadata.deletionTimestamp == null) | select([.status.conditions[]? | select(.type=="Ready" and .status=="True")] | length == 1) | .metadata.name] | sort | join(",")'; }
helm_set()     { helm ls -n $NS -a -o json | jq -r '[.[].name] | sort | join(",")'; }
helm_deployed_set() { helm ls -n $NS -a -o json | jq -r '[.[] | select(.status=="deployed") | .name] | sort | join(",")'; }
eq_hr()        { [ "$(hr_set)" = "$1" ]; }
eq_hr_ready()  { [ "$(hr_set)" = "$1" ] && [ "$(hr_ready_set)" = "$1" ]; }
eq_helm()      { [ "$(helm_set)" = "$1" ]; }
eq_helm_deployed() { [ "$(helm_set)" = "$1" ] && [ "$(helm_deployed_set)" = "$1" ]; }

snapshot() { # <phase> <timings-json>
  local phase=$1 timings=$2 hrs helm cms ks
  hrs=$(kubectl -n $NS get $HR -o json | jq --arg l "$LABEL" '[.items[] | {
      name: .metadata.name, uid: .metadata.uid, ring: (.metadata.labels[$l] // null),
      ready: ([.status.conditions[]? | select(.type=="Ready")][0].status // null),
      deleting: (.metadata.deletionTimestamp != null)}] | sort_by(.name)')
  helm=$(helm ls -n $NS -a -o json | jq '[.[] | {name, revision: (.revision | tonumber), status}] | sort_by(.name)')
  cms=$(kubectl -n $NS get configmap -o json | jq '[.items[] | select(.metadata.name != "kube-root-ca.crt") | .metadata.name] | sort')
  ks=$(kubectl -n flux-system get kustomization $KS -o json | jq '{
      generation: .metadata.generation, observedGeneration: .status.observedGeneration,
      patchEntries: ((.spec.patches // []) | length),
      lastAppliedRevision: .status.lastAppliedRevision,
      ready: ([.status.conditions[]? | select(.type=="Ready")][0] | {status, reason, message}),
      inventoryHelmReleases: ([.status.inventory.entries[]?.id | select(endswith("_helm.toolkit.fluxcd.io_HelmRelease")) | split("_")[1]] | sort),
      inventoryOther: ([.status.inventory.entries[]?.id | select(endswith("_helm.toolkit.fluxcd.io_HelmRelease") | not)] | sort)}')
  jq -n --arg phase "$phase" --argjson t "$timings" --argjson ks "$ks" --argjson hrs "$hrs" \
        --argjson helm "$helm" --argjson cms "$cms" \
    '{phase: $phase, secondsSinceApply: $t, kustomization: $ks, helmReleaseObjects: $hrs, helmStorage: $helm, configMaps: $cms}' \
    > "$OUT/$phase.json"
  log "snapshot $phase"
  cat "$OUT/$phase.json"
}

# ---- expected sets, computed from the fixture itself -----------------------
FIXTURE=$(kubectl kustomize "$HERE/base" | yq -o=json -I=0 'select(.kind == "HelmRelease")' - | jq -s --arg l "$LABEL" '[.[] | {name: .metadata.name, ring: (.metadata.labels[$l] // null)}]')
ALL=$(jq -r '[.[].name] | sort | join(",")' <<<"$FIXTURE")
NOT_OPS=$(jq -r '[.[] | select(.ring != "operations") | .name] | sort | join(",")' <<<"$FIXTURE")
OPS=$(jq -r '[.[] | select(.ring == "operations") | .name] | sort | join(",")' <<<"$FIXTURE")
NOT_THREE=$(jq -r '[.[] | select(.ring != "operations" and .ring != "observability" and .ring != "commerce") | .name] | sort | join(",")' <<<"$FIXTURE")
jq -n --argjson f "$FIXTURE" --arg all "$ALL" --arg ops "$OPS" --arg notops "$NOT_OPS" --arg notthree "$NOT_THREE" \
  '{fixture: $f, all: $all, operations: $ops, remainAfterOperationsOff: $notops, remainAfterThreeOff: $notthree}' > "$OUT/expected.json"
log "expected sets"; cat "$OUT/expected.json"

# ---- source ----------------------------------------------------------------
PHASE_T0=$(date +%s)
kubectl apply -f "$HERE/flux/gitrepository.yaml"
t_src=$(wait_until 900 gitrepo_ready)
kubectl -n flux-system get gitrepository svc210 -o json | jq --arg t "$t_src" --arg sha "${EXPECT_SHA:-}" \
  '{secondsToReady: $t, artifactRevision: .status.artifact.revision, expectedSha: $sha,
    ready: ([.status.conditions[]? | select(.type=="Ready")][0] | {status, reason, message})}' | tee "$OUT/source.json"

# ---- p0: every ring on -----------------------------------------------------
log "p0: apply ks-on"
PHASE_T0=$(date +%s)
kubectl apply -f "$HERE/flux/ks-on.yaml"
t_obs=$(wait_until 600 ks_observed $KS)
t_hr=$(wait_until 600 eq_hr_ready "$ALL")
t_helm=$(wait_until 300 eq_helm_deployed "$ALL")
snapshot p0 "$(jq -n --arg o "$t_obs" --arg h "$t_hr" --arg s "$t_helm" '{kustomizationObserved: $o, helmReleaseObjectsAsExpected: $h, helmStorageAsExpected: $s}')"

# ---- p1: ring operations off -----------------------------------------------
log "p1: apply ks-off-operations"
PHASE_T0=$(date +%s)
kubectl apply -f "$HERE/flux/ks-off-operations.yaml"
t_obs=$(wait_until 300 ks_observed $KS)
t_hr=$(wait_until 180 eq_hr "$NOT_OPS")
t_helm=$(wait_until 300 eq_helm "$NOT_OPS")
snapshot p1 "$(jq -n --arg o "$t_obs" --arg h "$t_hr" --arg s "$t_helm" '{kustomizationObserved: $o, helmReleaseObjectsAsExpected: $h, helmStorageAsExpected: $s}')"

# ---- p2: every ring on again ------------------------------------------------
log "p2: apply ks-on"
PHASE_T0=$(date +%s)
kubectl apply -f "$HERE/flux/ks-on.yaml"
t_obs=$(wait_until 300 ks_observed $KS)
t_hr=$(wait_until 600 eq_hr_ready "$ALL")
t_helm=$(wait_until 300 eq_helm_deployed "$ALL")
snapshot p2 "$(jq -n --arg o "$t_obs" --arg h "$t_hr" --arg s "$t_helm" '{kustomizationObserved: $o, helmReleaseObjectsAsExpected: $h, helmStorageAsExpected: $s}')"

# ---- p3: three rings off, one without members -------------------------------
log "p3: apply ks-off-three"
PHASE_T0=$(date +%s)
kubectl apply -f "$HERE/flux/ks-off-three.yaml"
t_obs=$(wait_until 300 ks_observed $KS)
t_hr=$(wait_until 180 eq_hr "$NOT_THREE")
t_helm=$(wait_until 300 eq_helm "$NOT_THREE")
snapshot p3 "$(jq -n --arg o "$t_obs" --arg h "$t_hr" --arg s "$t_helm" '{kustomizationObserved: $o, helmReleaseObjectsAsExpected: $h, helmStorageAsExpected: $s}')"

# ---- p4: every ring on again ------------------------------------------------
log "p4: apply ks-on"
PHASE_T0=$(date +%s)
kubectl apply -f "$HERE/flux/ks-on.yaml"
t_obs=$(wait_until 300 ks_observed $KS)
t_hr=$(wait_until 600 eq_hr_ready "$ALL")
t_helm=$(wait_until 300 eq_helm_deployed "$ALL")
snapshot p4 "$(jq -n --arg o "$t_obs" --arg h "$t_hr" --arg s "$t_helm" '{kustomizationObserved: $o, helmReleaseObjectsAsExpected: $h, helmStorageAsExpected: $s}')"

# ---- p5: the kit object applied from Git by a substituting parent ------------
log "p5: apply ks-parent"
PHASE_T0=$(date +%s)
kubectl apply -f "$HERE/flux/ks-parent.yaml"
t_obs=$(wait_until 300 ks_observed svc210-parent)
SRC_PATCH=$(yq '.spec.patches[0].patch' "$HERE/parent/kit-kustomization.yaml")
kubectl -n flux-system get kustomization svc210-kit-from-git -o json > "$OUT/p5-child.json" 2>/dev/null || echo '{}' > "$OUT/p5-child.json"
jq --arg src "$SRC_PATCH" --arg t "$t_obs" '{
    secondsToParentObserved: $t,
    childExists: (.metadata.name != null),
    fqdnAnnotation: (.metadata.annotations["svc210.openova.io/fqdn"] // null),
    storedPatch: (.spec.patches[0].patch // null),
    storedPatchEqualsSource: ((.spec.patches[0].patch // "") == ($src + "\n") or (.spec.patches[0].patch // "") == $src)}' \
  "$OUT/p5-child.json" | tee "$OUT/p5.json"
kubectl -n flux-system get kustomization svc210-parent -o json | jq '[.status.conditions[]? | {type, status, reason, message}]'

# ---- events and controller log lines ----------------------------------------
log "events (flux-system, kustomization $KS)"
kubectl -n flux-system get events --field-selector involvedObject.name=$KS -o json \
  | jq -r '.items | sort_by(.lastTimestamp) | .[] | "\(.lastTimestamp)  \(.reason)  \(.message)"' | tee "$OUT/events-kustomization.txt"
log "events ($NS)"
kubectl -n $NS get events -o json \
  | jq -r '.items | sort_by(.lastTimestamp) | .[] | "\(.lastTimestamp)  \(.involvedObject.kind)/\(.involvedObject.name)  \(.reason)  \(.message)"' | tee "$OUT/events-namespace.txt"
log "kustomize-controller lines for $KS"
kubectl -n flux-system logs deploy/kustomize-controller --tail=-1 | grep -F "$KS" | tee "$OUT/kustomize-controller.log" | tail -40
log "helm-controller lines for $NS"
kubectl -n flux-system logs deploy/helm-controller --tail=-1 | grep -F "\"namespace\":\"$NS\"" | tee "$OUT/helm-controller.log" | grep -i -E 'uninstall|install|delet' | tail -40

# ---- verdict ----------------------------------------------------------------
jq -n --slurpfile e "$OUT/expected.json" \
      --slurpfile p0 "$OUT/p0.json" --slurpfile p1 "$OUT/p1.json" --slurpfile p2 "$OUT/p2.json" \
      --slurpfile p3 "$OUT/p3.json" --slurpfile p4 "$OUT/p4.json" --slurpfile p5 "$OUT/p5.json" '
  def names(p): [p.helmReleaseObjects[].name] | sort | join(",");
  def helmnames(p): [p.helmStorage[].name] | sort | join(",");
  def uid(p; $n): [p.helmReleaseObjects[] | select(.name == $n) | .uid][0];
  def rev(p; $n): [p.helmStorage[] | select(.name == $n) | .revision][0];
  ($e[0]) as $x | ($p0[0]) as $a | ($p1[0]) as $b | ($p2[0]) as $c | ($p3[0]) as $d | ($p4[0]) as $f | ($p5[0]) as $g |
  ($x.operations | split(",")) as $ops |
  ($x.remainAfterOperationsOff | split(",")) as $keep |
  {
    p0_allInstalled: (names($a) == $x.all and helmnames($a) == $x.all and ([$a.helmStorage[].status] | all(. == "deployed"))),
    p1_kustomizationReady: ($b.kustomization.ready.status == "True"),
    p1_exactlyLabelledObjectsGone: (names($b) == $x.remainAfterOperationsOff),
    p1_inventoryMatches: (($b.kustomization.inventoryHelmReleases | join(",")) == $x.remainAfterOperationsOff),
    p1_helmUninstalled: (helmnames($b) == $x.remainAfterOperationsOff),
    p1_payloadsOfLabelledGone: ([$ops[] | . + "-payload"] - $b.configMaps == [$ops[] | . + "-payload"]),
    p1_labelledConfigMapKept: ($b.configMaps | index("svc210-ops-configmap") != null),
    p1_othersUntouched: ([$keep[] | (uid($a; .) == uid($b; .)) and rev($b; .) == 1] | all),
    p2_allBack: (names($c) == $x.all and helmnames($c) == $x.all and ([$c.helmStorage[].status] | all(. == "deployed"))),
    p2_labelledFreshInstall: ([$ops[] | (uid($a; .) != uid($c; .)) and rev($c; .) == 1] | all),
    p2_othersUntouched: ([$keep[] | (uid($a; .) == uid($c; .)) and rev($c; .) == 1] | all),
    p3_threeRingsOffOneEmpty: ($d.kustomization.ready.status == "True" and names($d) == $x.remainAfterThreeOff and helmnames($d) == $x.remainAfterThreeOff),
    p4_allBack: (names($f) == $x.all and helmnames($f) == $x.all),
    p5_patchSurvivesSubstitution: ($g.storedPatchEqualsSource == true),
    p5_substitutionRan: ($g.fqdnAnnotation == "svc210.example.test")
  } as $v |
  $v + {
    deletePatch: (if ($v.p1_kustomizationReady and $v.p1_exactlyLabelledObjectsGone and $v.p1_helmUninstalled and $v.p1_othersUntouched and $v.p2_allBack and $v.p2_labelledFreshInstall and $v.p2_othersUntouched)
                  then "works"
                  elif ($v.p1_kustomizationReady and names($b) == $x.all) then "ignored"
                  elif ($v.p1_kustomizationReady | not) then "build-or-apply-error"
                  else "partial" end)
  }' | tee "$OUT/verdict.json"
log "done"
