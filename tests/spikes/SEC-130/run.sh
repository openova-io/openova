#!/usr/bin/env bash
# Lab test SEC-130 (spike branch only, never merged).
#
# OpenBao 2.7.2 on the upstream chart 0.30.7 with a static seal read from a
# node directory, self-initialisation and a declared audit device; the
# upgrade and seal-migration path from the release today's bp-openbao runs;
# the reconciler, a templated policy and the External Secrets peer store
# against the new server. Everything runs on a throwaway kind cluster inside
# the CI job. No Service of type NodePort is created; HTTP probes from the
# runner go through `kubectl port-forward` to the pod.
#
#   run.sh setup|step1|step2|step3|step4|step5|step6|results
#
# Every step appends verdict lines to .spike/out/verdicts.txt and measured
# values to .spike/out/measured.txt. Unseal keys, root tokens and seal keys of
# the throwaway cluster stay under $RUNNER_TEMP and are never uploaded.
set -uo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/../../.." && pwd)"
OUT="$ROOT/.spike/out"
SEC="${RUNNER_TEMP:-/tmp}/sec130"
mkdir -p "$OUT" "$SEC"
CLUSTER=sec130
NODE=${CLUSTER}-control-plane
CHART_OLD=0.16.0
CHART_NEW=0.30.7
BAO_NEW=2.7.2
ESO_CHART=0.10.7
STEPWISE="2.1.1 2.2.2 2.3.2 2.4.4 2.5.5 2.6.5 2.7.2"
BODY="$SEC/body.json"
K=kubectl

log() { printf '[%s] %s\n' "$(date -u +%H:%M:%S)" "$*"; }
note() { printf '%s\n' "$*" | tee -a "$OUT/measured.txt"; }
verdict() { printf '%-6s %-4s %s\n' "$1" "$2" "$3" | tee -a "$OUT/verdicts.txt"; }
now() { date +%s.%N; }
dt() { python3 -c "import sys; print(f'{float(sys.argv[2])-float(sys.argv[1]):.2f}')" "$1" "$2"; }

# ── OpenBao helpers ──────────────────────────────────────────────────────
kb() { local ns=$1; shift; $K exec -n "$ns" openbao-0 -c openbao -- "$@"; }
status_json() { kb "$1" bao status -format=json 2>/dev/null; }
sealed() { status_json "$1" | jq -r '.sealed' 2>/dev/null; }
port_of() { case "$1" in openbao) echo 18200 ;; bao-b) echo 18201 ;; legacy21) echo 18210 ;; legacy23) echo 18211 ;; edgekey) echo 18220 ;; *) echo 18230 ;; esac; }
health_ok() { curl -s -o /dev/null --max-time 3 "http://127.0.0.1:$1/v1/sys/health?sealedcode=200&uninitcode=200&standbyok=true"; }
pf() { # NS — (re)establish a port-forward to pod openbao-0 of NS
  local ns=$1 p; p=$(port_of "$1")
  health_ok "$p" && return 0
  pkill -f "port-forward -n $ns pod/openbao-0" 2>/dev/null; sleep 0.5
  nohup $K port-forward -n "$ns" pod/openbao-0 "$p:8200" >>"$OUT/pf-$ns.log" 2>&1 </dev/null &
  for _ in $(seq 60); do health_ok "$p" && return 0; sleep 0.5; done
  log "port-forward to $ns did not come up"; return 1
}
api() { # METHOD NS PATH [TOKEN] [JSON] -> prints HTTP code; body in $BODY
  local m=$1 p; p=$(port_of "$2"); local path=$3 tok=${4:-} data=${5:-}
  pf "$2" >/dev/null 2>&1
  local a=(-s -o "$BODY" -w '%{http_code}' -X "$m" --max-time 30)
  [ -n "$tok" ] && a+=(-H "X-Vault-Token: $tok")
  [ -n "$data" ] && a+=(-H 'Content-Type: application/json' --data "$data")
  curl "${a[@]}" "http://127.0.0.1:$p/v1/$path"
}
body() { head -c "${1:-300}" "$BODY" | tr '\n' ' '; }
k8s_login() { # NS MOUNT ROLE JWT -> prints client token (empty on failure)
  local c; c=$(api POST "$1" "auth/$2/login" "" "$(jq -nc --arg r "$3" --arg j "$4" '{role:$r,jwt:$j}')")
  if [ "$c" = 200 ]; then jq -r '.auth.client_token' "$BODY"; else echo "login $1 $2/$3 -> $c $(body)" >&2; fi
}
admin_token() { # NS -> token of role openbao-config-admin via SA openbao-config of NS
  k8s_login "$1" kubernetes openbao-config-admin "$($K create token openbao-config -n "$1" --duration 30m)"
}
wait_running() { # NS [OLD_UID] — the container of openbao-0 runs (and is a new pod when OLD_UID given)
  local ns=$1 old=${2:-} u s
  for _ in $(seq 480); do
    u=$($K -n "$ns" get pod openbao-0 -o jsonpath='{.metadata.uid}' 2>/dev/null)
    s=$($K -n "$ns" get pod openbao-0 -o jsonpath='{.status.containerStatuses[0].state.running.startedAt}' 2>/dev/null)
    if [ -n "$u" ] && [ "$u" != "$old" ] && [ -n "$s" ]; then echo "$s"; return 0; fi
    sleep 0.25
  done
  return 1
}
wait_answering() { local ns=$1; for _ in $(seq 120); do [ -n "$(sealed "$ns")" ] && return 0; sleep 1; done; return 1; }
wait_unsealed() { local ns=$1 t=${2:-240} end; end=$(($(date +%s) + t))
  while [ "$(date +%s)" -lt "$end" ]; do [ "$(sealed "$ns")" = false ] && return 0; sleep 0.5; done; return 1; }
server_log() { $K -n "$1" logs openbao-0 -c openbao "${@:2}" 2>/dev/null | grep -v '^{'; }
audit_lines() { $K -n "$1" logs openbao-0 -c openbao 2>/dev/null | grep -c '^{.*"type":"\(request\|response\)"'; }
diag() { local ns=$1; log "diagnostics for $ns"; $K -n "$ns" get pods -o wide; $K -n "$ns" describe pod openbao-0 | tail -30; server_log "$ns" --tail=60; server_log "$ns" --previous --tail=30; }

