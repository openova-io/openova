package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// workload renders one line of a render file: a kind with a Pod template.
func workload(kind, ns, name, sa, image, secrets string) string {
	spec := `{"serviceAccountName":"` + sa + `","containers":[{"name":"c","image":"` + image + `"}]` + secrets + `}`
	meta := `"name":"` + name + `"`
	if ns != "" {
		meta += `,"namespace":"` + ns + `"`
	}
	switch kind {
	case "Pod":
		return `{"apiVersion":"v1","kind":"Pod","metadata":{` + meta + `},"spec":` + spec + `}`
	case "CronJob":
		return `{"apiVersion":"batch/v1","kind":"CronJob","metadata":{` + meta + `},"spec":{"jobTemplate":{"spec":{"template":{"spec":` + spec + `}}}}}`
	}
	return `{"apiVersion":"apps/v1","kind":"` + kind + `","metadata":{` + meta + `},"spec":{"template":{"spec":` + spec + `}}}`
}

const ghcrPull = `,"imagePullSecrets":[{"name":"ghcr-pull"}]`

// The kit as it renders a first-party controller that names its pull secret,
// one whose ServiceAccount names it, and an upstream image that needs none.
func cleanRender() map[string]string {
	return map[string]string{
		"1.bp-continuum.catalyst-system.jsonl": workload("Deployment", "catalyst-system", "continuum", "continuum",
			"ghcr.io/openova-io/openova/continuum-controller:5f3b7cb", ghcrPull) + "\nnull\n",
		"2.bp-k8s-ws-proxy.k8s-ws-proxy.jsonl": `{"apiVersion":"v1","kind":"ServiceAccount","metadata":{"name":"proxy"},"imagePullSecrets":[{"name":"ghcr-pull"}]}` + "\n" +
			workload("DaemonSet", "", "proxy", "proxy", "ghcr.io/openova-io/openova/k8s-ws-proxy:abc1234", "") + "\n",
		"3.bp-cnpg.cnpg-system.jsonl": workload("Deployment", "cnpg-system", "cnpg", "cnpg", "ghcr.io/cloudnative-pg/cloudnative-pg:1.25.0", "") + "\n",
	}
}

