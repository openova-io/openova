# ADR-0033 — Access plane: one Blueprint with three kinds, a fenced pod per session, sealed recordings

Status: Accepted
Date: 2026-11-13

Written by work package ACC-060. It records how a person reaches a target inside a Sovereign: who may ask, who approves, how the credential reaches the session, how the session is recorded, and how it is reviewed. A target is a set of pods, a machine over SSH or a database; web consoles and Windows desktops follow later.

Terms used here:

- **Target**: what a person may reach in a session, and the rules for it.
- **Grant**: one person's time-bound right to one target, given by approval.
- **Session**: one live or finished connection of a person to a target under a grant.
- **Session pod**: the pod that the access controller builds for one session and deletes when it ends.
- **Broker**: the HTTP part of the access controller. It relays the browser's tunnel to the session pod, takes in recordings and serves playback.
- **Break-glass**: a session opened without a grant, by a member of a named group who gives a reason.

## Context

Measured on `main` on 2026-11-13.

- Apache Guacamole 1.5.5 is installed by default. The Blueprint `bp-guacamole` (`platform/guacamole/chart`) is kit slot 52 (`clusters/_template/bootstrap-kit/52-bp-guacamole.yaml`), namespace `guacamole`. It runs `guacd` and the Guacamole web application, both pinned by tag, with a PostgreSQL database of its own (`platform/guacamole/chart/templates/cnpg-cluster.yaml`), a seed Job and a CronJob that runs every minute (`platform/guacamole/chart/templates/jdbc-schema-seed-job.yaml`, 718 lines).
- On a Sovereign with two regions the web application runs in the primary region only. The other region renders its Service as a ClusterMesh stub with no local pods. The chart does this with its values `crossRegion` and its file `platform/guacamole/chart/templates/crossregion-netpol.yaml`.
- The exec proxy `k8s-ws-proxy` (`core/cmd/k8s-ws-proxy`) runs as a DaemonSet in kit slot 51, namespace `catalyst-system`. It carries a gateway session into a pod.
- Sign-on to the gateway is `bp-oidc-gate` (kit slot 13c), with its entry `guacamole`: the authorization-code flow against the Keycloak realm `sovereign`. The web application trusts the identity header that the gate sets, and only gate pods may reach it.
- The console has a pod shell of its own: a WebSocket through catalyst-api to the Kubernetes API (`products/catalyst/bootstrap/api/internal/handler/k8s_exec.go`).
- `UserAccess` (`access.openova.io/v1alpha1`, cluster-scoped, `platform/crossplane-claims/chart/templates/crds/useraccess.yaml`) binds a person to one of five tiers. A controller turns it into role bindings. It has no time window and no approval. The organization controller rewrites its `spec` as a whole on every reconcile.
- No session is recorded. No connection of the chart carries a recording parameter, the recording volume of `guacd` is an `emptyDir`, and nothing uploads a recording.
- Documents state the opposite. `platform/guacamole/README.md`, `platform/guacamole/blueprint.yaml` and `docs/ARCHITECTURE.md` describe session recordings on object storage as present. Work package ACC-050 corrects them.
- catalyst-api invents sessions. With no gateway client wired, `synthesizeSession` in `products/catalyst/bootstrap/api/internal/handler/k8s_exec.go` returns a random connection identifier and an address that the gateway does not know. The session list is a map in memory. `SetGuacamoleClient` is called by tests only.
- Remote access has no approval, no time-bound grant and no scoping per Organization. The design sketch in `platform/guacamole/README.md` says that none of the three is built.
- No code uses the SSH, database or PKI engines of OpenBao.
- The NetBird chart `platform/netbird` has no slot in the kit.
- Sign-on through the gate to the gateway's connection list was walked on a live Sovereign (rows 35 and 115 of `docs/ledger/UAT.md`). No walk has opened a session, through the gateway or through the console shell.

## Decision

**A1 One Blueprint with its own kinds.** The access plane is delivered by the existing Blueprint `bp-guacamole`. Its resource kinds arrive with that Blueprint and are absent without it. No platform kind is added and none is changed. The Blueprint keeps its name and its namespace `guacamole`.

**A2 The default installation is fixed in place, not removed.** The gateway stays in the default kit (slots 51 and 52) until the parts of this decision replace it. Work package ACC-010 fixes it in place first.

**A3 The gateway is `guacd`; the web application and its database go.** `guacd` 1.6.0 is the protocol engine and the recorder, one process per session. The Guacamole web application, its PostgreSQL database, the seed Job and the CronJob are deleted (ACC-102). A broker in the access controller relays the browser's tunnel. The gateway stores no credential, because it has no store. Spike ACC-020 proves the mechanics before any package depends on them.

