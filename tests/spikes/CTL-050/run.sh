#!/usr/bin/env bash
# Lab test CTL-050: push trigger, batch commit, intent lock and adoption,
# measured on a throwaway single-node k3s inside one CI job, on the versions
# the platform pins (k3s v1.31.4+k3s1, Flux v2.4.0, Gitea chart 10.5.0).
#
# Usage: run.sh <section>
#   cluster | flux | gitea   set-up; a failure fails the step
#   s1 s2 s3a s4 s3b s6      measurements; always exit 0 and print RESULT lines
#   results                  prints every RESULT line again
#
# Every measured value is printed on a line that starts with "RESULT ".
set -o pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO="$(cd "$HERE/../../.." && pwd)"
M="$HERE/manifests"
WORK="${WORK:-$REPO/.spike}"
OUT="$WORK/out"
STATE="$WORK/state.env"
mkdir -p "$OUT" "$WORK/seed"
RES="$OUT/results.txt"
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
# shellcheck disable=SC1090
[ -f "$STATE" ] && . "$STATE"

K3S_VERSION="${K3S_VERSION:-v1.31.4+k3s1}"
FLUX_VERSION="${FLUX_VERSION:-v2.4.0}"
GITEA_CHART_VERSION="${GITEA_CHART_VERSION:-10.5.0}"
FS=flux-system
CTRL_SA=system:serviceaccount:catalyst-system:application-controller

# ---------------------------------------------------------------- helpers
rec()   { echo "RESULT $*" | tee -a "$RES"; }
note()  { echo; echo "=== $*"; }
save()  { printf 'export %s=%q\n' "$1" "$2" >> "$STATE"; export "$1=$2"; }
mask()  { echo "::add-mask::$1"; }
since() { awk -v a="$(date +%s.%N)" -v b="$1" 'BEGIN{printf "%.2f", a-b}'; }
one()   { tr '\n' ' ' | sed 's/  */ /g' | cut -c1-"${1:-300}"; }

# g METHOD PATH [JSON]: Gitea API call as the admin; prints the HTTP code,
# leaves the body in $WORK/g.body.
g() {
  local args=(-sS -m 30 -u "gitea_admin:$GITEA_PASS" -X "$1" -o "$WORK/g.body" -w '%{http_code}')
  if [ -n "${3:-}" ]; then args+=(-H 'Content-Type: application/json' --data "$3"); fi
  curl "${args[@]}" "$GITEA_URL/api/v1$2"
}
gb() { cat "$WORK/g.body"; }

head_sha() { g GET "/repos/$1/branches/main" >/dev/null; jq -r '.commit.id' "$WORK/g.body"; }
blob_sha() { g GET "/repos/$1/contents/$2" >/dev/null; jq -r '.sha' "$WORK/g.body"; }
exists()   { local c; c=$(g GET "/repos/$1/contents/$2"); [ "$c" = 200 ] && echo yes || echo "no (http $c)"; }

# batch_body MESSAGE "op|path|localfile|sha" ...: body of the batch contents call.
batch_body() {
  local msg=$1; shift
  local arr='[]' e op path file sha c
  for e in "$@"; do
    IFS='|' read -r op path file sha <<<"$e"
    c=""; [ -n "$file" ] && c=$(base64 -w0 "$file")
    arr=$(jq -c --arg op "$op" --arg p "$path" --arg c "$c" --arg s "$sha" \
      '. + [{operation:$op, path:$p} + (if $c != "" then {content:$c} else {} end) + (if $s != "" then {sha:$s} else {} end)]' <<<"$arr")
  done
  jq -cn --arg m "$msg" --argjson f "$arr" '{message:$m, branch:"main", files:$f}'
}

# commit_report REPO OLD_HEAD PREFIX: did one call make exactly one commit on top of OLD_HEAD?
commit_report() {
  local h1; h1=$(head_sha "$1")
  rec "$3.head_moved: $([ "$h1" != "$2" ] && echo yes || echo no)"
  g GET "/repos/$1/git/commits/$h1" >/dev/null
  rec "$3.new_commit_parent_is_old_head: $(jq -r --arg o "$2" '[.parents[].sha] | index($o) != null' "$WORK/g.body")"
  rec "$3.files_in_new_commit: $(jq -r '(.files // []) | length' "$WORK/g.body")"
}

# The serialised intent file: no status or server fields, keys sorted,
# two-space YAML, defaults written out.
intent_app() {
  cat <<EOF
apiVersion: apps.openova.io/v1
kind: Application
metadata:
  annotations:
    catalyst.openova.io/change-id: 01JCDZ7K9M3Q4T5V6W7X8Y9Z0A
  labels:
    app.kubernetes.io/managed-by: flux
  name: $1
  namespace: $2
spec:
  blueprintRef:
    name: bp-wordpress
    version: ${3:-1.0.0}
  bootstrap: false
  environmentRef: acme-prod
  isolationLevel: namespace
  regions:
  - me-east-215-a
EOF
}

app_json() {
  jq -cn --arg n "$1" --arg ns "$2" '{apiVersion:"apps.openova.io/v1", kind:"Application",
    metadata:{name:$n, namespace:$ns},
    spec:{blueprintRef:{name:"bp-wordpress", version:"1.0.0"}, environmentRef:"acme-prod", regions:["me-east-215-a"]}}'
}

# try ID EXPECT -- CMD...: run CMD and classify the outcome as
# ok | lock (refused by the intent lock) | rbac | error.
try() {
  local id=$1 expect=$2; shift 3
  local out rc obs
  out=$("$@" 2>&1); rc=$?
  if [ $rc -eq 0 ]; then obs=ok
  elif grep -q "ValidatingAdmissionPolicy 'catalyst-intent-lock'" <<<"$out"; then obs=lock
  elif grep -qiE "cannot|forbidden" <<<"$out"; then obs=rbac
  else obs=error; fi
  sed 's/^/    | /' <<<"$out"
  rec "$id: expect=$expect observed=$obs match=$([ "$obs" = "$expect" ] && echo yes || echo no)"
  if grep -qi '^Warning' <<<"$out"; then rec "$id.warning: $(grep -i '^Warning' <<<"$out" | head -1 | one 260)"; fi
  if [ "$obs" = lock ]; then rec "$id.message: $(grep -o "denied request:.*" <<<"$out" | head -1 | one 260)"; fi
  return 0
}

