#!/usr/bin/env bash
# ACC-030 lab test, job "items": items (a) to (h) on one throwaway kind
# cluster. Item (i) (rotation) runs in rotation.sh, one job per interval.
HERE=$(cd "$(dirname "$0")" && pwd)
# shellcheck source=lib.sh
source "$HERE/lib.sh"

waitfile() { local i; for i in $(seq 1 "${2:-900}"); do [ -s "$1" ] && return 0; sleep 1; done; return 1; }

# ---------------------------------------------------------------------------
hdr "precondition and versions"
pre=$(cd "$REPO" && git grep -l -E 'ssh/issue|pki/issue|db/creds' -- platform products core infra clusters | wc -l)
rec "PRECONDITION git grep -l -E 'ssh/issue|pki/issue|db/creds' -- platform products core infra clusters | wc -l = $pre"
rec "VERSION kubernetes=$(kubectl version -o json | jq -r .serverVersion.gitVersion) helm=$(helm version --short) go=$(go version 2>/dev/null | cut -d' ' -f3) $(ssh -V 2>&1) $(openssl version | cut -d' ' -f1-2)"

helm_repos
install_csi 3600s
install_openbao

# ---------------------------------------------------------------------------
hdr "background installs: virtual clusters, SeaweedFS, cert-manager, proxy build, python"
vc_values() { cat <<EOF
controlPlane:
  distro:
    k8s:
      enabled: true
  backingStore:
    database:
      embedded:
        enabled: true
  coredns:
    deployment:
      image: coredns/coredns:1.11.3
  statefulSet:
    image:
      repository: loft-sh/vcluster-oss
sync:
  toHost:
    serviceAccounts:
      enabled: $1
EOF
}
vc_values false >"$WORK/vc-org1.yaml"   # org1: the platform's Organization values (service-account sync off)
vc_values true >"$WORK/vc-org2.yaml"    # org2: the same with service-account sync on
for o in org1 org2; do
  ( helm install vcluster loft/vcluster --version "$VC_CHART" -n $o --create-namespace -f "$WORK/vc-$o.yaml" \
      --wait --timeout 10m >"$OUT/vc-$o-install.log" 2>&1; echo $? >"$WORK/vc-$o.rc" ) &
done

cat >"$WORK/sw-values.yaml" <<'EOF'
seaweedfs:
  global:
    registry: docker.io/chrislusf
  volume:
    replicas: 1
  s3:
    replicas: 1
    createBuckets:
      - name: access-recordings
        objectLock: true
        versioning: Enabled
EOF
( helm install seaweedfs "$REPO/platform/seaweedfs/chart" -n seaweedfs --create-namespace -f "$WORK/sw-values.yaml" \
    --wait --timeout 15m >"$OUT/seaweedfs-install.log" 2>&1; echo $? >"$WORK/sw.rc" ) &
( for i in $(seq 1 900); do
    p=$(kubectl -n seaweedfs get pods -l job-name=seaweedfs-bucket-hook -o jsonpath='{range .items[*]}{.metadata.name} {.status.phase}{"\n"}{end}' 2>/dev/null | awk '$2!="Pending"{print $1; exit}')
    if [ -n "$p" ]; then kubectl -n seaweedfs logs -f "$p" >"$OUT/seaweedfs-bucket-hook.log" 2>&1; break; fi
    sleep 1
  done ) &

( helm install cert-manager jetstack/cert-manager --version "$CM_CHART" -n cert-manager --create-namespace \
    --set crds.enabled=true --wait --timeout 8m >"$OUT/cert-manager-install.log" 2>&1; echo $? >"$WORK/cm.rc" ) &

( cd "$REPO/core/cmd/k8s-ws-proxy" && CGO_ENABLED=0 go build -o "$WORK/k8s-ws-proxy" . >"$OUT/proxy-build.log" 2>&1; echo $? >"$WORK/proxy.rc" ) &

( python3 -m venv "$WORK/venv" && "$WORK/venv/bin/pip" install -q 'boto3<1.36' cryptography >"$OUT/pip.log" 2>&1; echo $? >"$WORK/pip.rc" ) &

install_pg

# ---------------------------------------------------------------------------
hdr "OpenBao engines, policies and roles"
setup_auth
setup_pki
setup_ssh
install_sshd
setup_db
bx secrets enable -path=mounted -version=2 kv >/dev/null
sshd_ready
ns_with_sa spike access-session
ns_with_sa guacamole access-session access-controller
ns_with_sa other access-session

