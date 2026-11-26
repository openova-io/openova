# Cluster classes

One data file per cluster class: what a cluster of that class can do,
declared as one boolean per capability flag. This folder is data, not a
Blueprint, and no chart reads it. Decision record:
[ADR-0037](../../docs/adr/0037-managed-cluster-class-on-the-reference-cloud.md);
the reference class is the one of
[ADR-0019](../../docs/adr/0019-zones-are-namespaces-and-an-edge-pool.md).

This folder holds the schema and the classes that name no provider
(`provider: any`). The class of one provider lives in that provider's pack,
`infra/providers/<provider>/cluster-classes/<name>.yaml`, with `provider:` set
to that pack: a file outside the plug-in paths names no provider. The reader
reads both places and refuses a class name that has two files.

| Class | Where | May host the control plane |
|---|---|---|
| `reference-k3s-cilium`: k3s with the platform's own Cilium, every flag true | this folder | yes |
| The managed Kubernetes service of the reference cloud with the platform's own Cilium, Organization workloads in one region (ADR-0037 MC6, its text verbatim) | the pack of the reference cloud | no |

## The file

`schema.json` is the schema. Fields: `name` (the file name without
`.yaml`), `kind` (`reference`, `managed-byo-cilium`, `managed-native-cilium`,
`managed-chained` or `managed-opaque`), `provider` (`any` or a provider pack),
`distribution`, `mayHostControlPlane`, `capabilities` (every flag of the
schema, `true` or `false`) and `notes` (optional). `lb.noNodePort` must be
`true`: a class whose public entry needs a node port is not admitted
(ADR-0026).

A class file uses a subset of YAML, so that the acceptance suite, a module of
the standard library only, reads it as every YAML reader does: `key: value`
lines, the two-space indented lines of `capabilities`, blank lines and comment
lines; a value is `true`, `false` or text that starts with a letter and holds
neither `: ` nor ` #`. The reader is
`tests/acceptance/sovereign/internal/suite/cluster_class.go`; it refuses any
other line, and any keyword in `schema.json` that it does not apply.

## The conformance probe

Gate check NET-G17 (`tests/acceptance/sovereign/internal/suite/check_cluster_class.go`)
runs, in every region of a gate run, the probe of every flag that the
region's class declares `true`. A declared `true` that probes `false` fails the
check. A flag declared `false` is not applicable: its probe does not run.

| Flag | The probe passes when |
|---|---|
| `policy.k8s` | A dial to a server Pod without a policy connects, and a dial to one that a NetworkPolicy selects times out. |
| `policy.cilium` | A dial to a server Pod that a CiliumClusterwideNetworkPolicy opens only to the `ingress` entity times out. |
| `policy.l7` | Behind an HTTP rule, `GET /allowed` answers 200 and `GET /denied` answers 403. |
| `policy.fqdn` | A Pod whose egress rule names one DNS name reaches it, and another name, which a Pod without the rule reaches, is not resolved. A dial that times out fails: the rule opens only the cluster DNS, so the L3/L4 part of the policy drops that dial whether or not the DNS rule is enforced. |
| `dataplane.kubeProxyFree` | No kube-proxy Pod or DaemonSet in `kube-system`, nothing listens on `127.0.0.1:10256` of any node, and every Cilium agent reports `KubeProxyReplacement: True`. |
| `gateway.cilium` | GatewayClass `cilium` is accepted by Cilium's controller, and `console.<Sovereign>` answers 200 or a redirect on port 80 of a node the gateway binds. |
| `gateway.hostPort` | Port 443 connects on every node that cilium-config's gateway node selector chooses and is refused on every other node. |
| `mesh.cilium` | Step D0.10 of the discovery passed. |
| `encryption.wireguard` | `scripts/check-live-node-encryption.sh` passes for the region. |
| `observability.hubble` | Hubble of the client's node holds the flow of a dial from the client to a server. |
| `node.daemonSets` | DaemonSet `kube-system/cilium` runs on the host network with one Ready Pod per node. |
| `lb.noNodePort` | `scripts/check-live-nodeports.sh` passes for the region. |
| `cilium.platformOwned` | One Ready HelmRelease makes the Helm release `cilium` of `kube-system`, and the Cilium DaemonSet carries its Flux labels. |

Every region also passes: its nodes run the class's distribution, and no two
Pods share an address.

## Adding a class

Add the file, with every flag measured, and the decision record that admits
it. A flag declared `false` turns `true` only by a new decision record after
the measurement (ADR-0037 MC10). A new flag goes into `schema.json` with its
probe in the same change; the suite refuses a class that declares `true` a
flag it has no probe for.
