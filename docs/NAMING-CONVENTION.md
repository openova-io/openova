# OpenOva Naming Convention

**Status:** Authoritative | **Updated:** 2026-03-19

This document defines the unified naming standard for all OpenOva infrastructure, platform, and application resources across all cloud providers, regions, and environments. All new resources **must** follow this convention. Existing resources adopt the new names when touched.

---

## 1. Principles

### 1.1 Dimension-Based Naming

Every name is a **composition of typed dimensions** — never free-text, never descriptive prose. Each dimension has a defined abbreviation. Names are deterministic: given the dimensions, the name is computable.

### 1.2 Don't Repeat the Parent

When an object lives inside a container that already encodes location, **do not repeat** that information.

```
Parent provides context         →  Child adds only what is NEW

Provider + Region (cloud acct)  →  VPC/Network name = bb + env
VPC                             →  Subnet/SG/Route name = purpose only
K8s Cluster                     →  Namespace = env + app (or app only)
Namespace                       →  Secret/ConfigMap/Deployment = purpose only

No parent (global scope)        →  Full encoding required
  DNS names, K8s contexts, Crossplane CRs, server names
```

### 1.3 Building Blocks, Not Failover Roles

Clusters are named by their **functional security zone** (building block), not by a failover role such as "primary" or "dr". Geographic redundancy is achieved by running the **same building blocks in multiple regions** — k8gb and GSLB handle traffic distribution. The cluster name never changes; the routing does. Calling a cluster "primary" is operationally incorrect because after failover the other region becomes active — the building block label remains stable regardless.

### 1.4 Tags Carry What Names Cannot

Resource names are kept minimal. Cloud tags and Kubernetes labels carry the **full context** for cross-cloud dashboards, billing, and compliance audits.

### 1.5 No Tenant Identity in Resource Names

In multi-tenant deployments, tenant identity is expressed through the **Kubernetes namespace** on the management cluster — never embedded in resource names. This follows Principle 1.2: the namespace is the parent that provides tenant context.

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
| Management | `mgt` | `m` | Platform control plane — Catalyst, CI/CD, GitOps, observability |

### 2.4 Environment

| Full name   | 3-char | 1-char |
|-------------|--------|--------|
| Production  | `prod` | `p`    |
| Staging     | `stg`  | `s`    |
| UAT         | `uat`  | `u`    |
| Development | `dev`  | `d`    |
| POC         | `poc`  | `c`    |

---

## 3. Core Pattern

All **global** objects (no containing parent that encodes location) use the full pattern:

```
{provider}-{region}-{bb}-{env}
```

All **scoped** objects use only what the parent does not already provide — see Section 4.

---

## 4. Object-Type Reference

### 4.1 Global Objects (full encoding always required)

| Object | Pattern | Example |
|--------|---------|---------|
| K8s cluster context | `{prov}-{reg}-{bb}-{env}` | `hz-fsn-rtz-prod` |
| Server / VM | `{prov}{reg}{bb}-{app}-{#}{env}` | `hzfsnr-k8s-1p` |
| DNS location code | `{p}{r}{b}{e}` (4 chars) | `hfrp` |
| Crossplane CR (on mgt plane) | `{prov}-{reg}-{bb}-{env}-{type}` | `hz-fsn-rtz-prod-vpc` |
| Flux GitRepository | `{prov}-{reg}-{bb}-{env}` | `hz-fsn-rtz-prod` |

### 4.2 Within Provider + Region (don't repeat provider/region)

| Object | Pattern | Example | Parent context |
|--------|---------|---------|----------------|
| VPC / Network | `{bb}-{env}` | `rtz-prod`, `dmz-prod` | provider + region |
| Cloud SSH Key | `{purpose}-{env}` | `cluster-prod` | provider + region |
| Load Balancer | `{purpose}-{env}` | `ingress-prod` | provider + region |
| Floating IP / EIP | `{purpose}-{env}` | `ingress-prod` | provider + region |
| Object storage bucket | `{env}-{purpose}` | `prod-tf-state` | provider + region |
| Volume snapshot policy | `{purpose}` | `daily-7d` | provider + region |

### 4.3 Within VPC / Network (don't repeat provider/region/vpc)

