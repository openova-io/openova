# catalog epic (#3668) — live walk on omantel.biz — 2026-06-22

## Scope note
The catalog-IaC-edit epic (rows 123-158) is dominated by **write** operations: inline card-field edits, icon-picker saves, and full-CR Edit-IaC YamlEditor commits that write `blueprint.yaml` to Gitea and trigger Flux reconcile. On a **READ-ONLY** verifier walk against the PERMANENT env, these mutations were deliberately NOT performed (guardrail: do not mutate live resources). The render-level surfaces WERE verified; the edit/save/persist rows are marked WARN pending a dedicated catalog-edit pass.

## Verified read-only
- **Row 123** PASS — bare URL → /dashboard signed-in.
- **Row 124** PASS — catalog grid renders **94 Blueprint cards** (sample: bp-agenity, bp-alloy, bp-axon, bp-bge, bp-catalyst-platform, bp-cert-manager…), with search box; Alloy card visible (`evidence/model-catalog.png`).
- **Row 125** PASS — blueprint detail page renders hero (icon + name + summary + **Edit IaC**) + About + Instances list, no login redirect (verified on bp-postgres: hero "PostgreSQL", v0.1.6, multi-instance/shareable, Instances table; `evidence/model-bp-postgres-detail.png`).
- **Row 131** WARN — icon baseline render-only.
- **Rows 126-130, 132-158** WARN — inline edit / icon picker / per-field edit / Edit-IaC YamlEditor commit / cross-blueprint parity / acceptance headlines all require IaC **writes** that mutate Gitea + trigger reconcile; not exercised read-only. The catalog grid + detail + Edit-IaC affordance render is the read-only floor.

## Catalog inventory (live)
- 81 Blueprint CRs in-cluster (`blueprints.catalyst.openova.io`); 94 cards in the console grid (includes virtual/aggregate cards).
