#!/usr/bin/env bash
# SOV-060 spike, job "node": rows E1, E2, E3, E4, E6, E7 and two controls,
# on one k3s node at the pinned version started with
# --disable-default-registry-endpoint and the planned two-endpoint registry file.
# The local registry is Harbor at the platform's version with hosted public
# projects; the origin is a plain registry whose access log is the evidence.
# shellcheck source=tests/spikes/SOV-060/lib.sh
source "$(dirname "$0")/lib.sh"

section "setup"
ensure_tools
make_pki
setup_names
setup_blackhole
start_stubs
start_origin || exit 1
install_harbor || exit 1
# proxy-ghcr is deliberately not created (row E2-noproject)
for p in proxy-quay proxy-docker proxy-gcr proxy-k8s proxy-ecr proxy-gitlab; do harbor_project "$p"; done
harbor_robot
resolve_watched

# A real public registry that is not on the list, for E4 and the controls.
MCR_REPO=oss/kubernetes/pause
MCR_TAG=3.6
tags=$(curl -s --max-time 10 "https://mcr.microsoft.com/v2/$MCR_REPO/tags/list" | jq -r '.tags[]?' 2>/dev/null || true)
if [ -n "$tags" ] && ! grep -qx "$MCR_TAG" <<<"$tags"; then MCR_TAG=$(tail -1 <<<"$tags"); fi
MCR_REF="mcr.microsoft.com/$MCR_REPO:$MCR_TAG"
log "public image outside the list: $MCR_REF (tags listed: $(wc -w <<<"$tags"))"

# Every test image is in the origin under the proxy project of its name.
declare -A ORIGIN_PROJECT=(
  [e1]=proxy-quay [e1nx]=proxy-gcr [e1bh]=proxy-k8s
  [e2]=proxy-quay [e2b]=proxy-ghcr [e3]=proxy-quay
  [e7a]=proxy-ecr [e7b]=proxy-docker [e7c]=proxy-quay [e7d]=proxy-gitlab
  [e6base]=proxy-quay [e6a]=proxy-quay [e6b]=proxy-quay [e6c]=proxy-quay
)
for n in "${!ORIGIN_PROJECT[@]}"; do
  build_image "$n"
  push_image "$n" "$ORIGIN_HOST/${ORIGIN_PROJECT[$n]}/spike/$n:1"
done

section "k3s $K3S_VERSION with the design registry file"
write_registries design
install_k3s || exit 1
dump_certs_d design
log "CRI registry logins written by k3s (passwords masked):"
sudo grep -A3 'grpc.v1.cri".registry.configs' /var/lib/rancher/k3s/agent/etc/containerd/config.toml \
  | sed -E 's/(password = ).*/\1"***"/'

section "E1: the first endpoint refuses connections"
docker stop nginx >/dev/null
curl -s -o /dev/null -w 'probe https://harbor.local.test/v2/ -> %{http_code} (000 = no connection)\n' --max-time 5 "https://$LOCAL_HOST/v2/"
pull_case E1 quay.io/spike/e1:1
pull_case E1-nx gcr.io/spike/e1nx:1
pull_case E1-blackhole registry.k8s.io/spike/e1bh:1 420
docker start nginx >/dev/null
wait_harbor

section "E2: the first endpoint answers not found (hosted project exists, empty)"
pull_case E2 quay.io/spike/e2:1
pull_case E2-noproject ghcr.io/spike/e2b:1

section "E3: the first endpoint holds the image"
push_image e3 "$LOCAL_HOST/proxy-quay/spike/e3:1"
pull_case E3 quay.io/spike/e3:1
pull_case E3-originhost "$ORIGIN_HOST/proxy-quay/spike/e3:1"

section "E7: the first endpoint answers 401 or 503"
push_image e7a "$LOCAL_HOST/proxy-ecr/spike/e7a:1"
pull_case E7-401-harbor public.ecr.aws/spike/e7a:1
pull_case E7-503-stub docker.io/spike/e7b:1
pull_case E7-401-stub registry.gitlab.com/spike/e7d:1
docker stop harbor-core >/dev/null
sleep 3
curl -s -o /dev/null -w 'probe https://harbor.local.test/v2/ with harbor-core stopped -> %{http_code}\n' --max-time 10 "https://$LOCAL_HOST/v2/"
pull_case E7-harbor-core-down quay.io/spike/e7c:1
docker start harbor-core >/dev/null
wait_harbor

section "E4: names that have no entry of their own"
pull_case E4-invalid registry.example.invalid/x/y:1
pull_case E4-public "$MCR_REF"
pull_case E4-listed-absent quay.io/spike/absent:1
pull_case E4-alias registry.example.invalid/proxy-quay/spike/e3:1

section "E6: remove the origin from registries.yaml on the running node"
pull_case E6-before quay.io/spike/e6base:1
before=$(sudo sha256sum "$CERTS_D/quay.io/hosts.toml" | cut -c1-16)
write_registries e6-edited
sleep 45
after=$(sudo sha256sum "$CERTS_D/quay.io/hosts.toml" | cut -c1-16)
log "quay.io hosts.toml hash before=$before after=$after (k3s rewrote it on the edit alone: $([ "$before" = "$after" ] && echo no || echo yes))"
pull_case E6 quay.io/spike/e6a:1

section "E6b: write the one-endpoint hosts.toml that k3s renders into place, no restart"
sudo cp "$CERTS_D/pivot-render.invalid/hosts.toml" "$CERTS_D/quay.io/hosts.toml"
sudo cat "$CERTS_D/quay.io/hosts.toml"
pull_case E6b quay.io/spike/e6b:1

section "E6c: after a k3s restart with the edited registries.yaml"
restart_k3s
sudo cat "$CERTS_D/quay.io/hosts.toml"
pull_case E6c quay.io/spike/e6c:1

section "Control C1: no wildcard entry, flag still set"
write_registries nowild
restart_k3s
sudo ls "$CERTS_D"
pull_case C1-public "$MCR_REF"
pull_case C1-invalid registry.example.invalid/x/y:1

section "Control C2: wildcard entry present, flag removed"
write_registries design
sudo sed -i "/--disable-default-registry-endpoint/d" /etc/systemd/system/k3s.service
sudo systemctl daemon-reload
restart_k3s
k3s_args
dump_certs_d noflag
pull_case C2-listed-absent quay.io/spike/absent:1
pull_case C2-public "$MCR_REF"

section "results"
cat "$SOV060_RESULTS"
python3 "$SPIKE_DIR/verdict.py" node "$EV"
