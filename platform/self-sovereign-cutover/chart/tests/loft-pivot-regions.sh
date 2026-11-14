#!/usr/bin/env bash
# #5650 — behavioural test for step-06 Phase-2c: the vcluster loft chart-source
# pivot must cover EVERY region, not just the primary.
#
# WHY THIS TEST EXISTS, and why it is not a grep.
#
# 0.1.168 shipped Phase-2c as a primary-only block: every kubectl in it ran
# against the Job's own in-cluster context. Slot 60 installs the loft
# HelmRepository in EVERY region, so on a 2-region Sovereign the tether is TWO
# objects. Measured live on hw292 (dep 1c56518035a83e03), ~63h after
# cutoverComplete=true, source-controller in BOTH regions was still fetching
# https://charts.loft.sh on its 15m interval.
#
# The defect class #5650 is about is "a guard aimed at a moment that cannot
# fail". A single-shot sample inside a window proves nothing about a periodic
# actor, so this test does not observe traffic at all — it asserts on the
# DECLARATION the pivot leaves behind, per region, which is not a race.
#
# HOW IT AVOIDS BEING THE SAME MISTAKE. It extracts the REAL Phase-2c block out
# of the RENDERED Job (not a copy pasted here — #5646) and executes it against
# stub kubectl/helm, then asserts the VERDICT and the recorded call log in both
# directions. Every case below was run against the PRE-FIX tree first: cases 1,
# 4 and 5 go RED there and green here; cases 2, 3 and 6 are CONTROLS that are
# green on BOTH trees, so the suite cannot be satisfied by deleting, disabling
# or blanket-suppressing Phase-2c — doing that turns the controls red.
set -uo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
FQDN="hw292.omani.works"
HARBOR_HOST="registry.${FQDN}"
LOCAL_URL="oci://${HARBOR_HOST}/openova-io"
UPSTREAM="https://charts.loft.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0

fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

command -v jq >/dev/null 2>&1 || { echo "FAIL — jq is required (Phase-2c itself shells out to jq)."; exit 1; }

# ── Extract the block under test straight from the render ──────────────────
helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

# Extraction is plain awk over the render text — deliberately NO PyYAML, so the
# suite has the same dependency surface as the Job it tests (sh + jq) and cannot
# skip itself on a runner missing a python module.
#
# The two delimiters are stable across the pre-fix and post-fix trees on
# purpose: this same extraction has to work on the tree that FAILS, or the
# red-on-pre-fix claim is unverifiable. Leading indentation is stripped because
# the block lives inside a YAML block scalar; sh does not care about it and
# Phase-2c contains no heredoc whose body would be disturbed.
# The end pattern accepts EITHER terminator on purpose. Post-fix, Phase-2c lives
# in the ConfigMap's phase2c.sh key (moved out of the inline args for #5593) and
# ends on its own marker; pre-fix it was inline and ran up to the "# NOTE (Refs
# #3379 #3526)" comment. Matching both is what lets the identical suite run
# against the tree it must go RED on.
awk '/# ---- Phase 2c \(#5650\)/,/# ---- end Phase 2c \(#5650\)|# NOTE \(Refs #3379 #3526\)/' "${TMP}/render.yaml" \
  | sed '$d' | sed 's/^ *//' >"${TMP}/phase2c.sh"
if ! grep -q 'VCL_LOFT_PIVOT_ENABLED' "${TMP}/phase2c.sh"; then
  echo "FAIL — Phase-2c did not extract from the render (marker comments changed?)."
  exit 1
fi
# Truncation guard: the block must end on the `fi` that closes the
# vclusterLoftPivot.enabled conditional. A short extraction would still `sh`
# cleanly and could still exit 0 — the fail-open shape this suite exists to
# eliminate.
if ! tail -5 "${TMP}/phase2c.sh" | grep -q 'Phase-2c SKIP: vclusterLoftPivot.enabled=false'; then
  echo "FAIL — Phase-2c extraction was truncated (missing the enabled=false else-branch tail)."
  exit 1
fi
sh -n "${TMP}/phase2c.sh" || { echo "FAIL — extracted Phase-2c is not valid POSIX sh."; exit 1; }

# ── Harness ────────────────────────────────────────────────────────────────
# Stub kubectl keeps PER-CLUSTER state under ${STATE}/<cluster>/ where <cluster>
# is the --kubeconfig basename or "primary". That is the whole point: a
# primary-only implementation writes only ${STATE}/primary and the secondary
# assertions below have nothing to read.
mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
cluster="primary"
args=()
for a in "$@"; do
  case "$a" in
    --kubeconfig=*) kc="${a#--kubeconfig=}"; cluster="$(basename "${kc}" .yaml)" ;;
    *) args+=("$a") ;;
  esac
