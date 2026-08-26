# Dragonfly

Per-cluster **P2P container-image distribution**. A per-node `dfdaemon` serves
every node's containerd registry-mirror; each image blob is fetched from the
upstream registry **once per cluster** and then distributed **peer-to-peer over
the LAN** — the node IS the cache.

**Role in Catalyst:** control-plane infrastructure (the per-cluster image
distribution layer). It replaces two pieces of the legacy image path:

1. the centralized **bastion proxy-cache** (`harbor.openova.io`, a SPOF that
   pulled anonymously → 401s, and caused live `ImagePullBackOff` outages), and
2. the **per-cloud hairpin registry-pivot** that wedges the self-sovereign
   cutover on no-hairpin clouds (kom4dc/Huawei) — the `node → 127.0.0.1
   dfdaemon` path is identical on every cloud, so the cutover becomes a
   one-line upstream flip with zero per-cloud branches.

Canonical design (sovereign-admin-reviewed), tracked by **#4639**.

This Blueprint wraps the **upstream `dragonfly` Helm chart**
(`dragonflyoss/helm-charts`, `dragonfly@1.7.0`, appVersion 2.5.0 / client
v1.4.0) as a Helm subchart via `chart/Chart.yaml` `dependencies:`. Catalyst-
curated values flow into the subchart under the `dragonfly:` key in
`chart/values.yaml`.

> See [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) §3.5 for where this
> sits in the Catalyst control plane, and the `bp-harbor` README for the
> companion local-registry component Dragonfly distributes from post-cutover.

---

## Components deployed

| Component | Kind | Role |
|---|---|---|
| **Manager** | Deployment | Control plane: scheduler registry, seed-peer registry, console (internal). Backed by MySQL + Redis. |
| **Scheduler** | StatefulSet | Decides which peer serves which piece of a blob. |
| **Seed Peer** (`seedClient`) | StatefulSet | Does the per-cluster upstream fetch — pulls each blob from the upstream registry once, then the mesh distributes it. Has a local cache PVC. |
| **dfdaemon** (`client`) | **DaemonSet** | The per-node peer + HTTP proxy. containerd on every node mirrors through its **local** dfdaemon at `127.0.0.1:4001`. |
| MySQL + Redis | StatefulSet | Bundled (Bitnami subcharts) manager backing store. A production overlay can point at external CNPG/Valkey. |

---

## How the containerd mirror works

```
  node containerd ──mirror──▶ 127.0.0.1:4001 (local dfdaemon proxy)
                                   │  cache miss
                                   ▼
                          upstream registry  (ghcr.io pre-cutover)
                                   │  once per blob per cluster
                                   ▼
                          P2P mesh: node1 ◀─LAN─▶ node2 ◀─LAN─▶ node3
```

- **dfdaemon proxy port:** `127.0.0.1:4001` (`client.config.proxy.server.port`).
  The design doc names `65001` — that is the **Dragonfly 1.x `dfget`** proxy
  port; the modern 2.x client (the version this Blueprint wraps) listens on
  `4001`. Override `proxyPort` + the dfinit `proxy.addr` in lockstep to keep a
  legacy `65001` endpoint.
- **Upstream registry (pre-cutover):** `https://ghcr.io`
  (`client.config.proxy.registryMirror.addr` + the same on `seedClient`). The
  Dragonfly **component images** themselves pull from ghcr via the `ghcr-pull`
  Secret (`global.imagePullSecrets`).
