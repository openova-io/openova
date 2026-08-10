#!/usr/bin/env python3
"""Fail a chart whose Helm release Secret would exceed the Kubernetes object ceiling.

WHY THIS EXISTS (#6004)
-----------------------
On hw293 the Phase-1 install of `bp-self-sovereign-cutover@0.1.177` failed 10
times in 13 minutes with:

    create: failed to create: Secret "sh.helm.release.v1.self-sovereign-cutover.v1"
    is invalid: data: Too long: must have at most 1048576 bytes

Helm stores each release as a Secret whose single `release` key is

    base64( gzip( json(release) ) )

and Kubernetes caps the summed length of a Secret's data values at 1 MiB.
Nothing in CI measured that number, so the chart grew across ~7 releases until
it crossed the ceiling — and the only place the crossing was observable was a
fresh Sovereign's Phase 1, where it takes out Pillar 5 entirely.

WHAT IT MEASURES
----------------
The same four payload components Helm stores, in Helm's own order:

  * chart.templates[].data  — every file under templates/, base64 in the JSON
  * chart.values            — values.yaml PARSED (comments are dropped here)
  * chart.files[].data      — every other packaged file, base64 in the JSON
  * release.manifest        — the rendered output

Two consequences drive how you keep a chart under the ceiling:

  1. A `#` comment in a template is paid for TWICE — once in the template
     bytes, once again in the rendered manifest. A `{{/* … */}}` comment is
     paid for once. Converting the former to the latter costs a reader of the
     source nothing and removes the bytes from every release Secret.
  2. Files that only CI needs (chart/tests/*.sh are executed against the
     working tree, never from the package) belong in `.helmignore`. They are
     pure payload otherwise.

MODEL FIDELITY
--------------
This reproduces Go's encoder rather than calling it: keys sorted, `<`, `>`,
`&` escaped as \\uXXXX the way encoding/json does, non-ASCII emitted raw,
gzip level 9.

It was cross-checked against helm.sh/helm/v3 v3.16.3's OWN
storage/driver encoder (action.Install with ClientOnly, then the release
struct through the driver's json→gzip→base64), on bp-self-sovereign-cutover
with the slot-06a overlay, on both sides of the #6004 fix:

    chart state          helm's encoder      this model      delta
    0.1.177 (failing)         1233060          1216220      -1.37%
    0.1.179 (fixed)            757264           745988      -1.49%

The residual is Go's flate implementation against zlib's, and it goes the
UNSAFE way — the model reads slightly low. MODEL_ALLOWANCE below scales the
measurement up by more than the observed drift so the verdict stays
conservative, and the default budget then leaves a further 20% of the ceiling
unused. A chart that clears this gate has real headroom, not a rounding error.
"""
from __future__ import annotations

import argparse
import base64
import glob
import gzip
import io
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile

K8S_SECRET_LIMIT = 1048576  # hard cap on the summed length of a Secret's data
# Fail above 90% — a chart within 10% of the cliff is one comment block away
# from an install that cannot succeed on any Sovereign, and the cliff is only
# observable at install time. NOTICE_PCT is loud but non-fatal, so a chart's
# approach shows up in CI output long before it blocks a publish.
DEFAULT_BUDGET_PCT = 90.0
NOTICE_PCT = 75.0
# Scale the modelled payload up before judging it. The model reads ~1.5% below
# helm's own encoder (see MODEL FIDELITY above) and the error goes the unsafe
# way, so the verdict is taken on the scaled figure, never the raw one.
MODEL_ALLOWANCE = 1.03


class MeasureError(RuntimeError):
    """A chart could not be measured — never silently treated as passing."""


def _fatal(msg: str) -> None:
    print(f"FATAL: {msg}", file=sys.stderr)
    sys.exit(2)


def go_json(obj) -> bytes:
    """Serialize the way Go's encoding/json does.

    Go sorts map keys, emits raw UTF-8, and HTML-escapes `<`, `>` and `&`.
    Those three characters cannot appear outside a JSON string literal, so
    escaping them after serialization is exact, not an approximation.
    """
    s = json.dumps(obj, sort_keys=True, separators=(",", ":"), ensure_ascii=False)
    s = s.replace("<", "\\u003c").replace(">", "\\u003e").replace("&", "\\u0026")
    return s.encode("utf-8")


def encode_release(release: dict) -> int:
    """len(base64(gzip(json(release)))) — Helm storage/driver.encodeRelease."""
    raw = go_json(release)
    buf = io.BytesIO()
    # mtime=0 so the measurement is reproducible run to run.
    with gzip.GzipFile(fileobj=buf, mode="wb", compresslevel=9, mtime=0) as gz:
        gz.write(raw)
    return len(base64.b64encode(buf.getvalue()))


