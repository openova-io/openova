#!/usr/bin/env bash
# bp-grafana — founder NS-3 (#3263): `sovereign-admins` must map to Grafana
# SERVER admin (GrafanaAdmin), not org-level Admin/Viewer.
#
# Root cause this guards against (hw130 live measurement, 2026-06-12): the
# Sovereign owner landed in Grafana as Viewer. Two coupled halves fix it:
#   (a) bp-keycloak 1.4.24 seeds the owner into `sovereign-admins` in the
#       realm import (the groups claim now carries the group), and
#   (b) THIS chart maps that group to 'GrafanaAdmin' via
#       role_attribute_path AND sets allow_assign_grafana_admin=true —
#       without the flag Grafana silently degrades GrafanaAdmin to org
#       Admin and the Administration nav (users/orgs/plugins/settings)
#       never appears.
#
# Cases:
#   1. Rendered grafana.ini maps sovereign-admins → GrafanaAdmin in
#      role_attribute_path (fallback stays Viewer).
#   2. Rendered grafana.ini sets allow_assign_grafana_admin = true in the
#      [auth.generic_oauth] block (the mapping is inert without it).

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"
helm="${HELM_BIN:-helm}"

"$helm" dependency build "$chart_dir" >/dev/null 2>&1 || true

out=$("$helm" template smoke "$chart_dir" 2>/dev/null)

# The upstream grafana subchart renders grafana.ini into a ConfigMap; pull
# every rendered document's data and scan the [auth.generic_oauth] block.
ini=$(echo "$out" | python3 -c "
import yaml, sys
for d in yaml.safe_load_all(sys.stdin):
    if d and d.get('kind') == 'ConfigMap':
        ini = (d.get('data') or {}).get('grafana.ini')
        if ini and '[auth.generic_oauth]' in ini:
            print(ini)
            sys.exit(0)
sys.exit(1)
")

if [ -z "${ini}" ]; then
  echo "FAIL: no rendered ConfigMap carries a grafana.ini with [auth.generic_oauth]" >&2
  exit 1
fi

oauth_block=$(echo "${ini}" | awk '/^\[auth\.generic_oauth\]/{f=1; next} /^\[/{f=0} f')

# ── Case 1: role_attribute_path maps sovereign-admins → GrafanaAdmin ──────
echo "[sso-admin-role-mapping] Case 1: sovereign-admins → GrafanaAdmin in role_attribute_path (#3263)"
role_line=$(echo "${oauth_block}" | grep "role_attribute_path" || true)
if [ -z "${role_line}" ]; then
  echo "FAIL: role_attribute_path missing from [auth.generic_oauth]" >&2
  exit 1
fi
if ! grep -q "sovereign-admins" <<<"${role_line}"; then
  echo "FAIL: role_attribute_path no longer keys on sovereign-admins: ${role_line}" >&2
  exit 1
fi
if ! grep -q "GrafanaAdmin" <<<"${role_line}"; then
  echo "FAIL: role_attribute_path must grant 'GrafanaAdmin' (server admin), got: ${role_line}" >&2
  echo "      Org-level 'Admin' leaves the owner without the Administration nav (hw130 founder NS-3 measurement)." >&2
  exit 1
fi
if ! grep -q "Viewer" <<<"${role_line}"; then
  echo "FAIL: role_attribute_path lost its non-admin 'Viewer' fallback: ${role_line}" >&2
  exit 1
fi
echo "  PASS"

# ── Case 2: allow_assign_grafana_admin = true ─────────────────────────────
echo "[sso-admin-role-mapping] Case 2: allow_assign_grafana_admin=true (GrafanaAdmin is inert without it) (#3263)"
allow_line=$(echo "${oauth_block}" | grep "allow_assign_grafana_admin" || true)
if [ -z "${allow_line}" ]; then
  echo "FAIL: allow_assign_grafana_admin missing — Grafana degrades GrafanaAdmin to org Admin" >&2
  exit 1
fi
if ! grep -qiE "allow_assign_grafana_admin *= *true" <<<"${allow_line}"; then
  echo "FAIL: allow_assign_grafana_admin must be true, got: ${allow_line}" >&2
  exit 1
fi
echo "  PASS"

echo "[bp-grafana #3263 sso-admin-role-mapping] All cases PASS"