done
d="${STATE}/${cluster}"; mkdir -p "$d"
printf '%s\n' "${args[*]}" >>"${d}/calls.log"
printf '%s\n' "${cluster} ${args[*]}" >>"${STATE}/all-calls.log"
# A cluster marked unreachable fails every verb with a transport error (NOT a
# NotFound) — the fail-closed path.
if [ -e "${STATE}/${cluster}.unreachable" ]; then
  echo "Unable to connect to the server: dial tcp 10.0.0.1:6443: i/o timeout" >&2
  exit 1
fi
verb="${args[0]:-}"
case "${verb}" in
  get)
    kind="${args[1]:-}"; name="${args[2]:-}"
    case "${kind}" in
      helmrepository|helmrepositories)
        if [ ! -e "${d}/loft.url" ]; then
          echo "Error from server (NotFound): helmrepositories.source.toolkit.fluxcd.io \"${name}\" not found" >&2
          exit 1
        fi
        url="$(cat "${d}/loft.url")"; typ="$(cat "${d}/loft.type" 2>/dev/null || echo "")"
        if printf '%s\n' "${args[*]}" | grep -q 'jsonpath.*\.spec\.type'; then
          printf '%s' "${typ}"
        elif printf '%s\n' "${args[*]}" | grep -q 'jsonpath'; then
          printf '%s' "${url}"
        else
          jq -n --arg u "${url}" --arg t "${typ}" \
            '{apiVersion:"source.toolkit.fluxcd.io/v1beta2",kind:"HelmRepository",metadata:{name:"loft",namespace:"vcluster-system"},spec:({url:$u}+(if $t=="" then {} else {type:$t} end))}'
        fi
        ;;
      helmrelease|helmreleases)
        [ -e "${d}/hr.present" ] || { echo "Error from server (NotFound): helmreleases.helm.toolkit.fluxcd.io \"${name}\" not found" >&2; exit 1; }
        echo '{}' ;;
      secret|secrets)
        case "${name}" in
          cutover-harbor-ca) jq -n '{apiVersion:"v1",kind:"Secret",type:"Opaque",metadata:{name:"cutover-harbor-ca",namespace:"flux-system"},data:{"ca.crt":"Q0EK"}}' ;;
          *)                 jq -n '{apiVersion:"v1",kind:"Secret",type:"kubernetes.io/dockerconfigjson",metadata:{name:"ghcr-pull",namespace:"flux-system"},data:{".dockerconfigjson":"e30K"}}' ;;
        esac ;;
      *) echo '{}' ;;
    esac ;;
  patch)
    kind="${args[1]:-}"
    if [ "${kind}" = "helmrepository" ] || [ "${kind}" = "helmrepositories" ]; then
      # STICKY=0 simulates a patch that reports success but is reverted — the
      # read-back must catch it.
      if [ -e "${STATE}/${cluster}.nonsticky" ]; then exit 0; fi
      newurl="$(printf '%s\n' "${args[*]}" | sed -n 's/.*"url":"\([^"]*\)".*/\1/p')"
      [ -n "${newurl}" ] && printf '%s' "${newurl}" >"${d}/loft.url"
      printf 'oci' >"${d}/loft.type"
      printf '%s\n' "PIVOTED ${newurl}" >>"${d}/pivots.log"
    else
      printf 'SUSPENDED\n' >>"${d}/pivots.log"
    fi
    exit 0 ;;
  apply) cat >/dev/null; printf 'APPLIED\n' >>"${d}/pivots.log"; exit 0 ;;
  *) exit 0 ;;
esac
STUB
cat >"${TMP}/bin/helm" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${STATE}/helm-calls.log"
case "$1" in
  pull) for a in "$@"; do [ "${prev:-}" = "-d" ] && { mkdir -p "$a"; : >"$a/vcluster-0.33.0.tgz"; }; prev="$a"; done ;;
esac
exit 0
STUB
chmod +x "${TMP}/bin/kubectl" "${TMP}/bin/helm"