seal_key() { # NS [edge] -> writes a 32-byte key file on the node; prints the key id
  local ns=$1 f="$SEC/seal-$1.key" id
  if [ "${2:-}" = edge ]; then # first byte a space, last byte a newline: proves the file is read raw
    { printf ' '; head -c 30 /dev/urandom; printf '\n'; } > "$f"
  else head -c 32 /dev/urandom > "$f"; fi
  id=$(base64 -w0 < "$f" | tr -d '\n' | sha256sum | cut -c1-16)
  docker exec "$NODE" install -d -m 0700 -o 100 -g 1000 /var/lib/catalyst/seal
  docker cp -q "$f" "$NODE:/var/lib/catalyst/seal/openbao-seal-$id.key" >/dev/null 2>&1 || docker cp "$f" "$NODE:/var/lib/catalyst/seal/openbao-seal-$id.key" >/dev/null
  docker exec "$NODE" sh -c "chown 100:1000 /var/lib/catalyst/seal/openbao-seal-$id.key && chmod 0400 /var/lib/catalyst/seal/openbao-seal-$id.key"
  echo "$id" > "$SEC/keyid-$ns"; echo "$id"
}
install_new() { # NS [edge] — chart 0.30.7, 2.7.2, static seal + self-init + audit
  local ns=$1 id
  $K create ns "$ns" --dry-run=client -o yaml | $K apply -f - >/dev/null
  id=$(seal_key "$ns" "${2:-}")
  python3 "$HERE/sealcfg.py" "$ns" "$id" | $K apply -f - >/dev/null
  python3 "$HERE/values.py" new "$BAO_NEW" > "$SEC/values-new-$ns.json"
  helm upgrade --install openbao openbao/openbao --version "$CHART_NEW" -n "$ns" -f "$SEC/values-new-$ns.json" >/dev/null
}
kill_measure() { # NS LABEL — delete the pod, measure the restart to sealed=false
  local ns=$1 lbl=$2 old st t_del t_up lg first unl ready
  old=$($K -n "$ns" get pod openbao-0 -o jsonpath='{.metadata.uid}')
  t_del=$(now)
  $K -n "$ns" delete pod openbao-0 --wait=false >/dev/null
  st=$(wait_running "$ns" "$old") || { note "$lbl: new pod did not start"; return 1; }
  for _ in $(seq 900); do [ "$(sealed "$ns")" = false ] && break; sleep 0.2; done
  t_up=$(now)
  for _ in $(seq 120); do ready=$($K -n "$ns" get pod openbao-0 -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}'); [ "$ready" = True ] && break; sleep 0.5; done
  ready=$($K -n "$ns" get pod openbao-0 -o jsonpath='{.status.conditions[?(@.type=="Ready")].lastTransitionTime}')
  lg=$($K -n "$ns" logs openbao-0 -c openbao 2>/dev/null | grep -E '^[0-9]{4}-[0-9]{2}-[0-9]{2}T')
  first=$(printf '%s\n' "$lg" | head -1 | awk '{print $1}')
  unl=$(printf '%s\n' "$lg" | grep -m1 -E 'vault is unsealed|post-unseal setup complete' | awk '{print $1}')
  python3 - "$lbl" "$st" "$t_up" "$ready" "$first" "$unl" "$t_del" <<'PY' | tee -a "$OUT/measured.txt"
import sys, datetime as d
lbl, st, up, ready, first, unl, tdel = sys.argv[1:]
P = lambda s: d.datetime.fromisoformat(s.replace("Z", "+00:00")).timestamp() if s else None
st, ready, first, unl = P(st), P(ready), P(first), P(unl)
up, tdel = float(up), float(tdel)
f = lambda x: "n/a" if x is None else f"{x:.2f}"
print(f"{lbl}: container start -> sealed=false seen by exec poll: {f(up - st)} s; "
      f"first server log line -> 'vault is unsealed' log line: {f(unl - first if unl and first else None)} s; "
      f"container start -> pod Ready: {f(ready - st if ready else None)} s; pod delete -> sealed=false: {f(up - tdel)} s")
PY
}

# ── setup ────────────────────────────────────────────────────────────────
setup() {
  set -e
  python3 -c 'import yaml' 2>/dev/null || sudo apt-get install -y -q python3-yaml >/dev/null
  if ! kind version 2>/dev/null | grep -q "v${KIND_VERSION#v}"; then
    curl -fsSL --retry 5 -o "$RUNNER_TEMP/kind" "https://kind.sigs.k8s.io/dl/${KIND_VERSION}/kind-linux-amd64"
    sudo install -m 0755 "$RUNNER_TEMP/kind" /usr/local/bin/kind
  fi
  kind version; helm version --short; kubectl version --client | head -1; docker version --format '{{.Server.Version}}'
  kind create cluster --name "$CLUSTER" --image "$KIND_NODE_IMAGE" --wait 120s
  $K label node "$NODE" catalyst.openova.io/role=control-plane --overwrite
  $K get nodes -o wide
  helm repo add openbao https://openbao.github.io/openbao-helm >/dev/null
  helm repo add external-secrets https://charts.external-secrets.io >/dev/null
  helm repo update >/dev/null
  helm search repo openbao/openbao --versions | grep -E "^openbao/openbao\s+($CHART_OLD|$CHART_NEW)\s" | tee -a "$OUT/measured.txt"
  for i in 1 2 3 4 5; do helm dependency build "$ROOT/platform/openbao/chart" >/dev/null && break; sleep $((i * 5)); done
  note "versions: kind $(kind version | awk '{print $2}'), node image $KIND_NODE_IMAGE, helm $(helm version --short), kubectl $(kubectl version --client -o json | jq -r .clientVersion.gitVersion)"
}

# ── step 1: fresh path ───────────────────────────────────────────────────
step1() {
  local ns=openbao ok=1 t0 t1 id sj
  python3 "$HERE/values.py" new "$BAO_NEW" > "$OUT/final-values.json"
  helm template openbao openbao/openbao --version "$CHART_NEW" -n "$ns" -f "$OUT/final-values.json" > "$OUT/render-fresh.yaml"
  python3 - "$OUT/render-fresh.yaml" <<'PY' | tee -a "$OUT/measured.txt"
import sys, yaml
ss = next(d for d in yaml.safe_load_all(open(sys.argv[1])) if d and d["kind"] == "StatefulSet")
p = ss["spec"]["template"]["spec"]; c = p["containers"][0]
sc = p.get("securityContext", {})
print(f"step1 rendered StatefulSet: image={c['image']} runAsUser={sc.get('runAsUser')} runAsGroup={sc.get('runAsGroup')} fsGroup={sc.get('fsGroup')} podManagementPolicy={ss['spec'].get('podManagementPolicy')} updateStrategy={ss['spec']['updateStrategy']['type']}")
print("step1 rendered server command:", c["args"][0].strip().splitlines()[-1].strip())
print("step1 rendered seal mount:", [m for m in c["volumeMounts"] if m["name"] in ("seal", "userconfig-openbao-seal-config")])
print("step1 rendered seal volume:", [v for v in p["volumes"] if v["name"] == "seal"])
PY
  t0=$(now)
  install_new "$ns" || ok=0
  id=$(cat "$SEC/keyid-$ns")
  python3 "$HERE/sealcfg.py" "$ns" "$id" > "$OUT/final-seal-config.yaml"
  if wait_running "$ns" >/dev/null && wait_unsealed "$ns" 300; then
    t1=$(now); note "step1 first start: helm install -> sealed=false: $(dt "$t0" "$t1") s (includes image pull and volume provisioning)"
  else ok=0; diag "$ns"; fi
  sj=$(status_json "$ns"); echo "$sj" > "$OUT/status-fresh.json"
  note "step1 status: $(echo "$sj" | jq -c '{version, type, initialized, sealed, t, n, recovery_seal, recovery_seal_type, storage_type, migration}')"
  [ "$(echo "$sj" | jq -r .type)" = static ] && [ "$(echo "$sj" | jq -r .initialized)" = true ] || ok=0
  note "step1 process identity in the pod: $(kb "$ns" id)"
  note "step1 seal directory as the pod sees it: $(kb "$ns" ls -ln /openbao/seal | tr '\n' ' ')"
  if kb "$ns" sh -c 'touch /openbao/seal/probe' 2>"$SEC/ro.err"; then note "step1 seal mount writable: YES (unexpected)"; ok=0
  else note "step1 seal mount read-only: $(tr '\n' ' ' < "$SEC/ro.err")"; fi
  note "step1 node directory: $(docker exec "$NODE" stat -c '%a %u:%g %n' /var/lib/catalyst/seal /var/lib/catalyst/seal/openbao-seal-"$id".key | tr '\n' ' ')"
  note "step1 server log lines about initialisation: $(server_log "$ns" | grep -i -E 'initializ|self-init|initialize|request' | head -30 | cut -c1-200 | tr '\n' '|')"
  note "step1 audited requests of the first start, in order: $($K -n "$ns" logs openbao-0 -c openbao 2>/dev/null | grep '^{.*"type":"response"' | jq -c '[.request.operation, .request.path, (.auth.policies // [] | join(",")), (.error // "")]' 2>/dev/null | head -12 | tr '\n' ' ')"
  # The self-init role must exist and work: log in as openbao-config-admin with
  # the reconciler's service account (normally rendered by the chart).
  $K -n "$ns" create sa openbao-config >/dev/null 2>&1
  local tok; tok=$(admin_token "$ns")
  if [ -n "$tok" ]; then note "step1 login as openbao-config-admin with SA openbao/openbao-config: OK"
  else note "step1 login as openbao-config-admin: FAILED"; ok=0; fi
  local c; c=$(api GET "$ns" sys/policies/acl/openbao-config-admin "$tok")
  local want; want=$(python3 -c 'import json, sys; print(json.dumps(open(sys.argv[1]).read()))' "$HERE/config-admin-policy.hcl")
  note "step1 policy openbao-config-admin read back: HTTP $c, byte-equal to the configured text: $([ "$(jq -c .data.policy "$BODY" 2>/dev/null)" = "$want" ] && echo yes || echo no)"
  c=$(api GET "$ns" sys/init); note "step1 sys/init: HTTP $c $(body 100)"
  c=$(api POST "$ns" sys/generate-root/attempt "" '{}'); note "step1 unauthenticated sys/generate-root/attempt on the self-initialised server: HTTP $c $(body 160)"
  [ "$c" = 200 ] && api DELETE "$ns" sys/generate-root/attempt >/dev/null
  note "step1 audit lines on stdout so far: $(audit_lines "$ns")"
  $K -n "$ns" logs openbao-0 -c openbao 2>/dev/null | grep -m1 '^{.*"type":"response"' | jq -c '{type, path: .request.path, op: .request.operation, mount: .request.mount_type}' 2>/dev/null | sed 's/^/step1 sample audit entry: /' | tee -a "$OUT/measured.txt"
  for i in 1 2 3; do kill_measure "$ns" "step1 restart $i (static seal, auto-unseal)" || ok=0; done
  sleep 3; tok=$(admin_token "$ns")
  note "step1 after the restarts: audit lines in the new pod after one login: $(audit_lines "$ns")"
  [ "$(audit_lines "$ns")" -gt 0 ] || { note "step1 no audit line after restart"; ok=0; }
  # Fail closed: the key file missing on the node.
  docker exec "$NODE" sh -c "mkdir -p /var/lib/catalyst/seal-off && mv /var/lib/catalyst/seal/openbao-seal-$id.key /var/lib/catalyst/seal-off/"
  local old; old=$($K -n "$ns" get pod openbao-0 -o jsonpath='{.metadata.uid}')
  $K -n "$ns" delete pod openbao-0 --wait=false >/dev/null; sleep 50
  note "step1 key file absent: pod $($K -n "$ns" get pod openbao-0 -o jsonpath='{.status.phase} ready={.status.conditions[?(@.type=="Ready")].status} restarts={.status.containerStatuses[0].restartCount} state={.status.containerStatuses[0].state}' 2>&1), seal status: $(status_json "$ns" | jq -c '{sealed, initialized}' 2>/dev/null || echo 'no answer')"
  note "step1 key file absent, server log: $( { server_log "$ns"; server_log "$ns" --previous; } | grep -i -E 'error|seal' | head -4 | cut -c1-240 | tr '\n' '|')"
  [ "$($K -n "$ns" get pod openbao-0 -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" != True ] || { note "step1 fail-closed check: pod Ready without key (unexpected)"; ok=0; }
  docker exec "$NODE" sh -c "mv /var/lib/catalyst/seal-off/openbao-seal-$id.key /var/lib/catalyst/seal/"
  old=$($K -n "$ns" get pod openbao-0 -o jsonpath='{.metadata.uid}')
  $K -n "$ns" delete pod openbao-0 --wait=false >/dev/null
  wait_running "$ns" "$old" >/dev/null && wait_unsealed "$ns" 180 && note "step1 key file restored: unsealed again" || { ok=0; diag "$ns"; }
  # Raw-file check: a key whose first byte is a space and last byte a newline.
  install_new edgekey edge
  if wait_running edgekey >/dev/null && wait_unsealed edgekey 240; then
    kill_measure edgekey "step1 edge key (leading space, trailing newline) restart" && note "step1 key file is read raw (no whitespace trimming): yes"
  else note "step1 key file is read raw: NO, the edge-byte key did not unseal"; diag edgekey; ok=0; fi
  helm uninstall openbao -n edgekey >/dev/null 2>&1; $K delete ns edgekey --wait=false >/dev/null 2>&1
  [ "$ok" = 1 ] && verdict step1 PASS "fresh install unseals itself, self-initialises and audits; wiring recorded" || verdict step1 FAIL "see measured.txt"
}

