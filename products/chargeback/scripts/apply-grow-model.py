#!/usr/bin/env python3
"""Apply the capped-or-grow model to an existing BSS.

DESIGN.md §22.11. On the plans book ("OpenOva plans" by default), through
the BSS API as the sovereign-admin named by --email, writing only what
differs (a second run writes nothing):

  1. every package (S, M, L, XL) allows grow — S, M and L up to the XL
     shape (8 vCPU / 16 GB / 250 GB / 1000 Mbps), XL up to twice it
     (16 / 32 / 500 / 2000) — with its OWN compute overage
     rates — the package's unit price (1 vCPU + 2 GB) plus 10 %:
         S  1.992 per vCPU, 0.374 per GB a month
         M  1.796 / 0.337    L  1.598 / 0.300    XL 1.399 / 0.263
  2. the disk and bandwidth meters priced in the book where they are not
     yet (k8s.pvc_gb 0.423 per GB a year, eip.bandwidth_mbps 15.038 per Mbps
     a year) — merge: a rate the operator set is never overwritten;
  3. the DR topology on S, M and L becomes optional and GROW ONLY at
     "single region" (active-passive comes with grow mode, billed as usage;
     XL keeps it included);
  4. every included quantity cell (bandwidth, disk) becomes metered — the
     customer's mode now decides what happens above the allowance.

k8s.vcpu and k8s.mem_gb are deliberately NOT priced in the plans book: on a
plans book those request meters are the allocation basis, and a price there
would bill them on every package. Grow compute is rated from the package's
own rates on the limit meters instead.

    python3 apply-grow-model.py --base http://127.0.0.1:18080 \\
        --email admin@example.org [--header X-Forwarded-Email] \\
        [--book "OpenOva plans"] [--dry-run]

Standard library only. A package's settings are written WHOLE, so every
other setting (tagline, recommended, months free, shape, icon, accent,
badge) is read back from the book's packages document and re-sent as it is.
"""

import argparse
import json
import sys
import urllib.error
import urllib.parse
import urllib.request

# The founder's pricing sheet, 2026-10-10.
RATES = {"plan.s": ("1.992", "0.374"), "plan.m": ("1.796", "0.337"),
         "plan.l": ("1.598", "0.300"), "plan.xl": ("1.399", "0.263")}
# S, M, L grow to the XL shape; XL to twice it (founder decision 2026-10-10).
XL_SHAPE = {"vcpu": "8", "memory_gb": "16", "disk_gb": "250", "bandwidth_mbps": "1000"}
CEILINGS = {"plan.s": XL_SHAPE, "plan.m": XL_SHAPE, "plan.l": XL_SHAPE,
            "plan.xl": {"vcpu": "16", "memory_gb": "32", "disk_gb": "500", "bandwidth_mbps": "2000"}}
METERS = [("k8s.pvc_gb", "gb-hour", "0.423", "Disk per GB above what the package includes (pricing sheet 2026-10-10)"),
          ("eip.bandwidth_mbps", "mbps-hour", "15.038", "EIP bandwidth per Mbps above what the package includes (pricing sheet 2026-10-10)")]
SHAPE_KEYS = ("vcpu", "memory_gb", "vcpu_guaranteed", "memory_gb_guaranteed", "disk_gb")
DR_KEY = "dr_topology"


