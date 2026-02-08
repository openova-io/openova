# External Secrets Operator

Secrets management with ESO + Vault using 100% PushSecrets architecture.

**Status:** Accepted | **Updated:** 2026-02-07

---

## Overview

External Secrets Operator (ESO) provides the Kubernetes-native interface for secrets management. Kubernetes Secrets are the **source of truth**, pushed to external backends.

**Critical:** SOPS is completely eliminated. No secrets in Git, ever.

```mermaid
flowchart LR
    subgraph K8s["Kubernetes"]
        Gen[ESO Generators]
        KS[K8s Secrets]
        PS[PushSecrets]
        ES[ExternalSecrets]
    end

    subgraph Vault["Secrets Backend"]
        VL[Vault]
    end

    Gen -->|"generate"| KS
    KS -->|"source"| PS
    PS -->|"push"| VL
    VL -->|"pull"| ES
    ES -->|"sync"| KS
```

> **See Also:** [ADR-VAULT](../vault/docs/ADR-VAULT.md) for complete secrets backend architecture including active-active multi-region sync.

---

## Key Principles

| Principle | Implementation |
|-----------|----------------|
| No secrets in Git | SOPS eliminated, interactive bootstrap |
| K8s is source of truth | All secrets originate as K8s Secrets |
| Push, not pull | PushSecrets push to backends |
| Multi-region sync | Push to both Vaults simultaneously |
| Auto-generation | ESO Generators create complex secrets |

---

## ESO Components

| Component | Purpose |
|-----------|---------|
| **ExternalSecret** | Pulls secrets from Vault into K8s |
| **PushSecret** | Pushes K8s Secrets to Vault(s) |
| **ClusterSecretStore** | Connection to secrets backend |
| **Generators** | Auto-generate passwords, UUIDs, tokens |

---

## Bootstrap Secrets Flow

```mermaid
sequenceDiagram
    participant Wizard as Bootstrap Wizard
    participant TF as Terraform
    participant Vault as Vault
    participant ESO as ESO

    Wizard->>TF: Enter cloud credentials
    TF->>TF: Create terraform.tfvars (local only)
    TF->>Vault: Provision & initialize
    Vault->>Wizard: Return unseal keys
    Note over Wizard: Operator saves unseal keys offline
    ESO->>Vault: Connect via K8s auth
```

---

## Configuration

### ClusterSecretStore (Vault)

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ClusterSecretStore
metadata:
  name: vault-region1
spec:
  provider:
    vault:
      server: "https://vault.<domain>"
      path: "secret"
      version: "v2"
      auth:
        kubernetes:
          mountPath: "kubernetes"
          role: "external-secrets"
          serviceAccountRef:
            name: external-secrets
            namespace: external-secrets
```

### ExternalSecret Template

```yaml
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: <service>-secrets
  namespace: <tenant>-prod
spec:
  refreshInterval: 1h
  secretStoreRef:
    name: vault-region1
    kind: ClusterSecretStore
  target:
    name: <service>-secrets
  data:
    - secretKey: DATABASE_URL
      remoteRef:
        key: <tenant>/postgres
        property: url
    - secretKey: API_KEY
      remoteRef:
        key: <tenant>/api-keys
        property: main
```

### PushSecret to Multiple Vaults

```yaml
apiVersion: external-secrets.io/v1alpha1
kind: PushSecret
metadata:
  name: push-db-credentials
  namespace: databases
spec:
  secretStoreRefs:
    - name: vault-region1
      kind: ClusterSecretStore
    - name: vault-region2
      kind: ClusterSecretStore
  selector:
    secret:
      name: db-credentials
  data:
    - match:
        secretKey: password
        remoteRef:
          remoteKey: databases/db-credentials
          property: password
```

---

## ESO Generators

ESO Generators create complex secrets automatically, eliminating manual password creation.

### Password Generator

```yaml
apiVersion: generators.external-secrets.io/v1alpha1
kind: Password
metadata:
  name: db-password-generator
  namespace: databases
