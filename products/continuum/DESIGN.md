# Continuum — design (ADR-0015 D8)

Continuum moves the Primary of an Application group between regions **only by
committing to Git**. Flux applies each commit; CNPG demotes and promotes; the
chart moves its own traffic and DNS. Continuum waits on each operator's own
status between commits. It writes no workload object.

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
evaluating and resumes by itself if the native condition later holds.

## Code

- `core/controllers/continuum/internal/transition` — the pure phase machine,
  dependency groups, the lease file format.
- `core/controllers/continuum/internal/observe` — read-only native observer +
  the `/v1/observations` HTTP transport.
- `core/controllers/continuum/internal/controller` — the tick loop: read Git,
  observe, evaluate, CAS the lease, make the one commit, write status.
- `core/controllers/pkg/regionfile` — the one definition of the region file and
  of the two fields Continuum owns.

## Chart

`products/continuum/chart` (bootstrap-kit slot 62) renders in every region:
Deployment, read-only RBAC (+ status subresources), `continuum-<region-id>`
Services for every region (own: selects local pods; peers: selector-less
stubs; ClusterIP — never a nodePort), NetworkPolicy, and the Continuum CRD in
`crds/` (byte-identical to bp-catalyst-platform's copy).
