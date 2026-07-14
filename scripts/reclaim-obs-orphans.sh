#!/usr/bin/env bash
# reclaim-obs-orphans.sh — version-aware reclaim of orphaned catalyst-hw* OBS
# buckets on kom4dc (private HCS). Refs #5078.
#
# WHY: the wipe's tofu-destroy deliberately does NOT force_destroy the Velero
# archive bucket (it holds the backup archive — see the design note at
# infra/providers/hetzner/main.tf), so every wiped env leaves an orphan
# bucket; kom4dc caps 100 buckets/project → fresh fires die at Phase-0 with
# `Code=TooManyBuckets` (hw252, dep 281715eb, at 100/100 with 80 orphans).
# `aws s3 rb --force` is NOT enough: it does not remove object VERSIONS +
# DELETE-MARKERS (live finding on #5078: 11 of 80 orphans survived it). This
# tool pages GET /?versions, deletes every Version AND DeleteMarker with
# `DELETE /<key>?versionId=<id>`, sweeps any remaining plain objects, then
# deletes the bucket itself.
#
# INTERIM tool: the DURABLE fix is a wipe-handler OBS-purge step in the
# mothership wipe path (openova-private — out of scope for this repo).
# Refs #5078.
#
# Usage:
#   PROTECT="hw240,hw250" HW_TFVARS=/deps/tofu/<dep-id>/tofu.auto.tfvars.json \
#     bash scripts/reclaim-obs-orphans.sh [--dry-run]
#
#   --dry-run   list the buckets that WOULD be deleted; delete NOTHING.
#   PROTECT     comma/space-separated env stems to protect (matched as a
#               hyphen-delimited segment of the bucket name, so hw24 never
#               protects hw240 and vice versa). hw240 is ALWAYS protected
#               (founder protect-list, docs/PROTOCOL.md §5.0) even when
#               PROTECT is unset or omits it — PROTECT can only ADD.
#   HW_TFVARS   tofu.auto.tfvars.json carrying Huawei AK/SK (+ region).
#
# SCOPE GUARD: only buckets matching ^catalyst-hw are EVER candidates; every
# other bucket (velero mothership archives, foreign buckets, …) is ignored
# outright and never touched.
#
# Auth: OBS is S3-plane — OBS SigV2 (`Authorization: OBS <ak>:<base64(
# hmac-sha1(sk, StringToSign))>`), NOT the SDK-HMAC-SHA256 used by EVS/VPC/ECS.
# Same idiom as prov-preflight.sh check 3e. Endpoint is private HCS
# (obs.<region>.kom4dc.nationalcloud.om, private CA → TLS verify disabled) —
# reachable from the mothership, not from dev sandboxes.
set -euo pipefail

DRY_RUN=0
for a in "$@"; do case "$a" in
  --dry-run) DRY_RUN=1 ;;
  *) echo "unknown arg: $a" >&2
     echo "usage: PROTECT=\"hw240,...\" HW_TFVARS=/path/tofu.auto.tfvars.json bash $0 [--dry-run]" >&2
     exit 2 ;;
esac; done

if [ -z "${HW_TFVARS:-}" ] || [ ! -s "${HW_TFVARS}" ]; then
  echo "HW_TFVARS unset or empty — need tofu.auto.tfvars.json with Huawei AK/SK" >&2
  exit 2
fi

# hw240 is HARD-CODED into the protect union (founder protect-list) — the env
# var can only add stems, never remove hw240.
PROTECT_UNION="hw240 ${PROTECT:-}"

DRY_RUN="$DRY_RUN" PROTECT_UNION="$PROTECT_UNION" python3 - "$HW_TFVARS" <<'PY'
import sys, os, re, json, ssl, hmac, base64, hashlib, socket
import urllib.request, urllib.error, urllib.parse
import xml.etree.ElementTree as ET
from email.utils import formatdate

