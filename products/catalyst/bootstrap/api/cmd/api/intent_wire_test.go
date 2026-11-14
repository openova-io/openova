package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"sigs.k8s.io/yaml"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite/fake"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/intent"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

var intentTestGVR = schema.GroupVersionResource{Group: "apps.openova.io", Version: "v1", Resource: "applications"}

func intentTestApp(name, changeID string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps.openova.io/v1")
	u.SetKind("Application")
	u.SetNamespace("acme")
	u.SetName(name)
	if changeID != "" {
		u.SetAnnotations(map[string]string{intent.ChangeIDAnnotation: changeID})
	}
	return u
}

func intentTestDynamic(objs ...runtime.Object) *dynamicfake.FakeDynamicClient {
	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "apps.openova.io", Version: "v1", Kind: "Application"}, &unstructured.Unstructured{})
	scheme.AddKnownTypeWithName(schema.GroupVersionKind{Group: "apps.openova.io", Version: "v1", Kind: "ApplicationList"}, &unstructured.UnstructuredList{})
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, map[schema.GroupVersionResource]string{intentTestGVR: "ApplicationList"}, objs...)
}

// intentTestFactory starts an informer cache over two fake clusters, c1
// holding the Application "seeded" and c2 empty.
func intentTestFactory(t *testing.T) (*k8scache.Factory, *dynamicfake.FakeDynamicClient, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	r := k8scache.NewRegistry()
	if err := r.Add(k8scache.Kind{Name: "application", GVR: intentTestGVR, Namespaced: true}); err != nil {
		t.Fatal(err)
	}
	c1 := intentTestDynamic(intentTestApp("seeded", "01JCDZ7K9M3Q4T5V6W7X8Y9Z0A"))
	c2 := intentTestDynamic()
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Registry: r,
		Clusters: []k8scache.ClusterRef{
			{ID: "c1", DynamicClient: c1, CoreClient: kfake.NewSimpleClientset()},
			{ID: "c2", DynamicClient: c2, CoreClient: kfake.NewSimpleClientset()},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Stop)
	if err := f.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f, c1, c2
}

