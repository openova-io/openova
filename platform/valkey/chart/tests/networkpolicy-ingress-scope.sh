#!/usr/bin/env bash
# bp-valkey NetworkPolicy ingress-scope gate (#5487).
#
# THE DEFECT THIS LOCKS OUT (verified live on hw291, 2026-07-29):
#   The upstream bitnami template keys the ingress `from:` off
#   `networkPolicy.allowExternal`, which defaults to TRUE. With it true the
#   rendered rule is `ingress: [{ports: [6379]}]` — NO `from:` — and a
#   Kubernetes ingress rule with no `from:` admits EVERY Pod in the cluster.
#   With `auth.enabled: false` (TBD-V12 #2003) that meant any container
#   foothold anywhere could run arbitrary commands against the shared cache
#   holding the gateway's rate-limit counters. `PING` → `+PONG` from an
#   unrelated `grafana` Pod was the live proof.
#
# WHAT THIS GATE ASSERTS
#   Case 1 (VACUITY): the default render actually emits a NetworkPolicy that
#           selects the valkey Pods. A `grep -q` for an absent string passes
#           trivially against an empty render, so every later assertion is
#           worthless unless this one holds first.
#   Case 2: EVERY ingress rule in that policy carries a non-empty `from:`,
#           and no `from:` peer is an empty/match-everything selector.
#   Case 3: the declared consumers (org-services + newapi) are admitted on
#           6379 — the fix must not close the hole by breaking the product.
#   Case 4 (NEGATIVE CONTROL): re-render with `allowExternal=true` and assert
#           the Case-2 detector FIRES. Without this the detector could be
#           silently inert and Cases 1-3 would still report green.
#   Case 5 (NEGATIVE CONTROL): re-render with `extraIngress` emptied and
#           assert the Case-3 detector FIRES.
#
# Usage: bash tests/networkpolicy-ingress-scope.sh [CHART_DIR]
# CI consumes this via blueprint-release.yaml's `tests/*.sh` gate.

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

cd "$CHART_DIR"

fail() { echo "FAIL: $1" >&2; exit 1; }

# Same dep handling as contexts-render.sh / observability-toggle.sh.
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

# ── The checker ───────────────────────────────────────────────────────────
# Reads a rendered manifest stream, isolates the valkey NetworkPolicy, and
# runs one named check. Exits 0 = check satisfied, 1 = violated, 2 = the
# render carried no NetworkPolicy at all (vacuous input — never "satisfied").
check() {
  local mode="$1" file="$2"
  python3 - "$mode" "$file" <<'PY'
import sys, yaml

mode, path = sys.argv[1], sys.argv[2]
with open(path) as fh:
    docs = [d for d in yaml.safe_load_all(fh) if d]

pols = [d for d in docs
        if d.get("kind") == "NetworkPolicy"
        and (d.get("spec", {}).get("podSelector", {}).get("matchLabels", {})
             .get("app.kubernetes.io/name") == "valkey")]

# VACUITY — an empty/NetworkPolicy-less render makes every other assertion
# below trivially true. Treat it as a hard, distinct failure.
if not pols:
    kinds = sorted({d.get("kind", "?") for d in docs})
    print("VACUOUS RENDER: no NetworkPolicy selecting app.kubernetes.io/name=valkey "
          f"in {len(docs)} rendered doc(s) (kinds: {', '.join(kinds) or 'none'}). "
          "Every ingress assertion in this gate would pass trivially.", file=sys.stderr)
    sys.exit(2)

# An "open" peer: a rule with no `from:` at all admits every Pod in the
# cluster; a `from:` peer that is an empty namespaceSelector/podSelector does
# the same thing one level down.
def open_peers(rule):
    peers = rule.get("from")
    if not peers:
        return ["<no from: — admits ALL Pods in the cluster>"]
    bad = []
    for p in peers:
        if not p:
            bad.append("<empty peer>")
        for key in ("namespaceSelector", "podSelector"):
            if key in p and not p[key]:
                bad.append(f"<empty {key}: matches everything>")
    return bad

def ns_values(pol):
    """Namespaces admitted by a namespaceSelector on a 6379 rule."""
    out = set()
    for rule in pol.get("spec", {}).get("ingress", []) or []:
        ports = {str(p.get("port")) for p in (rule.get("ports") or [])}
        if ports and "6379" not in ports:
            continue
        for peer in rule.get("from") or []:
            sel = peer.get("namespaceSelector") or {}
            for k, v in (sel.get("matchLabels") or {}).items():
                if k == "kubernetes.io/metadata.name":
                    out.add(v)
            for expr in sel.get("matchExpressions") or []:
                if expr.get("key") == "kubernetes.io/metadata.name" and \
                   expr.get("operator") == "In":
                    out.update(expr.get("values") or [])
    return out

rc = 0
for pol in pols:
    name = pol.get("metadata", {}).get("name", "?")

    if mode == "vacuity":
        rules = pol.get("spec", {}).get("ingress", []) or []
        if not rules:
            print(f"NetworkPolicy/{name} renders with an EMPTY ingress list — "
                  "nothing for this gate to assert against.", file=sys.stderr)
            rc = 1
        else:
            print(f"ok: NetworkPolicy/{name} renders with {len(rules)} ingress rule(s)")

    elif mode == "scoped":
        for i, rule in enumerate(pol.get("spec", {}).get("ingress", []) or []):
            bad = open_peers(rule)
            if bad:
                print(f"NetworkPolicy/{name} ingress[{i}] is OPEN TO THE CLUSTER: "
                      f"{'; '.join(bad)}  (#5487 — set "
                      "valkey.networkPolicy.allowExternal=false)", file=sys.stderr)
                rc = 1

    elif mode == "consumers":
        want = {"org-services", "newapi"}
        got = ns_values(pol)
        missing = want - got
        if missing:
            print(f"NetworkPolicy/{name} does not admit the declared Valkey "
                  f"consumer namespace(s) {sorted(missing)} on 6379 "
                  f"(admitted: {sorted(got) or 'none'}) — the fix must not close "
                  "the hole by breaking org-services/newapi.", file=sys.stderr)
            rc = 1

    else:
        print(f"unknown check mode {mode!r}", file=sys.stderr)
        sys.exit(2)

sys.exit(rc)
PY
}

