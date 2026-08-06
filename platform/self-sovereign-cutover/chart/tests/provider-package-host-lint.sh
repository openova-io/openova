#!/usr/bin/env bash
# #5650 — behavioural test for step-08's Crossplane Provider PACKAGE host lint.
#
# WHY this is a separate guard from flux-source-host-lint.sh, not a case added
# to it. fluxSourceHostLint enumerates HelmRepository / GitRepository /
# OCIRepository — Flux's three source kinds — via source-controller. A
# `pkg.crossplane.io/v1.Provider`'s `spec.package` is neither: Crossplane's
# package manager fetches it with its own `remote.Image()` call directly from
# the crossplane-system controller Pod (step-11's header — never through
# containerd, never through Flux), on its own reconcile loop, independent of
# the 600s deny-egress hold. That is the SAME defect class #5650 found on
# vcluster-system/loft — a source with its own timing, invisible to a
# time-boxed hold — one CRD kind wider than the issue's original finding.
# Step-11 (crossplane-provider-pivot) already pivots spec.package durably;
# this is the interval-independent proof that the pivot HELD, matching the
# relationship flux-source-host-lint.sh has to steps 05/06.
#
# Same behavioural-test discipline as flux-source-host-lint.sh: extract the
# real shell function out of the RENDER (not a hand-kept copy — #5646) and
# drive it against a stub kubectl, asserting the VERDICT in both directions.
set -uo pipefail

CHART_DIR="$(cd "$(dirname "$0")/.." && pwd)"
FQDN="hw292.omani.works"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
FAILURES=0

fail() { echo "  FAIL — $*"; FAILURES=$((FAILURES + 1)); }
pass() { echo "  ok   — $*"; }

# ── Extract the function under test straight from the rendered chart ────────
helm template ssc "${CHART_DIR}" --set sovereign.fqdn="${FQDN}" >"${TMP}/render.yaml" 2>"${TMP}/render.err" || {
  echo "helm template failed:"; cat "${TMP}/render.err"; exit 1
}

awk '/^ *run_provider_package_host_lint\(\) \{/,/^ *\}$/' "${TMP}/render.yaml" \
  | sed 's/^ *//' >"${TMP}/fn.sh"

if ! grep -q 'run_provider_package_host_lint()' "${TMP}/fn.sh"; then
  echo "FAIL — could not extract run_provider_package_host_lint from the rendered Job."
  echo "       The lint is missing from the chart, or the function name changed."
  exit 1
fi
# #5646 truncation guard — see flux-source-host-lint.sh for the full rationale.
# If a nested multi-line construct is ever added inside the lint, the awk
# range terminates on the first bare '}' and every case below would then be
# driving a FRAGMENT that can still '.'-source and still return 0.
if ! grep -q '^return 0$' "${TMP}/fn.sh"; then
  echo "FAIL — fn.sh was truncated during extraction (no trailing 'return 0')."
  exit 1
fi

# ── Harness: stub kubectl feeds the Provider inventory under test ───────────
# `providers.pkg.crossplane.io` is CLUSTER-SCOPED — no `-A`, no namespace
# column, unlike the three Flux source kinds. The stub still keys off the
# `get <kind>` argv shape, matching flux-source-host-lint.sh's mechanism.
mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
kind=""
want=0
for a in "$@"; do
  if [ "$want" = "1" ]; then kind="$a"; break; fi
  [ "$a" = "get" ] && want=1
done
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

# Runs the lint over a given Provider inventory (rows of "<name> <spec.package>").
run_case() {
  local case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  : >"${case_dir}/providers.pkg.crossplane.io.txt"
  for row in "$@"; do
    printf '%s\n' "${row}" >>"${case_dir}/providers.pkg.crossplane.io.txt"
  done
  mkdir -p "${TMP}/work"
  (
    export PATH="${TMP}/bin:${PATH}"
    export STUB_DIR="${case_dir}"
    export SOVEREIGN_FQDN="${FQDN}"
    export LOCAL_REGISTRY_HOST="registry.${FQDN}"
    export PROVIDER_PACKAGE_HOST_ALLOW="${ALLOW:-}"
    export WORK_DIR="${TMP}/work"
    cd "${TMP}"
    # shellcheck disable=SC1090
    . "${TMP}/fn.sh"
    if run_provider_package_host_lint "${REGION_LABEL:-primary}" "${REGION_KC:-}" >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
  )
}

expect() {
  local desc="$1" want="$2" got="$3"
  if [ "${got}" = "${want}" ]; then pass "${desc} (${got})"
  else fail "${desc}: expected ${want}, got ${got}"; sed 's/^/        /' "${TMP}/out.txt"; fi
}

echo "[provider-package-host-lint] #5650 — the lint must fail on an external Provider package and pass on a local one"

# 1. POSITIVE CONTROL — a Provider already pivoted, tag-form. Must PASS.
#    Without this, a lint that always failed would look correct in case 2.
ALLOW="" got=$(run_case "provider-hcloud harbor.${FQDN}/proxy-xpkg/crossplane-contrib/provider-hcloud:v0.4.0")
expect "provider already pivoted to Sovereign-local Harbor" PASS "${got}"

