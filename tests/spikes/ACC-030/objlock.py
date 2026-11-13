#!/usr/bin/env python3
"""ACC-030 item (g): S3 object lock on the SeaweedFS bucket created by the
chart's bucket hook, and a prefix listing of more than 1,000 keys.

Every printed line that starts with "G " is one measured result. Subcommands:
  main    lock, delete, overwrite, retention, read-only identity, governance, listing
  broker  the same checks with a bucket-scoped Read,Write,List identity
  check   one object version after a filer-level delete (KEY, VID)
  bucket  the bucket after a filer-level bucket delete
"""
import base64
import concurrent.futures as cf
import datetime as dt
import hashlib
import json
import os
import sys
import time

import boto3
import botocore
from botocore.config import Config

EP = os.environ["S3_EP"]
BUCKET = "access-recordings"
UID = os.environ["SUID"]
PREFIX = f"org/acme/2026/11/13/{UID}"
CFG = Config(signature_version="s3v4", s3={"addressing_style": "path"},
             retries={"max_attempts": 1, "mode": "standard"})


def cli(ak, sk):
    return boto3.client("s3", endpoint_url=EP, aws_access_key_id=ak, aws_secret_access_key=sk,
                        region_name="us-east-1", config=CFG)


def call(fn, **kw):
    try:
        r = fn(**kw)
        return "OK", r.get("ResponseMetadata", {}).get("HTTPStatusCode"), r
    except botocore.exceptions.ClientError as e:
        err = e.response.get("Error", {})
        return "ERR", e.response.get("ResponseMetadata", {}).get("HTTPStatusCode"), {
            "Code": err.get("Code"), "Message": (err.get("Message") or "")[:160]}
    except Exception as e:  # noqa: BLE001 - a measured result, not a crash
        return "EXC", None, {"Exception": repr(e)[:200]}


def show(label, res, keys=()):
    st, code, body = res
    extra = {k: body.get(k) for k in keys if k in body} if st == "OK" else body
    print(f"G {label}: {st} http={code} {json.dumps(extra, default=str, sort_keys=True)}", flush=True)
    return res


def md5b64(b):
    return base64.b64encode(hashlib.md5(b).digest()).decode()


def until(seconds):
    return (dt.datetime.now(dt.timezone.utc) + dt.timedelta(seconds=seconds)).replace(microsecond=0)


def body_of(c, key, vid=None):
    kw = {"Bucket": BUCKET, "Key": key}
    if vid:
        kw["VersionId"] = vid
    st, code, r = call(c.get_object, **kw)
    if st != "OK":
        return f"{st} http={code} {r}"
    return hashlib.sha256(r["Body"].read()).hexdigest()[:12]


def locked_put(c, key, body, mode="COMPLIANCE", seconds=86400, meta=None):
    kw = {"Bucket": BUCKET, "Key": key, "Body": body, "ContentMD5": md5b64(body),
          "ObjectLockMode": mode, "ObjectLockRetainUntilDate": until(seconds)}
    if meta:
        kw["Metadata"] = meta
    return call(c.put_object, **kw)


def versions(c, key):
    st, code, r = call(c.list_object_versions, Bucket=BUCKET, Prefix=key)
    if st != "OK":
        return f"{st} http={code} {r}"
    v = [(x["VersionId"][:12], x.get("IsLatest")) for x in r.get("Versions", [])]
    m = [(x["VersionId"][:12], x.get("IsLatest")) for x in r.get("DeleteMarkers", [])]
    return f"versions={v} deleteMarkers={m}"


