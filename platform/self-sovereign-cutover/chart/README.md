# bp-self-sovereign-cutover

Catalyst Self-Sovereignty Cutover Blueprint. Pivots a freshly handed-over
Sovereign from soft-tether (mothership Harbor + GitHub upstream Git source)
to hard-independent (local Sovereign Harbor + Sovereign Gitea).

## Trigger semantics — DORMANT install

This chart installs **dormant**. There are NO Helm post-install hooks. Every
step is shipped as a `ConfigMap cutover-step-NN-<name>` carrying a PodSpec
under key `podspec.yaml`. The `catalyst-api` cutover endpoint (filed
separately under epic [#790](https://github.com/openova-io/openova/issues/790))
reads each step ConfigMap, stamps a real `batch/v1.Job` from the embedded
PodSpec, and updates the `self-sovereign-cutover-status` ConfigMap as each
step completes.

Per the parent-epic scope-correction comment, the cutover trigger is
**post-handover**, not Phase-1.5:

- The `sovereign-admin` clicks **"Achieve True Sovereignty"** in the operator console, OR
- `catalyst-api` starts it by itself when the hand-over is sealed, and its
  level-triggered reconciler starts it again for as long as the hand-over is
  sealed and the cutover is not complete. Both are behind
  `CATALYST_FIRE_CUTOVER_ON_HANDOVER` (kit slot 13, `HANDOVER_AUTO`), default
  true.

The chart itself starts nothing. From 0.1.x to #7900 it also shipped an
auto-trigger hook Job; that hook waited for the hand-over inside the
installation, so it held the first phase for its whole wait on every fresh
installation, and it is gone.

A handed-over Sovereign therefore starts soft-tethered (still pulling
through `harbor.openova.io`) and becomes hard-independent when the cutover
has run.

## Step inventory

| # | Resource | Phase | Description |
|---|---|---|---|
| 01 | `cutover-step-01-gitea-mirror` ConfigMap | pre-pivot | `git clone --mirror` upstream → `git push --mirror` to local Gitea |
| 02 | `cutover-step-02-harbor-projects` ConfigMap | pre-pivot | Create 7 proxy_cache projects in local Harbor |
| 03 | `cutover-step-03-harbor-prewarm` ConfigMap | pre-pivot | HEAD-pull every image referenced by bootstrap-kit through proxy-cache |
| 04 | `registry-pivot` DaemonSet | pre-pivot | Always-on per-node reconcile loop: on `registriesYamlActive=v2` it writes a containerd `certs.d/<host>/hosts.toml` per upstream host of the offline-mirror map → the local Harbor, with no `server` key (no public fallback, #4975), pins `registry.<fqdn>` in the node's `/etc/hosts` (#5007) and flips the `bp-dragonfly` dfdaemon upstream → local Harbor. It owns only the files it marks: a file k3s wrote from `registries.yaml` is kept as `hosts.toml.pre-pivot` and put back on rollback, the dormant `v1` changes no file, and after a k3s restart it re-applies v2 (SOV-132) |
| 05 | `cutover-step-05-flux-gitrepository-patch` ConfigMap | post-pivot | Patch `flux-system/openova` GitRepository.url → local Gitea — in EVERY region (#5359: per-secondary-region leg via `/secondary-kubeconfigs`, fail-loud Ready wait) |
| 06 | `cutover-step-06-helmrepository-patches` ConfigMap | post-pivot | Patch every OCI HelmRepository → `oci://harbor.<sov-fqdn>/openova-io` — in EVERY region (#5359: per-secondary leg syncs ghcr-pull auth + the harbor-ca trust Secret, pivots + read-back-asserts zero upstream HRs remain) |
| 07 | `cutover-step-07-catalyst-api-env-patch` ConfigMap | post-pivot | `kubectl set env deploy/catalyst-api CATALYST_GITOPS_REPO_URL=...` |
| 08 | `cutover-step-08-egress-block-test` ConfigMap | post-pivot | CiliumNetworkPolicy denies egress to upstream for 10 min, asserts cluster stays Ready — the hold + green-assertion cover EVERY region (#5359) |
| 09 | `self-sovereign-cutover-status` ConfigMap | mutable state | Per-step state machine read+written by every step + the UI (#5359 adds `step.<step>.region.<regionKey>.*` per-secondary-region keys) |

### Secondary-region legs (#5359)

On a 2-region Sovereign the pre-0.1.151 chain pivoted ONLY the
control-plane region — region-B's Flux stayed on github/ghcr while
`cutoverComplete=true` (hw288 false positive; also the #5339 per-region
version drift). Steps 05/06/08 now run a leg per secondary region using
`kubectl --kubeconfig /secondary-kubeconfigs/<regionKey>.yaml`. The
kubeconfigs come from the `cutover-secondary-kubeconfigs` Secret that
catalyst-api (cutover.go `runCutover`, bp-catalyst-platform ≥1.4.1213
RBAC) materialises at every run start from the secondary kubeconfig
store it already holds on its PVC (the ClusterMesh-establish / #5244
mechanism). The volume is `optional: true`: on a single-region
Sovereign the Secret is guaranteed absent and every leg no-ops —
behavior identical to 0.1.150. See `values.yaml` `secondaryRegions.*`.
Step-04's region-B leg (secondary node containerd/Dragonfly pivot) is
the remaining increment, tracked on #5359.

The phase column controls image routing: `pre`-phase steps must use
upstream-public images because they run BEFORE step-04 pivots the node
containerd onto the local Dragonfly dfdaemon (whose upstream then flips to
the local Harbor). `post`-phase steps may pull through local Harbor when
`.Values.global.imageRegistry` is set in the overlay.

## Values

See [`values.yaml`](./values.yaml). The required overlay-supplied keys are:

| Key | Purpose |
|---|---|
| `sovereign.fqdn` | The Sovereign's base FQDN (e.g. `otech.omani.works`). Drives every public URL in this chart. |
| `sovereign.harborPublicURL` | `https://registry.<sovereign.fqdn>`. The bp-harbor HTTPRoute publishes at `registry.<sov>`, NOT `harbor.<sov>` — see `clusters/_template/bootstrap-kit/19-harbor.yaml` `gateway.host`. The step-04 Dragonfly upstream flip + step 06 repoint to this host. |
| `sovereign.giteaPublicURL` | `https://gitea.<sovereign.fqdn>`. Informational; jobs reach Gitea via the in-cluster Service URL. |

Per `docs/PRINCIPLES.md` #4 (never hardcode), the chart ships
no real defaults for the Sovereign coordinates — `helm template` smoke
renders against `example.local` placeholders, and runtime use without an
overlay is non-functional by design.

## Bootstrap-kit slot

The HelmRelease that installs this chart is at
`clusters/_template/bootstrap-kit/06a-bp-self-sovereign-cutover.yaml`.
It depends on `bp-gitea`, `bp-harbor`, and `bp-dragonfly` (the local Gitea +
Harbor + the per-node dfdaemon mesh must exist before the chart's step
ConfigMaps + step-04's Dragonfly upstream flip reference them at trigger
time) and uses `disableWait: true` because the chart is dormant (no Pods to
wait on except the registry-pivot DaemonSet, which is event-driven on
the status ConfigMap).

## Post-cutover Day-2 Harbor reconciler (`daytwoReconciler`, #5265)

`step-03` (`harbor-prewarm`) pushes every pinned chart into the local Harbor
**exactly once** at cutover. Post-cutover, the local Gitea mirror re-syncs
upstream pin bumps on the operator's schedule and Flux moves each HelmRelease to
the new pin — but the local Harbor still holds only the cutover-time versions,
so the `HelmChart` cannot reconcile and the upgrade wedges (workloads keep
running on the installed version; it is a strand, not an outage). The Git leg
has a companion (`step-01`'s re-runnable mirror); this template
(`templates/12-daytwo-harbor-pin-reconciler.yaml`) is the **Harbor** companion.

It is a **Deployment**, not a CronJob — a CronJob's scheduler dies with the
control-plane region, so the loop must be a rescheduled workload to survive a
region kill. It enumerates the frozen bootstrap-kit chart pins via the **same**
`03b` `bootstrapkit_pins` extractor `step-03`/`step-06` use, diffs them against
the local Harbor (the `#5030` durable artifact-API probe), and closes the drift.

**Sovereignty-safe: ships disabled by default** (`daytwoReconciler.enabled:
false` — the same severance-safe default as `mirrorResync`, so a canonical
fresh-prov Sovereign renders nothing here and the `step-08` deny-egress proof is
untouched). When an operator opts into a managed-update posture:

- `mode: detect` (default when enabled) reaches **only** the local Gitea mirror +
  local Harbor (zero external egress) and writes the exact set of missing pinned
  chart artifacts to ConfigMap `self-sovereign-cutover-daytwo-missing` + logs —
  the air-gapped Sovereign's offline-media side-load manifest, and the signal
  that turns the previously-silent strand visible.
- `mode: warm` additionally `helm pull`s each missing chart from
  `daytwoReconciler.warm.upstreamOciPrefix` (with an operator-supplied
  `warm.credentialSecret` — the cutover strips its own ghcr auth by design) and
  `helm push`es it into local Harbor, then re-checks. Fail-soft: a warm miss is
  logged and left in the manifest; the loop never crash-loops.

## Release mechanism (#7938)

A release delivery (`PUT /api/v1/sovereign/release` on a cut-over Sovereign)
runs the mechanism of the release it delivers, not the one installed. The
installed catalyst-api only bootstraps it; the rest runs from the candidate's own
copy of this chart.

**Bootstrap** (the installed API's `release-run.sh`, `stage` then `deliver`;
kept small and stable):

1. Stage every region (fetch `rc/<id>`, merge it into each region's `main`, apply
   the region's pivot, push `release/<id>`; a dry run pushes nothing). The
   candidate decides which files exist and what they hold: a conflicted file
   whose region side is exactly the region's registry rewrite of the merge base
   (step 06's `rewrite_kit_tree`, with or without step 07's `imageRegistry`
   seam) is deleted when the candidate deletes it, else taken from the candidate
   with the rewrite applied again. Any other conflict stops the stage, names the
   file and moves nothing (`tests/step7938-stage-takes-candidate-deletions.sh`).
2. Export this chart's folder (`platform/self-sovereign-cutover/chart`) and its
   kit slot (`clusters/_template/bootstrap-kit/06a-bp-self-sovereign-cutover.yaml`)
   from the staged commit of the primary region.
3. Verify it is the chart the candidate pins: the folder is the candidate's
   (no commit of the Sovereign changes it), and `Chart.yaml` name and version
   equal the slot's `spec.chart.spec.chart` and `version`.
4. Render it with `helm template`, with the `spec.values` of the installed
   HelmRelease `flux-system/bp-self-sovereign-cutover`, its release name and
   target namespace.
5. A dry run: run the chart's own `files/release/release-run.sh verify`, record
   its verdict (`serverDryRun`) and stop. A delivery: run the chart's own
   `release-run.sh compose`, and record its outcome.

The state ConfigMap `sovereign-release` names what ran (`mechanism`, and
`deliveryJob`).

**One region per request, and its rollback** (SOV-521). The body of
`PUT /api/v1/sovereign/release` takes two optional fields:

- `region` names one region as the state names it: `primary`, or a follower's
  name (the key `<name>.yaml` of `cutover-secondary-kubeconfigs`); any other
  name is answered `400 bad-region`. The stage and the prewarm run in every
  region as above, so a conflict anywhere moves nothing; `promote` checks and
  pushes that region only, and every other region's `main` stays the commit it
  was. Without `region` every region is promoted, the followers first, as
  before. The stage refuses a region request, nothing moved, when the
  candidate's `release-run.sh` has no phase `rollback` (read as `verify` is
  read): that mechanism promotes every region. The installed route of an
  earlier release answers `400` to a body with a field it does not know, so
  the first delivery that carries this to a Sovereign is a whole one.
- `rollback: true` with a `region` puts that region back to the `main` recorded
  at the stage of the request that promoted it. It runs no stage and no
  prewarm, and refuses `dryRun` and `credential`. The route refuses it
  (`409 not-promoted`) unless the state records that promote for this
  candidate; the bootstrap's one container `release-run.sh rollback` then runs
  `release-region.sh rollback` in that region, which refuses (phase `moved`,
  nothing pushed) unless the region's `main` is still the promoted commit, and
  pushes one commit whose tree is the recorded `main`'s and whose parent is the
  promoted commit: a plain push, a fast-forward the server checks, never a
  force push. Flux applies the earlier tree; the promoted commit stays in the
  history. A second rollback of the same promote pushes nothing. The candidate
  then counts as merged in that region, so delivering it there again takes a
  later candidate.

Each promote records, per region, `<region>.promotedFrom` (the `main` of the
stage), `<region>.promotedTo` (the commit pushed) and `<region>.phase`
(`promoted`, `rolled-back`); a promote that pushes nothing, because the
region's `main` already held the candidate, leaves them as they are. The route
keeps these keys across the requests of one candidate and drops them for another; `GET` answers them in `status` and as
`promoted.<region>`. `tests/step7938-promote-one-region.sh` runs this chart's
two scripts on Git repositories on disk: one region promoted, its rollback, a
rollback refused after the region moved.

**The upstream credential.** After cutover the Sovereign holds no credential
for `ghcr.io` (step 06 strips it), and nothing is made on it by hand. A delivery
request carries the credential itself, from the signed-in sovereign-admin over
the console's TLS: `"credential": {"registry": "ghcr.io", "username": "<user>",
"password": "<password or token>"}`. The route signs in to the registry with it
(the distribution API's sign-in: `/v2/`, then the token realm, which must be
https) and, before anything is written, answers `422
registry-credential-missing`, `registry-credential-registry` or
`registry-credential-refused`, or `502 registry-unreachable`. The sign-in
proves the credential, not its read access to the packages the release pins:
the state says `signed in to ghcr.io`, and a credential that may not read a
pinned package fails the prewarm, which names it. Then the route makes the pull
Secret `sovereign-release-pull` in the cutover namespace itself (field manager
`catalyst-release`, labelled with and owned by the bootstrap Job) and passes its
name as `PULL_SECRET`. The credential is in that Secret and its copies only:
never in a log line, the state, a ConfigMap, an event, a Job, the region's Git
or an answer. A dry run and a rollback refuse a credential
(`dry-run-credential`, `rollback-credential`); the former field `pullSecret`, a
Secret made by hand, is refused (`400 pull-secret-removed`).

The Secret goes in every outcome. The release scripts delete it in every region
(the copies in each follower and the source) at every end of a delivery: the
end of the prewarm, the start of promote, a stage that fails or refuses, and the
bootstrap's deliver container whether the delivery completed or failed; that
last one reads the names of step 03's follower copy from the delivery Job it
composed, so a delivery Job whose Pod was killed leaves none. A bootstrap Job
whose own Pod was killed runs no end and fails (`backoffLimit: 0`, as every Job
of a delivery): the Secret is owned by it, so the garbage collector deletes it
with the Job, which its `ttlSecondsAfterFinished` deletes three days after it
ended; the next request deletes it before that. That covers the primary's
Secret only: copies such a killed stage made in the followers stay until the
next delivery's end deletes the credential in every region. `GET` changes
nothing.

**A killed Pod.** No Job of a delivery replaces a Pod, so a Pod killed by a
drained or lost node fails the delivery. Its containers record nothing then;
the bootstrap's deliver records it: phase `failed`, the step, and per region
what moved. "No region was promoted" is said only when the state is still at
`staged`, which is certain because promote records `promoting` before it
checks or pushes any region, or stops. A delivery killed during its promote
names each region in the order promote pushes them (the followers, then the
primary): promoted, unchanged, those promote reached with no answer recorded
(may have moved: a write of an answer can fail and promote goes on; their
`main` is not read), and those it never reached. That last is certain because
promote records each region it reaches (`promoteRegion`) before it pushes it,
or stops.

**What a chart version must carry for the bootstrap** (contract `1`):

- `files/release/contract` holds `1`, and `files/release/release-run.sh` and
  `release-region.sh` are this version's release scripts (byte-identical to
  `products/catalyst/bootstrap/api/internal/lifecycle/release/`, tested);
  `release-run.sh` has the phases `verify`, `compose`, `prewarm` and `promote`
  (a chart without `verify` is delivered, and its dry run says its objects were
  not sent to the API server), and `rollback` for a request that names a region
  (a chart without it is delivered whole only). They are read from the chart folder; nothing
  renders or installs them.
- The render holds ConfigMaps `cutover-step-01-gitea-mirror` and `cutover-step-03-harbor-prewarm`
  with a `podSpec`, `cutover-step-06-helmrepository-patches` with
  `kit-rewrite.sh`, `cutover-bootstrap-kit-pins` with `extract-pins.sh`, and, on a
  Sovereign with follower regions, `cutover-secondary-pivot-script` with
  `secondary-pivot-job.yaml`.

`compose` builds the delivery Job from those: an init container `prewarm` (step
03's container, its command run by `release-run.sh prewarm`) and a container
`promote` (step 01's image and settings). Every ConfigMap it mounts is the
render's, applied under `<name>-release`, so no installed object changes and the
installed step 03 never runs. The Job itself is created, not applied: its name is
new for every request, and the runner may create Jobs but not patch them (a
server-side apply is a PATCH).

Every YAML the release turns into an object (the render, each step's `podSpec`,
the follower Job manifests) is read by `k8s_json`: Helm's `fromYaml`, which is
`sigs.k8s.io/yaml`, the reader that installs the chart. It reads YAML 1.1 as
Kubernetes does (`0755` is octal 493, `yes` is true); yq reads YAML 1.2, where
`0755` is the decimal 755, and the sixth delivery to a Sovereign was refused for
it (`volumes[8].configMap.defaultMode: Invalid value: 755`). What the release
writes is JSON. So a template may write a mode as `0755`: the delivery reads it
as Kubernetes does.

`verify` (the dry run) and `compose` (before it creates anything) compose every
object the delivery creates, in every region: in the primary, the ConfigMaps the
Job mounts, its scripts, its record and the Job; in each follower, the stage's
copy of the pull credential, what step 03's #6764 leg creates there (its copy of
the credential, its ConfigMap and its Job) and promote's scripts ConfigMap and
check and promote Jobs. Each is sent as a server-side dry run
(`--dry-run=server`) by the account that will create it: this Pod's in the
primary region (the bootstrap's, which the deliver container creates the
delivery's objects with) and the peer kubeconfig in each follower; a Job the
step deletes by name before it recreates it is dry-run as a create under a
generated name, and what step 03 sends by a client-side apply is dry-run as a
server-side apply of the same object. Every refusal is named in `serverDryRun` and in
the message, not the first; a delivery creates nothing unless all are accepted.
`TestReleaseDeliveryObjectsOfTheCutoverChartAreValidAndAreItsRender` builds
these objects from this chart with this code, validates each offline (the
Kubernetes schema client-go carries, a strict decode, and the pod rules the API
server refused) and checks the delivery Job's pod against step 03's as
Kubernetes reads the render; it fails on the reader of the sixth delivery.

Every kubectl call of `release-run.sh`, on every
path of a delivery, is checked against this chart's rendered RBAC by
`TestReleaseScriptCallsAreGrantedToTheirAccount` in
`products/catalyst/bootstrap/api/internal/lifecycle/release_rbac_test.go`. The
candidate's scripts run under the RBAC the Sovereign has installed, so a call
that needs a new grant can only be used by the delivery after the one that
installs it. A candidate whose chart carries no contract-1
mechanism is refused by name; the bootstrap never falls back to the installed
mechanism. `files/release/` is left out of the package (`.helmignore`): Helm never
renders it, and the release reads it from the staged tree.

## RBAC discipline

The runner ServiceAccount + ClusterRole + ClusterRoleBinding live at
`templates/serviceaccount.yaml` + `templates/rbac.yaml`. Per
`feedback_rbac_create_no_resourcenames.md` (auto-memory anchor), `create`
verbs are split into a separate Rule **without** `resourceNames` —
combining them produces a 403 every POST. Update verbs (`patch`,
`update`, `delete`) live in their own Rule alongside `resourceNames`
where appropriate.

## Smoke test

```bash
helm dependency build platform/self-sovereign-cutover/chart   # no-op (no upstream subchart)
helm template smoke platform/self-sovereign-cutover/chart \
  --set sovereign.fqdn=otechN.omani.works \
  --set sovereign.harborPublicURL=https://registry.otechN.omani.works \
  --set sovereign.giteaPublicURL=https://gitea.otechN.omani.works
```

CI (`blueprint-release.yaml`) runs the same smoke render with default
values and uploads the rendered output as a workflow artifact.

## References

- Issue: [#791](https://github.com/openova-io/openova/issues/791) — chart implementation
- Parent epic: [#790](https://github.com/openova-io/openova/issues/790) — Sovereign sovereignty cutover
- Inviolable Principles: `docs/PRINCIPLES.md` (#3 IaC-first, #4 never hardcode, §10 credential hygiene)
- RBAC anchor: auto-memory `feedback_rbac_create_no_resourcenames.md`
