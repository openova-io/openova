# ADR-0021 — Control plane: Git-first write path, one commit library, the intent lock, push from Git to Flux, one event stream

Status: Accepted
Date: 2026-11-13
Supersedes: ADR-0009
Supersedes: ADR-0001 in part: the stream layout of section 6, which the chart `platform/nats-jetstream` renders as three streams for audit, events and billing; one stream, `CATALYST_ORG` on the subjects `catalyst.>`, replaces it
Unfreezes: topology-region-git-layout
Unfreezes: topology-region-file
Unfreezes: topology-continuum
Unfreezes: application-controller

Written by work package CTL-060. It records decisions C1 to C21 of the control plane: how a change of desired state reaches the cluster, how the cluster refuses other writers, how events travel, and the shape of the API. It takes in the result of lab test CTL-050 (`docs/ledger/plan/spikes/CTL-050.md`). It replaces ADR-0009 and one point of ADR-0001 (section "What happens to ADR-0009 and ADR-0001"). The four `Unfreezes:` lines are explained under "What the Unfreezes lines permit".

Terms used here:

- **Intent**: an object that says what should run, for whom and under which policy. The intent kinds are the kinds the lock of C6 lists.
- **Writer region**: the region whose Gitea accepts commits and whose controllers run.

## Context

Measured on `main` at commit `bedc93f4f`, on 2026-11-13.

- The API creates and changes intent objects in the cluster directly, for example the Application create route (`products/catalyst/bootstrap/api/internal/handler/applications.go`). The Application controller then writes files into Git from the object (`core/controllers/application/internal/controller/region_delivery.go`). Git is a copy, not the source. The guard `scripts/check-no-direct-cluster-write.py` counts the direct writes in `scripts/baselines/direct-cluster-writes.json` and lets the count only fall (CTL-010).
- Two repositories carry the layout of ADR-0015: the fleet repository `openova/sovereign-regions` and one `<slug>/catalyst-tenant` per Organization. A third, `<org>/iac` of ADR-0009, is created by `core/controllers/organization/internal/iacbootstrap/bootstrap.go` with branch protection, and `products/catalyst/bootstrap/api/internal/giteapr/` opens pull requests into it.
- Contracts C3 (`docs/contracts/C3-event-subjects.yaml`) and C4 (`docs/contracts/C4-git-layout-and-commit-library.md`) are published (CTL-044). C4 fixes the commit library, the paths of intent and the Flux objects that read them.
- Flux 2.4.0 runs with notification-controller v1.4.0 (`platform/flux/chart/values.yaml`). No manifest declares a `Receiver`. Sources poll Git every minute.
- NATS JetStream runs one stream, `CATALYST_ORG` on the subjects `catalyst.>`, created by code (`core/services/shared/events/bridge_subscriber.go`, `core/controllers/pkg/natsbus/subscriber.go`). The chart `platform/nats-jetstream` also holds three streams for audit, events and billing; they collide with that stream and are off (`streamsEnabled: false` in `platform/nats-jetstream/chart/values.yaml`). A Kafka client remains in `core/services/shared/events/events.go`.
- The handler package and `main` of the API are held by a ratchet, `scripts/check-api-structure.py` (CTL-025). Polling loops are counted by `scripts/check-no-new-polling.py` (CTL-020). The API runs one replica with the `Recreate` strategy (`products/catalyst/chart/templates/api-deployment.yaml`).
- The custom resource definitions of the platform chart are hand-written in `products/catalyst/chart/crds`. The unused second copy of the types, `core/pkg/apis`, was deleted (FND-173).

## Decision

