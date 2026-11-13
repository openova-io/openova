#!/usr/bin/env bash
# Lab test SVC-210, offline part: the same delete patch on the REAL kit path,
# rendered by the pinned flux CLI (`flux build kustomization --dry-run` runs
# the kustomize-controller's own build code, without a cluster).
#
# 1. Copy clusters/_template/bootstrap-kit to a scratch directory.
# 2. In the copy, put the ring label on the HelmRelease of four slot files
#    (operations) and a different ring value on every other HelmRelease.
#    No slot file is added, moved or removed; kustomization.yaml is untouched.
# 3. Render: the original kit, the labelled copy with no patch, the labelled
#    copy with the operations delete patch.
# 4. Also render the substituting parent of the fixture (parent/) and check
#    that the "$patch" line survives Flux's variable substitution.
set -uo pipefail

ROOT=$(git rev-parse --show-toplevel)
HERE=$ROOT/tests/spikes/SVC-210
OUT=${OUT_DIR:-$PWD/out-render}
mkdir -p "$OUT"
WORK=$(mktemp -d)
LABEL=catalyst.openova.io/ring
OPS_FILES="11a-bp-powerdns-admin.yaml 29-vpa.yaml 56-bp-openova-flow-server.yaml 57-bp-openova-flow-emitter.yaml"

