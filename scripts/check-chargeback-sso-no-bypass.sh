#!/usr/bin/env bash
# check-chargeback-sso-no-bypass.sh — #6841
#
# chargeback shipped its OWN PIN sign-in on a Sovereign that already has SSO,
# so a user signed in at the console was asked to sign in a second time — and
# the PIN never arrived, because SMTP is unset and the binary only logs it.
#
# The fix has TWO halves and either alone is wrong:
#   A. bp-oidc-gate (slot 13c) owns chargeback.<fqdn> and verifies the browser
#   B. slot 13f disables chargeback's own HTTPRoute and names the trusted header
#
# Half A without B  -> two HTTPRoutes on one hostname (undefined routing) AND a
#                      public door that bypasses the gate carrying a SPOOFABLE
#                      identity header: anyone could assume the operator role.
# Half B without A  -> the app trusts a header nothing sets; no one can sign in.
#
# This gate pins both halves and the chart-level fail-closed rule.
set -euo pipefail

KIT="${1:-clusters/_template/bootstrap-kit}"
CHART="${2:-products/chargeback/chart}"
GATE="$KIT/13c-bp-oidc-gate.yaml"
CB="$KIT/13f-bp-chargeback.yaml"
fail=0

for f in "$GATE" "$CB"; do
  [[ -f "$f" ]] || { echo "FAIL: $f not found"; exit 1; }
done

# --- half A: the gate declares a chargeback instance --------------------
if grep -qE '^\s*-\s*name:\s*chargeback\s*$' "$GATE"; then
  echo "  ok: bp-oidc-gate declares a chargeback instance"
else
  echo "FAIL: $GATE has no 'chargeback' oidc-gate instance — nothing authenticates the app,"
  echo "      so users get chargeback's own PIN prompt instead of Sovereign SSO (#6841)."
  fail=1
fi

# --- half B: the app's own route is off and the header is named ----------
if grep -qE '^\s*header:\s*X-Auth-Request-Email\s*$' "$CB"; then
  echo "  ok: slot 13f names the trusted forward-auth header"
else
  echo "FAIL: $CB does not set forwardAuth.header — chargeback will prompt for its own PIN"
  echo "      even behind the gate (#6841)."
  fail=1
fi

# httpRoute.enabled must be false in the chargeback slot
if awk '/^\s*httpRoute:/{f=1;next} f&&/^\s*enabled:\s*false\s*$/{print "off";exit} f&&/^\s*[a-zA-Z]/{f=0}' "$CB" | grep -q off; then
  echo "  ok: chargeback's own HTTPRoute is disabled (the gate owns the hostname)"
else
  echo "FAIL: $CB does not disable httpRoute. The app would keep a public door that"
  echo "      BYPASSES the gate while trusting a spoofable identity header (#6841)."
  fail=1
fi

# --- the chart itself must refuse the spoofable pair ---------------------
if command -v helm >/dev/null 2>&1; then
  if helm template cb "$CHART" --set config.sovereignFqdn=t99.omani.works \
       --set forwardAuth.header=X-Auth-Request-Email --set httpRoute.enabled=true \
       >/dev/null 2>&1; then
    echo "FAIL: the chart RENDERED forwardAuth.header together with httpRoute.enabled=true."
    echo "      That combination is spoofable and must be refused at render time."
    fail=1
  else
    echo "  ok: chart refuses forwardAuth + own HTTPRoute (fail-closed)"
  fi
  # vacuity control: the safe combination must still render
  if helm template cb "$CHART" --set config.sovereignFqdn=t99.omani.works \
       --set forwardAuth.header=X-Auth-Request-Email --set httpRoute.enabled=false \
       >/dev/null 2>&1; then
    echo "  ok: the gated combination still renders (control — the refusal is targeted)"
  else
    echo "FAIL: the SAFE combination does not render — the fail-closed rule is over-broad."
    fail=1
  fi
else
  echo "  note: helm not present, skipped the chart render checks"
fi

if [[ $fail -ne 0 ]]; then
  echo "FAILED — chargeback SSO wiring is incomplete or bypassable."
  exit 1
fi
echo "PASS — chargeback is gated by Sovereign SSO with no bypass."
