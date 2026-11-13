#!/usr/bin/env python3
"""Spike SVC-120: wait on and evaluate helm-controller test-hook behaviour.

Subcommands
  wait-installed            phase A: every release has finished its first
                            install + test (or the control has failed)
  wait-handled --token T    phase B: the no-change reconcile request T is handled
  wait-version --version N  phase C: the upgrade to Helm revision N is tested
                            (phase D, a steady window with no change, is a
                            plain wait in run.sh)
  evaluate --dir OUT        read the snapshots and the watch stream, print the
                            measured values, write result.json + summary.md and
                            name the outcome. Exit 0 when the outcome is
                            determined, 2 when the experiment is inconclusive.

A wait never fails the job on timeout: evaluate reads whatever state was
reached and says which measurement is missing.
"""
import argparse
import json
import os
import subprocess
import sys
import time

SEL = "spike.openova.io/svc-120=true"
NS = "flux-system"
IGNORING = ["spike-pass", "spike-fail", "spike-none"]
CONTROL = "spike-fail-strict"


def kubectl_json(*args):
    out = subprocess.run(["kubectl", *args, "-o", "json"], capture_output=True, text=True)
    if out.returncode != 0:
        return None
    return json.loads(out.stdout)


def releases():
    doc = kubectl_json("-n", NS, "get", "helmreleases.helm.toolkit.fluxcd.io", "-l", SEL)
    if not doc:
        return {}
    return {i["metadata"]["name"]: i for i in doc.get("items", [])}


def cond(hr, ctype):
    for c in (hr.get("status") or {}).get("conditions") or []:
        if c.get("type") == ctype:
            return c
    return None


def history(hr):
    return (hr.get("status") or {}).get("history") or []


def hook_phases(snapshot):
    hooks = (snapshot or {}).get("testHooks") or {}
    return {k: (v or {}).get("phase") for k, v in hooks.items()}


def settled(hr):
    st = hr.get("status") or {}
    ready = cond(hr, "Ready")
    return (
        ready is not None
        and ready.get("status") in ("True", "False")
        and st.get("observedGeneration") == hr["metadata"]["generation"]
    )


def tested(hr, version=None):
    """True when the newest snapshot carries a finished test result, or the
    TestSuccess condition says the test step is done for this generation."""
    hist = history(hr)
    if not hist:
        return False
    if version is not None and hist[0].get("version") != version:
        return False
    phases = hook_phases(hist[0])
    if phases and all(p in ("Succeeded", "Failed") for p in phases.values()):
        return True
    ts = cond(hr, "TestSuccess")
    return ts is not None and ts.get("observedGeneration") == hr["metadata"]["generation"]


def poll(check, timeout, label):
    start = time.time()
    last = ""
    while time.time() - start < timeout:
        hrs = releases()
        ok, why = check(hrs)
        if why != last:
            print(f"  [{int(time.time() - start):4d}s] {label}: {why}", flush=True)
            last = why
        if ok:
            print(f"  {label}: reached after {int(time.time() - start)}s", flush=True)
            return 0
        time.sleep(3)
    print(f"  {label}: TIMEOUT after {timeout}s (evaluate reports what is missing)", flush=True)
    return 1


def cmd_wait_installed(a):
    def check(hrs):
        pending = []
        for n in IGNORING:
            hr = hrs.get(n)
            if hr is None or not settled(hr):
                pending.append(f"{n}:not-settled")
            elif n != "spike-none" and not tested(hr):
                pending.append(f"{n}:not-tested")
        hr = hrs.get(CONTROL)
        st = (hr or {}).get("status") or {}
        failed = hr is not None and (
            (cond(hr, "Ready") or {}).get("status") == "False"
            or (st.get("installFailures") or 0) > 0
            or cond(hr, "Remediated") is not None
        )
        if not failed:
            pending.append(f"{CONTROL}:not-failed-yet")
        return (not pending, ",".join(pending) or "all done")

    return poll(check, a.timeout, "phase A")


def cmd_wait_handled(a):
    names = a.names.split(",")

    def check(hrs):
        pending = []
        for n in names:
            hr = hrs.get(n) or {}
            st = hr.get("status") or {}
            if st.get("lastHandledReconcileAt") != a.token or not settled(hr):
                pending.append(n)
        return (not pending, ",".join(pending) or "all handled")

    return poll(check, a.timeout, "phase B")


def cmd_wait_version(a):
    names = a.names.split(",")

    def check(hrs):
        pending = []
        for n in names:
            hr = hrs.get(n)
            if hr is None or not settled(hr) or not tested(hr, a.version):
                pending.append(n)
        return (not pending, ",".join(pending) or f"all at revision {a.version}")

    return poll(check, a.timeout, "phase C")


# ---------------------------------------------------------------- evaluate


