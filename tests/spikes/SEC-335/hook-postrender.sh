#!/bin/sh
# Does Helm pass hook manifests through a post-renderer? Render bp-keycloak with a renderer that renames
# two strings, one in a hook (the config-cli Job) and one in a normal manifest (the env ConfigMap).
export KUBECONFIG=/dev/null
export HELM_CACHE_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-cache
export HELM_CONFIG_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-config
export HELM_DATA_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-data
T=/home/openova/.cache/ns-scratch/SEC-335-tools
chmod +x $T/pr-mark.sh
helm version --short
helm template keycloak $T/charts/keycloak -n keycloak --set sso.sovereignFqdn=t01.omani.works --post-renderer $T/pr-mark.sh > $T/render/keycloak-pr.yaml 2>/dev/null
echo "post-render rc=$?"
echo "hook Job (helm.sh/hook: $(yq 'select(.kind=="Job" and .metadata.name=="keycloak-keycloak-config-cli") | .metadata.annotations["helm.sh/hook"]' $T/render/keycloak-pr.yaml)) env names:"
yq 'select(.kind=="Job" and .metadata.name=="keycloak-keycloak-config-cli") | .spec.template.spec.containers[0].env[].name' $T/render/keycloak-pr.yaml | grep "PASSWORD"
echo "env ConfigMap keycloak-env-vars keys:"
yq 'select(.kind=="ConfigMap" and .metadata.name=="keycloak-env-vars") | .data | keys | .[]' $T/render/keycloak-pr.yaml | grep "POSTRENDERED\|KC_BOOTSTRAP_ADMIN_PASSWORD"
echo "helm-controller pinned by the kit: $(grep -A1 'image: harbor.openova.io/proxy-ghcr/fluxcd/helm-controller' /home/openova/.cache/ns-scratch/SEC-335/platform/flux/chart/values.yaml | tail -1 | tr -d ' ')"
echo "its Helm library (go.mod of fluxcd/helm-controller v1.1.0): $(grep 'helm.sh/helm/v3 ' $T/upstream/hc/go.mod | tr -d '\t')"