# ── step 2: reconciler ───────────────────────────────────────────────────
render_reconciler() { # SELF_NS BAO_ADDR [SELF PEERS_JSON] -> rendered yaml on stdout
  python3 - "$2" "${3:-}" "${4:-}" > "$SEC/rv.json" <<'PY'
import json, sys
addr, self_, regions = sys.argv[1:]
v = {"autoUnseal": {"enabled": True, "baoAddress": addr},
     "config": {"image": "docker.io/library/alpine:3.20", "intervalSeconds": 10}}
if self_:
    v["peerTrust"] = {"selfRegion": self_, "regions": regions}
print(json.dumps(v))
PY
  helm template openbao "$ROOT/platform/openbao/chart" -n openbao -f "$SEC/rv.json" --show-only templates/config-reconciler.yaml \
    | python3 -c "
import sys, yaml
ns = sys.argv[1]
docs = [d for d in yaml.safe_load_all(sys.stdin) if d]
for d in docs: d['metadata']['namespace'] = ns
print(yaml.safe_dump_all(docs, sort_keys=False, width=float('inf')))" "$1"
}
wait_converged() { # NS SECONDS -> prints the converged line
  local ns=$1 t=${2:-180} end line; end=$(($(date +%s) + t))
  $K -n "$ns" rollout status deploy/openbao-config --timeout=120s >/dev/null 2>&1
  while [ "$(date +%s)" -lt "$end" ]; do
    line=$($K -n "$ns" logs deploy/openbao-config 2>/dev/null | grep 'converged (' | tail -1)
    [ -n "$line" ] && { echo "$line"; return 0; }; sleep 2
  done
  $K -n "$ns" logs deploy/openbao-config --tail=30 2>&1 | sed 's/^/  reconciler: /'; return 1
}
check_desired() { # NS RENDERED_YAML TOKEN -> notes policy/role read-back counts; returns 0 when all match
  python3 - "$2" > "$SEC/ops.tsv" <<'PY'
import json, sys, yaml
cm = next(d for d in yaml.safe_load_all(open(sys.argv[1])) if d and d["metadata"]["name"] == "openbao-config-desired")
for line in cm["data"]["manifest"].splitlines():
    f = line.split()
    if len(f) < 2: continue
    p = json.loads(cm["data"][f[1].replace("/", ".") + ".json"])
    print("\t".join([f[0], f[1], json.dumps(p.get("policy", ""))]))
PY
  local pol=0 polok=0 rol=0 rolok=0 kind path want c
  while IFS=$'\t' read -r kind path want; do
    case "$path" in
      sys/policies/acl/*) pol=$((pol + 1)); c=$(api GET "$1" "$path" "$3")
        [ "$c" = 200 ] && [ "$(jq -c .data.policy "$BODY")" = "$want" ] && polok=$((polok + 1)) ;;
      auth/*/role/*) rol=$((rol + 1)); c=$(api GET "$1" "$path" "$3"); [ "$c" = 200 ] && rolok=$((rolok + 1)) ;;
    esac
  done < "$SEC/ops.tsv"
  note "  $1: $(wc -l < "$SEC/ops.tsv") operations; policies byte-equal on read-back $polok/$pol; roles present $rolok/$rol"
  [ "$polok" = "$pol" ] && [ "$rolok" = "$rol" ]
}
step2() {
  local ns=openbao ok=1 line tok c
  render_reconciler "$ns" "http://openbao.openbao.svc.cluster.local:8200" > "$OUT/reconciler-single.yaml"
  $K apply -f "$OUT/reconciler-single.yaml" >/dev/null
  if line=$(wait_converged "$ns" 180); then note "step2 reconciler with today's single-region desired state: $line"; else ok=0; note "step2 reconciler did not converge"; fi
  tok=$(admin_token "$ns")
  check_desired "$ns" "$OUT/reconciler-single.yaml" "$tok" || ok=0
  # Its own policy: prepend a write of sys/policies/acl/openbao-config-admin with a marked text.
  python3 - "$OUT/reconciler-single.yaml" "$HERE/config-admin-policy.hcl" > "$SEC/reconciler-own.yaml" <<'PY'
import json, sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if d]
cm = next(d for d in docs if d["metadata"]["name"] == "openbao-config-desired")
text = open(sys.argv[2]).read() + "# rewritten by the reconciler (lab test SEC-130)\n"
cm["data"]["sys.policies.acl.openbao-config-admin.json"] = json.dumps({"policy": text})
cm["data"]["manifest"] = "write sys/policies/acl/openbao-config-admin\n" + cm["data"]["manifest"]
print(yaml.safe_dump_all(docs, sort_keys=False, width=float("inf")))
PY
  $K apply -f "$SEC/reconciler-own.yaml" >/dev/null && $K -n "$ns" rollout restart deploy/openbao-config >/dev/null
  sleep 5
  if line=$(wait_converged "$ns" 180); then note "step2 with its own policy as first operation: $line"; else ok=0; fi
  tok=$(admin_token "$ns")
  c=$(api GET "$ns" sys/policies/acl/openbao-config-admin "$tok")
  if [ "$c" = 200 ] && jq -r .data.policy "$BODY" | grep -q 'rewritten by the reconciler'; then note "step2 own policy rewritten by the reconciler: yes (read back HTTP $c)"
  else note "step2 own policy rewritten: NO (HTTP $c $(body))"; ok=0; fi
  c=$(api POST "$ns" auth/kubernetes/role/openbao-config-admin "$tok" '{"token_policies":["root"]}'); note "step2 reconciler rewriting its own role: HTTP $c (expected 403)"
  [ "$c" = 403 ] || ok=0
  c=$(api POST "$ns" secret/data/x "$tok" '{"data":{"a":"b"}}'); note "step2 reconciler writing secret data: HTTP $c (expected 403 or 404)"
  [ "$ok" = 1 ] && verdict step2 PASS "reconciler logs in, applies today's desired state unchanged and writes its own policy" || verdict step2 FAIL "see measured.txt"
}

# ── step 3: templated policy ─────────────────────────────────────────────
step3() {
  local ns=openbao ok=1 tok c acc_jq acc_sed raw pol wtok xtok
  tok=$(admin_token "$ns")
  c=$(api POST "$ns" sys/mounts/mounted "$tok" '{"type":"kv","options":{"version":"2"}}'); note "step3 mount mounted/ (kv v2) by openbao-config-admin: HTTP $c $(body 120)"
  [ "$c" = 204 ] || [ "$c" = 200 ] || ok=0
  pf "$ns"; raw=$(curl -s -H "X-Vault-Token: $tok" "http://127.0.0.1:$(port_of "$ns")/v1/sys/auth")
  acc_jq=$(printf '%s' "$raw" | jq -r '.data["kubernetes/"].accessor')
  BAO_K8S_AUTH_PATH=kubernetes
  acc_sed="$(printf '%s' "${raw}" | tr -d '\n' | sed -n "s/.*\"${BAO_K8S_AUTH_PATH}\/\":{\"accessor\":\"\([^\"]*\)\".*/\1/p")"
  note "step3 accessor of kubernetes/: from JSON '$acc_jq'; from the one-line sed of the reconciler design '$acc_sed'; equal: $([ -n "$acc_sed" ] && [ "$acc_sed" = "$acc_jq" ] && echo yes || echo no)"
  local NSX="{{identity.entity.aliases.${acc_jq}.metadata.service_account_namespace}}" SAX="{{identity.entity.aliases.${acc_jq}.metadata.service_account_name}}"
  pol=$(printf 'path "mounted/data/%s/sa/%s/*" { capabilities = ["read"] }\npath "mounted/data/%s/shared/*" { capabilities = ["read"] }\n' "$NSX" "$SAX" "$NSX")
  printf '%s\n' "$pol" > "$OUT/policy-workload.hcl"
  c=$(api POST "$ns" sys/policies/acl/workload "$tok" "$(jq -nc --arg p "$pol" '{policy:$p}')"); note "step3 write policy workload: HTTP $c"
  c=$(api POST "$ns" auth/kubernetes/role/workload "$tok" '{"bound_service_account_names":["*"],"bound_service_account_namespaces":["*"],"audience":"openbao","token_policies":["workload"],"token_ttl":"15m","token_max_ttl":"1h"}'); note "step3 write role workload (any SA, audience openbao): HTTP $c $(body 160)"
  c=$(api POST "$ns" sys/policies/acl/spike-writer "$tok" '{"policy":"path \"mounted/data/*\" { capabilities = [\"create\", \"update\"] }"}'); note "step3 write test policy spike-writer: HTTP $c"
  c=$(api POST "$ns" auth/kubernetes/role/spike-writer "$tok" '{"bound_service_account_names":["writer"],"bound_service_account_namespaces":["spike"],"token_policies":["spike-writer"],"token_ttl":"15m"}'); note "step3 write test role spike-writer: HTTP $c"
  for n in a b spike; do $K create ns "$n" --dry-run=client -o yaml | $K apply -f - >/dev/null; done
  $K -n a create sa x >/dev/null; $K -n a create sa y >/dev/null; $K -n spike create sa writer >/dev/null
  wtok=$(k8s_login "$ns" kubernetes spike-writer "$($K create token writer -n spike)")
  for p in a/sa/x/t a/sa/y/t a/shared/t b/shared/t; do
    for _ in $(seq 30); do c=$(api POST "$ns" "mounted/data/$p" "$wtok" "$(jq -nc --arg v "value-of-$p" '{data:{v:$v}}')"); [ "$c" = 200 ] && break; sleep 1; done
    note "step3 writer puts mounted/data/$p: HTTP $c"
  done
  xtok=$(k8s_login "$ns" kubernetes workload "$($K create token x -n a --audience openbao)")
  [ -n "$xtok" ] || ok=0
  note "step3 SA a/x token policies: $(api GET "$ns" auth/token/lookup-self "$xtok" >/dev/null; jq -c '.data.policies' "$BODY")"
  local exp
  for pe in "a/sa/x/t 200" "a/shared/t 200" "b/shared/t 403" "a/sa/y/t 403" "b/sa/x/t 403"; do
    set -- $pe; c=$(api GET "$ns" "mounted/data/$1" "$xtok")
    exp=$2; note "step3 SA a/x reads mounted/data/$1: HTTP $c (expected $exp)$([ "$c" = 200 ] && echo " value=$(jq -r .data.data.v "$BODY")")"
    [ "$c" = "$exp" ] || ok=0
  done
  c=$(api POST "$ns" auth/kubernetes/login "" "$(jq -nc --arg j "$($K create token x -n a)" '{role:"workload",jwt:$j}')"); note "step3 login with the default audience (not openbao): HTTP $c $(body 120)"
  [ "$c" != 200 ] || ok=0
  [ "$ok" = 1 ] && verdict step3 PASS "templated policy with the real accessor: own path read, other namespace refused" || verdict step3 FAIL "see measured.txt"
}

