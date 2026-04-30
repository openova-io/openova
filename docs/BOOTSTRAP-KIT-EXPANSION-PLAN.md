# Bootstrap-Kit Expansion Plan — From 14 HRs to 40+ HRs (Wave 2 Dispatch Reference)

**Status:** Design (no implementation in this PR). **Author:** W1.D. **Updated:** 2026-04-30.
**Authoritative anchors:** [`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md), [`PROVISIONING-PLAN.md`](PROVISIONING-PLAN.md), [`ARCHITECTURE.md`](ARCHITECTURE.md) §11, [`BLUEPRINT-AUTHORING.md`](BLUEPRINT-AUTHORING.md).

---

## 0. Purpose & non-goals

**Purpose.** Define the deterministic plan by which Wave 2 (W2) of the bootstrap-kit expansion grows `clusters/_template/bootstrap-kit/` from its current 14 HelmReleases (post-PR-247 baseline) to **≥40 Ready HelmReleases** on the first franchised Sovereign (`omantel.omani.works`). This document is the dispatch reference for four parallel W2 agents (W2.K1 – W2.K4) so they can work on disjoint slot ranges without merge collisions or dependency surprises.

**Non-goals.**
- This PR does **not** add, edit, or rename any HR file.
- This PR does **not** modify `clusters/_template/bootstrap-kit/kustomization.yaml`.
- This PR does **not** edit any chart in `platform/` or `products/`.
- This PR does **not** open issues — the W2 dispatch process owns ticket creation.

**File path isolation for this PR:** the only file touched is `docs/BOOTSTRAP-KIT-EXPANSION-PLAN.md`. Anything else is a violation of the W1.D scope.

---

## 1. Inventory + classification

### 1.1 Current bootstrap-kit baseline (post-PR-247)

The `clusters/_template/bootstrap-kit/` directory currently contains **14 HelmReleases** organized into Tiers 0–4. The "14" count includes the powerdns/external-dns split (PR-167/168) and the bp-catalyst-platform umbrella (#247-class):

| Slot | File | Blueprint | Tier | Notes |
|---:|---|---|---|---|
| 01 | `01-cilium.yaml` | bp-cilium | 0 — Foundation | CNI, Gateway API, mTLS substrate. Root of the DAG. |
| 02 | `02-cert-manager.yaml` | bp-cert-manager | 0 — Foundation | Issuers + CertificateRequest CRDs that downstream HRs assume. |
| 03 | `03-flux.yaml` | bp-flux | 0 — Foundation | Host-Flux. (Bootstrap Flux that loaded this kit is replaced.) |
| 04 | `04-crossplane.yaml` | bp-crossplane | 0 — Foundation | Day-2 IaC. Adopts Phase-0 OpenTofu artefacts. |
| 05 | `05-sealed-secrets.yaml` | bp-sealed-secrets | 0 — Foundation | Bootstrap-only; transient until ESO+OpenBao take over. |
| 06 | `06-spire.yaml` | bp-spire | 1 — Identity | SPIFFE root + agent. Workload SVIDs. |
| 07 | `07-nats-jetstream.yaml` | bp-nats-jetstream | 2 — Eventbus | Control-plane event spine. |
| 08 | `08-openbao.yaml` | bp-openbao | 1 — Identity/secret | Per-Sovereign secret backend. Raft. |
| 09 | `09-keycloak.yaml` | bp-keycloak | 1 — Identity | OIDC/OAuth, per-Sovereign or per-Org realms. |
| 10 | `10-gitea.yaml` | bp-gitea | 2 — Git | Per-Sovereign Git server (5 conventional Orgs). |
| 11 | `11-powerdns.yaml` | bp-powerdns | 3 — DNS authoritative | Per-Sovereign authoritative DNS, gpgsql backend. |
| 12 | `12-external-dns.yaml` | bp-external-dns | 3 — DNS sync | `pdns` provider; reconciles Service/Ingress hostnames. |
| 13 | `13-bp-catalyst-platform.yaml` | bp-catalyst-platform | 4 — Catalyst umbrella | The control plane: console, marketplace, catalog-svc, projector, provisioning, environment-controller, blueprint-controller, billing. |
| 14 | (reserved by PR-247 for crossplane-claims wiring) | bp-crossplane-claims | 0 — Foundation extension | Reserved slot for Compositions/Claims registration once cloud providers are wired in. **If your cluster doesn't yet have this file, treat slot 14 as the current head and W2.K1 starts at 15.** |

> **Note on the "14" count.** The slot count in this document is the post-PR-247 numbering convention. If a cluster's current `kustomization.yaml` lists fewer than 14 entries (for example, a tree where `crossplane-claims` is still inlined into `04-crossplane.yaml`), the W2 batches still **start at slot 15**; the empty slots before are intentionally reserved for upstream foundation work and must not be reused by W2.

### 1.2 The 61 platform blueprints — full classification

`/home/openova/repos/openova/platform/` contains **61** blueprint directories. The table below classifies each into the tier model in the W1.D spec, with the bootstrap-kit disposition (already present / add in W2 / excluded from omantel-1 with reason).

Legend:
- **Disposition** column — `present` = already in bootstrap-kit (slots 01–14), `W2.Kn` = add in Wave 2 batch n, `excluded` = intentionally not in omantel-1.
- **Layer** = `mgt` (management-cluster control plane), `host` (every host cluster), `app` (per-Org vcluster, App Blueprint).
- The classification follows `PLATFORM-TECH-STACK.md` §2–4 strictly. Where the W1.D tier table conflicts with `PLATFORM-TECH-STACK.md`, this document defers to `PLATFORM-TECH-STACK.md`; conflicts are called out in §6.

| # | Blueprint | Tier | Layer | Disposition | Notes |
|---:|---|---|---|---|---|
| 1 | bp-cilium | 0 | host | present (slot 01) | Root of DAG. |
| 2 | bp-cert-manager | 0 | host | present (slot 02) | TLS automation. |
| 3 | bp-flux | 0 | host | present (slot 03) | GitOps. |
| 4 | bp-crossplane | 0 | mgt | present (slot 04) | Day-2 IaC. |
| 5 | bp-sealed-secrets | 0 | host (transient) | present (slot 05) | Decommissioned after Phase 1. |
| 6 | bp-spire | 1 | host | present (slot 06) | SVIDs. |
| 7 | bp-nats-jetstream | 2 | mgt | present (slot 07) | Event spine. |
| 8 | bp-openbao | 1 | mgt | present (slot 08) | Secret backend. |
| 9 | bp-keycloak | 1 | mgt | present (slot 09) | OIDC. |
| 10 | bp-gitea | 2 | mgt | present (slot 10) | Per-Sovereign Git. |
| 11 | bp-powerdns | 3 | host | present (slot 11) | Per-Sovereign DNS. |
| 12 | bp-external-dns | 3 | host | present (slot 12) | DNS sync. |
| 13 | bp-catalyst-platform (umbrella) | 4 | mgt | present (slot 13) | Control plane umbrella. |
| 14 | bp-crossplane-claims | 0 | mgt | present (slot 14, reserved) | Compositions/Claims wiring. |
| 15 | bp-external-secrets | 0/3 | host | **W2.K1 (slot 15)** | ESO. Day-2 secret pipeline; takes over from sealed-secrets. PR-247 shifted ESO out of slot 5 because OpenBao must be Ready first. |
| 16 | bp-cnpg | 5 | mgt | **W2.K1 (slot 16)** | PG operator. Required by PowerDNS (`pdns-pg`), Keycloak HA, Gitea metadata, Langfuse, Grafana, PDM. |
| 17 | bp-valkey | 5 | mgt | **W2.K1 (slot 17)** | Redis-compatible cache. Used by Catalyst control-plane services for ephemeral session/state. |
| 18 | bp-seaweedfs | 5 | host | **W2.K1 (slot 18)** | S3 encapsulation. Pre-req for Velero, Loki, Mimir, Tempo, Harbor. |
| 19 | bp-harbor | 5 (registry) | host | **W2.K1 (slot 19)** | Per-host registry. Mirrors blueprint OCI artefacts. Depends on SeaweedFS + CNPG. |
| 20 | bp-opentelemetry | 6 | host | **W2.K2 (slot 20)** | OTel Collector. Pipeline source for Mimir/Loki/Tempo. |
| 21 | bp-alloy | 6 | host | **W2.K2 (slot 21)** | Grafana Alloy collector (logs/metrics agent). |
| 22 | bp-loki | 6 | mgt | **W2.K2 (slot 22)** | Logs, SeaweedFS-backed. |
| 23 | bp-mimir | 6 | mgt | **W2.K2 (slot 23)** | Metrics, SeaweedFS-backed. |
| 24 | bp-tempo | 6 | mgt | **W2.K2 (slot 24)** | Traces, SeaweedFS-backed. |
| 25 | bp-grafana | 6 | mgt | **W2.K2 (slot 25)** | UI; CNPG-backed config DB; datasources = Loki + Mimir + Tempo. |
| 26 | bp-langfuse | 6 (LLM obs) | mgt | **W2.K2 (slot 26)** | LLM observability. CNPG + Keycloak (OIDC). |
| 27 | bp-kyverno | 7 | host | **W2.K3 (slot 27)** | Policy engine. Admission control. |
| 28 | bp-reloader | 7 | host | **W2.K3 (slot 28)** | Reload pods on ConfigMap/Secret change. Independent. |
| 29 | bp-vpa | 7 | host | **W2.K3 (slot 29)** | Vertical autoscaler. Independent. |
| 30 | bp-trivy | 7 | host | **W2.K3 (slot 30)** | Scanner. |
| 31 | bp-falco | 7 | host | **W2.K3 (slot 31)** | Runtime security (eBPF). |
| 32 | bp-sigstore | 7 | host | **W2.K3 (slot 32)** | Cosign admission verifier. |
| 33 | bp-syft-grype | 7 | host | **W2.K3 (slot 33)** | SBOM generation + match. |
| 34 | bp-velero | 7 | host | **W2.K3 (slot 34)** | Backup; SeaweedFS-backed. |
| 35 | bp-coraza | 8 (edge) | host | **W2.K4 (slot 35)** | WAF (OWASP CRS). DMZ-edge. Sits in front of Cilium Gateway. |
| 36 | bp-stunner | 8 (edge) | host | **W2.K4 (slot 36)** | K8s-native TURN/STUN. Deployed to support real-time-comms Apps when present; cheap when idle. Included in omantel-1 because Huawei iFlytek demo needs WebRTC paths. |
| 37 | bp-knative | 9 | host | **W2.K4 (slot 37)** | Serverless platform. Pre-req for kserve. |
| 38 | bp-kserve | 9 | host | **W2.K4 (slot 38)** | Model serving. |
| 39 | bp-vllm | 9 | app→bootstrapped | **W2.K4 (slot 39)** | LLM inference. Pinned at bootstrap so Cortex demo is one-click. |
| 40 | bp-llm-gateway | 9 | mgt | **W2.K4 (slot 40)** | Subscription proxy for Claude Code. |
| 41 | bp-anthropic-adapter | 9 | mgt | **W2.K4 (slot 41)** | OpenAI→Anthropic translation. |
| 42 | bp-bge | 9 | host | **W2.K4 (slot 42)** | Embeddings + reranking. |
| 43 | bp-nemo-guardrails | 9 | mgt | **W2.K4 (slot 43)** | AI safety firewall. |
| 44 | bp-temporal | 9 | mgt | **W2.K4 (slot 44)** | Saga orchestration. CNPG-backed. |
| 45 | bp-openmeter | 9 | mgt | **W2.K4 (slot 45)** | Usage metering. ClickHouse-backed in canonical, but for omantel-1 we deploy with the ClickHouse-less profile (CNPG + JetStream stream). See §6.4. |
| 46 | bp-livekit | 9 (relay) | host | **W2.K4 (slot 46)** | WebRTC SFU. Required for the Huawei iFlytek voice demo. |
| 47 | bp-matrix | 9 (relay) | mgt | **W2.K4 (slot 47)** | Team chat (Synapse). CNPG-backed. |
| 48 | bp-librechat | 9 | mgt | **W2.K4 (slot 48)** | Chat UI. Depends on llm-gateway + vllm + bge. |
| 49 | bp-failover-controller | 3.5 | host | **excluded (omantel-1 single-region)** | Multi-region failover; omantel-1 is single-region. Re-add when omantel adds rtz/dmz. |
| 50 | bp-keda | 7 | host | **excluded (omantel-1 ScaleToZero deferred)** | Event-driven autoscale; KServe and HPA cover the omantel-1 demo path. Re-add in W3. |
| 51 | bp-clickhouse | 5 | app | **excluded (heavy, OLAP-only)** | OpenMeter-without-ClickHouse profile is used (§6.4). Add when an analytics-heavy Org onboards. |
| 52 | bp-strimzi | 5 | app | **excluded (kafka duplicates NATS)** | NATS JetStream is the control-plane bus; Strimzi is an opt-in App Blueprint per `PLATFORM-TECH-STACK.md` §4.1. |
| 53 | bp-flink | 5 | app | **excluded (no streaming workload at omantel-1)** | Add when Fabric is enabled. |
| 54 | bp-debezium | 5 | app | **excluded (no CDC workload at omantel-1)** | Pairs with strimzi; deferred together. |
| 55 | bp-iceberg | 5 | app | **excluded (no lakehouse at omantel-1)** | Pairs with clickhouse/flink. |
| 56 | bp-opensearch | 5 | app | **excluded (heavy, app-tier only)** | Logs go to Loki; SIEM use-case is W3. |
| 57 | bp-ferretdb | 5 | app | **excluded (no MongoDB consumer at omantel-1)** | Add on demand. |
| 58 | bp-milvus | 5 | app | **excluded (no vector workload at omantel-1)** | Cortex demo uses bge+pgvector via CNPG. Add when corpus size ≥ 1M docs. |
| 59 | bp-neo4j | 5 | app | **excluded (no graph workload at omantel-1)** | Add when Specter is enabled. |
| 60 | bp-stalwart | 9 (relay) | app | **excluded (mail server is per-Sovereign post-Phase-2)** | Stalwart sits inside an Org's Relay vcluster, not in bootstrap-kit. |
| 61 | bp-guacamole | 9 (relay) | app | **excluded (admin-tooling, deferred)** | Add post-handoff if SRE needs browser remote-desktop. |
| 62 | bp-litmus | 9 | app | **excluded (chaos engineering, post-GA)** | Production-readiness add-on. |
| 63 | bp-opentofu | 0 | bootstrap-only | **excluded (Phase-0-only)** | OpenTofu runs once in the catalyst-provisioner; never deployed on a host cluster. |

> **Count check.** `present` = 14 (slots 01–14). `W2.K1`+`W2.K2`+`W2.K3`+`W2.K4` = 5 + 7 + 8 + 14 = **34 added** → end-state bootstrap-kit = **48 HRs**, well above the ≥40 target. The Sovereign reaches `48 Ready` only when every dependsOn chain converges; §2 below proves the DAG is finite-depth.

> **Catalog math.** 48 (bootstrap) + 0 (deferred for omantel-1) = 48 in-cluster HRs. The remaining 13 platform blueprints (#49–#63 above, minus the 2 not in `platform/` proper) are **registered in the marketplace catalog** but not pre-installed; Users opt them in per Application via the standard Catalyst Application flow. Marketplace registration is owned by `bp-catalyst-platform` (catalog-svc reads `platform/*/blueprint.yaml`) — no bootstrap-kit slot needed.

---

## 2. Dependency graph (Flux `dependsOn` semantics)

### 2.1 Conventions

- Edges are Flux `spec.dependsOn` declarations on the dependent HR. A → B means "A is declared as a dependency of B; Flux will not install B until A is `Ready`".
- **Hard implicit deps** (CRDs from a sibling) are noted as `[CRD]` annotations and must be expressed as `dependsOn` if the producing HR is in the kit.
- **Soft implicit deps** (e.g. an app reads a Service that another HR creates but only at runtime) are documented but not encoded — Flux retries on failure.
- All edges below are **machine-checkable** by the W2.audit step: each W2 PR's CI job runs `scripts/check-bootstrap-deps.sh` which parses every HR's `dependsOn` and asserts the union matches the DAG below. (Script ownership: W2.K0 — see §3.)

### 2.2 Tier 0–4 (current — for context)

```
                    bp-cilium (01)
                         │
                         ▼
                   bp-cert-manager (02)
                   ╱  │  │  │   ╲
                  ╱   │  │  │    ╲
                 ▼    ▼  ▼  ▼     ▼
        bp-flux(03) bp-sealed   bp-spire(06)   bp-keycloak(09)
              │     -secrets(05)   │ │
              ▼                    │ │
        bp-crossplane(04)          │ ▼
              │                    │ bp-openbao(08)
              ▼                    │
        bp-crossplane-             ▼
        claims(14)              bp-nats-jetstream(07)

        bp-cert-manager ──► bp-powerdns(11) ──► bp-external-dns(12)
        bp-keycloak     ──► bp-gitea(10)    ──► bp-catalyst-platform(13)
```

### 2.3 Tier 5 (W2.K1: storage + DB)

```
bp-flux(03) ──► bp-cnpg(16)
bp-flux(03) ──► bp-valkey(17)
bp-flux(03), bp-cert-manager(02) ──► bp-seaweedfs(18)
bp-cnpg(16), bp-seaweedfs(18), bp-cert-manager(02) ──► bp-harbor(19)
bp-openbao(08), bp-cert-manager(02) ──► bp-external-secrets(15)
```

`bp-external-secrets` is sequenced first in the slot order (15) to make slot ranges contiguous, but its `dependsOn` is `[bp-openbao, bp-cert-manager]` — Flux will install it only after Tier 1 is Ready, regardless of slot number.

### 2.4 Tier 6 (W2.K2: observability)

```
bp-cert-manager(02) ──► bp-opentelemetry(20)
bp-opentelemetry(20) ──► bp-alloy(21)
bp-seaweedfs(18) ──► bp-loki(22)
bp-seaweedfs(18) ──► bp-mimir(23)
bp-seaweedfs(18) ──► bp-tempo(24)
bp-cnpg(16), bp-loki(22), bp-mimir(23), bp-tempo(24), bp-keycloak(09) ──► bp-grafana(25)
bp-cnpg(16), bp-keycloak(09), bp-cert-manager(02) ──► bp-langfuse(26)
```

### 2.5 Tier 7 (W2.K3: security + policy)

Most Tier 7 blueprints are independent of Tier 5/6 and parallelize freely. The exceptions are velero (needs SeaweedFS) and trivy/grype (their UIs benefit from Grafana but don't depend on it).

```
bp-cilium(01) ──► bp-kyverno(27)
(none) ──► bp-reloader(28)
(none) ──► bp-vpa(29)
bp-cert-manager(02) ──► bp-trivy(30)
bp-cilium(01) ──► bp-falco(31)
bp-cert-manager(02) ──► bp-sigstore(32)
bp-cert-manager(02) ──► bp-syft-grype(33)
bp-seaweedfs(18) ──► bp-velero(34)
```

### 2.6 Tier 8 (W2.K4 prefix: edge)

```
bp-cilium(01), bp-cert-manager(02) ──► bp-coraza(35)
bp-cilium(01), bp-cert-manager(02) ──► bp-stunner(36)
```

### 2.7 Tier 9 (W2.K4: apps + AI runtime)

```
bp-cert-manager(02) ──► bp-knative(37)
bp-knative(37) ──► bp-kserve(38)
bp-kserve(38) ──► bp-vllm(39)
bp-cnpg(16), bp-keycloak(09) ──► bp-llm-gateway(40)
bp-llm-gateway(40) ──► bp-anthropic-adapter(41)
bp-cnpg(16) ──► bp-bge(42)
bp-llm-gateway(40), bp-bge(42), bp-cnpg(16) ──► bp-nemo-guardrails(43)
bp-cnpg(16), bp-cert-manager(02) ──► bp-temporal(44)
bp-cnpg(16), bp-nats-jetstream(07) ──► bp-openmeter(45)   # ClickHouse-less profile
bp-stunner(36), bp-cert-manager(02) ──► bp-livekit(46)
bp-cnpg(16), bp-keycloak(09), bp-cert-manager(02) ──► bp-matrix(47)
bp-llm-gateway(40), bp-vllm(39), bp-bge(42), bp-keycloak(09) ──► bp-librechat(48)
```

### 2.8 Full DAG depth

The longest dependency chain (max chain length, root → leaf):

```
bp-cilium (1)
  → bp-cert-manager (2)
    → bp-spire (3)
      → bp-openbao (4)
        → bp-keycloak (5)              # via cert-manager parallel branch resolves identically
          → bp-gitea (6)
            → bp-catalyst-platform (7) # the umbrella

# OR through observability:
bp-cilium (1)
  → bp-cert-manager (2)
    → bp-flux (3)
      → bp-cnpg (4)
        → bp-loki (5)                  # waits for seaweedfs at depth 5 too
          → bp-grafana (6)
            (terminal)

# OR through AI runtime (deepest):
bp-cilium (1)
  → bp-cert-manager (2)
    → bp-flux (3)
      → bp-cnpg (4)
        → bp-llm-gateway (5)
          → bp-bge (6) ← parallel chain
            → bp-nemo-guardrails (7)
              (terminal)

# Deepest librechat path:
bp-cilium → bp-cert-manager → bp-knative → bp-kserve → bp-vllm
                                                       ↓
bp-cilium → bp-cert-manager → bp-flux → bp-cnpg → bp-llm-gateway
                                                       ↓
                                           bp-librechat (joins all three)

# librechat depth (longest path through it):
bp-cilium → bp-cert-manager → bp-knative → bp-kserve → bp-vllm → bp-librechat   = 6
```

**Max chain length = 7** (cilium → cert-manager → spire → openbao → keycloak → gitea → catalyst-platform). All other chains are ≤ 7. This is well within Flux's ability to converge — at 1-min reconcile interval the worst-case full bring-up is ~7–10 minutes once images are cached.

---

## 3. File-slot allocation per Wave 2 batch

### 3.1 Slot ranges

| W2 batch | Slot range | Tier(s) | Blueprint count | Why isolated |
|---|---:|---|---:|---|
| **W2.K0** — pre-flight | (no slot) | n/a | 0 (script only) | Adds `scripts/check-bootstrap-deps.sh` and `tests/e2e/bootstrap-kit/dag-audit.sh`. Ungated; merged before K1–K4. |
| **W2.K1** — Storage + DB | `15-` … `19-` | 5 + ESO | 5 | DB foundation; everything in K2/K4 reads from CNPG or SeaweedFS. |
| **W2.K2** — Observability | `20-` … `26-` | 6 | 7 | Depends on K1 (CNPG, SeaweedFS); independent of K3/K4. |
| **W2.K3** — Security/policy | `27-` … `34-` | 7 | 8 | Mostly independent of K1/K2 (only velero needs SeaweedFS). Can run in parallel with K2. |
| **W2.K4** — Edge + Apps + AI runtime | `35-` … `48-` | 8 + 9 | 14 | Depends on K1 (CNPG) and K2 (otel/grafana); coraza/stunner are slot-prefixed in this range to keep edge contiguous. |

**Confirmation of W1.D's proposed allocation.** The W1.D spec sketched K1 = 15–19 (Tier 5), K2 = 20–26 (Tier 6), K3 = 27–34 (Tier 7), K4 = 35–48 (Tier 9). This document **confirms** that allocation with two adjustments:

1. **`bp-external-secrets` rolled into K1 at slot 15** (not "Tier 0 — Foundation" as the W1.D table implied). Rationale: ESO HelmRelease reconciles cleanly only when OpenBao is `Ready`; since OpenBao is slot 08 (Tier 1), placing ESO at slot 15 puts it *after* its hard dep, and grouping it with K1's storage cohort avoids fragmenting Tier 0.
2. **Tier 8 (`bp-coraza`, `bp-stunner`) merged into K4's prefix (slots 35–36)** rather than a fifth batch. Rationale: only 2 blueprints; W2.K4 already owns the slot range; a fifth batch buys nothing and adds a merge-conflict point.

No other deviations from W1.D's proposal.

### 3.2 Slot-numbering rule

Each W2 agent appends new HR files **in numeric order, contiguously**, starting at the first slot in their range. They MUST NOT skip slot numbers and MUST NOT extend past their assigned range without raising a W1.D-amend ticket first.

If an agent finds they need more slots (e.g. K2 discovers the canonical otel chart pattern needs a separate `bp-otel-operator` HR), they:
1. Stop work.
2. Open a `w2.amend` ticket against this document.
3. Wait for a `docs/bootstrap-kit-expansion-plan` follow-up PR re-allocating ranges.

This avoids the failure mode where K2 quietly reaches into K3's range and creates an unresolvable merge conflict.

---

## 4. `kustomization.yaml` merge protocol

### 4.1 The contention point

`clusters/_template/bootstrap-kit/kustomization.yaml` is a single file with a single `resources:` list. Each W2 PR appends ~5–14 entries to that list. Four parallel PRs ⇒ four conflicting append blocks ⇒ guaranteed `git merge` conflicts on whichever PRs land second/third/fourth.

### 4.2 Resolution: append in numeric order, rebase on merge

Every W2 PR follows this rule:

1. **Branch off `main`** at the time the PR is opened.
2. The agent edits `kustomization.yaml` to append **only their own slots**, in numeric order, immediately after the last slot present on `main` at branch time. (Do **not** preserve other in-flight PRs' slot entries — that creates phantom commits.)
3. **PR merge order is K0 → K1 → K2 → K3 → K4** (enforced by labels: `w2/k1`, `w2/k2`, `w2/k3`, `w2/k4`).
4. After K1 merges, K2/K3/K4 PRs **rebase on `main`**. The conflict on `kustomization.yaml` is structural (both branches appended to the same list); the resolution is mechanical — keep both blocks, in slot-number order. The agent's pre-rebase content is preserved verbatim; only the position shifts.
5. The K2/K3/K4 rebase commits are signed off by the rebasing agent and recorded in the PR body as `Rebased on K1 at <commit-sha>`.

### 4.3 Worked example

State after K1 merges (slots 01–19):
```yaml
resources:
  - 01-cilium.yaml
  - …
  - 14-bp-crossplane-claims.yaml
  - 15-bp-external-secrets.yaml
  - 16-bp-cnpg.yaml
  - 17-bp-valkey.yaml
  - 18-bp-seaweedfs.yaml
  - 19-bp-harbor.yaml
```

K2's PR was opened against `main` at slot-14 head, with this append:
```yaml
  - 20-bp-opentelemetry.yaml
  - 21-bp-alloy.yaml
  - …
  - 26-bp-langfuse.yaml
```

When K2 rebases on the now-merged K1, `git rebase` reports a conflict on `kustomization.yaml`. Resolution: keep K1's slots 15–19 (the upstream side) AND K2's slots 20–26 (the local side). Final ordering preserves slot numbers — no manual reasoning required.

### 4.4 Why this works

- File-slot isolation (§3) means HR file names never collide.
- The `resources:` list is order-preserving but Flux doesn't care about list order — `dependsOn` is the actual install ordering.
- Numeric slot prefix makes the merge resolution algorithmic, not editorial.

### 4.5 Guard

Each W2 PR's CI runs:
```bash
scripts/check-bootstrap-deps.sh \
  --kustomization clusters/_template/bootstrap-kit/kustomization.yaml \
  --hrs clusters/_template/bootstrap-kit/*.yaml \
  --dag docs/BOOTSTRAP-KIT-EXPANSION-PLAN.md
```
This script (owned by W2.K0) parses every HR's `dependsOn`, parses the DAG in §2 of this doc, and fails the PR if there's drift in either direction. That guarantee removes the "did anyone double-check the dep edges?" review burden.

---

## 5. Smoke test plan per blueprint

Each W2 PR adds, in `tests/e2e/bootstrap-kit/<slot>-<bp>.sh`, a **single 1-line readiness probe** that proves the HR is `Ready` AND its primary surface answers. The W2.K0 harness runs every probe against the omantel cluster after the kit reconciles, with a 10-minute timeout.

| Slot | Blueprint | 1-line readiness probe |
|---:|---|---|
| 15 | bp-external-secrets | `kubectl wait --for=condition=Ready externalsecret -A --all --timeout=60s` |
| 16 | bp-cnpg | `kubectl get cluster.postgresql.cnpg.io -A -o jsonpath='{.items[?(@.status.phase=="Cluster in healthy state")].metadata.name}' \| grep .` |
| 17 | bp-valkey | `kubectl exec -n valkey deploy/valkey -- valkey-cli ping \| grep -q PONG` |
| 18 | bp-seaweedfs | `curl -fsS http://seaweedfs.seaweedfs.svc.cluster.local:8333/status \| jq -e '.status=="OK"'` |
| 19 | bp-harbor | `curl -fsS https://harbor.<sov>/api/v2.0/health \| jq -e '.status=="healthy"'` |
| 20 | bp-opentelemetry | `kubectl get otelcol -A -o jsonpath='{.items[*].status.phase}' \| grep -qv Failed` |
| 21 | bp-alloy | `kubectl rollout status -n alloy ds/alloy --timeout=60s` |
| 22 | bp-loki | `curl -fsS http://loki.loki.svc.cluster.local:3100/ready \| grep -q ready` |
| 23 | bp-mimir | `curl -fsS http://mimir.mimir.svc.cluster.local:8080/ready \| grep -q ready` |
| 24 | bp-tempo | `curl -fsS http://tempo.tempo.svc.cluster.local:3200/ready \| grep -q ready` |
| 25 | bp-grafana | `curl -fsS https://grafana.<sov>/api/health \| jq -e '.database=="ok"'` |
| 26 | bp-langfuse | `curl -fsS https://langfuse.<sov>/api/public/health \| jq -e '.status=="OK"'` |
| 27 | bp-kyverno | `kubectl get clusterpolicies.kyverno.io -o jsonpath='{.items[*].status.ready}' \| grep -q true` |
| 28 | bp-reloader | `kubectl rollout status -n reloader deploy/reloader --timeout=60s` |
| 29 | bp-vpa | `kubectl get verticalpodautoscalercheckpoints -A 2>&1 \| grep -qv "the server doesn't have a resource"` |
| 30 | bp-trivy | `kubectl rollout status -n trivy-system deploy/trivy-operator --timeout=60s` |
| 31 | bp-falco | `kubectl rollout status -n falco ds/falco --timeout=60s` |
| 32 | bp-sigstore | `kubectl get policy.policy.sigstore.dev -A -o name \| grep .` |
| 33 | bp-syft-grype | `kubectl rollout status -n syft-grype deploy/syft-grype --timeout=60s` |
| 34 | bp-velero | `kubectl get backuplocation -A -o jsonpath='{.items[*].status.phase}' \| grep -q Available` |
| 35 | bp-coraza | `curl -fsS -H "X-Test-WAF: 1' OR 1=1 --" https://<sov>/ -o /dev/null -w '%{http_code}' \| grep -q 403` |
| 36 | bp-stunner | `kubectl get gateway -n stunner -o jsonpath='{.items[*].status.conditions[?(@.type=="Programmed")].status}' \| grep -q True` |
| 37 | bp-knative | `kubectl get knativeserving -A -o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].status}' \| grep -q True` |
| 38 | bp-kserve | `kubectl get inferenceservice -A 2>&1 \| grep -qv "the server doesn't have a resource"` |
| 39 | bp-vllm | `kubectl get inferenceservice -n cortex -o jsonpath='{.items[*].status.conditions[?(@.type=="Ready")].status}' \| grep -q True` |
| 40 | bp-llm-gateway | `curl -fsS https://llm-gateway.<sov>/health \| jq -e '.status=="ok"'` |
| 41 | bp-anthropic-adapter | `curl -fsS https://anthropic-adapter.<sov>/v1/models \| jq -e '.data[0].id'` |
| 42 | bp-bge | `curl -fsS -X POST https://bge.<sov>/embed -d '{"texts":["hello"]}' \| jq -e '.embeddings \| length > 0'` |
| 43 | bp-nemo-guardrails | `curl -fsS https://guardrails.<sov>/v1/health \| jq -e '.status=="ok"'` |
| 44 | bp-temporal | `kubectl exec -n temporal deploy/temporal-frontend -- tctl --address localhost:7233 cluster get-search-attributes 2>&1 \| grep -qv error` |
| 45 | bp-openmeter | `curl -fsS https://openmeter.<sov>/api/v1/health \| jq -e '.status=="ok"'` |
| 46 | bp-livekit | `curl -fsS https://livekit.<sov>/ \| grep -q "LiveKit"` |
| 47 | bp-matrix | `curl -fsS https://matrix.<sov>/_matrix/client/versions \| jq -e '.versions \| length > 0'` |
| 48 | bp-librechat | `curl -fsS https://librechat.<sov>/api/health \| grep -q OK` |

The harness substitutes `<sov>` with `omantel.omani.works` at runtime.

---

## 6. Excluded from omantel-1 (rationale)

The blueprints below are intentionally **not** in `bootstrap-kit-1` for the omantel Sovereign. Each is registered in the marketplace catalog (via `bp-catalyst-platform`'s catalog-svc) so an Org can opt in per Application — they're just not pre-installed on the host cluster.

### 6.1 Multi-region resilience (deferred until omantel adds rtz/dmz)

| Blueprint | Reason |
|---|---|
| **bp-failover-controller** | Implements lease-based multi-region failover. omantel-1 is single-region (mgt only); failover is a no-op. Re-add when omantel adds a workload region. |

### 6.2 Heavy data services (App-tier; deferred until first consumer)

| Blueprint | Reason |
|---|---|
| **bp-clickhouse** | OLAP. omantel-1 uses the OpenMeter-without-ClickHouse profile (CNPG + JetStream stream). Add when an analytics-heavy Org onboards. |
| **bp-strimzi** | Kafka. NATS JetStream is the control-plane bus per `PLATFORM-TECH-STACK.md` §4.1; Strimzi is opt-in. Defer until a customer App requires Kafka semantics. |
| **bp-flink** | Stream processing. Pairs with Kafka/Iceberg; not needed at omantel-1. |
| **bp-debezium** | CDC source-of-record into Kafka. Pairs with Strimzi; deferred together. |
| **bp-iceberg** | Lakehouse table format. Pairs with Flink/ClickHouse; deferred together. |
| **bp-opensearch** | Heavy SIEM/search backend. Logs go to Loki at omantel-1; SIEM is a W3 add. |
| **bp-ferretdb** | MongoDB wire protocol. No MongoDB consumer at omantel-1. Add on demand. |
| **bp-milvus** | Vector DB. The Cortex demo uses bge + pgvector via CNPG (small corpus). Add when corpus ≥ 1M docs. |
| **bp-neo4j** | Graph DB. Specter (AIOps) uses it; Specter isn't part of omantel-1. |

### 6.3 App Blueprints that live inside Org vclusters (per `PLATFORM-TECH-STACK.md` §4.5 / §4.7)

| Blueprint | Reason |
|---|---|
| **bp-stalwart** | Mail server. Per-Org vcluster (Relay product), not host-cluster. The contabo-mkt Stalwart is a separate provisioner-tier deployment. |
| **bp-guacamole** | Browser remote-desktop gateway. Admin-tooling; deferred to post-handoff if SRE asks. |
| **bp-litmus** | Chaos engineering. Production-readiness add-on; post-GA. |

### 6.4 OpenMeter ClickHouse-less profile note

`bp-openmeter` IS in omantel-1 (slot 45). The canonical OpenMeter chart depends on ClickHouse; we deploy the ClickHouse-less profile (CNPG materialized views + a JetStream subject for raw events). This profile is a chart-level toggle in `platform/openmeter/chart/values.yaml` (`backend.kind: cnpg`); no chart fork required.

If a future Org needs the high-cardinality OLAP path, ClickHouse is added per §6.2 and openmeter is re-rolled with `backend.kind: clickhouse`. This decision is documented here, not in chart values, so it's discoverable.

### 6.5 Bootstrap-only blueprints (Phase-0 only)

| Blueprint | Reason |
|---|---|
| **bp-opentofu** | Runs once in the catalyst-provisioner during Phase 0; never deployed on a host cluster. Not a bootstrap-kit candidate. |

### 6.6 Autoscale-deferred

| Blueprint | Reason |
|---|---|
| **bp-keda** | Event-driven autoscale + scale-to-zero. omantel-1 demo path uses HPA + KServe-native autoscale; KEDA value lands when Apps run mostly-idle. Re-add in W3. |

### 6.7 Conflict with W1.D's tier table — for the record

The W1.D dispatch spec listed several blueprints as "Tier 5 / Tier 9 — Add to bootstrap-kit". Of those, the following are excluded above with stated rationale:

- Tier 5: `opensearch`, `seaweedfs` (kept), `ferretdb` (excluded), `neo4j` (excluded), `milvus` (excluded). Five sub-decisions.
- Tier 9: `harbor` is in W2.K1 (registry — slot 19) not W2.K4. Rationale: Harbor stores container images and is a consumer of CNPG + SeaweedFS, so it sits with the storage cohort, not the apps cohort.

These deviations are deliberate and follow `PLATFORM-TECH-STACK.md` §3.5 (registry sits in storage tier) and §4.1 (App-Blueprint data services are user-installed, not bootstrapped).

---

## 7. End-state count

| Category | Count |
|---:|---|
| Bootstrap-kit HRs after W2 (slots 01–48) | **48** |
| Of which `Ready` on omantel-1 day-1 | **48** (all HRs in the kit are bootstrap-required for omantel-1) |
| Marketplace-registered, NOT pre-installed | **15** (the §6 excluded set + bp-litmus + bp-opentofu) |
| Total `platform/` blueprints registered in catalog | **61** |

End-state on omantel-1: **48 ≥ 40 target.** ✅

---

## 8. References

- [`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md) §2 (Catalyst control plane), §3 (Per-host infra), §4 (App Blueprints), §5 (Composite Blueprints).
- [`PROVISIONING-PLAN.md`](PROVISIONING-PLAN.md) §3 (architectural agreements), §4 (8-phase waterfall).
- [`ARCHITECTURE.md`](ARCHITECTURE.md) §11 (Catalyst-on-Catalyst).
- [`BLUEPRINT-AUTHORING.md`](BLUEPRINT-AUTHORING.md) (HR file conventions, `dependsOn` semantics).
- Current `clusters/_template/bootstrap-kit/` slots 01–14 (post-PR-247 baseline).

---

## 9. Out of scope for this PR

- **Implementation.** No HR files created. No `kustomization.yaml` edited. No charts touched. Implementation lands in W2.K0 → W2.K4, four parallel PRs, against this doc as the contract.
- **Issue creation.** W2 dispatch process owns ticket creation; this PR is design-only.
- **Authoring rules.** Each blueprint's HR follows `BLUEPRINT-AUTHORING.md` — this doc only addresses *which* blueprints, *which slot*, *which deps*. Per-HR YAML structure is canonical.
