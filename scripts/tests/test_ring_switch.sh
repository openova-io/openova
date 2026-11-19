#!/usr/bin/env bash
# test_ring_switch.sh - switching a ring off removes exactly that ring's releases from the kit.
#
# Every release of clusters/_template/bootstrap-kit carries the label catalyst.openova.io/ring.
# A ring is switched off by one entry in spec.patches of the kit's Flux Kustomization object: a
# strategic-merge "$patch: delete" for kind HelmRelease, targeted by the label selector
# catalyst.openova.io/ring=<ring>. Spike SVC-210 (docs/ledger/plan/spikes/SVC-210.md) proved that
# form on Flux v2.4.0, whose kustomize-controller builds with sigs.k8s.io/kustomize/api v0.17.3.
#
# For each ring this test copies the kit, adds the delete patch to the copy's kustomization.yaml
# (as the controller adds spec.patches to the kustomization.yaml of its path) and renders both
# with kustomize v5.4.3, the release built on that api. It passes when the patched render equals
# the kit's render with exactly the HelmReleases of that ring left out, and at least one release
# carries the ring. Every number comes from the render of the tree. The rings tested are the one
# given, or else every ring on a HelmRelease of the kit except foundation and provider, which are
# never switched off.
#
# It stops (exit 2) before rendering when the pinned Flux is not the one of the record: the Flux
# release the boot template installs and the kustomize-controller tag of platform/flux/chart must
# be the versions of its line "Flux version tested", and it must name kustomize api v0.17.3.
#
# Usage: bash scripts/tests/test_ring_switch.sh [ring]
#        bash scripts/tests/test_ring_switch.sh --self-test    prove that each rule can fail
# Needs bash, Go (go install fetches kustomize), awk, sed, grep, diff.
# Exit codes: 0 pass; 1 a switch did not remove exactly its ring's releases; 2 cannot judge.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
KIT=clusters/_template/bootstrap-kit
RECORD=docs/ledger/plan/spikes/SVC-210.md
TEMPLATE=infra/providers/_shared/cloudinit-control-plane.tftpl
VALUES=platform/flux/chart/values.yaml
LABEL=catalyst.openova.io/ring
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

# pinned_flux ROOT: 0 when the Flux the tree pins is the Flux the record tested.
pinned_flux() {
  local want got
  want="$(sed -n 's/^Flux version tested: \(v[0-9.]*\) (kustomize-controller \(v[0-9.]*\),.*/\1 \2/p' "$1/$RECORD")"
  got="$(grep -o 'flux2/releases/download/v[0-9.]*/install.yaml' "$1/$TEMPLATE" | sort -u | cut -d/ -f4 | tr '\n' ' ')"
  got="$got$(grep -A1 -E '^ *image: .*/fluxcd/kustomize-controller$' "$1/$VALUES" | sed -n 's/^ *tag: *//p')"
  echo "Flux pinned: $got; tested by the record: ${want:-none}"
  [ -n "$want" ] && [ "$got" = "$want" ] && grep -q -F '`sigs.k8s.io/kustomize/api` v0.17.3' "$1/$RECORD"
}

# docs FILE DROP: every document of a render, each after a line "---"; with DROP, the
# HelmReleases whose ring label is DROP are left out. With LIST=1, one line "<name> <ring>"
# per HelmRelease instead ("-" for no ring).
docs() {
  awk -v drop="$2" -v list="${LIST:-}" -v label="    $LABEL:" '
    function flush() {
      if (buf != "" && list && kind == "HelmRelease") print name, (ring == "" ? "-" : ring)
      if (buf != "" && !list && !(kind == "HelmRelease" && drop != "" && ring == drop)) printf "---\n%s", buf
      buf = kind = name = ring = top = key = ""
    }
    /^---$/ { flush(); next }
    { buf = buf $0 "\n" }
    /^[A-Za-z]/ { top = $1; if ($1 == "kind:") kind = $2 }
    top == "metadata:" && /^  [A-Za-z]/ { key = $1; if ($1 == "name:") name = $2 }
    top == "metadata:" && key == "labels:" && index($0, label " ") == 1 { ring = $2 }
    END { flush() }
  ' "$1"
}

# switch KIT RING [SELECTOR-LABEL]: 0 when the delete patch for RING removes exactly its releases.
switch() {
  local w="$WORK/$2" n_all n_ring n_off
  mkdir -p "$w/kit"
  cp -R "$1"/. "$w/kit/"
  if grep -q '^patches:' "$w/kit/kustomization.yaml"; then
    echo "cannot judge: $1/kustomization.yaml already has patches"
    return 2
  fi
  printf '%s\n' "patches:" "  - target:" "      kind: HelmRelease" "      labelSelector: \"${3:-$LABEL}=$2\"" \
    "    patch: |" "      \$patch: delete" "      apiVersion: helm.toolkit.fluxcd.io/v2" "      kind: HelmRelease" \
    "      metadata:" "        name: any" >> "$w/kit/kustomization.yaml"
  "$KUSTOMIZE" build "$1" > "$w/all.yaml" || return 2
  "$KUSTOMIZE" build "$w/kit" > "$w/off.yaml" || return 2
  n_all="$(LIST=1 docs "$w/all.yaml" "" | wc -l)"
  n_ring="$(LIST=1 docs "$w/all.yaml" "" | awk -v r="$2" '$2 == r' | wc -l)"
  n_off="$(LIST=1 docs "$w/off.yaml" "" | wc -l)"
  echo "ring $2: the kit renders $n_all HelmReleases, $n_ring carry the ring, $n_off remain with the ring switched off"
  if [ "$n_ring" -eq 0 ]; then
    echo "FAIL: no HelmRelease carries ring $2, so the switch proves nothing"
    return 1
  fi
  docs "$w/all.yaml" "$2" > "$w/want.yaml"
  docs "$w/off.yaml" "" > "$w/got.yaml"
  if ! diff -u "$w/want.yaml" "$w/got.yaml" > "$w/diff.txt"; then
    echo "FAIL: ring $2: the patched render is not the kit's render without exactly the ring's HelmReleases"
    head -n 40 "$w/diff.txt"
    return 1
  fi
}

