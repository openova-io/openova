# UAT Walkthrough — #3646 Jobs: one honest canvas, no fabrication, with remediation

**Ticket:** #3646 — *JOBS one honest `/jobs` canvas: a GENERIC source-bridge projects HelmReleases + the 11-step cutover + DR-switchover + recurring/child/reconciler activities with `dependsOn` edges; status is NEVER ahead of reality; NO fabricated Succeeded; per-row remediation (retry / run-now / re-submit).*

**Branch:** `feat/3646-jobs-one-honest-canvas` (PR open against `main`, `Refs #3646`, NOT merged).

**Live env (to walk):** `https://console.hw150.omantel.biz` — sign in zero-click via the handover JWT as the sovereign-admin (the handover URL lands on `/dashboard`, 0 password fields), then drive the spine below. `KUBECONFIG=/tmp/hw150.kubeconfig` for the kubectl cross-checks.

> This doc is the click-by-click acceptance spine. Each row is ONE UI action. The `Result` column is `☐` until an operator walks it on a fresh prov and lands a screenshot under `docs/sessions/<date>/evidence/3646-<env>/` (acceptance = the founder walking these rows, NOT PR merge).

## What this PR builds (the three reinforcing defects, all closed)

1. **Ingestion breadth (§5a)** — a GENERIC reconciler reader (`internal/helmwatch/reconcilers.go` → `ListReconcilerObservations`) observes **every** non-install reconciler GVR (Flux `Kustomizations`, `CronJobs`, standalone `Jobs`, reconciler-marked `Deployments`) via the SAME dynamic client the HelmRelease snapshot uses, and ONE generic bridge method (`internal/jobs/reconciler_bridge.go` → `Bridge.OnReconcilerObservation` / `SeedReconcilerObservations`) projects each into the jobs Store. Wired into the chroot seed (`internal/handler/jobs.go` → `chrootSeedReconcilerObservations`). No per-app code — a new CronJob in any blueprint surfaces automatically.
2. **Faithful typed model (§5b)** — every `jobs.Job` carries a typed `Kind` (`internal/jobs/types.go`: `install|reconcile|step|mutation|cron|task|reconciler|lifecycle|group`), stamped at write time at the `UpsertJob` chokepoint and back-filled on read (`kindForLeaf`). The FE de-merge: `JobsPage.tsx` drops `applyHandoverStageOverride` (deleted), `synthesizeJobFromFlowNode`-as-a-list-source, and `mergeJobs`+dedupe — it renders the single backend `/jobs` list. `JobsTable.tsx` gains a **Kind** column read from `job.kind` (never a JobName-prefix string-match) + a **health** tone (`healthy/degraded/failing`) for recurring/reconciler kinds.
3. **Remediation write surface (§5c)** — ONE generic Flux-native endpoint `POST /api/v1/deployments/{depId}/jobs/{jobId}/retry` (`internal/handler/jobs_retry.go` → `RetryJob`) dispatches on the leaf `Kind`: HR/Kustomization → annotate `reconcile.fluxcd.io/requestedAt`; CronJob → create a one-off Job from `jobTemplate`; reconciler Deployment → roll it. Owner-checked (404 cross-tenant) + RBAC-gated (403 non-operator); writes a NEW Execution with the operator identity (`[retry] requested by <sub> at <ts>`); NEVER `exec.Command`. The FE `RetryJobButton.tsx` renders the kind-specific control on Failed/degraded rows.

## Walk — every row is one UI action (all on `https://console.hw150.omantel.biz`)

