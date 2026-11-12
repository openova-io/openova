#!/usr/bin/env python3
"""Guard G4 of contract C3 (CTL-044). Run from the repository root, or with --root <folder>.
Rule 1: docs/contracts/C3-event-subjects.yaml has the keys schema (openova.io/event-subjects/v1),
  stream (CATALYST_ORG) and subjects (a list, not empty), and nothing else. Each entry has exactly
  subject, envelope (cloudevents, legacy or foreign), owner (three capital letters), producers and
  consumers (lists of distinct names, may be empty), payload (a file of the repository; a .json file
  must be valid JSON) and delivery (at-least-once, or at-most-once with envelope foreign only).
Rule 2: no subject is listed twice. A legacy subject is catalyst.<token>.<token>[.<token>...]; a
  cloudevents or foreign subject is catalyst.<domain>.<event>.v<major>. A token is [a-z][a-z0-9_]*.
Rule 3: every subject that a .go file names is listed. The files are those git lists (tracked, or new
  and not ignored), *_test.go excepted; NOT_SUBJECTS are no subjects. (a) A subject literal is a quoted
  or raw string literal outside comments whose whole text is a legacy subject name; a wildcard filter
  is none. (b) An event type names catalyst.billing.<type> for order.* and catalyst.<type> otherwise,
  as CanonicalSubject of core/services/shared/events/bridge.go does. It is the first string literal
  after the "(" of a call of NewEvent or publishEvent, on the same line, when its text is
  <token>.<token>[.<token>...]; or a string literal of a list that opens with "EventTypes: []string{"
  or "eventTypes := []string{", up to the first "}". A line that starts with // is not read for (b).
Rule 4: two counts per .go file, each less its allow entries, equal the file's entry under their
  metric in scripts/baselines/streams.json (form openova.io/guard-baseline/v1; an entry is 1 or more,
  an absent file is allowed 0; an allow entry has metric, path, count, owner and a reason of ten
  characters or more). stream_create_calls: the lines (not starting with //) that call AddStream,
  CreateStream, CreateOrUpdateStream or UpdateStream. event_type_indirections: the calls of NewEvent
  or publishEvent with no event type as their first string literal (their func lines excepted), the
  lines "EventTypes:" that open no list, and the lines that build events.Event{ or natsbus.Event{.
  An indirection hides event types from rule 3: its entry is raised only when this guard reads the
  new name too. Above its entry fails; below it fails until it is lowered.
Prints one "FAIL rule <n>: ..." line per failure, then subjects=, code_subjects= and one line per
metric. Exit 0 pass, 1 a rule failed, 2 a file could not be read (no judgement).
--self-test proves every rule can fail, with the Python standard library alone.
"""
import json, os, re, subprocess, sys

REGISTRY, BASELINE = "docs/contracts/C3-event-subjects.yaml", "scripts/baselines/streams.json"
METRICS = ["event_type_indirections", "stream_create_calls"]  # in the order of the names
KEYS = ["consumers", "delivery", "envelope", "owner", "payload", "producers", "subject"]
LEGACY = re.compile(r"catalyst(\.[a-z][a-z0-9_]*){2,}")
VERSIONED = re.compile(r"catalyst\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.v[0-9]+")
AREA = re.compile(r"[A-Z]{3}")
EVENT_TYPE = re.compile(r"[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+")
NOT_SUBJECTS = {"catalyst.openova.io", "catalyst.event.dead_letter"}  # the API group; the Kafka-only dead-letter event
STREAM_CALL = re.compile(r"\.(AddStream|CreateStream|CreateOrUpdateStream|UpdateStream)\(")
EVENT_CALL = re.compile(r"(func (?:\([^)]*\) )?)?\b(?:NewEvent|publishEvent)\(")
TYPE_LIST, TYPE_FIELD, EVENT_BUILD = re.compile(r"\b[Ee]ventTypes\s*:?=?\s*\[\]string\{"), re.compile(r"\bEventTypes:"), re.compile(r"\b(events|natsbus)\.Event\{")

