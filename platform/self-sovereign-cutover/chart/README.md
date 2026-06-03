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
| 04 | `registry-pivot` DaemonSet | pre-pivot | Always-on per-node reconcile loop for `/etc/rancher/k3s/registries.yaml` (v1 ↔ v2) |
| 05 | `cutover-step-05-flux-gitrepository-patch` ConfigMap | post-pivot | Patch `flux-system/openova` GitRepository.url → local Gitea |
| 06 | `cutover-step-06-helmrepository-patches` ConfigMap | post-pivot | Patch every OCI HelmRepository → `oci://harbor.<sov-fqdn>/openova-io` |
| 07 | `cutover-step-07-catalyst-api-env-patch` ConfigMap | post-pivot | `kubectl set env deploy/catalyst-api CATALYST_GITOPS_REPO_URL=...` |
| 08 | `cutover-step-08-egress-block-test` ConfigMap | post-pivot | CiliumNetworkPolicy denies egress to upstream for 10 min, asserts cluster stays Ready |
| 09 | `self-sovereign-cutover-status` ConfigMap | mutable state | Per-step state machine read+written by every step + the UI |

The phase column controls image routing: `pre`-phase steps must use
upstream-public images because they run BEFORE registries.yaml v2 takes
effect on the node containerd. `post`-phase steps may pull through local
Harbor when `.Values.global.imageRegistry` is set in the overlay.

## Values

See [`values.yaml`](./values.yaml). The required overlay-supplied keys are:

| Key | Purpose |
|---|---|
| `sovereign.fqdn` | The Sovereign's base FQDN (e.g. `otech.omani.works`). Drives every public URL in this chart. |
| `sovereign.harborPublicURL` | `https://registry.<sovereign.fqdn>`. The bp-harbor HTTPRoute publishes at `registry.<sov>`, NOT `harbor.<sov>` — see `clusters/_template/bootstrap-kit/19-harbor.yaml` `gateway.host`. Routed to by registries.yaml v2 + step 06. |
| `sovereign.giteaPublicURL` | `https://gitea.<sovereign.fqdn>`. Informational; jobs reach Gitea via the in-cluster Service URL. |

Per `docs/PRINCIPLES.md` #4 (never hardcode), the chart ships
no real defaults for the Sovereign coordinates — `helm template` smoke
renders against `example.local` placeholders, and runtime use without an
overlay is non-functional by design.

## Bootstrap-kit slot

The HelmRelease that installs this chart is at
`clusters/_template/bootstrap-kit/06a-bp-self-sovereign-cutover.yaml`.
It depends on `bp-gitea` and `bp-harbor` (so the local Gitea + Harbor
exist before the chart's step ConfigMaps reference them at trigger time)
and uses `disableWait: true` because the chart is dormant (no Pods to
wait on except the registry-pivot DaemonSet, which is event-driven on
the status ConfigMap).

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
