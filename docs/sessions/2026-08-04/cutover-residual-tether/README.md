# A `cutoverComplete=true` Sovereign still fetches from `charts.loft.sh`

> Env: `hw292.omani.works`, dep `1c56518035a83e03`, `cutoverComplete=true` since 08:12:30Z.
> Found 2026-08-04 while verifying #5527 (per-Org HelmRepositories cutover-aware, PR #5629).
> Filed as **#5650**; fix ships in `bp-self-sovereign-cutover` 0.1.162.

## Correction notice

The first version of this document and of #5650 claimed step-08's deny-egress hold "blocks a named
list, so it cannot detect any unenumerated tether." **That was wrong**, and acting on it would have
meant rewriting a mechanism that is already correct. The live Job spec:

    DEFAULT_DENY_EGRESS=true
    ENFORCE_CIDR_BLOCK=true

`defaultDenyEgress: true` has shipped since #3678. It emits a cluster-wide CCNP with
`endpointSelector: {}`, `enableDefaultDeny.egress: true`, and an allow-list of only the Sovereign's
own CIDRs plus the IaaS provider API. Every public destination is black-holed, `charts.loft.sh`
included. `blockedDomains` survives only as the active call-home assertion — the hosts we positively
prove unreachable from inside the policy. I read the legacy subtractive branch and generalised from
it without checking which branch ships. The corrected finding below is narrower and more useful.

## What #5527 proves, stated exactly

Of **70** HelmRepositories, **69** point at the local Harbor and **0** at ghcr. The generator fix
holds; #5527's own claim is verified.

## The 70th, and the real gap

    vcluster-system/loft   url: https://charts.loft.sh   type: default   managed-by: Helm

| Probe | Reading |
|---|---|
| Referenced by | 1 HelmRelease |
| Status | `Ready=True`, `ArtifactInStorage=True` |
| `spec.interval` | **15m**, `suspend` unset |
| Artifact fetched | **2026-08-03T18:36:28Z** |
| `ccnp/cutover-egress-block` now | **ABSENT — torn down after the hold, by design** |
| Control (a pivoted repo) | `bp-cilium` → `oci://registry.hw292.omani.works/openova-io` |

Both of these are true at once, and that is the whole finding:

- nothing external was reachable for the 600 seconds of the hold, and
- this Sovereign fetches from `charts.loft.sh` every 15 minutes.

The hold is **time-boxed and then removed**. It proves the Sovereign *survives* 600s of isolation.
It does not prove the Sovereign has *no external dependency*, and ADR-0002 asserts the second. A
15-minute reconcile interval can straddle a 10-minute window entirely — the tether need never fire
while anyone is watching, and it resumes the moment the policy is gone.

**Reachability-during-a-window and dependency-in-the-object-graph are different properties.** Only
the first was tested.

## Why the pivot missed this object

Step-06 rewrites HelmRepository URLs; step-10 pivots the vcluster **image** reference. Step-10's own
header records that it exists precisely because step-06 "only rewrites HelmRepository" URLs. Between
them the image is pivoted and the loft chart source is not: it lives in `vcluster-system` as a
`managed-by: Helm` object rather than a Flux-owned repo in `flux-system`. Neither step is wrong about
its own scope; the gap is between their scopes, and nothing asserted over the union.

## The fix (0.1.162)

A pre-hold **Flux source host lint**, modelled on the ref-host lint step-08 already runs for pod-spec
image hosts (#5026/#5036) — a static assertion over the object graph, independent of timing. Every
non-suspended `HelmRepository` / `GitRepository` / `OCIRepository` must resolve to a Sovereign-local
host. `allowHosts` defaults **empty**, so an external host is a tether until a human declares an
exception in Git under review.

Its exemption set is deliberately narrower than the pod-spec lint's. That one exempts step-04
`HOST_PROJECT_MAP` hosts because containerd rewrites image pulls via `certs.d`. Flux's
source-controller fetches these URLs itself and never traverses containerd, so a `certs.d` redirect
cannot cover a source object — `harbor.openova.io` as a source URL stays a failure.

## The test, and what it caught

`chart/tests/flux-source-host-lint.sh` extracts the shell function from the **render** rather than a
hand-kept copy (per #5646, where a suite validated a copy that had silently drifted) and drives it
against a stub `kubectl` in both directions: external source fails, local source passes, and the
*same* source with an `allowHosts` entry passes — that last case being the vacuity control proving
the failure keyed on the host rather than on something incidental.

It earned itself immediately. The first draft **failed open**: when the scratch file could not be
created, every append silently no-op'd, `-s` was false, and the lint returned PASS having examined
nothing. A guard reporting success from an empty measurement is the exact defect this lint exists to
eliminate, and it would have shipped unnoticed had the suite only ever been run in the passing
direction. Now fail-closed, with case 8 pinning it.

That makes four instances this week of the same underlying error — checking that something is
present instead of checking what it says:

| Instance | Shape |
|---|---|
| `npm test \|\| echo WARN` (#5633/#5626) | a gate that could not go red for two months |
| `grep -q 'key: openova.io/region'` (#5639/#5641) | an assertion on the KEY, passing on an empty VALUE |
| step-08 covers reachability only (**#5650**) | a proof of the wrong property, correct as far as it went |
| the new lint's first draft (**caught pre-merge**) | PASS returned from a measurement that never ran |

Refs #5650 #5527 #3379 #3678 #960
