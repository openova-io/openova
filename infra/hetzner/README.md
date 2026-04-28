# `infra/hetzner/` — Catalyst Sovereign provisioning module

Canonical Phase 0 OpenTofu module that provisions a single-region Catalyst Sovereign on Hetzner Cloud and bootstraps it onto Flux-driven GitOps. After `tofu apply` finishes, every subsequent change to the Sovereign goes through Crossplane (cloud resources) and Flux (Kubernetes resources). OpenTofu state is archived and never touched again.

This module is the implementation of [`docs/SOVEREIGN-PROVISIONING.md`](../../docs/SOVEREIGN-PROVISIONING.md) §3 (Phase 0 — Bootstrap) and follows [`docs/INVIOLABLE-PRINCIPLES.md`](../../docs/INVIOLABLE-PRINCIPLES.md) — every value the wizard or operator picks is a variable; nothing is hardcoded.

---

## What this module creates

| Resource | Purpose |
|---|---|
| `hcloud_network` + `hcloud_network_subnet` | Private 10.0.0.0/16 with 10.0.1.0/24 reserved for control-plane and workers. |
| `hcloud_firewall` | Inbound rules for 80/443 (HTTPS), 6443 (k3s API), ICMP, and an opt-in SSH rule keyed to operator CIDRs. |
| `hcloud_ssh_key` | The operator's existing SSH key (from their Hetzner project) — never auto-generated. |
| `hcloud_server` (control plane) | 1 node by default (`ha_enabled=false`); 3 nodes when HA is on. Cloud-init installs k3s + Flux + the bootstrap kit pointer. |
| `hcloud_server` (workers) | `worker_count` nodes (default 0 — solo Sovereign). |
| `hcloud_load_balancer` (`lb11`) | Public IPv4; forwards 80→31080 and 443→31443 (Cilium Gateway NodePorts post-bootstrap). |
| `null_resource.dns_pool` | Calls `/usr/local/bin/catalyst-dns` (a helper inside the catalyst-api container) when `domain_mode=pool` to write Dynadot A records for the new sovereign FQDN. |

After Phase 0, the cluster's Flux pulls `clusters/<sovereign_fqdn>/` from the public OpenOva monorepo and installs the 11-component bootstrap kit (Cilium → cert-manager → Crossplane → ESO → SPIRE → NATS → OpenBao → Keycloak → Gitea → catalyst-platform). Hetzner adoption by Crossplane happens once `provider-hcloud` is up.

---

## Sizing rationale — why `cx42` is the default

`docs/PLATFORM-TECH-STACK.md` §7.1 sets the RAM budget for a Catalyst-only mgt cluster at **~11.3 GB**, and §7.4 adds **~8.8 GB** for per-host-cluster infrastructure that runs on every host cluster including mgt (Cilium, Flux, Crossplane, cert-manager, ESO, Kyverno, Trivy Operator, Falco, Harbor, SeaweedFS, Velero, plus small operators).

For a **solo** Sovereign (single node hosting both the Catalyst control plane and the per-host-cluster infra), the floor is therefore **~20 GB RAM minimum**, before adding any application Blueprints.

