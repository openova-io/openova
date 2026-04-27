# OpenOva Naming Convention

**Status:** Authoritative | **Updated:** 2026-04-27

This document defines the unified naming standard for all OpenOva infrastructure, platform, and application resources across all cloud providers, regions, and Catalyst Sovereigns. All new resources **must** follow this convention. Existing resources adopt the new names when touched.

> **Glossary**: see [`GLOSSARY.md`](GLOSSARY.md). This document deals with how to compose names from the dimensions defined there.

---

## 1. Principles

### 1.1 Dimension-Based Naming

Every name is a **composition of typed dimensions** — never free-text, never descriptive prose. Each dimension has a defined abbreviation. Names are deterministic: given the dimensions, the name is computable.

### 1.2 Don't Repeat the Parent

When an object lives inside a container that already encodes location, **do not repeat** that information.

```
Parent provides context             →  Child adds only what is NEW

Provider + Region (cloud account)   →  VPC/Network name = bb + env_type
VPC                                 →  Subnet/SG/Route name = purpose only
K8s Cluster                         →  vcluster name = org only
                                       Namespace = env_type + app (or app only)
vcluster                            →  Namespace = app only (when 1 vcluster = 1 Environment)
Namespace                           →  Secret/ConfigMap/Deployment = purpose only

No parent (global scope)            →  Full encoding required
  DNS names, K8s contexts, Crossplane CRs, server names
```

### 1.3 Building Blocks, Not Failover Roles

Clusters are named by their **functional security zone** (building block), not by a failover role such as "primary" or "dr". Geographic redundancy is achieved by running the **same building blocks in multiple regions** — k8gb and GSLB handle traffic distribution. The cluster name never changes; the routing does. Calling a cluster "primary" is operationally incorrect because after failover the other region becomes active — the building block label remains stable regardless.

### 1.4 Tags Carry What Names Cannot

Resource names are kept minimal. Cloud tags and Kubernetes labels carry the **full context** for cross-cloud dashboards, billing, and compliance audits.

### 1.5 Organization Identity Lives in the vcluster Layer

In multi-tenant deployments, Organization identity is expressed through the **vcluster name** (and on the host cluster, the Kubernetes namespace that hosts that vcluster) — never embedded in resource names below. This follows Principle 1.2: the vcluster is the parent that provides Organization context.

---

## 2. Dimension Taxonomy

### 2.1 Provider

| Full name | 2-char | 1-char |
|-----------|--------|--------|
| Hetzner   | `hz`   | `h`    |
| Huawei Cloud | `hw` | `w`  |
| OCI (Oracle Cloud) | `oci` | `o` |
| AWS       | `aws`  | `a`    |
| GCP       | `gcp`  | `g`    |
| Azure     | `az`   | `z`    |
| Contabo   | `ct`   | `c`    |

### 2.2 Region

Region codes are **provider-scoped**. The same 3-char code is never reused across providers.

#### Hetzner

| Location | 3-char | 1-char | Notes |
|----------|--------|--------|-------|
| Falkenstein, DE | `fsn` | `f` | |
| Nuremberg, DE   | `nbg` | `n` | |
| Helsinki, FI    | `hel` | `l` | `h` reserved for Hetzner provider |
| Ashburn, VA, US | `ash` | `a` | |
| Hillsboro, OR, US | `hil` | `i` | `h` reserved → use `i` |
| Singapore, SG   | `sin` | `s` | |

#### Huawei Cloud

| Location | 3-char | 1-char |
|----------|--------|--------|
| AP Southeast (Singapore) | `apse` | `p` |
| CN North (Beijing)       | `cnn`  | `c` |
| LA South (São Paulo)     | `las`  | `q` |
| ME (Riyadh)              | `mer`  | `r` |

#### OCI

| Location | 3-char | 1-char |
|----------|--------|--------|
| ME Dubai         | `dxb` | `x` |
| EU Frankfurt     | `fra` | `r` |
| AP Singapore     | `sg`  | `g` |
| US Ashburn       | `iad` | `d` |
| AP Sydney        | `syd` | `y` |

