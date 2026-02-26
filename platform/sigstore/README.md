# Sigstore/Cosign

Container image signing and verification for supply chain security.

**Category:** Supply Chain Security | **Type:** Mandatory

---

## Overview

Sigstore/Cosign provides keyless container image signing using OIDC identity, ensuring provenance verification for all images deployed to the cluster. Combined with Kyverno policies, unsigned images are rejected at admission time.

## Key Features

- Keyless signing via OIDC (Gitea Actions identity)
- Image signature verification at admission (Kyverno integration)
- Transparency log for audit trail
- SBOM attestation support

## Integration

| Component | Integration |
|-----------|-------------|
| Harbor | Stores signatures alongside images |
| Kyverno | Enforces signature verification policies |
| Gitea Actions | Signs images during CI/CD pipeline |
| Syft + Grype | Attaches SBOM attestations |

## Deployment

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: sigstore
  namespace: flux-system
spec:
  interval: 10m
  path: ./platform/sigstore
  prune: true
```

---

*Part of [OpenOva](https://openova.io)*