def literals(src):
    """The string literals of Go source; comments and rune literals are skipped."""
    out, i, n = [], 0, len(src)
    while i < n:
        if src.startswith("//", i):
            j = src.find("\n", i)
            i = n if j < 0 else j
        elif src.startswith("/*", i):
            j = src.find("*/", i + 2)
            i = n if j < 0 else j + 2
        elif src[i] == "`":
            j = src.find("`", i + 1)
            j = n if j < 0 else j
            out.append(src[i + 1:j]); i = j + 1
        elif src[i] in "\"'":
            j = i + 1
            while j < n and src[j] not in (src[i], "\n"):
                j += 2 if src[j] == "\\" else 1
            if src[i] == '"':
                out.append(src[i + 1:j])
            i = j + 1
        else:
            i += 1
    return out

def event_types(src):
    """Rule 3 (b): the subjects of the event types a Go file names, and its indirections."""
    types, hidden, lines = [], 0, src.split("\n")
    for k, line in enumerate(lines):
        if line.lstrip().startswith("//"): continue
        call, opened = EVENT_CALL.search(line), TYPE_LIST.search(line)
        if call and not call.group(1):
            first = literals(line[call.end():])[:1]
            if first and EVENT_TYPE.fullmatch(first[0]): types += first
            else: hidden += 1
        if opened: types += literals("\n".join(lines[k:])[opened.end():].split("}")[0])
        elif TYPE_FIELD.search(line): hidden += 1
        hidden += bool(EVENT_BUILD.search(line))
    return [t if t.startswith("catalyst.") else ("catalyst.billing." if t.startswith("order.") else "catalyst.") + t for t in types], hidden

def names(v):
    return isinstance(v, list) and all(isinstance(x, str) and x for x in v) and len(set(v)) == len(v)

def check_registry(reg, read):
    """Rules 1 and 2; read(path) gives a file's text or None. Returns (failures, subjects)."""
    if not isinstance(reg, dict) or sorted(reg) != ["schema", "stream", "subjects"]:
        return ["rule 1: the top-level keys must be exactly schema, stream and subjects"], set()
    f, seen = [], set()
    if reg["schema"] != "openova.io/event-subjects/v1": f.append("rule 1: schema must be openova.io/event-subjects/v1")
    if reg["stream"] != "CATALYST_ORG": f.append("rule 1: stream must be CATALYST_ORG")
    if not isinstance(reg["subjects"], list) or not reg["subjects"]:
        return f + ["rule 1: subjects must be a list that is not empty"], seen
    for k, e in enumerate(reg["subjects"], 1):
        if not isinstance(e, dict) or sorted(e) != KEYS:
            f.append("rule 1: entry %d must have exactly the keys %s" % (k, ", ".join(KEYS))); continue
        s, p, env = e["subject"], e["payload"], e["envelope"]
        at = s if isinstance(s, str) else "entry %d" % k
        if env not in ("cloudevents", "legacy", "foreign"): f.append("rule 1: %s: envelope must be cloudevents, legacy or foreign" % at)
        if not (isinstance(e["owner"], str) and AREA.fullmatch(e["owner"])): f.append("rule 1: %s: owner must be three capital letters" % at)
        for key in ("producers", "consumers"):
            if not names(e[key]): f.append("rule 1: %s: %s must be a list of distinct names" % (at, key))
        text = read(p) if isinstance(p, str) and p and ".." not in p.split("/") else None
        if text is None: f.append("rule 1: %s: payload %r is not a file of the repository" % (at, p))
        elif p.endswith(".json"):
            try: json.loads(text)
            except ValueError: f.append("rule 1: %s: payload %s is not valid JSON" % (at, p))
        if e["delivery"] not in ("at-least-once", "at-most-once"): f.append("rule 1: %s: delivery must be at-least-once or at-most-once" % at)
        elif e["delivery"] == "at-most-once" and env != "foreign": f.append("rule 1: %s: delivery at-most-once needs envelope foreign" % at)
        if not isinstance(s, str): f.append("rule 2: entry %d: subject must be a string" % k); continue
        if s in seen: f.append("rule 2: %s is listed twice" % s)
        seen.add(s)
        if not (LEGACY if env == "legacy" else VERSIONED).fullmatch(s): f.append("rule 2: %s is not a valid name for envelope %s" % (s, env))
    return f, seen

