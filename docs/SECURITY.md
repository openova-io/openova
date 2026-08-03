# Catalyst Security Model

**Status:** Authoritative target architecture. **Updated:** 2026-05-20.
**Implementation:** Per-component status tracked in [`STATUS.md`](STATUS.md). OpenBao, ESO, Keycloak component READMEs exist; Catalyst's integration glue is design-stage. SPIRE/SPIFFE was dropped from the bootstrap-kit by founder PR #665 (2026-05-03, "drop bp-spire — Cilium WireGuard is canonical east-west mesh") — the `platform/spire/` chart is retained as opt-in for future re-introduction (see §2 below for re-enable triggers).

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
│  - 100% pod-to-pod coverage (no exemptions), zero sidecars            │
│  - encryption.nodeEncryption = true extends the same tunnel to        │
│    node↔node / node↔pod (host-namespace) traffic — on every node      │
│    EXCEPT one that matches the opt-out selector. Read §2.1 before     │
│    citing this as fleet-wide host-traffic encryption.                 │
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

### 2.1 Node-to-node encryption — the control-plane exclusion is declared, not accidental

**The posture, stated once so nobody has to re-derive it from a ConfigMap:**

> WireGuard node-encryption is on for every node in every region **except** a node
> whose labels match `--node-encryption-opt-out-labels`, whose value is
> `node-role.kubernetes.io/control-plane`. On k3s every server node carries that
> label, so **the control-plane node of each region is excluded** — by upstream
> default, deliberately, for a reason that matters (below). Pod-to-pod encryption
> is unaffected on those nodes and remains 100%.

**Mechanism** (Cilium 1.19.3, read off the agent rather than off the docs):

```
$ cilium-agent --help | grep node-encryption-opt-out-labels
  --node-encryption-opt-out-labels string   Label selector for nodes which will
       opt-out of node-to-node encryption (default "node-role.kubernetes.io/control-plane")
```

The agent logs the decision at startup on a matching node:

```
level=info msg="Opting out from node-to-node encryption on this node as per
 node-encryption-opt-out-labels label selector"
 module=agent.datapath.wireguard-agent Selector=node-role.kubernetes.io/control-plane
```

`platform/cilium/chart/values.yaml` sets `encryption.nodeEncryption: true` and does
**not** set the selector, so the upstream default is in force. Nothing in the
rendered chart, in `cilium-config`, or in the headline `Encryption: Wireguard
[… Peers: N]` status line reveals the exclusion — only the `NodeEncryption:`
sub-field of `cilium-dbg status` does, on the excluded node alone.

**Why the exclusion is kept rather than overridden.** The connection a node uses to
publish its rotated WireGuard public key would otherwise be encrypted with the very
key being replaced. Excluding the node that hosts the API server keeps key rotation
and node re-provisioning from deadlocking against themselves. Overriding it (an empty
selector) is a security-posture change that has to be argued here **and** re-proven on
a fresh prov through a key rotation — never flipped silently in a values file.

**What the exclusion costs, precisely.** On an excluded node, traffic sourced from or
destined to the **host network namespace** is not WireGuard-wrapped: API server
(:6443), etcd (:2379/:2380), kubelet (:10250), cilium-health probes, the cilium
agent's ClusterMesh client connections, and the hostNetwork `cilium-envoy` gateway
hop to backend pods. Every one of those except the envoy hop carries its own TLS
(k3s serving certs, etcd peer/client TLS, ClusterMesh mTLS), so the loss is a layer of
defence in depth rather than credentials in the clear. The envoy hop is the one that
is post-TLS-termination, and it stays on the per-region private VPC subnet. **Pod-to-pod
traffic — including every cross-region ClusterMesh pod flow — is unaffected**, because
that is the base WireGuard feature and not the node-encryption extension.