**A4 Three kinds: target, grant, session.** `AccessTarget` says what can be reached and under which rules. `AccessGrant` is a request that becomes a time-bound grant when it is approved. `AccessSession` is one live or finished session; it is runtime state only. Group `access.openova.io`, version `v1alpha1`, all namespaced. A request and a grant are one object in two phases.

**A5 `UserAccess` is not extended.** It stays the standing tier grant. It gains no target, window or approver field. The tier it gives decides who may request and who may approve.

**A6 Grants live in Git; an approval is an entry committed by the approver.** Targets and grants are files in the fleet repository. Only catalyst-api writes them, through the commit library, in the folder that `docs/contracts/C4-git-layout-and-commit-library.md` reserves for the access kinds. An approval is a second commit, authored by the approver, that adds one entry to `spec.approvals` of the grant. It is not a pull request. The API refuses an approval by the grant's own subject.

**A7 The authentication chain.** The browser signs on at the existing gate `bp-oidc-gate` (authorization-code flow against Keycloak). The gate passes the request to the broker. The broker trusts the gate's identity headers and accepts connections only from gate pods. It opens a session only for an active grant of that identity, or for break-glass (A18).

**A8 Per-session credentials through the mount of the session pod.** OpenBao issues every credential at session start. The Secrets Store CSI mount delivers it into one container of the session pod: an SSH key with a signed certificate, a dynamic database user, or a client certificate for pod exec. No Kubernetes Secret object, no database row and no value in Git holds it. The session pod holds no service-account token. The person's own tool container never mounts the credential.

**A9 One fenced pod per session, built by a controller.** A watch-based controller builds, for each `AccessSession`, a network policy, a `SecretProviderClass` and a pod, in that order. It owns them through owner references. The pod is deleted when the session ends. No sweep, reaper or undo path exists.

**A10 Placement of the session pod.** A session that reaches its target over the network (`ssh`, `database`, later `web` and `rdp`) runs in the target's own namespace, inside the Organization's boundary. A session that reaches its target through the Kubernetes API (`pod`) runs in namespace `guacamole`.

**A11 The database credential never reaches the person's tool.** In a database session a sidecar proxy holds the dynamic user and authenticates to the database. The tool container connects to the loopback address without a password and has no shell. PostgreSQL on CloudNativePG is the first engine.

**A12 Recording.** `guacd` writes a screen recording for every session and a typescript for text sessions. The session agent sends the files in chunks to the broker while the session runs. The broker writes them to object storage. The chunks form a hash chain. A manifest is signed with an OpenBao transit key, and its digest is published on the event stream. A session whose recording cannot be stored is ended. The bucket and its credential come through the `object-storage` capability binding of ADR-0020, as for any other consumer. Recordings are written under object lock in compliance mode. Retention is set per target, 365 days by default, and the lock equals it. Spike ACC-030 measures whether the in-cluster object store enforces the lock. If it does not, what replaces the lock is decided before ACC-111 and recorded in a new decision record.

**A13 Playback and separation of duties.** Recordings are read only through the broker, which verifies the chain first. A person can never replay their own session. Every replay is an event. The player is a page served by the broker.

**A14 Review by an agent: rules decide, the model explains.** A review agent reads the text of sessions. A deterministic rule match on typed input is the only thing that can end a session or suspend grants, and only where the target's policy says `enforce`. The default is `advisory`. Every finding needs the disposition of a named person. Review uses only a model alias that keeps data inside the Sovereign.

**A15 Isolation per Organization.** Targets, grants and sessions are objects in the Organization's host namespace. Session pods run inside the Organization's network fence, with a stricter policy of their own. Recordings sit under one key prefix per Organization and are read only through the broker.

**A16 Events and audit are written ahead.** The controller publishes `catalyst.access.*` events on the one event stream. A session does not start unless its opening event was acknowledged. No component polls for sessions, grants or recordings.

**A17 The console pod shell becomes an access session.** The invented session code in catalyst-api is deleted first (ACC-101). Once the recorded pod session is proven, the console's shell button opens an access session (ACC-123). The direct exec route stays only as break-glass.

**A18 Break-glass.** A member of the break-glass group may open a session without a grant by giving a reason. Such a session is always recorded with keystrokes, raises its own event and always needs a disposition.

**A19 One gateway, browser only.** The grant carries no field that is specific to a gateway, so a second driver for native tools (a local `ssh`, `kubectl` or database tool) can be added later. None is built or piloted now, and no capability contract is written for it.