def package_chart(chart_dir: str, workdir: str) -> str:
    """helm package the chart so .helmignore is applied exactly as at release."""
    out = subprocess.run(
        ["helm", "package", chart_dir, "--destination", workdir],
        capture_output=True, text=True,
    )
    if out.returncode != 0:
        raise MeasureError(f"helm package failed for {chart_dir}:\n{out.stderr.strip()}")
    tgz = [f for f in os.listdir(workdir) if f.endswith(".tgz")]
    if len(tgz) != 1:
        raise MeasureError(f"expected one packaged chart in {workdir}, found {tgz}")
    return os.path.join(workdir, tgz[0])


def read_package(tgz: str) -> tuple[dict, dict, list, list]:
    """Split a packaged chart into (metadata, values, templates, files) as Helm's loader does."""
    values: dict = {}
    metadata: dict = {}
    templates: list = []
    files: list = []
    with tarfile.open(tgz, "r:gz") as tf:
        for member in sorted(tf.getmembers(), key=lambda m: m.name):
            if not member.isfile():
                continue
            # Strip the leading "<chartname>/" the package adds.
            rel = member.name.split("/", 1)[1] if "/" in member.name else member.name
            data = tf.extractfile(member).read()
            if rel == "Chart.yaml":
                # Stored as a PARSED Metadata struct, never as bytes — which is
                # why Chart.yaml comments cost nothing in the release Secret
                # while template comments cost twice.
                metadata = load_yaml_bytes(data)
                continue
            if rel == "values.yaml":
                values = load_yaml_bytes(data)
                continue
            entry = {"name": rel, "data": base64.b64encode(data).decode("ascii")}
            if rel.startswith("templates/"):
                templates.append(entry)
            else:
                files.append(entry)
    return metadata, values, templates, files


def load_yaml_bytes(data: bytes):
    try:
        import yaml  # type: ignore
    except ImportError:
        # PyYAML is not guaranteed on every runner. values.yaml contributes a
        # low-single-digit-KB share of the payload, so fall back to counting it
        # as its own raw bytes rather than skipping the measurement entirely.
        return {"__unparsed_values__": data.decode("utf-8", "replace")}
    return yaml.safe_load(data) or {}


def render(tgz: str, values_files: list[str], release: str, namespace: str) -> str:
    cmd = ["helm", "template", release, tgz, "--namespace", namespace]
    for v in values_files:
        cmd += ["-f", v]
    out = subprocess.run(cmd, capture_output=True, text=True)
    if out.returncode != 0:
        raise MeasureError(f"helm template failed for {tgz}:\n{out.stderr.strip()}")
    return out.stdout


def measure(chart_dir: str, values_files: list[str], release: str, namespace: str) -> dict:
    workdir = tempfile.mkdtemp(prefix="relpayload-")
    try:
        tgz = package_chart(chart_dir, workdir)
        metadata, values, templates, files = read_package(tgz)
        manifest = render(tgz, values_files, release, namespace)
        user_values: dict = {}
        for v in values_files:
            with open(v, "rb") as fh:
                merged = load_yaml_bytes(fh.read())
            if isinstance(merged, dict):
                user_values.update(merged)
        rel = {
            "name": release,
            "info": {"status": "deployed", "description": "Install complete"},
            "chart": {
                "metadata": metadata,
                "templates": templates,
                "values": values,
                "files": files,
            },
            "config": user_values,
            "manifest": manifest,
            "version": 1,
            "namespace": namespace,
        }
        payload = encode_release(rel)
        tpl_bytes = sum(len(base64.b64decode(t["data"])) for t in templates)
        file_bytes = sum(len(base64.b64decode(f["data"])) for f in files)
        return {
            "payload": payload,
            "manifest_bytes": len(manifest.encode("utf-8")),
            "template_bytes": tpl_bytes,
            "file_bytes": file_bytes,
            "n_templates": len(templates),
            "n_files": len(files),
        }
    finally:
        shutil.rmtree(workdir, ignore_errors=True)


def declares_dependencies(chart_dir: str) -> bool:
    """True when Chart.yaml declares upstream subcharts.

    Such a chart cannot be packaged without `helm dependency build` (which needs
    registry credentials), so a plain repo sweep cannot measure it. It is still
    gated — blueprint-release.yaml runs this same check after it builds the
    dependencies and before it pushes, so nothing reaches GHCR unmeasured.
    """
    with open(os.path.join(chart_dir, "Chart.yaml"), encoding="utf-8") as fh:
        for line in fh:
            if re.match(r"^dependencies:\s*(#.*)?$", line):
                return True
    return False


