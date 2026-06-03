# `docs/sessions/` — date-stamped session reports + walk runbooks

Per the lean-doc strategy (root `CLAUDE.md` §Lean documentation strategy), this
directory holds **the most recent + still-relevant** walk runbooks, UAT matrices,
and session reports. Superseded one-off session logs and walk-evidence dumps are
moved to [`docs/archive/sessions/`](../archive/sessions/) — never hard-deleted —
so the historical trail is preserved.

## What's here

### Current UAT matrices (go/no-go gates)

- [`2026-06-03-uat-matrix.md`](2026-06-03-uat-matrix.md) — 7-capability pre-go-live UAT against the last session's code-side claims (fresh-prov gate for hw87).
- [`2026-06-03-uat-matrix-wave2.md`](2026-06-03-uat-matrix-wave2.md) — Wave-2 follow-up UAT matrix.

### Canonical session reports (indexed from the repo-root `README.md`)

- [`2026-05-17-convergence.md`](2026-05-17-convergence.md) — convergence wave + Sandbox scaffold session report.
- [`2026-05-19-20-trust-recovery.md`](2026-05-19-20-trust-recovery.md) — trust-recovery cycle whole-day retrospective.
- [`2026-05-20-trust-audit.md`](2026-05-20-trust-audit.md) — random-sample evidence audit of closed issues (cited from `docs/ledger/TRUST.md`).
- [`2026-05-20-walk-runbook.md`](2026-05-20-walk-runbook.md) — fresh-prov walk runbook for 42 unverified PRs.

### Live-cited audits + forensic archives (referenced from `docs/ledger/TRACKER.md`)

- [`2026-05-31-post-mandate-audit/`](2026-05-31-post-mandate-audit/) — hw78 5-pillar gap analysis.
- [`2026-05-31-zt2-hw75-archive/`](2026-05-31-zt2-hw75-archive/) — forensic hw75 tofu state + kubeconfig + verifier log (G68 #2617).
- [`2026-06-02-G117-sso-fanout-audit/`](2026-06-02-G117-sso-fanout-audit/) — per-Blueprint Tier-2/Tier-3 SSO fan-out audit driving G117.5 dispatch.
- [`2026-06-02-per-blueprint-topology-audit.md`](2026-06-02-per-blueprint-topology-audit.md) — App-tier topology audit table (drives G117.1 #2740).
- [`screenshots/`](screenshots/) — G113 silent-SSO walk evidence (9 screenshots cited from TRACKER).

### Templates

- [`templates/`](templates/) — reusable session-doc templates (e.g. the EPIC-cycle codification-audit template).

## Where the rest went

Superseded one-off walks (the 2026-05-23 / 2026-05-24 hw01 app-detail + BSS +
pillar + EPIC walk-evidence dumps, the G117 Playwright scaffolds + W4 walk specs,
and the RCA / crash-recovery / codification-execution logs) live under
[`docs/archive/sessions/`](../archive/sessions/), grouped by their original
date-prefixed directory names.
