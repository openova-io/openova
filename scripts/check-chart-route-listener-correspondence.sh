#!/usr/bin/env bash
# check-chart-route-listener-correspondence.sh — SOURCE-side guard (#6140):
# every hostname a chart renders an HTTPRoute for must be admitted by a
# listener the parent Gateway will actually carry. Proven from `helm template`
# output alone. NO CLUSTER.
#
# WHY A SOURCE-SIDE CHECK, WHEN TWO LIVE ONES ALREADY EXIST
# ---------------------------------------------------------
# `scripts/check-live-route-backends.sh` (#6165) and
# `scripts/check-live-gateway-host-ports.sh` (#6143) both answer questions
# about route/listener correspondence — and both need a running Sovereign to
# say anything at all. That is the whole gap: a defect that is fully decided
# by the chart source could only be discovered after a provision, on an
# environment, by a walker. Region B shipped HTTPRoutes with zero admitting
# listeners and nothing in CI could have objected, because nothing in CI ever
# looked at the rendered pair.
#
# This guard looks at the pair. It renders the listener producer and the
# route producers and joins them, so the same class fails in a pull request
# instead of on a customer's TLS handshake.
#
# WHAT MAKES THIS DECIDABLE FROM SOURCE
# -------------------------------------
# No chart in this repo renders a `kind: Gateway`. Both Sovereign Gateways are
# Kustomize manifests under `clusters/_template/sovereign-tls/` whose entire
# `spec.listeners` is a Flux `postBuild` substitution placeholder:
#
#     cilium-gateway.yaml          listeners: ${PARENT_DOMAINS_LISTENERS_YAML}
#     cilium-gateway-console.yaml  listeners: ${CONSOLE_LISTENERS_YAML}
#
# Both values are rendered — as JSON arrays — by ONE chart,
# `platform/sovereign-tls-vars/chart`. So the listener set IS source, and this
# guard reconstructs each Gateway exactly as Flux would: Gateway name and
# namespace read out of the cluster template (never hardcoded here, so a
# rename shows up as NO-GATEWAY rather than as a silent pass), listeners
# parsed out of the rendered ConfigMap.
#
# THE OWNERSHIP RULE IS THE parentRef, NOT A HOSTNAME PREFIX
# ----------------------------------------------------------
# The Sovereign runs TWO Gateways, and which one serves a hostname is the
# thing that produced the false `404` this work started from: the console sits
# on its own isolated Gateway (#4053/#4706, because two Gateways cannot both
# bind node:443 under hostNetwork), the shared gateway serves everything else,
# and BOTH terminate the same wildcard cert — so a probe on the wrong port
# completes TLS and only then meets an envoy with no matching vhost.
#
# A guard that decided ownership by hostname prefix would be wrong in both
# directions on this repo's real data:
#
#   * `marketplace.<fqdn>` is on the CONSOLE gateway
#     (products/catalyst/chart/values.yaml `ingress.gateway.parentRef.name:
#     cilium-gateway-console`, and `marketplace` is in
#     `consoleGateway.hostPrefixes`). A `console.*` prefix rule sends it to
#     the shared gateway, where the `*.<fqdn>` wildcard admits it — a PASS
#     for entirely the wrong reason.
#   * `mcp.<fqdn>` is on the SHARED gateway
#     (products/openova-mcp/chart/values.yaml, and
#     catalyst-edge-routes' deliberately separate `ingress.mcpGateway` key),
#     because #5341 found that a console-gateway wildcard collided with the
#     shared gateway's own `*.<fqdn>` filter chain and 404'd ~50% of every
#     other subdomain. Per-Org `mcp.<slug>.<pool>` IS on the console gateway,
#     but it is stamped at install time by catalyst-api
#     (bootstrap/api/internal/handler/application_parameters.go), not by a
#     chart, so no prefix rule could have known.
#
# So ownership here is resolved the only way that cannot drift: each route's
# own `parentRefs[].name`/`namespace` selects the Gateway, and only THAT
# Gateway's listeners are considered. `sectionName` and `port` on the
# parentRef are honoured as PINS — a route that names a section attaches only
# to the listener of that exact name, and a route that pins a port attaches
# only to listeners on that port.
#
# THE TWO PORT PROFILES
# ---------------------
# `consoleGateway.httpsPort`/`httpPort` are 443/80 on Hetzner and 8443/8080 on
# Huawei (infra/providers/_shared/cloudinit-control-plane.tftpl threads
# CONSOLE_GATEWAY_HTTPS_PORT into bootstrap-kit slot 12a). The port split is
# the mechanism behind the false 404, so correspondence is asserted under BOTH
# profiles — a route that only works on one of them is a defect on the other.
#
# THE TWO LISTENER PRODUCERS
# --------------------------
# Static listeners come from the chart. Per-Organization listeners do not:
# catalyst-api's org_console_tls.go appends `console-https-<slug>` /
# `console-http-<slug>` hostnamed `*.<slug>.<parentDomain>` to the console
# Gateway, reading its ports from the apex `console-https`/`console-http`
# listeners. A source-side guard that ignored that producer would report every
# per-Org hostname as NO-LISTENER; one that merely SKIPPED per-Org hostnames
# would be fail-open. So the minter is MODELLED, and `--self-test` asserts the
# Go producer still declares the shape being modelled — if that source stops
# saying `console-https-` / `"*." + slug`, this guard goes red rather than
# quietly continuing to model something that no longer exists.
#
# WILDCARDS: TWO DIFFERENT RULES THAT LOOK ALIKE
# ----------------------------------------------
# Gateway API listener wildcards are SUFFIX matches of one or MORE labels:
# `*.a.b` admits `x.a.b` AND `x.y.a.b`, never the bare `a.b`. Let's Encrypt
# wildcard SAN certs are a different rule — exactly ONE label — which is why
# the per-prov `*.<fqdn>` listener pair exists at all. Conflating the two is a
# trap in both directions, so admission lives in ONE place,
# `scripts/lib/gateway_route_admission.py`, shared with
# check-live-route-backends.sh rather than re-typed here.
#
# Exit codes:
#   0 — every rendered hostname is admitted by a listener of its parent Gateway.
#   1 — at least one is not (NO-LISTENER), or its Gateway is absent (NO-GATEWAY).
#   2 — setup error (missing helm/python3/pyyaml, unrenderable declared chart,
#       a scenario that rendered fewer routes than declared). Never a pass.
#
# Usage:
#   scripts/check-chart-route-listener-correspondence.sh              # repo mode
#   scripts/check-chart-route-listener-correspondence.sh --self-test  # fixtures
#   scripts/check-chart-route-listener-correspondence.sh --vacuity    # must go RED
#
# 🛑 This repo is PUBLIC. The guard renders charts with fixture hostnames on
#    the canonical test domains only, reads no Secret data, and prints object
#    names and hostnames only.

