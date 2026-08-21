# UAT thumbnail exemption — operator decision requested (hw302, 2026-08-22)

**Status: awaiting operator sign-off.** 273/286 UAT rows carry a screenshot
thumbnail. The **13 below cannot** get a genuine *passing* thumbnail from this
session's vantage — each for a concrete physical/network reason, not a matter of
effort. This note formally requests an operator decision (Option 1 or Option 2
below) so the ledger can record how these 13 are evidenced.

Every one of the 286 rows already carries **evidence-text**; the gap is only the
visual thumbnail on these 13, and only because a *passing* visual surface does
not exist for them on hw302.

## The 13 rows and the precise reason no passing capture exists

| Row(s) | Result | Precise reason (not "blocked") |
|---|---|---|
| **G8, 220, 222** | ❌ | Served TLS cert SAN is `*.hw302.omani.works` (one-label) — cannot present for the two-label host `chepherd.uatco.hw302.omani.works`; the **TLS handshake ends before any HTTP response**, so no page or API response exists. Live failure already captured (curl `(60) SSL no-SAN`, PR #6568). |
| **R18, M4** | ✅ | Franchised-Sovereign backend facts, no UI. Only observation point is hw302 apiserver `212.72.24.1:6443`, which is **TCP-unreachable (VPC-firewalled) from both this vantage and the mothership** (proven by debug-pod probe). Evidenced by code/test. |
| **R20** | ✅ | Env-independent CLI fact (`scripts/bump-chart-version.sh`), evidenced by the script at HEAD. A terminal image is not a UI proof. |
| **20, 98, 102, 105** | ✅ | A passing treemap needs vcluster-tier customer Orgs; hw302's customer Orgs are **namespace-isolated**, so the dashboard treemap holds only platform items — any capture shows the rows' own vacuity-FAIL state (misleading on a ✅ row). |
| **G12** | ✅ | Region-kill **fault-injection sequence**; needs a live region-kill, not performable from the firewalled vantage. Proven on prior envs (hw273/hw292). |
| **163** | ✅ | Cutover-step honest-status; hw302 is **pre-cutover**, so no `cutover-step-*` rows exist to screenshot. |
| **228** | ⏳ | Orphan-VPC janitor on a **wipe+re-prov** cycle; no console surface. |

## Operator decision requested — pick one

**Option 1 — Accept type-appropriate evidence, waive the visual thumbnail for these 13.**
Code/test for R18/R20/M4; the live failure-capture already present for the ❌
agentic rows (G8/220/222); carried-✅-with-documented-rationale for the
infra-state rows (G12/163/20/98/102/105) and the ⏳ row (228). This records the
ledger as evidence-complete with a documented, reasoned thumbnail waiver.

**Option 2 — Authorize the infra path that produces the passing states**, after
which the thumbnails become genuinely capturable:
- **bastion sign-off** — permit an SSH ProxyJump through the protected bastion
  (`212.72.24.20`) to reach hw302 for diagnosis/capture; and/or
- **land #6509** (per-Org app-zone wildcard cert) **+ a fresh prov** — clears the
  8 agentic rows (G8/G9/218-223); and/or
- **a cutover run** on hw302 — clears the cutover-family rows (163/165/166/G11).

## Why this is an honest request, not an exemption-of-convenience

No row was stamped green without genuine evidence; no screenshot was fabricated.
The 13 are the exact set where a *passing* visual artifact is physically
impossible on this env without either fabrication or an infra action that is
founder-gated. The full state is in
[`docs/ledger/PATH-TO-100.md`](../../ledger/PATH-TO-100.md) and the companion
[`uat-evidence-completeness-report.md`](uat-evidence-completeness-report.md).