bind() {
  cat <<EOF | kubectl apply -f -
apiVersion: admissionregistration.k8s.io/v1
kind: ValidatingAdmissionPolicyBinding
metadata:
  name: catalyst-intent-lock-applications
spec:
  policyName: catalyst-intent-lock
  validationActions: $1
  matchResources:
    resourceRules:
      - apiGroups: ["apps.openova.io"]
        apiVersions: ["*"]
        operations: ["CREATE", "UPDATE", "DELETE"]
        resources: ["applications"]
EOF
}

# wait_lock PATTERN: dry-run creates a probe until the answer shows PATTERN.
wait_lock() {
  local i out t0; t0=$(date +%s.%N)
  app_json probe lock > "$WORK/probe.json"
  for i in $(seq 1 90); do
    out=$(kubectl create --dry-run=server -f "$WORK/probe.json" 2>&1)
    if grep -q "$1" <<<"$out"; then echo "binding effective after $(since "$t0")s: $(one 200 <<<"$out")"; return 0; fi
    sleep 1
  done
  echo "binding not effective: $out"; return 1
}

snap_metrics() { kubectl get --raw /metrics | grep '^apiserver_validating_admission_policy_check_total' | sort > "$1" || true; }
delta_metrics() {
  awk 'NR==FNR{a[$1]=$2; next} {b=(($1 in a)?a[$1]:0); d=$2-b; if (d!=0) print $1, d}' "$1" "$2"
}

ks() { # NAME PATH SOURCE TARGET_NAMESPACE [SERVICE_ACCOUNT]
  {
    cat <<EOF
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: $1
  namespace: flux-system
spec:
  interval: 10m
  retryInterval: 1m
  timeout: 3m
  path: $2
  prune: true
  targetNamespace: $4
  sourceRef:
    kind: GitRepository
    name: $3
EOF
    if [ -n "${5:-}" ]; then echo "  serviceAccountName: $5"; fi
  } | kubectl apply -f -
}

# wait_until TIMEOUT CMD...: seconds until CMD succeeds, or "not within Ns".
wait_until() {
  local t=$1; shift
  local t0 end; t0=$(date +%s.%N); end=$(( $(date +%s) + t ))
  while [ "$(date +%s)" -lt "$end" ]; do
    if "$@" >/dev/null 2>&1; then since "$t0"; return 0; fi
    sleep 0.5
  done
  echo "not within ${t}s"; return 1
}
absent() { ! kubectl get "$@" >/dev/null 2>&1; }
jp_is() { [ "$(kubectl get "${@:3}" -o jsonpath="$1" 2>/dev/null)" = "$2" ]; }
rev_has() { [[ "$(kubectl -n $FS get gitrepository "$1" -o jsonpath='{.status.artifact.revision}' 2>/dev/null)" == *"$2"* ]]; }
ks_done() { [ "$(kubectl -n $FS get kustomization "$1" -o jsonpath='{.status.lastHandledReconcileAt}' 2>/dev/null)" = "$2" ]; }
ks_ready() { kubectl -n $FS get kustomization "$1" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status} {.status.conditions[?(@.type=="Ready")].reason}: {.status.conditions[?(@.type=="Ready")].message}' 2>/dev/null; }
ks_not_ready() { [ "$(kubectl -n $FS get kustomization "$1" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)" = False ]; }
nudge() { kubectl -n $FS annotate --overwrite "$1" reconcile.fluxcd.io/requestedAt="$2" >/dev/null; }

obj_report() { # NS NAME PREFIX
  local j; j=$(kubectl -n "$1" get application "$2" -o json --show-managed-fields) || { rec "$3: object missing"; return; }
  rec "$3.generation: $(jq -r .metadata.generation <<<"$j")"
  rec "$3.resourceVersion: $(jq -r .metadata.resourceVersion <<<"$j")"
  rec "$3.managers: $(jq -c '[.metadata.managedFields[] | {manager, operation, ownsSpec: ((.fieldsV1 // {}) | has("f:spec"))}]' <<<"$j")"
  rec "$3.nonFluxSpecManagers: $(jq -c '[.metadata.managedFields[] | select((.fieldsV1 // {}) | has("f:spec")) | select(.manager != "kustomize-controller" and .manager != "helm-controller") | .manager]' <<<"$j")"
  rec "$3.labels: $(jq -c '.metadata.labels' <<<"$j")"
}

live_equals_file() { # NS NAME FILE
  kubectl -n "$1" get application "$2" -o json > "$WORK/live.json"
  sed "s/^  namespace: adopt\$/  namespace: $1/" "$3" | kubectl create --dry-run=client -o json -f - > "$WORK/file.json"
  python3 - "$WORK/live.json" "$WORK/file.json" <<'PY'
import json, sys
live = json.load(open(sys.argv[1])); f = json.load(open(sys.argv[2]))
live.pop("status", None)
for k in ["managedFields", "resourceVersion", "uid", "creationTimestamp", "generation", "selfLink"]:
    live["metadata"].pop(k, None)
if live == f:
    print("yes")
else:
    diff = {k: (live.get(k), f.get(k)) for k in set(live) | set(f) if live.get(k) != f.get(k)}
    print("no " + json.dumps(diff, sort_keys=True)[:400])
PY
}

dump_gitea_db() { # TAG
  local dbp x
  dbp=$(kubectl -n gitea exec "$GITEA_POD" -- sh -c 'find /data / -name gitea.db -type f 2>/dev/null | head -1')
  rec "gitea.db.path: $dbp"
  rm -f "$WORK/gitea.db" "$WORK/gitea.db-wal" "$WORK/gitea.db-shm"
  kubectl -n gitea exec "$GITEA_POD" -- base64 "$dbp" | base64 -d > "$WORK/gitea.db"
  for x in wal shm; do
    if kubectl -n gitea exec "$GITEA_POD" -- test -f "$dbp-$x"; then
      kubectl -n gitea exec "$GITEA_POD" -- base64 "$dbp-$x" | base64 -d > "$WORK/gitea.db-$x"
    fi
  done
  python3 "$HERE/dbdump.py" "$WORK/gitea.db" | tee "$OUT/gitea-hooks-$1.txt"
}

