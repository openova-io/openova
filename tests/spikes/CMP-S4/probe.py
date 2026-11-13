#!/usr/bin/env python3
"""Spike CMP-S4: API-server watch latency, list latency, and server metrics.

Three probes, each against the live API server of the k3s node in the CI job:

  cm      a watch on ConfigMaps of one namespace; a writer creates N objects
          at a fixed rate; for each object: ack = time the create returned,
          recv = time the watch event arrived. Lag = recv - ack; end-to-end =
          recv - (time just before the create was sent).
  report  a cluster-wide watch on one report kind (as an informer holds it);
          a writer changes a label on one real report object N times at a
          fixed rate; same timings, plus the event size.
  list    for every report kind: a full list served from the watch cache
          (resourceVersion=0, what an informer does at start) and a paginated
          consistent list (limit=500, served from etcd); time and bytes.

Also reads /metrics of the API server (k3s runs API server and etcd in one
process, so process_resident_memory_bytes is both together).
"""
import argparse
import json
import socket
import statistics
import threading
import time
import urllib.parse

from s4lib import Api, list_path, report_resources, item_path


def stats(xs):
    if not xs:
        return {"n": 0}
    s = sorted(xs)

    def q(p):
        return s[min(len(s) - 1, int(p * (len(s) - 1) + 0.5))]

    return {"n": len(s), "p50_ms": round(q(0.5) * 1000, 2), "p95_ms": round(q(0.95) * 1000, 2),
            "p99_ms": round(q(0.99) * 1000, 2), "max_ms": round(s[-1] * 1000, 2),
            "mean_ms": round(statistics.mean(s) * 1000, 2)}


class Watch(threading.Thread):
    def __init__(self, api, path, rv, on_event):
        super().__init__(daemon=True)
        self.api, self.on_event = api, on_event
        sep = "&" if "?" in path else "?"
        self.path = f"{path}{sep}watch=true&resourceVersion={rv}&allowWatchBookmarks=true&timeoutSeconds=1200"
        self.ready = threading.Event()
        self.conn = None
        self.error = None
        self.stopped = False

    def run(self):
        try:
            self.conn = self.api.conn(timeout=None)
            self.conn.request("GET", self.path, headers={"Accept": "application/json"})
            r = self.conn.getresponse()
            if r.status != 200:
                self.error = f"HTTP {r.status}: {r.read()[:300]!r}"
                self.ready.set()
                return
            self.ready.set()
            while True:
                line = r.readline()
                if not line:
                    return
                t = time.time()
                try:
                    ev = json.loads(line)
                except ValueError:
                    continue
                self.on_event(t, len(line), ev)
        except Exception as e:
            if self.error is None and not self.stopped:
                self.error = repr(e)
            self.ready.set()

    def stop(self):
        self.stopped = True
        try:
            self.conn.sock.shutdown(socket.SHUT_RDWR)
        except Exception:
            pass


def current_rv(api, path):
    sep = "&" if "?" in path else "?"
    return api.get_json(f"{path}{sep}limit=1")["metadata"]["resourceVersion"]


def paced(n, rate):
    t0 = time.time()
    for i in range(n):
        d = t0 + i / rate - time.time()
        if d > 0:
            time.sleep(d)
        yield i


