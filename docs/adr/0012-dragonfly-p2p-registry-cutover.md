# ADR-0012 — Dragonfly P2P registry distribution + cloud-agnostic cutover

- **Status:** Accepted (2026-06-29)
- **Shipped by:** #4640 (`bp-dragonfly`), #4641 (cutover registry-pivot rewrite), #4642 (auto-cutover wiring)
- **Supersedes the registry-pivot mechanism in:** ADR-0002 (post-handover sovereignty cutover) — the cutover *contract* is unchanged; only the registry-repoint *mechanism* changes.
- **Related:** ADR-0001 §9 (ClusterMesh is inter-region, same-plane), ADR-0011 (OpenTofu/Crossplane seam), issues #4637 / #4639.

## Context

The self-sovereignty cutover repoints every container-image source from the OpenOva
mothership to the Sovereign's own registry. Its registry-pivot **wedged on kom4dc
(Huawei)**: nodes were pointed at `registry.<fqdn>` → a public EIP → and kom4dc nodes
**cannot hairpin** to their own cluster's EIP. It "worked" on Hetzner only because
Hetzner allows the hairpin — an **implicit per-cloud assumption** (the registry-pivot
literally branched `if Hetzner / Huawei / Contabo`), plus `/etc/hosts` pins, a
non-standard `:30443` gateway port, and DNAT workarounds.

Separately, image distribution relied on a **single centralized bastion proxy-cache**
(`harbor.openova.io`, hardcoded in ~203 files) — a SPOF + bottleneck that caused real
outages (`#3735` hw159 ImagePullBackOff, anonymous-pull 401s) and an "artificial"
mothership tether the founder flagged for removal.

## Decision

Adopt **Dragonfly** (CNCF P2P image distribution) as the per-cluster registry layer,
and rewrite the cutover registry-pivot around it. Generic, zero per-cloud branches:

1. **Per-node `dfdaemon` as the containerd registry mirror** at `127.0.0.1:4001`
   (`hostNetwork`, proxy port). containerd `certs.d/_default/hosts.toml` points at the
   node-local dfdaemon — never the un-hairpinnable public EIP. Identical on every cloud.
2. **Node-as-cache / P2P:** dfdaemon fetches each blob from the upstream **once per
   cluster** (the seed peer) and distributes peer-to-peer over the LAN. The bastion
   proxy-cache is **removed from the image path**; ghcr is hit minimally (rate-limit
   resistant). The mothership remains the *provisioner only*, never in the image path.
3. **Each plane / cluster is self-contained** (its own registry + Dragonfly mesh). There
   is **NO cross-plane ClusterMesh** for images — that would breach the
   `bp-plane-isolation` default-deny and raise the blast radius (a DMZ compromise reaching
   mgmt). ClusterMesh stays **region-scoped, same-plane** per ADR-0001. Blast radius for
   the image path = one cluster.
4. **Cutover = a one-line `dfdaemon` upstream flip** (`registryMirror.addr`
   ghcr → in-cluster Harbor) **plus a `hostAlias`** mapping `registry.<fqdn>` → harbor
   ClusterIP. The hostAlias is essential: because dfdaemon is `hostNetwork` it inherits
   the node's hairpin, so without it the Harbor `Www-Authenticate` token realm
   (`https://registry.<fqdn>/service/token`) would hairpin — the actual #4637 root. No
   `/etc/hosts` / `:30443` / DNAT node surgery remains.
5. **Zero-touch cutover** (#4642): a qa/test prov (`qaFixtures.enabled`) auto-fires the
   cutover on handover for the full unattended walk; **permanent customer Sovereigns stay
   operator-gated** via the BSS "Achieve True Sovereignty" CTA, preserving #4061.

## Consequences

- **Cloud-agnostic:** the node→dfdaemon→registry path is byte-identical on Huawei and
  Hetzner. No per-cloud branch survives in the image/cutover path.
- **DR-correct:** each cluster self-seeds and survives loss of any other (region-kill).
- **Isolation-correct:** no cross-plane reach; default-deny intact.
- **Rate-limit-resistant:** once-per-blob-per-cluster upstream pulls + P2P fan-out.
- **Cost / follow-ups:** per-cluster registry footprint (full Harbor vs a lightweight
  per-plane registry is an open sizing choice); the dfdaemon "egg" should be pre-baked
  into the node image (k3s airgap bundle) so bootstrap needs no upstream — tracked as a
  follow-up, not blocking.

## Alternatives rejected

- **Keep the bastion proxy-cache:** SPOF, hardcoded ×203, recurring outages, artificial
  mothership tether.
- **Shared Harbor via the external endpoint** (`registry.<fqdn>` for dmz/rtz): reintroduces
  the exact kom4dc hairpin and couples DR to one endpoint.
- **Cross-plane ClusterMesh global service:** breaches plane isolation; blast-radius increase.
