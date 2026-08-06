#!/usr/bin/env python3
"""check-lb-nodeport0.py — every LoadBalancer Service template must state nodePort:0.

§854 / #5348 / #5690: a `type: LoadBalancer` Service that OMITS an explicit
`nodePort` renders perfectly clean but the apiserver silently auto-allocates a
NodePort per port at apply time (30000-32767). `check-no-nodeports.sh` catches
nonzero `nodePort:` LITERALS and `type: NodePort` — but it structurally CANNOT
catch this class, because the defect is an ABSENCE: there is no forbidden token
to match. #5690 (bp-stalwart-sovereign mail, 5 unguarded LB ports) lived behind
a clean render for exactly this reason and was found only by a manual audit.
This guard is the durable version of that audit.

For every chart Service template that can render `type: LoadBalancer` — a literal
`type: LoadBalancer`, or `type: {{ .Values.<path>.type }}` whose values.yaml
default resolves to LoadBalancer (a `| default "ClusterIP"` or a ClusterIP
values default is NOT LB-capable) — this asserts the template carries BOTH the
established §854 guard tokens:
  - `allocateLoadBalancerNodePorts: false` on the spec, and
  - `nodePort: 0` on the ports (the release sentinel the apiserver clears on apply).

Source-based (no helm needed → deterministic, offline). Self-test proves it
CATCHES a synthetic unguarded LB template (discrimination) and passes on nothing
only when there genuinely are no LB services (vacuity floor).

Run:  scripts/check-lb-nodeport0.py [--self-test]
Exit: 0 clean · 1 an unguarded LoadBalancer Service · 2 vacuity/setup error
"""

import glob
import os
import pathlib
import re
import sys

import yaml

ROOT = pathlib.Path(os.environ.get("ROOT", "."))
GLOBS = [
    "platform/*/chart/templates/**/*.yaml",
    "products/*/chart/templates/**/*.yaml",
    "products/*/charts/*/templates/**/*.yaml",
]
# There are ~4 LB Service templates today (powerdns-anycast, stalwart
# sovereign-mail, stalwart-tenant smtp/imap). A floor well under that so
# ordinary growth never trips it, but a glob that reaches nothing does.
VACUITY_FLOOR = 3

_TYPE_RE = re.compile(r"(?m)^\s*type:\s*(.+?)\s*$")
_VALUES_PATH_RE = re.compile(r"\.Values\.([A-Za-z0-9_.]+?)\.type\b")
_DEFAULT_RE = re.compile(r'\|\s*default\s+"([A-Za-z]+)"')
# `{{- $svcType := .Values.service.mail.type | default "LoadBalancer" }}` — the
# established §854 idiom (stalwart-tenant/sovereign, powerdns-anycast) resolves
# the type through a local var, so the type line reads `type: {{ $svcType }}`.
_ASSIGN_RE = re.compile(r"\{\{-?\s*\$(\w+)\s*:=\s*(.+?)\s*-?\}\}")


def _is_service(text: str) -> bool:
    return re.search(r"(?m)^\s*kind:\s*Service\s*$", text) is not None


def _values_default(chart_dir: pathlib.Path, dotted: str) -> str | None:
    """Resolve <dotted>.type default from the chart's values.yaml, or None."""
    vpath = chart_dir / "values.yaml"
    if not vpath.exists():
        return None
    try:
        data = yaml.safe_load(vpath.read_text(errors="replace")) or {}
    except yaml.YAMLError:
        return None
    node = data
    for key in dotted.split("."):
        if not isinstance(node, dict) or key not in node:
            return None
        node = node[key]
    node = node.get("type") if isinstance(node, dict) else None
    return node if isinstance(node, str) else None


def _expr_is_lb(expr: str, chart_dir: pathlib.Path) -> bool:
    """Does a `.Values.<path>.type [| default "X"]` expression default to LB?

    values.yaml wins (that's the actual default); the inline `| default "X"`
    only applies when values.yaml leaves the key unset.
    """
    pm = _VALUES_PATH_RE.search(expr)
    dm = _DEFAULT_RE.search(expr)
    if pm:
        v = _values_default(chart_dir, pm.group(1))
        if v is not None:
            return v == "LoadBalancer"
        return bool(dm) and dm.group(1) == "LoadBalancer"
    return bool(dm) and dm.group(1) == "LoadBalancer"


