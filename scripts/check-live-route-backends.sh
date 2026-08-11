#!/usr/bin/env bash
# check-live-route-backends.sh — CLUSTER-side guard: a Gateway must not
# ADVERTISE a hostname it cannot serve (#6040, same class as #5394/#5406/
# #5414/#5416/#5511).
#
# WHY THIS EXISTS, AND WHY NO SOURCE-SIDE SCAN CAN REPLACE IT
#
# Every `helm template` render of the secondary region on hw293 is CORRECT.
# `bp-catalyst-edge-routes` is supposed to render Services that select zero
# local Pods — that is its documented design ("so every dial falls through the
# mesh to region-a's singleton"). A render guard therefore has nothing to
# object to. The defect only exists at runtime, in the join between three
# facts that live in three different objects:
#
#     an HTTPRoute advertises host H at backend B
#     B has zero ready Endpoints
#     the mesh that was supposed to supply B's remote Endpoints is not up
#
# Measured live on hw293 (`hw293.omantel.biz`, dep a0077ba47e3720e5),
# 2026-08-11, both regions read read-only:
#
#     region A   69 HTTPRoute backendRefs    0 empty      curl -> 200
#     region B   17 HTTPRoute backendRefs   10 empty      curl -> 503
#
# Both regions' nodes are members of the same round-robin ELB pool, so half of
# every fresh TCP connection to console./api./auth./marketplace./mcp./hubble.
# landed on region B and got envoy's 19-byte `no healthy upstream`. Walkers
# who did not resample recorded false ❌ against features that work — the
# defect corrupts MEASUREMENT across the whole ledger, not just those hosts.
#
# The ELB cannot notice: its health monitor is `protocol = "TCP"` against the
# hostNetwork cilium-envoy host port (infra/providers/huawei/main.tf,
# huaweicloud_elb_monitor.{https,http,console_https,console_http}), and envoy
# binds that port whether or not it has a single resolvable upstream. That
# check cannot fail while the pod is Running, so a region that answers 503 to
# every request stays in rotation forever. This script is the check that CAN
# fail.
#
# THE INVARIANT
#
# For every backendRef of every HTTPRoute, the backend Service must have a
# path to at least one endpoint:
#
#   * has >=1 local ready Endpoint                        -> SERVING
#   * has 0 local Endpoints and NO ClusterMesh annotation -> DEAD BACKEND
#         An unannotated Service with no Pods is a 503 generator with no
#         mechanism that could ever fill it. hw293 region B: keycloak/keycloak
#         (Init:0/1 for 11h on a missing hub Secret) and
#         kube-system/hubble-ui-oauth2-proxy (CreateContainerConfigError).
#   * has 0 local Endpoints and IS annotated
#     `service.cilium.io/global: "true"`                  -> MESH STUB
#         Legitimate by design — but ONLY while the local cilium agent
#         actually has a remote cluster. On hw293 both regions reported
#         `ClusterMesh: 0/0 remote clusters ready`, so all 8 stubs were black
#         holes. A stub with zero remote clusters is a DEAD BACKEND that
#         merely looks intentional.
#   * created within --grace-seconds                      -> PENDING
#         A freshly installed Application has not had time to schedule. The
#         invariant is about steady-state advertisement, not the first
#         seconds of an install, so young Services are reported and not
#         counted. Region A carried exactly one of these (an Application
#         installed 3 minutes before the sweep) and was clean once it settled.
#
# Exit 0 = every advertised backend can serve. 1 = at least one advertised
# hostname is a 503 generator. 2 = setup error.
#
# Usage:
#   scripts/check-live-route-backends.sh                        # current context
#   scripts/check-live-route-backends.sh --kubeconfig <path>
#   scripts/check-live-route-backends.sh --context <ctx>
#   scripts/check-live-route-backends.sh --self-test            # no cluster needed
#
# --self-test is a VACUITY CHECK, not decoration. A guard that has only ever
# been observed passing is worthless, so the self-test drives the classifier
# through four fixtures — two that MUST pass and two that MUST fail — and
# fails the script if any must-fail fixture comes back clean.
#
# 🛑 This repo is PUBLIC. The script prints object names, namespaces, hostnames
#    and counts only. It never reads Secret data and never prints a value.

set -euo pipefail

KUBECTL_ARGS=()
SELF_TEST_ONLY=0
GRACE_SECONDS="${GRACE_SECONDS:-300}"

