#!/usr/bin/env bash
# Spike CLD-050 — shared helpers for the experiment jobs.
# Sourced by job-*.sh. Runs only inside a GitHub Actions job: it creates a
# kind cluster and Docker containers on the runner and nowhere else.

# ---------------------------------------------------------------- versions
CROSSPLANE_V2=2.4.3
KIND_NODE_V2="kindest/node:v1.33.7@sha256:d26ef333bdb2cbe9862a0f7c3803ecc7b4303d8cea8e814b481b09949d353040"
KIND_NODE_V1="kindest/node:v1.32.11@sha256:5fc52d52a7b9574015299724bd68f183702956aa4a2116ae75a63cb574b35af8"
# Digests as recorded by .github/workflows/mirror-crossplane-packages.yaml (CLD-063).
PROVIDER_V1111="xpkg.upbound.io/upbound/provider-opentofu@sha256:b4690e8ffe0a963b1d7df64f18f409217600b2a3bda25d1e01e4bafde976e675"
PROVIDER_V113="xpkg.upbound.io/upbound/provider-opentofu@sha256:4aeddb701725498d06baaf3532ec534c278169f19b5ad89aced615ac8360eb90"
FUNCTION_PT="xpkg.upbound.io/crossplane-contrib/function-patch-and-transform@sha256:4a36131ebf3b4203f9e79ad1810f198c5feb52a2a796cbf0e91f34efe741e27b"
OPENBAO_IMAGE="openbao/openbao:2.1.0"      # the version platform/openbao pins
SEAWEEDFS_IMAGE="chrislusf/seaweedfs:4.22"  # the version platform/seaweedfs pins
GITSERVER_IMAGE="gitea/gitea:1.24.6"
REGISTRY_IMAGE="registry:3.0.0"
NGINX_IMAGE="nginx:1.27-alpine"

S3_ACCESS=spikeaccess
S3_SECRET=spikesecret0123456789
S3_BUCKET=sovereign-state

SPIKE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
EV="${EVIDENCE_DIR:-$PWD/evidence}"
WORK="${WORK_DIR:-$PWD/spike-work}"
mkdir -p "$EV" "$WORK"
: > "$EV/verdicts.tsv"
: > "$EV/measures.tsv"

export AWS_ACCESS_KEY_ID=$S3_ACCESS AWS_SECRET_ACCESS_KEY=$S3_SECRET AWS_DEFAULT_REGION=us-east-1
export AWS_REQUEST_CHECKSUM_CALCULATION=when_required AWS_RESPONSE_CHECKSUM_VALIDATION=when_required
export AWS_PAGER=""

WSR=workspaces.opentofu.m.upbound.io   # job-upgrade switches this to the cluster-scoped kind

# ---------------------------------------------------------------- logging
log() { echo "[$(date -u +%H:%M:%S)] $*"; }
verdict() { # id verdict detail
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$EV/verdicts.tsv"
  echo "=== VERDICT $1: $2 -- $3"
}
measure() { # id key value
  printf '%s\t%s\t%s\n' "$1" "$2" "$3" >> "$EV/measures.tsv"
  echo "=== MEASURE $1 $2=$3"
}
note() { # id text
  printf '%s\tnote\t%s\n' "$1" "$2" >> "$EV/measures.tsv"
  echo "=== NOTE $1: $2"
}

# ---------------------------------------------------------------- cluster
kind_up() { # name node-image
  kind create cluster --name "$1" --image "$2" --wait 180s
  kubectl cluster-info
  kubectl version
}

# Containers on the kind Docker network stand in for services outside the
# cluster (object store, Git server, registry, web mirror). Docker assigns
# their addresses (kind's network has no user-configured IPv4 subnet, so
# --ip is not used); pods reach them by IP or by the names below, which the
# provider pod resolves through hostAliases in its runtime configuration.
GIT_HOST=git.spike.test
REG_HOST=registry.spike.test
WEB_HOST=mirror.spike.test
container_ip() { docker inspect -f '{{.NetworkSettings.Networks.kind.IPAddress}}' "$1"; }
kind_ips() { log "kind network: $(docker network inspect kind -f '{{range .IPAM.Config}}{{.Subnet}} {{end}}')"; }
spike_host_aliases() { # one hostAliases item per running spike container (indent 12)
  local n ip
  for n in "gitserver:$GIT_HOST" "registry:$REG_HOST" "webmirror:$WEB_HOST"; do
    ip=$(container_ip "${n%%:*}" 2>/dev/null)
    [[ -n $ip ]] && printf '            - ip: "%s"\n              hostnames: ["%s"]\n' "$ip" "${n#*:}"
  done
  return 0
}

