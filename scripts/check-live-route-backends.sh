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
# THE INVARIANT — PART 1, BACKENDS
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
# THE INVARIANT — PART 2, LISTENERS AND REGION SYMMETRY (UAT rows 87/90/95)
#
# Part 1 joins a route to its BACKEND. It is blind to the two ways a per-Org
# application hostname fails on the SAME front door, both measured on hw293:
#
#   * The route exists and its backend is healthy, but the parent Gateway
#     carries no listener whose hostname admits it. Gateway API then reports
#     `Accepted=False  NoMatchingListenerHostname` and envoy resets the TLS
#     handshake before any HTTP status exists — so a 503-hunting probe sees
#     nothing at all. Namespace `g7doora` on hw293 held a healthy
#     `bp-stalwart-tenant` and a `mail.g7doora.omani.rest` HTTPRoute while
#     `kube-system/cilium-gateway-console` carried 14 listeners covering four
#     OTHER Organizations and none for that one. Part 1 scores that route
#     SERVING, because its backend genuinely serves — nothing can reach it.
#
#   * The route is absent from ONE region. Rows 87/90/95 are exactly this:
#     `wordpress.<orgslug>.<pool-tld>` renders and is publicly trusted, and
#     4-to-7 of every 12 fresh TCP connections are `Connection reset by peer`,
#     because region B holds zero routes for that hostname while the ELB
#     round-robins :443 across both regions' cilium-envoy. A single-cluster
#     scan CANNOT see this: a region with no route for a hostname produces no
#     row, no verdict, and a clean PASS. Absence is invisible by construction,
#     so the peer region's route inventory has to be read too.
#
# Hence:
#
#   * every hostname on an HTTPRoute must be admitted by at least one listener
#     of at least one of its parent Gateways, honouring Gateway API wildcard
#     semantics (`*.a.b` admits `x.a.b` and `x.y.a.b`, never the bare `a.b`)
#     and any `sectionName` / `port` pin on the parentRef -> else NO-LISTENER
#   * a parentRef naming a Gateway that does not exist here    -> NO-GATEWAY
#   * with --peer-kubeconfig / --peer-context, a hostname advertised in one
#     region and not the other                                 -> REGION-ASYMMETRY
#
# Exit 0 = every advertised hostname is admitted by a listener, backed by a
# reachable endpoint, and (when a peer is supplied) served by both regions.
# 1 = at least one advertised hostname cannot serve. 2 = setup error.
#
# Usage:
#   scripts/check-live-route-backends.sh                        # current context
#   scripts/check-live-route-backends.sh --kubeconfig <path>
#   scripts/check-live-route-backends.sh --context <ctx>
#   scripts/check-live-route-backends.sh --kubeconfig <a> --peer-kubeconfig <b>
#   scripts/check-live-route-backends.sh --self-test            # no cluster needed
#
# --self-test is a VACUITY CHECK, not decoration. A guard that has only ever
# been observed passing is worthless, so the self-test drives the classifier
# through fixtures — some that MUST pass and some that MUST fail — and fails
# the script if any must-fail fixture comes back clean. Every must-fail
# fixture is paired with a CONTROL that shares its suspect property and must
# stay green, so a verdict can never be read as "this classifier bans a
# shape" when it should be reading one specific fact about that shape.
#
# 🛑 This repo is PUBLIC. The script prints object names, namespaces, hostnames
#    and counts only. It never reads Secret data and never prints a value.

set -euo pipefail

# The shared admission module lives next to this script, so the classifier can
# import it no matter what directory the guard is invoked from.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export GUARD_LIB="$SCRIPT_DIR/lib"

KUBECTL_ARGS=()
PEER_ARGS=()
PEER_SUPPLIED=0
SELF_TEST_ONLY=0
GRACE_SECONDS="${GRACE_SECONDS:-300}"

