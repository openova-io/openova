#!/usr/bin/env bash
# bp-catalyst-platform — SEC-212: the auth service's refresh-token signing secret
# is born once into OpenBao and reaches the auth Pod as a mounted file; no Secret
# object holds it.
#
# Renders the chart with the marketplace on and asserts the values each clause
# names:
#   1. No rendered document names JWT_REFRESH_SECRET; Secret org-services-secrets
#      keeps exactly its other ten keys.
#   2. The auth container mounts the CSI volume of SecretProviderClass auth
#      read-only at /var/run/secrets/openova.
#   3. SecretProviderClass auth in org-services: provider openbao, role workload,
#      audience openbao, the in-cluster OpenBao address, one object
#      jwt-refresh-secret from key password of
#      mounted/data/org-services/sa/auth/jwt-refresh-secret/e1, no secretObjects.
#   4. The birth Job of platform/secrets-lib runs in org-services as
#      secrets-birth and its plan holds the one line that writes that path
#      (mounted/ + the line's path): 64 characters, no symbols, this region only.
#      (That OpenBao admits this Job: platform/plane-isolation/chart/tests/
#      openbao-birth-ingress.sh.)
#   5. The auth Pod carries the value's path; epoch 2 moves the Pod annotation,
#      the object's path and the plan line to e2 together.
#   6. A value the auth Pod cannot read is refused at render: another service
#      account, delivery synced.
#   7. With the marketplace off neither the object nor the Job renders, and
#      org-services-secrets still does.
# On the chart before SEC-212 cases 1 to 6 fail.
#
# Usage: bash tests/auth-refresh-secret-mounted.sh [CHART_DIR]   (yq v4, helm v3)
set -uo pipefail

chart_dir="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
helm="${HELM_BIN:-helm}"
# The chart depends on platform/secrets-lib (SEC-216): build it into charts/ before the first render.
"$helm" dependency build --skip-refresh "${chart_dir}" >/dev/null || { echo "helm dependency build failed"; exit 1; }
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT
failures=0
pass() { echo "  PASS  $1"; }
fail() { echo "  FAIL  $1"; failures=$((failures + 1)); }
check() { if [ "$2" = "$3" ]; then pass "$1"; else fail "$1: got [$2], want [$3]"; fi; }

base=(--namespace catalyst-system --set global.sovereignFQDN=t01.omani.works
  --set ingress.hosts.marketplace.host=marketplace.t01.omani.works)
render() {  # $1 = output file, the rest: helm flags
  local out="$1"; shift
  "$helm" template smoke "${chart_dir}" "${base[@]}" "$@" >"${out}" 2>"${tmp}/err" \
    || { echo "helm template failed:"; cat "${tmp}/err"; exit 1; }
}
refuses() {  # $1 = case, $2 = the message, the rest: helm flags
  local name="$1" want="$2"; shift 2
  if "$helm" template smoke "${chart_dir}" "${base[@]}" --set ingress.marketplace.enabled=true "$@" >/dev/null 2>"${tmp}/err"; then
    fail "${name}: the render succeeded"
  elif grep -q -F -- "${want}" "${tmp}/err"; then
    pass "${name}"
  else
    fail "${name}: refused without [${want}]: $(tr '\n' ' ' <"${tmp}/err")"
  fi
}
render "${tmp}/on.yaml" --set ingress.marketplace.enabled=true
render "${tmp}/e2.yaml" --set ingress.marketplace.enabled=true --set orgServices.generatedSecrets.jwt-refresh-secret.epoch=2
render "${tmp}/off.yaml" --set ingress.marketplace.enabled=false

q() { yq ea "$1" "$2"; }   # one expression over every document of a render, collected
path1="mounted/data/org-services/sa/auth/jwt-refresh-secret/e1"
pod='[select(.kind == "Deployment" and .metadata.name == "auth" and .metadata.namespace == "org-services")] | .[0].spec.template'
spc='[select(.kind == "SecretProviderClass" and .metadata.name == "auth" and .metadata.namespace == "org-services")]'
job='[select(.kind == "Job" and .metadata.namespace == "org-services" and (.metadata.name | test("^secrets-birth-[0-9a-f]{10}$")))]'
plan='[select(.kind == "ConfigMap" and .metadata.namespace == "org-services" and (.metadata.name | test("^secrets-birth-")))] | .[0].data.plan'
f="${tmp}/on.yaml"

