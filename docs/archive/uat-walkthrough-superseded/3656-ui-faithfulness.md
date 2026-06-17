# 3656 — The UI is a faithful view: no flash, no stale, no pop-in — UAT walk (web UI)

> **Ticket:** [#3656](https://github.com/openova-io/openova/issues/3656) · **Car:** T4 · **PR:** #3649 (part) · **Train:** `train/hw150`
>
> **What this proves:** the console renders **nothing definitive until the source resolves** and never
> offers an action it then retracts — no flashing buttons, no stale status, no late pop-in. The UI
> corollary of IaC-first (founder #6).
>
> **Format law:** pure UI rows, observed **on open** (the loading window is the test). Replace
> `<fqdn>`/`<JWT>`. Tick **☑**/**☒**.

**Sign-in (once).** `https://console.<fqdn>/auth/handover?token=<JWT>` → signed in, no login form.

## Section 1 — No "+ New instance" flash (DONE, #3649)

| Step | Go to (URL) | Do | Expect | ☐ |
|---|---|---|---|---|
| 1.1 | `/catalog/bp-grafana` (multi-instance) | open cold, watch the header | the **+ New instance** button does NOT flash before the list loads; it appears once, after the list resolves | ☐ |

## Section 2 — A full singleton never offers "+ New instance"

| Step | Go to | Do | Expect | ☐ |
|---|---|---|---|---|
| 2.1 | `/catalog/<a singleton app that already has its 1 instance>` | open the page | **no** "+ New instance" button at all — it never appears-then-disappears | ☐ |
| 2.2 | `/catalog/<a singleton app with 0 instances>` | open | the button IS shown (the first is creatable) | ☐ |

## Section 3 — The Open button shows immediately for front-door apps, never on headless

| Step | Go to | Do | Expect | ☐ |
|---|---|---|---|---|
| 3.1 | the apps grid → harbor / gitea card | open the grid cold | the **Open** affordance is present immediately (disabled "Open…" placeholder → enabled), not a late pop-in | ☐ |
| 3.2 | a headless app card (a controller / openova-flow) | open | NO transient Open chip flashes (placeholder shows only for declared-UI apps — depends on T2 `hasUserUIEndpoint`) | ☐ |

## Section 4 — Jobs show their true terminal state on open

| Step | Go to | Do | Expect | ☐ |
|---|---|---|---|---|
| 4.1 | `/jobs` (with jobs that completed long ago) | open the page | completed jobs show **Succeeded** immediately (or an explicit "confirming…"), never Pending/Running then a late flip to Succeeded | ☐ |

## Section 5 — Generality

| Step | Go to | Do | Expect | ☐ |
|---|---|---|---|---|
| 5.1 | a fourth async surface chosen at walk time (e.g. the org/environment list) | open cold | a skeleton/loading state, never a wrong-then-corrected value | ☐ |

## Appendix — automated (NOT acceptance)
- `InstancesSection.test.tsx` "+ New instance gating (#3648, founder #6)": singleton+instance → hidden; multi → shown after load; singleton+empty → shown.
- Open-button + jobs-stale root cause + fix direction recorded in `docs/sessions/2026-06-16/train-iac-first-hw150.md` §T4 (needs T2 `hasUserUIEndpoint` + T3 live model; walked on hw150).