def main():
    a = cli(os.environ["ADMIN_AK"], os.environ["ADMIN_SK"])
    ro = cli(os.environ["READ_AK"], os.environ["READ_SK"])
    show("g0 head-bucket", call(a.head_bucket, Bucket=BUCKET))
    show("g0 get-bucket-versioning", call(a.get_bucket_versioning, Bucket=BUCKET), ("Status",))
    show("g0 get-object-lock-configuration", call(a.get_object_lock_configuration, Bucket=BUCKET),
         ("ObjectLockConfiguration",))

    key = f"{PREFIX}/screen-1/00000001.chunk"
    original = b"chunk 1: original bytes\n"
    ru = until(86400)
    p1 = show("g1 PUT with x-amz-object-lock-mode COMPLIANCE and retain-until now+1d (admin)",
              locked_put(a, key, original, meta={"chain": "a" * 64, "prev": "b" * 64}), ("VersionId", "ETag"))
    v1 = p1[2].get("VersionId") if p1[0] == "OK" else None
    print(f"G-V1 {v1}", flush=True)
    print(f"G g1 retain-until sent: {ru.isoformat()} version={v1}", flush=True)
    if not v1:
        print("G g1 no version id returned; the remaining lock checks are not meaningful", flush=True)
    else:
        show("g2 HEAD v1", call(a.head_object, Bucket=BUCKET, Key=key, VersionId=v1),
             ("ObjectLockMode", "ObjectLockRetainUntilDate", "Metadata", "VersionId"))
        show("g2 GET object retention v1", call(a.get_object_retention, Bucket=BUCKET, Key=key, VersionId=v1),
             ("Retention",))
        show("g3 DELETE v1 (admin)", call(a.delete_object, Bucket=BUCKET, Key=key, VersionId=v1))
        show("g3 DELETE v1 with x-amz-bypass-governance-retention (admin)",
             call(a.delete_object, Bucket=BUCKET, Key=key, VersionId=v1, BypassGovernanceRetention=True))
        show("g3 DELETE v1 (read-only identity)", call(ro.delete_object, Bucket=BUCKET, Key=key, VersionId=v1))
        show("g3 DeleteObjects batch with v1 (admin)",
             call(a.delete_objects, Bucket=BUCKET, Delete={"Objects": [{"Key": key, "VersionId": v1}]}),
             ("Deleted", "Errors"))
        print(f"G g3 v1 after the delete attempts: {body_of(a, key, v1)} (original {hashlib.sha256(original).hexdigest()[:12]})",
              flush=True)
        show("g4 PUT retention v1 with an earlier retain-until (shorten)",
             call(a.put_object_retention, Bucket=BUCKET, Key=key, VersionId=v1,
                  Retention={"Mode": "COMPLIANCE", "RetainUntilDate": until(120)}))
        show("g4 PUT retention v1 to GOVERNANCE (weaken)",
             call(a.put_object_retention, Bucket=BUCKET, Key=key, VersionId=v1,
                  Retention={"Mode": "GOVERNANCE", "RetainUntilDate": ru}))
        show("g4 GET object retention v1 after the attempts",
             call(a.get_object_retention, Bucket=BUCKET, Key=key, VersionId=v1), ("Retention",))
        over = b"chunk 1: overwritten bytes\n"
        show("g5 overwrite: PUT the same key without lock headers (admin)",
             call(a.put_object, Bucket=BUCKET, Key=key, Body=over, ContentMD5=md5b64(over)), ("VersionId",))
        print(f"G g5 latest={body_of(a, key)} (overwrite {hashlib.sha256(over).hexdigest()[:12]}) "
              f"v1={body_of(a, key, v1)} (original {hashlib.sha256(original).hexdigest()[:12]})", flush=True)
        show("g5 PUT the same key by the read-only identity",
             call(ro.put_object, Bucket=BUCKET, Key=key, Body=b"x", ContentMD5=md5b64(b"x")))
        show("g6 DELETE without a version id (admin)", call(a.delete_object, Bucket=BUCKET, Key=key),
             ("DeleteMarker", "VersionId"))
        print(f"G g6 after the delete marker: latest GET={body_of(a, key)} v1={body_of(a, key, v1)} {versions(a, key)}",
              flush=True)

    # Governance mode and an object with no lock headers, for contrast.
    gkey = f"{PREFIX}/screen-1/governance.chunk"
    gp = show("g7 PUT GOVERNANCE retain-until now+1d", locked_put(a, gkey, b"governance", mode="GOVERNANCE"), ("VersionId",))
    gv = gp[2].get("VersionId") if gp[0] == "OK" else None
    if gv:
        show("g7 DELETE governance version without bypass (admin)",
             call(a.delete_object, Bucket=BUCKET, Key=gkey, VersionId=gv))
        show("g7 DELETE governance version with bypass (admin)",
             call(a.delete_object, Bucket=BUCKET, Key=gkey, VersionId=gv, BypassGovernanceRetention=True))
    nkey = f"{PREFIX}/screen-1/no-lock-headers.chunk"
    np_ = show("g8 PUT without lock headers in the locked bucket", call(a.put_object, Bucket=BUCKET, Key=nkey, Body=b"n",
               ContentMD5=md5b64(b"n")), ("VersionId",))
    nv = np_[2].get("VersionId") if np_[0] == "OK" else None
    if nv:
        show("g8 HEAD (default retention applied?)", call(a.head_object, Bucket=BUCKET, Key=nkey, VersionId=nv),
             ("ObjectLockMode", "ObjectLockRetainUntilDate"))
        show("g8 DELETE that version (admin)", call(a.delete_object, Bucket=BUCKET, Key=nkey, VersionId=nv))

    # More than 1,000 keys under one prefix, each written as the broker would (locked).
    n = 1201
    lp = f"{PREFIX}/screen-2/"
    t0 = time.time()

    def put_one(i):
        return locked_put(a, f"{lp}{i:08d}.chunk", f"chunk {i}\n".encode())[0]

    with cf.ThreadPoolExecutor(max_workers=16) as ex:
        res = list(ex.map(put_one, range(1, n + 1)))
    print(f"G g9 wrote {res.count('OK')}/{n} locked objects under one prefix in {time.time() - t0:.1f}s "
          f"(failures {len(res) - res.count('OK')})", flush=True)
    st, code, r1 = call(a.list_objects_v2, Bucket=BUCKET, Prefix=lp)
    if st == "OK":
        print(f"G g9 ListObjectsV2 page 1 (no MaxKeys): KeyCount={r1.get('KeyCount')} IsTruncated={r1.get('IsTruncated')} "
              f"NextContinuationToken={'present' if r1.get('NextContinuationToken') else 'absent'} "
              f"first={r1['Contents'][0]['Key'][-14:] if r1.get('Contents') else None} "
              f"last={r1['Contents'][-1]['Key'][-14:] if r1.get('Contents') else None}", flush=True)
        if r1.get("NextContinuationToken"):
            st2, code2, r2 = call(a.list_objects_v2, Bucket=BUCKET, Prefix=lp,
                                  ContinuationToken=r1["NextContinuationToken"])
            if st2 == "OK":
                print(f"G g9 ListObjectsV2 page 2: KeyCount={r2.get('KeyCount')} IsTruncated={r2.get('IsTruncated')} "
                      f"first={r2['Contents'][0]['Key'][-14:] if r2.get('Contents') else None} "
                      f"last={r2['Contents'][-1]['Key'][-14:] if r2.get('Contents') else None}", flush=True)
            else:
                show("g9 ListObjectsV2 page 2", (st2, code2, r2))
    else:
        show("g9 ListObjectsV2 page 1", (st, code, r1))
    keys = []
    try:
        for page in a.get_paginator("list_objects_v2").paginate(Bucket=BUCKET, Prefix=lp):
            keys += [x["Key"] for x in page.get("Contents", [])]
        print(f"G g9 paginated total={len(keys)} distinct={len(set(keys))} sorted={keys == sorted(keys)} "
              f"expected={n}", flush=True)
    except Exception as e:  # noqa: BLE001
        print(f"G g9 paginated listing failed after {len(keys)} keys: {repr(e)[:200]}", flush=True)
    st, code, r = call(a.list_objects_v2, Bucket=BUCKET, Prefix=lp, MaxKeys=1500)
    print(f"G g9 ListObjectsV2 MaxKeys=1500: {st} KeyCount={r.get('KeyCount') if st == 'OK' else r} "
          f"IsTruncated={r.get('IsTruncated') if st == 'OK' else None}", flush=True)
    st, code, r = call(a.list_objects, Bucket=BUCKET, Prefix=lp)
    print(f"G g9 ListObjects (v1) page 1: {st} count={len(r.get('Contents', [])) if st == 'OK' else r} "
          f"IsTruncated={r.get('IsTruncated') if st == 'OK' else None} "
          f"NextMarker={'present' if st == 'OK' and r.get('NextMarker') else 'absent'}", flush=True)
    vt = 0
    try:
        for page in a.get_paginator("list_object_versions").paginate(Bucket=BUCKET, Prefix=lp):
            vt += len(page.get("Versions", []))
        print(f"G g9 ListObjectVersions paginated total={vt}", flush=True)
    except Exception as e:  # noqa: BLE001
        print(f"G g9 ListObjectVersions pagination failed after {vt}: {repr(e)[:200]}", flush=True)