def check_baseline(base, have):
    """Rule 4; have maps each metric to {a .go file: its count}."""
    if not isinstance(base, dict) or sorted(base) != ["allow", "counts", "schema"] or base["schema"] != "openova.io/guard-baseline/v1":
        return ["rule 4: the baseline must have exactly the keys schema, counts and allow, schema openova.io/guard-baseline/v1"]
    c, allow, allowed = base["counts"], base["allow"], {}
    if not (isinstance(c, dict) and sorted(c) == METRICS and all(isinstance(c[m], dict) for m in METRICS)
            and all(type(v) is int and v >= 1 for m in METRICS for v in c[m].values())):
        return ["rule 4: counts must hold exactly %s, each entry 1 or more" % " and ".join(METRICS)]
    for a in allow if isinstance(allow, list) else [None]:
        if not (isinstance(a, dict) and sorted(a) == ["count", "metric", "owner", "path", "reason"] and a["metric"] in METRICS
                and type(a["count"]) is int and a["count"] >= 1 and isinstance(a["path"], str)
                and isinstance(a["reason"], str) and len(a["reason"]) >= 10 and isinstance(a["owner"], str) and AREA.fullmatch(a["owner"])):
            return ["rule 4: an allow entry needs a metric of counts, path, count, owner and a reason of ten characters or more"]
        allowed[a["metric"], a["path"]] = allowed.get((a["metric"], a["path"]), 0) + a["count"]
    f = []
    for m in METRICS:
        for path in sorted(set(have[m]) | set(c[m]) | {p for am, p in allowed if am == m}):
            n, w = have[m].get(path, 0) - allowed.get((m, path), 0), c[m].get(path, 0)
            if n < 0: f.append("rule 4: %s: its allow entries are above its %d %s" % (path, have[m].get(path, 0), m))
            elif n > w: f.append("rule 4: %s has %d %s; the baseline allows %d" % (path, n, m, w))
            elif n < w: f.append("rule 4: %s has %d %s; lower its baseline entry from %d" % (path, n, m, w))
    return f

def judge(reg, read, go_files, base):
    """All four rules; go_files maps a path to its text. Returns (failures, metric lines)."""
    f, registered = check_registry(reg, read)
    found, have = {}, {m: {} for m in METRICS}
    for path in sorted(go_files):
        typed, hidden = event_types(go_files[path])
        for s in [x for x in literals(go_files[path]) if LEGACY.fullmatch(x)] + typed:
            if s not in NOT_SUBJECTS: found.setdefault(s, path)
        n = sum(1 for line in go_files[path].splitlines() if STREAM_CALL.search(line) and not line.lstrip().startswith("//"))
        for m, v in zip(METRICS, (hidden, n)):
            if v: have[m][path] = v
    f += ["rule 3: %s (in %s) is not listed in %s" % (s, found[s], REGISTRY) for s in sorted(found) if s not in registered]
    f += check_baseline(base, have)
    return f, ["subjects=%d" % len(registered), "code_subjects=%d" % len(found)] + ["%s=%d" % (m, sum(have[m].values())) for m in METRICS]

