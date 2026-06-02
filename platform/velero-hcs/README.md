# Velero (HCS)

HCS-native variant of the Catalyst Velero Blueprint — Kubernetes backup +
disaster recovery for Huawei Cloud Sovereigns. Backups land DIRECTLY in
Huawei OBS via its S3-compatible endpoint per
[`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) §3.5 + ADR-0001 §13's
S3-aware-app rule.

**Status:** Accepted | **Updated:** 2026-06-03 | **Refs:** #2847 #2846

---

## Why a separate `bp-velero-hcs`?

PR #2846 SUSPENDED the Hetzner-flavored `bp-velero` HelmRelease on every
HCS Sovereign because the Hetzner chart depended on the Hetzner Object
Storage endpoint shape + the `hcloud-storage` plugin lineage. The result
left HCS Sovereigns with NO backup engine — a Pillar-3 / DoD gap.

This Blueprint closes that gap with a values-overlay sibling chart:
- Same upstream chart pin (`vmware-tanzu/velero` 12.0.1 / appVersion
  1.18.0) as `bp-velero`.
- Same `velero-plugin-for-aws` (S3-compatible plugin — Huawei OBS speaks
  the S3 protocol).
- Same `flux-system/object-storage` Secret seam (issue #371, vendor-
  agnostic since #425) — the Huawei cloud-init at
  `infra/providers/huawei/cloudinit-control-plane.tftpl` already writes
  the canonical 5 keys (`s3-endpoint` / `s3-region` / `s3-bucket` /
  `s3-access-key` / `s3-secret-key`) from `var.obs_*`, so the bootstrap
  -kit `valuesFrom` shape is byte-identical aside from the chart name.

---

## Slot wiring

- Bootstrap-kit slot: `clusters/_template/bootstrap-kit/34a-bp-velero-hcs.yaml`.
- `spec.suspend: ${HUAWEI_HR_SUSPEND:=true}` — defaults to suspended
  (so a fresh Hetzner Sovereign never installs this chart). The Huawei
  cloud-init substitutes `HUAWEI_HR_SUSPEND="false"` at bootstrap-kit
  Kustomization apply time; the Hetzner cloud-init leaves the default
  (true), inverting the existing `HCLOUD_HR_SUSPEND` pattern (#2447).
- Pair semantics: at most ONE of `34-velero.yaml` (Hetzner) and
  `34a-bp-velero-hcs.yaml` (HCS) renders per Sovereign — the other is
  suspended by its provider-keyed env-substitute.

---

## OBS endpoint shape

Huawei OBS exposes an S3-compatible REST API at
`https://obs.<region>.myhuaweicloud.com`. The bootstrap-kit slot pulls
the per-Sovereign endpoint from `flux-system/object-storage` key
`s3-endpoint` (seeded by `var.obs_endpoint`), the region from
`s3-region` (`var.obs_region`), and the bucket name from `s3-bucket`
(`var.obs_bucket_name`). The `velero-plugin-for-aws` then talks to OBS
exactly as if it were AWS S3 (path-style URLs).

See [`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) §3.5 for the
Catalyst-wide backup-engine architecture and
[`docs/RUNBOOKS.md`](../../docs/RUNBOOKS.md) for restore procedures
(identical between bp-velero and bp-velero-hcs — the CLI surface is the
upstream Velero binary in both cases).
