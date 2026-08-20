#!/usr/bin/env python3
"""UAT ledger-identity guard — the executable form of UAT row 184.

Row 184 asserts the one property nothing else checks: that the frozen
denominator is INTACT and no clause changes silently. Until now it was
asserted by a human reading two files side by side, which is how it came to
be measured exactly once and drifted four ways afterwards.

WHY A DRIFTED CLAUSE IS NOT COSMETIC. `scripts/uat-backfill.py` computes each
row's identity window against the CANON text in `docs/ledger/uat-testcases.csv`.
When `UAT.md` and the canon disagree, that row's window collapses and the
backfill writes NO observations for it. The row does not read as *wrong* in
`uat-raw.csv` — it DISAPPEARS from it, and `scripts/uat-retest-policy.py` then
reads that absence as never-passed. So a one-word edit in one file silently
deletes a row's entire evidence history, and every downstream number moves.

LEG C IS BLIND TO A SHARED ERROR, WHICH IS WHY LEGS E AND F EXIST. Leg C
compares the two COPIES of a clause. It therefore reports green whenever both
copies are wrong in the same way, and that is not hypothetical: row G11
asserted an EIGHT-step sovereignty cutover for months while the shipped chart
ran ELEVEN, and `uat-testcases.csv` carried the same stale eight, so the two
files agreed and the guard could not see it (#6111, adjudicated in PR #6115).
Leg C also looks at ONE column. The Epic and Ticket columns were never compared
at all, and both had live drift the day legs E/F were written: row 109 read
`sso` in the ledger and `placement` in the canon, and row 192's ticket cell
carried link TEXT `#3925` over a URL pointing at `/issues/3996` — the identical
dead-pointer shape PR #6115 had just fixed on rows 187/188/189.

THE SIX LEGS, each measured separately so a failure names its own cause:

  A  UAT.md holds exactly 286 row-ID rows — the frozen denominator.
  B  the row-ID set is in BIJECTION with uat-testcases.csv, both directions.
  C  every row's clause text is IDENTICAL in both files.
  D  every row whose Evidence says RETIRED AND REPLACED has a
     docs/ledger/uat-retirements.csv entry carrying a non-empty old clause,
     new clause and ground.
  E  clause CLAIMS are checked against the REPO, not just against the other
     copy — the leg leg C structurally cannot be:
       E1 an `N-step` cutover claim must equal the number of steps the shipped
          `platform/self-sovereign-cutover/chart` actually runs;
       E2 a `<plural>.<group>` token whose group is an in-repo CRD group must
          name a plural those CRDs actually declare (a wrong group returns an
          empty listing that is indistinguishable from a real absence — row
          57's own METHOD NOTE records exactly that near-miss with
          `continuums.continuum.openova.io`).
  F  the columns leg C never compares: Epic is identical in both files, each
     ticket link's TEXT number equals its URL number, and every issue the canon
     names is present in the ledger's ticket cell.

WHAT LEG E DOES NOT CLAIM. It catches a wrong COUNT and a wrong API GROUP. It
cannot catch a clause that names a Kubernetes kind in prose — row 237 asserted
"the spine's continuumRef" and a walker duly ran `kubectl get spines`, getting
`the server doesn't have a resource type "spines"` for an object that never
existed. "spine" is an ordinary English word here; no regex separates that from
a real claim without inventing failures. That class is caught by adjudication,
not by this guard, and saying so is cheaper than a check that pretends to.

WHAT LEG F DOES NOT ENFORCE, measured rather than assumed: 18 rows carry MORE
issues in the ledger's ticket cell than the canon records, which is the
long-standing "canon keeps the primary ticket" convention rather than drift.
Leg F therefore asserts CONTAINMENT (canon ⊆ ledger) instead of equality, so a
dead pointer surviving in the canon still fails while the convention does not.

VACUITY GUARD, and it is not decoration. Legs B/C/D/E/F all have the shape "no
row is bad", which is trivially true on an empty parse — a drifted row regex
would make this guard pass LOUDEST at the moment it stopped checking anything.
So a scan that parses fewer than 250 rows is a FAIL, not a pass. Row 184's own
clause spells this out; it is reproduced here rather than paraphrased.

Legs E1 and E2 need the SAME protection one level down, because their
populations are not the row set but the handful of clauses that make a checkable
claim (2 and 2 today). Rewording those clauses would take each population to
zero and the leg would go green having read nothing. Both therefore carry their
own floor, print their own population, and FAIL at zero — as does the CRD
inventory E2 compares against, which is derived by scanning the tree and would
otherwise turn an empty scan into a universal pass.

ROW-ID NAMESPACES ARE DISTINCT. `1`, `R1`, `G1`, `W1` and `M1` are five
different rows. Conflating the bare-numeric namespace with `R#` manufactures a
false bijection failure — it did on the first hand-measurement of this row.

    python3 scripts/test_uat_clause_identity.py             # guard the ledger
    python3 scripts/test_uat_clause_identity.py --self-test # prove it goes red
"""
import argparse
import csv
import os
import re
import sys
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from uat_html_compat import to_pipe  # HTML-<table> ledger -> markdown rows

