# platform/kube-access-lib

Helm **library chart** holding the one way a Helm test hook of this repository
reaches the Kubernetes API server. Platform plumbing — not a Blueprint, never
installed on its own, no `blueprint.yaml`.

## What it holds

One named template, `kube-access-lib.in-cluster-kubeconfig`: shell lines that
write a kubeconfig from the Pod's own ServiceAccount (token, CA, namespace),
point it at `KUBERNETES_SERVICE_HOST:KUBERNETES_SERVICE_PORT` and export
`KUBECONFIG`, exactly as the bp-sso-bridge reconciler does
(`platform/sso-bridge/chart/templates/configmap-reconciler.yaml`).

Without it, kubectl falls back to the in-cluster configuration only while no
client flag changes its merged configuration. One `--request-timeout=15s` is
enough to send every call to `http://localhost:8080`: measured with
`alpine/k8s:1.31.4` on 19 November 2026, and the cause of the bp-sso-bridge
function test that never passed (`couldn't get current server API group list:
Get "http://localhost:8080/api?timeout=15s"`). The template's header records
the three measured requests.

## Consuming it

```yaml
# Chart.yaml
dependencies:
  - name: kube-access-lib
    version: "0.1.0"
    repository: "file://../../kube-access-lib/chart"
```

```yaml
# templates/tests/function-test.yaml, the container script
      args:
        - |
          set -u
          {{- include "kube-access-lib.in-cluster-kubeconfig" . | nindent 10 }}
```

`helm dependency build` vendors the library into the consumer's package
(`charts/` is gitignored), as for `platform/topology-lib`.

## Guards

- `tools/guards/cmd/kube-access` (tree-guards): every test hook under
  `platform/*/chart` and `products/*/chart` whose script runs kubectl includes
  the template before its first kubectl call, and its chart declares this
  dependency; the template still builds the kubeconfig from the ServiceAccount.
- `scripts/check-kube-access-rendered.sh`: the same rule on a `helm template`
  render; each consuming chart's `chart/tests/function-test-render.sh` runs it.
