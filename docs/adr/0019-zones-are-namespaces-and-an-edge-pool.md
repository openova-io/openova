# ADR-0019 — Zones are namespaces and an edge pool

Status: Accepted
Date: 2026-11-13

Written by work package NET-050. It records the zone model of a Sovereign region, the trial of zones as virtual clusters and its withdrawal, and the posture of node-to-node encryption. It also closes the numbers 0005 to 0008.

## Context

Measured on `main` on 2026-11-13.

- On 2026-05-16 commit `c073db28a` added three charts: `platform/bp-mgmt-vcluster`, `platform/bp-rtz-vcluster` and `platform/bp-dmz-vcluster`. They ran the platform's management plane, its restricted plane and its internet-facing plane (the "zones") as one virtual cluster each. A scaffold for a DMZ virtual cluster, `products/dmz-vcluster`, had been added on 2026-05-09 (commit `2c32fde84`).
- On 2026-06-16 commit `5ef034d60` moved Keycloak and Grafana into the management virtual cluster. Both moves were reverted the next day. Keycloak's database Secret could not cross from the host into the virtual cluster (commit `19b41a32a`), and Grafana's ExternalSecret deadlocked its Kustomization before the kinds it needs were installed (commit `9abad75b7`).
- On 2026-06-25 commit `158ea9e2c` withdrew the design. Every platform component moved into a host namespace of its own, the plane virtual clusters were retired, and the new chart `platform/plane-isolation` added a default-deny policy for each of those namespaces. A virtual cluster stayed the boundary of an Organization only. Fixes for the fallout followed on 2026-06-25 and 2026-06-26, for example commits `5e3411e95`, `feb102578` and `c732f93ef`.
- No decision record was written for the trial or for its withdrawal.
- Traces of the trial remain: the four chart folders named above, the API route `/api/v1/sovereigns/{id}/networking/dmz` (`products/catalyst/bootstrap/api/internal/handler/networking.go`) with its console page, and the `tier:` values `mgmt`, `rtz` and `dmz` in the topology entries of `blueprint.yaml` files.
- Cluster names carry the token `rtz`, for example `hw-me-east-215-a-rtz-prod` (ADR-0015).
- `platform/cilium/chart/values.yaml` turns on WireGuard encryption with `nodeEncryption: true` and does not set the opt-out selector of the Cilium agent, so the upstream default `node-role.kubernetes.io/control-plane` applies. `docs/SECURITY.md` section 2.1 explains the effect. `scripts/check-live-node-encryption.sh` checks it on a live Sovereign.
- The numbers 0005 to 0008 were held for records that were never written (see the header of ADR-0009).

## Decision

1. On 2026-06-25 the zone-by-virtual-cluster design was withdrawn (commit `158ea9e2c`). This record is the decision.
2. A Sovereign region is one cluster. Zones inside it are: the edge (the edge node pool), the platform (the kit namespaces) and Organizations (one namespace and one virtual cluster each).
3. Separation is enforced by default-deny policy, the Organization fence, the edge taint, and security groups. The word "DMZ" is not used for anything else.
4. A virtual cluster is created only per Organization.
5. `rtz` inside cluster names is a fixed token with no zone meaning. It is not changed.
6. Separate clusters per zone are a future option. Triggers: (a) a regulator or contract requires that internet-facing and data workloads share no node, kernel or control plane; (b) a cluster class cannot enforce Cilium policy; (c) a penetration test shows a path from an edge node to data nodes that node isolation does not close; (d) a customer mandates an appliance tier and no cloud-native edge service exists. If triggered, a zone is a cluster with its own network and one authenticated hop, modelled as an optional `zoneRole` on a region record. No new kind.
7. Public claims allowed: encryption in transit between nodes; identity-based default-deny policy; no workload mutual TLS; no egress gateway; firewall in path only where a `WebApplicationFirewall` is Ready.
8. Node-to-node encryption keeps the upstream exclusion of control-plane nodes. WireGuard encrypts pod-to-pod traffic on every node. Node encryption, which also covers the traffic of the host network namespace, is on for every node except a node whose labels match the opt-out selector of the Cilium agent. That selector stays at the upstream default, `node-role.kubernetes.io/control-plane`, so the control-plane node of each region is excluded. The reason: the connection a node uses to publish its rotated WireGuard public key would otherwise be encrypted with the key that is being replaced. On an excluded node the host-network services (API server, etcd, kubelet) carry their own TLS. The claim "encryption in transit between nodes" of point 7 means exactly this. `scripts/check-live-node-encryption.sh` holds the same selector in `EXPECTED_OPT_OUT_SELECTOR` and fails when the state of a node differs from what its labels imply. Changing the selector needs a new decision record that replaces this point, and a key rotation proven on a fresh installation.
9. The numbers 0005 to 0008 are closed. No record will carry them.

## Where each part stands

