# Dragonfly P2P Registry Distribution + Cutover Redesign

> **Status:** PROPOSAL — for founder sign-off **before** any build.
> **Date:** 2026-06-29
> **Why:** the self-sovereign cutover wedges on kom4dc (no-hairpin Huawei) at
> step-07 image-pull. Root cause is **not** a single bug — it's the hand-rolled,
> per-cloud registry-pivot plus the centralized bastion proxy-cache. This
> proposal replaces **both** with Dragonfly P2P, making the image path generic
> (zero per-cloud branches), bastion-free, and node-local-cached.

---

## 1. The problem (why patching keeps failing)

Two coupled defects, both confirmed in the live tree:

1. **Per-cloud hairpin hack.** To make a node pull the platform's images from a
   registry, the node is pointed at `registry.<fqdn>` → which DNS-resolves to a
   **public EIP** → the node must "hairpin" back into its own cluster. Hetzner
   allows the hairpin; **kom4dc/Huawei does not** → every cutover image-pull
   breaks. The workarounds (`/etc/hosts` pin, gateway `:30443`, DNAT) are all
   symptoms. The registry-pivot literally branches `if Hetzner / if Huawei /
   if Contabo` (`platform/self-sovereign-cutover/.../04-registry-pivot-daemonset.yaml`).

2. **Bastion hub-and-spoke cache.** "Avoid hammering the mothership" is done
   today by pointing every node at **one shared proxy-cache on the bastion**
   (`harbor.openova.io`, hardcoded in **203 files**). It is a SPOF + bottleneck,
   it pulls **anonymously** (→ 401s), and it has caused real outages
   (`#3735 hw159 ImagePullBackOff: "dial tcp: lookup harbor.openova.io: Try again"`).
   The founder already flagged the bastion tether as **"artificial."**

```
  TODAY (broken):
     Sovereign-A nodes ─┐
     Sovereign-B nodes ─┼─▶ harbor.openova.io ──▶ ghcr.io
     Sovereign-C nodes ─┘   (ONE bastion proxy-cache, hardcoded ×203,
                             SPOF, anon-401s, hw159 ImagePullBackOff)
     + each node also hairpins to registry.<fqdn>:public-EIP at cutover
       → works on Hetzner, BREAKS on kom4dc (no hairpin)
```

---

## 2. Target: Dragonfly P2P distribution