| Object | Pattern | Example |
|--------|---------|---------|
| Subnet | `{purpose}` | `workers`, `lb`, `cp` |
| Security Group / Firewall Rule | `{purpose}` | `k8s-nodes`, `lb-https` |
| Route Table | `{purpose}` | `default`, `nat` |
| NAT Gateway | `{purpose}` | `default` |
| VPC Peering / Network Attachment | `to-{target-bb}` | `to-dmz`, `to-mgt` |

### 4.4 Within K8s Cluster (don't repeat provider/region/bb/env)

| Object | Pattern | Example |
|--------|---------|---------|
| Namespace | `{app}` (single-env cluster) or `{env}-{app}` (multi-env) | `grafana`, `dev-fingate` |
| Helm release | `{app}` | `external-secrets` |
| Flux Kustomization | `{scope}` | `infrastructure`, `apps` |
| Certificate | `{domain-purpose}` | `openova-io-wildcard` |
| ServiceAccount | `{role}` | `flux-reconciler` |
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

### 4.6 Multi-Tenant on Management Cluster (Catalyst SaaS)

The management cluster (`{prov}-{reg}-mgt-{env}`) hosts Crossplane and manages customer clusters. Tenant isolation uses **namespace as parent**:

```
Namespace:    {customer-slug}          ← tenant boundary (e.g., acme-corp)
  Crossplane CR: hz-fsn-rtz-prod-vpc  ← no customer in name; namespace is parent
  Crossplane CR: hz-hel-rtz-prod-vpc
  Secret:        hcloud-token          ← purpose only; namespace provides tenant context
```

Customer identity is **never** embedded in resource names. The namespace is the parent.

---

## 5. DNS Pattern

### 5.1 Structure

```
{app}.{location-code}.{domain}
```

The **location code** is a 4-character dense encoding of provider + region + building-block + environment, derived from the 1-char columns in Section 2.

### 5.2 Location Code Lookup Table

| Location code | Provider | Region | Building Block | Environment | Example DNS |
|---------------|----------|--------|----------------|-------------|-------------|
| `hfrp` | Hetzner | Falkenstein | rtz | prod | `grafana.hfrp.openova.io` |
| `hfrd` | Hetzner | Falkenstein | rtz | dev | `catalyst.hfrd.openova.io` |
| `hfdp` | Hetzner | Falkenstein | dmz | prod | `ingress.hfdp.openova.io` |
| `hfmp` | Hetzner | Falkenstein | mgt | prod | `flux.hfmp.openova.io` |
| `hlrp` | Hetzner | Helsinki | rtz | prod | `grafana.hlrp.openova.io` |
| `hldp` | Hetzner | Helsinki | dmz | prod | `ingress.hldp.openova.io` |
| `hnrp` | Hetzner | Nuremberg | rtz | prod | `grafana.hnrp.openova.io` |
| `hnmp` | Hetzner | Nuremberg | mgt | prod | `catalyst.hnmp.openova.io` |
| `hnmd` | Hetzner | Nuremberg | mgt | dev | `catalyst.hnmd.openova.io` |
| `hasrp`| Hetzner | Ashburn | rtz | prod | `grafana.harp.openova.io` |
| `hsrp` | Hetzner | Singapore | rtz | prod | `grafana.hsrp.openova.io` |
| `wprp` | Huawei | AP Southeast | rtz | prod | `grafana.wprp.customer.io` |
| `oxrp` | OCI | Dubai | rtz | prod | `grafana.oxrp.customer.io` |
| `orrp` | OCI | Frankfurt | rtz | prod | `grafana.orrp.customer.io` |

> To derive a code not listed: concatenate the four 1-char codes from the dimension tables in Section 2. If a collision exists, it will already appear in this table with a resolution. Do not invent new collision resolutions — raise a PR to extend this table.

### 5.3 Coexistence During Migration

Old names remain as CNAMEs until all consumers have migrated:

```dns
# Old name (CNAME → new)
old-service.openova.io    CNAME  new-service.hfmp.openova.io

# New name (real A/AAAA record)
new-service.hfmp.openova.io    A    <ip>
```

---

## 6. Tags and Labels

Since resource names are minimal (a VPC named `rtz-prod`, not `hz-fsn-rtz-prod`), tags carry the full context.

