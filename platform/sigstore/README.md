# Sigstore/Cosign

Container image signing and verification for supply chain security. Per-host-cluster infrastructure (see [`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §3.3) — every host cluster runs cosign-based admission verification. Catalyst's CI signs every Blueprint OCI artifact (`ghcr.io/openova-io/bp-<name>:<semver>`) at release; Kyverno's verify-signatures policy denies unsigned/wrong-issuer artifacts at admission.

**Category:** Supply Chain Security | **Type:** Mandatory per host cluster

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
