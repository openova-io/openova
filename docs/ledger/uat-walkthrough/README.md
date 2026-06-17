# UAT walkthrough runbooks — index (agreed browser-walk standard)

> ## 🟢 ALL 10 RUNBOOKS ARE NOW **BROWSER-WALK FORMAT** (the agreed standard). Current env: **hw158** (`hw158.omani.works`, deployment `ab2135d4cf2d01e4`).
> **The agreed format (mandatory for every runbook):** each runbook is a 100% **browser** walk — a table of `| Tested page | Description | Status | Evidence |` rows. **Tested page** is a clickable link to a live page; **Description** is the action + the screen you must SEE; **Status** is `☐` until a real browser walk flips it `✅` (`❌` on a witnessed defect, `GAP` where there is no web-UI); **Evidence** is a **screenshot** under [`../../sessions/2026-06-17/evidence/`](../../sessions/2026-06-17/evidence/). **A redirect ending on a login / PIN / token screen = `FAIL`; a rendered working screen = `✅`. NO `curl` / `kubectl` / `grep` / command-output is acceptance evidence — only screenshots are.**
> **⚠️ THE PRIOR curl/kubectl-FORMAT WALK IS SUPERSEDED.** The earlier per-runbook pass/fail tallies (the "≈89 ✅ / ≈161 ❌" step counts) came from a `curl`/`kubectl`/`grep` walk that the agreed standard bans. Those tallies are **removed** from this index. Every runbook has been re-cast to the browser format with all rows **RESET to `☐`** and is being **re-walked in a browser** — the screenshot evidence replaces the command transcripts.
> **Status right now:** the browser re-walk is **IN PROGRESS**. Each runbook below is marked `☐ pending walk` until its browser walk lands screenshots, then `✅ walked`. Do **not** read a runbook as passed until its Status column reads `✅` with screenshots in the evidence dir.

> **Why this file exists:** the 10 docs in this folder are the per-canonical-ticket **browser-walk runbooks** (env-independent click-paths). They were originally authored against the prior env **hw150** (`hw150.omantel.biz`, wiped/void) and in a curl-based format. Per the founder rule *"each new environment flushes all the evidence"* — no hw150 evidence carries, and per the agreed standard no curl evidence carries either. This index is the single place that tells you, for each runbook, **which canonical ticket it covers, which browser surfaces it walks, whether its browser walk has landed, and where its screenshots live.**

---

## Status legend

