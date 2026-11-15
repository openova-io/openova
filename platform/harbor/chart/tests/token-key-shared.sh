#!/usr/bin/env bash
# bp-harbor — one registry token key for the Sovereign (#7908).
#
# Harbor's core signs registry tokens with /etc/core/private_key.pem and
# drops any presented token that its own key does not verify; the request is
# then anonymous and a push is refused. The upstream chart generates that key
# with `genCA` on EVERY render into its `<fullname>-core` Secret, so each region
# of a two-region Sovereign had its own key (while the registry's public name
# sends one push's requests to both regions), and each upgrade rotated it.
# Measured on a fresh two-region installation on 15 November 2026: 24 of 24
# tagged `GET /v2/` requests answered 200 in the region that signed the token
# and 401 in the other, with `token signature is invalid` logged by the
# receiving core in the second of each cross-region request.
#
# The core now reads the key pair from the Secret `harbor.core.secretName`.
# cert-manager issues it in the primary region and keeps the key on renewal;
# a follower issues nothing and receives the primary's copy from the parent's
# cross-region orchestrator (products/catalyst/bootstrap/api/internal/crossregion).
#
# Cases:
#   1. The core Deployment reads its token key from that Secret, and its pod
#      template names that Secret for bp-reloader (the key is a subPath mount,
#      read once at start).
#   2. The upstream core Secret holds no token key any more (it is rewritten
#      on every render; a key there changes at every upgrade).
#   3. A primary render: a self-signed Issuer and a Certificate that writes
#      exactly that Secret, RSA 2048 in PKCS#1 like upstream, the key kept on
#      renewal.
#   4. The kit slot's own values, substituted the way Flux does for each
#      region role: the primary issues the key, a follower issues nothing and
#      still mounts the same Secret (the copy).
#   5. The chart writes no key material and no Secret of that name itself.
#   6. `harbor.core.secretName: ""` brings the upstream layout back and renders
#      no Certificate (nothing writes a Secret nobody reads).
#
# Usage: bash tests/token-key-shared.sh [CHART_DIR]

set -euo pipefail

CHART_DIR="${1:-$(cd "$(dirname "$0")/.." && pwd)}"
SLOT="${SLOT_FILE:-$CHART_DIR/../../../clusters/_template/bootstrap-kit/19-harbor.yaml}"
helm="${HELM_BIN:-helm}"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
fail() { echo "FAIL: $*" >&2; exit 1; }

if [ ! -d "$CHART_DIR/charts" ] || [ -z "$(ls -A "$CHART_DIR/charts" 2>/dev/null)" ]; then
  "$helm" dependency build "$CHART_DIR" >/dev/null
fi

# docs <render-file> -> every document as one JSON line, with its source.
docs() {
  python3 - "$1" <<'PY'
import json, sys, yaml
text = open(sys.argv[1]).read()
for chunk in text.split("\n---\n"):
    src = ""
    for line in chunk.splitlines():
        if line.startswith("# Source: "):
            src = line[len("# Source: "):]
    # An upstream manifest ends a line with a tab, which Helm accepts and
    # PyYAML does not.
    chunk = "\n".join(l.rstrip(" \t") for l in chunk.splitlines())
    for d in yaml.safe_load_all(chunk):
        if isinstance(d, dict) and d.get("kind"):
            print(json.dumps({"source": src, "doc": d}))
PY
}

"$helm" template harbor "$CHART_DIR" --namespace harbor > "$TMP/render.yaml" || fail "the chart does not render"
docs "$TMP/render.yaml" > "$TMP/render.jsonl"