- **Post-cutover:** the self-sovereign cutover flips `registryMirror.addr` to
  the cluster-local Harbor via its **external host** `https://registry.<fqdn>`
  (:443 through the Cilium Gateway `Service type=LoadBalancer`, #4682) — a
  **single line**, no node surgery, no in-cluster ClusterIP / NodePort / CoreDNS
  rewrite. That pivot lives in `platform/self-sovereign-cutover/` and is a
  **separate, later-validated step** (NOT wired here).

### Who writes the containerd config?

Two options, gated by `client.dfinit.enable` (default **`false`**):

- **Default (v1, #4639):** the **node image / cloud-init** owns the
  containerd-mirror wiring (the k3s airgap-bundle change — pre-bakes dfdaemon +
  Cilium and sets the `127.0.0.1:4001` mirror at boot). That cloud-init change
  is **deferred** and not part of this Blueprint. `dfinit` stays off so two
  writers never fight over `config.toml` and `restartContainerRuntime` (which
  bounces containerd) stays out of the default path.
- **Opt-in:** a per-Sovereign overlay that is NOT pre-baking the mirror sets
  `dragonfly.client.dfinit.enable: true`, letting Dragonfly itself install the
  containerd `hosts.toml` (with `proxyAllRegistries: true` + an explicit
  `ghcr.io`/`docker.io` registry list) at install time.

---

## Self-contained per cluster (no cross-plane image path)

Per the sovereign-admin decision (design §8a, option **(A)**): every cluster — each
region, each plane (mgmt/dmz/rtz) — runs its **own** Dragonfly mesh and
**self-seeds from ghcr** through its **own** additively-allow-listed egress.
There is **no cross-plane or cross-region image path**, so a DMZ compromise
cannot traverse to mgmt via images; **blast radius = one cluster**. Cross-cluster
P2P (a peer borrowing blobs from another cluster's peers) is an **optimization
only, never a hard dependency**. Per-plane/per-region deployment is a
bootstrap-kit slot concern, not chart internals.

---

## Configuration knobs

Catalyst-curated values under `dragonfly:` in `chart/values.yaml`:

| Value | Default | Notes |
|---|---|---|
| `dragonfly.global.imagePullSecrets` | `[{name: ghcr-pull}]` | Pulls the Dragonfly component images from ghcr. |
| `dragonfly.manager.replicas` | `1` | HA overlay → 3. |
| `dragonfly.scheduler.replicas` | `1` | HA overlay → 3. |
| `dragonfly.seedClient.replicas` | `1` | Seed peers. |
| `dragonfly.seedClient.persistence.size` | `20Gi` | Per-seed cache. |
| `dragonfly.seedClient.persistence.storageClass` | `""` | Empty ⇒ cluster-default **durable** cloud CSI. local-path is FORBIDDEN (#3971). |
| `dragonfly.client.config.proxy.server.port` | `4001` | Node containerd mirror endpoint. |
| `dragonfly.client.config.proxy.registryMirror.addr` | `https://ghcr.io` | Upstream registry; the cutover step flips this to local Harbor. |
| `dragonfly.client.dfinit.enable` | `false` | See "Who writes the containerd config?" above. |
| `dragonfly.mysql.enable` / `dragonfly.redis.enable` | `true` | Bundled manager backing store. |
| `dragonfly.injector.enable` | `false` | Pod-mutation path OFF — Catalyst uses the containerd-mirror path. |

### Kyverno compliance

Every workload's **primary container** declares `resources.requests` (cpu +
memory) and `livenessProbe` + `readinessProbe`, so the cluster Kyverno
`resource-requests` and `probes-present` **Enforce** ClusterPolicies admit it.
The upstream chart defaults requests to `'0'` (would be denied) — this Blueprint
sets non-zero requests on manager / scheduler / seedClient / dfdaemon and their
init containers. (Note: the upstream `wait-for-scheduler` init container exposes
no values hook for resources; the policies validate primary `containers` only,
not init containers, so this is admitted.)

---

## Operational notes

- **Backups:** the mesh is effectively **stateless** — each seed's cache PVC is
  a regenerable read-through cache (re-fetched from upstream on a miss). The
  manager's MySQL holds only ephemeral peer/seed registry state. Nothing here is
  in the Velero must-backup set.
- **Scaling:** dfdaemon is a DaemonSet — it scales with the node count
  automatically. Bump `seedClient.replicas` to widen the upstream-fetch fan-out.
- **Multi-region / DR:** install **per cluster** (both regions, all planes).
  Each cluster is independent; region-B survives region-A loss because it runs
  its own mesh + self-seeds. There is no replication/switchover primitive — the
  mesh is reconstituted from upstream, not from a peer cluster.
- **Air-gap:** post-cutover each cluster serves entirely from its local Harbor
  via dfdaemon — no ghcr, no bastion, no mothership in the image path.

---

*Part of [OpenOva](https://openova.io)*
