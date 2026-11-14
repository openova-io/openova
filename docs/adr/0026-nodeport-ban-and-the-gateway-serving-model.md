# ADR-0026 — NodePort ban and the gateway serving model

Status: Accepted
Date: 2026-11-13

Written by work package NET-052. Since 2026-07-03 the platform forbids node ports. The rule is written in the repository guide `CLAUDE.md` and enforced by guards, but no decision record states it, and no record says how the public entries of a Sovereign are served without node ports. This record states the rule, the serving model of today and the target model.

Terms used here:

- **Node port**: a port from 30000 to 32767 that Kubernetes opens on every node for a Service of type `NodePort`, and by default also for each port of a Service of type `LoadBalancer`.
- **Host port**: a fixed port that a pod binds on the address of its node, with `hostPort` or because the pod runs in the host network.
- **Reference provider**: the provider module `infra/providers/huawei/`, the primary substrate (ADR-0024).

## Context

Measured on `main` on 2026-11-13.

- Before 2026-07-03 the gateway load balancer of the reference provider targeted the node port of the gateway Service (the version history in `clusters/_template/bootstrap-kit/01-cilium.yaml`). On 2026-07-03 commit `5c97fbf2d` moved the gateway to host ports, and commit `ba887fdf5` wrote the ban into `CLAUDE.md`. On 2026-07-04 commit `3ce78f0d7` removed the node ports of the mesh and DNS Services and added `scripts/check-no-nodeports.sh`. It replaced two LB-IPAM pools that claimed one address, whose conflict had made the mesh Service fall back to a node port, by one pool, `sovereign-vip`.
- Kubernetes allocates a node port for each port of a Service of type `LoadBalancer` unless the Service sets `allocateLoadBalancerNodePorts: false`. That field stops new allocations only. An apply that omits `nodePort` does not release a port that is already allocated. On 2026-08-01 a DNS Service was found with two node ports behind a clean render, for this reason (`scripts/check-live-nodeports.sh` describes the case).
- The reference provider serves its public entries on host ports today:
  - Gateway. Cilium's Gateway API runs with `gatewayAPI.hostNetwork.enabled: true`, set for this provider by `infra/providers/_shared/cloudinit-control-plane.tftpl`. The Envoy pods of Cilium run in the host network and bind host ports 443 and 80 on every node, the control-plane node included. The console Gateway binds host ports 8443 and 8080.
  - Load balancers. `infra/providers/huawei/main.tf` creates a gateway load balancer and, by default, a console load balancer, each with listeners on 443 and 80. Their members are the nodes of every region at host ports 443 and 80 (gateway) and 8443 and 8080 (console). The variables `gateway_member_port_https` and `gateway_member_port_http` refuse a port from 30000 up.
  - Shared address. The chart `platform/cilium` renders one `CiliumLoadBalancerIPPool`, `sovereign-vip`, whose one address is the public address of the region's control-plane node. Two Services of type `LoadBalancer` share it through the annotation `lbipam.cilium.io/sharing-key: sovereign-vip`: `clustermesh-apiserver` on port 2379 and `powerdns-anycast` on port 53. Cilium serves them itself, and both set `allocateLoadBalancerNodePorts: false`.
  - Mesh endpoint. The control-plane node runs the etcd of k3s on host port 2379, so peers dial the DaemonSet `clustermesh-proxy` on host port 12379, which forwards to `clustermesh-apiserver`.
- The second provider, `infra/providers/hetzner/`, leaves the gateway host network off: the cloud controller makes the load balancer of the gateway Service. There `dnsdist` runs as a DaemonSet with host port 53. That provider is unproven (ADR-0024).
- Guards in force:
  - `scripts/check-no-nodeports.sh` (workflow `.github/workflows/check-no-nodeports.yaml`) fails on a NodePort type, a non-zero `nodePort` or a `type = "NodePort"` in the sources, on the same in every chart it can render, and on a port from 30000 to 32767 in the provider modules.
  - `scripts/check-lb-nodeport0.py` (workflow `.github/workflows/check-lb-nodeport0.yaml`) fails when a chart template of a Service of type `LoadBalancer` lacks `allocateLoadBalancerNodePorts: false` or `nodePort: 0`.
  - The Kyverno policy `forbid-nodeport-service` (`platform/kyverno-policies/chart/templates/baseline/24-forbid-nodeport-service.yaml`) denies a Service of type `NodePort`, and a Service of type `LoadBalancer` without `allocateLoadBalancerNodePorts: false`, at admission once the chart's bootstrap mode is off. While bootstrap mode is on, it reports them. `scripts/check-kyverno-nodeport-exclusions.sh` keeps the policy's exclusion of solver Services bound to their label.
  - `scripts/check-live-nodeports.sh` reads the Services of a live cluster and fails on any Service whose type is `NodePort` or that has a port with a non-zero `nodePort`. It does not fail on six namespaces it lists as not owned by the platform, `kube-system` among them.
  - Gate check FND-G05, "No NodePort" (`docs/ledger/gates.yaml`), checks every region of a live Sovereign. Work package FND-034 implements it by running `scripts/check-live-nodeports.sh` once for each region.
