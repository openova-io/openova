# Sovereign Provisioning

**Status:** Authoritative procedure. **Updated:** 2026-04-29 (Reconcile Pass 3).
**Implementation:** §3 below now reflects the deployed shape — the Go provisioner with bundled OpenTofu CLI + `infra/hetzner/` module (`9b6c297d`/`61c61226`), all 11 bp-* umbrella Helm charts at v1.1.0 (`43aff202`/`e42799fa`), the cloud-init Cilium-before-Flux installer (`e571ec7a`/`54872009`), the bootstrap-kit + infrastructure-config Kustomization split (`34c8de84`/`2da4c43c`), the per-Sovereign PowerDNS zone model (#167/#168), and the pool-domain-manager (PDM) with registrar adapters (#163/#170) all exist in this monorepo today (per [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md) §7). End-to-end DoD against a real Hetzner project is pending Group M of [`PROVISIONING-PLAN.md`](PROVISIONING-PLAN.md). Catalyst-Zero (Contabo k3s, namespace `catalyst`) is the running catalyst-provisioner today.

How to provision a new **Sovereign** — a self-sufficient deployed instance of Catalyst. Defer to [`GLOSSARY.md`](GLOSSARY.md) for terminology and [`ARCHITECTURE.md`](ARCHITECTURE.md) for the model.

---

## 1. Inputs

| Input | Required | Notes |
|---|---|---|
| Cloud provider | Hetzner / AWS / GCP / Azure / OCI / Huawei | Hetzner is the most-tested path. |
| Cloud credentials | Provider API token | Used by OpenTofu (one-shot bootstrap) and Crossplane (ongoing). |
| Sovereign name | e.g. `omantel`, `bankdhofar` | Slug, lowercase, 3–32 chars. |
| Sovereign domain | e.g. `omantel.omani.works`, `acme.bank.com` | Three modes (#169): **pool** (subdomain under `omani.works` / `openova.io`, allocated by pool-domain-manager); **byo-manual** (customer pastes OpenOva NS records into their own registrar UI); **byo-api** (customer pastes a registrar API token, OpenOva flips NS via the registrar adapter). Supported registrars for byo-api: Cloudflare, Namecheap, GoDaddy, OVH, Dynadot (#170). |
| Region(s) | 1+ | Single-region simplest for SME; 2+ for regulated/HA. |
| Building blocks per region | typically `mgt` + `rtz` (+ `dmz`) | At minimum `mgt` + `rtz`. |
| Keycloak topology | `per-organization` (SME) / `shared-sovereign` (corporate) | Determines Keycloak deployment shape. |
| Federation IdP (optional) | Azure AD / Okta / Google / etc. | For corporate; SME tier defers to per-Org Org-IdP federation. |
| TLS strategy | Let's Encrypt / cert-manager / corporate CA | cert-manager-managed, Let's Encrypt by default. |
| Object storage | Cloud-provider native | Used as the cold-tier backend behind SeaweedFS (which is the in-cluster S3 encapsulation layer that all consumers — Velero, Harbor, CNPG WAL, OpenSearch snapshots, Loki/Mimir/Tempo, Iceberg — talk to). |

---

## 2. Provisioning runs from `catalyst-provisioner`

The bootstrap is performed by `catalyst-provisioner.openova.io`, an always-on provisioning service operated by OpenOva. It is **not** part of any Sovereign at runtime — once a Sovereign is up, it is fully self-sufficient.

Why a permanent provisioner instead of "boot from your laptop":
- OpenTofu state must be durably stored — keeping it on a single person's laptop is fragile and a security risk.
- Provider credentials are scoped, stored in OpenBao on the provisioner, and never leave it.
- New Sovereigns can be created without a manual installer dance — the same machinery serves the next Sovereign provisioning request, regardless of who initiates it.

A self-host route exists for organizations that want zero OpenOva involvement: `catalyst-provisioner` is itself a Blueprint (`bp-catalyst-provisioner`) and can be deployed in a customer's own infrastructure. From there it bootstraps further Sovereigns. This is the air-gap path.

---

## 3. Phase 0 — Bootstrap

The implementation maps cleanly onto two artifacts in this monorepo:

| Step | Lives in | What runs |
|---|---|---|
| 1. Wizard input → tofu vars | [`products/catalyst/bootstrap/api/internal/provisioner/`](../products/catalyst/bootstrap/api/internal/provisioner/) | Go service writes `tofu.auto.tfvars.json` from validated wizard input, runs `tofu init && tofu plan && tofu apply -auto-approve` against the bundled `infra/hetzner/` module (the canonical Tofu sources are baked into the catalyst-api image at `/infra/hetzner/`, and the `tofu` v1.11.6 CLI is bundled and SHA256-verified at build time — `9b6c297d`/`61c61226` — so the catalyst-api Pod IS the OpenTofu runner; no host-side `tofu` install required), streams stdout/stderr lines to the wizard via SSE. No cloud APIs called from Go (per [`INVIOLABLE-PRINCIPLES.md`](INVIOLABLE-PRINCIPLES.md) #3). |
| 2. Cloud resources | [`infra/hetzner/main.tf`](../infra/hetzner/main.tf) | OpenTofu provisions: hcloud_network (10.0.0.0/16) + subnet (10.0.1.0/24), hcloud_firewall (80/443/6443/ICMP open; 22 closed by default — operator adds source-CIDR rule via Crossplane post-bootstrap), hcloud_ssh_key from wizard input, 1 control-plane server (or 3 if `ha_enabled`) on Ubuntu 24.04 with cloud-init, `worker_count` worker servers, hcloud_load_balancer (lb11) targeting NodePorts 31080/31443. **No DNS in this module** — the historical `null_resource.dns_pool` was removed at `330211d2` because pool-domain-manager (PDM) is the single owner of pool-domain Dynadot writes; PDM `/v1/commit` runs once the LB IP is known (after `tofu-output` resolves) and creates the per-Sovereign PowerDNS zone, writes the canonical 6-record set, and for pool sovereigns also writes the parent-zone NS delegation via the OpenOva Dynadot registrar adapter. For `byo-api` Sovereigns the matching registrar adapter (Cloudflare / Namecheap / GoDaddy / OVH / Dynadot, #170) flips the NS records at the customer's registrar. `byo-manual` Sovereigns instead show the OpenOva NS list in the wizard and poll until the customer's own registrar propagates the delegation. SKU validation regex accepts every Hetzner family (`cx*` / `cpx*` / `ccx*` / `cax*` — the wizard's recommended **CPX32** (4 vCPU AMD / 8 GB / €0.0232/hr) lives in the `cpx*` family at `c6cbfe68`); `worker_size = ""` is also valid for solo Sovereigns where `worker_count = 0`. |
| 3. k3s + Cilium + Flux bootstrap | [`infra/hetzner/cloudinit-control-plane.tftpl`](../infra/hetzner/cloudinit-control-plane.tftpl) | cloud-init on the control-plane node installs k3s v1.31.4+k3s1 with `--flannel-backend=none --disable-network-policy --disable=traefik --disable=servicelb --disable=local-storage --tls-san=<sovereign-fqdn>`, then **installs Cilium first via Helm** (`helm install cilium cilium/cilium --version 1.16.5 --set k8sServiceHost=127.0.0.1 ...`) so the cluster has a CNI BEFORE Flux runs — `e571ec7a` (Cilium-before-Flux ordering) + `54872009` (`k8sServiceHost=127.0.0.1` so the bootstrap doesn't deadlock on the LB IP that doesn't exist yet). When Flux later reconciles `bp-cilium`, it adopts the existing Helm release. After Cilium rolls out, cloud-init installs Flux v2.4.0 core, then applies a single manifest (`/var/lib/catalyst/flux-bootstrap.yaml`) that creates the GitRepository plus **two** Kustomizations — `bootstrap-kit` (HelmReleases) and `infrastructure-config` (ProviderConfig + Crossplane Compositions, `dependsOn: bootstrap-kit`, `wait: true` per `34c8de84`/`2da4c43c`) — both pointing at `clusters/<sovereign-fqdn>/`. From this point Flux owns the cluster. Workers join via [`cloudinit-worker.tftpl`](../infra/hetzner/cloudinit-worker.tftpl) using the project-derived k3s_token. |
| 4. Bootstrap-kit install | `clusters/<sovereign-fqdn>/bootstrap-kit/` (Flux-reconciled) | Flux installs the 11 bp-* umbrella Helm charts at v1.1.0 — each a `bp-<name>:1.1.0` OCI artefact at `oci://ghcr.io/openova-io` (HelmRepository `secretRef: ghcr-pull` per `efa41803`), each declaring its upstream chart under `Chart.yaml`'s `dependencies:` so the published artefact carries the full upstream payload (the historical v1.0.0 / v1.0.1 artefacts were hollow — see [`BLUEPRINT-AUTHORING.md`](BLUEPRINT-AUTHORING.md) §11.1). Dependency order via `dependsOn`: cilium (already installed via cloud-init Helm — Flux adopts) → cert-manager → flux (host-level reconciler for the cluster's own Kustomizations) → crossplane → sealed-secrets (transient) → spire (server + agent) → nats-jetstream → openbao (3-node Raft) → keycloak (per topology choice) → gitea (with public Blueprint mirror) → bp-catalyst-platform (umbrella; itself depends on the 10 leaves + bp-external-dns and brings the full upstream payload — no Catalyst-side bootstrap installer required). The duplicate `kube-system` Namespace declarations on `01-cilium.yaml` + `05-sealed-secrets.yaml` were dropped at `2022e1af` (kubectl-built-in namespace, never re-declare). bp-powerdns is installed on Catalyst-Zero only (it serves authoritative DNS for every other Sovereign's zone) and is not part of the franchised Sovereign bootstrap-kit. The `infrastructure-config` Kustomization (Crossplane Provider package + ProviderConfig + Compositions) reconciles after `bootstrap-kit` is Ready. |
| 5. Crossplane adoption | Crossplane Compositions in `clusters/<sovereign-fqdn>/infrastructure/` | Crossplane adopts management of all infrastructure created by OpenTofu in step 2; sealed-secrets is decommissioned in favour of ESO + OpenBao for day-2 secret distribution; further DNS records (gitea/admin/api/harbor) are written by `external-dns` against the per-Sovereign PowerDNS zone via the PowerDNS REST API (NOT against the registrar). Phase 1 begins (see §4). |

The wizard's progress surface is the **Sovereign Admin landing page** at `/sovereign/provision/$deploymentId` (route module [`AdminPage.tsx`](../products/catalyst/bootstrap/ui/src/pages/sovereign/AdminPage.tsx); the legacy `ProvisionPage.tsx` DAG view was gutted at `4047ba1d` and replaced with an Application card grid — every Application installed on this Sovereign renders as a card from first paint, with a status pill that flips `pending → installing → installed | failed | degraded` as the catalyst-api emits per-component HelmRelease events). The catalyst-api `internal/helmwatch/` package (`5be6bcba`) attaches an informer to the new cluster's HelmReleases via the kubeconfig captured at `tofu-output` and emits SSE events shaped `phase: "component", component: <id>, state: <state>` so the AdminPage doesn't need to poll. Click any card for the per-Application page at `/sovereign/provision/$deploymentId/app/$componentId` ([`ApplicationPage.tsx`](../products/catalyst/bootstrap/ui/src/pages/sovereign/ApplicationPage.tsx)) with Overview / Logs / Dependencies / Status tabs. Steady-state is reached when every Application card shows `installed` and the page-level overall status pill goes green.

**DNS records written in Phase 0** — into the per-Sovereign PowerDNS zone (`<sovereign-fqdn>.`), see [`PLATFORM-POWERDNS.md`](PLATFORM-POWERDNS.md) §"Per-Sovereign zone model":

```
@                A → load balancer IP
*                A → load balancer IP
console          A → load balancer IP
api              A → load balancer IP
gitea            A → load balancer IP
harbor           A → load balancer IP
```

The PDM `/v1/commit` endpoint writes the canonical 6-record set into the freshly-created Sovereign zone via the PowerDNS REST API. The wildcard A record covers every additional subdomain a Sovereign might add at runtime (`axon`, `umami`, `langfuse`, etc.) without re-issuing certificates. Per NAMING §5.1 the canonical control-plane DNS pattern is `{component}.{location-code}.{sovereign-domain}` — the wildcard handles per-Application records under per-Environment subdomains.

**OpenTofu state:** kept in the catalyst-api Pod under `/tmp/catalyst/tofu/<sovereign-fqdn>/` — pinned via the `CATALYST_TOFU_WORKDIR` env var on the catalyst-api Deployment (commit `27527e4c`) and backed by the Pod's writable `/tmp` emptyDir (2 Gi sizeLimit; the in-code default `/var/lib/catalyst/...` is unwritable for UID 65534, hence the override). Re-running with the same FQDN is idempotent (`tofu apply` on existing state). For air-gap installs the operator MUST configure a remote backend with encryption-at-rest so the Hetzner token isn't carried only on Pod ephemeral storage.

**Deployment state persistence:** Per-deployment metadata (wizard inputs with secrets redacted — `hetznerToken`, `dynadotKey`, `dynadotSecret`, `registrarToken` are stripped — plus the SSE event tail and the `Result` struct fields `ComponentStates map[string]string`, `Phase1FinishedAt *time.Time`, and `Kubeconfig string`) is persisted to one JSON file per deployment id under `/var/lib/catalyst/deployments/<id>.json` by `internal/store/` (`418cead0`). The directory is backed by the RWO PVC `catalyst-api-deployments` (1 Gi, `Recreate` strategy on the Deployment, `fsGroup: 65534`) so a Pod restart mid-Phase-1 does not lose the in-flight state. Two new endpoints surface the persisted state: `GET /api/v1/deployments/<id>/kubeconfig` returns the kubeconfig captured at `tofu-output` (so an operator can `kubectl --kubeconfig=...` into the new Sovereign during Phase 1), and `GET /api/v1/deployments/<id>/events` replays the persisted SSE history for a reconnecting AdminPage. The watch loop honours the `CATALYST_PHASE1_WATCH_TIMEOUT` env var (default 60m) and the persistence root honours `CATALYST_DEPLOYMENTS_DIR` (default `/var/lib/catalyst/deployments`).

**Implementation status:** the Go wrapper (with bundled OpenTofu CLI v1.11.6 and bundled `infra/hetzner/` module per `9b6c297d`/`61c61226`), all 11 bp-* umbrella Helm charts at v1.1.0 (`43aff202`/`e42799fa`), and bp-powerdns 1.1.0 on Catalyst-Zero (live HelmRelease `bp-powerdns@1.1.0+ef3c785bfd24`) all exist today (verified at [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md) §7). The pool-domain-manager (`core/pool-domain-manager/`) and its 5 registrar adapters are deployed and running in `openova-system`. End-to-end DoD against a real Hetzner project is pending Group M of the [Catalyst-Zero Provisioning Plan](PROVISIONING-PLAN.md).

Total Phase 0 time: 30–60 minutes for a single-region Hetzner Sovereign once DoD lands.

---

## 4. Phase 1 — Hand-off

After Phase 0 completes:

1. Crossplane in the new Sovereign **adopts** management of all infrastructure created by OpenTofu. From this point forward, all infrastructure changes go through Crossplane.
2. The bootstrap k3s nodes are not "thrown away" — they are claimed by Crossplane via the cloud provider's adoption mechanism.
3. OpenTofu state is archived and read-only. It is never touched again.
4. `catalyst-provisioner` no longer has any active connection to the new Sovereign.

The Sovereign is now self-sufficient. It has the full Catalyst control-plane set per [`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md) §2.3:

- Its own Crossplane managing further infrastructure.
- Its own OpenBao for secrets.
- Its own JetStream as event spine.
- Its own Keycloak for users.
- Its own SPIFFE/SPIRE for workload identity (5-min rotating SVIDs).
- Its own Gitea (with mirror of the public Blueprint catalog).
- Its own observability stack (Grafana + Alloy + Loki + Mimir + Tempo) for self-monitoring.
- Its own Catalyst control plane (console, marketplace, admin, projector, catalog-svc, provisioning, environment-controller, blueprint-controller, billing).

---

## 5. Phase 2 — Day-1 setup

The first `sovereign-admin` logs into `console.<location-code>.<sovereign-domain>`:

```
Day-1 actions
──────────────────────────────────────────────────────────────────
1. Configure cert-manager issuers (Let's Encrypt / corporate CA).
2. Configure backup destination (cloud object storage for Velero).
3. Configure Harbor with image-scanning policies.
4. (Optional) Federate Keycloak's catalyst-admin realm to corporate IdP.
5. (Optional) Configure observability exports (SIEM, datadog, etc.).
6. Onboard the first Organization:
     Catalyst console → Admin → Organizations → New
     Provide: name, contact, plan.
   Environment-controller does NOT create vclusters yet.
   They are created when the first Environment is provisioned.
7. Create the first Environment in that Organization:
     Console → switch to Org context → Environments → New
     Environment-controller spins up a vcluster on the chosen host cluster
     and bootstraps Flux inside (watching the env-appropriate branch on
     every Application repo within this Org's Gitea Org). Apps not yet
     installed have no repos yet; repos are created on demand by the
     provisioning-service when each App is installed.
     Ready in ~60 seconds.
```

---

## 6. Phase 3 — Steady-state operation

From here on, the Sovereign runs autonomously. Sovereign-admins use the Catalyst admin UI for:

- Onboarding more Organizations
- Adding host clusters in new regions (Crossplane provisions them, environment-controller adopts them)
- Updating Catalyst itself (umbrella Blueprint version bumps, applied via Flux PR)
- Configuring SecretPolicies and EnvironmentPolicies
- Monitoring the Sovereign's own observability stack
- Reviewing audit logs

Everyday Application installs and configurations are done by `org-admins` and `org-developers` within their Organizations — see [`PERSONAS-AND-JOURNEYS.md`](PERSONAS-AND-JOURNEYS.md).

---

## 7. Multi-region topology

### 7.1 Single-region (SME default)

```
Region A
└── Host cluster: hz-fsn-mgt-prod    ← Catalyst control plane + per-Org vclusters
    └── all building blocks collapse onto one cluster (mgt + rtz + dmz workloads
        in separate namespaces, with Cilium NetworkPolicies enforcing isolation)
```

Cheapest topology. Single-region failure = Sovereign down. Acceptable for SME tier where customers also accept SME-tier SLAs.

### 7.2 Multi-region (corporate default)

```
Region A (primary mgt)              Region B                       Region C (DR)
─────────────────                  ─────────────                  ─────────────
hz-nbg-mgt-prod                    hz-fsn-rtz-prod                hz-hel-rtz-prod
  Catalyst control plane             per-Org vclusters              per-Org vclusters
  Gitea, JetStream, OpenBao,         (sibling realizations          (sibling realizations
  Keycloak, projector,               of each Org's Environment)     of each Org's Environment)
  catalog-svc, marketplace,
  console, admin, billing
hz-nbg-dmz-prod                    hz-fsn-dmz-prod                hz-hel-dmz-prod
  ingress, WAF, PowerDNS            ingress, WAF, PowerDNS          ingress, WAF, PowerDNS
```

The `mgt` building block is typically NOT replicated (one Catalyst control plane per Sovereign). The `rtz` and `dmz` blocks ARE replicated for workload HA.

OpenBao runs in BOTH the mgt cluster (primary) and each rtz region (replica) — see [`SECURITY.md`](SECURITY.md) §5 for replication semantics.

---

## 8. Adding a region post-provisioning

```
sovereign-admin in Catalyst admin UI:
  Admin → Infrastructure → Add Region
    Provider: Hetzner
    Region: hel
    Building blocks: rtz, dmz
    Apply
```

Catalyst:
1. Crossplane provisions the new VPC, hosts, k3s cluster, etc.
2. Cluster registered in Catalyst's cluster registry.
3. cert-manager + Cilium + Flux + Crossplane + SPIRE + ESO + OpenBao replica deployed via the cluster's Flux Kustomization.
4. New region available as a Placement target for new and existing Environments.

Existing Applications with `placement.mode: single-region` do not migrate automatically. To extend an existing Application to the new region, the user explicitly switches Placement to `active-active` (or `active-hotstandby`) and adds the new region to `placement.regions` — that's a one-line edit in the Application's Gitea repo on the appropriate branch (or a click in the Topology tab).

---

## 9. Air-gap deployment

```
Connected zone (one-time)             Air-gapped Sovereign
──────────────────────────            ───────────────────────────────
1. Mirror public Blueprint OCI       Harbor receives blobs via physical
   artifacts to portable media.      transfer / data diode.
2. Mirror Catalyst control-plane     Sovereign's Gitea adopts blobs as
   container images.                 OCI manifests in local registry.
3. Mirror cert-manager root +        cert-manager configured with
   organization CA bundle.           internal CA only.
4. Configure Keycloak to local LDAP  Keycloak federates to internal AD/LDAP.
   (no external IdPs).
```

Catalyst is air-gap-ready by construction: every artifact (Blueprints, Catalyst code, base images) is OCI-signed. Mirror once, run forever.

---

## 10. Migration and decommission

### 10.1 Migrating an Organization between Sovereigns

Rare but supported. Example: a Bank Dhofar Organization started life on the openova Sovereign (paid SaaS), now wants to move to its own bankdhofar Sovereign (self-host).

```
1. Provision bankdhofar Sovereign (Phases 0–2).
2. On openova Sovereign: Admin → Organization → Export
     Catalyst produces an export bundle:
       - Org metadata
       - All Application Gitea repos under this Org (cloned + bundled, including all branches)
       - The Org's `shared-blueprints` repo
       - Keycloak realm export (users, federated identities)
       - OpenBao export (sealed secrets only)
3. On bankdhofar Sovereign: Admin → Organization → Import
     Environment-controller recreates Environments → vclusters.
     Flux pulls manifests, reconciles.
     Apps come up.
4. Final cutover: DNS swap.
5. Verify, then decommission on openova side.
```

Time depends on data volume; typically minutes to hours per Org.

### 10.2 Decommissioning a Sovereign

Reverse of provisioning:

```
1. Migrate all Organizations off (Section 10.1).
2. Catalyst admin → Sovereign → Decommission
3. Crossplane begins teardown of host clusters.
4. OpenBao final state exported and stored encrypted.
5. DNS records removed.
6. Cloud resources reclaimed.
```

The customer keeps the OpenBao export and Gitea bundles for whatever retention period their compliance demands.

---

*Cross-reference [`ARCHITECTURE.md`](ARCHITECTURE.md) and [`SECURITY.md`](SECURITY.md). For day-to-day operation see [`SRE.md`](SRE.md).*
