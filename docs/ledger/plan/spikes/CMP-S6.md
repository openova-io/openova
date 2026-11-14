# CMP-S6: result

## Question

On the installed Flux line, after a ConfigMap read through HelmRelease `valuesFrom` changes, does stamping `reconcile.fluxcd.io/requestedAt` on the HelmRelease make it re-read the values at once?

## Outcome: yes

On Flux v2.4.0 (helm-controller v1.1.0), a `requestedAt` stamp applied the changed values every time: 14 of 14 trials over two runs. The longest wait was 1.01 s. The bound for "at once" was set before the runs at 60 s. Without a stamp, the release did not notice the change in 90 s.

## What was run

- Where: GitHub Actions in `openova-io/openova`, on the branch `spike/CMP-S6`, which is never merged. The workflow is `.github/workflows/spike-cmp-s6.yaml`. The script, the stand-in chart and the manifests are under `tests/spikes/CMP-S6/`.
- Two runs. Each run has two legs, and both legs use the same manifests, script and trials.
  - **Installed leg.** Flux v2.4.0, installed from the upstream `install.yaml` (the URL cloud-init applies). This gives helm-controller v1.1.0 and source-controller v1.4.1, the tags the platform's Flux chart pins. kind v0.31.0 with Kubernetes v1.31.14 (production k3s is v1.31.4).
  - **Contrast leg.** Flux v2.10.0 (helm-controller v1.7.1, source-controller v1.10.1) on kind v0.33.0 with Kubernetes v1.35.8. It is the positive control for the watch-label trial.
  - Common tools: runner ubuntu-24.04, docker 28.0.4, helm v3.21.4. A registry 2.8.3 runs inside the cluster (ClusterIP). The chart is pushed to it through `kubectl port-forward`.
- The objects copy slot 27a:
  - An OCI HelmRepository with interval 15m.
  - The HelmRelease `bp-kyverno-policies`: interval 15m, `disableWait`, install retries -1, upgrade retries 3, `crds: CreateReplace`.
  - `spec.values.compliancePolicies.sovereignConsume: origin`.
  - `valuesFrom` pointing at the ConfigMap `compliance-policy-values` in `flux-system`, key `values.yaml`, `optional: true`.
  - The ConfigMap carries the change-id annotation, the substitute-disabled annotation and the managed-by label. Its document has the planned shape: `compliancePolicies.probesPresent.action`.
- The chart is a stand-in. It writes the effective `action` into one ConfigMap, and a trial reads that value every 0.25 s.
- Latency runs from the stamp command until the new value is seen. Resolution is about 0.4 s, which is the poll loop, not Flux.

## Measured values

Each cell gives run 1, then run 2. A dash means the trial was not in run 1.

| Trial | Installed v2.4.0 | Contrast v2.10.0 |
|---|---|---|
| S0: install with the ConfigMap absent | Ready, chart default rendered (both runs) | same |
| T1: ConfigMap changed, no stamp, watched 90 s | not applied; not applied | not applied; not applied |
| T2, R1–R5, T7: ConfigMap changed, then `requestedAt` stamp (7 per run) | 7/7 in 0.39–1.01 s; 7/7 in 0.40–0.99 s | 7/7 in 0.42–1.02 s; 7/7 in 0.39–1.00 s |
| T3: stamp with nothing changed | handled, no new Helm revision (both runs) | same |
| T4: stamp handled first, ConfigMap changed afterwards, watched 60 s | not applied; not applied | not applied; not applied |
| T5: `forceAt` alone on a pending change, watched 60 s | not applied; not applied | not applied; not applied |
| T6: `forceAt` and `requestedAt`, same token, pending change | applied 0.39 s; 0.41 s | applied 0.42 s; 0.40 s |
| T7: empty document `{}` (part of the 7) | chart default restored | same |
| T8a: watch label `reconcile.fluxcd.io/watch: Enabled` added, values unchanged | no new revision | no new revision |
| T8b: labelled ConfigMap changed, no stamp, watched 90 s | not applied; not applied | applied 0.50 s; 0.46 s |
| T9: stamp after T8b (shows the release was still live) | applied 0.40 s; 0.41 s | not needed |
| T10: `forceAt` and `requestedAt` with nothing changed | –; a new Helm revision | –; a new Helm revision |
| X0: a value the API server refuses, then stamp | –; Stalled/RetriesExceeded after 36.9 s, 4 failures | –; same after 38.0 s |
| X1: Stalled release, stamp, values unchanged | –; nothing happens | –; nothing happens |
| X2: Stalled release, value fixed, plain `requestedAt` stamp | –; applied 0.40 s, Ready=True, Stalled cleared | –; applied 0.41 s, same |

Exact latencies on the installed leg:

- Run 1: 0.39, 0.40, 0.44, 1.01, 0.41, 1.00, 0.40 s.
- Run 2: 0.41, 0.41, 0.40, 0.43, 0.40, 0.99, 0.40 s.

Other observations:

- `status.lastHandledReconcileAt` equalled the stamp token 0.04 to 0.09 s after the stamp.
- For every upgrade, helm-controller v1.1.0 logged "release out-of-sync with desired state: release config values changed".
- For the stamp with nothing changed, it logged "release in-sync with desired state".

Evidence:

