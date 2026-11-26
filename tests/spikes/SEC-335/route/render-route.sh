#!/bin/sh
# Render Keycloak and Grafana with the route's values; count references to the entry's Secret per pod template.
export KUBECONFIG=/dev/null
export HELM_CACHE_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-cache
export HELM_CONFIG_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-config
export HELM_DATA_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-data
T=/home/openova/.cache/ns-scratch/SEC-335-tools
Q=$T/route
chmod 0755 $Q/pr-kustomize.sh
F=t01.omani.works
AV="--api-versions external-secrets.io/v1beta1 --api-versions external-secrets.io/v1"
# refs FILE SECRET: per workload kind/name, how many times the pod template names SECRET.
refs() {
  for k in StatefulSet Deployment Job; do
    yq "select(.kind==\"$k\") | .metadata.name" "$1" | grep -v '^---$' | while read -r n; do
      c=$(yq "select(.kind==\"$k\" and .metadata.name==\"$n\") | .spec.template" "$1" | grep -c -w -F "$2")
      echo "  $k/$n: $c"
    done
  done
}
echo "== keycloak at main"; refs $T/render/keycloak.yaml keycloak-admin
helm template keycloak $T/charts/keycloak -n keycloak $AV --set sso.sovereignFqdn=$F -f $Q/kc-values.yaml > $Q/keycloak-values.yaml 2>$Q/keycloak-values.err; echo "values-only rc=$?"
echo "== keycloak, route values only"; refs $Q/keycloak-values.yaml keycloak-admin
helm template keycloak $T/charts/keycloak -n keycloak $AV --set sso.sovereignFqdn=$F -f $Q/kc-values.yaml --post-renderer $Q/pr-kustomize.sh > $Q/keycloak-route.yaml 2>$Q/keycloak-route.err; echo "values+post-render rc=$?"
grep -v WARNING $Q/keycloak-route.err | head -3
echo "== keycloak, route values + post-render patch"; refs $Q/keycloak-route.yaml keycloak-admin
echo "   KC_BOOTSTRAP_ADMIN_PASSWORD_FILE in the StatefulSet env:"
yq 'select(.kind=="StatefulSet" and .metadata.name=="keycloak") | .spec.template.spec.containers[0].env[] | select(.name=="KC_BOOTSTRAP_ADMIN_PASSWORD_FILE") | .value' $Q/keycloak-route.yaml
echo "   projected sources left:"; yq 'select(.kind=="StatefulSet" and .metadata.name=="keycloak") | .spec.template.spec.volumes[] | select(.name=="keycloak-secrets") | .projected.sources[].secret.name' $Q/keycloak-route.yaml
echo "   csi volume:"; yq -o=json -I=0 'select(.kind=="StatefulSet" and .metadata.name=="keycloak") | .spec.template.spec.volumes[] | select(.name=="openova-secrets")' $Q/keycloak-route.yaml
echo "== grafana at main"; refs $T/render/grafana.yaml grafana-sso-oidc-credentials
helm template grafana $T/charts/grafana -n grafana $AV --set sso.sovereignFqdn=$F -f $Q/grafana-values.yaml > $Q/grafana-route.yaml 2>$Q/grafana-route.err; echo "rc=$?"
echo "== grafana, route values"; refs $Q/grafana-route.yaml grafana-sso-oidc-credentials
echo "   client_secret line:"; yq 'select(.kind=="ConfigMap" and .metadata.name=="grafana") | .data["grafana.ini"]' $Q/grafana-route.yaml | grep '^client_secret'
echo "   csi volume:"; yq -o=json -I=0 'select(.kind=="Deployment" and .metadata.name=="grafana") | .spec.template.spec.volumes[] | select(.name=="openova-secrets")' $Q/grafana-route.yaml
echo "   mount:"; yq -o=json -I=0 'select(.kind=="Deployment" and .metadata.name=="grafana") | .spec.template.spec.containers[] | select(.name=="grafana") | .volumeMounts[] | select(.name=="openova-secrets")' $Q/grafana-route.yaml
rm -rf $Q/pr.*
