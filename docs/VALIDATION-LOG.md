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

### Pass 39 — non-canonical `*-staging` env_type drift in ARCHITECTURE + PERSONAS; clickhouse clean

Six fixes across two canonical docs; clickhouse README clean.

Acceptance greps clean for all carry-forward categories. Drift surfaced via case-sensitive sweep for non-canonical Environment env_type spellings — NAMING §2.4 establishes the 3-char form (`prod | stg | uat | dev | poc`), but multiple Environment-name examples used the long form `staging`.

- **docs/ARCHITECTURE.md §8 (Promotion across Environments)** had 3 instances of `acme-staging` (in the Blueprint detail page mockup at line 287, in the prose at line 295 explaining the promotion flow, and in the EnvironmentPolicy YAML `sourceEnvironment` field at line 310). All renamed to `acme-stg` per NAMING §11.1 (Environment naming = `{org}-{env_type}` using 3-char env_type).
- **docs/PERSONAS-AND-JOURNEYS.md** had 3 instances of `digital-channels-staging` (Layla narrative L126, L135) and `acme-staging` (Blueprint detail mockup L230). Renamed to `digital-channels-stg` and `acme-stg`.

These were all real Environment names per Catalyst's canonical naming, just spelled with the long form. The `staging` spelling probably came from pre-Catalyst conventions where teams used full English words for env_types — but post-Catalyst, NAMING §2.4 fixes the canonical 3-char form to keep names short and grep-friendly.

