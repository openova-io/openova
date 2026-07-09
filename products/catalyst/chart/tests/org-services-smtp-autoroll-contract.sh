#!/usr/bin/env bash
# bp-catalyst-platform — org-services SMTP auto-roll + marketplace-api relay render contract (#4919).
#
# WHY THIS GATE EXISTS (#4919, live hw233 walk, funnel row 84 — Pillar-1):
#
# The anonymous signup PIN (POST /api/auth/magic-link → org-services/gateway →
# org-services/auth `sendCodeEmail`) never sent on hw233. Root cause: the auth +
# notification Pods read SMTP_HOST/PORT/FROM/USER/PASS from org-services-secrets
# via secretKeyRef, which the kubelet resolves ONCE at Pod start. The
# sovereign-smtp-credentials seed (sovereign_smtp_seed.go → mail.openova.io:587)
# lands ASYNC from the mothership, so on a fresh prov these Pods can start before
# it. org-services-secrets.yaml's #934 source-wins lookup then bakes the correct
# bytes on the next reconcile, but the running Pod keeps its stale-EMPTY SMTP env
# until an unrelated reconcile happens to roll it — a non-deterministic window in
# which the funnel PIN silently no-sends.
#
# Fix: the catalyst.orgSmtpChecksum helper (_smtp-helpers.tpl) hashes the seed and
# is stamped as a `checksum/smtp-config` pod annotation on auth + notification, so
# Flux DETERMINISTICALLY rolls them onto the populated env the moment the seed
# appears. Separately, marketplace-api's own SMTP env is repointed off the dead
# in-cluster Stalwart host onto the same durable seed.
#
# This gate asserts both so a future revert (annotation dropped, or the stalwart
# hardcode reintroduced) fails Blueprint Release publish BEFORE the OCI artifact
# reaches a Sovereign.
#
# Test framework: pure `helm template` + grep on rendered YAML, matching the
# established tests/nats-url-host-ns-contract.sh pattern. Picked up automatically
# by .github/workflows/blueprint-release.yaml ("Run chart integration tests").
#
# Usage: bash tests/org-services-smtp-autoroll-contract.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

DEAD_SMTP="stalwart-mail.stalwart.svc.cluster.local"
SEED_SECRET="sovereign-smtp-credentials"

render() { # $1=template  -> $TMP/out.yaml
  helm template smoke . \
    --set global.sovereignFQDN=omantel.biz \
    --set ingress.marketplace.enabled=true \
    --set 'ingress.hosts.marketplace.host=marketplace.omantel.biz' \
    --show-only "$1"
}

echo "[smtp-autoroll] Case 1: org-services/auth pod template carries a non-empty checksum/smtp-config annotation (#4919)"
render "templates/org-services/auth.yaml" > "$TMP/auth.yaml"
if ! grep -qE '^[[:space:]]*checksum/smtp-config:[[:space:]]*"[0-9a-f]{64}"' "$TMP/auth.yaml"; then
  echo "FAIL: auth Deployment is missing the checksum/smtp-config auto-roll annotation — a Pod that starts before the SMTP seed lands keeps stale-empty env and the signup PIN silently no-sends (#4919)" >&2
  grep -nE 'checksum/smtp-config|annotations:' "$TMP/auth.yaml" >&2 || true
  exit 1
fi
echo "  PASS (auth carries checksum/smtp-config)"

echo "[smtp-autoroll] Case 2: org-services/notification pod template carries the same auto-roll annotation (#4919)"
render "templates/org-services/notification.yaml" > "$TMP/notification.yaml"
if ! grep -qE '^[[:space:]]*checksum/smtp-config:[[:space:]]*"[0-9a-f]{64}"' "$TMP/notification.yaml"; then
  echo "FAIL: notification Deployment is missing the checksum/smtp-config auto-roll annotation — its SMTP env can freeze stale-empty exactly like auth (#4919)" >&2
  grep -nE 'checksum/smtp-config|annotations:' "$TMP/notification.yaml" >&2 || true
  exit 1
fi
echo "  PASS (notification carries checksum/smtp-config)"

echo "[smtp-autoroll] Case 3: marketplace-api SMTP_HOST sources the durable ${SEED_SECRET} seed, not the dead in-cluster Stalwart host (#4919)"
render "templates/marketplace-api/deployment.yaml" > "$TMP/mp.yaml"
# SMTP_HOST must be a secretKeyRef on the seed Secret, immediately followed by key: smtp-host.
if ! grep -Pzoq "name:\s*SMTP_HOST\s*\n\s*valueFrom:\s*\n\s*secretKeyRef:\s*\n\s*name:\s*${SEED_SECRET}\s*\n\s*key:\s*smtp-host" "$TMP/mp.yaml"; then
  echo "FAIL: marketplace-api SMTP_HOST is not a secretKeyRef on ${SEED_SECRET}/smtp-host (#4919)" >&2
  grep -nA4 'name: SMTP_HOST' "$TMP/mp.yaml" >&2 || true
  exit 1
fi
# Scope to value lines — comments documenting the removed host are allowed (they
# explain why this gate exists), matching Case 4 + the nats-url test philosophy.
if grep -qE '(value|host|SMTP_HOST):.*'"${DEAD_SMTP}" "$TMP/mp.yaml"; then
  echo "FAIL: marketplace-api Deployment still sets the dead in-cluster Stalwart SMTP host '${DEAD_SMTP}' as a value — no Stalwart runs on a fresh Sovereign, so the signup PIN black-holes (#4919)" >&2
  grep -nE '(value|host|SMTP_HOST):.*'"${DEAD_SMTP}" "$TMP/mp.yaml" >&2 || true
  exit 1
fi
echo "  PASS (marketplace-api SMTP_HOST → ${SEED_SECRET}/smtp-host; no stalwart hardcode)"

echo "[smtp-autoroll] Case 4: no live chart template hardcodes the dead Stalwart SMTP host as a value (belt-and-braces)"
# Comments documenting the history of the dead host are allowed (they explain why
# this gate exists), so scope the scan to value lines (value:/host:/SMTP_HOST:).
if grep -rnE '(value|host|SMTP_HOST):.*'"${DEAD_SMTP}" templates/ values.yaml >/dev/null 2>&1; then
  echo "FAIL: a live chart value still points at the dead in-cluster Stalwart SMTP host '${DEAD_SMTP}' (#4919)" >&2
  grep -rnE '(value|host|SMTP_HOST):.*'"${DEAD_SMTP}" templates/ values.yaml >&2 || true
  exit 1
fi
echo "  PASS (no live template value hardcodes ${DEAD_SMTP})"

echo "[smtp-autoroll] ALL CASES PASS — SMTP consumers auto-roll on seed arrival; marketplace-api + org-services source the durable ${SEED_SECRET} relay."
