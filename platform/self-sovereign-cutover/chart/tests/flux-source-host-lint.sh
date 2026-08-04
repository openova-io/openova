#!/usr/bin/env bash
# #5650 — behavioural test for step-08's Flux SOURCE host lint.
#
# WHY a behavioural test and not a render grep. The defect class this lint
# exists to catch was itself created by assertions that check a KEY exists
# rather than what it SAYS (#5639/#5641), and by a CI gate that could not go
# red for two months (#5633/#5626). So this test extracts the real shell
# function out of the rendered Job and drives it against a stub `kubectl`,
# asserting the VERDICT in both directions. A guard that has only ever been
# observed passing is not yet known to be a guard.
#
# The lint's job: after cutover, every non-suspended HelmRepository /
# GitRepository / OCIRepository must resolve to a Sovereign-local host. The
# deny-egress hold cannot cover this, because it is time-boxed and torn down
# while a source keeps reconciling on its own interval.
set -uo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
FQDN="hw292.omani.works"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0

fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

# ── Extract the function under test straight from the rendered chart ────────
# Taking it from the RENDER (not from a copy pasted into this file) is
# deliberate: #5646 showed a test suite validating a hand-kept copy that had
# silently drifted from the shipped source, so it went on passing after the
# real thing changed.
helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

awk '/^ *run_flux_source_host_lint\(\) \{/,/^ *\}$/' "${TMP}/render.yaml" \
  | sed 's/^ *//' >"${TMP}/fn.sh"

if ! grep -q 'run_flux_source_host_lint()' "${TMP}/fn.sh"; then
  echo "FAIL — could not extract run_flux_source_host_lint from the rendered Job."
  echo "       The lint is missing from the chart, or the function name changed."
  exit 1
fi

# ── Harness: stub kubectl feeds the source inventory under test ─────────────
mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
# args: get <kind> -A -o jsonpath=...
kind="$2"
f="${STUB_DIR}/${kind}.txt"
[ -f "$f" ] && cat "$f"
exit 0
STUB
chmod +x "${TMP}/bin/kubectl"

# Runs the lint over a given inventory; echoes PASS or FAIL.
run_case() {
  local desc="$1" allow="$2"; shift 2
  local case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  : >"${case_dir}/helmrepositories.txt"
  : >"${case_dir}/gitrepositories.txt"
  : >"${case_dir}/ocirepositories.txt"
  for spec in "$@"; do
    local kind="${spec%%|*}" row="${spec#*|}"
    printf '%s\n' "${row}" >>"${case_dir}/${kind}.txt"
  done
  mkdir -p "${TMP}/work"
  (
    export PATH="${TMP}/bin:${PATH}"
    export STUB_DIR="${case_dir}"
    export SOVEREIGN_FQDN="${FQDN}"
    export LOCAL_REGISTRY_HOST="registry.${FQDN}"
    export FLUX_SOURCE_HOST_ALLOW="${allow}"
    export WORK_DIR="${TMP}/work"
    cd "${TMP}"
    # shellcheck disable=SC1090
    . "${TMP}/fn.sh"
    if run_flux_source_host_lint >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
  )
}

expect() {
  local desc="$1" want="$2" got="$3"
  if [ "${got}" = "${want}" ]; then pass "${desc} (${got})"
  else fail "${desc}: expected ${want}, got ${got}"; sed 's/^/        /' "${TMP}/out.txt"; fi
}

echo "[flux-source-host-lint] #5650 — the lint must fail on an external source and pass on a local one"

# 1. POSITIVE CONTROL — all sources local. Must PASS.
#    Without this case a lint that always failed would look correct in case 2.
got=$(run_case "all-local" "" \
  "helmrepositories|flux-system bp-cilium oci://registry.${FQDN}/openova-io " \
  "helmrepositories|flux-system bp-agenity oci://registry.${FQDN}/openova-io " \
  "gitrepositories|flux-system catalog https://gitea.${FQDN}/catalog/openova ")
expect "all-local sources" PASS "${got}"

# 2. THE REAL DEFECT — the live hw292 finding. Must FAIL.
got=$(run_case "loft-tether" "" \
  "helmrepositories|flux-system bp-cilium oci://registry.${FQDN}/openova-io " \
  "helmrepositories|vcluster-system loft https://charts.loft.sh ")
expect "external charts.loft.sh source" FAIL "${got}"

# 3. VACUITY, THE OTHER DIRECTION — same inventory, host declared as a
#    reviewed exception. Must PASS, proving case 2 failed on the HOST and not
#    on something incidental like the namespace or the extra row.
got=$(run_case "loft-allowed" "charts.loft.sh" \
  "helmrepositories|flux-system bp-cilium oci://registry.${FQDN}/openova-io " \
  "helmrepositories|vcluster-system loft https://charts.loft.sh ")
expect "same source with an explicit allowHosts entry" PASS "${got}"

# 4. A suspended source cannot reconcile — reported, but must not fail.
got=$(run_case "loft-suspended" "" \
  "helmrepositories|vcluster-system loft https://charts.loft.sh true")
expect "suspended external source" PASS "${got}"

# 5. Other Flux source kinds are covered too, not just HelmRepository.
got=$(run_case "git-tether" "" \
  "gitrepositories|flux-system upstream https://github.com/openova-io/openova ")
expect "external GitRepository" FAIL "${got}"
got=$(run_case "oci-tether" "" \
  "ocirepositories|flux-system up oci://ghcr.io/openova-io/bp-x ")
expect "external OCIRepository" FAIL "${got}"

# 6. The exemption set must NOT inherit podspecRefHostLint's containerd
#    redirect logic. source-controller dials these URLs itself and never
#    traverses containerd, so a HOST_PROJECT_MAP entry cannot cover a source
#    object — harbor.openova.io is the mothership and must stay a failure.
got=$(run_case "mothership-harbor" "" \
  "helmrepositories|flux-system bp-x oci://harbor.openova.io/openova-io ")
expect "mothership harbor.openova.io source (no containerd redirect applies)" FAIL "${got}"

# 7. Port-bearing and path-bearing URLs must parse to the bare host.
got=$(run_case "port-parse" "" \
  "helmrepositories|flux-system bp-x oci://registry.${FQDN}:5000/openova-io/deep/path ")
expect "local host with port and path" PASS "${got}"

# 8. REGRESSION PIN for a fail-open bug this suite caught during development.
#    The lint records offenders in a scratch file. When that file could not be
#    created, every append silently no-op'd, `-s` was false, and the lint
#    returned PASS having examined nothing — a guard that reported success from
#    an empty measurement. It must fail CLOSED instead.
echo "  (fail-closed: an unwritable scratch dir must not be reported as PASS)"
got=$(
  export PATH="${TMP}/bin:${PATH}"
  export STUB_DIR="${TMP}/case"
  export SOVEREIGN_FQDN="${FQDN}"
  export LOCAL_REGISTRY_HOST="registry.${FQDN}"
  export FLUX_SOURCE_HOST_ALLOW=""
  export WORK_DIR="${TMP}/definitely-not-a-real-dir"
  cd "${TMP}"
  # shellcheck disable=SC1090
  . "${TMP}/fn.sh"
  if run_flux_source_host_lint >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "unwritable scratch dir" FAIL "${got}"

echo
if [ "${FAILURES}" -ne 0 ]; then
  echo "[flux-source-host-lint] ${FAILURES} assertion(s) FAILED"
  exit 1
fi
echo "[flux-source-host-lint] all assertions passed — lint proven to fail on a tether AND pass on a local source"
