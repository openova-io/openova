#!/usr/bin/env python3
# #4347 helper — read a multi-doc helm render from stdin and assert every
# PRIMARY container of a Deployment / StatefulSet / DaemonSet declares BOTH a
# livenessProbe AND a readinessProbe (the exact shape the Kyverno
# `probes-present` Enforce ClusterPolicy requires). initContainers are EXEMPT
# (the K8s API rejects probes on them, and the policy excludes them). Job /
# CronJob are NOT matched by the policy and are skipped here.
#
# Prints a one-line OK summary on success; prints the offending
# kind/name:container lines and exits 1 on any miss.
import sys, re
try:
    import yaml
except ImportError:
    print("PyYAML not available; cannot run probes-present assertion", file=sys.stderr)
    sys.exit(2)

raw = sys.stdin.read()
# Some upstream subchart templates emit stray trailing whitespace/tabs that
# break a strict YAML scan; rstrip each line (cosmetic, render-neutral).
raw = "\n".join(line.rstrip() for line in raw.splitlines())

bad = []
checked = 0
for chunk in re.split(r"(?m)^---\s*$", raw):
    chunk = chunk.strip()
    if not chunk:
        continue
    try:
        d = yaml.safe_load(chunk)
    except Exception:
        continue
    if not isinstance(d, dict):
        continue
    kind = d.get("kind")
    if kind not in ("Deployment", "StatefulSet", "DaemonSet"):
        continue
    name = d.get("metadata", {}).get("name", "<noname>")
    try:
        podspec = d["spec"]["template"]["spec"]
    except (KeyError, TypeError):
        continue
    # initContainers are EXEMPT per the probes-present policy.
    for c in list(podspec.get("containers", []) or []):
        checked += 1
        has_live = bool(c.get("livenessProbe"))
        has_ready = bool(c.get("readinessProbe"))
        if not (has_live and has_ready):
            miss = []
            if not has_live:
                miss.append("livenessProbe")
            if not has_ready:
                miss.append("readinessProbe")
            bad.append("%s/%s container=%s MISSING %s"
                       % (kind, name, c.get("name"), "+".join(miss)))

if bad:
    print("workload containers MISSING liveness/readiness probes:")
    for b in bad:
        print(b)
    sys.exit(1)

if checked == 0:
    print("no workload containers rendered — subchart deps not built? (run `helm dependency build`)")
    sys.exit(1)

print("%d workload container(s) all declare livenessProbe + readinessProbe" % checked)