def lb_capable(text: str, chart_dir: pathlib.Path) -> bool:
    """True if this Service template can render type: LoadBalancer by default."""
    assigns = {name: expr for name, expr in _ASSIGN_RE.findall(text)}
    for m in _TYPE_RE.finditer(text):
        raw = m.group(1).strip()
        if raw == "LoadBalancer":
            return True
        if raw.startswith("{{"):
            inner = raw.strip("{}").strip().lstrip("-").strip().rstrip("-").strip()
            vm = re.match(r"\$(\w+)", inner)
            if vm:
                # type: {{ $svcType }} — resolve the local-var assignment.
                if vm.group(1) in assigns and _expr_is_lb(assigns[vm.group(1)], chart_dir):
                    return True
                continue
            if _expr_is_lb(inner, chart_dir):
                return True
    return False


# A port entry in a Service `ports:` list. Helm templates write these as
# `- port: 25` / `- name: smtp`, one list item per exposed port.
_PORT_ITEM_RE = re.compile(r"(?m)^\s*-\s+(?:name|port|targetPort)\s*:")
_NP0_RE = re.compile(r"(?m)^\s*nodePort:\s*0\s*$")


def port_items(text: str) -> int:
    """Count `ports:` list entries. Deliberately counts the FIRST key of each
    list item only, so a 3-key port block still counts as one port."""
    n = 0
    in_ports = False
    ports_indent = -1
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        indent = len(line) - len(line.lstrip())
        if re.match(r"^ports\s*:", stripped):
            in_ports, ports_indent = True, indent
            continue
        if in_ports:
            # a key at or left of `ports:` indentation ends the block
            if indent <= ports_indent and not stripped.startswith("-"):
                in_ports = False
                continue
            if stripped.startswith("- "):
                n += 1
    return n


def guarded(text: str) -> tuple[bool, str]:
    """True if the template carries the §854 LB guards on EVERY port.

    #5690 follow-up: this previously asked only whether `nodePort: 0` appeared
    ANYWHERE in the file — a boolean presence check. A Service exposing five
    ports with the sentinel on ONE of them therefore passed, which is the very
    shape #5690 reported (bp-stalwart-sovereign mail, 5 unguarded ports).
    Removing four of five sentinels left this check green.

    `allocateLoadBalancerNodePorts: false` alone is not sufficient (#5348): it
    stops NEW allocations, but an apply that merely OMITS `nodePort` leaves an
    existing allocation in place. The per-port `nodePort: 0` is the sentinel the
    apiserver actually clears, so it must be present once per port.
    """
    if "allocateLoadBalancerNodePorts: false" not in text:
        return False, "missing `allocateLoadBalancerNodePorts: false`"
    ports = port_items(text)
    np0 = len(_NP0_RE.findall(text))
    if np0 == 0:
        return False, "no `nodePort: 0` on any port"
    if ports and np0 < ports:
        return False, f"{np0} `nodePort: 0` for {ports} port(s) — {ports - np0} port(s) unguarded"
    return True, ""


def scan():
    seen = set()
    for g in GLOBS:
        seen.update(glob.glob(str(ROOT / g), recursive=True))
    lb, unguarded = [], []
    for f in sorted(seen):
        text = pathlib.Path(f).read_text(errors="replace")
        if not _is_service(text):
            continue
        chart_dir = pathlib.Path(f)
        while chart_dir.name != "chart" and chart_dir.parent != chart_dir:
            chart_dir = chart_dir.parent
        if not lb_capable(text, chart_dir):
            continue
        lb.append(f)
        ok, why = guarded(text)
        if not ok:
            unguarded.append((f, why))
    return lb, unguarded