| # | Decision |
|---|---|
| C1 | **Git-first intent.** A route that changes intent validates the request, commits one change to Git and returns. Flux applies it, and the controllers reconcile. The API never creates, updates or deletes an intent object in the cluster. |
| C2 | **Operations are not intent; every mutating route has a write class.** Restart, exec, job retry and "reconcile now" stay direct calls. They are audited before they run, and they change no field that Git owns. Every mutating route is listed in contract C9, the mutating-route table of CTL-045, with one write class: `git`, `database`, `external` or `none`. |
| C3 | **Two repositories, three places of intent.** The fleet repository `openova/sovereign-regions` and one `<slug>/catalyst-tenant` per Organization keep their names and their layout. The fleet repository is the per-Sovereign repository; `clusters/<fqdn>/sovereign/` stays reserved. Intent lives in three places, each a scope of the commit library (contract C4, section 2.2): writer-only intent, every-region intent, and one region's infrastructure. The Organization record and the access grants are written in the fleet repository. The repository `<org>/iac` is retired (CTL-162, CTL-250). |
| C4 | **One commit library.** One Go package, `gitwrite`, on the existing Gitea client `core/controllers/pkg/gitea/client.go`. It adds an atomic multi-file commit, compare-and-swap per file, one robot per Organization as committer, the human as author, a path policy per scope and a refusal of every Secret document. Its interface is contract C4 (CTL-110, CTL-112). Controllers on frozen paths keep their present Git calls until CTL-260. |
| C5 | **Direct commit by default.** A routine change is a direct commit to `main`. Where a second person must approve, the library's pull-request mode is used (CTL-700). Its first user is AIP-245. No mode field is added without a reader. |
| C6 | **Admission lock.** One `ValidatingAdmissionPolicy`, `catalyst-intent-lock`, with one binding per stage. Only Flux identities may create, change the `spec` of, or delete a listed kind. The listed kinds are Application, Organization, UserAccess, Environment, EnvironmentPolicy, Blueprint, Continuum, CNPGPair and the five Flux kinds GitRepository, HelmRepository, OCIRepository, Kustomization and HelmRelease. Status, labels, annotations and finalizers stay open. Deletes by the garbage collector and the namespace controller pass. Break-glass needs the group `catalyst:break-glass` and a reason annotation of ten characters or more, and is recorded in the audit log. Every stage starts at `audit` (CTL-150). A stage moves to `deny` only by its named package and only at zero in the gate records of two consecutive release trains (ADR-0022), and never back. RBAC is the second control. |
| C7 | **The commit is the change record.** No change table and no tracker. Each intent commit carries a `Change-Id` trailer, and the object carries the same value in the annotation `catalyst.openova.io/change-id` (contract C4). A screen reads the state of a change from the cluster through the API's informers. |
| C8 | **Push from Git to Flux.** One Gitea system webhook calls one Flux `Receiver` of type `github` in each region, on the installed Flux 2.4.0 (CTL-130). The source intervals become ten minutes, as a safety net, only after the push check CTL-G01 has passed in two release trains (CTL-132). |
| C9 | **Status comes back on informers.** The result of a commit returns through the informers on `Kustomization` and on the target kind. No Flux `Alert` or `Provider` posts to the API. The API stamps no reconcile request after a commit. |
| C10 | **Flux upgrade.** The manifests move to stable API versions on 2.4 first, with no change of behaviour (CTL-040). Flux then moves to the 2.10 line as one routine upgrade (CTL-620), proven by the release train's fresh provisions (FND-141). The write path waits for neither. |
| C11 | **One event stream.** Kubernetes state travels from the informers to the browser by server-sent events, with no broker. Domain events use NATS JetStream with one stream, `CATALYST_ORG` on `catalyst.>`, as it runs today. The NATS chart declares it once; the three chart streams and the unused bucket `idempotency` are deleted, and the code that creates the stream binds to the declared one (CTL-300). The Kafka client is deleted (CTL-330). Subjects are registered in contract C3. |
| C12 | **One envelope, no outbox in the API.** No new event library. `core/controllers/pkg/natsbus` gains a CloudEvents 1.0 envelope for new subjects. The API has no database, so it has no outbox: an audited action is published and acknowledged before it runs (CTL-320). The outbox rule binds a producer that owns a database. |
| C13 | **One browser pattern.** One live-query hook: a stream event updates or invalidates the query cache, and a 60-second poll runs only while the stream is disconnected (CTL-360). The jobs store gains a subscribe interface and one stream route (CTL-340). |
| C14 | **Controllers.** Controllers use controller-runtime directly; there is no shared kit package. The organization and environment controllers gain watches (CTL-420). The Application controller moves to controller-runtime only after the topology freeze, with the permission of its `Unfreezes:` line (CTL-410). |
| C15 | **No decomposition programme; a ratchet.** The handler package is not split by plan. Its line count and method count may only fall, and new code goes in new packages (CTL-025). Route registration leaves `main()` (CTL-035). |
| C16 | **State out of flat files: deferred.** Not in this plan. The trigger is the Sovereign-lifecycle area moving OpenTofu and the deployment records out of the API process. The unwired store `products/catalyst/bootstrap/api/internal/store/crd_store.go` and the `ProvisioningState` kind are deleted (SOV-035). |
| C17 | **More than one API replica: deferred.** It follows C16 with one package. Until then the API stays one replica with `Recreate`. |
| C18 | **One Organization pipeline.** Every entry point commits one Organization file. The organization-controller performs every step and reports each in status. The in-process pipeline, the legacy overlay writer and the provisioning service's cluster writes and renderers are deleted (epic CTL-200). |
| C19 | **Projection rule.** A decision taken in a database that changes intent reaches the cluster only as a commit made by one committer function. The database never patches the cluster, and no route does it on its behalf. Entitlement decisions arrive as the event `catalyst.entitlement.decision.v1` (CTL-195). |
| C20 | **Typed kinds.** No new module of types. The hand-written definitions in `products/catalyst/chart/crds` stay the schema source and are embedded in the API for validation. Generating definitions from Go types is not in this plan. |
| C21 | **Git survives the cluster.** No new component. Two copies live outside the writer's Gitea: the follower region's copy that exists today, and the Git bundles in object storage that ADR-0036 decides (FND-184). The rebuild-from-Git drill restores from the bundles and is the acceptance test (CTL-610). The follower's one-minute copy job stays a named entry of the polling allow-list until record 0027 decides the follower's copy (CTL-800, after lab test CTL-055). |