#### Contabo (legacy)

| Location | 3-char | 1-char |
|----------|--------|--------|
| EU (generic) | `eu` | `e` |

> **Collision rule**: the provider's 1-char code takes precedence. Region codes must not reuse the provider's 1-char. The table above already resolves all known collisions.

### 2.3 Building Block

Building blocks describe the **security zone** the cluster or resource belongs to. This is stable regardless of which region is serving traffic.

| Full name | 3-char | 1-char | Purpose |
|-----------|--------|--------|---------|
| Restricted Trust Zone | `rtz` | `r` | Production workloads — most restricted, no direct internet exposure |
| DMZ (edge) | `dmz` | `d` | Internet-facing — WAF, ingress controllers, WireGuard endpoints |
| Management | `mgt` | `m` | Catalyst control plane — console, projector, gitea, JetStream, OpenBao, Keycloak, etc. |

### 2.4 Env Type

Renamed from the older `{env}` to avoid collision with the user-facing **Environment** object (see §11). Values unchanged.

| Full name   | 3-char | 1-char |
|-------------|--------|--------|
| Production  | `prod` | `p`    |
| Staging     | `stg`  | `s`    |
| UAT         | `uat`  | `u`    |
| Development | `dev`  | `d`    |
| POC         | `poc`  | `c`    |

### 2.5 Organization

| Field | Rule |
|---|---|
| Format | Lowercase slug, hyphenated. Length 3–32 characters. Must match `^[a-z][a-z0-9-]{2,31}$`. |
| Reserved | `system`, `flux`, `crossplane`, `catalyst`, `gitea`, `kube-*`, anything matching a provider/region/bb/env_type code. |
| Examples | `acme`, `bankdhofar`, `muscatpharmacy`, `omantel-internal` |
| Source of truth | The Organization CRD on the Sovereign's management cluster. |

---

## 3. Core Patterns

All **global** objects (no containing parent that encodes location) use the full pattern:

```
{provider}-{region}-{bb}-{env_type}
```

All **scoped** objects use only what the parent does not already provide — see §4.

The **Catalyst Environment** (logical, user-facing) uses:

```
{org}-{env_type}
```

See §11 for the Environment object definition.

---

## 4. Object-Type Reference

### 4.1 Global Objects (full encoding always required)

| Object | Pattern | Example |
|--------|---------|---------|
| K8s cluster context | `{prov}-{reg}-{bb}-{env_type}` | `hz-fsn-rtz-prod` |
| Server / VM | `{prov}{reg}{bb}-{app}-{#}{env_type}` | `hzfsnr-k8s-1p` |
| DNS location code | `{p}{r}{b}{e}` (4 chars) | `hfrp` |
| Crossplane CR (on mgt plane) | `{prov}-{reg}-{bb}-{env_type}-{type}` | `hz-fsn-rtz-prod-vpc` |
| Flux GitRepository (Sovereign-level) | `{prov}-{reg}-{bb}-{env_type}` | `hz-fsn-rtz-prod` |

### 4.2 Within Provider + Region (don't repeat provider/region)

| Object | Pattern | Example | Parent context |
|--------|---------|---------|----------------|
| VPC / Network | `{bb}-{env_type}` | `rtz-prod`, `dmz-prod` | provider + region |
| Cloud SSH Key | `{purpose}-{env_type}` | `cluster-prod` | provider + region |
| Load Balancer | `{purpose}-{env_type}` | `ingress-prod` | provider + region |
| Floating IP / EIP | `{purpose}-{env_type}` | `ingress-prod` | provider + region |
| Object storage bucket | `{env_type}-{purpose}` | `prod-tf-state` | provider + region |
| Volume snapshot policy | `{purpose}` | `daily-7d` | provider + region |

### 4.3 Within VPC / Network (don't repeat provider/region/vpc)

