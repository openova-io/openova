# ADR-0023 — Sandbox withdrawn; the per-Organization agent workspace and the tool server replace it

Status: Accepted
Date: 2026-11-13

Written by work package FND-106. It records a decision that was taken on 2026-06-30 and never written as a decision record.

## Context

The Sandbox was a product for an agent session per Organization: a custom resource `Sandbox`, a controller, a terminal server, a tool server of its own, console pages and API routes.

| Date | What happened | Where it is recorded |
|---|---|---|
| 2026-05-15 | The first Sandbox files enter the tree. | Commit `2c3ea44af` |
| 2026-06-30 | The founder removes the Sandbox concept and its menu. Pillar 4 is the agent workspace and the tool server from then on. | `docs/DOD.md`, the note at its head and the note under "Pillar 4" |
| 2026-07-15 | Bootstrap-kit slot 19a, which installed the Sandbox controller on every Sovereign, is retired. The tool server `bp-openova-mcp` gets slot 13d. | Commit `9ed9619e1`; the comment in `clusters/_template/bootstrap-kit/kustomization.yaml` |
| 2026-11-06 | The Sandbox card is removed from the catalogue of the sovereign-admin console. | Commit `2fde3b866` |

The decision was written in `docs/DOD.md`, `docs/STATUS.md` and `docs/GLOSSARY.md`, and in no decision record. The code stayed. Measured at commit `1de1896b3`:

- 87 files in `products/sandbox`, `platform/sandbox` and `core/controllers/sandbox`.
- Three workflows still build Sandbox images: `build-sandbox-controller.yaml`, `build-sandbox-mcp-server.yaml`, `build-sandbox-pty-server.yaml`. A bot committed 74 image bumps to the Sandbox paths after the slot was retired.
- Nine routes under `/api/v1/sandbox/` in the API, four console pages with three console routes, an orchestrator in the service `core/services/tenant`, and the definition file `products/catalyst/chart/crds/sandbox.yaml`.

## Decision

1. **The Sandbox is withdrawn.** It is not a product, not a custom resource and not a console surface of the platform. It does not return under another name.
2. **Two things replace it.**
   - The per-Organization agent workspace, Blueprint `bp-agenity` (`products/agenity/`). A User installs it into an Organization from the catalogue. The console reaches it at `/apps/bp-agenity/dashboard`, an entry that the Blueprint itself declares.
   - The tool server, Blueprint `bp-openova-mcp` (`products/openova-mcp/`). It is a thin facade over the platform API. A caller sees exactly the tools that its own role allows in the console.
3. **The agent workspace stays, and it runs on each Organization's own sign-in.** No credential supplied by one party serves another party: each Organization brings its own sign-in or key. Package AIP-155 replaces the credential that is seeded from the Sovereign today. No package of the removal below takes away the workspace's model path or its tool identity.
4. **Every remaining Sandbox surface is deleted, in this order.** Each step is one work package.

| Step | What is removed | Package |
|---|---|---|
| 1 | The three build workflows named above. | FND-092 |
| 2 | The nine API routes, their handlers and tests, and the publisher of Sandbox events. | FND-093 |
| 3 | The four console pages, the file `sandbox.api.ts` that calls the routes, the three console routes and their browser test. | FND-094 |
| 4 | The orchestrator in `core/services/tenant`, the role grants on Sandbox objects, and the entries of the cutover chart for `bp-sandbox`. | FND-095 |
| 5 | `products/catalyst/chart/crds/sandbox.yaml`, `core/controllers/sandbox`, `products/sandbox`, `platform/sandbox` and `tests/e2e/sandbox-mcp-contract.sh`. | FND-096 |

5. **A deleted path does not return.** Each step appends its paths to `docs/ledger/deleted-paths.txt`, which a guard reads (package FND-091 adds the list and the guard). Step 5 adds a guard that counts the files outside `docs/` that still hold the word, and that count may only fall.

## What is not Sandbox and stays

- The workflow `build-bp-newapi-sandbox-bridge.yaml`. It builds a sidecar of the model gateway chart from `platform/newapi/internal/handler/**`. Only its name holds the word. It is not renamed here; it leaves with that chart (package AIP-184).
- The rule in `docs/GLOSSARY.md` that user-facing text says Agenity, never Sandbox.
- Comment lines that mention the retired slot. A delete package removes what creates, reads, grants or pins a Sandbox object. The word-count guard brings the rest down.

## Consequences

When the five steps are merged:

- No workflow builds a Sandbox image, so no bot commit bumps one.
- The API, the console and the service `core/services/tenant` hold no Sandbox code path.
- The agent workspace is reached only through its Blueprint. The definition of done for pillar 4 is not reworded by this decision.
- The plan still uses the word for other things: a runtime class for agent pods and a test ledger of a later product. Neither is this product.
