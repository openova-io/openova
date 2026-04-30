# bp-matrix

Self-hosted, federation-capable team chat. Catalyst Application
Blueprint wrapping the **Synapse** Matrix homeserver. See
[`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §4.5
(Communication).

> "Synapse" here = the Matrix server implementation, **NOT** the
> retired OpenOva product noun (which has been replaced by `bp-axon`
> for the SaaS LLM gateway).

**Status:** Accepted | **Updated:** 2026-04-30

---

## Overview

Synapse is the reference Matrix homeserver. Catalyst pairs it with:

| Component | Integration |
|-----------|-------------|
| `bp-cnpg` | PostgreSQL backend (via `externalPostgresql`) |
| `bp-keycloak` | OIDC SSO (via `extraConfig.oidc_providers`) |
| `bp-cert-manager` | Ingress TLS via cluster `Issuer` |
| `bp-valkey` | Workers signaling backend (only when workers are enabled) |
| `bp-element-web` | Web client at `chat-web.<sovereign-fqdn>` (separate Blueprint, slot 47) |

## Per-Sovereign tenancy default — federation OFF

Catalyst's per-Sovereign tenancy default keeps each Sovereign's Matrix
instance private. Operator overlays flip `federation.enabled: true`
per-Organization for cross-Sovereign collaboration. The chart's
NetworkPolicy template only opens federation port 8448 when
`federation.enabled` is true (verified by Case 5 of
`tests/observability-toggle.sh`).

## Local registration OFF

Catalyst standard is OIDC-only accounts (registration is handled in
Keycloak). The wrapper sets `extraConfig.enable_registration: false` by
default; operator overlays may flip it on for development Sovereigns.

## Chart shape

```
platform/matrix/
├── blueprint.yaml                     # Catalyst Blueprint CRD
├── chart/
│   ├── Chart.yaml                     # umbrella; deps: matrix-synapse (Helm)
│   ├── values.yaml                    # Catalyst defaults (federation OFF, OIDC ON)
│   └── templates/
│       ├── _helpers.tpl
│       ├── networkpolicy.yaml         # default OFF; federation port gated by federation.enabled
│       ├── servicemonitor.yaml        # default OFF (CRD-gated)
│       └── hpa.yaml                   # default OFF
├── chart/tests/observability-toggle.sh
└── README.md
```

## Observability toggles (all default OFF)

Per [`docs/BLUEPRINT-AUTHORING.md`](../../docs/BLUEPRINT-AUTHORING.md)
§11.2.

| Toggle | Default | Why |
|--------|---------|-----|
| `serviceMonitor.enabled` | `false` | upstream chart has no ServiceMonitor; Catalyst overlay default off |
| `networkPolicy.enabled` | `false` | Operator supplies consumer-namespace selectors per-Sovereign |
| `hpa.enabled` | `false` | Solo-Sovereign baseline runs Synapse monolithic |
| `federation.enabled` | `false` | Catalyst per-Sovereign tenancy default (private rooms) |
| `extraConfig.enable_registration` | `false` | OIDC-only accounts (registration in Keycloak) |

## Verification

```bash
helm dependency update platform/matrix/chart
helm template platform/matrix/chart | grep -E "^kind:" | sort -u
helm lint platform/matrix/chart
bash platform/matrix/chart/tests/observability-toggle.sh
```

---

*Part of [OpenOva](https://openova.io). Closes #274.*
