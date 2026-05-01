#!/usr/bin/env bash
# bp-keycloak OIDC kubectl-client integration test (issue #326).
#
# Verifies the kubectl OIDC client + sovereign realm are wired through
# the canonical keycloakConfigCli seam:
#
#   1. The post-install/post-upgrade/post-rollback Helm hook Job
#      `*-keycloak-keycloak-config-cli` is rendered (config-cli is enabled).
#   2. The ConfigMap carrying the realm payload contains a `kubectl`
#      client with publicClient=true and the localhost:8000 redirect URI
#      kubectl-oidc-login uses by default.
#   3. The realm name is `sovereign` — invariant per Sovereign so the
#      api-server's --oidc-issuer-url flag (in
#      infra/hetzner/cloudinit-control-plane.tftpl) resolves consistently.
#   4. The `groups` claim mapper is present so the api-server's
#      --oidc-groups-claim flag binds id-token groups to RoleBinding
#      subjects (oidc:<group> per --oidc-groups-prefix).
#
# Companion to docs/omantel-handover-wbs.md §11 "kubectl OIDC for
# customer admins" — those runbook commands assume the realm + client
# this chart ships, so this test guards against silent drift.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

echo "[oidc-kubectl-client] Case 1: default render contains keycloak-config-cli post-install Job"
helm template smoke-kc . > "$TMP/default.yaml"
if ! grep -q 'helm.sh/hook: post-install,post-upgrade,post-rollback' "$TMP/default.yaml"; then
  echo "FAIL: default render does not contain a keycloak-config-cli post-install Helm hook." >&2
  exit 1
fi
if ! grep -q 'name: smoke-kc-keycloak-keycloak-config-cli$' "$TMP/default.yaml"; then
  echo "FAIL: default render does not contain the keycloak-config-cli Job by expected name." >&2
  exit 1
fi
echo "  PASS"

echo "[oidc-kubectl-client] Case 2: realm 'sovereign' is shipped"
if ! grep -q '"realm": "sovereign"' "$TMP/default.yaml"; then
  echo "FAIL: default render does not declare realm 'sovereign'. The k3s api-server" >&2
  echo "       --oidc-issuer-url flag points at /realms/sovereign so this realm name" >&2
  echo "       is INVARIANT — do not rename without coordinating with" >&2
  echo "       infra/hetzner/cloudinit-control-plane.tftpl." >&2
  exit 1
fi
echo "  PASS"

echo "[oidc-kubectl-client] Case 3: kubectl client is shipped with publicClient + localhost:8000 redirect"
if ! grep -q '"clientId": "kubectl"' "$TMP/default.yaml"; then
  echo "FAIL: default render does not contain a Keycloak Client with clientId=kubectl." >&2
  exit 1
fi
if ! grep -q '"publicClient": true' "$TMP/default.yaml"; then
  echo "FAIL: default render's kubectl client is not declared publicClient=true." >&2
  echo "       kubectl-oidc-login runs locally on the operator's machine and cannot" >&2
  echo "       safely hold a client secret." >&2
  exit 1
fi
if ! grep -q '"http://localhost:8000"' "$TMP/default.yaml"; then
  echo "FAIL: default render's kubectl client does not list http://localhost:8000 as a redirect URI." >&2
  exit 1
fi
echo "  PASS"

echo "[oidc-kubectl-client] Case 4: groups claim mapper is wired"
if ! grep -q '"protocolMapper": "oidc-group-membership-mapper"' "$TMP/default.yaml"; then
  echo "FAIL: default render does not contain the oidc-group-membership-mapper. The" >&2
  echo "       api-server's --oidc-groups-claim flag depends on the id-token carrying" >&2
  echo "       the 'groups' claim." >&2
  exit 1
fi
if ! grep -q '"claim.name": "groups"' "$TMP/default.yaml"; then
  echo "FAIL: default render's group mapper does not emit the 'groups' claim name." >&2
  exit 1
fi
echo "  PASS"

echo "[oidc-kubectl-client] All bp-keycloak OIDC-kubectl-client gates green."
