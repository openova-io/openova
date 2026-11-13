#!/usr/bin/env python3
"""intent-detach.py - stop Flux from applying the intent of one kind, or let it apply again.

When a batch of routes moves its writes to Git and must be rolled back, Flux must stop
applying the files that batch committed, because the old routes write the cluster
directly again. This tool sets the annotation

  kustomize.toolkit.fluxcd.io/reconcile: disabled

on every object of one kind, so that Flux leaves those objects alone; with --reattach it
removes the annotation again. The files stay in Git. It is the rollback tool of the batches
that move routes onto the package intent of catalyst-api.

Usage:
  python3 scripts/intent-detach.py <kind> --kubeconfig <path> [--namespace <ns>] [--reattach]
  python3 scripts/intent-detach.py --self-test

<kind> is a kind as kubectl names it, for example application or applications.apps.openova.io.
Without --namespace every namespace is read. The tool runs kubectl, which must be on PATH,
once to list the objects and once per object to annotate it. It prints one line per object
and then the count:

  detached <n> objects of kind <kind>        (or: reattached <n> objects of kind <kind>)

Exit codes: 0 done; 1 a kubectl call failed (the objects before it are already changed, and
running the same command again is safe); 2 wrong usage.
"""
import argparse
import json
import subprocess
import sys

ANNOTATION = "kustomize.toolkit.fluxcd.io/reconcile"


def kubectl(args):
    """Run kubectl with args. Return (exit code, stdout, stderr)."""
    p = subprocess.run(["kubectl"] + args, capture_output=True, text=True, check=False)
    return p.returncode, p.stdout, p.stderr


def run(kind, kubeconfig, namespace, reattach, runner, out, err):
    """Annotate or un-annotate every object of kind. Return the exit code."""
    base = ["--kubeconfig", kubeconfig]
    scope = ["-n", namespace] if namespace else ["-A"]
    code, stdout, stderr = runner(base + ["get", kind] + scope + ["-o", "json"])
    if code != 0:
        print("intent-detach: kubectl get %s failed: %s" % (kind, stderr.strip()), file=err)
        return 1
    try:
        items = json.loads(stdout).get("items", [])
    except (ValueError, AttributeError) as e:
        print("intent-detach: kubectl get %s printed no list: %s" % (kind, e), file=err)
        return 1
    change = ANNOTATION + "-" if reattach else ANNOTATION + "=disabled"
    count = 0
    for item in items:
        meta = item.get("metadata", {})
        name, ns = meta.get("name", ""), meta.get("namespace", "")
        where = ["-n", ns] if ns else []
        code, _, stderr = runner(base + ["annotate", "--overwrite", kind, name] + where + [change])
        if code != 0:
            print("intent-detach: kubectl annotate %s %s failed: %s" % (kind, name, stderr.strip()), file=err)
            return 1
        print("%s %s" % (ns + "/" + name if ns else name, "reattached" if reattach else "detached"), file=out)
        count += 1
    print("%s %d objects of kind %s" % ("reattached" if reattach else "detached", count, kind), file=out)
    return 0


class Recorder:
    """A fake kubectl for the self-test: it records each call and answers from a script."""

    def __init__(self, listing, fail_on=None):
        self.calls = []
        self.listing = listing
        self.fail_on = fail_on

    def __call__(self, args):
        self.calls.append(args)
        if self.fail_on and self.fail_on in args:
            return 1, self.listing, "refused"
        if "get" in args:
            return 0, self.listing, ""
        return 0, "", ""


class Sink:
    def __init__(self):
        self.lines = []

    def write(self, s):
        self.lines.append(s)

    def text(self):
        return "".join(self.lines)


def self_test():
    """Prove that every rule of the tool can fail. Return the exit code."""
    failures = []
    cases = []

    def check(name, cond):
        cases.append(name)
        if not cond:
            failures.append(name)

    two = json.dumps({"items": [
        {"metadata": {"name": "site", "namespace": "acme"}},
        {"metadata": {"name": "acme"}},
    ]})

    r, out = Recorder(two), Sink()
    code = run("application", "/k", None, False, r, out, Sink())
    check("detach exits 0", code == 0)
    check("detach lists every namespace", r.calls[0] == ["--kubeconfig", "/k", "get", "application", "-A", "-o", "json"])
    check("detach annotates a namespaced object in its namespace",
          r.calls[1] == ["--kubeconfig", "/k", "annotate", "--overwrite", "application", "site", "-n", "acme", ANNOTATION + "=disabled"])
    check("detach annotates a cluster object without a namespace",
          r.calls[2] == ["--kubeconfig", "/k", "annotate", "--overwrite", "application", "acme", ANNOTATION + "=disabled"])
    check("detach prints the count", out.text().endswith("detached 2 objects of kind application\n"))

    r, out = Recorder(two), Sink()
    code = run("application", "/k", "acme", True, r, out, Sink())
    check("reattach exits 0", code == 0)
    check("reattach lists one namespace", r.calls[0] == ["--kubeconfig", "/k", "get", "application", "-n", "acme", "-o", "json"])
    check("reattach removes the annotation", r.calls[1][-1] == ANNOTATION + "-")
    check("reattach prints the count", out.text().endswith("reattached 2 objects of kind application\n"))

    r, out = Recorder(json.dumps({"items": []})), Sink()
    check("no objects is a count of 0", run("application", "/k", None, False, r, out, Sink()) == 0
          and out.text() == "detached 0 objects of kind application\n" and len(r.calls) == 1)

    r, out = Recorder(two, fail_on="get"), Sink()
    check("a failed listing exits 1", run("application", "/k", None, False, r, out, Sink()) == 1 and len(r.calls) == 1)

    r, out = Recorder(two, fail_on="annotate"), Sink()
    check("a failed annotation exits 1", run("application", "/k", None, False, r, out, Sink()) == 1 and len(r.calls) == 2)

    r, out = Recorder("not json"), Sink()
    check("a listing that is not JSON exits 1", run("application", "/k", None, False, r, out, Sink()) == 1)

    for name in failures:
        print("self-test FAIL: " + name)
    if failures:
        return 1
    print("self-test: %d cases passed" % len(cases))
    return 0


def main(argv):
    ap = argparse.ArgumentParser(
        prog="intent-detach.py",
        description="Set or remove kustomize.toolkit.fluxcd.io/reconcile=disabled on every object of one kind.")
    ap.add_argument("kind", nargs="?", help="the kind, as kubectl names it (for example: application)")
    ap.add_argument("--kubeconfig", help="the kubeconfig of the cluster")
    ap.add_argument("--namespace", help="read one namespace only (default: every namespace)")
    ap.add_argument("--reattach", action="store_true", help="remove the annotation instead of setting it")
    ap.add_argument("--self-test", action="store_true", help="prove that every rule of the tool can fail")
    a = ap.parse_args(argv)
    if a.self_test:
        return self_test()
    if not a.kind or not a.kubeconfig:
        ap.print_usage(sys.stderr)
        print("intent-detach.py: <kind> and --kubeconfig are required", file=sys.stderr)
        return 2
    return run(a.kind, a.kubeconfig, a.namespace, a.reattach, kubectl, sys.stdout, sys.stderr)


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
