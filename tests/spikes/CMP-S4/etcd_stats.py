#!/usr/bin/env python3
"""Spike CMP-S4: objects and stored bytes per resource, read from etcd itself.

Read-only. Pages through the whole keyspace with `etcdctl get --from-key
--limit` (so a large report population never has to fit in one response),
groups keys by resource, and adds `etcdctl endpoint status` (database size).

Key layout: /registry/<resource>/... for built-in kinds (services/specs and
services/endpoints are kept apart), /registry/<group>/<resource>/... for
custom resources.
"""
import argparse
import base64
import collections
import json
import subprocess
import sys

from s4lib import REPORT_GROUPS

ETCD = ["sudo", "etcdctl", "--endpoints=https://127.0.0.1:2379",
        "--cacert=/var/lib/rancher/k3s/server/tls/etcd/server-ca.crt",
        "--cert=/var/lib/rancher/k3s/server/tls/etcd/client.crt",
        "--key=/var/lib/rancher/k3s/server/tls/etcd/client.key",
        "--command-timeout=60s"]


def etcd(args):
    r = subprocess.run(ETCD + args, capture_output=True, check=True)
    return json.loads(r.stdout)


def resource_of(key):
    p = key.split("/")
    if len(p) < 3 or p[1] != "registry":
        return "(non-registry) /" + (p[1] if len(p) > 1 else "")
    a = p[2]
    if "." in a and len(p) > 3:
        return f"{a}/{p[3]}"
    if a == "services" and len(p) > 3:
        return f"services/{p[3]}"
    return a


def scan(limit):
    start, first = "/", True
    while True:
        doc = etcd(["get", "--from-key", start, "--limit", str(limit), "-w", "json"])
        kvs = doc.get("kvs") or []
        if not first and kvs and base64.b64decode(kvs[0]["key"]).decode() == start:
            kvs = kvs[1:]
        first = False
        for kv in kvs:
            yield base64.b64decode(kv["key"]).decode(), len(base64.b64decode(kv.get("value", "")))
        if not doc.get("more") or not kvs:
            return
        start = base64.b64decode(kvs[-1]["key"]).decode()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--phase", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--limit", type=int, default=200)
    a = ap.parse_args()

    per = collections.defaultdict(lambda: {"objects": 0, "valueBytes": 0, "maxValueBytes": 0, "sizes": []})
    for key, n in scan(a.limit):
        p = per[resource_of(key)]
        p["objects"] += 1
        p["valueBytes"] += n
        p["maxValueBytes"] = max(p["maxValueBytes"], n)
        p["sizes"].append(n)

    st = etcd(["endpoint", "status", "-w", "json"])
    s0 = st[0]["Status"] if isinstance(st, list) and st else {}
    db = {"dbSize": s0.get("dbSize"), "dbSizeInUse": s0.get("dbSizeInUse"),
          "revision": (s0.get("header") or {}).get("revision"), "version": s0.get("version")}

    def pct(xs, q):
        if not xs:
            return 0
        xs = sorted(xs)
        return xs[min(len(xs) - 1, int(q * (len(xs) - 1) + 0.5))]

    res = {}
    for r, p in per.items():
        xs = p.pop("sizes")
        p["avgValueBytes"] = p["valueBytes"] // p["objects"] if p["objects"] else 0
        p["p50ValueBytes"] = pct(xs, 0.5)
        p["p95ValueBytes"] = pct(xs, 0.95)
        res[r] = p

    rep = {r: p for r, p in res.items() if r.split("/")[0] in REPORT_GROUPS}
    rep_o = sum(p["objects"] for p in rep.values())
    rep_b = sum(p["valueBytes"] for p in rep.values())
    all_o = sum(p["objects"] for p in res.values())
    all_b = sum(p["valueBytes"] for p in res.values())
    summary = {"phase": a.phase, "etcd": db, "reportObjects": rep_o, "reportValueBytes": rep_b,
               "allObjects": all_o, "allValueBytes": all_b, "perResource": res}
    with open(a.out, "w") as f:
        json.dump(summary, f, sort_keys=True, indent=1)

    w = sys.stdout.write
    w(f"== etcd {a.phase}: database {db['dbSize']} bytes, in use {db['dbSizeInUse']} bytes, "
      f"revision {db['revision']}, server {db['version']}\n")
    w(f"   all keys: {all_o} objects, {all_b} value bytes\n")
    w(f"   REPORT OBJECTS: {rep_o} objects, {rep_b} value bytes ({rep_b / 1e6:.2f} MB)\n")
    hdr = f"   {'resource':58} {'objects':>8} {'bytes':>12} {'avg':>8} {'p50':>8} {'p95':>8} {'max':>8}\n"
    w("   -- report kinds\n" + hdr)
    for r in sorted(rep, key=lambda k: -rep[k]["valueBytes"]):
        p = rep[r]
        w(f"   {r:58} {p['objects']:>8} {p['valueBytes']:>12} {p['avgValueBytes']:>8} "
          f"{p['p50ValueBytes']:>8} {p['p95ValueBytes']:>8} {p['maxValueBytes']:>8}\n")
    w("   -- every other resource (inventory)\n" + hdr)
    for r in sorted(res, key=lambda k: -res[k]["objects"]):
        if r in rep:
            continue
        p = res[r]
        w(f"   {r:58} {p['objects']:>8} {p['valueBytes']:>12} {p['avgValueBytes']:>8} "
          f"{p['p50ValueBytes']:>8} {p['p95ValueBytes']:>8} {p['maxValueBytes']:>8}\n")
    w("JSON-SUMMARY " + json.dumps({"phase": a.phase, "etcd": db, "reportObjects": rep_o,
                                    "reportValueBytes": rep_b, "allObjects": all_o,
                                    "allValueBytes": all_b}) + "\n")


if __name__ == "__main__":
    main()
