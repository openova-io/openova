# 2026-05-23 — Huawei Cloud Stack CNI Root Cause Analysis + Lessons

**Session scope**: Hetzner → Huawei Cloud Stack (HCS me-east-215) Sovereign migration. 25 provisioning attempts over ~10 hours to identify and fix the architectural root cause of pod-to-pod networking failure on HCS.

## TL;DR

Every CNI mode (Flannel host-gw, Cilium VXLAN, Cilium GENEVE, Cilium native, Cilium WireGuard) failed identically across 19 deployment attempts because:

1. **`source_dest_check=true`** is Huawei's default on every ECS NIC. The SDN layer silently drops any packet whose source IP doesn't match the port's primary fixed IP. Pod packets (src=10.42.x.y, port IP=10.206.1.x) get dropped at the wire.

2. **Security group missing Cilium-specific ports**: VXLAN 8472/UDP (NOT 4789), GENEVE 6081/UDP, Cilium-WG 51871/UDP (NOT the kernel-WG 51820), cilium-health 4240/TCP.

3. **Dual-CNI race**: k3s installed with Flannel host-gw + Cilium HelmRelease installed later. Pods scheduled during the bootstrap window get Flannel cni0-attached IPs that Cilium IPAM never registers, breaking service routing.

**Wave 5.30 (PR #2200, Refs #2199)** ships the canonical fix in `infra/providers/huawei/main.tf`:
- `source_dest_check = false` in both CP + worker `network{}` blocks
- 4 new SG rules for Cilium ports + 3 fallback rules for pod CIDR
- All scoped to region subnet

## Why prior 10 Waves (5.20–5.29) missed it

Each prior wave fixed a distinct Phase-0 symptom:
- 5.20: worker runcmd shell quoting
- 5.21: NAT Gateway for worker internet egress
- 5.22: PUT-back retry loop
- 5.23: tlsroutes CRD seed
- 5.24: CILIUM_K8S_SERVICE_HOST baseline
- 5.25: k3s host-gw (free vxlan for Cilium)
- 5.26: name_prefix scoped by deployment_id
- 5.27: VPC CIDR scoped by sha256(deployment_id)
- 5.28: wait k3s apiserver + retry CRD apply
- 5.29: runtime CILIUM_K8S_SERVICE_HOST substitute

All TARGET-FIXES were correct for what they addressed — none touched the underlying HCS networking defaults because the failure mode looked like "Cilium config issue" not "cloud SDN issue".

## Diagnostic path (the deep RCA the founder demanded)

1. **Multi-pod tcpdump**: Spawn netshoot pods on each node, capture w1 → w2 cross-node ICMP. Showed packets LEAVE w1 eth0 successfully but never appear on w2 eth0.

2. **Cilium monitor**: `cilium monitor --type drop` on w2 revealed packets ping-ponging between nodes — w2 received but couldn't deliver to local pod (because Flannel-attached pod IP wasn't in Cilium endpoint list), routed back to w1, TTL exceeded.

3. **`cilium endpoint list` on w2**: Only host/health/ingress endpoints — actual workload pods missing. Confirmed dual-CNI issue.