while [ $# -gt 0 ]; do
  case "$1" in
    --kubeconfig)      KUBECTL_ARGS+=(--kubeconfig "$2"); shift 2 ;;
    --context)         KUBECTL_ARGS+=(--context "$2");    shift 2 ;;
    --peer-kubeconfig) PEER_ARGS+=(--kubeconfig "$2"); PEER_SUPPLIED=1; shift 2 ;;
    --peer-context)    PEER_ARGS+=(--context "$2");    PEER_SUPPLIED=1; shift 2 ;;
    --grace-seconds)   GRACE_SECONDS="$2";                shift 2 ;;
    --self-test)       SELF_TEST_ONLY=1;                  shift ;;
    -h|--help)
      sed -n '2,118p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "Unknown arg: $1" >&2
       echo "Usage: $0 [--kubeconfig <path>] [--context <ctx>]" >&2
       echo "          [--peer-kubeconfig <path>] [--peer-context <ctx>]" >&2
       echo "          [--grace-seconds N] [--self-test]" >&2
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
#   "routes":    [{"ns","name","hosts":[..],
#                  "backends":[{"ns","name"}],
#                  "parents": [{"ns","name","sectionName","port"}]}],
#   "gateways":  [{"ns","name","listeners":[{"name","hostname","port"}]}],
#   "peerHosts": [<hostname>, ..]   # present only when a peer region was read
# }
#
# `gateways` and `peerHosts` are OPTIONAL keys and each gates its own pass.
# That is not a fail-open: the LIVE path always supplies `gateways` and exits
# 2 if the cluster will not serve them, and it prints a loud NOTICE when no
# peer was supplied. The keys are optional so that the four original backend
# fixtures keep exercising the backend classifier alone — they are the
# controls proving this change did not disturb it.
# ─────────────────────────────────────────────────────────────────────────────
read -r -d '' CLASSIFY_PY <<'PY' || true
import json, os, sys

# Hostname admission + the route/listener correspondence pass live in
# scripts/lib/gateway_route_admission.py, shared with the SOURCE-side guard
# scripts/check-chart-route-listener-correspondence.sh (#6140). They were
# inline here until that guard needed the identical rule from `helm template`
# output instead of from a cluster. A second copy of the wildcard semantics
# would have been a copy free to drift, and the drift would have been silent —
# both guards would have kept passing, each against its own idea of the spec.
sys.path.insert(0, os.environ["GUARD_LIB"])
from gateway_route_admission import listener_admits, correspondence_rows

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

# ── Pass 2: listener coverage. A hostname nothing listens for is not a 503,
# it is a TLS reset — which is why the backend pass above cannot see it.
gws = {(g["ns"], g["name"]): g for g in b.get("gateways", [])}
listener_pass = "gateways" in b
# `local_hosts` is needed by the region-symmetry pass below even when the
# listener pass is skipped, so it is collected from the SAME shared helper —
# which returns it alongside the rows — rather than from a second walk here.
hostrows, local_hosts = correspondence_rows(b.get("routes", []), b.get("gateways", []))
if not listener_pass:
    # No `gateways` key means the caller asked for the backend pass alone (the
    # four original backend fixtures). Every row would be NO-GATEWAY against an
    # empty Gateway set, which is not a verdict anyone asked for — but the
    # hostnames still have to reach pass 3. So keep the hosts, drop the rows.
    hostrows = []

# ── Pass 3: region symmetry. Both regions' nodes sit in ONE round-robin pool,
# so a hostname either region cannot route is a coin-flip failure for the
# customer — the shape behind UAT rows 87/90/95.
peer_known = "peerHosts" in b
peer_hosts = set(b.get("peerHosts") or [])
asym = []
if peer_known:
    for h in sorted(local_hosts - peer_hosts):
        asym.append(("REGION-ASYMMETRY", h, "(this region only)",
                     "advertised here, absent from the peer region"))
    for h in sorted(peer_hosts - local_hosts):
        asym.append(("REGION-ASYMMETRY", h, "(peer region only)",
                     "advertised by the peer region, absent here"))

hostbad = [r for r in hostrows if r[0] != "SERVING"] + asym
horder = {"NO-GATEWAY": 0, "NO-LISTENER": 1, "REGION-ASYMMETRY": 2, "SERVING": 3}
hostrows.sort(key=lambda r: (horder[r[0]], r[1], r[2]))

print(f"ClusterMesh remote clusters ready: {mesh}")
print(f"HTTPRoute backendRefs examined:    {len(rows)}")
print(f"  SERVING       {sum(1 for r in rows if r[0] == 'SERVING')}")
print(f"  PENDING       {len(pending)}  (younger than {grace}s — reported, not counted)")
print(f"  DEAD-BACKEND  {len(bad)}")
if listener_pass:
    print(f"HTTPRoute hostnames examined:      {len(hostrows)}  "
          f"(against {len(gws)} Gateway(s))")
    print(f"  SERVING       {sum(1 for r in hostrows if r[0] == 'SERVING')}")
    print(f"  NO-LISTENER   {sum(1 for r in hostrows if r[0] == 'NO-LISTENER')}")
    print(f"  NO-GATEWAY    {sum(1 for r in hostrows if r[0] == 'NO-GATEWAY')}")
