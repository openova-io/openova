package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodLibrary = `{{- /* header */ -}}
{{- define "kube-access-lib.in-cluster-kubeconfig" -}}
# kube-access-lib.in-cluster-kubeconfig: kubectl reaches the API server through this Pod's ServiceAccount
KUBE_ACCESS_SA=/var/run/secrets/kubernetes.io/serviceaccount
export KUBECONFIG=/tmp/kube-access-kubeconfig
kubectl config set-cluster in-cluster --server="https://${KUBERNETES_SERVICE_HOST:-kubernetes.default.svc}:${KUBERNETES_SERVICE_PORT:-443}" --certificate-authority="${KUBE_ACCESS_SA}/ca.crt" >/dev/null
kubectl config set-credentials sa --token="$(cat "${KUBE_ACCESS_SA}/token")" >/dev/null
kubectl config set-context in-cluster --cluster=in-cluster --user=sa --namespace="$(cat "${KUBE_ACCESS_SA}/namespace")" >/dev/null
kubectl config use-context in-cluster >/dev/null
{{- end }}
`

const goodChart = `apiVersion: v2
name: bp-demo
version: 1.0.0
dependencies:
  - name: upstream
    version: "1.0.0"
    repository: "https://example.invalid/charts"
  # The shared API access set-up.
  - name: kube-access-lib
    version: "0.1.0"
    repository: "file://../../kube-access-lib/chart"
`

const goodHook = `{{- /*
Function test. The image is the kubectl image of the kit.
kubectl --request-timeout=15s get pods  (a comment, not a call)
*/ -}}
apiVersion: v1
kind: Pod
metadata:
  name: {{ .Release.Name }}-function-test
  annotations:
    helm.sh/hook: test
spec:
  containers:
    - name: function-test
      args:
        - |
          set -u
          {{- include "kube-access-lib.in-cluster-kubeconfig" . | nindent 10 }}
          # read the Deployment
          if kubectl --request-timeout=15s -n "${NS}" get deployment demo > /tmp/out; then
            echo "FUNCTION-TEST pass bp-demo"
          fi
`

// tree writes files (repository path -> content) under a new root.
func tree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for path, content := range files {
		full := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func good() map[string]string {
	return map[string]string{
		LibraryTemplate: goodLibrary,
		"platform/kube-access-lib/chart/Chart.yaml":              "apiVersion: v2\nname: kube-access-lib\nversion: 0.1.0\ntype: library\n",
		"platform/demo/chart/Chart.yaml":                         goodChart,
		"platform/demo/chart/templates/tests/function-test.yaml": goodHook,
		// A Job that is not a test hook may run kubectl without the set-up: not judged here.
		"platform/demo/chart/templates/gate-job.yaml": "kind: Job\nmetadata:\n  annotations:\n    helm.sh/hook: pre-install\nspec:\n  args: [\"kubectl --request-timeout=5s get crd\"]\n",
		// A test hook that only mentions kubectl in comments is not a kubectl test hook.
		"products/other/chart/Chart.yaml":                         "apiVersion: v2\nname: other\nversion: 1.0.0\n",
		"products/other/chart/templates/tests/function-test.yaml": "{{- /* The image is the kubectl image. */ -}}\nmetadata:\n  annotations:\n    \"helm.sh/hook\": test\n# kubectl get pods\nspec:\n  args: [\"curl -fsS http://demo\"]\n",
	}
}

func judge(t *testing.T, files map[string]string) (int, string) {
	t.Helper()
	root := tree(t, files)
	var out, errOut bytes.Buffer
	code := run([]string{"--root", root}, &out, &errOut)
	return code, out.String() + errOut.String()
}

func TestGoodTreePasses(t *testing.T) {
	code, out := judge(t, good())
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "kube-access: ok platform/demo/chart/templates/tests/function-test.yaml") {
		t.Fatalf("the kubectl test hook is not listed:\n%s", out)
	}
	if strings.Contains(out, "products/other") || strings.Contains(out, "gate-job") {
		t.Fatalf("a template that is not a kubectl test hook was judged:\n%s", out)
	}
	if !strings.Contains(out, "PASS — 1 test hook(s)") {
		t.Fatalf("want exactly one kubectl test hook:\n%s", out)
	}
}

func TestEachRuleFails(t *testing.T) {
	hook := "platform/demo/chart/templates/tests/function-test.yaml"
	cases := []struct {
		name   string
		mutate func(map[string]string)
		want   string
	}{
		{"K1 the library is missing", func(f map[string]string) { delete(f, LibraryTemplate) },
			"K1 " + LibraryTemplate + ": the shared template is missing"},
		{"K1 the library no longer exports the kubeconfig", func(f map[string]string) {
			f[LibraryTemplate] = strings.Replace(goodLibrary, "export KUBECONFIG=/tmp/kube-access-kubeconfig\n", "", 1)
		}, "K1 " + LibraryTemplate + `: the shared template no longer holds "export KUBECONFIG="`},
		{"K1 the library no longer reads the ServiceAccount token", func(f map[string]string) {
			f[LibraryTemplate] = strings.Replace(goodLibrary, `--token="$(cat "${KUBE_ACCESS_SA}/token")"`, `--token=""`, 1)
		}, `no longer holds "/token"`},
		{"K2 a kubectl test hook without the include", func(f map[string]string) {
			f[hook] = strings.Replace(goodHook, "          {{- include \"kube-access-lib.in-cluster-kubeconfig\" . | nindent 10 }}\n", "", 1)
		}, "K2 " + hook + ": the test hook runs kubectl without including"},
		{"K2 the include after the first kubectl call", func(f map[string]string) {
			moved := strings.Replace(goodHook, "          {{- include \"kube-access-lib.in-cluster-kubeconfig\" . | nindent 10 }}\n", "", 1)
			f[hook] = strings.Replace(moved, "          fi\n", "          fi\n          {{- include \"kube-access-lib.in-cluster-kubeconfig\" . | nindent 10 }}\n", 1)
		}, "K2 " + hook + ": the test hook runs kubectl before it includes"},
		{"K2 a test hook in products runs kubectl through a variable", func(f map[string]string) {
			f["products/other/chart/templates/tests/function-test.yaml"] = "metadata:\n  annotations:\n    \"helm.sh/hook\": test\nspec:\n  args:\n    - |\n      KC=\"kubectl --request-timeout=20s\"\n      $KC get pods\n"
		}, "K2 products/other/chart/templates/tests/function-test.yaml: the test hook runs kubectl without including"},
		{"K3 the chart does not declare the library", func(f map[string]string) {
			f["platform/demo/chart/Chart.yaml"] = strings.Replace(goodChart, "kube-access-lib\n", "other-lib\n", 1)
		}, "K3 platform/demo/chart/Chart.yaml: the chart does not declare"},
		{"K3 the chart declares the library from another repository", func(f map[string]string) {
			f["platform/demo/chart/Chart.yaml"] = strings.Replace(goodChart, "file://../../kube-access-lib/chart", "oci://example.invalid/charts", 1)
		}, "K3 platform/demo/chart/Chart.yaml: the chart does not declare"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := good()
			c.mutate(files)
			code, out := judge(t, files)
			if code != 1 {
				t.Fatalf("exit %d, want 1:\n%s", code, out)
			}
			if !strings.Contains(out, c.want) {
				t.Fatalf("output does not name the finding %q:\n%s", c.want, out)
			}
		})
	}
}

func TestAbsentRootFails(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--root", filepath.Join(t.TempDir(), "absent")}, &out, &errOut); code != 1 && code != 2 {
		t.Fatalf("exit %d for an absent root, want a failure", code)
	}
}
