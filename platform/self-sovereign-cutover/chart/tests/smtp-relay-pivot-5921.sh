#!/usr/bin/env bash
# Render guard for step-07a smtp-relay-pivot (#5921).
#
# Vacuity first: this suite asserts on rendered CONTENT, so it opens by proving
# the template renders anything at all. A grep-based guard that silently matches
# an empty render is the failure mode this repo keeps re-learning.
set -euo pipefail

CHART="$(cd "$(dirname "$0")/.." && pwd)"
TPL="templates/07a-smtp-relay-pivot-job.yaml"
fail() { echo "FAIL: $*" >&2; exit 1; }

render() { helm template t "$CHART" -s "$TPL" "$@" 2>/dev/null; }

# ── 0. vacuity ────────────────────────────────────────────────────────────
out="$(render)"
[ -n "$out" ] || fail "template rendered EMPTY — every assertion below would pass vacuously"
echo "$out" | grep -q 'kind: Job' || fail "no Job in render"

# ── 1. the pivot targets the Sovereign-local relay, not the mothership ────
echo "$out" | grep -q 'stalwart-web.stalwart.svc.cluster.local' \
  || fail "TARGET_HOST is not the Sovereign-local Stalwart"
echo "$out" | grep -q 'mail\.openova\.io' \
  && fail "render still names mail.openova.io as a target — that is the tether"

# ── 2. it must patch the Secret the seed actually writes ─────────────────
echo "$out" | grep -q 'sovereign-smtp-credentials' || fail "does not name the SMTP Secret"
echo "$out" | grep -q 'catalyst-system'             || fail "does not name the Secret namespace"

# ── 3. fail-loud gates: probe before pivot, readback after ───────────────
# Without these the step reports success while having changed nothing real.
echo "$out" | grep -q 'did not answer within' \
  || fail "no pre-pivot reachability gate — would cut sign-in onto a dead relay"
echo "$out" | grep -q 'readback mismatch' \
  || fail "no post-patch readback — a patch that does not land would report green"
echo "$out" | grep -q 'has no smtp-host key' \
  || fail "no missing-key gate — a no-op would be indistinguishable from a pivot"

# ── 4. consumers get rolled, else the pivot sits unread ──────────────────
echo "$out" | grep -q 'rollout restart' || fail "consumers are never rolled"
echo "$out" | grep -q 'marketplace-api' || fail "marketplace-api not in the consumer set"

# ── 5. CONTROL: the step is disableable, and disabling it renders NOTHING ─
# Proves the enabled flag is actually wired rather than decorative.
off="$(render --set smtpRelayPivot.enabled=false || true)"
[ -z "$(echo "${off}" | grep -c 'kind: Job' | grep -v '^0$' || true)" ] \
  || fail "smtpRelayPivot.enabled=false still rendered a Job"

echo "PASS: step-07a pivots SMTP to the Sovereign-local relay, with probe + readback gates."
