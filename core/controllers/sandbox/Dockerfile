# sandbox-controller — Wave 1 of the Sandbox product.
#
# A Catalyst-built Go binary that reconciles Sandbox.sandbox.openova.io/v1
# CRs into per-Sandbox namespace + RBAC + PVCs + placeholder Secret
# manifests written to the per-Org `catalyst-tenant` Gitea repo. Flux on
# the host cluster picks up the manifests and reconciles them into the
# Org vcluster (sister of organization-controller — same patterns).
#
# Build context: invoked with the repository ROOT as the build context.
# Mirrors core/controllers/organization/Containerfile (slice CC1 layout:
# shared go.mod at core/controllers/, shared pkg at core/controllers/pkg).
#
# Two stages:
#   build  — golang:1.23-alpine
#   final  — alpine:3.20 minimal runtime (CA certs + the binary)

FROM docker.io/library/golang:1.23-alpine AS build
WORKDIR /workspace

# Stage 1: cache module downloads — go.mod/go.sum at the shared root.
COPY core/controllers/go.mod core/controllers/go.sum core/controllers/
WORKDIR /workspace/core/controllers
RUN go mod download

# Stage 2: copy source + build. Same layout the organization-controller
# Containerfile uses (Fix #42 follow-up — shared internal + pkg dirs
# MUST be copied before the per-controller dir, else `go build` fails
# resolving the github.com/openova-io/openova/core/controllers/pkg/gitea
# import.
WORKDIR /workspace
COPY core/controllers/internal /workspace/core/controllers/internal
COPY core/controllers/pkg /workspace/core/controllers/pkg
COPY core/controllers/sandbox /workspace/core/controllers/sandbox

WORKDIR /workspace/core/controllers/sandbox
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /sandbox-controller ./cmd/sandbox-controller

# Stage 3: minimal runtime.
FROM docker.io/library/alpine:3.20
RUN apk add --no-cache ca-certificates tzdata

COPY --from=build /sandbox-controller /sandbox-controller

# Alpine 3.20 already ships UID 65534 as `nobody`. The numeric form
# satisfies runAsNonRoot=true + runAsUser=65534 in the chart's
# Deployment.
USER 65534:65534

EXPOSE 8080 8081

ENTRYPOINT ["/sandbox-controller"]
