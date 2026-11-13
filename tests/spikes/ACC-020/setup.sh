#!/usr/bin/env bash
# Lab test ACC-020, part 1: throwaway single-node k3s, images, targets, a
# development OpenBao with an SSH mount, certificates, and the session pod
# (item k, cold start). Throwaway; never merged.
# shellcheck source=tests/spikes/ACC-020/lib.sh
source "$(dirname "$0")/lib.sh"

note "precondition: lines 'tag: \"1.5.5\"' in platform/guacamole/chart/values.yaml = $(grep -c 'tag: "1.5.5"' platform/guacamole/chart/values.yaml)"
note "k3s $K3S_VERSION"
curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION="$K3S_VERSION" \
  INSTALL_K3S_EXEC="--disable=traefik --disable=servicelb --write-kubeconfig-mode=644" sh - > "$EV/k3s-install.log" 2>&1 \
  || { tail -40 "$EV/k3s-install.log"; exit 1; }
for _ in $(seq 1 90); do kubectl get nodes 2>/dev/null | grep -q ' Ready' && break; sleep 2; done
kubectl get nodes -o wide | tee "$EV/nodes.txt"
kubectl -n kube-system rollout status deploy/coredns --timeout=180s

note "guacd image $GUACD_IMAGE"
docker pull -q "$GUACD_IMAGE" >/dev/null
{
  echo "pinned: $GUACD_IMAGE"
  echo "--- tag 1.6.0 on Docker Hub today:"
  docker buildx imagetools inspect docker.io/guacamole/guacd:1.6.0 2>&1 | head -12
  echo "--- image config:"
  docker image inspect "$GUACD_IMAGE" --format 'User={{.Config.User}} Entrypoint={{json .Config.Entrypoint}} Cmd={{json .Config.Cmd}}'
  echo "--- inside:"
  docker run --rm --entrypoint /bin/sh "$GUACD_IMAGE" -c \
    'echo alpine=$(cat /etc/alpine-release); ls -l /opt/guacamole/sbin; /opt/guacamole/sbin/guacd -v 2>&1 | head -2; ls /opt/guacamole/lib | grep -E "ssh2|websockets|vncclient|guac-client" | tr "\n" " "; echo; apk info -v 2>/dev/null | grep -E "^(libssh2|openssl|libssl|libwebsockets|libvncserver)" | tr "\n" " "; echo'
} > "$EV/guacd-image.txt" 2>&1
cat "$EV/guacd-image.txt"
GUACD_BIN=$(docker run --rm --entrypoint /bin/sh "$GUACD_IMAGE" -c 'if [ -x /opt/guacamole/sbin/guacd ]; then echo /opt/guacamole/sbin/guacd; else command -v guacd; fi')
echo "GUACD_BIN=$GUACD_BIN" >> "$EV/state.env"
note "guacd binary: $GUACD_BIN"

note "browser library guacamole-common-js 1.6.0 from Maven Central"
mkdir -p "$SPIKE_DIR/web/lib" /tmp/gcjs/x
curl -sSfL -o /tmp/gcjs/gcjs.zip "$GCJS_BASE/guacamole-common-js-1.6.0.zip"
want_sha1=$(curl -sSfL "$GCJS_BASE/guacamole-common-js-1.6.0.zip.sha1" | awk '{print $1}')
echo "$want_sha1  /tmp/gcjs/gcjs.zip" | sha1sum -c - | tee "$EV/library-check.txt"
unzip -o -q /tmp/gcjs/gcjs.zip -d /tmp/gcjs/x
JS=$(find /tmp/gcjs/x -name all.min.js | head -1)
cp "$JS" "$SPIKE_DIR/web/lib/all.min.js"
echo "bundled script used by the pages: ${JS#/tmp/gcjs/x/} sha256 $(sha256sum "$JS" | awk '{print $1}')" | tee -a "$EV/library-check.txt"

