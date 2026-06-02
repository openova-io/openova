# G117 — worktree isolation policy + per-wave file-touch matrix

> Produced by Wave-0 (PR linked from #2738). Binding for every Wave-1+ dispatch. Disregarding this policy causes the chart-version-collision class of failures captured in `feedback_chart_version_collision_serialize_or_rebase.md`.

## Rules

1. **One worktree per agent when 2+ agents touch the SAME file.** Each worker checks out a fresh `git worktree add ../openova-g117-<slot> feat/g117-<slot>-<scope>` so their per-file edits don't clobber on a shared branch.
2. **No `isolation: worktree`** for slots that touch DISJOINT files. The overhead isn't free; spend it only when collision risk is real.
3. **Serialize (NOT parallelize)** when 2+ agents must touch a SINGLE file (e.g. `core/controllers/pkg/apis/blueprint/v1alpha1/topology_types.go`).
4. **Pre-flight per agent**, ALWAYS:
   ```bash
   git fetch origin main
   git pull --rebase origin main
   # "latest" for a file ALWAYS comes from git show origin/main:<path>
   git show origin/main:platform/grafana/blueprint.yaml > /tmp/baseline.yaml
   # NEVER use a remembered value from a prior conversation turn.
   ```
5. **Chart-version lockstep**: Chart.yaml + blueprint.yaml + bootstrap-kit slot pin MUST all bump in the same PR. If you touch one, you touch all three (per `feedback_chart_version_collision_serialize_or_rebase.md`).

## File-touch matrix — Wave-1 (6 parallel agents)

| Slot | Sub-EPIC | Touches | Reads (no write) | Isolation |
|---|---|---|---|---|
| **B1** | G117.1 — Blueprint schema extension | `core/controllers/blueprint/internal/validate/*.go`, `platform/_schemas/blueprint-topology.json` (refine), `tests/e2e/playwright/tests/g117-blueprint-admission.spec.ts` | `core/pkg/apis/blueprint/v1alpha1/`, `examples/blueprint-grafana-fully-declared.yaml` | **none** (disjoint from B2/B3) |
| **B2** | G117.2 — Catalog redesign + multi-instance | `core/services/catalyst-catalog/**`, `products/catalyst/console/src/routes/catalog/`, `products/catalyst/console/src/lib/api/catalystApi.ts`, `tests/e2e/playwright/tests/g117-catalog-drilldown.spec.ts` | `docs/api/catalyst-api-openapi.yaml`, `products/catalyst/console/tests/e2e/fixtures/mock-blueprints.yaml` | **worktree** (shares console/ with B3+B4) |
| **B3** | G117.3 — Endpoints/Ingress tab + Git-IaC PR pipeline | `core/controllers/application/internal/endpoints/**`, `products/catalyst/console/src/routes/apps/[id]/endpoints/`, `tests/e2e/playwright/tests/g117-endpoints-tab.spec.ts` | `docs/api/catalyst-api-openapi.yaml`, `tools/bootstrap-org-iac-repo.sh` | **worktree** (shares console/ with B2+B4) |
| **B4** | G117.4 — Launch button + silent SSO | `core/services/catalyst-api/internal/launch/**`, `products/catalyst/console/src/routes/apps/[id]/+page.svelte` (Launch button only), `tests/e2e/playwright/tests/g117-launch-silent-sso.spec.ts` | `docs/adr/0009-per-org-iac-repo-bootstrap.md`, KC realm config | **worktree** (shares apps/[id]/+page.svelte with B3) |
| **B5** | G117.5 — SSO fan-out 3 tiers + per-Org realm | `platform/keycloak/chart/templates/configmap-*-realm.yaml`, `platform/<bp>/chart/templates/secret-sso-*.yaml` for each SSO-capable bp | KC realm-config conventions | **worktree** (shares KC realm-config templates across all C-slots) |
| **B6** | G117.6 — application-controller multi-instance + topology + per-Org realm | `core/controllers/application/internal/controller/*.go`, `core/controllers/application/internal/render/*.go` | `core/controllers/pkg/apis/blueprint/v1alpha1/topology_types.go`, ADR-0009 | **none** (disjoint) |

### Why this split

- B1 → contract-first (admission + validator + tests). Wave-2 chart edits depend on B1's schema being merged.
- B2 → catalyst-api + console catalog. Reads B1's contract.
- B3 → endpoints PR pipeline. Reads B1's contract + ADR-0009.
- B4 → Launch button. Reads B3's endpoints registry shape.
- B5 → KC realm config. Cross-cutting across all SSO-capable Blueprints; must serialize per Blueprint OR worktree-isolate per Blueprint.
- B6 → application-controller. Consumes B1's topology types + spec.topology data.

## Wave-2 staging (after Wave-1 GREEN on main)

Wave-2 fills out per-Blueprint topology stubs across `platform/<bp>/blueprint.yaml`. Six parallel agents, each owning a partition of `tools/migrate-blueprints-topology.py`'s output:

| Slot | Partition | Files touched | Isolation |
|---|---|---|---|
| **C1** | Catalyst-CP tier (12 BPs) | `platform/grafana/`, `platform/keycloak/`, `platform/openbao/`, `platform/gitea/`, … | **worktree** (each agent gets its own; per-Blueprint Chart.yaml lockstep mandatory) |
| **C2** | Per-host-cluster infra (~25 BPs) | `platform/cilium/`, `platform/flux/`, `platform/cert-manager/`, … | **worktree** |
| **C3** | App Blueprints (~25 BPs) | `platform/ferretdb/`, `platform/valkey/`, `platform/clickhouse/`, … | **worktree** |
| **C4** | Charts: lockstep version bumps | `platform/<bp>/chart/Chart.yaml` for every changed bp | **worktree** + flock GHCR push lock |
| **C5** | Bootstrap-kit pin updates | `clusters/_template/bootstrap-kit/kustomization.yaml` | serialize after C1+C2+C3+C4 |
| **C6** | Docs + memory + ADR pass | `docs/STATUS.md`, `docs/ledger/TRACKER.md`, `docs/sessions/<date>.md`, memory feedback files | **none** (docs-only) |

## Worktree creation cheatsheet

```bash
# From the bastion, on the openova repo:
cd /home/openova/repos/openova

# Create a fresh worktree for slot B2 of Wave-1:
git fetch origin main
git worktree add -b feat/g117.2-catalog-redesign \
  ../openova-g117-B2 \
  origin/main

cd ../openova-g117-B2
# Verify clean baseline
git status
git log --oneline | head -5
# Now safe to edit + commit + push WITHOUT colliding with sibling agents
```

## Anti-collision pre-commit hook (recommended for B5/C-slots)

Drop into `.git/hooks/pre-commit` of each worktree:

```bash
#!/usr/bin/env bash
# Refuse to commit if files in the diff overlap with sibling worktree's staged files.
set -euo pipefail
my_files=$(git diff --cached --name-only)
for sibling in /home/openova/repos/openova-g117-*; do
  [ "$sibling" = "$PWD" ] && continue
  [ -d "$sibling/.git" ] || continue
  sib_files=$(cd "$sibling" && git diff --cached --name-only 2>/dev/null || true)
  for f in $my_files; do
    if echo "$sib_files" | grep -qxF "$f"; then
      echo "BLOCK: $f is also staged in $sibling — serialize or rebase" >&2
      exit 1
    fi
  done
done
```

## Branch naming convention

| Wave | Branch pattern |
|---|---|
| Wave-0 | `feat/g117-wave-0-preflight` |
| Wave-1 | `feat/g117.<N>-<short-topic>` (e.g. `feat/g117.2-catalog-redesign`) |
| Wave-2 | `feat/g117.<N>-<partition>` (e.g. `feat/g117-platform-topology-cp`) |

## Merge order (per Wave)

1. B1 merges first (contract).
2. B6 + B2 + B3 + B4 merge in parallel (consumers).
3. B5 merges last (per-Blueprint Secret templates depend on B2's catalog-api shape).

If a later slot needs a baseline that a not-yet-merged earlier slot provides, **rebase**, don't fix-forward. Per `feedback_chart_version_collision_serialize_or_rebase.md`: always `git show origin/main:<file>` for the latest value before bumping.