# ---------------------------------------------------------------------------
hdr "(a) ssh/issue through the mount"
spc spike access-s-a access-session "$(objs_ssh)"
session_pod spike access-sa access-s-a
RA=$(pod_result spike access-sa)
rec "ITEM-A mount of ssh/issue/acc.spike.target1 (method PUT, secretArgs) in a pod of namespace spike: $RA"
if [ "$RA" = PASS ]; then
  rec "ITEM-A files: $(kubectl -n spike exec access-sa -- ls -ln /var/run/secrets/openova/ 2>&1 | grep -v '^total' | awk '{print $1, $3":"$4, $5"B", $NF}' | paste -sd';' -)"
  pf spike access-sa private-key >"$WORK/a.key"; pf spike access-sa public-key >"$WORK/a.cert"; chmod 600 "$WORK/a.key"
  rec "ITEM-A private-key first line: $(head -1 "$WORK/a.key") | public-key type: $(cut -d' ' -f1 "$WORK/a.cert")"
  L=$(ssh-keygen -L -f "$WORK/a.cert" 2>&1)
  rec "ITEM-A certificate: $(grep -E 'Type:|Key ID:|Valid:|Signing CA:' <<<"$L" | oneline 400) | principals: $(sed -n '/Principals:/,/Critical Options:/p' <<<"$L" | grep -v -E 'Principals:|Critical Options:' | oneline 80) | critical options: $(sed -n '/Critical Options:/,/Extensions:/p' <<<"$L" | grep -v -E 'Critical Options:|Extensions:' | oneline 80) | extensions: $(sed -n '/Extensions:/,$p' <<<"$L" | grep -v 'Extensions:' | oneline 120)"
  kfp=$(ssh-keygen -y -f "$WORK/a.key" 2>"$WORK/kfp.err" | ssh-keygen -lf - 2>/dev/null | awk '{print $2}')
  cfp=$(awk '/Public key:/{print $NF}' <<<"$L")
  if [ -n "$kfp" ] && [ "$kfp" = "$cfp" ]; then m=yes; else m="no (key=$kfp cert=$cfp $(oneline 120 <"$WORK/kfp.err"))"; fi
  rec "ITEM-A private key and signed key are one pair: $m"
  rec "ITEM-A ssh login as alice (the principal in the certificate): $(ssh_try "$WORK/a.key" "$WORK/a.cert" alice)"
  rec "ITEM-A ssh login as bob (not in the certificate): $(ssh_try "$WORK/a.key" "$WORK/a.cert" bob)"
fi
spc other access-s-a-other access-session "$(objs_ssh)"
session_pod other access-sa-other access-s-a-other
rec "ITEM-A negative: a pod of namespace other asks ssh/issue/acc.spike.target1: $(pod_result other access-sa-other 60)"
rec "ITEM-A openbao: successful ssh/issue responses=$(audit_ok ssh/issue/acc.spike.target1) errors=[$(audit_err ssh/issue/acc.spike.target1)]"

# ---------------------------------------------------------------------------
hdr "(b) pki/issue with a URI name; templated allowed_uri_sans"
cert_desc() { # file
  local s e
  s=$(date -d "$(openssl x509 -in "$1" -noout -startdate | cut -d= -f2)" +%s); e=$(date -d "$(openssl x509 -in "$1" -noout -enddate | cut -d= -f2)" +%s)
  echo "subject=[$(openssl x509 -in "$1" -noout -subject | sed 's/^subject=//')] san=[$(openssl x509 -in "$1" -noout -ext subjectAltName 2>/dev/null | tail -n +2 | oneline 200)] eku=[$(openssl x509 -in "$1" -noout -ext extendedKeyUsage 2>/dev/null | tail -n +2 | oneline 80)] key=[$(openssl x509 -in "$1" -noout -text | grep -E 'Public Key Algorithm|NIST CURVE' | sed 's/^ *//' | paste -sd' ' -)] lifetime=$((e - s))s issuer=[$(openssl x509 -in "$1" -noout -issuer | sed 's/^issuer=//')]"
}
spc spike access-s-b access-session "$(objs_pki pki/issue/access-exec-self urn:openova:access:exec:spike:access-sb:tool)"
session_pod spike access-sb access-s-b
RB=$(pod_result spike access-sb)
rec "ITEM-B mount of pki/issue/access-exec-self with uri_sans of the pod's own namespace (spike): $RB"
if [ "$RB" = PASS ]; then
  pf spike access-sb exec-cert >"$WORK/b.crt"; pf spike access-sb exec-key >"$WORK/b.key"; pf spike access-sb exec-ca >"$WORK/b.ca"
  rec "ITEM-B exec-cert: $(cert_desc "$WORK/b.crt")"
  rec "ITEM-B exec-key first line: $(head -1 "$WORK/b.key") | key matches certificate: $([ "$(openssl pkey -in "$WORK/b.key" -pubout 2>/dev/null | h8)" = "$(openssl x509 -in "$WORK/b.crt" -pubkey -noout | h8)" ] && echo yes || echo no)"
  rec "ITEM-B exec-ca: certificates=$(grep -c 'BEGIN CERTIFICATE' "$WORK/b.ca") subject=[$(openssl x509 -in "$WORK/b.ca" -noout -subject | sed 's/^subject=//')] equals the pki intermediate: $([ "$(openssl x509 -in "$WORK/b.ca" -outform der | h8)" = "$(openssl x509 -in "$WORK/int.pem" -outform der | h8)" ] && echo yes || echo no)"