set -euo pipefail

SELF_TEST=0
VACUITY=0
ROOT="${ROOT:-.}"

while [ $# -gt 0 ]; do
  case "$1" in
    --self-test) SELF_TEST=1; shift ;;
    --vacuity)   VACUITY=1; shift ;;
    --root)      ROOT="$2"; shift 2 ;;
    *) echo "Unknown arg: $1" >&2
       echo "Usage: $0 [--root <repo-root>] [--self-test] [--vacuity]" >&2
       exit 2 ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
export GUARD_LIB="$SCRIPT_DIR/lib"

# ─────────────────────────────────────────────────────────────────────────────
# The classifier. Reads ONE normalized JSON bundle on stdin, exactly like
# check-live-route-backends.sh does, and delegates the verdict to the SHARED
# admission module so the two guards cannot drift apart on the wildcard rule.
#
# bundle = {
#   "profile":  "<label>",
#   "routes":   [{"ns","name","hosts":[..],
#                 "parents":[{"ns","name","sectionName","port"}]}],
#   "gateways": [{"ns","name","listeners":[{"name","hostname","port"}]}]
# }
# ─────────────────────────────────────────────────────────────────────────────
read -r -d '' CLASSIFY_PY <<'PY' || true
import json, os, sys
sys.path.insert(0, os.environ["GUARD_LIB"])
from gateway_route_admission import correspondence_rows

b = json.load(sys.stdin)
rows, _ = correspondence_rows(b.get("routes", []), b.get("gateways", []))

order = {"NO-GATEWAY": 0, "NO-LISTENER": 1, "SERVING": 2}
rows.sort(key=lambda r: (order[r[0]], r[1], r[2]))
bad = [r for r in rows if r[0] != "SERVING"]

print("profile:                      %s" % b.get("profile", "(unnamed)"))
print("Gateways reconstructed:       %d" % len(b.get("gateways", [])))
for g in b.get("gateways", []):
    print("    %s/%s  %d listener(s)" % (g["ns"], g["name"], len(g.get("listeners") or [])))
print("HTTPRoute hostnames examined: %d" % len(rows))
print("  SERVING       %d" % sum(1 for r in rows if r[0] == "SERVING"))
print("  NO-LISTENER   %d" % sum(1 for r in rows if r[0] == "NO-LISTENER"))
print("  NO-GATEWAY    %d" % sum(1 for r in rows if r[0] == "NO-GATEWAY"))
print()
for v, h, who, why in rows:
    if v == "SERVING":
        continue
    print("  %-13s host=%-42s route=%-46s %s" % (v, h, who, why))

if bad:
    print()
    print("FAIL: %d rendered hostname(s) have no listener that admits them." % len(bad))
    print("      NO-LISTENER  — the Gateway exists; its listener set does not cover")
    print("                     this host. Owner: the listener declaration")
    print("                     (consoleGateway.hostPrefixes / parentZones), or the")
    print("                     route's sectionName/port pin. Gateway API answers")
    print("                     Accepted=False NoMatchingListenerHostname and envoy")
    print("                     RESETS the TLS handshake — there is no HTTP status")
    print("                     for a probe to see.")
    print("      NO-GATEWAY   — the parentRef names a Gateway nothing installs here.")
    print("                     Different owner: a bootstrap slot, not a values key.")
    sys.exit(1)

print()
print("PASS: every rendered hostname is admitted by a listener of its parent Gateway.")
PY

classify() { python3 -c "$CLASSIFY_PY"; }

