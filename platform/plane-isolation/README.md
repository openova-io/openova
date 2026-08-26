# bp-plane-isolation

Per-component default-deny + explicit-allow micro-segmentation for the
de-vclustered platform planes (#4291 Workstream C — the inverse of #3642).

## Role in Catalyst

Control-plane network-policy substrate. When the mgmt / rtz / dmz platform
planes stop being vClusters and each platform component runs in its own
first-class **host namespace** (`placement.yaml vcluster: host`), the
whole-plane NetworkPolicy + gateway-ingress CiliumNetworkPolicy that
`bp-{mgmt,rtz,dmz}-vcluster` shipped per syncer-namespace must be replaced
by a **per-component** policy pair. This chart is that replacement — and the
core security win of Workstream C: the allow-set tightens from coarse
whole-plane to per-component, so a compromised component's blast radius is
one component instead of the whole plane.

Canonical placement of the planes: [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md)
(EPIC #4293 — a vcluster is the per-Organization boundary ONLY).

## What it renders

For each declared component host namespace (`values.yaml components[]`):

1. **A default-deny `NetworkPolicy`** (`podSelector: {}`, Ingress + Egress)
   whose ingress allow-list is the union of:
   - same-namespace pod-to-pod,
   - `flux-system` (the helm-controller reconciles the component's HR + CRs),
   - the EXPLICIT `allowIngressFrom` cross-component namespaces.
   Egress always permits DNS to CoreDNS; remaining egress stays open (the
   whole-plane policy left egress open too — per-component egress narrowing
   is a follow-up once the live call graph is captured).

2. **The gateway-ingress `CiliumNetworkPolicy`** `fromEntities: [ingress,
   host, remote-node]` — rendered ONLY when `gatewayIngress: true`. This is
   the single most important carry-over from the whole-plane policy: a
   vanilla K8s NetworkPolicy cannot express the reserved `ingress` ENTITY
   that Cilium-Gateway/envoy traffic carries, so without it every
   gateway→app request is silently dropped → curl 000/503 (the #2940
   hw159–hw165 app-plane wedge). No `world` — only the gateway reaches the
   component. Pure-backend components (loki/mimir/tempo/valkey/seaweedfs/
   vllm/nats-system) set `gatewayIngress: false` so they get NO
   `ingress`-entity admit — a tighter posture than the whole-plane policy
   which admitted the entity for every pod in the plane.

## Configuration knobs

- `enabled` (default `true`) — master gate; operators may flip off per
  Sovereign for debug.
- `dnsNamespace` (default `kube-system`) — CoreDNS namespace for the DNS
  egress allow.
- `components[]` — one entry per de-vclustered component: `namespace`,
  `gatewayIngress` (does it face a public HTTPRoute), `allowIngressFrom`
  (the explicit source namespaces).

## Operational notes

- Default ON per ADR-0001 §2 (zero-trust). Cilium unions every policy
  selecting an endpoint, so these per-component policies are purely additive
  to the cluster-wide `bp-network-policies` baseline.
- Installed by bootstrap-kit slot `26b-bp-plane-isolation` (after slot 26
  `bp-network-policies` and slot 01 `bp-cilium`).
- The CiliumNetworkPolicy is gated on the `cilium.io/v2` Capabilities check
  so the chart still installs on CRD-less clusters (kind CI).