class API:
    def __init__(self, base, header, email, dry_run):
        self.base = base.rstrip("/")
        self.header = header
        self.email = email
        self.dry_run = dry_run
        self.writes = 0

    def call(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        if method != "GET":
            self.writes += 1
            if self.dry_run:
                print(f"  (dry run) {method} {path} {json.dumps(body) if body is not None else ''}")
                return {}
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header(self.header, self.email)
        req.add_header("Accept", "application/json")
        if data is not None:
            req.add_header("Content-Type", "application/json")
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as e:
            sys.exit(f"{method} {path} -> {e.code}: {e.read().decode(errors='replace')}")
        return json.loads(raw) if raw else {}


def num_eq(a, b):
    try:
        return a is not None and b is not None and abs(float(a) - float(b)) < 1e-9
    except (TypeError, ValueError):
        return False


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--base", required=True, help="BSS base URL, e.g. http://127.0.0.1:18080")
    ap.add_argument("--email", required=True, help="the sovereign-admin the trusted header names")
    ap.add_argument("--header", default="X-Forwarded-Email", help="the trusted forward-auth header (default X-Forwarded-Email)")
    ap.add_argument("--book", default="OpenOva plans", help='the plans book (default "OpenOva plans")')
    ap.add_argument("--dry-run", action="store_true", help="read everything, write nothing, print what would be written")
    args = ap.parse_args()
    api = API(args.base, args.header, args.email, args.dry_run)

    books = api.call("GET", "/api/v1/pricebooks").get("pricebooks", [])
    book = next((b for b in books if b.get("name", "").lower() == args.book.lower()), None)
    if book is None:
        sys.exit(f'no price book named "{args.book}"')
    bid = urllib.parse.quote(book["id"])

    # 2 first: the meters the grow rates of disk and bandwidth are read from.
    full = api.call("GET", "/api/v1/pricebooks/" + bid)
    priced = {it["sku"] for it in full.get("items", [])}
    for sku in ("k8s.vcpu", "k8s.mem_gb"):
        if sku in priced:
            print(f"WARNING: {sku} is priced in {args.book}; on a plans book that bills the request meter on every package — remove it")
    missing = [{"sku": s, "unit": u, "annual_price": a, "description": d} for s, u, a, d in METERS if s not in priced]
    if missing:
        api.call("PUT", "/api/v1/pricebooks/" + bid + "/items?merge=true", {"items": missing})
    print(f"meters: {len(missing)} priced, {len(METERS) - len(missing)} already priced (left as they are)")

    doc = api.call("GET", "/api/v1/pricebooks/" + bid + "/packages")

    # 1. The packages' grow settings, written whole.
    settings_written = 0
    for p in doc.get("packages", []):
        rates = RATES.get(p["sku"])
        if rates is None:
            continue
        ceiling = CEILINGS[p["sku"]]
        g = p.get("grow") or {}
        have_rates = {r["key"]: r["price_month"] for r in g.get("overage_rates", [])}
        ceil = g.get("ceiling", {})
        if g.get("allowed") and all(num_eq(ceil.get(k), v) for k, v in ceiling.items()) \
                and num_eq(have_rates.get("vcpu"), rates[0]) and num_eq(have_rates.get("memory"), rates[1]):
            continue
        body = {"tagline": p.get("tagline", ""), "recommended": p.get("recommended", False),
                "annual_months_free": p.get("annual_months_free", 0),
                "icon_id": (p.get("icon") or {}).get("src", "").rsplit("/", 1)[-1] if p.get("icon") else "",
                "accent": p.get("accent", ""), "badge": p.get("badge", ""),
                "grow_allowed": True, "overage_vcpu_month": rates[0], "overage_mem_gb_month": rates[1]}
        for k in SHAPE_KEYS:
            v = p.get("shape", {}).get(k)
            if v is not None:
                body[k] = str(v)
        for k, v in ceiling.items():
            body["grow_ceiling_" + k] = v
        api.call("PUT", "/api/v1/pricebooks/" + bid + "/packages/" + urllib.parse.quote(p["sku"]) + "/settings", body)
        settings_written += 1
    print(f"packages: {settings_written} given grow, the rest already had it")

    # 3 and 4. The cells.
    cells_written = 0
    for f in doc.get("features", []):
        for sku, c in f.get("cells", {}).items():
            path = "/api/v1/pricebooks/" + bid + "/packages/" + urllib.parse.quote(sku) + "/features/" + urllib.parse.quote(f["key"])
            if f["key"] == DR_KEY and f.get("kind") == "level" and sku in ("plan.s", "plan.m", "plan.l"):
                if c.get("state") == "optional" and c.get("grow_only") and c.get("level") == 0:
                    continue
                api.call("PUT", path, {"state": "optional", "level": 0, "grow_only": True})
                cells_written += 1
            elif f.get("kind") == "quantity" and c.get("state") == "included" and c.get("overage") not in ("metered", "unlimited"):
                api.call("PUT", path, {"state": "included", "included_quantity": str(c.get("quantity")), "overage": "metered"})
                cells_written += 1
    print(f"cells: {cells_written} written (DR grow-only on S/M/L, quantity overage metered)")
    print(f"done: {api.writes} write(s){' (dry run: none sent)' if args.dry_run else ''}")


if __name__ == "__main__":
    main()
