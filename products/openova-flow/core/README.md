# `@openova/flow-core`

Plugin-shaped contract + pure layout engine for OpenovaFlow — the
standalone, OpenOva-independent flow-visualization product.

This package has **zero runtime side effects**, **zero network code**,
**zero React-DOM** dependencies. It is the only package callers need
to type their data against before subscribing to a `FlowAdapter`.

## Concepts

- **FlowInstance** — a runtime DAG-run. Carries `id`, optional
  `definitionId` (the template id, for DAG vs DAG-run), optional
  `parentFlowId` (nested child workflow), and a `triggeredBy[]`
  chain for event-triggered flows.
- **FlowNode** — one node inside a flow. Belongs to **exactly one**
  flow via `flowId`. Cross-flow references travel on Relationships,
  not on the node.
- **Relationship** — a typed directed edge. Six types: `contains` for
  structural hierarchy + the five PMI temporal types (`finish-to-start`,
  `start-to-start`, `finish-to-finish`, `start-to-finish`) + `triggers`
  for event-driven edges. Edges may be `on-success` (default),
  `on-failure` (overlay, not counted for depth), or `always`.
- **FlowAdapter** — the plugin interface. Implements `subscribe()` to
  push `FlowMessage` events to a sink, supplies a `statusPalette`,
  `families`, `regions`, and optional `renderDetail` / `actions`.
- **FlowMessage** — the wire protocol. Six variants: `snapshot`,
  `upsert-flow`, `upsert-nodes`, `upsert-rels`, `delete-nodes`,
  `delete-rels`.

## Usage

```ts
import { layout, type FlowAdapter } from '@openova/flow-core'

const myAdapter: FlowAdapter = {
  schemaVersion: 1,
  subscribe(flowId, sink) {
    const es = new EventSource(`/flow/${flowId}/events`)
    es.onmessage = (e) => sink(JSON.parse(e.data))
    return () => es.close()
  },
  statusPalette: {
    pending: { fill: '...', ring: '...', /* ... */, label: 'Pending' },
    running: { /* ... */ },
    succeeded: { /* ... */ },
    failed: { /* ... */ },
  },
}

// Later, when you have a snapshot:
const positioned = layout({
  flow,
  nodes,
  relationships,
  folded: new Set<string>(),
  hints: {
    perNode: new Map([
      ['n1', { region: 'eu', family: 'platform' }],
    ]),
    families: myAdapter.families,
    regions: myAdapter.regions,
  },
})
// → positioned.positionedNodes, positioned.edges, positioned.components
```

## Layout semantics — locked invariants

| Invariant | Origin |
|---|---|
| Cycle safety in parent-chain walks (no infinite loops on malformed inputs) | Bug #476 |
| Fold-aware rewiring — folded groups emit ONE node | Issue #351 |
| Parent-elision — unfolded groups with visible children disappear from the bubble set; their inbound + outbound deps fan out / lift onto the visible children ("parent calling their parents") | Bug #481 round 2 |
| `MAX_VISIBLE_DEPTH = 8` defence-in-depth cap | Bug #481 round 2 |
| Global topological-sort `depRank` for stable Y-axis order | Issue #532 |
| `on-failure` edges are NOT counted toward depth | Founder locked 2026-05-11 |

## Testing

```sh
npm test               # vitest --pool=threads --maxWorkers=2 --no-isolate
npm run typecheck      # tsc --noEmit -p tsconfig.json
```

NEVER `npm run build` / `playwright install` / `playwright test`.
