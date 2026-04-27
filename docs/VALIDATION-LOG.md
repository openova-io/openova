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

### Pass 32 — `harbor.<domain>` / `registry.<domain>` registry-DNS sweep (9 files, 11 instances)

Pass 25's deferred sweep, executed. The pattern: image references with `harbor.<domain>/...` (and one `registry.<domain>/...` in temporal) collapse the location-code segment in the same way Pass 24/25/29 fixes addressed for service URLs. NAMING §5.1 establishes Catalyst per-host-cluster Harbor as `harbor.{location-code}.{sovereign-domain}` (e.g. `harbor.hfmp.openova.io`).

Fixed:
- platform/anthropic-adapter/README.md L68 — Application image ref.
- platform/bge/README.md L68 + L95 — bge-m3 + bge-reranker image refs.
- platform/debezium/README.md L151 — Kafka Connect build output.
- platform/harbor/README.md L132 (ingress hosts) + L236 (Kyverno image-pattern policy).
- platform/knative/README.md L99 + L123 — sample knative-serving image refs.
- platform/llm-gateway/README.md L72 — gateway image ref.
- platform/strimzi/README.md L164 — Kafka Connect build output.
- platform/temporal/README.md L279 — `registry.<domain>/fuse/order-worker:latest` had two drift items in one line: the off-spec `registry.<domain>` placeholder (Catalyst's per-host-cluster registry is Harbor — there's no separate `registry` component) AND the legacy product name `fuse` (renamed to `bp-fabric` in BUSINESS-STRATEGY §16.2 / Pass 26). Rewritten to `harbor.<location-code>.<sovereign-domain>/fabric/order-worker:latest`.
- platform/trivy/README.md L178 — Kyverno verifyImages policy `imageReferences:` glob.

Out of scope (intentional): the `:latest` tag hygiene and the broader question of whether a Catalyst-published Application Blueprint should reference `ghcr.io/openova-io/bp-<name>:<semver>` directly vs the Sovereign's Harbor mirror. Both axes warrant their own pass; this pass strictly fixed the DNS placeholder shape.

Out of scope (correctly): platform/stalwart/README.md `<domain>` placeholders in MX/A/TXT/DKIM/DMARC examples — those refer to the customer's email-receiving domain, not Catalyst control-plane DNS, so the bare `<domain>` is correct. platform/external-dns/README.md `gslb.<domain>` / `api.<domain>` / `svc.<domain>` references — those describe upstream external-dns behavior generically; clarifying them as Catalyst-specific would change their semantic.

Final sweep grep confirms zero remaining `harbor.<domain>` / `registry.<domain>` instances. With Pass 29 (canonical doc DNS sweep), Pass 31 (openbao + librechat carry-over), and now Pass 32 (image registry sweep), the recurring DNS-placeholder collapse drift category is addressed end-to-end.

### Pass 31 — openbao DNS placeholder + librechat callback URL (Pass 22/29 carry-over); GLOSSARY clean

Two real DNS-placeholder fixes; GLOSSARY confirmed clean.

- **platform/openbao/README.md** ingress hosts list at line 108 had `bao.<domain>` — same DNS-placeholder collapse Pass 29 swept across canonical docs. The same file uses the canonical `bao.<location-code>.<sovereign-domain>` form on line 127 in the ClusterSecretStore example, so this was internal inconsistency: Pass 7 fixed the active-active drift in the body but missed the ingress hosts placeholder. Fixed.
- **platform/librechat/README.md** OAuth callback URL line 154 had `https://chat.ai-hub.<domain>/oauth/openid/callback` — Pass 22 marked librechat clean and missed it; Pass 29 fixed the Keycloak issuer line in the same file but didn't re-sweep the rest. Per NAMING §5.2 Application endpoints are `{app}.{environment}.{sovereign-domain}` — the `ai-hub.<domain>` form has the same shape problem Pass 25 fixed in llm-gateway. Rewritten to `https://chat.<env>.<sovereign-domain>/oauth/openid/callback`.
- **docs/GLOSSARY.md**: clean. Core nouns, roles, infrastructure, Catalyst components, surfaces, banned terms, acronyms — all consistent with Pass 6 (NATS Account/subject reconciliation), Pass 7 (OpenBao independent Raft), Pass 14/22/26 (Catalyst/OpenOva separation), Pass 20 (placement modes), Pass 27 (Keycloak topology). The single-source-of-truth has held up across the loop.

