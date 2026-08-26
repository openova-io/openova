# cert-manager

TLS certificate automation. Per-host-cluster infrastructure (see [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) §3.3) — runs on every host cluster a Sovereign owns.

**Status:** Accepted | **Updated:** 2026-04-27

---

## Overview

cert-manager provides automated TLS certificate management using Let's Encrypt with automatic renewal and Kubernetes-native integration.

---

## Architecture

```mermaid
flowchart TB
    subgraph CM["cert-manager"]
        Controller[Controller]
        Webhook[Webhook]
        CAInjector[CA Injector]
    end

    subgraph Issuers["Issuers"]
        LE[Let's Encrypt]
        CA[Internal CA]
    end

    subgraph Resources["K8s Resources"]
        Cert[Certificate]
        Secret[TLS Secret]
        Ingress[Gateway/Ingress]
    end

    Controller --> LE
    Controller --> CA
    Cert --> Controller
    Controller --> Secret
    Secret --> Ingress
```

---

## Challenge Types

| Challenge | Use Case | DNS Provider |
|-----------|----------|--------------|
| HTTP-01 | Public endpoints | Not required |
| DNS-01 | Wildcards, internal | PowerDNS (central) via [`bp-cert-manager-powerdns-webhook`](../cert-manager-powerdns-webhook/README.md) |

**Recommended:** DNS-01 for wildcard certificates.

On OpenOva the DNS-01 solver is the **PowerDNS external webhook** shipped by
[`bp-cert-manager-powerdns-webhook`](../cert-manager-powerdns-webhook/README.md),
which writes ACME challenge TXT records to the central OpenOva PowerDNS and
ships the canonical `letsencrypt-dns01-prod-powerdns` ClusterIssuer. Dynadot is
legacy; Cloudflare/Route53 are not used.

---

## Configuration

### ClusterIssuer (Let's Encrypt)

```yaml
apiVersion: cert-manager.io/v1
kind: ClusterIssuer
metadata:
  name: letsencrypt-prod
spec:
  acme:
    server: https://acme-v02.api.letsencrypt.org/directory
    email: admin@<domain>
    privateKeySecretRef:
      name: letsencrypt-prod-key
    solvers:
      - dns01:
          # DNS-01 is solved by the PowerDNS external webhook shipped by
          # bp-cert-manager-powerdns-webhook (writes ACME TXT records to the
          # central OpenOva PowerDNS). See that chart for the canonical
          # letsencrypt-dns01-prod-powerdns ClusterIssuer.
          webhook:
            groupName: acme.powerdns.openova.io
            solverName: powerdns
            config:
              host: https://pdns.openova.io
```

### Certificate

```yaml
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: wildcard-cert
  namespace: cilium-gateway
spec:
  secretName: wildcard-tls
  issuerRef:
    name: letsencrypt-prod
    kind: ClusterIssuer
  dnsNames:
    - "*.<domain>"
    - "<domain>"
```

---

## Gateway API Integration

cert-manager integrates with Cilium Gateway API:

```yaml
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata:
  name: main-gateway
  namespace: cilium-gateway
spec:
  gatewayClassName: cilium
  listeners:
    - name: https
      protocol: HTTPS
      port: 443
      tls:
        mode: Terminate
        certificateRefs:
          - name: wildcard-tls
```

---

## Renewal

| Setting | Value |
|---------|-------|
| Renewal window | 30 days before expiry |
| Check interval | 24 hours |
| Retry interval | 1 hour on failure |

cert-manager automatically renews certificates before expiration.

---

## Monitoring

| Metric | Description |
|--------|-------------|
| `certmanager_certificate_expiration_timestamp_seconds` | Certificate expiry time |
| `certmanager_certificate_ready_status` | Certificate readiness |
| `certmanager_http_acme_client_request_count` | ACME requests |

---

*Part of [OpenOva](https://openova.io)*
