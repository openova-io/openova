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

# extract_login_routes reads an HTTPRoute list (kubectl -o json) on stdin and
# prints one "<routeName> -> <backendRefs>" line per rule carrying an Exact
# `/login` path match — the #5612 recovery rule. Empty output = the rule is
# absent. Kept as a function so Phase 0 can prove it against fixtures before
# Phase 2 trusts it against a cluster.
extract_login_routes() {
  python3 -c '
import json, sys
d = json.load(sys.stdin)
hits = []
for it in d.get("items", []):
    name = it.get("metadata", {}).get("name", "")
    for rule in it.get("spec", {}).get("rules", []):
        for m in rule.get("matches", []):
            p = m.get("path", {})
            if p.get("type") == "Exact" and p.get("value") == "/login":
                backs = [b.get("name", "") for b in rule.get("backendRefs", [])]
                hits.append("%s -> %s" % (name, ",".join(backs)))
print("\n".join(hits))'
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

# Phase 0b — the SAME vacuity discipline for the route detector. A
# "no Exact /login rule" assertion reports clean both when the rule is genuinely
# missing AND when the extractor is broken, so prove it can answer BOTH ways
# before Phase 2 believes a clean answer. The negative fixture is the shape
# measured live on hw292 2026-08-10 (read-only) — Exact `/` to the bridge plus
# PathPrefix `/` to newapi, and no /login rule at all, on chart 1.4.146.
echo "[newapi-sso-version] self-test: Exact /login route detector"
ROUTES_WITHOUT_LOGIN='{"items":[{"metadata":{"name":"newapi-bp-newapi-public"},"spec":{
  "hostnames":["newapi.hw292.omani.works"],"rules":[
  {"matches":[{"path":{"type":"Exact","value":"/"}}],"backendRefs":[{"name":"newapi-bp-newapi-bridge"}]},
  {"matches":[{"path":{"type":"PathPrefix","value":"/"}}],"backendRefs":[{"name":"newapi-bp-newapi"}]}]}}]}'
ROUTES_WITH_LOGIN='{"items":[{"metadata":{"name":"newapi-bp-newapi-public"},"spec":{
  "hostnames":["newapi.t00.omani.works"],"rules":[
  {"matches":[{"path":{"type":"Exact","value":"/"}}],"backendRefs":[{"name":"newapi-bp-newapi-bridge"}]},
  {"matches":[{"path":{"type":"Exact","value":"/login"}}],"backendRefs":[{"name":"newapi-bp-newapi-bridge"}]},
  {"matches":[{"path":{"type":"PathPrefix","value":"/"}}],"backendRefs":[{"name":"newapi-bp-newapi"}]}]}}]}'

[ -z "$(printf '%s' "${ROUTES_WITHOUT_LOGIN}" | extract_login_routes)" ] \
  || FAIL_ST "detector found an Exact /login rule in the hw292 fixture, which has none — a false PASS on a known-broken cluster"
[ -n "$(printf '%s' "${ROUTES_WITH_LOGIN}" | extract_login_routes)" ] \
  || FAIL_ST "detector found NO Exact /login rule in the FIXED fixture — the check can never pass, i.e. it is vacuous"
# A PathPrefix /login must NOT satisfy the rule: the chart renders type Exact,
# and a prefix match would also swallow /login-callback style paths.
ROUTES_PREFIX_ONLY="${ROUTES_WITHOUT_LOGIN//\"PathPrefix\",\"value\":\"\/\"/\"PathPrefix\",\"value\":\"\/login\"}"
[ -z "$(printf '%s' "${ROUTES_PREFIX_ONLY}" | extract_login_routes)" ] \
  || FAIL_ST "a PathPrefix /login was accepted as the Exact /login rule"
echo "  ok: detector answers BOTH ways (absent on the live hw292 shape, present on the fixed shape) and rejects PathPrefix"

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

# APPLIED, not DESIRED (#5750 class). `.spec.chart.spec.version` is what the
# HelmRelease WANTS; `.status.history[0].chartVersion` is what Helm last
# actually installed. They diverge whenever an upgrade is pending — measured
# live on hw292 2026-08-06 for a sibling chart: bp-guacamole region-a read
# spec=0.2.37 / applied=0.2.33 with Ready=False "dependency
# 'flux-system/bp-cilium' is not ready". Asserting from spec would have this
# guard print "this cluster carries the fixes" about a version the cluster has
# never run — the exact SOURCE-vs-LIVE confusion this file's own header warns
# about, one level down.
LIVE_VERSION="$(printf '%s' "${RAW}" | python3 -c 'import json,sys
d=json.load(sys.stdin)
h=(d.get("status",{}) or {}).get("history") or []
print((h[0].get("chartVersion") if h else "") or "")')"
VERSION_SOURCE="applied"

