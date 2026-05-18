# openova-sandbox-mcp

Per-Sandbox MCP server. One sidecar in every Sandbox pod. Speaks
JSON-RPC 2.0 over stdio to the local agent process
(`claude`, `cursor-agent`, `qwen-code`, `aider`, `opencode`). Speaks
HTTPS to the Sovereign control plane using the Sandbox-scoped token.

See `products/sandbox/docs/architecture.md` §3 for the full namespace
list and §4 for how this server connects the static / procedural / live
/ corpus knowledge layers.

## Wave 2 (this PR) — scaffold

- JSON-RPC plumbing (Content-Length framing, `initialize`, `tools/list`,
  `tools/call`, `ping`, init/cancel notifications).
- Tool catalogue stubs across the four required namespaces:
  - `gitea.*`
  - `k8s.read.*`
  - `sandbox.db.*`
  - `sandbox.auth.*`
- Plus `sandbox.session.*` (whoami / info) for agent self-discovery.

Every Wave 2 tool returns `{"status":"not_implemented", "tool":"<name>",
"wave":2}` so the agent can list, dispatch, and reason about the surface
end-to-end before any backend is wired.

## Wave 3+ (next)

- Real handlers wired against:
  - Gitea API (per-Org Org URL from `core/services/domain`).
  - Sovereign k8s read API (vcluster-scoped client).
  - CNPG provisioning via `Cluster.postgresql.cnpg.io`
    (`platform/cnpg/`).
  - Keycloak Admin REST against the per-Sandbox realm.
- MCP `resources/subscribe` for JetStream-backed live updates
  (architecture.md §3 / ADR-0001 §6).
- RBAC enforcement on every call against the Sandbox JWT claims
  (`sovereign_id`, `org_id`, `sandbox_id`, `roles[]`).

## Build & run

```bash
cd products/sandbox/mcp-server
go build ./cmd/openova-sandbox-mcp
./openova-sandbox-mcp        # waits for JSON-RPC on stdin
```

Local smoke (one-shot `initialize` then exit):

```bash
{
  printf 'Content-Length: 81\r\n\r\n'
  printf '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"x"}}'
} | ./openova-sandbox-mcp
```

You should see a `Content-Length: ...` framed JSON-RPC response on
stdout with `serverInfo.name = "openova-sandbox-mcp"`.

## Container

```bash
docker build -t ghcr.io/openova-io/openova/sandbox-mcp:dev .
```

The image has no port — the orchestrator pipes stdin/stdout into the
agent's child process.