def main() -> int:
    lb, unguarded = scan()
    rel = lambda p: os.path.relpath(p, ROOT)  # noqa: E731

    if len(lb) < VACUITY_FLOOR:
        print(
            f"GUARD VACUOUS: found only {len(lb)} LoadBalancer Service template(s) "
            f"(expected >= {VACUITY_FLOOR}). A templates glob was renamed/moved, so "
            f"a 'clean' result proves nothing.",
            file=sys.stderr,
        )
        return 2

    if unguarded:
        print("FAIL: LoadBalancer Service template(s) omit the §854 nodePort:0 guard", file=sys.stderr)
        for f, why in unguarded:
            print(f"  - {rel(f)}: {why}", file=sys.stderr)
        print(
            "\nA LoadBalancer Service without `allocateLoadBalancerNodePorts: false`\n"
            "+ explicit `nodePort: 0` auto-allocates a NodePort per port on apply\n"
            "(§854/#5348/#5690). Add both, mirroring platform/stalwart-tenant/chart/\n"
            "templates/service-smtp.yaml.",
            file=sys.stderr,
        )
        return 1

    print(f"OK: all {len(lb)} LoadBalancer Service template(s) carry the §854 nodePort:0 guard.")
    for f in lb:
        print(f"     ✓ {rel(f)}")
    return 0


# ── self-test: prove the guard CATCHES an unguarded LB (discrimination) ──────
def self_test() -> int:
    fails = []
    guarded_tpl = (
        "kind: Service\nspec:\n  type: LoadBalancer\n"
        "  allocateLoadBalancerNodePorts: false\n  ports:\n    - name: a\n      nodePort: 0\n"
    )
    unguarded_tpl = "kind: Service\nspec:\n  type: LoadBalancer\n  ports:\n    - name: a\n      port: 25\n"
    clusterip_tpl = 'kind: Service\nspec:\n  type: {{ .Values.svc.type | default "ClusterIP" }}\n'

    tmp = pathlib.Path(os.environ.get("ROOT", "."))  # chart_dir unused for literals
    if not lb_capable(guarded_tpl, tmp):
        fails.append("literal LoadBalancer not detected as LB-capable")
    if not guarded(guarded_tpl)[0]:
        fails.append("guarded template not recognised as guarded")
    if guarded(unguarded_tpl)[0]:
        fails.append("DISCRIMINATION: unguarded LB template passed as guarded")

    # #5690 follow-up — the discrimination case the ORIGINAL guard missed:
    # a multi-port LB with the sentinel on SOME ports. The old boolean
    # presence check passed this; it is the exact partial shape of #5690.
    partial_tpl = (
        "kind: Service\nspec:\n  type: LoadBalancer\n"
        "  allocateLoadBalancerNodePorts: false\n  ports:\n"
        "    - name: a\n      port: 25\n      nodePort: 0\n"
        "    - name: b\n      port: 587\n"
        "    - name: c\n      port: 993\n"
    )
    ok_partial, why_partial = guarded(partial_tpl)
    if ok_partial:
        fails.append("DISCRIMINATION: 1-of-3 guarded ports passed as fully guarded (#5690 partial shape)")
    elif "unguarded" not in why_partial:
        fails.append(f"partial-guard reason unhelpful: {why_partial!r}")

    # And the inverse must stay green: a fully-guarded multi-port LB.
    full_tpl = partial_tpl.replace(
        "    - name: b\n      port: 587\n", "    - name: b\n      port: 587\n      nodePort: 0\n"
    ).replace(
        "    - name: c\n      port: 993\n", "    - name: c\n      port: 993\n      nodePort: 0\n"
    )
    if not guarded(full_tpl)[0]:
        fails.append("fully-guarded 3-port LB wrongly flagged unguarded")
    if not lb_capable(unguarded_tpl, tmp):
        fails.append("unguarded literal LB not detected as LB-capable")
    if lb_capable(clusterip_tpl, tmp):
        fails.append("`| default \"ClusterIP\"` wrongly flagged as LB-capable")

    # And the live tree must pass (all current LB services are guarded post-#5690).
    lb, unguarded = scan()
    if unguarded:
        fails.append(f"live tree has unguarded LB services: {unguarded}")
    if len(lb) < VACUITY_FLOOR:
        fails.append(f"live tree LB count {len(lb)} < floor {VACUITY_FLOOR}")

    if fails:
        print("check-lb-nodeport0 self-test: FAIL", file=sys.stderr)
        for f in fails:
            print(f"  - {f}", file=sys.stderr)
        return 1
    print(f"check-lb-nodeport0 self-test: PASS ({len(lb)} LB services, all guarded; discrimination OK)")
    return 0


if __name__ == "__main__":
    if "--self-test" in sys.argv[1:]:
        sys.exit(self_test())
    sys.exit(main())
