#!/usr/bin/env bash
# bp-harbor: a roll never removes every copy of the registry (SOV-155).
#
# The upstream chart has one strategy knob, harbor.updateStrategy.type, and
# reads it in two Deployments only: harbor-registry and harbor-jobservice.
# bp-harbor sets it to Recreate for harbor-jobservice, whose job logs sit on a
# ReadWriteOnce claim (#5022). With the kit's storage (type s3) harbor-registry
# mounts no volume and runs two copies (SOV-138), and Recreate removed both in
# one roll: cutover step 08 rolls it under blocked outside access, the old
# copies went first and the new one could not pull its own image. Slot 19 of
# the kit patches harbor-registry after the render (spec.postRenderers of
# HelmRelease bp-harbor) to RollingUpdate with maxUnavailable 0, maxSurge 1.
#
# The test renders the chart with the kit's storage, applies every kustomize
# patch of HelmRelease bp-harbor in slot 19 to the Deployment its target names
# (a yq merge), and judges the effective strategy of every Deployment. No
# strategy is the API default, RollingUpdate with 25 % and 25 %; a percentage
# resolves against .spec.replicas, maxUnavailable rounded down and maxSurge
# rounded up, and when both resolve to 0 maxUnavailable is 1 (the controller).
#
# Cases (bash, helm and yq; no cluster), each printing
# "[bp-harbor] roll case <n>: PASS":
#   1. The Deployments of two or more copies are exactly harbor-nginx,
#      harbor-core and harbor-registry; each rolls with RollingUpdate and a
#      resolved maxUnavailable below its copies; harbor-registry with
#      maxUnavailable 0 and maxSurge 1.
#   2. The Deployments that mount a ReadWriteOnce claim of the render are
#      exactly harbor-jobservice, and it rolls with Recreate.
#   3. A copy of the slot without postRenderers fails the judge of case 1,
#      naming harbor-registry and Recreate.
#   4. A copy whose patches also give harbor-jobservice RollingUpdate fails
#      the judge of case 2, naming harbor-jobservice.
#   5. Every patch names a Deployment of the render and carries only
#      apiVersion, kind, metadata.name and spec.strategy, so the yq merge
#      equals the strategic merge Flux applies (a patch with more, or naming
#      another kind, is refused); --effective harbor-registry prints one JSON
#      line with replicas 2, the strategy of case 1 and the render's spread.
#
# Usage: roll-strategy.sh [--effective <Deployment>] [chart dir]
#   SLOT   the slot file (default: the kit's 19-harbor.yaml beside the chart)
#   --effective <Deployment> prints only that Deployment's effective replicas,
#   strategy and topologySpreadConstraints as one JSON line (SOV-139 reads it).
set -euo pipefail

effective=""
if [ "${1:-}" = "--effective" ]; then
  [ -n "${2:-}" ] || { echo "usage: $0 [--effective <Deployment>] [chart dir]" >&2; exit 2; }
  effective="$2"
  shift 2
fi
chart_dir="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
slot="${SLOT:-$chart_dir/../../../clusters/_template/bootstrap-kit/19-harbor.yaml}"
helm="${HELM_BIN:-helm}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }
say() { if [ -z "$effective" ]; then echo "$*"; fi; }

[ -f "$slot" ] || fail "no slot file at $slot (set SLOT)"
"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

# The HelmRelease bp-harbor of the slot, alone in its own file.
yq ea -N 'select(.kind == "HelmRelease") | .metadata.name' "$slot" > "$TMP/releases"
[ "$(grep -c -x -F -e bp-harbor "$TMP/releases" || true)" = 1 ] \
  || fail "the slot $slot holds $(grep -c -x -F -e bp-harbor "$TMP/releases" || true) HelmReleases bp-harbor, expected 1"
yq ea -N 'select(.kind == "HelmRelease" and .metadata.name == "bp-harbor")' "$slot" > "$TMP/hr.yaml"
release="$(yq '.spec.releaseName' "$TMP/hr.yaml")"
namespace="$(yq '.spec.targetNamespace' "$TMP/hr.yaml")"
[ "$release" != null ] || fail "HelmRelease bp-harbor names no releaseName"
[ "$namespace" != null ] || fail "HelmRelease bp-harbor names no targetNamespace"

