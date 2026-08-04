# A `cutoverComplete=true` Sovereign still fetches from `charts.loft.sh`

> Env: `hw292.omani.works`, dep `1c56518035a83e03`, `cutoverComplete=true` since 08:12:30Z.
> Found 2026-08-04 while verifying #5527 (per-Org HelmRepositories cutover-aware, PR #5629).
> Filed as **#5650**.

## What #5527 proves, stated exactly

Of **70** HelmRepositories on the Sovereign, **69** point at the local Harbor and **0** point at
ghcr. The generator fix holds; #5527's own claim is verified.

    { "ghcr": 0, "local": 69, "total": 70 }
    CONTROL: cutoverComplete === true

## The 70th

    vcluster-system/loft   url: https://charts.loft.sh   type: default   managed-by: Helm

It is **not inert**, which is the part that matters:

| Probe | Reading |
|---|---|
| HelmReleases referencing `loft` | **1** |
| Status conditions | `Ready=True`, `ArtifactInStorage=True` |
| Artifact `lastUpdateTime` | **2026-08-03T18:36:28Z** — ~10h *after* cutover completed |
| Control (a pivoted repo) | `bp-cilium` → `type=oci`, `oci://registry.hw292.omani.works/openova-io` |

So a Sovereign that reports `cutoverComplete=true` reached the public internet for a chart, after
cutover, and its Flux status reports that as healthy.

Pillar 5 / ADR-0002 claim that post-cutover every Flux reconcile pulls exclusively from the local
Gitea and Harbor. This is one object away from that being true.

## Why the sovereignty proof cannot see it — the finding worth more than the tether

Step-08's deny-egress hold, the sovereignty proof itself, blocks a **named list** of hosts:

    docker.io, ghcr.io, github.com, harbor.openova.io, k8s.io, quay.io, openova.io

`charts.loft.sh` is not on it. The proof therefore returns green while an unblocked external host
is actively being fetched from — and it would return green for **any** future tether to a host
nobody enumerated. The check is allowlist-shaped where the claim is property-shaped.

This is the same failure mode this repo has now paid for three times in one week:

| Instance | Shape |
|---|---|
| `npm test \|\| echo WARN` (#5633/#5626) | a gate that could not go red for two months |
| `grep -q 'key: openova.io/region'` (#5639/#5641) | an assertion on the KEY that passes on an empty VALUE |
| step-08 host allowlist (**#5650**) | a proof that enumerates instances of the thing it claims to exclude |

Per PRINCIPLES III.4, the second recurrence of a class calls for one fail-closed class fix rather
than another per-instance patch. The class fix here is **deny-all egress except the Sovereign's own
endpoints**, which converts step-08 from "these seven hosts are unreachable" into "nothing external
is reachable" — which is what Pillar 5 actually asserts.

## Why the pivot missed this specific object

Step-06 rewrites HelmRepository URLs. Step-10 pivots the vcluster **image** reference
(`harbor.openova.io/proxy-ghcr/loft-sh/vcluster` → local Harbor); its own header records that it
exists precisely because step-06 "only rewrites HelmRepository" URLs. Between the two, the vcluster
*image* is pivoted and the loft *chart source* is not: it lives in `vcluster-system` as a
`managed-by: Helm` object rather than a Flux-owned repo in `flux-system`, so it falls outside the
set step-06 walks. Neither step is wrong about its own scope; the gap is between their scopes, and
nothing asserts over the union.

## Fixes, in the order that matters

1. **Class fix** — make step-08 deny-all-except-local rather than deny-a-list. Without this the
   next unenumerated host repeats the defect silently and the proof keeps certifying a false claim.
2. **Instance fix** — mirror `charts.loft.sh` into the local Harbor and repoint
   `vcluster-system/loft`.
3. **Standing gate** — after cutover, assert that *every* HelmRepository and *every* image
   reference on the Sovereign resolves to a Sovereign-local host, failing closed on the first that
   does not. A property check, which would have caught this on the first fresh prov.

Refs #5650 #5527 #3379 #960