note "build images"
build() { # tag context [dockerfile]
  local tag=$1 ctx=$2 file=${3:-$2/Dockerfile}
  if docker build -t "$tag" -f "$file" "$ctx" > "$EV/build-${tag//[\/:]/_}.log" 2>&1; then
    docker save "$tag" | sudo k3s ctr images import - >/dev/null && note "built and imported $tag"
  else
    note "BUILD FAILED $tag"; tail -30 "$EV/build-${tag//[\/:]/_}.log"
  fi
}
build spike.local/acc020-agent:1 "$SPIKE_DIR" "$SPIKE_DIR/agent/Dockerfile"
build spike.local/sshd:1 "$SPIKE_DIR/targets/sshd"
build spike.local/vnc:1 "$SPIKE_DIR/targets/vnc"
git fetch -q --depth=1 origin main
MAIN_SHA=$(git rev-parse FETCH_HEAD)
rm -rf /tmp/proxyctx && mkdir -p /tmp/proxyctx
git archive FETCH_HEAD core/cmd/k8s-ws-proxy | tar -x -C /tmp/proxyctx
echo "k8s-ws-proxy source: origin/main $MAIN_SHA, tree $(git rev-parse FETCH_HEAD:core/cmd/k8s-ws-proxy)" | tee "$EV/proxy-source.txt"
build spike.local/k8s-ws-proxy:main /tmp/proxyctx /tmp/proxyctx/core/cmd/k8s-ws-proxy/Containerfile

note "OpenBao development server $OPENBAO_IMAGE with an SSH mount"
docker run -d --name bao --cap-add IPC_LOCK -p 127.0.0.1:8200:8200 "$OPENBAO_IMAGE" \
  server -dev -dev-root-token-id=root -dev-listen-address=0.0.0.0:8200 >/dev/null
for _ in $(seq 1 60); do curl -sf "$BAO/v1/sys/health" >/dev/null && break; sleep 1; done
bao "$BAO/v1/sys/health" | jq -c '{version, initialized, sealed}' | tee "$EV/bao-health.json"
bao -X POST -d '{"type":"ssh"}' "$BAO/v1/sys/mounts/ssh"
bao -X POST -d '{"generate_signing_key":true,"key_type":"ssh-ed25519"}' "$BAO/v1/ssh/config/ca" | jq -c '.data // .errors' | tee "$EV/bao-ca.json"
curl -sS "$BAO/v1/ssh/public_key" > /tmp/trusted-user-ca.pub
echo "CA public key type: $(awk '{print $1}' /tmp/trusted-user-ca.pub)" | tee -a "$EV/bao-ca.json"
bao -X POST "$BAO/v1/ssh/roles/$SSH_ROLE" -d '{
  "key_type":"ca","allow_user_certificates":true,"allowed_users":"spike","allowed_users_template":false,
  "default_user":"spike","ttl":"5m","max_ttl":"5m","allow_user_key_ids":true,
  "default_extensions":{"permit-pty":""},"allowed_extensions":"permit-pty","allowed_critical_options":""}'
bao "$BAO/v1/ssh/roles/$SSH_ROLE" | jq -c '.data | {key_type, allowed_users, default_user, ttl, max_ttl, allow_user_key_ids, default_extensions, allowed_extensions, algorithm_signer}' | tee "$EV/bao-role.json"

note "certificates of the exec proxy's certificate leg (openssl, ECDSA P-256, as the chart's cert-manager objects)"
rm -rf "$PKI" "$CREDS" && mkdir -p "$PKI" "$CREDS"
(
  cd "$PKI" || exit 1
  openssl ecparam -name prime256v1 -genkey -noout -out ca.key
  openssl req -x509 -new -key ca.key -subj "/CN=ca.k8s-ws-proxy.catalyst-system" -days 2 -out ca.crt
  leaf() { # name subject san eku
    openssl ecparam -name prime256v1 -genkey -noout -out "$1.key"
    openssl req -new -key "$1.key" -subj "$2" -out "$1.csr"
    printf 'subjectAltName=%s\nextendedKeyUsage=%s\nkeyUsage=digitalSignature,keyEncipherment\n' "$3" "$4" > "$1.ext"
    openssl x509 -req -in "$1.csr" -CA ca.crt -CAkey ca.key -CAcreateserial -days 2 -extfile "$1.ext" -out "$1.crt" 2>/dev/null
  }
  leaf server "/CN=k8s-ws-proxy.catalyst-system.svc" \
    "DNS:k8s-ws-proxy,DNS:k8s-ws-proxy.catalyst-system,DNS:k8s-ws-proxy.catalyst-system.svc,DNS:k8s-ws-proxy.catalyst-system.svc.cluster.local" serverAuth
  leaf client "/CN=guacd.guacamole.svc.cluster.local" "DNS:guacd.guacamole.svc.cluster.local" clientAuth
  leaf session "/CN=session-5b0f6f0e-6f1e-4c58-9d0c-0d6b1f1f3a11" "URI:urn:openova:access:exec:acme:exec-target:main" clientAuth
  head -c 32 /dev/urandom | base64 > hmac
)
cp "$PKI/client.crt" "$CREDS/exec-client.crt"; cp "$PKI/client.key" "$CREDS/exec-client.key"
cp "$PKI/session.crt" "$CREDS/exec-session.crt"; cp "$PKI/session.key" "$CREDS/exec-session.key"
cp "$PKI/ca.crt" "$CREDS/exec-ca.crt"
openssl x509 -in "$PKI/session.crt" -noout -subject -ext subjectAltName 2>/dev/null | tr '\n' ' ' | tee "$EV/session-cert.txt"; echo

