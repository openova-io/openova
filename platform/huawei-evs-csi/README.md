# bp-huawei-evs-csi

**Status**: Bootstrap-kit slot 55b (#3971 follow-up) — installed on every fresh
**Huawei (HCS / kom4dc)** Sovereign as the cluster-default StorageClass.
**Updated**: 2026-06-21

The Huawei sibling of [`bp-hcloud-csi`](../hcloud-csi/README.md). Vendors the
**open-source `huaweicloud-csi-driver` EVS block-storage plugin**
(provisioner `evs.csi.huaweicloud.com`) as raw manifests and ships the durable
`evs-ssd` StorageClass that backs ALL stateful workloads on Huawei Sovereigns
(CNPG primary/replica pairs, openbao, keycloak, gitea, harbor, the 3 vCluster
`data-*` PVCs) — durable EVS block volumes instead of ephemeral node-local
disk.

> **⚠️ LIVE-PROVEN STATUS (hw174, 2026-06-21) — deploys clean, but the
> off-the-shelf driver CANNOT authenticate to HCS me-east-215.** Applied to the
> live wedged hw174 cluster, the chart deployed cleanly: the `evs-ssd`
> StorageClass + the `evs.csi.huaweicloud.com` CSIDriver registered, the
> default-class flip hook ran (`evs-ssd (default)`), and the default
> propagated to 9/12 stateful PVCs. BUT the driver controller CrashLoops at
> startup: its golangsdk does an upfront Keystone auth
> (`GET /v3/auth/catalog`, `POST /v3/auth/tokens`) and **HCS me-east-215's IAM
> APIGW publishes NEITHER endpoint** — every Keystone v3 path returns
> `APIGW.0101 "API does not exist"` (probed exhaustively across the iam /
> iam-apigateway-proxy / iam-cache-proxy hosts). The `idc` cloud-config field
> that would bypass the catalog fetch is **declared-but-unused** in the driver
> source (verified against upstream master). The EVS data API itself works
> (the tofu provider provisions EVS system disks with the same AK/SK via direct
> request signing). **So slot 55b ships DEFAULT-OFF (`EVS_CSI_ENABLED=false`)**
> — the HR installs empty + Ready (no convergence regression), and a Huawei
> prov honestly has no default class (the Phase-1 storage-durability gate
> FAILS it; never silent local-path). The remaining work is an **auth fix**:
> patch the driver to sign EVS requests with AK/SK directly + skip the catalog
> fetch, OR discover the real HCS IAM token endpoint. Flip `EVS_CSI_ENABLED`
> true the moment that lands. See #3971.

> **The Huawei half of the P0 blocker it closes (#3971):** #3992 forbade k3s
> `local-path` on ALL providers (`--disable=local-storage` + Kyverno K23 deny)
> and wired the stateful root slots to `dependsOn: bp-hcloud-csi`. But
> `bp-hcloud-csi` renders **empty** on Huawei (it's the *Hetzner* Cloud CSI),
> so a Huawei prov came up with **no StorageClass at all**: every stateful PVC
> sat Pending → the vCluster StatefulSets never scheduled → the
> `vc-mgmt/rtz/dmz` kubeconfig secrets were never created → every in-vCluster
> app failed `could not get KubeConfig secret 'mgmt/vc-mgmt'` → the whole
> `bp-catalyst-platform` spine froze at ~40/50 HRs. This slot installs the
> missing CSI so those PVCs bind and the spine resumes.

## Why the open-source `huaweicloud-csi-driver`, not the everest add-on

The substrate (kom4dc) is **Huawei Cloud Stack (HCS)** — an on-prem,
OpenStack-derived private cloud, endpoints `*.me-east-215.kom4dc.nationalcloud.om`,
insecure TLS, authenticated by **AK/SK** (not Keystone username/password).

| | everest (`disk.csi.everest.io`) | **huaweicloud-csi-driver** (`evs.csi.huaweicloud.com`) |
|---|---|---|
| Source / manifests | closed-source CCE add-on, none public | **public, vendorable** |
| Auth | CCE agency / IAM pod-identity (control-plane bound) | **portable AK/SK INI `cloud-config`** |
| Bare k3s | not supported / infeasible | **designed for self-managed clusters** |
| Node identity | CCE metadata shim | **OpenStack metadata service** (no CCM) |

- everest is a closed-source CCE add-on; it has no public manifests and can't
  authenticate with a portable AK/SK cloud-config, so it **cannot run on bare
  k3s**.
- The open-source driver authenticates with the **AK/SK INI cloud-config** we
  already have (`flux-system/cloud-credentials` carries
  `huawei-ak`/`huawei-sk`/`huawei-project-id`/`huawei-region`) and
  self-discovers each node's ECS server-id from the **OpenStack metadata
  service** — so it needs **no cloud-controller-manager** (bare k3s nodes carry
  `providerID=k3s://<name>`; the driver's `--nodeid` flag is deprecated and
  ignored).
- `cinder-csi-plugin` was rejected — it needs Keystone username/password/domain,
  which HCS does not expose to us.

These were confirmed on the **live hw174** cluster: the OpenStack metadata
service at `http://169.254.169.254/openstack/latest/meta_data.json` returns the
instance `uuid` + `availability_zone me-east-215a`, and the EVS API answers at
`https://evs.me-east-215.kom4dc.nationalcloud.om/v3/<project>/types` (IAM-token
gated). Driver facts (cloud-config struct, metadata URL, the flat `type` SC
parameter, the `evs.csi.huaweicloud.com` driver name) were verified directly
from the driver source (`pkg/config/cloud.go`, `pkg/utils/metadatas/metadata.go`,
`pkg/evs/driver.go`,`nodeserver.go`) and the upstream
`deploy/evs-csi-plugin/kubernetes` manifests.

## What it ships

| Template | Effect |
|---|---|
| `csidriver.yaml` | CSIDriver `evs.csi.huaweicloud.com` (attachRequired, podInfoOnMount). |
| `controller-deployment.yaml` | EVS controller (provisioner/attacher/resizer/snapshotter sidecars + `evs-csi-plugin`). |
| `node-daemonset.yaml` | EVS node plugin DaemonSet + node-driver-registrar (k3s `/var/lib/kubelet` paths, not CCE's). |
| `storageclass.yaml` | StorageClass `evs-ssd` — `provisioner: evs.csi.huaweicloud.com`, `parameters.type: SSD`, `reclaimPolicy: Retain`, `WaitForFirstConsumer`, `allowVolumeExpansion: true`. |
| `cloud-config-secret.yaml` | The `cloud-config` Secret (INI `[Global]` AK/SK + HCS endpoints) rendered from cloud-credentials via Flux `valuesFrom`. |
| `volumesnapshotclass.yaml` | VolumeSnapshotClass `evs-snapshots`. Default ON. |
| `default-class-hook.yaml` + `-rbac.yaml` | Post-install hook Job (+ scoped ClusterRole) promoting `evs-ssd` to cluster-default. Reuses the provisioner-agnostic flip logic from `bp-hcloud-csi`. |
| `rbac.yaml` | ServiceAccounts + scoped ClusterRoles for the controller + node sidecars. |

## Provider gating

The bootstrap-kit slot (`55b-bp-huawei-evs-csi.yaml`) is **default-suspended**
(`suspend: ${HUAWEI_HR_SUSPEND:=true}`): a **Hetzner** Sovereign never installs
the Hetzner-irrelevant EVS driver. The **Huawei** cloud-init sets
`HUAWEI_HR_SUSPEND=false`, so a Huawei Sovereign installs it with
`enabled: true` + `defaultStorageClass: true`. (This is the inverse convention
to slot 55a, which renders active-but-empty on Huawei because the stateful root
slots `dependsOn` it; nothing `dependsOn` slot 55b.)

## Image registry note

The `evs-csi-plugin` driver image is published to Huawei SWR (`cn-north-4`),
which is slow/blocked from kom4dc/Oman; the sidecar images live on the
deprecated `k8s.gcr.io`. Both are repointed through the `harbor.openova.io`
proxy-cache projects (the bastion NAT bypass — see
`infra/providers/huawei/main.tf` `registry_mirror_yaml_huawei`) so the Kyverno
`harbor-proxy-pull` policy passes. If the local SWR mirror carries a different
driver version, override `image.driver.tag` per Sovereign.

## References

- docs/ARCHITECTURE.md §3.9 (storage / CSI)
- platform/hcloud-csi/README.md (the Hetzner sibling)
- Upstream: https://github.com/huaweicloud/huaweicloud-csi-driver
- #3971 (the P0 blocker) · #3992 (the local-path-FORBIDDEN base)