# run_case <label> <secondary-region-list> <primary-loft-url> <secondary-loft-url>
#   *-loft-url = "" means the HelmRepository is absent in that region.
# Extra per-case knobs come in via PRESET_* files created by the caller.
run_case() {
  local label="$1" secondaries="$2" purl="$3" surl="$4"
  local case_dir="${TMP}/case-${label}"
  rm -rf "${case_dir}"; mkdir -p "${case_dir}/state" "${case_dir}/kc"
  if [ -n "${purl}" ]; then mkdir -p "${case_dir}/state/primary"; printf '%s' "${purl}" >"${case_dir}/state/primary/loft.url"; : >"${case_dir}/state/primary/hr.present"; printf '%s' "${PRESET_TYPE:-}" >"${case_dir}/state/primary/loft.type"; fi
  for r in ${secondaries}; do
    : >"${case_dir}/kc/${r}.yaml"
    if [ -n "${surl}" ]; then mkdir -p "${case_dir}/state/${r}"; printf '%s' "${surl}" >"${case_dir}/state/${r}/loft.url"; : >"${case_dir}/state/${r}/hr.present"; printf '%s' "${PRESET_TYPE:-}" >"${case_dir}/state/${r}/loft.type"; fi
  done
  [ -n "${PRESET_UNREACHABLE:-}" ] && : >"${case_dir}/state/${PRESET_UNREACHABLE}.unreachable"
  [ -n "${PRESET_NONSTICKY:-}" ] && : >"${case_dir}/state/${PRESET_NONSTICKY}.nonsticky"
  CASE_DIR="${case_dir}"
  (
    export PATH="${TMP}/bin:${PATH}"
    export STATE="${case_dir}/state"
    export SECONDARY_KUBECONFIG_DIR="${case_dir}/kc"
    export VCL_LOFT_PIVOT_ENABLED=true VCL_LOFT_HR_NAME=loft VCL_LOFT_NAMESPACE=vcluster-system
    export VCL_LOFT_RELEASE=bp-vcluster-helmrepo VCL_LOFT_RELEASE_NS=flux-system
    export VCL_LOFT_SLOT_FILE="${case_dir}/absent-slot.yaml"
    export VCL_LOFT_UPSTREAM_REPO="${UPSTREAM}" VCL_LOFT_CHART=vcluster VCL_LOFT_VERSION='0.33.*'
    export VCL_LOFT_LOCAL_PROJECT=openova-io VCL_LOFT_PULL_ATTEMPTS=1
    export SOVEREIGN_FQDN="${FQDN}" HARBOR_USERNAME=admin HARBOR_PASSWORD=x
    export GHCR_PULL_SECRET_NAME=ghcr-pull GHCR_PULL_SECRET_NAMESPACE=flux-system
    export HELMREPO_NAMESPACE=flux-system
    harbor_endpoint="${HARBOR_HOST}"
    trust_secret="cutover-harbor-ca"
    edited=0
    # shellcheck disable=SC1090
    . "${TMP}/phase2c.sh"
  ) >"${case_dir}/out.log" 2>&1
  echo "$?" >"${case_dir}/rc"
}

pivoted_to_local() { # <case> <cluster>
  grep -q "PIVOTED ${LOCAL_URL}" "${TMP}/case-$1/state/$2/pivots.log" 2>/dev/null
}
targeted_with_kubeconfig() { # <case> <cluster>
  grep -q "^$2 patch helmrepository" "${TMP}/case-$1/state/all-calls.log" 2>/dev/null
}

echo "── #5650 step-06 Phase-2c: the loft pivot must cover every region ──"

# ── Case 1 (RED on the pre-fix tree): 2-region Sovereign ───────────────────
# The whole finding. Both regions carry loft on charts.loft.sh; step-08's
# fluxSourceHostLint measures BOTH with an empty allowHosts, so a pivot that
# reaches only the primary does not merely leave half a tether — it makes the
# cutover unable to complete on the multi-region topology the DoD requires.
PRESET_TYPE="" PRESET_UNREACHABLE="" PRESET_NONSTICKY="" run_case two-region "me-east-215-b-1" "${UPSTREAM}" "${UPSTREAM}"
rc=$(cat "${TMP}/case-two-region/rc")
if [ "${rc}" != "0" ]; then
  fail "2-region: Phase-2c exited ${rc}; expected 0. Output:"; sed 's/^/         /' "${TMP}/case-two-region/out.log" | tail -12
else
  pivoted_to_local two-region primary \
    && pass "2-region: primary loft HR pivoted to ${LOCAL_URL}" \
    || fail "2-region: primary loft HR was NOT pivoted to ${LOCAL_URL}"
  if targeted_with_kubeconfig two-region me-east-215-b-1 && pivoted_to_local two-region me-east-215-b-1; then
    pass "2-region: SECONDARY me-east-215-b-1 was targeted with --kubeconfig and pivoted to ${LOCAL_URL}"
  else
    fail "2-region: secondary me-east-215-b-1 was never pivoted — every kubectl in Phase-2c ran against the primary context, so region-b keeps fetching ${UPSTREAM} on its 15m interval and step-08's per-region fluxSourceHostLint fails closed on it (#5650)"
  fi
fi