note "deploy targets"
for ns in spike guacamole catalyst-system acme; do kubectl create namespace "$ns" >/dev/null; done
kubectl -n spike create configmap sshd-ca --from-file=trusted-user-ca.pub=/tmp/trusted-user-ca.pub >/dev/null
kubectl -n catalyst-system create configmap proxy-files --from-file=tls.crt="$PKI/server.crt" \
  --from-file=tls.key="$PKI/server.key" --from-file=ca.crt="$PKI/ca.crt" --from-file=hmac="$PKI/hmac" >/dev/null
kubectl apply -f "$SPIKE_DIR/k8s/targets.yaml"
kubectl -n spike rollout status deploy/sshd --timeout=180s
kubectl -n spike rollout status deploy/vnc --timeout=180s || note "vnc target not ready"
kubectl -n catalyst-system rollout status deploy/k8s-ws-proxy --timeout=180s || kubectl -n catalyst-system logs deploy/k8s-ws-proxy --tail=30
kubectl -n acme wait --for=condition=Ready pod/exec-target --timeout=180s
kubectl get svc -A -o wide | tee "$EV/services.txt"
echo "services with a node port: $(kubectl get svc -A -o json | jq '[.items[].spec.ports[]? | select(.nodePort != null)] | length')" | tee -a "$EV/services.txt"

