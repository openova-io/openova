# bp-secrets-store-csi

The Secrets Store CSI driver: it mounts values from OpenBao into a Pod as
memory-backed files, and never copies them into a Secret object.

## Role in Catalyst

Control-plane plumbing, installed on every region by bootstrap-kit slot 15b
(`clusters/_template/bootstrap-kit/15b-bp-secrets-store-csi.yaml`) as
HelmRelease `bp-secrets-store-csi` in namespace `kube-system` (the upstream default). Never a
catalog item (`visibility: unlisted`). It is the injection mechanism of
[`docs/adr/0018-secrets-and-trust.md`](../../docs/adr/0018-secrets-and-trust.md):
an Application credential is delivered into the running workload from OpenBao
and never becomes a Kubernetes Secret object. See
[`docs/ARCHITECTURE.md`](../../docs/ARCHITECTURE.md) for the bootstrap kit.

The driver talks to OpenBao through the OpenBao CSI provider, a DaemonSet of
the bp-openbao chart that slot 08 switches on (`openbao.csi.enabled: true`).
The slot depends on `bp-openbao`.

## Configuration

The chart wraps the upstream chart `secrets-store-csi-driver` 1.6.2; its
values sit under the key `secrets-store-csi-driver:` of `chart/values.yaml`.

| Value | Setting | Why |
|---|---|---|
| `syncSecret.enabled` | `false` | The driver gets no right on Secret objects. Never switch it on. |
| `enableSecretRotation`, `rotationPollInterval` | `true`, `3600s` | One re-read an hour, equal to the default lease of a dynamic database user. |
| `tokenRequests` | `[{audience: openbao}]` | The kubelet hands the driver the Pod's token for OpenBao role `workload`. |
| images | `harbor.openova.io/proxy-k8s/...` | Admitted by the Harbor proxy pull policy; pivoted to the Sovereign's own Harbor at cutover. |

A workload mounts a value with a `SecretProviderClass` of provider `openbao`
(parameters `openbaoAddress`, `roleName: workload`, `audience: openbao`,
`objects`) and a CSI volume of driver `secrets-store.csi.k8s.io`. A
`SecretProviderClass` never names a Secret to sync the value into.

## Operations

- Gate check SEC-G08 proves on every region that a mounted value is in no
  Secret object, that the driver's service account cannot create a Secret,
  and that another service account gets 403 on the value's path.
- The render test is `chart/tests/secrets-store-csi-render.sh`.
- No state, no backup: the driver holds nothing; a Pod that starts while
  OpenBao does not answer waits in `ContainerCreating`.
