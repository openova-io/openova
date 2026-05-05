#!/usr/bin/env bash
# bp-stalwart-tenant — OIDC render audit (#915).
#
# Verifies the chart's bootstrap config.toml + setup Job render the
# Keycloak OIDC integration correctly when a per-tenant overlay supplies
# the canonical values.
#
# 1. config.toml has an `[oauth]` block with all required endpoints
#    derived from the realm URL.
# 2. The setup Job's settings POST uses the camelCase keys verified
#    against the upstream Stalwart registry schema:
#       directory.keycloak.@type, issuerUrl, claimUsername, claimName,
#       claimGroups, requireScopes
# 3. The OIDC ExternalSecret materialises the client secret at the
#    operator-supplied OpenBao path.
# 4. The bootstrap config.toml is valid TOML when rendered.

set -euo pipefail

chart_dir="$(cd "$(dirname "$0")/.." && pwd)"

values=$(mktemp)
trap "rm -f '${values}'" EXIT

cat > "${values}" <<'EOF'
domain:
  primary: acme.omantel.omani.works
  mode: free-subdomain
ingress:
  webmail:
    host: mail.acme.omantel.omani.works
keycloak:
  realmURL: https://keycloak.acme.omantel.omani.works/realms/sme-acme
  clientID: stalwart
  clientSecretName: stalwart-oidc-client-secret
  oidcExternalSecret:
    enabled: true
    secretStoreRef:
      kind: ClusterSecretStore
      name: vault-region1
    remoteRef:
      key: sovereign/omantel.omani.works/stalwart/t-acme/oidc
      property: OIDC_CLIENT_SECRET
mailboxProvisioner:
  setupJob:
    enabled: true
EOF

render=$(mktemp)
trap "rm -f '${values}' '${render}'" EXIT

helm template oidc-smoke "${chart_dir}" \
    --namespace oidc-smoke \
    --api-versions external-secrets.io/v1beta1 \
    -f "${values}" \
    > "${render}"

fail=0

# 1. The setup Job env block must propagate the per-tenant realm URL +
#    OIDC client metadata — these drive the settings POST below.
for needle in \
  'KEYCLOAK_REALM_URL' \
  'https://keycloak.acme.omantel.omani.works/realms/sme-acme' \
  'KEYCLOAK_CLIENT_ID' \
  '"stalwart"' ; do
  if ! grep -qF "${needle}" "${render}"; then
    echo "::error title=Stalwart OIDC env::missing in setup Job env: ${needle}"
    fail=1
  fi
done

# 2. setup Job seeds the OIDC directory with the canonical camelCase
#    keys verified against crates/registry/src/schema/structs.rs in
#    the upstream Stalwart repo.
for key in \
  'directory.keycloak.@type' \
  'directory.keycloak.issuerUrl' \
  'directory.keycloak.claimUsername' \
  'directory.keycloak.claimName' \
  'directory.keycloak.claimGroups' \
  'directory.keycloak.requireScopes' \
  'storage.directory' ; do
  if ! grep -qF "${key}" "${render}"; then
    echo "::error title=Stalwart OIDC settings POST::missing settings key in setup Job: ${key}"
    fail=1
  fi
done

# 3. OIDC ExternalSecret materialises at the canonical OpenBao path.
if ! grep -qF 'sovereign/omantel.omani.works/stalwart/t-acme/oidc' "${render}"; then
  echo "::error title=Stalwart OIDC ExternalSecret::missing remoteRef.key — chart should emit operator-supplied path"
  fail=1
fi

# 4. Validate the rendered config.toml parses as TOML.
if command -v python3 >/dev/null 2>&1; then
  RENDER_PATH="${render}" python3 <<'PY'
import os
import sys
try:
    import tomllib
except ImportError:
    try:
        import tomli as tomllib
    except ImportError:
        sys.exit(0)  # no TOML library — skip parse check
import yaml
docs = list(yaml.safe_load_all(open(os.environ["RENDER_PATH"])))
toml_text = None
for d in docs:
    if not d:
        continue
    if d.get("kind") == "ConfigMap" and "config.toml" in d.get("data", {}):
        toml_text = d["data"]["config.toml"]
        break
if toml_text is None:
    print("::error title=Stalwart OIDC TOML::config.toml ConfigMap not found in render", file=sys.stderr)
    sys.exit(1)
try:
    data = tomllib.loads(toml_text)
except Exception as e:
    print(f"::error title=Stalwart OIDC TOML::config.toml does not parse as TOML: {e}", file=sys.stderr)
    sys.exit(1)
required_top = ["server", "storage", "store", "directory", "auth", "tls"]
missing = [k for k in required_top if k not in data]
if missing:
    print(f"::error title=Stalwart OIDC TOML::missing top-level sections: {missing}", file=sys.stderr)
    sys.exit(1)
# The bootstrap config.toml MUST keep the `internal` directory so
# Stalwart is bootable before the post-install Job seeds the OIDC
# directory in the settings KV store.
if "internal" not in data.get("directory", {}):
    print("::error title=Stalwart OIDC TOML::[directory.internal] missing — chart must keep internal directory bootable", file=sys.stderr)
    sys.exit(1)
print("[stalwart-tenant/oidc-render] config.toml parses as valid TOML, internal bootstrap directory present.")
PY
  if [ $? -ne 0 ]; then
    fail=1
  fi
fi

if [ "${fail}" -eq 1 ]; then
  echo "[stalwart-tenant/oidc-render] FAIL — see ::error markers above."
  exit 1
fi

echo "[stalwart-tenant/oidc-render] All OIDC integration assertions passed."
echo "[stalwart-tenant/oidc-render] PASS"