REPO = Path(__file__).resolve().parent.parent
UAT = REPO / "docs" / "ledger" / "UAT.md"
CANON = REPO / "docs" / "ledger" / "uat-testcases.csv"
RETIREMENTS = REPO / "docs" / "ledger" / "uat-retirements.csv"

# The frozen denominator. Moving this number moves the headline percentage
# without anything passing, which is the exact behaviour row 184 exists to
# prevent — so it is a constant here and a retirement is the only legitimate
# way to change what occupies a slot.
FROZEN_DENOMINATOR = 286

# A scan that parses fewer rows than this has lost its grip on the table and
# every "no row is bad" assertion below has gone vacuous.
VACUITY_FLOOR = 250

# `1` / `R1` / `G1` / `W1` / `M1` are five distinct rows.
ROW_ID = re.compile(r"^(?:[RGWM])?\d+$")
UNESCAPED_PIPE = re.compile(r"(?<!\\)\|")

RETIRED_MARKER = "RETIRED AND REPLACED"

# ── Leg E sources of truth, both in-repo ────────────────────────────────────
CUTOVER_TEMPLATES = REPO / "platform" / "self-sovereign-cutover" / "chart" / "templates"
CUTOVER_STEP = re.compile(r"cutover-step-(\d{2})")
# "the 11-step chain" / "an 8 step cutover", in either order relative to the
# word `cutover` and within one sentence of it.
STEP_CLAIM = re.compile(
    r"(\d+)[-\s]step\b[^.]{0,80}?cutover|cutover[^.]{0,80}?\b(\d+)[-\s]step\b", re.I
)
# `<plural>.<group>` where the group is dotted. Hostnames (console.openova.io,
# mail.openova.io) match the shape too and are filtered by requiring the group
# to be one an in-repo CRD actually DECLARES — which is why no hand-maintained
# allowlist of hostnames is needed, and why one cannot rot.
GVR_TOKEN = re.compile(r"\b([a-z][a-z0-9]*)\.((?:[a-z0-9-]+\.)+openova\.io)\b")
CRD_GROUP = re.compile(r"^\s{2}group:\s*(\S+)", re.M)
CRD_PLURAL = re.compile(r"^\s{4}plural:\s*(\S+)", re.M)

# Leg F — the ticket cell's markdown links.
TICKET_LINK = re.compile(r"\[#(\d+)\]\(https://github\.com/openova-io/openova/issues/(\d+)\)")

# Leg E populations below this are reading nothing and must FAIL, not pass.
STEP_CLAIM_FLOOR = 1
GVR_TOKEN_FLOOR = 1
CRD_GROUP_FLOOR = 3


def shipped_cutover_steps() -> set:
    """The step numbers the shipped cutover chart actually runs.

    Derived from the `cutover-step-NN` object names the chart templates carry,
    not from a constant in this file — a constant would just be a third copy of
    the number the ledger already got wrong twice.
    """
    steps = set()
    if not CUTOVER_TEMPLATES.is_dir():
        return steps
    for path in sorted(CUTOVER_TEMPLATES.rglob("*.yaml")):
        steps.update(CUTOVER_STEP.findall(path.read_text(encoding="utf-8", errors="ignore")))
    return steps


