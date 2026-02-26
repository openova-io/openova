# LiveKit

Real-time video, audio, and data communication.

**Category:** Communication | **Type:** A La Carte

---

## Overview

LiveKit provides WebRTC-based real-time communication infrastructure for video conferencing, audio rooms, and live streaming. Paired with STUNner for Kubernetes-native TURN/STUN, it delivers enterprise-grade communication capabilities.

## Key Features

- WebRTC SFU (Selective Forwarding Unit)
- Video conferencing and screen sharing
- Audio rooms and live streaming
- Data channels for real-time messaging
- Recording and egress to MinIO

## Integration

| Component | Integration |
|-----------|-------------|
| STUNner | Kubernetes-native TURN/STUN for NAT traversal |
| MinIO | Recording storage |
| Keycloak | Authentication via OIDC |
| Grafana | Call quality metrics |

## Used By

- **OpenOva Relay** - Video/audio communication component

## Deployment

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: livekit
  namespace: flux-system
spec:
  interval: 10m
  path: ./platform/livekit
  prune: true
```

---

*Part of [OpenOva](https://openova.io)*
