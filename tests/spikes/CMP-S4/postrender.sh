#!/usr/bin/env bash
# Helm post-renderer for spike CMP-S4: the platform charts pull through the
# Harbor proxy of the platform, which a CI runner cannot reach. Replace each
# proxy prefix with the upstream registry it proxies. Nothing else changes.
exec sed \
  -e 's#harbor\.openova\.io/proxy-dockerhub/#docker.io/#g' \
  -e 's#harbor\.openova\.io/proxy-ghcr/#ghcr.io/#g' \
  -e 's#harbor\.openova\.io/proxy-quay/#quay.io/#g' \
  -e 's#harbor\.openova\.io/proxy-k8s/#registry.k8s.io/#g' \
  -e 's#harbor\.openova\.io/proxy-gcr/#gcr.io/#g'