def crd_inventory() -> dict:
    """{group: {plural, ...}} for every in-repo CRD whose group is ours."""
    out = {}
    for path in REPO.rglob("*.yaml"):
        if ".git" in path.parts:
            continue
        try:
            text = path.read_text(encoding="utf-8", errors="ignore")
        except OSError:
            continue
        if "CustomResourceDefinition" not in text:
            continue
        # One file can hold several CRDs; split on the kind line so a group and
        # a plural are only ever paired within the same document.
        for doc in text.split("\nkind: CustomResourceDefinition"):
            g, p = CRD_GROUP.search(doc), CRD_PLURAL.search(doc)
            if g and p and g.group(1).endswith("openova.io"):
                out.setdefault(g.group(1), set()).add(p.group(1))
    return out


def normalise(text: str) -> str:
    """A clause is the same clause whether or not its pipes are markdown-escaped.

    UAT.md must escape a literal `|` as `\\|` or it ends the cell; the canon CSV
    is quoted and carries the bare character. Comparing them raw would report a
    drift that exists only in the transport, so the escape is undone on the
    markdown side before the comparison — and ONLY the escape. Backticks,
    case and whitespace inside the clause are content and are compared as-is,
    because a canon copy that lost its backticks is a lossy transcription and
    is exactly the drift class this guard caught first.
    """
    return text.replace("\\|", "|").strip()


def parse_uat(text: str):
    """Yield (row_id, clause, evidence, epic, ticket) for every row-ID row."""
    for line in text.split("\n"):
        if not line.startswith("|"):
            continue
        cells = UNESCAPED_PIPE.split(line)
        # | # | Epic | Ticket | Test case | Walk | Result | Evidence |
        # splits to a leading '', the 7 columns, and a trailing ''.
        if len(cells) < 8:
            continue
        row_id = cells[1].strip()
        if not ROW_ID.match(row_id):
            continue
        yield (row_id, normalise(cells[4]), cells[7] if len(cells) > 7 else "",
               cells[2].strip(), cells[3].strip())


def map_clause_cells(text: str, fn, only: str = None) -> str:
    """Rewrite the CLAUSE cell of every row-ID row (or just `only`) through fn.

    Self-test mutants have to land in the same cell the guard reads. A
    substitution over the raw markdown can rewrite a number inside an Evidence
    cell or match across a `|`, which desynchronises the two copies for a
    reason unrelated to the leg being proven — and the mutant then reports a
    guard defect that does not exist.
    """
    out = []
    for line in text.split("\n"):
        cells = UNESCAPED_PIPE.split(line)
        if line.startswith("|") and len(cells) >= 8 and ROW_ID.match(cells[1].strip()) \
                and (only is None or cells[1].strip() == only):
            cells[4] = fn(cells[4])
            line = "|".join(cells)
        out.append(line)
    return "\n".join(out)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--self-test", action="store_true")
    args = ap.parse_args()
    return self_test() if args.self_test else check()


