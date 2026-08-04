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
# #5359 — the SOURCE-HEALTH lint is a second function in the same Job and is
# extracted the same way, from the render, for the same #5646 reason.
awk '/^ *run_flux_source_health_lint\(\) \{/,/^ *\}$/' "${TMP}/render.yaml" \
  | sed 's/^ *//' >"${TMP}/fn-health.sh"

if ! grep -q 'run_flux_source_host_lint()' "${TMP}/fn.sh"; then
  echo "FAIL — could not extract run_flux_source_host_lint from the rendered Job."
  echo "       The lint is missing from the chart, or the function name changed."
  exit 1
fi
if ! grep -q 'run_flux_source_health_lint()' "${TMP}/fn-health.sh"; then
  echo "FAIL — could not extract run_flux_source_health_lint from the rendered Job (#5359)."
  exit 1
fi
# Both extractions terminate on the first bare `}` line. If a nested multi-line
# function definition is ever added inside either lint, the awk range truncates
# and every case below would then be driving a FRAGMENT — which would still
# `.`-source cleanly and could still return 0. Pin the tail explicitly so that
# truncation is a test failure rather than a silent change of subject.
for _f in "${TMP}/fn.sh" "${TMP}/fn-health.sh"; do
  if ! grep -q '^return 0$' "${_f}"; then
    echo "FAIL — ${_f##*/} was truncated during extraction (no trailing 'return 0')."
    echo "       A nested function or a bare '}' line inside the lint breaks the awk range."
    exit 1
  fi
done

# ── Harness: stub kubectl feeds the source inventory under test ─────────────
# #5359 — the stub now scans for the `get` verb rather than assuming argv[2],
# because the lint may prepend `--kubeconfig=<path>` when it measures a
# SECONDARY region. STUB_FAIL_KIND makes a named kind's enumeration fail, which
# is how the fail-closed cases below are driven.
mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
# args: [--kubeconfig=<p>] get <kind> -A -o jsonpath=...
kind=""
want=0
for a in "$@"; do
  if [ "$want" = "1" ]; then kind="$a"; break; fi
  [ "$a" = "get" ] && want=1
done
# Record argv so a case can assert the lint actually TARGETED the region it was
# asked to measure, rather than silently reading the local cluster.
[ -n "${STUB_ARGV_LOG:-}" ] && printf '%s\n' "$*" >>"${STUB_ARGV_LOG}"
if [ -n "${STUB_FAIL_KIND:-}" ] && [ "${kind}" = "${STUB_FAIL_KIND}" ]; then
  echo "${STUB_FAIL_MSG:-error: You must be logged in to the server (Unauthorized)}" >&2
  exit 1
fi
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

