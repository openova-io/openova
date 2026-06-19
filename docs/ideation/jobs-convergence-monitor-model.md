# Provisioning "Jobs" page → **Convergence Monitor**: the model

> Ideation, 2026-06-20. Captures the design synthesis from the founder thread on why the
> provisioning Jobs page / DAG is broken and the native model that fixes it at the root.
> Status: **proposed — pending founder sign-off on the spine.**

## 1. The problem we are actually resolving

The page at `/sovereign/provision/{id}/jobs` exists to answer **one** operator question during a
provision: *"Is my Sovereign converging to its desired state, and if not, where is it stuck?"*
It is a **convergence monitor**.

We built a **job-execution monitor** instead: it ingests every `batch/v1` Job + reconciler in the
cluster into **one flat global DAG**, with no filter on purpose and one dependency model. That
mismatch — not the scanners — is the whole bug. Symptoms:

- **Accumulation** — the count climbs forever (region A "128 → 141 → 145") though only ~23 Jobs
  exist live; the page mints a permanent row per scan run and never removes GC'd ones.
- **Flapping** — nodes flip `Failed → Success → Failed` as continuous reconciles re-fire.
- **Wrong edges** — scanners have no `dependsOn`, so they float / mis-attach in the graph.
- **Never done** — an infinite node stream means the "DAG" never reaches a terminal state.

## 2. Root cause: a category error

| | Imperative scheduler (UC4/Automic) | Declarative reconciler (Kubernetes/Flux) |
|---|---|---|
| Model | run A, then B | here is the desired graph; converge it |
| Execution | each run = an activation w/ a RunID | reconcile continuously, **forever** |
| "ran 50 times" | 50 real activations | one desired node **not yet Ready** (50 retries) |
| Flow | a thing you **author** | **emergent** from declared `dependsOn` |

A reconciler retrying **is** "the job ran 50 times." Forcing declarative reconciliation into an
imperative run-log is what manufactures every symptom above. We are not a job scheduler; we are a
GitOps reconciler. The native frame is a **desired-state dependency graph coloured by live state**,
not a flow of runs.

## 3. Job-type taxonomy (grounded — hw171, 2026-06-20, both regions queried live)

| Role | Live examples (hw171) | Finite / Infinite | Native model |
|---|---|---|---|
| **Convergence-graph nodes (the spine)** | ~60–65 HelmReleases, Kustomization tiers 00→08 | Infinite reconcile → **converges** to Ready | **State** (Pending→Reconciling→Ready/Failed) |
| **Finite one-shot jobs** | newapi seed/promote, cutover steps, Helm hooks | **Finite** (terminal) | **Flow/job** — run, retries, logs |
| **Day-2 / recurring** | trivy scans (11 live / 614 report CRs), syft SBOM, cnpg backups, pgbasebackup + streaming | Infinite (periodic / steady) | **State** — last-run + health |

Finite = the one-shot Jobs (hooks / seed / cutover). **Everything else** (HRs, crons, scans,
data-plane) is infinite and must be rendered as **state**, never as accumulating runs.

## 4. Architecture: 3 native models, 1 primary spine

1. **PRIMARY — dependency / desired-state graph.** Node set = the *declared* desired components
   (bounded). Edges = real Flux/HR `dependsOn`. Colour = live reconcile state. **"Done" = all-Ready.**
   *This* is the convergence monitor.
2. **Finite jobs = execution detail on their owning node.** seed/promote/cutover hang off the
   component they belong to; runs / retries / logs live there. The only place a "flow of runs" is
   meaningful (e.g. cutover's sequential 8 steps).
3. **Infinite / Day-2 = separate state panels.** Scanning (614 CRs), backups, data-plane health —
   health, not runs; **never** in the convergence graph.

This kills every symptom at the root:

| Symptom | Why it's gone |
|---|---|
| Accumulation | node set = the **bounded** desired graph, not the unbounded run history |
| Flapping | sticky **state machine** (Ready stays Ready; only Flux `Stalled` = terminal Failed) |
| Never-done | **all-Ready** is a real terminal condition for *provisioning* |
| Pollution | Day-2 separated into its own panel/runtime |
| Wrong edges | edges = real declared `dependsOn`, not inferred from a global job list |

## 5. Wireframe — the convergence monitor

```
┌─ Convergence · hw171 ··················· 58 / 65 Ready ───────────────┐
│ ▸ bootstrap-kit      ████████████  8/8 tiers Ready                    │
│ ▸ platform (HRs)     ██████████░░  52/60 Ready · 3 reconciling        │
│   • bp-keycloak      ● Ready                                          │
│   • bp-newapi        ✕ FAILED   ⤷ seed→promote chain                  │
│        └ seed     ✕ DeadlineExceeded (schema not migrated)            │
│        └ promote  ⏸ blocked on seed                                   │
│   • bp-harbor        ● Ready                                          │
│ ▸ cutover            ▷ dormant — not started (runs post-handover)     │
└──────────────────────────────────────────────────────────────────────┘
┌─ Day-2 (separate, not convergence) ──────────────────────────────────┐
│ security scans   ● active    614 reports     last run 12s ago         │
│ backups          ● healthy   last cnpg backup 4m ago                  │
└──────────────────────────────────────────────────────────────────────┘
```

Bounded spine, state-coloured; the finite seed→promote sub-DAG is detail **under** `bp-newapi`;
Day-2 lives in its own panel.

## 6. Open branch (one decision)

Multi-step **finite** chains (cutover's 8 steps, seed→promote): render as a real **local sub-DAG**
on the node *(recommended — the sequential failure point is exactly what an operator needs)* vs a
flat ordered checklist.

## 7. Consequence for in-flight PRs

- **#3920** (exclude Day-2 scanners) — right instinct, wrong frame: under this model scanners get
  their **own state panel**, not a delete. **Hold.**
- **#3918** (stable keying + terminal mapping) — necessary primitives, but the key must be
  `(component-node, run)` within the convergence model, not a global base-name. **Hold / re-target.**

No code until the spine (the dependency-state graph) is agreed.
