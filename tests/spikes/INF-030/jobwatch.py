#!/usr/bin/env python3
"""INF-030 spike — watches the Jobs and Pods that system-upgrade-controller makes for one Plan.

  jobwatch.py PLAN WINDOW_START WINDOW_END TIMEOUT_S OUT_JSON

Polls every 3 seconds until the Plan reports Complete=True after at least one
Job, or until the time-out. Prints KEY<TAB>VALUE result lines on stdout.
"""
import json
import os
import subprocess
import sys
import time
from datetime import datetime, timezone

KCFG = os.environ["KCFG"]
IPS = os.environ["SERVER_IPS"].split()


def kubectl(args):
    for ip in IPS:
        try:
            r = subprocess.run(["kubectl", "--kubeconfig", KCFG, "--server", f"https://{ip}:6443",
                                "--request-timeout=4s"] + args, capture_output=True, text=True, timeout=8)
        except subprocess.TimeoutExpired:
            continue
        if r.returncode == 0:
            return json.loads(r.stdout)
    return None


def parse(s):
    return datetime.strptime(s, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc) if s else None


def fmt(t):
    return t.strftime("%H:%M:%S") if t else "-"


def rel(t, ref):
    if not t:
        return "-"
    s = int((t - ref).total_seconds())
    return f"{'+' if s >= 0 else ''}{s}s"


def main():
    plan, w_start, w_end, timeout, out = sys.argv[1], parse(sys.argv[2]), parse(sys.argv[3]), int(sys.argv[4]), sys.argv[5]
    sel = f"upgrade.cattle.io/plan={plan}"
    jobs, pod_labels, conds_closed, versions = {}, None, [], {}
    max_active, closed_polls, jobs_seen_while_closed = 0, 0, 0
    deadline = time.time() + timeout
    complete_at = None
    while time.time() < deadline:
        t = datetime.now(timezone.utc)
        jl = kubectl(["-n", "system-upgrade", "get", "jobs", "-l", sel, "-o", "json"])
        pl = kubectl(["-n", "system-upgrade", "get", "pods", "-l", sel, "-o", "json"])
        p = kubectl(["-n", "system-upgrade", "get", "plan", plan, "-o", "json"])
        nl = kubectl(["get", "nodes", "-o", "json"])
        if jl is None or p is None:
            time.sleep(3)
            continue
        active = 0
        for j in jl["items"]:
            name = j["metadata"]["name"]
            rec = jobs.setdefault(name, {"node": j["metadata"]["labels"].get("upgrade.cattle.io/node"),
                                         "created": j["metadata"]["creationTimestamp"],
                                         "first_seen": t.strftime("%Y-%m-%dT%H:%M:%SZ")})
            st = j.get("status", {})
            rec["started"] = st.get("startTime")
            rec["completed"] = st.get("completionTime")
            rec["succeeded"] = st.get("succeeded", 0)
            rec["failed"] = st.get("failed", 0)
            if st.get("active", 0) > 0:
                active += 1
        max_active = max(max_active, active)
        if pl:
            for pod in pl["items"]:
                if pod_labels is None:
                    pod_labels = pod["metadata"]["labels"]
                    sys.stderr.write(f"{t:%H:%M:%S} first pod {pod['metadata']['name']} on {pod['spec'].get('nodeName')}\n")
                jobs_name = pod["metadata"]["labels"].get("batch.kubernetes.io/job-name") or pod["metadata"]["labels"].get("job-name")
                if jobs_name in jobs:
                    jobs[jobs_name]["pod_phase"] = pod["status"].get("phase")
        conds = {c["type"]: c for c in p.get("status", {}).get("conditions", [])}
        c = conds.get("Complete", {})
        if t < w_start:
            closed_polls += 1
            jobs_seen_while_closed = max(jobs_seen_while_closed, len(jl["items"]))
            item = f"Complete={c.get('status')} reason={c.get('reason')} message={c.get('message')}"
            if item not in conds_closed:
                conds_closed.append(item)
        if nl:
            for n in nl["items"]:
                v = n["status"]["nodeInfo"]["kubeletVersion"]
                name = n["metadata"]["name"]
                if versions.get(name, [None])[-1] != v:
                    versions.setdefault(name, []).append(v)
                    versions.setdefault(name + "@", []).append(t.strftime("%H:%M:%S"))
        if jobs and c.get("status") == "True":
            complete_at = t
            break
        time.sleep(3)

    def emit(k, v):
        print(f"{k}\t{v}")

    created = sorted((parse(r["created"]), n) for n, r in jobs.items())
    emit("q5_closed_window_polls", f"{closed_polls} polls before {fmt(w_start)}; most Jobs seen at one poll while closed: {jobs_seen_while_closed}")
    emit("q5_plan_condition_while_closed", " || ".join(conds_closed) or "-")
    emit("q5_jobs_created_before_window_start", sum(1 for c, _ in created if c < w_start))
    if created:
        emit("q5_first_job_created", f"{fmt(created[0][0])} ({rel(created[0][0], w_start)} after window start {fmt(w_start)})")
    emit("q5_jobs_created_after_window_end", ", ".join(f"{jobs[n]['node']} at {fmt(c)} ({rel(c, w_end)} after end {fmt(w_end)})" for c, n in created if c >= w_end) or "none")
    for c, n in created:
        r = jobs[n]
        emit(f"q3_job_{r['node']}", f"created {fmt(c)} started {fmt(parse(r.get('started')))} completed {fmt(parse(r.get('completed')))} succeeded={r.get('succeeded')} failed={r.get('failed')} (window {fmt(w_start)}-{fmt(w_end)})")
    emit("q3_max_active_jobs_at_once", max_active)
    emit("q3_pod_labels", json.dumps(pod_labels, sort_keys=True) if pod_labels else "no pod seen")
    emit("q3_plan_complete", f"yes at {fmt(complete_at)}" if complete_at else f"no (time-out {timeout}s)")
    emit("q3_kubelet_version_history", "; ".join(f"{k}: {' -> '.join(f'{v}@{a}' for v, a in zip(versions[k], versions[k + '@']))}" for k in sorted(versions) if not k.endswith("@")))
    with open(out, "w") as f:
        json.dump({"plan": plan, "window_start": sys.argv[2], "window_end": sys.argv[3], "jobs": jobs,
                   "pod_labels": pod_labels, "max_active": max_active, "conds_closed": conds_closed,
                   "versions": versions}, f, indent=1)


if __name__ == "__main__":
    main()
