#!/usr/bin/env python3
"""INF-030 spike monitor.

Every 5 seconds: reads all Node objects and the kured DaemonSet from the first
API server that answers, and every 15 seconds the etcd endpoint health. Writes

  timeline.tsv     one line per poll
  events.log       one line per change (boot ID, Ready, cordon, lock holder, kubelet version)
  monitor.json     running maxima and minima, rewritten on every poll

until the file monitor.stop appears in $OUT.
"""
import json
import os
import subprocess
import time
from datetime import datetime, timezone

OUT = os.environ["OUT"]
WORK = os.environ["WORK"]
KCFG = os.environ["KCFG"]
IPS = os.environ["SERVER_IPS"].split()
LOCK_ANN = "weave.works/kured-node-lock"
STOP = os.path.join(OUT, "monitor.stop")


def now():
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def kubectl(args):
    for ip in IPS:
        try:
            r = subprocess.run(
                ["kubectl", "--kubeconfig", KCFG, "--server", f"https://{ip}:6443",
                 "--request-timeout=4s"] + args,
                capture_output=True, text=True, timeout=8)
        except subprocess.TimeoutExpired:
            continue
        if r.returncode == 0:
            return r.stdout, ip
    return None, None


def etcd_healthy():
    eps = ",".join(f"https://{ip}:2379" for ip in IPS)
    try:
        r = subprocess.run(
            ["etcdctl", f"--endpoints={eps}", "--cacert", f"{WORK}/etcd-ca.crt",
             "--cert", f"{WORK}/etcd-client.crt", "--key", f"{WORK}/etcd-client.key",
             "--dial-timeout=2s", "--command-timeout=3s", "endpoint", "health", "-w", "json"],
            capture_output=True, text=True, timeout=10, env=dict(os.environ, ETCDCTL_API="3"))
        data = json.loads(r.stdout or "[]")
        return sum(1 for e in data if e.get("health") is True)
    except Exception:
        return -1


def main():
    tl = open(os.path.join(OUT, "timeline.tsv"), "a", buffering=1)
    ev = open(os.path.join(OUT, "events.log"), "a", buffering=1)
    tl.write("time\tapi\tnodes(name:boot:ready:cordoned:version)\tlock_holder\tunavailable\tetcd_healthy\n")
    prev = {}
    prev_lock = None
    stats = {"polls": 0, "api_unreachable_polls": 0, "max_unavailable": 0, "max_cordoned": 0,
             "max_not_ready": 0, "min_etcd_healthy": None, "lock_values": [], "node_annotations": {},
             "first_poll": now()}
    last_etcd = 0
    etcd = -1
    while not os.path.exists(STOP):
        t0 = time.time()
        stats["polls"] += 1
        if t0 - last_etcd >= 15:
            etcd = etcd_healthy()
            last_etcd = t0
            if etcd >= 0:
                m = stats["min_etcd_healthy"]
                stats["min_etcd_healthy"] = etcd if m is None else min(m, etcd)
        out, ip = kubectl(["get", "nodes", "-o", "json"])
        if out is None:
            stats["api_unreachable_polls"] += 1
            tl.write(f"{now()}\t-\tAPI unreachable\t-\t-\t{etcd}\n")
            ev.write(f"{now()} API unreachable on all three servers\n")
            time.sleep(5)
            continue
        nodes = json.loads(out)["items"]
        cells, unavailable, cordoned, not_ready = [], 0, 0, 0
        for n in sorted(nodes, key=lambda x: x["metadata"]["name"]):
            name = n["metadata"]["name"]
            boot = n["status"]["nodeInfo"]["bootID"]
            ver = n["status"]["nodeInfo"]["kubeletVersion"]
            ready = next((c["status"] for c in n["status"]["conditions"] if c["type"] == "Ready"), "?")
            unsched = bool(n["spec"].get("unschedulable", False))
            ann = {k: v for k, v in n["metadata"].get("annotations", {}).items() if k.startswith("weave.works/")}
            if ann:
                stats["node_annotations"].setdefault(name, [])
                for k, v in ann.items():
                    item = f"{k}={v}"
                    if item not in stats["node_annotations"][name]:
                        stats["node_annotations"][name].append(item)
            if ready != "True":
                not_ready += 1
            if unsched:
                cordoned += 1
            if ready != "True" or unsched:
                unavailable += 1
            cur = {"boot": boot, "ready": ready, "unsched": unsched, "ver": ver}
            p = prev.get(name)
            if p is None:
                ev.write(f"{now()} {name} initial boot={boot} ready={ready} cordoned={unsched} version={ver}\n")
            else:
                if p["boot"] != boot:
                    ev.write(f"{now()} {name} BOOT_ID_CHANGED {p['boot']} -> {boot}\n")
                if p["ready"] != ready:
                    ev.write(f"{now()} {name} READY {p['ready']} -> {ready}\n")
                if p["unsched"] != unsched:
                    ev.write(f"{now()} {name} {'CORDONED' if unsched else 'UNCORDONED'}\n")
                if p["ver"] != ver:
                    ev.write(f"{now()} {name} KUBELET_VERSION {p['ver']} -> {ver}\n")
            prev[name] = cur
            cells.append(f"{name}:{boot[:8]}:{ready}:{int(unsched)}:{ver}")
        lock_holder = "-"
        dsout, _ = kubectl(["-n", "kube-system", "get", "ds", "kured", "-o", "json"])
        if dsout:
            val = json.loads(dsout)["metadata"].get("annotations", {}).get(LOCK_ANN, "")
            if val:
                try:
                    lock_holder = json.loads(val).get("nodeID") or "-"
                except ValueError:
                    lock_holder = "?"
                if val not in stats["lock_values"] and len(stats["lock_values"]) < 20:
                    stats["lock_values"].append(val)
            if lock_holder != prev_lock:
                ev.write(f"{now()} LOCK holder {prev_lock} -> {lock_holder} (annotation {LOCK_ANN}={val!r})\n")
                prev_lock = lock_holder
        stats["max_unavailable"] = max(stats["max_unavailable"], unavailable)
        stats["max_cordoned"] = max(stats["max_cordoned"], cordoned)
        stats["max_not_ready"] = max(stats["max_not_ready"], not_ready)
        stats["last_poll"] = now()
        tl.write(f"{now()}\t{ip}\t{' '.join(cells)}\t{lock_holder}\t{unavailable}\t{etcd}\n")
        with open(os.path.join(OUT, "monitor.json.tmp"), "w") as f:
            json.dump(stats, f, indent=1)
        os.replace(os.path.join(OUT, "monitor.json.tmp"), os.path.join(OUT, "monitor.json"))
        time.sleep(max(0.5, 5 - (time.time() - t0)))


if __name__ == "__main__":
    main()