def self_test():
    def e(subject="catalyst.x.made", envelope="legacy", delivery="at-least-once", **kw):
        return dict({"subject": subject, "envelope": envelope, "owner": "CTL", "producers": ["a"], "consumers": [],
                     "payload": "t.go", "delivery": delivery}, **kw)
    def reg(*entries, **kw):
        return dict({"schema": "openova.io/event-subjects/v1", "stream": "CATALYST_ORG", "subjects": list(entries) or [e()]}, **kw)
    def base(counts=None, allow=(), hidden=None):
        return {"schema": "openova.io/guard-baseline/v1", "allow": list(allow), "counts": {
            METRICS[0]: {"w.go": 1} if hidden is None else hidden, METRICS[1]: {"s.go": 1} if counts is None else counts}}
    go = {"a.go": 'const S = "catalyst.x.made"\n', "s.go": "func f() { js.CreateOrUpdateStream(ctx, cfg) }\n",
          "w.go": 'func (h *H) publishEvent(c context.Context, eventType string) {\n\te := events.NewEvent(eventType, "svc")\n}\n'}
    ok = {"metric": METRICS[1], "path": "s.go", "count": 1, "reason": "kept by another area", "owner": "BSS"}
    quiet = ("// \"catalyst.y.made\" js.AddStream(c)\n/* `catalyst.y.made` */ var r = '\"'\nvar w, g, t = \"catalyst.y.>\", \"catalyst.openova.io\", \"catalyst.y\"\n"
             '// h.publishEvent(ctx, "y.made", id)\nvar d = NewEvent("event.dead_letter", g)\nvar l = C{EventTypes: []string{"x.made"}, Topics: []string{"y.made"}}\n'
             'eventTypes := []string{\n\t// "y.made"\n\t"x.made",\n}\nh.publishEvent(ctx, "x.made", "y.made")\n')
    cases = [  # name, registry, Go files, baseline, text of a failure that must appear (None: must pass)
        ("the repository's shape passes", reg(), go, base(), None),
        ("comments, filters, the API group, short names and runes are no subjects", reg(), dict(go, c=quiet), base(), None),
        ("an allow entry and a foreign at-most-once JSON payload pass",
         reg(e(), e("catalyst.x.seen.v1", "foreign", "at-most-once", payload="p.json")), go, base({}, [ok]), None),
        ("rule 1: a wrong schema can fail", reg(schema="v0"), go, base(), "rule 1: schema"),
        ("rule 1: a wrong stream can fail", reg(stream="X"), go, base(), "rule 1: stream"),
        ("rule 1: an extra top-level key can fail", reg(extra=1), go, base(), "rule 1: the top-level keys"),
        ("rule 1: an empty list can fail", reg(subjects=[]), go, base(), "rule 1: subjects"),
        ("rule 1: a missing key can fail", reg({k: v for k, v in e().items() if k != "owner"}), go, base(), "rule 1: entry 1"),
        ("rule 1: an unknown envelope can fail", reg(e(envelope="plain")), go, base(), "envelope must be"),
        ("rule 1: a bad owner can fail", reg(e(owner="ctl")), go, base(), "owner must be"),
        ("rule 1: a doubled producer can fail", reg(e(producers=["a", "a"])), go, base(), "producers must be"),
        ("rule 1: an empty consumer name can fail", reg(e(consumers=[""])), go, base(), "consumers must be"),
        ("rule 1: a payload that is no file can fail", reg(e(payload="none.go")), go, base(), "is not a file"),
        ("rule 1: a payload that is no JSON can fail", reg(e(payload="bad.json")), go, base(), "not valid JSON"),
        ("rule 1: an unknown delivery can fail", reg(e(delivery="once")), go, base(), "delivery must be"),
        ("rule 1: at-most-once without foreign can fail", reg(e(), e("catalyst.x.seen.v1", "cloudevents", "at-most-once")), go, base(), "needs envelope foreign"),
        ("rule 2: a doubled subject can fail", reg(e(), e()), go, base(), "listed twice"),
        ("rule 2: a new subject without a version can fail", reg(e(), e("catalyst.x.seen", "cloudevents")), go, base(), "not a valid name"),
        ("rule 2: a legacy name with a capital can fail", reg(e(), e("catalyst.X.made")), go, base(), "not a valid name"),
        ("rule 3: a quoted literal can fail", reg(), dict(go, b='var s = "catalyst.y.made"\n'), base(), "rule 3: catalyst.y.made (in b)"),
        ("rule 3: a raw literal can fail", reg(), dict(go, b="var s = `catalyst.y.made`\n"), base(), "rule 3: catalyst.y.made"),
        ("rule 3: an event type in a NewEvent call can fail", reg(), dict(go, b='e := events.NewEvent("y.made", "svc", id)\n'), base(), "rule 3: catalyst.y.made (in b)"),
        ("rule 3: an order event type in a publishEvent call can fail", reg(), dict(go, b='h.publishEvent(ctx, "order.made", id)\n'), base(), "rule 3: catalyst.billing.order.made"),
        ("rule 3: an event type in a field list can fail", reg(), dict(go, b='c := C{EventTypes: []string{"x.made", "y.made"}}\n'), base(), "rule 3: catalyst.y.made"),
        ("rule 3: an event type in a list of several lines can fail", reg(), dict(go, b='eventTypes := []string{\n\t"x.made", // one\n\t"y.made",\n}\n'), base(), "rule 3: catalyst.y.made"),
        ("rule 4: a call with no event type literal can fail", reg(), dict(go, b='e := events.NewEvent(t, "svc", id)\n'), base(), "rule 4: b has 1 event_type_indirections"),
        ("rule 4: a list that is not written in the field can fail", reg(), dict(go, b="c := C{EventTypes: types}\n"), base(), "rule 4: b has 1 event_type_indirections"),
        ("rule 4: an event built without NewEvent can fail", reg(), dict(go, b='e := &events.Event{Type: "x.made"}\n'), base(), "rule 4: b has 1 event_type_indirections"),
        ("rule 4: a missing metric can fail", reg(), go, {"schema": "openova.io/guard-baseline/v1", "counts": {METRICS[1]: {"s.go": 1}}, "allow": []}, "rule 4: counts"),
        ("rule 4: a call in a new file can fail", reg(), dict(go, b="js.AddStream(cfg)\n"), base(), "rule 4: b has 1 stream_create_calls"),
        ("rule 4: a second call in a counted file can fail", reg(), dict(go, **{"s.go": go["s.go"] * 2}), base(), "s.go has 2"),
        ("rule 4: an entry above the calls can fail", reg(), go, base({"s.go": 2}), "lower its baseline entry"),
        ("rule 4: an entry of zero can fail", reg(), go, base({"s.go": 1, "x.go": 0}), "rule 4: counts"),
        ("rule 4: an allow entry without a reason can fail", reg(), go, base({}, [dict(ok, reason="short")]), "rule 4: an allow entry"),
        ("rule 4: an allow entry with an unknown metric can fail", reg(), go, base({}, [dict(ok, metric="other")]), "rule 4: an allow entry"),
        ("rule 4: allow entries above the calls can fail", reg(), go, base({}, [ok, ok]), "above its 1 stream_create_calls"),
        ("rule 4: a wrong baseline schema can fail", reg(), go, dict(base(), schema="v0"), "rule 4: the baseline"),
    ]
    files, bad = {"t.go": "package t\n", "p.json": "{}", "bad.json": "{"}, 0
    for name, r, g, b, want in cases:
        fails = judge(r, files.get, g, b)[0]
        good = not fails if want is None else any(want in x for x in fails)
        bad += not good
        print("%s %s%s" % ("ok  " if good else "FAIL", name, "" if good else " -- got: %s" % (fails or "no failure")))
    print("check-event-subjects: self-test %s: %d of %d cases as expected" % ("FAILED" if bad else "passed", len(cases) - bad, len(cases)))
    return 1 if bad else 0