- Run 1, commit 45d5cb9f0: https://github.com/openova-io/openova/actions/runs/43349322676
- Run 2, commit 8e2d96bc2: https://github.com/openova-io/openova/actions/runs/43350533863
- Each job log has one TRIAL line per trial, plus RESULT and OUTCOME lines. Each job also uploads `trials.jsonl`, the helm-controller log, the release events and the Helm history as artifact `cmp-s6-<leg>`.

## What follows for the plan

1. CMP-071 to CMP-073 stand as written. The stamp is `requestedAt`. The `forceAt` fallback is not needed, and CMP-072 does not depend on CTL-620.
2. Stamp `requestedAt` only, never `forceAt`:
   - `forceAt` alone does nothing (T5).
   - The pair forces an upgrade even when nothing changed (T10). That would add one Helm revision per region on every switch.
3. A plain stamp with nothing changed adds no revision (T3). It is safe to stamp every region on each switch.
4. **Order matters (T4).** A stamp that is handled before the region's ConfigMap changes does not pick up the change later. The values then wait for the next scheduled run of the HelmRelease; its interval is 15 m, and that wait was not timed here.
   - In production the ConfigMap changes only after the region's source and Kustomization have run. A stamp sent right after the commit will usually come too early, and check CMP-G08 (180 s) would fail.
   - Fix: in CMP-072 step 1, stamp a region only once its ConfigMap carries the change-id of the commit. The cache already holds that annotation for `pendingChangeId`.
5. A stalled release (upgrade retries exhausted, the state of issue #5261) does not block a mode switch. New values plus a plain stamp applied in 0.40 s and cleared Stalled (X2). A stamp with unchanged values does nothing on a stalled release (X1). The route needs neither `resetAt` nor `forceAt`.
6. The watch label:
   - On v2.4.0 it does nothing and does no harm (T8a, T8b).
   - On v2.10.0 it applies a change with no stamp in under 0.5 s, so CTL-620's premise holds on that line.
   - The upstream helm-controller changelog places the feature at v1.4.0 (Flux 2.7.0). Only 2.4.0 and 2.10.0 were measured.

## Not measured, and why

- **The Git to ConfigMap path.** The trials wrote the ConfigMap with `kubectl apply`. The path from a Git commit through the region's source and Kustomization to the ConfigMap was not built; T4 stands in for its ordering only. Building it needs a Git server inside the job.
- **The time an early stamp waits for the 15 m interval.** A trial would need about 16 minutes per leg.
- **The real policy chart and the Kyverno engine.** The stand-in chart only renders a ConfigMap.
- **`dependsOn`.** The production HelmRelease has `dependsOn: bp-kyverno`; the test release had none.
- **k3s.** The legs ran on kind.
- **Cross-region cluster clients and RBAC.** Each region holds its own HelmRelease, and the per-release behaviour is what was measured. Whether the API account may patch HelmReleases in every region was not tested.

## Evidence

Outcome: **yes**. The experiment is the branch `spike/CMP-S6` of this repository (never merged). Runs:

- https://github.com/openova-io/openova/actions/runs/43349322676
- https://github.com/openova-io/openova/actions/runs/43350533863

## What later tasks must take into account

Outcome is yes: on the installed Flux line (v2.4.0, helm-controller v1.1.0) a requestedAt stamp applies a changed valuesFrom ConfigMap in under 1.1 s, in 14 of 14 trials over two runs. Without a stamp, the change is not seen in 90 s. CMP-071 to CMP-073 stand. CMP-072 does not depend on CTL-620, and the forceAt fallback is not needed.

What later packages must take into account:

1. **CMP-072 timing (T4).** A stamp that is handled before the region's ConfigMap changes does not pick up the later change; the values then wait for the HelmRelease's next scheduled run (interval 15 m). A route that stamps right after the Git commit will usually be too early, and CMP-G08 (180 s) would fail. CMP-072 step 1 should stamp a region only once its ConfigMap carries the commit's change-id (already cached for pendingChangeId), or re-stamp until it does.
2. **Stamp requestedAt only.** forceAt alone does nothing. The forceAt+requestedAt pair adds a Helm revision even when nothing changed.
3. **Stalled releases.** A release stalled on exhausted upgrade retries (the #5261 state) still takes a mode switch: new values plus a plain stamp applied it and cleared Stalled. A stamp with unchanged values does nothing there.
4. **CTL-620.** The watch label is ignored and harmless on v2.4.0. On v2.10.0 it applies changes with no stamp in under 0.5 s. The upstream changelog places the feature at helm-controller 1.4.0 (Flux 2.7.0); only 2.4.0 and 2.10.0 were measured.

What could not be measured in CI, and what it needs:

- The Git to source to Kustomization to ConfigMap path: needs a Git server in the job; the ConfigMap was written with kubectl apply.
- The 15 m wait after an early stamp: about 16 minutes per leg.
- The real policy chart with the Kyverno engine; the production dependsOn on bp-kyverno; k3s instead of kind.
- Cross-region cluster clients and the API account's patch right on HelmReleases in each region.

Evidence:

- Branch spike/CMP-S6 (commits 45d5cb9f0 and 8e2d96bc2), left in place.
- Two pushes used of the five allowed.
- Per-leg artifacts on both runs expire 2027-02-11.
