"""Shared reader for ADR-0015 D9 acceptance records and the function-row list.

One module, two callers, so they cannot disagree about what a valid citation
is:

  * scripts/uat-confidence.py         — stamps `evidence_class` on observations
  * scripts/check-function-rows-cite-acceptance.py — refuses a ✅ on a
    function-clause row that does not cite a passing acceptance run

An acceptance record is the JSON that `scripts/acceptance-sovereign.sh <env>
--record` writes to docs/ledger/acceptance/<env>-<run_id>.json
(tests/acceptance/sovereign). The suite writes schema
`openova.io/acceptance-sovereign/v2`; the records it wrote before are
`openova.io/acceptance-sovereign/v1`. Both are read here: the fields this
module reads (`env`, `mode`, `journeys`) are the same in both.

A ledger row cites a run by writing the token `acceptance:<run_id>` in its
evidence cell, e.g. `acceptance:20261108T101500Z-3f2a`.
"""
import csv
import json
import os
import re

SCHEMA = "openova.io/acceptance-sovereign/v1"
SCHEMA_V2 = "openova.io/acceptance-sovereign/v2"
SCHEMAS = (SCHEMA, SCHEMA_V2)

# run_id = <UTC yyyymmddThhmmssZ>-<4 hex>, as suite.NewRunID mints it.
CITATION = re.compile(r"\bacceptance:(\d{8}T\d{6}Z-[0-9a-f]{4})\b")

EVIDENCE_CLASSES = ("function", "state", "presence")

# Evidence that names a measured cluster/API state but no acceptance run. Only
# consulted when no valid citation exists; deliberately broad — the point of
# the split is function vs not-function, and state vs presence is advisory.
STATE_HINT = re.compile(
    r"(?i)\b(kubectl|psql|curl|HTTP\s*\d{3}|\d{3}\s*OK|Ready=|status\.|phase|"
    r"conditions?|readyInstances|currentPrimary|pg_is_in_recovery|log line|wire)\b")


def function_rows(path):
    """{row_id: [journeys]} from docs/ledger/uat-function-rows.csv.

    A missing file yields {} — no row is a function row — which every caller
    must treat as "nothing to gate", never as "everything passes": the CSV is
    checked in next to the ledger and the gate's self-test asserts it parses.
    """
    if not os.path.exists(path):
        return {}
    out = {}
    with open(path, newline="", encoding="utf-8") as fh:
        for r in csv.DictReader(fh):
            rid = (r.get("row_id") or "").strip()
            if not rid or rid.startswith("#"):
                continue
            out[rid] = [j.strip() for j in (r.get("journeys") or "").split("+") if j.strip()]
    return out


def load_record(acceptance_dir, env, run_id):
    """The record for (env, run_id), or None if it does not exist / is unreadable."""
    p = os.path.join(acceptance_dir, f"{env}-{run_id}.json")
    if not os.path.exists(p):
        return None
    try:
        with open(p, encoding="utf-8") as fh:
            return json.load(fh)
    except (OSError, ValueError):
        return None


def citation_verdict(evidence, env, journeys, acceptance_dir):
    """(ok, reason) — does `evidence` cite a passing acceptance run on `env`
    that covers every journey in `journeys`?

    Every refusal names its reason, so a red gate says exactly what to fix.
    """
    ids = CITATION.findall(evidence or "")
    if not ids:
        return False, "no `acceptance:<run_id>` citation in the evidence cell"
    reasons = []
    for rid in ids:
        rec = load_record(acceptance_dir, env, rid)
        if rec is None:
            reasons.append(f"{rid}: no record docs/ledger/acceptance/{env}-{rid}.json")
            continue
        if rec.get("schema") not in SCHEMAS:
            reasons.append(f"{rid}: schema {rec.get('schema')!r} is not one of {', '.join(SCHEMAS)}")
            continue
        if rec.get("env") != env:
            reasons.append(f"{rid}: record is for env {rec.get('env')!r}, ledger header is {env}")
            continue
        if rec.get("mode") != "run":
            reasons.append(f"{rid}: a {rec.get('mode')!r} record certifies nothing")
            continue
        js = rec.get("journeys") or {}
        missing = [j for j in journeys if js.get(j) != "PASS"]
        if missing:
            got = ", ".join("%s=%s" % (j, js.get(j, "absent")) for j in missing)
            reasons.append(f"{rid}: journeys {got}")
            continue
        return True, f"{rid} PASS ({'+'.join(journeys) or 'any'})"
    return False, "; ".join(reasons)


def classify(row_id, evidence, env, frows, acceptance_dir):
    """evidence_class for one observation: function | state | presence.

    `function` only when the evidence cites a passing acceptance run on this
    env covering the row's journeys (or, for a row not on the function list,
    any passing run). Otherwise `state` if it names a measured state, else
    `presence`.
    """
    js = frows.get(row_id, [])
    ok, _ = citation_verdict(evidence, env, js, acceptance_dir)
    if ok:
        return "function"
    if STATE_HINT.search(evidence or ""):
        return "state"
    return "presence"
