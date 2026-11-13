"""CLD-055 spike: CPU and memory REQUESTS per namespace (the scheduler's view).

Reads `kubectl get pods -A -o json` on stdin. A pod's request is the larger of
the sum over its containers and the largest init container. Prints one line
per namespace, heaviest CPU first:
  <cpu_m_scheduled> <cpu_m_pending> <mem_mi_scheduled> <mem_mi_pending> <namespace>
and a TOTAL line. Finished pods (Succeeded/Failed) are left out.
"""
import json
import sys


def cpu_m(v):
    if not v:
        return 0
    v = str(v)
    if v.endswith("m"):
        return int(float(v[:-1]))
    return int(float(v) * 1000)


def mem_mi(v):
    if not v:
        return 0
    v = str(v)
    units = {"Ki": 1 / 1024, "Mi": 1, "Gi": 1024, "Ti": 1024 * 1024, "k": 1 / 1048.576, "M": 1 / 1.048576, "G": 1000 / 1.048576}
    for u, f in units.items():
        if v.endswith(u):
            return int(float(v[: -len(u)]) * f)
    return int(float(v) / 1048576)


def req(c, key):
    return ((c.get("resources") or {}).get("requests") or {}).get(key)


rows = {}
for p in json.load(sys.stdin)["items"]:
    phase = p.get("status", {}).get("phase")
    if phase in ("Succeeded", "Failed"):
        continue
    spec = p["spec"]
    cpu = sum(cpu_m(req(c, "cpu")) for c in spec.get("containers", []))
    mem = sum(mem_mi(req(c, "memory")) for c in spec.get("containers", []))
    for c in spec.get("initContainers", []) or []:
        cpu = max(cpu, cpu_m(req(c, "cpu")))
        mem = max(mem, mem_mi(req(c, "memory")))
    scheduled = bool(spec.get("nodeName"))
    r = rows.setdefault(p["metadata"]["namespace"], [0, 0, 0, 0])
    if scheduled:
        r[0] += cpu
        r[2] += mem
    else:
        r[1] += cpu
        r[3] += mem

tot = [sum(r[i] for r in rows.values()) for i in range(4)]
for ns, r in sorted(rows.items(), key=lambda kv: -(kv[1][0] + kv[1][1])):
    print(r[0], r[1], r[2], r[3], ns)
print(tot[0], tot[1], tot[2], tot[3], "TOTAL")