# #5359 — same harness, driving the SOURCE-HEALTH lint. Rows are `|`-separated
# (ns|name|suspend|generation|observedGeneration|readyStatus|readyMessage)
# because the empty-middle-field collapse is exactly what the separator choice
# in the lint exists to prevent, and the test must feed the real shape.
run_health_case() {
  local desc="$1"; shift
  local case_dir="${TMP}/hcase"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  : >"${case_dir}/gitrepositories.txt"
  : >"${case_dir}/ocirepositories.txt"
  for spec in "$@"; do
    local kind="${spec%%@*}" row="${spec#*@}"
    printf '%s\n' "${row}" >>"${case_dir}/${kind}.txt"
  done
  mkdir -p "${TMP}/work"
  (
    export PATH="${TMP}/bin:${PATH}"
    export STUB_DIR="${case_dir}"
    export WORK_DIR="${TMP}/work"
    cd "${TMP}"
    # shellcheck disable=SC1090
    . "${TMP}/fn-health.sh"
    if run_flux_source_health_lint "${REGION_LABEL:-primary}" "" >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
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

# ── #5359 — the lint must be PER-REGION and fail closed on an unmeasurable one ──
echo
echo "[flux-source-host-lint] #5359 — per-region targeting + fail-closed enumeration"

# 9. The lint must DIAL the region it was handed. Before #5359 the function
#    always read the local cluster, so a "region-b" verdict was really a
#    second measurement of region-a — a per-region report that is not
#    per-region is worse than no report, because it reads as coverage.
argv_log="${TMP}/argv.log"; : >"${argv_log}"
got=$(
  case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  printf 'flux-system bp-cilium oci://registry.%s/openova-io \n' "${FQDN}" >"${case_dir}/helmrepositories.txt"
  : >"${case_dir}/gitrepositories.txt"; : >"${case_dir}/ocirepositories.txt"
  export PATH="${TMP}/bin:${PATH}" STUB_DIR="${case_dir}" STUB_ARGV_LOG="${argv_log}"
  export SOVEREIGN_FQDN="${FQDN}" LOCAL_REGISTRY_HOST="registry.${FQDN}"
  export FLUX_SOURCE_HOST_ALLOW="" WORK_DIR="${TMP}/work"
  cd "${TMP}"; . "${TMP}/fn.sh"
  if run_flux_source_host_lint "me-east-215-b-1" "/secondary-kubeconfigs/me-east-215-b-1.yaml" >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "secondary region with all-local sources" PASS "${got}"
if grep -q -- '--kubeconfig=/secondary-kubeconfigs/me-east-215-b-1.yaml' "${argv_log}"; then
  pass "lint dialled the SECONDARY kubeconfig (not the local cluster)"
else
  fail "lint did not pass --kubeconfig to kubectl; it measured the local cluster and labelled the verdict 'me-east-215-b-1'"
  sed 's/^/        /' "${argv_log}"
fi

# 10. THE LIVE hw292 SHAPE — region-b holds 64 HelmRepositories on ghcr.io
#     while region-a holds 0. The same function, handed region-b's inventory,
#     must go RED. This is the assertion whose absence let cutoverComplete=true
#     be set from a region-a-only measurement.
got=$(
  case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  { printf 'flux-system bp-cilium oci://ghcr.io/openova-io \n'
    printf 'flux-system bp-keycloak oci://ghcr.io/openova-io \n'; } >"${case_dir}/helmrepositories.txt"
  : >"${case_dir}/gitrepositories.txt"; : >"${case_dir}/ocirepositories.txt"
  export PATH="${TMP}/bin:${PATH}" STUB_DIR="${case_dir}"
  export SOVEREIGN_FQDN="${FQDN}" LOCAL_REGISTRY_HOST="registry.${FQDN}"
  export FLUX_SOURCE_HOST_ALLOW="" WORK_DIR="${TMP}/work"
  cd "${TMP}"; . "${TMP}/fn.sh"
  if run_flux_source_host_lint "me-east-215-b-1" "/secondary-kubeconfigs/me-east-215-b-1.yaml" >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "secondary region still on ghcr.io (the live hw292 finding)" FAIL "${got}"
if grep -q 'EXTERNAL-SOURCE \[me-east-215-b-1\]' "${TMP}/out.txt"; then
  pass "offender lines name the REGION, so an operator can tell which cluster is tethered"
else
  fail "offender lines do not carry the region label"; sed 's/^/        /' "${TMP}/out.txt"
fi

# 11. FAIL-CLOSED. An unreadable secondary kubeconfig used to yield zero rows
#     through `2>/dev/null`, zero offenders, and a PASS — a verdict rendered
#     over a region that was never measured. Against a secondary cluster that
#     is the single most likely failure mode.
got=$(
  case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  : >"${case_dir}/helmrepositories.txt"; : >"${case_dir}/gitrepositories.txt"; : >"${case_dir}/ocirepositories.txt"
  export PATH="${TMP}/bin:${PATH}" STUB_DIR="${case_dir}"
  export STUB_FAIL_KIND="helmrepositories"
  export STUB_FAIL_MSG="error: unable to load kubeconfig: no such file or directory"
  export SOVEREIGN_FQDN="${FQDN}" LOCAL_REGISTRY_HOST="registry.${FQDN}"
  export FLUX_SOURCE_HOST_ALLOW="" WORK_DIR="${TMP}/work"
  cd "${TMP}"; . "${TMP}/fn.sh"
  if run_flux_source_host_lint "me-east-215-b-1" "/secondary-kubeconfigs/me-east-215-b-1.yaml" >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "unreadable secondary (enumeration error)" FAIL "${got}"

# 12. VACUITY THE OTHER WAY for case 11 — a kind that is merely NOT INSTALLED
#     is zero rows, not a tether, and must NOT fail. Without this, case 11
#     would also pass for a lint that failed on every kubectl non-zero exit,
#     which would wedge any Sovereign lacking the OCIRepository CRD.
got=$(
  case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  printf 'flux-system bp-cilium oci://registry.%s/openova-io \n' "${FQDN}" >"${case_dir}/helmrepositories.txt"
  : >"${case_dir}/gitrepositories.txt"; : >"${case_dir}/ocirepositories.txt"
  export PATH="${TMP}/bin:${PATH}" STUB_DIR="${case_dir}"
  export STUB_FAIL_KIND="ocirepositories"
  export STUB_FAIL_MSG="error: the server doesn't have a resource type \"ocirepositories\""
  export SOVEREIGN_FQDN="${FQDN}" LOCAL_REGISTRY_HOST="registry.${FQDN}"
  export FLUX_SOURCE_HOST_ALLOW="" WORK_DIR="${TMP}/work"
  cd "${TMP}"; . "${TMP}/fn.sh"
  if run_flux_source_host_lint "me-east-215-b-1" "/secondary-kubeconfigs/me-east-215-b-1.yaml" >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "uninstalled source kind (not a tether)" PASS "${got}"

# ── #5359 — the SOURCE-HEALTH lint ──────────────────────────────────────────
# The host lint reads a DECLARATION. hw292 proved a declaration can be
# cosmetic: region-b's GitRepository url said local Gitea while
# source-controller had never fetched from it (generation=2,
# observedGeneration=1, Ready=False) and kept serving the github.com artifact
# it had cached before the pivot. These cases pin the discriminator.
echo
echo "[flux-source-host-lint] #5359 — source-HEALTH lint (converged vs cosmetically-pivoted)"

# 13. POSITIVE CONTROL — a converged source. Must PASS, or case 14 proves
#     nothing (a lint that always failed would look correct there).
got=$(run_health_case "converged" \
  "gitrepositories@flux-system|openova|false|2|2|True|stored artifact for revision main@sha1:7bcd1d59")
expect "converged GitRepository (observedGeneration==generation, Ready=True)" PASS "${got}"

# 14. THE LIVE hw292 OBJECT, field for field. Must FAIL.
got=$(run_health_case "hw292-region-b" \
  "gitrepositories@flux-system|openova|false|2|1|False|failed to checkout and determine revision: unable to list remote for 'http://gitea-http.gitea.svc.cluster.local:3000/openova/openova': pkt-line 3: EOF")
expect "hw292 region-b GitRepository (gen=2 obs=1 Ready=False)" FAIL "${got}"
if grep -q 'STALE-SOURCE' "${TMP}/out.txt"; then
  pass "verdict names the object as a STALE-SOURCE"
else
  fail "verdict did not name the stale source"; sed 's/^/        /' "${TMP}/out.txt"
fi

# 15. DISCRIMINATION — the generation lag ALONE must fail, even with Ready=True.
#     This is the exact state the old step-05 wait accepted: the Ready condition
#     still described the PREVIOUS spec. Without this case, cases 13/14 would
#     both be explained by the Ready field alone and the observedGeneration
#     half of the predicate would be untested.
got=$(run_health_case "stale-ready-true" \
  "gitrepositories@flux-system|openova|false|2|1|True|stored artifact for revision main@sha1:4cc85ad9")
expect "Ready=True but observedGeneration behind generation" FAIL "${got}"

# 16. And the Ready half alone must fail too, with generations in step.
got=$(run_health_case "ready-false-in-step" \
  "gitrepositories@flux-system|openova|false|3|3|False|auth required")
expect "converged generations but Ready=False" FAIL "${got}"

# 17. A SUSPENDED source cannot reconcile — reported by the host lint, not
#     failed by the health lint (same treatment, so the two lints agree).
got=$(run_health_case "suspended" \
  "gitrepositories@flux-system|openova|true|2|1|False|suspended")
expect "suspended source" PASS "${got}"

# 18. VACUITY — an empty inventory must not be read as health. A lint that
#     examined nothing and returned 0 is the fail-open shape (#5633).
got=$(
  case_dir="${TMP}/hcase"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  : >"${case_dir}/gitrepositories.txt"; : >"${case_dir}/ocirepositories.txt"
  export PATH="${TMP}/bin:${PATH}" STUB_DIR="${case_dir}"
  export STUB_FAIL_KIND="gitrepositories"
  export STUB_FAIL_MSG="error: Unauthorized"
  export WORK_DIR="${TMP}/work"
  cd "${TMP}"; . "${TMP}/fn-health.sh"
  if run_flux_source_health_lint "me-east-215-b-1" "/secondary-kubeconfigs/me-east-215-b-1.yaml" >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "health lint on an unmeasurable region" FAIL "${got}"

# 19. The `|` separator is load-bearing: with a space separator an ABSENT
#     observedGeneration collapses the field and every later value shifts left,
#     so the reader compares the wrong two things and passes. Feed the shape a
#     never-reconciled object actually has — generation set, observedGeneration
#     and Ready both empty — and require FAIL.
got=$(run_health_case "never-reconciled" \
  "gitrepositories@flux-system|openova|false|1|||")
expect "never-reconciled source (empty observedGeneration and Ready)" FAIL "${got}"

echo
if [ "${FAILURES}" -ne 0 ]; then
  echo "[flux-source-host-lint] ${FAILURES} assertion(s) FAILED"
  exit 1
fi
echo "[flux-source-host-lint] all assertions passed — host lint proven to fail on a tether AND pass on a local source, per-region and fail-closed; health lint proven to fail on the live hw292 cosmetic pivot AND pass on a converged source"
