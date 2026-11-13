#!/usr/bin/env bash
# ACC-030 lab test (never merged): shared helpers, sourced by items.sh and
# rotation.sh. Everything runs on a throwaway kind cluster inside one CI job.
#
# Every measurement is one line in $OUT/results.txt. A FAIL line is a measured
# result, not a script error; the scripts carry on to the next measurement.
set -uo pipefail

LIBDIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO=$(cd "$LIBDIR/../../.." && pwd)
OUT=${OUT:-$PWD/out}
mkdir -p "$OUT"
RES="$OUT/results.txt"
: >"$RES"
WORK=$(mktemp -d)
trap 'kill $(jobs -p) 2>/dev/null; rm -rf "$WORK"' EXIT

BAO_CHART=0.30.7            # OpenBao 2.7.2, ships CSI provider 2.0.3 (as SEC-140)
CSI_CHART=1.6.2             # Secrets Store CSI driver (the version SEC-211 wraps)
VC_CHART=0.33.4             # Organization virtual cluster (platform pins 0.33.*)
CM_CHART=v1.16.5            # cert-manager (bp-cert-manager pins v1.16.5)
BAO_SVC=http://openbao.openbao.svc.cluster.local:8200
PROBE_IMAGE=busybox:1.36
PG_IMAGE=postgres:16-alpine
SSHD_IMAGE=alpine:3.20
SUID=5b0f6f0e-6f1e-4c58-9d0c-0d6b1f1f3a11     # the session UID used in arguments
DBROLE=spike.access-session.billing.ro       # db/roles/<ns>.<sa>.<database>.<mode>
ROOT=""

rec() { printf '%s\n' "$*" | tee -a "$RES"; }
hdr() { printf '\n========== %s ==========\n' "$*"; }
oneline() { tr '\n\t' '  ' | tr -s ' ' | cut -c1-"${1:-600}"; }
h8() { sha256sum | cut -c1-8; }
now() { date -u +%Y-%m-%dT%H:%M:%SZ; }

# OpenBao CLI inside the server pod. bx: no stdin. bxi: stdin passed. bxt: other token.
bx()  { kubectl -n openbao exec openbao-0 -c openbao -- env BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN="$ROOT" bao "$@" </dev/null; }
bxi() { kubectl -n openbao exec -i openbao-0 -c openbao -- env BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN="$ROOT" bao "$@"; }
bxt() { local t=$1; shift; kubectl -n openbao exec openbao-0 -c openbao -- env BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN="$t" bao "$@" </dev/null; }
bao_log() { kubectl -n openbao logs openbao-0 -c openbao --tail=-1 2>/dev/null; }
prov_log() { kubectl -n openbao logs -l app.kubernetes.io/name=openbao-csi-provider -c openbao-csi-provider --tail=-1 2>/dev/null; }
# Unauthenticated raw read through a port-forward (ca_chain, ssh public key, ...).
bget() { curl -sS --max-time 10 "http://127.0.0.1:18200/v1/$1"; }

