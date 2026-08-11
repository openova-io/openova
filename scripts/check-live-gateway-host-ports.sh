#!/usr/bin/env bash
# check-live-gateway-host-ports.sh — EDGE-side guard: a node-pinned probe must
# be aimed at the gateway that actually owns the hostname (#6140).
#
# WHY THIS EXISTS
#
# A Sovereign runs TWO Cilium Gateways, and they carry DISJOINT hostname sets:
#
#   cilium-gateway-console   catalyst-ui + catalyst-api HTTPRoutes only (#4053)
#   cilium-gateway           every other platform HTTPRoute — keycloak, grafana,
#                            gitea, harbor, openbao, powerdns, newapi, ...
#
# Under hostNetwork two Gateways cannot both bind node:443, so per #4706 the
# console gateway moves to unprivileged host ports and the shared one keeps the
# privileged pair:
#
#   CONSOLE_GATEWAY_HTTPS_PORT   8443   (HTTP 8080)
#   SOVEREIGN_GATEWAY_HTTPS_PORT  443   (HTTP   80)
#
# (infra/providers/_shared/cloudinit-control-plane.tftpl; mirrored as
# consoleHostPortHTTPS in
# products/catalyst/bootstrap/api/internal/handler/post_handover_gateway_elb.go
# and as the tofu console_member_port_* the console ELB forwards public :443 to.
# Neither port is a NodePort — both are durable hostPorts on the hostNetwork
# cilium-envoy pods, §854-clean.)
#
# BOTH listeners terminate the SAME `*.<fqdn>` wildcard certificate. So a probe
# pinned at node:443 carrying `Host: console.<fqdn>` completes the TLS handshake
# and only THEN meets an envoy that has no matching vhost. The answer is a
# routeless 404 with a ZERO-byte body and NO x-envoy-upstream-service-time.
#
# That 404 is STRUCTURAL. It is returned on a perfectly healthy Sovereign, in
# EVERY region, forever. It is a probe that cannot pass — the inverse of a guard
# that cannot fail, and just as useless. Measured on hw293 (`hw293.omantel.biz`,
# dep a0077ba47e3720e5) 2026-08-11, read-only:
#
#   console.  node-A:443    404  0 bytes  no ust     <- structural, NOT a defect
#   console.  node-B:443    404  0 bytes  no ust     <- same, in the other region
#   console.  node-A:8443   200  1063 b   ust present <- the feature is UP
#   console.  node-B:8443   503  19 bytes no ust     <- the real fault (#6040)
#   gitea.    node-A:443    303           ust present <- shared gateway is fine
#
# The 404 appeared in the HEALTHY region too. That is the discriminator that
# separates it from the #6040 fan-out and from the #5341 SNI collision. It was
# nonetheless recorded as fan-out residue against #6114, so the instrument —
# not the platform — corrupted the ledger.
#
# THE INVARIANT
#
# For every hostname, the gateway that OWNS it must serve it on ITS OWN host
# port. What the OTHER gateway's port answers for that hostname is irrelevant
# and must never raise a defect.
#
#   served on its own gateway's port                  -> SERVING
#   routeless 404 on its own gateway's port           -> NO-ROUTE      (FAIL)
#   503 no-healthy-upstream on its own gateway's port -> NO-UPSTREAM   (FAIL, #6040)
#   no listener / TLS refused on its own port         -> NO-LISTENER   (FAIL)
#   anything at all on the OTHER gateway's port       -> EXPECTED-ISOLATION
#
# Exit 0 = every hostname serves on its own gateway. 1 = at least one does not.
# 2 = setup error.
#
# Usage:
#   scripts/check-live-gateway-host-ports.sh --fqdn <sovereign-fqdn> --node <ip> [--node <ip>...]
#   scripts/check-live-gateway-host-ports.sh --fqdn hw293.omantel.biz --node 212.72.24.43
#   scripts/check-live-gateway-host-ports.sh --bundle <file.json>  # re-classify a saved reading
#   scripts/check-live-gateway-host-ports.sh --self-test        # no cluster needed
#
# --self-test is a VACUITY CHECK, not decoration. A classifier that has only
# ever been observed passing proves nothing, so the self-test drives it through
# fixtures that MUST pass and fixtures that MUST fail — including the exact
# hw293 readings above — and fails if any must-fail fixture comes back clean.
#
# This guard is READ-ONLY: it issues GET requests from outside the cluster and
# touches no Kubernetes object. It never uses, needs or suggests a NodePort.
#
# 🛑 This repo is PUBLIC. The script prints hostnames, ports, status codes and
#    header presence only. It sends no credential and prints no response body.

