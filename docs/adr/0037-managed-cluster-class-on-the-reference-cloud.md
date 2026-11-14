# ADR-0037 — Managed cluster class on the reference cloud: the platform's own Cilium on the managed Kubernetes service, for the workloads of an Organization in one region

Status: Accepted
Date: 2026-11-14

Written by work package NET-321, from the result of lab test NET-290 (`docs/ledger/plan/spikes/NET-290.md`, outcome POSSIBLE-WITH-LIMITS). It records the mechanism that runs the platform's own Cilium on the managed Kubernetes service of the reference cloud, the flags that were measured, what the class may host, and what the vendor must allow. The decisions are MC1 to MC10. The packages of epic NET-320 build them.

Terms used here:

- **Reference cloud**: the Huawei Cloud Stack installation that is the primary substrate (ADR-0024). Its provider pack is `infra/providers/huawei/`.
- **Managed Kubernetes service**: the managed Kubernetes service of the reference cloud (CCE). It runs the Kubernetes control plane and creates the node pools.
- **The service's plug-in**: the network plug-in that the service installs on every node. Its node agent runs as the systemd services `yangtse` and `yangtse-monit`.
- **Reference class**: k3s with the platform's own Cilium, the only cluster class today (ADR-0019).
- **Class file**: the data file of a cluster class under `platform/cluster-classes/`, with the fields `name`, `kind`, `provider`, `distribution`, `mayHostControlPlane`, `capabilities` and `notes`. NET-322 adds the folder, its schema and the conformance probe.

## Context

Measured by NET-290 on the reference cloud on 2026-11-13, in two sessions, on a standard cluster with the VPC network model, version v1.31.14-r20 upgraded to v1.32.9-r20, with Cilium 1.19.3, the version that `platform/cilium/chart` pins.

- The service's add-on catalogue has 23 entries. None is a Cilium-based data plane.
- Mechanism M1 of the lab test held: the platform's Cilium replaced the service's plug-in. The Cilium agent wrote its own CNI file and renamed the service's file `cni.conflist` to `cni.conflist.cilium_bak`. Every pod started after that was a Cilium endpoint. A Flux HelmRelease installed Cilium and applied seven later changes (Helm revisions 1 to 8). Mechanism M2, the tunnel model, was not needed and was not run.
- The service's node agent cannot be stopped in a supported way. With it stopped, the upgrade pre-check failed with `CNI Agent is not active` (Fatal).
- About every 90 seconds the agent re-adds the node's pod-range /24 route on its own gateway device and replaces Cilium's route. While that route was in place, the node could not reach its pods (`no route to host`) and CoreDNS went NotReady. With `endpointRoutes.enabled: true` every pod has its own /32 route and the agent's route carries no traffic: 0 failed dials in 144 probe lines, through a whole minor upgrade.
- The service leaves `Node.spec.podCIDR` empty. Each node's /24 is in the annotation `yangtse.io/vpc-router-node-allocated-subnets` of the service's `NodeNetworkConfig`. A small platform controller copied it into `Node.spec.podCIDR`, and a node added by scale-out joined with no hand step.
- Pods that started before Cilium kept the service's interfaces and had to be restarted once. Cilium's operator restarts only pods labelled `k8s-app=kube-dns`; the service labels its CoreDNS `k8s-app=coredns`.
- The minor upgrade rewrote the service's CNI file on each node. The running Cilium agent renamed it again in the same second, did not restart, and kept every change made through Flux.
- The node security group that the service creates opens TCP and UDP 30000 to 32767 and TCP 22 to any source.
- The service's create call offers kube-proxy in mode `iptables` or `ipvs` only. kube-proxy was stopped on one node by a host action; Services kept working, and the service did not start it again within 10 minutes. Whether an upgrade or a node repair starts it again was not measured.
- Nodes run docker 18.9.0, which the upgrade pre-check flags as deprecated.
- From a new network, the cloud's internal name servers did not answer and nodes did not join. With public name servers, nodes joined, but the upgrade pre-check hung on the internal time server name. A rebooted node did not come back; the likely cause is that time server name, and it is not proven.
- The cloud API gateway's path to a cluster answered `/version` with 404. An address bound to the cluster's API server worked for every call.
- Public entry with no node port worked: Cilium's Gateway API in host-network mode answered on port 80 of an edge-labelled node, and a load balancer created through the cloud's load-balancer API, not through the service's controller, reached host port 443 of that node. All 14 Services were ClusterIP. A pod on the member node could not reach the load balancer's address (hairpin). The Gateway's status stayed `Programmed=False` (`AddressNotAssigned`) while it served traffic.

The flags of the measurement sheet, and the value the class declares (MC6):

