# openova-flow-server — stateless HTTP+SSE event router.
#
# Per docs/INVIOLABLE-PRINCIPLES.md #4a images are built by GitHub
# Actions and pulled through harbor.openova.io / per-Sovereign Harbor.
# This Dockerfile is the CI build recipe — never `docker build`d locally
# for shipment.
#
# The builder Go comes from go.mod (#7922). The golang images set
# GOTOOLCHAIN=local, so a builder older than go.mod's `go` line stops at
# `go mod download`. CI passes GO_VERSION, the <major>.<minor> that
# `bash scripts/check-go-builder-version.sh --builder-version go.mod` prints,
# and checks the result before it builds. The default `1` is the newest Go 1
# release, so a plain `docker build` is never older than go.mod either.
ARG GO_VERSION=1
FROM golang:${GO_VERSION}-alpine AS build
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