while [ $# -gt 0 ]; do
  case "$1" in
    --kubeconfig)     KUBECTL_ARGS+=(--kubeconfig "$2"); shift 2 ;;
    --context)        KUBECTL_ARGS+=(--context "$2");    shift 2 ;;
    --grace-seconds)  GRACE_SECONDS="$2";                shift 2 ;;
    --self-test)      SELF_TEST_ONLY=1;                  shift ;;
    -h|--help)
      sed -n '2,80p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "Unknown arg: $1" >&2
       echo "Usage: $0 [--kubeconfig <path>] [--context <ctx>] [--grace-seconds N] [--self-test]" >&2
       exit 2 ;;
  esac
done

# ─────────────────────────────────────────────────────────────────────────────
# The classifier. Reads ONE normalized JSON bundle on stdin so that the live
# path and the self-test path exercise the SAME code — a self-test that ran a
# reimplementation would prove nothing about the guard that actually runs.
#
# bundle = {
#   "meshRemoteClusters": <int>,   # cilium `ClusterMesh: N/M remote clusters ready`
#   "graceSeconds":       <int>,
#   "services":  [{"ns","name","global","ageSeconds"}],
#   "endpoints": [{"ns","name","ready"}],
#   "routes":    [{"ns","name","hosts":[..],"backends":[{"ns","name"}]}]
# }
# ─────────────────────────────────────────────────────────────────────────────
read -r -d '' CLASSIFY_PY <<'PY' || true
import json, sys

b = json.load(sys.stdin)
mesh = int(b.get("meshRemoteClusters", 0))
grace = int(b.get("graceSeconds", 300))
svc = {(s["ns"], s["name"]): s for s in b.get("services", [])}
ready = {(e["ns"], e["name"]): int(e.get("ready", 0)) for e in b.get("endpoints", [])}

rows, seen = [], set()
for r in b.get("routes", []):
    hosts = ",".join(r.get("hosts") or []) or "-"
    for be in r.get("backends") or []:
        key = (be.get("ns") or r["ns"], be["name"])
        if (hosts, key) in seen:
            continue
        seen.add((hosts, key))
        s = svc.get(key)
        n = ready.get(key, 0)
        if n > 0:
            verdict, why = "SERVING", f"{n} ready endpoint(s)"
        elif s is None:
            verdict, why = "DEAD-BACKEND", "backend Service does not exist"
        elif int(s.get("ageSeconds", 10 ** 9)) < grace:
            verdict, why = "PENDING", f"Service is {s.get('ageSeconds')}s old (< {grace}s grace)"
        elif s.get("global") == "true":
            if mesh > 0:
                verdict, why = "SERVING", f"ClusterMesh stub, {mesh} remote cluster(s) ready"
            else:
                verdict = "DEAD-BACKEND"
                why = "ClusterMesh stub but 0 remote clusters ready — black hole"
        else:
            verdict, why = "DEAD-BACKEND", "0 endpoints and no service.cilium.io/global"
        rows.append((verdict, hosts, f"{key[0]}/{key[1]}", why))

order = {"DEAD-BACKEND": 0, "PENDING": 1, "SERVING": 2}
rows.sort(key=lambda r: (order[r[0]], r[1], r[2]))

bad = [r for r in rows if r[0] == "DEAD-BACKEND"]
pending = [r for r in rows if r[0] == "PENDING"]

print(f"ClusterMesh remote clusters ready: {mesh}")
print(f"HTTPRoute backendRefs examined:    {len(rows)}")
print(f"  SERVING       {sum(1 for r in rows if r[0] == 'SERVING')}")
print(f"  PENDING       {len(pending)}  (younger than {grace}s — reported, not counted)")
print(f"  DEAD-BACKEND  {len(bad)}")
print()
for v, h, be, why in rows:
    if v == "SERVING":
        continue
    print(f"  {v:<13} host={h:<40} backend={be:<44} {why}")

if bad:
    print()
    print(f"FAIL: {len(bad)} advertised backendRef(s) cannot serve. Every request this")
    print("      Gateway round-robins onto this region for those hostnames returns")
    print("      envoy 503 `no healthy upstream`. Any UAT verdict recorded against")
    print("      these hostnames from this region is measurement noise, not a result.")
    sys.exit(1)

print()
print("PASS: every advertised backendRef has a path to at least one endpoint.")
PY

# Run the classifier with the bundle on STDIN. `python3 -c` (not `python3 -`)
# is load-bearing: a heredoc-fed `python3 - ` would consume stdin itself and
# the classifier would parse the empty string instead of the bundle. That is
# not hypothetical — it is the first thing --self-test caught, and it had made
# the two must-fail fixtures "pass" for the wrong reason (a JSON crash exits 1
# too). Hence the verdict-token assertions in the self-test below.
classify() { python3 -c "$CLASSIFY_PY"; }