def probe_cm(api, ns, n, rate):
    api.ensure_namespace(ns)
    tag = str(int(time.time()))
    path = f"/api/v1/namespaces/{ns}/configmaps"
    recv = {}

    def on_event(t, size, ev):
        o = ev.get("object") or {}
        if ev.get("type") == "ADDED":
            recv.setdefault(o.get("metadata", {}).get("name"), t)

    w = Watch(api, path, current_rv(api, path), on_event)
    w.start()
    w.ready.wait(15)
    time.sleep(0.5)
    sent, errors = {}, 0
    c = api.conn()
    for i in paced(n, rate):
        name = f"p{tag}-{i}"
        body = {"apiVersion": "v1", "kind": "ConfigMap",
                "metadata": {"name": name, "labels": {"cmps4-probe": tag}}, "data": {"i": str(i)}}
        t0 = time.time()
        try:
            st, _ = api.req("POST", path, body, conn=c)
        except Exception:
            c.close()
            c = api.conn()
            st = -1
        t1 = time.time()
        if st != 201:
            errors += 1
            continue
        sent[name] = (t0, t1)
    deadline = time.time() + 20
    while time.time() < deadline and any(k not in recv for k in sent):
        time.sleep(0.1)
    w.stop()
    lag = [recv[k] - t1 for k, (t0, t1) in sent.items() if k in recv]
    e2e = [recv[k] - t0 for k, (t0, t1) in sent.items() if k in recv]
    rtt = [t1 - t0 for (t0, t1) in sent.values()]
    api.req("DELETE", f"{path}?labelSelector=cmps4-probe%3D{tag}")
    return {"probe": "cm", "objects": n, "ratePerSec": rate, "createErrors": errors,
            "missingEvents": sum(1 for k in sent if k not in recv), "watchError": w.error,
            "createRoundTrip": stats(rtt), "watchLagAfterAck": stats(lag), "watchEndToEnd": stats(e2e)}


def probe_report(api, target, n, rate):
    g, v, plural, ns, name = target
    wpath = list_path(g, v, plural)
    ipath = item_path(g, v, plural, True, ns, name)
    tag = str(int(time.time()))
    recv, sizes = {}, []

    def on_event(t, size, ev):
        o = ev.get("object") or {}
        md = o.get("metadata", {})
        if ev.get("type") == "MODIFIED" and md.get("name") == name and md.get("namespace") == ns:
            s = (md.get("labels") or {}).get("cmps4-seq", "")
            if s.startswith(tag + "-") and s not in recv:
                recv[s] = t
                sizes.append(size)

    w = Watch(api, wpath, current_rv(api, wpath), on_event)
    w.start()
    w.ready.wait(15)
    time.sleep(0.5)
    sent, errors = {}, 0
    c = api.conn()
    for i in paced(n, rate):
        s = f"{tag}-{i}"
        t0 = time.time()
        try:
            st, _ = api.req("PATCH", ipath, {"metadata": {"labels": {"cmps4-seq": s}}},
                            ctype="application/merge-patch+json", conn=c)
        except Exception:
            c.close()
            c = api.conn()
            st = -1
        t1 = time.time()
        if st != 200:
            errors += 1
            continue
        sent[s] = (t0, t1)
    deadline = time.time() + 30
    while time.time() < deadline and any(k not in recv for k in sent):
        time.sleep(0.1)
    w.stop()
    lag = [recv[k] - t1 for k, (t0, t1) in sent.items() if k in recv]
    e2e = [recv[k] - t0 for k, (t0, t1) in sent.items() if k in recv]
    rtt = [t1 - t0 for (t0, t1) in sent.values()]
    return {"probe": "report", "target": f"{plural}/{ns}/{name}", "changes": n, "ratePerSec": rate,
            "patchErrors": errors, "missingEvents": sum(1 for k in sent if k not in recv),
            "watchError": w.error, "eventBytesMax": max(sizes) if sizes else 0,
            "patchRoundTrip": stats(rtt), "watchLagAfterAck": stats(lag), "watchEndToEnd": stats(e2e)}


def probe_list(api, reps):
    out = []
    for (g, v, plural, namespaced, kind) in report_resources(api):
        path = list_path(g, v, plural)
        cache, paged = [], []
        items = nbytes = 0
        for _ in range(reps):
            t0 = time.time()
            st, raw = api.req("GET", path + "?resourceVersion=0")
            cache.append(time.time() - t0)
            nbytes = len(raw)
            del raw
            # paginated, consistent; items counted page by page (time includes
            # the client's JSON decoding of each page, as an informer pays it)
            t0 = time.time()
            cont, n = None, 0
            while True:
                q = {"limit": "500"}
                if cont:
                    q["continue"] = cont
                st, raw = api.req("GET", path + "?" + urllib.parse.urlencode(q))
                if st != 200:
                    break
                doc = json.loads(raw)
                n += len(doc.get("items", []))
                cont = (doc.get("metadata") or {}).get("continue")
                if not cont:
                    break
            paged.append(time.time() - t0)
            items = n
        out.append({"resource": f"{g}/{plural}", "items": items, "bytes": nbytes,
                    "fromWatchCache": stats(cache), "consistentPaged": stats(paged)})
    return out


