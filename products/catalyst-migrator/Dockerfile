# catalyst-migrator — minimal runtime image for one-shot Catalyst schema
# migration Jobs (currently: G117 W2.C2 backfill of spec.instanceId /
# spec.isolationLevel / spec.namingTemplate on legacy Application CRs).
#
# Why a dedicated image?
#   The bp-catalyst-platform chart's `migrate-applications-instanceId-job.yaml`
#   delivers the script via ConfigMap and mounts it at /opt/migrations/.
#   The Pod just needs (a) a working python3 stdlib and (b) the kubectl
#   client binary so the script can shell out for CR patches. The Catalyst
#   controllers' own images are Distroless-static — no python, no kubectl —
#   so they cannot host this Job.
#
# Image surface (kept intentionally small):
#   - Alpine base (smaller than python:3.x-slim, no dpkg dance for kubectl)
#   - python3 from Alpine's apk (pure stdlib only — the script uses
#     `subprocess`, `json`, `argparse`, `sys`, no pip deps)
#   - kubectl pinned to v1.31.4 (matches the cluster API server floor
#     that bp-cilium ships in our supported Sovereigns 2026-06)
#   - runs as UID 65532 (nonroot), readOnlyRootFilesystem-compatible
#     (the script writes only to stdout)
#
# Refs #2830 — script + Job are shipped by bp-catalyst-platform 1.4.438;
# this image is the missing GHCR artifact that the Job pulls.

FROM alpine:3.20

ARG KUBECTL_VERSION=v1.31.4
ARG TARGETOS=linux
ARG TARGETARCH=amd64

# Tini for proper PID-1 signal handling on Job termination.
# ca-certificates so kubectl can talk TLS to the in-cluster API server
# (the projected serviceaccount-token CA bundle is layered at runtime).
RUN apk add --no-cache \
        ca-certificates \
        python3 \
        tini \
        curl \
    && curl -fsSLo /usr/local/bin/kubectl \
        "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/${TARGETOS}/${TARGETARCH}/kubectl" \
    && chmod 0755 /usr/local/bin/kubectl \
    && apk del curl \
    && rm -rf /var/cache/apk/* \
    && kubectl version --client=true

# Non-root user matching the Job Pod spec
# (runAsUser: 65532 / runAsNonRoot: true).
RUN addgroup -S -g 65532 catalyst \
    && adduser -S -u 65532 -G catalyst -h /home/catalyst catalyst

USER 65532:65532
WORKDIR /home/catalyst

# Default ENTRYPOINT runs through tini so SIGTERM from kubelet reaches
# the python process cleanly during eviction. The actual command is
# supplied by the bp-catalyst-platform Job template
# (/usr/bin/python3 /opt/migrations/migrate-applications-instanceId.py).
ENTRYPOINT ["/sbin/tini", "--"]
CMD ["/usr/bin/python3", "--version"]

LABEL org.opencontainers.image.source="https://github.com/openova-io/openova"
LABEL org.opencontainers.image.title="catalyst-migrator"
LABEL org.opencontainers.image.description="Runtime image for one-shot Catalyst CR schema migrations (python3 + kubectl)."
LABEL org.opencontainers.image.licenses="Apache-2.0"