set -euo pipefail

FQDN=""
NODES=()
CONSOLE_PORT="${CONSOLE_PORT:-8443}"
SHARED_PORT="${SHARED_PORT:-443}"
SELF_TEST_ONLY=0
BUNDLE_FILE=""
TIMEOUT="${TIMEOUT:-12}"

# Hostname prefixes owned by the dedicated console Gateway (#4053). Every other
# `*.<fqdn>` host belongs to the shared gateway. `marketplace.` rides the
# console gateway with catalyst-ui: it is served by the same catalyst-ui
# HTTPRoute family and was measured on :8443 (200) / :443 (404) on hw293.
CONSOLE_HOSTS_DEFAULT="console api marketplace"
# Shared-gateway hosts used as the CONTROL. These share every suspect property
# with the console hosts — same node, same hostNetwork cilium-envoy, same
# wildcard certificate, same node host port class — and differ ONLY in which
# Gateway owns them. If a "the node's host port is broken" theory were right,
# these would fail too.
SHARED_HOSTS_DEFAULT="gitea auth grafana"

while [ $# -gt 0 ]; do
  case "$1" in
    --fqdn)          FQDN="$2";           shift 2 ;;
    --node)          NODES+=("$2");       shift 2 ;;
    --console-port)  CONSOLE_PORT="$2";   shift 2 ;;
    --shared-port)   SHARED_PORT="$2";    shift 2 ;;
    --console-hosts) CONSOLE_HOSTS_DEFAULT="$2"; shift 2 ;;
    --shared-hosts)  SHARED_HOSTS_DEFAULT="$2";  shift 2 ;;
    --timeout)       TIMEOUT="$2";        shift 2 ;;
    --bundle)        BUNDLE_FILE="$2";    shift 2 ;;
    --self-test)     SELF_TEST_ONLY=1;    shift ;;
    -h|--help)
      sed -n '2,95p' "$0" | sed 's/^# \{0,1\}//'
      exit 0 ;;
    *) echo "Unknown arg: $1" >&2
       echo "Usage: $0 --fqdn <sovereign-fqdn> --node <ip> [--node <ip>...] [--self-test]" >&2
       exit 2 ;;
  esac
done

# ─────────────────────────────────────────────────────────────────────────────
# The classifier. Reads ONE normalized JSON bundle on stdin so the live path and
# the self-test path exercise the SAME code — a self-test that ran a
# reimplementation would prove nothing about the guard that actually runs.
#
# bundle = {
#   "consolePort": <int>, "sharedPort": <int>,
#   "probes": [{"host","node","port","owner","code","bytes","ust","connected"}]
# }
#   owner     — "console" | "shared": which Gateway owns this hostname
#   code      — HTTP status, or 0 when the connection never completed
#   ust       — true when x-envoy-upstream-service-time was present
#   connected — false when TCP/TLS failed (no listener for that SNI)
# ─────────────────────────────────────────────────────────────────────────────
read -r -d '' CLASSIFY_PY <<'PY' || true
import json, sys

b = json.load(sys.stdin)
console_port = int(b.get("consolePort", 8443))
shared_port = int(b.get("sharedPort", 443))

