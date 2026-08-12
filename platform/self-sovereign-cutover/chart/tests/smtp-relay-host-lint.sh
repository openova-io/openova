#!/usr/bin/env bash
# #5921 — behavioural test for step-08's outbound customer-auth SMTP relay lint.
#
# THE DEFECT. catalyst-system/marketplace-api takes SMTP_HOST from
# catalyst-system/sovereign-smtp-credentials by secretKeyRef and dials it to
# deliver every customer sign-in code. On hw292 that host is mail.openova.io
# with cutoverComplete=true: a Sovereign certified independent cannot complete
# a purchase unless the OpenOva mothership's mail server answers. ADR-0002
# enumerates EIGHT tethers and outbound customer-auth SMTP is not among them,
# so no cutover step pivots it.
#
# WHY A BEHAVIOURAL TEST AND NOT A RENDER GREP. The lint exists to catch a
# defect that a "does the key exist" assertion cannot see (#5639/#5641), and it
# would be the fourth pre-hold lint in this Job whose only observed state is
# PASS. So this suite extracts the real shell function out of the RENDER and
# drives it against a stub kubectl, asserting the VERDICT in both directions.
# A guard that has only ever been observed passing is not yet known to be a
# guard.
#
# Taking the function from the render rather than from a copy pasted into this
# file is the #5646 reason: a suite validating a hand-kept copy goes on passing
# after the shipped thing changes.
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

awk '/^ *run_smtp_relay_host_lint\(\) \{/,/^ *\}$/' "${TMP}/render.yaml" \
  | sed 's/^ *//' >"${TMP}/fn.sh"

if ! grep -q 'run_smtp_relay_host_lint()' "${TMP}/fn.sh"; then
  echo "FAIL — could not extract run_smtp_relay_host_lint from the rendered Job."
  echo "       The lint is missing from the chart, or the function name changed."
  exit 1
fi
# The awk range terminates on the first bare `}` line. If a nested multi-line
# helper is ever added inside the lint the range truncates and every case below
# would be driving a FRAGMENT — which would still `.`-source cleanly and could
# still return 0. Pin the distinctive TAIL so truncation is a test failure
# rather than a silent change of subject.
if ! grep -q 'NINTH tether' "${TMP}/fn.sh"; then
  echo "FAIL — the extracted function is truncated (its terminal FAIL message is missing)."
  echo "       A nested function or a bare '}' line inside the lint breaks the awk range."
  exit 1
fi

# ── Harness: stub kubectl serves the Secret under test ──────────────────────
# STUB_HOST is the PLAINTEXT smtp-host; the stub base64-encodes it exactly as
# the API server would, so the lint's own decode path is exercised rather than
# bypassed. STUB_RC + STUB_ERR drive the failure branches.
mkdir -p "${TMP}/bin"
cat >"${TMP}/bin/kubectl" <<'STUB'
#!/usr/bin/env bash
if [ -n "${STUB_ERR:-}" ]; then printf '%s\n' "${STUB_ERR}" >&2; exit "${STUB_RC:-1}"; fi
# A warning on stderr alongside a SUCCESSFUL call — the shape that must not be
# mistaken for an unmeasurable relay.
[ -n "${STUB_WARN:-}" ] && printf '%s\n' "${STUB_WARN}" >&2
if [ "${STUB_ABSENT_KEY:-}" = "1" ]; then exit 0; fi
printf '%s' "${STUB_HOST:-}" | base64 | tr -d '\n'
exit 0
STUB
chmod +x "${TMP}/bin/kubectl"

# Runs the lint over one stubbed Secret; echoes PASS or FAIL.
run_case() {
  # Shift ONE at a time: `shift 2` is a no-op when only one argument was
  # passed, which silently left the host in "$@" and fed it to the env loop.
  local host="${1:-}"; shift || true
  local allow="${1:-}"; shift || true
  mkdir -p "${TMP}/work"
  (
    export PATH="${TMP}/bin:${PATH}"
    export SOVEREIGN_FQDN="${FQDN}"
    export SMTP_RELAY_HOST_ALLOW="${allow}"
    export SMTP_RELAY_SECRET_NAMESPACE="catalyst-system"
    export SMTP_RELAY_SECRET_NAME="sovereign-smtp-credentials"
    export WORK_DIR="${TMP}/work"
    export STUB_HOST="${host}"
    # Per-case overrides arrive as NAME=VALUE arguments.
    for kv in "$@"; do export "${kv?}"; done
    cd "${TMP}"
    # shellcheck disable=SC1090
    . "${TMP}/fn.sh"
    if run_smtp_relay_host_lint >"${TMP}/out.txt" 2>&1; then echo PASS; else echo FAIL; fi
  )
}

expect() {
  local desc="$1" want="$2" got="$3"
  if [ "${got}" = "${want}" ]; then pass "${desc} (${got})"
  else fail "${desc}: expected ${want}, got ${got}"; sed 's/^/        /' "${TMP}/out.txt"; fi
}

echo "[smtp-relay-host-lint] #5921 — the lint must fail on the mothership relay and pass on a Sovereign-local one"