fi
spc spike access-s-b-neg access-session "$(objs_pki pki/issue/access-exec-self urn:openova:access:exec:other:access-sbn:tool)"
session_pod spike access-sbn access-s-b-neg
rec "ITEM-B template negative: a pod of namespace spike asks a uri of namespace other: $(pod_result spike access-sbn 60)"
spc guacamole access-s-p access-session-platform "$(objs_pki pki/issue/access-exec urn:openova:access:exec:spike:target:app)"
session_pod guacamole access-sp access-s-p
RP=$(pod_result guacamole access-sp)
rec "ITEM-B platform role: pod of namespace guacamole, role access-session-platform, pki/issue/access-exec: $RP"
if [ "$RP" = PASS ]; then
  pf guacamole access-sp exec-cert >"$WORK/p.crt"; pf guacamole access-sp exec-key >"$WORK/p.key"; pf guacamole access-sp exec-ca >"$WORK/p.ca"
  rec "ITEM-B platform exec-cert: $(cert_desc "$WORK/p.crt")"
fi
rec "ITEM-B openbao: pki/issue/access-exec-self ok=$(audit_ok pki/issue/access-exec-self) errors=[$(audit_err pki/issue/access-exec-self)]; pki/issue/access-exec ok=$(audit_ok pki/issue/access-exec) errors=[$(audit_err pki/issue/access-exec)]"

# ---------------------------------------------------------------------------
hdr "(c) db/creds through the mount; the user after pod deletion; the lease identifier"
spc spike access-s-c access-session "$(objs_db)
- objectName: db-lease
  secretPath: db/creds/$DBROLE"
session_pod spike access-sc access-s-c
RC=$(pod_result spike access-sc)
TC0=$(date +%s)
rec "ITEM-C mount of db/creds/$DBROLE (method GET) in a pod of namespace spike: $RC"
CU=""; CP=""; CLEASE=""
if [ "$RC" = PASS ]; then
  CU=$(pf spike access-sc db-username); CP=$(pf spike access-sc db-password)
  pf spike access-sc db-lease >"$WORK/c.json"
  CLEASE=$(jq -r '.lease_id // empty' "$WORK/c.json")
  rec "ITEM-C user=$CU connect with the mounted pair: $(pg_try "$CU" "$CP")"
  rec "ITEM-C object without secretKey: top-level keys=$(jq -c 'keys' "$WORK/c.json") lease_id=$CLEASE lease_duration=$(jq -r .lease_duration "$WORK/c.json") renewable=$(jq -r .renewable "$WORK/c.json") data keys=$(jq -c '.data|keys' "$WORK/c.json") same user as db-username: $([ "$(jq -r .data.username "$WORK/c.json")" = "$CU" ] && echo yes || echo no)"
  rec "ITEM-C openbao: db/creds reads for this pod's mount=$(audit_ok "db/creds/$DBROLE") (three objects, one mount request)"
  rec "ITEM-C lease lookup: $(bx write -format=json sys/leases/lookup lease_id="$CLEASE" 2>&1 | jq -c '.data | {ttl, expire_time, renewable}' 2>/dev/null)"
  rec "ITEM-C login token of the mount (from the audit log): $(bao_log | jq -R -c 'fromjson? | select(.type=="response" and .request.path=="auth/kubernetes/login" and (.auth.metadata.role // "")=="access-session" and (.auth.metadata.service_account_namespace // "")=="spike") | {ttl: .auth.token_ttl, policies: .auth.policies}' | sort -u | oneline 300)"
  kubectl -n spike delete pod access-sc --wait=true >/dev/null 2>&1
  sleep 10
  rec "ITEM-C 10 s after pod deletion: user in pg_roles=$(pg_users | tr ',' '\n' | grep -cxF "$CU") lease present=$(bx write -format=json sys/leases/lookup lease_id="$CLEASE" >/dev/null 2>&1 && echo yes || echo no) connect=$(pg_try "$CU" "$CP")"
fi
for r in "$DBROLE" "${DBROLE}2"; do
  j=$(bx read -format=json "db/creds/$r" 2>&1); u=$(jq -r '.data.username // empty' <<<"$j" 2>/dev/null); l=$(jq -r '.lease_id // empty' <<<"$j" 2>/dev/null)
  o=$(bx lease revoke -sync "$l" 2>&1); rc=$?
  if [ "$r" = "$DBROLE" ]; then a="openbao_dbadmin, PostgreSQL defaults"; else a="openbao_dbadmin2 with createrole_self_grant='set, inherit'"; fi
  rec "ITEM-C explicit revocation, role $r (connection user $a): rc=$rc output=[$(oneline 300 <<<"$o")] user still in pg_roles=$(pg_users | tr ',' '\n' | grep -cxF "$u")"
done

