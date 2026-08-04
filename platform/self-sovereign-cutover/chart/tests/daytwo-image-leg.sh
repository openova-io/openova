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
#   S4  no open delivery request → never dials an upstream registry: the stub
#       records every host curl/skopeo is asked for, and only the local Harbor
#       may appear.
#
# #5640 round 2 — the operator-gated delivery path and its fail-closed surface:
#   S5  mode=request with NO request ConfigMap → detect-equivalent. Zero egress,
#       and the missing set is still reported. This is the DEFAULT posture, so
#       it is the one that must be proven, not assumed.
#   S6  mode=request with an in-scope request → the copy fires for the named ref
#       and NOT for the ref the request omits. The omitted ref is the CONTROL:
#       without it, "delivery happened" would not prove delivery was BOUNDED.
#   S7  a step-07-PIVOTED ref (registry.<fqdn>/openova-io/...) — the only shape
#       the hw292 defect appears in post-cutover — must be copied FROM ghcr.io,
#       never from the local registry. Before the inverse map this was a
#       local→local self-copy that could not deliver anything, ever.
#   S8  a request whose id was already consumed → zero egress. One-shot means
#       the window closes; re-applying the same request must not re-open it.
#   S9  the fail-closed readiness verdict: NOT-READY with a missing pin,
#       NOT-READY on a broken enumeration, READY only when the cycle MEASURED
#       everything present. Both directions, or it is not known to be a gate.
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

# kubectl: workload enumeration, the delivery-request read, the consumed-id read
# and the ConfigMap writes.
cat > "$TMP/bin/kubectl" <<'KSTUB'
#!/usr/bin/env bash
args="$*"
case "$args" in
  *"get deployments,statefulsets,daemonsets"*) cat "${STUB_DECLARED}" ;;
  *"get pods -A"*)                             echo '{"items":[]}' ;;
  *"get cronjobs"*)                            echo '{"items":[]}' ;;
  *"get jobs"*)                                echo '{"items":[]}' ;;
  # The operator's delivery request. Absent file => the ConfigMap does not
  # exist, which is the DEFAULT posture (no request filed).
  *"get configmap self-sovereign-cutover-daytwo-request"*)
      [ -s "${STUB_REQUEST_JSON:-/nonexistent}" ] || exit 1
      cat "${STUB_REQUEST_JSON}" ;;
  # Durable one-shot state + prior audit trail.
  *"lastConsumedRequestId"*)                   printf '%s' "${STUB_CONSUMED_ID:-}" ;;
  *"get configmap self-sovereign-cutover-daytwo-delivery"*) printf '' ;;
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

# skopeo / nslookup: witnesses. A mode with no open delivery window must never
# invoke skopeo at all; when it IS invoked, the recorded argv is what proves
# WHICH source the copy was pointed at (the S7 assertion).
cat > "$TMP/bin/skopeo" <<'SSTUB'
#!/usr/bin/env bash
echo "SKOPEO_INVOKED $*" >> "${STUB_CURL_LOG}"
exit "${STUB_SKOPEO_RC:-1}"
SSTUB
chmod +x "$TMP/bin/skopeo"
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/nslookup"; chmod +x "$TMP/bin/nslookup"
# The rendered script sleeps between probe retries and copy attempts. Those
# sleeps are real behaviour but pure latency here, and 12 scenarios' worth of
# them turns a 20-second gate into a multi-minute one nobody runs. Stub it.
printf '#!/usr/bin/env bash\nexit 0\n' > "$TMP/bin/sleep"; chmod +x "$TMP/bin/sleep"

# The local registry host is a RENDER value (registry.<fqdn>); the pivoted-ref
# scenarios must use the SAME one the resolver was given, or every pivoted ref
# reads as an UNMAPPED foreign host and the inverse map is never exercised.
LOCALREG=$(sed -n 's/^LOCAL_REGISTRY_HOST=//p' "$TMP/env.txt")
[ -n "${LOCALREG}" ] || fail "render does not project LOCAL_REGISTRY_HOST — the pivoted-ref scenarios cannot be built"

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

