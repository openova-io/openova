#!/usr/bin/env python3
"""Spike CMP-S3: count the scanner's report objects as etcd stores them.

Input is the output of
  etcdctl get /registry/aquasecurity.github.io/ --prefix -w json
and of
  etcdctl endpoint status -w json
(both read-only). Prints objects and value bytes per resource, and the etcd
database size, and writes them as one JSON line to stdout's last line.
"""
import argparse
import base64
import collections
import json


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--phase", required=True)
    ap.add_argument("--kv", required=True)
    ap.add_argument("--status", required=True)
    a = ap.parse_args()

    with open(a.kv) as f:
        doc = json.load(f)
    per = collections.defaultdict(lambda: {"objects": 0, "valueBytes": 0, "keyBytes": 0, "maxValueBytes": 0})
    for kv in doc.get("kvs", []) or []:
        key = base64.b64decode(kv["key"]).decode()
        val = base64.b64decode(kv.get("value", ""))
        parts = key.split("/")
        res = parts[3] if len(parts) > 3 else key
        p = per[res]
        p["objects"] += 1
        p["valueBytes"] += len(val)
        p["keyBytes"] += len(key)
        p["maxValueBytes"] = max(p["maxValueBytes"], len(val))

    with open(a.status) as f:
        st = json.load(f)
    st0 = st[0]["Status"] if isinstance(st, list) and st else {}
    db = {"dbSize": st0.get("dbSize"), "dbSizeInUse": st0.get("dbSizeInUse"),
          "revision": (st0.get("header") or {}).get("revision")}

    total_o = sum(p["objects"] for p in per.values())
    total_b = sum(p["valueBytes"] for p in per.values())
    print(f"etcd phase {a.phase}: database size {db['dbSize']} bytes, in use {db['dbSizeInUse']} bytes, revision {db['revision']}")
    print(f"{'resource':38} {'objects':>8} {'value bytes':>12} {'avg':>8} {'max':>8}")
    for res in sorted(per):
        p = per[res]
        avg = p["valueBytes"] // p["objects"] if p["objects"] else 0
        print(f"{res:38} {p['objects']:>8} {p['valueBytes']:>12} {avg:>8} {p['maxValueBytes']:>8}")
    print(f"{'TOTAL':38} {total_o:>8} {total_b:>12}")
    print("JSON " + json.dumps({"phase": a.phase, "etcd": db, "perResource": per,
                                "totalObjects": total_o, "totalValueBytes": total_b}, sort_keys=True))


if __name__ == "__main__":
    main()
