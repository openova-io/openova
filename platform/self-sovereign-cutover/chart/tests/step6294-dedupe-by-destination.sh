#!/usr/bin/env bash
# #6294 — behavioural test for step-03 harbor-prewarm's worklist de-duplication
# by DESTINATION.
#
# WHAT #6294 FIXES. The Phase A enumeration unions every image reference on
# the cluster. The same artifact routinely appears TWICE — once in the
# mothership-proxy form (harbor.openova.io/proxy-dockerhub/X, pinned by the
# bootstrap-kit) and once in the direct-upstream form (docker.io/X, pinned by a
# chart's values) — and both resolve to the byte-identical local destination
# registry.<fqdn>/proxy-dockerhub/X. Two concurrent copy_one workers then race
# on one destination; whichever lands second pushes content the first already
# wrote, Harbor answers `unauthorized ... action: push` for the proxy-<x>
# project, and the whole step FATALs for an image that WAS mirrored (hw296
# 2026-08-14, curlimages/curl:8.10.1 — a ~55-minute Phase A burned + one of
# three backoffLimit retries).
#
# The fix keeps ONE work item per destination set, preferring the
# mothership-proxy form (copy_one gives it both legs: proxy first, #5095 direct
# fallback). This suite executes the rendered dedupe block VERBATIM (extracted
# between its anchors) with a stubbed resolver, so it tests the shipped shell,
# not a re-implementation.

set -uo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0
fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

helm template ssc "${CHART_DIR}" --set sovereign.fqdn="hw296.omani.works" >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

python3 - "${TMP}/render.yaml" "${TMP}" <<'PY'
import sys, yaml
render, out = sys.argv[1], sys.argv[2]
for d in yaml.safe_load_all(open(render)):
    if not d or d.get("kind") != "ConfigMap":
        continue
    if d["metadata"]["name"] == "cutover-step-03-harbor-prewarm":
        open(out + "/run.sh", "w").write(d["data"]["run.sh"])
PY
[ -f "${TMP}/run.sh" ] || { echo "FAIL: could not extract cutover-step-03-harbor-prewarm run.sh from the render"; exit 1; }

awk '/prewarm-dedupe-by-dest BEGIN/{f=1} f{print} /prewarm-dedupe-by-dest END/{exit}' "${TMP}/run.sh" >"${TMP}/dedupe.sh"
if ! grep -q 'prewarm_dedupe_by_dest()' "${TMP}/dedupe.sh"; then
  fail "the rendered run.sh carries no prewarm_dedupe_by_dest block between its #6294 anchors"
  echo "FAILURES=${FAILURES}"; exit 1
fi
pass "rendered run.sh carries the #6294 dedupe block ($(wc -l <"${TMP}/dedupe.sh") lines)"

if ! grep -q 'prewarm_dedupe_by_dest ${mirror_work}' "${TMP}/run.sh"; then
  fail "the dedupe block is defined but the Phase A worklist is never passed through it"
else
  pass "the Phase A worklist is passed through prewarm_dedupe_by_dest before the fan-out"
fi

