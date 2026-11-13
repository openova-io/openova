#!/usr/bin/env bash
# SEC-140 spike: delivery of a mounted (tier B) value by the Secrets Store CSI
# driver and the OpenBao CSI provider, on the host and inside an Organization's
# virtual cluster. Runs in CI only, on a throwaway kind cluster.
#
# Every measurement is one line in $OUT/results.txt. A FAIL line is a measured
# result, not a script error; the script carries on to the next measurement.
set -uo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
OUT=${OUT:-$PWD/out}
WORK=$(mktemp -d)
mkdir -p "$OUT"
RES="$OUT/results.txt"
: >"$RES"
trap 'kill $(jobs -p) 2>/dev/null; rm -rf "$WORK"' EXIT

BAO_CHART=0.30.7          # chart of OpenBao 2.7.2; ships CSI provider 2.0.3
CSI_CHART=1.6.2           # Secrets Store CSI driver
VC_CHART=0.33.4           # loft vcluster, the Organization virtual cluster (platform pins 0.33.*)
ESO_VERSIONS=${ESO_VERSIONS:-"0.10.7 0.16.2 2.14.0"}
BAO_IMAGE=quay.io/openbao/openbao:2.7.2
PROBE_IMAGE=busybox:1.36
BAO_SVC=http://openbao.openbao.svc.cluster.local:8200

rec() { printf '%s\n' "$*" | tee -a "$RES"; }
hdr() { printf '\n========== %s ==========\n' "$*"; }
oneline() { tr '\n\t' '  ' | tr -s ' ' | cut -c1-500; }
sha() { tr -d '\n' | sha256sum | cut -c1-16; }

