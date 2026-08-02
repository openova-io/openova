# Mothership live walk — 2026-08-02T08:32–08:55Z (read-only kubectl)

Three live walks against the production mothership. Recorded here rather than only in issue
comments so the evidence is committed, dated, and greppable.

**Nothing was mutated.** Every command below is a read.

---

## Walk 1 — §854 live NodePort sweep (feeds UAT row 240)

```
165 Services scanned · 14 carry a nodePort · exactly 1 in a namespace we own
  openova-system/powerdns-anycast   LoadBalancer   32015,31425   95d
```

The violation is **still live** although the fix shipped today. Traced in the same sweep:

```
live HelmRelease openova-system/powerdns
  lastApplied = 1.2.22+3dee9b09e026     Ready=True
```

`bp-powerdns` **1.2.23** — the version carrying the explicit `nodePort: 0` deallocation — is on
`main` and published to ghcr. The cluster runs **1.2.22**.

**Three gates exist and only two are passed: merged → published → DEPLOYED.** Every prior
"merged ≠ delivered" note in this repo stops at two. The Service is Flux/Helm-owned
(`helm.toolkit.fluxcd.io/name=powerdns`), so it clears only when that HelmRelease advances;
hand-patching is reverted on the next reconcile, which is the #5348 shape exactly.

Solver ageing confirms **#5567 is not self-healing**: `chepherd-hub/cm-acme-http-solver-rsfzq` was
4h at the 05:08Z walk and 11h here, still stuck behind its 503 self-check.

Committed to row 240 in `67722190c`.

---

## Walk 2 — #5563 is failing in production, not latent

I filed #5563 as a build-time finding ("unbuildable from a clean checkout, none customer-visible").
The cluster contradicts that in its own words:

```
kubectl -n kube-system get helmrelease sealed-secrets
  [Ready]    False  SourceNotReady
     HelmChart 'kube-system/kube-system-sealed-secrets' is not ready: does not have an artifact
  [Released] True   InstallSucceeded
     Helm install succeeded for release kube-system/sealed-secrets.v1 with chart sealed-secrets@2.17.9

kubectl -n kube-system get helmrepository sealed-secrets
  url: https://bitnami-labs.github.io/sealed-secrets
  [FetchFailed] True — failed to fetch .../index.yaml : 404 Not Found
```

`Released=True` with `Ready=False` is the signature: it **installed once**, when the upstream still
answered, and has been unable to reconcile since. Nothing alerted because the workload from that
first install is still running.

Three independent confirmations of one fact: the repo declares the URL
(`platform/sealed-secrets/chart/Chart.yaml:27`), a direct `curl` returns 404, and Flux's own fetch
returns the identical 404.

The constraint is `2.17.*`, so Flux resolves a **floating range against a dead index** on every
reconcile — a permanent retry loop. `sealed-secrets` is the decryption path for sealed Secrets, so a
cluster that cannot reinstall it is one controller eviction from a much worse morning.

**No UAT row exists for this** — `grep -ci 'sealed-secrets' docs/ledger/UAT.md` → 0. The mothership
is not a Sovereign and the UAT ledger is the Sovereign acceptance walk, so mothership infrastructure
health has no acceptance row. Recorded here and on #5563 instead of forcing an unrelated row.

---

## Walk 3 — `axon` HelmRelease reports a failure that is not happening

```
kubectl -n axon get helmrelease axon
  [Released] False  UpgradeFailed
     timeout waiting for: Deployment/axon/axon-valkey status: 'InProgress',
                          Deployment/axon/axon status: 'InProgress'

kubectl -n axon get pods
  axon-6897449555-8964p          Running  1/1 ready  restarts=0  age=19h  [axon:69706a8]
  axon-valkey-76d5f58d8d-vh5c9   Running  1/1 ready  restarts=0  age=1d   [valkey:8-alpine]
```

Both Deployments are healthy — **1/1 ready, zero restarts**, running for 19h and 1d. The upgrade
timed out at its 5-minute readiness window, the pods became ready afterwards, and the HelmRelease
status was never re-reconciled to reflect it. **The status is stale, not wrong-at-the-time.**

---

## What the three walks establish together

Flux status on this cluster is unreliable **in both directions**, which generalises #5558:

| object | Flux says | reality |
|---|---|---|
| `catalyst-platform` (#5558) | `Healthy=True` | zero pods, console 503 |
| `apps` (#5558) | `HealthCheckFailed` | four objects all `Ready=True` |
| `openova-system/powerdns` | `Ready=True` | serving a §854 violation |
| `kube-system/sealed-secrets` | `Ready=False` | accurate — source genuinely dead |
| `axon/axon` | `UpgradeFailed` | both Deployments 1/1, 0 restarts |

**Anything gating on HelmRelease `Ready`/`Released` as ground truth is being misled**, and the
powerdns row shows the sharper form: `Ready=True` is not `compliant=true`. Health status and
compliance status are different questions, and only one of them is being asked.

## Follow-ups

- #5563 — claimed, severity raised from latent to live-failing.
- #5567 — orphaned Kyverno webhooks; solver ageing 4h → 11h confirms no self-heal.
- #5558 — this walk adds two more status inversions to its evidence.
- powerdns deployment gap — the HelmRelease must advance to 1.2.23; tracked on row 240.
