#!/usr/bin/env bash
# bp-grafana — SEC-344: the sign-on client secret reaches Grafana as a mounted
# file, never through a Secret object.
#
# Renders the chart and asserts the values each clause names:
#   1. No rendered document names the Secret grafana-sso-oidc-credentials, the
#      chart renders no ExternalSecret even where the External Secrets API
#      exists, and its one Secret is the upstream admin login grafana.
#   2. grafana.ini reads the client secret from the mounted file.
#   3. The grafana container mounts the CSI volume of SecretProviderClass
#      grafana-sso read-only at /var/run/secrets/openova, and its envFrom is
#      exactly ConfigMap grafana-sso-settings.
#   4. SecretProviderClass grafana-sso: provider openbao, role workload,
#      audience openbao, one object client-secret from key client_secret of
#      mounted/data/<namespace>/shared/sso-oidc/e1, no secretObjects.
#   5. With sso.sovereignFqdn: ConfigMap grafana-sso-settings holds the client
#      id, the three realm endpoints (authorize URL with the kc_idp_hint) and
#      root_url; with sso.idpHint empty the authorize URL has no hint.
#   6. Without sso.sovereignFqdn the ConfigMap holds the client id only.
#   7. sso.enabled=false renders neither object.
# On the chart before SEC-344 cases 1 to 6 fail.
#
# Usage: bash tests/sso-mounted-render.sh [CHART_DIR]   (yq v4, helm v3)
set -uo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
failures=0
pass() { echo "  PASS  $1"; }
fail() { echo "  FAIL  $1"; failures=$((failures + 1)); }
check() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1: got [$2], want [$3]"; fi; }

render() {  # $1 = output file, the rest: helm flags
  local out="$1"; shift
  "$helm" template grafana "${chart_dir}" -n grafana "$@" >"${out}" 2>"${tmp}/err" \
    || { echo "helm template failed:"; cat "${tmp}/err"; exit 1; }
}
render "${tmp}/fqdn.yaml" --set sso.sovereignFqdn=t01.omani.works --api-versions external-secrets.io/v1beta1
render "${tmp}/nohint.yaml" --set sso.sovereignFqdn=t01.omani.works --set sso.idpHint=
render "${tmp}/default.yaml"
render "${tmp}/off.yaml" --set sso.enabled=false

q() { yq ea "$1" "$2"; }   # one expression over every document of a render, collected
dep='[select(.kind == "Deployment" and .metadata.name == "grafana")] | .[0].spec.template.spec'
spc='[select(.kind == "SecretProviderClass" and .metadata.name == "grafana-sso")]'
cm='[select(.kind == "ConfigMap" and .metadata.name == "grafana-sso-settings")]'
f="${tmp}/fqdn.yaml"

echo "[sso-mounted-render] SEC-344"
check "1 no document names grafana-sso-oidc-credentials" "$(grep -c -F grafana-sso-oidc-credentials "${f}")" 0
check "1 no ExternalSecret is rendered" "$(q '[select(.kind == "ExternalSecret")] | length' "${f}")" 0
check "1 the one Secret is the upstream admin login grafana" \
  "$(q '[select(.kind == "Secret") | .metadata.name] | to_json(0)' "${f}")" '["grafana"]'

ini="$(q '[select(.kind == "ConfigMap" and .metadata.name == "grafana")] | .[0].data["grafana.ini"]' "${f}")"
check "2 grafana.ini reads the client secret from the mounted file" \
  "$(printf '%s\n' "${ini}" | grep -F 'client_secret')" 'client_secret = $__file{/var/run/secrets/openova/client-secret}'

check "3 the grafana container mounts volume openova-secrets read-only at /var/run/secrets/openova" \
  "$(q "${dep}"' | .containers[] | select(.name == "grafana") | .volumeMounts[] | select(.name == "openova-secrets") | .mountPath + " " + (.readOnly | tostring)' "${f}")" \
  "/var/run/secrets/openova true"
check "3 volume openova-secrets is the CSI volume of SecretProviderClass grafana-sso" \
  "$(q "${dep}"' | .volumes[] | select(.name == "openova-secrets") | .csi.driver + " " + (.csi.readOnly | tostring) + " " + .csi.volumeAttributes.secretProviderClass' "${f}")" \
  "secrets-store.csi.k8s.io true grafana-sso"
check "3 the grafana container takes its environment from ConfigMap grafana-sso-settings only" \
  "$(q "${dep}"' | .containers[] | select(.name == "grafana") | .envFrom | to_json(0)' "${f}")" \
  '[{"configMapRef":{"name":"grafana-sso-settings","optional":false}}]'

check "4 one SecretProviderClass grafana-sso in namespace grafana" "$(q "${spc}"' | length' "${f}")" 1
check "4 its provider, role, audience and address" \
  "$(q "${spc}"' | .[0].spec | .provider + " " + .parameters.roleName + " " + .parameters.audience + " " + .parameters.openbaoAddress' "${f}")" \
  "openbao workload openbao http://openbao.openbao.svc.cluster.local:8200"
check "4 its one object is client_secret of mounted/data/grafana/shared/sso-oidc/e1" \
  "$(q "${spc}"' | .[0].spec.parameters.objects | from_yaml | to_json(0)' "${f}")" \
  '[{"objectName":"client-secret","secretPath":"mounted/data/grafana/shared/sso-oidc/e1","secretKey":"client_secret"}]'
check "4 its spec is provider and parameters only (no secretObjects)" \
  "$(q "${spc}"' | .[0].spec | keys | sort | to_json(0)' "${f}")" '["parameters","provider"]'

oidc="https://auth.t01.omani.works/realms/sovereign/protocol/openid-connect"
check "5 ConfigMap grafana-sso-settings with sso.sovereignFqdn" "$(q "${cm}"' | .[0].data | to_json(0)' "${f}")" \
  "{\"GF_AUTH_GENERIC_OAUTH_CLIENT_ID\":\"grafana\",\"GF_AUTH_GENERIC_OAUTH_AUTH_URL\":\"${oidc}/auth?kc_idp_hint=catalyst-pin\",\"GF_AUTH_GENERIC_OAUTH_TOKEN_URL\":\"${oidc}/token\",\"GF_AUTH_GENERIC_OAUTH_API_URL\":\"${oidc}/userinfo\",\"GF_SERVER_ROOT_URL\":\"https://grafana.t01.omani.works/\"}"
check "5 with sso.idpHint empty the authorize URL carries no hint" \
  "$(q "${cm}"' | .[0].data.GF_AUTH_GENERIC_OAUTH_AUTH_URL' "${tmp}/nohint.yaml")" "${oidc}/auth"
check "6 without sso.sovereignFqdn the ConfigMap holds the client id only" \
  "$(q "${cm}"' | .[0].data | to_json(0)' "${tmp}/default.yaml")" '{"GF_AUTH_GENERIC_OAUTH_CLIENT_ID":"grafana"}'
check "7 sso.enabled=false still renders the grafana Deployment" \
  "$(q '[select(.kind == "Deployment" and .metadata.name == "grafana")] | length' "${tmp}/off.yaml")" 1
check "7 sso.enabled=false renders neither object" \
  "$(q '[select(.kind == "SecretProviderClass" or .metadata.name == "grafana-sso-settings")] | length' "${tmp}/off.yaml")" 0

if [ "${failures}" -ne 0 ]; then echo "[sso-mounted-render] ${failures} case(s) FAILED"; exit 1; fi
echo "[sso-mounted-render] all cases PASS"
