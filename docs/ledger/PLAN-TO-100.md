# The plan to 100%

Written 2026-08-10, after auditing all 50 rows that regressed from the June peak
and rebuilding the measurement system. Every number here is measured and
reproducible; none is asserted. Supersedes `WBS-TO-100.md`, which was built on a
denominator that turned out to be wrong.

---

## 1. Where we actually are

| | count |
|---|--:|
| test cases (frozen canon, `uat-testcases.csv`) | **286** |
| green today | 190 |
| **green with a covering artifact** | **130** |
| green on prose alone | 60 |
| not green | 96 |

**130/286 is the real number.** The other 60 greens cite text a walker typed, with
no screenshot, no command output, nothing a second person can open. They are the
rows that flap, because an unreproducible pass gets re-decided from scratch by the
next walker.

The gap to 100% is therefore **156 rows**, not 96.

---

## 2. What actually went wrong — five mechanisms, each measured

These are not opinions. Each one was proven this week and each one has a rule
attached in §4.

### 2.1 The ledger recorded verdicts that its own evidence contradicted

Three examples, quoted from the June cell that carries the ✅:

- Row 41 — clause says *"the **single** owner principal"*; the evidence reads
  `alongside uatb1-owner@omantel.biz`. Two principals, stamped green.
- Row 218 — clause needs card **and** wizard; evidence says the wizard half was
  `UI-gated (headless session-guard)`, i.e. never walked.
- Row 177 — clause needs the Re-run button *used*; evidence says
  `button-action fault on click tracked at row 176`.

### 2.2 Greens were stamped against a different layer than the clause names

Rows 33, 37, 62, 176, 188, 218, 223 graded config, kubectl or REST output as a
stand-in for a UI or MCP clause. Rows 212/213 cite `catalyst-api` REST routes while
the clause names MCP tools — the named surface was never exercised on either date.

### 2.3 Nine environment resets destroyed 886 banked green rows

06-21 −124 · 06-26 −108 · 06-28 −188 · 07-10 −85 · 07-15 −167 · 07-29 −77 ·
07-31 −133. Era peaks: 124 → 195 → 205 → 205 → 168 → 48 → 184 → 133 → 190. **The
ceiling has not moved since 24 June.** Every era spends itself re-earning the same
~190 rows and never reaches the ~96 that have never been green.

### 2.4 Checks that cannot fail — five found in one week

`runDRPreflight` hardcodes `Pass` for CR kinds with zero instances · step-08's
egress proof passes with 62 live `ghcr.io` tethers · the redeem limiter's branch is
unreachable if evaluated after `StripPrefix` · the console infers
`isolation=vcluster` from `kind=customer`, so a UI screenshot proves nothing · my
own artifact-coverage grep can never match a PNG.

### 2.5 One real platform regression, dated