def discover_charts(root: str) -> list[str]:
    found = []
    for pattern in ("platform/*/chart", "products/*/chart", "products/*/charts/*"):
        for d in sorted(glob.glob(os.path.join(root, pattern))):
            if os.path.isfile(os.path.join(d, "Chart.yaml")):
                found.append(os.path.relpath(d, root))
    return found


def sweep(root: str, budget_pct: float) -> int:
    """Measure every chart that packages standalone; defer the rest, loudly."""
    measured, deferred, over, broken = [], [], [], []
    budget = int(K8S_SECRET_LIMIT * budget_pct / 100.0)
    for chart in discover_charts(root):
        full = os.path.join(root, chart)
        if declares_dependencies(full):
            deferred.append(chart)
            continue
        try:
            m = measure(full, [], "release", "default")
        except MeasureError as exc:
            broken.append((chart, str(exc).splitlines()[0]))
            continue
        judged = int(m["payload"] * MODEL_ALLOWANCE)
        measured.append((chart, judged))
        if judged > budget:
            over.append((chart, judged))

    for chart, judged in sorted(measured, key=lambda kv: -kv[1]):
        pct = 100.0 * judged / K8S_SECRET_LIMIT
        flag = "OVER " if judged > budget else ("near " if pct >= NOTICE_PCT else "     ")
        print(f"  {flag}{judged:>9} bytes {pct:>6.2f}%  {chart}")
    print(f"\nmeasured {len(measured)} chart(s); "
          f"{len(deferred)} deferred to blueprint-release (declare dependencies:)")

    if broken:
        for chart, why in broken:
            print(f"::error title=Chart could not be measured::{chart}: {why}",
                  file=sys.stderr)
        print("A dependency-free chart that will not package is not a pass — fix it "
              "or the payload ceiling goes unmeasured for that chart.", file=sys.stderr)
        return 1
    if over:
        for chart, judged in over:
            print(f"::error title=Helm release Secret too large::{chart} would store "
                  f"~{judged} bytes, over the {budget_pct:g}% budget of the "
                  f"{K8S_SECRET_LIMIT}-byte Secret ceiling (#6004).", file=sys.stderr)
        return 1
    return 0


SELF_TEST_CHART_YAML = "apiVersion: v2\nname: payload-size-selftest\nversion: 0.0.1\n"


def _write_selftest_chart(root: str, comment_lines: int) -> str:
    """Build a chart whose only variable is how much `#` comment it renders."""
    os.makedirs(os.path.join(root, "templates"), exist_ok=True)
    with open(os.path.join(root, "Chart.yaml"), "w") as fh:
        fh.write(SELF_TEST_CHART_YAML)
    with open(os.path.join(root, "values.yaml"), "w") as fh:
        fh.write("{}\n")
    # Pseudo-random-ish but deterministic filler: repetitive text would gzip to
    # nothing and the test would prove only that gzip works.
    body = []
    for i in range(comment_lines):
        body.append("    # line %07d %s" % (i, hex(i * 2654435761 % (1 << 60))[2:]))
    with open(os.path.join(root, "templates", "cm.yaml"), "w") as fh:
        fh.write("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: selftest\n"
                 "data:\n  payload.sh: |\n" + "\n".join(body) + "\n    exit 0\n")
    return root