**A20 Class, leftovers and the version.** `bp-guacamole` is class `platform-component` (ADR-0020). The NetBird chart `platform/netbird` is not part of the access plane. Whether it is deleted is decided separately, and no package named here changes it. SSH to the platform's own nodes is not a default target. `guacd` 1.6.0 arrives with the session image (ACC-106) as routine upgrade work. The web application is not upgraded; it is removed.

**A21 One controller per Sovereign, in the primary region, until regional brokers exist.** Until ACC-250 the access controller runs only in the region whose role is primary. The other region renders the controller's Service as a ClusterMesh stub with no local pods, so its gate reaches the one controller, as the web application is reached today. Sessions reach targets in the primary region's cluster only. While this holds, targets and grants are intent that is applied in one region (A6). With regional brokers they move to the place that every region applies.

## Alternatives rejected

**A1.** (a) New platform kinds in the platform chart. The platform's entities are fixed: Sovereign, Organization, Environment, Application, Blueprint and User. A kind that is empty on every installation without the access plane is a kind of a Blueprint. (b) A new Blueprint name such as `bp-access`. A rename touches the kit slot, the catalogue, the class list, the ring list and many chart lists, and it adds no function. (c) A new API group. `access.openova.io` exists and fits.

**A2.** (a) Remove slots 51 and 52 from the default kit now. It changes the kit list, the placement file, the dependency file, the catalogue and the ring lists. It also discards the one part that was walked on a live Sovereign: sign-on to the connection list. (b) Leave the gateway as it is until the rebuild. The fixes of ACC-010 do not wait for it.

**A3.** (a) Keep the web application and drive its database with a controller. The controller would need a standing super-administrator credential for the gateway. Per-session credentials would rest in the gateway's database for the life of the session. The seed Job, the database cluster and the sign-on secret job would stay. (b) Keep the web application without its database and hand it sessions through its encrypted-JSON extension. That extension takes the `guacd` address from the server's own settings, not from the connection, so one web application serves one `guacd` pool. Isolation per Organization (A15) would then need one web application per Organization. The hand-off also passes the encrypted credential through the browser. (c) Replace Guacamole with another gateway. Of the open-source candidates, one is archived, one forbids embedding, one is copyleft with paid governance features, and one has no 1.0 release. What stays from Apache Guacamole is the part with no substitute: the protocol engine, its recorder and its browser library, all under the Apache-2.0 licence.

**A4.** (a) Four or more kinds (target, policy, request, grant, session, rule, review). Each extra kind is a file layout, a lock rule and a screen. A policy is a block of the target. A request and a grant differ only by approval and time. The output of review is part of the session's status. (b) Sessions in Git. A session is runtime state, like a Job; only desired state is in Git. (c) Cluster-scoped kinds. The host namespace is the boundary of an Organization; namespaced kinds make isolation a property of the API server.

**A5.** Extending `UserAccess` with target, window and approvers. It is a platform kind on every installation, and these fields would be empty wherever the access plane is absent. It is cluster-scoped, and the organization controller rewrites its `spec` as a whole on every reconcile, so a grant in it would be wiped. Its move to Git is other work (CTL-210, CTL-212), and a second meaning on it would couple the two. What is kept from the proposal: there is no second tier model.

**A6.** (a) Approval as a pull-request merge. The approval is data of the grant, and the pending grant must exist as an object before anyone approves it. A console button would merge on the approver's behalf anyway. (b) A separate approval kind. It is one more file per grant for one list entry. (c) Grants in the Organization's own repository. An Organization could then approve its own access. (d) Approval state in a database. It changes desired state, so it belongs in Git.

**A7.** (a) The gateway's own OpenID Connect extension. Upstream supports the implicit flow only. (b) Passing the console's session token to the broker. The broker would need the console's signing keys and a second token audience, and the gate is already walked.

**A8.** (a) A Kubernetes Secret per session, owned by the pod. It rests in etcd, and the credential of a workload never becomes a Secret object. (b) The broker fetches credentials and hands them to `guacd`. One process would then hold the right to mint credentials for every Organization. With the mount, only a pod that the controller built, in the target's namespace, under one service-account name, can obtain a credential, and OpenBao enforces the path by the pod's identity. (c) The session agent calls OpenBao itself. The Organization fence (NET-170) denies egress to the OpenBao namespace. The mount is fetched by the node, outside the pod network.

**A9.** (a) A shared, long-lived pool of `guacd`. One process would hold the credentials and the network reach of every live session. (b) Session logic inside catalyst-api. Its handler package may only shrink, and the API is one replica that restarts on every release.

**A10.** (a) All session pods in namespace `guacamole`. The Organization fence denies ingress from other namespaces to the pods of an Organization, so SSH and database targets inside an Organization would be out of reach. (b) All session pods in the target's namespace. The fence denies egress from an Organization namespace to the namespace of the exec proxy. Pod exec is carried by the API server, not by the pod network, so it needs no place inside the fence.

