#!/usr/bin/env bash
# #5443 — catalog chart-set guardrail.
#
# Proves the three halves of the marketplace-coverage fix, against the RENDERED
# chart (never the source template), so a regression in either the library or
# its wiring into step-03 fails at PR time:
#
#   A. templates/03c-catalog-chart-set.yaml renders a `cutover-catalog-chart-set`
#      ConfigMap whose `catalog_chart_set` derives the right chart:version rows
#      — and the right HARD/SOFT split — from a live-shaped Blueprint CR list.
#   B. `catalog_guard_missing` reports exactly the contractual entries absent
#      from the local registry (the probe that found #5443 on hw290).
#   C. `chart_declared_images` enumerates a chart's images by RENDERING it, which
#      is the only enumeration available for a chart nobody installed (#5442).
#   D. Step-03 actually WIRES all of it: mount, source, mirror-set union, guard,
#      image pass — plus the RBAC that lets the Job read Blueprint CRs at all.
#
# Run standalone or via tests/cutover-contract.sh Case 72.
# Usage: bash tests/catalog-chart-set.sh [CHART_DIR]

set -euo pipefail

# Resolve to an ABSOLUTE path: `helm template` on a bare relative path that no
# longer exists relative to the caller's cwd is read as a repo/chart reference
# ("Error: repo <x> not found"), which would look like a chart bug.
CHART_DIR="$(cd "${1:-$(dirname "${BASH_SOURCE[0]:-$0}")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail=0

note() { echo "  $*"; }
bad()  { printf 'FAIL: %b\n' "$*" >&2; fail=1; }

# ── Render + extract the library exactly as the Job mounts it ────────────────
helm template smoke "$CHART_DIR" > "$TMP/render.yaml"

python3 - "$TMP/render.yaml" "$TMP" <<'PY'
import sys, yaml
render, out = sys.argv[1], sys.argv[2]
lib = pod = None
for d in yaml.safe_load_all(open(render)):
    if not d or d.get("kind") != "ConfigMap":
        continue
    n = d["metadata"]["name"]
    if n == "cutover-catalog-chart-set":
        lib = d["data"].get("catalog-set.sh")
    if n == "cutover-step-03-harbor-prewarm":
        spec = yaml.safe_load(d["data"]["podSpec"])
        pod = (d["data"]["podSpec"], spec["containers"][0]["args"][0])
if lib:
    open(out + "/catalog-set.sh", "w").write(lib)
if pod:
    open(out + "/podspec.yaml", "w").write(pod[0])
    open(out + "/prewarm.sh", "w").write(pod[1])
PY

if [ ! -f "$TMP/catalog-set.sh" ]; then
  echo "FAIL: no ConfigMap/cutover-catalog-chart-set in the render — the catalog chart-set extractor (03c) is missing, so nothing enumerates what the marketplace OFFERS and the cutover pivots openova-catalog at a registry stocked only from what is INSTALLED (#5443)" >&2
  exit 1
fi
if [ ! -f "$TMP/prewarm.sh" ]; then
  echo "FAIL: could not extract the step-03 harbor-prewarm script from the render" >&2
  exit 1
fi

# shellcheck source=/dev/null
. "$TMP/catalog-set.sh"

for fn in catalog_chart_set catalog_guard_missing chart_declared_images; do
  if ! command -v "$fn" >/dev/null 2>&1; then
    echo "FAIL: ${fn} not defined by the rendered library (#5443)" >&2
    exit 1
  fi
done

# ── Case A: catalog_chart_set over a live-shaped Blueprint CR list ───────────
echo "[catalog-chart-set] Case A: chart:version rows + HARD/SOFT split from the live Blueprint CRs"
cat > "$TMP/blueprints.json" <<'JSON'
{"apiVersion":"v1","kind":"List","items":[
 {"metadata":{"name":"bp-wordpress-tenant"},
  "spec":{"version":"0.4.22","visibility":"listed",
          "manifests":{"chart":"bp-wordpress-tenant"},
          "source":{"url":"oci://ghcr.io/openova-io","chart":"bp-wordpress-tenant","version":"0.4.22"}}},
 {"metadata":{"name":"bp-agenity"},
  "spec":{"version":"0.5.20","visibility":"listed",
          "manifests":{"chart":"bp-agenity"},
          "source":{"url":"oci://ghcr.io/openova-io","chart":"bp-agenity","version":"0.5.19"}}},
 {"metadata":{"name":"bp-alloy"},
  "spec":{"version":"1.0.3","visibility":"listed",
          "manifests":{"chart":"bp-alloy"},
          "source":{"url":"oci://ghcr.io/openova-io","chart":"bp-alloy","version":"1.0.2"}}},
 {"metadata":{"name":"bp-cilium"},
  "spec":{"version":"1.17.1","visibility":"unlisted",
          "manifests":{"chart":"bp-cilium"},
          "source":{"url":"oci://ghcr.io/openova-io","chart":"bp-cilium","version":"1.17.1"}}},
 {"metadata":{"name":"bp-nomanifests"},
  "spec":{"version":"2.0.0","visibility":"listed",
          "source":{"url":"oci://ghcr.io/openova-io","version":"2.0.0"}}},
 {"metadata":{"name":"bp-foreign"},
  "spec":{"version":"9.9.9","visibility":"listed",
          "manifests":{"chart":"bp-foreign"},
          "source":{"url":"oci://ghcr.io/somebodyelse","chart":"bp-foreign","version":"9.9.9"}}},
 {"metadata":{"name":"bp-range"},
  "spec":{"version":">=1.0.0","visibility":"listed",
          "manifests":{"chart":"bp-range"},
          "source":{"url":"oci://ghcr.io/openova-io","chart":"bp-range","version":">=1.0.0"}}}
]}
JSON

catalog_chart_set "$TMP/blueprints.json" "oci://ghcr.io/openova-io" "listed" \
  > "$TMP/set.tsv" 2> "$TMP/set.err" || true

expect_row() { # chart version hard
  if ! grep -qP "^$1\t$2\t$3\t" "$TMP/set.tsv"; then
    bad "expected row ${1}:${2} hard=${3}; got:\n$(cat "$TMP/set.tsv")"
  fi
}
absent_chart() {
  if cut -f1 "$TMP/set.tsv" | grep -qx "$1"; then
    bad "${1} must NOT be in the mirror set ($2)"
  fi
}

# spec.version of a `listed` entry is the coordinate the install path requests
# (blueprintChart() -> blueprintRef.version -> HR chart.spec.version) => HARD.
expect_row bp-wordpress-tenant 0.4.22 1
expect_row bp-agenity          0.5.20 1
expect_row bp-alloy            1.0.3  1
# spec.source.version is read by NO controller — mirrored (drift insurance) but
# never contractual. This is the #5444 shape observed live on hw290.
expect_row bp-agenity          0.5.19 0
expect_row bp-alloy            1.0.2  0
# unlisted: mirrored best-effort, never fatal.
expect_row bp-cilium           1.17.1 0
# manifests.chart absent -> metadata.name, per the controller's firstNonEmpty().
expect_row bp-nomanifests      2.0.0  1
# Outside the openova-io mirror contract, and a semver range is not an OCI tag.
absent_chart bp-foreign "spec.source.url is a foreign registry"
absent_chart bp-range   "a semver range is not a concrete OCI tag"
for token in bp-foreign bp-range; do
  grep -q "$token" "$TMP/set.err" || bad "${token} was dropped SILENTLY — every skip must be named on stderr"
done
[ "$fail" -eq 0 ] && note "PASS ($(wc -l < "$TMP/set.tsv") rows; hard=$(awk -F'\t' '$3==1' "$TMP/set.tsv" | wc -l), soft=$(awk -F'\t' '$3==0' "$TMP/set.tsv" | wc -l); foreign + range skipped and named)"

# ── Case B: catalog_guard_missing ────────────────────────────────────────────
echo "[catalog-chart-set] Case B: the guard reports exactly the contractual entries the registry cannot serve"
awk -F'\t' '$3==1 {print $1"\t"$2}' "$TMP/set.tsv" > "$TMP/hard.tsv"
# hw290's actual shape: some contractual charts resolve, the rest 404.
printf 'bp-wordpress-tenant:0.4.22\nbp-nomanifests:2.0.0\n' > "$TMP/present.txt"
catalog_guard_missing "$TMP/hard.tsv" "$TMP/present.txt" > "$TMP/missing.txt"
sort -o "$TMP/missing.txt" "$TMP/missing.txt"
printf 'bp-agenity:0.5.20\nbp-alloy:1.0.3\n' | sort > "$TMP/missing.want"
if ! diff -u "$TMP/missing.want" "$TMP/missing.txt" >/dev/null; then
  bad "guard output wrong;\nwant:\n$(cat "$TMP/missing.want")\ngot:\n$(cat "$TMP/missing.txt")"
fi
# Nothing present => every contractual entry is missing (never a silent pass).
: > "$TMP/present.empty"
n_all=$(catalog_guard_missing "$TMP/hard.tsv" "$TMP/present.empty" | wc -l)
n_hard=$(wc -l < "$TMP/hard.tsv")
[ "$n_all" -eq "$n_hard" ] || bad "empty registry must yield ${n_hard} missing entries, got ${n_all}"
# Everything present => empty output (no false positives).
cut -f1,2 "$TMP/hard.tsv" | tr '\t' ':' > "$TMP/present.all"
n_none=$(catalog_guard_missing "$TMP/hard.tsv" "$TMP/present.all" | wc -l)
[ "$n_none" -eq 0 ] || bad "a fully-stocked registry must yield zero missing entries, got ${n_none}"
[ "$fail" -eq 0 ] && note "PASS (partial => the exact 2 missing; empty => all ${n_hard}; complete => none)"

# ── Case C: chart_declared_images renders a chart to enumerate its images ────
echo "[catalog-chart-set] Case C: images come from the CHART (the live-cluster walk cannot see an uninstalled chart, #5442)"
FIX="$TMP/fixture"
mkdir -p "$FIX/templates"
cat > "$FIX/Chart.yaml" <<'YAML'
apiVersion: v2
name: bp-fixture
version: 0.1.0
YAML
cat > "$FIX/values.yaml" <<'YAML'
image:
  repository: ghcr.io/openova-io/openova/fixture-app
  tag: "1.2.3"
missing:
  tag: ""
YAML
cat > "$FIX/templates/deploy.yaml" <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: fixture
spec:
  template:
    spec:
      initContainers:
        - name: init
          image: docker.io/library/busybox:1.36
      containers:
        - name: app
          image: "{{ .Values.image.repository }}:{{ .Values.image.tag }}"
        - name: side
          image: quay.io/cilium/cilium:v1.17.1
        - name: floating
          image: ghcr.io/openova-io/openova/no-tag-here
        # An overlay-supplied tag renders EMPTY; mirroring it would fail the
        # copy and, under chartImages.fatal, fail the cutover on a ref nothing
        # will ever pull.
        - name: emptytag
          image: "ghcr.io/openova-io/openova/overlay-supplied:{{ .Values.missing.tag }}"
        # FLOW style: `helm template` emits template text verbatim, so a chart
        # authored this way never puts `image:` at the start of a line.
        - {name: flow, image: registry.k8s.io/pause:3.10}
---
apiVersion: postgresql.cnpg.io/v1
kind: Cluster
metadata:
  name: fixture-db
spec:
  imageName: ignored-not-an-image-key
  image: ghcr.io/cloudnative-pg/postgresql:16.4
YAML
helm package "$FIX" -d "$TMP" >/dev/null
TGZ="$TMP/bp-fixture-0.1.0.tgz"
CATALOG_SET_TMPDIR="$TMP" chart_declared_images "$TGZ" > "$TMP/imgs.txt" 2> "$TMP/imgs.err" || true
for want in \
  "ghcr.io/openova-io/openova/fixture-app:1.2.3" \
  "docker.io/library/busybox:1.36" \
  "quay.io/cilium/cilium:v1.17.1" \
  "registry.k8s.io/pause:3.10" \
  "ghcr.io/cloudnative-pg/postgresql:16.4"; do
  grep -qx "$want" "$TMP/imgs.txt" || bad "chart render did not yield ${want}; got:\n$(cat "$TMP/imgs.txt")"
done
if grep -q 'no-tag-here' "$TMP/imgs.txt"; then
  bad "an untagged ref must not be mirrored (an implicit :latest is not a deterministic target)"
fi
grep -q 'no-tag-here' "$TMP/imgs.err" || bad "the untagged ref was dropped SILENTLY — it must be named on stderr"
if grep -q 'overlay-supplied' "$TMP/imgs.txt"; then
  bad "an EMPTY-tag ref must not be mirrored — under chartImages.fatal it would fail the cutover on a ref nothing pulls (#5442)"
fi
grep -q 'overlay-supplied' "$TMP/imgs.err" || bad "the empty-tag ref was dropped SILENTLY — it must be named on stderr"
# A chart that cannot render must say so, not pretend it has no images.
printf 'not a chart' > "$TMP/broken.tgz"
CATALOG_SET_TMPDIR="$TMP" chart_declared_images "$TMP/broken.tgz" > "$TMP/broken.out" 2> "$TMP/broken.err" || true
[ ! -s "$TMP/broken.out" ] || bad "a broken chart must yield no images"
grep -qi 'WARN' "$TMP/broken.err" || bad "a chart that cannot be rendered must WARN, not fail silently"
[ "$fail" -eq 0 ] && note "PASS ($(wc -l < "$TMP/imgs.txt") images incl. initContainer + CR field; untagged skipped + named; unrenderable chart WARNs)"

# ── Case D: step-03 + RBAC actually wire the library ─────────────────────────
echo "[catalog-chart-set] Case D: step-03 mounts, unions, guards, and warms images from the PINNED CHARTS"
grep -q '/catalog-set/catalog-set.sh' "$TMP/prewarm.sh" \
  || bad "step-03 never sources the catalog chart-set library"
grep -q 'catalog_chart_set ' "$TMP/prewarm.sh" \
  || bad "step-03 never calls catalog_chart_set — the mirror set is still derived only from what is INSTALLED (#5443)"
grep -q 'catalog_guard_missing ' "$TMP/prewarm.sh" \
  || bad "step-03 never calls catalog_guard_missing — nothing asserts the marketplace's advertised set against the pivoted registry (#5443)"
grep -q 'chart_declared_images ' "$TMP/prewarm.sh" \
  || bad "step-03 never calls chart_declared_images — image tags would still come from the LIVE CLUSTER while chart versions come from the mirror pins (#5442)"
grep -q 'harbor_durable_digest' "$TMP/prewarm.sh" \
  || bad "the guard must probe Harbor's DURABLE artifact API (#5030), not a pull-through-able registry read"
grep -q 'chart_hard' "$TMP/prewarm.sh" \
  || bad "step-03 has no HARD/SOFT split — either the widened mirror set makes every unlisted stale pin fatal, or the contractual set lost its teeth (#5443)"
grep -q 'name: catalog-chart-set' "$TMP/podspec.yaml" \
  || bad "the step-03 podSpec does not mount the cutover-catalog-chart-set ConfigMap"
# #5442: the image enumeration must iterate the WHOLE contractual chart set
# (bootstrap-kit pins + live-HR union + contractual catalog), not the catalog
# slice alone — the hw290 control-plane outage was on bootstrap-kit charts.
grep -q 'done < "${chart_hard}"' "$TMP/prewarm.sh" \
  || bad "the chart-derived image pass does not iterate the full pinned chart set — the bootstrap-kit leg of #5442 (catalyst-api/ui/organization-controller) stays uncovered"
grep -q 'PREWARM_CHART_IMAGES' "$TMP/prewarm.sh" \
  || bad "step-03 has no chart-derived image phase (#5442)"
# The union property: the live-cluster walk must NOT have been replaced.
grep -q 'openova_images=' "$TMP/prewarm.sh" \
  || bad "the live-cluster image walk was removed — the chart-derived set must UNION with it, never replace it (a default-values render cannot see a conditional image the runtime walk does)"
# NB: two greps in a pipeline would SIGPIPE the writer under `set -o pipefail`
# the moment the reader exits early (#5370, #5406) — stage through a file.
grep -A3 'apiGroups: \["catalyst.openova.io"\]' "$TMP/render.yaml" > "$TMP/rbac.txt" || true
if ! grep -q 'resources: \["blueprints"\]' "$TMP/rbac.txt"; then
  bad "the runner ClusterRole cannot read Blueprint CRs — the enumeration would 403 and the step would FATAL on an empty catalog"
fi
[ "$fail" -eq 0 ] && note "PASS (mount + source + union + guard + image pass + RBAC all present)"

if [ "$fail" -ne 0 ]; then
  echo "[catalog-chart-set] FAILED" >&2
  exit 1
fi
echo "[catalog-chart-set] All gates green."
