#!/bin/sh
# Pull the pinned images from their origin registry and read the five answers' image facts.
export KUBECONFIG=/dev/null
O=/home/openova/.cache/ns-scratch/SEC-335-tools/images.out
: > $O
for img in \
  docker.io/bitnamilegacy/keycloak:26.3.3-debian-12-r0 \
  docker.io/bitnamilegacy/keycloak-config-cli:6.4.0-debian-12-r11 \
  docker.io/alpine/k8s:1.31.4 \
  docker.io/curlimages/curl:8.10.1 \
  docker.io/grafana/grafana:12.3.1 \
  docker.io/powerdnsadmin/pda-legacy:0.4.2 ; do
  echo "######## $img" >> $O
  podman pull -q "$img" >> $O 2>&1
  podman image inspect "$img" --format 'digest={{.Digest}} entrypoint={{json .Config.Entrypoint}} cmd={{json .Config.Cmd}} user={{.Config.User}} workdir={{.Config.WorkingDir}}' >> $O 2>&1
  podman run --rm --network none --entrypoint /bin/sh "$img" -c 'ls -l /bin/sh; command -v bash || echo "no bash"' >> $O 2>&1
  echo "sh-exit=$?" >> $O
done
