#!/usr/bin/env bash
# check-live-newapi-sso-version.sh — CLUSTER-side guard for UAT rows 37/38
# (#3374/#3858/#4136, #5612, #5599).
#
# WHY THIS EXISTS — SOURCE-SIDE CLEAN IS NOT PLATFORM CLEAN
# ──────────────────────────────────────────────────────────────────────────
# Fresh live evidence on hw292, 2026-08-06 (docs/ledger/UAT.md rows 37/38,
# screenshots docs/sessions/2026-08-06/hw292-screenshots/row37-*.png): a
# genuine first-hit bare-URL visit to newapi.hw292.omani.works, in a real
# owner session (passwordless-PIN + one-time kcBrokerURL identity-broker
# roundtrip already completed, proven silent on 5 OTHER apps in the same
# session), still lands on NewAPI's own `/login?expired=true` page with an
# inert "Continue with OpenOva SSO" button — clicking it does nothing (no
# navigation, no network request).
#
# That looks like an open code defect. It is not — it is a STALE ROLLOUT.
# Both root causes were already root-caused, fixed, and merged to `main`
# BEFORE this evidence was captured:
#
#   platform/newapi/chart/templates/httproute.yaml:127-135 — #5612 (PR #5689,
#     chart 1.4.149): routes an Exact `/login` match to the sandbox-bridge
#     Service (platform/newapi/internal/handler/sso_init.go's SSOInitHandler,
#     ssoInitAllowedPaths = {"/", "/login"}) instead of leaving `/login` to
#     NewAPI's own SPA, whose click handler cannot discover a
#     custom_oauth_provider at render time (NewAPI v0.13.2 /api/status omits
#     the field) and is therefore permanently inert.
#   clusters/_template/bootstrap-kit/80-newapi.yaml:388-401 + platform/newapi/
#     chart/templates/crossregion-netpol.yaml — #5599 (PR #5707, chart
#     1.4.151): a 2-region Sovereign ran TWO independent newapi Deployments
#     over TWO independent CNPG Clusters behind ONE `newapi.<fqdn>` hostname;
#     a single-use Keycloak authorization code minted against one region's
#     Postgres 403'd when the callback landed on the other region behind the
#     shared VIP (#5459). `crossRegion.enabled` collapses this to one primary
#     Deployment + ClusterMesh-fanned Services.
#
# Both fixes render correctly today (see platform/newapi/chart/tests/
# httproute-render.sh Case 7/8 and crossregion-render.sh) and bp-newapi is at
# chart 1.4.153 on `main`. hw292 (dep 1c56518035a83e03, provisioned
# 2026-08-03) predates BOTH — measured live (read-only), both regions:
#
#   KUBECONFIG=hw292.yaml   kubectl -n flux-system get helmrelease bp-newapi \
#     -o jsonpath='{.spec.chart.spec.version}'   -> 1.4.146
#   KUBECONFIG=hw292-b.yaml kubectl -n flux-system get helmrelease bp-newapi \
#     -o jsonpath='{.spec.chart.spec.version}'   -> 1.4.146
#
# hw292's `bootstrap-kit` Kustomization tracks GitRepository `openova` (its
# own local Gitea mirror of this catalog, self-sovereign-cutover step 1),
# refreshed on the Sovereign's OWN sync schedule (default daily) — not on
# every commit to this repo. A chart-render test can never see this: it
# proves the SOURCE renders correctly, never what a given live Sovereign is
# actually running. This is the exact §854 nodeport class documented in
# scripts/check-live-nodeports.sh ("Source-side clean is NOT platform
# clean") applied to a chart-version drift instead of a Service field.
#
# WHAT THIS GUARD DOES
# ──────────────────────────────────────────────────────────────────────────
# Reads the live bp-newapi HelmRelease's `.spec.chart.spec.version` off a
# given kubeconfig/context and fails if it is below MIN_FIXED_VERSION
# (1.4.151 — the higher of the two fix versions; monotonic chart numbering
# means >=1.4.151 implies >=1.4.149). Absence of the HelmRelease (newapi not
# installed on this cluster) is NOT a failure — nothing to check.
#
# Usage:
#   scripts/check-live-newapi-sso-version.sh                  # current kube context
#   scripts/check-live-newapi-sso-version.sh --context <ctx>
#   scripts/check-live-newapi-sso-version.sh --kubeconfig <path>
#   scripts/check-live-newapi-sso-version.sh --self-test       # no cluster needed
#
# Exit: 0 = current context's bp-newapi is >= MIN_FIXED_VERSION (or absent),
#       1 = a live bp-newapi is stale (rows 37/38 will still fail there),
#       2 = setup/self-test failure.
set -euo pipefail

MIN_FIXED_VERSION="1.4.151"

CONTEXT=""
KUBECONFIG_ARG=""
SELF_TEST_ONLY=0

while [ $# -gt 0 ]; do
  case "$1" in
    --context)    CONTEXT="$2"; shift 2 ;;
    --kubeconfig) KUBECONFIG_ARG="$2"; shift 2 ;;
    --self-test)  SELF_TEST_ONLY=1; shift ;;
    *) echo "Unknown arg: $1" >&2
       echo "Usage: $0 [--context <ctx>] [--kubeconfig <path>] [--self-test]" >&2
       exit 2 ;;
  esac
done

# semver_ge A B -> 0 (true) if A >= B, comparing dotted numeric components
# (NOT lexicographic — "1.4.9" must sort BELOW "1.4.10"). Missing/short
# components compare as 0, so "1.4" >= "1.4.0" is true.
semver_ge() {
  python3 -c '
import sys
def parts(v):
    return [int(x) for x in v.strip().split(".") if x != ""]
a, b = parts(sys.argv[1]), parts(sys.argv[2])
n = max(len(a), len(b))
a += [0] * (n - len(a)); b += [0] * (n - len(b))
sys.exit(0 if a >= b else 1)
' "$1" "$2"
}

