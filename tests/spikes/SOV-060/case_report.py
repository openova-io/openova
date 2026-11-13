#!/usr/bin/env python3
"""SOV-060 spike: summarise one pull case from the logs the runner collected.

Usage: case_report.py CASE_DIR CASE_ID REF RC T0 T1
Env:   SOV060_WATCH     public registry names whose lookups are counted
       SOV060_RESOLVED  JSON {name: [ipv4...]} resolved once at setup
       SOV060_RESULTS   TSV file the one-line row is appended to

Reads from CASE_DIR: pull.out, origin.log, harbor.log, stub.log,
containerd.log, client.log (Dragonfly job only), capture.txt (tcpdump -r).
Writes CASE_DIR/report.json and prints a readable block.
"""
import ipaddress
import json
import os
import re
import sys

d, cid, ref, rc, t0, t1 = sys.argv[1:7]
rc = int(rc)
secs = round(float(t1) - float(t0), 1)
WATCH = os.environ.get("SOV060_WATCH", "").split()
resolved_path = os.environ.get("SOV060_RESOLVED", "")
RESOLVED = json.load(open(resolved_path)) if resolved_path and os.path.exists(resolved_path) else {}

REQ = re.compile(
    r'(?:(\d+\.\d+\.\d+\.\d+) - (?:- \[[^\]]*\] )?)?"(GET|HEAD|POST|PUT|PATCH|DELETE) (\S+) HTTP/[\d.]+" (\d{3})'
)
QUERY = re.compile(r' > \S+\.53: (\d+)\+?(?: \[[^\]]*\])* (A|AAAA)\? (\S+?)\.? \(')
ANSWER = re.compile(r'\.53 > \S+: (\d+)\S* (\d+)/(\d+)/(\d+) (.*)')
SYN = re.compile(r'IP (\d+\.\d+\.\d+\.\d+)\.(\d+) > (\d+\.\d+\.\d+\.\d+)\.(\d+): Flags \[S\]')


def read(name):
    try:
        with open(os.path.join(d, name), errors="replace") as f:
            return f.read().splitlines()
    except FileNotFoundError:
        return []


def requests(lines):
    out = []
    for line in lines:
        m = REQ.search(line)
        if m:
            out.append({"src": m.group(1) or "", "method": m.group(2), "path": m.group(3), "status": int(m.group(4))})
    return out


def is_public(ip):
    try:
        return ipaddress.ip_address(ip).is_global
    except ValueError:
        return False


def name_matches(qname, watched):
    q = qname.lower().rstrip(".")
    return q == watched or q.startswith(watched + ".")


APPLOG = re.compile(r'http\.request\.method=(\w+).*?http\.request\.uri="([^"]+)".*?http\.response\.status=(\d{3})')


def app_requests(lines):
    # The origin's own "response completed" lines: a second count, so that a
    # missing access log can never read as "the origin received nothing".
    out = []
    for line in lines:
        if "response completed" not in line:
            continue
        m = APPLOG.search(line)
        if m:
            out.append({"src": "", "method": m.group(1), "path": m.group(2), "status": int(m.group(3))})
    return out


origin_lines = read("origin.log")
origin = requests(origin_lines)
origin_app = app_requests(origin_lines)
if len(origin_app) > len(origin):
    origin = origin_app
harbor = requests(read("harbor.log"))
stub = requests(read("stub.log"))
client_lines = read("client.log")
ctrd = read("containerd.log")
cap = read("capture.txt")

# DNS lookups and answers seen on any interface during the case
queries = {}
answers_by_id = {}
for line in cap:
    m = QUERY.search(line)
    if m:
        queries.setdefault(m.group(3).lower(), []).append(m.group(1))
        continue
    m = ANSWER.search(line)
    if m:
        ips = re.findall(r'\bA (\d+\.\d+\.\d+\.\d+)', m.group(5))
        answers_by_id.setdefault(m.group(1), set()).update(ips)

