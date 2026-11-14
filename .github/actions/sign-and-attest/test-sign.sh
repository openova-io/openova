#!/usr/bin/env bash
# Test of the step "Sign, attest and verify" of .github/actions/sign-and-attest/action.yaml.
#
# It runs the body of that step with stand-in `cosign` and Syft commands that only record their
# arguments, in six cases, and checks which cosign commands ran:
#   1. on main with the release key: the keyless signature, the release-key signature without a
#      log upload, the attestation, the two keyless verifications and the release-key verification
#      against keys/release-cosign.pub, in this order;
#   2. on main without the key: the keyless commands only, and a warning;
#   3. on another ref with the key: the keyless commands only, and a notice;
#   4. on main with the key and no public key file: the step fails before cosign runs;
#   5. on main with the key, when the release-key verification fails: the step fails;
#   6. on main with the key, when the release-key signature fails: the step fails.
# It needs bash and Python 3 with PyYAML. No network, no registry, no cosign.
#
# Run from the repository root: bash .github/actions/sign-and-attest/test-sign.sh
# Exit 0: every case passed. Exit 1: a case failed; the lines above name it.

set -euo pipefail

ACTION="$(cd "$(dirname "$0")" && pwd)/action.yaml"
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# The body of the step "Sign, attest and verify", exactly as the runner gets it.
python3 - "$ACTION" "${WORK}/step.sh" <<'PY'
import sys
import yaml

steps = yaml.safe_load(open(sys.argv[1], encoding="utf-8"))["runs"]["steps"]
body = [s["run"] for s in steps if s.get("name") == "Sign, attest and verify"]
if len(body) != 1:
    sys.exit("test-sign: the action has %d steps named 'Sign, attest and verify'" % len(body))
with open(sys.argv[2], "w", encoding="utf-8") as handle:
    handle.write(body[0])
PY

# Stand-in commands. cosign writes one line per call to $CALLS: its arguments, and for a call
# with --key whether the key and its password reached it. STUB_FAIL makes the call whose
# arguments start with that text fail.
mkdir -p "${WORK}/bin"
cat > "${WORK}/bin/cosign" <<'SH'
#!/usr/bin/env bash
line="$*"
case " $* " in
  *" --key env://RELEASE_KEY "*) line="${line} [key=${RELEASE_KEY:-} password=${COSIGN_PASSWORD:-}]" ;;
esac
echo "$line" >> "$CALLS"
if [ -n "${STUB_FAIL:-}" ] && [[ "$*" == "${STUB_FAIL}"* ]]; then
  exit 1
fi
exit 0
SH
cat > "${WORK}/bin/syft" <<'SH'
#!/usr/bin/env bash
if [ "${1:-}" = version ]; then
  echo "Version: 1.43.0"
  exit 0
fi
for arg in "$@"; do
  case "$arg" in
    spdx-json=*) echo '{"spdxVersion": "SPDX-2.3"}' > "${arg#spdx-json=}" ;;
  esac
done
SH
chmod +x "${WORK}/bin/cosign" "${WORK}/bin/syft"

DIGEST="sha256:$(printf 'a%.0s' $(seq 64))"
IMAGE="ghcr.io/openova-io/openova/test@${DIGEST}"
IDENTITY='^https://github\.com/openova-io/openova/\.github/workflows/'
ISSUER="https://token.actions.githubusercontent.com"
mkdir -p "${WORK}/workspace/keys"
printf -- '-----BEGIN PUBLIC KEY-----\ntest\n-----END PUBLIC KEY-----\n' > "${WORK}/workspace/keys/release-cosign.pub"

FAILED=0

# run_case <name> <ref> <key> <public key path> <text a failing cosign call starts with>
# Runs the step; leaves its exit code in $CODE, its output in $OUT and the cosign calls in $CALLS.
run_case() {
  CASE="$1"
  CALLS="${WORK}/calls-${CASE}"
  OUT="${WORK}/out-${CASE}"
  : > "$CALLS"
  mkdir -p "${WORK}/temp-${CASE}"
  set +e
  env PATH="${WORK}/bin:${PATH}" CALLS="$CALLS" STUB_FAIL="$5" \
    REF="ghcr.io/openova-io/openova/test" DIGEST="$DIGEST" SBOM_SOURCE="" \
    SYFT="${WORK}/bin/syft" ISSUER="$ISSUER" \
    RELEASE_KEY="$3" RELEASE_KEY_PASSWORD="test-password" RELEASE_PUBLIC_KEY="$4" \
    GITHUB_REF="$2" GITHUB_REPOSITORY="openova-io/openova" RUNNER_TEMP="${WORK}/temp-${CASE}" \
    bash -e "${WORK}/step.sh" > "$OUT" 2>&1
  CODE=$?
  set -e
}

