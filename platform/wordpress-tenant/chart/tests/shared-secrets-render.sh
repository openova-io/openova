#!/usr/bin/env bash
# bp-wordpress-tenant — shared cross-region secrets render gate (0.5.1, Refs #7067).
#
# A two-region CNPG pair needs `<pair>-replication-ca` and
# `<pair>-app-credentials` with the SAME bytes in every region. The chart
# renders, per region:
#
#   S1  primary: the SEED — ESO Password generator + cert-manager self-signed
#       CA (Issuer + Certificate) + two PushSecrets, both
#       updatePolicy=IfNotExists and deletionPolicy=None (write-once; never
#       regenerated, never deleted on a primary move);
#   S2  replica: NO seed object at all (only the writer pushes);
#   S3  every region: the two ExternalSecrets, reading the SAME remote keys in
#       both regions even though each region's release has a different name
#       (the HelmRelease is `<app>-<cluster>`) — the prefix is the PAIR
#       identity `openova/apps/<ns>/<cnpgClusterName>`;
#   S4  the seed writes exactly the keys/properties the ExternalSecrets read;
#   S5  swapped primary: the seed moves to B, A renders none;
#   S6  seed.secretStoreRefs fans the push out to every listed store;
#   S7  storeRef.name="" ⇒ neither seed nor ExternalSecret; seed.enabled=false
#       ⇒ ExternalSecrets only; contract absent ⇒ none of it.
#
# Vacuity: S1's body run against the replica render must FAIL, and S2's body
# against the primary render must FAIL.
#
# Usage: bash tests/shared-secrets-render.sh [CHART_DIR]
set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
cd "$CHART_DIR"

if [ ! -d charts ] || [ "$(ls charts 2>/dev/null | wc -l)" -lt 2 ]; then
  helm dependency build >/dev/null
fi

A="hw-me-east-215-a-rtz-prod"
B="hw-me-east-215-b-rtz-prod"
COMMON=(
  --namespace acme
  --set smeDomain=acme.example.local
  --set adminUser.email=admin@acme.example.local
  --api-versions postgresql.cnpg.io/v1
  --api-versions cilium.io/v2
  --api-versions external-secrets.io/v1alpha1
  --api-versions external-secrets.io/v1beta1
  --api-versions cert-manager.io/v1
)
topo() {  # $1 self  $2 primary  $3 peer
  printf '%s\n' --set openova.topology.mode=active-hot-standby \
    --set "openova.topology.self=$1" --set "openova.topology.primary=$2" \
    --set "openova.topology.peers={$3}" \
    --set database.topology.promotion.mechanism=manual
}
render() {  # $1 out, $2 release, rest = args
  local out="$1" rel="$2"; shift 2
  helm template "$rel" . "${COMMON[@]}" "$@" > "$out"
}

mapfile -t TA < <(topo "$A" "$A" "$B")
mapfile -t TB < <(topo "$B" "$A" "$A")
mapfile -t SA < <(topo "$A" "$B" "$B")
mapfile -t SB < <(topo "$B" "$B" "$A")
# Per-region release names, exactly as the application-controller names them.
render "$TMP/a-primary.yaml" "wordpress-rtz-a" "${TA[@]}"
render "$TMP/b-replica.yaml" "wordpress-rtz-b" "${TB[@]}"
render "$TMP/a-replica.yaml" "wordpress-rtz-a" "${SA[@]}"
render "$TMP/b-primary.yaml" "wordpress-rtz-b" "${SB[@]}" --set database.topology.promotionToken=T
render "$TMP/fanout.yaml" "wordpress-rtz-a" "${TA[@]}" \
  --set 'database.topology.sharedSecrets.seed.secretStoreRefs[0].name=vault-region1' \
  --set 'database.topology.sharedSecrets.seed.secretStoreRefs[1].name=vault-peer-b'
render "$TMP/nostore.yaml" "wordpress-rtz-a" "${TA[@]}" --set database.topology.sharedSecrets.externalSecret.storeRef.name=
render "$TMP/noseed.yaml" "wordpress-rtz-a" "${TA[@]}" --set database.topology.sharedSecrets.seed.enabled=false
render "$TMP/legacy.yaml" "wordpress"

cat > "$TMP/check.py" <<'PYEOF'
import sys, yaml, json
mode, path = sys.argv[1], sys.argv[2]
docs = [d for d in yaml.safe_load_all(open(path)) if d]
kind = lambda k: [d for d in docs if d.get("kind") == k]
errs = []
def ok(c, m):
    if not c: errs.append(m)
PREFIX = "openova/apps/acme/wordpress-db"
SEED_KINDS = ("PushSecret", "Password", "Issuer", "Certificate")

def es_keys(ds):
    out = set()
    for e in ds:
        for d in e["spec"].get("data", []):
            out.add((d["remoteRef"]["key"], d["remoteRef"]["property"]))
    return out

def ps_keys(ds):
    out = set()
    for p in ds:
        for d in p["spec"]["data"]:
            r = d["match"]["remoteRef"]
            out.add((r["remoteKey"], r["property"]))
    return out

