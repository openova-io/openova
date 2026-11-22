#!/usr/bin/env bash
# eso-openbao-conformance.sh - the behaviours of External Secrets that the platform uses, against
# a real OpenBao (SEC-261). An upgrade of External Secrets (SEC-262, SEC-263) keeps this green.
#
# It runs on a throwaway kind cluster (workflow test-eso-openbao-conformance): OpenBao is the image
# tag platform/openbao/chart/values.yaml pins, as a dev server (one pod, a ClusterIP Service, no
# NodePort; the test reaches it with kubectl exec). External Secrets is the upstream chart that
# platform/external-secrets/chart/Chart.yaml pins, installed from the archive that
# `helm dependency build platform/external-secrets/chart` vendors. Each behaviour has a negative
# control, so that its assertion is seen to be able to fail:
#
#   E1  ExternalSecret read: a ClusterSecretStore with Kubernetes auth (the shape of vault-region1)
#       syncs two properties and follows a change. Control: a key the role is denied gives
#       Ready=False and no Secret.
#   E2  PushSecret with a Password generator and updatePolicy IfNotExists (the shape of the shared
#       seeds): an absent key is written once and keeps its value over later reconciles; a key
#       written by hand is left as it was. Control: the same PushSecret with Replace changes.
#   E3  PushSecret to a JWT-authenticated store (the shape of the peer stores): the token is an
#       OpenBao identity token minted by a VaultDynamicSecret generator; the push lands. Control:
#       a key outside the grant of the JWT role is refused.
#   E4  deletionPolicy: Delete: deleting the PushSecret deletes its key in OpenBao. Control: with
#       deletionPolicy None the key stays.
#   E5  Namespaced SecretStore with serviceAccountRef: it logs in as its own ServiceAccount.
#       Controls: a key that only the controller's role may read is refused; an ExternalSecret of
#       another namespace cannot use the store; a store that names a ServiceAccount of another
#       namespace is refused.
#
# One OpenBao plays both regions of E3: its identity issuer is its own loopback address (the peer
# stores use the peer's mesh Service). The policies are the texts of
# platform/openbao/chart/templates/_config.tpl with kv mount secret and apps prefix openova/apps.
#
# Usage:
#   bash tests/e2e/eso-openbao-conformance.sh              run against kube context kind-eso-conformance
#   bash tests/e2e/eso-openbao-conformance.sh --list       print the five behaviours
#   bash tests/e2e/eso-openbao-conformance.sh --self-test  offline: every helper can fail
# The run refuses any context other than ESO_CONFORMANCE_CONTEXT (default kind-eso-conformance).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
CTX="${ESO_CONFORMANCE_CONTEXT:-kind-eso-conformance}"
ESO_API="external-secrets.io/v1beta1"
GEN_API="generators.external-secrets.io/v1alpha1"
PUSH_API="external-secrets.io/v1alpha1"
BAO_NS=openbao
ESO_NS=external-secrets-system
APP_NS=conf-app
SCOPED_NS=conf-ns
BAO_PORT="${BAO_PORT:-8200}"
BAO_SVC="http://openbao.$BAO_NS.svc.cluster.local:8200"
AUD=openbao-peer-push

BEHAVIOURS="E1 ExternalSecret read
E2 PushSecret with generator and IfNotExists
E3 PushSecret to a JWT-authenticated store
E4 deletionPolicy: Delete
E5 namespaced store with serviceAccountRef"

pass() { echo "PASS $*"; }
fail() { echo "FAIL $*" >&2; exit 1; }

# check_eq <label> <got> <want>: 0 when equal, else 1 with both values on stderr.
check_eq() {
  [ "$2" = "$3" ] && return 0
  echo "  $1: got '$2', want '$3'" >&2
  return 1
}

# context_ok <context>: the run goes only to the throwaway cluster.
context_ok() { [ -n "$1" ] && [ "$1" = "$CTX" ] && [ "${1#kind-}" != "$1" ]; }

