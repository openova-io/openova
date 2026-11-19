#!/usr/bin/env python3
"""check-crossplane-runtime-probes.py - every Crossplane Provider and Function
the repository installs runs with liveness and readiness probes on a port its
runtime serves.

WHY
Crossplane's package manager creates one Deployment per ProviderRevision and
per FunctionRevision. Its pod template is built from the
DeploymentRuntimeConfig the package names in spec.runtimeConfigRef; Crossplane
itself adds no probe. The Kyverno policy probes-present (Enforce) refuses a
Deployment whose containers lack a liveness or a readiness probe. Measured on
hw307 (candidate rc/20261119T053558Z-6972370, Crossplane core 1.20.14 ->
2.4.3): the new revision of provider-opentofu was refused, the provider went
NOT Healthy and the old pod kept running. A package without runtimeConfigRef
gets the runtime config `default`, which Crossplane creates without probes.

WHAT IT PROVES, for every document of kind Provider or Function with an
apiVersion of pkg.crossplane.io in clusters/, platform/ and products/:
  R1 spec.runtimeConfigRef.name is set;
  R2 a DeploymentRuntimeConfig of that name is declared in the same directory;
  R3 every container of its deploymentTemplate, and the runtime container
     package-runtime in any case, declares livenessProbe and readinessProbe,
     each with exactly one handler (httpGet, tcpSocket, grpc or exec);
  R4 every httpGet, tcpSocket or grpc port of package-runtime is a port
     Crossplane 2.4.3 declares on the runtime container: 8080 (`metrics`) for
     a Provider and a Function, 9443 (`grpc`) for a Function
     (internal/controller/pkg/runtime/runtime.go MetricsPortNumber and GRPCPort);
     a probe of a port the runtime does not declare can only fail;
  R5 a Provider or Function in a file this guard cannot read as YAML (a chart
     template) is refused with the instruction to extend the guard, so a new
     placement is never skipped in silence;
  R6 the scan finds at least one Provider and one Function, so it cannot pass
     by reading nothing.

Python 3 and PyYAML only (the tree-guards job). `--self-test` proves each rule
can fail.

Exit: 0 all rules hold, 1 a rule fails, 2 usage error.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
SCAN_DIRS = ("clusters", "platform", "products")
SUFFIXES = (".yaml", ".yml", ".tpl")
RUNTIME_CONTAINER = "package-runtime"
HANDLERS = ("httpGet", "tcpSocket", "grpc", "exec")
PORTS = {
    "Provider": {8080, "metrics"},
    "Function": {8080, "metrics", 9443, "grpc"},
}
PKG_KIND = re.compile(r"^kind:\s*[\"']?(Provider|Function)[\"']?\s*$", re.M)


def collect(root: Path) -> dict[str, str]:
    """Every scanned file that mentions a pkg.crossplane.io kind we judge."""
    files: dict[str, str] = {}
    for top in SCAN_DIRS:
        base = root / top
        if not base.is_dir():
            continue
        for p in sorted(base.rglob("*")):
            if p.suffix not in SUFFIXES or not p.is_file():
                continue
            try:
                text = p.read_text(encoding="utf-8")
            except (OSError, UnicodeDecodeError):
                continue
            if "pkg.crossplane.io/" not in text:
                continue
            if PKG_KIND.search(text) or "kind: DeploymentRuntimeConfig" in text:
                files[str(p.relative_to(root))] = text
    return files


def probe_ports(probe: dict) -> list:
    out = []
    for h in ("httpGet", "tcpSocket", "grpc"):
        if isinstance(probe.get(h), dict) and "port" in probe[h]:
            out.append(probe[h]["port"])
    return out


def check(files: dict[str, str]) -> list[str]:
    errors: list[str] = []
    packages: list[tuple[str, dict]] = []
    configs: dict[tuple[str, str], dict] = {}
    for path, text in sorted(files.items()):
        try:
            docs = [d for d in yaml.safe_load_all(text) if isinstance(d, dict)]
        except yaml.YAMLError as e:
            if PKG_KIND.search(text):
                errors.append(
                    f"R5 {path}: declares a Crossplane Provider or Function but is not plain YAML "
                    f"({e.__class__.__name__}); extend {Path(__file__).name} to render it."
                )
            continue
        for d in docs:
            api = str(d.get("apiVersion", ""))
            if not api.startswith("pkg.crossplane.io/"):
                continue
            kind = d.get("kind")
            if kind in ("Provider", "Function"):
                packages.append((path, d))
            elif kind == "DeploymentRuntimeConfig":
                name = (d.get("metadata") or {}).get("name", "")
                configs[(str(Path(path).parent), name)] = d

    kinds = {d.get("kind") for _, d in packages}
    for want in ("Provider", "Function"):
        if want not in kinds:
            errors.append(f"R6 no {want} found under {', '.join(SCAN_DIRS)}: the scan read nothing to judge.")

    for path, pkg in packages:
        kind = pkg["kind"]
        name = (pkg.get("metadata") or {}).get("name", "?")
        who = f"{kind}/{name} ({path})"
        ref = ((pkg.get("spec") or {}).get("runtimeConfigRef") or {}).get("name")
        if not ref:
            errors.append(f"R1 {who}: no spec.runtimeConfigRef.name; Crossplane would use the runtime config "
                          f"`default`, which has no probes, and probes-present refuses its Deployment.")
            continue
        drc = configs.get((str(Path(path).parent), ref))
        if drc is None:
            errors.append(f"R2 {who}: runtimeConfigRef names `{ref}`, and no DeploymentRuntimeConfig of that "
                          f"name is declared in {Path(path).parent}/.")
            continue
        containers = (((((drc.get("spec") or {}).get("deploymentTemplate") or {}).get("spec") or {})
                       .get("template") or {}).get("spec") or {}).get("containers") or []
        names = [c.get("name") for c in containers if isinstance(c, dict)]
        if RUNTIME_CONTAINER not in names:
            errors.append(f"R3 {who}: runtime config `{ref}` declares no container `{RUNTIME_CONTAINER}`, "
                          f"so the runtime container has no probes.")
        for c in containers:
            cname = c.get("name", "?")
            for field in ("livenessProbe", "readinessProbe"):
                probe = c.get(field)
                if not isinstance(probe, dict):
                    errors.append(f"R3 {who}: runtime config `{ref}`, container `{cname}` has no {field}.")
                    continue
                handlers = [h for h in HANDLERS if h in probe]
                if len(handlers) != 1:
                    errors.append(f"R3 {who}: runtime config `{ref}`, container `{cname}`, {field} has "
                                  f"{len(handlers)} handlers; it needs exactly one of {', '.join(HANDLERS)}.")
                if cname == RUNTIME_CONTAINER:
                    for port in probe_ports(probe):
                        if port not in PORTS[kind]:
                            errors.append(
                                f"R4 {who}: runtime config `{ref}`, {field} probes port {port!r}, which "
                                f"Crossplane does not declare on a {kind} runtime "
                                f"(allowed: {sorted(PORTS[kind], key=str)})."
                            )
    return errors


GOOD_PROVIDER = """\
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata: {name: p}
spec:
  package: example/p:v1
  runtimeConfigRef: {name: p}
