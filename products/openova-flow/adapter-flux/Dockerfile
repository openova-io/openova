# openova-flow-adapter-flux — DaemonSet sidecar that watches Flux
# HelmRelease CRs and POSTs FlowMessage envelopes to openova-flow-server.
#
# Per docs/INVIOLABLE-PRINCIPLES.md #4a images are built by GitHub
# Actions and pulled through the per-Sovereign Harbor proxy. Never
# `docker build`d locally for shipment.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY test ./test
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /openova-flow-adapter-flux ./cmd/openova-flow-adapter-flux

FROM scratch
LABEL org.opencontainers.image.source="https://github.com/openova-io/openova"
LABEL org.opencontainers.image.description="OpenovaFlow Flux adapter — HelmRelease informer to FlowMessage emitter"
LABEL org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /openova-flow-adapter-flux /openova-flow-adapter-flux
USER 1001:1001
EXPOSE 8081
ENTRYPOINT ["/openova-flow-adapter-flux"]