# OpenBao CLI inside the server pod.
bx() { kubectl -n openbao exec -i openbao-0 -c openbao -- env BAO_ADDR=http://127.0.0.1:8200 BAO_TOKEN="${ROOT:-}" bao "$@"; }
bao_log() { kubectl -n openbao logs openbao-0 -c openbao --tail=-1 2>/dev/null; }
prov_log() { kubectl -n openbao logs -l app.kubernetes.io/name=openbao-csi-provider -c openbao-csi-provider --tail=-1 2>/dev/null; }

# k <kubeconfig or -> args: kubectl against the host (-) or a virtual cluster.
# A virtual cluster is reached through a port-forward that a loop restarts; a
# call that hits the restart window is retried. Never pass stdin to k.
k() {
  local kc=$1; shift
  if [ "$kc" = - ]; then kubectl "$@"; return; fi
  local i rc errf="$WORK/k.err"
  for i in 1 2 3 4 5 6; do
    kubectl --kubeconfig "$kc" "$@" 2>"$errf"; rc=$?
    if [ $rc -ne 0 ] && grep -q -E 'connection refused|lost connection|broken pipe|EOF|connection reset|i/o timeout' "$errf"; then
      sleep 3; continue
    fi
    break
  done
  cat "$errf" >&2; return $rc
}
vkc() { echo "$WORK/vc-$1.kubeconfig"; }

csi_pod() { # kc ns name serviceaccount spc automount
  local kc=$1 ns=$2 name=$3 sa=$4 spc=$5 am=$6 f="$WORK/pod-$2-$3.yaml"
  cat >"$f" <<EOF
apiVersion: v1
kind: Pod
metadata: {name: $name, namespace: $ns, labels: {spike: sec-140}}
spec:
  serviceAccountName: $sa
  automountServiceAccountToken: $am
  terminationGracePeriodSeconds: 1
  containers:
    - name: probe
      image: $PROBE_IMAGE
      command: ["sh", "-c", "ls -l /var/run/secrets/openova/; sleep 3600"]
      volumeMounts:
        - {name: openova, mountPath: /var/run/secrets/openova, readOnly: true}
  volumes:
    - name: openova
      csi:
        driver: secrets-store.csi.k8s.io
        readOnly: true
        volumeAttributes: {secretProviderClass: $spc}
EOF
  k "$kc" apply -f "$f" >/dev/null
}

# Prints PASS, or FAIL(<last mount event>). With a 5th argument (host namespace)
# the pod is a virtual-cluster pod and its readiness is read from the host pod
# the virtual cluster synced, which is the pod the kubelet actually runs.
pod_result() {
  local kc=$1 ns=$2 name=$3 t=${4:-120} hostns=${5:-} ev hp i
  if [ -z "$hostns" ]; then
    if kubectl -n "$ns" wait --for=condition=Ready "pod/$name" --timeout="${t}s" >/dev/null 2>&1; then echo PASS; return 0; fi
    ev=$(kubectl -n "$ns" get events --field-selector "involvedObject.name=$name" -o jsonpath='{range .items[*]}{.reason}: {.message}{"\n"}{end}' 2>/dev/null | grep -E 'FailedMount|Failed|Error' | tail -1 | oneline)
    echo "FAIL(${ev:-not ready, no event})"; return 1
  fi
  for i in $(seq 1 60); do hp=$(host_pod "$hostns" "$ns" "$name" | cut -d' ' -f1); [ -n "$hp" ] && break; sleep 2; done
  [ -z "$hp" ] && { echo "FAIL(never synced to the host)"; return 1; }
  if kubectl -n "$hostns" wait --for=condition=Ready "pod/$hp" --timeout="${t}s" >/dev/null 2>&1; then echo PASS; return 0; fi
  ev=$(kubectl -n "$hostns" get events --field-selector "involvedObject.name=$hp" -o jsonpath='{range .items[*]}{.reason}: {.message}{"\n"}{end}' 2>/dev/null | grep -E 'FailedMount|Failed|Error|BackOff' | tail -1 | oneline)
  echo "FAIL(${ev:-not ready, no event})"; return 1
}

# Host pod of a virtual-cluster pod: "<host name> <service account> <automount>".
host_pod() { # hostns vns vname
  kubectl -n "$1" get pods -o json 2>/dev/null | jq -r --arg vns "$2" --arg vn "$3" '.items[]
    | select((.metadata.annotations["vcluster.loft.sh/object-name"]==$vn and .metadata.annotations["vcluster.loft.sh/object-namespace"]==$vns)
             or .metadata.name == ($vn + "-x-" + $vns + "-x-vcluster"))
    | "\(.metadata.name) \(.spec.serviceAccountName) \(.spec.automountServiceAccountToken)"' | head -1
}

file_hash() { k "$1" -n "$2" exec "$3" -- cat /var/run/secrets/openova/db-password 2>/dev/null | sha; }
value_hash() { bx kv get -mount=mounted -field=password "$1" 2>/dev/null | sha; }

spc() { # ns name secretPath [address line] [audience line]
  local ns=$1 name=$2 path=$3 addrline=${4-"openbaoAddress: $BAO_SVC"} audline=${5-"audience: openbao"}
  kubectl -n "$ns" apply -f - >/dev/null <<EOF
apiVersion: secrets-store.csi.x-k8s.io/v1
kind: SecretProviderClass
metadata: {name: $name, namespace: $ns}
spec:
  provider: openbao
  parameters:
    $addrline
    roleName: workload
    $audline
    objects: |
      - objectName: db-password
        secretPath: $path
        secretKey: password
EOF
}

birth_prep() { # ns
  kubectl get ns "$1" >/dev/null 2>&1 || kubectl create ns "$1" >/dev/null
  kubectl -n "$1" create sa secret-birth --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  kubectl -n "$1" create configmap spike-scripts --from-file="$HERE/scripts" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
}

birth_job() { # ns jobname key [script]
  local ns=$1 name=$2 key=$3 script=${4:-birth.sh}
  kubectl -n "$ns" apply -f - >/dev/null <<EOF
apiVersion: batch/v1
kind: Job
metadata: {name: $name, namespace: $ns}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      serviceAccountName: secret-birth
      automountServiceAccountToken: false
      containers:
        - name: birth
          image: $BAO_IMAGE
          command: ["/bin/sh", "/scripts/$script"]
          env:
            - {name: BAO_ADDR, value: "$BAO_SVC"}
            - {name: KEY, value: "$key"}
            - name: NS
              valueFrom: {fieldRef: {fieldPath: metadata.namespace}}
          volumeMounts:
            - {name: scripts, mountPath: /scripts, readOnly: true}
            - {name: token, mountPath: /var/run/secrets/openbao, readOnly: true}
      volumes:
        - name: scripts
          configMap: {name: spike-scripts}
        - name: token
          projected:
            sources:
              - serviceAccountToken: {audience: openbao, expirationSeconds: 600, path: token}
EOF
}

# Waits for a Job to finish (max t seconds) and prints "<exit code>|<log>".
job_out() { # kc ns name [t]
  local kc=$1 ns=$2 name=$3 t=${4:-150} i st code
  for i in $(seq 1 "$t"); do
    st=$(k "$kc" -n "$ns" get job "$name" -o jsonpath='{.status.succeeded}/{.status.failed}' 2>/dev/null)
    case "$st" in 1/*|*/1) break ;; esac
    sleep 1
  done
  code=$(k "$kc" -n "$ns" get pods -l "job-name=$name" -o jsonpath='{.items[0].status.containerStatuses[0].state.terminated.exitCode}' 2>/dev/null)
  echo "${code:-none}|$(k "$kc" -n "$ns" logs "job/$name" --tail=40 2>/dev/null | oneline)"
}

