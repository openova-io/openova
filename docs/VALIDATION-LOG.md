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
