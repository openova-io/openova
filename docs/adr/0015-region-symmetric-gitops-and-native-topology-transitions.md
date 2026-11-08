# ADR-0015 — Region-symmetric GitOps delivery, one topology contract, native topology transitions

> Status: **Accepted** (2026-11-08)
>
> Refines ADR-0001 (control plane), ADR-0004 (CNPG sync replication — kept), ADR-0009 (per-Org IaC repo — extended to per-region paths). Supersedes the imperative switchover sequencer of `products/continuum/DESIGN.md` K-Cont-2 §4.

## Context

On 2026-11-08 the founder tried to install WordPress on hw307 (two regions, A and B) and to move it between regions. Every step failed. Four audits traced the failures to structural causes, each verified live on hw307:

1. **Region B is not delivered from Git for applications.** Region A's Flux has `catalog-sovereign` and the per-Org `catalyst-tenant-<org>-*` Kustomizations; region B's Flux has only `bootstrap-kit`, `bootstrap-kit-crs`, `sovereign-tls`. The organization-controller creates the per-Org Flux objects by API call in region A only (`core/controllers/organization/internal/controller/per_org_flux.go`). Region B runs no Catalyst controller at all.
2. **Cross-region placement is improvised five ways.** Bootstrap-kit per-region `${SOVEREIGN_REGION_ROLE}` substitution (the only working path); application-controller kubeconfig fan-out whose region-B leg silently applies in A (`RemoteRegionSecrets` never populated); a `_openova_standby` value no chart reads; catalyst-api `org-tenants` writes read by A only; catalyst-api imperatively copying HelmReleases/HTTPRoutes/Services into B (`org_app_standby_regions.go`, `org_app_surface_mesh.go`). Charts use seven different role vocabularies; `bp-wordpress-tenant` renders both CNPG halves into one cluster and separates them by node affinity, so its replica has been unschedulable for 58 days and — with ADR-0004 `dataDurability: required` — every write blocks.
3. **Placement/primary state has six writers and region identity six spellings** (`A`/`B`, `primary`, `me-east-215-a`, `hw-me-east-215-a-rtz-prod`, `meea`, `platform-bootstrap-owned-host`), bridged by string parsing in three normalisers.
4. **The API fabricates references.** `organizationRef: sovereign` (a UI routing constant), `environmentRef: <org>-prod`, `regions: [primary]`, namespaces created on demand, HTTP 201 for objects that can never reconcile. Blueprint `bp-wordpress` is a hand-copied alias of `bp-wordpress-tenant` whose placement schema has drifted.
5. **Two install paths.** The signup funnel writes bare HelmReleases; the console writes Application CRs. Funnel installs have no Application, so no placement, status rollup or transitions.
6. **Status is derived 13 ways**, unknown rounds up to Healthy/Ready, no signal measures whether an app serves or can write, and only HelmReleases and the cutover reach Jobs.
7. **Region-local file state (PVCs) is unmodelled** for multi-region apps.
8. **Verification asserts presence, not function.** No test installs an app, writes and reads data, or moves it between regions on a live Sovereign; most green UAT rows on hw307 were stamped on other environments.
9. **The writer move (Continuum) is an imperative 7-step goroutine** that patches CNPG, HTTPRoutes and PowerDNS directly instead of declaring their native fields, is invisible in Jobs, and runs in region A only.

## Decision

### D1 — Every region reconciles its own desired state from Git (region symmetry)
- Each region's Flux applies `clusters/<sovereign-fqdn>/regions/<region-id>/` from its **own** Gitea. Region B's Gitea pull-mirrors every repo Region A writes (native Gitea mirroring).
- Per-Org Flux objects (GitRepository + Kustomizations) are **committed to Git** under that region path by the organization-controller, never created by API call. Every region therefore gets every Org's Flux objects.
- Each per-Org repo holds one folder per region: `regions/<region-id>/apps/<app>.yaml` — that region's HelmRelease with its role stated explicitly.
- Deleted: application-controller cross-region kubeconfig fan-out and `RemoteRegionSecrets`; catalyst-api `org_app_standby_regions.go`, `org_app_surface_mesh.go`; the legacy `org-tenants` overlay writer.

### D2 — One topology values contract, rendered per region
- Every multi-region chart consumes exactly `openova.topology: {mode, self, primary, peers[]}` (region ids) through one shared helper library chart, and renders **only the `self` half**.
- The database tier declares CNPG's native distributed topology (`spec.replica.primary`, `self`, `source`, `externalClusters` over ClusterMesh). Traffic uses Gateway API weights; DNS uses PowerDNS native health-checked records — all rendered from the same contract.
- ADR-0004 stays: `dataDurability: required` for a delivered pair. A chart renders the synchronous standby name only for a peer that is declared in `peers[]`.
- CI renders every chart once per region and fails on any resource pinned (affinity/label/selector) to a region other than `self`.

### D3 — One writer, one region vocabulary
- The Application's `spec.placement` is the only writer of placement and primary. The application-controller translates it into the per-region files of D1 (Git commits). Nothing patches CNPG/HTTPRoute/DNS at runtime.
- Region identity is the cluster region id (`hw-me-east-215-a-rtz-prod`), read from one registry: the Sovereign's region list. A lint rejects the other spellings outside the registry.

### D4 — References, never fabrication
- Organization, Environment, Region and Blueprint in any API request must name an existing object; the API returns **422** otherwise and never creates a namespace for an unknown Organization. Console option lists are read from those objects.
- Deleted: every `"sovereign"` identity fallback, every `+ "-prod"` derivation, the `primary` region default, the `bp-wordpress` alias Blueprint.