cp -r "$ROOT/clusters/_template/bootstrap-kit" "$WORK/kit"
for f in "$WORK"/kit/*.yaml; do
  b=$(basename "$f")
  [ "$b" = kustomization.yaml ] && continue
  ring=foundation
  case " $OPS_FILES " in *" $b "*) ring=operations ;; esac
  yq -i "(select(.kind == \"HelmRelease\") | .metadata.labels.\"$LABEL\") = \"$ring\"" "$f"
done
echo "== source diff: only label lines may differ (comments may be re-flowed by yq) =="
diff -r "$ROOT/clusters/_template/bootstrap-kit" "$WORK/kit" | grep -E '^[<>]' | grep -v -E '^[<>]\s*#' | sort | uniq -c | sort -rn | head -20
echo "== HelmReleases labelled operations in the copy =="
for b in $OPS_FILES; do yq -N 'select(.kind == "HelmRelease") | .metadata.name' "$WORK/kit/$b"; done | sort | tee "$OUT/labelled-operations.txt"

cat > "$WORK/ks-on.yaml" <<'EOF'
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: bootstrap-kit
  namespace: flux-system
spec:
  interval: 5m
  path: ./clusters/_template/bootstrap-kit
  prune: true
  sourceRef:
    kind: GitRepository
    name: openova
  wait: true
  timeout: 5m
  postBuild:
    substitute:
      SOVEREIGN_FQDN: svc210.example.test
      PROVIDER: huawei
EOF
# Same object plus the delete patch (quoted heredoc: "$patch" is literal).
cat "$WORK/ks-on.yaml" > "$WORK/ks-off.yaml"
cat >> "$WORK/ks-off.yaml" <<'EOF'
  patches:
    - target:
        kind: HelmRelease
        labelSelector: "catalyst.openova.io/ring=operations"
      patch: |
        $patch: delete
        apiVersion: helm.toolkit.fluxcd.io/v2
        kind: HelmRelease
        metadata:
          name: any
EOF

build() { # <out-name> <path> <ks-file>
  if flux build kustomization bootstrap-kit --path "$2" --kustomization-file "$3" --dry-run > "$OUT/$1.yaml" 2> "$OUT/$1.err"; then
    echo "build $1: exit 0"
  else
    echo "build $1: exit $? -- $(head -c 2000 "$OUT/$1.err")"
  fi
}
build kit-original "$ROOT/clusters/_template/bootstrap-kit" "$WORK/ks-on.yaml"
build kit-labelled-on "$WORK/kit" "$WORK/ks-on.yaml"
build kit-labelled-off "$WORK/kit" "$WORK/ks-off.yaml"

hrs()  { yq -N 'select(.kind == "HelmRelease") | .metadata.name' "$1" | sort; }
objs() { yq -N 'select(.kind != null) | .kind + "/" + (.metadata.namespace // "") + "/" + .metadata.name' "$1" | sort; }
for n in kit-original kit-labelled-on kit-labelled-off; do
  hrs "$OUT/$n.yaml" > "$OUT/$n.hr.txt"
  objs "$OUT/$n.yaml" > "$OUT/$n.objects.txt"
done
comm -23 "$OUT/kit-labelled-on.hr.txt" "$OUT/kit-labelled-off.hr.txt" > "$OUT/removed.txt"
comm -23 "$OUT/kit-labelled-on.objects.txt" "$OUT/kit-labelled-off.objects.txt" > "$OUT/removed-objects.txt"
comm -13 "$OUT/kit-labelled-on.objects.txt" "$OUT/kit-labelled-off.objects.txt" > "$OUT/added-objects.txt"
echo "== removed HelmReleases =="; cat "$OUT/removed.txt"
echo "== removed objects of any kind =="; cat "$OUT/removed-objects.txt"
echo "== objects that appear only with the patch =="; cat "$OUT/added-objects.txt"
# The labelled copy must render the same objects as the original kit, and
# with the ring label removed again, byte-identical JSON per object.
if diff -q "$OUT/kit-original.objects.txt" "$OUT/kit-labelled-on.objects.txt" >/dev/null; then same=true; else same=false; fi
norm() { yq -o=json -I=0 'select(.kind != null)' "$1" \
  | jq -c --arg l "$LABEL" 'del(.metadata.labels[$l]) | if .metadata.labels == {} then del(.metadata.labels) else . end' | sort; }
norm "$OUT/kit-original.yaml" > "$OUT/kit-original.norm"
norm "$OUT/kit-labelled-on.yaml" > "$OUT/kit-labelled-on.norm"
if cmp -s "$OUT/kit-original.norm" "$OUT/kit-labelled-on.norm"; then identical=true; else identical=false; diff "$OUT/kit-original.norm" "$OUT/kit-labelled-on.norm" | head -c 3000; fi
# Remaining operations-labelled HelmReleases in the patched render.
left=$(yq -N "select(.kind == \"HelmRelease\" and .metadata.labels.\"$LABEL\" == \"operations\") | .metadata.name" "$OUT/kit-labelled-off.yaml" | wc -l)

# ---- substituting parent ----------------------------------------------------
if flux build kustomization svc210-parent --path "$HERE/parent" --kustomization-file "$HERE/flux/ks-parent.yaml" --dry-run > "$OUT/parent.yaml" 2> "$OUT/parent.err"; then pexit=0; else pexit=$?; fi
echo "== parent render (exit $pexit) =="; cat "$OUT/parent.yaml" "$OUT/parent.err"
ppatch=$(yq 'select(.kind == "Kustomization") | .spec.patches[0].patch' "$OUT/parent.yaml")
psrc=$(yq '.spec.patches[0].patch' "$HERE/parent/kit-kustomization.yaml")
pfqdn=$(yq 'select(.kind == "Kustomization") | .metadata.annotations."svc210.openova.io/fqdn"' "$OUT/parent.yaml")

jq -n \
  --arg orig "$(wc -l < "$OUT/kit-original.hr.txt")" \
  --arg on "$(wc -l < "$OUT/kit-labelled-on.hr.txt")" \
  --arg off "$(wc -l < "$OUT/kit-labelled-off.hr.txt")" \
  --arg objon "$(wc -l < "$OUT/kit-labelled-on.objects.txt")" \
  --arg objoff "$(wc -l < "$OUT/kit-labelled-off.objects.txt")" \
  --rawfile removed "$OUT/removed.txt" --rawfile labelled "$OUT/labelled-operations.txt" \
  --rawfile removedobj "$OUT/removed-objects.txt" --rawfile addedobj "$OUT/added-objects.txt" \
  --arg same "$same" --arg identical "$identical" --arg left "$left" \
  --arg pexit "$pexit" --arg ppatch "$ppatch" --arg psrc "$psrc" --arg pfqdn "$pfqdn" '
  {
    kitHelmReleasesOriginal: ($orig | tonumber),
    kitHelmReleasesLabelledNoPatch: ($on | tonumber),
    kitHelmReleasesLabelledWithPatch: ($off | tonumber),
    kitObjectsLabelledNoPatch: ($objon | tonumber),
    kitObjectsLabelledWithPatch: ($objoff | tonumber),
    labellingChangesNoObject: ($same == "true"),
    renderIdenticalApartFromLabel: ($identical == "true"),
    removedHelmReleases: ($removed | split("\n") | map(select(length > 0))),
    removedEqualsLabelled: ($removed == $labelled),
    removedObjectsOfAnyKind: ($removedobj | split("\n") | map(select(length > 0))),
    objectsAddedByPatch: ($addedobj | split("\n") | map(select(length > 0))),
    operationsLabelledLeftAfterPatch: ($left | tonumber),
    parentRenderExit: ($pexit | tonumber),
    parentSubstitutionRan: ($pfqdn == "svc210.example.test"),
    parentPatchTextUnchanged: ($ppatch == $psrc),
    parentPatchText: $ppatch
  }' | tee "$OUT/render-verdict.json"