# ─────────────────────────────────────────────────────────────────────────────
# Self-test — the vacuity check.
# ─────────────────────────────────────────────────────────────────────────────
if [ "$SELF_TEST_ONLY" = "1" ]; then
  fail_count=0

  # An exit code alone is NOT enough evidence. A crashed classifier also exits
  # non-zero, which would let a broken guard score every must-fail fixture as
  # "ok" while proving nothing. So each fixture must ALSO emit the verdict
  # token that only the classifier's own decision path can print.
  run_fixture() {
    local name="$1" want="$2" bundle="$3" out rc token
    set +e
    out="$(printf '%s' "$bundle" | classify 2>&1)"; rc=$?
    set -e
    if [ "$want" = "pass" ]; then token="PASS:"; else token="FAIL:"; fi
    if [ "$want" = "pass" ] && [ "$rc" -ne 0 ]; then
      echo "SELF-TEST FAIL: fixture '$name' expected exit 0, got $rc"; echo "$out"
      fail_count=$((fail_count + 1)); return
    fi
    if [ "$want" = "fail" ] && [ "$rc" -eq 0 ]; then
      echo "SELF-TEST FAIL: fixture '$name' expected a non-zero exit but the guard PASSED."
      echo "                A guard that cannot fail on this input is vacuous."
      echo "$out"
      fail_count=$((fail_count + 1)); return
    fi
    if ! printf '%s' "$out" | grep -q "^${token}"; then
      echo "SELF-TEST FAIL: fixture '$name' exited $rc but never printed '${token}' —"
      echo "                the exit code came from a crash, not from a verdict."
      echo "$out"
      fail_count=$((fail_count + 1)); return
    fi
    echo "  ok  $name (want=$want, exit=$rc, verdict=${token%:})"
  }

  echo "self-test: driving the classifier through 4 fixtures (2 must pass, 2 must fail)"

  # 1. Healthy backend — must PASS.
  run_fixture "healthy backend" pass '{
    "meshRemoteClusters": 0, "graceSeconds": 300,
    "services":  [{"ns":"openbao","name":"openbao","global":null,"ageSeconds":50000}],
    "endpoints": [{"ns":"openbao","name":"openbao","ready":1}],
    "routes":    [{"ns":"openbao","name":"bao","hosts":["bao.example.test"],
                   "backends":[{"ns":"openbao","name":"openbao"}]}]}'

  # 2. Unannotated empty backend — must FAIL (hw293 region-B keycloak shape).
  run_fixture "dead backend, no mesh annotation" fail '{
    "meshRemoteClusters": 0, "graceSeconds": 300,
    "services":  [{"ns":"keycloak","name":"keycloak","global":null,"ageSeconds":50000}],
    "endpoints": [{"ns":"keycloak","name":"keycloak","ready":0}],
    "routes":    [{"ns":"keycloak","name":"keycloak","hosts":["auth.example.test"],
                   "backends":[{"ns":"keycloak","name":"keycloak"}]}]}'

  # 3. ClusterMesh stub with NO remote cluster — must FAIL (the #6040 shape:
  #    the Service looks intentional, the annotation is present and correct,
  #    and it is still a black hole).
  run_fixture "mesh stub, 0 remote clusters" fail '{
    "meshRemoteClusters": 0, "graceSeconds": 300,
    "services":  [{"ns":"catalyst-system","name":"catalyst-ui","global":"true","ageSeconds":50000}],
    "endpoints": [{"ns":"catalyst-system","name":"catalyst-ui","ready":0}],
    "routes":    [{"ns":"catalyst-system","name":"catalyst-ui","hosts":["console.example.test"],
                   "backends":[{"ns":"catalyst-system","name":"catalyst-ui"}]}]}'

  # 4. The SAME stub once the mesh is up — must PASS. This is the discriminator
  #    that proves fixture 3 fails on the mesh state and not merely on
  #    "endpoints == 0"; without it the guard would be indistinguishable from
  #    one that bans zero-endpoint Services outright and would fire on every
  #    correctly-meshed 2-region Sovereign.
  run_fixture "mesh stub, 1 remote cluster" pass '{
    "meshRemoteClusters": 1, "graceSeconds": 300,
    "services":  [{"ns":"catalyst-system","name":"catalyst-ui","global":"true","ageSeconds":50000}],
    "endpoints": [{"ns":"catalyst-system","name":"catalyst-ui","ready":0}],
    "routes":    [{"ns":"catalyst-system","name":"catalyst-ui","hosts":["console.example.test"],
                   "backends":[{"ns":"catalyst-system","name":"catalyst-ui"}]}]}'

  echo
  if [ "$fail_count" -ne 0 ]; then
    echo "SELF-TEST FAILED ($fail_count fixture(s))"
    exit 1
  fi
  echo "SELF-TEST PASSED — the classifier passes what must pass and FAILS what must fail."
  exit 0