**The gate.** `scripts/check-live-node-encryption.sh` reads every cilium agent in every
region given to it and fails closed unless each agent's `NodeEncryption` state is the
one its node's labels imply under the selector declared above. It catches a worker that
silently opted out, a control-plane node that silently did not, an agent still carrying
a state its node's labels no longer imply, an exclusion set that grew, and a state it
could not read. It is wired into `scripts/verify-sovereign-convergence.sh` (step 7) and
its detector self-test runs on every PR that touches the guard or the Cilium chart.

```bash
scripts/check-live-node-encryption.sh --kubeconfig <region-a> --kubeconfig <region-b>
scripts/check-live-node-encryption.sh --self-test     # detector proof, no cluster
```

Live on hw292 (dep `1c56518035a83e03`, 2026-08-03): 8 agents, 2 regions —
`NodeEncryption Enabled=6  OptedOut=2`, the two being `…-a-cp1-54be6d` and
`…-b-cp1-6a755c`, exactly the label-selector match. Refs #5637.

### Why this configuration is sufficient today

| Concern | How it's met today |
|---|---|
| In-flight encryption (pod-to-pod) | Cilium WireGuard, kernel-level, 100% mesh, no opt-out |
| In-flight encryption (host-namespace: node↔node, node↔pod) | Cilium WireGuard node-encryption on every node whose labels do not match the opt-out selector — today that is every worker; the control-plane node of each region is excluded upstream-by-default. Declared and gated in §2.1 |
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
Sovereign: customer-self-hosted
└── ONE Keycloak (HA, 3 replicas, Postgres backend)
    Federates to the customer's corporate Azure AD
    │
    ├── Realm: catalyst-admin (sovereign-admin team)
    ├── Realm: core-banking (Org)
    ├── Realm: digital-channels (Org)
    ├── Realm: analytics (Org)
    └── Realm: corporate-it (Org)
```

**Why shared for corporate**: the customer's security perimeter is the entire Sovereign. Every Organization within is a business unit of the same legal entity. Federation to Azure AD is the single auth choke-point anyway. Per-Org Keycloak would mean N times the Azure AD federation config — operational overhead with no security benefit.

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

## 11. Rotation cadence and operator procedures

> Source: previously `docs/SECRET-ROTATION.md` (merged here on 2026-05-20).

§7 above states the **policy** (cadence + class). This section is the **operator runbook** for executing each rotation and rolling back if it breaks something live.

The canonical credential set Catalyst handles outside the dynamically-minted classes in §4:

| Credential | Where it lives | Rotation cadence | Rollback window |
|---|---|---|---|
| GHCR pull token (`catalyst-ghcr-pull-token`) | K8s Secret in `catalyst` ns, key `token` | **Yearly** | 24 h via 1Password version history |
| Hetzner Cloud API token (per Sovereign) | Wizard input → catalyst-api memory only | Per Sovereign apply | n/a — single-use, never persisted |
| Dynadot API key + secret (`dynadot-api-credentials`) | K8s Secret in `openova-system` ns, keys `api-key` + `api-secret` | **Yearly** (or on personnel change) | 24 h via 1Password version history |
| Sovereign Admin SSO client secret (Keycloak `catalyst-admin` realm) | Per-Sovereign K8s Secret in `keycloak` ns | **Yearly** | 1 h — Keycloak supports two active client secrets during rollover |
| SOPS / SealedSecrets cluster key (per Sovereign) | K8s Secret in `kube-system` ns | **Per Sovereign**, never rotated post-bootstrap | n/a — re-key requires migrating every existing SealedSecret |

The hard rule from [`PRINCIPLES.md`](PRINCIPLES.md) §4 (Never hardcode) applies in full: **passwords, tokens, API keys, client secrets, kubeconfig contents, TLS private keys, and `.env` values are all credentials and treated identically.** No credential is committed to git, ever. The catalyst-api Pod's runtime env is the single source of truth for every secret it consumes; persisted deployment records redact every one of them via `internal/store.Redact`.

### 11.1 GHCR pull token (`catalyst-ghcr-pull-token`)

**What it is.** A long-lived GitHub Personal Access Token (PAT) or fine-grained token with the `packages:read` scope on the `openova-io` organisation. The token authenticates the GHCR pulls Flux performs on every freshly-provisioned Sovereign — every `HelmRepository` CR in `clusters/<sovereign-fqdn>/bootstrap-kit/` references the `flux-system/ghcr-pull` Secret, and that Secret's content comes from this token.

**Why this token has its own runbook.** The bootstrap-kit pulls the `bp-*` OCI artifacts from `ghcr.io/openova-io/`, which is a **private** registry path. Without the token, the source-controller logs:

```
failed to get authentication secret 'flux-system/ghcr-pull':
  secrets "ghcr-pull" not found
