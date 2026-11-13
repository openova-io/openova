#!/usr/bin/env python3
"""Spike CLD-050 E7: the CLD-G6 check on the adoption Workspaces.

g6.py WORKSPACES_JSON BASELINE_JSON EXPECTED_REGION

The adoption Workspaces are the cluster-scoped opentofu.upbound.io
Workspaces named adopt-*. Passes (exit 0) when: the count equals the
baseline count, every one is Synced=True and Ready=True, each keeps the
UID and the terraform_data id it had at the baseline (no re-creation),
and each output hw_api_region equals EXPECTED_REGION (the last region
patched into the claims, so the whole claim -> composite -> Workspace ->
apply chain ran in this step). The first call writes the baseline.
"""
import json
import os
import sys


def cond(item, kind):
    for c in item.get("status", {}).get("conditions", []) or []:
        if c.get("type") == kind:
            return c.get("status")
    return None


def main():
    items = json.load(open(sys.argv[1])).get("items", [])
    base_path, region = sys.argv[2], sys.argv[3]
    rows = {}
    for it in items:
        name = it["metadata"]["name"]
        if not name.startswith("adopt-"):
            continue
        out = it.get("status", {}).get("atProvider", {}).get("outputs", {}) or {}
        rows[name] = {
            "uid": it["metadata"]["uid"],
            "synced": cond(it, "Synced"),
            "ready": cond(it, "Ready"),
            "anchor_id": out.get("anchor_id"),
            "region": out.get("hw_api_region"),
        }
    if not os.path.exists(base_path):
        ok = len(rows) > 0 and all(r["synced"] == "True" and r["ready"] == "True" and r["anchor_id"]
                                   and r["region"] == region for r in rows.values())
        if ok:
            json.dump(rows, open(base_path, "w"), indent=1)
        for n, r in sorted(rows.items()):
            print(f"  {n} synced={r['synced']} ready={r['ready']} region={r['region']} anchor={r['anchor_id']} uid={r['uid']}")
        print(f"count={len(rows)} baseline-written={ok}")
        return 0 if ok else 1
    base = json.load(open(base_path))
    problems = []
    if len(rows) != len(base):
        problems.append(f"count {len(rows)} != baseline {len(base)}")
    for n, b in base.items():
        r = rows.get(n)
        if r is None:
            problems.append(f"{n} missing")
            continue
        if r["synced"] != "True" or r["ready"] != "True":
            problems.append(f"{n} synced={r['synced']} ready={r['ready']}")
        if r["uid"] != b["uid"]:
            problems.append(f"{n} uid changed (object re-created)")
        if r["anchor_id"] != b["anchor_id"]:
            problems.append(f"{n} terraform_data id changed {b['anchor_id']} -> {r['anchor_id']}")
        if r["region"] != region:
            problems.append(f"{n} region output {r['region']} != {region}")
    for n, r in sorted(rows.items()):
        print(f"  {n} synced={r['synced']} ready={r['ready']} region={r['region']} anchor={r['anchor_id']}")
    print(f"count={len(rows)} problems={'; '.join(problems) if problems else 'none'}")
    return 0 if not problems else 1


if __name__ == "__main__":
    sys.exit(main())
