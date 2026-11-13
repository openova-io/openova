#!/usr/bin/env python3
"""INF-030 spike — turns the monitor timeline and kured logs into result lines.

Prints KEY<TAB>VALUE lines (the job appends them to results.tsv).

  summarise.py reboots   TIMELINE  NODE=SENTINEL_TIME ...   # per-node reboot timing + serial check
  summarise.py kuredlog  LOGFILE   PREFIX [SINCE]           # drain / reboot / blocked lines of one kured log
"""
import re
import sys
from datetime import datetime, timezone


def ts(s):
    s = s.strip()
    if "." in s:  # kubectl --timestamps gives nanoseconds
        s = s.split(".")[0] + "Z"
    return datetime.strptime(s, "%Y-%m-%dT%H:%M:%SZ").replace(tzinfo=timezone.utc)


def rows(path):
    out = []
    for line in open(path):
        f = line.rstrip("\n").split("\t")
        if len(f) < 6 or f[0] == "time" or f[2] == "API unreachable":
            continue
        nodes = {}
        for cell in f[2].split():
            name, boot, ready, cordoned, ver = cell.split(":", 4)
            nodes[name] = {"boot": boot, "ready": ready == "True", "cordoned": cordoned == "1", "ver": ver}
        out.append((ts(f[0]), nodes, f[3], int(f[4]) if f[4].isdigit() else -1))
    return out


def emit(k, v):
    print(f"{k}\t{v}")


def reboots(timeline, specs):
    data = rows(timeline)
    if not data:
        emit("summary_error", "empty timeline")
        return
    intervals = {}
    for spec in specs:
        node, sent = spec.split("=", 1)
        t_sent = ts(sent)
        before = [r for r in data if r[0] <= t_sent and node in r[1]]
        boot0 = (before[-1] if before else data[0])[1][node]["boot"]
        t_cordon = t_boot = t_ready = t_back = t_unavail0 = t_unavail1 = None
        for t, nodes, _lock, _u in data:
            if t < t_sent or node not in nodes:
                continue
            s = nodes[node]
            unavailable = s["cordoned"] or not s["ready"]
            if unavailable:
                t_unavail0 = t_unavail0 or t
                t_unavail1 = t
            if s["cordoned"] and t_cordon is None:
                t_cordon = t
            if s["boot"] != boot0 and t_boot is None:
                t_boot = t
            if t_boot and s["ready"] and t_ready is None:
                t_ready = t
            if t_boot and s["ready"] and not s["cordoned"] and t_back is None:
                t_back = t
                break

        def d(a):
            return f"{int((a - t_sent).total_seconds())}s" if a else "never"
        emit(f"timing_{node}", f"sentinel {sent}; cordoned +{d(t_cordon)}; new boot ID seen +{d(t_boot)}; "
                               f"Ready on new boot +{d(t_ready)}; schedulable again +{d(t_back)}")
        if t_unavail0:
            intervals[node] = (t_unavail0, t_back or t_unavail1)
    names = sorted(intervals, key=lambda n: intervals[n][0])
    emit("reboot_order", " then ".join(f"{n} (unavailable {intervals[n][0]:%H:%M:%S}-{intervals[n][1]:%H:%M:%S})" for n in names))
    overlap = []
    for i, a in enumerate(names):
        for b in names[i + 1:]:
            if intervals[a][0] <= intervals[b][1] and intervals[b][0] <= intervals[a][1]:
                overlap.append(f"{a}/{b}")
    emit("unavailable_intervals_overlap", "none" if not overlap else ",".join(overlap))
    emit("max_nodes_unavailable_in_timeline", max(r[3] for r in data))


PATTERNS = [
    ("drain_start", re.compile(r"Draining node")),
    ("drain_failed", re.compile(r"Unable to cordon or drain")),
    ("reboot_trigger", re.compile(r"Triggering reboot")),
    ("lock_held_by_other", re.compile(r"Lock already held")),
    ("release_wait", re.compile(r"before releasing lock")),
    ("uncordon", re.compile(r"Uncordoning node|best-effort uncordon")),
    ("blocked", re.compile(r"Reboot blocked")),
    ("reboot_required", re.compile(r"Reboot required")),
]


def kuredlog(path, prefix, since=None):
    t_since = ts(since) if since else None
    hits = {k: [] for k, _ in PATTERNS}
    pods = set()
    for line in open(path, errors="replace"):
        if line.startswith("###") or not line.strip():
            continue
        stamp = line.split(" ", 1)[0]
        try:
            t = ts(stamp)
        except ValueError:
            continue
        if t_since and t < t_since:
            continue
        for k, rx in PATTERNS:
            if rx.search(line):
                hits[k].append(t)
                if k == "blocked":
                    m = re.search(r"matching pods: \[([^\]]*)\]", line)
                    if m:
                        pods.add(m.group(1))
    for k, _ in PATTERNS:
        v = hits[k]
        if v:
            emit(f"{prefix}_{k}", f"{len(v)} lines, first {v[0]:%H:%M:%S}, last {v[-1]:%H:%M:%S}")
        else:
            emit(f"{prefix}_{k}", "0 lines")
    if hits["drain_start"] and hits["reboot_trigger"]:
        emit(f"{prefix}_drain_seconds", int((hits["reboot_trigger"][0] - hits["drain_start"][0]).total_seconds()))
    if pods:
        emit(f"{prefix}_blocking_pods", "; ".join(sorted(pods)))


def overlap(timeline):
    data = rows(timeline)
    hits = [r for r in data if r[3] >= 2]
    emit("x_overlap_polls_with_two_or_more_unavailable", f"{len(hits)} of {len(data)} polls")
    if not hits:
        return
    spans, start, prev = [], hits[0][0], hits[0][0]
    for t, _n, _l, _u in hits[1:]:
        if (t - prev).total_seconds() > 15:
            spans.append((start, prev))
            start = t
        prev = t
    spans.append((start, prev))
    emit("x_overlap_spans", "; ".join(f"{a:%H:%M:%S}-{b:%H:%M:%S} ({int((b - a).total_seconds()) + 5}s)" for a, b in spans))
    t, nodes, lock, u = hits[0]
    cells = ", ".join(f"{n} ready={s['ready']} cordoned={s['cordoned']}" for n, s in sorted(nodes.items()))
    emit("x_overlap_first_poll", f"{t:%H:%M:%S}: {cells}; kured lock holder {lock}; unavailable {u}")


if __name__ == "__main__":
    mode = sys.argv[1]
    if mode == "overlap":
        overlap(sys.argv[2])
    elif mode == "reboots":
        reboots(sys.argv[2], sys.argv[3:])
    elif mode == "kuredlog":
        kuredlog(sys.argv[2], sys.argv[3], sys.argv[4] if len(sys.argv) > 4 else None)
    else:
        sys.exit(f"unknown mode {mode}")
