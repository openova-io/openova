# bp-k8s-ws-proxy — k8s-ws-proxy Blueprint

Catalyst-built Go binary + Helm chart wrapping the per-node
WebSocket exec proxy (`core/cmd/k8s-ws-proxy/`).

## Why this exists

Browsers can't reach the kube-apiserver directly without exposing
kubeconfig tokens (INVIOLABLE-PRINCIPLES #5). Putting a per-node
DaemonSet in front lets:

1. The catalyst-api forward exec requests with HMAC-signed
   WebSocket upgrades — **no kubeconfig in the browser**.
2. Sessions stay node-local (`internalTrafficPolicy: Local`) — the
   kube-proxy short-circuits onto the same node's pod, eliminating
   cross-node hops.
3. NetworkPolicy gates exec traffic at the per-node DaemonSet's
   pod IPs (one selector, one policy).

See `core/cmd/k8s-ws-proxy/DESIGN.md` for the wire contract +
failure-mode matrix.

## Default-OFF gate

`values.yaml` ships `k8sWsProxy.enabled: false`. Per-Sovereign
overlay flips on AND populates:

- `k8sWsProxy.image.tag` — SHA-pinned (CI populates)
- `k8sWsProxy.hmacSecret.name` — name of the SealedSecret holding
  the shared HMAC key (operator pre-creates with `kubeseal`)

Empty values for either fail the `helm template` render.

## Render check

```bash
# 0 resources when off
helm template bp-k8s-ws-proxy . | grep -c '^kind:'

# Full set when on
helm template bp-k8s-ws-proxy . \
  --set k8sWsProxy.enabled=true \
  --set k8sWsProxy.image.tag=abc1234 \
  | grep -c '^kind:'
```