[Dragonfly](https://github.com/dragonflyoss/dragonfly) (CNCF-graduated) runs a
node agent (`dfdaemon`) on every node. containerd is pointed at the **local**
`dfdaemon` (`127.0.0.1`) — a standard, documented containerd mirror. `dfdaemon`
fetches each blob from upstream **once per cluster** and distributes it
**peer-to-peer over the LAN**. The node *is* the cache.

**This was the recorded plan** (`docs/sessions/2026-06-12`: *"Dragonfly
registries"* — an unprioritized hw126 gap) that was never built; the hand-rolled
bastion/hairpin path filled the vacuum.

### What is removed / kept

| Component | Today | With Dragonfly |
|---|---|---|
| **Bastion** (`harbor.openova.io`) | central proxy-cache, SPOF, hardcoded ×203 | **REMOVED** from the image path; 203 refs deleted |
| **Mothership** | provisions + image-cache via bastion | **provisions ONLY**; never in the image path; runtime dep cut at cutover |
| **ghcr.io** | hammered by every node via the bastion | hit **once per blob per cluster** (authed), then P2P |
| **per-cloud branches** | `if Hetzner/Huawei/Contabo` in registry-pivot | **deleted** — node→127.0.0.1 path is identical on every cloud |
| **node config at cutover** | rewritten (`/etc/hosts`, ports, DNAT) | **unchanged** — only `dfdaemon` upstream flips |

---

## 3. The chicken-and-egg (the operator's key question)

The local Harbor **cannot** be the bootstrap cache — it doesn't exist yet during
early Phase-1. Resolution: **pre-bake `dfdaemon` + `Cilium` into the node image**
via k3s's standard airgap-images bundle (`/var/lib/rancher/k3s/agent/images/*.tar`,
imported into containerd at startup, **zero network pulls**). Those two are the
only "egg"; once present at boot, `dfdaemon` serves everything else.

```
  STAGE 0 — node boot (cloud-init)
     node image ALREADY contains:  k3s + Cilium + dfdaemon   (airgap tar, 0 pulls)
     containerd mirror = 127.0.0.1 (dfdaemon)        ← the egg is in the shell

  STAGE 1 — Phase-1 convergence (everything else, INCLUDING Harbor itself)
     containerd → 127.0.0.1 dfdaemon ──miss──▶ ghcr.io   (ONCE per blob, authed)
                               └──peer has it──▶ LAN copy (P2P)
     Cilium/Flux/crossplane/cnpg/keycloak/HARBOR/... all come up via dfdaemon.
     Harbor here is just a CONSUMER — not the cache.

  STAGE 2 — Harbor now up → warm-up SEEDS it with the full image set
     (push blobs into Harbor so it can serve locally once egress is cut)

  CUTOVER — flip dfdaemon upstream:  ghcr  →  harbor-core.harbor.svc   (one line)
     the node→dfdaemon path NEVER changes; no node surgery, no hairpin, no branch

  AFTER — node → dfdaemon → local Harbor (no ghcr, no bastion, no mothership)
```

---

## 4. Topology — BEFORE cutover

```
              ghcr.io   ← OpenOva's REAL image source (the only upstream)
                 ▲
                 │  fetched ONCE per blob per cluster (authed), by the seed peer
   ┌─────────────┴──────────── SOVEREIGN CLUSTER ───────────────────┐
   │   Dragonfly seed peer (1–2 nodes) ─ does the upstream fetch     │
   │      ▲                                                          │
   │   P2P mesh:   node1 ◀──LAN──▶ node2 ◀──LAN──▶ node3            │
   │      ▲ every node: containerd → 127.0.0.1 dfdaemon             │
   │   (Harbor is converging here as a normal workload, NOT a cache)│
   └────────────────────────────────────────────────────────────────┘
     ✗ NO bastion.   Mothership only PROVISIONED this cluster — not in image path.
```

## 5. Topology — AFTER cutover

```
   ghcr.io ┃ github.com ┃ harbor.openova.io ┃ mothership   ✂ ALL BLOCKED
                                                  (600s egress-hold proves it)
   ┌──────────────── SOVEREIGN CLUSTER (self-sufficient) ───────────────┐
   │   Harbor  (now holds EVERYTHING locally — no upstream at all)      │
   │      ▲ dfdaemon upstream = harbor-core.harbor.svc                  │
   │   Dragonfly P2P:   node1 ◀──LAN──▶ node2 ◀──LAN──▶ node3          │
   │      ▲ containerd → 127.0.0.1 dfdaemon                            │
   └────────────────────────────────────────────────────────────────────┘
```

**The cutover is now a one-line upstream flip** (`ghcr → harbor.svc`), not
node-network surgery. That is the whole simplification.

---

## 6. Work breakdown

1. **`bp-dragonfly`** (new platform Blueprint): Dragonfly Manager + Scheduler +
   Seed Peer + per-node `dfdaemon` DaemonSet. A bootstrap-kit slot, early order.
2. **Cloud-init / node image**: bundle `dfdaemon` + `Cilium` as a k3s airgap tar;
   set containerd registry mirror to `127.0.0.1:65001`. Remove the
   `harbor.openova.io` mirror redirects (the 203-ref hardcode). Generic — no
   per-cloud branch.
3. **Cutover rewrite**: delete the `04-registry-pivot-daemonset` per-cloud
   `/etc/hosts`/hosts.toml surgery. Replace the "registry pivot" step with a
   **one-line `dfdaemon` upstream flip** (ghcr → local Harbor). Warm-up step
   seeds Harbor (push), as today, but nodes consume via dfdaemon P2P.
4. **Remove the bastion from the image path** entirely; keep it only for
   operator SSH / non-image roles (or retire).

## 7. Rollout (de-risked — no blind prov)

1. Build `bp-dragonfly`; validate `dfdaemon`-as-mirror on a throwaway cluster
   (a real `crictl pull` succeeds via 127.0.0.1) **before** touching cutover.
2. Wire the airgap bundle + containerd mirror in cloud-init; one prov proves
   Phase-1 converges with **zero bastion + zero ghcr-per-node hammering**.
3. Rewrite the cutover registry step to the upstream-flip; one prov proves
   cutover→`cutoverComplete` on **kom4dc** (the cloud that breaks today).

## 8. Multi-cluster topology (per-region DR)

A Sovereign can be multi-cluster (e.g. region-a primary + region-b DR). The
upstream target must be **cluster-local**, never a shared/external name:

```
  ┌──────── CLUSTER region-a ────────┐   ┌──────── CLUSTER region-b ────────┐
  │ Harbor-a (harbor-core.harbor.svc)│   │ Harbor-b (harbor-core.harbor.svc)│
  │   ▲ dfdaemon upstream = LOCAL    │   │   ▲ dfdaemon upstream = LOCAL    │
  │ Dragonfly mesh-a                 │   │ Dragonfly mesh-b                 │
  └──────────────────────────────────┘   └──────────────────────────────────┘
     each cluster self-sufficient · region-b survives region-a loss
```

- **Use `harbor-core.harbor.svc` (cluster-local), NOT `registry.<fqdn>` (external).**
  The svc name is the same string in every cluster but always resolves to *that
  cluster's own* Harbor → zero cross-cluster coupling.
- **Why not the external name:** `region-b → registry.<fqdn> → WAN/gateway →
  region-a Harbor` makes region-b depend on region-a — **breaks the region-kill
  DR test** and reintroduces the gateway/hairpin/TLS mess over the WAN.
- Each cluster runs its **own** Harbor + Dragonfly mesh, **independently seeded**
  (region-b's warm-up fills Harbor-b). Cross-cluster P2P (region-b borrowing
  blobs from region-a peers when both are up) is an **optimization only — never
  a hard dependency**.
- Implication: `bp-harbor` + `bp-dragonfly` install **per cluster** (both
  regions), not primary-only.

## 8a. Plane-isolation topology — each plane SELF-CONTAINED (no cross-plane mesh)

Real Sovereign topology can be **6 isolated clusters** (2 regions ×
{mgmt, dmz, rtz}).

> ⚠️ **Rejected approach (would breach isolation):** an earlier draft proposed a
> Cilium ClusterMesh *global service* so dmz/rtz pull from mgmt's Harbor. This is
> **WRONG**: (1) ClusterMesh is **region-scoped, same-plane** per ADR-0001 §9
> (the canonical *inter-region* mechanism), **not** a cross-plane primitive;
> (2) `bp-plane-isolation` enforces a **hard default-deny between planes**
> (purely-additive allow-list, no `world`). Meshing `dmz → mgmt` punches a hole
> in that isolation → a DMZ compromise could reach mgmt → **blast-radius
> increase.** Do not do this.

**Correct design — zero cross-plane image path:**

```
  REGION A                                  REGION B
  mgmt-a [own registry + Dragonfly]         mgmt-b [own registry + Dragonfly]
  dmz-a  [own registry + Dragonfly]         dmz-b  [own registry + Dragonfly]
  rtz-a  [own registry + Dragonfly]         rtz-b  [own registry + Dragonfly]

  ✗ NO mesh between planes (mgmt ↛ dmz ↛ rtz) — default-deny holds; blast radius = 1 cluster
  ✓ region mesh (mgmt-a↔mgmt-b, SAME plane) stays for stateful DR per ADR-0001 — untouched
```

- Each of the 6 clusters runs its **own** registry + **own** Dragonfly mesh,
  fully independent. `dfdaemon` upstream = **that cluster's local registry svc**.
- Each cluster **seeds itself from ghcr** through its **own
  additively-allow-listed egress** (pre-cutover) — **never from another plane**.
  Post-cutover, each cluster's egress is blocked and it runs on its own registry.
- **No cross-plane connectivity for images, ever** → a DMZ compromise cannot
  traverse to mgmt via the image path. Blast radius = one cluster.
- Images need **no mesh at all** (cross-plane *or* cross-region). The region mesh
  (mgmt-a↔mgmt-b) exists only for stateful DR (CNPG-pair) and is orthogonal.

## 9. Open questions for the founder

- **Cross-cluster dedup:** the bastion gave one shared cache across *all*
  Sovereigns. Per-cluster Dragonfly hits ghcr once-per-blob *per cluster*
  (authed → not rate-limited). Acceptable, or do we also want an OpenOva-level
  "super-seed" (optional, not a per-node hardcode)?
- **Node-image ownership:** pre-baking the airgap tar means we own a node-image
  build step. OK, or pull `dfdaemon` first from ghcr (1 small image) instead?
- **Scope/sequence:** ship this as the cutover fix now, or land it as the
  generic registry layer first (also fixes the bastion outages independent of
  cutover)?
- **Per-plane registry footprint (from §8a):** isolation requires each of the 6
  clusters to be self-contained for images. Full Harbor per plane (heavy: core +
  db + redis + jobservice + portal ×6) vs a **lighter per-plane registry**
  (mgmt keeps the full Harbor with scanning/replication; dmz/rtz get a minimal
  local registry, e.g. `registry:2`/Zot, fronted by Dragonfly)? Isolation is
  identical either way — pure footprint trade-off.