### D5 — One install path
- The signup funnel creates Application CRs exactly like the console. No bare HelmRelease installs.

### D6 — Region-local files are declared and replicated
- A Blueprint declares its region-local volumes. `active-hot-standby` is admitted only if each declared volume has a replication mechanism (object storage replicated across regions, or a volume replication operator). WordPress media move to the replicated store.

### D7 — The object is the status
- Every reconciled kind publishes `Ready{status, reason, message}`; Applications add a per-region `Serving` condition from a functional probe (endpoint answers; database accepts a write). Unknown stays Unknown.
- catalyst-api exposes one `statusOf(object)`; the console renders state + reason verbatim. Bespoke derivations and the Topology "Disaster recovery" panel are deleted.
- Every reconciling object (Application, HelmRelease per region, topology transition) feeds Jobs through the existing `jobs.ActivityBridge`.

### D8 — Topology transitions are native and observable
- Changing the Primary is a **topology transition**: a sequence of Git commits (traffic off → data primary moved → traffic on, ordered by declared dependencies), each applied by Flux and carried out by the native operator, each waited on via native Ready conditions.
- Continuum is reduced to: the cross-region lease (fencing during partition), region-loss detection, and committing the next phase. It runs in **every** region. Its runs appear in Jobs as type `topology-transition` with one row per phase and member.
- The Topology tab shows declared vs actual role per region and links the running Job; a central Continuum dashboard lists every protected group.

### D9 — Function is the acceptance criterion
- A live acceptance suite runs after every delivery to hw307 through the same API the console uses: install WordPress active-hot-standby across A and B; write a post and upload media; read both back; change Primary to B; verify both in B, DNS moved, a complete Jobs record; move back.
- It gates the next deploy-bot bump. A UAT row about function can be ✅ only with this suite's run id on the environment named in the ledger header.

## Consequences
- Region B becomes a full peer: it holds every desired state locally and keeps reconciling if A is lost.
- Large deletions (imperative cross-region writers, status overlays, sequencer steps) — net code shrinks.
- Migration: existing Orgs/apps on hw307 are re-expressed in the per-region layout; bootstrap-kit already follows the per-region pattern and is the template.

## Implementation note — D8 (2026-11-08, additive)

- **Single source of the desired primary.** `Application.spec.placement`. The application-controller renders the Application's Continuum into **every region file** (`spec.desiredPrimaryRegion`, `spec.regions`, `spec.transition.{repo,app,stateful,dependsOn,…}`), so region B — which has no Application CR and no API path to region A — reads the same desired state from Git. The API create/patch path for these Continuums is deleted.
- **Field ownership in one file.** Once a governed Application's file exists, Continuum owns `openova.topology.primary` and `database.topology.promotionToken` of its HelmReleases; the application-controller keeps them on re-render and writes with Gitea compare-and-swap (`PutFileIfMatch`), so neither writer can revert the other. `core/controllers/pkg/regionfile` is the one definition of the file and of these two fields.
- **Phases** (`core/controllers/continuum/internal/transition`, pure): `traffic-off` (old primary's file → wait CNPG `status.demotionToken`; dependents first), `data-primary` (new primary's file with the token → wait `status.lastPromotionToken` + Ready primary + HelmRelease applied; backing services first), `peers`, `traffic-on` (HelmRelease Ready, DB primary, Application `Serving` not False). One commit per evaluation; every wait has a deadline after which the phase reports `failed` (no partial success). DNS / traffic move by the chart's own rendering; Continuum calls no DNS, CNPG or route API.
- **Lease: a file in Git, not a Kubernetes Lease.** A `coordination.k8s.io` Lease lives in one cluster's etcd; regions share no API server, so it cannot fence region A against region B and disappears with region A. The writer Gitea is the one store every region reaches (`gitea-<writer-id>` over ClusterMesh) and the one whose order decides what Flux applies; the lease (`continuum/transitions/<group>.yaml` in the Org repo, outside `regions/`) is compare-and-swapped on its blob SHA and also carries the transition record, so any region resumes a transition from Git. Held only while a transition runs — no heartbeat commits.
- **Region-loss detection.** Each region serves its own native observations read-only on `continuum-<region-id>` (ClusterMesh, ClusterIP). A region that stops answering beyond `spec.transition.regionLossSeconds` is lost; with `autoPromoteOnRegionLoss: true` (default false) the survivor commits the move without a demotion token. Limit: if the lost region hosts the Git **writer**, no region can commit (the follower Gitea is a pull replica of a shared DB) — the group reports Blocked; writer failover is outside D8.
- **Deleted:** the 7-step sequencer and its packages (switchover, dns/PowerDNS lua-records, pdm, cnpg patching, pod exec/delete, raft peers.json, NATS audit, k8s-lease / cloudflare-kv / dns-quorum witnesses, dry-run API), catalyst-api `failback`, `failback/approve`, `PUT continuum`, `PUT …/settings`, `switchover/preview`. `scripts/check-continuum-no-workload-mutation.sh` keeps workload writes out of the Go source and the chart RBAC.
- **Not covered:** platform-scope Continuums not delivered from Git (bp-cnpg-pair, bp-openbao, bootstrap spine adoption) are reported `NotGitDelivered` and never acted on until they are re-expressed in the per-region layout.

## Shipped via
Tracked in #7067; each decision lands as its own PR and is proven by D9 on hw307.
