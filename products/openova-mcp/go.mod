// openova-mcp is the OpenOva MCP server (#3988) — an RBAC-scoped-per-user
// thin facade that exposes the OpenOva product surface (Applications,
// Environments, Organizations, …) as MCP tools whose surface is IDENTICAL
// to what the same user can do in the Catalyst console UI.
//
// First vertical slice (this module): the MCP server core + a handful of
// REAL read-only Org-scoped tools backed by the LIVE catalyst-api. The
// server is a THIN facade — it reimplements NOTHING. It:
//
//   - parses the caller's bearer into the SINGLE source-of-truth claim
//     contract core/services/shared/auth.Claims (the SAME shape the
//     console + catalyst-api already use — NOT a parallel auth type),
//   - derives the caller's realm context (Org realm vs Sovereign realm),
//     tier, and Org scope from those claims,
//   - filters tools/list by (tier, scope) so the agent's surface == the
//     user's UI surface (defense-in-depth layer 1),
//   - re-authorizes every tools/call against the same claims (layer 2),
//   - forwards the caller's IDENTITY to the real catalyst-api REST
//     endpoint for the actual data — holding no god-token of its own.
//
// Deferred to follow-ups (NOT in this slice): chepherd injection, the
// HTTP/SSE transport, write/mutating tools, Sovereign-scoped admin tools,
// the full ~26/~70 tool catalog, the registry-coverage parity test.
//
// We depend on the in-tree shared-auth module via the same `replace`
// pattern catalyst-bootstrap + the sandbox MCP already use, so the claim
// contract has exactly one definition repo-wide.
module github.com/openova-io/openova/products/openova-mcp

go 1.23.0

toolchain go1.23.4

require (
	github.com/golang-jwt/jwt/v5 v5.2.2
	github.com/openova-io/openova/core/services/shared v0.0.0
)

replace github.com/openova-io/openova/core/services/shared => ../../core/services/shared