# ── step 4: upgrade path and seal migration ──────────────────────────────
seed_legacy() { # NS ROOT TAG
  local c
  c=$(api POST "$1" sys/mounts/secret "$2" '{"type":"kv","options":{"version":"2"}}'); note "  $1 enable kv-v2 secret/: HTTP $c"
  for _ in $(seq 30); do c=$(api POST "$1" secret/data/openova/apps/acme/db "$2" "{\"data\":{\"password\":\"legacy-$3\"}}"); [ "$c" = 200 ] && break; sleep 1; done
  note "  $1 write secret/data/openova/apps/acme/db: HTTP $c"
  c=$(api POST "$1" sys/auth/kubernetes "$2" '{"type":"kubernetes"}'); note "  $1 enable auth kubernetes/: HTTP $c"
  c=$(api POST "$1" sys/policies/acl/legacy-probe "$2" '{"policy":"path \"secret/data/openova/*\" { capabilities = [\"read\"] }"}'); note "  $1 write policy legacy-probe: HTTP $c"
}
verify_legacy() { # NS ROOT TAG -> 0 when the seeded data is intact
  local c v
  c=$(api GET "$1" secret/data/openova/apps/acme/db "$2"); v=$(jq -r .data.data.password "$BODY" 2>/dev/null)
  note "  $1 read secret/data/openova/apps/acme/db: HTTP $c value=$v (expected legacy-$3)"
  c=$(api GET "$1" sys/policies/acl/legacy-probe "$2"); note "  $1 policy legacy-probe: HTTP $c"
  [ "$v" = "legacy-$3" ] && [ "$c" = 200 ]
}
legacy_install() { # NS TAG -> init Shamir 1/1, unseal, seed
  local ns=$1 tag=$2
  $K create ns "$ns" --dry-run=client -o yaml | $K apply -f - >/dev/null
  python3 "$HERE/values.py" today "$tag" > "$SEC/values-today-$ns.json"
  helm install openbao openbao/openbao --version "$CHART_OLD" -n "$ns" -f "$SEC/values-today-$ns.json" >/dev/null || return 1
  wait_running "$ns" >/dev/null || return 1
  for _ in $(seq 60); do kb "$ns" bao operator init -key-shares=1 -key-threshold=1 -format=json > "$SEC/init-$ns.json" 2>/dev/null && break; sleep 2; done
  jq -e .root_token "$SEC/init-$ns.json" >/dev/null || return 1
  kb "$ns" bao operator unseal "$(jq -r '.unseal_keys_b64[0]' "$SEC/init-$ns.json")" >/dev/null
  wait_unsealed "$ns" 120 || return 1
  sleep 5   # raft leader election after unseal
  note "  $ns running: $(status_json "$ns" | jq -c '{version, type, t, n, initialized, sealed}')"
  seed_legacy "$ns" "$(jq -r .root_token "$SEC/init-$ns.json")" "$tag"
}
roll_pod() { # NS -> deletes openbao-0 (updateStrategy OnDelete) and waits for the new container
  local old; old=$($K -n "$1" get pod openbao-0 -o jsonpath='{.metadata.uid}')
  $K -n "$1" delete pod openbao-0 --wait=false >/dev/null
  wait_running "$1" "$old" >/dev/null && wait_answering "$1"
}
legacy_path() { # TAG NS
  local tag=$1 ns=$2 ok=1 key root rc out c sj
  note "step4 [$tag] install chart $CHART_OLD with image $tag, Shamir 1 of 1"
  if ! legacy_install "$ns" "$tag"; then note "step4 [$tag] legacy install failed"; diag "$ns"; verdict "step4" FAIL "[$tag] legacy install failed"; return 1; fi
  key=$(jq -r '.unseal_keys_b64[0]' "$SEC/init-$ns.json"); root=$(jq -r .root_token "$SEC/init-$ns.json")
  # Direct jump: chart 0.16.0/image $tag -> chart 0.30.7/image 2.7.2, today's values unchanged.
  python3 "$HERE/values.py" today "$BAO_NEW" > "$SEC/v-today-new.json"
  out=$(helm upgrade openbao openbao/openbao --version "$CHART_NEW" -n "$ns" -f "$SEC/v-today-new.json" 2>&1); rc=$?
  note "step4 [$tag] helm upgrade to chart $CHART_NEW with today's values unchanged: rc=$rc $(printf '%s' "$out" | tail -2 | cut -c1-400 | tr '\n' ' ')"
  if [ "$rc" != 0 ]; then
    python3 "$HERE/values.py" upgrade "$BAO_NEW" > "$SEC/v-upgrade.json"
    out=$(helm upgrade openbao openbao/openbao --version "$CHART_NEW" -n "$ns" -f "$SEC/v-upgrade.json" 2>&1); rc=$?
    note "step4 [$tag] helm upgrade with server.podManagementPolicy=Parallel added: rc=$rc $(printf '%s' "$out" | tail -2 | cut -c1-300 | tr '\n' ' ')"
  fi
  if [ "$rc" != 0 ]; then
    $K -n "$ns" delete statefulset openbao --cascade=orphan >/dev/null
    out=$(helm upgrade openbao openbao/openbao --version "$CHART_NEW" -n "$ns" -f "$SEC/v-upgrade.json" 2>&1); rc=$?
    note "step4 [$tag] helm upgrade after deleting the StatefulSet with --cascade=orphan: rc=$rc $(printf '%s' "$out" | tail -1 | cut -c1-300)"
  fi
  note "step4 [$tag] StatefulSet updateStrategy: $($K -n "$ns" get sts openbao -o jsonpath='{.spec.updateStrategy.type}'); pod image before roll: $($K -n "$ns" get pod openbao-0 -o jsonpath='{.spec.containers[0].image}')"
  roll_pod "$ns" || { ok=0; diag "$ns"; }
  sj=$(status_json "$ns"); note "step4 [$tag] after roll to 2.7.2 (still Shamir): $(echo "$sj" | jq -c '{version, sealed, type, t, n}')"
  kb "$ns" bao operator unseal "$key" >/dev/null 2>&1
  if wait_unsealed "$ns" 120; then sleep 5; note "step4 [$tag] unsealed with the old key on 2.7.2: $(status_json "$ns" | jq -c '{version, sealed, type}')"
  else note "step4 [$tag] did not unseal on 2.7.2"; ok=0; diag "$ns"; fi
  c=$(api POST "$ns" sys/generate-root/attempt "" '{}'); note "step4 [$tag] unauthenticated sys/generate-root/attempt on 2.7.2, Shamir, unsealed (the endpoint today's auth-bootstrap hook uses): HTTP $c $(body 200)"
  [ "$c" = 200 ] && api DELETE "$ns" sys/generate-root/attempt >/dev/null
  local direct=0
  verify_legacy "$ns" "$root" "$tag" && direct=1
  note "step4 [$tag] direct jump $tag -> $BAO_NEW keeps the data: $([ $direct = 1 ] && echo yes || echo NO)"
  if [ "$direct" = 0 ]; then stepwise "$tag"; ok=0; fi
  # Seal migration: Shamir -> static, with the self-init block and the audit stanza present.
  local id; id=$(seal_key "$ns")
  python3 "$HERE/sealcfg.py" "$ns" "$id" | $K apply -f - >/dev/null
  python3 "$HERE/values.py" new "$BAO_NEW" > "$SEC/v-new-$ns.json"
  out=$(helm upgrade openbao openbao/openbao --version "$CHART_NEW" -n "$ns" -f "$SEC/v-new-$ns.json" 2>&1); rc=$?
  note "step4 [$tag] helm upgrade adding the seal, self-init and audit configuration: rc=$rc"
  roll_pod "$ns" || { ok=0; diag "$ns"; }
  sleep 3
  note "step4 [$tag] after restart with the static seal configured: $(status_json "$ns" | jq -c '{sealed, type, migration, recovery_seal, t, n}')"
  note "step4 [$tag] server log at migration start: $(server_log "$ns" | grep -i -E 'migrat|seal' | head -4 | cut -c1-220 | tr '\n' '|')"
  out=$(kb "$ns" bao operator unseal -migrate "$key" 2>&1); rc=$?
  note "step4 [$tag] command: bao operator unseal -migrate <old unseal key> -> rc=$rc: $(printf '%s' "$out" | grep -E 'Seal Type|Sealed|Seal Migration|Recovery|Total|Threshold|Version|Error|error' | tr -s ' ' | tr '\n' ';' | cut -c1-500)"
  if wait_unsealed "$ns" 120; then sleep 5
    sj=$(status_json "$ns"); echo "$sj" > "$OUT/status-migrated-$tag.json"
    note "step4 [$tag] after migration: $(echo "$sj" | jq -c '{version, sealed, type, recovery_seal, recovery_seal_type, t, n, migration}')"
    [ "$(echo "$sj" | jq -r .type)" = static ] || ok=0
  else note "step4 [$tag] not unsealed after -migrate"; ok=0; diag "$ns"; fi
  verify_legacy "$ns" "$root" "$tag" || ok=0
  c=$(api GET "$ns" sys/policies/acl/openbao-config-admin "$root"); note "step4 [$tag] self-init block on an initialised store: policy openbao-config-admin HTTP $c (404 = block skipped)"
  c=$(api GET "$ns" sys/audit "$root"); note "step4 [$tag] audit devices after migration: HTTP $c $(jq -c '[(.data // .) | to_entries[] | select(.value|type=="object") | {path:.key, type:.value.type}]' "$BODY" 2>/dev/null)"
  c=$(api POST "$ns" sys/audit/api-probe "$root" '{"type":"file","options":{"file_path":"stdout"}}'); note "step4 [$tag] creating an audit device through the API with a root token: HTTP $c $(body 160)"
  note "step4 [$tag] audit lines on stdout: $(audit_lines "$ns")"
  kill_measure "$ns" "step4 [$tag] restart after migration" || ok=0
  verify_legacy "$ns" "$root" "$tag" || ok=0
  [ "$ok" = 1 ] && verdict step4 PASS "[$tag] direct jump to $BAO_NEW keeps data; unseal -migrate moves Shamir to static" || verdict step4 FAIL "[$tag] see measured.txt"
}
stepwise() { # TAG — only when the direct jump lost data: every last patch of each minor in turn
  local tag=$1 ns="step${tag//./}" key root v
  note "step4 [$tag] stepwise path: $STEPWISE"
  legacy_install "$ns" "$tag" || { note "  stepwise install failed"; return 1; }
  key=$(jq -r '.unseal_keys_b64[0]' "$SEC/init-$ns.json"); root=$(jq -r .root_token "$SEC/init-$ns.json")
  for v in $STEPWISE; do
    python3 "$HERE/values.py" upgrade "$v" > "$SEC/v-step.json"
    helm upgrade openbao openbao/openbao --version "$CHART_NEW" -n "$ns" -f "$SEC/v-step.json" >/dev/null 2>&1
    roll_pod "$ns"; kb "$ns" bao operator unseal "$key" >/dev/null 2>&1; wait_unsealed "$ns" 120; sleep 5
    if verify_legacy "$ns" "$root" "$tag"; then note "  stepwise $v: data intact"; else note "  stepwise $v: data NOT intact"; fi
  done
}
step4() {
  legacy_path 2.1.0 legacy21
  legacy_path 2.3.1 legacy23
}

