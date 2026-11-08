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
#   P2  A's reconciler payloads: issuer = http://openbao-<A>…:8200, identity
#       role aud = openbao-peer-push + claim openova_region=<A>; one jwt mount
#       payload per PEER whose oidc_discovery_url is the PEER's issuer and
#       whose role binds openova_region=<peer> — never self;
#   P3  the push policy grants create/update/read on secret/{data,metadata}/
#       openova/apps/* only — no delete, nothing wider;
#   P4  auth-bootstrap binds role peer-trust (policy peer-trust-admin, which
#       reaches only jwt-peer-* mounts + the two peer policies + the identity
#       objects) and role openbao-peer-token to the ESO SA; the main
#       external-secrets role keeps exactly read + push;
#   P5  ingress CNP admits ESO + peer OpenBao from any ClusterMesh cluster on
#       8200 only; egress CNP lets OpenBao reach peers' OpenBao on 8200;
#   P6  no Service of type NodePort and no nodePort field anywhere;
#   P7  single region / self unset / self not in the registry ⇒ NOTHING of
#       the above renders (and the auth-bootstrap script has no peer roles);
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

    cm = get("ConfigMap", "openbao-peer-trust")
    ok(cm is not None, "P2 openbao-peer-trust ConfigMap missing")
    if cm:
        d = cm["data"]
        ok(json.loads(d["issuer.json"])["issuer"] == f"http://{host(self_id)}:8200", "P2 issuer must be this region's openbao-<self>")
        role = json.loads(d["role.json"])
        ok(role["client_id"] == "openbao-peer-push" and role["key"] == "openbao-peer-push", "P2 identity role aud/key")
        ok(json.loads(role["template"]) == {"openova_region": self_id}, "P2 identity token must claim openova_region=<self>")
        ok("openbao-peer-push" in json.loads(d["key.json"])["allowed_client_ids"], "P2 key must allow the audience")
        cfgs = sorted(k for k in d if k.startswith("jwt-config-"))
        ok(cfgs == sorted(f"jwt-config-{p}.json" for p in peers), f"P2/P8 jwt mounts {cfgs} != peers {peers}")
        ok(f"jwt-config-{self_id}.json" not in d, "P2 a region must not trust ITSELF as a peer")
        for p in peers:
            c = json.loads(d.get(f"jwt-config-{p}.json", "{}"))
            ok(c.get("oidc_discovery_url") == f"http://{host(p)}:8200/v1/identity/oidc", f"P2 jwt-peer-{p} must discover the PEER issuer, got {c}")
            r = json.loads(d.get(f"jwt-role-{p}.json", "{}"))
            ok(r.get("bound_claims") == {"openova_region": p}, f"P2 jwt-peer-{p} role must bind openova_region={p}")
            ok(r.get("bound_audiences") == ["openbao-peer-push"] and r.get("token_policies") == ["openbao-peer-push"], f"P2 jwt-peer-{p} role aud/policy")
        pol = json.loads(d["policy-push.json"])["policy"]
        paths = dict(re.findall(r'path "([^"]+)" \{\s*capabilities = \[([^\]]*)\]', pol))
        ok(set(paths) == {"secret/data/openova/apps/*", "secret/metadata/openova/apps/*"}, f"P3 push policy paths {sorted(paths)}")
        ok(all("delete" not in v and "sudo" not in v for v in paths.values()), "P3 push policy must not grant delete/sudo")
        ok("secret/data/openova/apps/*" in paths and "create" in paths["secret/data/openova/apps/*"], "P3 push policy must grant create")
        ok(f"PEERS" in d["reconcile.sh"], "P2 reconcile.sh present")
    dep = get("Deployment", "openbao-peer-trust")
    ok(dep is not None, "P2 openbao-peer-trust Deployment missing")
    if dep:
        env = {e["name"]: e.get("value") for e in dep["spec"]["template"]["spec"]["containers"][0]["env"]}
        ok(env.get("PEERS", "").split() == peers, f"P2 Deployment PEERS={env.get('PEERS')} != {peers}")

    job = get("Job", "openbao-auth-bootstrap")
    script = job["spec"]["template"]["spec"]["containers"][0]["args"][0] if job else ""
    ok("bao policy write peer-trust-admin" in script, "P4 auth-bootstrap must write peer-trust-admin")
    blk = script.split("bao policy write peer-trust-admin -", 1)[-1].split("EOF", 1)[0]
    pp = re.findall(r'path "([^"]+)"', blk)
    ok(pp and all(p == "sys/auth" or p.startswith(("sys/auth/jwt-peer-", "auth/jwt-peer-", "identity/oidc/")) or p in ("sys/policies/acl/openbao-peer-push", "sys/policies/acl/openbao-peer-token") for p in pp), f"P4 peer-trust-admin reaches beyond its scope: {pp}")
    ok(re.search(r"role/peer-trust \\\s*\n\s*bound_service_account_names=openbao-peer-trust", script) is not None, "P4 role peer-trust must bind the openbao-peer-trust SA")
    ok(re.search(r"role/openbao-peer-token \\\s*\n\s*bound_service_account_names=\$ESO_SA_NAME", script) is not None, "P4 role openbao-peer-token must bind the ESO SA")
    ok("policies=external-secrets-read,external-secrets-push \\" in script, "P4 the main ESO role must keep exactly read + push")

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
    job = get("Job", "openbao-auth-bootstrap")
    script = job["spec"]["template"]["spec"]["containers"][0]["args"][0] if job else ""
    ok("peer-trust" not in script and "openbao-peer-token" not in script, "P7 single-region auth-bootstrap must not bind peer roles")
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