# ---------------------------------------------------------------- set-up
s_cluster() {
  note "k3s $K3S_VERSION"
  sudo mkdir -p /etc/rancher/k3s /var/lib/rancher/k3s/server/logs
  sudo cp "$M/audit-policy.yaml" /etc/rancher/k3s/audit-policy.yaml
  curl -sfL https://get.k3s.io | INSTALL_K3S_VERSION="$K3S_VERSION" \
    INSTALL_K3S_EXEC="server --disable=traefik --disable=metrics-server --write-kubeconfig-mode=644 --kube-apiserver-arg=audit-log-path=/var/lib/rancher/k3s/server/logs/audit.log --kube-apiserver-arg=audit-policy-file=/etc/rancher/k3s/audit-policy.yaml" \
    sh - || return 1
  local i
  for i in $(seq 1 90); do kubectl get nodes >/dev/null 2>&1 && break; sleep 2; done
  kubectl wait --for=condition=Ready node --all --timeout=180s || return 1
  kubectl -n kube-system rollout status deploy/coredns --timeout=180s || return 1
  rec "env.k8s.server: $(kubectl version -o json | jq -r .serverVersion.gitVersion)"
  rec "env.kubectl.client: $(kubectl version --client -o json | jq -r .clientVersion.gitVersion)"
  rec "env.node: $(kubectl get node -o jsonpath='{.items[0].status.nodeInfo.osImage}, kernel {.items[0].status.nodeInfo.kernelVersion}, {.items[0].status.nodeInfo.containerRuntimeVersion}')"
}