Commit `53a57bff0` / merge `6f62061a6` (**2026-07-19**, #5246) put every region's
nodes in the console ELB pool while the per-Org listener writer stayed
host-region-only. Region B holds zero `*.<org>` listeners, so ~50% of per-Org
traffic hits a node with no listener and resets at TLS. Measured:
`console.uatco` 5/10 vs `console.hw292` 10/10 on the same VIP. Rows 87, 90, R16.

---

## 3. The plan

Four phases. Each has an exit gate that can fail.

### Phase A — fix what is known broken (no environment needed)

| item | rows freed | state |
|---|--:|---|
| **#5246** ELB pool vs per-Org listeners — emit listeners in every region in the pool, or scope the pool to regions that have them | 3 | **no fix exists** |
| **#5921** customer sign-in mail via `mail.openova.io` — a 9th sovereignty tether on the purchase path | 3 (84/20/23) | no fix |
| **#5919** cutover chart floor — the next prov must pin ≥ `0.1.171` so the durable pivot assert exists from the start | 9 | version floor only |
| **#4986** no producer for `CnpgPair`/`Continuum` CRs; `runDRPreflight` hardcodes `Pass` | 2 (57, G3) | design call |
| **#5920** retired Sandbox still sold in the catalog | 1 | delete entry |

**Gate A:** every fix has a test that fails without it. No exceptions — §2.4 is why.

### Phase B — close the 60 unbacked greens (current environment, no prov)

These need one artifact each, on the surface the clause names. Roughly half are
`sso` and `model`. **15 are browser-gated** and need a single authenticated session,
which also closes row 38 (the last ☐) and the SSO landing rows in one pass.

**Gate B:** `uat-artifact-audit.csv` shows `current_verdict_backed = YES` for every
green. Re-run the audit; it must print zero unbacked.

### Phase C — fire once, deliberately

Not before Phase A is merged and pinned. Preconditions, all checkable:

1. cutover chart ≥ `0.1.171` pinned in the bootstrap kit
2. #5246 fixed, or per-Org reachability accepted as knowingly broken
3. every merged fix verified as an **ancestor of the deployed image**, not merely
   merged — `git merge-base --is-ancestor <fix> <image-sha>`

**The cost is 190 re-walks.** That is the measured price of a prov and it is why
this happens once, not on the first defect found.

**Gate C:** cutover reaches `cutoverComplete=true` **and** step-08's egress proof
covers the SMTP host (else it cannot see #5921's tether).

### Phase D — walk to 286, then hold

Re-walk the 190, then attack the ~96 that have never been green — the set no era
has ever reached. `e2e-journey` is at 0%, `mcp` 33%, `placement` 29%.

**Gate D:** 286/286 with `proof_tier = ARTIFACT` on every row.

**Hold the environment.** Do not re-fire on the next defect. The treadmill in §2.3
is the single largest cause of lost progress.

---

## 4. Rules that stop this recurring

Each is derived from a specific failure this week, most of them mine.

1. **A verdict requires an artifact that shows the asserted surface.** Not a
   directory near it — row 86's greens cited a 720K folder whose 16 screenshots
   never showed the clause. Open the file; do not count links.
2. **Grade the layer the clause names.** If the clause says UI and you have
   kubectl, the row is NOT-OBSERVABLE, not green. §2.2 is 7 rows of this.
3. **Every positive claim carries a control** that shows the check could have come
   out the other way. The strongest evidence in this ledger is row R3's three-way
   control: allowed <1s, policy-denied 8s timeout, closed port 0.7s refused.
4. **Verify the deployed artifact, never the desired one.** `lastAttemptedRevision`,
   not `spec.chart.spec.version`. I read the wrong field and published a wrong
   correction within ten minutes of the original error.
5. **Source-present ≠ runtime-armed.** I read the redeem limiter in `ratelimit.go`
   and reported it "on by default"; it needed 20 concurrent requests to prove.
   And a rate-limit test without concurrency cannot fail — rule 3 again.
6. **Never infer from data you skipped.** I concluded "605 flips crossed a prov
   boundary" from rows my filter dropped for having no env. That is fabrication by
   omission.
7. **Chain the gate to the action:** `guard && commit && push`. A guard on its own
   line is decorative — I read past two failing guards and shipped anyway.
8. **Read the file's own header before parsing it.** `UAT.md` has an `Epic` column
   and a `Test case` column; I ignored both and derived worse values from ticket
   numbers.
9. **The denominator is frozen at 286 and never moves.** Reclassifying a row out of
   scope is not progress. The old floating STONE rose 0.6% on a day nothing passed.
10. **Agent reports are claims.** Re-run the decisive command yourself. Three agents
    caught real errors in my briefs this week; I caught real gaps in theirs.

---

## 5. What to watch instead of the score

**Artifact-backed green.** It has been flat at **130 for five days** while total
green drifted 181→190. That drift is measurement churn; 130 is the truth.

The score resets to near-zero on every prov (measured: 133 → 25 on 2026-08-03).
Track **train-completeness** — are the merged fixes actually in the deployed image
— because that is what survives a wipe.

---

## 6. Verify any of this yourself

```bash
bash scripts/uat-verify-reproducible.sh   # delete the sheet, rebuild from git, compare sha256
python3 scripts/uat-audit.py              # integrity gate, exit 1 on any violation
python3 scripts/uat-pivot.py --trend      # proven observations per cycle
python3 scripts/uat-artifact-audit.py     # per-row artifact coverage
```

Sources: `uat-raw.csv` (proven observations, one row per test case per cycle, with
the readable clause) · `uat-testcases.csv` (the frozen 286) ·
`uat-artifact-audit.csv` (per-row coverage) · `REGRESSION-VERDICT.md` (the 50-row
audit) · `UAT-TREADMILL-ANALYSIS.md` (the nine resets).
