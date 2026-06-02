#!/usr/bin/env python3
"""G117.1 Wave-1 B3 — apply per-blueprint topology+endpoints+sso+multiInstance.

For each of the 29 App-tier Blueprints declared in g117_w1b3_app_tier_decls:
  - If platform/<bp>/blueprint.yaml exists → load, merge the 4 new blocks under
    spec, write back (preserving comments by appending raw YAML blocks).
  - If platform/<bp>/blueprint.yaml is missing → emit a minimal scaffold with
    only the 4 new blocks + standard metadata header.

Validates each result against platform/_schemas/blueprint-topology.json.

Usage:
  python3 tools/g117_w1b3_apply.py --apply
  python3 tools/g117_w1b3_apply.py --validate-only
"""
from __future__ import annotations

import argparse
import json
import sys
from pathlib import Path

try:
    import yaml
except ImportError:
    sys.exit("install pyyaml: pip3 install --user pyyaml")

try:
    import jsonschema
except ImportError:
    sys.exit("install jsonschema: pip3 install --user jsonschema")

sys.path.insert(0, str(Path(__file__).parent))
from g117_w1b3_app_tier_decls import all_decls  # noqa: E402

REPO_ROOT = Path(__file__).resolve().parent.parent
SCHEMA_PATH = REPO_ROOT / "platform/_schemas/blueprint-topology.json"
PLATFORM_DIR = REPO_ROOT / "platform"

# Marker comment for the appended blocks (idempotent re-apply support).
START_MARKER = "  # ── G117.1 (Wave-1/B3) — topology + endpoints + sso + multiInstance ──"
END_MARKER = "  # ── /G117.1 ──"


def _emit_blocks(decl: dict) -> str:
    """Render the 4 new blocks as a YAML fragment indented under spec:."""
    out_obj: dict = {}
    if "topology" in decl and decl["topology"] is not None:
        out_obj["topology"] = decl["topology"]
    if "endpoints" in decl and decl["endpoints"] is not None:
        out_obj["endpoints"] = decl["endpoints"]
    if decl.get("sso") is not None:
        out_obj["sso"] = decl["sso"]
    if "multiInstance" in decl and decl["multiInstance"] is not None:
        out_obj["multiInstance"] = decl["multiInstance"]

    # Dump and re-indent to fit under spec:
    raw = yaml.safe_dump(out_obj, sort_keys=False, default_flow_style=False, width=1000)
    indented = "\n".join("  " + line if line else line for line in raw.splitlines())
    return "\n".join([START_MARKER, indented, END_MARKER, ""])


def _strip_existing_g117_block(text: str) -> str:
    """If the markers are already present, remove the prior block (idempotent)."""
    if START_MARKER not in text:
        return text
    start = text.index(START_MARKER)
    end = text.index(END_MARKER, start) + len(END_MARKER)
    # Also consume trailing newline if present
    if end < len(text) and text[end] == "\n":
        end += 1
    return text[:start] + text[end:]


def apply_to_existing(bp_dir: Path, bp_name: str, decl: dict) -> None:
    """Append the 4 new blocks to an existing blueprint.yaml under spec:."""
    bp_yaml = bp_dir / "blueprint.yaml"
    text = bp_yaml.read_text()
    text = _strip_existing_g117_block(text)

    # The blocks belong under spec:. Append to end of file (file must already
    # have `spec:` at top level). Trailing newline-safe append.
    if not text.endswith("\n"):
        text += "\n"
    text += _emit_blocks(decl)
    bp_yaml.write_text(text)


def create_scaffold(bp_dir: Path, bp_name: str, decl: dict) -> None:
    """Create a minimal blueprint.yaml for a Blueprint that has no manifest yet."""
    bp_dir.mkdir(parents=True, exist_ok=True)
    short = bp_name[len("bp-"):]
    header = f"""apiVersion: catalyst.openova.io/v1
kind: Blueprint
metadata:
  name: {bp_name}
  labels:
    catalyst.openova.io/section: pts-4-application-tier
spec:
  # G117.1 (Wave-1/B3) — scaffold-only Application Blueprint.
  #
  # This Blueprint folder currently only ships a README; the upstream chart
  # is tracked but not yet packaged. The four new blocks below declare the
  # intended target-state per docs/sessions/2026-06-02-per-blueprint-topology-audit.md,
  # so the catalog UI, application-controller, and SSO fan-out can plan
  # against it ahead of the chart being authored.
  version: 0.0.0
  card:
    title: {short.replace('-', ' ').title()}
    summary: |
      Scaffold-only Application Blueprint — chart not yet packaged.
      Topology + endpoints + sso + multiInstance are declared per the
      G117.1 audit so downstream Catalyst surfaces can render against
      it.
"""
    bp_yaml = bp_dir / "blueprint.yaml"
    bp_yaml.write_text(header)
    # Now append the 4 new blocks (idempotent path uses same logic).
    apply_to_existing(bp_dir, bp_name, decl)


def validate_file(bp_yaml: Path, schema: dict) -> tuple[bool, str]:
    try:
        doc = yaml.safe_load(bp_yaml.read_text())
        # We only validate the new fields' shape — top-level required keys
        # (apiVersion/kind/metadata/spec) are also present on every file we
        # touched, but the schema's "additionalProperties" stance leaves
        # legacy fields free.
        jsonschema.validate(doc, schema)
        return True, ""
    except Exception as e:
        return False, str(e).splitlines()[0][:200]


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--apply", action="store_true", help="write changes")
    ap.add_argument("--validate-only", action="store_true",
                    help="only validate (no writes)")
    args = ap.parse_args()

    if not (args.apply or args.validate_only):
        ap.error("specify --apply or --validate-only")

    schema = json.loads(SCHEMA_PATH.read_text())
    decls = all_decls()
    failures: list[str] = []
    created: list[str] = []
    appended: list[str] = []

    for bp_name, decl in decls.items():
        short = bp_name[len("bp-"):]
        bp_dir = PLATFORM_DIR / short
        bp_yaml = bp_dir / "blueprint.yaml"

        if args.apply:
            if bp_yaml.exists():
                apply_to_existing(bp_dir, bp_name, decl)
                appended.append(bp_name)
            else:
                create_scaffold(bp_dir, bp_name, decl)
                created.append(bp_name)

        if bp_yaml.exists() or args.apply:
            ok, err = validate_file(bp_yaml, schema)
            if not ok:
                failures.append(f"{bp_name}: {err}")

    print(f"appended: {len(appended)}")
    print(f"created scaffolds: {len(created)} → {', '.join(created) if created else '(none)'}")
    if failures:
        print(f"\nVALIDATION FAILURES ({len(failures)}):", file=sys.stderr)
        for f in failures:
            print(f"  {f}", file=sys.stderr)
        return 1
    print(f"\nall {len(decls)} blueprints valid against schema")
    return 0


if __name__ == "__main__":
    sys.exit(main())
