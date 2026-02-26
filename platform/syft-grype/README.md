# Syft + Grype

SBOM generation and vulnerability matching for supply chain security.

**Category:** Supply Chain Security | **Type:** Mandatory

---

## Overview

Syft generates Software Bill of Materials (SBOM) for container images, and Grype matches SBOMs against vulnerability databases. Together they provide continuous supply chain visibility required by EU CRA and banking regulators.

## Key Features

- SBOM generation in CycloneDX and SPDX formats
- Vulnerability matching against NVD, GitHub Advisory, OSV databases
- CI/CD integration via Gitea Actions
- Runtime scanning via Harbor integration

## Integration

| Component | Integration |
|-----------|-------------|
| Harbor | Stores SBOMs as OCI artifacts |
| Sigstore/Cosign | Attaches SBOM attestations to signed images |
| Trivy | Complementary scanning (Trivy for runtime, Grype for CI) |
| Gitea Actions | SBOM generation in build pipeline |

## Deployment

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: syft-grype
  namespace: flux-system
spec:
  interval: 10m
  path: ./platform/syft-grype
  prune: true
```

---

*Part of [OpenOva](https://openova.io)*
