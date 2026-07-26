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
- `catalyst-api` auto-fires after the first successful sovereign-admin login on
  the new Sovereign (configurable via `.Values.trigger.auto`, default
  `false`).

A handed-over Sovereign therefore starts soft-tethered (still pulling
through `harbor.openova.io`) and becomes hard-independent only after the
`sovereign-admin` chooses or after the auto-fire grace period.

## Step inventory

| # | Resource | Phase | Description |
|---|---|---|---|
| 01 | `cutover-step-01-gitea-mirror` ConfigMap | pre-pivot | `git clone --mirror` upstream → `git push --mirror` to local Gitea |
| 02 | `cutover-step-02-harbor-projects` ConfigMap | pre-pivot | Create 7 proxy_cache projects in local Harbor |
| 03 | `cutover-step-03-harbor-prewarm` ConfigMap | pre-pivot | HEAD-pull every image referenced by bootstrap-kit through proxy-cache |
| 04 | `registry-pivot` DaemonSet | pre-pivot | Always-on per-node reconcile loop (Dragonfly, #4639): on `registriesYamlActive=v2` it writes the containerd `_default/hosts.toml` catch-all → the node-local dfdaemon proxy, flips the `bp-dragonfly` dfdaemon upstream ghcr → local Harbor, and adds a `registry.<fqdn>` → cilium-gateway ClusterIP hostAlias (the #4637 hairpin kill) |
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