spec:
  length: 32
  digits: 6
  symbols: 4
  noUpper: false
  allowRepeat: true
---
apiVersion: external-secrets.io/v1beta1
kind: ExternalSecret
metadata:
  name: db-password
  namespace: databases
spec:
  refreshInterval: "0"  # Generate once, never refresh
  target:
    name: db-credentials
    creationPolicy: Owner
  dataFrom:
    - sourceRef:
        generatorRef:
          apiVersion: generators.external-secrets.io/v1alpha1
          kind: Password
          name: db-password-generator
```

### Available Generator Types

| Generator | Use Case |
|-----------|----------|
| Password | Database passwords, API keys |
| UUID | Unique identifiers |
| ECRAuthorizationToken | AWS ECR tokens |
| GCRAccessToken | GCP GCR tokens |
| ACRAccessToken | Azure ACR tokens |

---

## Gitea Token Management

Gitea access tokens for Flux are managed via ESO, following the same patterns as all other secrets.

### Bootstrap Creates Gitea Token

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: gitea-token
  namespace: flux-system
type: Opaque
data:
  username: Zm... # base64 encoded username
  password: Z2l... # base64 encoded Gitea access token
```

### Flux Uses Token

```yaml
apiVersion: source.toolkit.fluxcd.io/v1
kind: GitRepository
metadata:
  name: component
  namespace: flux-system
spec:
  url: https://gitea.<domain>/<org>/component.git
  secretRef:
    name: gitea-token  # ESO-managed
```

---

## Managed Secrets

| Secret | Purpose | Created By |
|--------|---------|------------|
| `gitea-token` | Flux access to Gitea | Bootstrap |
| `cloudflare-credentials` | ExternalDNS | Bootstrap |
| `hetzner-credentials` | Cloud provider | Bootstrap |
| `vault-unseal-keys` | Vault auto-unseal | Displayed once |
| `db-credentials` | Database passwords | ESO Generator |

---

## Secret Types

| Secret | Layer | Storage | Rotation |
|--------|-------|---------|----------|
| Cloud credentials | Bootstrap | Interactive (never stored) | On compromise |
| SSH keys | Bootstrap | Interactive (never stored) | On compromise |
| Vault unseal keys | Bootstrap | Offline backup | On compromise |
| Database passwords | K8s | ESO + Vault | 90 days |
| API keys | K8s | ESO + Vault | On compromise |
| JWT signing keys | K8s | ESO + Vault | 30 days |
| TLS certificates | K8s | cert-manager | Auto |
| Gitea tokens | K8s | ESO + Vault | 90 days |

---

## Why No SOPS?

| SOPS Approach | PushSecrets Approach |
|---------------|---------------------|
| Secrets encrypted in Git | No secrets in Git |
| Manual age key management | Vault handles encryption |
| Decrypt before apply | K8s Secret is source |
| Risk of leaked decrypted files | Secrets never on disk |

**Decision:** Interactive bootstrap is simpler and more secure than SOPS.

---

## Critical Backup

The ONLY manual backup required:

- **Vault unseal keys** - Displayed once during bootstrap
- Backup: Password manager + physical copy

**Warning:** Losing unseal keys makes Vault secrets unrecoverable.

---

## Migration from SOPS

If migrating from SOPS-based setup:

1. Create K8s Secrets from decrypted SOPS files
2. Create PushSecrets to sync to Vault
3. Verify secrets in Vault
4. Delete SOPS-encrypted files from Git
5. Delete local decrypted files

---

## Consequences

**Positive:**
- No secrets in Git (eliminates leak risk)
- Auto-generation of complex secrets
- Multi-region sync via single PushSecret
- Backend-agnostic (swap without app changes)
- Gitea tokens managed consistently with all other secrets

**Negative:**
- Requires bootstrap for initial secrets
- ESO operator dependency
- Vault/backend operational overhead

---

*Part of [OpenOva](https://openova.io)*