| Object | Pattern | Example |
|--------|---------|---------|
| Subnet | `{purpose}` | `workers`, `lb`, `cp` |
| Security Group / Firewall Rule | `{purpose}` | `k8s-nodes`, `lb-https` |
| Route Table | `{purpose}` | `default`, `nat` |
| NAT Gateway | `{purpose}` | `default` |
| VPC Peering / Network Attachment | `to-{target-bb}` | `to-dmz`, `to-mgt` |

### 4.4 Within K8s Cluster (host level)

The **host cluster** (`{prov}-{reg}-{bb}-{env_type}`) hosts one vcluster per Organization plus Catalyst control-plane workloads.

| Object | Pattern | Example |
|--------|---------|---------|
| Catalyst control-plane Namespace | `catalyst-{component}` | `catalyst-projector`, `catalyst-gitea` |
| Per-Org vcluster hosting Namespace | `{org}` | `acme`, `bankdhofar` |
| Helm release (Catalyst level) | `{component}` | `external-secrets`, `cert-manager` |
| Flux Kustomization (Catalyst level) | `{scope}` | `infrastructure`, `catalyst`, `crossplane` |
| Certificate | `{domain-purpose}` | `openova-io-wildcard` |
| ServiceAccount | `{role}` | `flux-reconciler`, `projector` |
| PVC | `{purpose}` | `data`, `wal` |

### 4.5 Within Namespace (don't repeat anything above)

| Object | Pattern | Example |
|--------|---------|---------|
| Secret | `{purpose}` | `db-credentials`, `hcloud-token` |
| ConfigMap | `{purpose}` | `app-config`, `grafana-dashboards` |
| Deployment / StatefulSet | `{component}` | `api`, `worker`, `ui` |
| Service | `{component}` | `api`, `grpc` |
| NetworkPolicy | `{rule}` | `deny-all`, `allow-ingress` |
| Ingress / IngressRoute | `{component}` | `api`, `ui` |

### 4.6 Multi-Organization on a Sovereign Management Cluster

The management cluster (`{prov}-{reg}-mgt-{env_type}`) hosts Crossplane and Catalyst components. Per-Organization isolation lives in the **vcluster** layer (see §4.7), not in resource names below it.

```
Host cluster: hz-fsn-mgt-prod
  Namespace: catalyst-projector       ← Catalyst control-plane component
  Namespace: catalyst-gitea
  Namespace: acme                     ← parent namespace for Org acme's resources
    vcluster: acme                    ← (see §4.7)
    Crossplane CR: hz-fsn-rtz-prod-vpc  ← no Organization slug in CR name; namespace is parent
    Secret: hcloud-token              ← purpose only; namespace provides Org context
  Namespace: bankdhofar
    vcluster: bankdhofar
    ...
```

Organization identity is **never** embedded in the names of resources below the namespace. The namespace is the parent.

### 4.7 vcluster Naming (NEW)

The vcluster is the per-Organization control plane on a parent host cluster. One vcluster per Organization per host cluster.

| Object | Pattern | Example |
|--------|---------|---------|
| vcluster (within host cluster) | `{org}` | `acme`, `bankdhofar`, `muscatpharmacy` |
| vcluster fully-qualified ref (cross-cluster, kubeconfig context) | `{prov}-{reg}-{bb}-{env_type}-{org}` | `hz-fsn-rtz-prod-acme`, `hz-hel-rtz-prod-acme` |
| Flux GitRepository inside vcluster | `environment` | `environment` |
| Flux Kustomization inside vcluster | `applications` | `applications` |

**Sibling vclusters** named `acme` on `hz-fsn-rtz-prod` and on `hz-hel-rtz-prod` are two physical realizations of the same logical Catalyst Environment `acme-prod` (see §11).

### 4.8 Within a vcluster (per-Application namespace)

Inside an Organization's vcluster, each Application gets its own namespace.