**A11.** (a) Give the tool a password file or variable. An interactive tool can read its own files and environment, so the person could read the credential, and it would land in the recording. (b) One shared web database tool for all users. The free edition of the leading one keeps sign-on and secret providers for its paid edition, and another one leaked stored credentials across users. (c) All database engines at once. The platform's own database is PostgreSQL, and OpenBao has no plug-in for several common engines.

**A12.** (a) Upload at the end of the session. The recording would exist only on a pod disk for the whole session. (b) The session pod writes to object storage itself. Every session pod would hold a storage credential. (c) Integrity by a digest in the session object only. The object is runtime state and is deleted after 30 days. (d) Keystroke capture on by default. It records typed passwords.

**A13.** (a) Playback through the gateway's history extension. It needs the gateway database and a local directory. (b) Direct, signed links to the bucket. The check that a reviewer is not the recorded person would then sit in whoever mints the link.

**A14.** (a) The model's verdict ends sessions. A model can be steered by text on the screen, and ending a production session on an inference risks an outage. (b) No automated action at all. Review is meant to be able to act; the middle way is a narrow bar of rules that is off by default. (c) Screen reading with a vision model now. It needs a vision-capable model inside the Sovereign, and none exists yet.

**A15.** (a) One full access stack per Organization. That is one controller per Organization for a feature that most Organizations use rarely. (b) One bucket per Organization now. Until the object-storage capability provides a bucket per Organization, the key prefix is the boundary.

**A16.** Audit that is sent and not confirmed. A privileged session could then run with no record of its start.

**A17.** Two shell paths. One would be recorded and one not, and the unrecorded one would be the convenient one.

**A18.** (a) No break-glass. When Git is down or no approver is reachable, people would fall back to the cluster-administrator kubeconfig, which carries no personal identity and records nothing. (b) Break-glass for every tier.

**A19.** (a) A second gateway behind the same grant now. That is two gateways to patch and prove before the first is proven. (b) The cloud's own bastion or session service as a second driver. It is a second implementation of a contract that has no proven first implementation, and no capability contract is written before a second implementation is scheduled. It becomes possible once the gate checks ACC-G02 to ACC-G09 are required and a provider pack names such a service.

**A20.** (a) Class `network-security-service`. That class is for services an Organization installs; this is a controller of the Sovereign. (b) Rebuild NetBird. It has no routing peer and no slot. (c) Upgrade the web application to 1.6.0 first. It is deleted soon after.

**A21.** (a) A controller in every region from the start. On a Sovereign with two regions both would activate every grant, publish every event twice, count sessions apart and write recordings to two stores. A replay request could land in a region that does not hold the recording. (b) Leave the question to ACC-250. The gate environment has two regions, so every check would run against undefined behaviour. The mechanism is the one the chart already has (its values `crossRegion`, `platform/guacamole/chart/templates/crossregion-netpol.yaml` and the values of slot 52), pointed at the controller.

## Consequences

- The Guacamole web application, its PostgreSQL database, the seed Job and the CronJob are removed (ACC-102). Nothing is upgraded to keep them.
- `UserAccess` is unchanged. The tier it gives is the one input to who may request and who may approve.
- The kinds `AccessTarget`, `AccessGrant` and `AccessSession` arrive with `bp-guacamole` (ACC-103, ACC-104). An installation without the Blueprint has none of them.
- The invented sessions in catalyst-api are deleted (ACC-101), and the console shell opens an access session (ACC-123).
- Every credential of a session comes from OpenBao through the mount. The access plane adds no Secret object for a credential.
- Recordings stay in the object store of the region that wrote them. When the Primary moves, earlier recordings stay where they are; ACC-140 writes this into the runbook.
- Limits accepted now: access is through the browser only; a database session is recorded as screen and text, not as parsed statements; a dynamic database user lives until its lease ends, not until the session ends; review reads text, not screens.

## Shipped via

Work packages of area ACC in `docs/ledger/plan.yaml`: ACC-010, ACC-020, ACC-030, ACC-040, ACC-050, ACC-140, ACC-230, ACC-245, ACC-250, ACC-260, and the epics ACC-100 (kinds, controller and the fenced pod session), ACC-110 (recording), ACC-120 (requests, approvals and the console), ACC-130 (SSH and database kinds, isolation proof), ACC-200 (review), ACC-210 (web and graphical workbenches) and ACC-220 (Windows and RDP). ACC-240 (the platform's own nodes as SSH targets) runs only if SSH to those nodes is made a supported path.