| Part | Today | Work packages |
|---|---|---|
| Platform zone | Each platform component runs in a host namespace of its own. `platform/plane-isolation` puts a default-deny policy on each namespace that it lists. | Epic NET-160 puts default deny in both directions on every platform namespace. NET-166 then deletes the chart `platform/plane-isolation`. |
| Organization zone | One namespace and one virtual cluster per Organization (ADR-0025). | NET-170 adds the Organization fence: host-side deny rules that an Organization cannot override. NET-191 lets an Organization's routes attach only to that Organization's listeners. |
| Edge zone | No edge node pool exists. | Epic NET-180: NET-181 adds a tainted edge node pool, NET-182 binds the gateway listeners on edge nodes only, NET-183 closes the old entry, NET-184 adds a load balancer per region. |
| Security groups | The provider modules create them, for example `infra/providers/huawei/main.tf`. | NET-183 removes the web ports from the general security group. |
| Firewall | No `WebApplicationFirewall` kind exists, so the firewall claim of point 7 holds nowhere yet. | Epic NET-250. Record 0038 (NET-251) states the mechanism. NET-040 removes the Coraza chart and the SPIRE chart. |
| Traces of the trial | See Context. | NET-071 removes the dialog, the DMZ panels, two routes and the stub kind. NET-072 makes the placement selectors offer the host only. NET-221 deletes the four chart folders. Epic NET-220 removes the `tier:` values. |
| `zoneRole` | Not built. | None. It is added only when a trigger of point 6 holds. |
| Node encryption | As point 8 states. The gate check FND-G09 takes the verdict of `scripts/check-live-node-encryption.sh`. | NET-127 lets the script read its evidence without running a command inside an agent pod. |
| Canonical documents | `docs/ARCHITECTURE.md`, `docs/DOD.md` and `docs/GLOSSARY.md` still describe the zones as building blocks or as virtual clusters in places. | NET-055 brings them in line with ADR-0019. |

## Alternatives rejected

- **Separate clusters per zone as the default.** It triples the clusters of each region, and it adds new topology before the present one is proven. It stays a future option under the triggers of point 6. Separate edge clusters now are rejected for the same reason.
- **Zones as virtual clusters.** Tried from 2026-05-16 and withdrawn on 2026-06-25 (see Context).
- **Restricting the Envoy DaemonSet to edge nodes.** Envoy also serves Layer-7 policy on every node. The lever is where the gateway listeners bind.
- **A second virtual-cluster layer for hard isolation of an Organization.** The hard option is dedicated nodes per Organization, added later as an optional field.
- **Cilium egress gateway.** Upstream documents it as not compatible with ClusterMesh. Fixed egress addresses come from the provider module: the NAT addresses of each region, published by NET-100.
- **Cilium mutual authentication with SPIRE.** Deprecated in Cilium 1.20 and never compatible with ClusterMesh; its replacement requires ClusterMesh off. The SPIRE chart is deleted (NET-040).
- **A sidecar mesh for mutual TLS.** A second data plane for one claim. Encryption is WireGuard between nodes, identity is Cilium policy, and certificate identity is the application's own TLS where it is needed.
- **A cluster-class kind in the API.** Classes are catalogue data, like the cloud profile.
- **Rendering values from probed cluster flags.** The probe runs after the install, and rendering must not depend on it.
- **Chaining a second network plug-in onto the default plug-in of a managed cluster.** No managed class may do it.
- **Building class files, a probe chart and status fields while one class exists.** A field or a file is added only together with its reader. The reference class is k3s with platform-owned Cilium. It is the only class until a second one is admitted (epic NET-320, record 0037).
- **Treating a missing vendor support statement as a reason against platform-owned Cilium on a managed cluster.** The question is whether it is technically possible. Spike NET-290 answers it with measured flags and the list of what the vendor must allow. The NodePort ban of the platform holds on every cluster class.
- **Removing the exclusion of control-plane nodes from node encryption (an empty opt-out selector).** Key rotation and the re-provisioning of a node would then depend on the key that is being replaced. Point 8 says what a change needs.

## Consequences

- Every new need for a zone is met inside the region's one cluster: a namespace, a policy, a taint or a security group. A new cluster per zone needs one of the triggers of point 6 and a new record.
- A platform plane never again runs in a virtual cluster. A request for one is answered by point 4.
- The public claims about the network are the five of point 7, read with point 8. A document that claims more is wrong and is corrected (NET-055).
- `scripts/check-live-node-encryption.sh` cites ADR-0019 as the place where its selector is decided.

## Shipped via

Work packages of area NET in `docs/ledger/plan.yaml`: NET-040, NET-055, NET-071, NET-072, NET-127, NET-170, NET-191, NET-221, NET-290, and the epics NET-160 (default deny), NET-180 (edge pool and a load balancer per region), NET-220 (placement contract without tiers), NET-250 (firewall in the path) and NET-320 (the first managed cluster class).
