#!/usr/bin/env bash
# daytwo-image-leg.sh — #5640 BEHAVIOURAL gate for the Day-2 reconciler's image leg.
#
# Case 75 in cutover-contract.sh asserts the leg's SHAPE (which functions and env
# render). That is necessary but not sufficient: a render can carry every token
# and still compute the wrong answer. This gate EXECUTES the rendered shell.
#
# It extracts the reconciler's `args[0]` script from the real chart render,
# truncates it before the infinite loop, points the two ConfigMap mount paths at
# the rendered ConfigMap contents, stubs `kubectl` (workload enumeration) and
# `curl` (local Harbor manifest probe), then runs `reconcile_images_once` and
# asserts the OUTCOME.
#
# The scenarios are the hw292 defect and its controls:
#   S1  one declared image present in local Harbor, one 404 → exactly the 404 is
#       recorded, named, and written to the manifest. (The 200 leg is the CONTROL
#       that proves the probe is not simply reporting everything missing.)
#   S2  every probe 404s → the miss count tracks the input, so S1's single miss
#       was a discrimination, not a constant.
#   S3  the enumeration returns nothing → status is `enumeration-empty` and the
#       manifest must NOT claim ALL_PINS_PRESENT. A guard that treats a broken
#       read as an all-clear is the exact shape that let #5640 stay invisible.
#   S4  detect mode never dials an upstream registry: the stub records every
#       host curl/skopeo is asked for, and only the local Harbor may appear.
#
# Usage: bash tests/daytwo-image-leg.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")/.." && pwd)}"
HELM="${HELM_BIN:-helm}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"

fail() { echo "FAIL: $*" >&2; exit 1; }

command -v yq >/dev/null 2>&1 || { echo "SKIP: yq not installed — the behavioural gate needs it to extract args[0]"; exit 0; }
command -v jq >/dev/null 2>&1 || { echo "SKIP: jq not installed — the rendered script parses kubectl JSON with it"; exit 0; }

# ── Render the reconciler + the two ConfigMaps it mounts ──────────────────────
"$HELM" template dt . --show-only templates/12-daytwo-harbor-pin-reconciler.yaml > "$TMP/dt.yaml"
"$HELM" template dt . --show-only templates/03a-offline-mirror-resolver.yaml > "$TMP/03a.yaml"

yq -r '.spec.template.spec.containers[0].args[0]' "$TMP/dt.yaml" > "$TMP/script.raw.sh"
[ -s "$TMP/script.raw.sh" ] || fail "could not extract the reconciler args[0] script from the render"
yq -r '.data."resolver.sh"' "$TMP/03a.yaml" > "$TMP/resolver.sh"
[ -s "$TMP/resolver.sh" ] || fail "could not extract resolver.sh from the 03a ConfigMap render"

# Vacuity control on the extraction itself — an empty/short script would make
# every assertion below pass on nothing.
raw_lines=$(wc -l < "$TMP/script.raw.sh")
[ "${raw_lines}" -ge 200 ] || fail "extracted script is only ${raw_lines} lines — the render did not carry the loop body"
grep -q 'reconcile_images_once()' "$TMP/script.raw.sh" || fail "extracted script has no reconcile_images_once() — nothing to execute"

# Truncate before the infinite loop so the harness terminates, and repoint the
# two hardcoded ConfigMap mount paths at the extracted copies.
awk '/^[[:space:]]*# The Day-2 loop\./{exit} {print}' "$TMP/script.raw.sh" \
  | sed -e "s#/offline-mirror/resolver.sh#${TMP}/resolver.sh#g" \
        -e "s#/pin-extractor/extract-pins.sh#${TMP}/extract-pins.sh#g" \
  > "$TMP/script.sh"
grep -q 'reconcile_images_once()' "$TMP/script.sh" || fail "truncation removed reconcile_images_once()"
if grep -q 'while : ; do' "$TMP/script.sh"; then fail "truncation did not remove the infinite loop"; fi
: > "$TMP/extract-pins.sh"