| Object | Pattern | Example |
|--------|---------|---------|
| Application namespace | `{app}` | `marketing-site`, `blog`, `shared-postgres` |
| All workloads, secrets, configmaps inside | `{component}` / `{purpose}` | `api`, `worker`, `db-credentials` |

---

## 5. DNS Pattern

### 5.1 Structure

Two patterns coexist depending on whether the DNS is for **Catalyst control-plane** services or for **Application** endpoints inside an Organization.

#### Catalyst control-plane DNS (operator domain)

```
{component}.{location-code}.{sovereign-domain}
```

Example: `console.hfmp.openova.io`, `gitea.hfmp.openova.io`.

Used for Catalyst's own services on the management cluster of a Sovereign. The location code is a 4-character dense encoding of provider + region + building-block + env_type, derived from the 1-char columns in §2.

#### Application DNS (Environment domain)

```
{app}.{environment}.{sovereign-domain}
```

OR, for white-label Sovereigns (corporate self-host):

```
{app}.{environment}.{org-domain}
```

Examples:
- `marketing-site.acme-prod.omantel.openova.io` (acme on Omantel Sovereign)
- `marketing-site.acme-prod.acme.com` (acme on its own Sovereign with their own domain)
- `blog.acme-prod.omantel.openova.io` (second App in same Environment)

The Sovereign's `sovereign-domain` is set at provisioning time; corporate Sovereigns typically rebrand to their own domain.

### 5.2 Location Code Lookup Table

| Location code | Provider | Region | Building Block | Env Type | Example DNS |
|---------------|----------|--------|----------------|----------|-------------|
| `hfrp` | Hetzner | Falkenstein | rtz | prod | `console.hfrp.openova.io` |
| `hfrd` | Hetzner | Falkenstein | rtz | dev | `console.hfrd.openova.io` |
| `hfdp` | Hetzner | Falkenstein | dmz | prod | `ingress.hfdp.openova.io` |
| `hfmp` | Hetzner | Falkenstein | mgt | prod | `gitea.hfmp.openova.io` |
| `hlrp` | Hetzner | Helsinki | rtz | prod | `console.hlrp.openova.io` |
| `hldp` | Hetzner | Helsinki | dmz | prod | `ingress.hldp.openova.io` |
| `hnrp` | Hetzner | Nuremberg | rtz | prod | `console.hnrp.openova.io` |
| `hnmp` | Hetzner | Nuremberg | mgt | prod | `console.hnmp.openova.io` |
| `hnmd` | Hetzner | Nuremberg | mgt | dev | `console.hnmd.openova.io` |
| `harp` | Hetzner | Ashburn | rtz | prod | `console.harp.openova.io` |
| `hsrp` | Hetzner | Singapore | rtz | prod | `console.hsrp.openova.io` |
| `wprp` | Huawei | AP Southeast | rtz | prod | `console.wprp.customer.io` |
| `oxrp` | OCI | Dubai | rtz | prod | `console.oxrp.customer.io` |
| `orrp` | OCI | Frankfurt | rtz | prod | `console.orrp.customer.io` |

> To derive a code not listed: concatenate the four 1-char codes from the dimension tables in §2. If a collision exists, it will already appear in this table with a resolution. Do not invent new collision resolutions — raise a PR to extend this table.

### 5.3 Coexistence During Migration

Old names remain as CNAMEs until all consumers have migrated:

```dns
# Old name (CNAME → new)
old-service.openova.io          CNAME  new-service.hfmp.openova.io

# New name (real A/AAAA record)
new-service.hfmp.openova.io     A      <ip>
```

---

## 6. Tags and Labels

Since resource names are minimal (a VPC named `rtz-prod`, not `hz-fsn-rtz-prod`), tags carry the full context.

### 6.1 Cloud Resource Tags (all providers)

```yaml
openova.io/provider: hetzner
openova.io/region: fsn
openova.io/building-block: rtz
openova.io/env-type: prod                # renamed from environment
openova.io/cluster: hz-fsn-rtz-prod       # the host cluster this resource belongs to
openova.io/managed-by: catalyst           # or: crossplane, opentofu (bootstrap only), manual
```

