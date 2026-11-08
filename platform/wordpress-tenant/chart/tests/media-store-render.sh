#!/usr/bin/env bash
# bp-wordpress-tenant — media store render gate (ADR-0015 D6b, Refs #7067).
#
# WordPress uploads used to live on the region-local `wp-content` PVC; after a
# primary move the new primary served posts whose media existed only in the
# old region. 0.6.0 moves media to the replicated SeaweedFS bucket. This test
# pins that shape in every topology a region's release can render:
#
#   M1  default (media.store=s3), singleton: NO PersistentVolumeClaim; the
#       wp-content volume is an emptyDir; the S3 env contract is set
#       (endpoint, bucket=replicated, prefix=<ns>/<fullname>, credentials DIR);
#       credentials come from a mounted Secret volume, never an env literal;
#       DISALLOW_FILE_MODS is defined; the reflected credentials Secret
#       reflects seaweedfs/seaweedfs-s3-secret; the NetworkPolicy admits
#       egress to the seaweedfs namespace on 8333.
#   M2  active-hot-standby, self = primary AND self = standby: M1's no-PVC +
#       S3 contract hold in BOTH regions' renders (each region talks to its
#       own SeaweedFS; filer.sync replicates the bucket).
#   M3  media.store=pvc under a multi-region topology FAILS the render
#       naming D6; media.store=pvc single-region keeps the legacy PVC (with
#       the openova.io/volume-name label) and no S3 contract.
#   M4  a bucket bp-seaweedfs does not replicate (not `replicated[-*]`) FAILS.
#   M5  media-migrate: rendered with migrateLegacyPvc=always on the primary
#       (mounts the legacy PVC read-only); NOT rendered on the standby; NOT
#       rendered in `auto` when the PVC does not exist (helm template has no
#       cluster, so lookup is empty).
#
# VACUITY: the no-PVC assertion is re-run against the media.store=pvc render
# and must FAIL there — a check that passes on the PVC render is not checking.
#
# Usage: bash tests/media-store-render.sh [CHART_DIR]
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
COMMON=(--namespace acme --set smeDomain=acme.example.local --set adminUser.email=admin@acme.example.local
  --api-versions postgresql.cnpg.io/v1 --api-versions cilium.io/v2)
MULTI=(--set openova.topology.mode=active-hot-standby --set database.topology.promotion.mechanism=manual)

fails=0
pass() { printf '  PASS  %s\n' "$1"; }
fail() { printf '  FAIL  %s\n' "$1"; fails=$((fails + 1)); }

render() { # <out> <args...>
  local out="$1"; shift
  helm template wp . "${COMMON[@]}" "$@" > "$out"
}

cat > "$TMP/s3shape.py" <<'PYEOF'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if isinstance(d, dict)]
want_migrate = sys.argv[2]  # yes | no
errs = []
kinds = [d.get("kind") for d in docs]
if "PersistentVolumeClaim" in kinds:
    errs.append("a PersistentVolumeClaim is rendered under media.store=s3")
deps = [d for d in docs if d.get("kind") == "Deployment"]
if len(deps) != 1:
    errs.append(f"want 1 Deployment, got {len(deps)}")
