# Proposal — Jobs dependencies visualization

**Refs:** openova-io/openova#204 (item 11), #206

## Recommendation

Ship a **lightweight SVG-based DAG inline on the Job-detail Dependencies tab** as
the primary surface, paired with a stretch **fullscreen Gantt timeline** at
`/sovereign/provision/$id/jobs/timeline` for retrospective / parallelism analysis.

## Rationale

The founder reads dependency *relations* far more often than *timing*: "what
unlocks what", "did Cilium block cert-manager", "is bp-keycloak waiting on
postgres". A DAG answers those at a glance. The chart is small (~30 jobs at the
upper bound of a single Sovereign provision), so a pure-SVG topological-layered
layout in ~150 lines outperforms pulling in `reactflow`/`cytoscape`/`d3-dag`
(every one of which adds 100–300 KB to the wizard bundle, plus a `useEffect`
container-ref dance that conflicts with our test seam).

## Tradeoffs considered

| View | Strength | Weakness |
|------|----------|----------|
| Gantt timeline | Time + parallelism analysis. Operator-grade chart. | Hides dependency edges; dominated by tofu phase length; less useful pre-run. |
| DAG inline | Reads dependency relations directly; renders before any job has started. | Worse for "how long did this take". |
| Heavy graph lib (reactflow, cytoscape) | Pan/zoom/minimap out of the box. | 100–300 KB bundle; `useRef` pattern fights our memory-history test seam; founder rule #4 (never hardcode) prefers our own deterministic layout. |

We ship **DAG primary + Gantt stretch** because they're complementary, not
competing: the DAG lives in the per-job detail tab (operator drill-down),
the Gantt lives at a separate route (operator retrospective).

## Scope of this PR

- `src/widgets/job-deps-graph/JobDependenciesGraph.tsx` — SVG DAG.
- `src/shared/lib/depsLayout.ts` — pure topological-layered layout function.
- `src/test/fixtures/deps-graph.fixture.ts` — shared mock until backend lands.
- `src/pages/sovereign/JobsTimeline.tsx` (stretch) — `/jobs/timeline` Gantt.
- Tests + Playwright screenshots at 1440px under
  `.playwright-mcp/jobs-deps-viz/`.