## What lab test CTL-050 changed

Lab test CTL-050 ran on k3s v1.31.4+k3s1, Flux 2.4.0 and Gitea 1.22.3 (chart 10.5.0). These points are part of the decisions above.

1. **C8.** The Gitea call `POST /api/v1/admin/hooks` stores a default webhook unless its body sets `config.is_system_webhook` to `"true"`. A default webhook never fires for an existing repository, is copied into every new one, and is not listed, so an ensure loop would add one more at every start. CTL-130 sends the flag. The Receiver answered every delivery with 200, a push reached a new source revision in 2 to 3 seconds on one node, and a source the Receiver does not select was left alone.
2. **C6.** The Kubernetes 1.31 API server refuses an audit annotation whose two branches are a string and `null`. The other branch is the empty string `''`; it compiles, and only break-glass requests carry the annotation. CTL-150 writes the policy so, and its chart test applies the policy to a real API server, because a render test passes a policy the server refuses.
3. **C6.** One request that breaks the rule adds one to the `audit` series and one to the `warn` series of `apiserver_validating_admission_policy_check_total`. The burn-down count reads `enforcement_action="audit"` only.
4. **C6.** After Flux adopts an object the API created, the API stays a co-owner of the spec fields in `metadata.managedFields`. The count of non-Flux spec managers therefore names a manager only for a spec field that no Flux manager also owns. A field that both own holds the value Flux last applied, because an update by another manager that sets another value takes the field away from Flux. The flag `--override-manager` of kustomize-controller also cleared the entry, but it acts on every object Flux applies, so it is not used.
5. **C6.** No route creates Environment objects. The organization-controller creates `<slug>-<envType>` (`core/controllers/organization/internal/controller/environment_ensure.go`), and the API applies the control-plane Environment in the background (`products/catalyst/bootstrap/api/internal/handler/post_handover_spine_apps.go`). Under stage `environments` at deny both were refused. So the folder `intent/environments/` of contract C4 is not created, and stage `environments` stays at `audit` until a later decision record gives Environment objects one writer.
6. **C4 and C1.** The batch contents call of Gitea is all-or-nothing: a wrong `sha` or an existing path fails the whole call with 422 and changes nothing. The error names the sha, not the path, and only the first mismatch. Adoption changes nothing either: a byte-equal file applied by Flux keeps `metadata.generation`, and Flux adds only its two labels.

## What happens to ADR-0009 and ADR-0001

