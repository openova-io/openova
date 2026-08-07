# Challenging the 45 ⛔ rows — 11 are not blocked, and the headline number is 3.3 points optimistic

**Date:** 2026-08-07 · **Env:** hw292 (`catalyst-api fad88bd`, built 2026-08-02) · **Ledger:** `docs/ledger/UAT.md` @ 286 data rows

`⛔` has one meaning in this ledger: **the assertion is superseded by a merged,
founder-approved design decision and can never go green as written.** That is
why the STONE metric drops ⛔ from the denominator — you cannot be marked down
for failing a test that no longer describes the product.

That exclusion is only honest if every ⛔ actually earns it. This is the audit.

## Method

Two passes, both mechanical:

1. Split the ⛔ set by whether the evidence cell **cites a decision** (an issue
   number for the merged change that voided the row).
2. Read the uncited ones in full and classify what the evidence actually says.

Status is read from the **6th pipe-delimited cell**, never by searching the line
— several ✅ rows quote ⛔/❌ inside their prose.

## Result

| | count |
|---|---|
| ⛔ citing a design decision | **33** |
| ⛔ citing nothing at all | **12** |

The 33 are well-founded; the two largest are #4325 (de-vcluster of the
mgmt/rtz/dmz planes) and #3642 (host-bridge generator), voiding 12 rows each.
No challenge there.

The 12 uncited rows are where it falls apart. Only **one** is genuinely voided:

### Genuinely ⛔ — 1

- **R19** — the Sandbox concept was removed by the founder on 2026-06-30 and its
  capability succeeded by `bp-openova-mcp`. Live check agrees: zero
  sandbox-controller pods. The row's premise no longer exists. Valid, and it
  should carry the decision reference it currently lacks.

### Not ⛔ — walkable, needs a WRITE that is already authorised — 9

| row | the stated reason |
|---|---|
| G7 | two Org creations — *"out of scope for a read-only walk"* |
| 85 | sign-up mutation |
| 93 | mint a 2nd voucher (a POST) |
| 94 | depends on row 93's second Org |
| 95 | depends on row 93/94's second Org |
| 5, 6, 10, 11 | *"the precondition does not exist"* — needs a customer Org, which the funnel creates |

Every one of these is blocked by the **walk method**, not by the product. The
recurring phrase is *"out of scope for a read-only walk"* — a restriction the
walker imposed on itself. It contradicts the founder's standing rule that
Sovereign and deployment mutations are fully autonomous and need no approval.
Marking them ⛔ converts a decision not to walk into a permanent product
exemption, and then removes them from the denominator so the score rises.

Rows 5/6/10/11 are the clearest case: they need exactly one customer
Organization to exist. The funnel that creates one is itself green through the
review step. This is one walk, not a design impossibility.

### Not ⛔ — misfiled — 1

- **G5** — the janitor is a **mothership** surface. The evidence verifies zero
  janitor objects on hw292 and concludes *"cannot be walked on hw292"*, which is
  true and beside the point: it is walkable on the mothership. "Not on this env"
  is a routing fact, not a voided assertion.

### Not ⛔ — re-wordable — 1

- **63** — the grafana premise is absent, but the same evidence cell names an
  application that **does** reproduce it (`uatcorp/uatwalk-ahs-07300830`,
  declaring active-hot-standby with zero backing). The assertion is sound; only
  the fixture named in it is stale.

## What this does to the score

| denominator | figure | what it assumes |
|---|---|---|
| as quoted | **181/239 = 75.7%** | all 45 ⛔ are genuinely void |
| honest | **181/250 = 72.4%** | the 11 challenged rows return to the denominator |

**The headline is 3.3 points optimistic**, and the inflation comes entirely from
rows dismissed rather than walked. The raw figure (181/286 = 63.3%) is
unaffected — it never excluded anything.

## Next actions

1. **Reclassify the 9 write-gated rows** ⛔ → ☐ and walk them with mutation.
   They are one funnel run plus one voucher mint away, not a design change.
   Sequencing note: row 93 warns that consuming the single existing voucher
   destroys the fixture rows 75/78 depend on — so mint the second voucher first
   rather than spending the first.
2. **Re-file G5** against the mothership rather than hw292.
3. **Re-word 63** onto the app that reproduces its premise.
4. **Add the missing decision reference to R19** so the one valid ⛔ in this set
   is auditable like the other 33.

Until 1–3 land, quote **72.4%**, or quote 75.7% and say in the same breath that
it excludes 11 rows nobody has walked.

## Why this was worth doing

The ⛔ set is the only category that changes the denominator, which makes it the
only category where a wrong call silently improves the score. Every other
misclassification is self-correcting: a row wrongly marked ❌ gets walked and
flips. A row wrongly marked ⛔ is never looked at again.

Nine of these eleven were marked blocked because a walker declined to perform a
write it was authorised to perform. That is the specific failure mode worth
naming — not dishonesty, just a read-only habit hardening into a permanent
product exemption over several sessions.