note "issue SSH certificates (ttl 5m) from the development OpenBao SSH mount"
issue() { # key_type key_bits name
  local kt=$1 kb=$2 n=$3
  bao -X POST "$BAO/v1/ssh/issue/$SSH_ROLE" \
    -d "{\"valid_principals\":\"spike\",\"key_type\":\"$kt\",\"key_bits\":$kb,\"key_id\":\"spike-session-$n\",\"ttl\":\"5m\"}" > "/tmp/issue-$n.json"
  jq -r '.data.private_key // empty' "/tmp/issue-$n.json" > "$CREDS/ssh-$n.key"
  jq -r '.data.signed_key // empty' "/tmp/issue-$n.json" > "$CREDS/ssh-$n.cert"
  awk '{printf "%s", $2}' "$CREDS/ssh-$n.cert" > "$CREDS/ssh-$n.cert-b64"
  jq -c '{response_keys: (.data // {} | keys), private_key_type: .data.private_key_type, serial_number: .data.serial_number,
          private_key_first_line: ((.data.private_key // "") | split("\n")[0]), signed_key_type: ((.data.signed_key // "") | split(" ")[0]), errors}' \
    "/tmp/issue-$n.json" | tee "$EV/bao-issue-$n.json"
  ssh-keygen -L -f "$CREDS/ssh-$n.cert" > "$EV/ssh-cert-$n.txt" 2>&1; cat "$EV/ssh-cert-$n.txt"
}
issue ed25519 0 ed25519
issue ec 256 ecdsa
issue rsa 2048 rsa
date -u +%s > "$EV/certs-issued-at.txt"

note "agent configuration and credential files"
SSHD=sshd.spike.svc.cluster.local
PROXY=k8s-ws-proxy.catalyst-system.svc.cluster.local
jq -n --arg sshd "$SSHD" --arg proxy "$PROXY" '{
  subject: "jane.roe@example.org",
  scenarios: {
    "ssh-password": {protocol: "ssh", record: true, terminal: true,
      params: {hostname: $sshd, port: "22", username: "spike", password: "spike-pass-020", "enable-sftp": "false"}},
    "ssh-cert-ed25519": {protocol: "ssh", record: true, terminal: true,
      params: {hostname: $sshd, port: "22", username: "spike", "enable-sftp": "false"},
      paramFiles: {"private-key": "ssh-ed25519.key", "public-key": "ssh-ed25519.cert"}},
    "ssh-cert-ed25519-b64": {protocol: "ssh", record: true, terminal: true,
      params: {hostname: $sshd, port: "22", username: "spike", "enable-sftp": "false"},
      paramFiles: {"private-key": "ssh-ed25519.key", "public-key": "ssh-ed25519.cert-b64"}},
    "ssh-key-only-ed25519": {protocol: "ssh", record: true, terminal: true,
      params: {hostname: $sshd, port: "22", username: "spike", "enable-sftp": "false"},
      paramFiles: {"private-key": "ssh-ed25519.key"}},
    "ssh-cert-ecdsa": {protocol: "ssh", record: true, terminal: true,
      params: {hostname: $sshd, port: "22", username: "spike", "enable-sftp": "false"},
      paramFiles: {"private-key": "ssh-ecdsa.key", "public-key": "ssh-ecdsa.cert"}},
    "ssh-cert-rsa": {protocol: "ssh", record: true, terminal: true,
      params: {hostname: $sshd, port: "22", username: "spike", "enable-sftp": "false"},
      paramFiles: {"private-key": "ssh-rsa.key", "public-key": "ssh-rsa.cert"}},
    "k8s-cert": {protocol: "kubernetes", record: true, terminal: true,
      params: {hostname: $proxy, port: "8443", "use-ssl": "true", namespace: "acme", pod: "exec-target", container: "main", "exec-command": "/bin/sh"},
      paramFiles: {"client-cert": "exec-client.crt", "client-key": "exec-client.key", "ca-cert": "exec-ca.crt"}},
    "k8s-nocert": {protocol: "kubernetes", record: true, terminal: true,
      params: {hostname: $proxy, port: "8443", "use-ssl": "true", namespace: "acme", pod: "exec-target", container: "main", "exec-command": "/bin/sh"},
      paramFiles: {"ca-cert": "exec-ca.crt"}},
    "k8s-sessioncert": {protocol: "kubernetes", record: true, terminal: true,
      params: {hostname: $proxy, port: "8443", "use-ssl": "true", namespace: "acme", pod: "exec-target", container: "main", "exec-command": "/bin/sh"},
      paramFiles: {"client-cert": "exec-session.crt", "client-key": "exec-session.key", "ca-cert": "exec-ca.crt"}},
    "k8s-multiword": {protocol: "kubernetes", record: true, terminal: true,
      params: {hostname: $proxy, port: "8443", "use-ssl": "true", namespace: "acme", pod: "exec-target", container: "main", "exec-command": "/bin/echo SPLIT-OK"},
      paramFiles: {"client-cert": "exec-client.crt", "client-key": "exec-client.key", "ca-cert": "exec-ca.crt"}},
    "vnc": {protocol: "vnc", record: true, terminal: false,
      params: {hostname: "vnc.spike.svc.cluster.local", port: "5900", password: "spike-vnc"}}
  }}' > /tmp/config.json
kubectl -n guacamole create configmap spike-agent-config --from-file=config.json=/tmp/config.json >/dev/null
kubectl -n guacamole create configmap spike-creds --from-file="$CREDS" >/dev/null
kubectl -n guacamole get configmap spike-creds -o json | jq -c '.data | keys' | tee "$EV/creds-files.txt"

note "session pod (item k, cold: the guacd image is pulled by k3s now)"
if time_to_ready access-spike; then
  echo "SESSION_POD_SHAPE=strict" >> "$EV/state.env"
else
  kubectl -n guacamole describe pod access-spike | tail -40
  kubectl -n guacamole logs access-spike -c guacd --tail=40
  kubectl -n guacamole delete pod access-spike --wait=true >/dev/null
  note "strict shape did not become ready; retrying without the #strict lines"
  time_to_ready access-spike 1 && echo "SESSION_POD_SHAPE=relaxed" >> "$EV/state.env"
fi
kubectl -n guacamole get events --sort-by=.lastTimestamp -o custom-columns=T:.lastTimestamp,R:.reason,O:.involvedObject.name,M:.message \
  | grep -E 'access-spike' | tee "$EV/k-events.txt"
POD_IP=$(kubectl -n guacamole get pod access-spike -o jsonpath='{.status.podIP}')
echo "POD_IP=$POD_IP" >> "$EV/state.env"
curl -sS -m 5 -o /dev/null -w "runner to pod $POD_IP:8443/healthz -> HTTP %{http_code}\n" "http://$POD_IP:8443/healthz" | tee -a "$EV/timeline.txt"