else:
    pod = deps[0]["spec"]["template"]["spec"]
    vols = {v["name"]: v for v in pod.get("volumes", [])}
    if "emptyDir" not in vols.get("wp-content", {}):
        errs.append(f"wp-content volume is not an emptyDir: {vols.get('wp-content')}")
    sec = vols.get("media-s3", {}).get("secret", {})
    if not sec.get("secretName"):
        errs.append("no media-s3 Secret volume")
    paths = sorted(i["path"] for i in sec.get("items", []))
    if paths != ["access-key-id", "secret-access-key"]:
        errs.append(f"media-s3 items = {paths}")
    c = [x for x in pod["containers"] if x["name"] == "wordpress"][0]
    env = {e["name"]: e for e in c.get("env", [])}
    def val(n):
        return (env.get(n) or {}).get("value")
    if val("OPENOVA_MEDIA_S3_ENDPOINT") != "http://seaweedfs-s3.seaweedfs.svc.cluster.local:8333":
        errs.append(f"endpoint = {val('OPENOVA_MEDIA_S3_ENDPOINT')!r}")
    if val("OPENOVA_MEDIA_S3_BUCKET") != "replicated":
        errs.append(f"bucket = {val('OPENOVA_MEDIA_S3_BUCKET')!r}")
    if val("OPENOVA_MEDIA_S3_PREFIX") != "acme/wp-bp-wordpress-tenant":
        errs.append(f"prefix = {val('OPENOVA_MEDIA_S3_PREFIX')!r}")
    if val("OPENOVA_MEDIA_S3_CREDENTIALS_DIR") != "/etc/openova/media-s3":
        errs.append("credentials dir unset")
    for n, e in env.items():
        u = n.upper()
        if "value" in e and any(t in u for t in ("KEY", "SECRET", "TOKEN", "PASSWORD")):
            errs.append(f"env {n} carries a literal value (credential-shaped name)")
    if "DISALLOW_FILE_MODS" not in (val("WORDPRESS_CONFIG_EXTRA") or ""):
        errs.append("DISALLOW_FILE_MODS not defined")
    mounts = {m["name"]: m for m in c.get("volumeMounts", [])}
    if mounts.get("media-s3", {}).get("mountPath") != "/etc/openova/media-s3":
        errs.append("media-s3 not mounted at /etc/openova/media-s3")
secrets = [d for d in docs if d.get("kind") == "Secret" and d["metadata"]["name"].endswith("-media-s3")]
if len(secrets) != 1:
    errs.append(f"want 1 media-s3 Secret, got {len(secrets)}")
else:
    ann = secrets[0]["metadata"].get("annotations", {})
    if ann.get("reflector.v1.k8s.emberstack.com/reflects") != "seaweedfs/seaweedfs-s3-secret":
        errs.append(f"media Secret reflects = {ann.get('reflector.v1.k8s.emberstack.com/reflects')!r}")
    if any(v for v in (secrets[0].get("data") or {}).values()):
        errs.append("media Secret carries non-empty committed data")
nps = [d for d in docs if d.get("kind") == "NetworkPolicy" and d["metadata"]["name"] == "wp-bp-wordpress-tenant"]
ok = False
for np_ in nps:
    for rule in np_["spec"].get("egress", []):
        to = rule.get("to", [])
        ports = [p.get("port") for p in rule.get("ports", [])]
        if any((t.get("namespaceSelector") or {}).get("matchLabels", {}).get("kubernetes.io/metadata.name") == "seaweedfs" for t in to) and 8333 in ports:
            ok = True
if not ok:
    errs.append("WordPress NetworkPolicy has no egress to seaweedfs:8333")
jobs = [d for d in docs if d.get("kind") == "Job" and d["metadata"]["name"].endswith("-media-migrate")]
if want_migrate == "yes":
    if len(jobs) != 1:
        errs.append(f"want the media-migrate Job, got {len(jobs)}")
    else:
        vols = {v["name"]: v for v in jobs[0]["spec"]["template"]["spec"]["volumes"]}
        pvc = vols.get("legacy", {}).get("persistentVolumeClaim", {})
        if pvc.get("claimName") != "wp-bp-wordpress-tenant-wp-content" or not pvc.get("readOnly"):
            errs.append(f"media-migrate legacy mount = {pvc}")
elif jobs:
    errs.append("media-migrate Job rendered where it must not be")
if not docs:
    errs.append("render is EMPTY")
for e in errs:
    print("        " + e)
sys.exit(1 if errs else 0)
PYEOF

check() { # <label> <render> <want-migrate>
  if python3 "$TMP/s3shape.py" "$2" "$3"; then pass "$1"; else fail "$1"; fi
}

echo "══ M1 default singleton ══"
render "$TMP/single.yaml"
check "M1 singleton: media in the replicated bucket, no PVC" "$TMP/single.yaml" no

echo "══ M2 active-hot-standby, both regions ══"
render "$TMP/a-primary.yaml" "${MULTI[@]}" --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}"
check "M2 self=primary (A)" "$TMP/a-primary.yaml" no
render "$TMP/b-standby.yaml" "${MULTI[@]}" --set openova.topology.self="$B" --set openova.topology.primary="$A" --set "openova.topology.peers={$A}"
check "M2 self=standby (B)" "$TMP/b-standby.yaml" no
render "$TMP/b-primary.yaml" "${MULTI[@]}" --set openova.topology.self="$B" --set openova.topology.primary="$B" --set "openova.topology.peers={$A}"
check "M2 self=primary (B, swapped)" "$TMP/b-primary.yaml" no

