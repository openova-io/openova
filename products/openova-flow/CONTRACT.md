# OpenovaFlow wire contract — `FlowMessage` JSON schema

Schema version: **1** (Foundation release, 2026-05-11)

## Discriminator

Every `FlowMessage` has a `type` field that discriminates the variant:

```
"snapshot" | "upsert-flow" | "upsert-nodes" | "upsert-rels" | "delete-nodes" | "delete-rels"
```

Agent #2 (server) implements message emission; Agent #3 (catalyst-api
adapter) wires it to a concrete backend. The shape below is normative.

## Object schemas

### `FlowInstance`

```json
{
  "id": "string (unique, required)",
  "definitionId": "string (optional, template id)",
  "parentFlowId": "string (optional)",
  "triggeredBy": [
    { "flowId": "string", "when": "success | failure | always" }
  ],
  "status": "string (open vocabulary)",
  "startedAt": "number (unix ms)",
  "endedAt": "number (unix ms, optional)",
  "meta": { "...adapter-defined": "..." }
}
```

### `FlowNode`

```json
{
  "id": "string (unique within flowId, required)",
  "flowId": "string (required)",
  "label": "string (required)",
  "status": "string (open vocabulary, required)",
  "family": "string (optional)",
  "region": "string (optional)",
  "startedAt": "number (unix ms, optional)",
  "endedAt": "number (unix ms, optional)",
  "meta": { "...adapter-defined": "..." }
}
```

### `Relationship`

```json
{
  "fromId": "string (required)",
  "toId": "string (required)",
  "fromFlowId": "string (optional, omit if same-flow)",
  "toFlowId": "string (optional, omit if same-flow)",
  "type": "contains | finish-to-start | start-to-start | finish-to-finish | start-to-finish | triggers",
  "condition": "on-success | on-failure | always",
  "lag": "number (seconds, optional)"
}
```

## Message variants

### `snapshot`
Full state for a single flow.
```json
{
  "type": "snapshot",
  "flow": { "...FlowInstance": "..." },
  "nodes": [{ "...FlowNode": "..." }],
  "relationships": [{ "...Relationship": "..." }]
}
```

### `upsert-flow`
Flow-level metadata changed (status, endedAt, meta).
```json
{ "type": "upsert-flow", "flow": { "...FlowInstance": "..." } }
```

### `upsert-nodes`
One or more nodes added/updated. Consumer merges by `(flowId, id)`.
```json
{ "type": "upsert-nodes", "nodes": [{ "...FlowNode": "..." }] }
```

### `upsert-rels`
One or more relationships added/updated. Consumer merges by
`(fromId, toId, type)` — the natural key.
```json
{ "type": "upsert-rels", "relationships": [{ "...Relationship": "..." }] }
```

### `delete-nodes`
Remove nodes by id. Relationships pointing to/from the removed nodes
are pruned by the consumer.
```json
{ "type": "delete-nodes", "ids": ["string"] }
```

### `delete-rels`
Remove specific edges by their natural key.
```json
{
  "type": "delete-rels",
  "pairs": [{ "fromId": "string", "toId": "string", "type": "RelationshipType" }]
}
```

## RelationshipType vocabulary

| Type | Semantics |
|---|---|
| `contains` | Structural / hierarchical. `toId` (parent) contains `fromId` (child). Replaces the legacy `parentId` field on nodes. Not rendered as an edge — drives grouping only. |
| `finish-to-start` | PMI FS. `toId` starts after `fromId` finishes. The default temporal dependency. |
| `start-to-start` | PMI SS. `toId` starts when (or after) `fromId` starts. |
| `finish-to-finish` | PMI FF. `toId` finishes when (or after) `fromId` finishes. |
| `start-to-finish` | PMI SF. `toId` finishes after `fromId` starts. Rare; usually inventory / scheduling-driven. |
| `triggers` | Event-driven. `fromId` emits an event that triggers `toId`. No temporal constraint beyond the event itself. |

## Conventions per adapter

| Adapter | Typical relationship emissions |
|---|---|
| Catalyst (Flux + helmwatch + jobs) | `contains` for batches → installs; `finish-to-start` / `on-success` for `dependsOn`. No SS/FF/SF/triggers yet. |
| Temporal | `triggers` for signal-driven children; `contains` for parent-child workflow nesting. Failure paths emit `on-failure` finish-to-start. |
| Argo Workflows | `finish-to-start` for `dependencies`; `triggers` for sensor-triggered children. |
| Flux Reconcilers | `triggers` (event-driven by GitOps push); `finish-to-start` for `dependsOn`. |
| Custom user code | Whatever fits the domain; the canvas renders all 6 types. |

## Versioning

The schema version is pinned at `1` on `FlowAdapter.schemaVersion`.
Future incompatible changes (renaming a discriminator, changing a
required field) bump to `2` — adapters and hosts negotiate at
subscribe time. Backwards-compatible additions (new optional fields,
new relationship types in a future PMI extension) do NOT bump.
