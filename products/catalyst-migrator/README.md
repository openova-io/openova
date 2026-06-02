# catalyst-migrator

Minimal runtime image for one-shot Catalyst schema-migration `Job`s.

## What it is

A small Alpine-based image bundling `python3` (stdlib only) + `kubectl`.
It exists so the bp-catalyst-platform chart's Helm-hook migration `Job`s
have a known, signed runtime to execute against — the Catalyst control-plane
images themselves are Distroless-static and cannot host python.

| Field | Value |
|------:|:------|
| Image | `ghcr.io/openova-io/catalyst-migrator` |
| Tag pattern | `0.1.0`, `<short-sha>`, `latest` |
| Base | `alpine:3.20` |
| Platforms | `linux/amd64`, `linux/arm64` |
| Signing | Cosign keyless (Sigstore) |
| User | UID/GID `65532` (nonroot) |

## What it runs

Currently exactly one consumer:

- bp-catalyst-platform -> `templates/migrate-applications-instanceId-job.yaml`
  -> runs `/usr/bin/python3 /opt/migrations/migrate-applications-instanceId.py`
  (script mounted via ConfigMap; see `products/catalyst/chart/files/`).

The Job is the G117 W2.C2 backfill that adds `spec.instanceId`,
`spec.isolationLevel`, and `spec.namingTemplate` to legacy `Application` CRs.

## How it ships

- Source: this directory (`products/catalyst-migrator/Dockerfile`).
- CI: `.github/workflows/build-catalyst-migrator.yaml`. Triggers on any
  push to `products/catalyst-migrator/**` or
  `products/catalyst/chart/files/migrate-*.py`. Multi-arch build,
  publishes `:0.1.0` + `:<short-sha>` + `:latest`, cosign-signed.
- Registry: `ghcr.io/openova-io/catalyst-migrator:<tag>`. Public read.

## How to use it (operators)

The chart defaults `migrations.applicationsInstanceId.enabled=false`
because the image was historically absent (Refs #2830). With the image
now published, operators with legacy Application CRs can opt in via
per-Sovereign Helm overlay:

```yaml
migrations:
  applicationsInstanceId:
    enabled: true
    dryRun: false  # first pass with true to log planned patches
```

Refs #2830 #2823.
