#!/usr/bin/env python3
# #4347 helper — read a multi-doc helm render from stdin and assert every
# PRIMARY container in a Deployment / StatefulSet / DaemonSet declares BOTH a
# livenessProbe AND a readinessProbe — the exact shape the cluster-wide Kyverno
# `probes-present` Enforce ClusterPolicy requires (platform/kyverno-policies/
# chart/templates/baseline/04-probes.yaml).
#
# Scope mirrors the policy EXACTLY:
#   - Matched kinds: Deployment, StatefulSet, DaemonSet ONLY. Job / CronJob are
#     NOT matched by probes-present (a Job's containers run to completion;
#     liveness/readiness are meaningless there), so they are skipped here too.
#   - initContainers + ephemeralContainers are EXEMPT (the K8s API rejects
#     liveness/readiness probes on initContainers), so only `containers[]` is
#     checked.
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
    # Match the probes-present policy kinds exactly — NOT Job/CronJob.
    if kind not in ("Deployment", "StatefulSet", "DaemonSet"):
        continue
    name = d.get("metadata", {}).get("name", "<noname>")
    try:
        podspec = d["spec"]["template"]["spec"]
    except (KeyError, TypeError):
        continue
    # Only the primary containers[] are subject to the policy.
    for c in (podspec.get("containers", []) or []):
        checked += 1
        missing = []
        if not c.get("livenessProbe"):
            missing.append("livenessProbe")
        if not c.get("readinessProbe"):
            missing.append("readinessProbe")
        if missing:
            bad.append("%s/%s container=%s missing=%s" % (kind, name, c.get("name"), "+".join(missing)))

if bad:
    print("workload containers MISSING liveness/readiness probes:")
    for b in bad:
        print(b)
    sys.exit(1)

if checked == 0:
    print("no Deployment/StatefulSet/DaemonSet containers rendered — subchart deps not built? (run `helm dependency build`)")
    sys.exit(1)

print("%d Deployment/StatefulSet/DaemonSet container(s) all declare liveness+readiness probes" % checked)
