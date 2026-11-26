# Continuum — design (ADR-0015 D8)

Continuum moves the Primary of an Application group between regions **only by
committing to Git**. Flux applies each commit; CNPG demotes and promotes; the
chart moves its own traffic and DNS. Continuum waits on each operator's own
status between commits. It writes no workload object; besides Git and its own
status, its one write is the reboot agent's lock (below).

The pre-D8 design (an in-process 7-step sequencer that patched CNPG, HTTPRoutes
and PowerDNS, exec'd into pods and ran in region A only) is superseded and
deleted; see `docs/adr/0015-region-symmetric-gitops-and-native-topology-transitions.md`.

## What Continuum is

| Concern | Mechanism |
|---|---|
| Desired primary | `Application.spec.placement` → the application-controller renders the Continuum (`spec.desiredPrimaryRegion`) into every region file of the Application |
| The move | ordered commits to `regions/<region-id>/apps/<app>.yaml` of the Org repo |
| Waiting | native status: CNPG `status.demotionToken` / `status.lastPromotionToken` / Ready, HelmRelease Ready + applied values, Application `Serving` |
| One actor | a Git lease file `continuum/transitions/<group>.yaml`, compare-and-swapped through the writer Gitea |
| Region loss | a peer whose `continuum-<region-id>` observations stop answering; acted on only with `spec.transition.autoPromoteOnRegionLoss: true` |
| Observability | each member's Continuum `status` (Ready condition, `transition`, `activity[]`) and its Application's `status.activity[]` → Jobs type `topology-transition`; the central dashboard (Cloud → Continuums) |
| A move and a drain take turns | while a transition of its region runs, each region's Continuum holds kured's own lock (the `weave.works/kured-node-lock` annotation of DaemonSet `kube-system/kured`, nodeID `continuum-<region>`, TTL 5 min renewed; it lapses by kured's own expiry if the controller is gone). A move starts only when its old and new primary regions hold it, and no step runs while kured drains a node there or its state there could not be read; the hold lasts while the move can still act, as this tick's evaluation says (not the stored record): a Failed move keeps it at most `rebootAgent.waitSeconds` after it failed, a move whose demotion re-issue bound is exhausted releases it, a Blocked move releases it until it acts again, and a record left in flight that the evaluation no longer walks (a move re-targeted back to the primary every file still names: Converged) holds nothing (condition `RebootAgentHeld`: True `HeldForMove`, False `ReleasedFailedMove` / `ReleasedReissueExhausted` / `ReleasedBlockedMove`); each wait is bounded (`rebootAgent.waitSeconds`, 1200) from when it began (a move the evaluation stops walking ends its wait, so a move asked for again later waits anew) and named (condition `Interrupted`), and the time a wait holds a step that has begun does not count against that step's phase deadline |

## Phases

For a group (an Application and the governed Applications in its
`spec.dependsOn`), in order:

1. **traffic-off** — dependents first. Commit `openova.topology.primary: <to>`
   to the old primary's file. The chart scales the app down there and CNPG
   demotes the cluster. Wait: the live HelmRelease applied it and the CNPG
   Cluster published `status.demotionToken` (stateless: HelmRelease Ready).
2. **data-primary** — backing services first. Commit `primary: <to>` and
   `database.topology.promotionToken: <token>` to the new primary's file. Wait:
   CNPG `status.lastPromotionToken == token`, Cluster Ready as primary,
   HelmRelease Ready.
3. **peers** — commit `primary: <to>` to every other region's file.
4. **traffic-on** — wait: HelmRelease Ready in the new primary, CNPG primary
   Ready, Application `Serving` not False.

Each phase has a deadline (`phaseTimeoutSeconds`, default 900). Past it the
phase reports `failed` and the group reports Failed; the machine keeps
evaluating and resumes by itself if the native condition later holds. A
step's clock stands still while the reboot gate holds it (at most
`rebootAgent.waitSeconds` per wait), so waiting for a drain does not fail it.

A demotion that does not complete is issued again. When the old primary's
instance restarts while CNPG applies the demotion (its node drained), the
instance manager demotes it at start-up without a token and the operator never
publishes one. Seen from the Cluster's own status (no instance reported as
primary, no switch in progress, Ready, no token) for a minute, traffic-off is
run again: the old primary's file names it primary once more, and when its
Cluster runs a primary the demotion is committed again (at most 3 times;
condition `Interrupted`, reasons `DemotionInterrupted` /
`DemotionReissueExhausted`). It happens only inside traffic-off, before
anything is committed to the new primary.

## Code

- `core/controllers/continuum/internal/transition` — the pure phase machine,
  dependency groups, the lease file format.
- `core/controllers/continuum/internal/observe` — read-only native observer +
  the `/v1/observations` HTTP transport.
- `core/controllers/continuum/internal/controller` — the tick loop: read Git,
  observe, evaluate, CAS the lease, make the one commit, write status, hold or
  release the reboot agent.
- `core/controllers/continuum/internal/reboot` — kured's lock: read it (and
  the node it cordons), take, renew and release the hold.
- `core/controllers/pkg/regionfile` — the one definition of the region file and
  of the two fields Continuum owns.

## Chart

`products/continuum/chart` (bootstrap-kit slot 62) renders in every region:
Deployment, read-only RBAC (+ status subresources, + a Role on DaemonSet
`kured` in the reboot agent's namespace: get and patch of its lock), `continuum-<region-id>`
Services for every region (own: selects local pods; peers: selector-less
stubs; ClusterIP — never a nodePort), NetworkPolicy, and the Continuum CRD in
`crds/` (byte-identical to bp-catalyst-platform's copy).