echo "[auth-refresh-secret-mounted] SEC-212"
check "1 no rendered document names JWT_REFRESH_SECRET" "$(grep -c -F JWT_REFRESH_SECRET "${f}")" 0
check "1 Secret org-services-secrets keeps exactly its other ten keys" \
  "$(q '[select(.kind == "Secret" and .metadata.name == "org-services-secrets")] | .[0].data | keys | sort | join(",")' "${f}")" \
  "ADMIN_EMAIL,ADMIN_PASSWORD,GOOGLE_CLIENT_ID,GOOGLE_CLIENT_SECRET,JWT_SECRET,SMTP_FROM,SMTP_HOST,SMTP_PASS,SMTP_PORT,SMTP_USER"

check "2 the auth container mounts volume openova-secrets read-only at /var/run/secrets/openova" \
  "$(q "${pod}"' | .spec.containers[] | select(.name == "auth") | .volumeMounts[] | select(.name == "openova-secrets") | .mountPath + " " + (.readOnly | tostring)' "${f}")" \
  "/var/run/secrets/openova true"
check "2 volume openova-secrets is the read-only CSI volume of SecretProviderClass auth" \
  "$(q "${pod}"' | .spec.volumes[] | select(.name == "openova-secrets") | .csi.driver + " " + (.csi.readOnly | tostring) + " " + .csi.volumeAttributes.secretProviderClass' "${f}")" \
  "secrets-store.csi.k8s.io true auth"

check "3 one SecretProviderClass auth in org-services" "$(q "${spc}"' | length' "${f}")" 1
check "3 provider, role, audience and address" \
  "$(q "${spc}"' | .[0].spec | .provider + " " + .parameters.roleName + " " + .parameters.audience + " " + .parameters.openbaoAddress' "${f}")" \
  "openbao workload openbao http://openbao.openbao.svc.cluster.local:8200"
check "3 one object: jwt-refresh-secret from key password of the e1 path" \
  "$(q "${spc}"' | .[0].spec.parameters.objects' "${f}" | yq '[.[] | .objectName + " " + .secretPath + " " + .secretKey] | join(";")')" \
  "jwt-refresh-secret ${path1} password"
check "3 its spec is provider and parameters only: no secretObjects" \
  "$(q "${spc}"' | .[0].spec | keys | sort | join(",")' "${f}")" "parameters,provider"

check "4 one birth Job in org-services" "$(q "${job}"' | length' "${f}")" 1
check "4 the Job runs as secrets-birth" "$(q "${job}"' | .[0].spec.template.spec.serviceAccountName' "${f}")" secrets-birth
check "4 the plan writes the e1 path: 64 characters, no symbols, a new random value, this region" \
  "$(q "${plan}" "${f}")" "org-services/sa/auth/jwt-refresh-secret/e1 64 0 - self"
check "4 the plan's path is the object's path" "mounted/data/$(q "${plan}" "${f}" | cut -d' ' -f1)" "${path1}"

check "5 the auth Pod carries the e1 path" \
  "$(q "${pod}"' | .metadata.annotations["secrets.openova.io/jwt-refresh-secret"]' "${f}")" "${path1}"
check "5 epoch 2 moves the Pod annotation, the object's path and the plan line to e2" \
  "$(q "${pod}"' | .metadata.annotations["secrets.openova.io/jwt-refresh-secret"]' "${tmp}/e2.yaml") $(q "${spc}"' | .[0].spec.parameters.objects' "${tmp}/e2.yaml" | yq '.[0].secretPath') $(q "${plan}" "${tmp}/e2.yaml")" \
  "mounted/data/org-services/sa/auth/jwt-refresh-secret/e2 mounted/data/org-services/sa/auth/jwt-refresh-secret/e2 org-services/sa/auth/jwt-refresh-secret/e2 64 0 - self"

refuses "6 another service account is refused" "serviceAccount must be auth, the service account of the auth Pod" \
  --set orgServices.generatedSecrets.jwt-refresh-secret.serviceAccount=billing
refuses "6 delivery synced is refused" "must be a mounted value: the auth Pod reads it as a file" \
  --set orgServices.generatedSecrets.jwt-refresh-secret.delivery=synced \
  --set orgServices.generatedSecrets.jwt-refresh-secret.registerId=a-register-entry

g="${tmp}/off.yaml"
check "7 marketplace off: no SecretProviderClass, no birth Job" \
  "$(q "${spc}"' | length' "${g}") $(q "${job}"' | length' "${g}")" "0 0"
check "7 marketplace off: org-services-secrets still renders" \
  "$(q '[select(.kind == "Secret" and .metadata.name == "org-services-secrets")] | length' "${g}")" 1

if [ "${failures}" -ne 0 ]; then
  echo "[auth-refresh-secret-mounted] ${failures} case(s) failed"
  exit 1
fi
echo "[auth-refresh-secret-mounted] all cases passed"
