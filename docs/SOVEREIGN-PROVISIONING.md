# Sovereign Provisioning

**Status:** Authoritative target procedure. **Updated:** 2026-04-27.
**Implementation:** The bootstrap kit and Catalyst control plane referenced below are design-stage. See [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md). The legacy Contabo VPS runs the older SME marketplace today; provisioning is not yet automated.

How to provision a new **Sovereign** — a self-sufficient deployed instance of Catalyst. Defer to [`GLOSSARY.md`](GLOSSARY.md) for terminology and [`ARCHITECTURE.md`](ARCHITECTURE.md) for the model.

---

## 1. Inputs

| Input | Required | Notes |
|---|---|---|
| Cloud provider | Hetzner / AWS / GCP / Azure / OCI / Huawei | Hetzner is the most-tested path. |
| Cloud credentials | Provider API token | Used by OpenTofu (one-shot bootstrap) and Crossplane (ongoing). |
| Sovereign name | e.g. `omantel`, `bankdhofar` | Slug, lowercase, 3–32 chars. |
| Sovereign domain | e.g. `omantel.openova.io`, `bankdhofar.com` | Customers may use openova subdomains initially, then migrate. |
| Region(s) | 1+ | Single-region simplest for SME; 2+ for regulated/HA. |
| Building blocks per region | typically `mgt` + `rtz` (+ `dmz`) | At minimum `mgt` + `rtz`. |
| Keycloak topology | `per-organization` (SME) / `shared-sovereign` (corporate) | Determines Keycloak deployment shape. |
| Federation IdP (optional) | Azure AD / Okta / Google / etc. | For corporate; SME tier defers to per-Org Org-IdP federation. |
| TLS strategy | Let's Encrypt / cert-manager / corporate CA | cert-manager-managed, Let's Encrypt by default. |
| Object storage | Cloud-provider native | Used by Velero, MinIO tiering, Harbor. |

---

## 2. Provisioning runs from `catalyst-provisioner`

The bootstrap is performed by `catalyst-provisioner.openova.io`, an always-on provisioning service operated by OpenOva. It is **not** part of any Sovereign at runtime — once a Sovereign is up, it is fully self-sufficient.

Why a permanent provisioner instead of "boot from your laptop":
- OpenTofu state must be durably stored — keeping it on a single operator's laptop is fragile and a security risk.
- Provider credentials are scoped, vault-stored, and never leave the provisioner.
- New Sovereigns can be created without a manual installer dance — the same machinery serves the next operator.

A self-host route exists for organizations that want zero OpenOva involvement: `catalyst-provisioner` is itself a Blueprint (`bp-catalyst-provisioner`) and can be deployed in a customer's own infrastructure. From there it bootstraps further Sovereigns. This is the air-gap path.

---

## 3. Phase 0 — Bootstrap

```
catalyst-provisioner                          Target cloud (e.g. Hetzner)
─────────────────────                         ────────────────────────────

1. OpenTofu run                  ─────────►   VPC, subnets, security groups
                                              k3s nodes (3 mgt + workload nodes)
                                              Cloud LB, DNS A records
                                              Object storage bucket

2. Bootstrap kit deploys onto    ─────────►   Components (in order):
   the new k3s cluster:                          a. cilium (CNI + Gateway API)
                                                 b. cert-manager
                                                 c. flux (host-level)
                                                 d. crossplane + provider config
                                                 e. sealed-secrets (transient)
                                                 f. spire-server + agent
                                                 g. nats-jetstream (3 nodes)
                                                 h. openbao (3 Raft nodes)
                                                 i. keycloak (per topology choice)
                                                 j. gitea (with public Blueprint mirror)
                                                 k. catalyst control plane
                                                    (bp-catalyst-platform umbrella)

3. Domain registration / DNS     ─────────►   gitea.<sovereign>.<domain>     A
   records (via Crossplane)                   console.<sovereign>.<domain>   A
                                              admin.<sovereign>.<domain>     A

4. Keycloak realm provisioning   ─────────►   catalyst-operator realm
                                              (initial sovereign-admin user)

5. Smoke tests                   ─────────►   Console reachable with TLS
                                              First sovereign-admin can log in
                                              Catalog mirror populated
                                              Crossplane reconciles a test resource

6. OpenTofu state archive        ─────────►   Encrypted, stored in catalyst-provisioner.
                                              Never used in the Sovereign's runtime.
```

Total Phase 0 time: 30–60 minutes for a single-region Hetzner Sovereign.

---

## 4. Phase 1 — Hand-off

After Phase 0 completes:

1. Crossplane in the new Sovereign **adopts** management of all infrastructure created by OpenTofu. From this point forward, all infrastructure changes go through Crossplane.
2. The bootstrap k3s nodes are not "thrown away" — they are claimed by Crossplane via the cloud provider's adoption mechanism.
3. OpenTofu state is archived and read-only. It is never touched again.
4. `catalyst-provisioner` no longer has any active connection to the new Sovereign.

The Sovereign is now self-sufficient. It has:
- Its own Crossplane managing further infrastructure.
- Its own OpenBao for secrets.
- Its own JetStream as event spine.
- Its own Keycloak for users.
- Its own Gitea (with mirror of the public Blueprint catalog).
- Its own Catalyst control plane.

---

## 5. Phase 2 — Day-1 setup

The first `sovereign-admin` logs into `console.<sovereign>.<domain>`:

```
Day-1 actions
──────────────────────────────────────────────────────────────────
1. Configure cert-manager issuers (Let's Encrypt / corporate CA).
2. Configure backup destination (cloud object storage for Velero).
3. Configure Harbor with image-scanning policies.
4. (Optional) Federate Keycloak's catalyst-operator realm to corporate IdP.
5. (Optional) Configure observability exports (SIEM, datadog, etc.).
6. Onboard the first Organization:
     Catalyst console → Admin → Organizations → New
     Provide: name, contact, plan.
   Workspace-controller does NOT create vclusters yet.
   They are created when the first Environment is provisioned.
7. Create the first Environment in that Organization:
     Console → switch to Org context → Environments → New
     Workspace-controller spins up a vcluster on the chosen host cluster.
     Bootstraps Flux inside, creates Gitea repo, wires webhook.
     Ready in ~60 seconds.
```

---

## 6. Phase 3 — Steady-state operation

From here on, the Sovereign runs autonomously. Sovereign-admins use the Catalyst admin UI for:

- Onboarding more Organizations
- Adding host clusters in new regions (Crossplane provisions them, workspace-controller adopts them)
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
  ingress, WAF, k8gb                ingress, WAF, k8gb              ingress, WAF, k8gb
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

Existing Applications with `placement: active-active: false, single-region` do not migrate automatically. To extend an existing Application to the new region, the user explicitly updates the Placement on the Application — that's a one-line edit in the Environment Gitea repo (or a click in Topology tab).

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
       - All Environment Gitea repos (cloned + bundled)
       - All private Blueprint repos
       - Keycloak realm export (users, federated identities)
       - OpenBao export (sealed secrets only)
3. On bankdhofar Sovereign: Admin → Organization → Import
     Workspace-controller recreates Environments → vclusters.
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
