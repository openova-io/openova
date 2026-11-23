#!/bin/sh
# cutover-copy-functions.sh CHART_DIR - print the image copy of cutover step 03, cut from the
# render of the chart in CHART_DIR: skopeo_fetch (step 03's one pinned skopeo, values skopeo) and
# offlinemirror_skopeo_copy, offlinemirror_copy and offlinemirror_put_list (the resolver 03a; a
# list is copied entry by entry, then its own bytes, so every digest of the origin is kept).
#
# A copy outside the Sovereign that writes images a node pulls sources this output instead of a
# copy of its own: .github/workflows/warmup-bastion-harbor.yaml (the parent's hosted project
# openova-io, which every node pulls first-party images from until the cutover). Its own
# `skopeo copy` at the default --multi-arch system wrote the linux/amd64 child of each list under
# the tag; the Sovereign's registry holds the list (step 03), so one tag had two digests (the gate
# records of 23 Nov 2026). tests/hermetic/provider-package-copy runs the warm-up's copy with this
# output against two registries.
#
# Needs helm and awk. Exit 1 when the render lacks a function (nothing is printed then).
set -eu
chart="${1:?usage: cutover-copy-functions.sh CHART_DIR}"
t=$(mktemp -d)
trap 'rm -rf "${t}"' EXIT
for s in 03-harbor-prewarm-job 03a-offline-mirror-resolver; do
  helm template ssc "${chart}" --set sovereign.fqdn=sov.example.test --show-only "templates/${s}.yaml" >"${t}/${s}.yaml" 2>"${t}/err" \
    || { cat "${t}/err" >&2; echo "cutover-copy-functions: helm template of ${s} failed" >&2; exit 1; }
done
# key FILE KEY: the block scalar of the data KEY of a rendered ConfigMap.
key() { awk -v k="  $2: |" '$0 == k {f = 1; next} f && /^ ? ?[^ ]/ {exit} f {sub(/^    /, ""); print}' "$1"; }
# cut FILE NAME: the shell function NAME of FILE, from "NAME() {" to its closing "}".
cut_fn() { awk -v s="$2() {" '$0 == s {f = 1} f {print} f && $0 == "}" {exit}' "$1"; }
key "${t}/03-harbor-prewarm-job.yaml" run.sh >"${t}/run.sh"
key "${t}/03a-offline-mirror-resolver.yaml" resolver.sh >"${t}/resolver.sh"
cut_fn "${t}/run.sh" skopeo_fetch >"${t}/out"
for f in offlinemirror_skopeo_copy offlinemirror_copy offlinemirror_put_list; do cut_fn "${t}/resolver.sh" "${f}" >>"${t}/out"; done
for f in skopeo_fetch offlinemirror_skopeo_copy offlinemirror_copy offlinemirror_put_list; do
  grep -q "^${f}() {$" "${t}/out" || { echo "cutover-copy-functions: the render of ${chart} has no ${f}" >&2; exit 1; }
done
cat "${t}/out"
