# Catalyst Security Model

**Status:** Authoritative target architecture. **Updated:** 2026-05-20.
**Implementation:** Per-component status tracked in [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md). OpenBao, ESO, Keycloak component READMEs exist; Catalyst's integration glue is design-stage. SPIRE/SPIFFE was dropped from the bootstrap-kit by founder PR #665 (2026-05-03, "drop bp-spire — Cilium WireGuard is canonical east-west mesh") — the `platform/spire/` chart is retained as opt-in for future re-introduction (see §2 below for re-enable triggers).

Identity, secrets, rotation, and multi-region credential semantics for Catalyst Sovereigns. Defer to [`GLOSSARY.md`](GLOSSARY.md) for terminology.

---

## 1. Identity: two systems, two purposes

| Subject | System | Token | Lifetime | What it auths |
|---|---|---|---|---|
| **Workloads** (every Pod, every controller) | Cilium WireGuard mesh + K8s ServiceAccount TokenReview | Cilium-managed node-level WireGuard session keys (kernel) + projected SA bound-tokens (1h, kubelet-rotated) | WG session-keys rotate on every Cilium agent restart; bound tokens auto-rotate hourly | Pod ↔ Pod transport encryption (kernel WG); Pod ↔ OpenBao auth (via the `kubernetes` auth method = TokenReview); Pod ↔ NATS / Catalyst APIs (SA token in `Authorization` header, validated server-side) |
| **Users** (every human) | Keycloak | OIDC JWT | 15 min access / 30 day refresh | UI auth, REST/GraphQL API, Gitea, console SSE |