- Three gaps are open:
  - The issuer `letsencrypt-http01-prod` (`platform/cert-manager-issuers/chart/templates/clusterissuers.yaml`) uses the HTTP-01 solver of cert-manager and sets no service type for it, so cert-manager makes the Service of each open challenge a Service of type `NodePort`. The Kyverno policy and the live guard exempt these Services by the label `acme.cert-manager.io/http01-solver`. The live guard fails on one that has stood longer than one hour in a namespace the platform owns.
  - The live guard does not fail on a node port in `kube-system`, where the Services of Cilium run.
  - The Service `clustermesh-apiserver` comes from the upstream Cilium chart. A post-renderer in `clusters/_template/bootstrap-kit/01-cilium.yaml` sets `allocateLoadBalancerNodePorts: false` on it, but nothing sets `nodePort: 0`, and `scripts/check-lb-nodeport0.py` reads only the platform's own chart templates.

## Decision

1. No node port, anywhere. No Service has type `NodePort`. No port of a Service has a `nodePort` other than 0. No load-balancer member, listener, health monitor or security-group rule targets a port from 30000 to 32767. Nothing relies on a node port that Kubernetes or a cloud controller allocates. This holds in charts, in provider modules, on every live cluster, and in tests and one-off proofs.
2. Every Service of type `LoadBalancer` states `allocateLoadBalancerNodePorts: false`, and `nodePort: 0` on each of its ports. Omitting `nodePort` is not enough.
3. The rule has no exception, on any provider and on any cluster class. A cluster class whose public entry needs node ports, also inside a load balancer that the cloud manages, is not admitted: a class file must declare the flag `lb.noNodePort: true` (NET-322). The three gaps of the Context are defects to close at their source. They are not exceptions, and no list of exceptions is kept.
4. Serving model of today, on the reference provider. Every public entry is a host port. The gateway is the host-network Envoy pods of Cilium on host ports 443 and 80 of every node, and the console Gateway on 8443 and 8080. The provider's load balancers target those host ports. The mesh endpoint is host port 12379. The mesh Service (2379) and the DNS Service (53) share the one address of the pool `sovereign-vip`, which Cilium serves without a node port.
5. Target model, built by epic NET-180: host ports on edge nodes, behind one load balancer per region.
   - NET-181 adds a tainted edge node pool in each region. Edge nodes have no public address.
   - NET-182 makes the gateway listeners bind their host ports on edge nodes only.
   - NET-183 removes all other nodes from the load-balancer members and deletes the any-source rules for the web ports from the general security group.
   - NET-184 gives each region its own load balancer. Its members are that region's edge nodes, at host ports 443, 80, 8443 and 8080.
   - NET-185 moves the DNS front end to host port 53 on the edge pool and deletes the pool `sovereign-vip`.
   - NET-200 makes the public entry fail over by DNS.

   The second provider takes the same model (CLD-184), and so does the first managed cluster class (NET-325).
6. A new public port is a host port behind the region's load balancer: on every node today, on edge nodes once NET-182 is merged.
7. The guards of the Context stay in force, and gate check FND-G05 is the proof on a live Sovereign.

## Alternatives rejected

- **A load balancer that targets node ports**, the usual shape of a load balancer that a cloud controller makes. It was the gateway path before 2026-07-03, and it is what point 1 forbids.
- **`allocateLoadBalancerNodePorts: false` alone.** It does not release a port that is already allocated. Point 2 adds `nodePort: 0`, and the live guard reads the ports of live Services.
- **Counting only Services of type `NodePort`.** A Service of type `LoadBalancer` carries node ports too. The live guard counts every port with a non-zero `nodePort`.
- **A gateway address from an LB-IPAM pool on the node's public address.** On the reference provider that address is translated one to one to the node's private address, and the gateway Service was not reachable from outside on it (the version history in `clusters/_template/bootstrap-kit/01-cilium.yaml`). Today host ports behind the provider's load balancer serve the gateway.
- **An exception for managed clusters**, whose cloud load balancer often needs node ports. Point 3.

## Consequences

- A chart that adds a Service of type `LoadBalancer` without the two fields of point 2 fails CI.
- A provider module that targets a port from 30000 to 32767 fails CI.
- A Sovereign region with a live node port fails gate check FND-G05.
- `CLAUDE.md` keeps its statement of the rule. This record is the decision behind it.

## Shipped via

Work packages in `docs/ledger/plan.yaml`: NET-181, NET-182, NET-183, NET-184, NET-185 and NET-200 (epic NET-180), CLD-184, NET-322, NET-325, FND-034 and FND-080.
