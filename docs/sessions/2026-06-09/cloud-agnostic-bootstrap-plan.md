# Cloud-Agnostic Sovereign Bootstrap — Design & Remediation Plan

> Founder directive 2026-06-09: *"This is AWS, Azure, GCP, Alibaba and many others — an
> agnostic layer. Make that abstraction layer as solid and reusable as possible … never
> ever differentiate any single piece [at] the k3s layer again unless there is a huge
> dependency enforcing something specific to that cloud provider. Turn this layer [into a]
> solid, reusable, never-failing, bullet-proof layer."*

## 1. The disease (root cause of weeks of fragility)

Provisioning a Sovereign has TWO layers that got conflated:

- **Infra layer (below k3s)** — VPC, subnets, instances, LB, public IPs, object storage, DNS.
  Genuinely cloud-specific (different APIs/resources). Lives in per-provider OpenTofu modules.
- **Kubernetes layer (k3s and everything on it)** — k3s install, kubeconfig PUT, CNI, Flux
  bootstrap, bootstrap secrets. **Byte-for-byte identical on every cloud.**

The defect: the Kubernetes-layer bootstrap was **copy-pasted into each provider's cloud-init**
(`infra/providers/hetzner/cloudinit-control-plane.tftpl` = 962 lines,
`infra/providers/huawei/cloudinit-control-plane.tftpl` = 1447 lines) and then **drifted**:

| | Hetzner (stayed lean) | Huawei (rotted) |
|---|---|---|
| imperative helm/kubectl/CRD lines | 7 | **40** |
| kubeconfig PUT ordering | **early — right after k3s** | **last — after CNI+CRD+Flux** |
| result | converges | #3129: "did not PUT kubeconfig" × 8 provs, 5 failed-fix PRs |

Two root causes, both consequences of the copy-paste drift:
1. **PUT-last inversion** — Huawei buries the one time-critical step (kubeconfig PUT) behind
   400+ lines of slow/fragile work, so any failure there (a dash syntax error, slow GitHub)
   blocks the PUT → Phase-1 fail. Hetzner PUTs first; nothing downstream can block it.
2. **Imperative CRD/CNI cruft** — Huawei re-implements work Flux already owns
   (`01a-gateway-api`, `01-cilium` with `dependsOn`), in shell the kom4dc image's ancient
   `/bin/sh` rejects (unreproducible locally → every bug cost a ~1h live prov).

## 2. The principle (non-negotiable, founder)

- **From k3s upward = ONE cloud-agnostic implementation. Never differentiate per-provider.**
- Differentiate ONLY below k3s (infra), and ONLY where a hard cloud dependency forces it.
- The Kubernetes-bootstrap layer must be solid, reusable, bullet-proof — works on every cloud
  unchanged. Adding a provider = write its infra module + fill a tiny variable contract. Zero
  new bootstrap logic.

## 3. Target architecture

```
┌─ Flux (steady state) ──────────────── AGNOSTIC. Already correct. clusters/_template/bootstrap-kit/* ─┐
│  owns ALL 31+ components declaratively with dependsOn; self-healing.                                  │
├─ SHARED Kubernetes bootstrap ──────── AGNOSTIC. ONE template: infra/providers/_shared/ ──────────────┤
│  k3s install → PUT kubeconfig EARLY → gateway-api CRDs (1 apply) → Cilium CNI → Flux bootstrap →      │
│  bootstrap secrets → completion marker.   No per-provider differentiation. EVER.                      │
├─ Provider prelude ─────────────────── THIN shim, injected. ONLY hard cloud deps (enumerated §5).      │
├─ OpenTofu infra module ────────────── per-provider (VPC/instances/LB). Standardized OUTPUTS (§4).     │
└─ Crossplane day-2 adoption ────────── future: tofu→Crossplane handover (currently disabled).          │
```

## 4. OpenTofu output contract (every provider module MUST expose)

So the shared bootstrap is fed identically regardless of cloud:

