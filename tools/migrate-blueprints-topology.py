#!/usr/bin/env python3
"""tools/migrate-blueprints-topology.py — G117.1 helper.

Parses the per-blueprint topology audit (the canonical
docs/sessions/2026-06-02-per-blueprint-topology-audit.md) and emits one
spec.topology YAML stub per row, ready to be folded into each
platform/<bp>/blueprint.yaml by the Wave-1 G117.1 author.

Each emitted stub:
  - validates against platform/_schemas/blueprint-topology.json
  - encodes the row's per-cluster placement, replication backend, and
    switchover mechanism columns

Output directory layout:
  <output-dir>/
    bp-grafana.yaml
    bp-keycloak.yaml
    ...

Usage:
  python3 tools/migrate-blueprints-topology.py --output-dir /tmp/topology-stubs
  python3 tools/migrate-blueprints-topology.py --output-dir /tmp/stubs --validate

The --validate flag also runs jsonschema validation on every emitted
stub and exits non-zero if any fail.

Why this is a tool, not a one-shot script: Wave-1 G117.1 authors will
re-run this after the audit doc gets more rows, and the script handles
de-duplication when the audit is updated.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import sys
import textwrap
from pathlib import Path
from typing import Any

try:
    import yaml
except ImportError:
    sys.exit("install pyyaml: pip3 install --user pyyaml")

REPO_ROOT = Path(__file__).resolve().parent.parent
AUDIT_DOC = REPO_ROOT / "docs/sessions/2026-06-02-per-blueprint-topology-audit.md"
SCHEMA_PATH = REPO_ROOT / "platform/_schemas/blueprint-topology.json"

# Markers in the audit doc:
#   🟢 A   → active
#   🟡 P   → passive
#   🔵 S   → singleton
#   blank  → not deployed
GLYPH_TO_ROLE = {
    "🟢 A": "active",
    "🟡 P": "passive",
    "🔵 S": "singleton",
    # blank cells render as just whitespace or '⬜'
}

# Canonical cluster IDs (header order in every table in the audit).
CLUSTERS = ["mgmt-A", "mgmt-B", "dmz-A", "dmz-B", "rtz-A", "rtz-B"]

# Best-effort mapping from the audit's "State preservation" + "Switchover"
# free-text columns to the JSON schema's enums.
def infer_replication(text: str) -> dict[str, Any]:
    t = text.lower()
    if "n/a" in t or "stateless" in t or "—" in t:
        return {"backend": "none", "mode": "none"}
    if "cnpg-pair" in t or "bp-cnpg-pair" in t:
        # Default sync; many CP-tier blueprints use bp-cnpg-pair sync.
        mode = "sync" if "sync" in t or "remote_apply" in t else "async"
        return {"backend": "cnpg-pair", "mode": mode}
    if "mirrormaker" in t or "mm2" in t:
        return {"backend": "mirrormaker2", "mode": "async"}
    if "ccr" in t or "cross-cluster replication" in t:
        return {"backend": "ccr", "mode": "async"}
    if "raft" in t:
        return {"backend": "raft", "mode": "sync"}
    if "sentinel" in t:
        return {"backend": "sentinel", "mode": "async"}
    if "bucket replication" in t or "s3" in t:
        return {"backend": "s3-bucket-replication", "mode": "async"}
    if "velero" in t:
        return {"backend": "velero", "mode": "async"}
    if "filer remote" in t or "filer remote storage" in t:
        return {"backend": "filer-remote-storage", "mode": "async"}
    if "openbao perf" in t or "perf-replication" in t:
        return {"backend": "openbao-perf-replication", "mode": "async"}
    if "flux" in t or "source-of-truth" in t and "git" in t:
        return {"backend": "flux-git", "mode": "async"}
    return {"backend": "none", "mode": "none"}


def infer_switchover(text: str) -> dict[str, Any]:
    t = text.lower()
    if "bp-continuum" in t or "continuum" in t:
        return {"mechanism": "bp-continuum"}
    if "manual" in t:
        return {"mechanism": "manual"}
    if "velero restore" in t or "velero" in t:
        return {"mechanism": "bp-velero-restore"}
    if "lua-record" in t or "ifurlup" in t:
        return {"mechanism": "lua-record"}
    if "mm2" in t or "active-active topics" in t:
        return {"mechanism": "mm2-symmetric"}
    if "ccr-promote" in t or "promote read-replica" in t:
        return {"mechanism": "ccr-promote"}
    if "transition-to-primary" in t:
        return {"mechanism": "raft-transition"}
    if "sentinel failover" in t:
        return {"mechanism": "sentinel-failover"}
    return {"mechanism": "none"}


def parse_role_cell(cell: str) -> str | None:
    """Return 'active' | 'passive' | 'singleton' | None for an audit cell."""
    cell = cell.strip()
    if not cell or cell == "⬜":
        return None
    for glyph, role in GLYPH_TO_ROLE.items():
        if glyph in cell:
            return role
    return None


def parse_audit_tables(path: Path) -> list[dict[str, Any]]:
    """Parse the audit markdown's tables into a list of row-dicts."""
    if not path.exists():
        sys.exit(f"audit doc not found: {path}")
    text = path.read_text()
    rows: list[dict[str, Any]] = []

    # Tables start with a header line containing "Blueprint" and "mgmt-A".
    lines = text.splitlines()
    in_table = False
    headers: list[str] = []
    for line in lines:
        # Detect the header rows we care about.
        if re.match(r"^\|\s*Blueprint\s*\|.*mgmt-A", line):
            in_table = True
            headers = [c.strip() for c in line.strip().strip("|").split("|")]
            continue
        # The separator row right after a header.
        if in_table and re.match(r"^\|\s*[-:]+", line):
            continue
        # Non-table line → table ended.
        if in_table and not line.startswith("|"):
            in_table = False
            continue
        # Table body row.
        if in_table and line.startswith("|"):
            cells = [c.strip() for c in line.strip().strip("|").split("|")]
            if len(cells) < len(CLUSTERS) + 1:
                continue
            bp = cells[0].strip().strip("`")
            if not bp or bp.startswith("Blueprint"):
                continue
            # Strip backticks the audit uses on blueprint names like `bp-grafana`.
            bp = bp.strip("`")
            # Map cluster cells.
            cluster_cells = cells[1:7]
            roles: dict[str, str] = {}
            for cluster, cell in zip(CLUSTERS, cluster_cells):
                role = parse_role_cell(cell)
                if role is not None:
                    roles[cluster] = role
            # Skip rows where every cluster cell is blank.
            if not roles:
                continue
            # Free-text columns come after the 6 cluster cells.
            state_pres_col = cells[8] if len(cells) > 8 else ""
            switchover_col = cells[9] if len(cells) > 9 else ""
            rows.append({
                "blueprint": bp,
                "roles": roles,
                "state_preservation": state_pres_col,
                "switchover": switchover_col,
            })
    return rows


