#!/usr/bin/env bash
# FIXTURE — known-GOOD credential logging: the CONTROL.
#
# Every line here interpolates a credential-named variable and every line is
# legitimate. The guard must stay GREEN over all of them, on the pre-fix tree
# and the post-fix tree alike. If a future tightening of the detector starts
# flagging these, the detector has become noise and will be ignored — which is
# how #5467 survived in the first place.
#
# All values are obviously fake.
set -euo pipefail

ghcr_pat="FAKE-NOT-A-TOKEN-fixture-value-only-0000"
ghcr_user="fake-bot"
admin_password="fake-password-for-fixture-only"

# The canonical safe shape — the fix #5467 landed. Length + account only.
# An operator debugging a pull failure learns everything they need: a
# credential WAS extracted, it is the right length, and it belongs to this
# account. Zero bytes of it are disclosed.
echo "[fixture] ghcr.io auth extracted: user=${ghcr_user} (len=${#ghcr_pat})"

# Set/unset verdict without touching the value.
if [ -n "${admin_password}" ]; then
  echo "[fixture] admin password: set (len=${#admin_password})"
else
  echo "[fixture] admin password: UNSET"
fi

# Kubernetes object NAMES that merely happen to match the credential
# name-stem. These are not values and interpolate whole; the partial-run
# rule must let them through untouched.
TLS_SECRET="my-webhook-tls"
SOURCE_SECRET="harbor-database"
PAT_SOURCE_KEY="token"
echo "[fixture] waiting for secret ${TLS_SECRET}"
echo "[fixture] synced from ${SOURCE_SECRET} key ${PAT_SOURCE_KEY}"

# A hash is not a prefix: it is a one-way digest, discloses no secret
# material, and is the sanctioned way to compare two credentials across
# logs. Emitting it whole must not be flagged.
pat_sha="$(printf '%s' "${ghcr_pat}" | sha256sum | cut -d' ' -f1)"
echo "[fixture] pat sha256=${pat_sha}"
