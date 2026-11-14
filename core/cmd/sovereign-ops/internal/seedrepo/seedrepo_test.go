package seedrepo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openova-io/openova/core/cmd/sovereign-ops/internal/ops"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite/fake"
)

const (
	fqdn = "t1.example.invalid"
	root = "clusters/" + fqdn + "/regions/region-a/platform/root/"
)

var repo = gitwrite.Repo{Owner: "openova", Name: "sovereign-regions"}

// fakeCluster holds objects by "<kind>/<name>"; err is returned by every Get.
type fakeCluster struct {
	objects map[string]map[string]interface{}
	err     error
}

func (f *fakeCluster) Get(_ context.Context, _, kind, _, name string) (map[string]interface{}, error) {
	return f.objects[kind+"/"+name], f.err
}

// newCluster returns the ConfigMap and two root Kustomizations as the API
// server returns them, with status and server-written metadata.
func newCluster() *fakeCluster {
	meta := func(name string) map[string]interface{} {
		return map[string]interface{}{"name": name, "namespace": "flux-system", "uid": "u-1", "resourceVersion": "7",
			"generation": int64(2), "finalizers": []interface{}{"finalizers.fluxcd.io"},
			"managedFields": []interface{}{map[string]interface{}{"manager": "kubectl"}},
			"annotations":   map[string]interface{}{"kubectl.kubernetes.io/last-applied-configuration": "{}"}}
	}
	ks := func(name string) map[string]interface{} {
		return map[string]interface{}{"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization", "metadata": meta(name),
			"spec":   map[string]interface{}{"path": "./clusters/_template/" + name, "prune": true, "postBuild": map[string]interface{}{}},
			"status": map[string]interface{}{"lastAppliedRevision": "main@sha1:abc"}}
	}
	return &fakeCluster{objects: map[string]map[string]interface{}{
		"ConfigMap/sovereign-variables": {"apiVersion": "v1", "kind": "ConfigMap", "metadata": meta("sovereign-variables"),
			"data": map[string]interface{}{"SOVEREIGN_FQDN": fqdn, "SOVEREIGN_REGION_KEY": "a", "PROVIDER": "huawei"}},
		"Kustomization/bootstrap-kit": ks("bootstrap-kit"),
		"Kustomization/sovereign-tls": ks("sovereign-tls"),
	}}
}

func seed(c Cluster, w *fake.Writer) ([]string, error) {
	return Seed(context.Background(), c, func(string) (gitwrite.Writer, error) { return w, nil }, "region-a", repo)
}