if [ -z "${LIVE_VERSION}" ]; then
  # No install history at all (never installed). Fall back to the desired pin,
  # but LABEL it — a desired version is a weaker claim and must not read as
  # proof of what is running.
  LIVE_VERSION="$(printf '%s' "${RAW}" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("chart",{}).get("spec",{}).get("version","") or "")')"
  VERSION_SOURCE="desired-no-history"
fi

if [ -z "${LIVE_VERSION}" ]; then
  echo "WARN: bp-newapi HelmRelease carries neither .status.history[0].chartVersion" >&2
  echo "      nor .spec.chart.spec.version — cannot assert." >&2
  exit 0
fi

# Surface a pending upgrade explicitly: applied < desired means the cluster is
# NOT yet running what its HelmRelease asks for, and the operator should know
# that even when the applied version already clears the floor.
DESIRED_VERSION="$(printf '%s' "${RAW}" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("spec",{}).get("chart",{}).get("spec",{}).get("version","") or "")')"
if [ -n "${DESIRED_VERSION}" ] && [ "${DESIRED_VERSION}" != "${LIVE_VERSION}" ]; then
  echo "NOTE: HelmRelease desires ${DESIRED_VERSION} but has applied ${LIVE_VERSION} —"
  echo "      an upgrade is pending and has NOT landed. Asserting on the applied version."
fi

echo "live bp-newapi chart version: ${LIVE_VERSION} (${VERSION_SOURCE})"
echo "required minimum (rows 37/38, #5612+#5599 both fixed): ${MIN_FIXED_VERSION}"

if semver_ge "${LIVE_VERSION}" "${MIN_FIXED_VERSION}"; then
  echo "OK: ${LIVE_VERSION} >= ${MIN_FIXED_VERSION} — this cluster carries the #5612 (Exact"
  echo "    /login route) and #5599 (cross-region singleton) fixes for newapi's"
  echo "    zero-click bare-URL SSO landing."
  echo ""
  # ─── Phase 2 — assert the SURFACE, not the proxy ───────────────────────
  # A chart version is a PROXY for the fix; the thing UAT rows 37/38 actually
  # depend on is one live HTTPRoute rule. The implication only holds one way:
  # below the floor the rule cannot exist, but AT or above the floor it still
  # may not, because httproute.yaml gates the Exact `/login` match on
  # `{{- if .Values.sovereignFQDN }}` inside the `ingress.httpRoute.enabled`
  # block. An overlay that blanks sovereignFQDN, disables the HTTPRoute, or
  # re-points gateway.bridgeBackendService clears the version floor and still
  # ships no recovery route — the version check would print OK about a cluster
  # where the row fails. Read the rule itself so a green line means the
  # surface is there, not that a number is large enough.
  ROUTES="$("${KCTL[@]}" -n newapi get httproute -o json 2>/dev/null || true)"
  if [ -z "${ROUTES}" ]; then
    echo "WARN: no HTTPRoute readable in namespace newapi — version cleared the floor," >&2
    echo "      but the /login recovery route could not be verified on this cluster." >&2
    exit 0
  fi
  ROUTE_VERDICT="$(printf '%s' "${ROUTES}" | extract_login_routes)"

  if [ -z "${ROUTE_VERDICT}" ]; then
    echo "───────────────────────────────────────────────────────────────" >&2
    echo "FAIL: chart ${LIVE_VERSION} clears the ${MIN_FIXED_VERSION} floor, but NO live HTTPRoute in" >&2
    echo "namespace newapi carries an Exact \`/login\` match. That rule IS the #5612" >&2
    echo "fix — it routes the expired-session page to the sandbox-bridge" >&2
    echo "(platform/newapi/internal/handler/sso_init.go, ssoInitAllowedPaths =" >&2
    echo "{\"/\", \"/login\"}) instead of NewAPI's own SPA, whose \"Continue with" >&2
    echo "OpenOva SSO\" button is permanently inert. Without it UAT rows 37/38 fail" >&2
    echo "on this cluster despite a version that looks fixed." >&2
    echo "" >&2
    echo "Most likely cause — an overlay that satisfies the version but suppresses" >&2
    echo "the rule: \`sovereignFQDN\` blank (the gate on the rule), or" >&2
    echo "\`ingress.httpRoute.enabled: false\`. Check the bp-newapi HelmRelease" >&2
    echo "values against clusters/_template/bootstrap-kit/80-newapi.yaml." >&2
    echo "───────────────────────────────────────────────────────────────" >&2
    exit 1
  fi

  echo "OK: live HTTPRoute carries the Exact /login recovery rule:"
  printf '    %s\n' "${ROUTE_VERDICT}"
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