fi

# ─────────────────────────────────────────────────────────────────────────────
# Live path.
# ─────────────────────────────────────────────────────────────────────────────
command -v kubectl >/dev/null 2>&1 || { echo "kubectl not found" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "python3 not found" >&2; exit 2; }

k() { kubectl "${KUBECTL_ARGS[@]+"${KUBECTL_ARGS[@]}"}" "$@"; }

if ! k version -o json >/dev/null 2>&1 && ! k get --raw /version >/dev/null 2>&1; then
  echo "cannot reach the cluster with the supplied kubeconfig/context" >&2
  exit 2
fi

# ClusterMesh remote-cluster count, read from a cilium agent. `cilium-dbg
# status` prints `ClusterMesh: N/M remote clusters ready`; N is what decides
# whether a global-Service stub has anywhere to go. A cluster with no cilium
# agent (or a cilium too old for cilium-dbg) reports 0, which is the
# conservative answer: it makes stubs count as dead rather than silently
# excusing them.
mesh_remote=0
cilium_pod="$(k -n kube-system get pods -l k8s-app=cilium \
  -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
if [ -n "$cilium_pod" ]; then
  mesh_remote="$(k -n kube-system exec "$cilium_pod" -c cilium-agent -- \
    cilium-dbg status 2>/dev/null | awk -F'[ /]+' '/^ClusterMesh:/ {print $2; exit}' || true)"
  case "$mesh_remote" in ''|*[!0-9]*) mesh_remote=0 ;; esac
fi

# The three collections go to FILES, not argv. A real Sovereign carries a few
# hundred Services and Endpoints; passing those blobs as arguments overruns
# ARG_MAX and execve fails with "Argument list too long". That failure exits
# non-zero, so a guard that treated any non-zero exit as "violation found"
# would report a red that has nothing to do with the cluster — and one that
# piped a failed builder into the classifier would report a JSON crash. Both
# are the same anti-pattern this script exists to catch, so the bundle is
# built to a file, its build is checked, and only then classified.
WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

k get svc -A -o json > "$WORKDIR/svc.json"
k get endpoints -A -o json > "$WORKDIR/eps.json"
k get httproutes.gateway.networking.k8s.io -A -o json > "$WORKDIR/rt.json" 2>/dev/null \
  || echo '{"items":[]}' > "$WORKDIR/rt.json"

if ! MESH_REMOTE="$mesh_remote" GRACE="$GRACE_SECONDS" python3 - \
      "$WORKDIR/svc.json" "$WORKDIR/eps.json" "$WORKDIR/rt.json" \
      > "$WORKDIR/bundle.json" <<'PY'
import json, os, sys
from datetime import datetime, timezone

svc, eps, rt = (json.load(open(a)) for a in sys.argv[1:4])
now = datetime.now(timezone.utc)


def age(ts):
    if not ts:
        return 10 ** 9
    return int((now - datetime.strptime(ts, "%Y-%m-%dT%H:%M:%SZ").replace(
        tzinfo=timezone.utc)).total_seconds())


bundle = {
    "meshRemoteClusters": int(os.environ.get("MESH_REMOTE", 0)),
    "graceSeconds": int(os.environ.get("GRACE", 300)),
    "services": [{
        "ns": s["metadata"]["namespace"],
        "name": s["metadata"]["name"],
        "global": (s["metadata"].get("annotations") or {}).get("service.cilium.io/global"),
        "ageSeconds": age(s["metadata"].get("creationTimestamp")),
    } for s in svc["items"]],
    "endpoints": [{
        "ns": e["metadata"]["namespace"],
        "name": e["metadata"]["name"],
        "ready": sum(len(x.get("addresses") or []) for x in (e.get("subsets") or [])),
    } for e in eps["items"]],
    "routes": [{
        "ns": r["metadata"]["namespace"],
        "name": r["metadata"]["name"],
        "hosts": r["spec"].get("hostnames") or [],
        "backends": [{"ns": b.get("namespace", r["metadata"]["namespace"]), "name": b["name"]}
                     for rule in (r["spec"].get("rules") or [])
                     for b in (rule.get("backendRefs") or [])],
    } for r in rt["items"]],
}
json.dump(bundle, sys.stdout)
PY
then
  echo "failed to build the route/backend bundle from the live cluster" >&2
  exit 2
fi

classify < "$WORKDIR/bundle.json"
