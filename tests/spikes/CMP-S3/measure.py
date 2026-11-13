#!/usr/bin/env python3
"""Spike CMP-S3: read-only measurement of the scanner's report objects.

Uses only `kubectl get` and `kubectl get --raw`; it writes nothing to the
cluster. It can be pointed at any kubeconfig, a gate environment included:

  python3 measure.py --phase gate --out gate.json [--namespace NS]

It answers, from the live objects:
  owners      which labels and owner references a report carries, and the
              owner chain from the labelled subject up to the top owner;
  dates       whether `publishedDate` is present on each vulnerability;
  benchmarks  which ClusterComplianceReport objects exist and what they hold;
  volume      objects and JSON bytes per report kind (managed fields
              included, as the API server stores them).
"""
import argparse
import collections
import json
import re
import subprocess
import sys
import urllib.parse

GROUP = "aquasecurity.github.io"
VERSION = "v1alpha1"
PREFIX = "trivy-operator."

KIND_RESOURCE = {
    "ReplicaSet": "replicasets.apps",
    "Deployment": "deployments.apps",
    "StatefulSet": "statefulsets.apps",
    "DaemonSet": "daemonsets.apps",
    "Job": "jobs.batch",
    "CronJob": "cronjobs.batch",
    "Pod": "pods",
    "ReplicationController": "replicationcontrollers",
}


def kubectl(*args, check=True):
    p = subprocess.run(["kubectl", *args], capture_output=True, text=True)
    if p.returncode != 0:
        if check:
            raise RuntimeError(f"kubectl {' '.join(args)}: {p.stderr.strip()}")
        return None
    return p.stdout


def raw(path):
    return json.loads(kubectl("get", "--raw", path))


def list_all(plural):
    items, cont = [], None
    while True:
        path = f"/apis/{GROUP}/{VERSION}/{plural}?limit=250"
        if cont:
            path += "&continue=" + urllib.parse.quote(cont)
        doc = raw(path)
        items += doc.get("items") or []
        cont = (doc.get("metadata") or {}).get("continue")
        if not cont:
            return items


def size(obj):
    return len(json.dumps(obj, separators=(",", ":")).encode())


def owner_chain(kind, name, ns):
    """Follow controller owner references from the labelled subject upward."""
    chain = [f"{kind}/{name}"]
    for _ in range(6):
        res = KIND_RESOURCE.get(kind)
        if not res:
            break
        args = ["get", res, name, "-o", "json"]
        if ns:
            args += ["-n", ns]
        out = kubectl(*args, check=False)
        if out is None:
            chain.append("(subject not found)")
            break
        refs = [r for r in (json.loads(out)["metadata"].get("ownerReferences") or []) if r.get("controller")]
        if not refs:
            break
        kind, name = refs[0]["kind"], refs[0]["name"]
        chain.append(f"{kind}/{name}")
    return chain