# ── step 5: what the old key can still do after migration ───────────────
local_port() { case "$1" in legacy21) echo 18241 ;; legacy23) echo 18242 ;; *) echo 18249 ;; esac; }
local_listener() { # NS on|off — add or remove a second listener on 127.0.0.1:8210 that re-enables the legacy generate-root endpoints
  local ns=$1
  if [ "$2" = on ]; then
    $K -n "$ns" patch configmap openbao-seal-config --type merge -p '{"data":{"listener-local.hcl":"listener \"tcp\" {\n  address = \"127.0.0.1:8210\"\n  cluster_address = \"127.0.0.1:8211\"\n  tls_disable = 1\n  disable_unauthed_generate_root_endpoints = false\n}\n"}}' >/dev/null
  else
    $K -n "$ns" patch configmap openbao-seal-config --type json -p '[{"op":"remove","path":"/data/listener-local.hcl"}]' >/dev/null
  fi
  roll_pod "$ns" && wait_unsealed "$ns" 120; sleep 3
}
local_generate_root() { # NS KEY -> one-line summary; any token produced is revoked
  local ns=$1 key=$2 p c otp nonce enc tokn pol out
  p=$(local_port "$ns")
  pkill -f "port-forward -n $ns pod/openbao-0 $p:8210" 2>/dev/null
  nohup $K port-forward -n "$ns" pod/openbao-0 "$p:8210" >>"$OUT/pf-$ns-local.log" 2>&1 </dev/null &
  for _ in $(seq 40); do curl -s -o /dev/null --max-time 2 "http://127.0.0.1:$p/v1/sys/seal-status" && break; sleep 0.5; done
  c=$(curl -s -o "$BODY" -w '%{http_code}' --max-time 20 -X POST -H 'Content-Type: application/json' --data '{}' "http://127.0.0.1:$p/v1/sys/generate-root/attempt")
  otp=$(jq -r '.otp // empty' "$BODY" 2>/dev/null); nonce=$(jq -r '.nonce // empty' "$BODY" 2>/dev/null)
  out="attempt HTTP $c$([ "$c" != 200 ] && echo " $(body 140)") (otp issued: $([ -n "$otp" ] && echo yes || echo no))"
  if [ "$c" = 200 ]; then
    c=$(curl -s -o "$BODY" -w '%{http_code}' --max-time 20 -X POST -H 'Content-Type: application/json' \
      --data "$(jq -nc --arg k "$key" --arg n "$nonce" '{key:$k,nonce:$n}')" "http://127.0.0.1:$p/v1/sys/generate-root/update")
    enc=$(jq -r '.encoded_token // .encoded_root_token // empty' "$BODY" 2>/dev/null)
    out="$out; update with the key HTTP $c $(jq -c '{complete, progress, required}' "$BODY" 2>/dev/null)$([ "$c" != 200 ] && echo " $(body 140)")"
    if [ -n "$enc" ] && [ -n "$otp" ]; then
      tokn=$(python3 -c 'import base64, sys
e, o = sys.argv[1:]
r = base64.b64decode(e + "=" * (-len(e) % 4))
print(bytes(a ^ b for a, b in zip(r, o.encode())).decode())' "$enc" "$otp" 2>/dev/null)
      api GET "$ns" auth/token/lookup-self "$tokn" >/dev/null; pol=$(jq -c '.data.policies' "$BODY" 2>/dev/null)
      out="$out; decoded token is valid with policies: ${pol:-none}"
      api POST "$ns" auth/token/revoke-self "$tokn" >/dev/null
    fi
    curl -s -o /dev/null --max-time 20 -X DELETE "http://127.0.0.1:$p/v1/sys/generate-root/attempt"
  fi
  pkill -f "port-forward -n $ns pod/openbao-0 $p:8210" 2>/dev/null
  echo "$out"
}
step5_one() { # NS TAG
  local ns=$1 tag=$2 ok=1 key root c nonce any=0
  key=$(jq -r '.unseal_keys_b64[0]' "$SEC/init-$ns.json"); root=$(jq -r .root_token "$SEC/init-$ns.json")
  [ "$(sealed "$ns")" = false ] || { verdict step5 FAIL "[$tag] instance not unsealed"; return 1; }
  # Token-free endpoints that could turn a recovery key into a root token or new keys.
  for pr in "POST sys/generate-root/attempt {}" \
            "POST sys/generate-root-token/attempt {}" \
            "GET sys/generate-recovery-token/attempt -" \
            "POST sys/rekey-recovery-key/init {\"secret_shares\":1,\"secret_threshold\":1}" \
            "POST sys/rekey/init {\"secret_shares\":1,\"secret_threshold\":1}" \
            "POST sys/rotate/recovery/init {\"secret_shares\":1,\"secret_threshold\":1}" \
            "POST sys/rotate/root/init {\"secret_shares\":1,\"secret_threshold\":1}"; do
    set -- $pr
    if [ "$3" = - ]; then c=$(api "$1" "$ns" "$2"); else c=$(api "$1" "$ns" "$2" "" "$3"); fi
    note "step5 [$tag] no token: $1 /v1/$2 -> HTTP $c $(body 160)"
    if [ "$c" = 200 ] && [ "$1" = POST ]; then
      nonce=$(jq -r '.nonce // .data.nonce // empty' "$BODY")
      local up
      case "$2" in */attempt) up="${2%/attempt}/update" ;; *) up="${2%/init}/update" ;; esac
      c=$(api POST "$ns" "$up" "" "$(jq -nc --arg k "$key" --arg n "$nonce" '{key:$k,nonce:$n}')")
      note "step5 [$tag]   continued with the old key: POST /v1/$up -> HTTP $c; a token or new keys returned: $(jq -r 'if ((.encoded_token // .encoded_root_token // "") != "") or (((.keys // []) | length) > 0) then "YES" else "no" end' "$BODY" 2>/dev/null)"
      jq -e '((.encoded_token // .encoded_root_token // "") != "") or (((.keys // []) | length) > 0)' "$BODY" >/dev/null 2>&1 && any=1
      api DELETE "$ns" "$2" >/dev/null
    fi
  done
  note "step5 [$tag] token-free path from the old key to a root token or new keys: $([ $any = 1 ] && echo FOUND || echo none)"
  # The same, after someone with write access to the server configuration
  # re-enables the legacy endpoints on a pod-local listener.
  local_listener "$ns" on
  local lgr; lgr=$(local_generate_root "$ns" "$key")
  note "step5 [$tag] with a pod-local listener that sets disable_unauthed_generate_root_endpoints = false: $lgr"
  # Authenticated rotation of the recovery keys, wrapped with PGP.
  export GNUPGHOME="$SEC/gnupg-$ns"; mkdir -p -m 0700 "$GNUPGHOME"
  gpg --batch --pinentry-mode loopback --passphrase '' --quick-gen-key "sec130-$ns@example.invalid" rsa2048 encr never >/dev/null 2>&1
  local pgp; pgp=$(gpg --export "sec130-$ns@example.invalid" | base64 -w0)
  local rp='path "sys/rotate/recovery/*" { capabilities = ["create", "read", "update", "delete"] }'
  c=$(api POST "$ns" sys/policies/acl/recovery-rotate "$root" "$(jq -nc --arg p "$rp" '{policy:$p}')"); note "step5 [$tag] policy recovery-rotate (no sudo) written: HTTP $c"
  c=$(api POST "$ns" auth/token/create "$root" '{"policies":["recovery-rotate"],"ttl":"15m","no_default_policy":false}'); local rtok; rtok=$(jq -r .auth.client_token "$BODY")
  local used="recovery-rotate (no sudo)"
  c=$(api POST "$ns" sys/rotate/recovery/init "$rtok" "$(jq -nc --arg k "$pgp" '{secret_shares:1,secret_threshold:1,pgp_keys:[$k]}')")
  if [ "$c" = 403 ]; then
    note "step5 [$tag] rotate init with a token of policy recovery-rotate (no sudo): HTTP 403"
    rp='path "sys/rotate/recovery/*" { capabilities = ["create", "read", "update", "delete", "sudo"] }'
    api POST "$ns" sys/policies/acl/recovery-rotate "$root" "$(jq -nc --arg p "$rp" '{policy:$p}')" >/dev/null
    used="recovery-rotate (with sudo)"
    c=$(api POST "$ns" sys/rotate/recovery/init "$rtok" "$(jq -nc --arg k "$pgp" '{secret_shares:1,secret_threshold:1,pgp_keys:[$k]}')")
  fi
  # The rotate endpoints answer under "data".
  nonce=$(jq -r '(.data // .).nonce // empty' "$BODY")
  note "step5 [$tag] POST /v1/sys/rotate/recovery/init {secret_shares:1, secret_threshold:1, pgp_keys:[<base64 public key>]} with a token of policy $used: HTTP $c $(jq -c '(.data // .) | del(.nonce)' "$BODY" 2>/dev/null)"
  c=$(api POST "$ns" sys/rotate/recovery/update "$rtok" "$(jq -nc --arg k "$key" --arg n "$nonce" '{key:$k,nonce:$n}')")
  local enc; enc=$(jq -r '((.data // .).keys_base64 // (.data // .).keys // [])[0] // empty' "$BODY")
  note "step5 [$tag] POST /v1/sys/rotate/recovery/update {key:<old key>, nonce}: HTTP $c $(jq -c '(.data // .) | del(.keys, .keys_base64, .nonce)' "$BODY" 2>/dev/null | cut -c1-300) encrypted keys returned: $(jq -r '((.data // .).keys_base64 // []) | length' "$BODY" 2>/dev/null)"
  local newkey=""
  if [ -n "$enc" ]; then newkey=$(printf '%s' "$enc" | base64 -d 2>/dev/null | gpg --batch --pinentry-mode loopback --passphrase '' --decrypt 2>/dev/null)
    note "step5 [$tag] new recovery key decrypts with the PGP private key: $([ -n "$newkey" ] && echo "yes ($(printf '%s' "$newkey" | wc -c) characters)" || echo no)"
  else ok=0; fi
  # The old key must now be refused, the new one accepted (each in its own attempt).
  c=$(api POST "$ns" sys/rotate/recovery/init "$rtok" '{"secret_shares":1,"secret_threshold":1}'); nonce=$(jq -r '(.data // .).nonce // empty' "$BODY")
  c=$(api POST "$ns" sys/rotate/recovery/update "$rtok" "$(jq -nc --arg k "$key" --arg n "$nonce" '{key:$k,nonce:$n}')"); note "step5 [$tag] old key after rotation: HTTP $c $(body 160)"
  [ "$c" != 200 ] || ok=0
  api DELETE "$ns" sys/rotate/recovery/init "$rtok" >/dev/null
  note "step5 [$tag] pod-local legacy listener, old key after rotation: $(local_generate_root "$ns" "$key")"
  local_listener "$ns" off
  if [ -n "$newkey" ]; then
    c=$(api POST "$ns" sys/rotate/recovery/init "$rtok" '{"secret_shares":1,"secret_threshold":1}'); nonce=$(jq -r '(.data // .).nonce // empty' "$BODY")
    c=$(api POST "$ns" sys/rotate/recovery/update "$rtok" "$(jq -nc --arg k "$newkey" --arg n "$nonce" '{key:$k,nonce:$n}')"); note "step5 [$tag] new key accepted: HTTP $c complete=$(jq -r '(.data // .).complete' "$BODY" 2>/dev/null)"
    [ "$c" = 200 ] || ok=0
    api DELETE "$ns" sys/rotate/recovery/init "$rtok" >/dev/null
  fi
  [ "$any" = 0 ] || ok=0
  [ "$ok" = 1 ] && verdict step5 PASS "[$tag] no token-free root path from the old key; authenticated PGP rotation recorded" || verdict step5 FAIL "[$tag] see measured.txt"
}
step5() { step5_one legacy21 2.1.0; step5_one legacy23 2.3.1; }