RUN_EXTRA_ENV=""       # shell lines appended AFTER the render env (per-scenario)
STUB_REQUEST_JSON=""   # the operator's delivery request, or "" for none
STUB_CONSUMED_ID=""    # durable one-shot state
STUB_SKOPEO_RC=1       # copy outcome the skopeo stub reports

run_leg() {   # run_leg <declared-json> <present-regex> -> writes $TMP/out.txt
  : > "$TMP/curl.log"
  {
    echo 'set +e'
    while IFS= read -r line; do printf 'export %q\n' "$line"; done < "$TMP/env.txt"
    # Secret-backed env has no .value in the render; supply harness values.
    echo 'export HARBOR_USERNAME=admin HARBOR_PASSWORD=stub GHCR_DOCKERCONFIG='
    [ -n "${RUN_EXTRA_ENV}" ] && printf '%s\n' "${RUN_EXTRA_ENV}"
    cat "$TMP/script.sh"
    # The real loop runs these in this order inside reconcile_once; the harness
    # reproduces that order so request handling, delivery and the readiness
    # verdict are exercised exactly as they run in the cluster.
    echo 'daytwo_load_request'
    echo 'reconcile_images_once'
    echo 'echo "RESULT status=${IMAGE_STATUS} refs=${IMAGE_REFS} present=${IMAGE_PRESENT} warmed=${IMAGE_WARMED} missing=${IMAGE_MISS}"'
    echo 'echo "---MISSING---"; cat "${MISSING_IMAGES_FILE}" 2>/dev/null'
    echo 'echo "---MANIFEST---"; publish_manifest; cat "${WORK}/manifest.txt"'
    echo 'daytwo_consume_request'
    echo 'publish_health'
    echo 'echo "---HEALTH---"; cat "${HEALTH_FILE}"'
    echo 'echo "---AUDIT---"; cat "${WORK}/audit-all.log" 2>/dev/null'
    echo 'exit 0'
  } > "$TMP/harness.sh"
  STUB_DECLARED="$1" STUB_PRESENT_RE="$2" STUB_CURL_LOG="$TMP/curl.log" \
    STUB_REQUEST_JSON="${STUB_REQUEST_JSON}" STUB_CONSUMED_ID="${STUB_CONSUMED_ID}" \
    STUB_SKOPEO_RC="${STUB_SKOPEO_RC}" \
    PATH="$TMP/bin:$PATH" sh "$TMP/harness.sh" > "$TMP/out.txt" 2>&1 || true
}