echo "══ M3 legacy media.store=pvc ══"
if helm template wp . "${COMMON[@]}" "${MULTI[@]}" --set media.store=pvc \
     --set openova.topology.self="$A" --set openova.topology.primary="$A" --set "openova.topology.peers={$B}" \
     > /dev/null 2> "$TMP/pvc-multi.err"; then
  fail "M3 media.store=pvc + multi-region rendered (must fail closed)"
elif grep -q "ADR-0015 D6" "$TMP/pvc-multi.err"; then
  pass "M3 media.store=pvc + multi-region fails closed naming D6"
else
  fail "M3 media.store=pvc + multi-region failed for another reason: $(tail -1 "$TMP/pvc-multi.err")"
fi
render "$TMP/pvc-single.yaml" --set media.store=pvc
if python3 - "$TMP/pvc-single.yaml" <<'PYEOF'
import sys, yaml
docs = [d for d in yaml.safe_load_all(open(sys.argv[1])) if isinstance(d, dict)]
pvcs = [d for d in docs if d.get("kind") == "PersistentVolumeClaim"]
assert len(pvcs) == 1, pvcs
assert pvcs[0]["metadata"]["labels"].get("openova.io/volume-name") == "wp-content"
dep = [d for d in docs if d.get("kind") == "Deployment"][0]
c = dep["spec"]["template"]["spec"]["containers"][0]
assert not any(e["name"].startswith("OPENOVA_MEDIA_") for e in c.get("env", [])), "S3 contract on the pvc render"
assert not any(d.get("kind") == "Secret" and d["metadata"]["name"].endswith("-media-s3") for d in docs)
PYEOF
then pass "M3 media.store=pvc single-region keeps the labelled legacy PVC, no S3 contract"
else fail "M3 media.store=pvc single-region shape"; fi
# VACUITY — the S3-shape check must reject the PVC render.
if python3 "$TMP/s3shape.py" "$TMP/pvc-single.yaml" no > /dev/null; then
  fail "M3 VACUITY: the S3-shape check also passes the media.store=pvc render"
else
  pass "M3 vacuity: the S3-shape check rejects the PVC render"
fi

echo "══ M4 non-replicated bucket ══"
if helm template wp . "${COMMON[@]}" --set media.s3.bucket=wp-media > /dev/null 2> "$TMP/bucket.err"; then
  fail "M4 bucket=wp-media rendered (must fail: not replicated cross-region)"
elif grep -q "not a cross-region replicated bucket" "$TMP/bucket.err"; then
  pass "M4 non-replicated bucket fails closed"
else
  fail "M4 failed for another reason: $(tail -1 "$TMP/bucket.err")"
fi
render "$TMP/bucket-suffix.yaml" --set media.s3.bucket=replicated-acme-wp
if grep -q '"replicated-acme-wp"' "$TMP/bucket-suffix.yaml"; then pass "M4 replicated-<suffix> bucket admitted"; else fail "M4 replicated-<suffix> bucket"; fi

echo "══ M5 legacy-PVC media migration ══"
render "$TMP/migrate-a.yaml" --set media.migrateLegacyPvc=always
check "M5 migrateLegacyPvc=always renders the read-only migrate Job" "$TMP/migrate-a.yaml" yes
render "$TMP/migrate-b.yaml" "${MULTI[@]}" --set media.migrateLegacyPvc=always \
  --set openova.topology.self="$B" --set openova.topology.primary="$A" --set "openova.topology.peers={$A}"
check "M5 standby region renders no migrate Job" "$TMP/migrate-b.yaml" no
# `auto` with no cluster (lookup empty) = no Job — already asserted by M1 (want-migrate=no).

echo
if [ "$fails" -gt 0 ]; then
  echo "FAIL: $fails media-store assertion(s) (ADR-0015 D6)"
  exit 1
fi
echo "PASS: media live in the replicated store in every topology render (ADR-0015 D6)"