def check() -> int:
    uat_rows = list(parse_uat(to_pipe(UAT.read_text())))
    canon = {r["row_id"]: normalise(r["test_case"]) for r in csv.DictReader(CANON.open())}

    failures = []

    # ── Vacuity guard FIRST — before any "no row is bad" assertion runs ──────
    print(f"  parsed {len(uat_rows)} row-ID rows from {UAT.relative_to(REPO)}")
    if len(uat_rows) < VACUITY_FLOOR:
        print(
            f"  FAIL  VACUITY — only {len(uat_rows)} rows matched the row regex "
            f"(floor {VACUITY_FLOOR}); every check below would pass on nothing."
        )
        return 1
    print(f"  PASS  vacuity — {len(uat_rows)} rows parsed, above the {VACUITY_FLOOR} floor")

    # ── Leg A — the frozen denominator ──────────────────────────────────────
    if len(uat_rows) != FROZEN_DENOMINATOR:
        failures.append(
            f"LEG A — UAT.md holds {len(uat_rows)} rows, not the frozen {FROZEN_DENOMINATOR}"
        )
        print(f"  FAIL  leg A: denominator is {len(uat_rows)}, want {FROZEN_DENOMINATOR}")
    else:
        print(f"  PASS  leg A: denominator frozen at {FROZEN_DENOMINATOR}")

    # ── Leg B — bijection with the canon ────────────────────────────────────
    uat_ids = {rid for rid, _, _, _, _ in uat_rows}
    only_uat = sorted(uat_ids - set(canon))
    only_canon = sorted(set(canon) - uat_ids)
    if only_uat or only_canon:
        failures.append(
            f"LEG B — row-ID sets differ: in UAT.md only {only_uat}; in canon only {only_canon}"
        )
        print(f"  FAIL  leg B: UAT-only {only_uat}, canon-only {only_canon}")
    else:
        print(f"  PASS  leg B: {len(uat_ids)} row IDs in bijection with the canon")

    # ── Leg C — clause identity ─────────────────────────────────────────────
    drifted = [(rid, clause) for rid, clause, _, _, _ in uat_rows
               if rid in canon and clause != canon[rid]]
    if drifted:
        failures.append(
            f"LEG C — {len(drifted)} rows' clause text differs between UAT.md and the canon: "
            + " ".join(rid for rid, _ in drifted)
        )
        print(f"  FAIL  leg C: {len(drifted)} clauses drifted — {[r for r, _ in drifted]}")
        for rid, clause in drifted:
            print(f"          row {rid}")
            print(f"            UAT.md : {clause[:160]}")
            print(f"            canon  : {canon[rid][:160]}")
    else:
        print(f"  PASS  leg C: all {len(uat_rows)} clauses identical in both files")

    # ── Leg D — every retirement is recorded ────────────────────────────────
    retired_in_ledger = sorted(
        rid for rid, _, evidence, _, _ in uat_rows if RETIRED_MARKER in evidence
    )
    records = {}
    for r in csv.DictReader(RETIREMENTS.open()):
        records[r["row_id"]] = r
    missing, hollow = [], []
    for rid in retired_in_ledger:
        rec = records.get(rid)
        if rec is None:
            missing.append(rid)
            continue
        # Assert on the VALUE, not the presence of the key — a retirement record
        # with an empty ground records nothing and must not satisfy the leg.
        if not all(rec.get(k, "").strip() for k in
                   ("old_clause", "new_clause", "ground_for_retirement")):
            hollow.append(rid)
    if missing or hollow:
        failures.append(
            f"LEG D — retirement records missing for {missing}; empty-field records for {hollow}"
        )
        print(f"  FAIL  leg D: missing {missing}, hollow {hollow}")
    elif not retired_in_ledger:
        # Say so out loud rather than printing a confident PASS over an empty
        # population. Leg D is "no row is bad", which nothing satisfies more
        # easily than nothing — and this leg has been in that state the whole
        # time: `uat-retirements.csv` holds records while NO ledger row carries
        # the marker in its Evidence, so the mapping the leg checks has never
        # actually been exercised on real data. Not a failure (a ledger may
        # legitimately carry no retirement stamp), but it must not read as one
        # more green among six.
        print(
            f"  VACUOUS  leg D: 0 rows carry '{RETIRED_MARKER}' in Evidence, so the "
            f"leg checked nothing — {len(records)} record(s) sit in "
            f"{RETIREMENTS.name} with no ledger row pointing at them. To give the "
            "leg a real population, stamp a retired row's Evidence with the marker."
        )
    else:
        print(
            f"  PASS  leg D: all {len(retired_in_ledger)} RETIRED AND REPLACED rows "
            "carry a complete retirement record"
        )

    # ── Leg E — clause claims against the REPO, not against the other copy ───
    # Both copies agreeing is exactly what leg C measures, so a clause that is
    # wrong in BOTH files is invisible to it. These two checks read the source
    # of truth instead. Each prints its own population and fails at zero.
    canon_clauses = {r["row_id"]: r["test_case"]
                     for r in csv.DictReader(CANON.open())}
    all_clauses = list(canon_clauses.items()) + [(rid, cl) for rid, cl, _, _, _ in uat_rows]

    shipped = shipped_cutover_steps()
    step_claims = [(rid, m.group(1) or m.group(2))
                   for rid, clause in all_clauses for m in STEP_CLAIM.finditer(clause)]
    bad_steps = sorted({(rid, n) for rid, n in step_claims if int(n) != len(shipped)})
    if not shipped:
        failures.append(
            "LEG E1 — the cutover chart enumerates NO steps; the source of truth is "
            f"unreadable at {CUTOVER_TEMPLATES.relative_to(REPO)} and this leg would "
            "pass on nothing"
        )
        print("  FAIL  leg E1: cutover chart enumerates 0 steps — source of truth unreadable")
    elif len(step_claims) < STEP_CLAIM_FLOOR:
        failures.append(
            f"LEG E1 — {len(step_claims)} clauses make an N-step cutover claim "
            f"(floor {STEP_CLAIM_FLOOR}); the leg is reading nothing"
        )
        print(f"  FAIL  leg E1: population {len(step_claims)}, below floor {STEP_CLAIM_FLOOR}")
    elif bad_steps:
        failures.append(
            f"LEG E1 — {len(bad_steps)} clause(s) assert a step count the shipped chart "
            f"does not run ({len(shipped)}): " + " ".join(f"{r}={n}" for r, n in bad_steps)
        )
        print(f"  FAIL  leg E1: shipped chart runs {len(shipped)} steps, clauses claim {bad_steps}")
    else:
        print(f"  PASS  leg E1: {len(step_claims)} step-count claim(s) all match the "
              f"{len(shipped)} steps the shipped cutover chart runs")

    crds = crd_inventory()
    gvr_claims = [(rid, m.group(0), m.group(1), m.group(2))
                  for rid, clause in all_clauses for m in GVR_TOKEN.finditer(clause)
                  if m.group(2) in crds]
    bad_gvr = sorted({(rid, tok) for rid, tok, plural, group in gvr_claims
                      if plural not in crds[group]})
    if len(crds) < CRD_GROUP_FLOOR:
        failures.append(
            f"LEG E2 — only {len(crds)} in-repo CRD group(s) found (floor {CRD_GROUP_FLOOR}); "
            "the inventory this leg compares against has gone empty, which would turn "
            "every clause into a skip"
        )
        print(f"  FAIL  leg E2: CRD inventory holds {len(crds)} groups, below floor {CRD_GROUP_FLOOR}")
    elif len(gvr_claims) < GVR_TOKEN_FLOOR:
        failures.append(
            f"LEG E2 — {len(gvr_claims)} clauses name a `<plural>.<group>` for an in-repo "
            f"CRD group (floor {GVR_TOKEN_FLOOR}); the leg is reading nothing"
        )
        print(f"  FAIL  leg E2: population {len(gvr_claims)}, below floor {GVR_TOKEN_FLOOR}")
    elif bad_gvr:
        failures.append(
            "LEG E2 — clause(s) name a resource the shipped CRDs do not declare: "
            + " ".join(f"{r}:{t}" for r, t in bad_gvr)
        )
        print(f"  FAIL  leg E2: {bad_gvr}")
    else:
        print(f"  PASS  leg E2: {len(gvr_claims)} `<plural>.<group>` claim(s) resolve against "
              f"the {len(crds)} in-repo CRD groups")

    # ── Leg F — the columns leg C never compares ────────────────────────────
    canon_meta = {r["row_id"]: (r["epic"].strip(), r["ticket"].strip())
                  for r in csv.DictReader(CANON.open())}
    epic_drift, link_broken, canon_orphan = [], [], []
    for rid, _clause, _ev, epic, ticket in uat_rows:
        c_epic, c_ticket = canon_meta.get(rid, ("", ""))
        if epic != c_epic:
            epic_drift.append((rid, epic, c_epic))
        urls = set()
        for m in TICKET_LINK.finditer(ticket):
            urls.add(m.group(2))
            if m.group(1) != m.group(2):
                link_broken.append((rid, m.group(1), m.group(2)))
        # CONTAINMENT, not equality — the ledger routinely carries follow-up
        # issues the canon does not. An issue the CANON names that the ledger
        # does not is the dead-pointer shape and is a failure.
        missing_ticket = {t for t in re.split(r"[,\s]+", c_ticket) if t} - urls
        if missing_ticket:
            canon_orphan.append((rid, sorted(missing_ticket)))
    if epic_drift or link_broken or canon_orphan:
        failures.append(
            f"LEG F — epic drift {epic_drift}; ticket link text/URL mismatch {link_broken}; "
            f"canon issues absent from the ledger cell {canon_orphan}"
        )
        print(f"  FAIL  leg F: epic drift {epic_drift}, broken links {link_broken}, "
              f"canon-only tickets {canon_orphan}")
    else:
        print(f"  PASS  leg F: {len(uat_rows)} epics identical, every ticket link's text "
              "matches its URL, every canon issue present in the ledger cell")

    if failures:
        print()
        for f in failures:
            print(f"::error::{f}")
        print(f"\nLEDGER IDENTITY BROKEN — {len(failures)} of 4 legs failed.")
        return 1
    # The summary must not claim a leg that ran on nothing. "retirements recorded"
    # is exactly the sentence a reader greps for, and with leg D vacuous it would
    # assert a check that never touched a row.
    retirements_claim = ("retirements recorded" if retired_in_ledger
                         else "retirement mapping UNEXERCISED (leg D vacuous)")
    print(f"\nLEDGER IDENTITY OK — 286 rows, bijective, clause-identical, {retirements_claim}.")
    return 0


