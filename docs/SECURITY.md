# Catalyst Security Model

**Status:** Authoritative target architecture. **Updated:** 2026-04-27.
**Implementation:** Per-component status tracked in [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md). OpenBao, ESO, SPIRE, Keycloak component READMEs exist; Catalyst's integration glue is design-stage.

Identity, secrets, rotation, and multi-region credential semantics for Catalyst Sovereigns. Defer to [`GLOSSARY.md`](GLOSSARY.md) for terminology.

---

## 1. Identity: two systems, two purposes

| Subject | System | Token | Lifetime | What it auths |
|---|---|---|---|---|
| **Workloads** (every Pod, every controller) | SPIFFE/SPIRE | SVID (X.509 mTLS cert) | 5 minutes, auto-rotated | Pod ↔ Pod; Pod ↔ OpenBao; Pod ↔ NATS; Pod ↔ Catalyst APIs |
| **Users** (every human) | Keycloak | OIDC JWT | 15 min access / 30 day refresh | UI auth, REST/GraphQL API, Gitea, console SSE |

Two systems, never conflated. Workload identity is bound to a Kubernetes ServiceAccount. User identity is bound to a Keycloak realm subject. The two meet only at boundaries where a service acts on behalf of a user (and even then, the workload presents both: its own SVID for transport mTLS, and the user's JWT in the request body).

---

## 2. SPIFFE/SPIRE — workload identity

```
┌──────────────────────────────────────────────────────────────────────┐
│ Each Sovereign runs a SPIRE server (in catalyst-spire namespace)     │
│  - one HA SPIRE server per host cluster                              │
│  - upstream-bundle to a root SPIRE server in the management cluster  │
│  - issues SVIDs to a SPIRE agent on every node                       │
└──────────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌──────────────────────────────────────────────────────────────────────┐
│ SPIRE agent on each node                                             │
│  - exposes Workload API (Unix socket) to Pods on that node           │
│  - mints SVIDs scoped by SPIFFE ID:                                  │
│      spiffe://<sovereign>/ns/<namespace>/sa/<service-account>        │
│  - rotates every 5 minutes; Pods refresh in-memory                   │
└──────────────────────────────────────────────────────────────────────┘
```

**SPIFFE ID examples** in Catalyst:

```
spiffe://omantel/ns/catalyst-projector/sa/projector
spiffe://omantel/ns/catalyst-gitea/sa/gitea
spiffe://omantel/ns/muscatpharmacy/sa/wordpress     ← Application workload
spiffe://omantel/ns/catalyst-openbao/sa/openbao     ← OpenBao itself
```

OpenBao authenticates clients by their SVID. JetStream authenticates clients by their SVID. The Catalyst REST API authenticates workloads by their SVID and users by their JWT.

**Why SPIFFE over static service-account tokens:**
- Static tokens leak. SVIDs auto-rotate at 5-minute boundaries.
- SPIFFE IDs are portable across clusters (cross-region service-to-service auth works without cross-cluster ServiceAccount sync).
- mTLS by default — every connection is authenticated and encrypted.

---

## 3. Secrets: OpenBao + ESO

Static secrets (API tokens, passwords, signing keys, OAuth client secrets) live in OpenBao. They reach Pods via External Secrets Operator (ESO).

```
       OpenBao (Raft cluster, region-local)
              │
              │  ┌──────────────────────────────────────────────┐
              │  │  ExternalSecret CR in Git, in the Environment │
              │  │  Gitea repo. References path in OpenBao.     │
              │  └──────────────────────────────────────────────┘
              │                          │
              │                          ▼
              │  ┌──────────────────────────────────────────────┐
              │  │  ESO (in vcluster) reads ExternalSecret CR   │
              │  │  Authenticates to OpenBao via SVID           │
              │  └──────────────────────────────────────────────┘
              │                          │
              │                          ▼
              │  ┌──────────────────────────────────────────────┐
              │  │  K8s Secret (rendered, versioned)             │
              │  │  Reloader watches hash → rolling deploy      │
              │  └──────────────────────────────────────────────┘
              │                          │
              ▼                          ▼
   (audit log + telemetry)         Pod mounts the secret
```

**What's in Git** (always):

- `ExternalSecret` CR pointing at an OpenBao path
- `SecretStore` CR pointing at the OpenBao endpoint
- `SecretPolicy` CR (rotation rules)
- Public keys, root CA certs (CRDs)

**What's NEVER in Git:**

- Secret values (passwords, tokens, private keys, etc.)
- OpenBao root tokens
- Static API credentials

---

## 4. Dynamic credentials

For databases, S3, and other systems supporting short-lived credentials, OpenBao mints them on demand:

```
Pod                   catalyst-secret-sidecar          OpenBao (DB engine)
 │                          │                                  │
 │ "give me Postgres"      │ authenticates via SVID            │
 │─────────────────────────►│                                   │
 │                          │ mints Postgres user             │
 │                          │ TTL=1h                          │
 │                          │──────────────────────────────────►│
 │                          │ returns user/password           │
 │◄─────────────────────────│◄──────────────────────────────────│
 │
 │ connects to Postgres, opens connection pool
 │
 │ at T+50min: sidecar pre-emptively requests new creds
 │              app drains old pool, swaps to new creds
 │              no downtime
 │
 │ at T+1h: OpenBao revokes the old user
```

The sidecar is automatic for any Pod whose Blueprint declares `dynamicSecrets: true`. Apps that prefer in-process can use the Catalyst SDK directly. Apps that can't do either get a rolling restart at the TTL boundary (acceptable for low-tier workloads).

**Database engines supported:** PostgreSQL (CNPG), FerretDB, MongoDB-compatible, ClickHouse, Valkey, MinIO/S3.

---

## 5. Multi-region OpenBao — INDEPENDENT, NOT STRETCHED

Critical: each region runs its **own** Raft cluster. There is no cross-region Raft quorum. Region failures are independent failure domains.

```
   Region A (Muscat)              Region B (Salalah)              Region C (Frankfurt DR)
   ┌──────────────────┐           ┌──────────────────┐            ┌──────────────────┐
   │ OpenBao cluster  │           │ OpenBao cluster  │            │ OpenBao cluster  │
   │ 3 Raft nodes     │           │ 3 Raft nodes     │            │ 3 Raft nodes     │
   │ INDEPENDENT      │           │ INDEPENDENT      │            │ INDEPENDENT      │
   │ Raft quorum      │           │ Raft quorum      │            │ Raft quorum      │
   └──────┬───────────┘           └──────────────────┘            └──────────────────┘
          │                                ▲                                ▲
          │ async log shipping             │ async log shipping             │
          │ (Performance Replication)      │                                │
          └────────────────────────────────┴────────────────────────────────┘
                  one-way: primary → secondaries; no cross-region quorum
```

### 5.1 Fault domain semantics

- **Each region has its own self-contained 3-node Raft cluster.** Quorum is **intra-region only** (need 2-of-3 in the same region).
- **A total Region A failure does NOT require any other region to do anything.** Region B and C continue serving reads from their local replicated data.
- **Network partition between regions:** each region keeps operating independently. Writes pause on standby regions (since they're read-only by design).
- **DR promotion is explicit.** Either `sovereign-admin`-approved or automated by failover-controller with strict criteria. Not automatic on every blip.

### 5.2 Read/write semantics

- **Writes** (rotations, new secrets) → primary OpenBao only.
- **Reads** → local OpenBao replica (sub-10ms latency in same continent).
- **Replication lag** <1s typical. Apps in B and C read post-rotation values without any cross-region call.
- **Region failure** → DR replica promoted by the failover-controller. New writes are blocked briefly during promotion (~30s). After promotion, the DR region accepts writes.

### 5.3 Why NOT a stretched cluster

A stretched Raft cluster (5 nodes across 3 regions, single quorum) seems superficially appealing but is fragile:

- A single region's network blip can cause loss of quorum if 3 of 5 nodes are in the affected region.
- Cross-region latency degrades all writes (every write needs cross-region majority ack).
- An entire region failure can leave the cluster without quorum.

We deliberately reject this pattern. Each region is its own failure domain.

---

## 6. Keycloak topology

Set at Sovereign provisioning time:

```yaml
# In Sovereign CRD spec
keycloakTopology: per-organization      # SME-style: each Org gets its own
# OR
keycloakTopology: shared-sovereign      # Corporate: one Keycloak for the Sovereign
```

### 6.1 SME-style (`per-organization`)

```
Sovereign: omantel
└── Each Organization gets a minimal Keycloak (1 replica, embedded H2/sqlite,
    ~150 MB RAM, no HA)
    │
    ├── Organization muscat-pharmacy
    │     Keycloak realm: muscat-pharmacy
    │     Federations: Omantel-Mobile-OTP, Google, Apple
    ├── Organization acme-shop
    │     Keycloak realm: acme-shop
    └── …
```

**Why per-Org for SME**: blast radius. Muscat-pharmacy's Keycloak outage cannot affect Lulu-Hypermarket. Operationally cheap — minimal Keycloak fits in <200MB. SME tier customers don't need HA; if their Keycloak restarts in 10s during a deploy, that's tolerable.

**Larger SMEs** can opt into HA via a tier upgrade — same data model, just more replicas + Postgres backend instead of embedded H2.

### 6.2 Corporate (`shared-sovereign`)

```
Sovereign: bankdhofar
└── ONE Keycloak (HA, 3 replicas, Postgres backend)
    Federates to Bank Dhofar's corporate Azure AD
    │
    ├── Realm: catalyst-admin (sovereign-admin team)
    ├── Realm: core-banking (Org)
    ├── Realm: digital-channels (Org)
    ├── Realm: analytics (Org)
    └── Realm: corporate-it (Org)
```

**Why shared for corporate**: the bank's security perimeter is the entire Sovereign. Every Organization within is a business unit of the same legal entity. Federation to Azure AD is the single auth choke-point anyway. Per-Org Keycloak would mean N times the Azure AD federation config — operational overhead with no security benefit.

### 6.3 App-level SSO

Every Application Blueprint can declare SSO support:

```yaml
# in bp-wordpress configSchema
sso:
  enabled: true   # auto-creates a Keycloak client in the Org's realm
                  # injects credentials via OpenBao + ExternalSecret
```

End users get one-click SSO across all Apps in their Organization without ever seeing OAuth config.

---

## 7. Rotation policy

Every credential class has a SecretPolicy that drives automatic rotation.

```yaml
apiVersion: catalyst.openova.io/v1alpha1
kind: SecretPolicy
metadata:
  name: stricter-rotation
  namespace: catalyst-system
spec:
  appliesTo:
    organizationLabels:
      tier: regulated
  rules:
    - kind: database-credentials
      maxTTL: 1h
      autoRotate: true
    - kind: api-token
      maxTTL: 90d
      autoRotate: true
      rotateBefore: 7d
    - kind: oauth-client-secret
      maxTTL: 90d
      autoRotate: true
    - kind: signing-key
      maxTTL: 365d
      autoRotate: false               # requires explicit approval
      requireApproval: [security-officer]
    - kind: tls-cert
      maxTTL: cert-manager-managed
```

| Class | Default | Notes |
|---|---|---|
| Workload identity (SPIRE SVID) | 5 min, auto | Not configurable. |
| Dynamic DB creds | 1 h, auto | Per-Blueprint TTL configurable. |
| API tokens, OAuth client secrets | 90 d, auto | rotateBefore: 7d gives apps a refresh window. |
| Signing keys, root CAs | 365 d, manual approval | Auto-rotation possible but disabled by default for high-impact keys. |
| TLS certs | cert-manager controlled | Acme/Let's Encrypt, ~60 d, automatic. |
| User passwords (Keycloak) | User-managed + MFA | Min age policy enforced by realm. |

A `security-officer` sees a **RotationDashboard** view: every credential class, age, next rotation, force-rotate button (RBAC-gated).

---

## 8. The path of a secret value (no leakage)

```
1. Generated:   Crossplane composition or OpenBao auto-generator creates value.
                Never printed. Never echoed. Written directly to OpenBao via API.

2. Referenced:  ExternalSecret CR in Git names the OpenBao path. No value in Git.

3. Materialized: ESO reads OpenBao path (auth via SVID), renders K8s Secret.
                The K8s Secret is base64-encoded; never logged.

4. Consumed:    Pod mounts as env or file. Reloader watches hash; rolls deploy
                on change. Application sees plaintext only via mount or env.

5. Rotated:     SecretPolicy controller invokes rotation API on OpenBao.
                New value generated, replication propagates, ESO re-reads,
                Reloader rolls. Old value retained for grace window (24h),
                then revoked.

6. Audited:     Every step logged to Catalyst audit log. No plaintext.
```

**What never happens:**
- Plaintext secrets in Git.
- Plaintext secrets in shell command output.
- Plaintext secrets in issues, PRs, comments, or chat.
- Plaintext secrets in commit messages, branch names, tag names.

If a secret is ever leaked via terminal output (a misconfigured `kubectl describe`, a debug log), the leak is treated as a P1 incident: rotate immediately, audit history, communicate.

---

## 9. Compliance posture

| Standard | Catalyst posture |
|---|---|
| **SOC 2 Type 2** | Audit logging in JetStream + OpenSearch SIEM cold storage. SecretPolicy enforces rotation. EnvironmentPolicy enforces approvals. |
| **PSD2 / FAPI** | Fingate Blueprint composes Keycloak (FAPI authorization), eIDAS cert verification, ext_authz. |
| **DORA** | Resilience testing via Litmus chaos Blueprint. Multi-region by default for regulated tier. |
| **NIS2** | Falco runtime detection + OpenSearch SIEM + Kyverno policy + supply-chain (cosign + Syft+Grype). |
| **GDPR** | Per-region data residency via Placement spec. Right-to-be-forgotten flow defined per Application Blueprint. |
| **ISO 27001** | Mappings published per control; evidence surfaced via Catalyst console audit views and SIEM exports. |

Every Sovereign exports its audit log to a customer-specified SIEM. Default: OpenSearch in the Sovereign itself; customers may push to external Splunk, Datadog SIEM, etc.

---

## 10. Threat model summary

| Threat | Mitigation |
|---|---|
| Stolen ServiceAccount token | SVID is 5-min TTL; revoked by SPIRE on rotation. |
| Stolen K8s Secret | Encrypted at rest in etcd. Pulled only via ESO with SVID. |
| Compromised Pod | NetworkPolicy (Cilium) + L7 policies limit blast radius. Falco detects anomalous syscalls. |
| Malicious commit to Environment Gitea | EnvironmentPolicy requires PR approvals. Kyverno admission control denies non-policy-compliant manifests. |
| Compromised Blueprint upstream | All Blueprints are cosigned. Kyverno verify-signatures policy denies unsigned/wrong-issuer artifacts. |
| Cross-Org leakage | vcluster isolation. JetStream Account isolation. Keycloak realm isolation (per-Org or shared). |
| Compromised sovereign-admin account | MFA required at Keycloak. JIT elevation for production-impacting actions. Full audit trail to SIEM. |
| Compromised OpenBao node | 2-of-3 Raft quorum required for writes. Audit log captures every read. Rotate root token + re-shard quarterly. |
| Region-wide failure | Independent OpenBao Raft per region. k8gb removes affected endpoints. Apps with `active-active` keep serving from healthy region. |
| Supply-chain attack on a build | SLSA-3 build provenance, cosign signing, Syft+Grype SBOM scanned in CI and at runtime by Trivy. |

---

*See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the broader platform context.*