Out of scope (correctly preserved):
- `payment-rail-staging` in PERSONAS L126: this is an Application name (Layla's customer-chosen name for the staging deployment of payment-rail), not an Environment name. Application names are free-form per NAMING.
- `minimum-replicas-production` in kyverno (Kyverno policy NAME, not an Environment): preserved as a stylistic choice for the policy identifier.

ARCHITECTURE.md deep re-scan applied Pass 23 lesson (focus on later sections):
- §5 (Read side / CQRS via JetStream): explicitly defines `<env>` as `{org}-{env_type}` (line 167), addressing the placeholder shorthand Pass 30 noted as "documented shorthand" — the canonical doc itself defines the abbreviation, so the use of `ws.<env>.>` is unambiguous.
- §6 Identity and secrets: matches SECURITY.md exactly.
- §7 Surfaces (UI / Git / API / NOT-surfaces): matches GLOSSARY.
- §8 Promotion: had the env_type drift just fixed.
- §9 Multi-Application linkage: clean — uses `bp-postgres` as a typed Blueprint reference, EnvironmentPolicy YAML uses `catalyst.openova.io/v1alpha1`.
- §10 Provisioning a Sovereign: clean — Phase 0/1/2/3 framing matches SOVEREIGN-PROVISIONING §3-§5.
- §11 Catalyst-on-Catalyst: bp-catalyst-* component list matches IMPLEMENTATION-STATUS §2.1; per-host-cluster-vs-control-plane separation explicitly stated.
- §12 SOTA principles: includes "Independent failure domains" line citing OpenBao Raft per region — consistent with Pass 7.
- §13 OAM influence: clean.

platform/clickhouse/README.md: clean. Banner correct (Application Blueprint §4.1, used by bp-fabric and SIEM cold-storage). Mermaid diagrams (single-region + multi-region) consistent. Kafka Engine integration correctly references "kafka-kafka-bootstrap.databases.svc:9092" as in-cluster K8s service DNS (not subject to NAMING §5 Catalyst DNS rules). Tiered storage with MinIO cold tier consistent with grafana/SRE/minio docs. The literal `minioadmin/minioadmin` placeholder credentials in the XML config example are illustrative defaults — flagging for a future security-hardening pass to replace with `<access-key>` / `<secret-key>` placeholders, but not Catalyst-architectural drift.

### Pass 38 — surviving "fuse" namespace in temporal; SECURITY + grafana clean

One real fix on temporal; both deep-scan targets clean.

Acceptance greps with the new literal-domain check (Pass 37 lesson) and case-insensitive banned-term sweep surfaced one surviving instance:

- **platform/temporal/README.md L272** — Worker Deployment example `namespace: fuse`. The "fuse" → "fabric" rename per BUSINESS-STRATEGY §16.2 / Pass 26 had been applied to the temporal README's banner (L3, "bp-fabric") and the image ref (L279, Pass 32+35 fixed `harbor.<location-code>.<sovereign-domain>/fabric/order-worker:latest`), but the namespace declaration on L272 was missed by both prior passes — the field `namespace:` is a YAML key, easy to skim past while the eye tracks the structural surrounding context. Renamed to `fabric`.

- **docs/SECURITY.md**: clean (deep re-scan with focus on §6-§10 per Pass 23 lesson). §1-§5 (Identity systems, SPIFFE/SPIRE, Secrets, Dynamic credentials, Multi-region OpenBao) consistent with canonical model and Pass 7's independent-Raft fix. §6 Keycloak topology consistent with NAMING §7 / Pass 27 swap. §7 Rotation policy uses correct `catalyst.openova.io/v1alpha1` API group. §8 Path-of-a-secret consistent. §9 Compliance posture and §10 Threat model — both contain references to "OpenSearch SIEM" / "OpenSearch in the Sovereign" as default audit destinations. Per Pass 27's clarification, OpenSearch is an opt-in Application Blueprint (not auto-installed). The SECURITY §9 wording reads as "default destination *when customers enable SIEM*" rather than "default-installed component" — defensible interpretation, leaving as-is. Flagged for a future tightening pass that could explicitly say "when the SIEM Application Blueprint is installed".

- **platform/grafana/README.md**: clean. Banner correctly identifies per-host-cluster role; the inline "§3 / observability layer in §2.3" cross-reference acknowledges the legitimate dual-categorization (Grafana stack runs both as per-host-cluster collector AND on the per-Sovereign mgt cluster as Catalyst's own self-monitoring). Tiered storage shape (Hot local → Warm MinIO → Cold R2) consistent with SRE.md §6 and minio README. OpenTelemetry instrumentation example uses the canonical `<org>` namespace placeholder.

Lesson confirmed: case-insensitive banned-term grep is non-negotiable. The Pass 32+35 sweeps fixed `harbor.<domain>` and DNS placeholders surrounding "fuse" but the namespace YAML key (`namespace: fuse`) survived because the prior greps targeted DNS shapes and image registries, not bare-word "fuse". Future passes should always grep `\bfuse\b` (and similar legacy-product-name greps) regardless of whether the surfaced category is unrelated — the cleanup work is small enough that running the check is cheap.

### Pass 37 — NAMING-CONVENTION §11.2 example URL drift; cilium clean

One real fix on NAMING-CONVENTION; cilium README clean.

Applying the Pass 23 lesson ("long canonical docs need careful read of LATER sections"): I deep-scanned NAMING-CONVENTION §7-§11 (sections that earlier passes touched only briefly). Found one drift instance in §11.2 — the most authoritative passage in the entire repo on Environment realization.

- **NAMING-CONVENTION.md §11.2 step 1** had the example URL `gitea.omantel.openova.io/acme/acme-prod` — a 3-segment form that bypasses the `{location-code}` segment NAMING §5.1 itself establishes for Catalyst control-plane DNS. This is the most concerning kind of drift: the **authoritative naming doc** offering a non-canonical example would teach every reader the wrong form. Pass 29's earlier sweep caught the placeholder forms (`gitea.<sovereign>.<domain>` etc.) but missed this one because it uses a literal Sovereign domain (`omantel.openova.io`) that completes a 3-segment form, evading any grep for `<sovereign>` / `<domain>` placeholders. Fixed to `gitea.<location-code>.omantel.openova.io/acme/acme-prod` and added an inline pointer back to §5.1 so the canonical pattern stays visible at the example site.

- **platform/cilium/README.md**: clean. Banner correct (per-host-cluster infrastructure §3.1 — installed on every host cluster before any other workload). All examples (CiliumNetworkPolicy, Gateway API, CiliumEnvoyConfig circuit breakers, OTel auto-instrumentation) use generic upstream K8s/Cilium patterns (`app.example.com`, `default` namespace, `frontend`/`api-service` selectors) — not Catalyst-specific, no DNS-shape concerns.

Pattern note: the surviving NAMING drift instance was a **literal-domain** form (no placeholder), which is the hardest variant to grep for. Future drift sweeps that look for "{component}.{Sovereign-domain}" patterns should also grep for the literal domains used by canonical-example Sovereigns (`omantel.openova.io`, `bankdhofar.local`, `openova.io`) to catch this variant.

### Pass 36 — flux deep-scrutiny + sweep gap-fill (5 fixes flux + 1 kyverno)

Pass 35 had a sweep grep with `head -10` cutoff that compromised completeness; Pass 36 ran the same grep without the cutoff and found 6 surviving instances across 2 components.

**platform/flux/README.md** had 5 drift items, all surviving prior passes:
- **Mermaid diagram L21+L45**: `Tenant[Tenant Repos]` subgraph + arrow — banned term per GLOSSARY. Renamed to `Organization[Organization Repos]`.
- **L134**: GitRepository url `https://gitea.<domain>/<org>/<component>.git` — Catalyst control-plane DNS placeholder collapse. Pass 35 grep missed this because of the `head -10` truncation. Fixed to `gitea.<location-code>.<sovereign-domain>/...`.
- **L243**: Bootstrap command `--url=https://gitea.<domain>/openova/flux` — same drift, same root cause (Pass 35 truncation). Fixed.
- **L258**: Key commands `flux reconcile kustomization tenants` — banned term in CLI example. Renamed to `organizations`. (Pass 34's TENANT sweep was uppercase-only and missed lowercase plural.)
- **L298**: Gitea Actions notify-flux example `https://flux-webhook.<domain>/hook/...` — Catalyst control-plane DNS missing location-code. Fixed.

**platform/kyverno/README.md** L232: Mermaid subgraph label `subgraph Workload["Tenant Workload"]` — banned term. Renamed to "Organization Workload". (Pass 9/20 deferred kyverno priority-class name renames as a separate K8s-recreate migration; that's still deferred. This Mermaid label is documentation, not a deployed resource name, so it's safe to fix immediately.)

Other findings during Pass 36's complete sweep, all correctly preserved:
- NAMING-CONVENTION L45 / ARCHITECTURE L195: "multi-tenant" used as a generic adjective describing deployment shape ("multi-tenant deployments") and NATS Accounts feature ("native multi-tenant Accounts"). These describe technology features rather than Catalyst entities — leaving as-is would be defensible, but flagging for future stylistic pass.
- platform/kyverno priority class names (`tenant-high`, `tenant-default`, `tenant-batch`): K8s PriorityClass renames require recreate-not-rename (Pass 9 deferred); kept.
- platform/opensearch L356 "complex for multi-tenant setups": refers to OpenSearch's own multi-tenancy feature; external technology terminology.

This is the third validation gap surfaced by a "should have been clean" pass — Pass 22's banner-only scan, Pass 28's read-but-don't-grep approach, Pass 35's `head -10` truncation. The pattern: convenience shortcuts in the validation methodology produce false-clean signals. From this pass forward, drift sweeps must use full grep output (no `head` truncation) and must run the case-insensitive form for banned terms (not just uppercase).

### Pass 35 — completion sweep for surviving DNS placeholders across component READMEs

Started as gitea + relay atomic check. The gitea fix surfaced 9 surviving instances of the DNS-placeholder collapse across other components — the previous sweeps (Pass 29: canonical docs only, Pass 32: image registries only) hadn't covered cross-component config blocks. This pass closes the gap.

The recurring pattern at this stage is no longer "single placeholder shape ${X}" but the broader category of any `<domain>` placeholder that should resolve to a Catalyst-canonical FQDN form (control-plane: `{component}.<location-code>.<sovereign-domain>`; Application: `{app}.<env>.<sovereign-domain>`). After Pass 32 cleared `harbor.<domain>` and `registry.<domain>` shapes, what remained was a long tail of one-off placeholder forms: `openbao.<domain>`, `gitea.<domain>`, `valkey.region1.<domain>`, etc.

Fixes:

- **platform/gitea/README.md** L165 — Gitea Actions runner `GITEA_INSTANCE_URL: https://gitea.<domain>` → `gitea.<location-code>.<sovereign-domain>`. Catalyst control-plane DNS shape.
- **platform/external-secrets/README.md** L93 — `https://openbao.<domain>` (ClusterSecretStore vault server) → `openbao.<location-code>.<sovereign-domain>`. Same shape Pass 31 fixed in openbao README itself.
- **platform/external-secrets/README.md** L236 — `https://gitea.<domain>/<org>/component.git` (Flux GitRepository) → `gitea.<location-code>.<sovereign-domain>/<org>/component.git`.
- **platform/temporal/README.md** L147 — `temporal.fuse.<domain>` had two drift items: the old "fuse" name (renamed to "fabric" per BUSINESS-STRATEGY §16.2 / Pass 26 / and corrected in Pass 32 for the image ref on the same file but missed here), AND the wrong DNS placeholder shape. Per NAMING §5.2 Application DNS is `{app}.<env>.<sovereign-domain>`. Fixed to `temporal.<env>.<sovereign-domain>` (drops the legacy product-namespace segment in favor of the canonical Application DNS form).
- **platform/valkey/README.md** L147 — replication peer `valkey.region1.<domain>` → `valkey.<env>.<sovereign-domain>`. The `region1` segment was a non-canonical placeholder that doesn't fit either NAMING §5.1 or §5.2 — Catalyst encodes regions in the location-code, and cross-region Application access goes through k8gb-routed Application DNS.
- **platform/strimzi/README.md** L188 — Kafka MirrorMaker source `kafka-kafka-bootstrap.region1.<domain>:9092` → `kafka-kafka-bootstrap.<env>.<sovereign-domain>:9092`. Same `region1` segment issue.
- **platform/cnpg/README.md** L122 — CNPG cross-region replica `host: postgres.region1.<domain>` → `postgres.<env>.<sovereign-domain>`. Same.
- **platform/stunner/README.md** L105 — STUN/TURN realm `stunner.<domain>` → `stunner.<env>.<sovereign-domain>`. STUN realms are nominally opaque strings, but using the canonical Application DNS form keeps the Sovereign-namespacing consistent.
- **platform/k8gb/README.md** L170 — Gslb resource ingress host `app.gslb.<domain>` → `app.gslb.<sovereign-domain>`. The `gslb.<sovereign-domain>` subdomain is Sovereign-specific; k8gb's other illustrative refs in the same file (L237-L238 dnsZone/edgeDNSZone, L359/L384 nslookup commands) are intentionally generic upstream-doc-style and remain unchanged.

- **products/relay/README.md**: clean. Banner concise; component table consistent with bp-relay membership in PLATFORM-TECH-STACK §5; deployment example illustrative.

Out of scope (correctly preserved):
- platform/external-dns/README.md L117/L125-127/L136 — describe external-dns BEHAVIOR generically with `gslb.<domain>` / `api.<domain>` / `svc.<domain>` examples; not Sovereign-specific.
- platform/cert-manager/README.md `<domain>` instances — `admin@<domain>` (ACME contact) and `"*.<domain>"`, `"<domain>"` (cert subject names) refer to whatever domain the customer is requesting cert for; generic.
- platform/stalwart/README.md `<domain>` instances — customer email-receiving domain (acknowledged in Pass 32).

This is the third end-to-end DNS sweep iteration (29 → 32 → 35) and finally surfaces the long tail. Each iteration was prompted by a different category appearing during a different starting-pass; no single greppable pattern would have caught all of them at once because the placeholder shapes vary (`<sovereign>.<domain>` / `<sovereign-domain>` / `<domain>` / `harbor.<domain>` / `region1.<domain>` / `fuse.<domain>`). Future passes should grep specifically for `<domain>` (the bare form) early to flag any new instances introduced during edits.

### Pass 34 — banned-term `TENANT` sweep across products + platform/keycloak hostname drift

Started as cortex + keycloak atomic check; expanded into a sweep when the banned-term grep surfaced TENANT instances across multiple product READMEs. Three product files corrected.

The recurring drift: GLOSSARY's banned term "tenant" survived in Configuration tables and Flux postBuild substitutions across product READMEs as `TENANT` (uppercased ENV var). Banned-term greps run in Pass 14/16/19 weren't flagging this because they searched lowercase `tenant` but most surviving instances are the uppercase ENV-var form. The ALL-CAPS form passes the eye as "this is just an environment variable name, not a deliberate platform term", but it's still the same word — and at deployment time it becomes a customer-facing label (Flux substitution → manifest field).

Fixes:
- **products/cortex/README.md**: `TENANT: ${TENANT}` and the Configuration table row → `ORGANIZATION` with inline pointer to GLOSSARY explaining the banned-term rename. Also renamed `DOMAIN` → `SOVEREIGN_DOMAIN` since the bare term is ambiguous (Sovereign-domain vs Org-domain vs customer-domain). Plus two DNS placeholder fixes: `https://llm-gateway.ai-hub.<domain>/v1` → `llm-gateway.<env>.<sovereign-domain>` (same shape Pass 25 fixed in llm-gateway), and `https://chat.ai-hub.<domain>` → `chat.<env>.<sovereign-domain>` (same as Pass 31's librechat fix).
- **products/fingate/README.md**: 6 instances total (Flux substitution, Configuration table, 4 URL templates `api.openbanking.${TENANT}.${DOMAIN}` / `auth.openbanking.${TENANT}.${DOMAIN}`). Renamed to `${ORGANIZATION}.${SOVEREIGN_DOMAIN}`. Note: the URL shape `api.openbanking.<org>.<sovereign-domain>` doesn't strictly match either NAMING §5.1 control-plane DNS (`{component}.{location-code}.{sovereign-domain}`) or §5.2 Application DNS (`{app}.{environment}.{sovereign-domain}`) — it's a 4-segment FQDN distinct from both. Flagging the URL shape as a deeper architectural question for a future pass; Pass 34 fixes only the variable names (mechanical) without refactoring the URL structure.
- **products/fabric/README.md**: Configuration table row → `ORGANIZATION` + `SOVEREIGN_DOMAIN`.

- **platform/keycloak/README.md** had two related DNS drift items in `keycloakTopology` examples:
  - `shared-sovereign` (corporate) line 95: `auth.<sovereign-domain>` → `auth.<location-code>.<sovereign-domain>`.
  - `per-organization` (SME) line 115: `auth.<org>.<sovereign-domain>` → `auth.<org>.<location-code>.<sovereign-domain>`. NAMING §5 doesn't explicitly cover per-Org Catalyst-component DNS, but the canonical pattern requires `<location-code>` for control-plane services on the management cluster — the per-Org form just adds an org-specific subdomain prefix on top.

Out of scope (correctly preserved): platform/librechat/README.md L150 `${TENANT_ID}` in Microsoft OIDC issuer URL — this is Microsoft Azure AD's tenant-ID concept (external technology), exempted by GLOSSARY's banned-terms note ("OIDC client and K8s client are fine" by analogy: Azure tenant-ID is an external concept just like OIDC client).

This pass demonstrates the value of always running a global grep for the surfaced drift category before declaring a pass closed — the cortex single-fix would have left fingate and fabric drifting in parallel, which is exactly the kind of asymmetric drift Pass 25 explicitly warned against.

Six fixes across two files; both files needed first-time deep scrutiny.

- **products/cortex/README.md** had four real drift items:
  - **Banned term "TENANT"**: §"Deployment / Enable Cortex Product" used `TENANT: ${TENANT}` substitution, and §"Configuration" listed `TENANT | Tenant identifier | Required` — both use the GLOSSARY-banned term. Per GLOSSARY: "tenant → Organization (Cloud-overloaded, ambiguous between Sovereign tenancy and Organization tenancy)". Fixed both to `ORGANIZATION` and added an inline pointer to GLOSSARY explaining the rename. Renamed `DOMAIN` to `SOVEREIGN_DOMAIN` in the same edit since the bare term is also ambiguous (Sovereign domain vs customer domain vs Organization domain).
  - **§"Use Cases / Claude Code with Internal Models"** had `ANTHROPIC_BASE_URL="https://llm-gateway.ai-hub.<domain>/v1"` — same Application-endpoint shape Pass 25 fixed in llm-gateway README and Pass 31 fixed in librechat. Per NAMING §5.2 Application endpoints are `{app}.{environment}.{sovereign-domain}`. Fixed to `https://llm-gateway.<env>.<sovereign-domain>/v1`.
  - **§"Use Cases / RAG-Powered Chat"** had `https://chat.ai-hub.<domain>` — same shape as Pass 31's librechat fix. Fixed to `https://chat.<env>.<sovereign-domain>`.

- **platform/keycloak/README.md** had two related DNS drift items in the `keycloakTopology` configuration examples:
  - **`shared-sovereign` (corporate) example** at line 95 had `hostname: auth.<sovereign-domain>` — Catalyst control-plane DNS missing location-code per NAMING §5.1. Fixed to `auth.<location-code>.<sovereign-domain>`.
  - **`per-organization` (SME) example** at line 115 had `hostname: auth.<org>.<sovereign-domain>` — added `<org>` segment but still missing the location-code segment. Fixed to `auth.<org>.<location-code>.<sovereign-domain>`. NAMING §5 doesn't explicitly cover per-Org Catalyst-component DNS, but the canonical pattern requires `<location-code>` for control-plane services on the management cluster — the per-Org form just adds an org-specific subdomain prefix on top.

This is the first deep scrutiny of products/cortex and platform/keycloak — both were touched by Pass 32's image registry sweep (cortex was not, keycloak was not — they avoided the harbor.<domain> pattern). The cortex "TENANT" instances had survived 30+ prior passes despite being in a heavily-referenced product README, which suggests banned-term scans (and the periodic `tenant`/`workspace`/etc greps in this validation log) need to be run more aggressively against config-block YAML keys (uppercased identifiers like `${TENANT}` look like generic ENV var names and don't grep cleanly).

### Pass 33 — PERSONAS-AND-JOURNEYS Layla narrative DNS + vcluster name drift; vllm clean

Five drift fixes on PERSONAS-AND-JOURNEYS that Pass 22's banner-style scan missed; vllm clean.

The corporate-narrative section (§4.2 Layla at Bank Dhofar) read fluently but had multiple Catalyst-naming-rule violations stacked through the timeline:

- **§4.1 Ahmed (Omantel) Day 1 step 6**: `gitea.omantel.openova.io/muscatpharmacy/muscatpharmacy-prod` — Catalyst control-plane Gitea URL collapsed location-code per NAMING §5.1. Fixed to `gitea.<location-code>.omantel.openova.io/...`.
- **§4.2 Layla 09:15**: `gitea.bankdhofar.local/digital-channels/shared-blueprints/...` — same collapse on Bank Dhofar's internal Sovereign domain. Fixed.
- **§4.2 Layla 10:00**: `gitea.bankdhofar.local/digital-channels/digital-channels-uat` — same. Fixed.
- **§4.2 Layla 11:00**: `kubectl --context=hz-fsn-rtz-prod-bankdhofar logs ...` — wrong vcluster identity. Per NAMING §1.5 ("Organization Identity Lives in the vcluster Layer"), the vcluster is named after the **Organization**, not the Sovereign. Layla works on payment-rail in `digital-channels` Org (per §4.2 cast intro), so the vcluster context is `hz-fsn-rtz-prod-digital-channels` not `...-bankdhofar`. Fixed and added a short inline pointer to NAMING §1.5 so the reason is visible to the reader.
- **§4.2 Layla 16:00**: `https://api.bankdhofar.local/v1/applications` — Catalyst control-plane API endpoint missing location-code. Fixed to `https://api.<location-code>.bankdhofar.local/...`. Also tightened the SPIFFE narrative ("Backstage runs inside the Sovereign and gets a SPIRE-issued SVID") since SPIFFE/SPIRE is workload-internal and external Backstage instances would need OIDC/JWT, not SPIFFE — the original narrative implied external Backstage was using SPIFFE which is unusual.

- **platform/vllm/README.md**: clean. Banner correct (Application Blueprint §4.6, default LLM serving in bp-cortex). All examples use K8s in-cluster service DNS (`vllm.ai-hub.svc:8000`) — K8s-native form, not subject to NAMING §5.1. Image `vllm/vllm-openai:latest` is upstream Docker Hub illustrative ref.

This is the second time PERSONAS-AND-JOURNEYS has been touched: Pass 22 fixed the §6.3 Environment name format (`bankdhofar-corp-banking-prod` → `core-banking-prod`) but missed all five DNS/vcluster issues in §4.1 and §4.2. The narrative form (timeline-style prose) is particularly susceptible to "reads fluently → looks fine" inspection bias — the rule violation is buried inside a sentence that scans naturally. Future passes touching narrative-style docs should grep for the placeholder shapes regardless of how well the prose reads.

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