- **☐ pending walk** — the runbook is in the agreed browser format, rows reset to `☐`; its browser re-walk has not yet landed screenshots on hw158.
- **✅ walked** — every row was walked live in a browser on hw158 with linked screenshots; a redirect ending on a login screen counts as `FAIL`, not pass.
- **N/A — meta** — the discipline runbook (#3581); it governs the walk itself rather than a single feature surface (its own Parts A–B are still browser rows).

---

## THE INDEX (runbook → ticket → browser surfaces → status → evidence dir)

| # | Runbook doc | Ticket(s) | Browser surfaces walked | Status | Evidence dir |
|---|---|---|---|---|---|
| 1 | [`canonical-org-app-cr-model-live-end-to-end.md`](canonical-org-app-cr-model-live-end-to-end.md) | **#3687** | console dashboard · apps grid · org CR placement treemap | ☐ pending walk | [`../../sessions/2026-06-17/evidence/`](../../sessions/2026-06-17/evidence/) |
| 2 | [`sso-zero-login-everywhere-admin-by-default.md`](sso-zero-login-everywhere-admin-by-default.md) | **#3374** | console · grafana · gitea · registry(harbor) · bao · keycloak admin · guacamole · newapi · pdns-admin (all bare-URL → signed-in admin) | ☐ pending walk | [`../../sessions/2026-06-17/evidence/`](../../sessions/2026-06-17/evidence/) |
| 3 | [`topology-dr-one-vocabulary-built-and-region-kill-proven.md`](topology-dr-one-vocabulary-built-and-region-kill-proven.md) | **#3375** | cloud 2-region map · topology tab · continuum/DR vocabulary · region-kill walk | ☐ pending walk | [`../../sessions/2026-06-17/evidence/`](../../sessions/2026-06-17/evidence/) |
| 4 | [`3376-funnel-voucher-to-running-app.md`](3376-funnel-voucher-to-running-app.md) | **#3376** | marketplace landing · redeem-preview · checkout (due-zero) · post-launch redirect · running app | ☐ pending walk | [`../../sessions/2026-06-17/evidence/`](../../sessions/2026-06-17/evidence/) |
| 5 | [`ns1-migrate-7-host-apps-into-mgmt-vcluster.md`](ns1-migrate-7-host-apps-into-mgmt-vcluster.md) | **#3642** | placement / vCluster treemap (7 named apps resident in mgmt vCluster) | ☐ pending walk | [`../../sessions/2026-06-17/evidence/`](../../sessions/2026-06-17/evidence/) |
| 6 | [`organizations-eradicate-sme-tenant-naming.md`](organizations-eradicate-sme-tenant-naming.md) | **#3383** | console Organizations surfaces (no "sme"/"tenant" wording in the UI) | ☐ pending walk | [`../../sessions/2026-06-17/evidence/`](../../sessions/2026-06-17/evidence/) |
| 7 | [`catalog-edit-single-source-iac-not-overlay.md`](catalog-edit-single-source-iac-not-overlay.md) | **#3668** | catalog blueprint editor · IaC YAML editor (edit → CR moves) | ☐ pending walk | [`../../sessions/2026-06-17/evidence/`](../../sessions/2026-06-17/evidence/) |
| 8 | [`cutover-durable-true-deny-egress-and-faithful-pivot.md`](cutover-durable-true-deny-egress-and-faithful-pivot.md) | **#3379** | cutover console surfaces · handover health (operator-driven, handover-gated) | ☐ pending walk | [`../../sessions/2026-06-17/evidence/`](../../sessions/2026-06-17/evidence/) |
| 9 | [`jobs-one-honest-canvas-no-fabrication-with-remediation.md`](jobs-one-honest-canvas-no-fabrication-with-remediation.md) | **#3646** | `/jobs` canvas · failed-job re-run control | ☐ pending walk | [`../../sessions/2026-06-17/evidence/`](../../sessions/2026-06-17/evidence/) |
| 10 | [`uat-walkthrough-regenerate-on-current-env.md`](uat-walkthrough-regenerate-on-current-env.md) | **#3581** | meta — the regeneration discipline (Parts A–B are browser rows: external surfaces + rendered UAT.md) | N/A — meta | [`../../sessions/2026-06-17/evidence/`](../../sessions/2026-06-17/evidence/) |

> **Authoritative results dashboard:** [`../UAT.md`](../UAT.md) — the summary table + North-Star rows. It links the **screenshots** each browser walk lands (not command output). On the next wipe → fresh prov it is reset by `scripts/reset-uat.py <env>` (see the #3581 meta runbook, Part C) and re-walked.

---

## Which docs are current vs superseded (read this first)

- **CURRENT** — all 10 runbook `.md` files in this folder, in the **agreed browser-walk format** (4-column `Tested page · Description · Status · Evidence`, clickable links, screenshot evidence, no curl/kubectl). Rows are `☐` and re-walked in a browser per env.
- **CURRENT** — [`../UAT.md`](../UAT.md), the results dashboard, stamped to the live env (hw158) and linking the browser screenshots under `../../sessions/2026-06-17/evidence/`.
- **SUPERSEDED** — any prior revision of these runbooks that used `curl` / `kubectl` / `grep` rows and pasted command output as evidence, and the per-runbook step tallies derived from that walk. Those are replaced by the browser re-walk; do not cite them as acceptance.
- **VOID** — all prior-env (hw150 / hw144 / hw128) evidence. Per the founder flush rule no prior-env screenshot carries forward; each new env starts every row at `☐`.

---

## Coherence invariants (what "current" means here, exactly)

- **Format:** every runbook is browser-only. The only acceptance evidence is a **screenshot** of a rendered screen. A redirect that ends on a login / PIN / token form is `FAIL`. `curl` / `kubectl` / `grep` / command-output never count as a walk result (a script step like `reset-uat.py` is operator tooling, kept out of the walk rows — see the #3581 meta runbook, Part C).
- **Env scope:** the results dashboard [`../UAT.md`](../UAT.md) and every runbook name **only the live env (hw158)**. No prior-env ✅ and no curl-format result is carried.
- **Evidence location:** every `✅` screenshot lives under [`../../sessions/2026-06-17/evidence/`](../../sessions/2026-06-17/evidence/), named `hw158-*` (or the funnel `01..05` captures).
- **On the next wipe → fresh prov:** `scripts/reset-uat.py <new-env>` re-blanks every Status cell to `☐`, this README's banner + the per-runbook `## Status` headers are re-stamped to the new env, the browser walk is re-run, and the screenshot links are replaced. No hw158 evidence carries forward.