# ── step 6: External Secrets 0.10.7, PushSecret IfNotExists, peer JWT store ─
step6() {
  local ok=1 c line tok_b pjwt v1 v2 vg1 vg2
  # Region b: a second 2.7.2 server in namespace bao-b, built the same way.
  install_new bao-b
  wait_running bao-b >/dev/null && wait_unsealed bao-b 240 || { ok=0; diag bao-b; }
  # Region names resolve in namespace openbao, as the peer store expects.
  $K apply -f - >/dev/null <<'EOF'
apiVersion: v1
kind: Service
metadata: {name: openbao-a, namespace: openbao}
spec:
  type: ClusterIP
  selector: {app.kubernetes.io/name: openbao, app.kubernetes.io/instance: openbao, component: server}
  ports: [{name: http, port: 8200, targetPort: 8200}]
---
apiVersion: v1
kind: Service
metadata: {name: openbao-b, namespace: openbao}
spec:
  type: ExternalName
  externalName: openbao.bao-b.svc.cluster.local
EOF
  # Both reconcilers with today's two-region desired state.
  render_reconciler openbao "http://openbao.openbao.svc.cluster.local:8200" a '["a","b"]' > "$OUT/reconciler-a.yaml"
  render_reconciler bao-b "http://openbao.bao-b.svc.cluster.local:8200" b '["a","b"]' > "$OUT/reconciler-b.yaml"
  $K apply -f "$OUT/reconciler-a.yaml" >/dev/null && $K -n openbao rollout restart deploy/openbao-config >/dev/null
  $K apply -f "$OUT/reconciler-b.yaml" >/dev/null
  sleep 15
  for ns in openbao bao-b; do
    if line=$(wait_converged "$ns" 240); then note "step6 reconciler $ns (two-region desired state): $line"; else ok=0; fi
  done
  tok_b=$(admin_token bao-b)
  c=$(api POST bao-b sys/mounts/secret "$tok_b" '{"type":"kv","options":{"version":"2"}}'); note "step6 region b: mount secret/ (kv v2) by openbao-config-admin: HTTP $c"
  check_desired bao-b "$OUT/reconciler-b.yaml" "$tok_b" || ok=0
  # External Secrets
  helm upgrade --install external-secrets external-secrets/external-secrets --version "$ESO_CHART" -n external-secrets-system --create-namespace \
    --set installCRDs=true --wait --timeout 6m >/dev/null || { ok=0; log "ESO install failed"; }
  note "step6 External Secrets: $($K -n external-secrets-system get deploy external-secrets -o jsonpath='{.spec.template.spec.containers[0].image}')"
  note "step6 PushSecret selector fields in this CRD: $($K get crd pushsecrets.external-secrets.io -o json | jq -c '[.spec.versions[] | select(.name=="v1alpha1") | .schema.openAPIV3Schema.properties.spec.properties.selector.properties | keys[]]')"
  note "step6 PushSecret updatePolicy enum: $($K get crd pushsecrets.external-secrets.io -o json | jq -c '[.spec.versions[] | select(.name=="v1alpha1") | .schema.openAPIV3Schema.properties.spec.properties.updatePolicy.enum][0]')"
  echo '{"peerStores":{"selfRegion":"a","regions":"[\"a\",\"b\"]"}}' > "$SEC/stores.json"
  helm template ess "$ROOT/platform/external-secrets-stores/chart" -n external-secrets-system -f "$SEC/stores.json" \
    --show-only templates/clustersecretstore-peers.yaml > "$OUT/peer-stores.yaml"
  for _ in $(seq 30); do $K apply -f "$OUT/peer-stores.yaml" >/dev/null 2>&1 && break; sleep 4; done
  for _ in $(seq 60); do $K -n external-secrets-system get secret openbao-peer-push-token >/dev/null 2>&1 && break; sleep 3; done
  note "step6 peer token Secret: $($K -n external-secrets-system get externalsecret openbao-peer-push-token -o jsonpath='{.status.conditions[0].reason} {.status.conditions[0].message}' 2>&1 | cut -c1-200)"
  for _ in $(seq 40); do [ "$($K get clustersecretstore vault-b -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')" = True ] && break; sleep 3; done
  note "step6 ClusterSecretStore vault-b: $($K get clustersecretstore vault-b -o jsonpath='{.status.conditions[0].status} {.status.conditions[0].reason} {.status.conditions[0].message}' 2>&1 | cut -c1-200)"
  $K create ns acme --dry-run=client -o yaml | $K apply -f - >/dev/null
  $K apply -f - <<'EOF' 2>&1 | sed 's/^/  apply: /'
apiVersion: v1
kind: Secret
metadata: {name: spike-src, namespace: acme}
stringData: {password: first-value}
---
apiVersion: external-secrets.io/v1alpha1
kind: PushSecret
metadata: {name: spike-ifnotexists, namespace: acme}
spec:
  refreshInterval: 10s
  updatePolicy: IfNotExists
  deletionPolicy: None
  secretStoreRefs: [{kind: ClusterSecretStore, name: vault-b}]
  selector: {secret: {name: spike-src}}
  data:
    - match: {secretKey: password, remoteRef: {remoteKey: openova/apps/acme/spike-ifnotexists/e1, property: password}}
---
apiVersion: external-secrets.io/v1alpha1
kind: PushSecret
metadata: {name: spike-replace, namespace: acme}
spec:
  refreshInterval: 10s
  updatePolicy: Replace
  deletionPolicy: None
  secretStoreRefs: [{kind: ClusterSecretStore, name: vault-b}]
  selector: {secret: {name: spike-src}}
  data:
    - match: {secretKey: password, remoteRef: {remoteKey: openova/apps/acme/spike-replace/e1, property: password}}
---
apiVersion: generators.external-secrets.io/v1alpha1
kind: Password
metadata: {name: spike-gen-e1, namespace: acme}
spec: {length: 32, digits: 6, symbols: 0, noUpper: false, allowRepeat: true}
---
apiVersion: external-secrets.io/v1alpha1
kind: PushSecret
metadata: {name: spike-generator, namespace: acme}
spec:
  refreshInterval: 10s
  updatePolicy: IfNotExists
  deletionPolicy: None
  secretStoreRefs: [{kind: ClusterSecretStore, name: vault-b}]
  selector:
    generatorRef: {apiVersion: generators.external-secrets.io/v1alpha1, kind: Password, name: spike-gen-e1}
  data:
    - match: {secretKey: password, remoteRef: {remoteKey: openova/apps/acme/spike-generator/e1, property: password}}
EOF
  sleep 25
  for ps in spike-ifnotexists spike-replace spike-generator; do
    note "step6 PushSecret $ps: $($K -n acme get pushsecret "$ps" -o jsonpath='{.status.conditions[0].status} {.status.conditions[0].reason} {.status.conditions[0].message}' 2>&1 | cut -c1-220)"
  done
  # Read back in region b, logged in exactly as the store does (jwt-peer-a, role openbao-peer-push).
  pjwt=$($K -n external-secrets-system get secret openbao-peer-push-token -o jsonpath='{.data.token}' | base64 -d)
  local ptok; ptok=$(k8s_login bao-b jwt-peer-a openbao-peer-push "$pjwt")
  rb() { api GET bao-b "secret/data/openova/apps/acme/$1/e1" "$ptok" >/dev/null; jq -r '.data.data.password // empty' "$BODY"; }
  v1=$(rb spike-ifnotexists); v2=$(rb spike-replace); vg1=$(rb spike-generator)
  note "step6 region b after first sync: ifnotexists='$v1' replace='$v2' generator length=${#vg1}"
  [ "$v1" = first-value ] && [ "$v2" = first-value ] && [ "${#vg1}" = 32 ] || ok=0
  api GET bao-b secret/metadata/openova/apps/acme/spike-ifnotexists/e1 "$ptok" >/dev/null; note "step6 custom metadata written by the push: $(jq -c .data.custom_metadata "$BODY" 2>/dev/null)"
  $K -n acme patch secret spike-src --type merge -p '{"stringData":{"password":"second-value"}}' >/dev/null
  sleep 45
  v1=$(rb spike-ifnotexists); v2=$(rb spike-replace); vg2=$(rb spike-generator)
  note "step6 region b 45 s after the source changed (refresh 10 s): ifnotexists='$v1' (expected first-value) replace='$v2' (expected second-value) generator unchanged: $([ "$vg1" = "$vg2" ] && echo yes || echo NO)"
  [ "$v1" = first-value ] && [ "$v2" = second-value ] && [ -n "$vg1" ] && [ "$vg1" = "$vg2" ] || ok=0
  for ps in spike-ifnotexists spike-generator; do
    note "step6 PushSecret $ps after later refreshes: $($K -n acme get pushsecret "$ps" -o jsonpath='{.status.conditions[0].status} {.status.conditions[0].reason}' 2>&1)"
  done
  if [ "$ok" != 1 ]; then $K -n external-secrets-system logs deploy/external-secrets --tail=40 2>&1 | cut -c1-300 | sed 's/^/  eso: /'; fi
  [ "$ok" = 1 ] && verdict step6 PASS "PushSecret IfNotExists through the peer JWT store works against $BAO_NEW with External Secrets $ESO_CHART" || verdict step6 FAIL "see measured.txt"
}

results() {
  echo "================ verdicts"; cat "$OUT/verdicts.txt" 2>/dev/null
  echo "================ measured"; cat "$OUT/measured.txt" 2>/dev/null
  echo "================ final values (upstream chart $CHART_NEW): the keys added to today's openbao block"
  python3 "$HERE/values.py" today "$BAO_NEW" > "$SEC/t.json"; python3 "$HERE/values.py" new "$BAO_NEW" > "$SEC/n.json"
  python3 - "$SEC/t.json" "$SEC/n.json" <<'PY' | tee "$OUT/final-values-delta.yaml"
import json, sys, yaml
t, n = (json.load(open(p)) for p in sys.argv[1:])
d = {"server": {k: v for k, v in n["server"].items() if t["server"].get(k) != v}}
print(yaml.safe_dump(d, sort_keys=False))
PY
  echo "================ final seal-config ConfigMap (key id of this run)"; cat "$OUT/final-seal-config.yaml" 2>/dev/null
}

"$@"
rc=$?
# A failed measurement is a verdict line, not a red step; only setup may fail the job.
[ "${1:-}" = setup ] && exit "$rc"
exit 0