```

…and Phase 1 stalls at bp-cilium. The cloud-init template writes the Secret BEFORE `kubectl apply -f flux-bootstrap.yaml`, but the token itself is never in the template — OpenTofu interpolates it at apply time from `var.ghcr_pull_token`, sourced from the catalyst-api Pod's env var `CATALYST_GHCR_PULL_TOKEN`.

**Where the token must NEVER be:** git (any branch, any repo), the bootstrap-kit YAMLs, the catalyst-api Pod logs, the Hetzner project metadata, Slack/email/issue bodies. The provisioner stamps it onto the Request struct in memory, writes `tofu.auto.tfvars.json` (mode 0600), and that file is wiped when the per-deployment workdir is cleared. The `json:"-"` tag on `Request.GHCRPullToken` keeps it out of the persisted deployment records (see `internal/store.Redact`).

#### Generation

Generate a fine-grained PAT (preferred over classic PATs):

1. https://github.com/settings/personal-access-tokens/new
2. Resource owner: **openova-io**
3. Repository access: **Public Repositories (read-only)** — this is sufficient because GHCR packages inherit the openova-io org's GHCR visibility settings; the token does not need repo-level access.
4. Permissions:
   - **Account → Packages → Read** (the only scope this token uses)
5. Expiration: **365 days** (next rotation date — write it on the 1Password item).
6. Generate. **Copy the token to 1Password immediately** (the page shows it once); never paste it into a terminal or a chat window.

#### Storage

1Password vault: **OpenOva — Production**
Item title: **Catalyst — GHCR pull token (catalyst-ghcr-pull-token)**
Tags: `catalyst`, `ghcr`, `rotation:yearly`

Notes field on the 1Password item must record:
- Generation date.
- Expiration date.
- Username paired with this token at the registry: `openova-bot` (the literal string the cloud-init template uses; GitHub validates the token, not the username, but this string lands in audit-trail JSON).
- Operator who generated it.

#### Apply (the one-liner)

Replace `<GHCR_PULL_TOKEN>` with the token retrieved from 1Password — **never** paste a real token into git, an issue, a commit message, or a terminal session that will be transcribed.

```bash
kubectl create secret generic catalyst-ghcr-pull-token \
  --namespace=catalyst \
  --from-literal=token='<GHCR_PULL_TOKEN>' \
  --dry-run=client -o yaml | \
  kubectl apply -f -
```

The `--dry-run=client … | kubectl apply -f -` form is idempotent: a fresh install creates the Secret; a rotation overwrites the existing one in-place. The catalyst-api Deployment must be rolled to pick up the new value:

```bash
kubectl -n catalyst rollout restart deployment/catalyst-api
kubectl -n catalyst rollout status  deployment/catalyst-api
```

(`secretKeyRef`-mounted env vars are NOT auto-refreshed by the Pod — only volume mounts are. The catalyst-api chart mounts the token as `env.valueFrom.secretKeyRef`, so a rollout is required.)

#### Verify

```bash
# The Secret exists with the expected key.
kubectl -n catalyst get secret catalyst-ghcr-pull-token \
  -o jsonpath='{.data.token}' | base64 -d | wc -c
# (Output: a non-zero byte count. NEVER append `; echo` — that prints
# the token to your terminal.)