---
apiVersion: pkg.crossplane.io/v1beta1
kind: DeploymentRuntimeConfig
metadata: {name: p}
spec:
  deploymentTemplate:
    spec:
      selector: {}
      template:
        spec:
          containers:
            - name: package-runtime
              readinessProbe: {httpGet: {path: /metrics, port: 8080}}
              livenessProbe: {httpGet: {path: /metrics, port: 8080}}
"""
GOOD_FUNCTION = """\
apiVersion: pkg.crossplane.io/v1
kind: Function
metadata: {name: f}
spec:
  package: example/f:v1
  runtimeConfigRef: {name: f}
---
apiVersion: pkg.crossplane.io/v1beta1
kind: DeploymentRuntimeConfig
metadata: {name: f}
spec:
  deploymentTemplate:
    spec:
      selector: {}
      template:
        spec:
          containers:
            - name: package-runtime
              readinessProbe: {tcpSocket: {port: 9443}}
              livenessProbe: {httpGet: {path: /metrics, port: 8080}}
"""


def self_test() -> int:
    base = {"k/p.yaml": GOOD_PROVIDER, "k/f.yaml": GOOD_FUNCTION}
    cases = [
        ("a good Provider and Function pass", base, None),
        ("R1 a Provider without runtimeConfigRef fails",
         {**base, "k/p.yaml": GOOD_PROVIDER.replace("  runtimeConfigRef: {name: p}\n", "")}, "R1"),
        ("R2 a runtimeConfigRef to an undeclared config fails",
         {**base, "k/p.yaml": GOOD_PROVIDER.replace("runtimeConfigRef: {name: p}", "runtimeConfigRef: {name: q}")}, "R2"),
        ("R2 a config of that name in another directory does not count",
         {"k/p.yaml": GOOD_PROVIDER.split("---\n")[0], "other/c.yaml": GOOD_PROVIDER.split("---\n")[1],
          "k/f.yaml": GOOD_FUNCTION}, "R2"),
        ("R3 a runtime container without livenessProbe fails",
         {**base, "k/p.yaml": GOOD_PROVIDER.replace("              livenessProbe: {httpGet: {path: /metrics, port: 8080}}\n", "")}, "R3"),
        ("R3 a runtime container without readinessProbe fails",
         {**base, "k/f.yaml": GOOD_FUNCTION.replace("              readinessProbe: {tcpSocket: {port: 9443}}\n", "")}, "R3"),
        ("R3 a config without the package-runtime container fails",
         {**base, "k/p.yaml": GOOD_PROVIDER.replace("name: package-runtime", "name: sidecar")}, "R3"),
        ("R3 a probe with no handler fails",
         {**base, "k/p.yaml": GOOD_PROVIDER.replace("readinessProbe: {httpGet: {path: /metrics, port: 8080}}",
                                                     "readinessProbe: {periodSeconds: 10}")}, "R3"),
        ("R4 a provider probe on a port the runtime does not declare fails",
         {**base, "k/p.yaml": GOOD_PROVIDER.replace("readinessProbe: {httpGet: {path: /metrics, port: 8080}}",
                                                     "readinessProbe: {httpGet: {path: /healthz, port: 8081}}")}, "R4"),
        ("R4 a provider probe on the function-only gRPC port fails",
         {**base, "k/p.yaml": GOOD_PROVIDER.replace("readinessProbe: {httpGet: {path: /metrics, port: 8080}}",
                                                     "readinessProbe: {tcpSocket: {port: 9443}}")}, "R4"),
        ("R5 a Provider in a chart template is refused, not skipped",
         {**base, "c/templates/p.yaml": "{{- if .Values.x }}\n" + GOOD_PROVIDER + "{{- end }}\n"}, "R5"),
        ("R6 a scan with no Function fails", {"k/p.yaml": GOOD_PROVIDER}, "R6"),
        ("R6 a scan with nothing fails", {}, "R6"),
    ]
    failed = 0
    for name, files, want in cases:
        errs = check(files)
        ok = (not errs) if want is None else any(e.startswith(want + " ") for e in errs)
        print(f"self-test {'ok  ' if ok else 'FAIL'}: {name}")
        if not ok:
            failed += 1
            for e in errs:
                print(f"    got: {e}")
    # The real tree is read too, so a broken collect() cannot hide behind fixtures.
    real = collect(ROOT)
    if not any(PKG_KIND.search(t) for t in real.values()):
        print("self-test FAIL: collect() found no Provider or Function in the repository")
        failed += 1
    else:
        print(f"self-test ok  : collect() reads {len(real)} file(s) of the repository")
    print(f"self-test: {len(cases) + 1 - failed} of {len(cases) + 1} cases passed")
    return 1 if failed else 0


def main(argv: list[str]) -> int:
    if argv == ["--self-test"]:
        return self_test()
    if argv:
        print("usage: python3 scripts/check-crossplane-runtime-probes.py [--self-test]", file=sys.stderr)
        return 2
    files = collect(ROOT)
    errors = check(files)
    for e in errors:
        print(f"::error title=crossplane-runtime-probes::{e}")
    if errors:
        print(f"FAIL: {len(errors)} finding(s) in {len(files)} file(s).")
        return 1
    n = sum(len(PKG_KIND.findall(t)) for t in files.values())
    print(f"PASS: {n} Crossplane package(s) in {len(files)} file(s) run with liveness and readiness probes "
          f"on a port their runtime serves.")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