METRICS = ("process_resident_memory_bytes", "go_memstats_heap_inuse_bytes",
           "apiserver_storage_size_bytes", "apiserver_storage_db_total_size_in_bytes")


def probe_metrics(api):
    st, raw = api.req("GET", "/metrics")
    keep = {"storageObjects": {}, "other": {}}
    if st != 200:
        return {"error": f"HTTP {st}"}
    for line in raw.decode(errors="replace").splitlines():
        if line.startswith("#"):
            continue
        name = line.split("{")[0].split(" ")[0]
        if name == "apiserver_storage_objects":
            res = line.split('resource="')[1].split('"')[0]
            if any(gr in res for gr in ("aquasecurity", "wgpolicyk8s", "reports.kyverno", "openreports")):
                keep["storageObjects"][res] = float(line.rsplit(" ", 1)[1])
        elif name in METRICS:
            keep["other"][line.rsplit(" ", 1)[0]] = float(line.rsplit(" ", 1)[1])
    return keep


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--kubeconfig", required=True)
    ap.add_argument("--phase", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--cm", type=int, default=300)
    ap.add_argument("--cm-rate", type=float, default=20)
    ap.add_argument("--report-target", default="", help="group/version/plural/namespace/name")
    ap.add_argument("--report-n", type=int, default=100)
    ap.add_argument("--report-rate", type=float, default=5)
    ap.add_argument("--list-reps", type=int, default=3)
    a = ap.parse_args()
    api = Api(a.kubeconfig)
    res = {"phase": a.phase, "time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())}
    res["metricsBefore"] = probe_metrics(api)
    res["cm"] = probe_cm(api, "cmps4-probe", a.cm, a.cm_rate)
    if a.report_target:
        g, v, plural, ns, name = a.report_target.split("/")
        res["report"] = probe_report(api, (g, v, plural, ns, name), a.report_n, a.report_rate)
    if a.list_reps > 0:
        try:
            res["list"] = probe_list(api, a.list_reps)
        except Exception as e:
            res["list"] = {"error": repr(e)}
    res["metricsAfter"] = probe_metrics(api)
    with open(a.out, "w") as f:
        json.dump(res, f, indent=1, sort_keys=True)

    p = print
    p(f"== probe {a.phase} at {res['time']}")
    for k in ("cm", "report"):
        if k in res:
            r = res[k]
            p(f"   {k}: " + json.dumps({x: r[x] for x in r if x not in ("createRoundTrip", "patchRoundTrip", "watchLagAfterAck", "watchEndToEnd")}))
            for x in ("createRoundTrip", "patchRoundTrip", "watchLagAfterAck", "watchEndToEnd"):
                if x in r:
                    p(f"      {x:18} {json.dumps(r[x])}")
    if isinstance(res.get("list"), list):
        p(f"   {'list resource':52} {'items':>7} {'bytes':>11} {'cache p50 ms':>13} {'paged p50 ms':>13}")
        for r in res["list"]:
            p(f"   {r['resource']:52} {r['items']:>7} {r['bytes']:>11} "
              f"{r['fromWatchCache'].get('p50_ms', 0):>13} {r['consistentPaged'].get('p50_ms', 0):>13}")
    else:
        p(f"   list: {res.get('list')}")
    p("   metrics after: " + json.dumps(res["metricsAfter"]))


if __name__ == "__main__":
    main()