rows = []
for p in b.get("probes", []):
    owner = p.get("owner")
    port = int(p.get("port", 0))
    own_port = console_port if owner == "console" else shared_port
    code = int(p.get("code", 0))
    ust = bool(p.get("ust"))
    connected = bool(p.get("connected", True))
    nbytes = int(p.get("bytes", 0))

    if port != own_port:
        # The other Gateway's host port. Whatever it says about this hostname is
        # a statement about a Gateway that never claimed it (#4053). It carries
        # no information about the feature and must never raise a defect.
        verdict = "EXPECTED-ISOLATION"
        if not connected:
            why = "no listener for this SNI on the peer gateway's port"
        elif code == 404 and nbytes == 0 and not ust:
            why = "routeless 404 — this gateway does not own the hostname"
        else:
            why = f"answered {code} on a port that does not own this hostname"
    elif not connected:
        verdict, why = "NO-LISTENER", "TCP/TLS did not complete on the owning gateway's port"
    elif code == 503 and not ust:
        verdict, why = "NO-UPSTREAM", "envoy 503 no-healthy-upstream — route exists, backend does not (#6040)"
    elif code == 404 and not ust:
        verdict, why = "NO-ROUTE", "routeless 404 on the OWNING gateway — the hostname is not advertised here"
    else:
        verdict, why = "SERVING", f"{code} with x-envoy-upstream-service-time={'present' if ust else 'absent'}"

    rows.append((verdict, p.get("host", "-"), p.get("node", "-"), port, owner, why))

order = {"NO-ROUTE": 0, "NO-UPSTREAM": 1, "NO-LISTENER": 2, "EXPECTED-ISOLATION": 3, "SERVING": 4}
rows.sort(key=lambda r: (order[r[0]], r[1], r[2], r[3]))

bad = [r for r in rows if r[0] in ("NO-ROUTE", "NO-UPSTREAM", "NO-LISTENER")]
isolated = [r for r in rows if r[0] == "EXPECTED-ISOLATION"]

print(f"console gateway host port: {console_port}    shared gateway host port: {shared_port}")
print(f"probes examined: {len(rows)}")
for k in ("SERVING", "EXPECTED-ISOLATION", "NO-ROUTE", "NO-UPSTREAM", "NO-LISTENER"):
    print(f"  {k:<19} {sum(1 for r in rows if r[0] == k)}")
print()
for v, h, n, port, owner, why in rows:
    if v == "SERVING":
        continue
    print(f"  {v:<19} {h}  node={n} :{port} owner={owner}  {why}")

if isolated and not bad:
    print()
    print(f"NOTE: {len(isolated)} probe(s) hit the gateway that does not own the hostname.")
    print("      Those readings are STRUCTURAL (#4053/#4706), not defects. A 0-byte")
    print("      404 there is returned by a fully healthy Sovereign in every region.")

if bad:
    print()
    print(f"FAIL: {len(bad)} hostname/port pair(s) cannot serve on their OWN gateway.")
    print("      Only these are real. A verdict recorded against the peer gateway's")
    print("      host port is measurement noise, not a result.")
    sys.exit(1)

print()
print("PASS: every hostname serves on the gateway that owns it.")
PY

# `python3 -c` (not `python3 -`) is load-bearing: a heredoc-fed `python3 -`
# consumes stdin itself, so the classifier would parse the empty string instead
# of the bundle and its non-zero exit would look like a caught violation.
classify() { python3 -c "$CLASSIFY_PY"; }