# read_eso_version <Chart.yaml>: the version of the dependency external-secrets.
read_eso_version() {
  awk '/^  - name: external-secrets$/{f=1;next} f && /^ *version:/{gsub(/"/,"",$2); print $2; exit}' "$1"
}

# read_bao_tag <values.yaml>: the tag that follows the openbao/openbao image repository.
read_bao_tag() {
  awk '/^ *repository: .*openbao\/openbao"?$/{f=1;next} f && /^ *tag:/{gsub(/"/,"",$2); print $2; exit}' "$1"
}

# jwt_payload <token>: the decoded claims of a JWT.
jwt_payload() {
  local p="${1#*.}"
  p="${p%%.*}"; p="${p//_//}"; p="${p//-/+}"
  case $(( ${#p} % 4 )) in 2) p="$p==" ;; 3) p="$p=" ;; esac
  base64 -d <<<"$p" 2>/dev/null
}

# until_ok <seconds> <command...>: retry every 2 s until it succeeds or the time is up.
until_ok() {
  local end=$((SECONDS + $1)); shift
  until "$@"; do [ "$SECONDS" -lt "$end" ] || return 1; sleep 2; done
}

self_test() {
  local t n=0 bad=0 tok
  t="$(mktemp -d)"
  # expect <0 or 1> <label> <command...>: the command must exit 0 (or fail, for 1).
  expect() {
    local want=$1 label=$2 got=0; shift 2
    "$@" >/dev/null 2>&1 || got=1
    n=$((n + 1))
    if [ "$got" = "$want" ]; then echo "ok   $label"; else bad=$((bad + 1)); echo "FAIL $label"; fi
  }
  is() { [ "$($1 "$2")" = "$3" ]; }
  pinned() { [[ "$($1 "$2")" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; }
  claim() { grep -q '"openova_region":"b"' <<<"$(jwt_payload "$1")"; }
  ctx_prod() { local CTX=prod; context_ok prod; }
  printf 'dependencies:\n  - name: kube-access-lib\n    version: "0.1.0"\n  - name: external-secrets\n    version: "9.8.7"\n' > "$t/Chart.yaml"
  printf 'dependencies:\n  - name: external-secrets-x\n    version: "1.0.0"\n' > "$t/Other.yaml"
  printf 'server:\n  image:\n    repository: proxy-quay/openbao/openbao\n    tag: "1.2.3"\n  x:\n    tag: "9"\n' > "$t/values.yaml"
  printf 'image:\n  repository: nginx\n  tag: "1"\n' > "$t/v2.yaml"
  tok="x.$(printf '{"aud":"a","openova_region":"b"}' | base64 -w0 | tr '/+' '_-' | tr -d '=').y"
  expect 0 "check_eq passes on equal values" check_eq x a a
  expect 1 "check_eq fails on unequal values" check_eq x a b
  expect 0 "the kind context is accepted" context_ok kind-eso-conformance
  expect 1 "another context is refused" context_ok default
  expect 1 "an empty context is refused" context_ok ""
  expect 1 "a context must be a kind cluster, even when configured" ctx_prod
  expect 0 "the External Secrets version is read from its dependency" is read_eso_version "$t/Chart.yaml" 9.8.7
  expect 0 "no version without the dependency" is read_eso_version "$t/Other.yaml" ""
  expect 0 "the OpenBao tag follows its repository" is read_bao_tag "$t/values.yaml" 1.2.3
  expect 0 "no tag without the OpenBao repository" is read_bao_tag "$t/v2.yaml" ""
  expect 0 "the repository pins an External Secrets version" pinned read_eso_version "$ROOT/platform/external-secrets/chart/Chart.yaml"
  expect 0 "the repository pins an OpenBao tag" pinned read_bao_tag "$ROOT/platform/openbao/chart/values.yaml"
  expect 1 "a version that is not one is refused" pinned read_bao_tag "$t/v2.yaml"
  expect 0 "a JWT payload is decoded" claim "$tok"
  expect 1 "a missing claim is not found" claim x.e30.y
  expect 1 "until_ok gives up when the time is up" until_ok 0 false
  expect 0 "until_ok passes a command that succeeds" until_ok 4 true
  expect 0 "five behaviours are listed" test "$(grep -c '^E[1-5] ' <<<"$BEHAVIOURS")" = 5
  rm -rf "$t"
  echo "self-test: $((n - bad)) of $n passed"
  [ "$bad" = 0 ]
}

k() { kubectl --context "$CTX" "$@"; }
ob() { k -n "$BAO_NS" exec deploy/openbao -- env BAO_ADDR="http://127.0.0.1:$BAO_PORT" BAO_TOKEN=root bao "$@" </dev/null; }
ob_in() { k -n "$BAO_NS" exec -i deploy/openbao -- env BAO_ADDR="http://127.0.0.1:$BAO_PORT" BAO_TOKEN=root bao "$@"; }
apply() { k apply --server-side -f -; }

# remote <key> <property>: the value in OpenBao, empty when there is none.
remote() { ob kv get -field="$2" "secret/$1" 2>/dev/null || true; }
# remote_absent <key>: OpenBao answers that the key has no metadata (an error is not absence).
remote_absent() { grep -q "No value found" <<<"$(ob kv metadata get "secret/$1" 2>&1 || true)"; }
managed() { grep -q '"managed-by": *"external-secrets"' <<<"$(ob kv metadata get -format=json "secret/$1" 2>/dev/null || true)"; }
secret_val() { base64 -d <<<"$(k -n "$1" get secret "$2" -o "jsonpath={.data.$3}" 2>/dev/null || true)"; }
no_secret() { ! k -n "$1" get secret "$2" >/dev/null 2>&1; }
ready() { k ${2:+-n "$2"} wait --for=condition=Ready "$1" --timeout="${3:-120s}" >/dev/null; }
not_ready() { k ${2:+-n "$2"} wait --for=condition=Ready=False "$1" --timeout="${3:-90s}" >/dev/null; }
eq_secret() { [ "$(secret_val "$1" "$2" "$3")" = "$4" ]; }

diagnose() {
  echo "=== diagnosis" >&2
  k get clustersecretstores,secretstores,externalsecrets,pushsecrets -A -o wide >&2 || true
  k describe externalsecrets,pushsecrets -A 2>/dev/null | grep -A3 -E "^Name:|Message:|Reason:" >&2 || true
  k -n "$ESO_NS" logs deploy/external-secrets --tail=150 >&2 || true
  k -n "$BAO_NS" logs deploy/openbao --tail=60 >&2 || true
}
on_exit() { [ "$1" = 0 ] || diagnose; exit "$1"; }

deploy_openbao() {
  k create namespace "$BAO_NS" --dry-run=client -o yaml | apply >/dev/null
  apply >/dev/null <<EOF
apiVersion: v1
kind: ServiceAccount
metadata: {name: openbao, namespace: $BAO_NS}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata: {name: conformance-openbao-auth-delegator}
roleRef: {apiGroup: rbac.authorization.k8s.io, kind: ClusterRole, name: "system:auth-delegator"}
subjects: [{kind: ServiceAccount, name: openbao, namespace: $BAO_NS}]
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: openbao, namespace: $BAO_NS}
spec:
  replicas: 1
  selector: {matchLabels: {app: openbao}}
  template:
    metadata: {labels: {app: openbao}}
    spec:
      serviceAccountName: openbao
      containers:
        - name: openbao
          image: quay.io/openbao/openbao:$BAO_TAG
          command: [bao, server, -dev, -dev-root-token-id=root, "-dev-listen-address=0.0.0.0:$BAO_PORT", -dev-no-store-token]
          ports: [{name: http, containerPort: $BAO_PORT}]
          readinessProbe: {httpGet: {path: /v1/sys/health, port: $BAO_PORT}, periodSeconds: 2}
---
apiVersion: v1
kind: Service
metadata: {name: openbao, namespace: $BAO_NS}
spec:
  type: ClusterIP
  selector: {app: openbao}
  ports: [{name: http, port: 8200, targetPort: $BAO_PORT}]
EOF
}

# configure_openbao: auth mounts, policies, roles, the identity issuer and the seeded keys.
configure_openbao() {
  local kv=secret apps=openova/apps
  ob auth enable kubernetes >/dev/null
  ob write auth/kubernetes/config kubernetes_host=https://kubernetes.default.svc:443 >/dev/null
  ob_in policy write external-secrets-read - >/dev/null <<EOF
path "$kv/data/sso/*" { capabilities = ["read"] }
path "$kv/metadata/sso/*" { capabilities = ["read", "list"] }
path "$kv/data/catalyst/*" { capabilities = ["read"] }
path "$kv/metadata/catalyst/*" { capabilities = ["read", "list"] }
path "$kv/data/$apps/*" { capabilities = ["read"] }
path "$kv/metadata/$apps/*" { capabilities = ["read", "list"] }
path "$kv/data/catalyst/tofu-phase0-archive" { capabilities = ["deny"] }
path "$kv/metadata/catalyst/tofu-phase0-archive" { capabilities = ["deny"] }
EOF
  ob_in policy write external-secrets-push - >/dev/null <<EOF
path "$kv/data/catalyst/newapi/admin-token" { capabilities = ["create", "update", "read"] }
path "$kv/metadata/catalyst/newapi/admin-token" { capabilities = ["create", "update", "read"] }
path "$kv/data/$apps/*" { capabilities = ["create", "update", "read"] }
path "$kv/metadata/$apps/*" { capabilities = ["create", "update", "read"] }
EOF
  ob_in policy write openbao-peer-token - >/dev/null <<EOF
path "identity/oidc/token/$AUD" { capabilities = ["read"] }
EOF
  ob_in policy write "$AUD" - >/dev/null <<EOF
path "$kv/data/$apps/*" { capabilities = ["create", "update", "read"] }
path "$kv/metadata/$apps/*" { capabilities = ["create", "update", "read"] }
EOF
  ob_in policy write conformance-delete - >/dev/null <<EOF
path "$kv/data/$apps/conf/delete/*" { capabilities = ["create", "update", "read", "delete"] }
path "$kv/metadata/$apps/conf/delete/*" { capabilities = ["create", "update", "read", "delete"] }
EOF
  ob_in policy write conf-ns - >/dev/null <<EOF
path "$kv/data/$SCOPED_NS/*" { capabilities = ["read"] }
path "$kv/metadata/$SCOPED_NS/*" { capabilities = ["read", "list"] }
EOF
  local r
  for r in "external-secrets:external-secrets-read,external-secrets-push" "openbao-peer-token:openbao-peer-token" \
           "conformance-delete:conformance-delete"; do
    ob write "auth/kubernetes/role/${r%%:*}" bound_service_account_names=external-secrets \
      bound_service_account_namespaces="$ESO_NS" policies="${r#*:}" ttl=15m max_ttl=1h >/dev/null
  done
  ob write auth/kubernetes/role/conf-ns bound_service_account_names=conf-reader \
    bound_service_account_namespaces="$SCOPED_NS" policies=conf-ns ttl=15m max_ttl=1h >/dev/null
  ob write identity/oidc/config issuer="http://127.0.0.1:$BAO_PORT" >/dev/null
  ob write "identity/oidc/key/$AUD" algorithm=RS256 allowed_client_ids="$AUD" rotation_period=24h verification_ttl=24h >/dev/null
  ob write "identity/oidc/role/$AUD" key="$AUD" client_id="$AUD" ttl=1h template='{"openova_region":"b"}' >/dev/null
  ob auth enable -path=jwt-peer-b jwt >/dev/null
  ob write auth/jwt-peer-b/config oidc_discovery_url="http://127.0.0.1:$BAO_PORT/v1/identity/oidc" >/dev/null
  ob_in write "auth/jwt-peer-b/role/$AUD" - >/dev/null <<EOF
{"role_type": "jwt", "bound_audiences": ["$AUD"], "user_claim": "sub", "bound_claims": {"openova_region": "b"},
 "token_policies": ["$AUD"], "token_ttl": "15m", "token_max_ttl": "1h"}
EOF
  ob kv put "$kv/$apps/conf/read" username=alice password="$P_READ" >/dev/null
  ob kv put "$kv/$apps/conf/seed-kept" username=hand password="$P_HAND" >/dev/null
  ob kv put "$kv/catalyst/tofu-phase0-archive" x=denied >/dev/null
  ob kv put "$kv/$SCOPED_NS/item" value="$P_SCOPED" >/dev/null
}

install_eso() {
  local archive="$ROOT/platform/external-secrets/chart/charts/external-secrets-$ESO_VERSION.tgz" img=ghcr.io/external-secrets/external-secrets
  [ -f "$archive" ] || fail "no $archive: run helm dependency build platform/external-secrets/chart first"
  helm --kube-context "$CTX" install external-secrets "$archive" --namespace "$ESO_NS" --create-namespace \
    --set installCRDs=true --set image.repository="$img" --set webhook.image.repository="$img" \
    --set certController.image.repository="$img" --wait --timeout 8m >/dev/null
  echo "External Secrets $(k -n "$ESO_NS" get deploy external-secrets -o jsonpath='{.spec.template.spec.containers[0].image}')"
}

# store <kind> <name> <namespace or empty> <auth>: a store of the kv mount secret (version 2).
store() {
  printf 'apiVersion: %s\nkind: %s\nmetadata: {name: %s%s}\nspec:\n  provider:\n    vault:\n      server: %s\n' \
    "$ESO_API" "$1" "$2" "${3:+, namespace: $3}" "$BAO_SVC"
  printf '      path: secret\n      version: v2\n      auth: %s\n---\n' "$4"
}

# k8s_auth <role> <ServiceAccount> [<namespace>]: Kubernetes auth with serviceAccountRef.
k8s_auth() { printf '{kubernetes: {mountPath: kubernetes, role: %s, serviceAccountRef: {name: %s%s}}}' "$1" "$2" "${3:+, namespace: $3}"; }

stores() {
  store ClusterSecretStore vault-region1 "" "$(k8s_auth external-secrets external-secrets "$ESO_NS")"
  store ClusterSecretStore conformance-delete "" "$(k8s_auth conformance-delete external-secrets "$ESO_NS")"
  store SecretStore conf-ns-store "$SCOPED_NS" "$(k8s_auth conf-ns conf-reader)"
  cat <<EOF
apiVersion: $GEN_API
kind: VaultDynamicSecret
metadata: {name: $AUD-token, namespace: $ESO_NS}
spec:
  method: GET
  path: identity/oidc/token/$AUD
  resultType: Data
  provider:
    server: $BAO_SVC
    auth: $(k8s_auth openbao-peer-token external-secrets)
---
apiVersion: $ESO_API
kind: ExternalSecret
metadata: {name: $AUD-token, namespace: $ESO_NS}
spec:
  refreshInterval: 15m
  target: {name: $AUD-token, creationPolicy: Owner}
  dataFrom: [{sourceRef: {generatorRef: {apiVersion: $GEN_API, kind: VaultDynamicSecret, name: $AUD-token}}}]
EOF
}

# apply_stores: the webhook of a fresh install may not answer yet, so the caller retries this.
apply_stores() { stores | apply >/dev/null; }

# es <ns> <name> <store kind> <store> <key> <property...>: an ExternalSecret of one key.
es() {
  local ns=$1 name=$2 kind=$3 store=$4 key=$5 p; shift 5
  printf 'apiVersion: %s\nkind: ExternalSecret\nmetadata: {name: %s, namespace: %s}\nspec:\n  refreshInterval: 5s\n' "$ESO_API" "$name" "$ns"
  printf '  secretStoreRef: {kind: %s, name: %s}\n  target: {name: %s, creationPolicy: Owner}\n  data:\n' "$kind" "$store" "$name"
  for p in "$@"; do printf '    - {secretKey: %s, remoteRef: {key: %s, property: %s}}\n' "$p" "$key" "$p"; done
}

# seed <name> <update policy> <key>: a PushSecret of the Password generator, the shared-seed shape.
seed() {
  cat <<EOF
apiVersion: $PUSH_API
kind: PushSecret
metadata: {name: $1, namespace: $APP_NS}
spec:
  refreshInterval: 5s
  updatePolicy: $2
  deletionPolicy: None
  secretStoreRefs: [{kind: ClusterSecretStore, name: vault-region1}]
  selector: {generatorRef: {apiVersion: $GEN_API, kind: Password, name: conf-seed}}
  template: {data: {username: conf-owner, password: "{{ .password }}"}}
  data:
    - {match: {secretKey: username, remoteRef: {remoteKey: $3, property: username}}}
    - {match: {secretKey: password, remoteRef: {remoteKey: $3, property: password}}}
---
EOF
}

# push <name> <store> <source Secret> <key> <deletion policy>: a PushSecret of one Secret key.
push() {
  printf 'apiVersion: %s\nkind: PushSecret\nmetadata: {name: %s, namespace: %s}\nspec:\n  refreshInterval: 5s\n' "$PUSH_API" "$1" "$APP_NS"
  printf '  deletionPolicy: %s\n  secretStoreRefs: [{kind: ClusterSecretStore, name: %s}]\n  selector: {secret: {name: %s}}\n' "$5" "$2" "$3"
  printf '  data: [{match: {secretKey: value, remoteRef: {remoteKey: %s, property: value}}}]\n---\n' "$4"
}

# changes <key> <property> <count> <seconds>: the value takes <count> new values in time.
changes() {
  local last cur n=0 end=$((SECONDS + $4))
  last="$(remote "$1" "$2")"
  while [ "$n" -lt "$3" ]; do
    [ "$SECONDS" -lt "$end" ] || return 1
    sleep 2
    cur="$(remote "$1" "$2")"
    if [ -n "$cur" ] && [ "$cur" != "$last" ]; then n=$((n + 1)); last="$cur"; fi
  done
}

run() {
  context_ok "$(kubectl config current-context 2>/dev/null || true)" || fail "the current kube context is not $CTX (a kind cluster): refused"
  ESO_VERSION="$(read_eso_version "$ROOT/platform/external-secrets/chart/Chart.yaml")"
  BAO_TAG="$(read_bao_tag "$ROOT/platform/openbao/chart/values.yaml")"
  [ -n "$ESO_VERSION" ] && [ -n "$BAO_TAG" ] || fail "cannot read the pinned versions"
  echo "External Secrets chart $ESO_VERSION, OpenBao $BAO_TAG, context $CTX"
  P_READ="$(openssl rand -hex 12)"; P_HAND="$(openssl rand -hex 12)"; P_SCOPED="$(openssl rand -hex 12)"
  trap 'on_exit $?' EXIT

  deploy_openbao
  install_eso
  k -n "$BAO_NS" rollout status deploy/openbao --timeout=180s >/dev/null
  configure_openbao
  k create namespace "$APP_NS" --dry-run=client -o yaml | apply >/dev/null
  k create namespace "$SCOPED_NS" --dry-run=client -o yaml | apply >/dev/null
  k -n "$SCOPED_NS" create serviceaccount conf-reader >/dev/null
  until_ok 120 apply_stores || fail "the stores were not accepted"
  ready clustersecretstores.external-secrets.io/vault-region1 || fail "store vault-region1 not Ready"
  ready clustersecretstores.external-secrets.io/conformance-delete || fail "store conformance-delete not Ready"
  ready secretstores.external-secrets.io/conf-ns-store "$SCOPED_NS" || fail "store conf-ns-store not Ready"

  # E1
  { es "$APP_NS" conf-read ClusterSecretStore vault-region1 openova/apps/conf/read username password
    echo ---; es "$APP_NS" conf-read-denied ClusterSecretStore vault-region1 catalyst/tofu-phase0-archive x; } | apply >/dev/null
  ready externalsecrets.external-secrets.io/conf-read "$APP_NS" || fail "E1 ExternalSecret conf-read not Ready"
  check_eq username "$(secret_val "$APP_NS" conf-read username)" alice && check_eq password "$(secret_val "$APP_NS" conf-read password)" "$P_READ" \
    || fail "E1 the Secret does not hold the OpenBao values"
  local p2; p2="$(openssl rand -hex 12)"
  ob kv put secret/openova/apps/conf/read username=alice password="$p2" >/dev/null
  until_ok 90 eq_secret "$APP_NS" conf-read password "$p2" || fail "E1 the Secret did not follow the change in OpenBao"
  not_ready externalsecrets.external-secrets.io/conf-read-denied "$APP_NS" && no_secret "$APP_NS" conf-read-denied \
    || fail "E1 control: a denied key was synced"
  pass "E1 ExternalSecret read: two properties synced and a change followed; a denied key gives Ready=False and no Secret"

  # E2
  apply >/dev/null <<EOF
apiVersion: $GEN_API
kind: Password
metadata: {name: conf-seed, namespace: $APP_NS}
spec: {length: 32, digits: 6, symbols: 0, noUpper: false, allowRepeat: true}
---
$(seed conf-seed-new IfNotExists openova/apps/conf/seed-new)
$(seed conf-seed-kept IfNotExists openova/apps/conf/seed-kept)
$(seed conf-seed-replace Replace openova/apps/conf/seed-replace)
EOF
  local ps
  for ps in conf-seed-new conf-seed-kept conf-seed-replace; do
    ready "pushsecrets.external-secrets.io/$ps" "$APP_NS" || fail "E2 PushSecret $ps not Ready"
  done
  local v1; v1="$(remote openova/apps/conf/seed-new password)"
  check_eq "seed-new username" "$(remote openova/apps/conf/seed-new username)" conf-owner && check_eq "seed-new length" "${#v1}" 32 \
    || fail "E2 the generated seed was not written"
  changes openova/apps/conf/seed-replace password 2 90 || fail "E2 control: the Replace seed did not change"
  k -n "$APP_NS" annotate --overwrite pushsecrets.external-secrets.io/conf-seed-new pushsecrets.external-secrets.io/conf-seed-kept conformance/poke="$SECONDS" >/dev/null
  changes openova/apps/conf/seed-replace password 2 90 || fail "E2 control: the Replace seed stopped changing"
  check_eq "seed-new password" "$(remote openova/apps/conf/seed-new password)" "$v1" || fail "E2 IfNotExists overwrote a key it had written"
  check_eq "seed-kept username" "$(remote openova/apps/conf/seed-kept username)" hand && check_eq "seed-kept password" "$(remote openova/apps/conf/seed-kept password)" "$P_HAND" \
    || fail "E2 IfNotExists overwrote a key written by hand"
  managed openova/apps/conf/seed-kept && fail "E2 IfNotExists marked a key written by hand"
  pass "E2 PushSecret with generator and IfNotExists: written once and kept while the Replace control changed 4 times; a hand-written key untouched"

  # E3
  ready externalsecrets.external-secrets.io/$AUD-token "$ESO_NS" || fail "E3 the generator did not mint the push token"
  local claims; claims="$(jwt_payload "$(secret_val "$ESO_NS" $AUD-token token)")"
  grep -q '"openova_region":"b"' <<<"$claims" && grep -Eq "\"aud\":\\[?\"$AUD\"" <<<"$claims" || fail "E3 the token is not the identity token: $claims"
  store ClusterSecretStore vault-b "" "{jwt: {path: jwt-peer-b, role: $AUD, secretRef: {name: $AUD-token, namespace: $ESO_NS, key: token}}}" | apply >/dev/null
  ready clustersecretstores.external-secrets.io/vault-b || fail "E3 the JWT store vault-b not Ready"
  local vp; vp="$(openssl rand -hex 12)"
  k -n "$APP_NS" create secret generic conf-peer-src --from-literal=value="$vp" >/dev/null
  { push conf-peer-push vault-b conf-peer-src openova/apps/conf/peer None
    push conf-peer-outside vault-b conf-peer-src catalyst/conf/peer-outside None; } | apply >/dev/null
  ready pushsecrets.external-secrets.io/conf-peer-push "$APP_NS" || fail "E3 PushSecret conf-peer-push not Ready"
  check_eq peer "$(remote openova/apps/conf/peer value)" "$vp" && managed openova/apps/conf/peer || fail "E3 the push did not land"
  not_ready pushsecrets.external-secrets.io/conf-peer-outside "$APP_NS" && remote_absent catalyst/conf/peer-outside \
    || fail "E3 control: a key outside the JWT role's grant was written"
  pass "E3 PushSecret to a JWT-authenticated store: identity token minted by the generator, push landed; outside the grant refused"

  # E4
  k -n "$APP_NS" create secret generic conf-delete-src --from-literal=value="$(openssl rand -hex 12)" >/dev/null
  { push conf-delete conformance-delete conf-delete-src openova/apps/conf/delete/item Delete
    push conf-keep conformance-delete conf-delete-src openova/apps/conf/delete/kept None; } | apply >/dev/null
  ready pushsecrets.external-secrets.io/conf-delete "$APP_NS" && ready pushsecrets.external-secrets.io/conf-keep "$APP_NS" \
    || fail "E4 the PushSecrets are not Ready"
  [ -n "$(remote openova/apps/conf/delete/item value)" ] && [ -n "$(remote openova/apps/conf/delete/kept value)" ] || fail "E4 the pushes did not land"
  k -n "$APP_NS" delete pushsecrets.external-secrets.io conf-delete conf-keep --wait=true --timeout=120s >/dev/null || fail "E4 the PushSecrets were not deleted"
  until_ok 30 remote_absent openova/apps/conf/delete/item || fail "E4 deletionPolicy Delete left the key in OpenBao"
  [ -n "$(remote openova/apps/conf/delete/kept value)" ] || fail "E4 control: deletionPolicy None deleted the key"
  pass "E4 deletionPolicy: Delete: the key is gone with its PushSecret; with None it stays"

  # E5
  { es "$SCOPED_NS" conf-ns-read SecretStore conf-ns-store "$SCOPED_NS/item" value
    echo ---; es "$SCOPED_NS" conf-ns-outside SecretStore conf-ns-store openova/apps/conf/read username
    echo ---; es "$APP_NS" conf-ns-foreign SecretStore conf-ns-store "$SCOPED_NS/item" value; } | apply >/dev/null
  ready externalsecrets.external-secrets.io/conf-ns-read "$SCOPED_NS" || fail "E5 ExternalSecret conf-ns-read not Ready"
  check_eq scoped "$(secret_val "$SCOPED_NS" conf-ns-read value)" "$P_SCOPED" || fail "E5 the namespaced store did not read its key"
  not_ready externalsecrets.external-secrets.io/conf-ns-outside "$SCOPED_NS" && no_secret "$SCOPED_NS" conf-ns-outside \
    || fail "E5 control: the namespaced store read a key only the controller's role may read"
  not_ready externalsecrets.external-secrets.io/conf-ns-foreign "$APP_NS" && no_secret "$APP_NS" conf-ns-foreign \
    || fail "E5 control: another namespace used the namespaced store"
  local out
  if out="$(store SecretStore conf-ns-borrow "$SCOPED_NS" "$(k8s_auth external-secrets external-secrets "$ESO_NS")" | apply 2>&1)"; then
    not_ready secretstores.external-secrets.io/conf-ns-borrow "$SCOPED_NS" || fail "E5 control: a store borrowed the controller's ServiceAccount"
  else
    grep -qi namespace <<<"$out" || fail "E5 control: the borrowing store was refused for another reason: $out"
  fi
  pass "E5 namespaced store with serviceAccountRef: reads as its own ServiceAccount; outside reads, other namespaces and a borrowed ServiceAccount refused"
  echo "conformance: 5 of 5 behaviours passed (External Secrets chart $ESO_VERSION, OpenBao $BAO_TAG)"
}

main() {
  case "${1:-}" in
    --list) echo "$BEHAVIOURS" ;;
    --self-test) self_test ;;
    "") run ;;
    *) echo "usage: $0 [--list|--self-test]" >&2; exit 2 ;;
  esac
}

if [ "${BASH_SOURCE[0]}" = "$0" ]; then main "$@"; fi
