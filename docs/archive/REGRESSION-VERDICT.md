# Why the 2026-06-27 peak does not survive contact with today's ledger

**41 of the 50 rows that were ✅ on 2026-06-27 and are not ✅ today have been audited
one by one, against the June source tree and against live hw292.** Three agents ran
in parallel; I re-ran the decisive claims myself before recording any of them.

## Result

| verdict | rows |
|---|--:|
| **PLATFORM-BROKE** | **0** |
| ASSERTION-TIGHTENED | 23 |
| EVIDENCE-WEAKER-ONLY (June graded a lower layer) | 6 |
| ENV-DIFFERENT | 6 |
| SUPERSEDED-BY-DECISION | 3 |
| CAUSE-NOT-DETERMINED | 3 |

Not one row in 41 was shown to be a platform regression. The remaining 9 (`funnel`)
are still in audit.

## The handover cluster — one line of code explains three rows

Rows 96, 123 and 178 all fail with `401 {"error":"invalid issuer"}` when the Sovereign
redeems a token it just minted itself. Verified:

    auth_handover.go:232   const expectedIss = "https://console.openova.io"   (present on 2026-06-27)
    live hw292             CATALYST_HANDOVER_JWT_ISSUER = https://console.hw292.omani.works
    written by exactly one thing:
    platform/self-sovereign-cutover/chart/templates/07-catalyst-api-env-patch-job.yaml:910

The literal is hardcoded to the mothership. Pre-cutover the override is unset and the
fallback equals it, so the same binary passes. **Cutover pivots the issuer and the
Sovereign starts rejecting its own tokens.** June's environment was pre-cutover (its
G11 row reads `☐`, unwalked), so this could not have surfaced then — and rows 123 and
178 never redeemed a handover URL at all when they were stamped ✅.

## The three findings that settle it

Each is quoted from the **June cell itself** — the same cell that carries the ✅.

**Row 41** — clause: *"Users lists **the single** owner principal"*. June's evidence, verbatim:

> `alongside uatb1-owner@omantel.biz)`

A realm holding the owner *alongside* a second account is a two-principal realm.
The word "single" was already false at the moment ✅ was stamped.

**Row 218** — clause has two halves: the card is present AND the install wizard opens.
June's evidence, verbatim:

> `UI-gated (headless session-guard)`

June says in its own words that the wizard half was never walked, and stamped ✅ on
the controller half alone.

**Row 177** — clause: *"Use the same Re-run button"*. June's evidence, verbatim:

> `button-action fault on click tracked at row 176`

The ✅ names a live fault in the very control it is grading.

## The recurring shapes

- **The failing condition predates the ✅.** Row 48: the code that disables the option
  landed 7 days before the green. Rows 67/69: the `n/a — bootstrap component` string
  was in `TopologyTab.tsx:616` on 2026-06-27. Row 5: the June cell records `tier=org`,
  the exact value that fails today. Row 19: the apps grid was never keyed on
  Application CRs on either date. Row 29: the `if (hasCatalystSession()) return`
  short-circuit was already ahead of the whoami probe, and the June marker carried no
  age at all. Row 223: `requireDeployment` and its error string are byte-identical in
  the June tree, and the June bearer never minted `deployment_id`.
- **June graded a lower layer and said so.** Rows 33, 37, 62, 176, 188, 218, 223 —
  config, kubectl or REST evidence standing in for a UI or MCP clause the walker
  openly stated was not reachable.
- **Stale-environment carry-over.** Rows 53, 176, 223 still carry hw291 stamps; hw291
  was wiped 2026-08-03. Row 53 has no hw292 measurement at all.

## What is genuinely open

- **Row 219** — reachability flaps at TLS on a single VIP: `agenity.uatco.omani.homes`
  returned 000 on 4 of 10 fresh-TCP loads while `console.hw292.omani.works` on the
  identical IP returned 000 on 0 of 10, and a nonexistent host fails differently
  (curl rc=6). The per-Org listeners are **present** on the Gateway, contradicting the
  ledger's stated mechanism (#5635). Cause not isolated.
- **Rows 1 and 4** — the code paths are byte-identical to June and the fixes are
  confirmed *not* deployed, so the August behaviour cannot be a code regression; why
  it differs is a session/data-state question the wiped June environment cannot answer.

## Consequence

"205 green on 2026-06-27, 188 today" is not a story about the platform getting worse.
It is a story about the ledger getting stricter, plus environments being wiped nine
times. The right comparison was never peak-to-peak; it is **artifact-backed green**,
which stands at **128 of 189** today and was **1 of 205** on 2026-06-27.

Sources: `docs/ledger/uat-regressions-since-peak.csv` (the 50-row list),
`docs/ledger/uat-artifact-audit.csv` (per-row artifact coverage).
