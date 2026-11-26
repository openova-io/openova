#!/bin/sh
export KUBECONFIG=/dev/null
export HELM_CACHE_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-cache
export HELM_CONFIG_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-config
export HELM_DATA_HOME=/home/openova/.cache/ns-scratch/SEC-335-tools/helm-data
U=/home/openova/.cache/ns-scratch/SEC-335-tools/upstream
mkdir -p "$U"
cd "$U"
helm pull keycloak --repo https://charts.bitnami.com/bitnami --version 25.2.0 --untar --untardir kc 2>&1 | tail -2
helm pull gitea --repo https://dl.gitea.com/charts --version 10.5.0 --untar --untardir gitea 2>&1 | tail -2
helm pull harbor --repo https://helm.goharbor.io --version 1.18.3 --untar --untardir harbor 2>&1 | tail -2
helm pull grafana --repo https://grafana.github.io/helm-charts --version 10.5.15 --untar --untardir grafana 2>&1 | tail -2
ls "$U"/*