echo "[netpol] rendering default values"
helm template valkey . --namespace valkey > "$TMP/default.yaml" 2> "$TMP/default.err" || {
  cat "$TMP/default.err" >&2; fail "default render errored"; }

echo "[netpol] Case 1: VACUITY — the NetworkPolicy actually renders"
check vacuity "$TMP/default.yaml" \
  || fail "default render carries no usable valkey NetworkPolicy (see above) — every assertion below would be vacuous"

echo "[netpol] Case 2: every ingress rule carries a real from: selector"
check scoped "$TMP/default.yaml" \
  || fail "#5487 REGRESSION — the valkey NetworkPolicy admits the whole cluster on 6379"

echo "[netpol] Case 3: the declared consumers are still admitted"
check consumers "$TMP/default.yaml" \
  || fail "the ingress allow-list no longer names the declared Valkey consumers"

echo "[netpol] Case 4: NEGATIVE CONTROL — allowExternal=true must trip Case 2"
helm template valkey . --namespace valkey \
  --set valkey.networkPolicy.allowExternal=true > "$TMP/open.yaml" 2> "$TMP/open.err" || {
  cat "$TMP/open.err" >&2; fail "negative-control render errored"; }
if check scoped "$TMP/open.yaml" > "$TMP/open.out" 2>&1; then
  cat "$TMP/open.out" >&2
  fail "the Case-2 detector did NOT fire against a deliberately allow-all render — this gate is inert and would never catch the #5487 revert"
fi
echo "[netpol]   detector fired as expected: $(head -1 "$TMP/open.out")"

echo "[netpol] Case 5: NEGATIVE CONTROL — empty extraIngress must trip Case 3"
helm template valkey . --namespace valkey \
  --set-json 'valkey.networkPolicy.extraIngress=[]' > "$TMP/noconsumers.yaml" 2> "$TMP/nc.err" || {
  cat "$TMP/nc.err" >&2; fail "negative-control render errored"; }
if check consumers "$TMP/noconsumers.yaml" > "$TMP/nc.out" 2>&1; then
  cat "$TMP/nc.out" >&2
  fail "the Case-3 detector did NOT fire against a render with no consumer allow-list — this gate is inert"
fi
echo "[netpol]   detector fired as expected: $(head -1 "$TMP/nc.out")"

echo "[netpol] PASS — ingress is scoped to the declared consumers, and both detectors are live"