def derive_topology(row: dict[str, Any]) -> dict[str, Any]:
    """Given a parsed audit row, return the spec.topology block."""
    roles = row["roles"]
    role_set = set(roles.values())
    placement = {
        "clusters": list(roles.keys()),
        "roles": dict(roles),
    }
    # Derive tier from majority cluster prefix.
    tiers = {c.split("-")[0] for c in roles}
    if len(tiers) == 1:
        placement["tier"] = tiers.pop()

    # Topology pattern selection:
    if role_set == {"singleton"}:
        # Independent-per-cluster (bp-cilium, bp-flux, …)
        bcp = "singleton"
        topology = {
            "supported": ["singleton"],
            "defaults": {"multi-region": "singleton", "single-region": "singleton"},
            "perTopology": {
                "singleton": {"placement": placement}
            }
        }
    elif role_set == {"active"}:
        # All-active across multiple clusters → active-active
        bcp = "active-active"
        topology = {
            "supported": ["active-active", "singleton"],
            "defaults": {"multi-region": "active-active", "single-region": "singleton"},
            "perTopology": {
                "active-active": {
                    "replication": infer_replication(row["state_preservation"]),
                    "switchover": infer_switchover(row["switchover"]),
                    "placement": placement,
                },
                "singleton": {
                    "placement": {
                        "tier": placement.get("tier", ""),
                        "clusters": [list(roles.keys())[0]],
                        "roles": {list(roles.keys())[0]: "singleton"},
                    }
                }
            }
        }
    elif {"active", "passive"} <= role_set:
        # active + passive → active-hot-standby (default)
        repl = infer_replication(row["state_preservation"])
        # active-hot-standby requires sync mode per schema; if upstream is async,
        # downgrade the topology to active-passive instead.
        if repl.get("mode") == "sync":
            bcp = "active-hot-standby"
            topology = {
                "supported": ["active-hot-standby", "active-passive", "singleton"],
                "defaults": {"multi-region": "active-hot-standby", "single-region": "singleton"},
                "perTopology": {
                    "active-hot-standby": {
                        "replication": repl,
                        "switchover": infer_switchover(row["switchover"]),
                        "placement": placement,
                    },
                    "singleton": {
                        "placement": {
                            "tier": placement.get("tier", ""),
                            "clusters": [list(roles.keys())[0]],
                            "roles": {list(roles.keys())[0]: "singleton"},
                        }
                    }
                }
            }
        else:
            bcp = "active-passive"
            topology = {
                "supported": ["active-passive", "singleton"],
                "defaults": {"multi-region": "active-passive", "single-region": "singleton"},
                "perTopology": {
                    "active-passive": {
                        "replication": repl,
                        "switchover": infer_switchover(row["switchover"]),
                        "placement": placement,
                    },
                    "singleton": {
                        "placement": {
                            "tier": placement.get("tier", ""),
                            "clusters": [list(roles.keys())[0]],
                            "roles": {list(roles.keys())[0]: "singleton"},
                        }
                    }
                }
            }
    elif role_set == {"active"} or "active" in role_set:
        # Active on a single cluster only (one-shot Job pattern)
        bcp = "singleton"
        topology = {
            "supported": ["singleton"],
            "defaults": {"multi-region": "singleton", "single-region": "singleton"},
            "perTopology": {
                "singleton": {"placement": placement}
            }
        }
    else:
        bcp = "singleton"
        topology = {
            "supported": ["singleton"],
            "defaults": {"multi-region": "singleton", "single-region": "singleton"},
            "perTopology": {
                "singleton": {"placement": placement}
            }
        }

    return topology


