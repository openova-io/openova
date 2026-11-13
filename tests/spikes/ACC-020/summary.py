#!/usr/bin/env python3
"""Lab test ACC-020: print the measured values per item from evidence/.

Throwaway; never merged. Reads the driver results, the browser results and
the agent's log, and prints one block per item with PASS or FAIL as the
design record's item text defines it, followed by the raw values.
"""
import glob
import json
import os
import re
import sys

EV = sys.argv[1] if len(sys.argv) > 1 else "evidence"


def load(path):
    try:
        with open(path) as f:
            txt = f.read().strip()
        return json.loads(txt) if txt else {}
    except Exception as e:  # noqa: BLE001 - evidence may be missing
        return {"_missing": f"{path}: {e}"}


def text(path):
    try:
        with open(path) as f:
            return f.read()
    except Exception:  # noqa: BLE001
        return ""


drive = {os.path.basename(p)[6:-5]: load(p) for p in glob.glob(f"{EV}/drive-*.json")}
browser = {os.path.basename(p)[:-5]: load(p) for p in glob.glob(f"{EV}/browser/*.json")}
agent = []
for line in text(f"{EV}/agent.log").splitlines():
    if line.startswith("SPIKE "):
        try:
            agent.append(json.loads(line[6:]))
        except json.JSONDecodeError:
            pass


def ev(kind, run=None):
    return [a for a in agent if a.get("ev") == kind and (run is None or a.get("run") == run)]


def shell_ok(d):
    return bool((d.get("shell") or {}).get("ok"))


def verdict(ok):
    return "PASS" if ok else "FAIL"


out = []


def p(*a):
    out.append(" ".join(str(x) for x in a))


def j(x):
    return json.dumps(x, separators=(",", ":"))[:1800]


p("=" * 100)
p("ACC-020 RESULTS")
p("=" * 100)

# Handshake bytes and argument lists per protocol
for proto, run in (("ssh", "ssh-password"), ("kubernetes", "k8s-cert"), ("vnc", "d-vnc-ctl")):
    hs = ev("handshake", run)
    if hs:
        h = hs[0]
        p(f"[handshake {proto}] run={run} ms={h.get('ms')} err={h.get('err')!r} readyID={h.get('readyID')}")
        p(f"  args({len(h.get('args') or [])}): {j(h.get('args'))}")
        p(f"  sent: {j(h.get('sent'))}")
        p(f"  received: {j(h.get('received'))}")
        p(f"  params not in args: {j(h.get('paramsNotInArgs'))}")
for kind in ("server-first-frame", "guacd-first-after-ready", "browser-internal", "server-ping-echo"):
    for a in ev(kind)[:2]:
        p(f"[{kind}] run={a.get('run')} raw={a.get('raw')!r}")

# (a)
a = drive.get("a-password", {})
g1 = browser.get("g1-default", {})
bt = text(f"{EV}/browser-typescripts.txt")
br_ok = bool(re.search(r"br-g1: BROWSER-81 lines in typescript-br-g1 = [1-9]", bt))
p(f"(a) ssh password: {verdict(shell_ok(a) and br_ok)} driver={j(a.get('shell'))} browser_typescript_has_BROWSER-81={br_ok}")

# (b)
b = {k: drive.get(k, {}) for k in ("b-ed25519", "b-ed25519-b64", "b-keyonly", "b-ecdsa", "b-rsa")}
b_ok = shell_ok(b["b-ed25519"]) and not shell_ok(b["b-keyonly"])
p(f"(b) ssh certificate via parameter public-key: {verdict(b_ok)}")
for k, d in b.items():
    p(f"  {k}: ok={shell_ok(d)} errors={j(d.get('errors'))} dialError={d.get('dialError')}")
p("  sshd: " + " | ".join(text(f"{EV}/sshd-auth.log").splitlines()[-8:]))

# (c)
c = {k: drive.get(k, {}) for k in ("c-cert", "c-nocert", "c-sessioncert", "c-multiword")}
p(f"(c) kubernetes via exec proxy (certificate leg): {verdict(shell_ok(c['c-cert']) and not shell_ok(c['c-nocert']))}")
for k, d in c.items():
    p(f"  {k}: ok={shell_ok(d)} errors={j(d.get('errors'))} tail={d.get('typescriptTail', '')[-160:]!r}")