# ─────────────────────────────────────────────────────────────────────────────
# Self-test — the vacuity check.
# ─────────────────────────────────────────────────────────────────────────────
if [ "$SELF_TEST_ONLY" = "1" ]; then
  fail_count=0

  # An exit code alone is NOT enough evidence: a crashed classifier also exits
  # non-zero, which would score every must-fail fixture "ok" while proving
  # nothing. Each fixture must ALSO emit the verdict token that only the
  # classifier's own decision path can print.
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

  echo "self-test: driving the classifier through 6 fixtures (3 must pass, 3 must fail)"

  # 1. THE REGRESSION FIXTURE — the exact hw293 region-A reading that was
  #    recorded as a defect. console. answers 200 on :8443 (its own gateway) and
  #    a 0-byte routeless 404 on :443 (the shared gateway). This is a HEALTHY
  #    Sovereign and MUST pass. Before #6140 the walk rule was "a 404 from a
  #    node-pinned probe is a dead route", which fails this input.
  run_fixture "hw293 region A: console 200 on :8443, structural 404 on :443" pass '{
    "consolePort": 8443, "sharedPort": 443,
    "probes": [
      {"host":"console.hw293.omantel.biz","node":"212.72.24.43","port":8443,"owner":"console","code":200,"bytes":1063,"ust":true,"connected":true},
      {"host":"console.hw293.omantel.biz","node":"212.72.24.43","port":443,"owner":"console","code":404,"bytes":0,"ust":false,"connected":true}
    ]}'

  # 2. THE CONTROL — a shared-gateway hostname on the SAME node and the SAME
  #    host-port class, differing only in which Gateway owns it. It must stay
  #    green, which is what proves fixture 1 passes on OWNERSHIP and not merely
  #    on "ignore every 404".
  run_fixture "control: gitea 303 on its own :443, refused on :8443" pass '{
    "consolePort": 8443, "sharedPort": 443,
    "probes": [
      {"host":"gitea.hw293.omantel.biz","node":"212.72.24.43","port":443,"owner":"shared","code":303,"bytes":38,"ust":true,"connected":true},
      {"host":"gitea.hw293.omantel.biz","node":"212.72.24.43","port":8443,"owner":"shared","code":0,"bytes":0,"ust":false,"connected":false}
    ]}'

  # 3. THE DISCRIMINATOR — the same 0-byte routeless 404, but now on the
  #    hostname's OWN gateway port. This one IS a real defect and MUST fail.
  #    Without it the guard would be indistinguishable from one that ignores
  #    every 404, and would go blind to a genuinely unadvertised hostname.
  run_fixture "routeless 404 on the OWNING gateway port" fail '{
    "consolePort": 8443, "sharedPort": 443,
    "probes": [
      {"host":"console.hw293.omantel.biz","node":"212.72.24.43","port":8443,"owner":"console","code":404,"bytes":0,"ust":false,"connected":true}
    ]}'

  # 4. hw293 region B — 503 no-healthy-upstream on the OWNING gateway port.
  #    The #6040 fan-out is a real fault and must stay red; the guard must not
  #    have been widened into blindness by fixtures 1 and 2.
  run_fixture "hw293 region B: 503 no-healthy-upstream on :8443" fail '{
    "consolePort": 8443, "sharedPort": 443,
    "probes": [
      {"host":"console.hw293.omantel.biz","node":"212.72.24.25","port":8443,"owner":"console","code":503,"bytes":19,"ust":false,"connected":true}
    ]}'

  # 5. No listener at all on the owning gateway's port — the gateway never came
  #    up. Must fail.
  run_fixture "no listener on the owning gateway port" fail '{
    "consolePort": 8443, "sharedPort": 443,
    "probes": [
      {"host":"console.hw293.omantel.biz","node":"212.72.24.43","port":8443,"owner":"console","code":0,"bytes":0,"ust":false,"connected":false}
    ]}'

  # 6. A 401 from the owning gateway is SERVING — envoy reached an upstream and
  #    the upstream demanded auth. Guards that treat any non-2xx as dead have
  #    produced false reds on every authenticated surface.
  run_fixture "401 from the owning gateway is SERVING" pass '{
    "consolePort": 8443, "sharedPort": 443,
    "probes": [
      {"host":"api.hw293.omantel.biz","node":"212.72.24.43","port":8443,"owner":"console","code":401,"bytes":54,"ust":true,"connected":true}
    ]}'

  echo
  if [ "$fail_count" -ne 0 ]; then
    echo "SELF-TEST FAILED ($fail_count fixture(s))"
    exit 1
  fi
  echo "SELF-TEST PASSED — the classifier passes what must pass and FAILS what must fail."
  exit 0
fi

# ─────────────────────────────────────────────────────────────────────────────
# Offline path — re-classify a reading captured earlier. Same classifier, so a
# saved bundle and a live sweep can never disagree about the same numbers.
# ─────────────────────────────────────────────────────────────────────────────
if [ -n "$BUNDLE_FILE" ]; then
  command -v python3 >/dev/null 2>&1 || { echo "python3 not found" >&2; exit 2; }
  [ -r "$BUNDLE_FILE" ] || { echo "cannot read bundle: $BUNDLE_FILE" >&2; exit 2; }
  classify < "$BUNDLE_FILE"
  exit $?