| Flag | Measured | Declared |
|---|---|---|
| `policy.k8s` | true | true |
| `policy.cilium` | true | true |
| `policy.l7` | true | true |
| `policy.fqdn` | true | true |
| `dataplane.kubeProxyFree` | true on one node, by a host action; not across an upgrade | false |
| `gateway.cilium` | true; the Gateway's status stays `Programmed=False` | true |
| `gateway.hostPort` | true | true |
| `mesh.cilium` | not measured | false |
| `encryption.wireguard` | not measured | false |
| `observability.hubble` | true | true |
| `node.daemonSets` | true | true |
| `lb.noNodePort` | true | true |
| `cilium.platformOwned` | true | true |

## Decision

The governing rule: **on the managed Kubernetes service of the reference cloud, the platform's own Cilium replaces the service's plug-in on the VPC network model, and the class hosts the workloads of an Organization in one region only.**

**MC1 Mechanism.** A standard cluster with the VPC network model. The platform's Cilium, from the chart `platform/cilium/chart`, replaces the service's plug-in. Its values set: `cni.exclusive: true`, `routingMode: native`, `autoDirectNodeRoutes: false`, `ipv4NativeRoutingCIDR` equal to the cluster's pod range, `ipam.mode: kubernetes`, `endpointRoutes.enabled: true`, `kubeProxyReplacement: true` with `k8sServiceHost` and `k8sServicePort` set to the cluster's internal API endpoint, and `encryption.enabled: false`. `endpointRoutes.enabled: true` is mandatory on this class. The cloud route table carries pod traffic between nodes, so the chart's per-node route job, `catalystOverlay.vpcPodcidrRouteReconciler`, stays off. Every other value is the reference class's, from `platform/cilium/chart/values.yaml` and the kit slot `clusters/_template/bootstrap-kit/01-cilium.yaml`, unless NET-324 names the field. The tunnel model is not used, and chaining over the service's plug-in is excluded (ADR-0019).

**MC2 The platform owns Cilium.** The Helm release `cilium` in namespace `kube-system` is installed and upgraded by a Flux HelmRelease, as on the reference class. The service's node agent stays running; it is never stopped. The state in which Cilium's CNI file is in place and the service's file is renamed is the normal state of this class, after every upgrade too.

**MC3 The pod range of a node.** A platform controller copies each node's /24 from the annotation `yangtse.io/vpc-router-node-allocated-subnets` of the service's `NodeNetworkConfig` into `Node.spec.podCIDR` when that field is empty. It runs in the host network, so it needs no pod network. It never changes a `podCIDR` that is set. NET-324 builds it. It stays until the service sets `Node.spec.podCIDR` itself.

**MC4 Pods that started before Cilium.** When the Cilium agents of all nodes are Ready, the first boot restarts, once, every pod that runs outside the host network and is not a Cilium endpoint. NET-324 builds it. Cilium's operator setting is not widened for this.

**MC5 What the class may host.** The workloads of an Organization, in one region. The class file declares `mayHostControlPlane: false`: a managed cluster does not host a Sovereign's control plane. That changes only when a managed cluster passes the same gate as the reference class, and then by a new decision record. While `mesh.cilium` is false, a managed cluster offers no placement across regions, no database pair and no global Service, and NET-326 does not start. While `encryption.wireguard` is false, no document or console page claims encryption in transit between the nodes of a managed cluster.

**MC6 The class file.** NET-322 adds this file, `platform/cluster-classes/cce-standard-vpc.yaml`, with exactly this content:

```yaml
name: cce-standard-vpc
kind: managed-byo-cilium
provider: huawei
distribution: cce-standard-vpc
mayHostControlPlane: false
capabilities:
  policy.k8s: true
  policy.cilium: true
  policy.l7: true
  policy.fqdn: true
  dataplane.kubeProxyFree: false
  gateway.cilium: true
  gateway.hostPort: true
  mesh.cilium: false
  encryption.wireguard: false
  observability.hubble: true
  node.daemonSets: true
  lb.noNodePort: true
  cilium.platformOwned: true
notes: Organization workloads in one region. Measured by NET-290; ADR-0037.
```

The conformance probe of NET-322 checks every flag declared true; a declared true that probes false fails gate check NET-G17. No check waits for a Gateway's `Programmed` condition on this class.

**MC7 No node port.** ADR-0026 holds on this class without exception. The provider pack removes, from the node security group that the service creates, the rules that open TCP and UDP 30000 to 32767 and TCP 22 to any source (NET-323). The public entry is a load balancer that the provider pack creates through the cloud's load-balancer API, with TCP listeners on 443 and 80 whose members are the edge nodes at host ports 443 and 80 (NET-325). The service's own load-balancer controller is not used, and no Service of type `LoadBalancer` is handed to it. Cilium's Gateway API runs in host-network mode with `keepCapNetBindService: true` and `NET_BIND_SERVICE` in the Envoy capabilities, as the reference class's values already set them. Gate check FND-G05 applies to every managed cluster, and the guards `scripts/check-no-nodeports.sh` and `scripts/check-live-nodeports.sh` apply to its sources and to the live cluster as on the reference class.