# Successful logins with role workload, by host identity, for one namespace.
logins() { # ns
  bao_log | jq -R -r --arg ns "$1" 'fromjson? | select(.type=="response" and .request.path=="auth/kubernetes/login")
    | (.auth.metadata // .response.auth.metadata // {}) as $m
    | select(($m.service_account_namespace // "") == $ns)
    | "\($m.role):\($m.service_account_namespace)/\($m.service_account_name)"' \
    | sort | uniq -c | awk '{printf "%s(x%s) ", $2, $1}'
}

# Reads of mounted values below one namespace: identity, path, outcome.
reads() { # ns
  bao_log | jq -R -r --arg ns "$1" 'fromjson? | select(.type=="response" and .request.operation=="read")
    | select(.request.path | startswith("mounted/data/" + $ns + "/"))
    | (.auth.metadata // {}) as $m
    | "\($m.service_account_namespace // "-")/\($m.service_account_name // "-")->\(.request.path | sub("^mounted/data/"; "")):\(if .error then "denied" else "ok" end)"' \
    | sort | uniq -c | awk '{printf "%s(x%s) ", $2, $1}'
}

# ---------------------------------------------------------------------------
hdr "precondition and versions"
pre=$(cd "$HERE/../../.." && git grep -c 'SecretProviderClass' -- platform products core clusters 2>/dev/null)
rec "PRECONDITION git-grep-SecretProviderClass-in-platform/products/core/clusters: ${pre:-<nothing>}"
rec "VERSION kubernetes-host $(kubectl version -o json | jq -r .serverVersion.gitVersion) helm $(helm version --short)"

helm repo add openbao https://openbao.github.io/openbao-helm >/dev/null
helm repo add csi https://kubernetes-sigs.github.io/secrets-store-csi-driver/charts >/dev/null
helm repo add eso https://charts.external-secrets.io >/dev/null
helm repo add loft https://charts.loft.sh >/dev/null
helm repo update >/dev/null

# ---------------------------------------------------------------------------
hdr "step 1: CSI driver, OpenBao, CSI provider, mount, policies, roles"
helm install csi csi/secrets-store-csi-driver --version "$CSI_CHART" -n kube-system \
  --set syncSecret.enabled=false --set enableSecretRotation=true --set rotationPollInterval=3600s \
  --wait --timeout 5m
rec "STEP1 csi-driver chart=$CSI_CHART image=$(kubectl -n kube-system get ds -l app=secrets-store-csi-driver -o jsonpath='{.items[0].spec.template.spec.containers[?(@.name=="secrets-store")].image}') tokenRequests=$(kubectl get csidriver secrets-store.csi.k8s.io -o jsonpath='{.spec.tokenRequests}')"
rec "STEP1 csi-driver-can-create-secrets: $(kubectl auth can-i create secrets --as=system:serviceaccount:kube-system:secrets-store-csi-driver -A 2>&1 | oneline)"

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
helm install openbao openbao/openbao --version "$BAO_CHART" -n openbao --create-namespace -f "$WORK/openbao-values.yaml" --timeout 5m
for i in $(seq 1 120); do
  [ "$(kubectl -n openbao get pod openbao-0 -o jsonpath='{.status.phase}' 2>/dev/null)" = Running ] && bx status -format=json >/dev/null 2>&1; rc=$?
  [ "$rc" = 2 ] && break   # 2 = reachable and sealed
  sleep 2
done
if [ "${rc:-1}" != 2 ]; then
  rec "STEP1 openbao server did not come up (status rc=${rc:-none}); log tail: $(kubectl -n openbao logs openbao-0 -c openbao --tail=20 2>&1 | oneline)"
  kubectl -n openbao describe pod openbao-0 | tail -30
  exit 1
fi
INIT=$(bx operator init -key-shares=1 -key-threshold=1 -format=json)
UNSEAL=$(jq -r '.unseal_keys_b64[0]' <<<"$INIT")
ROOT0=$(jq -r '.root_token' <<<"$INIT")
bx operator unseal "$UNSEAL" >/dev/null
rec "STEP1 openbao $(bx version | oneline) sealed=$(bx status -format=json | jq -r .sealed)"

# Root token generated from the unseal key. First without any token (the way a
# live Sovereign whose root token is gone would need it), then authenticated with
# the init root token. ROOT is the token bx sends.
gen_root() { # prints a new root token, or nothing; details to $WORK/genroot.txt
  local g e nonce otp enc
  bx operator generate-root -cancel >/dev/null 2>&1
  g=$(bx operator generate-root -init -format=json 2>&1)
  nonce=$(jq -r '.nonce // empty' <<<"$g" 2>/dev/null); otp=$(jq -r '.otp // empty' <<<"$g" 2>/dev/null)
  e=$(bx operator generate-root -nonce="$nonce" -format=json "$UNSEAL" 2>&1)
  enc=$(jq -r '.encoded_token // .encoded_root_token // empty' <<<"$e" 2>/dev/null)
  printf '%s %s' "$g" "$e" | oneline >"$WORK/genroot.txt"
  [ -n "$enc" ] && bx operator generate-root -decode="$enc" -otp="$otp" 2>/dev/null | awk 'NF{t=$NF} END{print t}'
}
ROOT=""
MODE=without-token
NEW=$(gen_root)
if [ -n "$NEW" ]; then
  rec "STEP1 root-token-from-unseal-key without-token PASS"
else
  rec "STEP1 root-token-from-unseal-key without-token FAIL ($(cat "$WORK/genroot.txt"))"
  ROOT=$ROOT0; MODE=authenticated-with-init-root-token
  NEW=$(gen_root)
fi
if [ -n "$NEW" ] && ROOT=$NEW && bx token lookup -format=json 2>/dev/null | jq -e '.data.policies | index("root")' >/dev/null; then
  bx token revoke "$ROOT0" >/dev/null 2>&1
  rec "STEP1 root-token-from-unseal-key $MODE PASS; init root token revoked (rc=$?); the rest runs on the generated token"
else
  ROOT=$ROOT0
  rec "STEP1 root-token-from-unseal-key authenticated FAIL ($(cat "$WORK/genroot.txt")); using init root token"
fi

bx auth enable kubernetes >/dev/null
bx write auth/kubernetes/config kubernetes_host=https://kubernetes.default.svc:443 >/dev/null
bx secrets enable -path=mounted -version=2 kv >/dev/null
ACC=$(bx auth list -format=json | jq -r '."kubernetes/".accessor')
TNS="{{identity.entity.aliases.$ACC.metadata.service_account_namespace}}"
TSA="{{identity.entity.aliases.$ACC.metadata.service_account_name}}"
for p in workload eso-ns eso-ns-p1 eso-ns-p2 eso-ns-p3 birth; do
  sed -e "s|@NS@|$TNS|g" -e "s|@SA@|$TSA|g" "$HERE/policies/$p.hcl" | bx policy write "$p" - >/dev/null
done
bx write auth/kubernetes/role/workload bound_service_account_names='*' bound_service_account_namespaces='*' \
  audience=openbao token_policies=workload token_ttl=15m token_max_ttl=1h >/dev/null
for r in eso-ns eso-ns-p1 eso-ns-p2 eso-ns-p3; do
  bx write "auth/kubernetes/role/$r" bound_service_account_names=eso-reader bound_service_account_namespaces='*' \
    token_policies="$r" token_ttl=15m token_max_ttl=1h >/dev/null
done
bx write auth/kubernetes/role/birth bound_service_account_names=secret-birth bound_service_account_namespaces='*' \
  audience=openbao token_policies=birth token_ttl=15m token_max_ttl=1h >/dev/null
rec "STEP1 mount=$(bx secrets list -format=json | jq -r '."mounted/" | "\(.type) v\(.options.version)"') policies=$(bx policy list | oneline) roles=$(bx list -format=json auth/kubernetes/role | jq -r 'join(",")')"
kubectl -n openbao rollout status ds/openbao-csi-provider --timeout=180s >/dev/null 2>&1
rec "STEP1 csi-provider image=$(kubectl -n openbao get ds openbao-csi-provider -o jsonpath='{.spec.template.spec.containers[0].image}') ready=$(kubectl -n openbao get ds openbao-csi-provider -o jsonpath='{.status.numberReady}/{.status.desiredNumberScheduled}')"

# ---------------------------------------------------------------------------
hdr "step 2: birth by External Secrets PushSecret (IfNotExists), three policy variants"
eso_cleanup() {
  local kind o
  for kind in pushsecrets externalsecrets secretstores; do
    for o in $(kubectl get "$kind" -A -o jsonpath='{range .items[*]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}' 2>/dev/null); do
      kubectl -n "${o%%/*}" delete "$kind" "${o#*/}" --wait=false >/dev/null 2>&1
      kubectl -n "${o%%/*}" patch "$kind" "${o#*/}" --type=merge -p '{"metadata":{"finalizers":null}}' >/dev/null 2>&1
    done
  done
  helm uninstall eso -n external-secrets --wait --timeout 3m >/dev/null 2>&1
  kubectl get crd -o name | grep -E 'external-secrets\.io' | xargs -r kubectl delete --timeout=120s >/dev/null 2>&1
  kubectl delete ns external-secrets --timeout=120s >/dev/null 2>&1
}
for V in $ESO_VERSIONS; do
  hdr "step 2 with External Secrets $V"
  case "$V" in 0.10.*) API=external-secrets.io/v1beta1 ;; *) API=external-secrets.io/v1 ;; esac
  if ! helm install eso eso/external-secrets --version "$V" -n external-secrets --create-namespace \
      --set installCRDs=true \
      --set image.repository=ghcr.io/external-secrets/external-secrets \
      --set webhook.image.repository=ghcr.io/external-secrets/external-secrets \
      --set certController.image.repository=ghcr.io/external-secrets/external-secrets \
      --wait --timeout 6m; then
    rec "STEP2 eso=$V install FAILED"; eso_cleanup; continue
  fi
  NS="eso-${V//./-}"
  kubectl create ns "$NS" >/dev/null; kubectl -n "$NS" create sa eso-reader >/dev/null
  M="$WORK/$NS.yaml"; : >"$M"
  for p in p0 p1 p2 p3; do
    role=eso-ns; [ "$p" = p0 ] || role="eso-ns-$p"
    cat >>"$M" <<EOF
---
apiVersion: $API
kind: SecretStore
metadata: {name: openbao-mounted-$p, namespace: $NS}
spec:
  provider:
    vault:
      server: $BAO_SVC
      path: mounted
      version: v2
      auth:
        kubernetes:
          mountPath: kubernetes
          role: $role
          serviceAccountRef: {name: eso-reader}
EOF
  done
  for i in $(seq 1 30); do kubectl apply -f "$M" >/dev/null 2>&1 && break; sleep 4; done
  kubectl -n "$NS" wait --for=condition=Ready secretstore --all --timeout=90s >/dev/null 2>&1
  for p in p0 p1 p2 p3; do
    rec "STEP2 eso=$V store=openbao-mounted-$p ready=$(kubectl -n "$NS" get secretstore "openbao-mounted-$p" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}') $(kubectl -n "$NS" get secretstore "openbao-mounted-$p" -o jsonpath='{.status.conditions[?(@.type=="Ready")].message}' | oneline)"
  done
  M2="$WORK/$NS-push.yaml"
  cat >"$M2" <<EOF
apiVersion: generators.external-secrets.io/v1alpha1
kind: Password
metadata: {name: db-password-e1, namespace: $NS}
spec: {length: 32, digits: 6, symbols: 0, noUpper: false, allowRepeat: true}
EOF
  for p in p0 p1 p2 p3; do
    cat >>"$M2" <<EOF
---
apiVersion: external-secrets.io/v1alpha1
kind: PushSecret
metadata: {name: db-password-$p-e1, namespace: $NS}
spec:
  refreshInterval: 15s
  updatePolicy: IfNotExists
  deletionPolicy: Delete
  secretStoreRefs:
    - {kind: SecretStore, name: openbao-mounted-$p}
  selector:
    generatorRef: {apiVersion: generators.external-secrets.io/v1alpha1, kind: Password, name: db-password-e1}
  data:
    - match: {secretKey: password, remoteRef: {remoteKey: $NS/sa/wordpress/db-password-$p/e1, property: password}}
EOF
  done
  for i in $(seq 1 30); do out=$(kubectl apply -f "$M2" 2>&1) && break; sleep 4; done
  rec "STEP2 eso=$V apply-generator-and-pushsecrets: $(echo "$out" | oneline)"
  for i in $(seq 1 30); do
    n=$(kubectl -n "$NS" get pushsecret -o json | jq '[.items[] | select((.status.conditions // []) | length > 0)] | length')
    [ "$n" = 4 ] && break; sleep 3
  done
  sleep 20
  for p in p0 p1 p2 p3; do
    st=$(kubectl -n "$NS" get pushsecret "db-password-$p-e1" -o json 2>/dev/null | jq -r '((.status.conditions // []) | map(select(.type=="Ready")) | .[0]) as $c | "\($c.status // "none") \($c.reason // "") \($c.message // "")"' | oneline)
    ver=$(bx kv metadata get -mount=mounted -format=json "$NS/sa/wordpress/db-password-$p/e1" 2>/dev/null | jq -r '.data.current_version // empty')
    rec "STEP2 eso=$V policy=$p pushsecret-ready=${st%% *} remote-value=${ver:-absent}${ver:+(current_version)} status=\"${st#* }\""
  done
  # Second reconcile: does IfNotExists keep the first value?
  sleep 35
  for p in p0 p1 p2 p3; do
    ver=$(bx kv metadata get -mount=mounted -format=json "$NS/sa/wordpress/db-password-$p/e1" 2>/dev/null | jq -r '.data.current_version // empty')
    [ -n "$ver" ] && rec "STEP2 eso=$V policy=$p after-two-more-refreshes remote-version=$ver ready=$(kubectl -n "$NS" get pushsecret "db-password-$p-e1" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')"
  done
  bao_log | jq -R -r --arg ns "$NS" 'fromjson? | select(.type=="response") | select(.request.path | test("^mounted/(data|metadata)/" + $ns + "/"))
    | "\((.auth.policies // []) | map(select(. != "default")) | join(",")) \(.request.operation) \(.request.path | sub("^mounted/"; "") | sub($ns; "NS")) -> \(if .error then (.error | tostring | gsub("\\s+"; " ")) else "ok" end)"' \
    | sort | uniq -c >"$OUT/eso-$V-openbao-calls.txt"
  while read -r line; do rec "STEP2 eso=$V openbao-call: $line"; done <"$OUT/eso-$V-openbao-calls.txt"
  kubectl -n external-secrets logs deploy/eso-external-secrets --tail=200 >"$OUT/eso-$V-controller.log" 2>&1
  eso_cleanup
done

hdr "step 2 fallback: one-shot birth Job, create-only policy"
birth_prep birthlab
birth_job birthlab birth-1 shared/probe/e1
rec "STEP2F first-run exit|log: $(job_out - birthlab birth-1)"
birth_job birthlab birth-2 shared/probe/e1
rec "STEP2F second-run exit|log: $(job_out - birthlab birth-2)"
birth_job birthlab probe-1 shared/probe/e1 probe-birth.sh
rec "STEP2F probe exit|log: $(job_out - birthlab probe-1)"
rec "STEP2F value-after-probes version=$(bx kv metadata get -mount=mounted -format=json birthlab/shared/probe/e1 | jq -r .data.current_version) is-probe-string=$( [ "$(bx kv get -mount=mounted -field=password birthlab/shared/probe/e1)" = probe ] && echo yes || echo no)"

# ---------------------------------------------------------------------------
hdr "step 3: host-namespace pod, role workload, audience openbao"
birth_prep acme
kubectl -n acme create sa wordpress >/dev/null; kubectl -n acme create sa other >/dev/null
birth_job acme birth-wp sa/wordpress/db-password/e1
rec "STEP3 seed exit|log: $(job_out - acme birth-wp)"
P3=mounted/data/acme/sa/wordpress/db-password/e1
spc acme wordpress "$P3" "vaultAddress: $BAO_SVC"
spc acme wp-openbaoaddr "$P3" "openbaoAddress: $BAO_SVC"
spc acme wp-baoaddr "$P3" "baoAddress: $BAO_SVC"
spc acme wp-noaddr "$P3" ""
spc acme wp-noaud "$P3" "openbaoAddress: $BAO_SVC" ""
csi_pod - acme p3-vaultaddress wordpress wordpress true
csi_pod - acme p3-openbaoaddress wordpress wp-openbaoaddr true
csi_pod - acme p3-baoaddress wordpress wp-baoaddr true
csi_pod - acme p3-noaddress wordpress wp-noaddr true
csi_pod - acme p3-noaudience wordpress wp-noaud true
csi_pod - acme p3-otherserviceaccount other wordpress true
csi_pod - acme p3-noautomount wordpress wordpress false
VH=$(value_hash acme/sa/wordpress/db-password/e1)
for p in p3-vaultaddress p3-openbaoaddress p3-baoaddress p3-noaddress p3-noaudience p3-otherserviceaccount p3-noautomount; do
  r=$(pod_result - acme "$p" 90)
  m=""; [ "$r" = PASS ] && { [ "$(file_hash - acme "$p")" = "$VH" ] && m=" file-matches-openbao-value=yes" || m=" file-matches-openbao-value=NO"; }
  rec "STEP3 pod=$p result=$r$m"
done
rec "STEP3 file-mode-and-owner: $(kubectl -n acme exec p3-vaultaddress -- ls -lnL /var/run/secrets/openova/ 2>/dev/null | oneline)"
cnt=$(kubectl get secrets -A -o json | jq -r '.items[].data // {} | .[]' | while read -r v; do printf '%s' "$v" | base64 -d 2>/dev/null | sha; echo; done | grep -c -x "$VH")
rec "STEP3 secret-objects-holding-the-value=$cnt"
rec "STEP3 secrets-created-by-provider: $(kubectl -n openbao get secrets -o name | oneline)"
rec "STEP3 provider-token-path self-generated=$(prov_log | grep -c 'using self-generated service account JWT') from-mount-request=$(prov_log | grep -c 'using token from mount request')"
rec "STEP3 logins acme: $(logins acme)"
rec "STEP3 reads acme: $(reads acme)"

# ---------------------------------------------------------------------------
hdr "step 4: pods inside Organization virtual clusters"
vc_values() {
cat <<EOF
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
vc_values true >"$WORK/vc-org2.yaml"    # org2: same, with service-account sync on
helm install vcluster loft/vcluster --version "$VC_CHART" -n org1 --create-namespace -f "$WORK/vc-org1.yaml" --wait --timeout 8m >"$OUT/vc-org1-install.log" 2>&1 &
P1=$!
helm install vcluster loft/vcluster --version "$VC_CHART" -n org2 --create-namespace -f "$WORK/vc-org2.yaml" --wait --timeout 8m >"$OUT/vc-org2-install.log" 2>&1 &
P2=$!
wait $P1; rc1=$?; wait $P2; rc2=$?
rec "STEP4 vcluster chart=$VC_CHART install org1=$rc1 org2=$rc2 image=$(kubectl -n org1 get sts vcluster -o jsonpath='{.spec.template.spec.containers[0].image}')"
vc_connect() { # ns port
  local ns=$1 port=$2 kc i; kc=$(vkc "$1")
  for i in $(seq 1 60); do kubectl -n "$ns" get secret vc-vcluster >/dev/null 2>&1 && break; sleep 2; done
  kubectl -n "$ns" get secret vc-vcluster -o jsonpath='{.data.config}' | base64 -d \
    | sed -E "s#server: https://[^[:space:]]+#server: https://localhost:$port#" >"$kc"
  # kubectl port-forward exits on the first broken stream; restart it in a loop.
  ( while :; do kubectl -n "$ns" port-forward svc/vcluster "$port:443" >>"$OUT/port-forward-$ns.log" 2>&1; sleep 1; done ) &
  for i in $(seq 1 60); do kubectl --kubeconfig "$kc" get ns >/dev/null 2>&1 && break; sleep 2; done
}
vc_connect org1 18443
vc_connect org2 19443
VC1=$(vkc org1); VC2=$(vkc org2)
for vc in "$VC1" "$VC2"; do
  k "$vc" create ns apps >/dev/null
  k "$vc" -n apps create sa app >/dev/null
  k "$vc" -n apps create sa other >/dev/null
done
rec "STEP4 secretproviderclass-api-inside-virtual-cluster: $(k "$VC1" api-resources 2>/dev/null | grep -ci secretproviderclass) (0 = the object can only live on the host)"

# org1: service-account sync off (the platform's current values)
birth_prep org1
birth_job org1 birth-shared shared/db-password/e1
rec "STEP4 org1 seed exit|log: $(job_out - org1 birth-shared)"
spc org1 org-db mounted/data/org1/shared/db-password/e1
csi_pod "$VC1" apps v-app app org-db true
csi_pod "$VC1" apps v-other other org-db true
csi_pod "$VC1" apps v-noautomount app org-db false
VH1=$(value_hash org1/shared/db-password/e1)
for p in v-app v-other v-noautomount; do
  r=$(pod_result "$VC1" apps "$p" 150 org1)
  m=""; [ "$r" = PASS ] && { [ "$(file_hash "$VC1" apps "$p")" = "$VH1" ] && m=" file-matches=yes" || m=" file-matches=NO"; }
  rec "STEP4 org1 sa-sync=off virtual=apps/$p result=$r$m host-pod(name sa automount)=$(host_pod org1 apps "$p")"
done
rec "STEP4 org1 logins: $(logins org1)"
rec "STEP4 org1 reads: $(reads org1)"

# R26: a Job created at run time inside the virtual cluster with one CSI object.
cat >"$WORK/v-fetch.yaml" <<EOF
apiVersion: batch/v1
kind: Job
metadata: {name: v-fetch, namespace: apps}
spec:
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      serviceAccountName: app
      containers:
        - name: fetch
          image: $PROBE_IMAGE
          command: ["sh", "-c", "test -s /var/run/secrets/openova/db-password && echo mounted-bytes=\$(wc -c < /var/run/secrets/openova/db-password)"]
          volumeMounts:
            - {name: openova, mountPath: /var/run/secrets/openova, readOnly: true}
      volumes:
        - name: openova
          csi:
            driver: secrets-store.csi.k8s.io
            readOnly: true
            volumeAttributes: {secretProviderClass: org-db}
EOF
k "$VC1" apply -f "$WORK/v-fetch.yaml" >/dev/null
rec "STEP4 org1 runtime-job exit|log: $(job_out "$VC1" apps v-fetch 150) host-pod=$(kubectl -n org1 get pods -o json | jq -r '.items[] | select(.metadata.annotations["vcluster.loft.sh/object-name"] // "" | startswith("v-fetch-")) | "\(.metadata.name) \(.spec.serviceAccountName) \(.status.phase)"' | head -1)"

# org2: service-account sync on
birth_prep org2
birth_job org2 birth-shared shared/db-password/e1
rec "STEP4 org2 seed-shared exit|log: $(job_out - org2 birth-shared)"
spc org2 org-db mounted/data/org2/shared/db-password/e1
csi_pod "$VC2" apps v-app app org-db true
r=$(pod_result "$VC2" apps v-app 150 org2)
HP=$(host_pod org2 apps v-app)
HSA=$(echo "$HP" | cut -d' ' -f2)
rec "STEP4 org2 sa-sync=on virtual=apps/v-app spc=shared result=$r host-pod(name sa automount)=$HP"
if [ -n "$HSA" ]; then
  birth_job org2 birth-sa "sa/$HSA/db-password/e1"
  rec "STEP4 org2 seed-sa exit|log: $(job_out - org2 birth-sa)"
  spc org2 org-db-sa "mounted/data/org2/sa/$HSA/db-password/e1"
  csi_pod "$VC2" apps v-app-sa app org-db-sa true
  csi_pod "$VC2" apps v-other-sa other org-db-sa true
  for p in v-app-sa v-other-sa; do
    rec "STEP4 org2 sa-sync=on virtual=apps/$p spc=sa/$HSA result=$(pod_result "$VC2" apps "$p" 120 org2) host-pod(name sa automount)=$(host_pod org2 apps "$p")"
  done
  k "$VC2" -n apps delete pod v-app-sa --wait=true >/dev/null 2>&1
  for i in $(seq 1 30); do [ -z "$(host_pod org2 apps v-app-sa)" ] && break; sleep 2; done
  csi_pod "$VC2" apps v-app-sa app org-db-sa true
  rec "STEP4 org2 recreated virtual=apps/v-app-sa result=$(pod_result "$VC2" apps v-app-sa 120 org2) host-pod(name sa automount)=$(host_pod org2 apps v-app-sa)"
fi
# A service-account name whose translated host name would pass 63 characters.
LONGSA=wordpress-application-database-client-account
k "$VC2" -n apps create sa "$LONGSA" >/dev/null
csi_pod "$VC2" apps v-long "$LONGSA" org-db true
rec "STEP4 org2 long-name virtual-sa=apps/$LONGSA (${#LONGSA} chars) result=$(pod_result "$VC2" apps v-long 120 org2) host-pod(name sa automount)=$(host_pod org2 apps v-long)"
k "$VC2" -n apps delete pod v-long --wait=true >/dev/null 2>&1
for i in $(seq 1 30); do [ -z "$(host_pod org2 apps v-long)" ] && break; sleep 2; done
csi_pod "$VC2" apps v-long "$LONGSA" org-db true
rec "STEP4 org2 long-name recreated result=$(pod_result "$VC2" apps v-long 120 org2) host-pod(name sa automount)=$(host_pod org2 apps v-long)"
rec "STEP4 org2 host-serviceaccounts: $(kubectl -n org2 get sa -o name | oneline)"
rec "STEP4 org2 logins: $(logins org2)"
rec "STEP4 org2 reads: $(reads org2)"

# ---------------------------------------------------------------------------
hdr "step 5: file variable and entrypoint shim, inside the org1 virtual cluster"
k "$VC1" apply -f "$HERE/manifests/step5-db.yaml" >/dev/null
rec "STEP5 postgres:16-alpine POSTGRES_PASSWORD_FILE pod=$(pod_result "$VC1" apps db 240 org1)"
k "$VC1" apply -f "$HERE/manifests/step5-clients.yaml" >/dev/null
rec "STEP5 shim-client exit|log: $(job_out "$VC1" apps client-shim 180)"
rec "STEP5 shim-missing-file exit|log: $(job_out "$VC1" apps client-missing 180)"
rec "STEP5 control-no-password exit|log: $(job_out "$VC1" apps client-nopass 180)"
rec "STEP5 db-log-tail: $(k "$VC1" -n apps logs db --tail=5 2>/dev/null | oneline)"

# ---------------------------------------------------------------------------
hdr "phase B: driver asks the kubelet for the token (CSIDriver tokenRequests audience openbao)"
if ! helm upgrade csi csi/secrets-store-csi-driver --version "$CSI_CHART" -n kube-system --reuse-values \
    --set 'tokenRequests[0].audience=openbao' --wait --timeout 5m >"$OUT/csi-upgrade.log" 2>&1; then
  kubectl delete csidriver secrets-store.csi.k8s.io >/dev/null 2>&1
  helm upgrade csi csi/secrets-store-csi-driver --version "$CSI_CHART" -n kube-system --reuse-values \
    --set 'tokenRequests[0].audience=openbao' --wait --timeout 5m >>"$OUT/csi-upgrade.log" 2>&1
fi
rec "PHASEB csidriver tokenRequests=$(kubectl get csidriver secrets-store.csi.k8s.io -o jsonpath='{.spec.tokenRequests}')"
b0=$(prov_log | grep -c 'using token from mount request')
csi_pod - acme pb-noautomount wordpress wordpress false
csi_pod - acme pb-noaudience wordpress wp-noaud false
csi_pod "$VC1" apps vb-app app org-db true
[ -n "${HSA:-}" ] && csi_pod "$VC2" apps vb-app-sa app org-db-sa true
rec "PHASEB host pod=pb-noautomount result=$(pod_result - acme pb-noautomount 90)"
rec "PHASEB host pod=pb-noaudience(spc without audience) result=$(pod_result - acme pb-noaudience 90)"
rec "PHASEB org1 virtual=apps/vb-app result=$(pod_result "$VC1" apps vb-app 120 org1) host-pod=$(host_pod org1 apps vb-app)"
[ -n "${HSA:-}" ] && rec "PHASEB org2 virtual=apps/vb-app-sa result=$(pod_result "$VC2" apps vb-app-sa 120 org2) host-pod=$(host_pod org2 apps vb-app-sa)"
rec "PHASEB provider-token-path from-mount-request(new)=$(( $(prov_log | grep -c 'using token from mount request') - b0 ))"
CRB=$(kubectl get clusterrolebinding -o name | grep -i 'csi-provider' | head -1)
kubectl delete "$CRB" >/dev/null 2>&1
rec "PHASEB removed provider token-mint binding: ${CRB:-none found}; provider-can-create-tokens=$(kubectl auth can-i create serviceaccounts --subresource=token --as=system:serviceaccount:openbao:openbao-csi-provider -A 2>&1 | oneline)"
csi_pod - acme pc-noautomount wordpress wordpress false
csi_pod "$VC1" apps vc-app app org-db true
rec "PHASEB without-provider-token-right host pod=pc-noautomount result=$(pod_result - acme pc-noautomount 90)"
rec "PHASEB without-provider-token-right org1 virtual=apps/vc-app result=$(pod_result "$VC1" apps vc-app 120 org1)"
rec "PHASEB logins acme: $(logins acme)"
rec "PHASEB logins org1: $(logins org1)"
rec "PHASEB logins org2: $(logins org2)"

# ---------------------------------------------------------------------------
hdr "evidence"
bao_log | jq -R -r 'fromjson? | select(.type=="response" and .request.path=="auth/kubernetes/login")
  | (.auth.metadata // .response.auth.metadata // {}) as $m
  | "\(.time) role=\($m.role // "-") identity=\($m.service_account_namespace // "-")/\($m.service_account_name // "-") error=\(.error // "-" | tostring | gsub("\\s+"; " "))"' >"$OUT/openbao-logins.txt"
bao_log | grep -v '^{' >"$OUT/openbao-server.log"
prov_log >"$OUT/csi-provider.log"
kubectl -n kube-system logs -l app=secrets-store-csi-driver -c secrets-store --tail=-1 >"$OUT/csi-driver.log" 2>&1
kubectl get events -A --sort-by=.lastTimestamp >"$OUT/host-events.txt" 2>&1
kubectl get pods -A -o wide >"$OUT/host-pods.txt" 2>&1
rec "EVIDENCE failed-logins: $(grep -c -v 'error=-' "$OUT/openbao-logins.txt") successful-logins: $(grep -c 'error=-' "$OUT/openbao-logins.txt")"
hdr "results"
cat "$RES"