def load(path, default=None):
    try:
        with open(path) as f:
            return json.load(f)
    except (OSError, ValueError):
        return default


def describe(hr):
    st = (hr or {}).get("status") or {}

    def c(t):
        x = cond(hr or {}, t)
        return None if x is None else {k: x.get(k) for k in ("status", "reason", "message")}

    return {
        "resourceVersion": (hr or {}).get("metadata", {}).get("resourceVersion"),
        "generation": (hr or {}).get("metadata", {}).get("generation"),
        "observedGeneration": st.get("observedGeneration"),
        "Ready": c("Ready"),
        "Released": c("Released"),
        "TestSuccess": c("TestSuccess"),
        "Remediated": c("Remediated"),
        "Stalled": c("Stalled"),
        "installFailures": st.get("installFailures"),
        "upgradeFailures": st.get("upgradeFailures"),
        "failures": st.get("failures"),
        "lastHandledReconcileAt": st.get("lastHandledReconcileAt"),
        "history": [
            {
                "version": h.get("version"),
                "status": h.get("status"),
                "chartVersion": h.get("chartVersion"),
                "testHooks": h.get("testHooks"),
            }
            for h in history(hr or {})
        ],
    }


def watch_rows(watch, name):
    rows = [w for w in watch if w.get("name") == name]
    rows.sort(key=lambda w: int(w.get("rv") or 0))
    return rows


def ready_sequence(watch, name, from_first_helm_action=False):
    """Distinct consecutive Ready values (status/reason) seen on the watch.

    With from_first_helm_action, rows before the first row that carries the
    Released condition are skipped: before that, Ready only reflects the
    chart source (e.g. False/SourceNotReady while the chart is fetched),
    which every release shows whatever its test does.
    """
    seq = []
    started = not from_first_helm_action
    for w in watch_rows(watch, name):
        conds = w.get("conds") or {}
        if not started and "Released" in conds:
            started = True
        if not started:
            continue
        r = conds.get("Ready") or "absent"
        if not seq or seq[-1] != r:
            seq.append(r)
    return seq


def rows_where(watch, name, pred):
    return [w for w in watch_rows(watch, name) if pred(w)]


def helm_revisions(out, phase, name):
    doc = load(os.path.join(out, f"{phase}-helm-history-{name}.json"), [])
    return [{"revision": r.get("revision"), "status": r.get("status"), "description": r.get("description")} for r in doc]


def read_text(out, name):
    p = os.path.join(out, name)
    return open(p).read().strip() if os.path.exists(p) else ""


PHASES = ("A", "B", "C", "D")