# The catalyst-api Pod read it cleanly at startup.
kubectl -n catalyst logs deploy/catalyst-api | grep -i 'ghcr' || \
  echo "no ghcr-related warning — provisioner picked up the token"

# A fresh /api/v1/deployments POST validates without the
# 'CATALYST_GHCR_PULL_TOKEN missing' error (expected for managed-pool
# domain mode).
```

#### Rollback

If the new token does not authenticate (typo, wrong scope, expired):

1. Open 1Password's item version history; copy the previous token.
2. Re-run the `kubectl create secret … --dry-run=client | kubectl apply` one-liner with the previous token.
3. `kubectl -n catalyst rollout restart deployment/catalyst-api`.
4. File a follow-up issue to investigate why the new token failed.

The previous token remains valid until the next yearly rotation — GitHub does not invalidate replaced fine-grained tokens automatically. **Revoke the broken token in the GitHub UI** as a hygiene step once rollback succeeds.

### 11.2 Hetzner Cloud API token (per Sovereign)

Captured by the wizard's StepProvider, lives in catalyst-api memory only for the duration of one deployment. NEVER persisted (the `Request.HetznerToken` field is `json:"-"`; `internal/store.Redact` overwrites it with `<redacted>` for any record that ends up on disk).

Rotation: per-Sovereign apply. Each `tofu apply` accepts a fresh token; once `tofu apply` returns, catalyst-api drops the value out of memory (the Pod restart on next image roll loses the in-memory copy regardless).

If a Hetzner token is suspected of leaking: revoke at https://console.hetzner.cloud/projects → Security → API tokens. The next wizard run will accept a fresh one.

### 11.3 Dynadot API key + secret (`dynadot-api-credentials`)

K8s Secret in `openova-system` namespace, keys: `api-key`, `api-secret`, `domain` (legacy single-domain), `domains` (comma-separated list, preferred).

**Yearly rotation** via the Dynadot account UI:

1. https://www.dynadot.com → My Account → API Settings → Regenerate.
2. Copy both halves to the 1Password item **Dynadot — OpenOva pool domains API credentials**.
3. Apply:

```bash
kubectl create secret generic dynadot-api-credentials \
  --namespace=openova-system \
  --from-literal=api-key='<DYNADOT_API_KEY>' \
  --from-literal=api-secret='<DYNADOT_API_SECRET>' \
  --from-literal=domains='omani.works' \
  --dry-run=client -o yaml | \
  kubectl apply -f -

kubectl -n catalyst         rollout restart deployment/catalyst-api
kubectl -n openova-system   rollout restart deployment/pool-domain-manager
```

The `domains` value is the comma-separated allowlist of pool domains this account manages. Adding a third pool domain (e.g. `acme.io`) is a secret update, not a code change — see [`PRINCIPLES.md`](PRINCIPLES.md) §4.

### 11.4 Cross-cutting rules for every rotation

1. **NEVER print a credential to a terminal.** All retrievals pipe to a file (`> /path && chmod 600`) or directly into `kubectl create secret --from-literal`. Session transcripts are durable.
2. **NEVER commit a credential.** Use the `kubectl create secret … | kubectl apply` one-liner; the value never touches a file the working tree tracks.
3. **NEVER skip the rollout restart.** `secretKeyRef` env vars are read at Pod start. A Secret update with no rollout is a silent half-rotation: existing Pods serve the old value, new Pods (post next evict) serve the new one. The catalyst-api is single-replica with strategy `Recreate`, so this is one step.
4. **Log only metadata, never the value.** `kubectl describe secret` shows `data: token: <not shown>` — that is intentional. Reading the value via `-o jsonpath` and piping to a file is the sanctioned confirmation path; piping to `cat`/`echo` is not.

If you accidentally expose a credential — printed to a terminal that will be transcribed, committed it to a branch, posted it to an issue — **rotate immediately** following the per-credential procedure above. Do not try to "quietly fix it" by editing history; assume the leaked value is captured.

---

*See [`ARCHITECTURE.md`](ARCHITECTURE.md) for the broader platform context.*
