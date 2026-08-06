#!/usr/bin/env bash
# FIXTURE — known-BAD credential-prefix logging. NOT executed by anything;
# it exists so scripts/check-no-credential-prefix-logging.sh --self-test can
# prove the detector goes RED. Every value below is obviously fake.
#
# This file must NEVER be "fixed". If a cleanup pass makes these lines safe,
# the guard silently becomes vacuous and #5467 walks straight back in.
set -euo pipefail

ghcr_pat="FAKE-NOT-A-TOKEN-fixture-value-only-0000"
ghcr_user="fake-bot"
api_token="fake-token-value-for-fixture-only"
admin_password="fake-password-for-fixture-only"
client_secret="fake-client-secret-for-fixture-only"

# 1. The literal #5467 shape — bash substring prefix.
echo "[fixture] ghcr.io auth extracted: user=${ghcr_user} pat=${ghcr_pat:0:8}... (len=${#ghcr_pat})"

# 2. The same disclosure with the offset omitted — a rewrite that any
#    grep-for-":0:8" guard would sail straight past.
echo "[fixture] token head=${api_token::6}"

# 3. Suffix disclosure. Equally a secret; equally caught, because the
#    detector looks at emitted BYTES, not at the spelling of the operator.
echo "[fixture] password tail=${admin_password: -5}"

# 4. Split across two truncations so no single expression looks like a
#    prefix — still emits 8 real characters.
echo "[fixture] secret=${client_secret:0:4}${client_secret:4:4}"

# 5. printf precision, a form with no colon in it at all.
printf '[fixture] pat=%.8s\n' "${ghcr_pat}"
