#!/bin/sh
# Baseline render of the five umbrella charts at main, with the pinned upstream subcharts.
export KUBECONFIG=/dev/null
export HELM_CACHE_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-cache
export HELM_CONFIG_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-config
export HELM_DATA_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-data
T=/home/openova/.cache/ns-scratch/SEC-335-tools
W=/home/openova/.cache/ns-scratch/SEC-335
U=$T/upstream
R=$T/render
mkdir -p $R
rm -rf $T/charts
mkdir -p $T/charts
for c in keycloak gitea harbor grafana powerdns-admin; do
  cp -r $W/platform/$c/chart $T/charts/$c
done
mkdir -p $T/charts/keycloak/charts $T/charts/gitea/charts $T/charts/harbor/charts $T/charts/grafana/charts
cp -r $U/kc/keycloak $T/charts/keycloak/charts/keycloak
cp -r $U/gitea/gitea $T/charts/gitea/charts/gitea
cp -r $U/harbor/harbor $T/charts/harbor/charts/harbor
cp -r $U/grafana/grafana $T/charts/grafana/charts/grafana
F=t01.omani.works
AV="--api-versions external-secrets.io/v1beta1 --api-versions external-secrets.io/v1"
helm template keycloak $T/charts/keycloak -n keycloak $AV --set sso.sovereignFqdn=$F > $R/keycloak.yaml 2> $R/keycloak.err; echo "keycloak rc=$?"
helm template gitea $T/charts/gitea -n gitea $AV --set sso.sovereignFqdn=$F > $R/gitea.yaml 2> $R/gitea.err; echo "gitea rc=$?"
helm template harbor $T/charts/harbor -n harbor $AV --set sso.sovereignFqdn=$F > $R/harbor.yaml 2> $R/harbor.err; echo "harbor rc=$?"
helm template grafana $T/charts/grafana -n grafana $AV --set sso.sovereignFqdn=$F > $R/grafana.yaml 2> $R/grafana.err; echo "grafana rc=$?"
helm template powerdns-admin $T/charts/powerdns-admin -n powerdns-admin $AV --set sso.sovereignFqdn=$F > $R/powerdns-admin.yaml 2> $R/powerdns-admin.err; echo "pda rc=$?"
for f in keycloak gitea harbor grafana powerdns-admin; do echo "--- $f err:"; grep -v WARNING $R/$f.err | head -5; done
echo "=== references to each entry Secret in the rendered pod templates"
echo "keycloak-admin: $(grep -c 'keycloak-admin$\|name: keycloak-admin\b' $R/keycloak.yaml)"
grep -n "keycloak-admin" $R/keycloak.yaml | head -20
echo "gitea-oauth-source-credentials:"; grep -n "gitea-oauth-source-credentials" $R/gitea.yaml | head
echo "harbor-sso-oidc-credentials:"; grep -n "harbor-sso-oidc-credentials" $R/harbor.yaml | head
echo "grafana-sso-oidc-credentials:"; grep -n "grafana-sso-oidc-credentials" $R/grafana.yaml | head
echo "pda-sso-oidc-credentials:"; grep -n "pda-sso-oidc-credentials" $R/powerdns-admin.yaml | head