**MC8 The cluster and its node pools.** The provider pack creates them (NET-323):

- Node pools use the containerd runtime.
- Node subnets reach the cloud's internal name servers and its internal time server name. No host file is edited to work around them, because an upgrade resets host files.
- The cluster's API server has an endpoint that the provisioner reaches directly, open only to the sources that the request declares. The cloud API gateway's path to a cluster is not used.
- Node labels and taints, the edge label among them, are set on the node pool, because the service synchronises them from the pool.
- The node count of a pool is the only way to add or remove a node; a node machine is never deleted directly.

**MC9 What the vendor must allow.** This is the list for the vendor's certification:

1. The service's node agent runs while Cilium's CNI file replaces the service's file and the service's file is renamed; or a supported switch that stops the agent's route and CNI-file synchronisation.
2. The CNI directory is left as Cilium sets it across scale-out, upgrade and node repair. Node repair is not measured yet.
3. `Node.spec.podCIDR` is set by the service, or a platform controller may set it.
4. A supported mode without kube-proxy.
5. Privileged host-network DaemonSets with BPF.
6. Pod-range routes stay in the cloud route table.
7. A load balancer targets host ports of nodes without the service's controller.
8. No upgrade reverts what Cilium re-asserts.
9. The internal name servers and the internal time server name are reachable from node subnets.
10. The rules of the node security group that open 30000 to 32767 and SSH to any source can be removed and stay removed. Whether the service adds them again is not measured yet.
11. Node pools with the containerd runtime.

**MC10 Changing a declaration.** A flag declared false turns true only by a new decision record, after the measurement that proves it: `mesh.cilium` after a mesh between a managed cluster and a reference-class cluster over a private link; `encryption.wireguard` after WireGuard between two nodes of a managed cluster; `dataplane.kubeProxyFree` after kube-proxy stays removed across an upgrade. NET-329 also reboots one node of the managed cluster, in a subnet that reaches the internal name servers and the internal time server name, and the node must be Ready again with the platform's Cilium. The class is offered to an Organization only after that.

## Alternatives rejected

**MC1.** (a) The tunnel model with Cilium's own address pool. The VPC model held, and the tunnel model would need webhooks in the host network, because the managed API server cannot reach pod addresses. (b) Chaining over the service's plug-in: ADR-0019 excludes it. (c) Leaving `endpointRoutes.enabled` off: the agent's route replaces Cilium's every 90 seconds and the node loses its pods. (d) WireGuard on from the start: the mechanism was proven without it, and an unmeasured data path at first boot can stop the whole cluster.

**MC2.** Stopping the service's node agent. The upgrade pre-check then fails Fatal.

**MC4.** Widening the restart rule of Cilium's operator to the service's CoreDNS label. It covers CoreDNS only; the lab also had to restart two Flux controllers and a storage controller.

**MC5.** (a) A managed cluster that hosts a Sovereign's control plane now. It has not passed the gate of the reference class, and the mesh between regions is not proven on it. (b) Waiting for a vendor support statement before building the class. ADR-0019 does not count a missing statement as a reason; MC9 names what the vendor must allow instead.

**MC7.** (a) The service's own load-balancer controller. It allocates node ports, which ADR-0026 forbids. (b) Leaving the service's node-port rules open because no Service carries a node port. Any node port allocated later would then be reachable from any source.

**MC8.** A public time server written into the nodes' time configuration. The lab added one and the pre-check's node test passed, but the upgrade reset the file, and a node rebooted after it did not come back.

## Consequences

- An Organization can run its workloads on a managed cluster of the reference cloud in one region, once NET-322 to NET-325, NET-327 and NET-328 have merged and NET-329 is green.
- A managed cluster does not run a Sovereign's control plane, a database pair, a global Service or a placement across regions.
- Node-to-node encryption is off on a managed cluster, and the claim is withdrawn there.
- kube-proxy stays on a managed cluster as the service runs it, beside Cilium's replacement, until a supported mode without it exists.
- The service's node agent keeps running, and Cilium re-asserts its CNI file after every upgrade.

## Shipped via

NET-321 writes this decision record. In epic NET-320: NET-322 (the class file of MC6, its schema and the probe of gate check NET-G17), NET-323 (the cluster, its node pools and the network of MC7 and MC8), NET-324 (Cilium by MC1 to MC4), NET-325 (the public entry of MC7), NET-326 (the mesh, not started while `mesh.cilium` is false), NET-327 (the bootstrap kit profile), NET-328 (the class in the console) and NET-329 (the acceptance suite on the reference cloud).
