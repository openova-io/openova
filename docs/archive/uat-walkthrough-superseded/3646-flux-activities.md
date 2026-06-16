# 3646 — Every activity is a Flux/CR reconciliation on one honest canvas — UAT walk

> **Ticket:** [#3646](https://github.com/openova-io/openova/issues/3646) · **Car:** T3 · **PR:** #3652 · **Train:** `train/hw150`
>
> **What this proves:** the `/jobs` canvas shows **every** platform activity as a projection of a Flux
> object / CR reconciliation — HelmRelease installs, the 11-step cutover, a DR switchover — each with its
> `dependsOn` edges, and **never** showing a mid-flight activity as falsely "Succeeded". "Flux first"
> (founder #5).
>
> **Format law:** pure UI rows on `/jobs`. Replace `<fqdn>`/`<JWT>`. Tick **☑**/**☒**.

**Sign-in (once).** `https://console.<fqdn>/auth/handover?token=<JWT>` → signed in, no login form.

## Section 1 — The cutover EXECUTION is visible with its 11 steps + edges (the #3646 catch)

| Step | Go to (URL) | Do | Expect | ☐ |
|---|---|---|---|---|
| 1.1 | `/jobs` (during a live cutover) | open the activity view | a `Cutover` group with its **11 step rows** + `dependsOn` edges — not a single opaque "install" row | ☐ |
| 1.2 | `/jobs` (mid-flight) | read the group state while `cutoverComplete=false` | the group/steps show **running**, NEVER a premature "Succeeded" (the dormant-install confusion is gone) | ☐ |
| 1.3 | `/jobs` (after completion) | re-read | the group flips to Succeeded only when `cutoverComplete=true` | ☐ |

## Section 2 — A DR switchover appears via the SAME bridge (generality)

| Step | Go to | Do | Expect | ☐ |
|---|---|---|---|---|
| 2.1 | `/jobs` (after triggering a T5 switchover) | watch | the switchover appears as an activity group with its steps + edges — same model as the cutover | ☐ |

## Section 3 — HelmRelease activities read as Flux alongside the others

| Step | Go to | Do | Expect | ☐ |
|---|---|---|---|---|
| 3.1 | `/jobs` | open any app-install row | it reads as a Flux HelmRelease reconciliation — confirming the helm-fed jobs ARE flux, now beside the raw-Job activities | ☐ |

## Appendix — automated (NOT acceptance)
- `go test ./products/catalyst/bootstrap/api/internal/jobs ./.../internal/handler` green incl `-race`.
- `activity_bridge_test.go` (group+steps+edges, failed-step→group-failed) + `cutover_activity_bridge_test.go` (durable replay shows mid-flight as running, never Succeeded — the #3646 guarantee).