else:
    print("HTTPRoute hostnames examined:      skipped (no `gateways` in bundle)")
if peer_known:
    print(f"Region symmetry:                   {len(peer_hosts)} peer hostname(s), "
          f"{len(asym)} asymmetric")
else:
    print("Region symmetry:                   NOT CHECKED — no peer region supplied.")
    print("    A region that holds ZERO routes for a hostname produces no row and no")
    print("    verdict here, so THIS RUN CANNOT SEE rows 87/90/95's defect. Pass")
    print("    --peer-kubeconfig / --peer-context to close that hole.")
print()
for v, h, be, why in rows:
    if v == "SERVING":
        continue
    print(f"  {v:<17} host={h:<40} backend={be:<44} {why}")
for v, h, who, why in hostrows + asym:
    if v == "SERVING":
        continue
    print(f"  {v:<17} host={h:<40} route={who:<44} {why}")

if bad or hostbad:
    print()
    if bad:
        print(f"FAIL: {len(bad)} advertised backendRef(s) cannot serve. Every request this")
        print("      Gateway round-robins onto this region for those hostnames returns")
        print("      envoy 503 `no healthy upstream`.")
    if hostbad:
        print(f"FAIL: {len(hostbad)} advertised hostname(s) have no way to be served here.")
        print("      A hostname no listener admits is not a 503 — envoy resets the TLS")
        print("      handshake before any HTTP status exists, so a status-code probe")
        print("      sees nothing wrong. A hostname only one region routes is a")
        print("      coin flip for every customer behind the shared front door.")
    print("      Any UAT verdict recorded against these hostnames from this region")
    print("      is measurement noise, not a result.")
    sys.exit(1)

