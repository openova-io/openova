# sandbox-bridge sidecar image — Catalyst-side bridge handler for
# POST /admin/tokens/sandbox (Wave 5.57, Refs #2303).
#
# Multi-stage Go build → distroless static base.
# Built by .github/workflows/build-bp-newapi-sandbox-bridge.yaml on
# every push touching platform/newapi/internal/handler/**.

FROM golang:1.22-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/sandbox-bridge ./cmd/sandbox-bridge

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/sandbox-bridge /sandbox-bridge
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/sandbox-bridge"]