def volume(plurals, items):
    out = {}
    for pl in plurals:
        its = items[pl]
        sizes = [size(i) for i in its]
        out[pl] = {
            "count": len(its),
            "jsonBytes": sum(sizes),
            "avgBytes": (sum(sizes) // len(its)) if its else 0,
            "maxBytes": max(sizes, default=0),
            "subjectKinds": dict(collections.Counter(
                (i["metadata"].get("labels") or {}).get(PREFIX + "resource.kind", "(no label)") for i in its)),
            "namespaces": dict(collections.Counter(i["metadata"].get("namespace", "(cluster)") for i in its)),
        }
    return out


def owners(plurals, items, ns):
    rows = []
    for pl in plurals:
        for i in items[pl]:
            md = i["metadata"]
            if md.get("namespace") != ns:
                continue
            labels = md.get("labels") or {}
            kind = labels.get(PREFIX + "resource.kind")
            name = labels.get(PREFIX + "resource.name")
            rows.append({
                "report": pl,
                "name": md["name"],
                "labels": {k: v for k, v in sorted(labels.items())},
                "annotations": {k: v for k, v in sorted((md.get("annotations") or {}).items()) if "trivy" in k},
                "ownerReferences": [f'{o["kind"]}/{o["name"]} controller={o.get("controller")}'
                                    for o in md.get("ownerReferences") or []],
                "ownerChain": owner_chain(kind, name, labels.get(PREFIX + "resource.namespace", ns)) if kind and name else [],
            })
    return rows


def dates(items):
    vr = items.get("vulnerabilityreports", [])
    tot = collections.Counter()
    by_sev = collections.defaultdict(collections.Counter)
    field_count = collections.Counter()
    no_date_prefix = collections.Counter()
    samples_no_date, samples_date = [], []
    per_report = []
    scanners = collections.Counter()
    for r in vr:
        rep = r.get("report") or {}
        sc = rep.get("scanner") or {}
        scanners[f'{sc.get("name")} {sc.get("version")}'] += 1
        vulns = rep.get("vulnerabilities") or []
        n_no = 0
        for v in vulns:
            sev = v.get("severity", "?")
            fixable = bool(v.get("fixedVersion"))
            has = bool(v.get("publishedDate"))
            for k in v:
                field_count[k] += 1
            tot["vulnerabilities"] += 1
            tot["publishedDateKeyPresent"] += "publishedDate" in v
            tot["publishedDateNonEmpty"] += has
            tot["lastModifiedDateNonEmpty"] += bool(v.get("lastModifiedDate"))
            tot["fixable"] += fixable
            tot["fixableWithoutPublishedDate"] += fixable and not has
            by_sev[sev]["total"] += 1
            by_sev[sev]["withDate"] += has
            by_sev[sev]["fixable"] += fixable
            if not has:
                n_no += 1
                no_date_prefix[re.split(r"[-:]", v.get("vulnerabilityID", "?"))[0]] += 1
                if len(samples_no_date) < 20:
                    samples_no_date.append({k: v.get(k) for k in (
                        "vulnerabilityID", "severity", "resource", "installedVersion", "fixedVersion",
                        "publishedDate", "lastModifiedDate", "primaryLink")} | {"report": r["metadata"]["name"]})
            elif len(samples_date) < 5:
                samples_date.append({k: v.get(k) for k in (
                    "vulnerabilityID", "severity", "resource", "fixedVersion", "publishedDate",
                    "lastModifiedDate")})
        per_report.append({"report": r["metadata"]["name"], "namespace": r["metadata"].get("namespace"),
                           "artifact": f'{(rep.get("registry") or {}).get("server", "")}/'
                                       f'{(rep.get("artifact") or {}).get("repository", "")}:'
                                       f'{(rep.get("artifact") or {}).get("tag", "")}',
                           "vulnerabilities": len(vulns), "withoutPublishedDate": n_no,
                           "summary": rep.get("summary")})
    return {"totals": dict(tot), "bySeverity": {k: dict(v) for k, v in by_sev.items()},
            "fieldPresence": dict(field_count), "idPrefixWithoutDate": dict(no_date_prefix),
            "samplesWithoutDate": samples_no_date, "samplesWithDate": samples_date,
            "scanners": dict(scanners), "perReport": per_report}


def benchmarks(items):
    out = []
    for c in items.get("clustercompliancereports", []):
        md, spec, st = c["metadata"], c.get("spec") or {}, c.get("status") or {}
        comp = spec.get("compliance") or {}
        controls = comp.get("controls") or []
        sr = st.get("summaryReport") or {}
        cc = sr.get("controlCheck") or []
        fields = sorted({k for x in cc for k in x})
        out.append({
            "name": md["name"],
            "complianceId": comp.get("id"), "title": comp.get("title"),
            "platform": comp.get("platform"), "type": comp.get("type"), "version": comp.get("version"),
            "cron": spec.get("cron"), "reportType": spec.get("reportType"),
            "created": md.get("creationTimestamp"),
            "updateTimestamp": st.get("updateTimestamp"),
            "summary": st.get("summary"),
            "controlsInSpec": len(controls),
            "controlsWithoutChecks": sum(1 for x in controls if not x.get("checks")),
            "controlCheckEntries": len(cc),
            "controlCheckFields": fields,
            "controlsTotalFailAboveZero": sum(1 for x in cc if (x.get("totalFail") or 0) > 0),
            "controlsTotalFailZero": sum(1 for x in cc if "totalFail" in x and x.get("totalFail") == 0),
            "controlsTotalFailAbsent": sum(1 for x in cc if "totalFail" not in x),
            "controlCheckSample": cc[:4],
            "statusKeys": sorted(st),
        })
    return out


def metrics():
    text = kubectl("get", "--raw", "/metrics", check=False) or ""
    keep = []
    for line in text.splitlines():
        if line.startswith("#"):
            continue
        if ("apiserver_storage_objects" in line and GROUP in line) or line.startswith("apiserver_storage_size_bytes"):
            keep.append(line)
    return keep


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--phase", required=True)
    ap.add_argument("--out", required=True)
    ap.add_argument("--namespace", default="")
    a = ap.parse_args()

    res = raw(f"/apis/{GROUP}/{VERSION}")["resources"]
    plurals = sorted(r["name"] for r in res if "/" not in r["name"])
    items = {pl: list_all(pl) for pl in plurals}

    result = {
        "phase": a.phase,
        "volume": volume(plurals, items),
        "owners": owners(plurals, items, a.namespace) if a.namespace else [],
        "dates": dates(items),
        "benchmarks": benchmarks(items),
        "metrics": metrics(),
    }
    with open(a.out, "w") as f:
        json.dump(result, f, indent=1, sort_keys=True)

    p = print
    p(f"===== phase {a.phase} =====")
    p("--- volume (JSON bytes as served by the API, managed fields included)")
    tc = tb = 0
    for pl, v in result["volume"].items():
        tc += v["count"]
        tb += v["jsonBytes"]
        p(f"{pl:34} count={v['count']:>5} bytes={v['jsonBytes']:>10} avg={v['avgBytes']:>7} max={v['maxBytes']:>7} "
          f"subjects={v['subjectKinds']}")
    p(f"{'TOTAL':34} count={tc:>5} bytes={tb:>10}")
    p("--- apiserver metrics")
    for line in result["metrics"]:
        p("  " + line)
    if a.namespace:
        p(f"--- owners in namespace {a.namespace}")
        for o in result["owners"]:
            lab = {k: v for k, v in o["labels"].items() if k.startswith(PREFIX)}
            p(f"{o['report']}/{o['name']}")
            p(f"    labels: {lab}")
            p(f"    ownerReferences: {o['ownerReferences']}")
            p(f"    ownerChain: {' -> '.join(o['ownerChain'])}")
    d = result["dates"]
    p("--- vulnerability dates")
    p(f"totals: {d['totals']}")
    p(f"by severity: {d['bySeverity']}")
    p(f"scanners: {d['scanners']}")
    p(f"id prefix of findings without a date: {d['idPrefixWithoutDate']}")
    for s in d["samplesWithDate"][:3]:
        p(f"  with date: {s}")
    for s in d["samplesWithoutDate"][:10]:
        p(f"  without date: {s}")
    for r in d["perReport"]:
        p(f"  report {r['namespace']}/{r['report']} {r['artifact']} vulns={r['vulnerabilities']} "
          f"noDate={r['withoutPublishedDate']}")
    p("--- benchmark reports")
    for b in result["benchmarks"]:
        p(f"{b['name']}: id={b['complianceId']} platform={b['platform']} cron={b['cron']!r} type={b['reportType']} "
          f"created={b['created']} updated={b['updateTimestamp']} summary={b['summary']} "
          f"controls={b['controlsInSpec']} noChecks={b['controlsWithoutChecks']} entries={b['controlCheckEntries']} "
          f"fail>0={b['controlsTotalFailAboveZero']} fail=0={b['controlsTotalFailZero']} "
          f"failAbsent={b['controlsTotalFailAbsent']} fields={b['controlCheckFields']}")
        for s in b["controlCheckSample"][:2]:
            p(f"    sample: {s}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