def self_test() -> int:
    """Prove the guard can go red, and for the RIGHT reason.

    Three mutations, each aimed at a different leg, applied to in-memory copies.
    A guard whose self-test only asserts a non-zero exit is satisfied by a
    crash; each case below therefore asserts the failure MESSAGE names its leg.
    """
    import io
    import contextlib

    global UAT, CANON, RETIREMENTS
    real_uat, real_canon, real_ret = UAT, CANON, RETIREMENTS
    tmp = REPO / "scripts" / ".clause_identity_selftest"
    tmp.mkdir(exist_ok=True)
    ok = True
    try:
        uat_text = to_pipe(real_uat.read_text())
        canon_rows = list(csv.DictReader(real_canon.open()))
        ret_text = real_ret.read_text()

        def run(uat_t, canon_r, ret_t):
            (tmp / "UAT.md").write_text(uat_t)
            with (tmp / "canon.csv").open("w", newline="") as fh:
                w = csv.DictWriter(fh, fieldnames=["row_id", "epic", "ticket", "test_case"])
                w.writeheader()
                for r in canon_r:
                    w.writerow({k: r[k] for k in w.fieldnames})
            (tmp / "ret.csv").write_text(ret_t)
            globals()["UAT"] = tmp / "UAT.md"
            globals()["CANON"] = tmp / "canon.csv"
            globals()["RETIREMENTS"] = tmp / "ret.csv"
            buf = io.StringIO()
            with contextlib.redirect_stdout(buf):
                rc = check()
            return rc, buf.getvalue()

        # 0 — the real ledger must be the control. If the tree is red, say so
        #     plainly instead of letting the mutants below prove nothing.
        rc, out = run(uat_text, canon_rows, ret_text)
        control_clean = rc == 0
        print(f"[self-test] control (real ledger) exit={rc} "
              f"{'clean' if control_clean else '— tree is currently RED, mutants still checked'}")

        # 1 — leg C: strip a backtick out of one canon clause.
        mutated = [dict(r) for r in canon_rows]
        target = next(r for r in mutated if "`" in r["test_case"])
        target["test_case"] = target["test_case"].replace("`", "", 2)
        rc, out = run(uat_text, mutated, ret_text)
        if rc == 0 or "LEG C" not in out:
            print("[self-test] FAIL — a drifted canon clause did not trip leg C")
            ok = False
        else:
            print(f"[self-test] PASS — leg C catches a drifted clause (row {target['row_id']})")

        # 2 — leg B/A: delete one canon row.
        shortened = [dict(r) for r in canon_rows[:-1]]
        rc, out = run(uat_text, shortened, ret_text)
        if rc == 0 or "LEG B" not in out:
            print("[self-test] FAIL — a missing canon row did not trip leg B")
            ok = False
        else:
            print("[self-test] PASS — leg B catches a canon row that vanished")

        # 3 — leg D. The mutant must be IN leg D's population, since the leg only
        #     looks at rows whose Evidence carries the marker; mutating an
        #     out-of-population row would produce a green proving nothing.
        #
        #     This used to BORROW a member from the live ledger, and that made the
        #     control's existence contingent on ledger content. It went red on
        #     2026-08-13 (#6248) when the population reached zero, and the way it
        #     reached zero is the more interesting half: leg D's entire population
        #     had been ONE row, 184 — the META row describing this guard, whose
        #     Evidence merely QUOTED the marker while explaining what leg D does.
        #     No actual retired row has ever carried it. So the hw296 walk
        #     re-stamped 184's evidence with a fresh stamp, the quotation went with
        #     it, and a leg that had never had a real member lost its only
        #     accidental one.
        #
        #     So the fixture is SYNTHESIZED here instead. It marks a ledger row
        #     that genuinely has a retirement record, which puts a real member in
        #     the population without touching the denominator, the canon or the
        #     bijection — and it runs BOTH directions, because "the marked row
        #     fails" proves nothing unless the same row passes with its ground
        #     intact.
        ret_rows = list(csv.DictReader(io.StringIO(ret_text)))
        ledger_ids = {rid for rid, _, _, _, _ in parse_uat(uat_text)}
        seed = next(
            (r for r in ret_rows
             if r["row_id"] in ledger_ids
             and all(r.get(k, "").strip() for k in
                     ("old_clause", "new_clause", "ground_for_retirement"))),
            None,
        )
        if seed is None:
            print("[self-test] FAIL — no retirement record names a row that is in the "
                  "ledger with all three fields populated; leg D cannot be exercised")
            ok = False
        else:
            rid = seed["row_id"]

            def mark(text, row_id):
                """Put row_id INTO leg D's population by marking its Evidence cell."""
                out_lines = []
                for line in text.split("\n"):
                    if re.match(rf"^\|\s*{re.escape(row_id)}\s*\|", line):
                        f = re.split(r"(?<!\\)\|", line.rstrip())
                        if len(f) > 7:
                            f[7] = f[7].rstrip() + f" {RETIRED_MARKER}. "
                            line = "|".join(f)
                    out_lines.append(line)
                return "\n".join(out_lines)

            marked_uat = mark(uat_text, rid)
            if RETIRED_MARKER not in dict(
                (r, e) for r, _, e, _, _ in parse_uat(marked_uat)
            ).get(rid, ""):
                print(f"[self-test] FAIL — could not put row {rid} into leg D's "
                      "population; the fixture, not the guard, is broken")
                ok = False
            else:
                # DIRECTION 1 — marked, record intact: leg D must stay GREEN.
                # Without this the mutant below could be failing because marking a
                # row breaks something else entirely.
                rc, out = run(marked_uat, canon_rows, ret_text)
                if "LEG D" in out:
                    print(f"[self-test] FAIL — leg D tripped on row {rid} whose "
                          "retirement record is COMPLETE; it rejects valid ledgers")
                    ok = False
                else:
                    print(f"[self-test] PASS — CONTROL: a marked row with a complete "
                          f"record leaves leg D green (row {rid})")

                # DIRECTION 2 — same row, ground hollowed: leg D must go RED.
                hollowed = [dict(r) for r in ret_rows]
                for r in hollowed:
                    if r["row_id"] == rid:
                        r["ground_for_retirement"] = ""
                sio = io.StringIO()
                w = csv.DictWriter(sio, fieldnames=list(ret_rows[0].keys()))
                w.writeheader()
                w.writerows(hollowed)
                rc, out = run(marked_uat, canon_rows, sio.getvalue())
                if rc == 0 or "LEG D" not in out:
                    print("[self-test] FAIL — an empty ground did not trip leg D")
                    ok = False
                else:
                    print("[self-test] PASS — leg D catches a retirement record with "
                          f"an empty ground (row {rid})")

        # 4 — vacuity: a ledger the regex cannot read must FAIL, not pass.
        rc, out = run(uat_text.replace("\n|", "\nX|"), canon_rows, ret_text)
        if rc == 0 or "VACUITY" not in out:
            print("[self-test] FAIL — an unparseable ledger did not trip the vacuity guard")
            ok = False
        else:
            print("[self-test] PASS — vacuity guard catches a table the regex cannot read")

        # 5 — leg E1: put the ledger's OWN historical error back. This is the
        #     exact mutant leg C could not see, because it is applied to BOTH
        #     copies at once: G11 asserted an 8-step cutover and the canon
        #     agreed with it, so the two files were identical and wrong.
        step_row = next((r for r in canon_rows if STEP_CLAIM.search(r["test_case"])), None)
        if step_row is None:
            print("[self-test] FAIL — no clause makes a step-count claim; leg E1 has no "
                  "population and must not be trusted")
            ok = False
        else:
            shipped_n = len(shipped_cutover_steps())
            wrong = str(shipped_n - 3)

            def bend_steps(text):
                return STEP_CLAIM.sub(
                    lambda m: m.group(0).replace(m.group(1) or m.group(2), wrong, 1), text)

            mutated = [dict(r) for r in canon_rows]
            for r in mutated:
                r["test_case"] = bend_steps(r["test_case"])
            # Applied CELL-WISE, not over the whole file. A sub across the raw
            # markdown could rewrite a number inside an Evidence cell, or match
            # across a cell boundary, and the two copies would then stop being
            # identical for a reason that has nothing to do with E1 — the leg-C
            # assertion below would go red and report a mutant defect as a guard
            # defect.
            mutant_uat = map_clause_cells(uat_text, bend_steps)
            rc, out = run(mutant_uat, mutated, ret_text)
            if rc == 0 or "LEG E1" not in out:
                print("[self-test] FAIL — a step count wrong in BOTH files did not trip leg E1 "
                      "(this is the shared-error case leg C is blind to)")
                ok = False
            elif "LEG C" in out:
                print("[self-test] FAIL — the E1 mutant also tripped leg C, so it does not "
                      "prove E1 catches what C cannot")
                ok = False
            else:
                print(f"[self-test] PASS — leg E1 catches a {wrong}-step claim agreed by BOTH "
                      f"files against a chart that runs {shipped_n}, with leg C green")

        # 6 — leg E2: bend a resource token onto a group the CRDs do not
        #     declare. `continuums.continuum.openova.io` is the wrong-group
        #     probe row 57's METHOD NOTE records returning a false empty.
        crd_groups = crd_inventory()
        gvr_row = next((r for r in canon_rows
                        if any(m.group(2) in crd_groups
                               for m in GVR_TOKEN.finditer(r["test_case"]))), None)
        if gvr_row is None:
            print("[self-test] FAIL — no clause names an in-repo CRD group; leg E2 has no "
                  "population and must not be trusted")
            ok = False
        else:
            mutated = [dict(r) for r in canon_rows]
            hit = next(r for r in mutated if r["row_id"] == gvr_row["row_id"])
            m = next(mm for mm in GVR_TOKEN.finditer(hit["test_case"])
                     if mm.group(2) in crd_groups)
            broken = "nosuchplural." + m.group(2)
            hit["test_case"] = hit["test_case"].replace(m.group(0), broken, 1)
            mutant_uat = map_clause_cells(
                uat_text,
                lambda t, _r=hit["row_id"]: t.replace(m.group(0), broken, 1),
                only=hit["row_id"])
            rc, out = run(mutant_uat, mutated, ret_text)
            if rc == 0 or "LEG E2" not in out:
                print("[self-test] FAIL — a resource the CRDs do not declare did not trip leg E2")
                ok = False
            else:
                print(f"[self-test] PASS — leg E2 catches `{broken}` (row {hit['row_id']})")

        # 7 — leg F: drift one canon EPIC. Leg C compares clause text only, so
        #     this mutant must reach F and must NOT reach C.
        mutated = [dict(r) for r in canon_rows]
        victim_epic = mutated[0]
        victim_epic["epic"] = victim_epic["epic"] + "-drifted"
        rc, out = run(uat_text, mutated, ret_text)
        if rc == 0 or "LEG F" not in out:
            print("[self-test] FAIL — a drifted canon epic did not trip leg F")
            ok = False
        elif "LEG C" in out:
            print("[self-test] FAIL — the epic mutant tripped leg C, so leg F proves nothing")
            ok = False
        else:
            print(f"[self-test] PASS — leg F catches an epic that drifted between the files "
                  f"(row {victim_epic['row_id']})")

        # 8 — leg F: point a ticket link's URL at a different issue from its
        #     text. This is the 187/188/189 shape — the number a reader SEES
        #     and the page they REACH were two different issues.
        mutant_uat = uat_text.replace(
            "[#3374](https://github.com/openova-io/openova/issues/3374)",
            "[#3374](https://github.com/openova-io/openova/issues/9999)", 1)
        if mutant_uat == uat_text:
            print("[self-test] FAIL — could not construct a ticket-link mutant; leg F's "
                  "link check is unproven")
            ok = False
        else:
            rc, out = run(mutant_uat, canon_rows, ret_text)
            if rc == 0 or "LEG F" not in out:
                print("[self-test] FAIL — a link whose text and URL name different issues "
                      "did not trip leg F")
                ok = False
            else:
                print("[self-test] PASS — leg F catches a ticket link whose text and URL "
                      "name different issues")
    finally:
        globals()["UAT"], globals()["CANON"], globals()["RETIREMENTS"] = (
            real_uat, real_canon, real_ret,
        )
        for p in tmp.glob("*"):
            p.unlink()
        tmp.rmdir()

    print("\nSELF-TEST " + ("OK — the guard goes red for each leg." if ok else "FAILED."))
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