echo "[token-key] Case 1: the core reads its token key from harbor.core.secretName"
python3 - "$TMP/render.jsonl" <<'PY' || exit 1
import json, sys
for line in open(sys.argv[1]):
    e = json.loads(line)
    d = e["doc"]
    if d["kind"] == "Deployment" and e["source"].endswith("templates/core/core-dpl.yaml"):
        spec = d["spec"]["template"]["spec"]
        v = {x["name"]: x for x in spec["volumes"]}.get("token-service-private-key", {}).get("secret", {})
        if v.get("secretName") != "harbor-token-key":
            print(f"FAIL: the core's token key volume reads Secret {v.get('secretName')!r}, not harbor-token-key: "
                  "the subchart's Secret gets a new key on every render, a different one in each region")
            sys.exit(1)
        m = [x for c in spec["containers"] for x in c.get("volumeMounts", []) if x["name"] == "token-service-private-key"]
        if not m or m[0].get("mountPath") != "/etc/core/private_key.pem" or m[0].get("subPath") != "tls.key":
            print(f"FAIL: the core does not mount tls.key of that Secret at /etc/core/private_key.pem: {m}")
            sys.exit(1)
        # A subPath mount never changes in a running pod: Reloader must roll the
        # core when the Secret changes. Reloader reads the name from the pod
        # template when the Deployment itself carries no reload annotation.
        ann = d["spec"]["template"]["metadata"].get("annotations") or {}
        own = (d["metadata"].get("annotations") or {})
        if ann.get("secret.reloader.stakater.com/reload") != v.get("secretName") or any(k.startswith("secret.reloader") or k.startswith("reloader.") for k in own):
            print(f"FAIL: the core's pod template names {ann.get('secret.reloader.stakater.com/reload')!r} for Reloader, "
                  f"want {v.get('secretName')!r}: a changed key would stay unread until an unrelated restart")
            sys.exit(1)
        sys.exit(0)
print("FAIL: no core Deployment rendered")
sys.exit(1)
PY
echo "  PASS"

echo "[token-key] Case 2: the upstream core Secret holds no token key"
python3 - "$TMP/render.jsonl" <<'PY' || exit 1
import json, sys
for line in open(sys.argv[1]):
    e = json.loads(line)
    if e["doc"]["kind"] == "Secret" and e["source"].endswith("templates/core/core-secret.yaml"):
        left = sorted(k for k in ("tls.key", "tls.crt") if k in (e["doc"].get("data") or {}))
        if left:
            print(f"FAIL: the upstream Secret {e['doc']['metadata']['name']} still carries {left}: it is regenerated on every render")
            sys.exit(1)
        sys.exit(0)
print("FAIL: no upstream core Secret rendered")
sys.exit(1)
PY
echo "  PASS"

# issuance <jsonl> -> "issuer=<n> cert=<n>" for the token key objects, checked for shape.
check_issuance() {
  python3 - "$1" "$2" <<'PY'
import json, sys
path, want = sys.argv[1], sys.argv[2]
issuers, certs = {}, {}
for line in open(path):
    d = json.loads(line)["doc"]
    if d["kind"] == "Issuer":
        issuers[d["metadata"]["name"]] = d
    if d["kind"] == "Certificate" and d["spec"].get("secretName") == "harbor-token-key":
        certs[d["metadata"]["name"]] = d
if want == "none":
    if certs:
        print(f"FAIL: a follower renders a Certificate for the token key ({sorted(certs)}): it would make a key of its own")
        sys.exit(1)
    sys.exit(0)
if len(certs) != 1:
    print(f"FAIL: want exactly one Certificate writing harbor-token-key, got {sorted(certs)}")
    sys.exit(1)
c = next(iter(certs.values()))
pk = c["spec"].get("privateKey") or {}
for k, v in (("algorithm", "RSA"), ("size", 2048), ("encoding", "PKCS1"), ("rotationPolicy", "Never")):
    if pk.get(k) != v:
        print(f"FAIL: the token key Certificate has privateKey.{k}={pk.get(k)!r}, want {v!r}"
              + (" (a renewal would make a new key and break every token in flight)" if k == "rotationPolicy" else ""))
        sys.exit(1)
ref = c["spec"].get("issuerRef") or {}
iss = issuers.get(ref.get("name"))
if ref.get("kind") != "Issuer" or not iss or iss["metadata"]["namespace"] != c["metadata"]["namespace"] or "selfSigned" not in iss["spec"]:
    print(f"FAIL: the token key Certificate's issuer {ref} is not the self-signed Issuer of its namespace")
    sys.exit(1)
PY
}

echo "[token-key] Case 3: a primary render issues the key with cert-manager, kept on renewal"
check_issuance "$TMP/render.jsonl" one || exit 1
echo "  PASS"