- **ADR-0009** is replaced whole. Intent is a direct commit to the fleet repository or to an Organization's `catalyst-tenant` repository (C1, C3, C5), not a pull request into `<org>/iac`. The `iac` repository, its robot, its branch protection with three named checks and its Flux subscription are retired: CTL-162 stops the writes and CTL-250 stops the creation. ADR-0009 gets one header line that points here.
- **ADR-0001** keeps its body. Its section 6 puts Catalyst's own events on NATS JetStream in a layout that the chart `platform/nats-jetstream` renders as three streams for audit, events and billing, now switched off. The declared layout is the one stream of C11. ADR-0001 gets one row in its header table that points here.

## What the Unfreezes lines permit

Work package FND-120 freezes the topology surface of the stability register, `docs/ledger/stability.yaml`. A pull request that changes a frozen path carries the trailer `Decision: ADR-0021` and waits for FND-120. This record is cited for these changes only:

| Entry | Work package | The change |
|---|---|---|
| `topology-region-git-layout` | CTL-120 | The fleet intent path: one ignore line in the region source, a Kustomization `sovereign-intent` in the writer region, and a delete patch for it in the follower overlay. |
| `topology-region-git-layout` | CTL-122 | For an Organization's home region only, the organization-controller adds an account `org-intent-<slug>` and a Kustomization `catalyst-tenant-<slug>-intent`. The objects it renders today stay byte-identical. |
| `topology-region-git-layout` | CTL-130 | One Flux `Receiver` in the region base. |
| `topology-region-git-layout` | CTL-132 | Source and Kustomization intervals go from one minute to ten; the retry interval stays one minute. |
| `topology-region-git-layout`, `topology-continuum`, `application-controller` | CTL-260 | The controllers' Git calls move onto the commit library. The files written stay byte-identical. |
| `application-controller`, `topology-region-file` | CTL-410 | The Application controller runs under controller-runtime with its reconcile body unchanged, and the region file renderer adds two labels to each HelmRelease it writes. |
| `application-controller` | CTL-415 | The Application controller hands the identity realm and issuer to every Application of a customer Organization. |

CTL-172, CTL-174 and CTL-176 change the platform charts' `application-cr.yaml` templates and the bootstrap kit. No closed entry of the register holds those paths, so they need no line here.

## Alternatives rejected

**C1.** (a) Keep "object first, controller copies to Git" and add backups: Git stays a copy of the cluster, so the cluster stays the only source. (b) Let the API write the cluster and Git in one request: two writers, no single source. (c) A separate commit service: one more deployable for a library call.

**C2.** (a) Everything through Git, restart included: a restart is not state and would need empty commits. (b) Any write allowed on objects that Flux does not manage: the resource browser would stay a direct write channel.

**C3.** (a) The Organization record in the Organization's own repository: the Flux objects that read that repository are rendered from the record, so nothing could apply it first, and an Organization could lift its own suspension. (b) One repository for everything: no credential boundary between the sovereign-admin and an Organization. (c) One repository per Application: the controllers write one per Organization and the running layout proves it. (d) Renaming or re-laying the repositories: the layout is live and is frozen.

**C4.** (a) A new Gitea client: the existing one is sound. (b) Calling the `git` binary: it needs a working tree and a binary in the image. (c) Keeping the provisioning service's own client, `core/services/provisioning/github/client.go`: a second client of the same contents interface. (d) Moving every controller onto the library in the same package: several of those files are on frozen paths.

**C5.** A pull request for every change (ADR-0009): it puts a merge between every click and the cluster, and a routine change needs no second person. A mode field with no implementation and no reader is rejected too.

**C6.** (a) A Kyverno policy: the lock must hold while the policy engine is down or not yet installed, and a native policy runs inside the API server. (b) RBAC only: cluster-admin credentials and wildcard roles pass it. (c) Deny from day one: it would break every route not yet moved. (d) A parameter object for the writer list: whoever may edit it could add a writer. (e) Listing the organization-controller and the API as writers of Environments: two writers of intent outside Git.

**C7.** (a) A change table in a new database with a tracker, an outbox and three routes: a second system, when Git already holds who, what and when, and the cluster holds applied, ready and failed. (b) A custom resource per change: high-churn records in etcd. (c) No correlation: a handler could not tell which commit produced the object it waits for.