for k in ("c-cert", "c-nocert", "c-sessioncert", "c-multiword"):
    p(f"  proxy log {k}: " + " | ".join(l[:220] for l in text(f"{EV}/proxy-{k}.log").splitlines()[-3:]))

# (d)
for k in ("d-vnc-ctl", "d-vnc-off"):
    d = drive.get(k, {})
    v = d.get("vnc") or {}
    p(f"(d) vnc {k}: framebuffer={v.get('framebufferImg')} firstImgMs={v.get('firstImgMs')} size={v.get('size')} img={v.get('imgInstructions')} "
      f"copyReceived={v.get('copyReceived')} pasteAcks={j(v.get('pasteAcks'))} errors={j(d.get('errors'))}")
    p(f"    server clipboard after paste: {' '.join(text(f'{EV}/vnc-{k}-server-clipboard.txt').split())}")
    p(f"    x11vnc counters: {' | '.join(' '.join(l.split()[2:]) for l in text(f'{EV}/vnc-{k}-stats.txt').splitlines())}")

# (e)
gr = (a.get("growth") or {})
keys = (drive.get("e-keys", {}).get("keys") or {})
e_ok = gr.get("screenIncreases", 0) >= 3 and gr.get("typescriptIncreases", 0) >= 3 and not gr.get("closedDuring")
p(f"(e) recording files grow during the session: {verdict(e_ok)} screenIncreases={gr.get('screenIncreases')} "
  f"typescriptIncreases={gr.get('typescriptIncreases')} timingIncreases={gr.get('timingIncreases')}")
p(f"  samples: {j(gr.get('samples'))}")
p(f"  keystroke->screen file: max={keys.get('screenKeyMaxMs')}ms median={keys.get('screenKeyMedianMs')}ms missing={keys.get('screenKeyMissing')} "
  f"keystroke->typescript: max={keys.get('typescriptEchoMaxMs')}ms median={keys.get('typescriptEchoMedianMs')}ms missing={keys.get('typescriptEchoMissing')}")
p(f"  screen ms: {j(keys.get('screenKeyMs'))}")
p(f"  typescript ms: {j(keys.get('typescriptEchoMs'))}")

# (f)
for k in ("f-replay-browser-session", "f-replay-driver-session"):
    r = browser.get(k, {})
    modes = r.get("modes") or (r.get("partial") or {}).get("modes") or {}
    ok = all((modes.get(m) or {}).get("loaded") and (modes.get(m) or {}).get("durationMs", 0) > 0
             and ((modes.get(m) or {}).get("display") or {}).get("w", 0) > 0 for m in ("tunnelPieces", "partsBlob", "wholeBlob"))
    durs = {m: (modes.get(m) or {}).get("durationMs") for m in ("tunnelPieces", "partsBlob", "wholeBlob")}
    p(f"(f) replay {k}: {verdict(ok)} durations={j(durs)}")
    for m, v in modes.items():
        p(f"  {m}: {j(v)}")
    if r.get("harnessError") or r.get("exception"):
        p(f"  error: {r.get('harnessError') or r.get('exception')}")
p("  pieces served: " + j([x for x in ev("rec-served-in-pieces")]))

# (g)
for k in ("g1-default", "g2-no-uuid-frame", "g3-no-ping-echo", "g4-split-frames", "g5-internal-to-guacd", "g6-no-subprotocol",
          "g7-vnc-no-ping-echo", "g8-vnc-ping-echo"):
    r = browser.get(k, {})
    r = r if "events" in r else (r.get("partial") or r)
    p(f"(g) {k}: clientStates={j(r.get('clientStates'))} unstable={r.get('unstableCount')} tunnelState={r.get('tunnelState')} "
      f"uuid={r.get('uuid')!r} wsProtocol={r.get('wsProtocol')!r} pingEchoes={r.get('pingEchoes')} display={j(r.get('display'))}")
    p(f"    sent: {j(r.get('sent'))}")
    p(f"    wsFirst: {j(r.get('wsFirst'))}")
    errs = [e for e in (r.get('events') or []) if e.get('kind') in ('tunnel-error', 'client-error', 'ws-close', 'ws-error')]
    p(f"    errors/close: {j(errs)} harnessError={r.get('harnessError')}")