# ---------------------------------------------------------------------------
hdr "(d) pods without a service-account token"
for p in spike/access-sa spike/access-sb guacamole/access-sp; do
  rec "ITEM-D $p automountServiceAccountToken=$(kubectl -n "${p%/*}" get pod "${p#*/}" -o jsonpath='{.spec.automountServiceAccountToken}') token directory in the container: $(kubectl -n "${p%/*}" exec "${p#*/}" -- ls /var/run/secrets/kubernetes.io/serviceaccount 2>&1 | oneline 100) mount: $(pod_result "${p%/*}" "${p#*/}" 5)"
done

# ---------------------------------------------------------------------------
hdr "(h) transit/sign and transit/verify with an ed25519 key"
bx secrets enable transit >/dev/null
w transit/keys/access-recordings type=ed25519
rec "ITEM-H key: $(bx read -format=json transit/keys/access-recordings | jq -c '.data | {type, exportable, deletion_allowed, supports_signing, latest_version}')"
JWT=$(kubectl -n guacamole create token access-controller --audience openbao --duration 10m)
TB=$(bx write -field=token auth/kubernetes/login role=access-binder jwt="$JWT" 2>"$WORK/tb.err")
rec "ITEM-H login role access-binder: $([ -n "$TB" ] && echo ok || echo "fail $(oneline 200 <"$WORK/tb.err")")"
printf '{\n  "schema": "openova.io/access-recording/v1",\n  "sessionUID": "%s"\n}\n' "$SUID" >"$WORK/m.json"
B64=$(base64 -w0 "$WORK/m.json")
S=$(bxt "$TB" write -format=json transit/sign/access-recordings input="$B64" prehashed=false 2>&1)
SIG=$(jq -r '.data.signature // empty' <<<"$S" 2>/dev/null)
rec "ITEM-H sign: $([ -n "$SIG" ] && echo "ok signature-prefix=$(cut -d: -f1-2 <<<"$SIG") signature-bytes=$(cut -d: -f3 <<<"$SIG" | base64 -d 2>/dev/null | wc -c)" || echo "fail $(oneline 200 <<<"$S")")"
rec "ITEM-H verify original: $(bxt "$TB" write -format=json transit/verify/access-recordings input="$B64" signature="$SIG" 2>&1 | jq -c '.data' 2>/dev/null)"
B64X=$(sed 's/v1/v2/' "$WORK/m.json" | base64 -w0)
rec "ITEM-H verify one changed byte: $(bxt "$TB" write -format=json transit/verify/access-recordings input="$B64X" signature="$SIG" 2>&1 | jq -c '.data' 2>/dev/null)"
rec "ITEM-H sign with prehashed=true: $(bxt "$TB" write -format=json transit/sign/access-recordings input="$(printf x | sha256sum | cut -c1-64 | xxd -r -p | base64 -w0)" prehashed=true 2>&1 | oneline 200)"
PUB=$(bx read -format=json transit/keys/access-recordings | jq -r '.data.keys["1"].public_key // empty')
waitfile "$WORK/pip.rc" 300
rec "ITEM-H offline verify with the exported public key (python cryptography): $("$WORK/venv/bin/python" -I - "$PUB" "$(cut -d: -f3 <<<"$SIG")" "$WORK/m.json" <<'PY' 2>&1 | oneline 200
import base64, sys
from cryptography.hazmat.primitives.asymmetric.ed25519 import Ed25519PublicKey
pub = base64.b64decode(sys.argv[1]); sig = base64.b64decode(sys.argv[2]); data = open(sys.argv[3], "rb").read()
try:
    Ed25519PublicKey.from_public_bytes(pub).verify(sig, data); print("valid")
except Exception as e:
    print("invalid", repr(e))
PY
)"
JS=$(kubectl -n spike create token access-session --audience openbao --duration 10m)
TS=$(bx write -field=token auth/kubernetes/login role=access-session jwt="$JS" 2>/dev/null)
rec "ITEM-H a session token (role access-session) calls transit/sign: $(bxt "$TS" write transit/sign/access-recordings input="$B64" 2>&1 | oneline 160)"

# ---------------------------------------------------------------------------
hdr "(f) what the exec proxy needs in its client authority file"
waitfile "$WORK/cm.rc" 600; rec "SETUP cert-manager chart=$CM_CHART rc=$(cat "$WORK/cm.rc")"
waitfile "$WORK/proxy.rc" 600; rec "SETUP k8s-ws-proxy built from this commit: rc=$(cat "$WORK/proxy.rc") $(tail -3 "$OUT/proxy-build.log" | oneline 200)"
kubectl apply -f - >/dev/null <<'EOF'
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: {name: vault-issuer-token, namespace: cert-manager}
rules:
  - apiGroups: [""]
    resources: [serviceaccounts/token]
    resourceNames: [cert-manager]
    verbs: [create]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: {name: vault-issuer-token, namespace: cert-manager}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: Role, name: vault-issuer-token}
subjects:
  - {kind: ServiceAccount, name: cert-manager, namespace: cert-manager}