watched_queries = {}
watched_ips = {}
for w in WATCH:
    n = 0
    ips = set(RESOLVED.get(w, []))
    for q, ids in queries.items():
        if name_matches(q, w):
            n += len(ids)
            for i in ids:
                ips |= answers_by_id.get(i, set())
    watched_queries[w] = n
    watched_ips[w] = sorted(ips)

syns = []
for line in cap:
    m = SYN.search(line)
    if m:
        syns.append((m.group(1), m.group(3), int(m.group(4))))
public_syns = sorted({"%s:%d" % (dst, port) for _, dst, port in syns if is_public(dst)})
syn_to_watched = {}
for w, ips in watched_ips.items():
    n = sum(1 for _, dst, _ in syns if dst in ips)
    if n:
        syn_to_watched[w] = n

next_host = [line for line in ctrd if "trying next host" in line]
# The node-local proxy (Dragonfly job): containerd could not even connect to it.
proxy_refused = sum(1 for line in next_host if "127.0.0.1:4001" in line and "dial tcp" in line)
errors = [line for line in ctrd if "level=error" in line or "level=warning" in line]

report = {
    "id": cid,
    "ref": ref,
    "pulled": rc == 0,
    "rc": rc,
    "seconds": secs,
    "origin_requests": len(origin),
    "origin": ["%s %s %d" % (r["method"], r["path"], r["status"]) for r in origin],
    "local_requests": len(harbor),
    "local": ["%s %s %s %d" % (r["src"], r["method"], r["path"], r["status"]) for r in harbor],
    "local_statuses": sorted({r["status"] for r in harbor if r["path"].startswith("/v2/")}),
    "stub_requests": len(stub),
    "stub": ["%s %s %d" % (r["method"], r["path"], r["status"]) for r in stub],
    "dragonfly_log_lines": len(client_lines),
    "containerd_next_host": len(next_host),
    "proxy_refused": proxy_refused,
    "watched_lookups": {k: v for k, v in watched_queries.items() if v},
    "syn_to_watched": syn_to_watched,
    "public_syn_destinations": public_syns,
    "pull_output": read("pull.out")[-3:],
}
with open(os.path.join(d, "report.json"), "w") as f:
    json.dump(report, f, indent=2)


def show(title, items, limit=25):
    print("  %s (%d):" % (title, len(items)))
    for x in items[:limit]:
        print("    " + x[:220])
    if len(items) > limit:
        print("    ... %d more" % (len(items) - limit))


print("=" * 78)
print("CASE %s  ref=%s" % (cid, ref))
print("  pulled=%s rc=%d seconds=%.1f" % (report["pulled"], rc, secs))
show("crictl output", report["pull_output"], 3)
show("origin (second endpoint) requests", report["origin"])
show("local registry requests", report["local"])
show("stub requests", report["stub"])
print("  containerd 'trying next host' lines: %d" % len(next_host))
for line in next_host[:12]:
    print("    " + line[:260])
for line in errors[:6]:
    print("    " + line[:260])
print("  DNS lookups of watched public names: %s" % (report["watched_lookups"] or "none"))
print("  TCP SYN to addresses of watched names: %s" % (syn_to_watched or "none"))
print("  TCP SYN to any public address (unattributed, includes the runner's own traffic): %s"
      % (", ".join(public_syns) or "none"))
if client_lines:
    show("dragonfly dfdaemon log (last lines)", client_lines[-30:], 30)

results = os.environ.get("SOV060_RESULTS")
if results:
    with open(results, "a") as f:
        f.write("\t".join(str(x) for x in [
            cid, ref, rc, secs, len(origin), len(harbor), len(stub),
            ",".join("%s:%d" % kv for kv in report["watched_lookups"].items()) or "-",
            ",".join("%s:%d" % kv for kv in syn_to_watched.items()) or "-",
        ]) + "\n")