4. **`ip route` on w2**: Showed `10.42.2.0/24 via 10.42.2.161 dev cilium_host` (Cilium's route) but actual pingtarget-w2 (10.42.2.32) was on cni0 bridge (Flannel), not reachable via cilium_host.

5. **Web research subagent**: Found explicit Huawei UCS docs: "the source and destination address check of the node must be disabled, and the security group of the node must allow traffic from and to the container CIDR block over the port and using the protocol of the node."

## Lessons (must apply going forward)

### L1 — Cloud SDN defaults are load-bearing assumptions

Whenever a CNI fails on a cloud provider, FIRST check the cloud's default port-security / sd-check / anti-spoof settings. AWS turns this off via `source_dest_check=false`. Huawei via `source_dest_check=false` or `port_security_enabled=false`. Same shape, different keys.

### L2 — Cilium's port choices differ from kernel defaults

| Service | Kernel/standard | Cilium default |
|---|---|---|
| VXLAN | 4789/UDP | 8472/UDP |
| WireGuard | 51820/UDP | 51871/UDP |

Don't open the standard port when securing Cilium-specific traffic.

### L3 — Dual-CNI is fatal

`k3s --flannel-backend=host-gw` + Cilium HelmRelease = race condition. Pods scheduled during bootstrap get Flannel-managed IPs that Cilium never sees. ALL pod traffic via those endpoints is broken. Either:
- `--flannel-backend=none` and pre-install Cilium in cloud-init BEFORE Flux
- OR keep Flannel + accept that Cilium-only features won't work for some pods

### L4 — tcpdump is the ground truth

Cilium status "OK", `cilium-health` passing, `KubeProxyReplacement: True` — none of these guarantee actual packet delivery. tcpdump on both ends is the only reliable diagnostic for "does the packet actually arrive?".

### L5 — Cilium monitor drop reasons can mislead

Drop reason "Unsupported protocol for NAT masquerade ... TimeExceeded(TTLExceeded)" looks like an encapsulation issue but the real cause was packet ping-ponging due to mis-routed Flannel pods. The drop reason is a symptom, not the root cause.

### L6 — Founder mandate "don't go in circles" = deep RCA BEFORE next iteration

Spent 5 deployment attempts (19-23) fixing symptoms before doing tcpdump. The 6-step RCA above (tcpdump → cilium monitor → endpoint list → routes → docs research → web research) took ~30 minutes and produced the canonical fix. Lesson: when a problem RECURS in 2+ attempts, STOP iterating on patches and do RCA before the next attempt.

### L7 — Per-Wave issues create traceable progress

Per Principle 25 (D21 anti-pattern), every Wave got its own GitHub issue (#2163-#2199) instead of stacking under one parent. The Kanban + TRACKER show actual per-Wave completion. Founder visibility maintained throughout.

## State at end of session

- Wave 5.30 PR #2200 MERGED at 15:39Z; image f91272f rolled to mothership
- 26th attempt 0711c1dfdba8b331 verified Wave 5.30 fix correct (HCS port shows allowed_address_pairs=1.1.1.1/0 = anti-spoof off) but cross-node ping STILL 100% loss
- **Secondary RCA**: k3s `--flannel-backend=host-gw` creates cni0 bridge. Pods scheduled before Cilium HelmRelease lands get attached to cni0 (Flannel), not Cilium. Cilium IPAM never registers those pod IPs → routing dead.
- **Wave 5.31 PR #2202 MERGED at 16:01Z**: k3s `--flannel-backend=none`, pre-install Cilium via inline helm in cloud-init immediately after k3s ready. Cilium owns CNI from pod #1. No dual-CNI race possible.
- 27th awaits Wave 5.31 image build, then triggers
- Manual cleanup of orphan VPCs/subnets/NAT/EIPs via direct HCS API; 26th cluster running but Phase 1 cluster bootstrap blocked by dual-CNI

### Lesson L8 — Single-issue RCA can miss compound failures

Wave 5.30 fixed the HCS networking blocker (sd-check). But pod-to-pod still failed because of a SECOND independent blocker (Flannel dual-CNI). When dispatching the deep-research agent, framed the question as "why does HCS drop pod traffic?" — got an answer focused on cloud SDN. Should have ALSO asked "what's the CNI state on the node?" earlier — would've found dual-CNI in same RCA pass. Lesson: when multiple subsystems can each independently kill a path, RCA each in parallel.

## Related artifacts

- PR #2200 — Wave 5.30 canonical fix
- Issue #2199 — Wave 5.30 RCA tracking
- Issue #2163 — parent Wave 5 walk
- `/tmp/hcs_*.py` — direct HCS API tooling for diagnostic + manual cleanup