func write(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runOn(t *testing.T, files map[string]string) (int, string, string) {
	t.Helper()
	var out, errw bytes.Buffer
	rc := run([]string{"-render", write(t, files)}, &out, &errw)
	return rc, out.String(), errw.String()
}

func TestCleanRenderPasses(t *testing.T) {
	rc, out, errw := runOn(t, cleanRender())
	if rc != 0 {
		t.Fatalf("rc=%d out=%s err=%s", rc, out, errw)
	}
	if !strings.Contains(out, "every one of the 2 Pod templates with a first-party image") {
		t.Fatalf("the pass does not count the two first-party templates: %s", out)
	}
}

// The defect of 27 Nov 2026: the access controller of bp-guacamole, pinned by
// digest, with no pull secret and a ServiceAccount without one.
func TestDigestPinnedControllerWithoutPullSecretFails(t *testing.T) {
	files := cleanRender()
	files["4.bp-guacamole.guacamole.jsonl"] = `{"apiVersion":"v1","kind":"ServiceAccount","metadata":{"name":"access-controller","namespace":"guacamole"}}` + "\n" +
		workload("Deployment", "guacamole", "access-controller", "access-controller",
			"ghcr.io/openova-io/openova/access-controller@sha256:"+strings.Repeat("ab", 32), "") + "\n"
	rc, out, _ := runOn(t, files)
	if rc != 1 {
		t.Fatalf("rc=%d, want 1: %s", rc, out)
	}
	want := "FAIL bp-guacamole Deployment guacamole/access-controller runs ghcr.io/openova-io/openova/access-controller@sha256:"
	if !strings.Contains(out, want) || !strings.Contains(out, "ServiceAccount guacamole/access-controller") {
		t.Fatalf("the finding does not name the chart, the object, the image and the ServiceAccount:\n%s", out)
	}
	if strings.Contains(out, "continuum") || strings.Contains(out, "cnpg") {
		t.Fatalf("a template with a pull secret or an upstream image is a finding:\n%s", out)
	}
	// The second value set of the kit renders the same Deployment: one finding.
	files["9.bp-guacamole.guacamole.jsonl"] = files["4.bp-guacamole.guacamole.jsonl"]
	rc, out, _ = runOn(t, files)
	if rc != 1 || strings.Count(out, "FAIL ") != 1 {
		t.Fatalf("the same template rendered twice is not one finding: rc=%d\n%s", rc, out)
	}
}

// A chart pivoted by global.imageRegistry renders the image on the Sovereign's
// registry; it is first-party all the same. Every kind with a Pod template is read.
func TestEveryKindAndThePivotedNameAreRead(t *testing.T) {
	for _, kind := range []string{"Pod", "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob"} {
		files := cleanRender()
		files["5.bp-x.x.jsonl"] = workload(kind, "x", "w", "", "registry.t1.omani.works/openova-io/openova/x@sha256:"+strings.Repeat("cd", 32), "") + "\n"
		rc, out, _ := runOn(t, files)
		if rc != 1 || !strings.Contains(out, "FAIL bp-x "+kind+" x/w runs registry.t1.omani.works/openova-io/openova/x@") ||
			!strings.Contains(out, "ServiceAccount x/default") {
			t.Fatalf("%s: rc=%d out=%s", kind, rc, out)
		}
	}
}

// An init container counts; a ServiceAccount of another namespace does not.
func TestInitContainerAndAccountNamespace(t *testing.T) {
	files := cleanRender()
	files["6.bp-y.y.jsonl"] = `{"apiVersion":"v1","kind":"ServiceAccount","metadata":{"name":"y","namespace":"other"},"imagePullSecrets":[{"name":"ghcr-pull"}]}` + "\n" +
		`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"y","namespace":"y"},"spec":{"template":{"spec":{"serviceAccountName":"y",` +
		`"initContainers":[{"name":"i","image":"ghcr.io/openova-io/openova/y-init:1"}],"containers":[{"name":"c","image":"docker.io/library/busybox:1"}]}}}}` + "\n"
	rc, out, _ := runOn(t, files)
	if rc != 1 || !strings.Contains(out, "FAIL bp-y Deployment y/y runs ghcr.io/openova-io/openova/y-init:1") {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
}

// An empty pull-secret entry names nothing.
func TestEmptySecretNameIsNoSecret(t *testing.T) {
	files := cleanRender()
	files["7.bp-z.z.jsonl"] = workload("Deployment", "z", "z", "z", "ghcr.io/openova-io/openova/z:1", `,"imagePullSecrets":[{"name":""}]`) + "\n"
	if rc, out, _ := runOn(t, files); rc != 1 || !strings.Contains(out, "FAIL bp-z Deployment z/z") {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
}

func TestNothingToJudgeStops(t *testing.T) {
	cases := map[string]map[string]string{
		"no first-party image": {"1.bp-cnpg.cnpg-system.jsonl": workload("Deployment", "cnpg-system", "cnpg", "", "ghcr.io/cloudnative-pg/cloudnative-pg:1", "") + "\n"},
		"no render file":       {"notes.txt": "x"},
		"a bad file name":      {"render.jsonl": "null\n"},
		"a line not JSON":      {"1.bp-a.a.jsonl": "{not json\n"},
	}
	for name, files := range cases {
		if rc, out, errw := runOn(t, files); rc != 2 {
			t.Fatalf("%s: rc=%d, want 2: %s %s", name, rc, out, errw)
		}
	}
	var out, errw bytes.Buffer
	if rc := run(nil, &out, &errw); rc != 2 {
		t.Fatalf("no -render: rc=%d", rc)
	}
}

func TestFirstParty(t *testing.T) {
	for img, want := range map[string]bool{
		"ghcr.io/openova-io/openova/catalyst-api:1e45db8":             true,
		"registry.t1.omani.works/openova-io/openova/x@sha256:00":      true,
		"harbor.openova.io/openova-io/openova/x:1":                    true,
		"harbor.openova.io/proxy-ghcr/openova-io/openova/x:1":         false,
		"ghcr.io/fluxcd/source-controller:v1.8.0":                     false,
		"openova-io/openova/x:1":                                      false,
		"busybox:1":                                                   false,
		"localhost:5000/openova-io/x:1":                               true,
		"docker.io/openova-io/openova/x:1":                            false,
		"quay.io/openova-io-fork/x:1":                                 false,
		"registry.t1.omani.works/proxy-dockerhub/library/postgres:16": false,
	} {
		if got := firstParty(img); got != want {
			t.Errorf("firstParty(%q) = %v, want %v", img, got, want)
		}
	}
}
