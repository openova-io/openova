# huaweicloud-csi-driver EVS plugin — OpenOva fork (`idc` no-Keystone bypass)

This is a **pruned, patched fork** of
[`github.com/huaweicloud/huaweicloud-csi-driver`](https://github.com/huaweicloud/huaweicloud-csi-driver)
(Apache-2.0, see `LICENSE`). Only the **EVS** plugin is kept; the OBS / SFS /
SFS-Turbo drivers, the vendored deps, the upstream CI, and the example/deploy
manifests were removed (the Catalyst Helm chart in `../chart/` owns deployment).

## Why the fork exists (#3971)

The off-the-shelf driver authenticates by fetching the **Keystone v3 service
catalog** at startup (`pkg/config/cloud.go` → `openstack.Authenticate` →
`v3AKSKAuth` → `GET /v3/auth/catalog`, plus a `getProjectID`/`getDomainID`
token call when project-name resolution is needed).

On **HCS me-east-215 (kom4dc)** the IAM APIGW publishes **none** of the Keystone
v3 catalog/token endpoints — every `/v3/auth/*` path returns
`APIGW.0101 "API does not exist"`. So the stock controller **CrashLoops** at
startup before it ever issues an EVS call — even though the EVS **data API**
works fine with AK/SK request signing (OpenTofu's `huaweicloud` provider
provisions EVS disks the same way, and the catalyst-api Huawei adapter signs
ECS/VPC/EVS calls with the identical scheme).

## The patch — `pkg/config/cloud.go`

The driver already declared an `idc` (no-internet-data-center) flag in its
`[Global]` cloud-config struct, but it was **declared-but-unused**. This fork
wires it up:

* When `idc=true`, `newCloudClient()` **skips `openstack.Authenticate` entirely**
  (no catalog fetch, no token call) and instead populates the golangsdk
  `ProviderClient`'s AK/SK signing options + `ProjectID` **directly**.
* The golangsdk **per-request signer** engages purely on
  `AKSKAuthOptions.AccessKey != ""` (`provider_client.go` `doRequest`), so every
  EVS / ECS request is signed with **SDK-HMAC-SHA256** AK/SK signing.
* `newServiceClient()` derives every service endpoint locally as
  `https://<svc>.<region>.<cloud>/` — it never consults the Keystone catalog —
  so no catalog lookup is needed for normal operation.
* A **non-empty `project-id` is required** in `idc` mode (with no Keystone we
  cannot resolve project-name → project-id; every EVS path is
  `/<version>/<project-id>/cloudvolumes/...`).
* **Public Huawei Cloud is untouched**: with `idc` unset/false the stock
  Keystone flow runs exactly as before.

The change is a single gated branch in `newCloudClient()`; no other source file
is modified. Regression-guarded by `pkg/config/cloud_idc_test.go`.

## Live proof (hw174, 2026-06-21)

The patched controller ran **6/6 / 0-restart** (no CrashLoop), the driver log
showed real signed calls succeeding —
`Successfully published volume ... to EVS ...  Got device path: /dev/vdb`
against `https://ecs.me-east-215.kom4dc.nationalcloud.om/v1/<real-project-id>/...`
with **zero** `APIGW.0101` — the `evs-ssd` default StorageClass +
`evs.csi.huaweicloud.com` CSIDriver registered, and the 12 Pending stateful PVCs
(incl. `data-mgmt/rtz/dmz-vcluster-0`) **bound to real EVS disks**, unblocking
the vCluster StatefulSets and the `bp-catalyst-platform` spine.

## Build

`Dockerfile` builds the static EVS plugin (Go, CGO off) on a `debian:bookworm-slim`
runtime carrying the node-plugin filesystem tooling (`e2fsprogs`, `xfsprogs`,
`util-linux`, CA certs). CI: `.github/workflows/build-bp-huawei-evs-csi.yaml`
runs the unit tests and pushes
`ghcr.io/openova-io/openova/evs-csi-plugin:<tag>`.
