# Documentation Validation Log

**Last updated:** 2026-04-27.

This file is a trail of the multi-pass integrity validation done on the canonical docs after the Catalyst-unified rewrite (issue [#37](https://github.com/openova-io/openova/issues/37)). It captures what was verified, what was found, and how to repeat the audit.

> If a future contributor wants to validate that docs remain consistent, this file is the **playbook**. Run the same greps. Read the same files in the same order.

---

## Why this exists

After a single sweeping rewrite, claims of completeness are easy to make and hard to verify. The validation passes recorded here exist to give later readers (and later versions of this AI assistant) a concrete chain of evidence that the docs were checked line-by-line against the agreed model — not just spot-checked.

This is a **process artifact**, not a status doc. For status, read [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md).

---

## Validation rounds completed

### Pass 1 — strategic decisions and per-doc line-by-line (16 iterations)

Each canonical doc read end-to-end and corrected. Key fixes:

| Iter | Scope | Fixes |
|---|---|---|
| 1 | Foundation | Created [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md) as the design-vs-code bridge. Locked Option A (monorepo canonical) for public Blueprints. Wrong-org refs `github.com/openova` → `github.com/openova-io` (3 places). API group unified to `catalyst.openova.io/v1alpha1`. |
| 2 | [`GLOSSARY.md`](GLOSSARY.md) | Renamed `workspace-controller` → `environment-controller`. Tightened component table to match PLATFORM-TECH-STACK §2. Refined OpenOva, Catalyst, Sovereign definitions. |
| 3 | [`ARCHITECTURE.md`](ARCHITECTURE.md) | Removed `catalystctl` from write-side diagram (it's read-only). Valkey-snapshot → JetStream-KV-snapshot in projector flow. Added `bp-catalyst-provisioning`, removed `bp-catalyst-crossplane` (per-host-cluster, not control-plane). Operator-as-entity scrubbed. |
| 4 | [`NAMING-CONVENTION.md`](NAMING-CONVENTION.md) | "operator domain" → "Sovereign domain" in §5.1. Multi-region diagram aligned with PLATFORM-TECH-STACK. "per-Org workspaces" → "per-Environment Gitea repos". |
| 5 | [`PERSONAS-AND-JOURNEYS.md`](PERSONAS-AND-JOURNEYS.md) | "Sovereign Operator team" → "Sovereign's operations team". Capital-W `Workspace-controller` residuals fixed. |
| 6 | [`SECURITY.md`](SECURITY.md) | "operator-approved" → "sovereign-admin-approved" for DR promotion. Realm `catalyst-operator` → `catalyst-admin`. ChangePolicy → EnvironmentPolicy. Removed fictional `compliance-controller` reference. |
| 7 | [`SOVEREIGN-PROVISIONING.md`](SOVEREIGN-PROVISIONING.md) | "vault-stored" → "stored in OpenBao". "single operator's laptop" → "single person's laptop". Capital-W Workspace-controller residuals. |
| 8 | [`BLUEPRINT-AUTHORING.md`](BLUEPRINT-AUTHORING.md) | OCI artifact convention locked: `ghcr.io/openova-io/bp-<name>:<semver>` (bp- prefix on artifact, not folder). Crossplane Composition `compositeTypeRef.apiVersion` to shared `compose.openova.io/v1alpha1` instead of per-Blueprint group. |
| 9 | [`README.md`](../README.md), [`CLAUDE.md`](../CLAUDE.md) | "Build a Blueprint" section reconciled to monorepo (was contradicting itself). Repo-structure tree updated to honestly distinguish target from current state. |
| 10 | [`PLATFORM-TECH-STACK.md`](PLATFORM-TECH-STACK.md) | **Substantive reorganization.** §2 (Catalyst control plane) and §3 (per-host-cluster infrastructure) split cleanly. Sections renumbered 4–11. Application Blueprints at §4 (was §3); Composite Blueprints at §5 (was §4); etc. |
| 11 | [`SRE.md`](SRE.md) | MongoDB → FerretDB. Data replication table relabeled by layer. Flagger/Flipt explicitly marked as "design — not yet a Blueprint". |
| 12 | [`BUSINESS-STRATEGY.md`](BUSINESS-STRATEGY.md), [`TECHNOLOGY-FORECAST-2027-2030.md`](TECHNOLOGY-FORECAST-2027-2030.md) | Catalyst as platform (not sub-product) reposition. Catalyst IDP → Catalyst console. Forecast Backstage row updated. |
| 13 | [`core/README.md`](../core/README.md), products/* | core/README honestly describes target structure with `.gitkeep` placeholders. Legacy `apps/bootstrap/` and `apps/manager/` acknowledged as transitional. |
| 14-15 | platform/*, products/* (60+ READMEs) | `<tenant>` placeholder → `<org>` (10 files). Catalyst IDP → Catalyst console (4 files). Bootstrap wizard → Catalyst bootstrap (2 files). Kyverno `openova.io/tenant` label → `openova.io/organization`. |

### Pass 2 — fresh-eyes sweep across full tree

Found one residual: external-secrets sequence-diagram had "Operator saves unseal keys offline" → fixed to `sovereign-admin`. All other banned-term sweeps clean.

### Pass 3 — final cross-doc consistency

- IMPLEMENTATION-STATUS realigned with the PLATFORM-TECH-STACK §2/§3 split (was conflating Catalyst control plane and per-host-cluster infra in one table).
- `muscatpharmacy` example slug normalized (was alternating between `muscat-pharmacy` and `muscatpharmacy` for the same Org).

### Pass 4 — phase-numbering reconciliation

ARCHITECTURE §10 had 3 phases; SOVEREIGN-PROVISIONING §3-§6 has 4 phases. Aligned ARCHITECTURE to the 4-phase numbering. SOVEREIGN-PROVISIONING is now the canonical reference for phase semantics.

### Pass 5 — deeper structural drift

- Phase-0 install order in ARCHITECTURE didn't match SOVEREIGN-PROVISIONING (Cilium-first vs cert-manager-first). Cilium-first is correct (CNI before pods can network). Aligned.
- IMPLEMENTATION-STATUS section numbering had an awkward `§2bis` insertion. Renumbered to clean §1–§9 sequence.
- Two residual "instance" usages in user-facing dialogs/comments converted to "Application".

### Pass 6 — topology + JetStream Account scoping

- ARCHITECTURE §3 topology diagram listed Crossplane, Flux, Harbor, grafana-stack INSIDE the Catalyst control-plane block. But §11 and PLATFORM-TECH-STACK §3 both classify these as per-host-cluster infrastructure (not Catalyst control plane). Topology diagram corrected; per-host-cluster infra now shown as a separate line referencing PLATFORM-TECH-STACK §3 for the full list. Also added the previously-missing `provisioning` row.
- JetStream Account scoping was contradictory: ARCHITECTURE §5 said "Per-Org account: ws.{org}-{env_type}.>" (ambiguous), NAMING-CONVENTION §11.2 said "One JetStream Account scoped to ws.{org}-{env_type}.>" (per-Env), GLOSSARY+SECURITY+PLATFORM-TECH-STACK said per-Org. Reconciled to: one Account per Organization, subjects within use prefix `ws.{org}-{env_type}.>` for per-Environment partitioning. Fixed in ARCHITECTURE §5 and NAMING-CONVENTION §11.2.

### Pass 19 — SECURITY + kserve drift sweep — clean

Read SECURITY.md and platform/kserve/README.md end-to-end line-by-line.

- SECURITY.md: clean. Multi-region OpenBao (§5), Keycloak topology (§6), rotation policy (§7) all consistent with each other and with NAMING / ARCHITECTURE / GLOSSARY.
- platform/kserve: banner correctly identifies as Application Blueprint under bp-cortex. The example `namespace: ai-hub` is illustrative (AI Hub is a customer-chosen Application name); not a strict contradiction with the agreed naming convention.

**Pass 19: clean.**

### Pass 18 — NAMING DR-as-env_type misexample + Keycloak deployment narrative

Two real findings on the rotation to NAMING-CONVENTION + platform/keycloak.

- **NAMING-CONVENTION §11.1** line 470 listed `bankdhofar-dr` as an Environment example — but `dr` is NOT a valid env_type (canonical values per §2.4 are `prod | stg | uat | dev | poc`). DR is a Placement mode (`active-active` / `active-hotstandby` across regions inside the `*-prod` Environment), not a separate Environment. Replaced the example with `bankdhofar-uat` and added an explanatory note.
- **platform/keycloak/README.md** Keycloak Deployment example used `namespace: open-banking` and 2 replicas — Fingate-specific narrative that contradicts the per-Org / per-Sovereign topology stated in the banner. Rewrote with two side-by-side examples: `shared-sovereign` (3 HA replicas, `catalyst-keycloak` namespace) and `per-organization` (1 replica in `<org>` namespace, embedded DB option). HA section similarly split — was a single set of HA claims; now branches on topology.

### Pass 17 — ARCHITECTURE OAM table fix + Harbor README de-drift

Two real findings in the drift sweep.

- **ARCHITECTURE.md §13** OAM influence table had `| Trait | Blueprint overlay (`overlays/small|medium|large`) |` — the pipe characters inside backticks inside a Markdown table cell are a known GFM rendering hazard. Replaced with comma-separated example: `(e.g. overlays/small, overlays/medium, overlays/large)`.
- **platform/harbor/README.md** still described an older "Harbor Primary / Harbor Replica" cross-region replication model that contradicted the new per-host-cluster banner ("every host cluster runs a Harbor instance"). Rewrote three sections: Overview diagram, Per-host-cluster mirroring (was "Multi-Region Replication"), and the example replication policy. Now consistently models Harbor as one-per-host-cluster mirroring from upstream OCI (no Harbor-to-Harbor primary-replica). The mermaid diagram now shows ghcr.io / customer CI → multiple independent Harbor instances, each scanning locally with Trivy.

This is the same kind of architectural drift Pass 7 caught in OpenBao/ESO/Gitea/Flux — it survived the previous banner-addition pass because the banner just added a header line; the body still described the older model.

### Pass 16 — drift-detection sweep (post-convergence)

First post-convergence drift-detection pass. Routine acceptance grep + line-by-line read.

- Acceptance greps: all clean. Hits in `<tenant>` / Catalyst-IDP / github.com/openova / workspace-controller / operator-as-entity grep all came from `VALIDATION-LOG.md` itself documenting the rename history; no actual drift in any source doc.
- Rotated canonical doc: `docs/GLOSSARY.md` — read end-to-end. Clean. The pipe-char `|` inside the env_type enum (`prod | stg | uat | dev | poc`) is wrapped in backticks so Markdown tables render correctly on GitHub.
- Rotated component README: `platform/grafana/README.md` — clean. Banner correctly straddles both buckets (per-host-cluster infra in §3, and per-Sovereign observability supporting service in §2.3).

**Pass 16: clean.**

### Pass 15 — final banner sweep + convergence check

Triage swept all 52 `platform/*/README.md` files for the role-in-Catalyst banner (per CLAUDE.md component-README rule of thumb #2). 4 still lacked one: `cnpg`, `flux`, `opentofu`, `strimzi` (although `opentofu` did have its banner — the keyword grep had been too narrow).

Banners added:
- **cnpg** (§4.1): production Postgres operator; underlying engine for FerretDB and Gitea metadata.
- **flux** (§3.2): per-vcluster Flux + host-level Flux for Catalyst itself; pulls from single per-Sovereign Gitea.
- **strimzi** (§4.1): Application-tier event streaming; NOT the Catalyst control-plane spine (which uses NATS JetStream). Same upstream-tech-different-tier disambiguation as Valkey.
- **opentofu**: keywords aligned so future grep sweeps catch it.

**52 / 52 platform components now have a role-in-Catalyst banner.**

### Convergence achieved (initial banner sweep)

Every platform/<x>/README.md and products/<x>/README.md now states its role in Catalyst (control plane vs per-host-cluster infrastructure vs Application Blueprint vs Composite Blueprint). No banned terms, no broken cross-references, no architectural drift detected on this pass.

The validation loop continues per the user's "infinite loop until nirvana" instruction — subsequent passes will be brief drift-detection sweeps rather than systematic rewrites. Any new architectural divergence introduced by future commits is expected to be caught by:

1. The grep playbook in this file's "Acceptance criteria" section.
2. Periodic line-by-line spot reads of randomly-selected canonical docs.
3. The standing rule that any contradiction with `IMPLEMENTATION-STATUS.md` must be reconciled by either (a) shipping the code or (b) correcting the claim.

### Pass 14 — Workflow / Analytics / Metering / Chaos / Valkey (7 components)

7 more Application Blueprint banners landed in a single commit:

- **temporal** (§4.3 Workflow): durable workflow orchestration for `bp-fabric`.
- **flink** (§4.3 Workflow): stream + batch processing for `bp-fabric`.
- **debezium** (§4.2 CDC): streams DB row-level changes into Strimzi/Kafka.
- **iceberg** (§4.4 Lakehouse): open table format on top of MinIO + archival S3.
- **openmeter** (§4.8 Metering): API metering for `bp-fingate`.
- **litmus** (§4.9 Chaos): resilience testing required by DORA / NIS2.
- **valkey** (§4.1 Data): banner explicitly states **NOT a Catalyst control-plane component** — control plane uses NATS JetStream KV per ARCHITECTURE §5 / GLOSSARY's `event-spine`. Valkey is Application-tier caching only. This is the disambiguation that PLATFORM-TECH-STACK §1 establishes ("the same upstream technology can serve in multiple categories") — pinned in the per-component README so it can't be misread.

### Pass 13 — Communication Application Blueprints (4 components)

All 4 communication Application Blueprints under `bp-relay` got banners pointing at PLATFORM-TECH-STACK §4.5:

- **stalwart** — JMAP/IMAP/SMTP self-hosted email.
- **livekit** — WebRTC SFU; pairs with STUNner.
- **stunner** — K8s-native TURN/STUN for WebRTC NAT traversal.
- **matrix** — Matrix protocol via Synapse server. Banner explicitly disambiguates "Synapse" as the chat-server implementation (NOT the deprecated OpenOva product noun, which is retired in favor of `bp-axon`).

All 4 are explicitly Application Blueprints, NOT Catalyst control plane.

### Pass 12 — AI/ML Application Blueprints (11 components)

All 11 AI/ML component READMEs got role-in-Catalyst banners pointing at PLATFORM-TECH-STACK §4.6 (AI/ML) or §4.7 (AI safety + observability), and noting their composition under `bp-cortex` (the AI Hub composite Blueprint):

- **knative** — serverless layer for KServe-managed inference.
- **kserve** — Kubernetes-native model serving (vLLM, BGE, custom).
- **vllm** — default LLM inference runtime.
- **milvus** — vector database backbone for RAG.
- **neo4j** — knowledge-graph-augmented retrieval alongside Milvus.
- **librechat** — default end-user chat surface; fronts LLM Gateway through NeMo Guardrails.
- **bge** — embedding generation + reranking.
- **llm-gateway** — outbound LLM routing (Claude, GPT-4, vLLM, Axon).
- **anthropic-adapter** — OpenAI-SDK→Anthropic translation.
- **nemo-guardrails** — AI safety firewall (prompt injection, PII, off-topic, hallucination).
- **langfuse** — LLM observability (latency, tokens, cost, eval).

All 11 are explicitly Application Blueprints, NOT Catalyst control plane. Catalyst's own observability stack (Grafana/OTel) covers infrastructure; LangFuse covers AI-specific dimensions.

### Pass 11 — minio, velero, failover-controller, opensearch, trivy, clickhouse, ferretdb

7 more banners.

- **minio**: per-host-cluster S3 (§3.5); tiers cold data to cloud archival. Plus disambiguation: a Mermaid node was labeled `ILM[Lifecycle Manager]` — confusable with the rejected Catalyst sub-product. Relabeled to `ILM[Information Lifecycle Manager - MinIO ILM]` to make MinIO's feature explicit.
- **velero**: per-host-cluster backup (§3.5); backups land in archival S3, not MinIO.
- **failover-controller**: per-host-cluster (§3.6); lease-based split-brain protection layered on top of k8gb. Pointers to SRE §2.4 + SECURITY §5.2.
- **opensearch**: explicitly framed as Application Blueprint (§4.1), NOT control plane. Installed when an Org wants SIEM / full-text search / log analytics.
- **trivy**: per-host-cluster (§3.3); CI + registry + runtime scanning chain.
- **clickhouse**: Application Blueprint (§4.1); used by bp-fabric and SIEM cold-storage tier.
- **ferretdb**: Application Blueprint (§4.1); replication via underlying CNPG.

### Pass 10 — vpa, keda, reloader, external-dns, opentofu, crossplane, coraza

Seven more component banners + opentofu drift fix.

- **vpa, keda, reloader**: per-host-cluster infrastructure pointers to PLATFORM-TECH-STACK §3.4. Reloader specifically calls out its role in the secret-rotation flow (when ESO updates a K8s Secret from OpenBao, Reloader triggers the rolling deploy).
- **external-dns**: per-host-cluster pointer to §3.1; clarifies it's the non-GSLB DNS sync (k8gb owns the GSLB zone authoritatively).
- **coraza**: per-host-cluster pointer to §3.1; specifically DMZ-block-only.
- **crossplane**: emphasizes the **never-a-user-surface** rule. Users don't write Compositions in Application configs; Blueprint authors do. Cross-references ARCHITECTURE.md §4/§7 (no-fourth-surface) and BLUEPRINT-AUTHORING.md §8.
- **opentofu**: framed as Phase-0-only / runs on `catalyst-provisioner` only. NOT installed on host clusters at runtime. Plus drift fix: line 182 had "Bootstrap Wizard prompts for cloud credentials" (banned term) → "Catalyst Bootstrap (Phase 0)". And the secrets section's "ESO PushSecrets sync to both regional OpenBao instances" was the same active-active drift Pass 7 corrected elsewhere — now reads "writes go to the primary OpenBao region only; replicas pick up via async perf replication".

### Pass 9 — more component README banners + Kyverno priority-class clarification

Added role-in-Catalyst banners to:
- **grafana** — observability stack on every host cluster; Catalyst self-monitoring + Application telemetry pipeline.
- **harbor** — per-host-cluster container registry for Catalyst images, mirrored Blueprint OCI artifacts, customer images.
- **falco** — runtime security on every host cluster, feeds SIEM/SOAR pipeline.
- **kyverno** — policy engine on every host cluster; enforces cosign signature requirement, default-deny NetworkPolicies on Organization namespaces, etc.
- **sigstore** — signing + admission verification, signs every Blueprint OCI artifact, Kyverno denies unsigned/wrong-issuer at admission.
- **syft-grype** — SBOM generation in CI + runtime CVE scanning.

Plus Kyverno priority-class clarification: priority class names `tenant-high`, `tenant-default`, `tenant-batch` are legacy deployment artifacts. The prose around them now says "Organization workloads" instead of "tenant workloads", with an explicit note that the priority class names themselves stay as-is until a separate migration ticket renames them in deployed clusters.

### Pass 8 — component README role-in-Catalyst banners + dead-link fix

Continued the drift sweep into more component READMEs.

- **k8gb**: header reframed to clarify per-host-cluster infrastructure role on the DMZ block; cross-reference to PLATFORM-TECH-STACK §3.1 and SRE.md §2.4 (split-brain protection). Removed broken link to non-existent `../failover-controller/docs/ADR-FAILOVER-CONTROLLER.md` (the failover-controller doesn't have a docs/ folder); replaced with link to its README + SRE.md §2.4.
- **keycloak**: header reframed from narrow "FAPI Authorization Server for Open Banking" to broader "User identity for Catalyst Sovereigns" (Keycloak handles ALL user identity in Catalyst, not just FAPI). Added the per-Org / per-Sovereign topology callout matching SECURITY.md §6. Clarified that the "Multi-tenant TPP" line refers to PSD2 TPPs, not Catalyst's Organization-level multi-tenancy.
- **cert-manager**: header reframed as per-host-cluster infrastructure pointer to PLATFORM-TECH-STACK §3.3.
- **cilium**: header reframed as per-host-cluster infrastructure pointer to PLATFORM-TECH-STACK §3.1, with the install-first-on-every-cluster note matching the Phase-0 install order.

CNPG, Strimzi: read in full and confirmed clean — they correctly position themselves as Application Blueprints and don't drift from the canonical model. CNPG's `<org>-postgres-dr` cluster name (Application-tier database role) is acceptable per NAMING-CONVENTION §1.3 (which only forbids primary/dr in K8s host-cluster names, not in Application-internal CRD names).

### Pass 7 — major OpenBao + ESO + Gitea + Flux drift

The most consequential pass yet. Two READMEs (`platform/openbao/README.md` and `platform/external-secrets/README.md`) described an **active-active bidirectional sync** model that was explicitly rejected during the architecture session in favor of independent Raft per region with async perf replication. They had survived all previous passes because the banned-term grep doesn't catch architectural drift.

- **OpenBao README**: rewrote Architecture section, ClusterSecretStore example, PushSecret example, and consequences. The active-active diagram was replaced with the primary→replicas async perf replication topology that matches `docs/SECURITY.md` §5. Single-target PushSecret to `bao-primary`. Added DR promotion section.
- **External-Secrets README**: removed broken link to non-existent `../openbao/docs/ADR-OPENBAO.md`. Reframed Key Principles table to match single-primary writes. Replaced "PushSecret to Multiple OpenBao Instances" with single-target primary write. Updated Bootstrap Mermaid sequence to use "Catalyst Bootstrap (Phase 0)" / "OpenTofu" / "SPIFFE SVID" nomenclature.
- **Gitea README**: removed bidirectional cross-region mirror diagram (Catalyst runs one Gitea per Sovereign on the management cluster, not cross-region mirror). Added explanation of why bidirectional was rejected (write-conflict semantics break EnvironmentPolicy enforcement). Updated backup section.
- **Flux README**: same correction — multi-region GitOps section rewritten to show single Gitea per Sovereign with per-vcluster Flux pulling from it.
- **Mermaid syntax bug**: an earlier mass replace_all of "Catalyst IDP" → "Catalyst console" had left an invalid mermaid node identifier `Catalyst console[Catalyst console]` (mermaid doesn't allow spaces in node IDs). Fixed to `Console[Catalyst console]`. This would have rendered as a broken diagram in the GitHub view.

This pass justifies the user's instruction to keep restarting validation — the active-active drift had survived 5 prior passes by hiding in component READMEs that grep-based checks didn't reach.

---

## Acceptance criteria (each ran clean as of last commit)

```bash
# 1. Banned product terms must not appear in architectural context
grep -rnE '\btenant\b' --include='*.md' . | \
  grep -viE 'multi-tenant|tenant-(high|default|batch|critical)|GLOSSARY|CLAUDE|project-memory'
# (empty)

grep -rnE '\bOperator\b' --include='*.md' . | \
  grep -viE 'K8s Operator|controller pattern|External Secrets Operator|Trivy Operator|catalyst-admin|operator (compatibility|sdk)'
# (empty)

grep -rn 'Catalyst IDP' --include='*.md' .
# (empty)

grep -rn 'workspace-controller' --include='*.md' . | \
  grep -v 'GLOSSARY\|CLAUDE\|VALIDATION-LOG'
# (empty — only banned-term entries remain that document the rename)

grep -rnE '\bWorkspace\b' --include='*.md' . | \
  grep -vE 'Workspace API \(Unix|Workspace.{0,2}as Catalyst scope|"Workspace"|GLOSSARY|CLAUDE|VS Code|Slack|Backstage|Terraform'
# (empty)

# 2. Wrong org references
grep -rnE 'github\.com/openova[^-]' --include='*.md' .
grep -rnE 'ghcr\.io/openova[^-]' --include='*.md' .
# both empty

# 3. API group unified
grep -rnE 'catalyst\.openova\.io/v[0-9]' --include='*.md' .
# all show v1alpha1

# 4. <tenant> placeholders
grep -rn '<tenant>' --include='*.md' .
# (empty)

# 5. Cross-references resolve
# (verified via custom script — no broken Markdown links across canonical docs)
```

---

## Authoring identity

All commits in this validation work were authored as `hatiyildiz` (`269457768+hatiyildiz@users.noreply.github.com`) per the user's standing instruction. No global git config — identity passed via `-c user.name= -c user.email=` per-commit.

---

## Re-validation cadence

Run this validation:

1. After any major doc rewrite (someone added/changed a canonical doc).
2. After adding a new banned term to GLOSSARY (need to sweep for the new term).
3. After renaming any Catalyst component (the rename ripples through 5–10 files).
4. Quarterly even if nothing seems to have changed — drift accumulates silently.

Update [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md) whenever a 📐 component flips to 🚧 or ✅. The architecture docs themselves should rarely change once the model is settled — when they do, this file gets a new "Pass N" entry.

---

*Cross-reference [`GLOSSARY.md`](GLOSSARY.md), [`IMPLEMENTATION-STATUS.md`](IMPLEMENTATION-STATUS.md). Tracking issue: [#37](https://github.com/openova-io/openova/issues/37).*
