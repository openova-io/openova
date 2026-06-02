#!/usr/bin/env bash
# bp-keycloak — G117.E2E #2816 regression test for KC DB `varchar(255)` cap on
# CLIENT.DESCRIPTION.
#
# Keycloak's relational schema (Postgres + the bundled H2 / MariaDB / etc.)
# stores `public.CLIENT.DESCRIPTION` as `varchar(255)`. The keycloak-config-
# cli realm import opens a JDBC batch with the full description string;
# any client whose description exceeds the cap aborts the entire batch
# with `PSQLException: ERROR: value too long for type character varying(255)`
# and the import fails. The post-install / post-upgrade Job then exhausts
# its backoffLimit, helm rolls back, and the entire downstream cascade
# (bp-sso-bridge, bp-catalyst-platform, bp-grafana, bp-newapi, …) stays
# Ready=False on `dependency 'flux-system/bp-keycloak' is not ready`.
#
# Caught live on hw86 2026-06-03 (#2816). Same shape as PR #1285
# (catalyst-api-server client 2026-05-15) but for the Tier-2 clients
# added in PR #2802. This test asserts the cap at render time so the bug
# is caught BEFORE it reaches a live Sovereign.
#
# Cap = 255 chars per JDBC `varchar(255)` definition; Keycloak's source:
#   https://github.com/keycloak/keycloak/blob/main/model/jpa/src/main/resources/META-INF/jpa-changelog-1.0.0.Final.xml
#   <column name="DESCRIPTION" type="VARCHAR(255)"/>
#
# Usage: bash tests/g117-e2e-realm-client-description-255-cap.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="$(cd "${1:-$(dirname "$0")/..}" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

helm="${HELM_BIN:-helm}"

# Render with a representative sovereignFQDN so every templated description
# (none currently template the FQDN into description, but render anyway in
# case future descriptions do).
"$helm" template smoke "$CHART_DIR" \
  --set sovereignFQDN=hw00.omani.works \
  --show-only templates/configmap-sovereign-realm.yaml \
  > "$TMP/render.yaml" 2>/dev/null

# Extract the realm JSON from the ConfigMap.
yq -r 'select(.kind == "ConfigMap") | .data["sovereign-realm.json"]' \
  "$TMP/render.yaml" > "$TMP/realm.json"

# For every client + authenticationFlow + every other entity that carries
# `description`, assert the value is ≤255 chars.
fails=$(jq -r '
  def descs:
    [
      (.clients // [])[]
        | { kind: "client", id: .clientId, desc: (.description // "") },
      (.authenticationFlows // [])[]
        | { kind: "flow", id: .alias, desc: (.description // "") },
      (.authenticatorConfig // [])[]
        | { kind: "authenticatorConfig", id: .alias, desc: (.description // "") }
    ];
  descs[]
  | select((.desc | length) > 255)
  | "\(.kind)=\(.id) length=\(.desc | length) chars (>255 KC schema cap)"
' "$TMP/realm.json")

if [ -n "$fails" ]; then
  echo "FAIL: one or more realm-import descriptions exceed Keycloak's varchar(255) cap:"
  echo "$fails" | sed 's/^/  /'
  echo
  echo "Any of these will abort the keycloak-config-cli post-install/post-upgrade"
  echo "Job with PSQLException and the whole bp-keycloak HR will roll back."
  echo "Truncate the offending description to ≤255 chars (current pattern: keep"
  echo "the first sentence of the original rationale + drop the parenthetical"
  echo "implementation notes — they belong in the YAML comment above, not in the"
  echo "realm-import body)."
  exit 1
fi

# Report the close-to-cap descriptions for awareness, but don't fail.
echo "[g117-e2e-realm-client-description-255-cap] All descriptions ≤255 chars."
jq -r '
  def descs:
    [
      (.clients // [])[] | { id: .clientId, len: (.description // "" | length) }
    ];
  descs[]
  | select(.len > 200)
  | "  warn: client=\(.id) length=\(.len) (close to 255-cap)"
' "$TMP/realm.json" || true
echo "PASS"