# 2. THE REAL DEFECT SHAPE — the bootstrap-kit default, unpivoted. Must FAIL.
ALLOW="" got=$(run_case "provider-hcloud xpkg.upbound.io/crossplane-contrib/provider-hcloud:v0.4.0")
expect "external xpkg.upbound.io package (bootstrap-kit default)" FAIL "${got}"
if grep -q 'EXTERNAL-PROVIDER-PACKAGE' "${TMP}/out.txt"; then
  pass "offender line is labelled EXTERNAL-PROVIDER-PACKAGE"
else
  fail "offender not labelled"; sed 's/^/        /' "${TMP}/out.txt"
fi

# 3. VACUITY, THE OTHER DIRECTION — same package, host declared as a reviewed
#    exception. Must PASS, proving case 2 failed on the HOST and not on
#    something incidental like the name or path.
ALLOW="xpkg.upbound.io" got=$(run_case "provider-hcloud xpkg.upbound.io/crossplane-contrib/provider-hcloud:v0.4.0")
expect "same package with an explicit allowHosts entry" PASS "${got}"

# 4. THE LIVE hw292 SHAPE, verbatim — step-11's actual digest-pinned pivot
#    output (region-a, read-only, 2026-08-06). Must PASS: this is the
#    instance-fix proof for Crossplane, the same role case 3b plays in
#    flux-source-host-lint.sh for the loft HelmRepository.
ALLOW="" got=$(run_case "provider-opentofu harbor.${FQDN}/proxy-xpkg/upbound/provider-opentofu@sha256:4aeddb701725498d06baaf3532ec534c278169f19b5ad89aced615ac8360eb90")
expect "live hw292 digest-pinned pivoted package" PASS "${got}"

# 5. IMPLICIT-REGISTRY SHAPE — a package literal with NO explicit host
#    segment. This is the discriminator against run_podspec_refhost_lint's
#    design: that lint skips a bare "org/name[:tag]" because the kubelet's
#    containerd default-registry mirror redirects it to the local proxy.
#    Crossplane's package manager never traverses containerd (step-11's own
#    header), so there is no redirect here — an implicit ref resolves to the
#    REAL default registry (docker.io) exactly as dialled, and must FAIL.
#    Without this case, a Provider installed via marketplace shorthand would
#    silently pass this lint the way a podspec image ref legitimately does.
ALLOW="" got=$(run_case "provider-shorthand crossplane-contrib/provider-aws-s3:v0.44.0")
expect "implicit-registry package ref (no explicit host, resolves to docker.io)" FAIL "${got}"

# 6. THE MOTHERSHIP, NOT AN UPSTREAM — harbor.openova.io (the OpenOva
#    mothership Harbor, a HOST_PROJECT_MAP host for the podspec lint) must
#    NOT be treated as Sovereign-local here. Crossplane dials spec.package
#    directly, so no containerd redirect exemption applies — mirrors
#    flux-source-host-lint.sh case 6 for the same reason.
ALLOW="" got=$(run_case "provider-x harbor.openova.io/proxy-xpkg/crossplane-contrib/provider-x:v1.0.0")
expect "mothership harbor.openova.io package (no containerd redirect applies)" FAIL "${got}"

# 7. VACUITY — no Providers installed at all (a Sovereign without Crossplane,
#    or one where the CRD exists but zero Provider CRs are present). Must
#    PASS: zero rows is not a tether.
ALLOW="" got=$(run_case)
expect "no Provider CRs present" PASS "${got}"