install_kustomize() {
  GOBIN="$WORK/bin" GOFLAGS= go install sigs.k8s.io/kustomize/kustomize/v5@v5.4.3 || {
    echo "cannot judge: go install of kustomize v5.4.3 failed"
    exit 2
  }
  KUSTOMIZE="$WORK/bin/kustomize"
}

# expect TITLE WANT COMMAND...: one self-test case.
expect() {
  local title="$1" want="$2" got=0
  shift 2
  ("$@") > "$WORK/case.txt" 2>&1 || got=$?
  if [ "$got" = "$want" ]; then
    echo "ok   $title (exit $got)"
  else
    echo "FAIL $title (exit $got, want $want)"
    sed 's/^/     /' "$WORK/case.txt"
    FAILED=$((FAILED + 1))
  fi
}

self_test() {
  local f="$WORK/fixture" r
  FAILED=0
  mkdir -p "$f/kit" "$f/root/$(dirname "$RECORD")" "$f/root/$(dirname "$TEMPLATE")" "$f/root/$(dirname "$VALUES")"
  printf '%s\n' "apiVersion: kustomize.config.k8s.io/v1beta1" "kind: Kustomization" "resources:" \
    "  - 01-a.yaml" "  - 02-b.yaml" "  - 03-c.yaml" > "$f/kit/kustomization.yaml"
  for r in a:operations b:operations c:foundation; do
    printf '%s\n' "apiVersion: v1" "kind: Namespace" "metadata:" "  name: ${r%%:*}" "  labels:" "    $LABEL: ${r#*:}" "---" \
      "apiVersion: helm.toolkit.fluxcd.io/v2" "kind: HelmRelease" "metadata:" "  name: bp-${r%%:*}" "  namespace: flux-system" \
      "  labels:" "    $LABEL: ${r#*:}" "spec:" "  dependsOn:" "    - name: bp-c" > "$f/kit/0$((++n))-${r%%:*}.yaml"
  done
  printf '%s\n' "Flux version tested: v2.4.0 (kustomize-controller v1.4.0, helm-controller v1.1.0)" \
    'At tag v1.4.0, the go.mod of kustomize-controller pins `sigs.k8s.io/kustomize/api` v0.17.3' > "$f/root/$RECORD"
  echo "url: https://github.com/fluxcd/flux2/releases/download/v2.4.0/install.yaml" > "$f/root/$TEMPLATE"
  printf '%s\n' "kustomizeController:" "  image: x/fluxcd/kustomize-controller" "  tag: v1.4.0" > "$f/root/$VALUES"
  expect "the switch of a ring with two releases passes" 0 switch "$f/kit" operations
  expect "a selector that matches no release fails" 1 switch "$f/kit" observability-x
  expect "a patch the build ignores fails" 1 switch "$f/kit" foundation "$LABEL-x"
  cp -R "$f/kit" "$f/patched" && echo "patches: []" >> "$f/patched/kustomization.yaml"
  expect "a kit whose kustomization.yaml has patches cannot be judged" 2 switch "$f/patched" operations
  expect "the Flux of the record passes" 0 pinned_flux "$f/root"
  sed -i 's#v2.4.0/install#v2.5.0/install#' "$f/root/$TEMPLATE"
  expect "another Flux release in the boot template stops" 1 pinned_flux "$f/root"
  sed -i 's#v2.5.0/install#v2.4.0/install#' "$f/root/$TEMPLATE" && sed -i 's/tag: v1.4.0/tag: v1.5.0/' "$f/root/$VALUES"
  expect "another kustomize-controller tag stops" 1 pinned_flux "$f/root"
  sed -i 's/tag: v1.5.0/tag: v1.4.0/' "$f/root/$VALUES" && sed -i 's/v0.17.3/v0.18.0/' "$f/root/$RECORD"
  expect "a record that names another kustomize api stops" 1 pinned_flux "$f/root"
  if [ "$FAILED" -gt 0 ]; then
    echo "test_ring_switch: self-test: $FAILED cases not as expected"
    exit 1
  fi
  echo "test_ring_switch: self-test passed"
}

n=0
install_kustomize
if [ "${1:-}" = "--self-test" ]; then
  self_test
  exit 0
fi
if ! pinned_flux "$ROOT"; then
  echo "cannot judge: the pinned Flux is not the Flux spike SVC-210 tested; run the spike again before trusting the switch"
  exit 2
fi
if [ -n "${1:-}" ]; then
  rings="$1"
else
  "$KUSTOMIZE" build "$ROOT/$KIT" > "$WORK/kit.yaml" || { echo "cannot judge: the kit does not build"; exit 2; }
  rings="$(LIST=1 docs "$WORK/kit.yaml" "" | awk '$2 != "foundation" && $2 != "provider" && $2 != "-" {print $2}' | sort -u)"
  if [ -z "$rings" ]; then
    echo "FAIL: no HelmRelease of the kit carries a ring that can be switched off"
    exit 1
  fi
fi
status=0
for ring in $rings; do
  switch "$ROOT/$KIT" "$ring" || status=$?
done
[ "$status" -eq 0 ] && echo "test_ring_switch: every ring switch removes exactly its releases"
exit "$status"
