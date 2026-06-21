# hw177 worker-node wedge — forensic (DEBUG-BEFORE-WIPE)

**Deployment:** `068f217746ca7779` (`hw177.omani.works`)
**Provisioned on:** `main@116b616b0` (chart `1.0.2`, the durable #4031 Huawei-EVS storage fix)
**Cycle:** cycle-2c
**Status at debug time:** `phase1-watching`, 0 HelmReleases after ~90 min — WEDGED.

## Confirmed wedge state (live, via mothership catalyst-api pod kubeconfig)

Region-a cluster nodes:

```
NAME                                                           STATUS     ROLES                       AGE   VERSION
...-me-east-215-a-cp1-dea375                                   Ready      control-plane,etcd,master   90m   v1.31.4+k3s1
...-me-east-215-a-w7623fd                                      NotReady   <none>                      90m   v1.31.4+k3s1
...-me-east-215-a-w7fa9c6                                      NotReady   <none>                      90m   v1.31.4+k3s1
...-me-east-215-a-wbf1fc7                                      NotReady   <none>                      87m   v1.31.4+k3s1
...-me-east-215-a-wf3eec7                                      NotReady   <none>                      90m   v1.31.4+k3s1
```

Only `cp1` is Ready. All 4 region-a **worker** kubelets died.

Worker node conditions (w7623fd, representative of all 4):

```
MemoryPressure  Unknown  lastHeartbeat=03:21:51  lastTransition=03:23:47  NodeStatusUnknown  "Kubelet stopped posting node status."
DiskPressure    Unknown  ...                                              NodeStatusUnknown  "Kubelet stopped posting node status."
PIDPressure     Unknown  ...                                              NodeStatusUnknown  "Kubelet stopped posting node status."
Ready           Unknown  ...                                              NodeStatusUnknown  "Kubelet stopped posting node status."
```

The workers registered (joined k3s ~02:49), posted node status until **03:21:51**,
then their kubelets went silent and the control-plane flipped them to `Unknown`
at **03:23:47** (the standard `node-monitor-grace-period`). So the kubelets ran
for ~30+ minutes post-join and then stopped — they did NOT fail to join.

**No pressure transition** preceded the silence — the conditions jumped straight
from healthy to `Unknown` (NodeStatusUnknown), not through `MemoryPressure=True`
or `DiskPressure=True`. So this is **not** a kubelet-detected OOM/disk-full
eviction; it's the kubelet *process itself* (or the VM/network under it) dying.

## Downstream chain (all secondary, not root cause)

1. `coredns-7d45485988-wtfjv` → `Pending` (0/1) — cannot schedule: cp1 is tainted
   for control-plane and there are 0 Ready workers.
2. CoreDNS Service `10.96.0.10:53` has **no endpoints** → Cilium returns
   `connect: operation not permitted` on every cluster DNS lookup.
3. Flux `source-controller` → `lookup github.com on 10.96.0.10:53: operation not
   permitted` → GitRepository `openova` `READY=False` → **0 HelmReleases** in
   both regions.
4. One cilium agent (`cilium-prqsk`) is stuck `0/1` — that's a dead worker's
   agent. The flux + cilium-operator + metrics-server pods that ARE Running
   (86–90m) landed on cp1 before the workers went silent.

## Cloud-init log

`GET /sovereign/api/v1/deployments/068f217746ca7779/cloudinit-log` → **HTTP 404**
`{"error":"no-cloudinit-log","detail":"no cloud-init log has been uploaded for
this deployment yet"}`.

The cloud-init log push is itself kubelet/network-dependent; because the worker
kubelets died, the push never completed. The control-plane node's cloud-init
ran fine (cp1 is Ready, k3s server up, etcd healthy, Cilium/Flux/metrics-server
scheduled), so this is **not** a cloud-init / bootstrap-script regression — it is
an **infra-level worker-VM/kubelet stability flake** specific to the 4 region-a
worker VMs of this provisioning.

## Verdict

**INFRA FLAKE — not a `main` code regression.** hw174 and hw176 brought their
workers up on the *same* `main@116b616b0` and reached 37–67 HRs. The chart 1.0.2
storage fix is unrelated to kubelet liveness. Treat as a transient kom4dc
worker-VM flake → **wipe + re-fire (cycle-2d)**.

**Escalation guard:** if cycle-2d ALSO loses its workers the same way (kubelets
post for ~30 min then go silent with no pressure transition), that is
**SYSTEMATIC kom4dc worker-VM instability** — STOP, do not re-fire a 3rd time,
escalate with this evidence.
