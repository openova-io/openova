# Keycloak

User identity for Catalyst Sovereigns. Per-Sovereign supporting service in the Catalyst control plane (see [`docs/PLATFORM-TECH-STACK.md`](../../docs/PLATFORM-TECH-STACK.md) §2.3). Also serves as the FAPI Authorization Server for the Fingate (Open Banking) Blueprint.

**Status:** Accepted | **Updated:** 2026-04-27

> **Catalyst topology** (set at Sovereign provisioning time, see [`docs/SECURITY.md`](../../docs/SECURITY.md) §6):
> - **`per-organization`** (SME-style Sovereigns, e.g. omantel): one minimal Keycloak per Organization (single replica, embedded H2/sqlite, ~150 MB RAM, no HA). Blast radius limited to one Org.
> - **`shared-sovereign`** (corporate self-host, e.g. bankdhofar): one HA Keycloak for the entire Sovereign with multiple realms (one per Organization), federating to the corporation's identity provider (Azure AD, Okta).

---

## Overview

Keycloak provides:
- **User identity** for the Catalyst console, marketplace, admin, REST/GraphQL API, and per-Application SSO.
- **OIDC / OAuth 2.0 / SAML** federation to corporate IdPs.
- **FAPI 2.0** compliant authorization for the Fingate Open Banking Blueprint:
  - PSD2/FAPI 2.0 certification path
  - eIDAS certificate validation
  - Consent management
  - Multi-tenant TPP support (PSD2 sense — Third Party Providers, not platform tenants)

---

## Architecture

```mermaid
flowchart TB
    subgraph Keycloak["Keycloak"]
        Core[Core IAM]
        FAPI[FAPI Module]
        Consent[Consent Service]
    end

    subgraph Backend["Backend"]
        CNPG[CNPG Postgres]
    end

    subgraph Integration["Integration"]
        Envoy[Envoy/Cilium]
        TPP[TPP Registry]
    end

    Envoy -->|"ext_authz"| FAPI
    FAPI --> Consent
    Core --> CNPG
    FAPI --> TPP
```

---

## FAPI 2.0 Compliance

| Feature | Status |
|---------|--------|
| PKCE | Required |
| Signed JWT requests | Required |
| mTLS client auth | Required |
| PAR (Pushed Authorization) | Required |
| JARM responses | Required |

---

## Configuration

### Keycloak Deployment

```yaml
apiVersion: k8s.keycloak.org/v2alpha1
kind: Keycloak
metadata:
  name: keycloak
  namespace: open-banking
spec:
  instances: 2
  db:
    vendor: postgres
    host: keycloak-postgres-rw.databases.svc
    port: 5432
    database: keycloak
    usernameSecret:
      name: keycloak-db-credentials
      key: username
    passwordSecret:
      name: keycloak-db-credentials
      key: password
  http:
    tlsSecret: keycloak-tls
  hostname:
    hostname: auth.<domain>
```

### FAPI Realm Configuration

```json
{
  "realm": "open-banking",
  "enabled": true,
  "sslRequired": "all",
  "attributes": {
    "fapi.compliance.mode": "strict",
    "pkce.required": "S256",
    "require.pushed.authorization.requests": "true"
  },
  "clientPolicies": {
    "policies": [
      {
        "name": "fapi-advanced",
        "enabled": true,
        "conditions": [
          {
            "condition": "client-roles",
            "configuration": {
              "roles": ["fapi-client"]
            }
          }
        ],
        "profiles": ["fapi-2-security-profile"]
      }
    ]
  }
}
```

---

## eIDAS Certificate Validation

TPP certificates are validated against qualified trust service providers:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: eidas-config
  namespace: open-banking
data:
  trust-anchors: |
    # QTSPs for eIDAS validation
    - name: qualified-tsp-1
      certificate: |
        -----BEGIN CERTIFICATE-----
        ...
        -----END CERTIFICATE-----
```

---

## TPP Client Registration

```json
{
  "clientId": "tpp-12345",
  "clientAuthenticatorType": "client-jwt",
  "redirectUris": ["https://tpp.example.com/callback"],
  "attributes": {
    "tpp.authorization.number": "PSDGB-FCA-123456",
    "tpp.eidas.certificate": "...",
    "tpp.roles": ["AISP", "PISP"]
  },
  "defaultClientScopes": [
    "openid",
    "accounts",
    "payments"
  ]
}
```

---

## Consent Flow

```mermaid
sequenceDiagram
    participant TPP
    participant Keycloak
    participant User
    participant ConsentService

    TPP->>Keycloak: PAR request
    Keycloak->>TPP: request_uri
    TPP->>User: Redirect to Keycloak
    User->>Keycloak: Authenticate
    Keycloak->>ConsentService: Get consent page
    ConsentService->>User: Show accounts/permissions
    User->>Keycloak: Grant consent
    Keycloak->>ConsentService: Store consent
    Keycloak->>TPP: Authorization code
```

---

## High Availability

Keycloak runs with:
- 2+ replicas per region
- CNPG PostgreSQL with WAL streaming
- Session replication via Infinispan

---

*Part of [OpenOva](https://openova.io)*
