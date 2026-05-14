# openova-flow-server — stateless HTTP+SSE event router.
#
# Per docs/INVIOLABLE-PRINCIPLES.md #4a images are built by GitHub
# Actions and pulled through harbor.openova.io / per-Sovereign Harbor.
# This Dockerfile is the CI build recipe — never `docker build`d locally
# for shipment.
FROM golang:1.22-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY test ./test
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -o /openova-flow-server ./cmd/openova-flow-server

FROM scratch
LABEL org.opencontainers.image.source="https://github.com/openova-io/openova"
LABEL org.opencontainers.image.description="OpenovaFlow event router — HTTP ingest + SSE replay"
LABEL org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /openova-flow-server /openova-flow-server
USER 1001:1001
EXPOSE 8080
ENTRYPOINT ["/openova-flow-server"]
