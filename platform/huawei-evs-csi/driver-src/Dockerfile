# OpenOva-patched huaweicloud-csi-driver EVS plugin (#3971).
#
# Builds the EVS CSI plugin from the in-repo fork of
# github.com/huaweicloud/huaweicloud-csi-driver carrying the `idc` no-Keystone
# AK/SK bypass (pkg/config/cloud.go). The same binary runs BOTH the controller
# Deployment and the node DaemonSet:
#   - controller: only needs the binary + CA certs (issues signed EVS API calls).
#   - node:       additionally shells out to mkfs.ext4 / mkfs.xfs / mount /
#                 blkid / findmnt / xfs_repair (k8s.io/mount-utils
#                 SafeFormatAndMount) to format + mount the attached EVS block
#                 device into the Pod.
# So the runtime base ships e2fsprogs + xfsprogs + util-linux + the CA bundle.
#
# Multi-stage, fully static binary (CGO disabled). The Go base is pinned by digest,
# and its Go is not older than the go line of go.mod (scripts/check-go-base-image.py).

# ---- build stage ----
FROM golang:1.25.14-bookworm@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437 AS build

WORKDIR /src

# Module graph first for layer caching.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static build — the same flags the upstream Makefile uses
# (CGO_ENABLED=0 GOOS=linux), stripped.
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-s -w' \
    -o /out/evs-csi-plugin ./cmd/evs-csi-plugin

# ---- runtime stage ----
FROM debian:bookworm-slim

LABEL org.opencontainers.image.title="openova-evs-csi-plugin"
LABEL org.opencontainers.image.description="OpenOva-patched Huawei EVS CSI plugin — idc no-Keystone AK/SK bypass (#3971)"
LABEL org.opencontainers.image.source="https://github.com/openova-io/openova"

# Node-plugin filesystem tooling + CA certs. --no-install-recommends keeps the
# image lean; rm the apt lists so the layer stays small.
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
        ca-certificates \
        e2fsprogs \
        xfsprogs \
        util-linux \
        mount \
 && rm -rf /var/lib/apt/lists/*

COPY --from=build /out/evs-csi-plugin /bin/evs-csi-plugin

ENTRYPOINT ["/bin/evs-csi-plugin"]