def broker():
    b = cli(os.environ["BROKER_AK"], os.environ["BROKER_SK"])
    key = f"{PREFIX}/screen-3/00000001.chunk"
    p = show("g10 broker identity PUT locked COMPLIANCE", locked_put(b, key, b"broker chunk"), ("VersionId",))
    v = p[2].get("VersionId") if p[0] == "OK" else None
    if v:
        show("g10 broker DELETE that version", call(b.delete_object, Bucket=BUCKET, Key=key, VersionId=v))
        show("g10 broker DELETE without a version id (delete marker)", call(b.delete_object, Bucket=BUCKET, Key=key),
             ("DeleteMarker", "VersionId"))
        print(f"G g10 broker: version after the attempts={body_of(b, key, v)}", flush=True)
    show("g10 broker PUT into another bucket name", call(b.put_object, Bucket="other-bucket", Key="k", Body=b"x",
         ContentMD5=md5b64(b"x")))


def check():
    a = cli(os.environ["ADMIN_AK"], os.environ["ADMIN_SK"])
    key, vid = os.environ["KEY"], os.environ["VID"]
    print(f"G g11 after the filer-level delete: GET v1={body_of(a, key, vid)} {versions(a, key)}", flush=True)
    st, code, r = call(a.list_objects_v2, Bucket=BUCKET, Prefix=f"{PREFIX}/screen-1/")
    print(f"G g11 listing of screen-1 after the filer-level delete: {st} KeyCount={r.get('KeyCount') if st == 'OK' else r}",
          flush=True)


def bucket():
    a = cli(os.environ["ADMIN_AK"], os.environ["ADMIN_SK"])
    show("g12 HEAD bucket after the filer-level bucket delete", call(a.head_bucket, Bucket=BUCKET))
    st, code, r = call(a.list_objects_v2, Bucket=BUCKET, Prefix=f"{PREFIX}/screen-2/")
    print(f"G g12 listing of the 1,201 locked objects: {st} http={code} "
          f"KeyCount={r.get('KeyCount') if st == 'OK' else r}", flush=True)


if __name__ == "__main__":
    {"main": main, "broker": broker, "check": check, "bucket": bucket}[sys.argv[1]]()
