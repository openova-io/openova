# OpenClaw runtime — per-user pod image.
#
# Per locked decision [A] of #795, this image reads ONLY two env vars
# (NEWAPI_BASE_URL + NEWAPI_KEY). It is identity-blind: no Keycloak
# code, no key-management code, no SME-tenant model knowledge.
#
# Contract:
#   - HTTP server on $PORT (default 8080)
#   - GET /healthz   → 200 if NEWAPI_BASE_URL+NEWAPI_KEY are present
#   - GET /readyz    → 200 once the upstream NewAPI base URL responds
#   - POST /v1/chat/completions  → forwards to ${NEWAPI_BASE_URL}/v1/chat/completions
#                                   with `Authorization: Bearer ${NEWAPI_KEY}`
#   - GET /          → minimal HTML landing for browser confirm-loop
#
# This is a contract-minimal stub. Operators may override
# perUserPod.image.repository in the chart values to any image that
# satisfies the same two-env-var contract — e.g. a coding-CLI fork,
# the upstream OpenClaw, etc.

FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod main.go ./
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /openclaw-runtime .

FROM scratch
LABEL org.opencontainers.image.source="https://github.com/openova-io/openova"
LABEL org.opencontainers.image.description="OpenClaw per-user runtime — identity-blind NewAPI proxy"
LABEL org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /openclaw-runtime /openclaw-runtime
USER 1001:1001
EXPOSE 8080
ENTRYPOINT ["/openclaw-runtime"]
