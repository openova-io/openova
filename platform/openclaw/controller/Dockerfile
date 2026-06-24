# OpenClaw workspace controller — multi-tenant front-door image.
#
# This is the process bp-openclaw's chart was scaffolded around but never
# had: it authenticates the Organization end-user against the per-tenant
# Keycloak realm (OIDC discovery + JWKS RS256 validation), spawns one
# identity-blind runtime pod per active session from the mounted
# pod-template ConfigMap, reverse-proxies the user to that pod, and reaps
# idle pods. See controller/main.go for the full contract.
#
# Stdlib-only Go binary (no external deps) — the build is hermetic and
# needs no `go mod download`.
#
# Contract (all env-driven, Inviolable Principle 4):
#   - HTTP server on $PORT (default 8080)
#   - GET /healthz   → 200 once config parsed (liveness)
#   - GET /readyz    → 200 once api-server + OIDC JWKS reachable
#   - GET /metrics   → Prometheus counters
#   - ANY /*         → authenticate (Bearer JWT) → ensure pod → proxy
#
# Output: ghcr.io/openova-io/openova/openclaw-controller:<sha>

FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /openclaw-controller .

FROM gcr.io/distroless/static:nonroot
LABEL org.opencontainers.image.source="https://github.com/openova-io/openova"
LABEL org.opencontainers.image.description="OpenClaw workspace controller — per-tenant Keycloak OIDC auth + per-user runtime pod spawn/proxy/reap"
LABEL org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /openclaw-controller /openclaw-controller
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/openclaw-controller"]
