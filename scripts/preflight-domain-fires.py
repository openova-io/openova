#!/usr/bin/env python3
"""Pre-flight item 2b: the fires on the chosen parent domain in the last seven days.

  curl -sf .../deployments | preflight-domain-fires.py --fqdn <FQDN>
  preflight-domain-fires.py --self-test

Reads the deployments list ({"deployments": [...]}) on stdin. The parent domain is the FQDN without its
first label. A fire is an entry whose sovereignFQDN ends in "." + that domain, whatever its status. It is
recent when its startedAt (UTC, YYYY-MM-DDTHH:MM:SSZ) is less than seven days old; an entry on the domain
whose startedAt cannot be read counts as recent. Four recent fires refuse the fifth, and the line names
the other domain of the rotation pair (the domain rotation of docs/RUNBOOKS.md, section 0.3). Prints one
line. Exit 0: the fire may go. Exit 1: refused, also when the list cannot be read (fail-closed).
"""
import argparse, datetime, json, os, shutil, subprocess, sys, tempfile, time

LIMIT = 4  # recent fires allowed on one parent domain; the fifth is refused
WINDOW = 7 * 86400
ROTATION = {"omani.works": "omantel.biz", "omantel.biz": "omani.works"}
STAMP = "%Y-%m-%dT%H:%M:%SZ"


def recent(deps, domain, now):
    """(recent fires on domain, how many of them have no readable startedAt)."""
    n = unread = 0
    for d in deps:
        if not str(d.get("sovereignFQDN") or "").lower().rstrip(".").endswith("." + domain):
            continue
        try:
            t = datetime.datetime.strptime(d.get("startedAt") or "", STAMP)
            t = t.replace(tzinfo=datetime.timezone.utc).timestamp()
        except (TypeError, ValueError):
            t, unread = None, unread + 1
        n += t is None or t > now - WINDOW
    return n, unread


def verdict(text, fqdn, now):
    """(exit code, line)."""
    try:
        deps = json.loads(text)["deployments"]
        if not isinstance(deps, list) or not all(isinstance(d, dict) for d in deps):
            raise ValueError("deployments is not a list of objects")
    except (ValueError, KeyError, TypeError) as e:
        return 1, f"the deployments list could not be read ({type(e).__name__}: {e}); refused, fail-closed"
    domain = fqdn.lower().rstrip(".").partition(".")[2]
    if not domain:
        return 1, f"FQDN '{fqdn}' has no parent domain; refused"
    n, unread = recent(deps, domain, now)
    head = f"{n} fire(s) on {domain} in the last seven days" + (
        f" ({unread} without a readable startedAt, counted as recent)" if unread else "")
    if n < LIMIT:
        return 0, head + "; the fifth is refused"
    alt = ROTATION.get(domain)
    hint = (f"fire on {alt} instead ({recent(deps, alt, now)[0]} fire(s) there in the last seven days)" if alt
            else "no alternate is set for this domain; the rotation pair is " + " and ".join(sorted(ROTATION)))
    return 1, f"{head}: a fifth is refused; {hint}"


