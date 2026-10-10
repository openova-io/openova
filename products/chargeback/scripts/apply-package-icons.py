#!/usr/bin/env python3
"""Apply the showcase package icons and branding to an existing BSS.

DESIGN.md §22.10. Uploads the vendored icons in
cmd/seed-history/icons/ (content-addressed, so an icon already stored is
not sent again) and gives every feature, floor item and group its icon,
every feature its tile colour, and every package its accent and badge,
exactly as products/chargeback/cmd/seed-history/icons/manifest.json says —
the same file the seeder reads. Everything goes through the BSS API, as the
sovereign-admin named by --email; each write happens only where the value
differs, so a second run writes nothing.

    python3 apply-package-icons.py --base http://127.0.0.1:18080 \\
        --email admin@example.org [--header X-Forwarded-Email] \\
        [--book "OpenOva plans"] [--dry-run]

Standard library only.

A package's settings are written WHOLE (PUT .../packages/{plan}/settings),
so the tagline, recommended flag, months free and shape are read back from
the book's packages document and re-sent unchanged beside the new icon,
accent and badge. A package with no settings row yet publishes the
catalog's shape as its fallback; writing its branding stores that shape on
the row, which then reads the same.
"""

import argparse
import hashlib
import json
import os
import sys
import urllib.error
import urllib.parse
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
ICONS = os.path.join(HERE, "..", "cmd", "seed-history", "icons")
SHAPE_KEYS = ("vcpu", "memory_gb", "vcpu_guaranteed", "memory_gb_guaranteed", "disk_gb")


class API:
    def __init__(self, base, header, email, dry_run):
        self.base = base.rstrip("/")
        self.header = header
        self.email = email
        self.dry_run = dry_run
        self.writes = 0

    def call(self, method, path, body=None, content_type="application/json"):
        data = None
        if body is not None:
            data = body if isinstance(body, bytes) else json.dumps(body).encode()
        if method != "GET":
            self.writes += 1
            if self.dry_run:
                print(f"  (dry run) {method} {path}")
                return {}
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header(self.header, self.email)
        req.add_header("Accept", "application/json")
        if data is not None:
            req.add_header("Content-Type", content_type)
        try:
            with urllib.request.urlopen(req, timeout=30) as resp:
                raw = resp.read()
        except urllib.error.HTTPError as e:
            msg = e.read().decode(errors="replace")
            sys.exit(f"{method} {path} -> {e.code}: {msg}")
        return json.loads(raw) if raw else {}