| # | Go to (URL) | Then do (click / type) | You should see (screen) | Result |
|---|---|---|---|---|
| 1 | `/jobs` | observe the `install-openbao` row | green **Succeeded** (the install leaf) | ☐ |
| 2 | `/jobs` | set Status filter = `failed`, Kind filter = `cron` | a **RED `cron-openbao-snapshot-save`** row alongside the still-green `install-openbao` — the recurring DR backup is honestly failing | ☐ |
| 3 | click the `cron-openbao-snapshot-save` row | open the detail page | Executions list, one per `*/10` run, the latest **Failed** with the `bao login … no client_token` log line | ☐ |
| 4 | `/jobs` | find `task-cnpg-pair-…-3-join` | a `task-*` row in **running/failed**, not invisible (the stuck HA peer join is surfaced) | ☐ |
| 5 | `/jobs` | find `reconciler-sso-bridge` (Kind = `reconciler`); then `kubectl scale deploy/sso-bridge-reconciler -n sso-bridge --replicas=0` and refresh | the health flips **healthy → degraded**; scale back `--replicas=1` → **healthy** (never a one-shot "Succeeded") | ☐ |
| 6 | DevTools → Network → the `/jobs` XHR | inspect the JSON | every row carries a `kind`; the Apps/Handover/Cutover lifecycle rows carry a **backend** status (no client-rewritten placeholder) | ☐ |
| 7 | page sources (DevTools) | search `applyHandoverStageOverride`, `mergeJobs` | **not present** (deleted) — the client no longer fabricates or merges | ☐ |
| 8 | `/jobs` | hover a Failed `install-velero` row → click **Retry reconcile** | a toast/inline "Requested"; a new Execution badge appears on the row | ☐ |
| 9 | terminal | `kubectl get hr bp-velero -n flux-system -o jsonpath='{.metadata.annotations.reconcile\.fluxcd\.io/requestedAt}'` | the just-now RFC3339 timestamp (the annotation the Retry wrote) | ☐ |
| 10 | `/jobs` | on the `cron-openbao-snapshot-save` row click **Run now** | `kubectl get jobs -n openbao` shows a new `…-manual-<unix>` Job; a new Execution appears | ☐ |
| 11 | terminal | `curl -XPOST .../jobs/<id>/retry` with a NON-operator token | **HTTP 403** | ☐ |
| 12 | `/jobs` during a live cutover | watch | the 11 `cutover-step-*` rows with `dependsOn` edges; the `Cutover` group stays **running** until `cutoverComplete=true`, NEVER a premature "Succeeded" | ☐ |
| 13 | `/jobs` | set Kind filter = `reconcile` | the Flux `Kustomization` reconcile rows surface as `reconcile-<name>` (ingestion breadth proof) | ☐ |
| 14 | `/app/<consumer>` → **Jobs** tab | observe | the per-app Jobs tab carries the same Kind column + Retry control (AppDetail wiring) | ☐ |

## Generality proof (founder #4 — ONE mechanism, not N hacks)

`git diff` shows: ONE `OnReconcilerObservation` with a kind switch ingests a Kustomization, a CronJob, AND a batch Job with **zero per-app/per-cloud branching**; ONE `RetryJob` handler with a kind switch remediates an `install-*`, a `reconcile-*`, and a `cron-*` with **zero per-app code**; a DR switchover surfaces via the SAME `ActivityBridge` as the cutover; a brand-new blueprint's CronJob/Kustomization/claim surfaces + becomes actionable with `JobsPage`/`JobsTable`/the Activity model untouched.

## Automated gates (NOT acceptance — see appendix)

- `go test -race ./internal/jobs/... ./internal/helmwatch/... ./internal/handler/...` green (incl. the new `reconciler_bridge_test.go` ingestion proofs + `jobs_retry_test.go` 403/200/annotation/run-now proofs).
- `npm run build` + the FE unit tests (`jobs.types.test.ts`, `RetryJobButton.test.tsx`, the de-merged `JobsPage.handover.test.tsx`, the Kind-column `JobsTable.test.tsx`) green; the deleted-helper test (`handoverStageOverride.test.ts`, `JobsPage.flow-merge.test.tsx`) **removed, not skipped**.

**Result:** ☐ — awaiting the live operator walk on a fresh prov.