def self_test():
    failed, now, w = [], 1_800_000_000, "omani.works"
    def at(days):
        return time.strftime(STAMP, time.gmtime(now - days * 86400))
    def doc(*entries):
        return json.dumps({"deployments": [{"id": str(i), "status": s, "sovereignFQDN": f, "startedAt": t}
                                           for i, (f, s, t) in enumerate(entries)]})
    def expect(name, got, code, *words):
        ok = got[0] == code and all(x in got[1] for x in words)
        print(("ok   " if ok else "FAIL ") + name + ("" if ok else f": got {got}"))
        failed.extend([] if ok else [name])
    three = [(f"hw{i}.{w}", s, at(i)) for i, s in ((1, "ready"), (2, "wiped"), (3, "failed"))]
    for name, extra, fqdn, code, words in (
            ("three recent fires of any status pass", [], "hw9." + w, 0, ["3 fire(s) on omani.works"]),
            ("a fourth recent fire refuses and names the alternate", [("hw4." + w, "wiped", at(6.9))], "hw9." + w,
             1, ["4 fire(s)", "fire on omantel.biz"]),
            ("fires seven days old or more are not counted",
             [("hw4." + w, "wiped", at(7)), ("hw5." + w, "wiped", at(30))], "hw9." + w, 0, ["3 fire(s)"]),
            ("other domains and a name that only ends like it are not counted",
             [("hw4.omantel.biz", "ready", at(1)), ("hw5.xomani.works", "ready", at(1))], "hw9." + w, 0, ["3 fire"]),
            ("the match ignores case and a trailing dot", [("HW4.OMANI.WORKS.", "ready", at(1))], "hw9." + w, 1,
             ["4 fire(s)"]),
            ("an unreadable startedAt counts as recent", [("hw4." + w, "ready", "yesterday")], "hw9." + w, 1,
             ["1 without a readable startedAt"]),
            ("the alternate of omantel.biz is omani.works", [(f"hw{i}.omantel.biz", "wiped", at(i)) for i in range(4)],
             "hw9.omantel.biz", 1, ["fire on omani.works"]),
            ("a domain outside the pair is refused and the pair is named",
             [(f"hw{i}.omani.rest", "wiped", at(i)) for i in range(4)], "hw9.omani.rest", 1,
             ["the rotation pair is omani.works and omantel.biz"]),
            ("an FQDN without a parent domain is refused", [], "hw9", 1, ["no parent domain"])):
        expect(name, verdict(doc(*three, *extra), fqdn, now), code, *words)
    for name, text in (("an empty text", ""), ("text that is not JSON", "<html>"),
                       ("an object without deployments", '{"error":"unauthorized"}'),
                       ("deployments that is not a list", '{"deployments":{}}'),
                       ("an entry that is not an object", '{"deployments":["hw1.omani.works"]}')):
        expect(name + " is refused", verdict(text, "hw9." + w, now), 1, "could not be read")
    tmp = tempfile.mkdtemp(prefix="preflight-domain-fires-")
    try:  # the item as scripts/prov-preflight.sh runs it, with stand-ins for curl, kubectl, gh and git
        for tool, body in (("curl", '[ -n "$STUB_FAIL" ] && exit 22\ncat "$STUB_LIST"\n'),
                           ("kubectl", "exit 1\n"), ("gh", "exit 1\n"), ("git", "exit 1\n")):
            with open(os.path.join(tmp, tool), "w") as fh:
                fh.write("#!/bin/sh\n" + body)
            os.chmod(os.path.join(tmp, tool), 0o755)
        fires = [(f"hw{i}.{w}", "wiped", time.strftime(STAMP, time.gmtime(time.time() - i * 3600))) for i in range(4)]
        script = os.path.join(os.path.dirname(os.path.abspath(__file__)), "prov-preflight.sh")
        for name, entries, fail, want in (("three fires pass", fires[:3], "", "  ✅ 2b. 3 fire(s)"),
                                          ("four fires refuse", fires, "", "  ❌ 2b. 4 fire(s)"),
                                          ("an unreadable list refuses", [], "1", "  ❌ 2b. the deployments list")):
            with open(os.path.join(tmp, "list.json"), "w") as fh:
                fh.write(doc(*entries))
            env = {k: v for k, v in os.environ.items() if k not in ("BEARER", "HW_TFVARS")}
            env.update(PATH=tmp + os.pathsep + env.get("PATH", ""), COOK="stub", STUB_FAIL=fail,
                       STUB_LIST=os.path.join(tmp, "list.json"))
            out = subprocess.run(["bash", script, "hw9." + w, "auto-hw9"], env=env, stdout=subprocess.PIPE,
                                 stderr=subprocess.STDOUT, timeout=120).stdout.decode("utf-8", "replace")
            lines = [x for x in out.splitlines() if " 2b. " in x]
            expect("prov-preflight.sh: " + name, (0 if len(lines) == 1 and lines[0].startswith(want) else 1,
                                                  str(lines)), 0)
    finally:
        shutil.rmtree(tmp, ignore_errors=True)
    print(f"self-test: {len(failed)} case(s) failed" if failed else "self-test: every case passed")
    return 1 if failed else 0


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--fqdn")
    ap.add_argument("--self-test", action="store_true")
    a = ap.parse_args()
    if a.self_test:
        return self_test()
    if not a.fqdn:
        ap.error("--fqdn is required")
    code, line = verdict(sys.stdin.read(), a.fqdn, time.time())
    print(line)
    return code


if __name__ == "__main__":
    sys.exit(main())
