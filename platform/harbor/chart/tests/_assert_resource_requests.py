#!/usr/bin/env python3
# #4343 helper — read a multi-doc helm render from stdin and assert every
# workload container (Deployment/StatefulSet/DaemonSet/Job/CronJob, incl.
# initContainers) declares resources.requests.cpu AND resources.requests.memory
# (the exact shape the Kyverno `resource-requests` Enforce ClusterPolicy
# requires). Prints a one-line OK summary on success; prints the offending
# kind/name:container lines and exits 1 on any miss.
import sys, re
try:
    import yaml
except ImportError:
    print("PyYAML not available; cannot run resource-requests assertion", file=sys.stderr)
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
    if kind not in ("Deployment", "StatefulSet", "DaemonSet", "Job", "CronJob"):
        continue
    name = d.get("metadata", {}).get("name", "<noname>")
    try:
        if kind == "CronJob":
            podspec = d["spec"]["jobTemplate"]["spec"]["template"]["spec"]
        else:
            podspec = d["spec"]["template"]["spec"]
    except (KeyError, TypeError):
        continue
    containers = list(podspec.get("containers", []) or []) + list(podspec.get("initContainers", []) or [])
    for c in containers:
        checked += 1
        req = (c.get("resources") or {}).get("requests") or {}
        if "cpu" not in req or "memory" not in req:
            bad.append("%s/%s container=%s requests=%s" % (kind, name, c.get("name"), dict(req)))

if bad:
    print("workload containers MISSING cpu+memory requests:")
    for b in bad:
        print(b)
    sys.exit(1)

if checked == 0:
    print("no workload containers rendered — subchart deps not built? (run `helm dependency build`)")
    sys.exit(1)

print("%d workload container(s) all declare cpu+memory requests" % checked)
