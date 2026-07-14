# `infra/hetzner/` — Catalyst Sovereign provisioning module

Canonical Phase 0 OpenTofu module that provisions a single-region OR multi-region Catalyst Sovereign on Hetzner Cloud and bootstraps it onto Flux-driven GitOps. After `tofu apply` finishes, every subsequent change to the Sovereign goes through Crossplane (cloud resources) and Flux (Kubernetes resources). OpenTofu state is archived and never touched again.

This module is the implementation of [`docs/SOVEREIGN-PROVISIONING.md`](../../docs/SOVEREIGN-PROVISIONING.md) §3 (Phase 0 — Bootstrap) and follows [`docs/PRINCIPLES.md`](../../docs/PRINCIPLES.md) — every value the wizard or operator picks is a variable; nothing is hardcoded.

---

## What this module creates

| Resource | Purpose |
|---|---|
| `hcloud_network.region[*]` | **One private 10.0.0.0/16 PER REGION.** Each region gets its own isolated Network — never shared across regions. See [Network](#network) below. |
| `hcloud_network_subnet.region[*]` | One 10.0.1.0/24 subnet inside each region's Network. Uniform layout: CP at .2, workers at .10+, LB anchor at .254. |
| `hcloud_firewall.main` | Inbound rules for 80/443 (HTTPS), 6443 (k3s API), 53 (DNS), ICMP, **UDP 51871 (Cilium WireGuard inter-region encryption)**, and an opt-in SSH rule keyed to operator CIDRs. |
| `hcloud_ssh_key.main` | The operator's existing SSH key (from their Hetzner project) — never auto-generated. |
| `hcloud_server.control_plane[*]` (primary) | 1 node by default (`ha_enabled=false`); 3 nodes when HA is on. Cloud-init installs k3s + Flux + the bootstrap kit pointer. Attaches to `hcloud_network.region["primary"]`. |
| `hcloud_server.worker[*]` (primary) | `worker_count` nodes (default **2** — issue #733 multi-node Sovereign). Set to 0 explicitly for solo dev/POC. |
| `hcloud_server.secondary_control_plane[*]` | One per secondary region, single-CP. Attaches to its own region's `hcloud_network.region[<key>]`. |
| `hcloud_server.secondary_worker[*]` | Per-region worker fleet, sized by `regions[i].workerCount`. |
| `hcloud_load_balancer.main` (`lb11`) | Public IPv4 in the primary region; forwards 80→80, 443→443 (to the Cilium Gateway Service type=LoadBalancer — hostNetwork off, no NodePort; #4682) and 53→53 (powerdns-anycast Service type=LoadBalancer, `allocateLoadBalancerNodePorts: false` — §854 / #4765: NodePorts are ABSOLUTELY FORBIDDEN). ⚠️ The `53→53` forward is §854-clean but **INERT on Hetzner until [#5086](https://github.com/openova-io/openova/issues/5086)** — the anycast Service takes its VIP from Cilium LB-IPAM (gated off on Hetzner) and hcloud-ccm cannot materialise it (no nodePort/location annotation), so node:53 does not bind. Not a regression: node:30053 was equally dead since the bp-powerdns 1.2.18 §854 flip. |
| `hcloud_load_balancer.secondary[*]` | One `lb11` per secondary region. PowerDNS lua-records aggregate every LB IP behind the Sovereign FQDN with `ifurlup` health probes. |

After Phase 0, the cluster's Flux pulls `clusters/<sovereign_fqdn>/` from the public OpenOva monorepo and installs the 11-component bootstrap kit (Cilium → cert-manager → Crossplane → ESO → SPIRE → NATS → OpenBao → Keycloak → Gitea → catalyst-platform). Hetzner adoption by Crossplane happens once `provider-hcloud` is up.

---

## Network

**Per [`docs/DOD.md`](../../docs/DOD.md) A2 (founder ruling 2026-05-15): every region has its OWN `hcloud_network`. Provider private networks NEVER span regions — inter-region traffic flows exclusively over Cilium WireGuard (UDP 51871) on each region's public IP through the DMZ vCluster.**

This is the same rule whether the secondary region is Hetzner, AWS, or Huawei (DoD A6). The Hetzner module is provider-mix-friendly: a sister-provider module owns its own regions, and the inter-region link sits ABOVE the provider layer.

### Per-region addressing (uniform across regions)

Every region's Network carries an identical `/16` and `/24`:

| Address | Role |
|---|---|
| `10.0.0.0/16` | Region's private Network (one per region; ranges are identical inside isolated Networks). |
| `10.0.1.0/24` | Region's subnet (only subnet inside that Network). |
| `10.0.1.2` | Control plane (every region's CP — primary AND secondary). |
| `10.0.1.10` .. `10.0.1.<10+worker_count-1>` | Workers in that region. |
| `10.0.1.254` | LB anchor (pinned via `hcloud_load_balancer_network.ip`). |

### Phase-2 topology (DMZ-WG over public IPs)

```
                ┌────────────── DMZ WireGuard mesh (UDP 51871, public IPs) ──────────────┐
                │                                                                         │
   ┌────────────┴────────────┐    ┌─────────────────────────┐    ┌──────────────────────┐
   │ Region: primary (nbg1)  │    │ Region: fsn1-1 (fsn1)   │    │ Region: hel1-2 (hel1)│
   │  ┌───────────────────┐  │    │  ┌───────────────────┐  │    │  ┌─────────────────┐ │
   │  │ hcloud_network    │  │    │  │ hcloud_network    │  │    │  │ hcloud_network  │ │
   │  │ 10.0.0.0/16       │  │    │  │ 10.0.0.0/16       │  │    │  │ 10.0.0.0/16     │ │
   │  │  └ subnet 10.0.1.0/24 │ │  │  │  └ subnet 10.0.1.0/24 │ │ │  │  └ subnet 10.0.1.0/24 │ │
   │  └───────────────────┘  │    │  └───────────────────┘  │    │  └─────────────────┘ │
   │   CP @ 10.0.1.2         │    │   CP @ 10.0.1.2         │    │   CP @ 10.0.1.2      │
   │   Workers @ 10.0.1.10+  │    │   Workers @ 10.0.1.10+  │    │   Workers @ 10.0.1.10+│
   │   LB  @ 10.0.1.254      │    │   LB  @ 10.0.1.254      │    │   LB  @ 10.0.1.254   │
   │                         │    │                         │    │                      │
   │  k3s cluster-cidr:      │    │  k3s cluster-cidr:      │    │  k3s cluster-cidr:   │
   │   10.42.0.0/16          │    │   10.43.0.0/16          │    │   10.44.0.0/16       │
   │  k3s service-cidr:      │    │  k3s service-cidr:      │    │  k3s service-cidr:   │
   │   10.96.0.0/16          │    │   10.97.0.0/16          │    │   10.98.0.0/16       │
   │                         │    │                         │    │                      │
   │     ┌───────────────┐   │    │     ┌───────────────┐   │    │     ┌─────────────┐  │
   │     │ DMZ vCluster  │◀──┼────┼────▶│ DMZ vCluster  │◀──┼────┼────▶│ DMZ vCluster│  │
   │     └───────────────┘   │    │     └───────────────┘   │    │     └─────────────┘  │
   └─────────────────────────┘    └─────────────────────────┘    └──────────────────────┘
       │                              │                              │
       └─ Cilium ClusterMesh apiserver (Service type = LoadBalancer) ─┘
              public IPs only, never NodePort (DoD A3+A5)
```

The arrows between DMZ vClusters are WireGuard tunnels carried over each region's public IPv4 + UDP 51871. The provider's internal cross-region routing fabric is **never** in the path — even when all three regions are Hetzner.

### Per-region k3s CIDRs (DoD gate D11)

Cilium ClusterMesh demands non-overlapping pod and service CIDRs across peer clusters. The module allocates them deterministically from the regional index in `local.all_region_keys`:

| Region index | Region key | cluster-cidr (pods) | service-cidr |
|---|---|---|---|
| 0 | `primary` | `10.42.0.0/16` | `10.96.0.0/16` |
| 1 | `<region>-1` (first secondary) | `10.43.0.0/16` | `10.97.0.0/16` |
| 2 | `<region>-2` (second secondary) | `10.44.0.0/16` | `10.98.0.0/16` |
| 3 | `<region>-3` | `10.45.0.0/16` | `10.99.0.0/16` |
| ... up to 16 regions | ... | `10.42+i.0/16` | `10.96+i.0/16` |

The flags `--cluster-cidr` and `--service-cidr` are threaded into the k3s install line in `cloudinit-control-plane.tftpl` via the `${cluster_cidr}` and `${service_cidr}` template substitutes. The catalyst-api passes nothing region-related at runtime — the allocation is pure tofu.

---

## Multi-region wiring (slice G1, EPIC-0 #1095 → DMZ-WG refactor 2026-05-15)

The module accepts a `var.regions[]` list-of-objects payload that captures the wizard's per-region sizing. Slice G1 wires every entry in that list end-to-end; the 2026-05-15 DMZ-WG refactor replaced the shared-network model with one Network per region.

| `var.regions[i]` | Realised by | Notes |
|---|---|---|
| `regions[0]` | Legacy singular path (`hcloud_server.control_plane[0]`, `hcloud_load_balancer.main`, attached to `hcloud_network.region["primary"]`, …) | The catalyst-api provisioner mirrors `regions[0]` into `var.region` / `var.control_plane_size` / `var.worker_size` / `var.worker_count` before `tofu apply`. |
| `regions[1+]` | Multi-region overlay (`hcloud_server.secondary_control_plane["fsn1-1"]`, `hcloud_load_balancer.secondary["hel1-2"]`, …) | New resources keyed by `for_each = local.secondary_regions`. Each secondary region has its OWN `hcloud_network.region[<key>]`, its OWN `/24` subnet, and its OWN `lb11`. The shared resources across the Sovereign are `hcloud_firewall.main` + `hcloud_ssh_key.main` only. |

**Network address impact:** the legacy `hcloud_network.main` + `hcloud_network_subnet.main` singletons (and the slice-G1 `hcloud_network_subnet.secondary` overlay) were removed in this refactor. Every region — primary and secondary — is keyed under `hcloud_network.region[<key>]` / `hcloud_network_subnet.region[<key>]`. Per the DoD cycle protocol (every wipe-and-create is a fresh provision), legacy state migrates by destroy-and-recreate, **NOT** by `tofu state mv` — this is intentional.

### EPIC-6 (#1101) example: 3-region Continuum DR shape

Per [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) §3.8 + §11, the EPIC-6 demo brings up one mgmt cluster + two data-plane clusters with Cilium ClusterMesh between them. Slice G1 provisions the cloud substrate; slice G3 wires ClusterMesh.

```jsonc
{
  "sovereign_fqdn":     "demo.example.com",
  "org_name":           "Demo Org",
  "org_email":          "ops@example.com",
  "hcloud_token":       "<rotate>",
  "hcloud_project_id":  "12345",
  "ssh_public_key":     "ssh-ed25519 AAAA... operator@laptop",

  // Legacy singular fields — derived from regions[0] by the catalyst-api
  // provisioner before tofu apply. No need to set these by hand when
  // regions[] is supplied; they're shown here for reference.
  "region":             "nbg1",
  "control_plane_size": "cpx32",
  "worker_size":        "cpx32",
  "worker_count":       1,

  // Per-region payload — slice G1 wires every entry in this list.
  // regions[0] = mgmt (Nuremberg), regions[1] = fsn data plane (Falkenstein),
  // regions[2] = hel data plane (Helsinki) per the EPIC-6 §3.8 cluster table.
  "regions": [
    { "provider": "hetzner", "cloudRegion": "nbg1", "controlPlaneSize": "cpx32", "workerSize": "cpx32", "workerCount": 1 },
    { "provider": "hetzner", "cloudRegion": "fsn1", "controlPlaneSize": "cpx32", "workerSize": "cpx32", "workerCount": 2 },
    { "provider": "hetzner", "cloudRegion": "hel1", "controlPlaneSize": "cpx32", "workerSize": "cpx32", "workerCount": 2 }
  ],

  "k3s_version":        "v1.31.4+k3s1",
  "object_storage_region":      "nbg1",
  "object_storage_access_key":  "<from Hetzner Console>",
  "object_storage_secret_key":  "<from Hetzner Console>",
  "object_storage_bucket_name": "catalyst-demo-example-com",
  "domain_mode":        "byo"
}
```

Outputs after `tofu apply`:

| Output | Shape | EPIC-6 example value |
|---|---|---|
| `control_plane_ip` | string | `203.0.113.10` (mgmt CP, Nuremberg) |
| `load_balancer_ip` | string | `203.0.113.11` (mgmt LB, Nuremberg) |
| `secondary_region_keys` | `list(string)` | `["fsn1-1", "hel1-2"]` |
| `control_plane_ips_by_region` | `map(string)` | `{"fsn1-1": "203.0.113.20", "hel1-2": "203.0.113.30"}` |
| `load_balancer_ips_by_region` | `map(string)` | `{"fsn1-1": "203.0.113.21", "hel1-2": "203.0.113.31"}` |

The catalyst-api joins `secondary_region_keys` with `Request.Regions[1+]` to project per-region status into the deployment record. PowerDNS lua-records (`docs/MULTI-REGION-DNS.md`) aggregate every LB IP in `load_balancer_ips_by_region` into the Sovereign FQDN's A-record set so a single hostname spans every region with `ifurlup` health checking.

### Out of scope for slice G1

- **Cilium ClusterMesh wiring** — slice G3 (joins separate clusters into a single mesh).
- **Per-cluster GitOps differentiation** — every secondary CP today renders an identical Flux Kustomization pointed at `clusters/<sovereign_fqdn>/`. Per-cluster paths (`clusters/hz-fsn-rtz-prod/`, etc., per [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) §4.1) ship in slice G3 alongside ClusterMesh.
- **Non-Hetzner regions** — `var.regions[]` may carry `oci` / `aws` / `huawei` / `azure` entries; the Hetzner overlay filters them out (`if r.provider == "hetzner"`). Sister-provider modules (slice G2 / G4 / …) own their own iteration.

### Resource address contract

The 2026-05-15 DMZ-WG refactor introduced one Network per region (replacing the singleton `hcloud_network.main` and the slice-G1 secondary overlay) — pre-2026-05-15 Sovereigns will recreate the network on the next apply per the DoD cycle protocol.

Per-region resources (keyed by `for_each` on `toset(local.all_region_keys)`; the primary key is the literal string `"primary"`, secondary keys follow the slice-G1 `{cloudRegion}-{index}` shape):

```
hcloud_network.region["primary"]
hcloud_network.region["{cloudRegion}-{index}"]
hcloud_network_subnet.region["primary"]
hcloud_network_subnet.region["{cloudRegion}-{index}"]
```

Primary-region resources (legacy singular path, regions[0]):

```
hcloud_server.control_plane[0..2]              # count = 1 or 3 (HA)
hcloud_server.worker[0..N-1]                   # N = var.worker_count
hcloud_load_balancer.main
hcloud_load_balancer_network.main
hcloud_load_balancer_target.control_plane[0..2]
hcloud_load_balancer_target.workers[0..N-1]
hcloud_load_balancer_service.{http,https,dns}
```

Secondary-region resources (slice-G1 overlay, regions[1+], all keyed `for_each = local.secondary_regions`):

```
hcloud_server.secondary_control_plane["{key}"]
hcloud_server.secondary_worker["{key}-w{i}"]   # i = 1..workerCount
hcloud_load_balancer.secondary["{key}"]
hcloud_load_balancer_network.secondary["{key}"]
hcloud_load_balancer_target.secondary_control_plane["{key}"]
hcloud_load_balancer_target.secondary_workers["{key}-w{i}"]
hcloud_load_balancer_service.secondary_http["{key}"]
hcloud_load_balancer_service.secondary_https["{key}"]
hcloud_load_balancer_service.secondary_dns["{key}"]
```

### Tests

Module-local tests live under `tests/multi_region.tftest.hcl`. They exercise five scenarios offline (no real Hetzner) via `mock_provider` + `override_resource`:

```bash
cd infra/hetzner
tofu init -backend=false
tofu validate
tofu fmt -check
tofu test
```

CI runs these on every PR touching `infra/hetzner/**` via `.github/workflows/infra-hetzner-tofu.yaml`.

---

## Why `cpx21` / `cpx31` are NOT the default (issue #752)

Both `cpx21` (3 vCPU / 4 GB / €10.99/mo) and `cpx31` (4 vCPU / 8 GB / €20.49/mo) appear cheaper than the chosen `cpx22` / `cpx32` defaults and are LISTED in Hetzner's `GET /v1/server_types` response with full EU pricing (`fsn1`, `nbg1`, `hel1`). They are NOT orderable.

```bash
$ HCLOUD_TOKEN=...
$ for SKU in cpx21 cpx31; do for LOC in fsn1 nbg1 hel1; do
    curl -sH "Authorization: Bearer $HCLOUD_TOKEN" -X POST \
      "https://api.hetzner.cloud/v1/servers" \
      -H "Content-Type: application/json" \
      -d "{\"name\":\"probe-$SKU-$LOC\",\"server_type\":\"$SKU\",\"image\":\"ubuntu-24.04\",\"location\":\"$LOC\",\"start_after_create\":false}" \
      | jq -r '.error.message // "ORDERED"'
  done; done
unsupported location for server type   # cpx21/fsn1
unsupported location for server type   # cpx21/nbg1
unsupported location for server type   # cpx21/hel1
unsupported location for server type   # cpx31/fsn1
unsupported location for server type   # cpx31/nbg1
unsupported location for server type   # cpx31/hel1
```

`cpx22` and `cpx32` return `ORDERED` (verified 2026-05-04 against a real project — server IDs cleaned up immediately after provisioning).

The `/v1/server_types` price entry is misleading: Hetzner advertises a price for every (SKU, location) pair regardless of whether new orders are accepted. The authoritative source for "can I order this?" is `POST /v1/servers` itself. The cpx (no-letter) generation is being phased out in favour of the cpx22/cpx32/cpx52 generation across EU DCs; `cpx11`/`cpx21`/`cpx31`/`cpx41` are NOT orderable in `fsn1`/`nbg1`/`hel1` as of 2026-05-04.

PR #741 attempted a default of `cpx21` CP + `cpx31` workers based on the listed prices and got blocked at `tofu apply` time with the same "unsupported location" error. PR #744 reverted to the orderable `cpx22` + `cpx32`. Issue #752 documented the gap between the listed prices and the orderability constraint; this section is the durable record so future engineers don't re-attempt.

If Hetzner ever opens cpx21/cpx31 ordering in EU DCs (re-probe with the script above), the saving is ~€4/mo per Sovereign on CP + ~€11/mo per Sovereign per worker. Until then, `cpx22`/`cpx32` is the floor.

---

## Sizing rationale — why `cpx32 × 3` is the default (issue #733)

`docs/ARCHITECTURE.md` §7.1 sets the RAM budget for a Catalyst-only mgt cluster at **~11.3 GB**, and §7.4 adds **~8.8 GB** for per-host-cluster infrastructure that runs on every host cluster including mgt (Cilium, Flux, Crossplane, cert-manager, ESO, Kyverno, Trivy Operator, Falco, Harbor, SeaweedFS, Velero, plus small operators).

The total Sovereign footprint is **~20 GB RAM, ~10 vCPU minimum**. There are two ways to land that:

1. **Vertical scale** — single CPX52 node (12 vCPU / 24 GB) hosts everything.
2. **Horizontal scale (default)** — 1× CPX32 control plane + 2× CPX32 workers (3 nodes × 4 vCPU / 8 GB = 12 vCPU / 24 GB total). Same aggregate footprint, **multi-node fault tolerance**, real horizontal scale for workloads with `replicas: 2`.

The horizontal-scale shape is the canonical Catalyst architecture — `clusters/_template/` was designed for it. The previous single-node default was a regression that discarded horizontal scalability; this module restores the multi-node default per issue #733.

| Hetzner type | RAM | vCPU | Disk | Default role |
|---|---|---|---|---|
| `cx22` | 4 GB | 2 | 40 GB | Insufficient — OOM during Cilium install. |
| `cx32` | 8 GB | 4 | 80 GB | Too small for a solo Sovereign on its own. |
| `cpx32` | 8 GB | 4 (AMD) | 160 GB | **Default control plane AND default worker.** Multi-node — pair with `worker_count ≥ 2` for the canonical 3-node topology (12 vCPU / 24 GB total). |
| `cpx42` | 16 GB | 8 (AMD) | 320 GB | Mid-tier worker for trimmed component sets. |
| `cpx52` | 24 GB | 12 (AMD) | 480 GB | Solo dev/POC starter when `worker_count=0` (single-node mode). |
| `cx42` | 16 GB | 8 | 160 GB | Legacy single-node default — still allowed, no longer default. |
| `cx52` | 32 GB | 16 | 320 GB | Heavy single-node Sovereign with many Blueprints. |
| `ccx33` | 32 GB | 8 dedicated | 240 GB | **Production** dedicated-vCPU control plane — avoids noisy-neighbour latency on the API server. |
| `cax41` | 32 GB | 16 ARM | 320 GB | Cheapest path to 32 GB. Confirm all upstream Blueprint container images are multi-arch before using (most are; a handful aren't). |

### Upgrade path

Resizing is non-destructive on Hetzner — `tofu apply -var control_plane_size=ccx33` will trigger a `hcloud_server` resize. The node reboots once. On a single-node Sovereign that means ~60 seconds of console downtime; the LB health-check covers it. For HA Sovereigns (`ha_enabled=true`), the resize is rolling — no externally-visible downtime.

For a multi-node Sovereign, prefer **adding workers** (`worker_count`) before upsizing the control plane. The control plane's job is k3s + control-plane services; workers absorb the per-host-infra and application load.

---

## Firewall rules

The Phase-0 firewall is intentionally minimal. All long-term policy is enforced by Cilium NetworkPolicies (in-cluster) and tightened by Crossplane Compositions (cloud edge) once Phase 1 completes.

### Inbound (Phase-0 baseline)

| Port | Protocol | Source | Why |
|---|---|---|---|
| 80 | TCP | `0.0.0.0/0`, `::/0` | HTTP — for ACME HTTP-01 challenges and the cert-manager bootstrap. Cilium Gateway terminates. |
| 443 | TCP | `0.0.0.0/0`, `::/0` | HTTPS — the only port end-users reach. All Catalyst surfaces (`console`, `gitea`, `harbor`, `admin`, `api`) are served behind 443 via Cilium Gateway and SNI routing. |
| 6443 | TCP | `0.0.0.0/0`, `::/0` | k3s API server. Open to allow the wizard to fetch the kubeconfig and confirm the cluster is healthy. Crossplane Composition tightens this to operator-owned CIDRs in Phase 2. |
| 53 | TCP+UDP | `0.0.0.0/0`, `::/0` | Sovereign's PowerDNS authoritative server — open for LE DNS-01 challenges and subdomain NS delegation. |
| 51871 | UDP | `0.0.0.0/0`, `::/0` | **Cilium WireGuard inter-region encryption (DMZ-WG)**. Per DoD A2, inter-region pod-to-pod traffic flows exclusively over WireGuard on public IPs — never over the provider's internal network. Source is `0.0.0.0/0` because each region's CP/worker public IP rotates at provision time; Cilium's static-key crypto + node-discovery auth is the real security boundary, not the firewall source filter. |
| ICMP | ICMP | `0.0.0.0/0`, `::/0` | Diagnostics (Path MTU Discovery, traceroute). Open by default; closing it is a foot-gun that breaks PMTU. |
| 22 | TCP | `var.ssh_allowed_cidrs` (default: empty) | SSH break-glass. **Off by default** — the rule is omitted entirely when the list is empty. Operators add their own CIDRs at provisioning time or via a Crossplane Composition later. |

### Outbound (Hetzner default — open)

Hetzner's hcloud_firewall does not enforce egress unless you write explicit deny rules. We rely on the open-egress default plus in-cluster Cilium NetworkPolicies for fine-grained control. The egress flows the bootstrap requires:

| Destination | Why |
|---|---|
| `get.k3s.io`, `github.com/k3s-io/k3s/releases` | k3s installer + binary download. |
| `pool.ntp.org` (UDP 123) | Time sync — required for SPIRE workload identity (5-min SVID rotation). |
| `1.1.1.1`, `8.8.8.8` (UDP/TCP 53) | DNS until the Sovereign's own DNS lands. |
| `ghcr.io` (TCP 443) | Container images for Catalyst services + bootstrap kit (`bp-*` Blueprints). |
| `github.com/openova-io/openova` (TCP 443) | Flux GitRepository pull. |

### Deliberately blocked

| Port | Why blocked |
|---|---|
| 22 (SSH) | Default-closed at the firewall. Break-glass is via Hetzner Console (out-of-band, password-less) when no `ssh_allowed_cidrs` is set. Removing the world-open SSH attack surface is the largest single hardening win. |
| 10250 (kubelet) | Never exposed publicly. Cluster-internal only. |
| 2379/2380 (etcd) | Embedded in k3s; never exposed publicly. |
| 8472 (flannel VXLAN) | We disable flannel; Cilium uses geneve/wireguard within the cluster network. |

---

## k3s flags + rationale

k3s is installed via `curl get.k3s.io | sh -` from cloud-init. The `INSTALL_K3S_EXEC` argument carries the flag set required by the rest of the Catalyst stack. Each flag below maps to a specific architectural decision in `docs/ARCHITECTURE.md` §8.

| Flag | Why |
|---|---|
| `--cluster-init` | Initialise embedded etcd. Required for Phase-1 hand-off to add additional control-plane nodes (`ha_enabled=true`) without re-bootstrapping. |
| `--flannel-backend=none` | k3s ships with flannel; we replace the CNI with Cilium (gateway API, eBPF, mTLS via wireguard). Setting `none` keeps k3s from racing flannel against Cilium during boot. |
| `--disable=traefik` | k3s ships with Traefik; we use **Cilium Gateway API** (already part of the Cilium install). Catalyst's Gateway/HTTPRoute manifests assume Gateway API, not Traefik IngressRoute. |
| `--disable=servicelb` | k3s ships with klipper-lb; we use the Hetzner load balancer for ingress (`hcloud_load_balancer.main`) and PowerDNS lua-records (`ifurlup`) for cross-region failover. klipper-lb would otherwise race hcloud-ccm to materialise the Cilium Gateway's `type=LoadBalancer` Service (:443/:80; #4682). |
| `--disable=local-storage` | k3s ships local-path-provisioner; we use **hcloud-csi** (provisioned by Crossplane after Phase 1) so PVCs survive node deletion and can be migrated across regions via Velero. |
| `--disable-network-policy` | k3s ships kube-router NetworkPolicy; **Cilium** handles NetworkPolicy. Two NetworkPolicy controllers fight each other. |
| `--cluster-cidr=10.42+i.0/16` | Pod CIDR — non-overlapping across ClusterMesh peers (DoD gate D11). Index `i` is the region's position in `local.all_region_keys` (0 = primary). Allocated by `local.region_cluster_cidr` in `main.tf`. |
| `--service-cidr=10.96+i.0/16` | Service CIDR — non-overlapping across ClusterMesh peers. Same allocation rule as `--cluster-cidr`. |
| `--tls-san=<sovereign_fqdn>` | API server TLS cert must be valid for the public sovereign FQDN, otherwise the wizard's kubeconfig fetch and any operator running `kubectl --server=https://<fqdn>:6443` get a SAN mismatch. |
| `--node-label catalyst.openova.io/role=control-plane` | Used by NodeAffinity on Catalyst control-plane services (Console, projector, etc.) to pin them off worker nodes. |
| `--write-kubeconfig-mode=0644` | Lets the catalyst-api fetch the kubeconfig over the wizard channel without sudo. The kubeconfig is rotated and replaced with a SPIFFE-issued identity in Phase 2. |

The `INSTALL_K3S_VERSION` environment variable is `var.k3s_version` (default `v1.31.4+k3s1`). Pinned so a Sovereign provisioned today and one provisioned next month land on the same Kubernetes minor — the Catalyst compatibility matrix in `docs/ARCHITECTURE.md` §8.1 is keyed to k3s minor versions.

---

## SSH key management — why no auto-generated keys

The module **requires** the operator to provide their own SSH public key via `var.ssh_public_key`. We never generate an ephemeral keypair. Rationale:

1. **Break-glass continuity.** A Sovereign lives for years. An ephemeral key generated at provisioning time disappears the moment the catalyst-provisioner container restarts; at that point the only way back into the cluster is via Hetzner Console password-reset, which itself disrupts the in-cluster SPIRE identity if it forces a kubelet restart. Operator-owned keys (rooted in their corporate identity provider or hardware token) survive provisioner restarts.
2. **Audit trail.** Hetzner logs every `hcloud_ssh_key.create` and every login that uses it. With operator-owned keys, that log directly traces back to a named human in the operator's IdP. With auto-generated keys, the log says "catalyst-provisioner did it" — useless for incident forensics.
3. **No private-key custody problem.** Catalyst would have to store the auto-generated private key somewhere to give the operator break-glass. Either we put it in OpenBao (chicken-and-egg: OpenBao isn't running yet during Phase 0), or we ship it back to the wizard (we're now responsible for the key never leaking through the browser, the catalyst-provisioner logs, the OpenTofu state file, ...). Operator-owned keys move that custody problem to whoever's already responsible for it (the operator).
4. **Compliance.** Most enterprise frameworks (SOC 2 CC6.1, ISO 27001 A.9.4.3) require keys to trace back to a named individual. Auto-generated, vendor-held keys fail this.

The validation regex on `var.ssh_public_key` accepts `ssh-rsa`, `ssh-ed25519`, and `ecdsa-sha2-nistp256` formats. Recommend `ssh-ed25519` from a YubiKey-resident key for production.

---

## OS hardening (cloud-init)

Both `cloudinit-control-plane.tftpl` and `cloudinit-worker.tftpl` apply the same baseline. Each item is a template-conditional driven by a variable so an operator can disable it for a short-lived test Sovereign.

| Item | Variable (default) | What happens |
|---|---|---|
| sshd drop-in | always on | `/etc/ssh/sshd_config.d/99-catalyst-hardening.conf` sets `PasswordAuthentication no`, `KbdInteractiveAuthentication no`, `PermitRootLogin prohibit-password`, disables forwarding, tightens `MaxAuthTries=3` and `LoginGraceTime=30`. The `ssh-rsa`/`ssh-ed25519` key Hetzner injects via `ssh_keys[]` is the only path in. |
| `unattended-upgrades` | `enable_unattended_upgrades=true` | Daily security-only upgrades on Ubuntu, restricted to the `*-security` pocket. Auto-reboot at 02:30 if a kernel upgrade requires it; the LB health check covers the ~60 s window. Removes unused kernels to keep `/boot` from filling. |
| `fail2ban` (sshd jail) | `enable_fail2ban=true` | Defence-in-depth in case `ssh_allowed_cidrs` is later widened. `maxretry=5`, `findtime=10m`, `bantime=1h`, systemd backend. |

The hardening explicitly does **not** include AppArmor profile authoring, kernel-module blacklisting, or a CIS Level-2 sweep. Those are a Phase-2 task delivered by a Kyverno policy + a privileged DaemonSet (`bp-cis-hardening`), not Phase-0 cloud-init.

---

## Variables — reference

See [`variables.tf`](variables.tf) for the authoritative source. Highlights:

| Variable | Default | Validation |
|---|---|---|
| `region` | (required) | `fsn1`, `nbg1`, `hel1`, `ash`, `hil` |
| `control_plane_size` | `cx42` | `^(cx[0-9]+|ccx[0-9]+|cax[0-9]+)$` |
| `worker_size` | `cx32` | `^(cx[0-9]+|ccx[0-9]+|cax[0-9]+)$` |
| `worker_count` | `0` | `0 ≤ n ≤ 50` |
| `ha_enabled` | `false` | bool |
| `k3s_version` | `v1.31.4+k3s1` | `^v\d+\.\d+\.\d+\+k3s\d+$` |
| `ssh_public_key` | (required) | OpenSSH formats only |
| `ssh_allowed_cidrs` | `[]` | every entry must be a valid CIDR |
| `enable_unattended_upgrades` | `true` | bool |
| `enable_fail2ban` | `true` | bool |
| `domain_mode` | `pool` | `pool` or `byo` |
| `gitops_repo_url` | public OpenOva monorepo | string |
| `gitops_branch` | `main` | string |

Every default is the **common case** for a solo Sovereign. The waterfall doctrine ([`docs/PRINCIPLES.md`](../../docs/PRINCIPLES.md) §1) means the defaults must produce a working production-shape Sovereign, not a "demo it first" scaffold.

---

## How to invoke this module standalone

Most operators reach this module through the Catalyst console wizard, which writes a `tofu.auto.tfvars.json`, runs `tofu init && tofu apply`, and ships the outputs back to the user. The wizard path is the supported one.

If you need to drive provisioning by CLI (air-gapped sites, debugging, or a CI pipeline you own), the module accepts a flat `-var-file=` invocation:

```bash
# 1. Clone the module
git clone https://github.com/openova-io/openova.git
cd openova/infra/hetzner

# 2. Write a tfvars file (NEVER commit this — it contains the hcloud_token).
#    File ownership 0600, on an encrypted disk.
cat > sovereign.tfvars.json <<EOF
{
  "sovereign_fqdn":     "omantel.omani.works",
  "sovereign_subdomain": "omantel",
  "org_name":           "Omantel",
  "org_email":          "ops@omantel.om",
  "hcloud_token":       "<rotate after run>",
  "hcloud_project_id":  "<your project id>",
  "region":             "fsn1",
  "control_plane_size": "cx42",
  "worker_count":       0,
  "ha_enabled":         false,
  "k3s_version":        "v1.31.4+k3s1",
  "ssh_public_key":     "ssh-ed25519 AAAA... operator@laptop",
  "ssh_allowed_cidrs":  ["203.0.113.7/32"],
  "domain_mode":        "byo",
  "gitops_repo_url":    "https://github.com/openova-io/openova",
  "gitops_branch":      "main"
}
EOF
chmod 0600 sovereign.tfvars.json

# 3. Init + plan + apply
tofu init
tofu plan  -var-file=sovereign.tfvars.json -out=plan.bin
tofu apply plan.bin

# 4. Read outputs
tofu output -json
```

Outputs:

| Name | Use |
|---|---|
| `control_plane_ip` | First control-plane node's public IPv4. |
| `load_balancer_ip` | Public IPv4 the customer points DNS A records at (when `domain_mode=byo`). |
| `console_url` | `https://console.<sovereign_fqdn>` — usable once Flux finishes the bootstrap (~30 min). |
| `gitops_repo_url` | Path Flux on the new cluster watches; useful for audit. |

After `tofu apply` finishes, **archive the OpenTofu state file** and the tfvars file. Per `docs/SOVEREIGN-PROVISIONING.md` §4, the state is read-only from this point forward — Crossplane has adopted the cloud resources and any further change goes through it.

---

## What this module does NOT do

Out of scope by design — these are Crossplane / Flux territory:

- Cilium + Hubble installation (handled by `bp-cilium` reconciled by Flux).
- cert-manager issuers (handled by `bp-cert-manager` + Phase-2 day-1 setup).
- Keycloak realm provisioning (handled by `bp-keycloak` + Phase-2 day-1 setup).
- Object-storage bucket creation for Velero backups (Crossplane `provider-hcloud` + an `hcloud-storage-volume` Composition).
- DNS records beyond the Phase-0 wildcard (handled by External-DNS in the Sovereign once the bootstrap kit comes up).
- Day-2 cluster ops (node addition/removal — Crossplane Composition).

If you find yourself adding any of these to `main.tf`, you're violating [`docs/PRINCIPLES.md`](../../docs/PRINCIPLES.md) §3 — stop and route the work to Crossplane / Flux instead.

---

## Files

| File | Role |
|---|---|
| [`main.tf`](main.tf) | Resources + locals (network, firewall, SSH key, servers, LB, DNS hook). |
| [`variables.tf`](variables.tf) | Wizard inputs as variables, with validation blocks. |
| [`outputs.tf`](outputs.tf) | What the catalyst-api provisioner reads back after `tofu apply`. |
| [`versions.tf`](versions.tf) | OpenTofu + provider version constraints. |
| [`cloudinit-control-plane.tftpl`](cloudinit-control-plane.tftpl) | cloud-init for the first / HA control-plane nodes. Installs hardening, k3s, Flux, bootstrap pointer. |
| [`cloudinit-worker.tftpl`](cloudinit-worker.tftpl) | cloud-init for `worker_count` nodes. Installs hardening + joins the cluster. |

---

## Editing `*.tftpl` — escape `${VAR:-default}` in comments

OpenTofu's `templatefile()` parses **every** `${...}` interpolation in a `.tftpl` file regardless of whether it appears inside a YAML comment, an HCL string, or a shell heredoc. When the interpolation contains a colon (`${VAR:-default}` — the bash default-value shape envsubst uses on the Sovereign), tofu rejects it with:

```
Error: Extra characters after interpolation expression;
Template interpolation doesn't expect a colon at this location.
```

The colon is reserved in HCL's interpolation grammar (it's the conditional-expression `cond ? a : b` operator).

**Fix:** prefix the dollar with another dollar — `$$` is HCL's literal-dollar escape and emits one literal `$` from `templatefile()`:

```yaml
# WRONG — breaks `tofu plan`:
# slot 13 reads via ${QA_FIXTURES_ENABLED:-false} and the …

# RIGHT — survives templatefile() and reaches the shell as ${QA_FIXTURES_ENABLED:-false}:
# slot 13 reads via $${QA_FIXTURES_ENABLED:-false} and the …
```

This applies to **comments AND code lines**. Code lines also fail `tofu validate`; comment lines do not, so a CI guard in [`.github/workflows/infra-hetzner-tofu.yaml`](../../.github/workflows/infra-hetzner-tofu.yaml) (added by Fix #111 after PR #1311 / PR #1328) greps every `*.tftpl` here for the unescaped pattern and fails the workflow if any are introduced.

History: PR #1311 (Fix #73) shipped exactly this bug, broke `tofu plan` immediately, wasted ~30 min of provision-cycle on prov #9 before PR #1328 caught the one offender. The CI guard exists to ensure it never recurs.

---

*Part of the public OpenOva Catalyst monorepo. See [`docs/SOVEREIGN-PROVISIONING.md`](../../docs/SOVEREIGN-PROVISIONING.md) for the end-to-end provisioning narrative and [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) for the resource budget that drives the sizing defaults.*
