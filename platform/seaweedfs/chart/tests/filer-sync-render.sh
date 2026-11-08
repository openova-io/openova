#!/usr/bin/env bash
# bp-seaweedfs cross-region filer sync render test — ADR-0015 D6(a), #7067.
#
# Renders the chart ONCE PER REGION exactly as bootstrap-kit slot 18 feeds it
# (self = SOVEREIGN_REGION_KEY, configuredRegions =
# SOVEREIGN_CONFIGURED_REGIONS_YAML, enabled = SOVEREIGN_ENABLE_CNPG_PAIR) and
# asserts on the rendered VALUES, not on token presence:
#   1. default render: no filer-sync / filer-mesh object at all;
#   2. enabled but single region (no peers): nothing rendered;
#   3. region A: exactly ONE filer.sync Deployment, ONE process per bucket,
#      `-a=` = B's mesh Service, `-b=` = the local filer, -isActivePassive,
#      -a.filerProxy; mesh Services: A self (shared=true, local selector),
#      B stub (shared=false, zero-local selector); all ClusterIP;
#   4. region B: the mirror image (points at A);
#   5. ADR-0015 D2 `openova.topology.{self,peers}` overrides the slot seam;
#   6. a non-`replicated[-*]` bucket name fails the render;
#   7. CNPs only with networkPolicy.enabled + peerClusters + cilium CRDs;
#   8. no NodePort anywhere in any enabled render.
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"
if [ ! -d charts ] || [ -z "$(ls -A charts 2>/dev/null)" ]; then
  helm dependency build >/dev/null
fi

A=hw-me-east-215-a-rtz-prod
B=hw-me-east-215-b-rtz-prod
REGIONS="[\"$A\",\"$B\"]"

render() { # out-file, extra helm args...
  local out="$1"; shift
  helm template seaweedfs . -n seaweedfs "$@" > "$out"
}

render "$TMP/default.yaml"
render "$TMP/single.yaml" --set crossRegion.enabled=true --set crossRegion.self="$A" \
  --set-json "crossRegion.configuredRegions=[\"$A\"]"
render "$TMP/a.yaml" --set crossRegion.enabled=true --set crossRegion.self="$A" \
  --set-json "crossRegion.configuredRegions=$REGIONS"
render "$TMP/b.yaml" --set crossRegion.enabled=true --set crossRegion.self="$B" \
  --set-json "crossRegion.configuredRegions=$REGIONS"
render "$TMP/disabled.yaml" --set crossRegion.enabled=false --set crossRegion.self="$A" \
  --set-json "crossRegion.configuredRegions=$REGIONS"
render "$TMP/d2.yaml" --set crossRegion.enabled=true --set crossRegion.self=ignored-region \
  --set openova.topology.self="$B" --set-json "openova.topology.peers=[\"$A\"]"
render "$TMP/twobuckets.yaml" --set crossRegion.enabled=true --set crossRegion.self="$A" \
  --set-json "crossRegion.configuredRegions=$REGIONS" \
  --set-json 'crossRegion.filerSync.buckets=["replicated","replicated-acme-wp"]'
render "$TMP/cnp.yaml" --api-versions cilium.io/v2 --set crossRegion.enabled=true \
  --set crossRegion.self="$A" --set-json "crossRegion.configuredRegions=$REGIONS" \
  --set-json 'crossRegion.peerClusters=["hw307-me-east-b"]' \
  --set seaweedfsOverlay.networkPolicy.enabled=true
render "$TMP/cnp-off.yaml" --api-versions cilium.io/v2 --set crossRegion.enabled=true \
  --set crossRegion.self="$A" --set-json "crossRegion.configuredRegions=$REGIONS" \
  --set-json 'crossRegion.peerClusters=["hw307-me-east-b"]'

if helm template seaweedfs . -n seaweedfs --set crossRegion.enabled=true --set crossRegion.self="$A" \
    --set-json "crossRegion.configuredRegions=$REGIONS" \
    --set-json 'crossRegion.filerSync.buckets=["wordpress-media"]' > /dev/null 2> "$TMP/badbucket.err"; then
  echo "FAIL: bucket 'wordpress-media' (outside the replicated scope) rendered instead of failing" >&2
  exit 1
fi
grep -q "must be named \`replicated\`" "$TMP/badbucket.err" || { echo "FAIL: wrong error for bad bucket:" >&2; cat "$TMP/badbucket.err" >&2; exit 1; }

python3 - "$TMP" "$A" "$B" <<'PY'
import sys, yaml, os
tmp, A, B = sys.argv[1], sys.argv[2], sys.argv[3]

def docs(name):
    with open(os.path.join(tmp, name)) as f:
        return [d for d in yaml.safe_load_all(f) if isinstance(d, dict)]

def sync_deploys(ds):
    return [d for d in ds if d["kind"] == "Deployment"
            and d["metadata"].get("labels", {}).get("app.kubernetes.io/component") == "filer-sync"]

def mesh_svcs(ds):
    return {d["metadata"]["name"]: d for d in ds if d["kind"] == "Service"
            and d["metadata"].get("labels", {}).get("app.kubernetes.io/component") == "filer-mesh"}

def cross(ds):
    return [d for d in ds if d["metadata"].get("labels", {}).get("catalyst.openova.io/subcomponent") == "filer-cross-region"]

fails = []
def check(cond, msg):
    if not cond:
        fails.append(msg)

def flags(container):
    return {a.split("=", 1)[0]: (a.split("=", 1)[1] if "=" in a else True)
            for a in container["command"][2:]}

# 1/2/disabled: nothing
for n in ("default.yaml", "single.yaml", "disabled.yaml"):
    check(cross(docs(n)) == [], f"{n}: expected NO cross-region objects, got {[d['metadata']['name'] for d in cross(docs(n))]}")