func TestIntentCacheGetAndWatch(t *testing.T) {
	f, c1, c2 := intentTestFactory(t)
	c := newIntentCache(f)
	deadline := time.Now().Add(5 * time.Second)
	for c.Get("c1", "application", "acme", "seeded") == nil {
		if time.Now().After(deadline) {
			t.Fatal("the seeded Application never reached the cache")
		}
		time.Sleep(20 * time.Millisecond)
	}
	got := c.Get("c1", "application", "acme", "seeded")
	if got.GetAnnotations()[intent.ChangeIDAnnotation] != "01JCDZ7K9M3Q4T5V6W7X8Y9Z0A" {
		t.Fatalf("Get returned %v", got.GetAnnotations())
	}
	for _, miss := range [][3]string{{"c2", "acme", "seeded"}, {"c1", "other", "seeded"}, {"c1", "acme", "absent"}, {"c9", "acme", "seeded"}} {
		if c.Get(miss[0], "application", miss[1], miss[2]) != nil {
			t.Errorf("Get(%v) found an object", miss)
		}
	}

	events, cancel := c.Watch("c1", "applications")
	ctx := context.Background()
	if _, err := c2.Resource(intentTestGVR).Namespace("acme").Create(ctx, intentTestApp("in-c2", ""), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	for c.Get("c2", "application", "acme", "in-c2") == nil {
		if time.Now().After(deadline) {
			t.Fatal("the Application of c2 never reached the cache")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := c1.Resource(intentTestGVR).Namespace("acme").Create(ctx, intentTestApp("in-c1", ""), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	timeout := time.After(5 * time.Second)
	for got := false; !got; {
		select {
		case ev := <-events:
			if ev.Object.GetName() == "in-c2" {
				t.Fatal("Watch delivered an event of another cluster")
			}
			if ev.Object.GetName() == "in-c1" {
				if ev.Type != "ADDED" {
					t.Fatalf("event type %q", ev.Type)
				}
				got = true
			}
		case <-timeout:
			t.Fatal("no event for the Application created in c1")
		}
	}
	// Events that are still on their way must not be of c2 either.
	for quiet := time.After(200 * time.Millisecond); ; {
		select {
		case ev := <-events:
			if ev.Object.GetName() == "in-c2" {
				t.Fatal("Watch delivered an event of another cluster")
			}
			continue
		case <-quiet:
		}
		break
	}
	cancel()
	cancel()
	for range events {
	}
}

func TestIntentGitConfig(t *testing.T) {
	env := map[string]string{"CATALYST_GITEA_URL": "http://gitea.gitea.svc:3000", "CATALYST_GITEA_TOKEN": "tok", "SOVEREIGN_FQDN": "t1.example.invalid"}
	cfg, err := intentGitConfig(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "http://gitea.gitea.svc:3000" || cfg.RobotDomain != "robots.t1.example.invalid" {
		t.Fatalf("config %+v", cfg)
	}
	if tok, err := cfg.Tokens.Token(context.Background(), "acme-iac-bot"); err != nil || tok != "tok" {
		t.Fatalf("token %q %v", tok, err)
	}
	for _, k := range []string{"CATALYST_GITEA_URL", "CATALYST_GITEA_TOKEN", "SOVEREIGN_FQDN"} {
		saved := env[k]
		env[k] = ""
		if _, err := intentGitConfig(func(k string) string { return env[k] }); err == nil {
			t.Errorf("no error without %s", k)
		}
		env[k] = saved
	}
}

func TestNewIntentDeps(t *testing.T) {
	f, _, _ := intentTestFactory(t)
	env := map[string]string{"CATALYST_GITEA_URL": "http://gitea.gitea.svc:3000", "CATALYST_GITEA_TOKEN": "tok", "SOVEREIGN_FQDN": "t1.example.invalid"}
	getenv := func(k string) string { return env[k] }
	d, err := newIntentDeps(f, "c1", getenv)
	if err != nil {
		t.Fatal(err)
	}
	if d.Git == nil || d.Cache == nil || d.ClusterID != "c1" || d.FQDN != "t1.example.invalid" || d.SyncWait != 15*time.Second {
		t.Fatalf("deps %+v", d)
	}
	env["CATALYST_INTENT_SYNC_WAIT"] = "2s"
	if d, err = newIntentDeps(f, "c1", getenv); err != nil || d.SyncWait != 2*time.Second {
		t.Fatalf("sync wait 2s: %+v %v", d, err)
	}
	for _, bad := range []string{"soon", "0s", "-1s"} {
		env["CATALYST_INTENT_SYNC_WAIT"] = bad
		if _, err := newIntentDeps(f, "c1", getenv); err == nil {
			t.Errorf("no error for CATALYST_INTENT_SYNC_WAIT=%s", bad)
		}
	}
	env["CATALYST_INTENT_SYNC_WAIT"] = ""
	if _, err := newIntentDeps(nil, "c1", getenv); err == nil {
		t.Error("no error without a cache")
	}
	if _, err := newIntentDeps(f, "", getenv); err == nil {
		t.Error("no error without a cluster identifier")
	}
}

// applyWriter plays Flux for one cluster: each committed upsert is created
// in that cluster's API.
type applyWriter struct {
	*fake.Writer
	cluster dynamic.Interface
}

func (w applyWriter) Commit(ctx context.Context, c gitwrite.Change) (gitwrite.Result, error) {
	res, err := w.Writer.Commit(ctx, c)
	if err != nil || !res.Committed {
		return res, err
	}
	for _, fc := range c.Files {
		obj := &unstructured.Unstructured{}
		if err := yaml.Unmarshal(fc.Content, &obj.Object); err != nil {
			return res, err
		}
		if _, err := w.cluster.Resource(intentTestGVR).Namespace(obj.GetNamespace()).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
			return res, err
		}
	}
	return res, nil
}

// TestIntentSubmitThroughCache runs Submit against the real informer cache
// through the adapter: the wait ends when the Application with the new
// change identifier arrives in cluster c1.
func TestIntentSubmitThroughCache(t *testing.T) {
	f, c1, _ := intentTestFactory(t)
	w := fake.New()
	t.Cleanup(w.Close)
	d := intent.Deps{Git: applyWriter{Writer: w, cluster: c1}, Cache: newIntentCache(f), ClusterID: "c1", SyncWait: 5 * time.Second}
	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte("apiVersion: apps.openova.io/v1\nkind: Application\nmetadata:\n  name: site\n  namespace: acme\nspec:\n  environmentRef: acme-prod\n  blueprintRef:\n    name: bp-wordpress\n    version: 1.3.0\n"), &obj.Object); err != nil {
		t.Fatal(err)
	}
	out, err := intent.Submit(context.Background(), d, gitwrite.OrgIntent("acme"), gitwrite.Repo{Owner: "acme", Name: "catalyst-tenant"},
		[]*unstructured.Unstructured{obj}, nil, func(o *unstructured.Unstructured) string { return "intent/applications/" + o.GetName() + ".yaml" },
		"Install site", gitwrite.Identity{}, gitwrite.ViaAPI, gitwrite.AbsentSHA, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Applied || out.Noop {
		t.Fatalf("outcome %+v", out)
	}
	got := newIntentCache(f).Get("c1", "application", "acme", "site")
	if got == nil || got.GetAnnotations()[intent.ChangeIDAnnotation] != out.ChangeID {
		t.Fatalf("cache holds %v", got)
	}
}
