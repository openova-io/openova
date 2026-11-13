#!/usr/bin/env python3
"""Export every plan the conformance suite holds, whether or not the plan
runner finished it. Runs inside the suite's server container (stdin)."""
import json
import os
import ssl
import urllib.request

BASE = "https://conformance-suite.keycloak-fapi.org/api/"
OUT = "/conformance-suite/report/prd040/all"
CTX = ssl._create_unverified_context()


def get(path):
    with urllib.request.urlopen(BASE + path, context=CTX, timeout=120) as r:
        return r.read()


os.makedirs(OUT, exist_ok=True)
listing = json.loads(get("plan?length=200"))
plans = listing.get("data", listing) if isinstance(listing, dict) else listing
for p in plans:
    pid = p.get("_id") or p.get("id")
    desc = (p.get("config") or {}).get("description", "")
    try:
        data = get("plan/export/" + pid)
        with open(os.path.join(OUT, pid + ".zip"), "wb") as f:
            f.write(data)
        print("exported", pid, p.get("planName"), json.dumps(p.get("variant")), desc, len(data), "bytes")
    except Exception as e:  # noqa: BLE001 - report and continue with the next plan
        print("export failed", pid, desc, e)