# Audit: successful responses on one path (exact), optionally only for one auth role.
audit_ok() { # path [role]
  bao_log | jq -R -r --arg p "$1" --arg r "${2:-}" 'fromjson? | select(.type=="response" and .request.path==$p and (.error // "")=="")
    | select($r=="" or ((.auth.metadata.role // "")==$r)) | .request.id' | sort -u | wc -l
}
audit_err() { # path
  bao_log | jq -R -r --arg p "$1" 'fromjson? | select(.type=="response" and .request.path==$p and (.error // "")!="") | .error' | sort | uniq -c | oneline 300
}

helm_repos() {
  helm repo add openbao https://openbao.github.io/openbao-helm >/dev/null
  helm repo add csi https://kubernetes-sigs.github.io/secrets-store-csi-driver/charts >/dev/null
  helm repo add loft https://charts.loft.sh >/dev/null
  helm repo add jetstack https://charts.jetstack.io >/dev/null
  helm repo update >/dev/null
}

install_csi() { # rotation poll interval
  local rc
  helm install csi csi/secrets-store-csi-driver --version "$CSI_CHART" -n kube-system \
    --set syncSecret.enabled=false --set enableSecretRotation=true --set rotationPollInterval="$1" \
    --set 'tokenRequests[0].audience=openbao' --wait --timeout 5m >"$OUT/csi-install.log" 2>&1
  rc=$?
  rec "SETUP csi-driver chart=$CSI_CHART rc=$rc image=$(kubectl -n kube-system get ds -l app=secrets-store-csi-driver -o jsonpath='{.items[0].spec.template.spec.containers[?(@.name=="secrets-store")].image}') args=[$(kubectl -n kube-system get ds -l app=secrets-store-csi-driver -o json | jq -r '.items[0].spec.template.spec.containers[] | select(.name=="secrets-store") | .args | map(select(test("rotation|sync|filtered"))) | join(" ")')] csidriver.tokenRequests=$(kubectl get csidriver secrets-store.csi.k8s.io -o jsonpath='{.spec.tokenRequests}') requiresRepublish=$(kubectl get csidriver secrets-store.csi.k8s.io -o jsonpath='{.spec.requiresRepublish}')"
}

install_openbao() {
  cat >"$WORK/openbao-values.yaml" <<'EOF'
injector:
  enabled: false
server:
  dataStorage:
    enabled: false
  standalone:
    enabled: true
    config: |
      ui = false
      listener "tcp" {
        address         = "[::]:8200"
        cluster_address = "[::]:8201"
        tls_disable     = 1
      }
      storage "inmem" {}
      audit "file" "to-stdout" {
        options {
          file_path = "stdout"
        }
      }
csi:
  enabled: true
  debug: true
EOF
  helm install openbao openbao/openbao --version "$BAO_CHART" -n openbao --create-namespace \
    -f "$WORK/openbao-values.yaml" --timeout 5m >"$OUT/openbao-install.log" 2>&1
  local i rc=1
  for i in $(seq 1 150); do
    if [ "$(kubectl -n openbao get pod openbao-0 -o jsonpath='{.status.phase}' 2>/dev/null)" = Running ]; then
      bx status -format=json >/dev/null 2>&1; rc=$?
      [ "$rc" = 2 ] && break
    fi
    sleep 2
  done
  if [ "$rc" != 2 ]; then
    rec "SETUP openbao did not come up (status rc=$rc)"
    kubectl -n openbao describe pod openbao-0 | tail -30
    exit 1
  fi
  local init
  init=$(bx operator init -key-shares=1 -key-threshold=1 -format=json)
  bx operator unseal "$(jq -r '.unseal_keys_b64[0]' <<<"$init")" >/dev/null
  ROOT=$(jq -r .root_token <<<"$init")
  kubectl -n openbao rollout status ds/openbao-csi-provider --timeout=180s >/dev/null 2>&1
  ( while :; do kubectl -n openbao port-forward svc/openbao 18200:8200 >>"$OUT/pf-openbao.log" 2>&1; sleep 1; done ) &
  for i in $(seq 1 30); do curl -s -o /dev/null http://127.0.0.1:18200/v1/sys/health && break; sleep 1; done
  rec "SETUP openbao chart=$BAO_CHART $(bx version | oneline 80) csi-provider=$(kubectl -n openbao get ds openbao-csi-provider -o jsonpath='{.spec.template.spec.containers[0].image}') ready=$(kubectl -n openbao get ds openbao-csi-provider -o jsonpath='{.status.numberReady}/{.status.desiredNumberScheduled}')"
}

pol() { # name; policy text on stdin
  bxi policy write "$1" - >/dev/null 2>"$WORK/pol.err" || rec "SETUP policy $1 FAILED: $(oneline 300 <"$WORK/pol.err")"
}
w() { # bao write that records a failure; args as for bao write
  bx write "$@" >/dev/null 2>"$WORK/w.err" || rec "SETUP write $1 FAILED: $(oneline 300 <"$WORK/w.err")"
}
wj() { # path; JSON body on stdin
  bxi write "$1" - >/dev/null 2>"$WORK/w.err" || rec "SETUP write $1 FAILED: $(oneline 300 <"$WORK/w.err")"
}

# Kubernetes auth, the identity templates, and the policies and roles of the
# design record (section 4.9 and design record 03, section 4.6).
setup_auth() {
  bx auth enable kubernetes >/dev/null
  w auth/kubernetes/config kubernetes_host=https://kubernetes.default.svc:443
  ACC=$(bx auth list -format=json | jq -r '."kubernetes/".accessor')
  TNS="{{identity.entity.aliases.$ACC.metadata.service_account_namespace}}"
  TSA="{{identity.entity.aliases.$ACC.metadata.service_account_name}}"
  pol workload <<EOF
path "mounted/data/$TNS/sa/$TSA/*" { capabilities = ["read"] }
path "mounted/data/$TNS/shared/*"  { capabilities = ["read"] }
path "db/creds/$TNS.$TSA.*"        { capabilities = ["read"] }
path "db/static-creds/$TNS.$TSA.*" { capabilities = ["read"] }
EOF
  pol access-session <<EOF
path "pki/issue/access-exec-self" { capabilities = ["update"] }
path "ssh/issue/acc.$TNS.*"       { capabilities = ["update"] }
EOF
  pol access-session-platform <<'EOF'
path "pki/issue/access-exec" { capabilities = ["update"] }
EOF
  pol access-binder <<'EOF'
path "transit/sign/access-recordings"   { capabilities = ["update"] }
path "transit/verify/access-recordings" { capabilities = ["update"] }
path "ssh/roles/acc.*"                  { capabilities = ["create", "update", "read", "delete"] }
EOF
  pol pki-sign-internal <<'EOF'
path "pki/sign/internal-server"  { capabilities = ["create", "update"] }
path "pki2/sign/internal-server" { capabilities = ["create", "update"] }
EOF
  w auth/kubernetes/role/access-session bound_service_account_names=access-session bound_service_account_namespaces='*' \
    audience=openbao token_policies=workload,access-session token_ttl=5m token_max_ttl=10m
  w auth/kubernetes/role/access-session-platform bound_service_account_names=access-session bound_service_account_namespaces=guacamole \
    audience=openbao token_policies=access-session-platform token_ttl=5m token_max_ttl=10m
  w auth/kubernetes/role/access-binder bound_service_account_names=access-controller bound_service_account_namespaces=guacamole \
    audience=openbao token_policies=access-binder token_ttl=15m token_max_ttl=1h
  w auth/kubernetes/role/cert-manager-pki bound_service_account_names=cert-manager bound_service_account_namespaces=cert-manager \
    token_policies=pki-sign-internal token_ttl=15m token_max_ttl=1h
  w auth/kubernetes/role/workload bound_service_account_names='*' bound_service_account_namespaces='*' \
    audience=openbao token_policies=workload token_ttl=15m token_max_ttl=1h
  rec "SETUP auth accessor=$ACC policies=$(bx policy list | oneline 200) roles=$(bx list -format=json auth/kubernetes/role | jq -r 'join(",")')"
}

# PKI as design record 03 (SEC-250): pki-root holds the root; pki holds an
# intermediate signed by it and set-signed with the intermediate alone. pki2 is
# the same, set-signed with the bundle intermediate + root (variant for (f)).
setup_pki() {
  local m csr icert
  bx secrets enable -path=pki-root -max-lease-ttl=87600h pki >/dev/null
  ROOT_PEM=$(bx write -field=certificate pki-root/root/generate/internal common_name="spike root" ttl=87600h key_type=ec key_bits=384)
  for m in pki pki2; do
    bx secrets enable -path=$m -max-lease-ttl=8760h pki >/dev/null
    csr=$(bx write -field=csr $m/intermediate/generate/internal common_name="spike $m intermediate" key_type=ec key_bits=256)
    icert=$(printf '%s\n' "$csr" | bxi write -field=certificate pki-root/root/sign-intermediate csr=- ttl=8760h format=pem)
    if [ $m = pki ]; then
      printf '%s\n' "$icert" | bxi write $m/intermediate/set-signed certificate=- >/dev/null 2>"$WORK/ss.err" || rec "SETUP $m set-signed FAILED $(oneline <"$WORK/ss.err")"
      INT_PEM=$icert
    else
      printf '%s\n%s\n' "$icert" "$ROOT_PEM" | bxi write $m/intermediate/set-signed certificate=- >/dev/null 2>"$WORK/ss.err" || rec "SETUP $m set-signed FAILED $(oneline <"$WORK/ss.err")"
      INT2_PEM=$icert
    fi
    w $m/roles/internal-server allowed_domains=svc.cluster.local,cluster.local allow_subdomains=true \
      server_flag=true client_flag=true max_ttl=720h key_type=ec key_bits=256
    w $m/roles/access-exec allow_any_name=true enforce_hostnames=false allowed_uri_sans='urn:openova:access:exec:*' \
      client_flag=true server_flag=false key_type=ec key_bits=256 ttl=5m max_ttl=10m no_store=true
  done
  w pki/roles/access-exec-self allow_any_name=true enforce_hostnames=false allowed_uri_sans_template=true \
    allowed_uri_sans="urn:openova:access:exec:$TNS:*" \
    client_flag=true server_flag=false key_type=ec key_bits=256 ttl=5m max_ttl=10m no_store=true
  printf '%s\n' "$ROOT_PEM" >"$WORK/root.pem"; printf '%s\n' "$INT_PEM" >"$WORK/int.pem"; printf '%s\n' "$INT2_PEM" >"$WORK/int2.pem"
  rec "SETUP pki root=$(openssl x509 -noout -subject -in "$WORK/root.pem" | oneline 80) intermediate=$(openssl x509 -noout -subject -issuer -in "$WORK/int.pem" | oneline 160) role-access-exec-self=$(bx read -format=json pki/roles/access-exec-self | jq -c '.data | {allowed_uri_sans, allowed_uri_sans_template, allow_any_name, enforce_hostnames, client_flag, server_flag, key_type, key_bits, ttl, max_ttl, no_store}')"
}

# SSH engine as design record 03 4.10 (INF-410) with one per-target role of
# design record 13 4.9: ssh/roles/acc.<namespace>.<target>.
setup_ssh() {
  bx secrets enable ssh >/dev/null
  w ssh/config/ca generate_signing_key=true
  printf '%s' '{"key_type":"ca","allow_user_certificates":true,"allowed_users":"alice","default_user":"alice","allowed_users_template":false,"ttl":"5m","max_ttl":"5m","allow_user_key_ids":true,"default_extensions":{"permit-pty":""},"allowed_extensions":"permit-pty","allowed_critical_options":""}' \
    | wj ssh/roles/acc.spike.target1
  bget ssh/public_key >"$WORK/ssh-ca.pub"
  rec "SETUP ssh ca=$(cut -d' ' -f1 "$WORK/ssh-ca.pub") role=$(bx read -format=json ssh/roles/acc.spike.target1 | jq -c '.data | {key_type, allowed_users, default_user, ttl, max_ttl, allow_user_key_ids, default_extensions, allowed_extensions, allowed_user_key_lengths, algorithm_signer}')"
}

# PostgreSQL (throwaway). The superuser password is a literal in the pod spec of
# this throwaway Deployment; no Secret object is created.
install_pg() {
  PGPW=$(openssl rand -hex 16); PGADM=$(openssl rand -hex 16)
  kubectl create ns spike-db >/dev/null
  kubectl apply -f - >/dev/null <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: {name: pg, namespace: spike-db}
spec:
  replicas: 1
  selector: {matchLabels: {app: pg}}
  template:
    metadata: {labels: {app: pg}}
    spec:
      containers:
        - name: pg
          image: $PG_IMAGE
          env:
            - {name: POSTGRES_PASSWORD, value: "$PGPW"}
          ports: [{containerPort: 5432}]
          readinessProbe:
            exec: {command: ["pg_isready", "-h", "127.0.0.1", "-U", "postgres"]}
            periodSeconds: 2
---
apiVersion: v1
kind: Service
metadata: {name: pg, namespace: spike-db}
spec:
  selector: {app: pg}
  ports: [{port: 5432, targetPort: 5432}]
EOF
}
psql_admin() { kubectl -n spike-db exec "$PGPOD" -- psql -U postgres -v ON_ERROR_STOP=1 -Atq "$@" </dev/null; }
pg_try() { # user password -> ok | fail(...)
  local o rc
  o=$(kubectl -n spike-db exec "$PGPOD" -- env PGPASSWORD="$2" psql -h 127.0.0.1 -U "$1" -d billing -Atc 'select current_user' 2>&1 </dev/null); rc=$?
  if [ $rc -eq 0 ]; then echo "ok"; else echo "fail($(printf '%s' "$o" | oneline 140))"; fi
}
lease_count() { local o; o=$(bx list -format=json "sys/leases/lookup/db/creds/$DBROLE/" 2>/dev/null); if [ -n "$o" ]; then jq -r 'length' <<<"$o" 2>/dev/null; else echo 0; fi; }
pg_users() { psql_admin -d postgres -c "select rolname from pg_roles where rolname like 'v-%' order by 1" 2>/dev/null | paste -sd, -; }

setup_db() {
  kubectl -n spike-db rollout status deploy/pg --timeout=240s >/dev/null 2>&1
  PGPOD=$(kubectl -n spike-db get pod -l app=pg -o jsonpath='{.items[0].metadata.name}')
  local i
  for i in $(seq 1 60); do psql_admin -c 'select 1' >/dev/null 2>&1 && break; sleep 2; done
  psql_admin -c "CREATE DATABASE billing" \
    -c "CREATE ROLE billing_ro NOLOGIN" -c "CREATE ROLE billing_rw NOLOGIN" \
    -c "CREATE ROLE openbao_dbadmin LOGIN CREATEROLE PASSWORD '$PGADM'" \
    -c "GRANT billing_ro, billing_rw TO openbao_dbadmin WITH ADMIN OPTION" \
    -c "CREATE ROLE openbao_dbadmin2 LOGIN CREATEROLE PASSWORD '$PGADM'" \
    -c "GRANT billing_ro, billing_rw TO openbao_dbadmin2 WITH ADMIN OPTION" \
    -c "ALTER ROLE openbao_dbadmin2 SET createrole_self_grant = 'set, inherit'" \
    -c "GRANT CONNECT ON DATABASE billing TO billing_ro, billing_rw" >/dev/null 2>"$WORK/pg.err" \
    || rec "SETUP pg init FAILED $(oneline <"$WORK/pg.err")"
  psql_admin -d billing -c "CREATE TABLE invoices(id int)" -c "ALTER TABLE invoices OWNER TO billing_rw" \
    -c "GRANT SELECT ON invoices TO billing_ro" >/dev/null 2>"$WORK/pg.err" || rec "SETUP pg table FAILED $(oneline <"$WORK/pg.err")"
  bx secrets enable -path=db database >/dev/null
  local c
  for c in pg:openbao_dbadmin pg2:openbao_dbadmin2; do
    w "db/config/spike.${c%%:*}" plugin_name=postgresql-database-plugin \
      connection_url='postgresql://{{username}}:{{password}}@pg.spike-db.svc.cluster.local:5432/postgres?sslmode=disable' \
      username="${c#*:}" password="$PGADM" allowed_roles='spike.*' password_authentication=scram-sha-256
  done
  # Statements exactly as design record 03, section 4.10 (mode ro, database billing).
  local stm
  stm='{"default_ttl":"1h","max_ttl":"24h","creation_statements":["CREATE ROLE \"{{name}}\" WITH LOGIN PASSWORD '"'"'{{password}}'"'"' VALID UNTIL '"'"'{{expiration}}'"'"' IN ROLE \"billing_ro\";"],"revocation_statements":["REASSIGN OWNED BY \"{{name}}\" TO \"billing_rw\"; DROP OWNED BY \"{{name}}\"; DROP ROLE IF EXISTS \"{{name}}\";"]'
  printf '%s,"db_name":"spike.pg"}' "$stm" | wj "db/roles/$DBROLE"
  printf '%s,"db_name":"spike.pg2"}' "$stm" | wj "db/roles/${DBROLE}2"
  rec "SETUP db postgres=$(psql_admin -c 'show server_version' 2>/dev/null) role=$(bx read -format=json db/roles/$DBROLE | jq -c '.data | {db_name, default_ttl, max_ttl}')"
}

# sshd that trusts the OpenBao SSH authority (the target of kind ssh).
install_sshd() {
  kubectl create ns spike-ssh >/dev/null
  kubectl -n spike-ssh create configmap ssh-ca --from-file=ca.pub="$WORK/ssh-ca.pub" >/dev/null
  kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata: {name: sshd, namespace: spike-ssh}
spec:
  containers:
    - name: sshd
      image: $SSHD_IMAGE
      command:
        - sh
        - -c
        - |
          apk add --no-cache openssh-server >/dev/null 2>&1 || exit 1
          ssh-keygen -A >/dev/null
          adduser -D -s /bin/sh alice; adduser -D -s /bin/sh bob
          sed -i -e 's/^alice:!/alice:*/' -e 's/^bob:!/bob:*/' /etc/shadow
          exec /usr/sbin/sshd -D -e -p 2222 -o TrustedUserCAKeys=/ca/ca.pub -o PasswordAuthentication=no \
            -o KbdInteractiveAuthentication=no -o AuthorizedKeysFile=none -o LogLevel=VERBOSE
      readinessProbe: {tcpSocket: {port: 2222}, periodSeconds: 2}
      volumeMounts: [{name: ca, mountPath: /ca}]
  volumes: [{name: ca, configMap: {name: ssh-ca}}]
EOF
}
sshd_ready() {
  kubectl -n spike-ssh wait --for=condition=Ready pod/sshd --timeout=180s >/dev/null 2>&1 || rec "SETUP sshd not ready"
  ( while :; do kubectl -n spike-ssh port-forward pod/sshd 12222:2222 >>"$OUT/pf-sshd.log" 2>&1; sleep 1; done ) &
  sleep 3
}
ssh_try() { # keyfile certfile user -> ok(...) | fail(...)
  local o rc
  o=$(ssh -F /dev/null -o BatchMode=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o IdentitiesOnly=yes \
      -o ConnectTimeout=10 -o LogLevel=ERROR -i "$1" -o CertificateFile="$2" -p 12222 "$3@127.0.0.1" 'echo ssh-ok-as-$(id -un)' 2>&1); rc=$?
  if [ $rc -eq 0 ]; then echo "ok($(printf '%s' "$o" | oneline 60))"; else echo "fail(rc=$rc $(printf '%s' "$o" | oneline 160))"; fi
}

# SecretProviderClass with the given role and objects (YAML list text).
spc() { # ns name role objects
  { cat <<EOF
apiVersion: secrets-store.csi.x-k8s.io/v1
kind: SecretProviderClass
metadata: {name: $2, namespace: $1}
spec:
  provider: openbao
  parameters:
    openbaoAddress: $BAO_SVC
    roleName: $3
    audience: openbao
    objects: |
EOF
    printf '%s\n' "$4" | sed 's/^/      /'
  } | kubectl apply -f - >/dev/null
}
# Session-style pod: no service-account token, the mount read-only.
session_pod() { # ns name spc [serviceaccount]
  kubectl apply -f - >/dev/null <<EOF
apiVersion: v1
kind: Pod
metadata: {name: $2, namespace: $1, labels: {spike: acc-030}}
spec:
  serviceAccountName: ${4:-access-session}
  automountServiceAccountToken: false
  terminationGracePeriodSeconds: 1
  containers:
    - name: tool
      image: $PROBE_IMAGE
      command: ["sh", "-c", "sleep 7200"]
      volumeMounts:
        - {name: openova, mountPath: /var/run/secrets/openova, readOnly: true}
  volumes:
    - name: openova
      csi:
        driver: secrets-store.csi.k8s.io
        readOnly: true
        volumeAttributes: {secretProviderClass: $3}
EOF
}
pod_result() { # ns name [timeout] -> PASS | FAIL(<last mount event>)
  local ev
  if kubectl -n "$1" wait --for=condition=Ready "pod/$2" --timeout="${3:-120}s" >/dev/null 2>&1; then echo PASS; return 0; fi
  ev=$(kubectl -n "$1" get events --field-selector "involvedObject.name=$2" -o jsonpath='{range .items[*]}{.reason}: {.message}{"\n"}{end}' 2>/dev/null | grep -E 'FailedMount|Failed|Error' | tail -1 | oneline 700)
  echo "FAIL(${ev:-not ready, no event})"; return 1
}
pf() { kubectl -n "$1" exec "$2" -- cat "/var/run/secrets/openova/$3" 2>/dev/null; }

ns_with_sa() { # ns [sa...]
  local ns=$1 sa; shift
  kubectl get ns "$ns" >/dev/null 2>&1 || kubectl create ns "$ns" >/dev/null
  for sa in "$@"; do kubectl -n "$ns" create sa "$sa" --dry-run=client -o yaml | kubectl apply -f - >/dev/null; done
}

# Object lists used by several steps (design record 13, section 4.8).
objs_ssh() { cat <<EOF
- objectName: private-key
  secretPath: ssh/issue/acc.spike.target1
  method: PUT
  secretArgs: {valid_principals: alice, key_type: ed25519, key_id: "$SUID", ttl: 5m}
  secretKey: private_key
- objectName: public-key
  secretPath: ssh/issue/acc.spike.target1
  method: PUT
  secretArgs: {valid_principals: alice, key_type: ed25519, key_id: "$SUID", ttl: 5m}
  secretKey: signed_key
EOF
}
objs_pki() { # role-path uri
  cat <<EOF
- objectName: exec-cert
  secretPath: $1
  method: PUT
  secretArgs: {common_name: "session-$SUID", uri_sans: "$2", ttl: 5m}
  secretKey: certificate
- objectName: exec-key
  secretPath: $1
  method: PUT
  secretArgs: {common_name: "session-$SUID", uri_sans: "$2", ttl: 5m}
  secretKey: private_key
- objectName: exec-ca
  secretPath: $1
  method: PUT
  secretArgs: {common_name: "session-$SUID", uri_sans: "$2", ttl: 5m}
  secretKey: issuing_ca
EOF
}
objs_db() { cat <<EOF
- objectName: db-username
  secretPath: db/creds/$DBROLE
  secretKey: username
- objectName: db-password
  secretPath: db/creds/$DBROLE
  secretKey: password
EOF
}
