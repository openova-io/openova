#!/usr/bin/env bash
# catalyst-api-backchannel-platform-only.sh — a per-Organization bp-keycloak
# release must render nothing outside its own namespace, and in particular not
# the Sovereign's catalyst-pin backchannel anchor.
#
# THE DEFECT THIS LOCKS OUT
# ─────────────────────────────────────────────────────────────────────────
# templates/catalyst-api-backchannel-service.yaml renders
# Service catalyst-system/catalyst-api-backchannel, the host the sovereign
# realm's catalyst-pin identity provider calls server-side (#6172). Its guard
# was `sovereignRealm.enabled` alone. The per-Organization HelmRelease
# (products/catalyst/bootstrap/api/internal/handler/organization_gitops.go)
# sets `sovereignRealm.enabled: true` and `realmConfig.tenant.enabled: true`
# and keeps the default catalystAPIInternalURL, so every Organization release
# rendered the SAME Service, in catalyst-system, that the Sovereign's own
# release (keycloak/keycloak) owns.
#
# Measured on hw307 region A, 2026-11-19 (gate check NET-G07 creates two
# Organizations and deletes them; check FND-G07 signs in to 11 surfaces):
#   10:22:38  both Organization releases start `install` while the anchor is
#             absent; one creates it, the other fails with it in its stored
#             manifest ("release is in a failed state")
#   10:52     the Organizations are deleted; Helm uninstall deletes every
#             object of each stored manifest, the anchor included
#   10:58     Keycloak in region A: UnknownHostException
#             catalyst-api-backchannel.catalyst-system.svc.cluster.local;
#             region B: Connection refused (its copy has no local endpoints
#             and ClusterMesh has no region-A copy to merge) — 8 of 11
#             sign-ins end in a 502 at /broker/catalyst-pin/endpoint.
# While the Sovereign's copy exists the same render fails every Organization
# install with "exists and cannot be imported into the current release".
#
# Cases:
#   1. CONTROL   the Sovereign-mode render still renders the anchor in
#                catalyst-system (the gate did not simply remove it)
#   2. VACUITY   the tenant render succeeds and holds the tenant realm, so
#                Cases 3 and 4 cannot pass on an empty or errored render
#   3. the tenant render holds no Service named catalyst-api-backchannel
#   4. the tenant render holds no object in a namespace other than the
#      release namespace (the class, not just this one Service)
#
# Usage:  bash platform/keycloak/chart/tests/catalyst-api-backchannel-platform-only.sh [chart_dir]
# Exit:   0 = correct, 1 = a per-Organization release renders outside its namespace.

set -euo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
anchor="catalyst-api-backchannel"
tenant_ns="acme"
rc=0

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

fail() { echo "FAIL: $*" >&2; rc=1; }

# has <objects> <kind> <namespace> <name> — exact match on one objects() line.
has() {
  awk -F'\t' -v k="$2" -v n="$3" -v m="$4" '$1 == k && $2 == n && $3 == m { found = 1 } END { exit !found }' <<<"$1"
}

# objects <render> — one line per document: kind<TAB>namespace<TAB>name, read
# from the top-level `metadata:` block only (Helm renders its keys at two
# spaces; labels, annotations and data sit deeper). An absent namespace is
# printed as "-" (Helm installs such an object into the release namespace).
objects() {
  awk '
    function flush() { if (kind != "") print kind "\t" (ns == "" ? "-" : ns) "\t" name; kind = ""; ns = ""; name = ""; meta = 0 }
    /^---/                { flush(); next }
    /^kind:/              { kind = $2; next }
    /^metadata:/          { meta = 1; next }
    /^[^ #]/              { meta = 0 }
    meta && /^  name:/      { name = $2; gsub(/"/, "", name) }
    meta && /^  namespace:/ { ns = $2; gsub(/"/, "", ns) }
    END { flush() }
  ' <<<"$1"
}

echo "== Case 1 (control): the Sovereign-mode render renders the anchor in catalyst-system =="
if ! sov="$("$helm" template keycloak "$chart_dir" --namespace keycloak 2>&1)"; then
  echo "FAIL: the Sovereign-mode render errored:" >&2
  printf '%s\n' "$sov" | grep -E '^Error' >&2 || true
  exit 1
fi
if has "$(objects "$sov")" Service catalyst-system "$anchor"; then
  echo "OK — Service catalyst-system/${anchor} is rendered in Sovereign mode."
else
  fail "the Sovereign-mode render has no Service catalyst-system/${anchor}: the gate is too strict and the sovereign realm names a host nothing makes resolve (#6172)."
fi

echo ""
echo "== Case 2 (vacuity): the tenant render succeeds and holds the tenant realm =="
# The values the per-Organization HelmRelease sets that decide this template,
# plus the tenant values the chart requires (it fails the render without them).
if ! ten="$("$helm" template bp-keycloak "$chart_dir" --namespace "$tenant_ns" \
      --set sovereignRealm.enabled=true \
      --set realmConfig.tenant.enabled=true \
      --set realmConfig.tenant.subdomain=acme \
      --set realmConfig.tenant.realmName=org-acme \
      --set realmConfig.tenant.orgSlug=acme \
      --set realmConfig.tenant.parentDomain=omani.homes \
      --set sovereignFQDN=acme.omani.homes 2>&1)"; then
  echo "FAIL: the tenant render errored, so this test cannot judge it:" >&2
  printf '%s\n' "$ten" | grep -E '^Error' >&2 || true
  exit 1
fi
ten_objects="$(objects "$ten")"
if ! has "$ten_objects" ConfigMap "$tenant_ns" bp-keycloak-sovereign-realm-config; then
  echo "FAIL: the tenant render holds no tenant realm ConfigMap; a render this test cannot read would pass Cases 3 and 4 vacuously." >&2
  printf '%s\n' "$ten_objects" >&2
  exit 1
fi
echo "OK — $(printf '%s\n' "$ten_objects" | grep -c .) objects, the tenant realm among them."

echo ""
echo "== Case 3: the tenant render holds no Service ${anchor} =="
hits="$(awk -F'\t' -v m="$anchor" '$1 == "Service" && $3 == m' <<<"$ten_objects")"
if [ -n "$hits" ]; then
  fail "a per-Organization release renders the Sovereign's backchannel anchor; installing two Organizations and deleting them deletes it, and every catalyst-pin sign-in ends in a 502:"
  printf '  %s\n' "$hits" >&2
else
  echo "OK — none."
fi

echo ""
echo "== Case 4: the tenant render holds no object outside namespace ${tenant_ns} =="
outside="$(printf '%s\n' "$ten_objects" | awk -F'\t' -v ns="$tenant_ns" '$2 != "-" && $2 != ns' || true)"
if [ -n "$outside" ]; then
  fail "a per-Organization release renders objects in a namespace it does not own; another release owns them, and this release's uninstall deletes them:"
  printf '  %s\n' "$outside" >&2
else
  echo "OK — every object is in ${tenant_ns}."
fi

echo ""
if [ "$rc" -ne 0 ]; then
  echo "A per-Organization bp-keycloak release must render only into its own namespace." >&2
  echo "Gate Sovereign objects as configmap-sovereign-realm.yaml does:" >&2
  echo "  and .Values.sovereignRealm.enabled (not (and .Values.realmConfig (and .Values.realmConfig.tenant .Values.realmConfig.tenant.enabled)))" >&2
  exit 1
fi
echo "OK: the backchannel anchor is a Sovereign object; a per-Organization release renders nothing outside its namespace."
