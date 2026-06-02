#!/usr/bin/env python3
"""migrate-applications-instanceId.py — G117 W2.C2 backfill.

Backfills `spec.instanceId`, `spec.isolationLevel`, and `spec.namingTemplate`
on every legacy Application CR in a cluster — the new fields added to the
Application CRD by G117 W2.C2.

Idempotency contract (DoD §"Migration script idempotent — run twice on
same cluster, second run is no-op"):

  * Only Applications without `spec.instanceId` are patched.
  * The patch payload always sets all three fields together (so a CR that
    already has `spec.instanceId` is skipped entirely — no churn on the
    isolationLevel/namingTemplate fields).
  * `spec.instanceId` is derived deterministically from
    `metadata.uid[:8]` so re-running on the same cluster (in the
    unlikely event we lose the previous backfill) produces the SAME
    InstanceID. Future-proof against rare cases where someone deletes
    only the `instanceId` field by hand.

Usage:

    python3 tools/migrate-applications-instanceId.py [--dry-run] [--kubeconfig PATH]

Wiring into bp-catalyst-platform (per the W2.C2 brief anti-theater
red flag #4 — "Migration script that requires manual run on every
cluster — must be wired into bp-catalyst-platform Job"):

    Apply this script as a one-shot Job in
    products/catalyst/charts/bp-catalyst-platform/templates/migrate-applications-instanceId-job.yaml
    with restartPolicy: OnFailure + a sufficient ServiceAccount RBAC
    (get/list/patch on applications.apps.openova.io).

Exit codes:
  0 — success (regardless of how many CRs were patched)
  1 — usage / connection error
  2 — at least one CR failed to patch (others may have succeeded)
"""

import argparse
import json
import os
import subprocess
import sys


DEFAULT_TEMPLATE_NAMESPACE = "{{.AppName}}-{{.InstanceID}}"
DEFAULT_TEMPLATE_VCLUSTER  = "{{.AppName}}"


def kubectl(args, kubeconfig=None, capture=True, check=True):
    cmd = ["kubectl"]
    if kubeconfig:
        cmd.extend(["--kubeconfig", kubeconfig])
    cmd.extend(args)
    if capture:
        result = subprocess.run(cmd, capture_output=True, text=True, check=False)
    else:
        result = subprocess.run(cmd, text=True, check=False)
    if check and result.returncode != 0:
        raise RuntimeError(
            f"kubectl {' '.join(args)} failed (rc={result.returncode}): "
            f"{getattr(result, 'stderr', '')}"
        )
    return result


def list_applications(kubeconfig):
    """Return list of Application CRs as dicts."""
    r = kubectl(
        ["get", "applications.apps.openova.io", "-A", "-o", "json"],
        kubeconfig=kubeconfig,
    )
    payload = json.loads(r.stdout or "{}")
    return payload.get("items", [])


def first_uid_chars(uid):
    """Return first 8 chars of UID — same logic as
    appv1alpha1.FirstUIDChars on the Go side."""
    if not uid:
        return ""
    return uid[:8]


def derive_isolation_level(item):
    """Default isolationLevel = 'namespace'. The CRD schema's default
    handles new CRs; this function is for the migration backfill."""
    return "namespace"


def derive_naming_template(isolation_level):
    if isolation_level == "vcluster":
        return DEFAULT_TEMPLATE_VCLUSTER
    return DEFAULT_TEMPLATE_NAMESPACE


def needs_migration(item):
    """True iff spec.instanceId is missing or empty.

    Idempotency: any non-empty instanceId means we've already migrated
    this CR — leave it alone.
    """
    spec = item.get("spec") or {}
    return not spec.get("instanceId")


def build_patch(item):
    uid = (item.get("metadata") or {}).get("uid", "")
    instance_id = first_uid_chars(uid)
    if not instance_id:
        return None
    isolation = derive_isolation_level(item)
    template = derive_naming_template(isolation)
    return {
        "spec": {
            "instanceId":     instance_id,
            "isolationLevel": isolation,
            "namingTemplate": template,
        }
    }


def apply_patch(namespace, name, patch, kubeconfig, dry_run=False):
    args = [
        "patch", "applications.apps.openova.io",
        "-n", namespace, name,
        "--type=merge",
        "-p", json.dumps(patch),
    ]
    if dry_run:
        args.append("--dry-run=server")
    return kubectl(args, kubeconfig=kubeconfig, check=False)


def main():
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    p.add_argument("--kubeconfig", default=os.environ.get("KUBECONFIG"),
                   help="path to kubeconfig (defaults to $KUBECONFIG or in-cluster)")
    p.add_argument("--dry-run", action="store_true",
                   help="print intended patches without applying")
    p.add_argument("--quiet", action="store_true",
                   help="suppress per-CR log lines (still emits summary)")
    args = p.parse_args()

    try:
        items = list_applications(args.kubeconfig)
    except (RuntimeError, json.JSONDecodeError) as e:
        print(f"ERROR: could not list Applications: {e}", file=sys.stderr)
        return 1

    if not items:
        print("No Application CRs found — nothing to migrate.")
        return 0

    n_skipped = 0
    n_patched = 0
    n_failed  = 0

    for item in items:
        meta = item.get("metadata") or {}
        ns = meta.get("namespace", "")
        name = meta.get("name", "")

        if not needs_migration(item):
            if not args.quiet:
                print(f"  skip   {ns}/{name} (instanceId already set)")
            n_skipped += 1
            continue

        patch = build_patch(item)
        if patch is None:
            print(f"  ERROR  {ns}/{name}: empty metadata.uid — cannot derive instanceId", file=sys.stderr)
            n_failed += 1
            continue

        r = apply_patch(ns, name, patch, args.kubeconfig, dry_run=args.dry_run)
        if r.returncode != 0:
            print(f"  ERROR  {ns}/{name}: patch failed — {r.stderr.strip()}", file=sys.stderr)
            n_failed += 1
            continue

        verb = "would-patch" if args.dry_run else "patched"
        if not args.quiet:
            print(f"  {verb} {ns}/{name} → instanceId={patch['spec']['instanceId']} isolation={patch['spec']['isolationLevel']}")
        n_patched += 1

    print()
    print(f"summary: total={len(items)} patched={n_patched} skipped={n_skipped} failed={n_failed}{'  (dry-run)' if args.dry_run else ''}")

    return 2 if n_failed > 0 else 0


if __name__ == "__main__":
    sys.exit(main())