### 6.2 Catalyst Resource Tags (vcluster + Environment context)

For workloads running **inside** a vcluster:

```yaml
openova.io/sovereign: omantel                # which Sovereign hosts this
openova.io/organization: acme
openova.io/environment: acme-prod            # Catalyst Environment object
openova.io/vcluster: acme                    # vcluster name within parent host cluster
openova.io/host-cluster: hz-fsn-rtz-prod
openova.io/application: marketing-site       # Application instance name
openova.io/blueprint: bp-wordpress           # source Blueprint
openova.io/blueprint-version: 1.3.0
```

### 6.3 Kubernetes Resource Labels (all objects)

```yaml
metadata:
  labels:
    app.kubernetes.io/managed-by: flux       # or: helm, kustomize
    app.kubernetes.io/component: grafana
    openova.io/building-block: rtz
    openova.io/env-type: prod
```

---

## 7. Multi-Region Architecture and Building Block Symmetry

Geographic redundancy is achieved by deploying **the same building blocks in multiple regions**. Both clusters carry the same building block label; neither is designated "primary" or "dr". Traffic distribution is a routing concern owned by k8gb and GSLB — not a naming concern.

```
Region A (Falkenstein)              Region B (Helsinki)
────────────────────────            ──────────────────────────
hz-fsn-rtz-prod                     hz-hel-rtz-prod
  vcluster: acme                      vcluster: acme              ← Catalyst Environment acme-prod
  vcluster: bankdhofar                vcluster: bankdhofar         ← Environment bankdhofar-prod
    (each vcluster has its own        (each vcluster has its own
     Flux watching its Environment    Flux watching its Environment
     Gitea repo)                      Gitea repo)

hz-fsn-dmz-prod                     hz-hel-dmz-prod
  Ingress + WAF                       Ingress + WAF
  WireGuard endpoint                  WireGuard endpoint

              ↕ k8gb authoritative DNS (per Application)
        marketing-site.acme-prod.omantel.openova.io
              both regions registered — k8gb selects healthy endpoint

Management (one per Sovereign, single region recommended)
────────────────────────────────────────────────────────────
hz-nbg-mgt-prod
  Catalyst control plane (console, projector, marketplace, admin,
                          catalog-svc, blueprint-controller,
                          environment-controller)
  Gitea (Blueprint mirror + per-Org workspaces)
  NATS JetStream (event spine, per-Org accounts)
  OpenBao (secrets — one cluster here; sibling clusters in workload regions
           sync via async perf replication; see SECURITY.md)
  Keycloak (per-Org realms in SME-style; per-Sovereign realm in corporate)
  Flux (GitOps for Catalyst itself)
  Crossplane (manages workload clusters and cloud resources)
```

When FSN becomes unavailable, `hz-hel-rtz-prod` serves all traffic for Applications with `placement: active-active` or `active-hotstandby`. The cluster name does not change. k8gb removes the FSN endpoint from DNS. Recovery is a routing event, not a renaming event.

---

## 8. OpenOva Own Sovereign Naming

OpenOva's own deployed Sovereign (the one hosting our SaaS Organizations — formerly called "Nova") follows the same convention as any other Sovereign.

| Object | Current name | Canonical name | Notes |
|--------|-------------|----------------|-------|
| Bootstrap K8s context (legacy) | `contabo-mkt` | `ct-eu-mgt-prod` | Adopted as alias for the existing Contabo VPS |
| Future management cluster | — | `hz-nbg-mgt-prod` | After Hetzner migration |
| Catalyst console (current) | — | `console.cemp.openova.io` | Contabo, EU, mgt, prod |
| Catalyst console (post-migration) | — | `console.hnmp.openova.io` | Hetzner, Nuremberg, mgt, prod |