Sweep grep at end of pass: only `harbor.<domain>` patterns + customer-email-domain `<domain>` placeholders in stalwart remain (the latter are correct — they refer to the customer's email-receiving domain, not the Sovereign DNS). The `harbor.<domain>` cluster is on Pass 25's deferred sweep list and will be addressed in a dedicated pass.

This is the third pass to reopen a previously-marked-clean component (librechat: Pass 22 → Pass 29 → Pass 31). The pattern: short-form scans verify the banner and one or two visible config blocks, but YAML/code examples deeper in the file accumulate copy-paste drift that survives. Future Pass-N entries should default to a full grep-for-placeholder-shapes on every file touched, not just visual scan.

### Pass 30 — core/README catalyst-provisioner scope confusion + neo4j clean

One real fix on core/README; neo4j README clean.

- **core/README.md** "User journeys" table had: "Sovereign bootstrap | Phase 0 done by `catalyst-provisioner`; this codebase contains the OpenTofu modules under `apps/provisioning/opentofu/` and the post-bootstrap Catalyst install logic." But per SOVEREIGN-PROVISIONING.md §2, `catalyst-provisioner` is a **separate Blueprint** (`bp-catalyst-provisioner`) that is "not part of any Sovereign at runtime" — it is a self-host-able provisioner outside the Catalyst control plane in `core/`. The line conflated two services: `bp-catalyst-provisioner` (Phase 0 OpenTofu bootstrap, lives under products/Blueprint folders) and `core/apps/provisioning/` (runtime Application provisioning — validates configSchema and commits to Environment Gitea repos, an entirely different concern). Rewritten to call out the separation: Phase 0 belongs to bp-catalyst-provisioner; `apps/provisioning/` is the runtime install service.
- **platform/neo4j/README.md**: clean. Banner correct (Application Blueprint, §4.6, paired with Milvus in bp-cortex). Cypher schema and Python integration consistent with knowledge-graph-RAG description. The `bolt://neo4j.ai-hub.svc:7687` reference uses K8s in-cluster service DNS — not a Catalyst control-plane DNS shape, so not subject to the §5.1 NAMING rule.

Note on the JetStream `ws.<env>.>` placeholder: appears in 5 places (core/README L141, ARCHITECTURE L168-171). Per NAMING §11.2 the precise form is `ws.{org}-{env_type}.>`. The `<env>` shorthand is consistently used across canonical docs and is unambiguous given the surrounding context (Environment is the `{org}-{env_type}` composite). Treating as documented shorthand, not drift; future tightening pass could replace globally if desired.

### Pass 29 — DNS-placeholder sweep across canonical docs (CLAUDE/PROVISIONING/ARCHITECTURE/BLUEPRINT-AUTHORING/librechat) + nemo-guardrails clean

Started as CLAUDE.md + nemo-guardrails atomic check; expanded into a sweep when the third instance of the same drift pattern surfaced. Six files corrected.

The recurring drift pattern: Catalyst control-plane DNS placeholders that omit the `<location-code>` segment, producing two-segment forms like `gitea.<sovereign>` / `gitea.<sovereign>.<domain>` / `gitea.<sovereign-domain>` / `keycloak.<domain>`. Per NAMING §5.1 the canonical form is `{component}.{location-code}.{sovereign-domain}` (example: `gitea.hfmp.openova.io`). The shorter forms are not just abbreviations — they collapse the multi-region location dimension that gives Catalyst its routing model, so they re-drift the docs each time someone reads them as "obvious shorthand".

Fixes:
- **CLAUDE.md** "Customer Sync" section — `gitea.<sovereign>/catalog/bp-cilium/` and `gitea.<sovereign>/catalog/bp-cortex/` → `gitea.<location-code>.<sovereign-domain>/catalog/...`. Added parenthetical pointer to NAMING §5.1 so the form stays anchored.
- **docs/SOVEREIGN-PROVISIONING.md** §3 (Phase 0 procedure) had `gitea.<sovereign>.<domain>`, `console.<sovereign>.<domain>`, `admin.<sovereign>.<domain>` in the DNS-records bullet, and §5 had `console.<sovereign>.<domain>` in the Day-1 login line — all four collapsed location-code into the malformed `<sovereign>.<domain>` two-segment form. Rewritten.
- **docs/ARCHITECTURE.md** §4 write-path diagram had `Gitea: gitea.<sovereign-domain>/{org}/{org}-{env_type}` — missing location-code. Rewritten.
- **docs/BLUEPRINT-AUTHORING.md** §6.4 (private-Blueprint authoring journey) step 3 had `gitea.<sovereign-domain>/<org>/shared-blueprints/bp-<name>` — same omission. Rewritten.
- **platform/librechat/README.md** Keycloak issuer line had `https://keycloak.<domain>/realms/ai-hub` — same drift Pass 25 fixed in llm-gateway, and the `ai-hub` realm is an Application namespace not a Keycloak realm (per SECURITY §7 realms are per-Org or per-Sovereign). Rewritten to `https://keycloak.<location-code>.<sovereign-domain>/realms/<org>`. **Note: Pass 22 marked librechat clean and missed this exact line — a banner-style scan can miss config-block drift inside YAML examples.** Treating Pass 22 as partially incorrect; this is now corrected.
- **platform/nemo-guardrails/README.md**: clean. Short README, banner correct (Application Blueprint §4.7, used by bp-cortex), integration table consistent.

Final sweep grep confirms only canonical `<location-code>.<sovereign-domain>` forms remain in the codebase. Future passes should treat the collapsed-DNS pattern as an established drift category and grep for it on every pass that touches a doc with example URLs.

### Pass 28 — README + minio drift sweep — clean

Both targets verified against canonical docs; no edits needed.

- **README.md**: Catalyst banner, the model-in-60-seconds box, stack-at-a-glance table, and getting-started block are all consistent with current canonical docs:
  - "OpenOva (the company) publishes Catalyst (the platform). A deployed Catalyst is called a Sovereign." — matches GLOSSARY.
  - Stack table: Keycloak "(per-Org for SME, per-Sovereign for corporate)" matches PLATFORM-TECH-STACK §2.1 and Pass 27's TECHNOLOGY-FORECAST fix; OpenBao "(independent Raft per region, no stretched cluster)" matches SECURITY §5; NATS JetStream "(per-Org accounts)" matches Pass 6's reconciliation.
  - Getting-started "Self-host bp-catalyst-provisioner" matches SOVEREIGN-PROVISIONING §2 ("`catalyst-provisioner` is itself a Blueprint (`bp-catalyst-provisioner`)").
  - Status caveat referring readers to IMPLEMENTATION-STATUS is consistent with that file's "design vs built" framing.
- **platform/minio/README.md**: Banner correct (per-host-cluster infrastructure §3.5). Multi-region bucket replication diagram is consistent with SRE.md §6 ("MinIO | Per-host-cluster infra | Bucket replication | Minutes") — bidirectional S3 replication is the canonical pattern for object storage and is NOT the same drift category as OpenBao active-active (the SECURITY §5 prohibition is specifically about secrets needing single-writer Raft per region; object storage replication is fine).

**Pass 28: clean.**

### Pass 27 — TECHNOLOGY-FORECAST mandatory/à-la-carte categorization vs PLATFORM-TECH-STACK; milvus clean

Two real fixes on TECHNOLOGY-FORECAST; milvus README clean.

- **TECHNOLOGY-FORECAST-2027-2030.md** "Mandatory Components" listed `opensearch` and "A La Carte Components" listed `keycloak`, but per PLATFORM-TECH-STACK §2.1, Keycloak is part of the Catalyst control plane (per-Org realms in SME, per-Sovereign realm in corporate — installed on every Sovereign), and per §4.4 + §10, OpenSearch is an Application Blueprint that customers install only when they want the SIEM pipeline (paired with ClickHouse + bp-specter).
  - Swapped: keycloak moved to Mandatory with note "Catalyst control-plane identity — per-Org realms in SME, per-Sovereign realm in corporate"; opensearch moved to A La Carte with note "Application Blueprint — opt-in for SIEM (paired with ClickHouse + bp-specter)".
  - Added a classification-basis banner above the Mandatory section pointing at PLATFORM-TECH-STACK §2/§3/§4 so the document's "Mandatory" / "A La Carte" axis lines up with the architectural categorization in canonical docs.
- **platform/milvus/README.md**: clean. Banner correct (Application Blueprint, paired with BGE in bp-cortex). Helm values, schema, hybrid search, and partition examples consistent with §4.6 RAG description.

### Pass 26 — BUSINESS-STRATEGY OpenBao active-active drift + Catalyst/OpenOva conflation; matrix clean

Three real fixes on BUSINESS-STRATEGY; matrix README clean.

- **BUSINESS-STRATEGY.md §8.4 (CISO value prop)** still described "OpenBao per-cluster with ESO PushSecrets for cross-cluster secret sync" — the rejected active-active model. Same drift Pass 7 corrected in OpenBao/ESO/Gitea/Flux component READMEs but never propagated to BUSINESS-STRATEGY. SECURITY §5 establishes per-region independent Raft + async Performance Replication; ESO syncs into the local region only. Fixed; also added the SPIFFE/SPIRE 5-minute SVID detail that fits the CISO message.
- **BUSINESS-STRATEGY.md §5.1 (Product Family table)** had two product entries that overlapped: "OpenOva: The core platform. 52 curated open-source components..." and "OpenOva Catalyst: The platform itself..." — but per GLOSSARY, OpenOva is the **company**, Catalyst is the **platform**. They are not two different products. Removed the standalone "OpenOva" row, expanded the Catalyst row to absorb its 52-component description, and added a banner above the table explaining the Company/Platform/Sovereign vocabulary so older references in the doc still parse correctly.
- **BUSINESS-STRATEGY.md §5.2 (Architecture Relationship diagram)** showed `OPENOVA / (Core Platform)` at the top — but this is the same company-vs-platform conflation. Replaced top node with `CATALYST / (the platform — runs on every Sovereign)` and added a footer noting each child is a composite Blueprint installed via the marketplace.
- **platform/matrix/README.md**: clean. Banner correct, Synapse-vs-bp-axon disambiguation explicit, integration table consistent with bp-relay deployment.

### Pass 25 — llm-gateway DNS placeholders + IMPLEMENTATION-STATUS clean

Three placeholder fixes on platform/llm-gateway README; IMPLEMENTATION-STATUS confirmed clean against ARCHITECTURE/SECURITY/BLUEPRINT-AUTHORING.

- **platform/llm-gateway/README.md** — three malformed DNS placeholders:
  - `KEYCLOAK_URL = https://keycloak.<domain>/realms/ai-hub` — `<domain>` collapses location-code+sovereign-domain (same NAMING §5.1 violation Pass 24 fixed in SRE.md), and realm `ai-hub` (an Application namespace) is the wrong scope: per NAMING §7 highlights and SECURITY §7, Keycloak realms are per-Org in SME-style and per-Sovereign in corporate-style — never per-Application-namespace. Fixed to `https://keycloak.<location-code>.<sovereign-domain>/realms/<org>`.
  - `claude config set api_base "https://llm-gateway.ai-hub.<domain>/v1"` — Application-Blueprint endpoint pattern per NAMING §5.2 is `{app}.{environment}.{sovereign-domain}`, not `{app}.{namespace}.<domain>`. The `ai-hub` segment was an Application namespace standing in for the Environment slot. Fixed to `https://llm-gateway.<env>.<sovereign-domain>/v1`.
  - `ANTHROPIC_BASE_URL = https://llm-gateway.ai-hub.<domain>/v1` — same shape problem. Fixed to `https://llm-gateway.<env>.<sovereign-domain>/v1`.
- **docs/IMPLEMENTATION-STATUS.md**: clean. CRD list (§4) matches BLUEPRINT-AUTHORING and ARCHITECTURE; surfaces (§5) match the agreed UI/Git/API + debug-only kubectl model; control-plane component list (§2.1, §2.2) matches PLATFORM-TECH-STACK §2; Sovereigns running today (§6) accurately marks `openova` as 🚧 (legacy SME marketplace, not yet a Catalyst Sovereign).

Note on llm-gateway image refs (`harbor.<domain>/ai-hub/llm-gateway:latest`): same `<domain>` placeholder shape and `:latest` hygiene appear in many platform/*/README.md examples (anthropic-adapter, debezium, bge, knative, strimzi, temporal, etc.). Treating those as illustrative documentation snippets, not deployable manifests, so leaving them for a dedicated sweep pass — fixing only llm-gateway in isolation would create asymmetric drift.

### Pass 24 — SRE Alertmanager webhook URL form + livekit clean

One real fix on SRE.md; livekit confirmed clean.

- **SRE.md §12 (Alertmanager configuration)** webhook URLs at lines 442 and 451 used `gitea.<sovereign>.<domain>/api/v1/...` — the `<sovereign>.<domain>` form is malformed against NAMING §5.1, which establishes Catalyst control-plane DNS as `{component}.{location-code}.{sovereign-domain}` (example: `gitea.hfmp.openova.io`). The two-segment placeholder collapses location-code and sovereign-domain into ambiguous tokens. Fixed both URLs to `gitea.<location-code>.<sovereign-domain>/...` matching the canonical form.
- **platform/livekit/README.md**: clean. Banner correct (Application Blueprint, real-time media). Integration tables consistent with bp-cortex voice path. No drift.

### Pass 23 — PLATFORM-TECH-STACK §7 categorization slip + §10 fictional bp-siem; litmus clean

PLATFORM-TECH-STACK §6-§11 deep-read found two real fixes; litmus README clean.

- **§7.1 (Resource estimates)** had `Crossplane | ~0.5 GB` listed under "Catalyst control plane" — but Crossplane is per-host-cluster infrastructure per §3.2. The §7.1 table was conflating Catalyst-specific RAM with per-host-cluster overhead also running on mgt. Split into:
  - §7.1: Catalyst-specific only (added missing SPIRE server row; subtotal corrected to ~11.3 GB).
  - New §7.4: Per-host-cluster infrastructure overhead with explicit per-component breakdown (Cilium, Flux, Crossplane, cert-manager, ESO, Kyverno, Trivy, Falco, Harbor, MinIO, Velero, small operators) totalling ~8.8 GB per host cluster. Total mgt cluster budget = §7.1 + §7.4 ≈ ~20 GB before SME Keycloak fan-out.
  - Renamed §7.2 heading to "Per-Organization vcluster (workload regions)" for clarity.
- **§10 (SIEM/SOAR)** claimed "this pipeline is itself a composite Blueprint (`bp-siem`)" — but `bp-siem` doesn't exist in §5 composite Blueprints. The SIEM pipeline is a *composition of existing Application Blueprints* (Strimzi + OpenSearch + ClickHouse + bp-specter on top of per-host-cluster Falco/Trivy/Kyverno), not a single packaged composite. Reworded to reflect that. Also corrected §10's last sentence to point at the local Grafana stack (per-Sovereign observability) for fallback retention rather than nothing.

platform/litmus/README.md: clean. Banner correct, integration table consistent.

### Pass 22 — PERSONAS-AND-JOURNEYS Environment name format + librechat clean

- **PERSONAS-AND-JOURNEYS.md §6.3** Environment view example said `Environment: bankdhofar-corp-banking-prod` — implies a Sovereign-Org-EnvType three-segment form. But NAMING §11.1 establishes `{org}-{env_type}`: the Sovereign name is NOT in the Environment name. And §4.2 of this same doc says "Their internal Organizations are `core-banking`, `digital-channels`, `analytics`, `corporate-it`" — so the Org is `core-banking`, and the Environment is `core-banking-prod`. Fixed.
- **platform/librechat/README.md**: clean. The example `namespace: ai-hub` is a customer-chosen Application namespace (illustrative, not strict drift).

### Pass 21 — BLUEPRINT-AUTHORING CI pipeline contradicting §2 + langfuse clean

One real fix on BLUEPRINT-AUTHORING + langfuse confirmed clean.

- **BLUEPRINT-AUTHORING.md §11** described the CI pipeline as if it were per-Blueprint-repo — `on: push  # tags: vX.Y.Z` — but §2 establishes that we use a **monorepo with per-Blueprint fan-out** and the canonical tag form is `platform/<name>/v1.2.3` / `products/<name>/v1.2.3` (path-matrix). §11 was effectively documenting the rejected per-Blueprint-repo CI shape. Rewrote §11 to match the monorepo reality: single CI at the root, `pull_request.paths` triggers validate on PR, `push.tags: ['platform/*/v*', 'products/*/v*']` triggers build-and-sign, with the build job parsing the tag to identify which Blueprint folder + version to build. Includes a worked example: tagging `platform/wordpress/v1.3.0` builds `platform/wordpress/` and publishes `ghcr.io/openova-io/bp-wordpress:1.3.0`.
- **platform/langfuse/README.md**: clean. Banner correct (Application Blueprint, AI observability, used by bp-cortex). "Used by: OpenOva Cortex" is acceptable commercial phrasing alongside the technical `bp-cortex` reference.

### Pass 20 — SOVEREIGN-PROVISIONING placement-syntax + Kyverno label drift

Two real findings on the SOVEREIGN-PROVISIONING + platform/kyverno rotation.

- **SOVEREIGN-PROVISIONING.md §8** had `placement: active-active: false, single-region` — invalid YAML mixing a boolean toggle with an enum value. Rewrote to canonical `placement.mode: single-region` matching the placement modes defined in GLOSSARY (`single-region | active-active | active-hotstandby`). Updated the migration prose accordingly.
- **platform/kyverno/README.md V5 row** had `openova.io/env: production` — out-of-spec label name and value. NAMING-CONVENTION §6 establishes `openova.io/env-type: prod` (hyphen-form, short value). Fixed.

Note: `tenant-high` / `tenant-default` priority class names retained per Pass 9's deferred-migration note (renaming K8s PriorityClass objects requires recreate-not-rename, tracked separately).

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