t = json.load(open(sys.argv[1]))
AK = t.get('huawei_access_key') or t.get('access_key') or t.get('huawei_ak')
SK = t.get('huawei_secret_key') or t.get('secret_key') or t.get('huawei_sk')
REGION = t.get('huawei_region') or 'me-east-215'
if not AK or not SK:
    print("no Huawei AK/SK in tfvars — refusing to continue", file=sys.stderr); sys.exit(2)
ENDPOINT = f"obs.{REGION}.kom4dc.nationalcloud.om"
DRY = os.environ.get("DRY_RUN") == "1"
PROT = [p for p in re.split(r'[,\s]+', os.environ.get("PROTECT_UNION", "")) if p]
STUB = os.environ.get("OBS_STUB_DIR")  # test hook (#5078): canned XML dir, no network, deletes → deletes.log

CTX = ssl.create_default_context(); CTX.check_hostname = False; CTX.verify_mode = ssl.CERT_NONE  # private HCS CA
STYLE = ["virtual"]  # flips to path-style if <bucket>.obs... wildcard DNS is absent

def sign(method, canon_resource, date):
    sts = f"{method}\n\n\n{date}\n{canon_resource}"
    return base64.b64encode(hmac.new(SK.encode(), sts.encode(), hashlib.sha1).digest()).decode()

def stub_request(method, bucket, key, subresource, query):
    if method == "DELETE":
        with open(os.path.join(STUB, "deletes.log"), "a") as f:
            f.write(f"DELETE bucket={bucket} key={key} sub={subresource}\n")
        return b""
    if not bucket:
        return open(os.path.join(STUB, "list_buckets.xml"), "rb").read()
    marker = ""
    mk = "key-marker=" if subresource == "versions" else "marker="
    for part in (query or "").split("&"):
        if part.startswith(mk):
            marker = urllib.parse.unquote(part.split("=", 1)[1])
    kind = "versions" if subresource == "versions" else "objects"
    f = os.path.join(STUB, f"{kind}_{bucket}_{marker or 'START'}.xml")
    if os.path.exists(f):
        return open(f, "rb").read()
    return b"<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>"

def obs_request(method, bucket="", key="", subresource="", query=""):
    """OBS SigV2 request. `subresource` (e.g. 'versions', 'versionId=xx') IS
    part of the canonical resource; `query` (markers, max-keys) is NOT."""
    if STUB:
        return stub_request(method, bucket, key, subresource, query)
    path_key = urllib.parse.quote(key, safe='/') if key else ""
    sub = f"?{subresource}" if subresource else ""
    qs = sub + (("&" if sub else "?") + query if query else "")
    date = formatdate(usegmt=True)
    for attempt in ("first", "retry"):
        if STYLE[0] == "virtual" and bucket:
            host = f"{bucket}.{ENDPOINT}"
            path = f"/{path_key}"
            canon = f"/{bucket}/{path_key}{sub}"
        else:
            host = ENDPOINT
            path = f"/{bucket}/{path_key}" if bucket else "/"
            canon = f"{path}{sub}"
        req = urllib.request.Request(
            f"https://{host}{path}{qs}", method=method,
            headers={"Date": date, "Authorization": f"OBS {AK}:{sign(method, canon, date)}", "Host": host})
        try:
            with urllib.request.urlopen(req, timeout=30, context=CTX) as r:
                return r.read()
        except urllib.error.HTTPError as e:
            if e.code == 404 and method == "DELETE":
                return b""  # already gone — fine
            raise RuntimeError(f"HTTP {e.code} {method} https://{host}{path}{qs}: {e.read()[:200]!r}") from None
        except (urllib.error.URLError, OSError) as e:
            reason = getattr(e, "reason", e)
            if STYLE[0] == "virtual" and bucket and attempt == "first" and isinstance(reason, socket.gaierror):
                STYLE[0] = "path"  # no wildcard DNS for <bucket>.obs.… → path-style
                continue
            raise RuntimeError(f"unreachable {method} https://{host}{path}{qs}: {reason}") from None
    raise RuntimeError("unreachable")  # not hit — loop returns or raises

