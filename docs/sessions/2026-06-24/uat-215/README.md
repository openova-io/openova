# UAT-215 god-mode walk — rows 1–70 (omantel.biz, 2026-06-24)

Refs #4181. Live env: omantel.biz PERMANENT Sovereign, dep `4635277cae4ffed9`, me-east-215-a/-b.

Every formerly-⚠️ row in `docs/ledger/UAT.md` rows 1–70 was driven live (sovereign-admin /
org-admin minted JWT + Playwright signed-in session + live kubectl via the mothership pod +
direct API) and re-stamped ✅ / ❌(issue #). **Zero ⚠️ remain in rows 1–70.**

Tally (rows 1–70): **53 ✅ · 17 ❌ · 0 ⚠️ · 0 ⛔.**
- model (1–25): 24 ✅ · 1 ❌  (row 23 — only 1 customer Org, #3687)
- sso (26–45): 19 ✅ · 1 ❌    (row 27 — avatar generic-name defect, #4187)
- topology (46–70): 10 ✅ · 15 ❌  (the live Topology/Continuum surface mislabels shared-pg
  as singleton against a phantom region + Continuum has no lease holder → #3375/#3829;
  region-B grafana/guacamole secret-sync gap → #4158)

Access primitives proven live: [ACCESS.md](ACCESS.md).
Per-row evidence (screenshots / API JSON / kubectl) under `evidence/`.
New issues filed by this walk: **#4187** (avatar name), **#4188** (stale duplicate vc-demo).