print()
print("PASS: every advertised backendRef has a path to at least one endpoint,")
if listener_pass:
    print("      and every advertised hostname is admitted by a parent listener.")
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

  echo "self-test: driving the classifier through 11 fixtures (6 must pass, 5 must fail)"

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

  # ───────────────────────────────────────────────────────────────────────────
  # Listener coverage (UAT rows 87/90/95, and the g7doora shape behind 234).
  #
  # Every fixture below carries a HEALTHY backend, so a verdict can only come
  # from the listener pass. Org hostnames use the per-Org pool TLDs and TWO
  # DIFFERENT ones, because a Sovereign assigns pool TLDs per Org — a fixture
  # set that shared one TLD would pass a classifier that hardcoded it.
  # ───────────────────────────────────────────────────────────────────────────

  # 5. CONTROL for 6/7/8 — a per-Org app hostname on the shared console
  #    Gateway, admitted by that Org's wildcard listener. Shares every suspect
  #    property of the failing fixtures (same Gateway, same route shape, same
  #    per-Org hostname depth) and MUST stay green. Without it, a classifier
  #    that simply rejected per-Org hostnames would score 6/7/8 "caught".
  run_fixture "per-Org app host admitted by the Org wildcard listener" pass '{
    "meshRemoteClusters": 0, "graceSeconds": 300,
    "services":  [{"ns":"walkone","name":"wordpress","global":null,"ageSeconds":50000}],
    "endpoints": [{"ns":"walkone","name":"wordpress","ready":1}],
    "gateways":  [{"ns":"kube-system","name":"cilium-gateway-console","listeners":[
                    {"name":"apex-https","hostname":"*.sov.example.test","port":443},
                    {"name":"walkone-https","hostname":"*.walkone.omani.homes","port":443}]}],
    "routes":    [{"ns":"walkone","name":"app-wordpress-hostroute",
                   "hosts":["wordpress.walkone.omani.homes"],
                   "backends":[{"ns":"walkone","name":"wordpress"}],
                   "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

  # 6. The g7doora shape — the Gateway carries OTHER Organizations wildcards
  #    and none for this one. Backend is healthy; the customer still cannot
  #    reach it, and envoy resets TLS rather than answering 503.
  run_fixture "per-Org app host with no listener on the shared Gateway" fail '{
    "meshRemoteClusters": 0, "graceSeconds": 300,
    "services":  [{"ns":"g7doora","name":"bp-stalwart-tenant","global":null,"ageSeconds":50000}],
    "endpoints": [{"ns":"g7doora","name":"bp-stalwart-tenant","ready":1}],
    "gateways":  [{"ns":"kube-system","name":"cilium-gateway-console","listeners":[
                    {"name":"apex-https","hostname":"*.sov.example.test","port":443},
                    {"name":"walkone-https","hostname":"*.walkone.omani.homes","port":443},
                    {"name":"walktwo-https","hostname":"*.walktwo.omani.trade","port":443}]}],
    "routes":    [{"ns":"g7doora","name":"app-mail-hostroute",
                   "hosts":["mail.g7doora.omani.rest"],
                   "backends":[{"ns":"g7doora","name":"bp-stalwart-tenant"}],
                   "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

  # 7. parentRef naming a Gateway that does not exist in this region.
  run_fixture "route parented to an absent Gateway" fail '{
    "meshRemoteClusters": 0, "graceSeconds": 300,
    "services":  [{"ns":"walktwo","name":"wordpress","global":null,"ageSeconds":50000}],
    "endpoints": [{"ns":"walktwo","name":"wordpress","ready":1}],
    "gateways":  [],
    "routes":    [{"ns":"walktwo","name":"app-wordpress-hostroute",
                   "hosts":["wordpress.walktwo.omani.trade"],
                   "backends":[{"ns":"walktwo","name":"wordpress"}],
                   "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

  # 8. The off-by-one that a naive `endswith` gets wrong in BOTH directions:
  #    `*.walkone.omani.homes` must NOT admit the bare apex it wildcards.
  run_fixture "wildcard listener must not admit its own apex" fail '{
    "meshRemoteClusters": 0, "graceSeconds": 300,
    "services":  [{"ns":"walkone","name":"wordpress","global":null,"ageSeconds":50000}],
    "endpoints": [{"ns":"walkone","name":"wordpress","ready":1}],
    "gateways":  [{"ns":"kube-system","name":"cilium-gateway-console","listeners":[
                    {"name":"walkone-https","hostname":"*.walkone.omani.homes","port":443}]}],
    "routes":    [{"ns":"walkone","name":"apex","hosts":["walkone.omani.homes"],
                   "backends":[{"ns":"walkone","name":"wordpress"}],
                   "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

  # 9. CONTROL for 8 — the SAME wildcard must admit a deeper host, because
  #    Gateway API wildcards are suffix matches, not single-label matches.
  #    Paired with 8 this pins the rule from both sides; a classifier that
  #    passed one and failed the other would be caught here.
  run_fixture "wildcard listener admits a deeper sub-host" pass '{
    "meshRemoteClusters": 0, "graceSeconds": 300,
    "services":  [{"ns":"walkone","name":"wordpress","global":null,"ageSeconds":50000}],
    "endpoints": [{"ns":"walkone","name":"wordpress","ready":1}],
    "gateways":  [{"ns":"kube-system","name":"cilium-gateway-console","listeners":[
                    {"name":"walkone-https","hostname":"*.walkone.omani.homes","port":443}]}],
    "routes":    [{"ns":"walkone","name":"deep","hosts":["a.b.walkone.omani.homes"],
                   "backends":[{"ns":"walkone","name":"wordpress"}],
                   "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

  # 10. Rows 87/90/95 exactly — both regions sit in ONE round-robin pool and
  #     the peer holds no route for the purchased app hostname, so a fraction
  #     of every customer visit is reset. The local region is FLAWLESS here:
  #     healthy backend, admitting listener, nothing a single-cluster scan
  #     could object to. Only the peer inventory makes it visible.
  run_fixture "hostname served here, absent from the peer region" fail '{
    "meshRemoteClusters": 1, "graceSeconds": 300,
    "services":  [{"ns":"walkone","name":"wordpress","global":null,"ageSeconds":50000}],
    "endpoints": [{"ns":"walkone","name":"wordpress","ready":1}],
    "gateways":  [{"ns":"kube-system","name":"cilium-gateway-console","listeners":[
                    {"name":"walkone-https","hostname":"*.walkone.omani.homes","port":443}]}],
    "peerHosts": ["console.sov.example.test"],
    "routes":    [{"ns":"walkone","name":"app-wordpress-hostroute",
                   "hosts":["wordpress.walkone.omani.homes"],
                   "backends":[{"ns":"walkone","name":"wordpress"}],
                   "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

  # 11. CONTROL for 10 — the same two regions once BOTH advertise the host.
  #     Proves fixture 10 fails on the asymmetry and not merely on the
  #     presence of a peerHosts key.
  run_fixture "hostname served by both regions" pass '{
    "meshRemoteClusters": 1, "graceSeconds": 300,
    "services":  [{"ns":"walkone","name":"wordpress","global":null,"ageSeconds":50000}],
    "endpoints": [{"ns":"walkone","name":"wordpress","ready":1}],
    "gateways":  [{"ns":"kube-system","name":"cilium-gateway-console","listeners":[
                    {"name":"walkone-https","hostname":"*.walkone.omani.homes","port":443}]}],
    "peerHosts": ["wordpress.walkone.omani.homes"],
    "routes":    [{"ns":"walkone","name":"app-wordpress-hostroute",
                   "hosts":["wordpress.walkone.omani.homes"],
                   "backends":[{"ns":"walkone","name":"wordpress"}],
                   "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

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

# Gateways are NOT allowed the empty-on-error fallback that HTTPRoutes get. An
# empty Gateway list makes every route NO-GATEWAY, so a silent fallback would
# turn an unreadable cluster into a wall of false reds; and the opposite
# fallback (skip the pass) would be the fail-open that let #6040 hide for 12
# hours. Unreadable Gateways alongside readable HTTPRoutes is a setup error,
# and it says so.
if ! k get gateways.gateway.networking.k8s.io -A -o json > "$WORKDIR/gw.json" 2>/dev/null; then
  if grep -q '"items": *\[ *\]' "$WORKDIR/rt.json" || ! grep -q '"kind"' "$WORKDIR/rt.json"; then
    echo '{"items":[]}' > "$WORKDIR/gw.json"
  else
    echo "HTTPRoutes are readable but Gateways are not — cannot verify listener" >&2
    echo "coverage, and passing without it would be a fail-open guard." >&2
    exit 2
  fi
fi

# Peer region, when supplied: only the hostname inventory is needed, so this
# is one read and it never touches the peer's Services.
PEER_JSON=""
if [ "$PEER_SUPPLIED" = "1" ]; then
  if ! kubectl "${PEER_ARGS[@]}" get httproutes.gateway.networking.k8s.io -A -o json \
        > "$WORKDIR/peer-rt.json" 2>/dev/null; then
    echo "cannot read HTTPRoutes from the peer region with the supplied kubeconfig/context" >&2
    exit 2
  fi
  PEER_JSON="$WORKDIR/peer-rt.json"
fi

if ! MESH_REMOTE="$mesh_remote" GRACE="$GRACE_SECONDS" PEER_JSON="$PEER_JSON" python3 - \
      "$WORKDIR/svc.json" "$WORKDIR/eps.json" "$WORKDIR/rt.json" "$WORKDIR/gw.json" \
      > "$WORKDIR/bundle.json" <<'PY'
import json, os, sys
from datetime import datetime, timezone

svc, eps, rt, gw = (json.load(open(a)) for a in sys.argv[1:5])
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
        # parentRef.namespace defaults to the ROUTE's namespace per Gateway API,
        # which is why `cilium-gateway-console` must be written with its
        # kube-system namespace by every per-Org producer.
        "parents": [{"ns": p.get("namespace", r["metadata"]["namespace"]),
                     "name": p["name"],
                     "sectionName": p.get("sectionName"),
                     "port": p.get("port")}
                    for p in (r["spec"].get("parentRefs") or [])],
    } for r in rt["items"]],
    "gateways": [{
        "ns": g["metadata"]["namespace"],
        "name": g["metadata"]["name"],
        "listeners": [{"name": l.get("name"),
                       "hostname": l.get("hostname"),
                       "port": l.get("port")}
                      for l in (g["spec"].get("listeners") or [])],
    } for g in gw["items"]],
}

peer = os.environ.get("PEER_JSON") or ""
if peer:
    bundle["peerHosts"] = sorted({h
                                  for r in json.load(open(peer))["items"]
                                  for h in (r["spec"].get("hostnames") or [])})
json.dump(bundle, sys.stdout)
PY
then
  echo "failed to build the route/backend bundle from the live cluster" >&2
  exit 2
fi

classify < "$WORKDIR/bundle.json"
