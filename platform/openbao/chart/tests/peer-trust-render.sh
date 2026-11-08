#!/usr/bin/env bash
# bp-openbao — peer-region trust render gate (ADR-0015, Refs #7067).
#
# Each region runs its own OpenBao; the primary of a multi-region Application
# writes the shared material into every region's OpenBao. Rendered per region
# from the region registry (peerTrust.selfRegion + peerTrust.regions), this
# test asserts:
#
#   P1  region A renders the OWNER Service openbao-<A> (ClusterMesh global,
#       shared, selector = the active OpenBao server, ClusterIP) and a
#       selector-less stub openbao-<B> (global, shared=false); B mirrors it;
#   P2  A's declarative desired state (ConfigMap openbao-config-desired):
#       issuer = http://openbao-<A>…:8200, identity role aud =
#       openbao-peer-push + claim openova_region=<A>; per PEER a jwt mount +
#       config whose oidc_discovery_url is the PEER's issuer (and probes it)
#       and a role binding openova_region=<peer> — never self;
#   P3  the push policy grants create/update/read on secret/{data,metadata}/
#       openova/apps/* only — no delete, nothing wider;
#   P4  desired state binds role openbao-peer-token to the ESO SA (policy
#       openbao-peer-token only) and the main external-secrets role keeps
#       exactly read + push; the openbao-config Deployment applies it;
#   P5  ingress CNP admits ESO + peer OpenBao from any ClusterMesh cluster on
#       8200 only; egress CNP lets OpenBao reach peers' OpenBao on 8200;
#   P6  no Service of type NodePort and no nodePort field anywhere;
#   P7  single region / self unset / self not in the registry ⇒ NOTHING of
#       the above renders (and the desired state has no peer op);
#   P8  three regions: two stubs, two jwt mounts.
#
# Vacuity: P1's assertions run against the single-region render must fail.
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"
if ! ls charts/*.tgz >/dev/null 2>&1; then helm dependency build >/dev/null; fi

A="hw-me-east-215-a-rtz-prod"
B="hw-me-east-215-b-rtz-prod"
C="hw-me-east-215-c-rtz-prod"
REG2='[{"cloudRegion":"'"$A"'","provider":"huawei"},{"cloudRegion":"'"$B"'","provider":"huawei"}]'
REG3='["'"$A"'","'"$B"'","'"$C"'"]'
REG1='[{"cloudRegion":"'"$A"'"}]'

render() {  # $1 out, $2 self, $3 regions — passed exactly as Flux substitutes them (a string)
  python3 -c 'import json,sys; print(json.dumps({"peerTrust": {"selfRegion": sys.argv[1], "regions": sys.argv[2]}}))' "$2" "$3" > "$1.values.json"
  helm template openbao . --namespace openbao \
    --set autoUnseal.enabled=true \
    --set autoUnseal.baoAddress=http://openbao.openbao.svc.cluster.local:8200 \
    -f "$1.values.json" > "$1"
}
render "$TMP/a.yaml" "$A" "$REG2"
render "$TMP/b.yaml" "$B" "$REG2"
render "$TMP/c3.yaml" "$C" "$REG3"
render "$TMP/single.yaml" "$A" "$REG1"
render "$TMP/noself.yaml" "" "$REG2"
render "$TMP/foreign.yaml" "hw-eu-west-1-a-rtz-prod" "$REG2"

cat > "$TMP/check.py" <<'PYEOF'
import json, re, sys, yaml
mode, path, self_id, peers = sys.argv[1], sys.argv[2], sys.argv[3], [p for p in sys.argv[4].split(",") if p]
docs = [d for d in yaml.safe_load_all(open(path)) if d]
errs = []
def ok(c, m):
    if not c: errs.append(m)
def get(kind, name):
    m = [d for d in docs if d["kind"] == kind and d["metadata"]["name"] == name]
    return m[0] if m else None
host = lambda r: f"openbao-{r}.openbao.svc.cluster.local"

if mode == "peers":
    own = get("Service", f"openbao-{self_id}")
    ok(own is not None, f"P1 owner Service openbao-{self_id} missing")
    if own:
        a = own["metadata"].get("annotations", {})
        ok(a.get("service.cilium.io/global") == "true" and a.get("service.cilium.io/shared") == "true", "P1 owner must be global + shared")
        ok(own["spec"].get("type") == "ClusterIP", "P1 owner must be ClusterIP")
        ok(own["spec"].get("selector", {}).get("openbao-active") == "true", "P1 owner must select the ACTIVE OpenBao")
    for p in peers:
        s = get("Service", f"openbao-{p}")
        ok(s is not None, f"P1 stub openbao-{p} missing")
        if s:
            ok(not s["spec"].get("selector"), f"P1 stub openbao-{p} must have NO selector (no local backends)")
            a = s["metadata"].get("annotations", {})
            ok(a.get("service.cilium.io/global") == "true" and a.get("service.cilium.io/shared") == "false", f"P1 stub openbao-{p} must be global, shared=false")
    stubs = [d["metadata"]["name"] for d in docs if d["kind"] == "Service" and d["metadata"].get("labels", {}).get("catalyst.openova.io/component") == "openbao-region-service-stub"]
    ok(sorted(stubs) == sorted(f"openbao-{p}" for p in peers), f"P1/P8 stubs {stubs} != peers {peers}")

    cm = get("ConfigMap", "openbao-config-desired")
    ok(cm is not None, "P2 openbao-config-desired ConfigMap missing")
    d = cm["data"] if cm else {}
    man = [l.split() for l in d.get("manifest", "").splitlines() if l.strip()]
    pl = lambda path: json.loads(d.get(path.replace("/", ".") + ".json", "{}"))
    if cm:
        ok(pl("identity/oidc/config").get("issuer") == f"http://{host(self_id)}:8200", "P2 issuer must be this region's openbao-<self>")
        role = pl("identity/oidc/role/openbao-peer-push")
        ok(role.get("client_id") == "openbao-peer-push" and role.get("key") == "openbao-peer-push", "P2 identity role aud/key")
        ok(json.loads(role.get("template", "{}")) == {"openova_region": self_id}, "P2 identity token must claim openova_region=<self>")
        ok("openbao-peer-push" in pl("identity/oidc/key/openbao-peer-push").get("allowed_client_ids", []), "P2 key must allow the audience")
        mounts = sorted(o[1] for o in man if o[0] == "mount")
        ok(mounts == sorted(f"sys/auth/jwt-peer-{p}" for p in peers), f"P2/P8 jwt mounts {mounts} != peers {peers}")
        ok(all(f"jwt-peer-{self_id}" not in o[1] for o in man), "P2 a region must not trust ITSELF as a peer")
        for p in peers:
            disc = f"http://{host(p)}:8200/v1/identity/oidc"
            c = pl(f"auth/jwt-peer-{p}/config")
            ok(c.get("oidc_discovery_url") == disc, f"P2 jwt-peer-{p} must discover the PEER issuer, got {c}")
            ops = [o for o in man if f"jwt-peer-{p}" in o[1]]
            ok(len(ops) == 3 and all(len(o) == 3 and o[2] == disc + "/.well-known/openid-configuration" for o in ops), f"P2 jwt-peer-{p} ops must wait on the peer's discovery URL: {ops}")
            r = pl(f"auth/jwt-peer-{p}/role/openbao-peer-push")
            ok(r.get("bound_claims") == {"openova_region": p}, f"P2 jwt-peer-{p} role must bind openova_region={p}")
            ok(r.get("bound_audiences") == ["openbao-peer-push"] and r.get("token_policies") == ["openbao-peer-push"], f"P2 jwt-peer-{p} role aud/policy")
        pol = pl("sys/policies/acl/openbao-peer-push").get("policy", "")
        paths = dict(re.findall(r'path "([^"]+)" \{\s*capabilities = \[([^\]]*)\]', pol))
        ok(set(paths) == {"secret/data/openova/apps/*", "secret/metadata/openova/apps/*"}, f"P3 push policy paths {sorted(paths)}")
        ok(all("delete" not in v and "sudo" not in v for v in paths.values()), "P3 push policy must not grant delete/sudo")
        ok("create" in paths.get("secret/data/openova/apps/*", ""), "P3 push policy must grant create")
        tok = pl("auth/kubernetes/role/openbao-peer-token")
        ok(tok.get("bound_service_account_names") == ["external-secrets"] and tok.get("policies") == ["openbao-peer-token"], f"P4 openbao-peer-token role {tok}")
        eso = pl("auth/kubernetes/role/external-secrets")
        ok(eso.get("policies") == ["external-secrets-read", "external-secrets-push"], f"P4 main ESO role policies {eso.get('policies')}")
        order = [o[1] for o in man]
        ok(order.index("sys/policies/acl/openbao-peer-push") < order.index(f"auth/jwt-peer-{peers[0]}/role/openbao-peer-push") if peers and f"auth/jwt-peer-{peers[0]}/role/openbao-peer-push" in order else False, "P4 the push policy must be written before the roles naming it")
    dep = get("Deployment", "openbao-config")
    ok(dep is not None, "P4 openbao-config Deployment missing")

    ing = get("CiliumNetworkPolicy", "openbao-allow-peer-region-ingress")
    egr = get("CiliumNetworkPolicy", "openbao-allow-peer-region-egress")
    ok(ing is not None and egr is not None, "P5 peer ingress/egress CNPs missing")
    if ing:
        rule = ing["spec"]["ingress"][0]
        ports = [p["port"] for tp in rule["toPorts"] for p in tp["ports"]]
        ok(ports == ["8200"], f"P5 ingress ports {ports}")
        frm = rule["fromEndpoints"]
        ok(any(f["matchLabels"].get("k8s:io.kubernetes.pod.namespace") == "external-secrets-system" for f in frm), "P5 ingress must admit ESO")
        ok(all(any(e["key"] == "io.cilium.k8s.policy.cluster" for e in f.get("matchExpressions", [])) for f in frm), "P5 ingress must match any ClusterMesh cluster")
    if egr:
        ports = [p["port"] for r in egr["spec"]["egress"] for tp in r["toPorts"] for p in tp["ports"]]
        ok(ports == ["8200"], f"P5 egress ports {ports}")
elif mode == "none":
    names = [f"{d['kind']}/{d['metadata']['name']}" for d in docs
             if "peer" in d["metadata"]["name"] or d["metadata"]["name"].startswith("openbao-hw-")]
    ok(not names, f"P7 single-region render carries peer objects: {names}")
    cm = get("ConfigMap", "openbao-config-desired")
    man = cm["data"]["manifest"] if cm else "MISSING"
    ok(cm is not None, "P7 the desired state must still render (single region)")
    ok("peer" not in man and "identity/oidc" not in man, f"P7 single-region desired state carries peer ops: {man}")
else:
    raise SystemExit("bad mode")

for d in docs:   # P6 — every mode
    if d["kind"] == "Service":
        ok(d["spec"].get("type", "ClusterIP") != "NodePort", f"P6 {d['metadata']['name']} is a NodePort")
        ok(all("nodePort" not in p for p in d["spec"].get("ports", [])), f"P6 {d['metadata']['name']} carries a nodePort")
if errs:
    print("\n".join("    " + e for e in errs)); sys.exit(1)
PYEOF

FAIL=0
pass() { echo "  PASS  $1"; }
must()     { if python3 "$TMP/check.py" "$2" "$3" "$4" "$5"; then pass "$1"; else echo "  FAIL  $1"; FAIL=1; fi; }
must_not() { if python3 "$TMP/check.py" "$2" "$3" "$4" "$5" >/dev/null; then echo "  FAIL  $1 (vacuous)"; FAIL=1; else pass "$1"; fi; }

must     "P1-P6 region A: owner openbao-A, stub + trust for B"    peers "$TMP/a.yaml" "$A" "$B"
must     "P1-P6 region B: owner openbao-B, stub + trust for A"    peers "$TMP/b.yaml" "$B" "$A"
must     "P8 three regions: C trusts A and B"                     peers "$TMP/c3.yaml" "$C" "$A,$B"
must     "P7 single region: nothing"                              none  "$TMP/single.yaml" "$A" ""
must     "P7 selfRegion unset: nothing"                           none  "$TMP/noself.yaml" "" ""
must     "P7 self not in the registry: nothing"                   none  "$TMP/foreign.yaml" "x" ""
must_not "P1 vacuity: the single-region render has no peer trust" peers "$TMP/single.yaml" "$A" "$B"
must_not "P2 vacuity: A's render does not trust A as a peer"     peers "$TMP/a.yaml" "$A" "$A"

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-openbao peer-trust render"; exit 1; fi
echo "PASS: bp-openbao peer-region trust — owner + stubs, peer jwt mounts, scoped push grant"