| Output | Meaning |
|---|---|
| `control_plane_public_ip` | primary CP reachable IP for the kubeconfig server URL + tls-san |
| `control_plane_private_ip` | node-ip / advertise-address |
| `node_external_ip_cmd` | command OR pre-resolved value to discover the node's own public IP |
| `region_canonical_label` | `openova.io/region` node label |
| `provider_id_cmd` | sets `.spec.providerID` (`hcloud://…`, `huawei://…`, `aws:///…`) or empty |
| `registry_mirror_yaml` | optional `/etc/rancher/k3s/registries.yaml` body (Huawei kom4dc); empty default |
| `provider_prelude` | optional provider-specific runcmd block injected before k3s (NIC, log self-upload) |

## 5. The ONLY allowed differentiation (the "hard dependency" exceptions)

| Concern | Hetzner | Huawei | AWS | Azure | GCP | How abstracted |
|---|---|---|---|---|---|---|
| Own public IP | metadata `169.254/hetzner/v1/.../public-ipv4` | tofu var `primary_cp_eip` (HCS metadata doesn't populate it) | `169.254/latest/meta-data/public-ipv4` | IMDS | metadata.google | `node_external_ip_cmd` var |
| providerID | `hcloud://<id>` | `huawei://…` | `aws:///<az>/<id>` | azure | gce | `provider_id_cmd` var |
| Registry mirror | none | kom4dc mirror | none | none | none | `registry_mirror_yaml` var |
| No console (log via push) | console exists | **NO console → self-upload log (#3132)** | console | console | console | `provider_prelude` |
| Private NIC bring-up | netplan reconciler | eth0 native | ENI | native | native | `provider_prelude` |

Everything else — **k3s exec line, healthz wait, kubeconfig rewrite+PUT, gateway-api CRDs,
Cilium install, Flux bootstrap, secret seeding** — is SHARED and identical.

## 6. Execution steps (autonomous)

1. **ADR-00xx** recording this architecture (immutable). + this design doc.
2. **Author `infra/providers/_shared/cloudinit-control-plane.tftpl`** — the ONE bootstrap, from
   the proven-lean Hetzner pattern: PUT-early, 1-line gateway-api CRD bundle apply, single Cilium
   install, Flux bootstrap, secret seeding. Provider bits via the §4 var contract + `provider_prelude`.
3. **Refactor Hetzner** module → render `_shared` + Hetzner prelude/vars. (Hetzner is already the
   pattern, so this is mostly extraction — proves the abstraction holds for the lean case.)
4. **Refactor Huawei** module → render `_shared` + Huawei prelude (registry mirror, EIP-from-tofu-var,
   self-upload log, iptables DNAT) + vars. **DELETE the 1447-line divergent template.**
5. **CI gate — `preflight-cloudinit-posix`**: render the cloud-init for EVERY provider and run the
   `runcmd` through a strict POSIX parser in a container matching the target image's `/bin/sh`
   (the thing that would have caught every #3129 dash bug in 5 seconds instead of a 1h prov).
6. **tofu render fixtures** per provider (extend `cloudinit_path_test.go`) — pre-merge break catch.
7. **Provision ONE Huawei Sovereign** on the new shared bootstrap. **NO auto-wipe loop.** A watch
   timeout is recoverable (Flux keeps reconciling) — keep the env, inspect, nurse to converged.
8. **Fill the UAT** (`docs/ledger/UAT.md`) test cases on the converged env — the actual target.

## 7. Why this is bullet-proof (never-fails guarantees)

- **ONE template** → zero drift; a fix lands for all clouds at once.
- **PUT-early** → the kubeconfig is delivered ~2 min after k3s, before ANY heavy work; nothing
  downstream (CNI, CRDs, GitHub, Flux) can ever cause "did not PUT kubeconfig" again.
- **Flux owns everything post-bootstrap** → no imperative CRD/CNI shell to rot.
- **CI POSIX-lint of the rendered cloud-init** → shell bugs caught pre-merge in seconds, not by a
  1-hour live prov. (Local `bash -n`/`dash -n` lie — proven this session; the gate uses the real shell.)
- **tofu render fixtures** → a template that won't render fails the PR, not the prov.
- **New cloud = infra module + 6 variables.** No bootstrap logic touched. Ever.

## 8. Discipline carried in (founder rules)

- Re-prov resets `docs/ledger/UAT.md` from scratch AND commits it (the reset was never committed → stale).
- Debug-before-wipe; **no auto-wipe on watch-timeout** (it destroyed hw115 at 54/56 — recoverable).
- One env, steered manually, nursed to converged, then walked for UAT.
