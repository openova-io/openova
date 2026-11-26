#!/bin/sh
export KUBECONFIG=/dev/null
export HELM_CACHE_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-cache
export HELM_CONFIG_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-config
export HELM_DATA_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-data
U=/home/openova/.cache/ns-scratch/SEC-335-tools/upstream
cd "$U"
helm pull oci://registry-1.docker.io/bitnamicharts/keycloak --version 25.2.0 --untar --untardir kc 2>&1 | grep -v WARNING | tail -3
ls kc kc/keycloak/templates 2>&1 | head -40
cd /home/openova/.cache/ns-scratch/SEC-335
grep -rn "bitnamicharts\|charts.bitnami.com" .github/workflows/*.yaml scripts/*.sh 2>/dev/null | head -5