def self_test() -> int:
    """Prove the gate fails on an over-budget chart and passes on a small one.

    A size check nobody has seen fail is not a check. This runs both directions
    on every CI invocation, so the gate cannot rot into one that always passes.
    """
    ok = True
    tmp = tempfile.mkdtemp(prefix="relpayload-selftest-")
    try:
        for name, lines, want in (("under", 200, 0), ("over", 60000, 1)):
            root = _write_selftest_chart(os.path.join(tmp, name), lines)
            m = measure(root, [], "selftest", "default")
            judged = int(m["payload"] * MODEL_ALLOWANCE)
            budget = int(K8S_SECRET_LIMIT * DEFAULT_BUDGET_PCT / 100.0)
            got = 1 if judged > budget else 0
            verdict = "PASS" if got == want else "FAIL"
            if got != want:
                ok = False
            print(f"self-test {name:<6} {judged:>9} bytes vs budget {budget} "
                  f"-> exit {got} (want {want}) {verdict}")
        if not ok:
            print("::error title=payload-size guard self-test failed::The gate did "
                  "not return the expected verdict in both directions — it can no "
                  "longer be trusted to catch an over-ceiling chart.", file=sys.stderr)
            return 1
        print("self-test OK — the gate fails over budget and passes under it.")
        return 0
    finally:
        shutil.rmtree(tmp, ignore_errors=True)


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__,
                                 formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("chart", nargs="?", help="path to a chart directory")
    ap.add_argument("--self-test", action="store_true",
                    help="prove the gate goes red over budget and green under it")
    ap.add_argument("--all", metavar="REPO_ROOT", nargs="?", const=".",
                    help="sweep every chart in the repo that packages standalone")
    ap.add_argument("-f", "--values", action="append", default=[],
                    help="extra values file (repeatable) — pass the per-Sovereign "
                         "overlay to measure what a real install stores")
    ap.add_argument("--release-name", default="release")
    ap.add_argument("--namespace", default="default")
    ap.add_argument("--budget-pct", type=float, default=DEFAULT_BUDGET_PCT,
                    help=f"fail above this %% of the {K8S_SECRET_LIMIT}-byte "
                         f"ceiling (default {DEFAULT_BUDGET_PCT})")
    args = ap.parse_args()

    if not shutil.which("helm"):
        _fatal("helm not on PATH — this gate measures what helm itself would store")
    if args.self_test:
        return self_test()
    if args.all:
        return sweep(args.all, args.budget_pct)
    if not args.chart:
        _fatal("a chart directory is required (or pass --self-test / --all)")
    if not os.path.isfile(os.path.join(args.chart, "Chart.yaml")):
        _fatal(f"{args.chart} is not a chart directory (no Chart.yaml)")

    m = measure(args.chart, args.values, args.release_name, args.namespace)
    budget = int(K8S_SECRET_LIMIT * args.budget_pct / 100.0)
    judged = int(m["payload"] * MODEL_ALLOWANCE)
    pct = 100.0 * judged / K8S_SECRET_LIMIT

    print(f"chart                     : {args.chart}")
    print(f"  templates               : {m['n_templates']:>4} files, {m['template_bytes']:>9} bytes")
    print(f"  packaged files          : {m['n_files']:>4} files, {m['file_bytes']:>9} bytes")
    print(f"  rendered manifest       : {m['manifest_bytes']:>9} bytes")
    print(f"  modelled payload        : {m['payload']:>9} bytes")
    print(f"  judged (x{MODEL_ALLOWANCE:g} allowance) : {judged:>9} bytes  ({pct:.2f}% of {K8S_SECRET_LIMIT})")
    print(f"  budget ({args.budget_pct:g}% of ceiling) : {budget:>9} bytes")

    if judged > budget:
        over = judged - budget
        print()
        print(f"::error title=Helm release Secret too large::{args.chart} would store "
              f"~{judged} bytes in sh.helm.release.v1.* — {over} bytes over the "
              f"{args.budget_pct:g}% budget of the {K8S_SECRET_LIMIT}-byte Kubernetes "
              f"Secret ceiling. Above 100% every `helm install` of this chart fails with "
              f"`data: Too long: must have at most {K8S_SECRET_LIMIT} bytes` (#6004).",
              file=sys.stderr)
        print("Cheapest reductions, in order, none of which delete reasoning:",
              file=sys.stderr)
        print("  1. Convert whole-line `#` comments in templates/ to `{{/* … */}}`. "
              "A `#` comment is stored twice (template bytes + rendered manifest); a "
              "`{{/* */}}` comment is stored once and still reads inline in the source.",
              file=sys.stderr)
        print("  2. Add build-time-only paths (chart/tests/*.sh, fixtures) to "
              ".helmignore — CI runs them from the working tree, so packaging them "
              "is pure payload.", file=sys.stderr)
        print("  3. Split the chart if a single release genuinely needs this much "
              "rendered output.", file=sys.stderr)
        return 1

    if pct >= NOTICE_PCT:
        print(f"::notice title=Helm release Secret approaching the ceiling::"
              f"{args.chart} is at {pct:.1f}% of the {K8S_SECRET_LIMIT}-byte "
              f"Secret ceiling. It still publishes and installs, but it is the "
              f"next chart in line for #6004. Reduce it before it reaches "
              f"{args.budget_pct:g}%.")
    print(f"OK — {budget - judged} bytes under budget, "
          f"{K8S_SECRET_LIMIT - judged} bytes under the hard ceiling.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
