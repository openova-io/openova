# self-sovereign-cutover chart: design notes of the templates

Everything the chart packages is stored in every Helm release Secret, whose data
Kubernetes caps at 1048576 bytes (scripts/check-helm-release-payload-size.py,
#6004), and the lines of `templates/` may only shrink
(scripts/check-lifecycle-ratchets.sh). A long header comment of a template is paid
for in both. The notes below were those header comments, moved here word for word;
each template keeps one comment line that names its section. `.helmignore` keeps
this file out of the package, as it does README.md and tests/.

## templates/10-vcluster-registry-pivot-job.yaml

```text
Step 10 — vcluster-registry-pivot (TBD-V24 MISS-1, issue #2034).

Pivots the THREE bp-*-vcluster HelmReleases' `image.repository` values
from the chart-default `harbor.openova.io/proxy-ghcr/loft-sh/vcluster`
to the Sovereign-local `harbor.<SOVEREIGN_FQDN>/proxy-ghcr/loft-sh/
vcluster`. Without this step the MGMT/RTZ/DMZ vCluster control-plane
Pods keep pulling from mothership Harbor indefinitely — blocking
Pillar 5 (Sovereign independence). The chart's own comment at
`platform/bp-mgmt-vcluster/chart/values.yaml:77-79` already promises
"post-handover, the per-Sovereign overlay rewrites to
`harbor.<sovereign-fqdn>/proxy-ghcr/...`" but until this step shipped
no such rewrite existed anywhere in the cutover sequence.

Why step 04 (containerd registries.yaml pivot) does NOT catch it
─────────────────────────────────────────────────────────────────
`registries.yaml.v2` (written by Step 04's DaemonSet) only registers
mirrors for the 7 canonical UPSTREAMS (ghcr.io, docker.io,
registry.k8s.io, gcr.io, quay.io, xpkg.upbound.io, public.ecr.aws). The
host `harbor.openova.io` is treated as a literal endpoint, NOT an
upstream — Step 04 does NOT add a mirror for it. So a Pod whose
imageRef starts with `harbor.openova.io/...` ALWAYS hits the literal
mothership Harbor regardless of containerd mirror config.

Why not just `helm template` the bp-*-vcluster charts with an overlay
─────────────────────────────────────────────────────────────────────
The bp-*-vcluster charts default to mothership-friendly values so
non-Sovereign installs (the openova-io mothership itself, dev clusters)
work without threading a Sovereign FQDN through values. Coupling the
chart to a per-Sovereign FQDN at template time would pollute every
non-Sovereign deployment. The cutover-step pattern keeps chart
defaults untouched and only rewrites POST-handover, when the
per-Sovereign FQDN is well-defined.

Patches applied (TWO knobs per HelmRelease)
───────────────────────────────────────────
The bp-mgmt-vcluster / bp-rtz-vcluster / bp-dmz-vcluster charts expose
the vCluster image at TWO levels:

  1. `<role>Vcluster.image.repository` — the umbrella chart's image
     reference (see `platform/bp-mgmt-vcluster/chart/values.yaml:81`,
     bp-rtz-vcluster/...values.yaml:33, bp-dmz-vcluster/...:43)
  2. `vcluster.controlPlane.statefulSet.image.{registry,repository}`
     — the loft-sh upstream subchart's image reference (see
     platform/bp-mgmt-vcluster/chart/values.yaml:129-130 et al.)

This Job patches BOTH on each of the 3 HelmReleases. Idempotent: a
re-run reads the current value and skips when already pivoted.

bp-sso-bridge image host pivot (#2951 / #2940 ATTACK#9)
──────────────────────────────────────────────────────
UAT Wave-2 ATTACK#9 (#2940) found that a Sovereign which completes
cutover STILL pulls the bp-sso-bridge reconciler Pod image from
mothership Harbor: `platform/sso-bridge/chart/values.yaml` defaults
`image.repository: harbor.openova.io/proxy-dockerhub/alpine/k8s` and the
`13b-bp-sso-bridge.yaml` bootstrap-kit overlay carries NO
`spec.values.image.repository` override, so the chart default wins on
every reconcile. That is the same class of residual `harbor.openova.io`
tether this step already severs for the three vClusters, so #2951 folds
the sso-bridge image-host pivot into THIS step rather than bolting a
values-injection onto Step 06 (whose script only rewrites HelmRepository
OCI URLs, not arbitrary chart values). Phase 1b live-patches
`bp-sso-bridge` HR `spec.values.image.repository` host from
`harbor.openova.io` → `harbor.<SOVEREIGN_FQDN>` (preserving the
`/proxy-dockerhub/alpine/k8s` path), and Phase 2 injects the same
override into `13b-bp-sso-bridge.yaml` so the bootstrap-kit reconcile
doesn't revert it. Idempotent: skip-if-already-pivoted on the live patch,
sentinel-comment guard on the Gitea injection.

NOTE on the other #2951 candidates: the powerdns overlay
(`11-powerdns.yaml`) already sets `api.host: pdns.${SOVEREIGN_FQDN}`
(cloud-init envsubst), and the cert-manager-powerdns-webhook
`powerdns.host` deliberately targets contabo's central out-of-cluster
PowerDNS (`pdns.openova.io`) for ACME DNS-01 — an intentional external
dependency, NOT a mothership tether to sever (rewriting it would break
wildcard-cert issuance). Both are therefore intentionally left untouched;
the acceptance grep on #2951 excludes intentional bootstrap/external
paths.

Phase inventory
───────────────
  Phase 1  Live K8s patch — `kubectl patch helmrelease` against each
           of {bp-mgmt-vcluster, bp-rtz-vcluster, bp-dmz-vcluster} in
           flux-system, setting both image knobs to the Sovereign-local
           Harbor host. Triggers an immediate Flux reconcile annotation
           so helm-controller re-renders within seconds rather than
           waiting for the 15m HR interval.
  Phase 2  Push YAML edit to local Gitea (same pattern as Step 06
           Phase 2): clone the bootstrap-kit repo, inject `image:`
           overrides into clusters/_template/bootstrap-kit/{54,58,59}-
           bp-*-vcluster.yaml under spec.values, commit + push. Without
           this phase the bootstrap-kit Kustomization reconcile (every
           ~1 min from local Gitea) would silently revert the Phase 1
           live patch back to the chart default within minutes.

Pod restart-safety / image-pull semantics
──────────────────────────────────────────
The actively-running vCluster Pods do NOT churn on a `helm upgrade` of
the wrapper chart unless the StatefulSet PodSpec changes. Changing
ONLY `image.repository` to a different host of the SAME image tag
DOES trigger a rolling update because the StatefulSet's
`spec.template.spec.containers[].image` field changes. Local Harbor's
proxy-ghcr project (created by Step 02) will serve the same upstream
image bytes via Harbor's proxy-cache semantics, so the new image-pull
either hits Harbor's existing cache (Step 03 prewarm covered this) or
falls through to ghcr.io once and then caches locally.

Order rationale: 10 (after Step 09)
────────────────────────────────────
Placed at order 10 so it runs LAST in the cutover sequence. This
follows the precedent set by Step 09 (gitea-token-mint, chart 0.1.30):
"avoid renumbering 01..09 which would invalidate operator history".
Functionally the step is order-independent for everything except:
  - MUST run AFTER Step 02 (harbor-projects) so the local Harbor
    proxy-ghcr project exists for the new image pulls.
  - MUST run AFTER Step 06 (helmrepository-patches Phase-0) so the
    ghcr-pull Secret carries auth for harbor.<sov-fqdn> — that auth is
    what the vCluster Pods need to pull from the local Harbor mirror.

Image phase: post.
```

## templates/11-mirror-resync-cronjob.yaml

```text
Post-cutover continuous mirror re-sync (TBD-A37, issue #1899).

Why this exists
───────────────
Step 01 (gitea-mirror) runs EXACTLY ONCE at cutover time. It does a
bare-clone of the upstream openova-io/openova GitHub repo and a single
force-push to local Gitea. The local repo is then a STANDALONE,
non-mirror repo (PR #1029 — pull-mirror semantics block Step-06's
HelmRepository URL rewrite push, so the cutover migrated off Gitea's
built-in pull-mirror feature).

But standalone means: after cutover, the local repo never re-fetches
upstream. Any chart bump or controller-image bump that lands on
upstream main AFTER cutover never reaches the Sovereign. Live failure
on t31 2026-05-19 (A145 verifier): sandbox-controller still at image
:8017700 from 2026-05-16 even though PR #1862 had merged 2 days
earlier — the upstream values.yaml bump never crossed the mirror seam.

This CronJob closes that loop by re-running the same idempotent bare-
clone + force-push every 5 minutes. The script is INTENTIONALLY
identical to 01-gitea-mirror-job.yaml in semantics (defensive HTTP
probe → API auth → git clone --bare → git push --mirror --force) so
operators can reason about both as one mechanism.

Design notes
────────────
- Interval defaults to 5 minutes ("*\/5 * * * *"). Short enough that
  chart-bump → upstream-merge → Sovereign-reconcile stays under ~10
  minutes end-to-end (5 min for this CronJob to fire + ~5 min for the
  bp-* HelmRepository's `interval: 15m` Flux refresh — see notes in
  10-gitea.yaml). Long enough that GitHub anonymous-clone rate limits
  aren't a concern even with hundreds of Sovereigns (300 req/hr per
  IP for anonymous clone; one Sovereign clones once per 5 min = 12
  clones/hr).

- IDEMPOTENT-FIRST. The CronJob is created at chart-install time
  (before Step-01 runs). On every fire pre-cutover, the script
  detects the local repo is missing or empty and exits 0 (no-op).
  This avoids fighting with the in-flight Step-01.

- Force-push is SAFE in this architecture: the local Gitea
  openova/openova repo is the SAME content as upstream openova-io/
  openova by construction (Step 06 patches HelmRepository URLs but
  not the repo contents). Any local divergence is a bug we WANT to
  overwrite.

- Suspend knob (`.Values.mirrorResync.suspended`) ships at default
  `false` for canonical fresh-prov behaviour; an operator running a
  sandboxed soak Sovereign that intentionally stays N days behind
  upstream can suspend this CronJob via per-Sovereign overlay.

- G75 #2622 (2026-05-31): MASTER SEVERANCE GATE
  (`.Values.mirrorResync.enabled`) ships at default `true`. Cutover
  Step 01 flips it to `false` via HR patch on bp-self-sovereign-
  cutover, then DELETEs the live CronJob. The combination of (a)
  IaC flag false + (b) live delete means Flux won't recreate the
  resource on the next reconcile. This is the architectural
  severance of the GIT-source upstream tether per ADR-0002
  §Pillar 5 (one of the 8 tethers cutover is contractually
  required to sever). Previously cutover Step 06 (HelmRepository
  URL pivot) + Step 07 (Phase 2.5 sed) pushed changes that
  SURVIVED ~5min and then got CLOBBERED by this CronJob's next
  `git push --mirror --force` — verified live hw86
  afc8800bc03751c6 2026-05-31. Suspend is NOT equivalent
  (Principle 14): a suspended cronjob is a future drift trap;
  severance must be irreversible at the IaC layer.

- Image: same `images.git` as Step-01 (alpine/git). MUST use the
  cutoverPhase: "pre" image because pre-cutover the local Harbor
  proxy-cache isn't routable yet and the CronJob is created
  alongside Step-01.

- TTL on completed Pods: 1 hour. Operators get a small audit window
  to read logs but completed Pods don't pile up.

- SA: re-uses the cutover runner SA (`bp-self-sovereign-cutover-
  runner`). Doesn't require any new RBAC — the CronJob only talks to
  GitHub (anonymous) + local Gitea HTTP (BasicAuth from mounted
  Secret). It needs ZERO Kubernetes API verbs at runtime.

Acceptance contract (issue #1899)
─────────────────────────────────
On a fresh prov post-cutover:
  - kubectl get cronjob -n catalyst gitea-mirror-resync → Active
  - Wait 5 min, kubectl get jobs -n catalyst -l app.kubernetes.io/
    component=mirror-resync → at least one Job with Succeeded=True
  - kubectl logs <pod> → "pushed all refs successfully"
  - Re-push a values.yaml bump to upstream openova-io/openova:main,
    wait 10 min, observe sandbox-controller Deployment image SHA
    matches the new upstream tag.

Image phase: pre — uses upstream-public image like Step-01.
```

## templates/05-flux-gitrepository-patch-job.yaml

```text
Step 05 — flux-gitrepository-patch.

Patches `flux-system/<gitopsRepository.name>` GitRepository.spec.url
to the local Sovereign Gitea URL AND wires a basic-auth secretRef so
source-controller can authenticate to the local Gitea. After this patch
source-controller reconciles against the in-cluster Gitea Service.

#3536 (Refs #3379) — secretRef wiring (load-bearing for step 08)
────────────────────────────────────────────────────────────────
The cloud-init bootstrap GitRepository pulls from public github.com with
NO secretRef (anonymous clone). bp-gitea sets REQUIRE_SIGNIN_VIEW=true,
so once we flip the URL to the local Gitea, an anonymous clone 401s
("authentication required") even for a PUBLIC repo. source-controller
then freezes on the stale pre-cutover artifact, step 06's HelmRepository
URL rewrites never reconcile, and step 08's bounded pivot-wait can never
converge → the 600s deny-egress hold never runs → cutoverComplete=false.

Fix: BEFORE flipping the URL, materialise a Flux basic-auth Secret in the
GitRepository's namespace carrying the `catalyst-gitea-token` PAT (minted
at BOOTSTRAP by bp-catalyst-platform, in catalyst-system) in
username/password shape, and set GitRepository.spec.secretRef.name to it.
A Gitea PAT is accepted as the HTTP git password. This is the same
credential + pattern issue #3454 used for the openova-org-tenants
GitRepository. Flux secretRef takes only a name (no namespace) and
resolves it in the GitRepository's own namespace, so the Secret is
created locally from the cross-namespace PAT here.

#5359 (Refs #5265 #5339) — SECONDARY-REGION leg. On a 2-region Sovereign
region-B runs its OWN flux whose bootstrap GitRepository still points at
public github.com after the region-A patch — live-proven on hw288
(cc=true, region-B GitRepository -> github.com). After the region-A
patch this Job iterates every kubeconfig in /secondary-kubeconfigs
(the `cutover-secondary-kubeconfigs` Secret catalyst-api materialises
at run start from its on-PVC secondary kubeconfig store; mounted
optional:true so a single-region Sovereign renders the leg inert) and:

  1. mirrors the basic-auth Secret into the secondary's flux-system;
  2. applies the SAME branch/secretRef/ignore patch to the secondary's
     GitRepository with the URL from the candidate chain — DEFAULT is
     the Sovereign's OWN external Gitea door https://gitea.<fqdn>/...
     (the only target live-proven reachable from a secondary; hw292);
     secondaryRegions.giteaURL pins one explicit URL instead;
  3. FAIL-LOUD waits for the secondary GitRepository to CONVERGE —
     observedGeneration == generation with Ready=True (secondaryRegions.
     gitRepositoryReadySeconds per candidate); succeeding without
     region-B reconciling the pivoted URL would re-mint the #5359
     false positive;
  4. records `step.flux-gitrepository-patch.region.<key>.result` on
     the status ConfigMap (the per-region tracking #5359 asks for).

The 0.1.151..0.1.165 ClusterMesh global-Service leg (annotate region-A's
gitea-http + a same-named alias Service in the secondary) is DELETED,
not parked: both regions' gitea-http Services are HEADLESS (clusterIP:
None), which Cilium global-services cannot span — CoreDNS answers a
headless name straight from the LOCAL EndpointSlice — and the secondary
runs its OWN (empty, never-mirrored) bp-gitea behind that same name, so
from a secondary the in-cluster URL can never converge; even a
non-headless global pair would MERGE the empty local backend into the
pool. hw292 measured the consequence: region-b resolved the name to its
own zero-ref Gitea (10.43.3.228), source-controller failed every fetch
with "pkt-line 3: EOF", and — because source-controller v1.4.1 advances
status.observedGeneration only on a SUCCESSFUL reconcile
(ComputeReconcileResult adds WithStatusObservedGeneration on the nil /
Stalling / Generic-Ignore branches only) — the object sat at
generation=2 / observedGeneration=1 for 22h, serving the pre-pivot
github.com artifact behind a result=success stamp.

Image phase: post.
```

## templates/09-gitea-token-mint-job.yaml

```text
Step 09 — gitea-token-mint.

Why this step exists (TBD-C18, journey step 16 unblock)
───────────────────────────────────────────────────────
The catalyst-platform chart's
templates/org-services/provisioning-github-token.yaml mirrors the Gitea
admin Secret's `password` byte into Secret `org-services/provisioning-github-token`
under key `GITHUB_TOKEN`. The Organization provisioning service then sends

    Authorization: token <X>

against the local Gitea API. Gitea resolves the Bearer/token credential
as an API access token (sha1 lookup against `users` table) — the admin
PASSWORD is NOT an access token, so the lookup fails and Gitea returns

    HTTP/1.1 401 Unauthorized
    {"message":"user does not exist [uid: 0, name: ]"}

End result on t22: voucher checkout returns 200, customer is redirected
to /jobs, the provisioning service tries to materialise the tenant repo
in Gitea, every API call 401s, NO Organization CR is ever created and
the customer's screen sits indefinitely.

Verified on t22 (2026-05-18):
  - Secret org-services/provisioning-github-token.GITHUB_TOKEN == gitea/gitea-admin-secret.password
  - curl -H "Authorization: token <pwd>" → 401 user does not exist
  - curl -u gitea_admin:<pwd> → 200 OK (BasicAuth works, token does not)

Fix (Approach A — mint admin API token at cutover)
──────────────────────────────────────────────────
This step runs at cutover (the same lifecycle moment that runs step 01
gitea-mirror) and:

  1. Uses Gitea admin BasicAuth to POST /api/v1/users/{user}/tokens with
     scope `all`, creating a new API token bound to the gitea_admin user.
  2. Reads the returned `.sha1` (Gitea returns the raw token bytes
     exactly once on creation — there is no way to retrieve them later).
  3. Patches Secret `org-services/provisioning-github-token`'s `.data.GITHUB_TOKEN`
     to the new token (base64-encoded), overwriting the admin password
     placeholder the chart's `provisioning-github-token.yaml` template
     wrote.
  4. Validates by calling GET /api/v1/user with `Authorization: token <new>`
     and asserting HTTP 200 + non-empty `login` field.

Idempotency: Gitea rejects POST /tokens with a name that already exists
(HTTP 422 "name has already been used"). On re-run we delete the
catalyst-platform-bootstrap token first (DELETE /api/v1/users/{user}/tokens/{name})
and then re-mint. This keeps the Secret in sync with whatever Gitea
holds even if the operator manually rotated state between runs.

Image phase: pre — runs alongside step 01 against in-cluster Gitea via
the cluster-local Service URL (no public DNS dependency). Image is
alpine/k8s because we need both kubectl (to PATCH the Secret) AND
wget/sh (to call Gitea's REST API).

Order rationale: 9 (after step 08 egress-block-test) — the token mint
does NOT need to land before any of the existing cutover steps (none of
steps 02-08 read the provisioning-github-token Secret; they touch
Harbor, Flux, kubectl, CiliumNetworkPolicy). The Organization provisioning
service first consumes the token at TENANT CHECKOUT TIME, which always
postdates cutover by minutes-to-hours. Sliding into slot 9 (vs 1b) lets
us ship without renumbering the existing 01..08 chain (which would
invalidate every operator's historical cutover-status ConfigMap audit
trail).

Per docs/INVIOLABLE-PRINCIPLES.md #4 (never hardcode): every Gitea
coordinate flows through .Values.gitea.*; per #10 (no plaintext
credentials in this template): the admin password is read from the
existing gitea-admin-secret at Pod runtime, never embedded.
```

## templates/06-helmrepository-patches-job.yaml

```text
Step 06 — helmrepository-patches.

Three phases run in this Job:

  Phase 0  Merge harbor.<sov-fqdn> auth into the flux-system/ghcr-pull
           dockerconfigjson Secret so source-controller can pull from the
           per-Sovereign Harbor mirror once the URLs are pivoted in
           Phase 1. Without this, every HelmRepository pivoted to
           oci://harbor.<sov-fqdn>/openova-io 401s on the very first
           reconcile because ghcr-pull only carries auth for
           ghcr.io / harbor.openova.io. Caught manually on omantel
           2026-05-10 (session 5c468708) — bp-guacamole / bp-netbird /
           bp-dmz-vcluster all stuck Reconciling until a manual
           `kubectl patch secret ghcr-pull` was run. Idempotent: if
           harbor.<sov-fqdn> already maps to an auth entry the script
           no-ops. (#1184)
  Phase 1  Live K8s patches: for each HelmRepository in
           .Values.helmRepositories.names, patches spec.url from
           oci://ghcr.io/openova-io/<name> to
           oci://harbor.<sov-fqdn>/openova-io/<name>.
  Phase 2  Push the YAML edit to local Gitea so the bootstrap-kit
           Kustomization reconcile doesn't revert Phase 1 within ~1m.

#5359 (Refs #5265 #5339) — SECONDARY-REGION leg. On a 2-region Sovereign
region-B runs its OWN flux with its OWN HelmRepository set, all still on
oci://ghcr.io/openova-io after the region-A pivot (live-proven on hw288:
64/64 region-B HRs on ghcr despite cc=true — the direct cause of the
#5339 per-region chart-version drift). Between Phase 2 and Phase 3 this
Job now iterates every kubeconfig in /secondary-kubeconfigs (the
`cutover-secondary-kubeconfigs` Secret catalyst-api materialises at run
start; optional:true so single-region renders the leg inert) and, per
secondary region: syncs region-A's merged ghcr-pull auth bytes + the
cutover-harbor-ca trust Secret into its flux-system, pivots EVERY
ghcr-prefixed HelmRepository to local Harbor (same prefix-swap +
certSecretRef shape as Phase 1), read-back-asserts ZERO upstream-
prefixed HRs remain (fail-loud), and records `step.helmrepository-
patches.region.<key>.*` on the status ConfigMap. After Phase-3b's
readiness-gated strip lands on region-A, the (now-stripped) auth bytes
are re-synced to every secondary so region-B sheds the mothership creds
under the SAME proven-pullable gate — never before it (the hw139
strip-before-proof lesson applies per-region). Durability is PER REGION
(#5596, 0.1.202): since #6490 each secondary's Flux consumes its OWN
in-cluster Gitea (mirrored from upstream by step-01), which the Phase-2
push never reaches — hw305 measured 63/65 HRs reverting on every attempt
for 10 days. So before the live patch loop the leg stamps an in-region
Job that applies the same kit-rewrite.sh to that region's own clone and
pushes it (secondary-pivot-script-configmap.yaml), converges the region's
GitRepository onto that sha, and only then runs the #5596 durable assert.
Region-B chart PULLS go through the Sovereign's own public registry
door (registry.<fqdn> via the region-A gateway — the same EIP the
ClusterMesh control-plane already proves reachable cross-region).

SCOPE — what this step does NOT touch (#2951 decision record):
  This step rewrites HelmRepository spec.url (the chart SOURCE: ghcr.io
  → local Harbor) ONLY. It deliberately does NOT rewrite per-Blueprint
  chart-VALUES image tethers (e.g. bp-{rtz,mgmt,dmz}-vcluster values.yaml
  `repository: harbor.openova.io/proxy-ghcr/loft-sh/vcluster`). Those are
  left verbatim and handled by:
    - Step 03 (harbor-prewarm): skopeo-pushes every running Pod's image,
      incl. the vcluster image, into the local Harbor BEFORE egress is cut.
    - Step 04 (registry-pivot): containerd registries.yaml v2 redirects
      `harbor.openova.io` → local Harbor at image-pull time.
  Adding a sed of those chart-values here would be a NO-OP that breaks the
  intended cache pattern (the PR #3067 shape — it rewrites the Harbor
  proxy-cache, not a real mothership tether). End-to-end correctness is
  verified by Step 08's real egressDeny (#3072 / #2940), not a values sed.
  See #2951.

Image phase: post.
```

## templates/03c-catalog-chart-set.yaml

```text
#5443 (Refs #3627 #5237 #5442) — the CATALOG CHART-SET extractor.

WHY THIS EXISTS. Step-06 pivots the marketplace's chart resolver — the
`openova-catalog` HelmRepository every per-Org Application HelmRelease
resolves through — from oci://ghcr.io/openova-io to the local Harbor. Step-03
harbor-prewarm stocks that Harbor from TWO enumerations, and BOTH derive from
what is INSTALLED:

  • Phase A2      — chart:version off every live HelmRelease in the pivot set.
  • Phase A2-pins — the frozen bootstrap-kit pins in the local Gitea mirror.

The marketplace does not offer what is installed; it offers what the CATALOG
lists. Those two sets differ by definition and the catalog set is strictly
larger. Measured on hw290 2026-07-27: of 29 `visibility: listed` catalog
entries, 14 resolved 200 against the pivoted registry and 15 returned 404 —
and Harbor's `openova-io` project carries `registry_id: None`, i.e. it is a
HOSTED project, not a proxy-cache, so the 404 is final. A cut-over Sovereign
therefore advertised 29 installable Blueprints and could install 14. The 14
that worked were exactly the ones that double as platform components with a
bootstrap-kit HelmRelease — i.e. the ones the two installed-derived
enumerations already happened to cover.

WHAT THE AUTHORITATIVE SET IS. The install path resolves a Blueprint to an OCI
coordinate through exactly two fields of the LIVE Blueprint CR:

    chart   = spec.manifests.chart            (application_controller.go
                                               blueprintChart(), L3496-3504)
    version = spec.version                    (surfaced as the catalog item's
                                               version by parseBlueprintCRToCatalog
                                               L277-279, carried into
                                               Application.spec.blueprintRef.version,
                                               then HR ChartVersion L1087/L1338)

`spec.source.version` is NOT read by any controller — it is a declarative
mirror of the same pin that CAN drift from `spec.version` (the console's
catalog IaC editor writes whole-CR edits: catalog_iac_edit.go, exercised by
TestCatalogIaCEdit_RoundTrip_VersionPersistsInFluxLeg_BareName, which bumps
alloy 1.0.2 -> 1.0.3 through the UI). So the LIVE CRs — not the chart-shipped
catalog-seed — are the truth: an operator-curated or console-edited Blueprint
is in the marketplace and must be in local Harbor, and the seed cannot know
about it. `spec.source.version` is still unioned into the MIRROR set (cheap,
strict superset, covers a stale-spec.version/fresh-source.version drift) but
never into the HARD set, because nothing installs from it.

WHY A SHARED LIBRARY. Same single-source discipline as the #4975 offline-mirror
resolver (03a) and the #5237 bootstrap-kit pin extractor (03b): step-03 uses
`catalog_chart_set` to build the mirror set AND `catalog_guard_missing` to
assert the result, so the set that is mirrored and the set that is judged can
never fork. `chart_declared_images` closes #5442 and #5443's layer-down leg: prewarm's
image enumeration walks the LIVE cluster's Pods, which by construction cannot
know the images of a chart nobody has installed — so the images for a catalog
chart must come from the CHART. `helm template` is a faithful (not heuristic)
enumerator here because .github/workflows/blueprint-release.yaml gates EVERY
published bp-* chart on a default-values `helm template` render of the packaged
tgz, with a <5-line empty-render block — a chart that cannot render at defaults
cannot be published, so it cannot be in the catalog.

Every function is PURE (file in, stdout out, no cluster/registry calls) so
tests/catalog-chart-set.sh exercises all three against fixtures at PR time.

NOT a cutover-step ConfigMap: no bp.openova.io/cutover-order label, so it does
not count toward the 11-step contract (tests/cutover-contract.sh Case 2/4).
```

## templates/00-gitea-admin-secret-bridge-job.yaml

```text
RUNTIME bridge of `gitea-admin-secret` into the cutover RELEASE namespace
(#4660, Refs #4557 #3811 #3379 #3642).

WHY THIS FILE EXISTS (the durable fix for the render-time-fragile lookup)
=========================================================================
The companion 00-gitea-admin-secret-bridge.yaml Helm-`lookup`s the source
secret (`gitea/gitea-admin-secret`) at RENDER time and re-emits a copy into
this chart's release namespace (`catalyst`). But bp-self-sovereign-cutover
installs DORMANT at bootstrap-kit slot 06a, which renders BEFORE bp-gitea
(slot ~14) exists → the render-time lookup returns nil → the bridge Secret is
never emitted. Once the cutover HR reaches Ready (no drift) helm-controller
does NOT re-render, so the bridge stays absent indefinitely. The first cutover
step Job that references `gitea-admin-secret` (06-helmrepository-patches and
09-gitea-token-mint read it via a Kubernetes `secretKeyRef`, which resolves
ONLY in the Pod's OWN namespace — there is no cross-namespace secretKeyRef)
then fails `CreateContainerConfigError: secret "gitea-admin-secret" not found`
→ the cutover wedges at step-06 (live on prov 17c2e8b2, kom4dc).

THE FIX (runtime copy — event-driven, not render-time-bound)
============================================================
This Job ships with the chart and runs at install. It WAITS at RUNTIME (a
poll loop, ~30 min default budget, values-overridable) for the source secret
`gitea/gitea-admin-secret` to exist, then copies it into the release namespace
as `destSecretName`. Because the wait happens at runtime — not at template
render — a late-arriving secret is materialised regardless of when bp-gitea
converges relative to slot 06a. The render-time lookup Secret is KEPT as the
zero-cost fast path for re-installs/upgrades where gitea already exists; this
Job is the GUARANTEE of eventual materialisation.

NOT A HELM HOOK
===============
A `post-install` hook with a 30-min poll would BLOCK the cutover HR's
install/upgrade (Helm waits on hook completion), and on a fresh prov gitea is
not up at slot-06a time so the hook would burn its whole budget every install.
This is a REGULAR Job; the 06a HelmRelease sets `disableWait: true`, so the HR
reaches Ready immediately and the Job converges asynchronously in the
background (exactly like the dormant registry-pivot DaemonSet that also ships
as a regular workload in this chart).

NO cutover-step labels
======================
This Job carries `catalyst.openova.io/component: gitea-admin-secret-bridge`
(matching the lookup Secret), NEITHER the ConfigMap kind NOR the
`app.kubernetes.io/component: cutover-step` label — so catalyst-api's step
discovery (it selects step ConfigMaps by that label) and the contract test's
exact-step-count assertion (11 steps / 10 job-mode) are unaffected.

CREDENTIAL HYGIENE (docs/INVIOLABLE-PRINCIPLES.md #10)
=====================================================
The admin-password bytes are streamed kubectl→kubectl IN-POD (a `kubectl get
secret -o json` of the source piped straight into a `kubectl apply` of the
destination) and are NEVER echoed/printed. Per #4 (never hardcode): the
source/destination namespace + secret name + poll budget all flow through
.Values.giteaAdminSecretBridge.* with the observed defaults.

RBAC SCOPING (feedback_rbac_create_no_resourcenames.md)
=======================================================
A dedicated ServiceAccount with TWO Roles:
  - a same-namespace Role granting create (NO resourceNames) + get/patch
    (resourceNames-scoped to the destination secret) on Secrets, so the copy
    can land in the release namespace;
  - a Role in the SOURCE namespace (`gitea`) granting get on Secrets scoped via
    resourceNames to ONLY `gitea-admin-secret` — the Job cannot read any other
    Secret in `gitea` or anywhere else on the cluster.
`create` is in its OWN rule WITHOUT resourceNames (the apiserver authz layer
403s a create rule that carries resourceNames — the bp-openbao 6+ loop root).
```
