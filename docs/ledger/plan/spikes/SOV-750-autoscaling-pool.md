# SOV-750 Lab test: an auto-scaling group worker pool on the reference cloud (result)

Date: 2026-11-14, one session. Type: lab test. Outcome: **yes** on all three rows. The decision rule is applied: the `autoscaled` pool mode may be proposed for this cloud. Two conditions come with it:

- the worker boot data must set the kubelet provider-id to the instance id;
- the conformance test must still prove 1 to 2 to 1 on a real pool.

This run replaces the incomplete run of 2026-11-13, which never got a VPC.

## Question

Can a worker pool on the reference cloud be an auto-scaling (AS) group that the upstream cluster-autoscaler sizes?

- E1: Does the provider the reference module uses have AS group and configuration resources, and do they work on this installation?
- E2: Does a group whose configuration carries the module's worker boot data join one node to a test cluster?
- E3: Does the upstream cluster-autoscaler's provider for this cloud scale that group up and back down?

Decision rule: if E1, E2 and E3 are all `yes`, the mode `autoscaled` may be offered for a worker pool on this cloud, with a conformance test on a real pool. If any row is `no`, `fixed` stays the only pool mode on this cloud.

## What was run

- One lab session on 2026-11-14, 09:51 to 11:02 UTC, in the project and the region the test environment was installed with.
- Tools: OpenTofu 1.12.0 with huaweicloud provider 1.91.0 (the version in the lock file of `infra/providers/huawei`). Signed REST calls for probes and read checks.
- The provider was given the endpoint key `autoscaling`.
- The rig:
  - an own VPC and subnet;
  - a security group;
  - a NAT gateway with one new EIP: SNAT for egress, and DNAT for SSH and the Kubernetes API, open to the lab machine only;
  - a key pair;
  - one k3s server VM: s7n.medium.2, k3s v1.31.8+k3s1 (the module's pin), packaged components and CoreDNS disabled, tainted NoSchedule;
  - the AS group: min 0, desired 0, max 1, flavor s7n.small.1 (1 vCPU, 1 GB), the module's default image, 40 GB SSD.
- Two AS configurations:
  - "module": the module's worker boot data (`infra/providers/huawei/cloudinit-worker.tftpl`), rendered as the module renders it.
  - "pid": the same, plus one kubelet argument, provider-id set to the instance uuid from the metadata service.
  - In both, two items were removed: the registry mirror file (it points at the jump host's mirrors and needs a registry credential) and the image warm-up. Neither affects joining.
- The group ran with the "pid" configuration. The "module" configuration was created but not booted.
- Upstream cluster-autoscaler v1.31.5 (official image, amd64 binary) ran on the lab machine against the test cluster, with `--cloud-provider=huaweicloud`, `--nodes=0:1:<group name>`, a 10 s scan interval, 1 min unneeded time and 1 min delay after add. Its cloud config came through a pipe, never a file.
- The lab cap of two VMs at once (server plus one group node) set the E3 shape to 0 to 1 to 0.

## Measured values

| Row | Result |
|---|---|
| E1 | yes |
| E2 | yes |
| E3 | yes (shape 0 to 1 to 0) |

### E1

- AS configurations: created in under 1 s each, deleted in about 1 s.
- AS group: created right after its subnet, status INSERVICE.
- Switching the group to the other configuration took 1 s.
- Deleting the group took 10 s.
- Read check after deletion: 404 `AS.0007`.

### E2

- The autoscaler set the group to 1 at 10:49:21.
- The instance was INSERVICE at 10:50:15, 54 s later.
- The node was Ready in the test cluster at 10:52:35, about 3 min 14 s after the decision.
- The node's `spec.providerID` was the bare instance id. The kubelet argument worked, and k3s did not overwrite it.
- The module's boot data (package install, hardening, k3s agent install with retry) ran unchanged on the 1 GB flavor.
- The node name is the configuration name plus a random suffix, in lower case.

### E3

- The autoscaler found the group by name on its first loop and planned `0->1 (max: 1)` for the pending workload. That is a scale-up from zero, with the template node built from the flavor.
- Scale-down started after the workload was deleted at 10:52:54.
  - At 10:52:29, while the pods were still on the node, the node had been marked unremovable. The autoscaler re-checks such a node only after 5 minutes.
  - At 10:58:46 it removed the empty node.
  - At 10:59:18 the group was empty. The ECS record shows the instance DELETED at 10:59:14.

### The autoscaler's cloud config

- The key `region` is rejected: "can't store data at section Global, variable region".
- After that error the process panics with a nil-pointer error. It does not exit with a clear message.
- These keys work: `project-id`, `access-key`, `secret-key`, `ecs-endpoint`, `as-endpoint`.

### Lab capacity

- The project's VPC quota is 5 and its EIP quota is 10. Both were full from 09:55 to 10:42 (EIP) and 10:46 (VPC), because other lab tests held them.
- 117 apply attempts were made.
- Raw refusals:
  - VPC: 409 `VPC.0114`, OverQuota `router`;
  - EIP: 409 `VPC.0521`, OverQuota `floatingip`.
- The provider shows both only as "conflict in the request".

## Decision

E1, E2 and E3 are `yes`, so the rule offers `autoscaled` on this cloud, with the conformance test. Two limits on what was measured:

- E3 ran as 0 to 1 to 0, not 1 to 2 to 1, because of the two-VM cap. Scale-up from zero is the harder path, since the template comes from the flavor and not from a live node. The API calls are the same.
- E2 ran with the provider-id argument added. The module's boot data without it was not booted. The autoscaler source shows that, without it, nodes cannot be matched to the group.

## What follows for the plan

1. The `autoscaled` mode may be proposed for this cloud. No task of the plan builds it yet. The conformance test must prove 1 to 2 to 1 on a real pool, which needs three VMs at once.
2. The pool unit module must:
   - add the `autoscaling` endpoint to the provider block;
   - add `--kubelet-arg=provider-id=<instance uuid from metadata>` to the worker boot data;
   - write min and max on the AS group;
   - give each group a name that is unique in the project;
   - put taints in the group tags.
3. The autoscaler's cloud config must not contain `region`. It holds the account key, so the secrets area delivers it as a mounted file.
4. Scale-down takes about 6 minutes with the tuned flags, because of the 5-minute unremovable re-check. With the defaults, expect more than 10 minutes.
5. Labs that need their own network compete for 5 VPC slots and 10 EIPs. Book the slots first.

## Evidence

- Lab session 2026-11-14, 09:51 to 11:02 UTC. OpenTofu 1.12.0, huaweicloud provider 1.91.0, cluster-autoscaler v1.31.5, k3s v1.31.8+k3s1.
- AS configuration create under 1 s and delete about 1 s. Group change 1 s, group delete 10 s.
- Autoscaler: plan `0->1 (max: 1)` at 10:49:21. Instance INSERVICE at 10:50:15. Node Ready with providerID equal to the instance id at 10:52:35. "removing empty node" at 10:58:46. Group empty at 10:59:18.
- Cloud config: the `region` key was rejected, followed by a nil-pointer panic.
- Quota refusals: 409 `VPC.0114` and 409 `VPC.0521`, 117 attempts from 09:55 to 10:46 UTC.
- Deletion checked by read calls: no VPC, EIP, security group, NAT gateway, server, group or configuration with the test prefix. 404 `AS.0007` for the group, 404 for the key pair, and the group's instance shows DELETED.

## Resources of the run

- vpc nslab-sov-750-vpc: deleted
- subnet nslab-sov-750-subnet: deleted
- security group nslab-sov-750-sg with its 4 ingress rules: deleted
- elastic IP with bandwidth nslab-sov-750-nat-bw: deleted
- nat gateway nslab-sov-750-nat: deleted
- snat rule on nslab-sov-750-nat: deleted
- dnat rules (ssh, api) on nslab-sov-750-nat: deleted
- kps keypair nslab-sov-750-key: deleted
- ecs instance nslab-sov-750-server (k3s test cluster): deleted
- as configuration nslab-sov-750-cfg-module: deleted
- as configuration nslab-sov-750-cfg-pid: deleted
- as group nslab-sov-750-group: deleted
- ecs instance nslab-sov-750-cfg-pid-<suffix> launched by the group: deleted (removed by the autoscaler's scale-down, record shows DELETED)

## What later tasks must take into account

(1) All three rows are yes. The autoscaled mode may be proposed for this cloud, but E3 was measured as 0 to 1 to 0 because of the two-VM lab cap. The conformance test of the mode must show 1 to 2 to 1 on a real pool, and the lab cap must be three VMs for that run.
(2) The worker boot data must set the kubelet provider-id to the instance uuid from the metadata service. With it, k3s kept the bare instance id and the autoscaler matched the node. Without it, nodes keep k3s://<name> and cannot be matched. That case was not booted in this run; it comes from the autoscaler source.
(3) The autoscaler's huaweicloud cloud config rejects the key 'region' and then panics with a nil pointer. Use only project-id, access-key, secret-key, ecs-endpoint and as-endpoint. The file holds the account key, so deliver it as a mounted secret.
(4) The provider needs the endpoint key 'autoscaling'. The module's provider block (`infra/providers/huawei/versions.tf`) does not have it yet.
(5) Timings on this installation: instance in service 54 s after the scale decision, node Ready about 3 min 14 s after it, scale-down about 6 min with a 1 min unneeded time (it waits for the 5-minute unremovable re-check).
(6) Lab capacity limits parallel network labs: VPC quota 5, EIP quota 10. Both stayed full for about 50 minutes of this run. The provider hides the codes, so a refused create shows only as 'conflict in the request'. The raw codes are 409 VPC.0114 (router) and 409 VPC.0521 (floatingip). Book the slots before starting such a lab.
(7) The registry mirror file in the worker boot data points at the jump host's mirrors and needs a registry credential. This lab removed that file and the image warm-up. A pool that autoscales in production needs its own mirror path.

The session took about 71 minutes of work. The working folder was deleted and nothing was made under /tmp.