# ── Case 2 (CONTROL — green on BOTH trees): single-region Sovereign ────────
# No secondary kubeconfigs mounted. The primary must still be pivoted. Deleting
# or disabling Phase-2c to make case 1 "pass" turns this red.
PRESET_TYPE="" PRESET_UNREACHABLE="" PRESET_NONSTICKY="" run_case single-region "" "${UPSTREAM}" ""
rc=$(cat "${TMP}/case-single-region/rc")
if [ "${rc}" = "0" ] && pivoted_to_local single-region primary; then
  pass "CONTROL single-region: primary pivoted to ${LOCAL_URL}, exit 0"
else
  fail "CONTROL single-region: expected exit 0 with the primary pivoted; got rc=${rc}"
fi

# ── Case 3 (CONTROL — green on BOTH trees): slot 60 not installed ──────────
# A Sovereign without bp-vcluster-helmrepo has nothing to pivot. This must be a
# clean no-op, not a FATAL — otherwise the fail-closed cases below could be
# satisfied by failing on everything.
PRESET_TYPE="" PRESET_UNREACHABLE="" PRESET_NONSTICKY="" run_case loft-absent "me-east-215-b-1" "" ""
rc=$(cat "${TMP}/case-loft-absent/rc")
if [ "${rc}" = "0" ] && ! grep -q 'PIVOTED' "${TMP}/case-loft-absent/state"/*/pivots.log 2>/dev/null; then
  pass "CONTROL loft-absent: clean no-op, exit 0, no patch issued"
else
  fail "CONTROL loft-absent: expected a clean no-op; got rc=${rc}"
fi

# ── Case 4 (RED on the pre-fix tree): secondary unreadable ─────────────────
# An unreadable region is not a pivoted region. The pre-fix block never looks at
# a secondary at all, so it exits 0 here — reporting success over a region it
# never measured, which is the #5359 shape.
PRESET_TYPE="" PRESET_UNREACHABLE="me-east-215-b-1" PRESET_NONSTICKY="" run_case secondary-unreadable "me-east-215-b-1" "${UPSTREAM}" "${UPSTREAM}"
rc=$(cat "${TMP}/case-secondary-unreadable/rc")
if [ "${rc}" != "0" ] && grep -q 'unreadable region is NOT a pivoted region' "${TMP}/case-secondary-unreadable/out.log"; then
  pass "fail-closed: an unreadable secondary FATALs instead of recording success"
else
  fail "fail-closed: an unreadable secondary exited ${rc} — a region that could not be measured was treated as pivoted (#5359 #5650)"
fi

# ── Case 5 (RED on the pre-fix tree): patch reports success but reverts ────
# kubectl patch exiting 0 is not the object carrying the new URL. Without the
# read-back the Job logs OK over a region still on charts.loft.sh.
PRESET_TYPE="" PRESET_UNREACHABLE="" PRESET_NONSTICKY="me-east-215-b-1" run_case patch-not-sticky "me-east-215-b-1" "${UPSTREAM}" "${UPSTREAM}"
rc=$(cat "${TMP}/case-patch-not-sticky/rc")
if [ "${rc}" != "0" ] && grep -q 'read-back shows' "${TMP}/case-patch-not-sticky/out.log"; then
  pass "fail-closed: a non-sticky secondary patch is caught by the per-region read-back"
else
  fail "fail-closed: a secondary patch that silently reverted exited ${rc} — the pivot was asserted from the patch's exit code, not from the object (#5650)"
fi

# ── Case 6 (CONTROL — green on BOTH trees): already pivoted ───────────────
# Idempotent re-run: loft is already type=oci on a Sovereign-local URL. Nothing
# to do — no upstream pull, no patch, exit 0. This is the control that keeps the
# fail-closed cases honest: a Phase-2c rewritten to FATAL on everything would
# turn this red.
PRESET_TYPE=oci PRESET_UNREACHABLE="" PRESET_NONSTICKY="" run_case already-oci "me-east-215-b-1" "${LOCAL_URL}" "${LOCAL_URL}"
rc=$(cat "${TMP}/case-already-oci/rc")
if [ "${rc}" = "0" ] && ! grep -q '^pull' "${TMP}/case-already-oci/state/helm-calls.log" 2>/dev/null; then
  pass "CONTROL already-local: exit 0, no upstream pull, on a Sovereign whose loft source is already Sovereign-local"
else
  fail "CONTROL already-local: expected exit 0 with no upstream pull; got rc=${rc}"
fi

echo
if [ "${FAILURES}" -eq 0 ]; then
  echo "PASS — the loft chart-source pivot covers every region step-08 measures, and fails closed on any region it cannot prove (#5650)."
  exit 0
fi
echo "FAIL — ${FAILURES} assertion(s) failed (#5650)."
exit 1
