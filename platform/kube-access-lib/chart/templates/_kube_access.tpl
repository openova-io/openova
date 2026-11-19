{{- /*
kube-access-lib.in-cluster-kubeconfig

Shell lines that make kubectl reach the API server through the Pod's own
ServiceAccount, exactly as the bp-sso-bridge reconciler does it
(platform/sso-bridge/chart/templates/configmap-reconciler.yaml, "kubeconfig
bootstrap OK"): a kubeconfig written from the mounted token, CA and namespace,
pointed at KUBERNETES_SERVICE_HOST:KUBERNETES_SERVICE_PORT, and exported.

Why it is needed. kubectl uses the in-cluster configuration only when the
client configuration it merged from files and flags EQUALS the default one
(client-go tools/clientcmd/merged_client_builder.go, ClientConfig: a merged
configuration that is not the default is returned as it is). With no
kubeconfig, a client flag such as --request-timeout (also --as, --server,
--token, --context ...) makes the merged configuration differ from the
default, so kubectl keeps the default server http://localhost:8080 and never
reads the ServiceAccount. Measured with alpine/k8s 1.31.4 as user 1001,
HOME=/tmp, read-only root, a mounted ServiceAccount and
KUBERNETES_SERVICE_HOST/PORT set:

  kubectl --request-timeout=15s get --raw /api
      GET http://localhost:8080/api?timeout=15s  (connection refused)
  kubectl get --raw /api
      Using in-cluster configuration; GET https://<host>:<port>/api
  the same --request-timeout call after these lines
      Config loaded from file /tmp/kube-access-kubeconfig;
      GET https://<host>:<port>/api?timeout=15s

With the explicit kubeconfig every kubectl flag is safe.

Use it as the first lines of the container script, after `set -u`, in a
container whose /tmp is writable (the function tests mount an emptyDir there):

  set -u
  (include "kube-access-lib.in-cluster-kubeconfig" . | nindent 10, in double braces)

The chart declares the dependency (Chart.yaml):

  - name: kube-access-lib
    version: "0.1.0"
    repository: "file://../../kube-access-lib/chart"

POSIX sh. It needs kubectl and cat in the image. tools/guards/cmd/kube-access
holds that every test hook of the repository that runs kubectl uses it.
*/ -}}
{{- define "kube-access-lib.in-cluster-kubeconfig" -}}
# kube-access-lib.in-cluster-kubeconfig: kubectl reaches the API server through this Pod's ServiceAccount
KUBE_ACCESS_SA=/var/run/secrets/kubernetes.io/serviceaccount
export KUBECONFIG=/tmp/kube-access-kubeconfig
kubectl config set-cluster in-cluster --server="https://${KUBERNETES_SERVICE_HOST:-kubernetes.default.svc}:${KUBERNETES_SERVICE_PORT:-443}" --certificate-authority="${KUBE_ACCESS_SA}/ca.crt" >/dev/null
kubectl config set-credentials sa --token="$(cat "${KUBE_ACCESS_SA}/token")" >/dev/null
kubectl config set-context in-cluster --cluster=in-cluster --user=sa --namespace="$(cat "${KUBE_ACCESS_SA}/namespace")" >/dev/null
kubectl config use-context in-cluster >/dev/null
{{- end }}
