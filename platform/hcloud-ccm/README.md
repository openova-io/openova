# bp-hcloud-ccm

Catalyst Blueprint umbrella for [hcloud-cloud-controller-manager](https://github.com/hetznercloud/hcloud-cloud-controller-manager) — Hetzner Cloud's Kubernetes cloud-provider integration.

## Why this Blueprint exists

Without a cloud-provider implementation, k3s nodes get `providerID: k3s://<node-name>` (the `--cloud-provider=external` default). Two consequences cascade from that:

1. **Service-of-type-LoadBalancer stays `<pending>` forever.** kube-controller-manager has no cloud integration to call out to. This is the root cause `clustermesh-apiserver` could not migrate from NodePort to LB on omantel multi-region (qa-loop iter-12 Fix #53D + Fix #54 Workstream 1).
2. **Scheduler + cnpg/cnpg-pair cannot pin Pods to Hetzner zones.** `topology.kubernetes.io/zone` and the Hetzner-private-network IP fields are not populated until a CCM hot-fills them from the Hetzner API.

This Blueprint installs the upstream `hetznercloud/hcloud-cloud-controller-manager` chart, sourcing the Hetzner API token from the canonical `flux-system/cloud-credentials` Secret cloud-init writes at Phase 0.

## Wiring summary

```
infra/hetzner/cloudinit-control-plane.tftpl
  → flux-system/cloud-credentials  (key: hcloud-token)
       │
       │  Flux `valuesFrom`
       ▼
clusters/<sovereign>/bootstrap-kit/55-bp-hcloud-ccm.yaml HelmRelease
  → bp-hcloud-ccm chart (this directory)
       │
       │  templates/hcloud-token-secret.yaml
       ▼
kube-system/hcloud-token  (key: token)
       │
       │  upstream subchart's env.HCLOUD_TOKEN.valueFrom.secretKeyRef
       ▼
kube-system/hcloud-cloud-controller-manager Pod
  → reads HCLOUD_TOKEN, calls Hetzner Cloud API to:
      a) flip every Node's providerID from k3s://<name>
                                       to hcloud://<server-id>
      b) hot-fill .status.addresses (InternalIP from private network IF
                                     networkID is set, ExternalIP always)
      c) materialise type=LoadBalancer Services as Hetzner Cloud LBs
         (e.g. clustermesh-apiserver svc → real `hcloud://...` LB IP)
```

## Per-Sovereign overlay

```yaml
# clusters/<sovereign>/bootstrap-kit/55-bp-hcloud-ccm.yaml
spec:
  valuesFrom:
    - kind: Secret
      name: cloud-credentials
      valuesKey: hcloud-token
      targetPath: hcloudCcm.hcloudToken
  values:
    hcloudCcm:
      networkID: ""  # or "12345678" if the Sovereign uses a Hetzner Network
```

## ADR-0001 compliance

Per ADR-0001 §13 (cloud-direct architecture rule): every cloud-API call from inside the cluster is gated through a sanctioned operator. hcloud-CCM is the canonical operator for node providerID + LB materialisation; this Blueprint is the only path to that integration. Bespoke `kubectl patch node providerID=...` is forbidden.
