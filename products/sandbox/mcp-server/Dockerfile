# syntax=docker/dockerfile:1.7
#
# openova-sandbox-mcp — stdio MCP server, one sidecar per Sandbox pod.
# Talks JSON-RPC to the agent (claude / cursor-agent / qwen-code /
# aider / opencode) over stdin/stdout. See architecture.md §3.
#
# Build context: invoked with the repository ROOT as the build context.
# Wave-8 / PR #1658 added `replace` directives in this module's go.mod
# pointing at ../../../core/controllers + ../../../core/services/shared
# (re-using the canonical Gitea client + auth.Claims shape per Slice CC1
# + the rest of the repo). That means the build context MUST cover the
# repo root so the Dockerfile can COPY the replace targets into place,
# mirroring core/controllers/sandbox/Dockerfile (same Slice-CC1 layout).
#
# Two stages:
#   build  — golang:1.25-alpine (must satisfy go.mod `go 1.25.0`)
#   final  — distroless/static-debian12:nonroot (scratch-equivalent)

FROM docker.io/library/golang:1.25-alpine AS build
WORKDIR /repo
RUN apk add --no-cache git

# Stage 1: pre-stage every go.mod/go.sum the build will need so
# `go mod download` can resolve the replace directives without first
# copying every source file. Order matters: copy the replace targets'
# module roots before the dependent module so the resolver sees the
# replacement modules at the paths the go.mod points at.
COPY core/controllers/go.mod core/controllers/go.sum /repo/core/controllers/
COPY core/services/shared/go.mod core/services/shared/go.sum /repo/core/services/shared/
COPY products/sandbox/mcp-server/go.mod products/sandbox/mcp-server/go.sum /repo/products/sandbox/mcp-server/

# Stage 2: copy source for the dependent modules (the replace targets
# must be on disk in full for `go build` to compile the imported pkgs).
COPY core/controllers /repo/core/controllers
COPY core/services/shared /repo/core/services/shared
COPY products/sandbox/mcp-server /repo/products/sandbox/mcp-server

WORKDIR /repo/products/sandbox/mcp-server
RUN go mod download
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /out/openova-sandbox-mcp ./cmd/openova-sandbox-mcp

FROM gcr.io/distroless/static-debian12:nonroot
USER nonroot:nonroot
COPY --from=build /out/openova-sandbox-mcp /usr/local/bin/openova-sandbox-mcp
# stdio server — no port. The orchestrator wires stdin/stdout to the
# agent process.
ENTRYPOINT ["/usr/local/bin/openova-sandbox-mcp"]