# ── Env: taken from the REAL render so the harness cannot drift from the chart ─
yq -r '.spec.template.spec.containers[0].env[] | select(has("value")) | .name + "=" + (.value|tostring)' "$TMP/dt.yaml" > "$TMP/env.txt"
grep -q '^HOST_PROJECT_MAP=' "$TMP/env.txt" || fail "render does not project HOST_PROJECT_MAP"

# ── Stubs ─────────────────────────────────────────────────────────────────────
mkdir -p "$TMP/bin"

# kubectl: workload enumeration + the manifest ConfigMap write.
cat > "$TMP/bin/kubectl" <<'KSTUB'
#!/usr/bin/env bash
args="$*"
case "$args" in
  *"get deployments,statefulsets,daemonsets"*) cat "${STUB_DECLARED}" ;;
  *"get pods -A"*)                             echo '{"items":[]}' ;;
  *"get cronjobs"*)                            echo '{"items":[]}' ;;
  *"get jobs"*)                                echo '{"items":[]}' ;;
  *"create configmap"*)                        echo 'apiVersion: v1'; echo 'kind: ConfigMap' ;;
  *"apply -f -"*)                              cat > /dev/null ;;
  *)                                           exit 0 ;;
esac
KSTUB
chmod +x "$TMP/bin/kubectl"

# curl: the local-Harbor /v2 manifest probe. Records every host it is asked for
# (the S4 egress witness) and answers from ${STUB_PRESENT_RE}.
cat > "$TMP/bin/curl" <<'CSTUB'
#!/usr/bin/env bash
url=""
for a in "$@"; do case "$a" in http://*|https://*) url="$a" ;; esac; done
[ -n "$url" ] && printf '%s\n' "$url" >> "${STUB_CURL_LOG}"
if [ -n "${STUB_PRESENT_RE:-}" ] && printf '%s' "$url" | grep -Eq "${STUB_PRESENT_RE}"; then
  echo "200"
else
  echo "404"
fi
CSTUB
chmod +x "$TMP/bin/curl"

# skopeo / nslookup: presence-only witnesses. detect mode must never invoke skopeo.
cat > "$TMP/bin/skopeo" <<'SSTUB'
#!/usr/bin/env bash
echo "SKOPEO_INVOKED $*" >> "${STUB_CURL_LOG}"
exit 1
SSTUB
chmod +x "$TMP/bin/skopeo"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/nslookup"; chmod +x "$TMP/bin/nslookup"

# Two declared images: catalyst-ui on the tag deployed at cutover (present) and
# on the tag deploy-bot pinned afterwards (missing) — the hw292 pair verbatim.
cat > "$TMP/declared.json" <<'JSON'
{"items":[
 {"spec":{"template":{"spec":{"containers":[
   {"image":"ghcr.io/openova-io/openova/catalyst-ui:fad88bd"},
   {"image":"ghcr.io/openova-io/openova/catalyst-ui:d674a94"}]}}}}
]}
JSON
cat > "$TMP/declared-empty.json" <<'JSON'
{"items":[]}
JSON

run_leg() {   # run_leg <declared-json> <present-regex> -> writes $TMP/out.txt
  : > "$TMP/curl.log"
  {
    echo 'set +e'
    while IFS= read -r line; do printf 'export %q\n' "$line"; done < "$TMP/env.txt"
    # Secret-backed env has no .value in the render; supply harness values.
    echo 'export HARBOR_USERNAME=admin HARBOR_PASSWORD=stub GHCR_DOCKERCONFIG='
    cat "$TMP/script.sh"
    echo 'reconcile_images_once'
    echo 'echo "RESULT status=${IMAGE_STATUS} refs=${IMAGE_REFS} present=${IMAGE_PRESENT} warmed=${IMAGE_WARMED} missing=${IMAGE_MISS}"'
    echo 'echo "---MISSING---"; cat "${MISSING_IMAGES_FILE}" 2>/dev/null'
    echo 'echo "---MANIFEST---"; publish_manifest; cat "${WORK}/manifest.txt"'
  } > "$TMP/harness.sh"
  STUB_DECLARED="$1" STUB_PRESENT_RE="$2" STUB_CURL_LOG="$TMP/curl.log" \
    PATH="$TMP/bin:$PATH" sh "$TMP/harness.sh" > "$TMP/out.txt" 2>&1
}