**C8.** (a) Wait for a newer Flux before any webhook: `Receiver` is `notification.toolkit.fluxcd.io/v1` and the notification controller is installed. (b) One webhook per Gitea organisation, managed by a new package and a sweeper: one system webhook covers every repository. (c) An organisation webhook as the default: it also works, but needs one hook per organisation.

**C9.** (a) Flux `Alert` and `Provider` posting to an API route: a new endpoint and a new secret to deliver facts the informer cache already holds. (b) Stamping `reconcile.fluxcd.io/requestedAt` after each commit: a cluster write to work around a poll.

**C10.** (a) Stay on 2.4: six minor releases behind. (b) The Flux Operator: its AGPL-3.0 licence, and its console writes the cluster without a commit. (c) Make the upgrade a precondition of the write path: the Receiver does not need it.

**C11.** (a) The three chart streams: the broker refuses them beside the wildcard stream, and the Helm upgrade that tried rolled back. (b) Everything on NATS, Kubernetes state included, through a projector: a broker hop on the one path that works. (c) Keep polling and tune the intervals.

**C12.** (a) A new event package with a relay and an outbox table in a new API database: two new systems for one event type. (b) Change-data capture: another component. (c) Publish after the action, fire and forget: an audited action could run with no record.

**C13.** (a) A stream per page: every page would carry its own stream shape. (b) WebSocket for everything: no gain over server-sent events for one-way updates.

**C14.** (a) A shared controller kit: the organization, environment, user-access and Continuum controllers already call controller-runtime directly. (b) Port the Application controller now: it is on the frozen path. (c) One controller binary for every kind: larger than the gain.

**C15.** (a) Split the handler package into many packages with a module interface: a decomposition programme that touches the package every area edits. (b) Rewrite as services: it discards the tests. (c) No brake: the package keeps growing.

**C16.** (a) A control-plane database now: nothing in this plan needs one once C7 and C12 hold. (b) Custom resources as the store: an event log does not fit in an object.

**C17.** Two roles from one binary now: a decomposition programme by another name.

**C18.** Two pipelines kept "for compatibility": every gap between them must then be closed twice.

**C19.** (a) Billing calls an internal route that patches the object: a Flux prune or a later commit silently undoes a suspension. (b) Billing writes Git itself: a second committer and a second credential.

**C20.** (a) One new module with every kind and generated definitions: it changes every import of the frozen Application controller, and the definitions are single-writer files. (b) Keep the dead copy of the types.

**C21.** (a) A new component that copies on every push and writes the archive: a scheduled job and the existing peer copy meet the need. (b) An external Git server as the only copy: a dependency outside the Sovereign.

## Consequences

- Each mutating route moves to its write class in batches, Applications first and the Organization last, and each lock stage moves to `deny` only at zero.
- The three chart streams, the bucket `idempotency` and the Kafka client leave the tree; `CATALYST_ORG` is the declared stream.
- Limits accepted now: one API replica; Environment objects keep two writers outside Git, under stage `environments` at `audit`; the follower's Git copy is a one-minute job.

## Shipped via

Work packages of area CTL in `docs/ledger/plan.yaml`. Guards: CTL-010, CTL-020, CTL-025. Contracts: CTL-044, CTL-045. Write path: CTL-110, CTL-112, CTL-120, CTL-122, CTL-126, CTL-130, CTL-132, CTL-160, CTL-162, CTL-170 to CTL-178, CTL-180, CTL-190, the epic CTL-200, CTL-210, CTL-212, CTL-220, CTL-222, CTL-224, CTL-230, CTL-240, CTL-250, CTL-260 and CTL-700. Lock: CTL-150 and the stage packages CTL-178, CTL-209, CTL-212 and CTL-264. Events and screens: CTL-300, CTL-320, CTL-330, CTL-340, CTL-345, CTL-350, CTL-360, CTL-370 to CTL-376, CTL-420. Controllers: CTL-410, CTL-415. Git survival and Flux: CTL-610, CTL-620, CTL-800. Gate checks CTL-G01 to CTL-G11.