def cmd_evaluate(a):
    out = a.dir
    snaps = {}
    for p in PHASES:
        doc = load(os.path.join(out, f"{p}.json"), {"items": []})
        snaps[p] = {i["metadata"]["name"]: i for i in doc.get("items", [])}
    watch = []
    try:
        with open(os.path.join(out, "watch.jsonl")) as f:
            for line in f:
                line = line.strip()
                if line:
                    watch.append(json.loads(line))
    except OSError:
        pass

    images = read_text(out, "images.txt")
    hc_image = next((l.split("=", 1)[1] for l in images.splitlines() if l.startswith("helm-controller=")), "")
    flux_version = read_text(out, "flux-version.txt")

    result = {
        "fluxVersion": flux_version,
        "helmControllerImage": hc_image,
        "controllerImages": images.splitlines(),
        "phases": {},
        "readySequence": {},
        "readySequenceFromFirstHelmAction": {},
        "helmHistory": {},
        "checks": {},
        "observations": {},
    }
    names = IGNORING + [CONTROL]
    for p, hrs in snaps.items():
        result["phases"][p] = {n: describe(hrs.get(n)) for n in names}
    for n in names:
        result["readySequence"][n] = ready_sequence(watch, n)
        result["readySequenceFromFirstHelmAction"][n] = ready_sequence(watch, n, True)
        result["helmHistory"][n] = {p: helm_revisions(out, p, n) for p in PHASES}

    A, B, C, D = (snaps[p] for p in PHASES)
    checks = result["checks"]
    obs = result["observations"]
    missing = []

    def newest(hrs, n):
        h = history(hrs.get(n) or {})
        return h[0] if h else None

    # ---- Q1: phases recorded in status.history[0].testHooks.
    def hooks_ok(hrs, version):
        p, f = newest(hrs, "spike-pass"), newest(hrs, "spike-fail")
        if p is None or f is None:
            return None
        if p.get("version") != version or f.get("version") != version:
            return None
        pp, fp = hook_phases(p), hook_phases(f)
        return bool(pp) and all(v == "Succeeded" for v in pp.values()) and bool(fp) and any(v == "Failed" for v in fp.values())

    checks["hooksRecordedOnInstall"] = hooks_ok(A, 1)
    checks["hooksRecordedOnUpgrade"] = hooks_ok(C, 2)
    hp = history(C.get("spike-pass") or {})
    checks["newestRevisionIsHistory0"] = (len(hp) >= 2 and hp[0].get("version") == 2 and hp[1].get("version") == 1) if hp else None
    none_hist = newest(A, "spike-none")
    checks["noHookChartHasEmptyTestHooks"] = None if none_hist is None else not hook_phases(none_hist)

    # ---- Q2: Ready untouched on spike-fail.
    def ready_true(hrs, n):
        r = cond(hrs.get(n) or {}, "Ready")
        return None if r is None else r.get("status") == "True"

    fail_seq = result["readySequenceFromFirstHelmAction"]["spike-fail"]
    pass_seq = result["readySequenceFromFirstHelmAction"]["spike-pass"]
    checks["failReadyTrueAfterInstall"] = ready_true(A, "spike-fail")
    checks["failReadyTrueAfterNoopReconcile"] = ready_true(B, "spike-fail")
    checks["failReadyTrueAfterUpgrade"] = ready_true(C, "spike-fail")
    checks["failReadyTrueAfterSteadyWindow"] = ready_true(D, "spike-fail")
    checks["failReadyNeverFalseAfterFirstHelmAction"] = None if not fail_seq else not any(s.startswith("False") for s in fail_seq)
    checks["failReadySequenceEqualsPass"] = None if not (fail_seq and pass_seq) else fail_seq == pass_seq
    have_all = all(h.get("spike-fail") for h in (A, B, C, D))
    checks["failNoRemediation"] = all(
        cond(h.get("spike-fail") or {}, "Remediated") is None
        and not ((h.get("spike-fail") or {}).get("status") or {}).get("installFailures")
        and not ((h.get("spike-fail") or {}).get("status") or {}).get("upgradeFailures")
        for h in (A, B, C, D)
    ) if have_all else None
    hc_, hd_ = result["helmHistory"]["spike-fail"]["C"], result["helmHistory"]["spike-fail"]["D"]
    checks["failHelmHistoryNoRollback"] = ([r["status"] for r in hd_] == ["superseded", "deployed"]) if hd_ else None
    checks["failSteadyNoNewRevision"] = (hc_ == hd_) if (hc_ and hd_) else None
    fc, fd = C.get("spike-fail"), D.get("spike-fail")
    checks["failSteadyObjectUnchanged"] = (
        fc["metadata"].get("resourceVersion") == fd["metadata"].get("resourceVersion") if fc and fd else None
    )
    ha = history(A.get("spike-fail") or {})
    hb = history(B.get("spike-fail") or {})
    checks["noopReconcileDoesNotRerunTests"] = (
        len(ha) == len(hb) and (ha[0].get("testHooks") == hb[0].get("testHooks")) if ha and hb else None
    )

    # ---- Control: without ignoreFailures the same failing hook does change Ready.
    ctl_seq = result["readySequence"][CONTROL]
    ctl_states = [describe(h.get(CONTROL)) for h in (A, B, C, D) if h.get(CONTROL)]
    checks["controlReadyFalse"] = any(s.startswith("False/TestFailed") for s in ctl_seq) or any(
        (s.get("Ready") or {}).get("reason") == "TestFailed" and (s.get("Ready") or {}).get("status") == "False"
        for s in ctl_states
    )
    checks["controlRemediated"] = any(s.get("Remediated") is not None or (s.get("installFailures") or 0) > 0 for s in ctl_states)

    checks["helmControllerIsPinnedV1_1_0"] = hc_image.endswith(":v1.1.0")
    checks["fluxIsPinnedV2_4_0"] = flux_version == "v2.4.0"

    # ---- Observations that bear on the proof record (not part of the outcome).
    def awaiting(w):
        return (w.get("conds") or {}).get("TestSuccess") == "Unknown/AwaitingTests"

    for n in ("spike-pass", "spike-fail"):
        rows = rows_where(watch, n, awaiting)
        obs[f"{n}.readyTrueWhileTestsAwaited"] = any(((w.get("conds") or {}).get("Ready") or "").startswith("True") for w in rows)
        obs[f"{n}.history0HooksEmptyWhileTestsAwaited"] = any(
            (w.get("hist") or []) and not (w["hist"][0].get("th") or {}) for w in rows
        )
        obs[f"{n}.rowsWhileTestsAwaited"] = len(rows)
    for p in PHASES:
        for n in names:
            ts = cond(snaps[p].get(n) or {}, "TestSuccess")
            obs[f"{p}.{n}.TestSuccess"] = None if ts is None else f"{ts.get('status')}/{ts.get('reason')}: {ts.get('message')}"
            obs[f"{p}.{n}.failures"] = ((snaps[p].get(n) or {}).get("status") or {}).get("failures")
    for n in ("spike-pass", "spike-fail"):
        for h in history(D.get(n) or {}):
            for k, v in (h.get("testHooks") or {}).items():
                obs[f"D.{n}.v{h.get('version')}.{k}"] = f"{v.get('phase')} started {v.get('lastStarted')} completed {v.get('lastCompleted')}"
    ctl_c, ctl_d = (C.get(CONTROL) or {}).get("status") or {}, (D.get(CONTROL) or {}).get("status") or {}
    obs["control.installFailures.C"] = ctl_c.get("installFailures")
    obs["control.installFailures.D"] = ctl_d.get("installFailures")
    obs["control.watchRows"] = len(watch_rows(watch, CONTROL))

    # ---- Outcome.
    required_q2 = [
        "failReadyTrueAfterInstall",
        "failReadyTrueAfterUpgrade",
        "failReadyTrueAfterSteadyWindow",
        "failReadyNeverFalseAfterFirstHelmAction",
        "failNoRemediation",
        "failSteadyNoNewRevision",
    ]
    required_q1 = ["hooksRecordedOnInstall", "hooksRecordedOnUpgrade"]
    for k in required_q2:
        if checks.get(k) is None:
            missing.append(k)
    if not checks["controlReadyFalse"]:
        missing.append("controlReadyFalse (the failing hook never failed a release, so the experiment did not exercise a failure)")
    if not checks["helmControllerIsPinnedV1_1_0"]:
        missing.append("helmControllerIsPinnedV1_1_0 (the cluster does not run the pinned controller)")
    if not checks["fluxIsPinnedV2_4_0"]:
        missing.append("fluxIsPinnedV2_4_0 (the applied install.yaml is not the pinned Flux)")
    if missing:
        outcome = "inconclusive"
    elif not all(checks[k] for k in required_q2):
        outcome = "Ready changes on failure"
    elif not all(checks.get(k) for k in required_q1):
        outcome = "History lacks hook phases"
    else:
        outcome = "as expected"
    result["outcome"] = outcome
    result["missing"] = missing

    with open(os.path.join(out, "result.json"), "w") as f:
        json.dump(result, f, indent=2, sort_keys=True)

    lines = [
        "# Spike SVC-120 result",
        "",
        f"- Flux: `{flux_version}`; helm-controller image: `{hc_image}`",
        f"- **Outcome: {outcome}**",
        "",
        "## Checks",
        "",
        "| check | value |",
        "|---|---|",
    ]
    for k, v in checks.items():
        lines.append(f"| {k} | {v} |")
    lines += ["", "## Observations", "", "| observation | value |", "|---|---|"]
    for k, v in obs.items():
        lines.append(f"| {k} | {str(v).replace(chr(10), ' ').replace('|', '/')} |")
    lines += ["", "## Ready sequence on the watch (status/reason, consecutive duplicates collapsed)", ""]
    for n, seq in result["readySequence"].items():
        lines.append(f"- {n}: {' -> '.join(seq) or '(none)'}")
    lines += ["", "## Same, from the first Helm action on", ""]
    for n, seq in result["readySequenceFromFirstHelmAction"].items():
        lines.append(f"- {n}: {' -> '.join(seq) or '(none)'}")
    for p in PHASES:
        lines += ["", f"## Phase {p}", ""]
        for n, d in result["phases"][p].items():
            lines.append(f"### {n}")
            lines.append("```json")
            lines.append(json.dumps(d, indent=1, sort_keys=True))
            lines.append("```")
            lines.append(f"helm history: {json.dumps(result['helmHistory'][n][p])}")
    if missing:
        lines += ["", "## Missing measurements", ""] + [f"- {m}" for m in missing]
    text = "\n".join(lines) + "\n"
    with open(os.path.join(out, "summary.md"), "w") as f:
        f.write(text)
    print(text)
    print(f"SVC-120 OUTCOME: {outcome}")
    return 0 if outcome != "inconclusive" else 2


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    w = sub.add_parser("wait-installed")
    w.add_argument("--timeout", type=int, default=480)
    w = sub.add_parser("wait-handled")
    w.add_argument("--token", required=True)
    w.add_argument("--names", required=True)
    w.add_argument("--timeout", type=int, default=240)
    w = sub.add_parser("wait-version")
    w.add_argument("--version", type=int, required=True)
    w.add_argument("--names", required=True)
    w.add_argument("--timeout", type=int, default=480)
    e = sub.add_parser("evaluate")
    e.add_argument("--dir", required=True)
    a = ap.parse_args()
    return {
        "wait-installed": cmd_wait_installed,
        "wait-handled": cmd_wait_handled,
        "wait-version": cmd_wait_version,
        "evaluate": cmd_evaluate,
    }[a.cmd](a)


if __name__ == "__main__":
    sys.exit(main())