def lname(tag): return tag.rsplit('}', 1)[-1]
def find_text(el, name):
    for c in el:
        if lname(c.tag) == name:
            return c.text or ""
    return ""
def iter_named(root, name):
    for el in root.iter():
        if lname(el.tag) == name:
            yield el

def list_buckets():
    root = ET.fromstring(obs_request("GET"))
    return [find_text(b, "Name") for b in iter_named(root, "Bucket")]

def protected(name):
    # segment match: hw24 must not protect hw240, hw240 must not protect hw2400
    return any(re.search(rf'(^|-){re.escape(tok)}(-|$)', name) for tok in PROT)

def purge_versions(bucket):
    nv = nm = 0
    key_marker = vid_marker = ""
    for _ in range(10000):  # hard page cap
        q = "max-keys=1000"
        if key_marker: q += "&key-marker=" + urllib.parse.quote(key_marker)
        if vid_marker: q += "&version-id-marker=" + urllib.parse.quote(vid_marker)
        root = ET.fromstring(obs_request("GET", bucket, subresource="versions", query=q))
        entries = []
        for el in iter_named(root, "Version"):
            entries.append((find_text(el, "Key"), find_text(el, "VersionId"), "version"))
        for el in iter_named(root, "DeleteMarker"):
            entries.append((find_text(el, "Key"), find_text(el, "VersionId"), "marker"))
        for key, vid, kind in entries:
            if vid:
                obs_request("DELETE", bucket, key, subresource="versionId=" + urllib.parse.quote(vid))
            else:
                obs_request("DELETE", bucket, key)
            if kind == "version": nv += 1
            else: nm += 1
        if find_text(root, "IsTruncated").lower() != "true":
            break
        nk = find_text(root, "NextKeyMarker"); nvm = find_text(root, "NextVersionIdMarker")
        if not nk and entries:
            nk, nvm = entries[-1][0], entries[-1][1]
        if not nk:
            break
        key_marker, vid_marker = nk, nvm
    return nv, nm

def purge_plain(bucket):
    n = 0; marker = ""
    for _ in range(10000):
        q = "max-keys=1000" + (("&marker=" + urllib.parse.quote(marker)) if marker else "")
        root = ET.fromstring(obs_request("GET", bucket, query=q))
        keys = [find_text(el, "Key") for el in iter_named(root, "Contents")]
        for k in keys:
            obs_request("DELETE", bucket, k); n += 1
        if find_text(root, "IsTruncated").lower() != "true":
            break
        marker = find_text(root, "NextMarker") or (keys[-1] if keys else "")
        if not marker:
            break
    return n

buckets = list_buckets()
candidates, protected_skip, ignored = [], [], 0
for b in buckets:
    if not b.startswith("catalyst-hw"):
        ignored += 1; continue
    if protected(b):
        protected_skip.append(b); continue
    candidates.append(b)

print(f"OBS endpoint: {ENDPOINT} (total buckets: {len(buckets)}; non-catalyst-hw ignored: {ignored})")
print(f"protect-list (hw240 always included): {', '.join(PROT)}")
for b in protected_skip:
    print(f"  PROTECTED (skip): {b}")
if not candidates:
    print("no orphan candidates — nothing to do.")
    sys.exit(0)

ok = fail = 0
for b in candidates:
    if DRY:
        print(f"  WOULD-DELETE: {b}")
        continue
    try:
        nv, nm = purge_versions(b)
        np = purge_plain(b)
        obs_request("DELETE", b)
        ok += 1
        print(f"  ✅ OK {b} (versions:{nv} delete-markers:{nm} plain-objects:{np})")
    except Exception as e:
        fail += 1
        print(f"  ❌ FAIL {b}: {e}")

if DRY:
    print(f"DRY-RUN TALLY: would-delete={len(candidates)} protected={len(protected_skip)} ignored={ignored} — nothing deleted.")
else:
    print(f"TALLY: deleted={ok} failed={fail} protected={len(protected_skip)} ignored={ignored}")
    sys.exit(1 if fail else 0)
PY