EOF
kubectl create ns catalyst-system >/dev/null 2>&1
for v in "openbao-internal pki k8s-ws-proxy-access-trust" "openbao-internal-b pki2 k8s-ws-proxy-access-trust-b"; do
  set -- $v
  kubectl apply -f - >/dev/null <<EOF
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata: {name: $1}
spec:
  vault:
    server: $BAO_SVC
    path: $2/sign/internal-server
    auth:
      kubernetes:
        mountPath: /v1/auth/kubernetes
        role: cert-manager-pki
        serviceAccountRef: {name: cert-manager}
---
apiVersion: cert-manager.io/v1
kind: Certificate
metadata: {name: $3, namespace: catalyst-system}
spec:
  secretName: $3
  commonName: $3.catalyst-system.svc.cluster.local
  dnsNames: [$3.catalyst-system.svc.cluster.local]
  duration: 24h
  privateKey: {algorithm: ECDSA, size: 256}
  issuerRef: {kind: ClusterIssuer, name: $1}
EOF
done
pem_desc() { # file -> n=<count> [subjects]
  echo "n=$(grep -c 'BEGIN CERTIFICATE' "$1" 2>/dev/null) [$(openssl crl2pkcs7 -nocrl -certfile "$1" 2>/dev/null | openssl pkcs7 -print_certs -noout 2>/dev/null | grep '^subject' | sed 's/^subject=//' | paste -sd'|' -)]"
}
for c in k8s-ws-proxy-access-trust k8s-ws-proxy-access-trust-b; do
  if kubectl -n catalyst-system wait --for=condition=Ready "certificate/$c" --timeout=180s >/dev/null 2>&1; then
    kubectl -n catalyst-system get secret "$c" -o jsonpath='{.data.ca\.crt}' | base64 -d >"$WORK/$c.ca.crt"
    kubectl -n catalyst-system get secret "$c" -o jsonpath='{.data.tls\.crt}' | base64 -d >"$WORK/$c.tls.crt"
    rec "ITEM-F cert-manager Secret $c: ca.crt $(pem_desc "$WORK/$c.ca.crt") tls.crt $(pem_desc "$WORK/$c.tls.crt")"
  else
    rec "ITEM-F cert-manager Certificate $c not Ready: $(kubectl -n catalyst-system describe certificate "$c" | tail -6 | oneline 300) issuer: $(kubectl get clusterissuer -o jsonpath='{range .items[*]}{.metadata.name}={.status.conditions[0].message};{end}')"
  fi
done
bget pki/ca_chain >"$WORK/pki.ca_chain"; bget pki2/ca_chain >"$WORK/pki2.ca_chain"
rec "ITEM-F GET /v1/pki/ca_chain: $(pem_desc "$WORK/pki.ca_chain") | GET /v1/pki2/ca_chain: $(pem_desc "$WORK/pki2.ca_chain")"
# Client certificates: C1 from the mount of (b) (role access-exec on pki); C2 issued on pki2; C3 on pki with a CN that is not allow-listed.
if [ ! -s "$WORK/p.crt" ]; then
  j=$(bx write -format=json pki/issue/access-exec common_name="session-$SUID" uri_sans=urn:openova:access:exec:spike:target:app ttl=5m)
  jq -r .data.certificate <<<"$j" >"$WORK/p.crt"; jq -r .data.private_key <<<"$j" >"$WORK/p.key"; jq -r .data.issuing_ca <<<"$j" >"$WORK/p.ca"
  rec "ITEM-F note: client certificate C1 issued by the CLI because the mount of (b) failed"
fi
j=$(bx write -format=json pki2/issue/access-exec common_name="session-$SUID" uri_sans=urn:openova:access:exec:spike:target:app ttl=5m)
jq -r .data.certificate <<<"$j" >"$WORK/c2.crt"; jq -r .data.private_key <<<"$j" >"$WORK/c2.key"; jq -r .data.issuing_ca <<<"$j" >"$WORK/c2.ca"
j=$(bx write -format=json pki/issue/access-exec common_name="someone-else" uri_sans=urn:openova:access:exec:spike:target:app ttl=5m)
jq -r .data.certificate <<<"$j" >"$WORK/c3.crt"; jq -r .data.private_key <<<"$j" >"$WORK/c3.key"
cat "$WORK/p.crt" "$WORK/p.ca" >"$WORK/p.chain"; cat "$WORK/c2.crt" "$WORK/c2.ca" >"$WORK/c2.chain"
rec "ITEM-F C2 (pki2) issuing_ca $(pem_desc "$WORK/c2.ca") ca_chain-in-response n=$(jq -r '.data.ca_chain | length' <<<"$j" 2>/dev/null)"
sudo mkdir -p /var/run/secrets/kubernetes.io/serviceaccount && echo spike | sudo tee /var/run/secrets/kubernetes.io/serviceaccount/token >/dev/null
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes -subj /CN=k8s-ws-proxy -days 1 \
  -keyout "$WORK/srv.key" -out "$WORK/srv.crt" >/dev/null 2>&1