# 8. FAIL-CLOSED — the CRD kind is genuinely not installed (kubectl errors
#    with a "doesn't have a resource type" shape). Must PASS: distinguishing
#    "not installed" from "could not be measured" is the same discriminator
#    flux-source-host-lint.sh cases 11/12 pin.
got=$(
  case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  export PATH="${TMP}/bin:${PATH}" STUB_DIR="${case_dir}"
  export STUB_FAIL_KIND="providers.pkg.crossplane.io"
  export STUB_FAIL_MSG="error: the server doesn't have a resource type \"providers.pkg.crossplane.io\""
  export SOVEREIGN_FQDN="${FQDN}" LOCAL_REGISTRY_HOST="registry.${FQDN}"
  export PROVIDER_PACKAGE_HOST_ALLOW="" WORK_DIR="${TMP}/work"
  cd "${TMP}"; . "${TMP}/fn.sh"
  if run_provider_package_host_lint "primary" "" >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "Crossplane not installed (CRD absent — not a tether)" PASS "${got}"

# 9. FAIL-CLOSED — a genuine enumeration error (auth/TLS/connection) on a
#    region that IS supposed to have the CRD must FAIL, not silently pass as
#    zero rows. This is the single most likely failure mode against a
#    secondary region and re-mints the exact false positive #5359 found.
got=$(
  case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  export PATH="${TMP}/bin:${PATH}" STUB_DIR="${case_dir}"
  export STUB_FAIL_KIND="providers.pkg.crossplane.io"
  export STUB_FAIL_MSG="error: unable to load kubeconfig: no such file or directory"
  export SOVEREIGN_FQDN="${FQDN}" LOCAL_REGISTRY_HOST="registry.${FQDN}"
  export PROVIDER_PACKAGE_HOST_ALLOW="" WORK_DIR="${TMP}/work"
  cd "${TMP}"; . "${TMP}/fn.sh"
  if run_provider_package_host_lint "me-east-215-b-1" "/secondary-kubeconfigs/me-east-215-b-1.yaml" >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "unreadable secondary (enumeration error)" FAIL "${got}"

# 10. REGRESSION PIN — the same fail-open shape flux-source-host-lint.sh case
#     8 pins: if the offender scratch file cannot be created, every append
#     silently no-ops and an empty `-s` check reports PASS having examined
#     nothing. Must FAIL closed instead.
echo "  (fail-closed: an unwritable scratch dir must not be reported as PASS)"
got=$(
  case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  printf 'provider-hcloud xpkg.upbound.io/crossplane-contrib/provider-hcloud:v0.4.0\n' >"${case_dir}/providers.pkg.crossplane.io.txt"
  export PATH="${TMP}/bin:${PATH}" STUB_DIR="${case_dir}"
  export SOVEREIGN_FQDN="${FQDN}" LOCAL_REGISTRY_HOST="registry.${FQDN}"
  export PROVIDER_PACKAGE_HOST_ALLOW="" WORK_DIR="${TMP}/definitely-not-a-real-dir"
  cd "${TMP}"; . "${TMP}/fn.sh"
  if run_provider_package_host_lint "primary" "" >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "unwritable scratch dir" FAIL "${got}"

# ── Per-region targeting ─────────────────────────────────────────────────────
echo
echo "[provider-package-host-lint] per-region targeting (#5359 shape applied to Providers)"

# 11. The lint must DIAL the region it was handed, not silently read the
#     local (primary) cluster and mislabel the verdict. Providers are
#     typically primary-only on this platform (crossplane-system runs on the
#     primary region), so the all-clear case is a REGION WITH NO PROVIDERS —
#     which must still PASS, and must still have actually targeted the
#     secondary kubeconfig.
argv_log="${TMP}/argv.log"; : >"${argv_log}"
got=$(
  case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  : >"${case_dir}/providers.pkg.crossplane.io.txt"
  export PATH="${TMP}/bin:${PATH}" STUB_DIR="${case_dir}" STUB_ARGV_LOG="${argv_log}"
  export SOVEREIGN_FQDN="${FQDN}" LOCAL_REGISTRY_HOST="registry.${FQDN}"
  export PROVIDER_PACKAGE_HOST_ALLOW="" WORK_DIR="${TMP}/work"
  cd "${TMP}"; . "${TMP}/fn.sh"
  if run_provider_package_host_lint "me-east-215-b-1" "/secondary-kubeconfigs/me-east-215-b-1.yaml" >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "secondary region, no Providers (the common shape on this platform)" PASS "${got}"
if grep -q -- '--kubeconfig=/secondary-kubeconfigs/me-east-215-b-1.yaml' "${argv_log}"; then
  pass "lint dialled the SECONDARY kubeconfig (not the local cluster)"
else
  fail "lint did not pass --kubeconfig to kubectl; it measured the local cluster and labelled the verdict 'me-east-215-b-1'"
  sed 's/^/        /' "${argv_log}"
fi

# 12. A tethered Provider on a SECONDARY region must fail closed too, and the
#     offender line must name the region so an operator can tell which
#     cluster is tethered — same shape flux-source-host-lint.sh case 10 pins.
got=$(
  case_dir="${TMP}/case"; rm -rf "${case_dir}"; mkdir -p "${case_dir}"
  printf 'provider-hcloud xpkg.upbound.io/crossplane-contrib/provider-hcloud:v0.4.0\n' >"${case_dir}/providers.pkg.crossplane.io.txt"
  export PATH="${TMP}/bin:${PATH}" STUB_DIR="${case_dir}"
  export SOVEREIGN_FQDN="${FQDN}" LOCAL_REGISTRY_HOST="registry.${FQDN}"
  export PROVIDER_PACKAGE_HOST_ALLOW="" WORK_DIR="${TMP}/work"
  cd "${TMP}"; . "${TMP}/fn.sh"
  if run_provider_package_host_lint "me-east-215-b-1" "/secondary-kubeconfigs/me-east-215-b-1.yaml" >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
)
expect "secondary region with a tethered Provider" FAIL "${got}"
if grep -q 'EXTERNAL-PROVIDER-PACKAGE \[me-east-215-b-1\]' "${TMP}/out.txt"; then
  pass "offender line names the REGION"
else
  fail "offender line does not carry the region label"; sed 's/^/        /' "${TMP}/out.txt"
fi

echo
if [ "${FAILURES}" -ne 0 ]; then
  echo "[provider-package-host-lint] ${FAILURES} assertion(s) FAILED"
  exit 1
fi
echo "[provider-package-host-lint] all assertions passed — Provider package-host lint proven to fail on an external spec.package (explicit or implicit registry) AND pass on a Sovereign-local one, per-region and fail-closed"