# ── Execute the block with a stubbed resolver (the hw296 shape) ──────────────
# The stub mirrors offlinemirror_local_paths' contract: mothership-proxy refs
# host-swap (path preserved); docker.io refs map to proxy-dockerhub/<path>;
# ghcr.io/openova-io refs fan out to TWO destinations.
run_dedupe() { # $@ = worklist; prints the kept list on stdout, notices on stderr
  (
    cpdir="${TMP}/copies"
    MOTHERSHIP_HOST="harbor.openova.io"
    offlinemirror_local_paths() {
      case "$1" in
        harbor.openova.io/*) printf '%s' "${1#harbor.openova.io/}" ;;
        docker.io/*)         printf 'proxy-dockerhub/%s' "${1#docker.io/}" ;;
        ghcr.io/openova-io/*) printf 'openova-io/%s proxy-ghcr/openova-io/%s' "${1#ghcr.io/openova-io/}" "${1#ghcr.io/openova-io/}" ;;
        quay.io/*)           printf 'proxy-quay/%s' "${1#quay.io/}" ;;
        *) printf '' ;;
      esac
    }
    # shellcheck source=/dev/null
    . "${TMP}/dedupe.sh"
    prewarm_dedupe_by_dest "$@"
  )
}

echo "[step6294] Case A: the hw296 pair collapses to the mothership-proxy form"
kept=$(run_dedupe docker.io/curlimages/curl:8.10.1 harbor.openova.io/proxy-dockerhub/curlimages/curl:8.10.1 2>"${TMP}/a.err")
case " ${kept} " in
  *" harbor.openova.io/proxy-dockerhub/curlimages/curl:8.10.1 "*) pass "proxy form kept" ;;
  *) fail "proxy form missing from kept list: '${kept}'" ;;
esac
case " ${kept} " in
  *" docker.io/curlimages/curl:8.10.1 "*) fail "direct form still present — the destination would be pushed twice: '${kept}'" ;;
  *) pass "direct form dropped" ;;
esac
grep -q 'dedupe docker.io/curlimages/curl:8.10.1: same local destination as harbor.openova.io/proxy-dockerhub/curlimages/curl:8.10.1' "${TMP}/a.err" \
  && pass "notice names the dropped item and its owner" \
  || fail "no dedupe notice for the dropped item: $(cat "${TMP}/a.err")"
grep -q '1 duplicate work item(s) collapsed by destination (#6294)' "${TMP}/a.err" \
  && pass "collapse count reported" || fail "collapse count not reported"

echo "[step6294] Case B: order independence — proxy form listed SECOND still wins"
kept=$(run_dedupe docker.io/library/alpine:3.20 quay.io/x/y:1 harbor.openova.io/proxy-dockerhub/library/alpine:3.20 2>/dev/null)
[ "$(printf '%s\n' ${kept} | grep -c .)" = "2" ] && pass "two items kept out of three" || fail "kept='${kept}'"
case " ${kept} " in *" harbor.openova.io/proxy-dockerhub/library/alpine:3.20 "*) pass "proxy form kept regardless of position" ;; *) fail "proxy form lost: '${kept}'" ;; esac
case " ${kept} " in *" quay.io/x/y:1 "*) pass "an unrelated item is untouched" ;; *) fail "unrelated item dropped: '${kept}'" ;; esac

echo "[step6294] Case C: distinct destinations are never collapsed (tags differ)"
kept=$(run_dedupe docker.io/curlimages/curl:8.10.1 docker.io/curlimages/curl:8.11.0 harbor.openova.io/proxy-dockerhub/curlimages/curl:8.9.0 2>"${TMP}/c.err")
[ "$(printf '%s\n' ${kept} | grep -c .)" = "3" ] && pass "three distinct tags all kept" || fail "kept='${kept}'"
[ -s "${TMP}/c.err" ] && fail "unexpected notices: $(cat "${TMP}/c.err")" || pass "no notices when nothing collapses"

echo "[step6294] Case D: two direct forms of one destination collapse to the first"
kept=$(run_dedupe docker.io/library/nats:2.10 docker.io/library/nats:2.10 2>/dev/null)
[ "$(printf '%s\n' ${kept} | grep -c .)" = "1" ] && pass "duplicate direct forms collapse to one" || fail "kept='${kept}'"

echo "[step6294] Case E: a multi-destination ref blocks a later ref on ANY of its destinations"
kept=$(run_dedupe ghcr.io/openova-io/catalyst-api:abc harbor.openova.io/proxy-ghcr/openova-io/catalyst-api:abc 2>/dev/null)
[ "$(printf '%s\n' ${kept} | grep -c .)" = "1" ] && pass "one item kept" || fail "kept='${kept}'"
case " ${kept} " in *" harbor.openova.io/proxy-ghcr/openova-io/catalyst-api:abc "*) pass "proxy form preferred (pass 1 claims first)" ;; *) fail "kept='${kept}'" ;; esac

echo "[step6294] Case F: no MOTHERSHIP_HOST — nothing is treated as proxy-sourced, plain first-wins dedupe"
kept=$( (
  cpdir="${TMP}/copies2"; unset MOTHERSHIP_HOST
  offlinemirror_local_paths() { printf 'proxy-dockerhub/%s' "${1#*/}"; }
  . "${TMP}/dedupe.sh"
  prewarm_dedupe_by_dest docker.io/a/b:1 other.io/a/b:1
) 2>/dev/null )
[ "$(printf '%s\n' ${kept} | grep -c .)" = "1" ] && pass "collapses to one without a mothership host" || fail "kept='${kept}'"
case " ${kept} " in *" docker.io/a/b:1 "*) pass "first-listed wins" ;; *) fail "kept='${kept}'" ;; esac

echo
if [ "${FAILURES}" -gt 0 ]; then
  echo "[step6294] FAILURES=${FAILURES}"; exit 1
fi
echo "[step6294] all cases passed"