The existing cluster `contabo-mkt` is adopted as `ct-eu-mgt-prod` immediately in kubeconfig and documentation. The directory path `clusters/contabo-mkt/` migrates to `clusters/hz-nbg-mgt-prod/` when the bootstrap machine itself is migrated. The bootstrap machine remains online indefinitely as `catalyst-provisioner` (used to bootstrap further Sovereigns).

---

## 9. Migration Rules

| Phase | Action |
|-------|--------|
| **Now** | All new resources use the canonical name |
| **On touch** | When modifying an existing resource for any reason, rename it |
| **DNS** | Add new name as real record; old name becomes CNAME pointing to new |
| **K8s contexts** | Add new context alias alongside old; update scripts and CI |
| **Directory paths** | Migrate `clusters/` and `infra/` directories at migration time |
| **Tag rename** | `openova.io/environment` → `openova.io/env-type` (single relabel pass during touch) |
| **Never rename** | Kubernetes namespace names on running clusters (would require full redeploy) |

---

## 10. Quick Reference — Derivation Algorithm

To name any new resource:

1. **Identify scope**: is this a global object (no parent encoding location), a host-cluster-scoped object, a vcluster-scoped object, or a namespace-scoped object?
2. **If global**: compose `{provider}-{region}-{bb}-{env_type}` from the dimension tables.
3. **If scoped**: start from the innermost scope and add only the dimensions the parent does not already provide. Use `{purpose}` at the deepest levels.
4. **If vcluster**: use `{org}` within the host cluster; use the qualified form `{prov}-{reg}-{bb}-{env_type}-{org}` for cross-cluster references.
5. **If a Catalyst Environment**: use `{org}-{env_type}` (see §11).
6. **If DNS**: derive the 4-char location code from the 1-char columns; check the lookup table in §5.2. For Application DNS use `{app}.{environment}.{sovereign-or-org-domain}`.
7. **Always**: add the full tag set from §6 to the resource.
8. **If uncertain**: raise a PR — do not invent ad-hoc names.

---

## 11. Catalyst Environment (User-Facing Object)

The **Environment** is the user-facing scope where Applications are installed. Logical concept; one Environment can be realized by multiple vclusters across regions and building blocks.

### 11.1 Naming

```
{org}-{env_type}
```

Examples: `acme-prod`, `acme-dev`, `bankdhofar-prod`, `bankdhofar-dr`, `muscatpharmacy-prod`.

### 11.2 Realization

An Environment is realized by:

1. **One Gitea repo** in the Sovereign's Gitea: `<sovereign-gitea>/{org}/{org}-{env_type}` (e.g. `gitea.omantel.openova.io/acme/acme-prod`). This is the single source of truth for the Environment's manifests.
2. **One or more vclusters** (`{org}` named on each parent host cluster). The set of host clusters realizing the Environment is determined by the Environment's Placement spec.
3. **One Flux per vcluster**, all watching the same Environment Gitea repo. Each Flux applies manifests filtered to its region/building block via `kustomization.yaml` selectors.
4. **One JetStream Account** scoped to `ws.{org}-{env_type}.>` for event traffic.
5. **One projector consumer-group** materializing per-Environment KV state for the console.
6. **One OpenBao path** rooted at `org/{org}/env/{env_type}/`.

### 11.3 Single-region vs multi-region

| Mode | Vclusters | Notes |
|---|---|---|
| Single-region | 1 vcluster on one rtz cluster | SME default. No cross-region failover. |
| Multi-region | N vclusters across regions × bb | Corporate / regulated default. k8gb routes Application traffic. |

The Environment object's spec drives which vclusters get created; `environment-controller` (the Catalyst component) reconciles them.

### 11.4 Why a separate object instead of a tag?

- It owns its own Git repo (a tag couldn't).
- It owns Placement metadata (a tag couldn't).
- It is the unit of Application install/uninstall/promotion.
- Renaming it would break Git history and Flux state — naming is therefore stable for the lifetime of the Environment.

---

*Authoritative. Cross-reference [`GLOSSARY.md`](GLOSSARY.md) for definitions.*