| Hetzner type | RAM | vCPU | Disk | Verdict for solo Sovereign |
|---|---|---|---|---|
| `cx22` | 4 GB | 2 | 40 GB | Insufficient — OOM during Cilium install. |
| `cx32` | 8 GB | 4 | 80 GB | **Insufficient.** Used to be the default. Bootstrap kit OOMs around the OpenBao + Keycloak step (~12-15 GB working set). |
| `cx42` | 16 GB | 8 | 160 GB | **Default.** Smallest viable size for a solo Sovereign with no Blueprints. Leaves ~5 GB headroom for the first 1-2 Application Blueprints before scaling. |
| `cx52` | 32 GB | 16 | 320 GB | Recommended for a solo Sovereign that will also host workloads (10+ Blueprints). |
| `ccx33` | 32 GB | 8 dedicated | 240 GB | Recommended for **production** solo Sovereign — dedicated vCPUs avoid noisy-neighbour latency on the API server. |
| `cax41` | 32 GB | 16 ARM | 320 GB | Cheapest path to 32 GB. Confirm all upstream Blueprint container images are multi-arch before using (most are; a handful aren't). |

**This is a real fix.** The original `cx32` default was carried over from a development scratchpad; on a real provisioning run it would OOM during the bootstrap. The default is now `cx42`, validated against the §7.1 + §7.4 budget, and the variable's regex blocks anything outside the `cxNN | ccxNN | caxNN` namespace.

### Upgrade path

Resizing is non-destructive on Hetzner — `tofu apply -var control_plane_size=cx52` will trigger a `hcloud_server` resize. The node reboots once. On a single-node Sovereign that means ~60 seconds of console downtime; the LB health-check covers it. For HA Sovereigns (`ha_enabled=true`), the resize is rolling — no externally-visible downtime.

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

k3s is installed via `curl get.k3s.io | sh -` from cloud-init. The `INSTALL_K3S_EXEC` argument carries the flag set required by the rest of the Catalyst stack. Each flag below maps to a specific architectural decision in `docs/PLATFORM-TECH-STACK.md` §8.

| Flag | Why |
|---|---|
| `--cluster-init` | Initialise embedded etcd. Required for Phase-1 hand-off to add additional control-plane nodes (`ha_enabled=true`) without re-bootstrapping. |
| `--flannel-backend=none` | k3s ships with flannel; we replace the CNI with Cilium (gateway API, eBPF, mTLS via wireguard). Setting `none` keeps k3s from racing flannel against Cilium during boot. |
| `--disable=traefik` | k3s ships with Traefik; we use **Cilium Gateway API** (already part of the Cilium install). Catalyst's Gateway/HTTPRoute manifests assume Gateway API, not Traefik IngressRoute. |
| `--disable=servicelb` | k3s ships with klipper-lb; we use the Hetzner load balancer for ingress (`hcloud_load_balancer.main`) and k8gb for cross-region failover. klipper-lb would steal the NodePort 80/443 binding. |
| `--disable=local-storage` | k3s ships local-path-provisioner; we use **hcloud-csi** (provisioned by Crossplane after Phase 1) so PVCs survive node deletion and can be migrated across regions via Velero. |
| `--disable-network-policy` | k3s ships kube-router NetworkPolicy; **Cilium** handles NetworkPolicy. Two NetworkPolicy controllers fight each other. |
| `--tls-san=<sovereign_fqdn>` | API server TLS cert must be valid for the public sovereign FQDN, otherwise the wizard's kubeconfig fetch and any operator running `kubectl --server=https://<fqdn>:6443` get a SAN mismatch. |
| `--node-label catalyst.openova.io/role=control-plane` | Used by NodeAffinity on Catalyst control-plane services (Console, projector, etc.) to pin them off worker nodes. |
| `--write-kubeconfig-mode=0644` | Lets the catalyst-api fetch the kubeconfig over the wizard channel without sudo. The kubeconfig is rotated and replaced with a SPIFFE-issued identity in Phase 2. |

The `INSTALL_K3S_VERSION` environment variable is `var.k3s_version` (default `v1.31.4+k3s1`). Pinned so a Sovereign provisioned today and one provisioned next month land on the same Kubernetes minor — the Catalyst compatibility matrix in `docs/PLATFORM-TECH-STACK.md` §8.1 is keyed to k3s minor versions.

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

Every default is the **common case** for a solo Sovereign. The waterfall doctrine ([`docs/INVIOLABLE-PRINCIPLES.md`](../../docs/INVIOLABLE-PRINCIPLES.md) §1) means the defaults must produce a working production-shape Sovereign, not a "demo it first" scaffold.

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

If you find yourself adding any of these to `main.tf`, you're violating [`docs/INVIOLABLE-PRINCIPLES.md`](../../docs/INVIOLABLE-PRINCIPLES.md) §3 — stop and route the work to Crossplane / Flux instead.

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

*Part of the public OpenOva Catalyst monorepo. See [`docs/SOVEREIGN-PROVISIONING.md`](../../docs/SOVEREIGN-PROVISIONING.md) for the end-to-end provisioning narrative and [`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) for the resource budget that drives the sizing defaults.*