head -c 32 /dev/urandom | base64 >"$WORK/hmac"
proxy_start() { # name tlsport cafile
  env -i PATH="$PATH" HOME="$HOME" SHARED_SECRET_FILE="$WORK/hmac" WS_PROXY_LISTEN_ADDR="127.0.0.1:$(($2 + 1000))" \
    TLS_LISTEN_ADDR="127.0.0.1:$2" TLS_CERT_FILE="$WORK/srv.crt" TLS_KEY_FILE="$WORK/srv.key" TLS_CLIENT_CA_FILE="$3" \
    CLIENT_CERT_ALLOWED_SUBJECTS="session-$SUID" ALLOWED_NAMESPACES=guacamole \
    KUBERNETES_SERVICE_HOST=127.0.0.1 KUBERNETES_SERVICE_PORT=6443 "$WORK/k8s-ws-proxy" >"$OUT/proxy-$1.log" 2>&1 &
}
curl_try() { # port cert key path
  local o rc
  o=$(curl -sS -k --max-time 10 --cert "$2" --key "$3" -o "$WORK/curl.body" -w '%{http_code}' "https://127.0.0.1:$1$4" 2>&1); rc=$?
  if [ $rc -eq 0 ]; then echo "http=$o $(head -c 80 "$WORK/curl.body" | oneline 80)"; else echo "refused(curl-exit=$rc $(printf '%s' "$o" | oneline 140))"; fi
}
EXECP='/api/v1/namespaces/spike/pods/target/exec?container=app&command=sh'
n=0
for v in "V1-cert-manager-ca.crt $WORK/k8s-ws-proxy-access-trust.ca.crt p" \
         "V2-issuing_ca-from-the-mount $WORK/p.ca p" \
         "V3-pki-ca_chain $WORK/pki.ca_chain p" \
         "V4-root-only $WORK/root.pem p" \
         "V5-cert-manager-ca.crt-pki2-bundle $WORK/k8s-ws-proxy-access-trust-b.ca.crt c2" \
         "V6-pki2-ca_chain $WORK/pki2.ca_chain c2"; do
  set -- $v; n=$((n + 1)); port=$((19440 + n))
  if [ ! -s "$2" ]; then rec "ITEM-F $1: file missing, not run"; continue; fi
  proxy_start "$1" "$port" "$2"; sleep 2
  if [ "$3" = p ]; then leaf=$WORK/p.crt; key=$WORK/p.key; chain=$WORK/p.chain; else leaf=$WORK/c2.crt; key=$WORK/c2.key; chain=$WORK/c2.chain; fi
  rec "ITEM-F $1 (file $(pem_desc "$2")): leaf only -> healthz $(curl_try $port "$leaf" "$key" /healthz); exec $(curl_try $port "$leaf" "$key" "$EXECP") | leaf+issuing CA -> healthz $(curl_try $port "$chain" "$key" /healthz); exec $(curl_try $port "$chain" "$key" "$EXECP")"
  rec "ITEM-F $1 proxy log: $(grep -o -E 'TLS handshake error[^"]*|authorization failed[^}]*|namespace denied' "$OUT/proxy-$1.log" | sort | uniq -c | oneline 300)"
done
proxy_start V7-allowlist "$((19440 + 7))" "$WORK/p.ca"; sleep 2
rec "ITEM-F V7 issuing_ca file, certificate of the same authority with a CN that is not allow-listed: exec $(curl_try $((19440 + 7)) "$WORK/c3.crt" "$WORK/c3.key" "$EXECP")"
pkill -f "$WORK/k8s-ws-proxy" 2>/dev/null

