#!/usr/bin/env bash
# #5491 — §854's NodePort prohibition must be ENFORCED, not merely audited.
#
# It ran in Audit for months: the policy RECORDED a NodePort and admitted it,
# while six sibling policies enforced. The flip criterion the chart itself
# names was met (9 clean enumerations, 670 PolicyReport passes / 0 fails,
# bootstrapMode already false post-handover) and nobody had walked through it.
# Since CMP-079 it is a ValidatingPolicy with a fixed [Deny] whatever the values.
#
# This guard fails if anyone flips it back, AND refuses to report a pass when
# the policy does not render at all — a grep for "Deny" against an empty
# render succeeds trivially, which is how a guard silently stops guarding.
set -euo pipefail
cd "$(dirname "$0")/.."

# Every read of a render is a here-string, never `printf | grep -q` or
# `helm | awk '...exit'`: a reader that stops early makes the writer die of
# SIGPIPE once the render is larger than a pipe buffer, and pipefail turns
# that into a false failure (seen when the render grew past 64 KiB).

render() { helm template kyverno-policies . --show-only templates/baseline/24-forbid-nodeport-service.yaml "$@" 2>/dev/null; }

OUT="$(render)"

# ── vacuity control: the policy object must EXIST in the render ────────────
if ! grep -q -x 'kind: ValidatingPolicy' <<<"$OUT" || ! grep -q -x '  name: forbid-nodeport-service' <<<"$OUT"; then
  echo "ERROR (#5491): forbid-nodeport-service does not render as a ValidatingPolicy — this guard" >&2
  echo "               cannot judge an absent policy. Refusing to report a pass." >&2
  exit 2
fi
echo "[nodeport-enforce] vacuity OK — forbid-nodeport-service renders"

# ── the assertion, in every case of the values ────────────────────────────
deny_in() { # <label> [helm arguments...]
  local label="$1" actions
  shift
  actions="$(awk '/^  validationActions:/{print $2; exit}' <<<"$(render "$@")")"
  if [ "$actions" != "[Deny]" ]; then
    echo "FAIL (#5491): $label: forbid-nodeport-service validationActions=${actions:-<unset>}, want [Deny]." >&2
    echo "  §854 is a founder ABSOLUTE ban. In Audit the policy records a NodePort" >&2
    echo "  and admits it, so every 'zero NodePorts' audit measures OUTCOME, not" >&2
    echo "  PREVENTION. Do not relax this without clearing #5491." >&2
    exit 1
  fi
  echo "[nodeport-enforce] PASS — $label: validationActions=[Deny]"
}
deny_in "defaults (the bootstrap window open)"
deny_in "bootstrapMode=false" --set compliancePolicies.bootstrapMode=false
deny_in "sovereignConsume=local" --set compliancePolicies.sovereignConsume=local
deny_in "action=Audit" --set compliancePolicies.forbidNodePortService.action=Audit
deny_in "action=Audit, bootstrapMode=false" --set compliancePolicies.forbidNodePortService.action=Audit,compliancePolicies.bootstrapMode=false

# ── the carve-out must survive, or Enforce breaks cert issuance ───────────
if ! grep -q 'acme.cert-manager.io/http01-solver' <<<"$OUT"; then
  echo "FAIL (#5491): the cert-manager HTTP-01 solver carve-out is GONE." >&2
  echo "  cert-manager auto-creates short-lived type=NodePort solver Services;" >&2
  echo "  under Enforce, losing this carve-out blocks ACME challenges and" >&2
  echo "  certificate issuance stops. The carve-out is load-bearing now." >&2
  exit 1
fi
echo "[nodeport-enforce] PASS — cert-manager HTTP-01 solver carve-out intact"
echo "[nodeport-enforce] ALL PASS"
