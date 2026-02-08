# Crossplane

Day-2 cloud resource provisioning for OpenOva platform.

**Status:** Accepted | **Updated:** 2026-01-17

---

## Overview

Crossplane provides Kubernetes-native cloud resource provisioning for day-2 operations. Terraform handles initial bootstrap; Crossplane manages ongoing infrastructure.

---

## Architecture

```mermaid
flowchart TB
    subgraph K8s["Kubernetes"]
        subgraph Crossplane
            Controller[Crossplane Controller]
            Provider[Cloud Provider]
        end

        XR[Composite Resources]
        Claim[Claims]
    end

    subgraph Cloud["Cloud Provider"]
        Resources[Cloud Resources]
    end

    Claim --> XR
    XR --> Controller
    Controller --> Provider
    Provider --> Resources
```

---

## Terraform vs Crossplane

| Aspect | Terraform | Crossplane |
|--------|-----------|------------|
| Phase | Bootstrap (day-0/1) | Day-2+ operations |
| State | External state file | Kubernetes CRDs |
| Drift | Manual detection | Continuous reconciliation |
| Access | CI/CD pipeline | K8s RBAC |
| Lifecycle | Point-in-time | GitOps continuous |

**Decision:** Use Terraform for initial cluster bootstrap only. All subsequent infrastructure managed via Crossplane.

---

## Supported Providers

| Provider | Status | Crossplane Provider |
|----------|--------|---------------------|
| Hetzner Cloud | Available | hcloud |
| Huawei Cloud | Coming | huaweicloud |
| Oracle Cloud | Coming | oci |
| AWS | Coming | aws |
| GCP | Coming | gcp |
| Azure | Coming | azure |

---

## Configuration

### Provider Configuration

```yaml
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-hcloud
spec:
  package: xpkg.upbound.io/crossplane-contrib/provider-hcloud:v0.4.0
---
apiVersion: hcloud.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: Secret
    secretRef:
      namespace: crossplane-system
      name: hcloud-credentials
      key: token
```

### Composite Resource Definition

```yaml
apiVersion: apiextensions.crossplane.io/v1
kind: CompositeResourceDefinition
metadata:
  name: xdatabases.openova.io
spec:
  group: openova.io
  names:
    kind: XDatabase
    plural: xdatabases
  versions:
    - name: v1alpha1
      served: true
      referenceable: true
      schema:
        openAPIV3Schema:
          type: object
          properties:
            spec:
              type: object
              properties:
                size:
                  type: string
                  enum: [small, medium, large]
```

### Composition

```yaml
apiVersion: apiextensions.crossplane.io/v1
kind: Composition
metadata:
  name: database.hcloud.openova.io
spec:
  compositeTypeRef:
    apiVersion: openova.io/v1alpha1
    kind: XDatabase
  resources:
    - name: server
      base:
        apiVersion: hcloud.crossplane.io/v1alpha1
        kind: Server
        spec:
          forProvider:
            serverType: cx21
            image: ubuntu-22.04
```

---

## GitOps Integration

Crossplane resources are managed via Flux:

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: crossplane
  namespace: flux-system
spec:
  interval: 10m
  sourceRef:
    kind: GitRepository
    name: crossplane
  path: ./deploy/prod
  prune: true
```

---

## Backstage Integration

Crossplane compositions appear as Backstage software templates, enabling self-service infrastructure provisioning through the IDP.

---

*Part of [OpenOva](https://openova.io)*