# ---------------------------------------------------------------------------
hdr "(g) object lock on the SeaweedFS bucket made by the chart's hook; listing of more than 1,000 keys"
waitfile "$WORK/sw.rc" 900
rec "SETUP seaweedfs chart=platform/seaweedfs/chart $(grep -E '^version:' "$REPO/platform/seaweedfs/chart/Chart.yaml") rc=$(cat "$WORK/sw.rc") pods=$(kubectl -n seaweedfs get pods -o jsonpath='{range .items[*]}{.metadata.name}={.status.phase};{end}') image=$(kubectl -n seaweedfs get sts seaweedfs-master -o jsonpath='{.spec.template.spec.containers[0].image}')"
rec "ITEM-G bucket hook log: $(oneline 600 <"$OUT/seaweedfs-bucket-hook.log" 2>/dev/null)"
wsh() { kubectl -n seaweedfs exec seaweedfs-master-0 -- sh -c 'printf "%s\n" "$1" | weed shell -master=localhost:9333 -filer=seaweedfs-filer:8888 2>&1' _ "$1" </dev/null; }
rec "ITEM-G weed shell s3.bucket.list: $(wsh 's3.bucket.list' | grep -v -E '^\s*$|^>' | oneline 300)"
SWSEC=$(kubectl -n seaweedfs get secret -l app.kubernetes.io/component=s3 -o json | jq -r '.items[0].data')
dec() { jq -r --arg k "$1" '.[$k]' <<<"$SWSEC" | base64 -d; }
( while :; do kubectl -n seaweedfs port-forward svc/seaweedfs-s3 18333:8333 >>"$OUT/pf-s3.log" 2>&1; sleep 1; done ) &
sleep 3
export S3_EP=http://127.0.0.1:18333 SUID ADMIN_AK ADMIN_SK READ_AK READ_SK
ADMIN_AK=$(dec admin_access_key_id); ADMIN_SK=$(dec admin_secret_access_key); READ_AK=$(dec read_access_key_id); READ_SK=$(dec read_secret_access_key)
"$WORK/venv/bin/python" -I "$HERE/objlock.py" main 2>&1 | tee "$OUT/objlock.txt" | grep '^G ' | tee -a "$RES"
rec "ITEM-G boto3 $("$WORK/venv/bin/python" -I -c 'import boto3; print(boto3.__version__)')"
BAK=broker$(openssl rand -hex 6); BSK=$(openssl rand -hex 20)
rec "ITEM-G weed shell s3.configure broker (Read,Write,List on the bucket): $(wsh "s3.configure -user=broker -access_key=$BAK -secret_key=$BSK -buckets=access-recordings -actions=Read,Write,List -apply" | grep -v -E '^\s*$' | tail -3 | sed "s/$BSK/<redacted>/g" | oneline 300)"
sleep 8
BROKER_AK=$BAK BROKER_SK=$BSK "$WORK/venv/bin/python" -I "$HERE/objlock.py" broker 2>&1 | tee -a "$OUT/objlock.txt" | grep '^G ' | tee -a "$RES"
KEY1="org/acme/2026/11/13/$SUID/screen-1/00000001.chunk"
V1=$(grep -o 'G-V1 [^ ]*' "$OUT/objlock.txt" | head -1 | cut -d' ' -f2)
rec "ITEM-G filer layout: $(wsh "fs.ls -l /buckets/access-recordings/org/acme/2026/11/13/$SUID/screen-1/" | grep -v -E '^\s*$|^>' | oneline 400)"
rec "ITEM-G storage administrator deletes the locked object through the filer (weed shell fs.rm -r): $(wsh "fs.rm -r /buckets/access-recordings/org/acme/2026/11/13/$SUID/screen-1" | grep -v -E '^\s*$|^>' | oneline 200)"
KEY="$KEY1" VID="$V1" "$WORK/venv/bin/python" -I "$HERE/objlock.py" check 2>&1 | tee -a "$OUT/objlock.txt" | grep '^G ' | tee -a "$RES"
rec "ITEM-G storage administrator deletes the bucket (weed shell s3.bucket.delete): $(wsh 's3.bucket.delete -name access-recordings' | grep -v -E '^\s*$|^>' | oneline 200)"
sleep 3
KEY="$KEY1" VID="$V1" "$WORK/venv/bin/python" -I "$HERE/objlock.py" bucket 2>&1 | tee -a "$OUT/objlock.txt" | grep '^G ' | tee -a "$RES"