Two systems, never conflated. Workload identity is bound to a Kubernetes ServiceAccount (`spiffe://<sov>/ns/<ns>/sa/<sa>` shape preserved at the namespace+SA granularity, just verified via TokenReview against the K8s API server rather than via SPIRE-issued SVIDs). User identity is bound to a Keycloak realm subject. The two meet only at boundaries where a service acts on behalf of a user (and even then, the workload presents both: its SA token for in-band auth, the WireGuard mesh for transport encryption, and the user's JWT in the request body).

---

## 2. Workload identity — Cilium WireGuard + K8s ServiceAccount TokenReview

**Status:** Canonical since founder PR #665 (2026-05-03, "drop bp-spire — Cilium WireGuard is canonical east-west mesh"). The `bp-spire` slot was removed from `clusters/_template/bootstrap-kit/` (Slot 06 deleted). The `platform/spire/` chart remains in the repo as opt-in for future re-introduction; see "Re-enable triggers" below.

**What protects east-west pod-to-pod traffic today:**

```
┌──────────────────────────────────────────────────────────────────────┐
│ Cilium agent (DaemonSet) on every node                                │
│  - encryption.type = wireguard                                        │
│  - encryption.wireguard.userspaceFallback = false                     │
│  - every pod-to-pod packet that leaves a node is wrapped in a         │
│    WireGuard tunnel keyed per node-pair, at the kernel layer          │
│  - 100% mesh coverage (no exemptions), zero sidecars                  │
│  - L7 policy + identity-aware enforcement via Cilium NetworkPolicy    │
│    and CiliumNetworkPolicy CRs                                        │
└──────────────────────────────────────────────────────────────────────┘
```

**What proves workload identity today** (Pod → service-of-record):

```
┌──────────────────────────────────────────────────────────────────────┐
│ Every Pod has a projected ServiceAccount token                        │
│  - kubelet rotates the bound token hourly                             │
│  - audience-scoped per consumer (e.g. `https://openbao.catalyst.svc`) │
│  - Pod presents the SA token in Authorization: Bearer                 │
│  - Server (OpenBao, NATS, Catalyst API) validates via the K8s         │
│    TokenReview API → returns the (namespace, ServiceAccount) tuple    │
│  - Authorization decisions are made on that tuple                     │
└──────────────────────────────────────────────────────────────────────┘
```

**Identity tuple examples** in Catalyst (note the shape parallels SPIFFE ID `spiffe://<sov>/ns/<ns>/sa/<sa>` — preserved at namespace+SA granularity):

```
ns=catalyst-projector  sa=projector                ← control-plane microservice
ns=catalyst-gitea      sa=gitea                    ← per-Sovereign Git server
ns=muscatpharmacy      sa=wordpress                ← Application workload
ns=catalyst-openbao    sa=openbao                  ← OpenBao itself
```

**OpenBao auth method:** `kubernetes` (TokenReview-backed). Roles are bound to `(namespace, ServiceAccount)` tuples. Not the `cert` auth method, not JWT-SVID. See `platform/cilium/chart/values.yaml:107-118` for the canonical comment locking this decision.

**NATS JetStream auth:** the `bp-spire` `dependsOn` was removed from `clusters/_template/bootstrap-kit/07-nats-jetstream.yaml` in PR #665. NATS no longer needs SVID-based auth; the kernel-level WireGuard encryption between every pod covers in-flight traffic, and JetStream Account-level isolation handles per-Org boundaries.

**Catalyst REST API auth:** workload calls are authenticated by SA bound-token (TokenReview); user calls by Keycloak-issued JWT.

### Why this configuration is sufficient today

| Concern | How it's met today |
|---|---|
| In-flight encryption | Cilium WireGuard, kernel-level, 100% mesh, no opt-out |
| Workload-to-workload authentication | K8s SA tokens validated server-side via TokenReview |
| Token rotation | Projected SA bound-tokens auto-rotate hourly (kubelet) |
| Defense against stolen long-lived tokens | Bound tokens are scoped to a single Pod + audience + 1h TTL; the legacy unbound SA secret-tokens are not used |
| Cross-Org isolation | vcluster boundary + NATS Account boundary + Keycloak realm boundary; SA tokens don't cross vcluster boundaries |
| Node-level identity | Cilium gives every node a WireGuard public key; CiliumNetworkPolicy + identity labels enforce L3/L7 policy at the eBPF datapath |

### Re-enable triggers (when to re-introduce SPIRE)

The `platform/spire/` chart is retained for the following scenarios. None apply today; re-enable requires founder ruling that overrides PR #665.

1. **Cross-Sovereign workload federation.** When workloads in Sovereign A need to authenticate to services in Sovereign B without round-tripping through a shared K8s API server, SPIFFE federation (`SPIFFE/SPIRE` upstream-bundle exchange) is the canonical path. K8s SA TokenReview is local to one cluster.
2. **Compliance audit requiring sub-hour cryptographic workload attestation.** SOC2 Type II, PCI-DSS, or FedRAMP audits demanding (a) cryptographically attested workload identity (not bearer-token), (b) sub-hour rotation, (c) per-Pod fingerprint distinct from `(namespace, SA)`. The SA-bound-token model proves `(namespace, SA, audience)` but not Pod-fingerprint; SPIRE workload attestation (k8s_psat + parent selectors) proves the fingerprint.
3. **Per-workload-fingerprint authorization.** When the policy decision requires distinguishing two Pods running the same SA in the same namespace (e.g. canary vs stable, two replicas with different secrets), SA token alone cannot distinguish them. SPIRE workload attestation can.

If any of (1)/(2)/(3) becomes a hard requirement, the re-introduction roadmap lives in TBD-V29 (#2055) — the 8-PR sketch covers: split `platform/spire/` into `platform/spire-crds/` + `platform/spire/`, add `bp-spire-crds` + `bp-spire` to `clusters/_template/bootstrap-kit/`, author `ClusterSPIFFEID` CRs for the ~6 first-wave services, add `go-spiffe/v2` deps + `tlsconfig.MTLSClientConfig` to outbound HTTP clients, pair server-side `tlsconfig.MTLSServerConfig` + SPIFFE-ID ACLs, switch OpenBao auth from `kubernetes` to `cert`, re-enable oidc-discovery-provider, migrate remaining workloads in waves. Estimate 2000-3500 LOC, 2-4 weeks.

---

## 3. Secrets: OpenBao + ESO

Static secrets (API tokens, passwords, signing keys, OAuth client secrets) live in OpenBao. They reach Pods via External Secrets Operator (ESO).

```
       OpenBao (Raft cluster, region-local)
              │
              │  ┌──────────────────────────────────────────────┐
              │  │  ExternalSecret CR in Git, in the Application │
              │  │  Gitea repo. References path in OpenBao.     │
              │  └──────────────────────────────────────────────┘
              │                          │
              │                          ▼
              │  ┌──────────────────────────────────────────────┐
              │  │  ESO (in vcluster) reads ExternalSecret CR   │
              │  │  Authenticates to OpenBao via the `kubernetes`│
              │  │  auth method (projected SA bound-token →     │
              │  │  TokenReview); transport secured by Cilium WG│
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
 │ "give me Postgres"      │ authenticates via SA bound-token  │
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

**Database engines supported:** PostgreSQL (CNPG), FerretDB, MongoDB-compatible, ClickHouse, Valkey, SeaweedFS/S3.

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
    ├── Organization muscatpharmacy
    │     Keycloak realm: muscatpharmacy
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
| Workload identity (K8s SA bound-token) | 1 h, auto-rotated by kubelet | Not configurable. Audience-scoped per consumer. SPIRE SVID (5-min, X.509-cert) is the future-state target if a §2 re-enable trigger fires. |
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

3. Materialized: ESO reads OpenBao path (auth via projected SA bound-token + TokenReview; transport encrypted by Cilium WireGuard), renders K8s Secret.
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
| Stolen ServiceAccount token | Projected SA bound-tokens are 1h TTL, audience-scoped, Pod-bound (deleted when the Pod terminates) — legacy long-lived Secret-tokens are not used. (Future hardening: SPIRE SVID 5-min mTLS-cert if a §2 re-enable trigger fires.) |
| Stolen K8s Secret | Encrypted at rest in etcd. Pulled only via ESO with a projected SA bound-token (TokenReview-validated); transport encrypted by Cilium WireGuard. |
| Compromised Pod | NetworkPolicy (Cilium) + L7 policies limit blast radius. Falco detects anomalous syscalls. |
| Malicious commit to Environment Gitea | EnvironmentPolicy requires PR approvals. Kyverno admission control denies non-policy-compliant manifests. |
| Compromised Blueprint upstream | All Blueprints are cosigned. Kyverno verify-signatures policy denies unsigned/wrong-issuer artifacts. |
| Cross-Org leakage | vcluster isolation. JetStream Account isolation. Keycloak realm isolation (per-Org or shared). |
| Compromised sovereign-admin account | MFA required at Keycloak. JIT elevation for production-impacting actions. Full audit trail to SIEM. |
| Compromised OpenBao node | 2-of-3 Raft quorum required for writes. Audit log captures every read. Rotate root token + re-shard quarterly. |
| Region-wide failure | Independent OpenBao Raft per region. PowerDNS lua-records (`ifurlup`) drop the affected regional endpoint from authoritative responses within the health-check window. Apps with `active-active` keep serving from healthy region. |
| Supply-chain attack on a build | SLSA-3 build provenance, cosign signing, Syft+Grype SBOM scanned in CI and at runtime by Trivy. |

---

*See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the broader platform context.*
