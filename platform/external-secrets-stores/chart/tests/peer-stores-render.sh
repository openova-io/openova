#!/usr/bin/env bash
# bp-external-secrets-stores — peer-region ClusterSecretStores (ADR-0015, Refs #7067).
#
# Rendered per region from the region registry, asserts:
#
#   S1  region A renders vault-<B> and NOT vault-<A>; region B renders
#       vault-<A> and NOT vault-<B>; vault-region1 (self) stays in both;
#   S2  vault-<peer> dials http://openbao-<peer>.openbao.svc.cluster.local:8200
#       and authenticates with jwt mount jwt-peer-<self>, role
#       openbao-peer-push, token from Secret openbao-peer-push-token;
#   S3  the token is produced by a VaultDynamicSecret reading
#       identity/oidc/token/openbao-peer-push from THIS region's OpenBao via
#       the k8s-auth role openbao-peer-token, refreshed by an ExternalSecret;
#   S4  single region / self unset / self not in the registry ⇒ only
#       vault-region1 (no peer store, no generator);
#   S5  three regions: two peer stores;
#   S6  CROSS-CHART CONTRACT with bp-openbao rendered for the PEER region:
#       the peer renders the Service this store dials (owner, with backends),
#       this region renders the selector-less stub of it, the peer renders a
#       jwt mount payload named jwt-peer-<self> whose role is the one this
#       store logs in as, and this region's OpenBao issues tokens for that
#       role's audience with claim openova_region=<self>.
#
# Vacuity: S1 against the single-region render must fail.
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
BAO_DIR="$(cd "$CHART_DIR/../../openbao/chart" && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
if ! ls "$BAO_DIR"/charts/*.tgz >/dev/null 2>&1; then helm dependency build "$BAO_DIR" >/dev/null; fi

A="hw-me-east-215-a-rtz-prod"
B="hw-me-east-215-b-rtz-prod"
C="hw-me-east-215-c-rtz-prod"
REG2='[{"cloudRegion":"'"$A"'","provider":"huawei"},{"cloudRegion":"'"$B"'","provider":"huawei"}]'
REG3='["'"$A"'","'"$B"'","'"$C"'"]'
REG1='[{"cloudRegion":"'"$A"'"}]'

vals() {  # $1 file $2 key $3 self $4 regions — the registry as Flux substitutes it (a string)
  python3 -c 'import json,sys; print(json.dumps({sys.argv[1]: {"selfRegion": sys.argv[2], "regions": sys.argv[3]}}))' "$2" "$3" "$4" > "$1"
}
stores() {  # $1 out $2 self $3 regions
  vals "$1.v.json" peerStores "$2" "$3"
  helm template external-secrets-stores "$CHART_DIR" --namespace external-secrets-system -f "$1.v.json" > "$1"
}
bao() {  # $1 out $2 self $3 regions
  vals "$1.v.json" peerTrust "$2" "$3"
  helm template openbao "$BAO_DIR" --namespace openbao --set autoUnseal.enabled=true -f "$1.v.json" > "$1"
}
stores "$TMP/a.yaml" "$A" "$REG2"
stores "$TMP/b.yaml" "$B" "$REG2"
stores "$TMP/c3.yaml" "$C" "$REG3"
stores "$TMP/single.yaml" "$A" "$REG1"
stores "$TMP/noself.yaml" "" "$REG2"
stores "$TMP/foreign.yaml" "hw-eu-west-1-a-rtz-prod" "$REG2"
bao "$TMP/bao-a.yaml" "$A" "$REG2"
bao "$TMP/bao-b.yaml" "$B" "$REG2"

cat > "$TMP/check.py" <<'PYEOF'
import json, sys, yaml
mode, path, self_id = sys.argv[1], sys.argv[2], sys.argv[3]
peers = [p for p in sys.argv[4].split(",") if p]
load = lambda p: [d for d in yaml.safe_load_all(open(p)) if d]
docs = load(path)
errs = []
def ok(c, m):
    if not c: errs.append(m)
def get(ds, kind, name):
    m = [d for d in ds if d["kind"] == kind and d["metadata"]["name"] == name]
    return m[0] if m else None
stores = sorted(d["metadata"]["name"] for d in docs if d["kind"] == "ClusterSecretStore")

if mode in ("peers", "contract"):
    ok(stores == sorted(["vault-region1"] + [f"vault-{p}" for p in peers]), f"S1/S5 stores {stores}, want vault-region1 + {peers}")
    ok(f"vault-{self_id}" not in stores, "S1 a region must not render a peer store for ITSELF")
    for p in peers:
        s = get(docs, "ClusterSecretStore", f"vault-{p}")
        if not s: continue
        v = s["spec"]["provider"]["vault"]
        ok(v["server"] == f"http://openbao-{p}.openbao.svc.cluster.local:8200", f"S2 vault-{p} server {v['server']}")
        j = v["auth"].get("jwt", {})
        ok(j.get("path") == f"jwt-peer-{self_id}" and j.get("role") == "openbao-peer-push", f"S2 vault-{p} jwt auth {j}")
        ok(j.get("secretRef") == {"name": "openbao-peer-push-token", "namespace": "external-secrets-system", "key": "token"}, f"S2 vault-{p} token ref {j.get('secretRef')}")
    g = get(docs, "VaultDynamicSecret", "openbao-peer-push-token")
    ok(g is not None, "S3 VaultDynamicSecret missing")
    if g:
        ok(g["spec"]["path"] == "identity/oidc/token/openbao-peer-push" and g["spec"]["method"] == "GET", "S3 generator path/method")
        ok(g["spec"]["provider"]["server"] == "http://openbao.openbao.svc.cluster.local:8200", "S3 generator must read THIS region's OpenBao")
        ok(g["spec"]["provider"]["auth"]["kubernetes"]["role"] == "openbao-peer-token", "S3 generator role")
    es = get(docs, "ExternalSecret", "openbao-peer-push-token")
    ok(es is not None and es["spec"]["target"]["name"] == "openbao-peer-push-token"
       and es["spec"]["dataFrom"][0]["sourceRef"]["generatorRef"]["kind"] == "VaultDynamicSecret", "S3 token ExternalSecret")

if mode == "contract":     # S6 — argv[5] = this region's bp-openbao, argv[6] = the peer's
    mine, theirs = load(sys.argv[5]), load(sys.argv[6])
    p = peers[0]
    owner = get(theirs, "Service", f"openbao-{p}")
    ok(owner is not None and owner["spec"].get("selector", {}).get("openbao-active") == "true", f"S6 peer {p} must render the OWNER openbao-{p} with backends")
    stub = get(mine, "Service", f"openbao-{p}")
    ok(stub is not None and not stub["spec"].get("selector"), f"S6 this region must render the selector-less stub openbao-{p}")
    cm = get(theirs, "ConfigMap", "openbao-peer-trust")
    d = cm["data"] if cm else {}
    ok(f"jwt-config-{self_id}.json" in d, f"S6 peer {p} has no jwt-peer-{self_id} mount for this store's auth path")
    role = json.loads(d.get(f"jwt-role-{self_id}.json", "{}"))
    ok(role.get("bound_claims") == {"openova_region": self_id} and role.get("bound_audiences") == ["openbao-peer-push"], f"S6 peer role binds {role}")
    ok(json.loads(d.get(f"jwt-config-{self_id}.json", "{}")).get("oidc_discovery_url") == f"http://openbao-{self_id}.openbao.svc.cluster.local:8200/v1/identity/oidc", "S6 peer must discover THIS region's issuer")
    mcm = get(mine, "ConfigMap", "openbao-peer-trust")
    md = mcm["data"] if mcm else {}
    r = json.loads(md.get("role.json", "{}"))
    ok(r.get("client_id") == "openbao-peer-push" and json.loads(r.get("template", "{}")) == {"openova_region": self_id}, f"S6 this region's issuer role {r}")
    ok(json.loads(md.get("issuer.json", "{}")).get("issuer") == f"http://openbao-{self_id}.openbao.svc.cluster.local:8200", "S6 issuer must be the openbao-<self> Service the peer discovers")
elif mode == "none":
    ok(stores == ["vault-region1"], f"S4 stores {stores}, want only vault-region1")
    ok(get(docs, "VaultDynamicSecret", "openbao-peer-push-token") is None, "S4 single region renders the token generator")
elif mode != "peers":
    raise SystemExit("bad mode")
if errs:
    print("\n".join("    " + e for e in errs)); sys.exit(1)
PYEOF

FAIL=0
pass() { echo "  PASS  $1"; }
must()     { if python3 "$TMP/check.py" "$@"; then pass "$MSG"; else echo "  FAIL  $MSG"; FAIL=1; fi; }
must_not() { if python3 "$TMP/check.py" "$@" >/dev/null; then echo "  FAIL  $MSG (vacuous)"; FAIL=1; else pass "$MSG"; fi; }

MSG="S1-S3 region A renders vault-B";            must peers "$TMP/a.yaml" "$A" "$B"
MSG="S1-S3 region B renders vault-A";            must peers "$TMP/b.yaml" "$B" "$A"
MSG="S5 three regions: C renders vault-A + B";   must peers "$TMP/c3.yaml" "$C" "$A,$B"
MSG="S4 single region: vault-region1 only";      must none "$TMP/single.yaml" "$A" ""
MSG="S4 selfRegion unset: vault-region1 only";   must none "$TMP/noself.yaml" "" ""
MSG="S4 self not in registry: vault-region1 only"; must none "$TMP/foreign.yaml" "x" ""
MSG="S6 A's vault-B matches B's OpenBao trust";  must contract "$TMP/a.yaml" "$A" "$B" "$TMP/bao-a.yaml" "$TMP/bao-b.yaml"
MSG="S6 B's vault-A matches A's OpenBao trust";  must contract "$TMP/b.yaml" "$B" "$A" "$TMP/bao-b.yaml" "$TMP/bao-a.yaml"
MSG="S1 vacuity: single region has no peer store"; must_not peers "$TMP/single.yaml" "$A" "$B"
MSG="S6 vacuity: A's store vs A's own OpenBao as the peer"; must_not contract "$TMP/a.yaml" "$A" "$B" "$TMP/bao-b.yaml" "$TMP/bao-a.yaml"

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-external-secrets-stores peer stores"; exit 1; fi
echo "PASS: bp-external-secrets-stores — one store per peer region, contract-matched with bp-openbao"
