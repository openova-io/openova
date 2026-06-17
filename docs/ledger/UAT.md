# UAT — browser walkthrough dashboard · `hw158` (2026-06-17)

> **Env:** `hw158.omani.works` · deployment `ab2135d4cf2d01e4` · single physical kom4dc region
> (2 VPCs `me-east-215-a` / `-b`). On each wipe + re-prov this dashboard resets and the links flip
> to the new env.

## The acceptance standard (the agreed contract)

**UAT is 100% browser — no terminal, no kubectl, no git, no curl.** Every step is **open a URL →
click/type → SEE a rendered screen**. Evidence is a **screenshot** under
[`docs/sessions/2026-06-17/evidence/`](../sessions/2026-06-17/evidence/). A redirect that ends on a
**login screen is a FAIL** — only a rendered, signed-in screen is ✅. `GAP` = a requirement with no
web-UI surface (itself a finding; never a reason to drop to a terminal check).

**Sign-in (the zero-click owner-admin landing):** open the signed
`https://console.hw158.omani.works/auth/handover?token=<JWT>` URL in a fresh tab → it lands
**directly on the Dashboard signed in as `emrah.baysal@openova.io` (sovereign-admin)**, no login
form. Every app is then opened at its **bare public URL** in the same browser session.
Proof: [`hw158-uat-01-console-dashboard-signedin.png`](../sessions/2026-06-17/evidence/hw158-uat-01-console-dashboard-signedin.png) ✅.

**Table format (mandated), used in every per-ticket runbook:** a 4-column table —
**`Tested page · Description · Status · Evidence`** — where *Tested page* is a clickable link to the
live page and *Evidence* is a screenshot link.

---

## The 10 canonical runbooks — browser walk index

> **✅ BROWSER WALK COMPLETE (2026-06-17):** all 10 runbooks walked in a real browser (Playwright), ~179 embedded screenshots. **AGGREGATE: 72 ✅ / 77 ❌ / 38 GAP — 48% real browser pass rate** (of the 8 cleanly-tallied; #3668 + #3379 screenshots captured, verdicts finalizing). This is the honest screenshot-backed number — harsher than the curl 48% on some rows (real render required), but partly pessimistic where the console service-worker hijacked app navigations. Real ❌ confirmed: funnel terminal (no running app), 7 apps on host not mgmt vCluster, object-model lanes with no UI, guacamole/pdns-admin/newapi-1st SSO.


Each runbook below is the full per-ticket browser walk (the **455-step** canonical set). All have
been **revamped to the browser-walk standard** (4-column clickable-link table, screenshot evidence,
no curl/kubectl). `☐` = the browser walk + screenshot capture is in progress on hw158.

| # | Runbook | Ticket | Browser surfaces | Status |
|---|---|---|---|---|
| 1 | [canonical-org-app-cr-model](uat-walkthrough/canonical-org-app-cr-model-live-end-to-end.md) | #3687 | /dashboard treemap · /apps · /organizations · showback | ✅**6** / ❌**19** / GAP**14** |
| 2 | [sso-zero-login-everywhere](uat-walkthrough/sso-zero-login-everywhere-admin-by-default.md) | #3374 | each app bare URL → signed-in admin | ✅**17** / ❌**3** / GAP**6** |
| 3 | [topology-dr-one-vocabulary](uat-walkthrough/topology-dr-one-vocabulary-built-and-region-kill-proven.md) | #3375 | /catalog new-instance picker · /app Topology tab · Switchover | ✅**9** / ❌**16** / GAP**8** |
| 4 | [funnel-voucher-to-running-app](uat-walkthrough/3376-funnel-voucher-to-running-app.md) | #3376 | marketplace redeem → wizard → checkout → launch → Org console | ✅**2** / ❌**22** |
| 5 | [ns1-migrate-7-host-apps](uat-walkthrough/ns1-migrate-7-host-apps-into-mgmt-vcluster.md) | #3642 | /dashboard treemap vCluster layer | ✅**7** / ❌**13** / GAP**3** (7 apps on host, not mgmt) |
| 6 | [organizations-eradicate-sme-naming](uat-walkthrough/organizations-eradicate-sme-tenant-naming.md) | #3383 | /organizations · menus · BSS screens (no "tenant" word) | ✅**6** / ❌**1** / GAP**7** |
| 7 | [catalog-edit-single-source-iac](uat-walkthrough/catalog-edit-single-source-iac-not-overlay.md) | #3668 | /catalog/<bp> inline edit · Edit-IaC · icon picker | walked — 11 shots, verdict finalizing |
| 8 | [cutover-durable-deny-egress](uat-walkthrough/cutover-durable-true-deny-egress-and-faithful-pivot.md) | #3379 | Sovereignty/cutover screen · /jobs cutover steps | walked — 10 shots, Sovereignty UI found |
| 9 | [jobs-one-honest-canvas](uat-walkthrough/jobs-one-honest-canvas-no-fabrication-with-remediation.md) | #3646 | /jobs canvas · Kind column · filters · Re-run | ✅**16** / ❌**3** |
| 10 | [regenerate-on-current-env](uat-walkthrough/uat-walkthrough-regenerate-on-current-env.md) | #3581 | (meta — the browser-walk discipline itself) | ✅**9** / ❌**0** (meta) |

**Index + per-runbook verdicts:** [`uat-walkthrough/README.md`](uat-walkthrough/README.md).

---

> **What changed (2026-06-17):** the prior version of this file (and the runbooks) carried
> **curl/kubectl command-output** as "evidence" — a violation of the agreed browser-only contract.
> All 10 runbooks + this dashboard were revamped back to the **screenshot-based browser-walk
> format**. The browser re-walk that fills each `☐` with a real screenshot is in progress; the
> sign-in row above is the first witnessed screen.