# The render with the kit's storage; one file per Deployment.
"$helm" template "$release" "$chart_dir" --namespace "$namespace" \
  --set harbor.persistence.imageChartStorage.type=s3 > "$TMP/render.yaml" \
  || fail "the render errored"
yq ea -N 'select(.kind == "Deployment") | .metadata.name' "$TMP/render.yaml" > "$TMP/deployments"
[ -s "$TMP/deployments" ] || fail "the render holds no Deployment"
mkdir "$TMP/render"
while IFS= read -r d; do
  D="$d" yq ea -N 'select(.kind == "Deployment" and .metadata.name == strenv(D))' "$TMP/render.yaml" > "$TMP/render/$d.yaml"
done < "$TMP/deployments"
# The claims of the render that one node at a time can mount.
: > "$TMP/single-writer-claims"
yq ea -N 'select(.kind == "PersistentVolumeClaim") | .metadata.name' "$TMP/render.yaml" > "$TMP/claims"
while IFS= read -r c; do
  m="$(C="$c" yq ea -N 'select(.kind == "PersistentVolumeClaim" and .metadata.name == strenv(C)) | .spec.accessModes | join(",")' "$TMP/render.yaml")"
  case ",$m," in
    *,ReadWriteOnce,*|*,ReadWriteOncePod,*) echo "$c" >> "$TMP/single-writer-claims" ;;
  esac
done < "$TMP/claims"

# keys <file> <path>: the keys of the map at <path>, one per line; "!" when it is no map.
keys() {
  yq "$2 | select(tag == \"!!map\") | keys | .[]" "$1"
  [ "$(yq "$2 | tag" "$1")" = '!!map' ] || echo '!'
}
# only <file> <path> <allowed keys...>: the keys of <path> that are not allowed.
only() {
  local file="$1" path="$2" k
  shift 2
  keys "$file" "$path" > "$TMP/keys"
  while IFS= read -r k; do
    case " $* " in *" $k "*) ;; *) echo "$k" ;; esac
  done < "$TMP/keys"
}