### 6.1 Cloud Resource Tags (all providers)

```yaml
openova.io/provider: hetzner
openova.io/region: fsn
openova.io/building-block: rtz
openova.io/environment: prod
openova.io/managed-by: catalyst          # or: terraform, crossplane, manual
openova.io/cluster: hz-fsn-rtz-prod      # the cluster this resource belongs to
```

### 6.2 Kubernetes Resource Labels (all objects)

```yaml
metadata:
  labels:
    app.kubernetes.io/managed-by: flux       # or: helm, kustomize
    app.kubernetes.io/component: grafana
    openova.io/building-block: rtz
    openova.io/environment: prod
```

---

## 7. Multi-Region Architecture and Building Block Symmetry

Geographic redundancy is achieved by deploying **the same building blocks in multiple regions**. Both clusters carry the same building block label; neither is designated "primary" or "dr". Traffic distribution is a routing concern owned by k8gb and GSLB — not a naming concern.

```
Region A (Falkenstein)              Region B (Helsinki)
────────────────────────            ──────────────────────────
hz-fsn-rtz-prod                     hz-hel-rtz-prod
  Full workload stack                 Full workload stack
  CNPG standby replica                CNPG standby replica
  Valkey REPLICAOF                    Valkey REPLICAOF

hz-fsn-dmz-prod                     hz-hel-dmz-prod
  Ingress + WAF                       Ingress + WAF
  WireGuard endpoint                  WireGuard endpoint

              ↕ k8gb authoritative DNS
        grafana.hfrp.openova.io  AND  grafana.hlrp.openova.io
              both registered — k8gb selects healthy endpoint

Management (single, not replicated per region)
──────────────────────────────────────────────
hz-nbg-mgt-prod
  Catalyst (Bootstrap + Lifecycle Manager)
  Flux (GitOps source of truth)
  Harbor (container registry)
  Crossplane (manages hz-fsn-* and hz-hel-*)
```

When FSN becomes unavailable, `hz-hel-rtz-prod` serves all traffic. The cluster name does not change. k8gb removes the FSN endpoint from DNS. Recovery is a routing event, not a renaming event.

---

## 8. OpenOva Own Infrastructure Naming

OpenOva's own deployed infrastructure follows the same convention.

| Object | Current name | Canonical name | Notes |
|--------|-------------|----------------|-------|
| K8s context | `contabo-mkt` | `ct-eu-mgt-prod` | Contabo, EU, Management, Production |
| K8s context (after Hetzner migration) | — | `hz-nbg-mgt-prod` | Hetzner, Nuremberg, Management, Production |
| Catalyst UI | — | `catalyst.hnmp.openova.io` | After Hetzner migration |
| Axon gateway | — | `axon.hnmp.openova.io` | Runs on management cluster |

The existing cluster `contabo-mkt` is adopted as `ct-eu-mgt-prod` immediately in kubeconfig and documentation. The directory path `clusters/contabo-mkt/` migrates to `clusters/hz-nbg-mgt-prod/` when the server migrates to Hetzner.

---

## 9. Migration Rules

| Phase | Action |
|-------|--------|
| **Now** | All new resources use the canonical name |
| **On touch** | When modifying an existing resource for any reason, rename it |
| **DNS** | Add new name as real record; old name becomes CNAME pointing to new |
| **K8s contexts** | Add new context alias alongside old; update scripts and CI |
| **Directory paths** | Migrate `clusters/` and `infra/` directories at migration time |
| **Never rename** | Kubernetes namespace names on running clusters (would require full redeploy) |

---

## 10. Quick Reference — Derivation Algorithm

To name any new resource:

1. **Identify scope**: is this a global object (no parent encoding location) or a scoped object (lives inside something that already encodes location)?
2. **If global**: compose `{provider}-{region}-{bb}-{env}` from the dimension tables.
3. **If scoped**: start from the innermost scope and add only the dimensions the parent does not already provide. Use `{purpose}` at the deepest levels.
4. **If DNS**: derive the 4-char location code from the 1-char columns; check the lookup table in Section 5.2.
5. **Always**: add the full tag set from Section 6 to the resource.
6. **If uncertain**: raise a PR — do not invent ad-hoc names.