if mode == "seed":                      # S1 + S4
    ps = kind("PushSecret")
    ok(len(ps) == 2, f"S1 want 2 PushSecrets, got {len(ps)}")
    for p in ps:
        ok(p["spec"].get("updatePolicy") == "IfNotExists", f"S1 {p['metadata']['name']} must be IfNotExists (write-once)")
        ok(p["spec"].get("deletionPolicy") == "None", f"S1 {p['metadata']['name']} must keep the material on uninstall")
    ok(len(kind("Password")) == 1, "S1 want the ESO Password generator")
    certs = kind("Certificate")
    ok(len(certs) == 1 and certs[0]["spec"].get("isCA") is True, "S1 want one isCA Certificate")
    ok(len(kind("Issuer")) == 1, "S1 want the self-signed Issuer")
    gen = [p for p in ps if "generatorRef" in p["spec"]["selector"]]
    ok(len(gen) == 1, "S1 the db-owner PushSecret must push from the generator")
    want = {(PREFIX + "/db-owner", "username"), (PREFIX + "/db-owner", "password"),
            (PREFIX + "/replication-ca", "ca.crt"), (PREFIX + "/replication-ca", "ca.key")}
    ok(ps_keys(ps) == want, f"S4 seed writes {sorted(ps_keys(ps))}, want {sorted(want)}")
    ok(es_keys(kind("ExternalSecret")) == want, f"S4 readers read {sorted(es_keys(kind('ExternalSecret')))}, want {sorted(want)}")
elif mode == "noseed-replica":         # S2 + S3
    for k in SEED_KINDS:
        ok(not kind(k), f"S2 a replica rendered {k} — only the writer may push")
    want = {(PREFIX + "/db-owner", "username"), (PREFIX + "/db-owner", "password"),
            (PREFIX + "/replication-ca", "ca.crt"), (PREFIX + "/replication-ca", "ca.key")}
    ok(es_keys(kind("ExternalSecret")) == want, f"S3 replica reads {sorted(es_keys(kind('ExternalSecret')))}, want {sorted(want)}")
elif mode == "fanout":                  # S6
    for p in kind("PushSecret"):
        names = [s["name"] for s in p["spec"]["secretStoreRefs"]]
        ok(names == ["vault-region1", "vault-peer-b"], f"S6 {p['metadata']['name']} stores {names}")
elif mode == "none":                    # S7
    for k in SEED_KINDS + ("ExternalSecret",):
        ok(not kind(k), f"S7 rendered {k}")
elif mode == "es-only":
    for k in SEED_KINDS:
        ok(not kind(k), f"S7 seed.enabled=false rendered {k}")
    ok(len(kind("ExternalSecret")) == 2, "S7 seed.enabled=false must keep the 2 ExternalSecrets")
else:
    raise SystemExit("bad mode " + mode)
if errs:
    print("\n".join("    " + e for e in errs)); sys.exit(1)
PYEOF

FAIL=0
pass() { echo "  PASS  $1"; }
must()     { if python3 "$TMP/check.py" "$2" "$3"; then pass "$1"; else echo "  FAIL  $1"; FAIL=1; fi; }
must_not() { if python3 "$TMP/check.py" "$2" "$3" >/dev/null; then echo "  FAIL  $1 (vacuous: passed on the wrong render)"; FAIL=1; else pass "$1"; fi; }

must     "S1/S4 A primary renders the write-once seed"      seed "$TMP/a-primary.yaml"
must_not "S1 vacuity: the replica render carries no seed"   seed "$TMP/b-replica.yaml"
must     "S2/S3 B replica: readers only"                    noseed-replica "$TMP/b-replica.yaml"
must_not "S2 vacuity: the primary render has a seed"        noseed-replica "$TMP/a-primary.yaml"
must     "S5 swapped: B primary renders the seed"           seed "$TMP/b-primary.yaml"
must     "S5 swapped: A replica renders none"               noseed-replica "$TMP/a-replica.yaml"
must     "S6 seed fans out to every listed store"           fanout "$TMP/fanout.yaml"
must     "S7 storeRef.name empty: nothing"                  none "$TMP/nostore.yaml"
must     "S7 seed.enabled=false: ExternalSecrets only"      es-only "$TMP/noseed.yaml"
must     "S7 contract absent: nothing"                      none "$TMP/legacy.yaml"

# S3 across regions: the two regions' readers are byte-identical in their
# remote references although the releases differ in name.
ka="$(python3 -c "import yaml,sys,json;print(json.dumps(sorted((d['remoteRef']['key'],d['remoteRef']['property']) for e in yaml.safe_load_all(open(sys.argv[1])) if e and e.get('kind')=='ExternalSecret' for d in e['spec']['data'])))" "$TMP/a-primary.yaml")"
kb="$(python3 -c "import yaml,sys,json;print(json.dumps(sorted((d['remoteRef']['key'],d['remoteRef']['property']) for e in yaml.safe_load_all(open(sys.argv[1])) if e and e.get('kind')=='ExternalSecret' for d in e['spec']['data'])))" "$TMP/b-replica.yaml")"
if [ "$ka" = "$kb" ] && [ "$ka" != "[]" ]; then pass "S3 A and B read the same remote keys"; else echo "  FAIL  S3 A reads $ka, B reads $kb"; FAIL=1; fi

if [ "$FAIL" -ne 0 ]; then echo "FAIL: bp-wordpress-tenant shared-secrets render"; exit 1; fi
echo "PASS: bp-wordpress-tenant shared secrets — writer-only write-once seed, identical readers in every region"
