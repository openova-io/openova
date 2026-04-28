# Documentation Audit Procedure

**Status:** Authoritative. **Updated:** 2026-04-28.

This document is the procedure for performing a documentation-integrity validation pass on the canonical Catalyst docs and component READMEs. It is **on-demand only** — there is no scheduled audit loop.

For invocation via Claude Code, see the `audit-catalyst-docs` skill.

---

## When to run

- After any architectural change that touches multiple docs (component additions/removals, terminology shifts, structural model changes).
- Before tagging a public release of the canonical docs.
- Before adding a new Sovereign-curated catalog (`catalog-sovereign` Gitea Org) — to confirm the upstream canon is consistent.
- On request, ad-hoc, when a contributor questions whether a doc claim is current.

**Never run as a scheduled background loop.** Past loops over-anchored on incorrect models (see `VALIDATION-LOG.md` Pass 103); text-shape consistency is not the same as architectural soundness.

---

## What the audit verifies

The audit cross-checks the canonical docs and component READMEs against five categories of anchors:

1. **Banned-term hygiene** — 11 terms in `GLOSSARY.md` §"Banned terms" must not appear (in non-exempt contexts) anywhere in the canon.
2. **Naming canonicality** — `env_type` 3-char form, DNS pattern split (control-plane vs Application), API group split (`catalyst.openova.io` vs `compose.openova.io`), JetStream subject prefix.
3. **Structural invariants** — `App = Gitea Repo` (the unified rule from Pass 103), branches `develop`/`staging`/`main` map to envs, 5 Gitea Orgs convention (`catalog`, `catalog-sovereign`, per-Catalyst-Organization, `system`).
4. **Component-count consistency** — number of `platform/<x>/` folders matches the count anchored across `CLAUDE.md`, `TECHNOLOGY-FORECAST-2027-2030.md` L11, `BUSINESS-STRATEGY.md`, and the implicit table sums in TF.
5. **Defense-in-depth architectural anchors** — load-bearing decisions (OpenBao independent-Raft per region, SeaweedFS as unified S3 encapsulation, Catalyst-as-platform / OpenOva-as-company, Valkey-NOT-control-plane, no-bidirectional-Gitea-mirror) must each appear consistently across at least 4 representational levels.

---

## The 13 acceptance greps

Run from the repo root (`/home/openova/repos/openova`). All should produce zero output unless an exemption explanation is included.