p("  " + " | ".join(bt.strip().splitlines()))
for a2 in ev("tunnel-closed"):
    if str(a2.get("run", "")).startswith("br-"):
        p(f"  agent tunnel-closed {a2.get('run')}: fromBrowser={j(a2.get('fromBrowser'))} pingEchoes={a2.get('pingEchoes')}")
for a2 in ev("tunnel-upgrade-failed"):
    p(f"  agent upgrade failed: {j(a2)}")

# (h)
for k in ("h-ctl", "h-off"):
    cl = drive.get(k, {}).get("clip") or {}
    p(f"(h) ssh {k}: copyReceived={cl.get('copyReceived')} copyContainsMarker={cl.get('copyContainsMarker')} copyAfterReleaseMs={cl.get('copyAfterReleaseMs')} "
      f"pasteEffective={cl.get('pasteEffective')} method={cl.get('pasteMethod')!r} pastedTextVisible={cl.get('pastedTextVisible')} pasteAcks={j(cl.get('pasteAcks'))}")
    p(f"    clips={j(drive.get(k, {}).get('clips'))} tail={drive.get(k, {}).get('typescriptTail', '')[-200:]!r}")
hc, ho = drive.get("h-ctl", {}).get("clip") or {}, drive.get("h-off", {}).get("clip") or {}
h_ok = bool(hc.get("copyReceived") and hc.get("pasteEffective")) and not ho.get("copyReceived") and not ho.get("pasteEffective")
p(f"(h) disable-copy / disable-paste block the clipboard (SSH): {verdict(h_ok)}")

# (i)
fi = drive.get("i-fifo", {})
ff = fi.get("fifo") or {}
i_ok = shell_ok(fi) and ff.get("bytes", 0) > 0 and ff.get("instructions", 0) > 0 and "screen-fifo." not in text(f"{EV}/fifo-after.txt")
p(f"(i) recording-write-existing to a FIFO: {verdict(i_ok)} fifo={j(ff)}")
p("  " + " | ".join(text(f"{EV}/fifo-after.txt").splitlines()))

# (j)
p("(j) guacd container resources:")
for line in text(f"{EV}/j-samples.txt").splitlines():
    p("  " + line)
samples = {}
for line in text(f"{EV}/j-samples.txt").splitlines():
    m = re.match(r"(\S+) t=([\d.]+) memory_current=(\d+) anon=(\d+) file=(\d+) cpu_usage_usec=(\d+)", line)
    if m:
        samples[m.group(1)] = tuple(float(x) for x in m.groups()[1:])


def cpu(a_, b_):
    if a_ in samples and b_ in samples:
        dt = samples[b_][0] - samples[a_][0]
        du = samples[b_][4] - samples[a_][4]
        return round(du / (dt * 1e6) * 100, 2)
    return None


p(f"  CPU %: no session {cpu('no-session-0', 'no-session-10')}, idle session {cpu('idle-session-18', 'idle-session-38')}, "
  f"active session {cpu('active-session-12', 'active-session-27')}")
for lab in ("no-session-10", "idle-session-38", "active-session-27"):
    if lab in samples:
        s = samples[lab]
        p(f"  memory {lab}: current={s[1] / 2**20:.1f} MiB anon={s[2] / 2**20:.1f} MiB file={s[3] / 2**20:.1f} MiB")
for k in ("j-idle", "j-active"):
    d = drive.get(k, {})
    p(f"  {k}: ok={shell_ok(d)} active={j(d.get('active'))}")

# (k)
p("(k) seconds from pod creation to ready:")
for line in text(f"{EV}/k-ready.txt").splitlines():
    p("  " + line)
for line in text(f"{EV}/k-events.txt").splitlines():
    if "ull" in line:
        p("  " + line.strip()[:220])

p("=" * 100)
print("\n".join(out))
with open(f"{EV}/summary.txt", "w") as f:
    f.write("\n".join(out) + "\n")
