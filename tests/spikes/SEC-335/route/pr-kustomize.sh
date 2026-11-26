#!/bin/sh
# Helm post-renderer standing in for the HelmRelease's spec.postRenderers kustomize patch.
export KUBECONFIG=/dev/null
d=$(mktemp -d /home/openova/.cache/ns-scratch/SEC-335-tools/route/pr.XXXXXX)
cat > "$d/all.yaml"
cp /home/openova/.cache/ns-scratch/SEC-335-tools/route/kustomization.yaml "$d/"
kubectl kustomize "$d"
