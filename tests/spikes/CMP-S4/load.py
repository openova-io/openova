#!/usr/bin/env python3
"""Spike CMP-S4: raise the report population to a target count and size.

Templates are the REAL report objects the scanner and the policy engine wrote
earlier in this run (label cmps4-clone absent). Each clone keeps the report
body byte for byte and gets a new name, a namespace of its own, the label
cmps4-clone, and no owner reference (so garbage collection and the engines
leave it alone). The policy engine's managed-by label is removed for the same
reason.

Order: whole rounds of the real mix until either target is reached; then the
largest quarter of the templates until the byte target is reached; then the
smallest half until the object target is reached. The script aims about 3 %
over each target from its own size estimate; etcd_stats.py, run after it,
gives the real numbers. Ephemeral reports and compliance reports are not
cloned (see below).
"""
import argparse
import json
import queue
import re
import threading
import time

from s4lib import Api, item_path, list_all, list_path, report_resources

DROP_LABELS = ("app.kubernetes.io/managed-by",)


def dns(s):
    s = re.sub(r"[^a-z0-9.-]", "-", s.lower())[:240].strip("-.")
    return s or "x"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--kubeconfig", required=True)
    ap.add_argument("--tag", required=True)
    ap.add_argument("--cur-objects", type=int, required=True)
    ap.add_argument("--cur-bytes", type=int, required=True)
    ap.add_argument("--target-objects", type=int, required=True)
    ap.add_argument("--target-bytes", type=int, required=True)
    ap.add_argument("--threads", type=int, default=8)
    ap.add_argument("--rounds-per-namespace", type=int, default=10)
    ap.add_argument("--out", required=True)
    a = ap.parse_args()
    api = Api(a.kubeconfig)

    templates = []  # (g, v, plural, namespaced, srcNs, body-without-metadata, md, est)
    for (g, v, plural, namespaced, kind) in report_resources(api):
        # ephemeral reports are consumed and deleted by the engine's reports
        # controller; a compliance report carries a cron the scanner acts on
        if g == "reports.kyverno.io" or plural == "clustercompliancereports":
            continue
        for o in list_all(api, list_path(g, v, plural), label_selector="!cmps4-clone"):
            md = o.get("metadata", {})
            body = {k: val for k, val in o.items() if k not in ("metadata", "status")}
            est = len(json.dumps(o, separators=(",", ":")))
            templates.append((g, v, plural, namespaced, md.get("namespace", ""), body, md, est))
    if not templates:
        raise SystemExit("no report objects to clone")
    by_size = sorted(templates, key=lambda t: -t[7])
    big = by_size[:max(1, len(by_size) // 4)]
    small = by_size[len(by_size) // 2:] or by_size

    need_o = max(0, int(a.target_objects * 1.03) - a.cur_objects)
    need_b = max(0, int(a.target_bytes * 1.03) - a.cur_bytes)
    plan, acc_o, acc_b, rnd = [], 0, 0, 0

    def add(ts):
        nonlocal acc_o, acc_b, rnd
        rnd += 1
        for t in ts:
            plan.append((rnd, t))
            acc_o += 1
            acc_b += t[7]

    while acc_o < need_o and acc_b < need_b:
        add(templates)
    while acc_b < need_b:
        add(big)
    while acc_o < need_o:
        add(small)

    nss = sorted({f"s4-load-{a.tag}-{(r - 1) // a.rounds_per_namespace}" for r, t in plan if t[3]})
    for ns in nss:
        api.ensure_namespace(ns, {"cmps4-clone": a.tag})

    q = queue.Queue()
    for r, t in plan:
        g, v, plural, namespaced, src_ns, body, md, est = t
        labels = {k: val for k, val in (md.get("labels") or {}).items() if k not in DROP_LABELS}
        labels["cmps4-clone"] = a.tag
        ann = {k: val for k, val in (md.get("annotations") or {}).items()
               if not k.startswith("kubectl.kubernetes.io/")}
        new_md = {"labels": labels, "annotations": ann}
        if namespaced:
            ns = f"s4-load-{a.tag}-{(r - 1) // a.rounds_per_namespace}"
            new_md["name"] = dns(f"r{r}-{src_ns}-{md['name']}")
            new_md["namespace"] = ns
        else:
            ns = ""
            new_md["name"] = dns(f"s4-{a.tag}-r{r}-{md['name']}")
        obj = dict(body)
        obj["metadata"] = new_md
        q.put((item_path(g, v, plural, namespaced, ns), json.dumps(obj, separators=(",", ":")).encode(), plural, est))

    lock = threading.Lock()
    done = {"created": 0, "conflict": 0, "failed": 0, "estBytes": 0, "perResource": {}, "errors": []}

    def worker():
        c = api.conn()
        while True:
            try:
                path, data, plural, est = q.get_nowait()
            except queue.Empty:
                c.close()
                return
            st, raw = -1, b""
            for attempt in range(3):
                try:
                    st, raw = api.req("POST", path, data, conn=c)
                    break
                except Exception as e:
                    raw = repr(e).encode()
                    c.close()
                    c = api.conn()
            with lock:
                if st == 201:
                    done["created"] += 1
                    done["estBytes"] += est
                    done["perResource"][plural] = done["perResource"].get(plural, 0) + 1
                elif st == 409:
                    done["conflict"] += 1
                else:
                    done["failed"] += 1
                    if len(done["errors"]) < 10:
                        done["errors"].append(f"HTTP {st}: {raw[:300]!r}")

    t0 = time.time()
    ths = [threading.Thread(target=worker) for _ in range(a.threads)]
    for th in ths:
        th.start()
    for th in ths:
        th.join()
    dt = time.time() - t0
    res = {"tag": a.tag, "templates": len(templates), "rounds": rnd, "namespaces": len(nss),
           "planned": len(plan), "plannedEstBytes": acc_b, "seconds": round(dt, 1),
           "objectsPerSecond": round(done["created"] / dt, 1) if dt else 0, **done}
    with open(a.out, "w") as f:
        json.dump(res, f, indent=1, sort_keys=True)
    print("== load " + json.dumps(res, sort_keys=True))


if __name__ == "__main__":
    main()
