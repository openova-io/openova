# #3971 — Huawei HCS (kom4dc) EVS / everest CSI: grounded follow-up plan

**Status:** Hetzner path SHIPPED in this PR (bp-hcloud-csi bootstrap-kit slot
55a + default-class flip + the provider-agnostic Phase-1 DoD gate). The Huawei
EVS CSI path is **deliberately deferred** — the exact manifests/version/
provisioner string for a **bare k3s** cluster on **HCS** (Huawei Cloud Stack,
NOT public CCE) are not in-repo and not verifiable from here without a live
kom4dc cluster to probe. This doc is the precise, grounded plan so the next
session lands it without guessing.

Per the #3971 instruction: *"If the Huawei everest CSI install is genuinely
uncertain (manifests/version), implement the Hetzner hcloud-csi path fully +
the default-SC flip + the DoD gate, and write a precise, grounded plan for the
Huawei everest CSI (exact manifests/creds/slot) rather than guessing."*

## Why the Hetzner gate already protects Huawei

The Phase-1 DoD gate (`catalyst-api markPhase1Done`, #3971) is
**provider-agnostic**: it probes the new Sovereign's default StorageClass and
FAILS the prov if it is `rancher.io/local-path`. So a Huawei Sovereign that
comes up before its EVS CSI lands will be honestly marked `failed` (not
silently `ready` on ephemeral disk). The gate does NOT need a Huawei-specific
change — it already catches the Huawei case. What's missing is the Huawei CSI
itself so the gate goes GREEN there.

## What's available (grounded in the repo today)

- **Creds** — `flux-system/cloud-credentials` Secret already carries the
  Huawei AK/SK on every HCS Sovereign (`infra/providers/huawei/main.tf`
  `cloud_credentials_secret_yaml_huawei`, lines ~669-681):
  - `huawei-ak` = `var.huawei_access_key`
  - `huawei-sk` = `var.huawei_secret_key`
  - `huawei-region` = `var.huawei_region`
  - `huawei-project-id` — **NOTE a pre-existing bug**: line 679 sets it to
    `${var.huawei_region}`, not `${var.huawei_project_id}`. The EVS CSI needs
    the real project-id, so fix this line as part of the Huawei CSI PR (out of
    scope for THIS #3971 Hetzner PR; flagged here so it isn't missed).
- **Provider-keyed suspend gate** — the same `HUAWEI_HR_SUSPEND` /
  `HCLOUD_HR_SUSPEND` envsubst pair that gates bp-velero-hcs (slot 34a) and
  bp-hcloud-ccm (slot 55). The Huawei cloud-init sets `HUAWEI_HR_SUSPEND=false`
  + `HCLOUD_HR_SUSPEND=true`; the Hetzner cloud-init the inverse. So a Huawei
  CSI slot uses `suspend: ${HUAWEI_HR_SUSPEND:=true}` (default-suspended → a
  Hetzner Sovereign never installs it).
- **Object storage endpoint** — HCS OBS is reachable at
  `https://obs.<region>.kom4dc.nationalcloud.om` (main.tf line 887), proving
  the HCS API surface naming pattern for this private cloud.

## The plan (next session, separate PR — Refs #3971)

### 1. Vendor the everest EVS CSI as a Catalyst Blueprint chart

Create `platform/huawei-evs-csi/` mirroring `platform/hcloud-csi/`:
- `chart/Chart.yaml` — umbrella `bp-huawei-evs-csi`. The upstream is Huawei's
  **everest-csi-driver** (EVS block storage). On HCS/CCE this is normally
  delivered as the `everest` add-on; for a **bare k3s** cluster it must be
  installed as raw manifests (the CCE add-on assumes CCE's metadata service).
  The driver image + sidecars (`external-provisioner`, `external-attacher`,
  `external-resizer`, `node-driver-registrar`, `livenessprobe`) come from
  Huawei SWR (`swr.<region>.myhuaweicloud.com/everest/...` or the kom4dc SWR
  mirror). **VERIFY the exact image tags against a live kom4dc node's
  containerd / a Huawei HCS everest release before pinning** — do not guess the
  tag.
- `chart/templates/storageclass.yaml` — StorageClass `evs-ssd`:
  - `provisioner: disk.csi.everest.io` (the everest EVS provisioner string —
    **CONFIRM** against the live driver's CSIDriver object; some HCS builds use
    `everest-csi-provisioner`).
  - `parameters.type: SSD` (or `SAS`/`GPSSD` per the kom4dc EVS catalogue).
  - `reclaimPolicy: Retain`, `volumeBindingMode: WaitForFirstConsumer`,
    `allowVolumeExpansion: true` — same durability posture as hcloud-volumes.
  - The default-class flip hook is REUSABLE verbatim from bp-hcloud-csi
    (`default-class-hook.yaml` is provisioner-agnostic — it only needs the
    target SC name); copy it and set `DEFAULT_SC=evs-ssd`.
- `chart/templates/cloud-credentials.yaml` — render the everest cloud-config
  Secret (`cloud-config` in kube-system) from `.Values.huaweiAK/SK/projectId/
  region` populated via Flux `valuesFrom` against `cloud-credentials` (keys
  `huawei-ak`/`huawei-sk`/`huawei-project-id`/`huawei-region`). everest reads
  AK/SK + project + region to call the EVS OpenAPI.
- `chart/templates/volumesnapshotclass.yaml` — `evs-snapshots`
  (`driver: disk.csi.everest.io`) — EVS supports snapshots.

### 2. Bootstrap-kit slot `55b-bp-huawei-evs-csi.yaml`

Mirror `55a-bp-hcloud-csi.yaml` exactly, but:
- `suspend: ${HUAWEI_HR_SUSPEND:=true}` (default-suspended; Huawei cloud-init
  flips it false; Hetzner never installs it).
- `values: { enabled: true, defaultStorageClass: true }`.
- `valuesFrom` four entries pulling `huawei-ak`/`huawei-sk`/`huawei-project-id`/
  `huawei-region` from `cloud-credentials` into the chart's value paths.
- Register it in `kustomization.yaml` resources[] **and**
  `scripts/expected-bootstrap-deps.yaml` (slot 55b, name `bp-huawei-evs-csi`,
  `depends_on: []`, `wave: present`) — the drift guard requires both.

### 3. Validate

- `helm lint` + `helm template` the new chart (default-off renders zero;
  enabled renders SC + driver + hook).
- `kubectl kustomize clusters/_template/bootstrap-kit` green; `bash
  scripts/check-bootstrap-deps.sh` 0 drift.
- `scripts/lint-cloudinit.sh both` (no cloud-init change needed — the
  HUAWEI_HR_SUSPEND substitute already exists).
- The real proof is a **fresh zero-touch kom4dc prov**: `kubectl get sc` shows
  `evs-ssd` default + local-path non-default, every stateful PVC Bound to an
  EVS volume (verify the backing volume exists via the Huawei EVS OpenAPI), and
  the Phase-1 DoD gate goes GREEN (no `default-storageclass-ephemeral` fail).

## Open verification items (must confirm on a live kom4dc before pinning)

1. Exact everest driver + sidecar **image tags** (SWR registry path + version).
2. The CSIDriver **provisioner string** (`disk.csi.everest.io` vs
   `everest-csi-provisioner`).
3. The EVS **volume type** parameter values the kom4dc EVS catalogue exposes
   (`SSD`/`SAS`/`GPSSD`/`ESSD`).
4. Whether HCS everest on **bare k3s** needs the CCE metadata shim or runs with
   AK/SK cloud-config alone (the CCE add-on path assumes CCE).
5. Fix the `huawei-project-id` cloud-credentials bug (main.tf line 679).
