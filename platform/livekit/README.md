# bp-livekit

WebRTC SFU. Catalyst Application Blueprint. Real-time video, audio, and
data routing — powers the Huawei iFlytek voice demo and any Application
that needs sub-second media. Pairs with `bp-stunner` for K8s-native
TURN/STUN. See
[`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §4.5
(Communication).

**Status:** Accepted | **Updated:** 2026-04-30

---

## Overview

LiveKit is the WebRTC Selective Forwarding Unit (SFU). Catalyst pairs
it with `bp-stunner` so NAT traversal works without exposing a TURN
server's UDP port to the public internet.

## Catalyst integration

| Component | Integration |
|-----------|-------------|
| `bp-stunner` | K8s-native TURN/STUN — Catalyst routes LiveKit's TURN config at the stunner UDP-gateway Service |
| `bp-cert-manager` | TLS via cluster `Issuer` |
| `bp-valkey` | Signaling-state store when `replicaCount > 1` |
| `bp-keycloak` | (Optional) JWT identity for WebRTC participants |

## Hetzner firewall

LiveKit binds the UDP port range **50000-60000** for RTC traffic. The
per-Sovereign Hetzner firewall rule (Tofu-managed) opens this range to
the world. Pod-level NetworkPolicies do NOT cover host-network pods —
the firewall rule is the load-bearing control. See
[`docs/SECURITY.md`](../../docs/SECURITY.md) §4.

## Chart shape

```
platform/livekit/
├── blueprint.yaml                     # Catalyst Blueprint CRD
├── chart/
│   ├── Chart.yaml                     # umbrella; deps: livekit-server (Helm)
│   ├── values.yaml                    # Catalyst defaults; bundled TURN OFF
│   └── templates/
│       ├── _helpers.tpl
│       ├── networkpolicy.yaml         # default OFF (host-network caveat noted)
│       ├── servicemonitor.yaml        # default OFF (CRD-gated)
│       └── hpa.yaml                   # default OFF
├── chart/tests/observability-toggle.sh
└── README.md
```

## Dependencies

| Blueprint | Purpose |
|-----------|---------|
| `bp-stunner` | K8s-native TURN/STUN (required for NAT traversal) |
| `bp-cert-manager` | Ingress TLS via ClusterIssuer |
| `bp-valkey` | Signaling-state store (only when scaling beyond a single replica) |

## Observability toggles (all default OFF)

Per [`docs/BLUEPRINT-AUTHORING.md`](../../docs/BLUEPRINT-AUTHORING.md)
§11.2.

| Toggle | Default | Why |
|--------|---------|-----|
| `serviceMonitor.enabled` | `false` | `monitoring.coreos.com/v1` CRD ships with kube-prometheus-stack |
| `livekit-server.serviceMonitor.create` | `false` | upstream toggle — Catalyst restates the contract |
| `networkPolicy.enabled` | `false` | Operator supplies consumer-namespace selectors per-Sovereign |
| `hpa.enabled` | `false` | One LiveKit pod per node (port-range exclusivity) |
| `livekit-server.autoscaling.enabled` | `false` | Same — upstream HPA off |

## Verification

```bash
helm dependency update platform/livekit/chart
helm template platform/livekit/chart | grep -E "^kind:" | sort -u
helm lint platform/livekit/chart
bash platform/livekit/chart/tests/observability-toggle.sh
```

---

*Part of [OpenOva](https://openova.io). Closes #273.*
