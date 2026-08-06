# hw292 — LIVE §854 NodePort census (cluster-side), 2026-08-06

**Env:** hw292.omani.works, dep `1c56518035a83e03`, 2-region
(`me-east-215-a` / `me-east-215-b-1`). Read-only; no cluster writes.

Founder, 2026-07-03: *"YOU CAN NEVER EVER USE NODEPORT EVEN FOR TESTING
PURPOSE!!! FUCK THE NODEPORTS!!!"*

This is the **cluster-side** check (#5348). `scripts/check-no-nodeports.sh`
scans sources + `helm template` and structurally cannot see a live Service that
drifted from a spec which still renders clean — which is exactly how
`openova-system/powerdns-anycast` held `32015/31425` behind a clean render.

## Result — hw292 is CLEAN, both regions

| region | Services scanned | `type: NodePort` | allocated `nodePort` |
|---|---|---|---|
| `me-east-215-a`   | 192 | **0** | **0** |
| `me-east-215-b-1` | 159 | **0** | **0** |
| **total**         | **351** | **0** | **0** |

Both apiservers answered `/readyz` → `ok` at the time of the census.

## Positive control — the detector is not broken

A zero from an instrument that cannot register a hit is worthless. The identical
detector, identical command, run against the **mothership**:

| cluster | Services | `type: NodePort` | allocated `nodePort` |
|---|---|---|---|
| mothership (`default` ctx) | 167 | **11** | **23** |

Named offenders include:

```
openova-system/powerdns-anycast:dns-udp=32015
openova-system/powerdns-anycast:dns-tcp=31425     <- the #5348 pair, still live
kube-system/traefik:web=32558  websecure=30569  bolt=30574  metro=31495
iogrid/proxy-gateway-socks5:1080=31080
iogrid/vpn-gateway-wireguard:wireguard=31729
cinova/catalog-svc:8765=30341
+ 7 cert-manager ACME http-solver Services (ephemeral, iogrid + ping-marketing)
```

So the probe registers NodePorts when they exist. hw292's zero is a measurement,
not an artefact.

## Reading

- **hw292 (the Sovereign product): §854-clean.** This is the surface the DoD
  cares about — what a franchised Sovereign actually ships.
- **The mothership is not**, and this is pre-existing and already tracked:
  `powerdns-anycast` is #5348; the `iogrid` socks5 entry is the #5088 remainder
  awaiting a founder merge on `iogrid#844`; `cinova/*` is under the SME
  never-touch rule. The ACME solver Services are cert-manager-created and
  transient.

No action taken on the mothership — those are tracked elsewhere and two of them
are explicitly not mine to touch.

## Method note

The census reads `spec.type == "NodePort"` **and** any `spec.ports[].nodePort`,
because they are different failure modes: a `type: LoadBalancer` Service still
gets auto-allocated nodePorts unless `allocateLoadBalancerNodePorts: false`, and
targeting those is equally forbidden under §854. Counting only `type: NodePort`
would have reported the mothership as clean on 12 of its 23 allocations.