```bash
# 1. Banned terms (excluding contextual exemptions noted in GLOSSARY)
for term in 'tenant' 'Workspace' 'Lifecycle Manager' 'bootstrap wizard' 'Backstage' \
            'Synapse' 'Fuse' 'Module' 'Template' 'Operator' 'Client' 'Instance'; do
  grep -rni "\\b$term\\b" docs/ platform/*/README.md products/*/README.md core/README.md README.md CLAUDE.md \
    | grep -v 'GLOSSARY.md' | grep -v 'VALIDATION-LOG.md'
done

# 2. env_type long-form (must be 0)
grep -rnE 'acme-staging|acme-production|acme-development' docs/ platform/*/README.md products/*/README.md README.md CLAUDE.md \
  | grep -v VALIDATION-LOG

# 3. JetStream subject prefix (must show only NAMING §11.2 occurrence)
grep -rnE 'ws\.\{?(env|org)' docs/ARCHITECTURE.md docs/NAMING-CONVENTION.md docs/GLOSSARY.md docs/SECURITY.md docs/PLATFORM-TECH-STACK.md

# 4. API group split (count must be ≥7 across Catalyst CRDs + Crossplane XRDs)
grep -rnE 'compose\.openova\.io/v1alpha1|catalyst\.openova\.io/v1alpha1' \
  docs/ARCHITECTURE.md docs/NAMING-CONVENTION.md docs/SECURITY.md docs/BLUEPRINT-AUTHORING.md \
  core/README.md platform/crossplane/README.md | wc -l

# 5. Subsection ordering monotonicity
grep -nE '^### 7\.[0-9]' docs/PLATFORM-TECH-STACK.md
grep -nE '^### 2\.[0-9]|^### 11\.[0-9]' docs/NAMING-CONVENTION.md
grep -nE '^### 5\.[0-9]' docs/SECURITY.md
grep -nE '^### 9\.[0-9]' docs/SRE.md
# Manual check: numbers must be strictly increasing.

# 6. Old App-as-folder model (must be 0 outside VALIDATION-LOG)
grep -rnE 'Environment Gitea repo|/{org}/{org}-{env_type}|<org>/<org>-<env_type|per-Environment Gitea repos' \
  docs/*.md README.md CLAUDE.md | grep -v VALIDATION-LOG

# 7. Branches-map-to-envs anchor present in 4+ docs
grep -lE 'develop`/`staging`/`main|develop/staging/main|branches.*map.*env' \
  docs/GLOSSARY.md docs/NAMING-CONVENTION.md docs/ARCHITECTURE.md docs/PERSONAS-AND-JOURNEYS.md

# 8. 5 Gitea Orgs convention (must be in GLOSSARY + ARCHITECTURE + PTS + BLUEPRINT-AUTHORING)
grep -lE 'catalog-sovereign|`system` Gitea Org|five conventional Gitea Orgs|5 conventional Gitea Orgs' \
  docs/GLOSSARY.md docs/ARCHITECTURE.md docs/PLATFORM-TECH-STACK.md docs/BLUEPRINT-AUTHORING.md

# 9. Component count = 56 across all anchors (must produce no "53 components" except VALIDATION-LOG)
grep -rnE '\b53 components\b|\b53 curated\b|\b53-component\b|\ball 53\b|\b53 platform\b|\b53 folders\b' \
  docs/*.md README.md CLAUDE.md | grep -v VALIDATION-LOG
ls -d platform/*/ | wc -l    # must equal 56

# 10. SeaweedFS encapsulation (no MinIO except intentional TF L37 explanation)
grep -rinE '\bminio\b' docs/*.md README.md CLAUDE.md core/README.md products/*/README.md platform/*/README.md \
  | grep -v VALIDATION-LOG | grep -v 'platform/seaweedfs/' | grep -v 'TECHNOLOGY-FORECAST'

# 11. OpenBao independent-Raft (must appear in 5+ representational levels)
grep -lE 'INDEPENDENT, NOT STRETCHED|independent Raft cluster|no stretched cluster|Independent OpenBao Raft' \
  docs/SECURITY.md docs/ARCHITECTURE.md docs/GLOSSARY.md docs/PLATFORM-TECH-STACK.md docs/BUSINESS-STRATEGY.md

# 12. Catalyst-as-platform anchor (must appear in GLOSSARY + README + BUSINESS-STRATEGY)
grep -lE 'Company vs.*Platform|Catalyst is the open|OpenOva.*the company|Catalyst.*the platform itself' \
  docs/GLOSSARY.md README.md docs/BUSINESS-STRATEGY.md

# 13. DNS pattern split (NAMING + multiple consumers)
grep -nE '\{component\}\.\{location-code\}\.\{sovereign-domain\}|\{app\}\.\{environment\}\.\{sovereign-domain\}' \
  docs/NAMING-CONVENTION.md
grep -lE '<location-code>\.<sovereign-domain>|<env>\.<sovereign-domain>' \
  docs/SOVEREIGN-PROVISIONING.md docs/BLUEPRINT-AUTHORING.md docs/SRE.md \
  platform/llm-gateway/README.md platform/valkey/README.md
```

---

## Deep-read rotation

After greps, deep-read **one canonical doc + one component README** per pass. Rotate through the canon and the 56 platform components + 7 products (catalyst, cortex, axon, fingate, fabric, relay, specter) over time. The next-most-stale entry should be the target.

The deep-read confirms the doc's known anchors are present and consistent with the rest of the canon. For each:

1. Read the doc end-to-end.
2. Check known fix-trajectory anchors (see `VALIDATION-LOG.md` for what was previously fixed in that file).
3. Cross-check at least 2 other docs the deep-read target references, looking for bidirectional consistency.
4. Verify the **5 invariants** (Section above) hold.

---

## Output

Append a numbered Pass entry to `docs/VALIDATION-LOG.md` describing:

- Date, pass number, target doc + target component
- Acceptance grep results (clean / drift)
- Deep-read findings
- Any architectural anchors verified or flagged
- If drift: what was fixed and the new anchor

If clean: short entry confirming clean. If drift: longer entry documenting the fix and a Lesson if the drift represents a recurring pattern.

Commit message format: `docs(pass-N): <target-doc> <ordinal>-cycle + <component> <ordinal>-cycle <clean|fixed>`. Commit as `hatiyildiz` per the repo's git-identity convention.

---

## What this audit does NOT do

- **Architectural review.** Text-shape consistency does not validate that the architecture is right. Architectural review is a separate, complementary discipline. See Pass 103 and Lesson #21.
- **Code review.** Most code is design-stage per `IMPLEMENTATION-STATUS.md`. Code review is a separate concern.
- **Compliance review.** Mappings to PSD2/DORA/NIS2/SOX live in `bp-specter`'s Compliance Agent's runtime evaluation, not in doc audit.
- **Security review.** Security review is `/security-review` skill's domain.

---

*Part of [OpenOva](https://openova.io)*