# ---------------------------------------------------------------------------
hdr "(e) a pod inside the Organization's virtual cluster with a virtual service account access-session"
k() { # kubeconfig args...: kubectl against a virtual cluster, retried over port-forward restarts
  local kc=$1; shift
  local i rc errf="$WORK/k.err"
  for i in 1 2 3 4 5 6; do
    kubectl --kubeconfig "$kc" "$@" 2>"$errf"; rc=$?
    if [ $rc -ne 0 ] && grep -q -i -E 'connection refused|lost connection|broken pipe|EOF|connection reset|i/o timeout|unable to upgrade|error dialing|stream' "$errf"; then sleep 3; continue; fi
    break
  done
  cat "$errf" >&2; return $rc
}
vc_connect() { # ns port
  local ns=$1 port=$2 i kc="$WORK/vc-$1.kubeconfig"
  for i in $(seq 1 60); do kubectl -n "$ns" get secret vc-vcluster >/dev/null 2>&1 && break; sleep 2; done
  kubectl -n "$ns" get secret vc-vcluster -o jsonpath='{.data.config}' | base64 -d \
    | sed -E "s#server: https://[^[:space:]]+#server: https://localhost:$port#" >"$kc"
  ( while :; do kubectl -n "$ns" port-forward svc/vcluster "$port:443" >>"$OUT/pf-$ns.log" 2>&1; sleep 1; done ) &
  for i in $(seq 1 60); do kubectl --kubeconfig "$kc" get ns >/dev/null 2>&1 && break; sleep 2; done
}
host_pod() { # hostns vns vname -> "<host name> <service account>"
  kubectl -n "$1" get pods -o json 2>/dev/null | jq -r --arg vns "$2" --arg vn "$3" '.items[]
    | select((.metadata.annotations["vcluster.loft.sh/object-name"]==$vn and .metadata.annotations["vcluster.loft.sh/object-namespace"]==$vns)
             or .metadata.name == ($vn + "-x-" + $vns + "-x-vcluster"))
    | "\(.metadata.name) \(.spec.serviceAccountName)"' | head -1
}
vpod_result() { # hostns vns vname
  local hp ev i
  for i in $(seq 1 60); do hp=$(host_pod "$1" "$2" "$3" | cut -d' ' -f1); [ -n "$hp" ] && break; sleep 2; done
  [ -z "$hp" ] && { echo "FAIL(never synced to the host)"; return 1; }
  if kubectl -n "$1" wait --for=condition=Ready "pod/$hp" --timeout=120s >/dev/null 2>&1; then echo PASS; return 0; fi
  ev=$(kubectl -n "$1" get events --field-selector "involvedObject.name=$hp" -o jsonpath='{range .items[*]}{.reason}: {.message}{"\n"}{end}' 2>/dev/null | grep -E 'FailedMount|Failed|Error' | tail -1 | oneline 600)
  echo "FAIL(${ev:-not ready, no event})"; return 1
}
for o in org1 org2; do waitfile "$WORK/vc-$o.rc" 600; done
rec "SETUP vcluster chart=$VC_CHART install org1=$(cat "$WORK/vc-org1.rc") org2=$(cat "$WORK/vc-org2.rc") image=$(kubectl -n org1 get sts vcluster -o jsonpath='{.spec.template.spec.containers[0].image}')"
vc_connect org1 28443
vc_connect org2 29443
for o in org1 org2; do
  VC="$WORK/vc-$o.kubeconfig"
  bx kv put -mount=mounted "$o/shared/probe" value=probe-ok >/dev/null
  spc "$o" acc-e access-session "$(objs_pki pki/issue/access-exec-self "urn:openova:access:exec:$o:v-session:tool")"
  spc "$o" acc-e-ctl workload "- objectName: probe
  secretPath: mounted/data/$o/shared/probe
  secretKey: value"
  k "$VC" create ns apps >/dev/null
  k "$VC" -n apps create sa access-session >/dev/null
  for p in "v-session acc-e" "v-control acc-e-ctl"; do
    set -- $p
    cat >"$WORK/vpod-$o-$1.yaml" <<EOF
apiVersion: v1
kind: Pod
metadata: {name: $1, namespace: apps}
spec:
  serviceAccountName: access-session
  automountServiceAccountToken: false
  terminationGracePeriodSeconds: 1
  containers:
    - name: tool
      image: $PROBE_IMAGE
      command: ["sh", "-c", "sleep 3600"]
      volumeMounts:
        - {name: openova, mountPath: /var/run/secrets/openova, readOnly: true}
  volumes:
    - name: openova
      csi:
        driver: secrets-store.csi.k8s.io
        readOnly: true
        volumeAttributes: {secretProviderClass: $2}
EOF
    k "$VC" apply -f "$WORK/vpod-$o-$1.yaml" >/dev/null
  done
  if [ $o = org1 ]; then sync=off; else sync=on; fi
  rec "ITEM-E $o (service-account sync $sync) virtual pod v-session, virtual service account access-session: host pod and service account=[$(host_pod $o apps v-session)] role access-session mount: $(vpod_result $o apps v-session)"
  rec "ITEM-E $o (service-account sync $sync) control v-control, same identity, role workload: host=[$(host_pod $o apps v-control)] mount: $(vpod_result $o apps v-control)"
done
rec "ITEM-E openbao logins from org1/org2 (role:namespace/serviceaccount x count): $(bao_log | jq -R -r 'fromjson? | select(.type=="response" and .request.path=="auth/kubernetes/login" and (.error // "")=="") | (.auth.metadata // {}) as $m | select(($m.service_account_namespace // "") | test("^org")) | "\($m.role):\($m.service_account_namespace)/\($m.service_account_name)"' | sort | uniq -c | awk '{printf "%s(x%s) ", $2, $1}')"

# ---------------------------------------------------------------------------
hdr "(c) late check: the database user of the deleted pod after the login token's lifetime"
if [ -n "$CLEASE" ]; then
  left=$((TC0 + 420 - $(date +%s))); [ $left -gt 0 ] && sleep $left
  rec "ITEM-C $(( $(date +%s) - TC0 )) s after the mount (pod deleted at about 15 s; login token lifetime 5m): user in pg_roles=$(pg_users | tr ',' '\n' | grep -cxF "$CU") lease present=$(bx write -format=json sys/leases/lookup lease_id="$CLEASE" >/dev/null 2>&1 && echo yes || echo no) connect=$(pg_try "$CU" "$CP")"
  rec "ITEM-C openbao server log on revocation: $(kubectl -n openbao logs openbao-0 -c openbao --tail=-1 | grep -v '^{' | grep -i -E 'revok|expiration' | sed -E 's/lease_id=[^ ]+/lease_id=<id>/g' | cut -c1-260 | sort | uniq -c | sort -rn | head -6 | oneline 1200)"
fi
rec "END pg dynamic users now: $(pg_users)"

hdr "SUMMARY"
grep -E '^(PRECONDITION|ITEM-)' "$RES"
