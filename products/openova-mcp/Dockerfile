# syntax=docker/dockerfile:1.7
#
# openova-mcp — the OpenOva MCP server (#3988), packaged as a standalone
# OCI image for the bp-openova-mcp Blueprint (#899, DRAFT design packaging).
#
# ── Transport reality (READ THIS) ────────────────────────────────────────
# The first slice of openova-mcp speaks JSON-RPC 2.0 over **stdio** only
# (cmd/openova-mcp reads os.Stdin / writes os.Stdout — the same proven
# transport the sandbox MCP uses). It has NO HTTP listener and binds NO
# port, so there is deliberately NO `EXPOSE` here. Today the binary is
# consumed as a forked stdio child of an agent (agenity bakes this exact
# binary at /usr/local/bin/openova-mcp and launches it per session via
# ~/.claude.json `mcpServers.openova.command`). The streamable-HTTP/SSE
# transport that a long-lived, HTTPRoute-fronted Service needs is an
# explicit #3988 §5 follow-up (see products/openova-mcp/README.md). The
# accompanying Helm chart gates every serving resource behind
# `httpTransport.enabled` (default false) until that transport lands.
#
# Build context: invoked with the repository ROOT as the build context.
# products/openova-mcp/go.mod carries a `replace` directive pointing at
# ../../core/services/shared (re-using the canonical auth.Claims shape so
# the claim contract has exactly one definition repo-wide), so the build
# context MUST cover the repo root — same pattern as
# products/sandbox/mcp-server/Dockerfile.
#
# Two stages:
#   build — golang:1.23-alpine
#   final — distroless/static-debian12:nonroot (scratch-equivalent, non-root)

FROM docker.io/library/golang:1.23-alpine AS build
WORKDIR /repo
RUN apk add --no-cache git

# Stage 1: pre-stage the go.mod/go.sum of the replace target + the dependent
# module so `go mod download` resolves the replace directive without first
# copying every source file. Copy the replacement module root before the
# dependent module so the resolver sees it at the path the go.mod points at.
COPY core/services/shared/go.mod core/services/shared/go.sum /repo/core/services/shared/
COPY products/openova-mcp/go.mod products/openova-mcp/go.sum /repo/products/openova-mcp/

# Stage 2: copy source for both module trees (the replace target must be on
# disk in full for `go build` to compile the imported auth package).
COPY core/services/shared /repo/core/services/shared
COPY products/openova-mcp /repo/products/openova-mcp

WORKDIR /repo/products/openova-mcp
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /out/openova-mcp ./cmd/openova-mcp

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.source="https://github.com/openova-io/openova"
LABEL org.opencontainers.image.description="OpenOva MCP server — RBAC-scoped-per-user thin facade over the catalyst-api (stdio transport; HTTP/SSE is a #3988 follow-up)"
LABEL org.opencontainers.image.licenses="Apache-2.0"
USER nonroot:nonroot
COPY --from=build /out/openova-mcp /usr/local/bin/openova-mcp
# stdio server — no port today. The consuming agent/orchestrator wires
# stdin/stdout to the openova-mcp process; the OPENOVA_MCP_* env contract
# (see README.md) supplies the catalyst-api URL, verify mode + key, context.
ENTRYPOINT ["/usr/local/bin/openova-mcp"]
