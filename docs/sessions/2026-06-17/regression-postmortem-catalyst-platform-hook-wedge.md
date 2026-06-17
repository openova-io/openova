# Regression post-mortem — catalyst-platform hook wedge (hw158, 2026-06-17)

> **Founder question this answers:** *"I tried the application myself it is even
> behind 149 or 150 … why did you break and send us back to a worse situation
> instead of fixing things?"*

## What broke (operator-visible symptom)

On `hw158` the live console/API felt **worse than hw150** — intermittent console
instability, `bp-catalyst-platform` never reaching a clean `Ready`, and the
spine (`kubectl get hr -A`) oscillating in the 48–61 / 65 band instead of
settling.

## Root cause — a regression I introduced

The `catalyst-gitea-flux-auth-sync` **post-install/post-upgrade helm hook**
(added in **#3749**, "fixed" in **#3765**) ran a poll loop that **`exit 1`
(FATAL)** when the `catalyst-gitea-token` mint race left the secret empty past
its 300 s poll budget.

A FATAL post-upgrade hook **fails the entire `bp-catalyst-platform` helm
release**. The failure chain:

```
hook exit 1
  → helm release UpgradeFailed
    → Flux rolls back to the prior chart (1.4.664)
      → Flux re-attempts the new version
        → hook races the token again → exit 1
          → ∞ upgrade → fail → rollback → oscillate
```

That perpetual churn held `bp-continuum` / `bp-sandbox` / `vcluster` non-Ready
and made the `catalyst-api` / `catalyst-ui` spine unstable — **even though every
PR showed merged + CI-green.** The live-state condition was:

- `hr bp-catalyst-platform`: `Ready=Unknown: Running 'upgrade' action`
- `Released=False UpgradeFailed: post-upgrade hooks failed: timed out`
- `Remediated=True RollbackSucceeded … 1.4.664`

## Why CI never caught it

The hook only FATALs under the **live token-mint race** — a timing window that
does not occur in a clean `helm template` / kind install. Green CI + green merge
is **not** a working app; the only reliable signal is re-querying the live spine
after the merge. (Durably recorded as auto-memory
`feedback_chart_merges_can_wedge_platform_recheck_spine`.)

## The fix

**Live un-wedge (immediate):**
```
kubectl delete job catalyst-gitea-flux-auth-sync -n <ns>
kubectl annotate hr bp-catalyst-platform -n flux-system \
  reconcile.fluxcd.io/requestedAt=$(date +%s) --overwrite
```
With the token now present, the re-attempt succeeded (upgrade v20).

**Durable (#3780 / #3781, chart 1.4.672):**
- Hook poll loop `exit 1` → **`exit 0`** ("catalyst-platform is NOT wedged;
  self-heal on the next reconcile"). A non-load-bearing auth-sync hook must
  never be able to fail the whole release.
- `POLL_ATTEMPTS 60 → 240` (5 min → 20 min) so the normal mint latency never
  trips the timeout.
- RBAC (`Role`/`RoleBinding` scoped `resourceNames: [catalyst-gitea-token]`) is
  hook-annotated and correct.

## The discipline that prevents recurrence

1. **After ANY `bp-catalyst-platform` chart merge**, before claiming done:
   `kubectl get hr -A --no-headers | grep -v True` — is the spine Ready, or is
   `bp-catalyst-platform` stuck `Unknown/UpgradeFailed`?
2. **A hook whose failure fails the whole release is a latent platform-wide
   outage.** Non-required hooks `exit 0` on their own failure; only genuinely
   required hooks may block the release.
3. **A green PR/merge is not a working app** — re-query the live spine.

## Status

- Regression **root-caused and fixed at source** (#3780/#3781, chart 1.4.672;
  rolled up into the current train at 1.4.676).
- The cleanest validation that the whole train is healthy is a **fresh prov**
  (a clean install carries none of the live-env rollback oscillation) — in
  flight as `hw159`.