# ─────────────────────────────────────────────────────────────────────────────
# Self-test — fixtures only. No helm, no cluster, no repo render.
#
# Every must-fail fixture is paired with a CONTROL that shares its suspect
# property and must stay GREEN, so a red can never be read as "this classifier
# bans a shape" when it should be reading one specific fact about that shape.
#
# Org hostnames deliberately span THREE pool TLDs (omani.homes / .rest /
# .trade). A fixture set on one TLD would pass a classifier that hardcoded it.
# ─────────────────────────────────────────────────────────────────────────────
if [ "$SELF_TEST" -eq 1 ]; then
  fails=0

  # An exit code alone is not evidence — a crashed classifier also exits
  # non-zero. Each fixture must ALSO print the verdict token that only the
  # decision path can print, and the must-fail ones must print the SPECIFIC
  # verdict expected, so NO-LISTENER can never be scored by a NO-GATEWAY.
  run_fixture() { # run_fixture <name> <pass|fail> <expected-verdict|-> <bundle>
    local name="$1" want="$2" verdict="$3" bundle="$4" out rc
    set +e
    out="$(printf '%s' "$bundle" | classify 2>&1)"; rc=$?
    set -e
    if [ "$want" = "pass" ] && [ "$rc" -ne 0 ]; then
      echo "  FAIL: '$name' expected exit 0, got $rc"; echo "$out"; fails=1; return
    fi
    if [ "$want" = "fail" ] && [ "$rc" -eq 0 ]; then
      echo "  FAIL: '$name' expected non-zero but the guard PASSED —"
      echo "        a guard that cannot fail on this input is vacuous."
      echo "$out"; fails=1; return
    fi
    local token; [ "$want" = "pass" ] && token="PASS:" || token="FAIL:"
    if ! printf '%s' "$out" | grep -q "^${token}"; then
      echo "  FAIL: '$name' exited $rc but never printed '${token}' —"
      echo "        the exit code came from a crash, not from a verdict."
      echo "$out"; fails=1; return
    fi
    if [ "$verdict" != "-" ] && ! printf '%s' "$out" | grep -q "  ${verdict} "; then
      echo "  FAIL: '$name' failed, but not with '${verdict}' — wrong reason."
      echo "$out"; fails=1; return
    fi
    echo "  ok  $name (want=$want${verdict:+, verdict=$verdict})"
  }

  # The two Gateways as this repo really shapes them, Hetzner profile.
  GW_443='"gateways":[
    {"ns":"kube-system","name":"cilium-gateway","listeners":[
      {"name":"https-t99-omani-works","hostname":"*.t99.omani.works","port":443},
      {"name":"http-t99-omani-works","hostname":"*.t99.omani.works","port":80},
      {"name":"https-omani-homes","hostname":"*.omani.homes","port":443},
      {"name":"https-omani-rest","hostname":"*.omani.rest","port":443},
      {"name":"https-omani-trade","hostname":"*.omani.trade","port":443}]},
    {"ns":"kube-system","name":"cilium-gateway-console","listeners":[
      {"name":"console-https","hostname":"console.t99.omani.works","port":443},
      {"name":"api-https","hostname":"api.t99.omani.works","port":443},
      {"name":"marketplace-https","hostname":"marketplace.t99.omani.works","port":443},
      {"name":"console-https-walkone","hostname":"*.walkone.omani.homes","port":443}]}]'

  # The SAME Sovereign on the Huawei profile: the console gateway moved to
  # 8443, and the minter carried the per-Org pair with it.
  GW_8443='"gateways":[
    {"ns":"kube-system","name":"cilium-gateway","listeners":[
      {"name":"https-t99-omani-works","hostname":"*.t99.omani.works","port":443}]},
    {"ns":"kube-system","name":"cilium-gateway-console","listeners":[
      {"name":"console-https","hostname":"console.t99.omani.works","port":8443},
      {"name":"marketplace-https","hostname":"marketplace.t99.omani.works","port":8443},
      {"name":"console-https-walktwo","hostname":"*.walktwo.omani.trade","port":8443}]}]'

  echo "self-test: 14 fixtures (7 must pass, 7 must fail) over 3 pool TLDs"

  # ── OWNERSHIP: the parentRef decides, not the hostname prefix ─────────────

  # 1. CONTROL for 2. `marketplace.` does NOT start with `console.` and IS on
  #    the console gateway. A prefix rule would have checked the shared
  #    gateway, whose `*.<fqdn>` admits it — passing for the wrong reason.
  #    Here it is admitted by its OWN gateway's exact listener.
  run_fixture "marketplace host admitted by the CONSOLE gateway" pass - '{
    "profile":"ownership", '"$GW_443"',
    "routes":[{"ns":"catalyst-system","name":"marketplace",
               "hosts":["marketplace.t99.omani.works"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

  # 2. The #5341 shape: a host re-parented onto the console gateway without
  #    its prefix added to consoleGateway.hostPrefixes. The console listeners
  #    are EXACT 2-label hosts, so nothing admits it — even though the shared
  #    gateway's wildcard would have. This is the defect the chart comment
  #    calls "the deliberate, testable cost of a specific listener".
  run_fixture "mcp re-parented to console without a hostPrefix" fail NO-LISTENER '{
    "profile":"ownership", '"$GW_443"',
    "routes":[{"ns":"catalyst-system","name":"openova-mcp",
               "hosts":["mcp.t99.omani.works"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

  # 3. CONTROL for 2 — the SAME hostname on the gateway that really owns it.
  #    Proves fixture 2 fails on the OWNERSHIP fact and not on the hostname.
  run_fixture "the same mcp host on the SHARED gateway" pass - '{
    "profile":"ownership", '"$GW_443"',
    "routes":[{"ns":"catalyst-system","name":"openova-mcp",
               "hosts":["mcp.t99.omani.works"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway"}]}]}'

  # ── WILDCARD SEMANTICS ───────────────────────────────────────────────────

  # 4. `*.a.b` must NEVER admit the bare apex `a.b` it wildcards.
  run_fixture "wildcard must not admit its own apex" fail NO-LISTENER '{
    "profile":"wildcard", '"$GW_443"',
    "routes":[{"ns":"walkone","name":"apex","hosts":["walkone.omani.homes"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

  # 5. CONTROL for 4 — the SAME wildcard MUST admit a deeper host, because
  #    Gateway API wildcards are suffix matches, not single-label matches.
  #    Paired with 4 this pins the rule from both sides; a classifier that
  #    passed one and failed the other is caught here.
  run_fixture "wildcard admits a deeper sub-host" pass - '{
    "profile":"wildcard", '"$GW_443"',
    "routes":[{"ns":"walkone","name":"deep","hosts":["a.b.walkone.omani.homes"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

  # 6. CONTROL — a near-miss that a bare `endswith` would wrongly admit:
  #    `notwalkone.omani.homes` shares the suffix `walkone.omani.homes`'s
  #    TAIL but is a different zone. Must be admitted by `*.omani.homes` on
  #    the SHARED gateway and NOT by the console per-Org listener.
  run_fixture "sibling zone admitted by the pool wildcard, not the Org one" pass - '{
    "profile":"wildcard", '"$GW_443"',
    "routes":[{"ns":"other","name":"sibling","hosts":["notwalkone.omani.homes"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway"}]}]}'

  # ── sectionName PIN — the harbor alias-redirect shape ────────────────────

  # 7. A route pinning `sectionName: https` on a MULTI-ZONE Sovereign, where
  #    the listener is named `https-omani-homes` because more than one parent
  #    zone is declared. The pin matches no listener, so the route attaches to
  #    nothing even though a hostname-compatible listener is sitting right
  #    there. Real instance: harbor's alias-redirect route.
  run_fixture "sectionName pin naming a listener that multi-zone renamed" fail NO-LISTENER '{
    "profile":"sectionName", '"$GW_443"',
    "routes":[{"ns":"harbor","name":"harbor-alias-redirect",
               "hosts":["harbor.t99.omani.works"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway","sectionName":"https"}]}]}'

  # 8. CONTROL for 7 — the SAME route with the pin removed. Proves 7 fails on
  #    the PIN and not on the hostname, the gateway, or the zone count.
  run_fixture "the same route with no sectionName pin" pass - '{
    "profile":"sectionName", '"$GW_443"',
    "routes":[{"ns":"harbor","name":"harbor-alias-redirect",
               "hosts":["harbor.t99.omani.works"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway"}]}]}'

  # 9. CONTROL for 7 — a sectionName pin that NAMES A REAL listener must pass,
  #    so the guard is not merely "bans sectionName".
  run_fixture "sectionName pin that names a real listener" pass - '{
    "profile":"sectionName", '"$GW_443"',
    "routes":[{"ns":"harbor","name":"harbor",
               "hosts":["registry.t99.omani.works"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway","sectionName":"https-t99-omani-works"}]}]}'

  # ── PORT PIN — the isolation split that produced the false 404 ───────────

  # 10. The console gateway is on 8443 (Huawei). A route pinning :443 —
  #     the shared gateway's port — attaches to nothing. This is the exact
  #     shape of the measurement artifact: TLS completes on the wrong port
  #     because both gateways carry the same wildcard cert, and only then
  #     does the request meet an envoy with no matching vhost.
  run_fixture "port pin 443 against a console gateway on 8443" fail NO-LISTENER '{
    "profile":"huawei", '"$GW_8443"',
    "routes":[{"ns":"catalyst-system","name":"catalyst-ui",
               "hosts":["console.t99.omani.works"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway-console","port":443}]}]}'

  # 11. CONTROL for 10 — the SAME route, same gateway, same 8443 listeners,
  #     with no port pin. Proves 10 fails on the PORT and not on the profile.
  run_fixture "the same console route with no port pin on 8443" pass - '{
    "profile":"huawei", '"$GW_8443"',
    "routes":[{"ns":"catalyst-system","name":"catalyst-ui",
               "hosts":["console.t99.omani.works"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

  # 12. CONTROL — a per-Org host on the THIRD pool TLD, admitted by the
  #     minted `*.<slug>.<parent>` listener that org_console_tls.go writes,
  #     on the 8443 profile where the minter carried the apex port across.
  run_fixture "per-Org host admitted by the minted listener on 8443" pass - '{
    "profile":"huawei", '"$GW_8443"',
    "routes":[{"ns":"walktwo","name":"wordpress",
               "hosts":["wordpress.walktwo.omani.trade"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

  # ── NO-GATEWAY stays a DIFFERENT verdict from NO-LISTENER ────────────────

  # 13. A parentRef naming a Gateway nothing installs. Different owner (a
  #     bootstrap slot, not a values key), so it must not be collapsed into
  #     NO-LISTENER — asserted by verdict token, not just by exit code.
  run_fixture "parentRef to a Gateway that is not installed" fail NO-GATEWAY '{
    "profile":"owners", '"$GW_443"',
    "routes":[{"ns":"walkone","name":"orphan","hosts":["x.walkone.omani.rest"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway-typo"}]}]}'

  # 14. The region-B shape that started this: the Gateway is installed but
  #     carries ZERO listeners. That is NOT NO-GATEWAY — the object exists —
  #     and a guard that reported it as one would send it to the wrong owner.
  run_fixture "Gateway present but carrying zero listeners" fail NO-LISTENER '{
    "profile":"owners",
    "gateways":[{"ns":"kube-system","name":"cilium-gateway-console","listeners":[]}],
    "routes":[{"ns":"catalyst-system","name":"catalyst-ui",
               "hosts":["console.t99.omani.works"],
               "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]}'

  # ── The MODEL must track the producers it models ─────────────────────────
  #
  # This guard models two things it does not itself render: the runtime per-Org
  # listener minter, and the Gateway names in the cluster template. If either
  # source changes shape, the model silently becomes fiction and the guard
  # keeps passing. So assert the shapes still exist. These are not style
  # checks — they are the guard's own tripwires.
  echo
  echo "self-test: the modelled producers still declare the modelled shape"
  MINTER="$ROOT/products/catalyst/bootstrap/api/internal/handler/org_console_tls.go"
  assert_source() { # assert_source <label> <file> <grep-pattern>
    if [ ! -f "$2" ]; then
      echo "  FAIL: $1 — modelled source is gone: $2"; fails=1; return
    fi
    if grep -qF -- "$3" "$2"; then
      echo "  ok  $1"
    else
      echo "  FAIL: $1 — '$3' no longer present in $2."
      echo "        This guard MODELS that producer; the model is now fiction."
      fails=1
    fi
  }
  assert_source "minter still names listeners console-https-<slug>" "$MINTER" '"console-https-" + slug'
  assert_source "minter still hostnames them *.<slug>.<parent>"     "$MINTER" '"*." + slug + "." + parent'
  assert_source "shared Gateway is still named cilium-gateway" \
      "$ROOT/clusters/_template/sovereign-tls/cilium-gateway.yaml" 'name: cilium-gateway'
  assert_source "console Gateway is still named cilium-gateway-console" \
      "$ROOT/clusters/_template/sovereign-tls/cilium-gateway-console.yaml" 'name: cilium-gateway-console'
  assert_source "shared listeners still come from PARENT_DOMAINS_LISTENERS_YAML" \
      "$ROOT/clusters/_template/sovereign-tls/cilium-gateway.yaml" 'listeners: ${PARENT_DOMAINS_LISTENERS_YAML}'
  assert_source "console listeners still come from CONSOLE_LISTENERS_YAML" \
      "$ROOT/clusters/_template/sovereign-tls/cilium-gateway-console.yaml" 'listeners: ${CONSOLE_LISTENERS_YAML}'

  echo
  [ "$fails" -eq 0 ] || { echo "SELF-TEST FAILED" >&2; exit 1; }
  echo "SELF-TEST PASSED — ownership by parentRef, wildcard apex/depth, sectionName"
  echo "and port pins, and NO-GATEWAY vs NO-LISTENER are each pinned by a must-fail"
  echo "fixture AND a control that shares its suspect property."
  echo "OK: --self-test only; no cluster and no helm render was used."
  exit 0
fi

# ─────────────────────────────────────────────────────────────────────────────
# Repo mode — render the real charts and join them.
# ─────────────────────────────────────────────────────────────────────────────
command -v helm    >/dev/null 2>&1 || { echo "helm not found"    >&2; exit 2; }
command -v python3 >/dev/null 2>&1 || { echo "python3 not found" >&2; exit 2; }
python3 -c 'import yaml' 2>/dev/null || {
  echo "python3 pyyaml not found (pip install pyyaml)" >&2; exit 2; }

cd "$ROOT" || { echo "cannot cd to $ROOT" >&2; exit 2; }

FQDN="t99.omani.works"          # canonical test Sovereign per DOD.md domains-canon
ORG_SLUG="walkone"
ORG_PARENT="omani.homes"        # pool TLDs deliberately differ across the
ORG2_SLUG="walktwo"             # scenario so a hardcoded TLD cannot pass
ORG2_PARENT="omani.trade"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/route-listener.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

# prep_chart <chart-dir> — a throwaway copy with `dependencies:` removed.
#
# 67 charts in this repo declare external Helm dependencies that only resolve
# with network access, so a plain `helm template` cannot run offline in CI.
# Every HTTPRoute in this repo is rendered by its chart's OWN templates/, never
# by a subchart, so dropping the dependency block renders exactly the objects
# this guard is about — and keeps the guard runnable with no registry.
prep_chart() {
  local chart="$1" dst="$WORK/$(printf '%s' "$1" | tr '/' '_')"
  rm -rf "$dst"; mkdir -p "$dst"; cp -r "$chart/." "$dst/"
  python3 - "$dst/Chart.yaml" <<'PY'
import sys, re
p = sys.argv[1]; out, skip = [], False
for ln in open(p).read().split("\n"):
    if re.match(r'^dependencies:\s*$', ln):
        skip = True; continue
    if skip:
        if ln.startswith((' ', '\t', '-')) or ln.strip() == '':
            continue
        skip = False
    out.append(ln)
open(p, "w").write("\n".join(out))
PY
  printf '%s' "$dst"
}

# render_chart <label> <min-routes> <chart-dir> [--set ...]
#
# `min-routes` is the NON-VACUITY contract. Almost every chart here is
# default-off ("smoke-render-mode:default-off"), so a scenario whose values
# stopped matching the chart would render ZERO HTTPRoutes and this guard would
# report a serene PASS over nothing at all. Declaring the expected count turns
# that into a setup error (exit 2), which is the whole difference between a
# guard and a decoration.
render_chart() {
  local label="$1" min="$2" chart="$3"; shift 3
  local dst; dst="$(prep_chart "$chart")"
  local out="$WORK/render-$label.yaml"
  if ! helm template "$label" "$dst" "$@" >"$out" 2>"$WORK/err-$label.txt"; then
    echo "SETUP ERROR: declared chart $chart failed to render:" >&2
    sed 's/^/    /' "$WORK/err-$label.txt" >&2
    exit 2
  fi
  local got; got="$(grep -c '^kind: HTTPRoute' "$out" || true)"
  if [ "$got" -lt "$min" ]; then
    echo "SETUP ERROR: $chart rendered $got HTTPRoute(s), scenario declares >= $min." >&2
    echo "    The scenario values no longer switch this chart's routes on, so this" >&2
    echo "    guard would have passed over an empty render. Fix the scenario or the" >&2
    echo "    chart — do NOT lower the floor to match." >&2
    exit 2
  fi
  echo "  rendered $label: $got HTTPRoute(s)" >&2
}

# ── The scenario ─────────────────────────────────────────────────────────────
#
# Charts whose HTTPRoutes carry SOVEREIGN-APEX hostnames — the set whose
# admitting listeners are chart-rendered and therefore decidable from source.
# Per-Org charts (agenity / wordpress-tenant / stalwart-tenant / openclaw)
# carry `<x>.<slug>.<pool>` hostnames whose listeners the runtime minter
# writes; they are covered by the modelled per-Org listeners below and by the
# coverage assertion, not by an apex render.
scenario() {
  render_chart edge-routes 4 platform/catalyst-edge-routes/chart \
    --set ingress.gateway.enabled=true \
    --set "ingress.hosts.console.host=console.$FQDN" \
    --set "ingress.hosts.api.host=api.$FQDN" \
    --set "ingress.hosts.marketplace.host=marketplace.$FQDN" \
    --set "ingress.hosts.mcp.host=mcp.$FQDN" \
    --set ingress.marketplace.enabled=true
  render_chart catalyst 3 products/catalyst/chart \
    --set "ingress.hosts.console.host=console.$FQDN" \
    --set "ingress.hosts.api.host=api.$FQDN" \
    --set "ingress.hosts.marketplace.host=marketplace.$FQDN"
  render_chart openova-mcp 1 products/openova-mcp/chart --set "sovereignFqdn=$FQDN"
  render_chart harbor        2 platform/harbor/chart        --set "gateway.host=registry.$FQDN"
  render_chart keycloak      1 platform/keycloak/chart      --set "gateway.host=auth.$FQDN"
  render_chart gitea         1 platform/gitea/chart         --set "gateway.host=gitea.$FQDN"
  render_chart grafana       1 platform/grafana/chart       --set "gateway.host=grafana.$FQDN"
  render_chart openbao       1 platform/openbao/chart       --set "gateway.host=bao.$FQDN"
  render_chart powerdns-admin 1 platform/powerdns-admin/chart --set "gateway.host=pdns-admin.$FQDN"
}

# The listener producer. One chart renders BOTH Gateways' listener arrays.
render_listeners() { # render_listeners <console-https-port> <console-http-port>
  local dst; dst="$(prep_chart platform/sovereign-tls-vars/chart)"
  helm template stv "$dst" \
    --set "global.sovereignFQDN=$FQDN" \
    --set "parentZones[0].name=$FQDN"       --set 'parentZones[0].role=primary' \
    --set "parentZones[1].name=$ORG_PARENT"  --set 'parentZones[1].role=org-pool' \
    --set "parentZones[2].name=omani.rest"   --set 'parentZones[2].role=org-pool' \
    --set "parentZones[3].name=$ORG2_PARENT" --set 'parentZones[3].role=org-pool' \
    --set "consoleGateway.httpsPort=$1" \
    --set "consoleGateway.httpPort=$2" \
    >"$WORK/listeners.yaml" 2>"$WORK/err-stv.txt" || {
      echo "SETUP ERROR: the listener producer failed to render:" >&2
      sed 's/^/    /' "$WORK/err-stv.txt" >&2; exit 2; }
}

# ── Bundle assembly ──────────────────────────────────────────────────────────
read -r -d '' BUNDLE_PY <<'PY' || true
import json, os, re, sys, glob
import yaml

work    = sys.argv[1]
profile = sys.argv[2]
root    = sys.argv[3]
orgs    = json.loads(sys.argv[4])       # [{"slug","parent"}]
extra   = json.loads(sys.argv[5])       # extra routes injected by --vacuity

def docs(path):
    with open(path) as fh:
        for d in yaml.safe_load_all(fh):
            if isinstance(d, dict) and d.get("kind"):
                yield d

# ── 1. Listeners, out of the ConfigMap the producer chart renders ───────────
cm = None
for d in docs(os.path.join(work, "listeners.yaml")):
    if d.get("kind") == "ConfigMap" and d["metadata"]["name"] == "sovereign-tls-vars":
        cm = d
if cm is None:
    print("SETUP ERROR: sovereign-tls-vars ConfigMap absent from the render", file=sys.stderr)
    sys.exit(2)

# ── 2. Gateway identity, out of the CLUSTER TEMPLATE — never hardcoded here.
# The template is the thing Flux applies, so if a Gateway is renamed there the
# routes' parentRefs stop resolving and this guard must report NO-GATEWAY. A
# hardcoded name in this script would keep resolving and hide exactly that.
gateways, gw_key = [], {}
for fname, cmkey in (("cilium-gateway.yaml", "PARENT_DOMAINS_LISTENERS_YAML"),
                     ("cilium-gateway-console.yaml", "CONSOLE_LISTENERS_YAML")):
    path = os.path.join(root, "clusters/_template/sovereign-tls", fname)
    text = open(path).read()
    name = re.search(r'^\s*name:\s*(\S+)\s*$', text, re.M)
    ns   = re.search(r'^\s*namespace:\s*(\S+)\s*$', text, re.M)
    if not name or not ns:
        print("SETUP ERROR: cannot read Gateway identity from %s" % path, file=sys.stderr)
        sys.exit(2)
    if ("listeners: ${%s}" % cmkey) not in text:
        print("SETUP ERROR: %s no longer substitutes %s — the listener source moved."
              % (path, cmkey), file=sys.stderr)
        sys.exit(2)
    listeners = json.loads(cm["data"][cmkey])
    g = {"ns": ns.group(1), "name": name.group(1), "listeners": listeners}
    gateways.append(g)
    gw_key[cmkey] = g

# ── 3. The per-Organization listeners catalyst-api mints at install time.
# products/catalyst/bootstrap/api/internal/handler/org_console_tls.go appends
# `console-https-<slug>` / `console-http-<slug>` hostnamed `*.<slug>.<parent>`
# to the console Gateway, taking its ports from the apex console-https /
# console-http listeners (consoleApexListenerPorts). Modelled here so per-Org
# hostnames are DECIDED rather than skipped; --self-test asserts that Go source
# still declares this shape, so the model cannot outlive the producer.
console = gw_key["CONSOLE_LISTENERS_YAML"]
apex_https = next((l["port"] for l in console["listeners"] if l.get("name") == "console-https"), None)
apex_http  = next((l["port"] for l in console["listeners"] if l.get("name") == "console-http"), None)
if apex_https is None or apex_http is None:
    print("SETUP ERROR: console gateway has no apex console-https/console-http listener;"
          " the minter reads its ports from there.", file=sys.stderr)
    sys.exit(2)
for o in orgs:
    zone = "*.%s.%s" % (o["slug"], o["parent"])
    console["listeners"].append({"name": "console-https-" + o["slug"],
                                 "hostname": zone, "port": apex_https})
    console["listeners"].append({"name": "console-http-" + o["slug"],
                                 "hostname": zone, "port": apex_http})

# ── 4. Routes, out of every scenario render ─────────────────────────────────
routes = []
for path in sorted(glob.glob(os.path.join(work, "render-*.yaml"))):
    for d in docs(path):
        if d.get("kind") != "HTTPRoute":
            continue
        md, spec = d.get("metadata") or {}, d.get("spec") or {}
        parents = []
        for p in (spec.get("parentRefs") or []):
            parents.append({"ns": p.get("namespace"), "name": p.get("name"),
                            "sectionName": p.get("sectionName") or None,
                            "port": p.get("port") or None})
        routes.append({"ns": md.get("namespace") or "default",
                       "name": md.get("name") or "(unnamed)",
                       "hosts": list(spec.get("hostnames") or []),
                       "parents": parents})
routes.extend(extra)

if not routes:
    print("SETUP ERROR: the scenario produced ZERO HTTPRoutes.", file=sys.stderr)
    sys.exit(2)

json.dump({"profile": profile, "gateways": gateways, "routes": routes}, sys.stdout)
PY

ORGS_JSON="$(printf '[{"slug":"%s","parent":"%s"},{"slug":"%s","parent":"%s"}]' \
             "$ORG_SLUG" "$ORG_PARENT" "$ORG2_SLUG" "$ORG2_PARENT")"

# The vacuity injection. A guard observed only passing is worthless, so the
# --vacuity run re-runs the REAL scenario with ONE extra route: mcp.<fqdn>
# re-parented onto the console gateway. That is not a synthetic string — it is
# precisely the #5341 misconfiguration, against the real rendered console
# listener set, and the guard MUST go red on it.
VACUITY_ROUTE='[{"ns":"catalyst-system","name":"VACUITY-mcp-on-console-gateway",
  "hosts":["mcp.'"$FQDN"'"],
  "parents":[{"ns":"kube-system","name":"cilium-gateway-console"}]}]'

echo "Rendering the scenario (no cluster, no registry)..." >&2
scenario
echo >&2

rc_total=0
run_profile() { # run_profile <label> <https-port> <http-port> <extra-routes-json>
  echo "═══ profile: $1 (console gateway on :$2/:$3) ═══"
  render_listeners "$2" "$3"
  local bundle
  bundle="$(python3 -c "$BUNDLE_PY" "$WORK" "$1" "$ROOT" "$ORGS_JSON" "$4")"
  set +e
  printf '%s' "$bundle" | classify
  local rc=$?
  set -e
  echo
  return $rc
}

if [ "$VACUITY" -eq 1 ]; then
  # Vacuity is proven only if the guard goes RED, and only if it goes red with
  # the RIGHT verdict on the RIGHT route — a crash or an unrelated failure
  # would otherwise be scored as proof.
  out="$(run_profile "vacuity-hetzner" 443 80 "$VACUITY_ROUTE" 2>&1)" && {
    echo "$out"
    echo "VACUITY FAIL: the guard PASSED with mcp.$FQDN re-parented onto the"
    echo "console gateway, whose listeners are the exact hosts console./api./"
    echo "marketplace. only. A guard that cannot fail on the #5341 shape cannot"
    echo "fail on anything." >&2
    exit 1
  }
  echo "$out"
  printf '%s' "$out" | grep -q 'NO-LISTENER .*VACUITY-mcp-on-console-gateway' || {
    echo "VACUITY FAIL: the guard exited non-zero but not with NO-LISTENER on the"
    echo "injected route — the exit code came from something else." >&2
    exit 1
  }
  echo "OK — vacuity proven: the guard goes RED, with NO-LISTENER, on the"
  echo "injected #5341 shape, against the real rendered listener set."
  exit 0
fi

# Both port profiles. Hetzner keeps the console gateway on :443/:80; Huawei
# moves it to :8443/:8080 so two Gateways can coexist under hostNetwork. A
# route that only corresponds under one profile is broken under the other.
run_profile "hetzner" 443  80   '[]' || rc_total=1
run_profile "huawei"  8443 8080 '[]' || rc_total=1

# ── Coverage. A scenario is only as good as the set of charts it renders, and
# a new route-bearing chart that nobody adds here would be invisible — the
# guard would keep passing while the thing it guards grew a hole. So every
# chart in the repo that renders an HTTPRoute naming a cilium Gateway must be
# accounted for: either rendered above, or listed here with the reason its
# hostnames are not apex-decidable.
echo "═══ coverage ═══"
RENDERED="platform/catalyst-edge-routes/chart products/catalyst/chart
products/openova-mcp/chart platform/harbor/chart platform/keycloak/chart
platform/gitea/chart platform/grafana/chart platform/openbao/chart
platform/powerdns-admin/chart"

# Charts whose HTTPRoute hostnames are per-ORGANIZATION (`<x>.<slug>.<pool>`),
# admitted by the listener the runtime minter writes rather than by any
# chart-rendered listener. Modelled by the per-Org listeners above; an apex
# render of them would assert a hostname shape they never carry in production.
PER_ORG="platform/wordpress-tenant/chart platform/stalwart-tenant/chart
platform/openclaw/chart products/agenity/chart"

# Charts whose route hostname is not derivable without per-Sovereign values
# this guard has no source for. Listed EXPLICITLY so the hole is visible and
# reviewable, never silent.
DEFERRED="platform/cilium/chart platform/newapi/chart platform/guacamole/chart
platform/netbird/chart platform/oidc-gate/chart platform/openova-flow-server/chart
platform/powerdns/chart products/dmz-vcluster/chart"

uncovered=0
for chart in $(grep -rl 'kind: HTTPRoute' --include='*.yaml' platform/ products/ core/ 2>/dev/null \
               | grep '/templates/' | sed 's#/templates/.*##' | sort -u); do
  grep -rq 'cilium-gateway' "$chart" 2>/dev/null || continue    # no Gateway parentRef
  case " $(echo $RENDERED) $(echo $PER_ORG) $(echo $DEFERRED) " in
    *" $chart "*) ;;
    *) echo "  UNCOVERED: $chart renders an HTTPRoute on a cilium Gateway but is in"
       echo "             no scenario list. Add it to RENDERED (with values and a"
       echo "             minimum route count), or to PER_ORG / DEFERRED with the"
       echo "             reason its hostnames are not apex-decidable."
       uncovered=$((uncovered + 1)) ;;
  esac
done
if [ "$uncovered" -gt 0 ]; then
  echo "FAIL: $uncovered route-bearing chart(s) are outside the scenario."
  rc_total=1
else
  echo "  OK — every chart rendering an HTTPRoute on a cilium Gateway is either"
  echo "       rendered by the scenario, modelled as per-Organization, or listed"
  echo "       as deferred with a reason."
fi

echo
if [ "$rc_total" -ne 0 ]; then
  echo "RESULT: FAIL — see the rows above."
  exit 1
fi
echo "RESULT: PASS — route/listener correspondence holds on both port profiles,"
echo "        and every route-bearing chart is accounted for."