def assert_region(n, self_id, peer_id, buckets):
    ds = docs(n)
    deps = sync_deploys(ds)
    check(len(deps) == 1, f"{n}: expected exactly 1 filer-sync Deployment, got {len(deps)}")
    if not deps:
        return
    d = deps[0]
    check(d["metadata"]["name"] == f"seaweedfs-filer-sync-from-{peer_id}", f"{n}: Deployment name {d['metadata']['name']}")
    check(d["spec"]["replicas"] == 1 and d["spec"]["strategy"]["type"] == "Recreate", f"{n}: must be replicas=1 + Recreate")
    cs = d["spec"]["template"]["spec"]["containers"]
    check(len(cs) == len(buckets), f"{n}: expected {len(buckets)} sync process(es), got {len(cs)}")
    peer_mesh = f"seaweedfs-filer-mesh-{peer_id}.seaweedfs:8888.18888"
    for c, b in zip(cs, buckets):
        f = flags(c)
        check(c["command"][:2] == ["/usr/bin/weed", "filer.sync"], f"{n}: {c['name']} is not weed filer.sync")
        check(f.get("-a") == peer_mesh, f"{n}: {c['name']} -a={f.get('-a')} != {peer_mesh}")
        check(f.get("-b") == "seaweedfs-filer-client.seaweedfs:8888.18888", f"{n}: {c['name']} -b={f.get('-b')} is not the local filer")
        check(f.get("-isActivePassive") is True, f"{n}: {c['name']} missing -isActivePassive")
        check(f.get("-a.filerProxy") is True, f"{n}: {c['name']} missing -a.filerProxy")
        check(f.get("-a.path") == f"/buckets/{b}" and f.get("-b.path") == f"/buckets/{b}", f"{n}: {c['name']} path {f.get('-a.path')}")
        check(c.get("readinessProbe") and c.get("livenessProbe"), f"{n}: {c['name']} missing probes")
        rp = " ".join(c["readinessProbe"]["exec"]["command"])
        check(f"seaweedfs-filer-mesh-{peer_id}.seaweedfs:8888" in rp, f"{n}: readiness does not probe the peer filer")
    ports = [c["ports"][0]["containerPort"] for c in cs]
    check(len(set(ports)) == len(ports), f"{n}: metrics ports collide {ports}")
    svcs = mesh_svcs(ds)
    sname, pname = f"seaweedfs-filer-mesh-{self_id}", f"seaweedfs-filer-mesh-{peer_id}"
    check(set(svcs) == {sname, pname}, f"{n}: mesh Services {sorted(svcs)}")
    if sname in svcs and pname in svcs:
        s, p = svcs[sname], svcs[pname]
        check(s["metadata"]["annotations"]["service.cilium.io/global"] == "true", f"{n}: self mesh not global")
        check(s["metadata"]["annotations"]["service.cilium.io/shared"] == "true", f"{n}: self mesh must export (shared=true)")
        check("catalyst.openova.io/seaweedfs-region" not in s["spec"]["selector"], f"{n}: self mesh must select the local filer")
        check(p["metadata"]["annotations"]["service.cilium.io/global"] == "true", f"{n}: peer stub not global")
        check(p["metadata"]["annotations"]["service.cilium.io/shared"] == "false", f"{n}: peer stub must not export")
        check(p["spec"]["selector"].get("catalyst.openova.io/seaweedfs-region") == peer_id, f"{n}: peer stub must match zero local Pods")
    for d2 in cross(ds):
        if d2["kind"] == "Service":
            check(d2["spec"].get("type") == "ClusterIP", f"{n}: {d2['metadata']['name']} type {d2['spec'].get('type')}")

assert_region("a.yaml", A, B, ["replicated"])
assert_region("b.yaml", B, A, ["replicated"])
assert_region("d2.yaml", B, A, ["replicated"])
assert_region("twobuckets.yaml", A, B, ["replicated", "replicated-acme-wp"])

cnp = [d for d in docs("cnp.yaml") if d["kind"] == "CiliumNetworkPolicy"]
check(len(cnp) == 2, f"cnp.yaml: expected 2 CNPs, got {len(cnp)}")
for c in cnp:
    rules = c["spec"].get("ingress") or c["spec"].get("egress")
    sel = (rules[0].get("fromEndpoints") or rules[0].get("toEndpoints"))[0]["matchExpressions"]
    vals = [e["values"] for e in sel if e["key"] == "io.cilium.k8s.policy.cluster"]
    check(vals == [["hw307-me-east-b"]], f"cnp.yaml: {c['metadata']['name']} peer clusters {vals}")
check([d for d in docs("cnp-off.yaml") if d["kind"] == "CiliumNetworkPolicy"] == [],
      "cnp-off.yaml: CNP rendered while the seaweedfs NetworkPolicy is off (would default-deny the filer)")

for n in ("a.yaml", "b.yaml", "d2.yaml", "twobuckets.yaml", "cnp.yaml"):
    for d in docs(n):
        if d["kind"] == "Service":
            check(d["spec"].get("type", "ClusterIP") != "NodePort", f"{n}: NodePort Service {d['metadata']['name']}")
            for p in d["spec"].get("ports", []):
                check("nodePort" not in p or p["nodePort"] in (0, None), f"{n}: nodePort on {d['metadata']['name']}")

if fails:
    print("FAIL:")
    for f in fails:
        print("  - " + f)
    sys.exit(1)
print("PASS: per-region render = exactly one filer.sync Deployment pointing at the peer; mirror image on the peer; nothing when disabled / single-region")
PY
echo "[filer-sync-render] all cases PASS"
