#!/usr/bin/env bash
# check-no-dead-grant-theater.sh — CI guard that the #3374 Layer-C
# admin-by-default theater (#3685) stays DELETED.
#
# #3374 §3-c / §5 removed the dead grant subsystem from bp-sso-bridge:
#   - grant_operator_admin   — minted a per-Client `admin` role NO app reads
#                              (every app keys admin on the `groups` claim);
#   - grant_operator_realm_roles — per-email catalyst-admin grant, now
#                              SUPERSEDED by the /sovereign-admins group
#                              composite in the keycloak realm import.
# Both logged "skipping admin grant"/"skipping realm-role grant" every 60s
# tick on hw150 (orgEmail="") and NEVER granted anything. The ONE admin
# authority is now membership in /sovereign-admins (DoD box 6/7).
#
# This guard fails CI if the dead function definitions, call sites, or
# "skipping ... grant" log strings reappear in the bp-sso-bridge chart —
# i.e. if a future change resurrects the theater the founder rejected.
#
# It is deliberately NARROW: it scans only the reconciler ConfigMap (the
# bash that ran the grants), and it tolerates the DELETION-NOTE comment
# block that names the removed functions for the next reader.
#
# Usage:  scripts/check-no-dead-grant-theater.sh
# Exit:   0 = clean, 1 = a dead-grant artifact reappeared.

set -euo pipefail

ROOT="${ROOT:-.}"
RECONCILER="${ROOT}/platform/sso-bridge/chart/templates/configmap-reconciler.yaml"
EXIT=0

if [ ! -f "${RECONCILER}" ]; then
  echo "FATAL: ${RECONCILER} not found" >&2
  exit 2
fi

# A line is a VIOLATION only if it is a real shell statement (not a
# comment). We strip leading whitespace then drop comment lines (first
# non-space char is '#') before matching.
noncomment() {
  # shellcheck disable=SC2016
  awk '{ s=$0; sub(/^[[:space:]]+/, "", s); if (substr(s,1,1) != "#") print }' "$1"
}

fail() {
  echo "FAIL: $1" >&2
  EXIT=1
}

NC="$(noncomment "${RECONCILER}")"

# 1. No live function DEFINITIONS of the dead grants.
if printf '%s\n' "${NC}" | grep -Eq 'grant_operator_(admin|realm_roles)[[:space:]]*\(\)'; then
  fail "a dead grant function (grant_operator_admin / grant_operator_realm_roles) was re-defined in the reconciler — #3374 Layer-C deleted these; admin is conferred ONLY by /sovereign-admins membership."
fi

# 2. No live CALL SITES of the dead grants.
if printf '%s\n' "${NC}" | grep -Eq '(^|[[:space:];&|])grant_operator_(admin|realm_roles)[[:space:]]'; then
  fail "a dead grant function is being CALLED in the reconciler — remove the call; the group composite confers catalyst-admin generically."
fi

# 3. No "skipping admin grant" / "skipping realm-role grant" log emitters
#    (the exact dead-tick signature the founder flagged on hw150).
if printf '%s\n' "${NC}" | grep -Eq 'skipping (admin|realm-role) grant'; then
  fail "a 'skipping admin grant'/'skipping realm-role grant' log emitter reappeared — this was the dead-tick theater (#3685). The reconciler no longer grants admin."
fi

# 4. No per-HR realmRolesGranted plumbing (the dead config the per-email
#    grant consumed). The keycloak group composite replaces it.
if printf '%s\n' "${NC}" | grep -Eq 'realmRolesGranted'; then
  fail "per-HR 'realmRolesGranted' plumbing reappeared in the reconciler — admin is conferred by the /sovereign-admins group composite, not a per-app realm-role grant."
fi

if [ "${EXIT}" -eq 0 ]; then
  echo "OK: no dead admin-by-default grant theater in bp-sso-bridge reconciler (#3374 Layer-C / #3685)."
fi
exit "${EXIT}"