s_flux() {
  note "Flux $FLUX_VERSION"
  kubectl apply -f "https://github.com/fluxcd/flux2/releases/download/${FLUX_VERSION}/install.yaml" || return 1
  kubectl -n $FS wait --for=condition=Available deploy --all --timeout=300s || return 1
  local img
  for img in $(kubectl -n $FS get deploy -o jsonpath='{range .items[*]}{.spec.template.spec.containers[0].image}{"\n"}{end}'); do
    rec "env.flux.image: $img"
  done
  kubectl -n $FS get svc webhook-receiver -o wide
  kubectl -n $FS get networkpolicy
  note "platform CRDs"
  local f
  for f in "$REPO"/products/catalyst/chart/crds/*.yaml; do
    kubectl apply --server-side -f "$f" >/dev/null || echo "CRD apply failed: $f"
  done
  kubectl wait --for=condition=Established crd/applications.apps.openova.io --timeout=60s || return 1
  rec "env.crds.openova: $(kubectl get crd -o name | grep -c 'openova.io')"
}

s_gitea() {
  note "Gitea chart $GITEA_CHART_VERSION"
  local pass pod ip i
  pass=$(openssl rand -hex 16); mask "$pass"; save GITEA_PASS "$pass"
  helm repo add gitea-charts https://dl.gitea.com/charts/ >/dev/null && helm repo update >/dev/null
  rec "env.gitea.chart: $(helm search repo gitea-charts/gitea --version "$GITEA_CHART_VERSION" -o json | jq -r '.[0] | "\(.version) appVersion \(.app_version)"')"
  if ! helm install gitea gitea-charts/gitea --version "$GITEA_CHART_VERSION" -n gitea --create-namespace \
      -f "$HERE/gitea-values.yaml" --set "gitea.admin.password=$GITEA_PASS" --wait --timeout 10m; then
    kubectl -n gitea get pods -o wide; kubectl -n gitea describe pods | tail -60; return 1
  fi
  pod=$(kubectl -n gitea get pod -l app.kubernetes.io/name=gitea -o jsonpath='{.items[0].metadata.name}')
  ip=$(kubectl -n gitea get pod "$pod" -o jsonpath='{.status.podIP}')
  save GITEA_POD "$pod"
  save GITEA_URL "http://$ip:3000"
  if ! curl -sf -m 5 "$GITEA_URL/api/v1/version" >/dev/null; then
    echo "pod IP not reachable from the runner; using a port-forward"
    nohup kubectl -n gitea port-forward svc/gitea-http 3000:3000 >"$WORK/pf.log" 2>&1 &
    save GITEA_URL "http://127.0.0.1:3000"
  fi
  for i in $(seq 1 60); do curl -sf -m 5 "$GITEA_URL/api/v1/version" >/dev/null && break; sleep 2; done
  rec "env.gitea.version: $(curl -s "$GITEA_URL/api/v1/version" | jq -r .version)"
  rec "env.gitea.image: $(kubectl -n gitea get pod "$pod" -o jsonpath='{.spec.containers[0].image}')"
  rec "env.gitea.allowed_host_list: $(kubectl -n gitea exec "$pod" -- sh -c 'grep -A3 "^\[webhook\]" /data/gitea/conf/app.ini 2>/dev/null || true' | one 200)"

  note "organisations, repositories and seed files"
  rec "gitea.org.openova.http: $(g POST /orgs '{"username":"openova","visibility":"public"}')"
  rec "gitea.org.acme.http: $(g POST /orgs '{"username":"acme","visibility":"public"}')"
  rec "gitea.repo.sovereign-regions.http: $(g POST /orgs/openova/repos '{"name":"sovereign-regions","auto_init":true,"default_branch":"main","private":false}')"
  rec "gitea.repo.catalyst-tenant.http: $(g POST /orgs/acme/repos '{"name":"catalyst-tenant","auto_init":true,"default_branch":"main","private":false}')"
  local S="$WORK/seed" h0 code
  printf 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: intent-root\n  namespace: flux-system\n' > "$S/intent-root.yaml"
  printf 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fleet-root\n  namespace: fleet\n' > "$S/fleet-root.yaml"
  printf 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: org-intent-root\n  namespace: acme\n' > "$S/keep.yaml"
  intent_app adopted-site adopt > "$S/adopted-site.yaml"
  intent_app fleet-app fleet > "$S/fleet-app.yaml"
  intent_app marketing-site acme > "$S/marketing-site.yaml"
  echo seed > "$S/marker.txt"
  h0=$(head_sha openova/sovereign-regions)
  code=$(g POST /repos/openova/sovereign-regions/contents "$(batch_body 'seed fleet repository' \
    "create|clusters/spike.local/intent/intent-root.yaml|$S/intent-root.yaml|" \
    "create|adopt/adopted-site.yaml|$S/adopted-site.yaml|" \
    "create|fleet-writer/fleet-app.yaml|$S/fleet-app.yaml|" \
    "create|fleet-writer/fleet-root.yaml|$S/fleet-root.yaml|" \
    "create|s1/marker.txt|$S/marker.txt|")")
  rec "S2.control.five_creates.http: $code"
  [ "$code" = 201 ] || gb
  commit_report openova/sovereign-regions "$h0" S2.control.five_creates
  h0=$(head_sha acme/catalyst-tenant)
  code=$(g POST /repos/acme/catalyst-tenant/contents "$(batch_body 'seed organization intent' \
    "create|intent/applications/marketing-site.yaml|$S/marketing-site.yaml|" \
    "create|intent/.keep.yaml|$S/keep.yaml|")")
  rec "gitea.seed.catalyst-tenant.http: $code"
  [ "$code" = 201 ] || return 1
}

# ---------------------------------------------------------------- S1
push_measure() { # LABEL; returns 0 when the fleet source got the new revision
  local label=$1 t0 sha n r d2
  if ! (cd "$WORK/clone" && echo "push $label $(date -u +%FT%TZ)" >> s1/marker.txt && git add -A \
        && git -c user.name=spike -c user.email=spike@spike.invalid commit -qm "S1 push $label" && git push -q origin main); then
    rec "S1.$label.git_push: failed"; return 1
  fi
  t0=$(date +%s.%N)
  sha=$(git -C "$WORK/clone" rev-parse HEAD)
  rec "S1.$label.git_push: ok $sha"
  declare -A seen=()
  local deadline=$(( $(date +%s) + 60 ))
  while [ "$(date +%s)" -lt "$deadline" ]; do
    while read -r n r; do
      if [ -z "${seen[$n]:-}" ] && [[ "$r" == *"$sha"* ]]; then seen[$n]=$(since "$t0"); fi
    done < <(kubectl -n $FS get gitrepository -o json | jq -r '.items[] | "\(.metadata.name) \(.status.artifact.revision // "-")"')
    if [ -n "${seen[sovereign-region]:-}" ] && [ -n "${seen[s1-per-org]:-}" ] && [ -n "${seen[s1-catalog]:-}" ]; then break; fi
    sleep 0.25
  done
  if [ -n "${seen[sovereign-region]:-}" ] && [ -z "${seen[s1-unselected]:-}" ]; then
    d2=$(( $(date +%s) + 15 ))
    while [ "$(date +%s)" -lt "$d2" ]; do
      if rev_has s1-unselected "$sha"; then seen[s1-unselected]=$(since "$t0"); break; fi
      sleep 1
    done
  fi
  for n in sovereign-region s1-per-org s1-catalog s1-unselected; do
    rec "S1.$label.seconds_push_to_revision.$n: ${seen[$n]:-not within window}"
  done
  rec "S1.$label.unselected.requestedAt: $(kubectl -n $FS get gitrepository s1-unselected -o jsonpath='{.metadata.annotations.reconcile\.fluxcd\.io/requestedAt}' | sed 's/^$/absent/')"
  rec "S1.$label.sovereign-region.requestedAt: $(kubectl -n $FS get gitrepository sovereign-region -o jsonpath='{.metadata.annotations.reconcile\.fluxcd\.io/requestedAt}' | sed 's/^$/absent/')"
  [ -n "${seen[sovereign-region]:-}" ]
}

s_s1() {
  note "S1 Receiver and Gitea webhook"
  local tok wp body code
  tok=$(openssl rand -hex 20); mask "$tok"; save HOOK_TOKEN "$tok"
  kubectl -n $FS create secret generic gitea-webhook-token --from-literal=token="$HOOK_TOKEN"
  kubectl apply -f "$M/s1-sources.yaml"
  kubectl -n $FS wait gitrepository/sovereign-region gitrepository/s1-per-org gitrepository/s1-catalog gitrepository/s1-unselected \
    --for=condition=Ready --timeout=180s
  kubectl apply -f "$M/receiver.yaml"
  kubectl -n $FS wait receiver/sovereign-gitea --for=condition=Ready --timeout=120s
  rec "S1.receiver.ready: $(kubectl -n $FS get receiver sovereign-gitea -o jsonpath='{.status.conditions[?(@.type=="Ready")].status} {.status.conditions[?(@.type=="Ready")].message}')"
  wp=$(kubectl -n $FS get receiver sovereign-gitea -o jsonpath='{.status.webhookPath}')
  save HOOK_URL "http://webhook-receiver.flux-system.svc.cluster.local$wp"
  rm -rf "$WORK/clone"
  git clone -q "http://gitea_admin:$GITEA_PASS@${GITEA_URL#http://}/openova/sovereign-regions.git" "$WORK/clone"

  note "S1 variant A: POST /api/v1/admin/hooks with the planned fields"
  body=$(jq -cn --arg u "$HOOK_URL" --arg s "$HOOK_TOKEN" \
    '{type:"gitea", active:true, events:["push"], config:{url:$u, content_type:"json", secret:$s}}')
  code=$(g POST /admin/hooks "$body")
  rec "S1.A.admin_hook_create.http: $code"
  rec "S1.A.admin_hook_create.response: $(jq -c '{id, type, active, events, config: (.config | del(.secret))}' "$WORK/g.body" 2>/dev/null || gb | one)"
  code=$(g GET /admin/hooks)
  rec "S1.admin_hook_list.http: $code $(jq -c '[.[] | {id, type, active, events}]' "$WORK/g.body" 2>/dev/null || gb | one)"
  g POST /orgs/openova/repos '{"name":"after-hook","auto_init":true,"default_branch":"main"}' >/dev/null
  g GET /repos/openova/after-hook/hooks >/dev/null
  rec "S1.A.hooks_copied_into_repo_created_after: $(jq -r 'length' "$WORK/g.body")"
  g GET /repos/openova/sovereign-regions/hooks >/dev/null
  rec "S1.A.hooks_on_existing_repo: $(jq -r 'length' "$WORK/g.body")"

  if push_measure A; then
    save S1_WORKING A
  else
    note "S1 variant B: the same call with config.is_system_webhook=true"
    body=$(jq -cn --arg u "$HOOK_URL" --arg s "$HOOK_TOKEN" \
      '{type:"gitea", active:true, events:["push"], config:{url:$u, content_type:"json", secret:$s, is_system_webhook:"true"}}')
    code=$(g POST /admin/hooks "$body")
    rec "S1.B.admin_hook_create.http: $code"
    rec "S1.B.admin_hook_create.response: $(jq -c '{id, type, active, events, config: (.config | del(.secret))}' "$WORK/g.body" 2>/dev/null || gb | one)"
    save HOOK_B_ID "$(jq -r .id "$WORK/g.body")"
    code=$(g GET /admin/hooks)
    rec "S1.B.admin_hook_list.http: $code $(jq -c '[.[] | {id, type, active, events}]' "$WORK/g.body" 2>/dev/null || gb | one)"
    if push_measure B; then
      save S1_WORKING B
      note "S1 fall-back measured too: system hook set inactive with PATCH, organisation webhook instead"
      code=$(g PATCH "/admin/hooks/$HOOK_B_ID" '{"active":false}')
      rec "S1.B.admin_hook_patch_inactive.http: $code active=$(jq -r .active "$WORK/g.body" 2>/dev/null)"
      body=$(jq -cn --arg u "$HOOK_URL" --arg s "$HOOK_TOKEN" \
        '{type:"gitea", active:true, events:["push"], config:{url:$u, content_type:"json", secret:$s}}')
      code=$(g POST /orgs/openova/hooks "$body")
      rec "S1.C.org_hook_create.http: $code"
      if push_measure C; then rec "S1.C.org_hook_triggers: yes"; else rec "S1.C.org_hook_triggers: no"; fi
    else
      note "S1 variant C: organisation webhook POST /api/v1/orgs/openova/hooks"
      body=$(jq -cn --arg u "$HOOK_URL" --arg s "$HOOK_TOKEN" \
        '{type:"gitea", active:true, events:["push"], config:{url:$u, content_type:"json", secret:$s}}')
      code=$(g POST /orgs/openova/hooks "$body")
      rec "S1.C.org_hook_create.http: $code"
      if push_measure C; then save S1_WORKING C; else save S1_WORKING none; fi
    fi
  fi
  rec "S1.working_variant: $S1_WORKING"

  note "S1 what Gitea stored and sent"
  sleep 3
  dump_gitea_db s1
  grep '^WEBHOOK ' "$OUT/gitea-hooks-s1.txt" | sed 's/^WEBHOOK //' | jq -c '{id, repo_id, owner_id, is_system_webhook, type, is_active, events}' 2>/dev/null \
    | while read -r l; do rec "S1.webhook_row: $l"; done
  grep '^HOOKTASK ' "$OUT/gitea-hooks-s1.txt" | sed 's/^HOOKTASK //' | jq -c '{id, hook_id, event_type, is_delivered, is_succeed, response_status, response_body}' 2>/dev/null \
    | while read -r l; do rec "S1.delivery: $l"; done
  rec "S1.request_headers_first_delivery: $(grep '^HOOKTASK ' "$OUT/gitea-hooks-s1.txt" | head -1 | sed 's/^HOOKTASK //' | jq -c '.request_headers | with_entries(if (.key|test("Signature|signature")) then .value="<hmac>" else . end)' 2>/dev/null || echo none)"
  kubectl -n $FS logs deploy/notification-controller --tail=300 | grep -iE 'receiver|webhook|handl|annotat|signature|resource' | tail -30 | tee "$OUT/notification-controller.log"
  kubectl -n $FS get gitrepository -o custom-columns=NAME:.metadata.name,REVISION:.status.artifact.revision,REQUESTED:.metadata.annotations.reconcile\\.fluxcd\\.io/requestedAt
  return 0
}

# ---------------------------------------------------------------- S2
s_s2() {
  note "S2 batch contents call"
  local R=openova/sovereign-regions P=clusters/spike.local/intent/intent-root.yaml S="$WORK/seed"
  local WRONG=1111111111111111111111111111111111111111 cur h0 code msg
  cur=$(blob_sha $R $P)
  rec "S2.current_blob_sha_of_updated_file: $cur"
  echo "a: 1" > "$S/a.yaml"; echo "b: 1" > "$S/b.yaml"; echo "c: 1" > "$S/c.yaml"; echo "d: 1" > "$S/d.yaml"
  printf 'apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: intent-root\n  namespace: flux-system\ndata:\n  touched: "yes"\n' > "$S/intent-root-2.yaml"

  h0=$(head_sha $R)
  code=$(g POST /repos/$R/contents "$(batch_body 'S2 v1' "create|s2/a.yaml|$S/a.yaml|" "create|s2/b.yaml|$S/b.yaml|" "update|$P|$S/intent-root-2.yaml|$WRONG")")
  msg=$(jq -r '.message // empty' "$WORK/g.body" 2>/dev/null || gb); [ -z "$msg" ] && msg=$(gb)
  rec "S2.v1.two_creates_then_wrong_sha_update.http: $code"
  rec "S2.v1.message: $(one 300 <<<"$msg")"
  rec "S2.v1.message_names_the_file: $(grep -q 'intent-root' <<<"$msg" && echo yes || echo no)"
  rec "S2.v1.message_names_both_shas: $(grep -q "$WRONG" <<<"$msg" && grep -q "$cur" <<<"$msg" && echo yes || echo no)"
  rec "S2.v1.head_unchanged: $([ "$(head_sha $R)" = "$h0" ] && echo yes || echo no)"
  rec "S2.v1.create_a_applied: $(exists $R s2/a.yaml)"
  rec "S2.v1.create_b_applied: $(exists $R s2/b.yaml)"

  h0=$(head_sha $R)
  code=$(g POST /repos/$R/contents "$(batch_body 'S2 v2' "update|$P|$S/intent-root-2.yaml|$WRONG" "create|s2/c.yaml|$S/c.yaml|" "create|s2/d.yaml|$S/d.yaml|")")
  msg=$(jq -r '.message // empty' "$WORK/g.body" 2>/dev/null); [ -z "$msg" ] && msg=$(gb)
  rec "S2.v2.wrong_sha_update_then_two_creates.http: $code"
  rec "S2.v2.message: $(one 300 <<<"$msg")"
  rec "S2.v2.head_unchanged: $([ "$(head_sha $R)" = "$h0" ] && echo yes || echo no)"
  rec "S2.v2.create_c_applied: $(exists $R s2/c.yaml)"
  rec "S2.v2.create_d_applied: $(exists $R s2/d.yaml)"

  h0=$(head_sha $R)
  code=$(g POST /repos/$R/contents "$(batch_body 'S2 v3' "create|s2/a.yaml|$S/a.yaml|" "create|$P|$S/intent-root-2.yaml|")")
  msg=$(jq -r '.message // empty' "$WORK/g.body" 2>/dev/null); [ -z "$msg" ] && msg=$(gb)
  rec "S2.v3.create_plus_create_on_existing_path.http: $code"
  rec "S2.v3.message: $(one 300 <<<"$msg")"
  rec "S2.v3.head_unchanged: $([ "$(head_sha $R)" = "$h0" ] && echo yes || echo no)"
  rec "S2.v3.create_a_applied: $(exists $R s2/a.yaml)"

  h0=$(head_sha $R)
  code=$(g POST /repos/$R/contents "$(batch_body 'S2 v5' "create|s2/a.yaml|$S/a.yaml|" "update|s1/marker.txt|$S/c.yaml|$WRONG" "update|$P|$S/intent-root-2.yaml|$WRONG")")
  msg=$(jq -r '.message // empty' "$WORK/g.body" 2>/dev/null); [ -z "$msg" ] && msg=$(gb)
  rec "S2.v5.create_then_two_wrong_sha_updates.http: $code"
  rec "S2.v5.message: $(one 300 <<<"$msg")"
  rec "S2.v5.mismatches_reported: $(grep -o 'sha does not match' <<<"$msg" | wc -l)"
  rec "S2.v5.head_unchanged: $([ "$(head_sha $R)" = "$h0" ] && echo yes || echo no)"
  rec "S2.v5.create_a_applied: $(exists $R s2/a.yaml)"

  h0=$(head_sha $R)
  code=$(g POST /repos/$R/contents "$(batch_body 'S2 v4' "create|s2/a.yaml|$S/a.yaml|" "create|s2/b.yaml|$S/b.yaml|" "update|$P|$S/intent-root-2.yaml|$cur")")
  rec "S2.v4.control_correct_sha.http: $code"
  rec "S2.v4.response_files: $(jq -c '[.files[]? | .path]' "$WORK/g.body" 2>/dev/null) commit $(jq -r '.commit.sha // empty' "$WORK/g.body" 2>/dev/null)"
  commit_report $R "$h0" S2.v4
  rec "S2.v4.a_applied: $(exists $R s2/a.yaml)"
  rec "S2.v4.b_applied: $(exists $R s2/b.yaml)"
  return 0
}

# ---------------------------------------------------------------- S3, audit stage
# apply_policy: the planned policy first; when the API server refuses it, the
# smallest amendments of the one refused expression, in order. Each attempt is
# a server-side dry run, so the first that compiles is the one applied.
apply_policy() {
  local base="$WORK/policy.json" v expr out
  kubectl create --dry-run=client -o json -f "$M/lock-policy.yaml" > "$base"
  local orig; orig=$(jq -r '.spec.auditAnnotations[0].valueExpression' "$base")
  local reason="variables.target.metadata.annotations['catalyst.openova.io/break-glass-reason']"
  local -a names=(as-planned dyn-true-branch dyn-null-branch no-audit-annotation)
  local -a exprs=(
    "$orig"
    "variables.isBreakGlass && !variables.isWriter ? dyn(variables.user + ': ' + $reason) : null"
    "variables.isBreakGlass && !variables.isWriter ? variables.user + ': ' + $reason : dyn(null)"
    ""
  )
  for v in 0 1 2 3; do
    if [ -n "${exprs[$v]}" ]; then
      jq --arg e "${exprs[$v]}" '.spec.auditAnnotations[0].valueExpression = $e' "$base" > "$WORK/policy-$v.json"
    else
      jq 'del(.spec.auditAnnotations)' "$base" > "$WORK/policy-$v.json"
    fi
    out=$(kubectl apply --dry-run=server -f "$WORK/policy-$v.json" 2>&1)
    if [ $? -eq 0 ]; then
      rec "S3.policy.compile.${names[$v]}: accepted"
      [ -n "${exprs[$v]}" ] && rec "S3.policy.compile.${names[$v]}.expression: ${exprs[$v]}"
      kubectl apply -f "$WORK/policy-$v.json"
      rec "S3.policy.applied_variant: ${names[$v]}"
      return 0
    fi
    rec "S3.policy.compile.${names[$v]}: refused $(grep -o 'compilation failed.*' <<<"$out" | head -1 | one 200)"
  done
  rec "S3.policy.applied_variant: none"
  return 1
}

s_s3a() {
  note "S3 lock with the binding at Audit,Warn"
  kubectl apply -f "$M/rbac.yaml"
  apply_policy
  bind '["Audit","Warn"]'
  kubectl create ns lock
  wait_lock "Validation failed for ValidatingAdmissionPolicy" || true
  rec "S3.policy.status: $(kubectl get validatingadmissionpolicy catalyst-intent-lock -o json | jq -c '.status' | one 600)"
  snap_metrics "$WORK/m0"
  app_json lock-a lock > "$WORK/lock-a.json"
  try S3.audit.cluster_admin_create ok -- kubectl create -f "$WORK/lock-a.json"
  snap_metrics "$WORK/m1"
  rec "S3.audit.metric_lines_present: $(wc -l < "$WORK/m1")"
  rec "S3.metric_names_exposed: $(kubectl get --raw /metrics | grep -o '^[a-z_]*validating_admission_polic[a-z_]*' | sort -u | tr '\n' ' ')"
  delta_metrics "$WORK/m0" "$WORK/m1" | while read -r l v; do rec "S3.audit.metric_delta: $l +$v"; done
  sudo grep -c 'validation.policy.admission.k8s.io/validation_failure' /var/lib/rancher/k3s/server/logs/audit.log \
    | while read -r n; do rec "S3.audit.audit_log_validation_failure_annotations: $n"; done

  note "garbage-collection fixture, made while the stage is audit"
  local uid
  kubectl -n lock create configmap gc-owner
  uid=$(kubectl -n lock get configmap gc-owner -o jsonpath='{.metadata.uid}')
  app_json gc-child lock | jq --arg u "$uid" '.metadata.ownerReferences=[{apiVersion:"v1",kind:"ConfigMap",name:"gc-owner",uid:$u}]' > "$WORK/gc-child.json"
  kubectl create -f "$WORK/gc-child.json" 2>&1 | tail -2
  return 0
}

# ---------------------------------------------------------------- S4
adopt_case() { # NS KUSTOMIZATION TAG
  local ns=$1 k=$2 tag=$3 f="$WORK/seed/adopted-site.yaml" t0 stamp
  kubectl create ns "$ns"
  sed "s/^  namespace: adopt\$/  namespace: $ns/" "$f" | kubectl create --field-manager=catalyst-api -f - 2>&1 | tail -2
  obj_report "$ns" adopted-site "S4.$tag.before"
  rec "S4.$tag.before.live_equals_file: $(live_equals_file "$ns" adopted-site "$f")"
  t0=$(date +%s.%N)
  ks "$k" ./adopt fleet-src "$ns"
  kubectl -n $FS wait kustomization/"$k" --for=condition=Ready --timeout=180s >/dev/null
  rec "S4.$tag.kustomization: $(ks_ready "$k" | one 200) after $(since "$t0")s"
  obj_report "$ns" adopted-site "S4.$tag.after"
  rec "S4.$tag.after.live_equals_file: $(live_equals_file "$ns" adopted-site "$f")"
  rec "S4.$tag.flux_events: $(kubectl -n $FS get events --field-selector involvedObject.name="$k" -o json | jq -r '[.items[].message] | join(" | ")' | one 400)"
  stamp="s4-$tag-$(date +%s)"
  nudge kustomization/"$k" "$stamp"
  wait_until 120 ks_done "$k" "$stamp" >/dev/null
  obj_report "$ns" adopted-site "S4.$tag.after_second_reconcile"
}

s_s4() {
  note "S4 adoption of an existing object by a byte-equal file"
  kubectl apply -f "$M/kustomizations.yaml"
  kubectl -n $FS wait gitrepository/fleet-src gitrepository/catalyst-tenant-acme --for=condition=Ready --timeout=180s
  adopt_case adopt s4-adopt default
  kubectl -n $FS logs deploy/kustomize-controller --tail=400 | grep -E 's4-adopt|adopted-site' | tail -8 | tee "$OUT/kustomize-controller-s4.log"

  note "S4 variant: kustomize-controller started with --override-manager=catalyst-api"
  local orig
  orig=$(kubectl -n $FS get deploy kustomize-controller -o json | jq -c '.spec.template.spec.containers[0].args')
  kubectl -n $FS patch deploy kustomize-controller --type=json \
    -p '[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--override-manager=catalyst-api"}]'
  kubectl -n $FS rollout status deploy/kustomize-controller --timeout=180s
  adopt_case adopt2 s4-adopt2 override
  kubectl -n $FS patch deploy kustomize-controller --type=json \
    -p "[{\"op\":\"replace\",\"path\":\"/spec/template/spec/containers/0/args\",\"value\":$orig}]"
  kubectl -n $FS rollout status deploy/kustomize-controller --timeout=180s
  return 0
}

# ---------------------------------------------------------------- S3, deny stage
s_s3b() {
  note "S3 lock with the binding at Deny"
  bind '["Deny"]'
  wait_lock "denied request" || true
  snap_metrics "$WORK/m2"
  app_json lock-b lock > "$WORK/lock-b.json"
  try S3.deny.cluster_admin_create lock -- kubectl create -f "$WORK/lock-b.json"
  snap_metrics "$WORK/m3"
  delta_metrics "$WORK/m2" "$WORK/m3" | while read -r l v; do rec "S3.deny.metric_delta: $l +$v"; done

  note "controller account on an existing object"
  try S3.deny.controller_status_update ok -- kubectl --as=$CTRL_SA -n lock patch application lock-a --subresource=status --type=merge -p '{"status":{"phase":"Ready"}}'
  rec "S3.deny.status_phase_now: $(kubectl -n lock get application lock-a -o jsonpath='{.status.phase}')"
  try S3.deny.controller_add_finalizer ok -- kubectl --as=$CTRL_SA -n lock patch application lock-a --type=merge -p '{"metadata":{"finalizers":["catalyst.openova.io/spike"]}}'
  try S3.deny.controller_spec_change lock -- kubectl --as=$CTRL_SA -n lock patch application lock-a --type=merge -p '{"spec":{"blueprintRef":{"name":"bp-wordpress","version":"1.0.1"}}}'
  try S3.deny.cluster_admin_reconcile_annotation ok -- kubectl -n lock annotate application lock-a --overwrite reconcile.fluxcd.io/requestedAt=spike
  try S3.deny.cluster_admin_label ok -- kubectl -n lock label application lock-a --overwrite spike=yes
  try S3.deny.cluster_admin_delete lock -- kubectl -n lock delete application lock-a --wait=false

  note "Flux writers: impersonated organisation account, control account, default identity"
  kubectl apply -f "$M/tenant.yaml"
  kubectl create ns fleet
  ks catalyst-tenant-acme-intent ./intent catalyst-tenant-acme acme org-intent-acme
  ks tenant-beta-intent ./intent catalyst-tenant-acme beta tenant-beta
  ks fleet-writer ./fleet-writer fleet-src fleet
  rec "S3.flux.org-intent-acme.create_application: $(wait_until 180 kubectl -n acme get application marketing-site)"
  rec "S3.flux.org-intent-acme.kustomization: $(ks_ready catalyst-tenant-acme-intent | one 260)"
  rec "S3.flux.org-intent-acme.configmap_org-intent-root: $(kubectl -n acme get configmap org-intent-root -o name 2>&1 | one 120)"
  wait_until 180 ks_not_ready tenant-beta-intent >/dev/null
  rec "S3.flux.tenant-beta.kustomization: $(ks_ready tenant-beta-intent | one 300)"
  rec "S3.flux.tenant-beta.application_created: $(kubectl -n beta get application marketing-site -o name 2>/dev/null | grep -q . && echo yes || echo no)"
  rec "S3.flux.kustomize-controller.create_application: $(wait_until 180 kubectl -n fleet get application fleet-app)"
  rec "S3.flux.fleet-writer.kustomization: $(ks_ready fleet-writer | one 200)"

  note "Flux update through the impersonated account"
  local A=acme/catalyst-tenant AP=intent/applications/marketing-site.yaml sha code h stamp
  intent_app marketing-site acme 1.0.1 > "$WORK/seed/marketing-site-2.yaml"
  sha=$(blob_sha $A $AP)
  code=$(g POST /repos/$A/contents "$(batch_body 'bump version' "update|$AP|$WORK/seed/marketing-site-2.yaml|$sha")")
  rec "S3.flux.org-intent-acme.git_update.http: $code"
  h=$(head_sha $A); stamp="up-$(date +%s)"
  nudge gitrepository/catalyst-tenant-acme "$stamp"; wait_until 90 rev_has catalyst-tenant-acme "$h" >/dev/null
  nudge kustomization/catalyst-tenant-acme-intent "$stamp"
  rec "S3.flux.org-intent-acme.update_spec: $(wait_until 120 jp_is '{.spec.blueprintRef.version}' 1.0.1 -n acme application marketing-site)"

  note "Flux prune under Deny"
  local R=openova/sovereign-regions FP=fleet-writer/fleet-app.yaml
  sha=$(blob_sha $R $FP)
  code=$(g POST /repos/$R/contents "$(batch_body 'remove fleet-app' "delete|$FP||$sha")")
  rec "S3.flux.prune.git_delete.http: $code"
  h=$(head_sha $R); stamp="pr-$(date +%s)"
  nudge gitrepository/fleet-src "$stamp"; wait_until 90 rev_has fleet-src "$h" >/dev/null
  nudge kustomization/fleet-writer "$stamp"
  rec "S3.flux.kustomize-controller.prune_delete: $(wait_until 120 absent -n fleet application fleet-app)"

  note "garbage-collection delete"
  kubectl -n lock delete configmap gc-owner
  rec "S3.deny.gc_delete_by_owner_reference: $(wait_until 120 absent -n lock application gc-child)"

  note "break-glass"
  kubectl create ns bg
  app_json bg-1 bg | jq '.metadata.annotations={"catalyst.openova.io/break-glass-reason":"restore service after incident 42"}' > "$WORK/bg-1.json"
  app_json bg-2 bg | jq '.metadata.annotations={"catalyst.openova.io/break-glass-reason":"short"}' > "$WORK/bg-2.json"
  app_json bg-3 bg | jq '.metadata.annotations={"catalyst.openova.io/break-glass-reason":"restore service after incident 42"}' > "$WORK/bg-3.json"
  app_json bg-4 bg > "$WORK/bg-4.json"
  try S3.deny.break_glass_group_and_reason ok -- kubectl --as=alice --as-group=catalyst:break-glass create -f "$WORK/bg-1.json"
  try S3.deny.break_glass_group_reason_too_short lock -- kubectl --as=alice --as-group=catalyst:break-glass create -f "$WORK/bg-2.json"
  try S3.deny.break_glass_reason_without_group lock -- kubectl --as=alice create -f "$WORK/bg-3.json"
  try S3.deny.break_glass_group_without_reason lock -- kubectl --as=alice --as-group=catalyst:break-glass create -f "$WORK/bg-4.json"

  note "namespace delete"
  try S3.deny.controller_remove_finalizer ok -- kubectl --as=$CTRL_SA -n lock patch application lock-a --type=json -p '[{"op":"remove","path":"/metadata/finalizers"}]'
  kubectl delete ns lock --wait=false
  rec "S3.deny.namespace_delete_removes_object: $(wait_until 180 absent -n lock application lock-a)"
  rec "S3.deny.namespace_gone: $(wait_until 180 absent namespace lock)"

  note "audit log"
  sudo cp /var/lib/rancher/k3s/server/logs/audit.log "$OUT/audit.jsonl"; sudo chmod 644 "$OUT/audit.jsonl"
  jq -c 'select(.stage=="ResponseComplete" and (.verb=="delete" or .verb=="deletecollection") and .objectRef.resource=="applications")
         | {user:.user.username, verb, ns:.objectRef.namespace, name:.objectRef.name, code:.responseStatus.code}' "$OUT/audit.jsonl" \
    | sort | uniq -c | while read -r l; do rec "S3.audit_log.application_delete: $l"; done
  jq -c 'select(.stage=="ResponseComplete" and .annotations != null) | select([.annotations | keys[] | select(test("break-glass"))] | length > 0)
         | {user:.user.username, verb, name:.objectRef.name, code:.responseStatus.code, annotations:(.annotations | with_entries(select(.key|test("break-glass"))))}' "$OUT/audit.jsonl" \
    | while read -r l; do rec "S3.audit_log.break_glass: $l"; done
  rec "S3.audit_log.validation_failure_annotations_total: $(grep -c 'validation.policy.admission.k8s.io/validation_failure' "$OUT/audit.jsonl")"
  snap_metrics "$WORK/m4"
  sed 's/^/    /' "$WORK/m4"
  return 0
}

# ---------------------------------------------------------------- S6
s_s6() {
  note "S6 what creates Environment objects (read of the repository only)"
  cd "$REPO" || return 0
  rec "S6.source_commit: $(git rev-parse HEAD)"
  rec "S6.planned_search.matches: $(git grep -n 'environments' -- '*.go' | grep -ciE 'Create\(|kind: Environment')"
  echo "-- Go code that builds an object of kind Environment (tests excluded)"
  git grep -n -E 'SetKind\("Environment"\)|Kind: +"Environment"' -- '*.go' ':!*_test.go' | tee "$OUT/s6-kind.txt"
  echo "-- manifests of kind Environment"
  git grep -n -E '^kind: Environment$' -- '*.yaml' '*.yml' '*.tpl' | tee "$OUT/s6-manifests.txt"
  rec "S6.creator.organization-controller: $(grep -n 'r.Create(ctx, desired)' core/controllers/organization/internal/controller/environment_ensure.go | sed 's/^/environment_ensure.go:/' | one 160); called from $(grep -n 'r.ensureEnvironment(ctx' core/controllers/organization/internal/controller/organization_controller.go | cut -d: -f1 | sed 's/^/organization_controller.go:/')"
  rec "S6.creator.catalyst-api: $(grep -n 'applySpineClusterResource(dyn, EnvironmentGVR()' products/catalyst/bootstrap/api/internal/handler/post_handover_spine_apps.go | sed 's/^/post_handover_spine_apps.go:/' | one 160); started from $(git grep -n 'runPostHandoverSpineApplications(dep)' -- 'products/catalyst/bootstrap/api/internal/handler/*.go' ':!*_test.go' | cut -d: -f1,2 | sed 's#.*/##' | tr '\n' ' ')"
  rec "S6.manifests_outside_crd_tests: $(grep -v '/crds/tests/' "$OUT/s6-manifests.txt" | cut -d: -f1 | sort -u | tr '\n' ' ')"
  return 0
}

s_results() {
  note "all results"
  cat "$RES"
  { echo '```'; cat "$RES"; echo '```'; } >> "${GITHUB_STEP_SUMMARY:-/dev/null}"
}

case "${1:-}" in
  cluster) s_cluster ;;
  flux)    s_flux ;;
  gitea)   s_gitea ;;
  s1)      s_s1 ;;
  s2)      s_s2 ;;
  s3a)     s_s3a ;;
  s4)      s_s4 ;;
  s3b)     s_s3b ;;
  s6)      s_s6 ;;
  results) s_results ;;
  *) echo "usage: $0 cluster|flux|gitea|s1|s2|s3a|s4|s3b|s6|results"; exit 2 ;;
esac