# validate <hr file>: one line per patch that the judge cannot read as Flux
# applies it; nothing when every patch is a strategy patch of a Deployment.
validate() {
  local hr="$1" i j n m p target bad
  n="$(yq '.spec.postRenderers | length' "$hr")"
  for ((i = 0; i < n; i++)); do
    bad="$(only "$hr" ".spec.postRenderers[$i]" kustomize)"
    [ -z "$bad" ] || echo "postRenderers[$i] carries more than kustomize: $bad"
    bad="$(only "$hr" ".spec.postRenderers[$i].kustomize" patches)"
    [ -z "$bad" ] || echo "postRenderers[$i].kustomize carries more than patches: $bad"
    m="$(yq ".spec.postRenderers[$i].kustomize.patches | length" "$hr")"
    for ((j = 0; j < m; j++)); do
      p=".spec.postRenderers[$i].kustomize.patches[$j]"
      bad="$(only "$hr" "$p" target patch)"
      [ -z "$bad" ] || echo "$p carries more than target and patch: $bad"
      bad="$(only "$hr" "$p.target" group version kind name namespace)"
      [ -z "$bad" ] || echo "$p.target selects by more than its kind and name: $bad"
      target="$(yq "$p.target.kind" "$hr")/$(yq "$p.target.name" "$hr")"
      [ "$(yq "($p.target.group // \"apps\") + \"/\" + ($p.target.version // \"v1\")" "$hr")" = apps/v1 ] \
        || echo "$p.target is not of apps/v1"
      [ "$(yq "$p.target.namespace // \"$namespace\"" "$hr")" = "$namespace" ] \
        || echo "$p.target names another namespace than $namespace"
      case "$target" in
        Deployment/*) grep -q -x -F -e "${target#Deployment/}" "$TMP/deployments" \
                        || echo "$p.target names $target, which the render does not hold" ;;
        *) echo "$p.target names $target, not a Deployment" ;;
      esac
      yq "$p.patch" "$hr" > "$TMP/patch.yaml"
      bad="$(only "$TMP/patch.yaml" . apiVersion kind metadata spec)"
      [ -z "$bad" ] || echo "$p.patch carries more than apiVersion, kind, metadata and spec: $bad"
      [ "$(yq '.apiVersion + "/" + .kind + "/" + .metadata.name' "$TMP/patch.yaml")" = "apps/v1/$target" ] \
        || echo "$p.patch is not apiVersion apps/v1, kind and metadata.name of $target"
      bad="$(only "$TMP/patch.yaml" .metadata name)"
      [ -z "$bad" ] || echo "$p.patch carries more of metadata than its name: $bad"
      bad="$(only "$TMP/patch.yaml" .spec strategy)"
      [ -z "$bad" ] || echo "$p.patch carries more of spec than strategy: $bad"
      bad="$(only "$TMP/patch.yaml" .spec.strategy type rollingUpdate)"
      [ -z "$bad" ] || echo "$p.patch carries more of spec.strategy than type and rollingUpdate: $bad"
      if [ "$(yq '.spec.strategy.rollingUpdate | tag' "$TMP/patch.yaml")" != '!!null' ]; then
        bad="$(only "$TMP/patch.yaml" .spec.strategy.rollingUpdate maxUnavailable maxSurge)"
        [ -z "$bad" ] || echo "$p.patch carries more of rollingUpdate than maxUnavailable and maxSurge: $bad"
      fi
    done
  done
}

# resolve <value> <replicas> <down|up>: a count or a percentage of the copies.
resolve() {
  local v="$1" r="$2" p
  case "$v" in
    *%) p="${v%\%}"
        [[ "$p" =~ ^[0-9]+$ ]] || fail "the value $v is no percentage"
        if [ "$3" = down ]; then echo $((r * p / 100)); else echo $(((r * p + 99) / 100)); fi ;;
    *) [[ "$v" =~ ^[0-9]+$ ]] || fail "the value $v is no count"
       echo "$v" ;;
  esac
}

# effective <hr file> <out dir>: the render's Deployments with every patch of
# the HelmRelease merged in, and <out dir>/table with one line per Deployment:
# name, copies, type, maxUnavailable, maxSurge (resolved; - for Recreate), and
# yes when it mounts a single-writer claim of the render.
effective() {
  local hr="$1" out="$2" i j n m p d r t mu ms sw c
  validate "$hr" > "$TMP/problems"
  [ ! -s "$TMP/problems" ] || fail "the patches of HelmRelease bp-harbor: $(tr '\n' ';' < "$TMP/problems")"
  mkdir -p "$out"
  cp "$TMP"/render/*.yaml "$out/"
  n="$(yq '.spec.postRenderers | length' "$hr")"
  for ((i = 0; i < n; i++)); do
    m="$(yq ".spec.postRenderers[$i].kustomize.patches | length" "$hr")"
    for ((j = 0; j < m; j++)); do
      p=".spec.postRenderers[$i].kustomize.patches[$j]"
      d="$(yq "$p.target.name" "$hr")"
      yq "$p.patch" "$hr" > "$TMP/patch.yaml"
      yq ea -N '. as $item ireduce ({}; . * $item)' "$out/$d.yaml" "$TMP/patch.yaml" > "$TMP/merged.yaml"
      mv "$TMP/merged.yaml" "$out/$d.yaml"
    done
  done
  : > "$out/table"
  while IFS= read -r d; do
    r="$(yq '.spec.replicas // 1' "$out/$d.yaml")"
    t="$(yq '.spec.strategy.type // "RollingUpdate"' "$out/$d.yaml")"
    mu=-
    ms=-
    if [ "$t" = RollingUpdate ]; then
      mu="$(yq '.spec.strategy.rollingUpdate.maxUnavailable' "$out/$d.yaml")"
      ms="$(yq '.spec.strategy.rollingUpdate.maxSurge' "$out/$d.yaml")"
      [ "$mu" != null ] || mu=25%
      [ "$ms" != null ] || ms=25%
      mu="$(resolve "$mu" "$r" down)"
      ms="$(resolve "$ms" "$r" up)"
      if [ "$mu" = 0 ] && [ "$ms" = 0 ]; then mu=1; fi
    fi
    sw=no
    yq '.spec.template.spec.volumes[] | select(has("persistentVolumeClaim")) | .persistentVolumeClaim.claimName' "$out/$d.yaml" > "$TMP/mounted"
    while IFS= read -r c; do
      if grep -q -x -F -e "$c" "$TMP/single-writer-claims"; then sw=yes; fi
    done < "$TMP/mounted"
    printf '%s %s %s %s %s %s\n' "$d" "$r" "$t" "$mu" "$ms" "$sw" >> "$out/table"
  done < "$TMP/deployments"
}

# judge_roll <table>: one line per Deployment of two or more copies that a
# roll can empty; nothing when every roll keeps a copy.
judge_roll() {
  local got
  got="$(awk '$2 >= 2 {print $1}' "$1" | sort | tr '\n' ' ')"
  [ "$got" = "harbor-core harbor-nginx harbor-registry " ] \
    || echo "the Deployments of two or more copies are: ${got:-none}; expected harbor-core harbor-nginx harbor-registry"
  awk '$2 >= 2 && $3 != "RollingUpdate" {print $1 " runs " $2 " copies and rolls with " $3 ": a roll removes every copy at once"}
       $2 >= 2 && $3 == "RollingUpdate" && $4 >= $2 {print $1 " runs " $2 " copies and a roll may take " $4 " of them at once"}
       $1 == "harbor-registry" && ($3 != "RollingUpdate" || $4 != 0 || $5 != 1) {print "harbor-registry rolls with " $3 ", maxUnavailable " $4 " and maxSurge " $5 "; expected RollingUpdate, 0 and 1"}' "$1"
}
# judge_single_writer <table>: one line per Deployment on a single-writer
# claim that is not harbor-jobservice or does not roll with Recreate.
judge_single_writer() {
  local got
  got="$(awk '$6 == "yes" {print $1}' "$1" | sort | tr '\n' ' ')"
  [ "$got" = "harbor-jobservice " ] \
    || echo "the Deployments that mount a ReadWriteOnce claim are: ${got:-none}; expected harbor-jobservice"
  awk '$6 == "yes" && $3 != "Recreate" {print $1 " mounts a ReadWriteOnce claim and rolls with " $3 ": the new copy waits on the volume the old copy holds (#5022)"}' "$1"
}

if [ -n "$effective" ]; then
  effective "$TMP/hr.yaml" "$TMP/eff"
  [ -f "$TMP/eff/$effective.yaml" ] || fail "the render holds no Deployment $effective"
  read -r _ r t mu ms _ <<< "$(grep -E "^$effective " "$TMP/eff/table")"
  if [ "$t" = RollingUpdate ]; then
    R="$r" T="$t" MU="$mu" MS="$ms" yq -o=json -I=0 '{"replicas": env(R), "strategy": {"type": strenv(T), "rollingUpdate": {"maxUnavailable": env(MU), "maxSurge": env(MS)}}, "topologySpreadConstraints": (.spec.template.spec.topologySpreadConstraints // [])}' "$TMP/eff/$effective.yaml"
  else
    R="$r" T="$t" yq -o=json -I=0 '{"replicas": env(R), "strategy": {"type": strenv(T)}, "topologySpreadConstraints": (.spec.template.spec.topologySpreadConstraints // [])}' "$TMP/eff/$effective.yaml"
  fi
  exit 0
fi

effective "$TMP/hr.yaml" "$TMP/kit"

say "[bp-harbor] roll case 1: every Deployment of two or more copies keeps a copy through a roll"
judge_roll "$TMP/kit/table" > "$TMP/judged"
[ ! -s "$TMP/judged" ] || fail "case 1: $(tr '\n' ';' < "$TMP/judged")"
say "[bp-harbor] roll case 1: PASS"

say "[bp-harbor] roll case 2: the one Deployment on a ReadWriteOnce claim keeps Recreate"
judge_single_writer "$TMP/kit/table" > "$TMP/judged"
[ ! -s "$TMP/judged" ] || fail "case 2: $(tr '\n' ';' < "$TMP/judged")"
say "[bp-harbor] roll case 2: PASS"

say "[bp-harbor] roll case 3: a slot without postRenderers fails the judge of case 1"
yq 'del(.spec.postRenderers)' "$TMP/hr.yaml" > "$TMP/hr-none.yaml"
effective "$TMP/hr-none.yaml" "$TMP/none"
judge_roll "$TMP/none/table" > "$TMP/judged"
grep -q -E '^harbor-registry .*Recreate' "$TMP/judged" \
  || fail "case 3: the judge of case 1 does not name harbor-registry and Recreate on a slot without postRenderers: $(tr '\n' ';' < "$TMP/judged")"
say "[bp-harbor] roll case 3: PASS"

say "[bp-harbor] roll case 4: a patch that rolls harbor-jobservice fails the judge of case 2"
yq '.spec.postRenderers += [{"kustomize": {"patches": [{"target": {"kind": "Deployment", "name": "harbor-jobservice"}, "patch": "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: harbor-jobservice\nspec:\n  strategy:\n    type: RollingUpdate\n    rollingUpdate:\n      maxUnavailable: 0\n      maxSurge: 1\n"}]}}]' "$TMP/hr.yaml" > "$TMP/hr-jobservice.yaml"
effective "$TMP/hr-jobservice.yaml" "$TMP/jobservice"
judge_single_writer "$TMP/jobservice/table" > "$TMP/judged"
grep -q -E '^harbor-jobservice mounts a ReadWriteOnce claim and rolls with RollingUpdate' "$TMP/judged" \
  || fail "case 4: the judge of case 2 does not name harbor-jobservice on a patch that rolls it: $(tr '\n' ';' < "$TMP/judged")"
say "[bp-harbor] roll case 4: PASS"

say "[bp-harbor] roll case 5: every patch is a strategy patch of a Deployment; --effective"
validate "$TMP/hr.yaml" > "$TMP/problems"
[ ! -s "$TMP/problems" ] || fail "case 5: $(tr '\n' ';' < "$TMP/problems")"
[ "$(yq '[.spec.postRenderers[].kustomize.patches[]] | length' "$TMP/hr.yaml")" -ge 1 ] \
  || fail "case 5: HelmRelease bp-harbor carries no patch"
# The validator refuses what a yq merge would not apply as Flux does.
yq '.spec.postRenderers[0].kustomize.patches[0].patch |= sub("strategy:", "replicas: 3\n  strategy:")' "$TMP/hr.yaml" > "$TMP/hr-more.yaml"
validate "$TMP/hr-more.yaml" > "$TMP/problems"
grep -q -F -e 'carries more of spec than strategy: replicas' "$TMP/problems" \
  || fail "case 5: a patch that also sets spec.replicas is not refused: $(tr '\n' ';' < "$TMP/problems")"
yq '.spec.postRenderers[0].kustomize.patches[0].target.kind = "StatefulSet"' "$TMP/hr.yaml" > "$TMP/hr-kind.yaml"
validate "$TMP/hr-kind.yaml" > "$TMP/problems"
grep -q -F -e 'not a Deployment' "$TMP/problems" \
  || fail "case 5: a patch whose target is no Deployment is not refused: $(tr '\n' ';' < "$TMP/problems")"
SLOT="$slot" bash "$0" --effective harbor-registry "$chart_dir" > "$TMP/effective.json"
[ "$(grep -c . "$TMP/effective.json")" = 1 ] || fail "case 5: --effective harbor-registry printed $(grep -c . "$TMP/effective.json") lines, expected 1"
v="$(yq -p=json -o=yaml '(.replicas | tostring) + "," + .strategy.type + "," + (.strategy.rollingUpdate.maxUnavailable | tostring) + "," + (.strategy.rollingUpdate.maxSurge | tostring)' "$TMP/effective.json")"
[ "$v" = "2,RollingUpdate,0,1" ] || fail "case 5: --effective harbor-registry reads '$v', expected 2,RollingUpdate,0,1"
yq -p=json -o=json -I=0 '.topologySpreadConstraints' "$TMP/effective.json" > "$TMP/spread-effective"
yq -o=json -I=0 '.spec.template.spec.topologySpreadConstraints' "$TMP/render/harbor-registry.yaml" > "$TMP/spread-render"
cmp -s "$TMP/spread-effective" "$TMP/spread-render" || fail "case 5: --effective prints another spread than the render's"
[ "$(cat "$TMP/spread-render")" != null ] || fail "case 5: the render of harbor-registry carries no spread"
say "[bp-harbor] roll case 5: PASS"

say "[bp-harbor] roll strategy: all cases PASS"