fi

# ─────────────────────────────────────────────────────────────────────────────
# Live path.
# ─────────────────────────────────────────────────────────────────────────────
command -v curl >/dev/null 2>&1 || { echo "curl not found" >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "python3 not found" >&2; exit 2; }

[ -n "$FQDN" ] || { echo "--fqdn is required (e.g. --fqdn hw293.omantel.biz)" >&2; exit 2; }
[ "${#NODES[@]}" -gt 0 ] || { echo "--node is required at least once (node IP to pin with --resolve)" >&2; exit 2; }

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

# Probe one (host, node, port). Fresh TCP every time: HTTP/2 multiplexes onto a
# single socket, so sequential retries over one connection cannot resample a
# round-robin — they re-ask the same backend. --http1.1 --no-keepalive forces a
# new connection, which is the only way to sample both regions' ELB members.
#
# A connection failure is RETRIED (CONNECT_ATTEMPTS) before it is believed. This
# is not defensive padding: the first live sweep of this guard against hw293
# returned NO-LISTENER on all 12 probes — including hostnames that answered 200
# and 303 seconds earlier and seconds later — when a burst of rapid
# --no-keepalive connections was refused at the edge. A single-shot probe turns
# that burst into a fully red report against a healthy Sovereign, which is the
# same false-verdict failure this guard exists to stop. Only the CONNECTION is
# retried; an HTTP status is taken on the first answer.
CONNECT_ATTEMPTS="${CONNECT_ATTEMPTS:-3}"

probe() {
  local host="$1" node="$2" port="$3" owner="$4" code bytes ust connected attempt
  connected=false; code=0; bytes=0; ust=false
  attempt=0
  while [ "$attempt" -lt "$CONNECT_ATTEMPTS" ]; do
    attempt=$((attempt + 1))
    if curl -sS -k --http1.1 --no-keepalive --max-time "$TIMEOUT" \
         --resolve "$host:$port:$node" \
         -D "$WORKDIR/h" -o "$WORKDIR/b" \
         "https://$host:$port/" -w '%{http_code} %{size_download}' \
         > "$WORKDIR/w" 2>/dev/null; then
      connected=true
      code="$(cut -d' ' -f1 < "$WORKDIR/w")"
      bytes="$(cut -d' ' -f2 < "$WORKDIR/w")"
      if grep -qi '^x-envoy-upstream-service-time' "$WORKDIR/h" 2>/dev/null; then ust=true; else ust=false; fi
      break
    fi
    sleep 1
  done
  printf '{"host":"%s","node":"%s","port":%s,"owner":"%s","code":%s,"bytes":%s,"ust":%s,"connected":%s,"attempts":%s}' \
    "$host" "$node" "$port" "$owner" "${code:-0}" "${bytes:-0}" "$ust" "$connected" "$attempt"
}

{
  printf '{"consolePort":%s,"sharedPort":%s,"probes":[' "$CONSOLE_PORT" "$SHARED_PORT"
  first=1
  for node in "${NODES[@]}"; do
    for owner in console shared; do
      if [ "$owner" = console ]; then hosts="$CONSOLE_HOSTS_DEFAULT"; else hosts="$SHARED_HOSTS_DEFAULT"; fi
      for prefix in $hosts; do
        # Probe BOTH gateway ports for every hostname. The peer-port reading is
        # what makes the EXPECTED-ISOLATION classification evidence-backed
        # rather than assumed, and it is what a walker would otherwise
        # mis-record as a dead route.
        for port in "$CONSOLE_PORT" "$SHARED_PORT"; do
          [ "$first" = 1 ] || printf ','
          first=0
          probe "$prefix.$FQDN" "$node" "$port" "$owner"
        done
      done
    done
  done
  printf ']}'
} > "$WORKDIR/bundle.json"

python3 -c 'import json,sys; json.load(open(sys.argv[1]))' "$WORKDIR/bundle.json" 2>/dev/null || {
  echo "failed to build a valid probe bundle" >&2; exit 2; }

classify < "$WORKDIR/bundle.json"