make_certs() { # writes $WORK/certs/{ca.crt,tls.crt,tls.key,bundle.crt}
  local d="$WORK/certs"
  mkdir -p "$d"
  openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=cld-050 spike CA" \
    -keyout "$d/ca.key" -out "$d/ca.crt" 2>/dev/null
  openssl req -newkey rsa:2048 -nodes -subj "/CN=cld-050 spike services" \
    -keyout "$d/tls.key" -out "$d/tls.csr" 2>/dev/null
  printf 'subjectAltName=DNS:%s,DNS:%s,DNS:%s,DNS:localhost,IP:127.0.0.1\nbasicConstraints=CA:FALSE\nextendedKeyUsage=serverAuth\n' \
    "$GIT_HOST" "$REG_HOST" "$WEB_HOST" > "$d/ext.cnf"
  openssl x509 -req -in "$d/tls.csr" -CA "$d/ca.crt" -CAkey "$d/ca.key" -CAcreateserial \
    -days 2 -extfile "$d/ext.cnf" -out "$d/tls.crt" 2>/dev/null
  chmod 644 "$d"/*
  cat /etc/ssl/certs/ca-certificates.crt "$d/ca.crt" > "$d/bundle.crt"
  CA="$d/ca.crt"
  openssl x509 -in "$d/tls.crt" -noout -ext subjectAltName
}

ca_configmap() { # namespace — the CA bundle the provider pod trusts
  kubectl create namespace "$1" --dry-run=client -o yaml | kubectl apply -f -
  kubectl -n "$1" create configmap spike-ca --from-file=bundle.crt="$WORK/certs/bundle.crt" \
    --dry-run=client -o yaml | kubectl apply -f -
}

# ---------------------------------------------------------------- object store (outside the cluster)
start_seaweedfs() {
  cat > "$WORK/s3.json" <<EOF
{"identities":[{"name":"spike","credentials":[{"accessKey":"$S3_ACCESS","secretKey":"$S3_SECRET"}],"actions":["Admin","Read","Write","List","Tagging"]}]}
EOF
  docker run -d --name seaweedfs --network kind -p 8333:8333 \
    -v "$WORK/s3.json:/etc/s3.json:ro" "$SEAWEEDFS_IMAGE" \
    server -dir=/data -s3 -s3.port=8333 -s3.config=/etc/s3.json >/dev/null
  local i
  for i in $(seq 1 60); do
    if aws --endpoint-url http://127.0.0.1:8333 s3api list-buckets >/dev/null 2>&1; then break; fi
    sleep 2
  done
  aws --endpoint-url http://127.0.0.1:8333 s3api create-bucket --bucket "$S3_BUCKET" >/dev/null
  aws --endpoint-url http://127.0.0.1:8333 s3api list-buckets
  S3_IP=$(container_ip seaweedfs)
  log "object store: $S3_IP:8333 for pods, 127.0.0.1:8333 for the runner"
}
s3() { aws --endpoint-url http://127.0.0.1:8333 "$@"; }

# The s3 back end as section 4.7 names it: key terraform.tfstate,
# workspace_key_prefix tofu-state => tofu-state/<external-name>/terraform.tfstate
s3_backend_hcl() { # endpoint
  cat <<EOF
terraform {
  backend "s3" {
    bucket                      = "$S3_BUCKET"
    key                         = "terraform.tfstate"
    workspace_key_prefix        = "tofu-state"
    region                      = "us-east-1"
    endpoints                   = { s3 = "$1" }
    use_path_style              = true
    skip_credentials_validation = true
    skip_region_validation      = true
    skip_requesting_account_id  = true
    skip_metadata_api_check     = true
    skip_s3_checksum            = true
  }
}
EOF
}

# ---------------------------------------------------------------- Crossplane
install_crossplane_v2() {
  helm repo add crossplane-stable https://charts.crossplane.io/stable >/dev/null
  helm repo update >/dev/null
  helm install crossplane crossplane-stable/crossplane --version "$CROSSPLANE_V2" \
    -n crossplane-system --create-namespace --wait --timeout 10m
  kubectl -n crossplane-system get deploy -o wide
  kubectl get managedresourceactivationpolicies -o yaml 2>&1 | head -40 || true
}

provider_yaml() { # package-ref runtime-config-name
  cat <<EOF
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-opentofu
spec:
  package: $1
  runtimeConfigRef:
    name: $2
EOF
}

wait_provider_healthy() { # timeout-seconds
  local t=${1:-600} s=$SECONDS
  while (( SECONDS - s < t )); do
    local h i
    h=$(kubectl get providers.pkg.crossplane.io provider-opentofu -o jsonpath='{.status.conditions[?(@.type=="Healthy")].status}' 2>/dev/null)
    i=$(kubectl get providers.pkg.crossplane.io provider-opentofu -o jsonpath='{.status.conditions[?(@.type=="Installed")].status}' 2>/dev/null)
    if [[ $h == True && $i == True ]]; then
      log "provider Installed=True Healthy=True after $((SECONDS - s))s"
      kubectl get providers.pkg.crossplane.io,providerrevisions.pkg.crossplane.io -o wide
      return 0
    fi
    sleep 5
  done
  log "provider not healthy after ${t}s"
  kubectl get providers.pkg.crossplane.io,providerrevisions.pkg.crossplane.io -o yaml | tail -80
  kubectl -n crossplane-system get pods -o wide
  return 1
}

# Crossplane 2 starts SafeStart providers with inactive managed-resource
# definitions unless an activation policy covers them. Record what the
# default install does; only if the namespaced Workspace kind is absent,
# activate it explicitly (and say so).
ensure_workspace_kinds() {
  local s=$SECONDS
  while (( SECONDS - s < 120 )); do
    kubectl get crd workspaces.opentofu.m.upbound.io >/dev/null 2>&1 && {
      note setup "workspaces.opentofu.m.upbound.io served $((SECONDS - s))s after Healthy, with the chart's default activation policy"
      return 0
    }
    sleep 5
  done
  note setup "workspaces.opentofu.m.upbound.io absent 120s after Healthy; applying an activation policy for *.opentofu.m.upbound.io"
  kubectl get managedresourcedefinitions -o wide 2>&1 | head -10
  kubectl apply -f - <<EOF
apiVersion: apiextensions.crossplane.io/v1alpha1
kind: ManagedResourceActivationPolicy
metadata:
  name: spike-opentofu
spec:
  activate:
    - "*.opentofu.m.upbound.io"
    - "*.opentofu.upbound.io"
EOF
  s=$SECONDS
  while (( SECONDS - s < 120 )); do
    kubectl get crd workspaces.opentofu.m.upbound.io >/dev/null 2>&1 && return 0
    sleep 5
  done
  return 1
}

provider_pod() {
  kubectl -n crossplane-system get pods -l pkg.crossplane.io/provider=provider-opentofu \
    -o go-template='{{range .items}}{{if not .metadata.deletionTimestamp}}{{if eq .status.phase "Running"}}{{.metadata.name}}{{"\n"}}{{end}}{{end}}{{end}}' | head -1
}

# Wait until exactly one provider pod runs, is Ready and (optionally) carries env NAME.
wait_provider_pod() { # [env-name] [timeout]
  local want=${1:-} t=${2:-420} s=$SECONDS
  while (( SECONDS - s < t )); do
    local n total ready envok=yes
    total=$(kubectl -n crossplane-system get pods -l pkg.crossplane.io/provider=provider-opentofu --no-headers 2>/dev/null | wc -l)
    n=$(provider_pod)
    if [[ -n $n && $total -eq 1 ]]; then
      ready=$(kubectl -n crossplane-system get pod "$n" -o jsonpath='{.status.containerStatuses[0].ready}')
      if [[ -n $want ]]; then
        kubectl -n crossplane-system get pod "$n" -o jsonpath='{.spec.containers[0].env[*].name}' | tr ' ' '\n' | grep -qx "$want" || envok=no
      fi
      if [[ $ready == true && $envok == yes ]]; then
        log "provider pod $n ready after $((SECONDS - s))s"
        return 0
      fi
    fi
    sleep 5
  done
  log "provider pod not ready after ${t}s"
  kubectl -n crossplane-system get pods -o wide
  return 1
}

provider_logs() { # [lines]
  local p
  p=$(provider_pod)
  [[ -n $p ]] && kubectl -n crossplane-system logs "$p" --tail="${1:-80}" 2>&1
}

# ---------------------------------------------------------------- Workspaces
ws_get() { # ns name jsonpath   (ns empty => cluster-scoped kind)
  if [[ -n $1 ]]; then kubectl -n "$1" get "$WSR" "$2" -o "jsonpath=$3" 2>/dev/null
  else kubectl get "$WSR" "$2" -o "jsonpath=$3" 2>/dev/null; fi
}
ws_cond() { ws_get "$1" "$2" "{.status.conditions[?(@.type==\"$3\")].status}"; }
ws_msg() { ws_get "$1" "$2" "{.status.conditions[?(@.type==\"Synced\")].message}"; }
ws_out() { ws_get "$1" "$2" "{.status.atProvider.outputs.$3}" | sed -e 's/^"//' -e 's/"$//'; }

WAITED=0
ws_wait_ready() { # ns name timeout
  local t=${3:-300} s=$SECONDS
  while (( SECONDS - s < t )); do
    if [[ $(ws_cond "$1" "$2" Synced) == True && $(ws_cond "$1" "$2" Ready) == True ]]; then
      WAITED=$((SECONDS - s)); return 0
    fi
    sleep 2
  done
  WAITED=$((SECONDS - s)); return 1
}
ws_wait_output() { # ns name key value timeout
  local t=${5:-300} s=$SECONDS
  while (( SECONDS - s < t )); do
    if [[ $(ws_out "$1" "$2" "$3") == "$4" && $(ws_cond "$1" "$2" Synced) == True && $(ws_cond "$1" "$2" Ready) == True ]]; then
      WAITED=$((SECONDS - s)); return 0
    fi
    sleep 1
  done
  WAITED=$((SECONDS - s)); return 1
}
ws_wait_error() { # ns name timeout — wait for Synced=False with a message
  local t=${3:-240} s=$SECONDS
  while (( SECONDS - s < t )); do
    if [[ $(ws_cond "$1" "$2" Synced) == False && -n $(ws_msg "$1" "$2") ]]; then
      WAITED=$((SECONDS - s)); return 0
    fi
    sleep 3
  done
  WAITED=$((SECONDS - s)); return 1
}
SETTLED=timeout
ws_wait_settle() { # ns name timeout — returns when Ready or Synced=False; sets SETTLED
  local t=${3:-240} s=$SECONDS
  SETTLED=timeout
  while (( SECONDS - s < t )); do
    if [[ $(ws_cond "$1" "$2" Synced) == True && $(ws_cond "$1" "$2" Ready) == True ]]; then
      SETTLED=ready; WAITED=$((SECONDS - s)); return 0
    fi
    if [[ $(ws_cond "$1" "$2" Synced) == False && -n $(ws_msg "$1" "$2") ]]; then
      SETTLED=error; WAITED=$((SECONDS - s)); return 0
    fi
    sleep 2
  done
  WAITED=$((SECONDS - s)); return 1
}
ws_dump() { # ns name file
  if [[ -n $1 ]]; then kubectl -n "$1" get "$WSR" "$2" -o yaml > "$3" 2>&1
  else kubectl get "$WSR" "$2" -o yaml > "$3" 2>&1; fi
  echo "--- status of $2:"
  yq '.status' "$3" 2>/dev/null | head -40
}
ws_delete() { # ns name [timeout] — deletes, removes the finalizer if it hangs
  local t=${3:-180}
  if [[ -n $1 ]]; then
    kubectl -n "$1" delete "$WSR" "$2" --wait=true --timeout="${t}s" >/dev/null 2>&1 || {
      log "delete of $2 hung; removing finalizer"
      kubectl -n "$1" patch "$WSR" "$2" --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
    }
  else
    kubectl delete "$WSR" "$2" --wait=true --timeout="${t}s" >/dev/null 2>&1 || {
      log "delete of $2 hung; removing finalizer"
      kubectl patch "$WSR" "$2" --type=merge -p '{"metadata":{"finalizers":[]}}' >/dev/null 2>&1 || true
    }
  fi
}

# Minimal inline module: one terraform_data whose input carries a marker.
tfdata_module() { # canary-text
  cat <<EOF
variable "marker" {
  type    = string
  default = "m0"
}
resource "terraform_data" "unit" {
  input = "\${var.marker}|$1"
}
output "id" {
  value = terraform_data.unit.id
}
output "marker" {
  value = var.marker
}
EOF
}

indent() { sed "s/^/$(printf '%*s' "$1" '')/"; }

summary() {
  echo
  echo "================ CLD-050 $1 — verdicts ================"
  column -t -s $'\t' "$EV/verdicts.tsv" || cat "$EV/verdicts.tsv"
  echo "================ CLD-050 $1 — measured values ================"
  cat "$EV/measures.tsv"
  {
    echo "### CLD-050 $1"
    echo
    echo '| row | verdict | detail |'
    echo '|---|---|---|'
    awk -F'\t' '{printf "| %s | %s | %s |\n", $1, $2, $3}' "$EV/verdicts.tsv"
  } >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
}