# ─── Phase 0 — detector self-test (vacuity guard) ────────────────────────
# An absence-assertion ("no stale bp-newapi") reports clean both when the
# fleet is genuinely upgraded AND when the comparison logic is broken. Prove
# the comparator against fixtures first — including the exact byte values
# measured live on hw292 today — so a regression in semver_ge cannot report
# a false PASS on a real stale cluster.
echo "[newapi-sso-version] self-test: semver comparator"
FAIL_ST() { echo "SELF-TEST FAIL: $1" >&2; exit 2; }

semver_ge "1.4.151" "1.4.151" || FAIL_ST "1.4.151 >= 1.4.151 must be true (equal boundary)"
semver_ge "1.4.153" "1.4.151" || FAIL_ST "1.4.153 >= 1.4.151 must be true"
semver_ge "1.4.146" "1.4.151" && FAIL_ST "1.4.146 >= 1.4.151 was true — this is hw292's ACTUAL live version, misdetecting it as fixed would false-pass a known-broken cluster"
semver_ge "1.4.149" "1.4.151" && FAIL_ST "1.4.149 (the #5612-only fix, still missing #5599) was accepted as sufficient"
semver_ge "1.4.9" "1.4.10" && FAIL_ST "lexicographic comparison bug: '1.4.9' sorted >= '1.4.10'"
semver_ge "1.4.10" "1.4.9" || FAIL_ST "lexicographic comparison bug: '1.4.10' sorted < '1.4.9'"
semver_ge "1.5.0" "1.4.151" || FAIL_ST "a later minor (1.5.0) must satisfy a 1.4.151 floor"
echo "  ok: equal/above/below boundaries + numeric (not lexicographic) component compare"

if [ "${SELF_TEST_ONLY}" -eq 1 ]; then
  echo "OK: --self-test only; no cluster was contacted."
  exit 0
fi

# ─── Phase 1 — live sweep ─────────────────────────────────────────────────
if ! command -v kubectl >/dev/null 2>&1; then
  echo "WARN: kubectl not on PATH — cannot sweep a live cluster." >&2
  echo "      This run proves the DETECTOR only, not any cluster." >&2
  exit 0
fi

KCTL=(kubectl)
[ -n "${KUBECONFIG_ARG}" ] && KCTL+=(--kubeconfig "${KUBECONFIG_ARG}")
[ -n "${CONTEXT}" ] && KCTL+=(--context "${CONTEXT}")

echo ""
echo "== live bp-newapi HelmRelease check (${CONTEXT:-${KUBECONFIG_ARG:-current context}}) =="

set +e
RAW="$("${KCTL[@]}" -n flux-system get helmrelease bp-newapi -o json 2>/dev/null)"
RC=$?
set -e

if [ ${RC} -ne 0 ] || [ -z "${RAW}" ]; then
  echo "SKIP: no bp-newapi HelmRelease reachable in flux-system (not installed here," >&2
  echo "      or cluster/permission unreachable) — nothing for this guard to assert." >&2
  exit 0
fi

LIVE_VERSION="$(printf '%s' "${RAW}" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("chart",{}).get("spec",{}).get("version","") or "")')"

if [ -z "${LIVE_VERSION}" ]; then
  echo "WARN: bp-newapi HelmRelease has no .spec.chart.spec.version — cannot assert." >&2
  exit 0
fi

echo "live bp-newapi chart version: ${LIVE_VERSION}"
echo "required minimum (rows 37/38, #5612+#5599 both fixed): ${MIN_FIXED_VERSION}"

if semver_ge "${LIVE_VERSION}" "${MIN_FIXED_VERSION}"; then
  echo "OK: ${LIVE_VERSION} >= ${MIN_FIXED_VERSION} — this cluster carries the #5612 (Exact"
  echo "    /login route) and #5599 (cross-region singleton) fixes for newapi's"
  echo "    zero-click bare-URL SSO landing."
  exit 0
fi

echo "" >&2
echo "───────────────────────────────────────────────────────────────" >&2
echo "FAIL: bp-newapi is running chart ${LIVE_VERSION}, below the ${MIN_FIXED_VERSION}" >&2
echo "floor. UAT rows 37/38 (#3374 #3858 #4136, Refs #960) will still fail here:" >&2
echo "the bare-URL first hit to newapi.<fqdn>/ lands on NewAPI's own" >&2
echo "/login?expired=true page with an inert 'Continue with OpenOva SSO'" >&2
echo "button instead of landing signed-in." >&2
echo "" >&2
echo "Do NOT kubectl-patch the live HelmRelease version field — this" >&2
echo "Kustomization reconciles from a GitRepository (bootstrap-kit tracks" >&2
echo "clusters/_template/bootstrap-kit/, sourceRef kind=GitRepository) and an" >&2
echo "unmerged live edit reverts on the next sync (memory:" >&2
echo "deploybot_treadmill_chart_pr_version_collision). Fix at the SOURCE the" >&2
echo "Sovereign actually syncs from: re-sync this Sovereign's local Gitea" >&2
echo "mirror of openova/openova (self-sovereign-cutover step 1, default daily" >&2
echo "cadence) so Flux picks up clusters/_template/bootstrap-kit/80-newapi.yaml" >&2
echo "pinned at >= ${MIN_FIXED_VERSION}, then let the bootstrap-kit Kustomization" >&2
echo "(interval 5m) reconcile." >&2
echo "───────────────────────────────────────────────────────────────" >&2
exit 1