def main():
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--base", required=True, help="BSS base URL, e.g. http://127.0.0.1:18080")
    ap.add_argument("--email", required=True, help="the sovereign-admin the trusted header names")
    ap.add_argument("--header", default="X-Forwarded-Email", help="the trusted forward-auth header (default X-Forwarded-Email)")
    ap.add_argument("--book", default="OpenOva plans", help='the plans book to brand (default "OpenOva plans")')
    ap.add_argument("--dry-run", action="store_true", help="read everything, write nothing, print what would be written")
    args = ap.parse_args()

    with open(os.path.join(ICONS, "manifest.json")) as f:
        manifest = json.load(f)
    api = API(args.base, args.header, args.email, args.dry_run)

    # 1. The icons: upload each file BSS does not hold yet.
    files = sorted({v["icon"] for v in manifest["features"].values()}
                   | set(manifest["groups"].values())
                   | {v["icon"] for v in manifest["packages"].values() if v.get("icon")})
    stored = {ic["id"] for ic in api.call("GET", "/api/v1/icons").get("icons", [])}
    ids = {}
    uploaded = 0
    for name in files:
        with open(os.path.join(ICONS, name), "rb") as f:
            data = f.read()
        ids[name] = hashlib.sha256(data).hexdigest()
        if ids[name] in stored:
            continue
        out = api.call("POST", "/api/v1/icons", data, "image/svg+xml")
        if not args.dry_run and out.get("id") != ids[name]:
            sys.exit(f"{name}: BSS stored it as {out.get('id')}, not its sha256 {ids[name]}")
        uploaded += 1
    print(f"icons: {uploaded} uploaded, {len(files) - uploaded} already stored")

    # 2. The features and floor items: icon and tile colour.
    listing = api.call("GET", "/api/v1/features")
    by_key = {f["key"]: f for f in listing.get("features", [])}
    patched, missing = 0, []
    for key, want in sorted(manifest["features"].items()):
        cur = by_key.get(key)
        if cur is None:
            missing.append(key)
            continue
        icon_id, bg = ids[want["icon"]], want.get("bg", "")
        if cur.get("icon_id", "") == icon_id and cur.get("icon_bg", "") == bg.upper():
            continue
        api.call("PATCH", "/api/v1/features/" + urllib.parse.quote(cur["id"]), {"icon_id": icon_id, "icon_bg": bg})
        patched += 1
    print(f"features: {patched} given their icon, {len(manifest['features']) - patched - len(missing)} already had it")
    if missing:
        print(f"features not on this BSS, skipped: {', '.join(missing)}")

    # 3. The groups.
    groups_written = 0
    for g in listing.get("groups", []):
        name = manifest["groups"].get(g["key"])
        if not name or g.get("icon_id", "") == ids[name]:
            continue
        api.call("PUT", "/api/v1/feature-groups/" + urllib.parse.quote(g["key"]), {"icon_id": ids[name]})
        groups_written += 1
    print(f"groups: {groups_written} given their icon")

    # 4. The packages: accent, badge (and an icon where the manifest names
    #    one), with the rest of the settings re-sent as they are.
    books = api.call("GET", "/api/v1/pricebooks").get("pricebooks", [])
    book = next((b for b in books if b.get("name", "").lower() == args.book.lower()), None)
    if book is None:
        sys.exit(f'no price book named "{args.book}"')
    doc = api.call("GET", "/api/v1/pricebooks/" + urllib.parse.quote(book["id"]) + "/packages")
    settings_written = 0
    for p in doc.get("packages", []):
        brand = manifest["packages"].get(p["sku"])
        if brand is None:
            continue
        icon_id = ids[brand["icon"]] if brand.get("icon") else ""
        accent, badge = brand.get("accent", "").upper(), brand.get("badge", "")
        have_icon = p.get("icon", {}).get("src", "").rsplit("/", 1)[-1] if p.get("icon") else ""
        if have_icon == icon_id and p.get("accent", "") == accent and p.get("badge", "") == badge:
            continue
        body = {"tagline": p.get("tagline", ""), "recommended": p.get("recommended", False),
                "annual_months_free": p.get("annual_months_free", 0),
                "icon_id": icon_id, "accent": accent, "badge": badge}
        for k in SHAPE_KEYS:
            v = p.get("shape", {}).get(k)
            if v is not None:
                body[k] = str(v)
        # The grow settings (DESIGN.md §22.11) are part of the whole write:
        # re-send them as the document publishes them, or the branding would
        # switch grow off.
        grow = p.get("grow")
        if grow and grow.get("allowed"):
            body["grow_allowed"] = True
            for k, v in grow.get("ceiling", {}).items():
                body["grow_ceiling_" + k] = str(v)
            rates = {r["key"]: r["price_month"] for r in grow.get("overage_rates", [])}
            if "vcpu" in rates:
                body["overage_vcpu_month"] = rates["vcpu"]
            if "memory" in rates:
                body["overage_mem_gb_month"] = rates["memory"]
        api.call("PUT", "/api/v1/pricebooks/" + urllib.parse.quote(book["id"]) + "/packages/" + urllib.parse.quote(p["sku"]) + "/settings", body)
        settings_written += 1
    print(f"packages: {settings_written} given their accent and badge")
    print(f"done: {api.writes} write(s){' (dry run: none sent)' if args.dry_run else ''}")


if __name__ == "__main__":
    main()