# ── S1: one present, one missing ──────────────────────────────────────────────
echo "[daytwo-image-leg] S1: hw292 pair — catalyst-ui:fad88bd present (CONTROL), catalyst-ui:d674a94 404"
run_leg "$TMP/declared.json" 'manifests/fad88bd$'
grep -q 'RESULT status=ok refs=4 present=2 warmed=0 missing=2' "$TMP/out.txt" \
  || { sed 's/^/    /' "$TMP/out.txt"; fail "S1 did not discriminate present from missing (each ghcr.io/openova-io ref resolves to TWO local paths: proxy-ghcr/... and the host-drop openova-io/...)"; }
grep -q 'openova-io/openova/catalyst-ui:d674a94' "$TMP/out.txt" \
  || fail "S1 did not NAME the missing reference — the operator gets no actionable output"
grep -q 'openova-io/openova/catalyst-ui:fad88bd' "$TMP/out.txt" \
  && fail "S1 reported the PRESENT control tag as missing — the probe is answering constant-404"
grep -q 'ALL_PINS_PRESENT' "$TMP/out.txt" \
  && fail "S1 manifest claims ALL_PINS_PRESENT while an image is missing"
echo "  PASS (missing d674a94 named on both local paths; present fad88bd control untouched; no false all-clear)"

# ── S2: everything missing ────────────────────────────────────────────────────
echo "[daytwo-image-leg] S2: no tag present in local Harbor — the count must track the input, not a constant"
run_leg "$TMP/declared.json" 'MATCHES_NOTHING_XyZ'
grep -q 'RESULT status=ok refs=4 present=0 warmed=0 missing=4' "$TMP/out.txt" \
  || { sed 's/^/    /' "$TMP/out.txt"; fail "S2 miss count did not track the input — S1's result was not a discrimination"; }
echo "  PASS (4/4 missing — the S1 single-miss was a real comparison)"

# ── S3: broken enumeration must NOT read as clean ─────────────────────────────
echo "[daytwo-image-leg] S3: zero-ref enumeration is a broken read, never an all-clear"
run_leg "$TMP/declared-empty.json" 'manifests/fad88bd$'
grep -q 'RESULT status=enumeration-empty' "$TMP/out.txt" \
  || { sed 's/^/    /' "$TMP/out.txt"; fail "S3 did not flag the empty enumeration"; }
grep -q 'ALL_PINS_PRESENT' "$TMP/out.txt" \
  && fail "S3 published ALL_PINS_PRESENT on a ZERO-ref enumeration — a broken read laundered into an all-clear (#5640)"
echo "  PASS (status=enumeration-empty, no all-clear published)"

# ── S4: detect mode reaches only the local Harbor ─────────────────────────────
echo "[daytwo-image-leg] S4: detect mode must not dial any upstream registry"
run_leg "$TMP/declared.json" 'MATCHES_NOTHING_XyZ'
if grep -q 'SKOPEO_INVOKED' "$TMP/curl.log"; then
  fail "S4 detect mode invoked skopeo — the zero-external-egress contract is broken (#5640 Refs #5265)"
fi
offhost=$(grep -Ev '^https?://registry\.' "$TMP/curl.log" | grep -E '^https?://' || true)
if [ -n "${offhost}" ]; then
  printf '%s\n' "${offhost}" | sed 's/^/    /'
  fail "S4 detect mode dialled a non-local host — every URL must be the local Harbor"
fi
probes=$(grep -c '^https\?://' "$TMP/curl.log" || true)
[ "${probes}" -ge 4 ] || fail "S4 vacuity control — only ${probes} URL(s) recorded; the leg did not actually probe, so 'no upstream' is meaningless"
echo "  PASS (${probes} probes, all against the local Harbor; skopeo never invoked)"

echo "[daytwo-image-leg] All gates green."
