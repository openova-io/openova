# syntax=docker/dockerfile:1.7
#
# openova-sandbox-mcp — stdio MCP server, one sidecar per Sandbox pod.
# Talks JSON-RPC to the agent (claude / cursor-agent / qwen-code /
# aider / opencode) over stdin/stdout. See architecture.md §3.

FROM golang:1.23-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -trimpath -ldflags="-s -w" \
    -o /out/openova-sandbox-mcp ./cmd/openova-sandbox-mcp

FROM gcr.io/distroless/static-debian12:nonroot
USER nonroot:nonroot
COPY --from=build /out/openova-sandbox-mcp /usr/local/bin/openova-sandbox-mcp
# stdio server — no port. The orchestrator wires stdin/stdout to the
# agent process.
ENTRYPOINT ["/usr/local/bin/openova-sandbox-mcp"]