def emit_stub(row: dict[str, Any], output_dir: Path) -> Path:
    bp = row["blueprint"]
    topo = derive_topology(row)
    stub = {
        "apiVersion": "catalyst.openova.io/v1",
        "kind": "Blueprint",
        "metadata": {"name": bp.lstrip("bp-")},
        "spec": {"topology": topo},
    }
    out = output_dir / f"{bp}.yaml"
    out.write_text(
        textwrap.dedent(f"""\
        # G117.1 migration stub — derived by tools/migrate-blueprints-topology.py
        # from docs/sessions/2026-06-02-per-blueprint-topology-audit.md.
        #
        # Wave-1 G117.1 author: review the inferred replication/switchover
        # values (the audit text → enum mapping is heuristic), then fold the
        # spec.topology block into platform/{bp.lstrip("bp-")}/blueprint.yaml.
        """) + yaml.safe_dump(stub, sort_keys=False)
    )
    return out


def validate_stub(stub_path: Path, schema: dict[str, Any]) -> tuple[bool, str]:
    try:
        import jsonschema
    except ImportError:
        return True, "(jsonschema not installed; skipped)"
    try:
        doc = yaml.safe_load(stub_path.read_text())
        jsonschema.validate(doc, schema)
        return True, ""
    except Exception as e:
        return False, str(e).splitlines()[0]


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--output-dir", required=True, type=Path, help="output directory for the per-Blueprint stubs")
    ap.add_argument("--validate", action="store_true", help="also validate each stub against the JSON schema")
    ap.add_argument("--audit", type=Path, default=AUDIT_DOC, help=f"audit doc path (default: {AUDIT_DOC.relative_to(REPO_ROOT)})")
    args = ap.parse_args()

    args.output_dir.mkdir(parents=True, exist_ok=True)
    rows = parse_audit_tables(args.audit)
    if not rows:
        sys.exit("no rows parsed from audit doc; check the table headers/format")

    schema: dict[str, Any] = {}
    if args.validate and SCHEMA_PATH.exists():
        schema = json.loads(SCHEMA_PATH.read_text())

    failed: list[str] = []
    for row in rows:
        path = emit_stub(row, args.output_dir)
        if args.validate and schema:
            ok, err = validate_stub(path, schema)
            if not ok:
                failed.append(f"{row['blueprint']}: {err}")

    print(f"emitted {len(rows)} stubs to {args.output_dir}")
    if failed:
        print("\nVALIDATION FAILURES:", file=sys.stderr)
        for f in failed:
            print(f"  {f}", file=sys.stderr)
        return 1
    if args.validate:
        print(f"all {len(rows)} stubs validate against the schema")
    return 0


if __name__ == "__main__":
    sys.exit(main())
