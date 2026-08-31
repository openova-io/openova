# bp-chargeback — pre-Sovereign UI walk evidence (2026-08-31)

Published image `ghcr.io/openova-io/chargeback:0.1.0` (from #6726 merge `14b247fed`), run
locally against a fresh Postgres and OUR OWN National Cloud tenant (Kom4DC `me-east-215`),
driven entirely through the browser. The full DoD walk (installed on a fresh Sovereign via
kit slot 13f, reached through the console menu mapping) rides hw306; this is the
application-surface evidence for EPIC #6723 lane E.

| Step (all via the UI) | Evidence |
|---|---|
| Operator PIN sign-in → empty Overview | `cb-walk-01-overview.png` |
| Price book "National Cloud list 2026" created; 12-SKU NC list imported via CSV paste + preview | `cb-walk-02-pricebook-imported.png` |
| Customer `openova-own-tenant` created → source `me-east-215`/project added → credential + verify (auto-activated the pending customer — #6725 fix 2 live) → collector tick → **139 inventory rows**, live per-SKU usage | `cb-walk-03-usage-live.png` |
| Statements → Run period `2026-08` → draft statement: **subtotal 8,206.621 OMR · tax 410.331 · total 8,616.952 OMR** | `cb-walk-04-statement-2026-08.png`, `statement-2026-08.csv` (8 SKU lines) |

Defects found live, both actioned:
1. Sources tab always rendered "No cost sources" — the page read a non-existent `c.sources`
   off `GET /customers/{id}` instead of `GET /customers/{id}/sources` (`{sources:[…]}`
   envelope). Fix + vitest + 0.1.1 lockstep: PR #6736. Credential/verify for this walk was
   entered via the API; the fixed tab is re-walked on image 0.1.1.
2. During an earlier API-driven run: one failing source aborted the whole collector tick, and
   operator-created customers never collected while `pending` — both fixed in #6725
   (`bb70930c0`, `002888106`) and the second fix is what auto-activated this walk's customer.

Open validation item (unchanged): first-tick backfill semantics vs CTS timestamps — quantities
imply backfill from resource creation; to be pinned during the hw306 walk.

## Second pass (image 0.1.1, same walk data)

| Step | Evidence |
|---|---|
| Overview with live totals | `cb-walk-05-overview-live.png` |
| Customers list with the active row | `cb-walk-06-customers-list.png` |
| Audit trail: `customer.create → source.create → credential.rotate{verified} → customer.activate{via: source verification} → source.verify`, secret-free | `cb-walk-07-audit-activation.png` |
| Sources tab BEFORE the fix (0.1.0 — always "No cost sources" despite the persisted, verified source) | `cb-walk-08-sources-before-fix.png` |
| Sources tab AFTER the fix (0.1.1 — full row: verified, last-collected, Verify/Rotate/Delete) | `cb-walk-09-sources-after-fix.png` |

Publish incident closed en route: the 0.1.1 chart publish first failed because the chart's own
render-contract test asserted a hardcoded `0.1.0` image literal; fixed by deriving the expected
tag from `Chart.yaml appVersion` (#6739). Image `chargeback:0.1.1` and chart `bp-chargeback:0.1.1`
are both on GHCR (kit slot 13f and the catalog seed pins resolve again).