def main(argv):
    if argv == ["--self-test"]: return self_test()
    if argv and (len(argv) != 2 or argv[0] != "--root"):
        print("usage: check-event-subjects.py [--self-test | --root <folder>]"); return 2
    root = argv[1] if argv else "."
    def read(path):
        try:
            with open(os.path.join(root, path), encoding="utf-8") as fh: return fh.read()
        except (OSError, UnicodeDecodeError): return None
    unread = [p for p in (REGISTRY, BASELINE) if read(p) is None]
    if unread:
        print("check-event-subjects: cannot judge: cannot read %s" % " and ".join(unread)); return 2
    try:
        import yaml
        reg, base = yaml.safe_load(read(REGISTRY)), json.loads(read(BASELINE))
        listed = subprocess.run(["git", "-C", root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.go"],
                                capture_output=True, check=True).stdout.decode("utf-8").split("\0")
    except Exception as err:  # a file that cannot be read means no judgement
        print("check-event-subjects: cannot judge: %s" % (str(err) or type(err).__name__)); return 2
    go = {p: t for p in listed if p.endswith(".go") and not p.endswith("_test.go") for t in [read(p)] if t is not None}
    fails, metrics = judge(reg, read, go, base)
    for line in ["FAIL " + x for x in fails] + metrics: print(line)
    return 1 if fails else 0

if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