# request_cm <requestId> <scope-lines> -> writes a request ConfigMap JSON and
# points the stub at it.
request_cm() {
  jq -n --arg id "$1" --arg scope "$2" \
    '{data:{requestId:$id, requestedBy:"ops@customer.example", scope:$scope}}' \
    > "$TMP/request.json"
  STUB_REQUEST_JSON="$TMP/request.json"
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

# ── S4: no open delivery window reaches only the local Harbor ─────────────────
echo "[daytwo-image-leg] S4: with no open delivery request, nothing may dial an upstream registry"
run_leg "$TMP/declared.json" 'MATCHES_NOTHING_XyZ'
if grep -q 'SKOPEO_INVOKED' "$TMP/curl.log"; then
  fail "S4 invoked skopeo with no delivery window open — the zero-external-egress contract is broken (#5640 Refs #5265)"
fi
offhost=$(grep -Ev '^https?://registry\.' "$TMP/curl.log" | grep -E '^https?://' || true)
if [ -n "${offhost}" ]; then
  printf '%s\n' "${offhost}" | sed 's/^/    /'
  fail "S4 dialled a non-local host — every URL must be the local Harbor"
fi
probes=$(grep -c '^https\?://' "$TMP/curl.log" || true)
[ "${probes}" -ge 4 ] || fail "S4 vacuity control — only ${probes} URL(s) recorded; the leg did not actually probe, so 'no upstream' is meaningless"
echo "  PASS (${probes} probes, all against the local Harbor; skopeo never invoked)"

# ── S5: the DEFAULT posture (mode=request, no request filed) is zero-egress ────
# The render already carries the shipped default, so this asserts the posture a
# fresh prov actually gets rather than a mode the test itself selected.
echo "[daytwo-image-leg] S5: shipped default mode with NO delivery request — detect-equivalent"
shipped_mode=$(sed -n 's/^RECONCILE_MODE=//p' "$TMP/env.txt")
[ "${shipped_mode}" = "request" ] || fail "S5 expected the shipped default mode to be request, got '${shipped_mode}' — the rest of the #5640 delivery contract is not what ships"
STUB_REQUEST_JSON=""; STUB_CONSUMED_ID=""
run_leg "$TMP/declared.json" 'MATCHES_NOTHING_XyZ'
grep -q 'no delivery request' "$TMP/out.txt" \
  || { sed 's/^/    /' "$TMP/out.txt"; fail "S5 did not report the absent request — the operator gets no statement of the current posture"; }
grep -q 'SKOPEO_INVOKED' "$TMP/curl.log" \
  && fail "S5 mode=request made an upstream copy with NO request filed — the default posture is not zero-egress (#5640)"
grep -q 'RESULT status=ok refs=4 present=0 warmed=0 missing=4' "$TMP/out.txt" \
  || { sed 's/^/    /' "$TMP/out.txt"; fail "S5 stopped DETECTING while gating delivery — request mode must keep detect's full reporting"; }
echo "  PASS (zero egress, full missing set still reported)"

# ── S6: an in-scope request delivers exactly its scope ────────────────────────
# CONTROL: catalyst-ui:fad88bd is missing too but is NOT named by the request.
# Without that control, "a copy happened" would not prove the window is BOUNDED.
echo "[daytwo-image-leg] S6: an open request delivers ONLY the refs it names"
request_cm "req-2026-08-04-a" "ghcr.io/openova-io/openova/catalyst-ui:d674a94"
STUB_CONSUMED_ID=""
run_leg "$TMP/declared.json" 'MATCHES_NOTHING_XyZ'
grep -q 'delivery window OPEN' "$TMP/out.txt" \
  || { sed 's/^/    /' "$TMP/out.txt"; fail "S6 did not open the delivery window for a valid request"; }
grep -q 'SKOPEO_INVOKED.*d674a94' "$TMP/curl.log" \
  || { sed 's/^/    /' "$TMP/curl.log"; fail "S6 never attempted the copy the request named — the delivery path is inert"; }
grep -q 'SKOPEO_INVOKED.*fad88bd' "$TMP/curl.log" \
  && { sed 's/^/    /' "$TMP/curl.log"; fail "S6 copied a ref the request did NOT name — the window is not scope-bounded, which makes it an always-on mirror with extra steps (#5640)"; }
grep -q 'CONSUMED' "$TMP/out.txt" \
  || fail "S6 did not consume the requestId — the window would stay open every cycle"
grep -q 'REQUEST.*consumed.*req-2026-08-04-a' "$TMP/out.txt" \
  || { sed 's/^/    /' "$TMP/out.txt"; fail "S6 wrote no audit record for the consumed request — delivery is unaudited"; }
echo "  PASS (named ref copied, unnamed control ref untouched, request consumed + audited)"

# ── S7: a step-07 PIVOTED ref must be copied FROM ghcr.io, not from itself ────
# This is the ONLY shape the hw292 defect takes post-cutover, because step-07's
# pod-spec sweep has already rewritten every workload image to the local
# registry. A copy that uses that ref as its SOURCE is a local->local self-copy
# of an artifact the local registry has just 404'd: it can never deliver.
echo "[daytwo-image-leg] S7: a pivoted registry.<fqdn>/... ref is inverse-mapped to its upstream before copying"
jq -n --arg img "${LOCALREG}/openova-io/openova/catalyst-api:e3342f9" \
  '{items:[{spec:{template:{spec:{containers:[{image:$img}]}}}}]}' > "$TMP/declared-pivoted.json"
request_cm "req-2026-08-04-b" "ALL_MISSING"
STUB_CONSUMED_ID=""
run_leg "$TMP/declared-pivoted.json" 'MATCHES_NOTHING_XyZ'
grep -q 'inverse-mapped upstream = ghcr.io/openova-io/openova/catalyst-api:e3342f9' "$TMP/out.txt" \
  || { sed 's/^/    /' "$TMP/out.txt"; fail "S7 did not inverse-map the pivoted ref to its ghcr.io upstream (#5640)"; }
src=$(grep -o 'docker://[^ ]*' "$TMP/curl.log" | head -1 || true)
[ "${src}" = "docker://ghcr.io/openova-io/openova/catalyst-api:e3342f9" ] \
  || { sed 's/^/    /' "$TMP/curl.log"; fail "S7 copy SOURCE was '${src}', not the inverse-mapped ghcr.io upstream — a local->local self-copy can never deliver (#5640)"; }
grep -q "SKOPEO_INVOKED.*docker://${LOCALREG}[^ ]* docker://${LOCALREG}" "$TMP/curl.log" \
  && { sed 's/^/    /' "$TMP/curl.log"; fail "S7 asked skopeo to copy the local registry to ITSELF — the exact no-op #5640 round 2 exists to prevent"; }
echo "  PASS (source inverted to ghcr.io; no self-copy)"

# ── S7b: an un-invertible local ref is REFUSED, not attempted ─────────────────
echo "[daytwo-image-leg] S7b: a local ref the map cannot invert is refused, never attempted"
jq -n --arg img "${LOCALREG}/some-unmapped-project/thing:v1" \
  '{items:[{spec:{template:{spec:{containers:[{image:$img}]}}}}]}' > "$TMP/declared-uninvertible.json"
request_cm "req-2026-08-04-c" "ALL_MISSING"
STUB_CONSUMED_ID=""
run_leg "$TMP/declared-uninvertible.json" 'MATCHES_NOTHING_XyZ'
grep -q 'REFUSED' "$TMP/out.txt" \
  || { sed 's/^/    /' "$TMP/out.txt"; fail "S7b did not refuse an un-invertible local ref — it would self-copy and report a copy failure instead of an unknown upstream"; }
grep -q 'SKOPEO_INVOKED' "$TMP/curl.log" \
  && { sed 's/^/    /' "$TMP/curl.log"; fail "S7b invoked skopeo on a ref whose upstream is unknown (#5640)"; }
echo "  PASS (refused before any copy attempt)"

# ── S8: one-shot — an already-consumed requestId must not re-open the window ──
echo "[daytwo-image-leg] S8: re-applying a consumed requestId delivers nothing"
request_cm "req-2026-08-04-a" "ALL_MISSING"
STUB_CONSUMED_ID="req-2026-08-04-a"
run_leg "$TMP/declared.json" 'MATCHES_NOTHING_XyZ'
grep -q 'already consumed' "$TMP/out.txt" \
  || { sed 's/^/    /' "$TMP/out.txt"; fail "S8 did not recognise the consumed requestId"; }
grep -q 'SKOPEO_INVOKED' "$TMP/curl.log" \
  && fail "S8 re-delivered on a consumed requestId — the window never closes, which is the always-on mirror this mode exists to avoid (#5640)"
echo "  PASS (window stayed closed; zero egress)"

# ── S8b: a request with no requestId is refused (it could never be consumed) ──
echo "[daytwo-image-leg] S8b: a request without a requestId is refused"
jq -n '{data:{scope:"ALL_MISSING"}}' > "$TMP/request.json"
STUB_REQUEST_JSON="$TMP/request.json"; STUB_CONSUMED_ID=""
run_leg "$TMP/declared.json" 'MATCHES_NOTHING_XyZ'
grep -q 'carries no requestId' "$TMP/out.txt" \
  || { sed 's/^/    /' "$TMP/out.txt"; fail "S8b accepted an identity-less request — it would re-fire every cycle forever"; }
grep -q 'SKOPEO_INVOKED' "$TMP/curl.log" \
  && fail "S8b delivered on a request that can never be consumed (#5640)"
echo "  PASS (refused; zero egress)"

# ── S8c: an UNRECOGNISED mode must fail CLOSED ───────────────────────────────
# Found by mutating daytwo_delivery_allowed's default branch to `return 0` and
# watching every gate stay green: the shipped modes all match a named branch, so
# nothing exercised the fallthrough. A typo in daytwoReconciler.mode would have
# silently decided whether the Sovereign egresses. It is a guard only once
# something drives it.
echo "[daytwo-image-leg] S8c: an unrecognised RECONCILE_MODE delivers nothing"
request_cm "req-2026-08-04-d" "ALL_MISSING"
STUB_CONSUMED_ID=""
RUN_EXTRA_ENV='export RECONCILE_MODE=warmm'
run_leg "$TMP/declared.json" 'MATCHES_NOTHING_XyZ'
RUN_EXTRA_ENV=""
grep -q 'SKOPEO_INVOKED' "$TMP/curl.log" \
  && { sed 's/^/    /' "$TMP/curl.log"; fail "S8c an unrecognised mode reached upstream — daytwo_delivery_allowed failed OPEN, so a typo in daytwoReconciler.mode decides whether this Sovereign egresses (#5640)"; }
echo "  PASS (unrecognised mode made zero external egress)"

# ── S9: the fail-closed readiness verdict, in BOTH directions ────────────────
# A guard only ever observed passing is not yet known to be a guard, and one
# only ever observed failing may simply be broken. Both directions, same run.
echo "[daytwo-image-leg] S9: readiness is NOT-READY on a missing pin and READY only when the cycle measured everything present"
STUB_REQUEST_JSON=""; STUB_CONSUMED_ID=""
RUN_EXTRA_ENV='export CHART_LEG_ENABLED=false'   # isolate the image leg's verdict

run_leg "$TMP/declared.json" 'MATCHES_NOTHING_XyZ'
health=$(sed -n '/^---HEALTH---$/,$p' "$TMP/out.txt" | sed -n '2p')
case "${health}" in
  NOTREADY*unpullable-pins=4*) : ;;
  *) sed 's/^/    /' "$TMP/out.txt"; fail "S9 direction A: 4 missing images produced health '${health}', expected NOTREADY with the measured count — a Sovereign holding unpullable pins would stay silently READY (#5640)" ;;
esac
echo "  A: 4 missing -> '${health}'"

run_leg "$TMP/declared.json" 'manifests/'      # every probe answers present
health=$(sed -n '/^---HEALTH---$/,$p' "$TMP/out.txt" | sed -n '2p')
[ "${health}" = "READY" ] \
  || { sed 's/^/    /' "$TMP/out.txt"; fail "S9 direction B: with every image present the verdict was '${health}', not READY — a probe that never passes is not a gate, it is an outage"; }
echo "  B: 0 missing -> '${health}'"

run_leg "$TMP/declared-empty.json" 'manifests/'
health=$(sed -n '/^---HEALTH---$/,$p' "$TMP/out.txt" | sed -n '2p')
case "${health}" in
  NOTREADY*image-leg-enumeration-empty*) : ;;
  *) sed 's/^/    /' "$TMP/out.txt"; fail "S9 direction C: a BROKEN enumeration produced health '${health}', expected NOTREADY — 'I could not check' must never present as all-clear (#5640)" ;;
esac
echo "  C: broken enumeration -> '${health}'"
RUN_EXTRA_ENV=""
echo "  PASS (NOT-READY on a missing pin, READY on a clean measured cycle, NOT-READY on a broken read)"

echo "[daytwo-image-leg] All gates green."