# 1. THE REAL DEFECT — the live hw292 finding, byte for byte. Must FAIL.
got=$(run_case "mail.openova.io")
expect "mothership relay mail.openova.io (the live hw292 finding)" FAIL "${got}"
if grep -q 'NINTH tether' "${TMP}/out.txt"; then
  pass "verdict names the tether so an operator can act on it"
else
  fail "verdict did not explain what was found"; sed 's/^/        /' "${TMP}/out.txt"
fi

# 2. POSITIVE CONTROL — the pivoted relay. Must PASS. Without this case a lint
#    that always failed would look correct in case 1.
got=$(run_case "mail.${FQDN}")
expect "Sovereign-local relay mail.<fqdn>" PASS "${got}"

# 3. DISCRIMINATION — the Sovereign's own apex, and an in-cluster relay (the
#    shape a restored slot 95 ClusterIP Service produces).
got=$(run_case "${FQDN}")
expect "the Sovereign's own apex host" PASS "${got}"
got=$(run_case "stalwart-web.stalwart.svc.cluster.local")
expect "in-cluster submission relay" PASS "${got}"
got=$(run_case "localhost")
expect "loopback relay" PASS "${got}"

# 4. VACUITY, THE OTHER DIRECTION — the SAME mothership host declared as a
#    reviewed exception must PASS, proving case 1 failed on the HOST and not on
#    something incidental like the decode or the Secret name.
got=$(run_case "mail.openova.io" "mail.openova.io")
expect "same host with an explicit allowHosts entry" PASS "${got}"

# 5. An allowHosts entry must not be a substring free-for-all: a DIFFERENT
#    third-party relay is still a tether when only one is declared.
got=$(run_case "smtp.sendgrid.net" "mail.openova.io")
expect "undeclared third-party relay while another is allowed" FAIL "${got}"

# 6. A near-miss host that merely ENDS with the FQDN's characters but is not a
#    subdomain of it must FAIL. Without this, a `*.${FQDN}` glob written as a
#    bare substring test would pass on an attacker-shaped host.
got=$(run_case "mail.nothw292.omani.works")
expect "host that is not a subdomain of the Sovereign FQDN" FAIL "${got}"

# 7. Port-bearing hosts must parse to the bare host in BOTH directions, so the
#    port never turns a local relay into a tether or the reverse.
got=$(run_case "mail.${FQDN}:587")
expect "Sovereign-local relay with an explicit port" PASS "${got}"
got=$(run_case "mail.openova.io:587")
expect "mothership relay with an explicit port" FAIL "${got}"

# 8. THE ABSENT-SECRET CARVE-OUT. No Secret means marketplace-api renders
#    SMTP_HOST empty and dials nothing (core/marketplace-api/main.go
#    defaultSMTPHost is ""), which is a broken funnel but not a TETHER — the
#    same treatment the Flux lint gives an uninstalled CRD.
got=$(run_case "" "" 'STUB_ERR=Error from server (NotFound): secrets "sovereign-smtp-credentials" not found' "STUB_RC=1")
expect "absent Secret (no relay configured, not a tether)" PASS "${got}"
got=$(run_case "" "" "STUB_ABSENT_KEY=1")
expect "Secret present but carrying no smtp-host key" PASS "${got}"

# 9. FAIL-CLOSED. Any OTHER read error means the relay could not be measured,
#    and a verdict rendered over an unmeasured surface is the #5633 fail-open
#    shape this Job has already been bitten by twice.
got=$(run_case "" "" 'STUB_ERR=error: You must be logged in to the server (Unauthorized)' "STUB_RC=1")
expect "unreadable Secret (RBAC drift)" FAIL "${got}"

# 10. VACUITY FOR CASE 9 — a kubectl WARNING on stderr accompanies a fully
#     successful call. An earlier draft of this lint keyed on "stderr is
#     non-empty" rather than on the exit code, which would have failed every
#     Sovereign whose kubectl emits a deprecation notice: a guard that goes red
#     on a healthy cluster gets disabled, and then guards nothing.
got=$(run_case "mail.${FQDN}" "" "STUB_WARN=W0810 12:00:00 client-side throttling")
expect "Sovereign-local relay with a kubectl warning on stderr" PASS "${got}"

# 11. FAIL-CLOSED on an unwritable scratch dir — the regression the sibling
#     flux lint shipped and this suite caught there. A lint that cannot record
#     its own measurement must not report PASS.
got=$(run_case "mail.openova.io" "" "WORK_DIR=${TMP}/definitely-not-a-real-dir")
expect "unwritable scratch dir" FAIL "${got}"

# 12. Undecodable bytes are unmeasurable, not absent.
got=$(run_case "" "" "STUB_ABSENT_KEY=0" "STUB_HOST=")
expect "empty smtp-host value" PASS "${got}"

echo
if [ "${FAILURES}" -ne 0 ]; then
  echo "[smtp-relay-host-lint] ${FAILURES} assertion(s) FAILED"
  exit 1
fi
echo "[smtp-relay-host-lint] all assertions passed — the lint is proven to fail on the live hw292 mothership relay AND pass on a Sovereign-local one, with the absent-Secret carve-out and the unmeasurable-relay fail-closed both pinned"