echo "[token-key] Case 4: the kit slot's values per region role"
[ -f "$SLOT" ] || fail "kit slot not found at $SLOT (set SLOT_FILE)"
for role in primary secondary; do
  python3 - "$SLOT" "$role" > "$TMP/slot-$role.yaml" <<'PY' || exit 1
import re, sys, yaml
slot, role = sys.argv[1], sys.argv[2]
# Flux substitutes the variables in the raw text, then parses it.
env = {"SOVEREIGN_REGION_ROLE": role, "SOVEREIGN_FQDN": "t00.omani.works"}
def sub(m):
    name, default = m.group(1), m.group(3) or ""
    return env.get(name, default)
text = re.sub(r"\$\{([A-Z0-9_]+)(:?[=-]([^}]*))?\}", sub, open(slot).read())
hr = next(d for d in yaml.safe_load_all(text) if isinstance(d, dict) and d.get("kind") == "HelmRelease")
values = hr["spec"]["values"]
if values.get("crossRegion", {}).get("role") != role:
    print(f"FAIL: the slot does not pass SOVEREIGN_REGION_ROLE to crossRegion.role (got {values.get('crossRegion')})", file=sys.stderr)
    sys.exit(1)
print(yaml.safe_dump(values))
PY
  "$helm" template harbor "$CHART_DIR" --namespace harbor -f "$TMP/slot-$role.yaml" > "$TMP/r-$role.yaml" \
    || fail "the chart does not render with the slot's values for role $role"
  docs "$TMP/r-$role.yaml" > "$TMP/r-$role.jsonl"
  if [ "$role" = primary ]; then check_issuance "$TMP/r-$role.jsonl" one || exit 1; else check_issuance "$TMP/r-$role.jsonl" none || exit 1; fi
  python3 - "$TMP/r-$role.jsonl" "$role" <<'PY' || exit 1
import json, sys
for line in open(sys.argv[1]):
    e = json.loads(line)
    if e["doc"]["kind"] == "Deployment" and e["source"].endswith("templates/core/core-dpl.yaml"):
        v = {x["name"]: x for x in e["doc"]["spec"]["template"]["spec"]["volumes"]}["token-service-private-key"]["secret"]
        if v.get("secretName") != "harbor-token-key":
            print(f"FAIL: role {sys.argv[2]}: the core mounts {v.get('secretName')!r}, not the shared harbor-token-key")
            sys.exit(1)
        sys.exit(0)
print(f"FAIL: role {sys.argv[2]}: no core Deployment")
sys.exit(1)
PY
done
echo "  PASS"

echo "[token-key] Case 5: the chart writes no key and no Secret harbor-token-key itself"
python3 - "$TMP/render.jsonl" "$TMP/r-secondary.jsonl" <<'PY' || exit 1
import json, sys
for path in sys.argv[1:]:
    for line in open(path):
        e = json.loads(line)
        d = e["doc"]
        if d["kind"] == "Secret" and d["metadata"]["name"] == "harbor-token-key":
            print(f"FAIL: {e['source']} renders the Secret harbor-token-key: a chart-made key differs per region")
            sys.exit(1)
        if d["kind"] == "Secret" and "tls.key" in (d.get("data") or {}) | (d.get("stringData") or {}):
            print(f"FAIL: {e['source']} renders a token key into Secret {d['metadata']['name']}")
            sys.exit(1)
PY
echo "  PASS"

echo "[token-key] Case 6: an empty harbor.core.secretName brings the upstream layout back"
"$helm" template harbor "$CHART_DIR" --namespace harbor --set harbor.core.secretName= > "$TMP/optout.yaml" \
  || fail "the chart does not render with an empty harbor.core.secretName"
docs "$TMP/optout.yaml" > "$TMP/optout.jsonl"
check_issuance "$TMP/optout.jsonl" none >/dev/null 2>&1 || fail "a Certificate renders although the core does not read its Secret"
grep -q '^  tls.key: ' "$TMP/optout.yaml" \
  || fail "with an empty harbor.core.secretName the upstream Secret should carry the key again (render read wrongly)"
echo "  PASS"

echo "[token-key] All cases green."