func read(t *testing.T, w *fake.Writer, name string) string {
	b, _, err := w.Read(context.Background(), gitwrite.RegionPlatform(fqdn, []string{"root"}, []string{"region-a"}), repo, root+name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

// TestSeedRepoWritesOnceThenNoop: the first run writes the root in one
// commit, without server-written fields; the second run commits nothing.
func TestSeedRepoWritesOnceThenNoop(t *testing.T) {
	w := fake.New()
	defer w.Close()
	c := newCluster()
	evidence, err := seed(c, w)
	commits := w.Commits()
	if err != nil || len(commits) != 1 || len(evidence) != 5 || evidence[0] != "commit "+commits[0].SHA {
		t.Fatalf("first run: error %v, %d commits, evidence %q", err, len(commits), evidence)
	}
	if got := strings.Join(commits[0].Paths, " "); got != root+"sovereign-variables.yaml "+root+"bootstrap-kit.yaml "+
		root+"sovereign-tls.yaml "+root+"kustomization.yaml" {
		t.Fatalf("paths %s", got)
	}
	v, k := read(t, w, "sovereign-variables.yaml"), read(t, w, "bootstrap-kit.yaml")
	for _, s := range []string{"kind: ConfigMap", "SOVEREIGN_FQDN: " + fqdn, "kustomize.toolkit.fluxcd.io/substitute: disabled",
		"app.kubernetes.io/managed-by: flux", "catalyst.openova.io/change-id: "} {
		if !strings.Contains(v, s) {
			t.Fatalf("sovereign-variables.yaml lacks %q:\n%s", s, v)
		}
	}
	for _, s := range []string{"status", "uid", "resourceVersion", "generation", "finalizers", "managedFields", "last-applied"} {
		if strings.Contains(v+k, s) || !strings.HasSuffix(k, "path: ./clusters/_template/bootstrap-kit\n  postBuild: {}\n  prune: true\n") {
			t.Fatalf("a committed object holds %q or lost its spec:\n%s\n%s", s, v, k)
		}
	}
	if got := read(t, w, "kustomization.yaml"); got != "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n"+
		"resources:\n- sovereign-variables.yaml\n- bootstrap-kit.yaml\n- sovereign-tls.yaml\n" {
		t.Fatalf("kustomization.yaml:\n%s", got)
	}
	evidence, err = seed(c, w)
	if err != nil || len(w.Commits()) != 1 || len(evidence) != 1 || !strings.HasPrefix(evidence[0], "seeded already: "+root+"kustomization.yaml is blob ") {
		t.Fatalf("second run: error %v, %d commits, evidence %q", err, len(w.Commits()), evidence)
	}
}

// TestSeedRepoNeverOverwritesNewerCommit: a file changed in Git after the
// seed keeps its content, although the cluster's copy now differs.
func TestSeedRepoNeverOverwritesNewerCommit(t *testing.T) {
	w := fake.New()
	defer w.Close()
	c := newCluster()
	if _, err := seed(c, w); err != nil {
		t.Fatal(err)
	}
	newer := "apiVersion: v1\nkind: ConfigMap\ndata:\n  PROVIDER: newer\n"
	w.Put(repo, root+"sovereign-variables.yaml", []byte(newer))
	c.objects["ConfigMap/sovereign-variables"]["data"].(map[string]interface{})["PROVIDER"] = "changed"
	if _, err := seed(c, w); err != nil || len(w.Commits()) != 1 || read(t, w, "sovereign-variables.yaml") != newer {
		t.Fatalf("second run: error %v, %d commits, sovereign-variables.yaml %q", err, len(w.Commits()), read(t, w, "sovereign-variables.yaml"))
	}
}

// TestSeedRepoRefusals: each refusal commits nothing and names its cause.
func TestSeedRepoRefusals(t *testing.T) {
	data := func(c *fakeCluster) map[string]interface{} {
		return c.objects["ConfigMap/sovereign-variables"]["data"].(map[string]interface{})
	}
	for want, change := range map[string]func(*fakeCluster, *fake.Writer){
		"Kustomization flux-system/sovereign-tls carries an inline postBuild.substitute map": func(c *fakeCluster, _ *fake.Writer) {
			c.objects["Kustomization/sovereign-tls"]["spec"].(map[string]interface{})["postBuild"] = map[string]interface{}{"substitute": map[string]interface{}{}}
		},
		"the key POWERDNS_API_KEY of ConfigMap":         func(c *fakeCluster, _ *fake.Writer) { data(c)["POWERDNS_API_KEY"] = "x" },
		"does not exist or has no value SOVEREIGN_FQDN": func(c *fakeCluster, _ *fake.Writer) { delete(data(c), "SOVEREIGN_FQDN") },
		root + " holds " + root + "stray.yaml but no kustomization.yaml": func(_ *fakeCluster, w *fake.Writer) {
			w.Put(repo, root+"stray.yaml", []byte("a: b\n"))
		},
		"transient: refused": func(c *fakeCluster, _ *fake.Writer) { c.err = ops.Transient(errors.New("refused")) },
	} {
		w := fake.New()
		c := newCluster()
		change(c, w)
		_, err := seed(c, w)
		if err == nil || !strings.Contains(err.Error(), want) || len(w.Commits()) != 0 {
			t.Errorf("error %v, %d commits; want an error with %q and none", err, len(w.Commits()), want)
		}
		w.Close()
	}
}

// TestAPIServerGet: the reader's path, its token, and how it reports an
// absent object, a refusal and a server that fails.
func TestAPIServerGet(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get("Authorization") != "Bearer tok":
			rw.WriteHeader(http.StatusUnauthorized)
		case r.URL.Path == "/api/v1/namespaces/flux-system/configmaps/sovereign-variables":
			_, _ = rw.Write([]byte(`{"kind":"ConfigMap","metadata":{"generation":1000000}}`))
		case r.URL.Path == "/apis/kustomize.toolkit.fluxcd.io/v1/namespaces/flux-system/kustomizations/broken":
			rw.WriteHeader(http.StatusBadGateway)
		default:
			rw.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	a := &apiServer{base: srv.URL, token: "tok", client: srv.Client()}
	ctx := context.Background()
	if obj, err := a.Get(ctx, "v1", "ConfigMap", "flux-system", "sovereign-variables"); err != nil || obj["kind"] != "ConfigMap" ||
		fmt.Sprint(obj["metadata"].(map[string]interface{})["generation"]) != "1000000" {
		t.Fatalf("ConfigMap: %v %v", obj, err)
	}
	if obj, err := a.Get(ctx, "kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "absent"); err != nil || obj != nil {
		t.Fatalf("absent: %v %v", obj, err)
	}
	if _, err := a.Get(ctx, "kustomize.toolkit.fluxcd.io/v1", "Kustomization", "flux-system", "broken"); !errors.Is(err, ops.ErrTransient) {
		t.Fatalf("HTTP 502 is not transient: %v", err)
	}
	a.token = "wrong"
	if _, err := a.Get(ctx, "v1", "ConfigMap", "flux-system", "sovereign-variables"); err == nil || errors.Is(err, ops.ErrTransient) {
		t.Fatalf("HTTP 401 is transient or no error: %v", err)
	}
}

// TestRule5MatchesUpstream: the copy of rule 5 in this package equals the
// one of core/controllers/pkg/upstream, which the module does not import.
func TestRule5MatchesUpstream(t *testing.T) {
	src, err := os.ReadFile(filepath.Clean("../../../../controllers/pkg/upstream/upstream.go"))
	if err != nil {
		t.Fatal(err)
	}
	quoted := func(names []string) string {
		q := make([]string, len(names))
		for i, n := range names {
			q[i] = fmt.Sprintf("%q", n)
		}
		return strings.Join(q, ", ")
	}
	for _, line := range []string{
		"var SecretNameExceptions = []string{" + quoted(secretNameExceptions) + "}",
		"var secretSuffixes = []string{" + quoted(secretSuffixes) + "}",
	} {
		if strings.Count(string(src), "\n"+line+"\n") != 1 {
			t.Errorf("core/controllers/pkg/upstream/upstream.go has no line %s", line)
		}
	}
	if !namesSecret("POWERDNS_API_KEY") || !namesSecret("A_TOKEN") || namesSecret("SOVEREIGN_REGION_KEY") || namesSecret("SOVEREIGN_FQDN") {
		t.Error("namesSecret does not apply rule 5")
	}
}
