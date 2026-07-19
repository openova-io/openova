# OpenOva Sandbox

**Status:** Wave 1-5 implementation in flight (PRs **#1615 / #1618 / #1619 / #1621 / #1622 / #1632** merged; runtime smoke pending fresh prov). **Created:** 2026-05-15. **Implementation started:** 2026-05-17.

> **Founder TODO:** Register an Anthropic OAuth client_id for the BYOS Claude Code flow per [`docs/claude-code-byos.md`](docs/claude-code-byos.md), and paste it into the Sovereign Console BYOS settings (or set `SANDBOX_ANTHROPIC_OAUTH_CLIENT_ID` on the controller Deployment). The Sandbox controller looks up the value via env-var; everything else around it is already scaffolded.

OpenOva Sandbox is the per-user, per-Organization coding-agent plane that runs **inside** every OpenOva Sovereign. It hosts long-lived sessions of the agents developers already use (Claude Code, Cursor, Qwen Code, Aider, Opencode) — server-side, cluster-aware, identity-scoped — and surfaces them through a native terminal in the browser plus a card-stream view on mobile, both backed by the same persistent process.

Sandbox is also the conversational front-door to provisioning a brand-new Sovereign: the same shell, scoped to a narrower MCP tool surface, lets a non-technical user talk (text or voice) through standing up their cloud instead of filling in a wizard.

## Naming

The chosen name is **OpenOva Sandbox**. Alternatives we considered:

| Name | Positioning | Why we did not pick it |
|---|---|---|
| **Sandbox** *(chosen)* | "The cloud sandbox where your agents do real work" | Plain noun, matches `Sovereign` / `Catalyst` style. Inherits the moat directly: a real cloud sandbox per user, not a browser tab. |
| Forge | Active and agentic ("forge production code") | "Forge" is taken by smaller dev tools; trademark friction. |
| Studio | Lineage with Android/Visual Studio / Codespaces | Generic — nothing about the cluster-aware moat is implied in the name. |

## Contents

- [`docs/business-requirements.md`](docs/business-requirements.md) — what we are solving, who for, the moat, success criteria.
- [`docs/user-journey.md`](docs/user-journey.md) — end-to-end wireframe storyboard for the developer (Nova user) and the Sovereign admin, including multi-device handoff and the EventForge build walkthrough.
- [`docs/architecture.md`](docs/architecture.md) — technical architecture: native TUI in the browser via xterm.js + WebSocket + PTY, the card protocol for mobile, the MCP server tool catalogue, the four knowledge layers (static / procedural / live / corpus), and exact integration points with the existing OpenOva primitives (vcluster per Org, Keycloak modes, Gitea, marketplace BYOD, JetStream, SSE).
- [`docs/provisioning-chat.md`](docs/provisioning-chat.md) — the conversational alternative to the catalyst-ui wizard; text + voice; same shell, narrower MCP surface.

## What is already there, what we still need

Confirmed against the codebase (2026-05-15):

| Foundation primitive | State | Reference |
|---|---|---|
| `Organization` CRD (`orgs.openova.io/v1`) | Shipped | `products/catalyst/chart/crds/organization.yaml` |
| vcluster per Org | Shipped | `core/controllers/organization/internal/gitops/manifests.go` |
| Keycloak realm (sovereign-shared vs per-Org mode) | Shipped | `platform/keycloak/chart/values.yaml`, `chart/templates/configmap-{sovereign,tenant}-realm.yaml` |
| Gitea Org + `catalyst-tenant` repo auto-provisioned per Org | Shipped | `core/controllers/organization/internal/controller/organization_controller.go` |
| UserAccess CR → RoleBindings (RBAC fan-out) | Shipped | same controller |
| Marketplace: subdomain + BYO custom domain | Shipped | `core/services/domain/handlers/handlers.go` (`POST /domain/byod`), `core/marketplace-api/handlers/handlers.go` |
| JetStream subject convention `catalyst.<domain>.<event>` | Shipped (ADR-0001 §6) | `core/services/shared/events/nats.go` |
| SSE feeds for deployments / cutover / flow / RBAC / K8s / continuum / openova-flow | Shipped (7+ endpoints) | `products/catalyst/bootstrap/api/internal/handler/*.go`, `products/openova-flow/server/internal/api/stream.go` |
| Harbor / SeaweedFS at host-cluster scope (multi-Org via projects/buckets) | Shipped (by design — not per-Org instances) | `platform/harbor/README.md`, `platform/seaweedfs/README.md` |

The **one** prerequisite Sandbox needs that does not exist today:

| Gap | What we need | Where to wire |
|---|---|---|
| Long-lived API token carrying `org_id` claim | A persistent token issued by Keycloak (or `core/services/auth`) that includes `org`, `groups`, and a Sandbox capability set. Today only a 15-minute JWT with `{sub, email, role}` exists; the tenant-realm Keycloak import has a `groups` mapper but not an `org` mapper. | `core/services/auth/handlers/handlers.go` (token issuance) + `platform/keycloak/chart/templates/configmap-tenant-realm.yaml` (add `org` protocolMapper) |

Everything else Sandbox needs is greenfield product work and is described in the linked docs.