fail() {
  echo "test-sign: FAIL case ${CASE}: $1"
  echo "--- output of the step:"
  cat "$OUT"
  echo "--- cosign calls:"
  cat "$CALLS"
  FAILED=1
}

KEYLESS_SIGN="sign --yes ${IMAGE}"
KEY_SIGN="sign --yes --key env://RELEASE_KEY --tlog-upload=false ${IMAGE} [key=test-key password=test-password]"
ATTEST="attest --yes --type spdxjson --predicate ${WORK}/temp-CASE/sbom-${DIGEST#sha256:}.spdx.json ${IMAGE}"
VERIFY="verify --certificate-identity-regexp ${IDENTITY} --certificate-oidc-issuer ${ISSUER} ${IMAGE}"
VERIFY_ATT="verify-attestation --type spdxjson --certificate-identity-regexp ${IDENTITY} --certificate-oidc-issuer ${ISSUER} ${IMAGE}"
KEY_VERIFY="verify --key ${WORK}/workspace/keys/release-cosign.pub --insecure-ignore-tlog=true ${IMAGE}"

# expect_calls <line> ...: the cosign calls of the case are exactly these lines, in this order.
expect_calls() {
  local want
  want="$(printf '%s\n' "$@" | sed "s#temp-CASE#temp-${CASE}#")"
  if [ "$(cat "$CALLS")" != "$want" ]; then
    fail "the cosign calls differ; expected:
${want}"
  fi
}

# 1. main, with the key.
run_case with-key refs/heads/main test-key "${WORK}/workspace/keys/release-cosign.pub" ""
if [ "$CODE" -ne 0 ]; then fail "exit code ${CODE}, expected 0"; fi
expect_calls "$KEYLESS_SIGN" "$KEY_SIGN" "$ATTEST" "$VERIFY" "$VERIFY_ATT" "$KEY_VERIFY"
grep -q "carries the release-key signature" "$OUT" || fail "no line saying the release-key signature is there"

# 2. main, without the key.
run_case no-key refs/heads/main "" "${WORK}/workspace/keys/release-cosign.pub" ""
if [ "$CODE" -ne 0 ]; then fail "exit code ${CODE}, expected 0"; fi
expect_calls "$KEYLESS_SIGN" "$ATTEST" "$VERIFY" "$VERIFY_ATT"
grep -q "^::warning::sign-and-attest: no release key was passed" "$OUT" || fail "no warning that the key is missing"

# 3. another ref, with the key.
run_case other-ref refs/heads/wp/test test-key "${WORK}/workspace/keys/release-cosign.pub" ""
if [ "$CODE" -ne 0 ]; then fail "exit code ${CODE}, expected 0"; fi
expect_calls "$KEYLESS_SIGN" "$ATTEST" "$VERIFY" "$VERIFY_ATT"
grep -q "^::notice::sign-and-attest: the release key is used on refs/heads/main only" "$OUT" || fail "no notice that the key is used on main only"

# 4. main, with the key, without the public key file.
run_case no-public-key refs/heads/main test-key "${WORK}/workspace/keys/missing.pub" ""
if [ "$CODE" -eq 0 ]; then fail "exit code 0, expected a failure"; fi
expect_calls
grep -q "^::error::sign-and-attest: the release key was passed" "$OUT" || fail "no error about the missing public key"

# 5. main, with the key, the release-key verification fails.
run_case bad-key-verify refs/heads/main test-key "${WORK}/workspace/keys/release-cosign.pub" "verify --key"
if [ "$CODE" -eq 0 ]; then fail "exit code 0, expected a failure"; fi
expect_calls "$KEYLESS_SIGN" "$KEY_SIGN" "$ATTEST" "$VERIFY" "$VERIFY_ATT" "$KEY_VERIFY"

# 6. main, with the key, the release-key signature fails.
run_case bad-key-sign refs/heads/main test-key "${WORK}/workspace/keys/release-cosign.pub" "sign --yes --key"
if [ "$CODE" -eq 0 ]; then fail "exit code 0, expected a failure"; fi
expect_calls "$KEYLESS_SIGN" "$KEY_SIGN"

if [ "$FAILED" -ne 0 ]; then
  echo "test-sign: failed"
  exit 1
fi
echo "test-sign: ok, 6 cases of the step 'Sign, attest and verify' passed"
