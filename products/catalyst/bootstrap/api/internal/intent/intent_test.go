package intent

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite/fake"
)

const testCluster = "c1"

var (
	testScope = gitwrite.OrgIntent("acme")
	testRepo  = gitwrite.Repo{Owner: "acme", Name: "catalyst-tenant"}
	testUser  = gitwrite.Identity{Name: "Jane Roe", Email: "jane@example.org"}
)

// fakeCache is an in-memory ObjectCache. set and remove update the object
// first and then send the event, as the informer cache does.
type fakeCache struct {
	mu       sync.Mutex
	objects  map[string]*unstructured.Unstructured
	watchers map[string][]chan CacheEvent
	watches  int
}

func newFakeCache() *fakeCache {
	return &fakeCache{objects: map[string]*unstructured.Unstructured{}, watchers: map[string][]chan CacheEvent{}}
}

func cacheKey(cluster, kind, ns, name string) string {
	return cluster + "|" + kind + "|" + ns + "|" + name
}

func (c *fakeCache) Get(clusterID, kind, namespace, name string) *unstructured.Unstructured {
	c.mu.Lock()
	defer c.mu.Unlock()
	if o, ok := c.objects[cacheKey(clusterID, kind, namespace, name)]; ok {
		return o.DeepCopy()
	}
	return nil
}

func (c *fakeCache) Watch(clusterID, kind string) (<-chan CacheEvent, func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan CacheEvent, 16)
	k := clusterID + "|" + kind
	c.watchers[k] = append(c.watchers[k], ch)
	c.watches++
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			c.mu.Lock()
			defer c.mu.Unlock()
			list := c.watchers[k]
			for i, w := range list {
				if w == ch {
					c.watchers[k] = append(list[:i], list[i+1:]...)
					break
				}
			}
			close(ch)
		})
	}
}

func (c *fakeCache) event(kind, typ string, obj *unstructured.Unstructured) {
	for _, ch := range c.watchers[testCluster+"|"+kind] {
		select {
		case ch <- CacheEvent{Type: typ, Object: obj.DeepCopy()}:
		default:
		}
	}
}

func (c *fakeCache) set(obj *unstructured.Unstructured) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kind := cacheKind(obj.GetKind())
	c.objects[cacheKey(testCluster, kind, obj.GetNamespace(), obj.GetName())] = obj.DeepCopy()
	c.event(kind, "MODIFIED", obj)
}

func (c *fakeCache) remove(kind, ns, name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := cacheKey(testCluster, kind, ns, name)
	if o, ok := c.objects[k]; ok {
		delete(c.objects, k)
		c.event(kind, "DELETED", o)
	}
}

func (c *fakeCache) openWatches() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, list := range c.watchers {
		n += len(list)
	}
	return n
}

// flux is a gitwrite.Writer that plays Flux: after each commit it applies
// the committed files to the cache, after delay. With off it applies nothing.
type flux struct {
	*fake.Writer
	cache *fakeCache
	delay time.Duration
	off   bool
	mu    sync.Mutex
	paths map[string][3]string // path -> kind, namespace, name
	wg    sync.WaitGroup
}

func (f *flux) Commit(ctx context.Context, c gitwrite.Change) (gitwrite.Result, error) {
	res, err := f.Writer.Commit(ctx, c)
	if err != nil || !res.Committed || f.off {
		return res, err
	}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		time.Sleep(f.delay)
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, fc := range c.Files {
			if fc.Op == gitwrite.OpDelete {
				if k, ok := f.paths[fc.Path]; ok {
					f.cache.remove(k[0], k[1], k[2])
				}
				continue
			}
			m := map[string]interface{}{}
			if err := yaml.Unmarshal(fc.Content, &m); err != nil {
				continue
			}
			obj := &unstructured.Unstructured{Object: m}
			f.paths[fc.Path] = [3]string{cacheKind(obj.GetKind()), obj.GetNamespace(), obj.GetName()}
			f.cache.set(obj)
		}
	}()
	return res, nil
}

func newTest(t *testing.T) (*flux, *fakeCache, Deps) {
	t.Helper()
	w := fake.New()
	t.Cleanup(w.Close)
	c := newFakeCache()
	f := &flux{Writer: w, cache: c, delay: 20 * time.Millisecond, paths: map[string][3]string{}}
	t.Cleanup(f.wg.Wait)
	return f, c, Deps{Git: f, Cache: c, ClusterID: testCluster, FQDN: "example.invalid", SyncWait: 5 * time.Second}
}

func app(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	obj := readSample(t, "application-sample-valid.yaml")
	obj.SetName(name)
	return obj
}

func appPath(obj *unstructured.Unstructured) string {
	return "intent/applications/" + obj.GetName() + ".yaml"
}

func submit(ctx context.Context, d Deps, ups []*unstructured.Unstructured, dels []Target, ifMatch string) (Outcome, error) {
	return Submit(ctx, d, testScope, testRepo, ups, dels, appPath, "Change applications", testUser, gitwrite.ViaConsole, ifMatch, nil)
}

func appTarget(name string) Target {
	return Target{APIVersion: "apps.openova.io/v1", Kind: "Application", Namespace: "acme", Name: name, Path: "intent/applications/" + name + ".yaml"}
}

func TestSerialize(t *testing.T) {
	obj := app(t, "site")
	obj.SetResourceVersion("42")
	obj.SetUID("u-1")
	obj.SetGeneration(3)
	obj.SetSelfLink("/x")
	obj.Object["metadata"].(map[string]interface{})["creationTimestamp"] = "2026-01-01T00:00:00Z"
	obj.Object["metadata"].(map[string]interface{})["managedFields"] = []interface{}{map[string]interface{}{"manager": "x"}}
	obj.Object["status"] = map[string]interface{}{"phase": "Ready"}
	a, err := Serialize(obj)
	if err != nil {
		t.Fatal(err)
	}
	b, err := Serialize(obj)
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("two calls gave different bytes")
	}
	s := string(a)
	for _, gone := range []string{"status:", "resourceVersion", "uid:", "generation", "selfLink", "creationTimestamp", "managedFields"} {
		if strings.Contains(s, gone) {
			t.Errorf("%q is still in the file", gone)
		}
	}
	if !strings.HasPrefix(s, "apiVersion: apps.openova.io/v1\nkind: Application\nmetadata:\n  labels:\n") {
		t.Errorf("keys are not sorted or not indented by two spaces:\n%s", s)
	}
	if !strings.HasSuffix(s, "\n") || strings.HasSuffix(s, "\n\n") {
		t.Error("the file does not end in exactly one newline")
	}
	if _, ok := obj.Object["status"]; !ok {
		t.Error("Serialize changed its argument")
	}
}

func TestSubmitCreate(t *testing.T) {
	f, c, d := newTest(t)
	in := app(t, "site")
	out, err := submit(context.Background(), d, []*unstructured.Unstructured{in}, nil, gitwrite.AbsentSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Applied || out.Noop || len(out.ChangeID) != 26 || out.Commit == "" || out.ETag == "" {
		t.Fatalf("outcome: %+v", out)
	}
	if len(out.Targets) != 1 || out.Targets[0].Path != "intent/applications/site.yaml" || out.Targets[0].Op != gitwrite.OpUpsert || out.Targets[0].Kind != "Application" {
		t.Fatalf("targets: %+v", out.Targets)
	}
	content, sha, err := f.Read(context.Background(), testScope, testRepo, "intent/applications/site.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if sha != out.ETag {
		t.Errorf("ETag %s, file %s", out.ETag, sha)
	}
	got, _ := parseFile(content)
	if got.GetAnnotations()[ChangeIDAnnotation] != out.ChangeID || got.GetLabels()[ManagedByLabel] != ManagedByValue {
		t.Errorf("file metadata: %v %v", got.GetLabels(), got.GetAnnotations())
	}
	if in.GetAnnotations()[ChangeIDAnnotation] != "" {
		t.Error("Submit changed its argument")
	}
	commits := f.Commits()
	if len(commits) != 1 || commits[0].Author != testUser || !strings.Contains(commits[0].Message, "Change-Id: "+out.ChangeID) || !strings.Contains(commits[0].Message, "Requested-Via: console") {
		t.Fatalf("commits: %+v", commits)
	}
	if obj := c.Get(testCluster, "application", "acme", "site"); obj == nil || obj.GetAnnotations()[ChangeIDAnnotation] != out.ChangeID {
		t.Fatal("the cache does not hold the applied object")
	}
	if n := c.openWatches(); n != 0 {
		t.Errorf("%d watches are still open", n)
	}
}

func TestSubmitNoop(t *testing.T) {
	f, c, d := newTest(t)
	first, err := submit(context.Background(), d, []*unstructured.Unstructured{app(t, "site")}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	watches := c.watches
	again, err := submit(context.Background(), d, []*unstructured.Unstructured{app(t, "site")}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if !again.Noop || !again.Applied || again.ChangeID != first.ChangeID || again.Commit != first.Commit || again.ETag != first.ETag {
		t.Fatalf("first %+v, again %+v", first, again)
	}
	if n := len(f.Commits()); n != 1 {
		t.Fatalf("%d commits, want 1", n)
	}
	if c.watches != watches {
		t.Error("a no-op waited on the cache")
	}
	// A delete of an absent file is a no-op too.
	gone, err := submit(context.Background(), d, nil, []Target{appTarget("absent")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if !gone.Noop || !gone.Applied || len(f.Commits()) != 1 {
		t.Fatalf("delete of an absent file: %+v", gone)
	}
}

func TestSubmitUpdateIfMatch(t *testing.T) {
	f, _, d := newTest(t)
	first, err := submit(context.Background(), d, []*unstructured.Unstructured{app(t, "site")}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	changed := app(t, "site")
	if err := unstructured.SetNestedField(changed.Object, "bp-ghost", "spec", "blueprintRef", "name"); err != nil {
		t.Fatal(err)
	}
	second, err := submit(context.Background(), d, []*unstructured.Unstructured{changed}, nil, first.ETag)
	if err != nil {
		t.Fatal(err)
	}
	if second.Noop || !second.Applied || second.ETag == first.ETag || second.ChangeID == first.ChangeID {
		t.Fatalf("update: %+v", second)
	}
	// The ETag of the first version is stale now.
	_, err = submit(context.Background(), d, []*unstructured.Unstructured{app(t, "site")}, nil, first.ETag)
	var ce *gitwrite.ConflictError
	if !errors.As(err, &ce) || ce.Path != "intent/applications/site.yaml" || ce.Current != second.ETag {
		t.Fatalf("stale If-Match: %v", err)
	}
	// A stale If-Match is a conflict even when the content is unchanged.
	_, err = submit(context.Background(), d, []*unstructured.Unstructured{changed}, nil, first.ETag)
	if !gitwrite.IsConflict(err) {
		t.Fatalf("stale If-Match on equal content: %v", err)
	}
	if n := len(f.Commits()); n != 2 {
		t.Fatalf("%d commits, want 2", n)
	}
}

func TestSubmitConflict(t *testing.T) {
	f, _, d := newTest(t)
	f.Put(testRepo, "intent/applications/site.yaml", []byte("kind: Application\n"))
	_, err := submit(context.Background(), d, []*unstructured.Unstructured{app(t, "site")}, nil, gitwrite.AbsentSHA)
	var ce *gitwrite.ConflictError
	if !errors.As(err, &ce) || ce.Want != gitwrite.AbsentSHA || ce.Current == "" {
		t.Fatalf("create over an existing file: %v", err)
	}
	if n := len(f.Commits()); n != 0 {
		t.Fatalf("%d commits, want 0", n)
	}
}

func TestSubmitDelete(t *testing.T) {
	f, c, d := newTest(t)
	if _, err := submit(context.Background(), d, []*unstructured.Unstructured{app(t, "site")}, nil, ""); err != nil {
		t.Fatal(err)
	}
	out, err := submit(context.Background(), d, nil, []Target{appTarget("site")}, "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Noop || !out.Applied || len(out.Targets) != 1 || out.Targets[0].Op != gitwrite.OpDelete || out.ETag != "" {
		t.Fatalf("delete: %+v", out)
	}
	if _, _, err := f.Read(context.Background(), testScope, testRepo, "intent/applications/site.yaml"); !errors.Is(err, gitwrite.ErrNotFound) {
		t.Fatalf("the file is still there: %v", err)
	}
	if c.Get(testCluster, "application", "acme", "site") != nil {
		t.Fatal("the object is still in the cache")
	}
}

func TestSubmitTimeout(t *testing.T) {
	f, c, d := newTest(t)
	f.off = true
	d.SyncWait = 100 * time.Millisecond
	start := time.Now()
	out, err := submit(context.Background(), d, []*unstructured.Unstructured{app(t, "site")}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if out.Applied || out.Noop || out.Commit == "" {
		t.Fatalf("time-out: %+v", out)
	}
	if took := time.Since(start); took < 100*time.Millisecond || took > 3*time.Second {
		t.Fatalf("waited %s, want about 100ms", took)
	}
	if n := len(f.Commits()); n != 1 {
		t.Fatalf("%d commits, want 1", n)
	}
	if n := c.openWatches(); n != 0 {
		t.Errorf("%d watches are still open", n)
	}
	// An object that carries an older change identifier is not applied.
	stale := app(t, "site")
	stale.SetAnnotations(map[string]string{ChangeIDAnnotation: "01JCDZ7K9M3Q4T5V6W7X8Y9Z0A"})
	c.set(stale)
	changed := app(t, "site")
	if err := unstructured.SetNestedField(changed.Object, "bp-ghost", "spec", "blueprintRef", "name"); err != nil {
		t.Fatal(err)
	}
	out, err = submit(context.Background(), d, []*unstructured.Unstructured{changed}, nil, "")
	if err != nil || out.Applied {
		t.Fatalf("stale object: %+v %v", out, err)
	}
}

func TestSubmitSchemaInvalid(t *testing.T) {
	f, _, d := newTest(t)
	bad := app(t, "site")
	if err := unstructured.SetNestedField(bad.Object, "wordpress", "spec", "blueprintRef", "name"); err != nil {
		t.Fatal(err)
	}
	_, err := submit(context.Background(), d, []*unstructured.Unstructured{app(t, "ok"), bad}, nil, "")
	var se *SchemaError
	if !errors.As(err, &se) || se.Name != "site" || len(se.Errors) == 0 || se.Errors[0].Field != "spec.blueprintRef.name" {
		t.Fatalf("schema error: %v", err)
	}
	if n := len(f.Commits()); n != 0 {
		t.Fatalf("%d commits, want 0", n)
	}
}

func TestSubmitTwoObjectsOneCommit(t *testing.T) {
	f, c, d := newTest(t)
	out, err := submit(context.Background(), d, []*unstructured.Unstructured{app(t, "site"), app(t, "db")}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	commits := f.Commits()
	if len(commits) != 1 || len(commits[0].Paths) != 2 || !out.Applied || len(out.Targets) != 2 {
		t.Fatalf("commits %+v, outcome %+v", commits, out)
	}
	for _, n := range []string{"site", "db"} {
		if obj := c.Get(testCluster, "application", "acme", n); obj == nil || obj.GetAnnotations()[ChangeIDAnnotation] != out.ChangeID {
			t.Errorf("%s is not applied with the change identifier", n)
		}
	}
	// An upsert and a delete in one commit.
	out, err = submit(context.Background(), d, []*unstructured.Unstructured{app(t, "web")}, []Target{appTarget("db")}, "")
	if err != nil || !out.Applied || len(f.Commits()) != 2 || len(f.Commits()[1].Paths) != 2 {
		t.Fatalf("upsert and delete: %+v %v", out, err)
	}
}

func TestSubmitInvalid(t *testing.T) {
	_, _, d := newTest(t)
	cases := map[string]func() error{
		"no objects": func() error { _, err := submit(context.Background(), d, nil, nil, ""); return err },
		"no Git": func() error {
			_, err := Submit(context.Background(), Deps{Cache: d.Cache}, testScope, testRepo, []*unstructured.Unstructured{app(t, "a")}, nil, appPath, "s", testUser, gitwrite.ViaAPI, "", nil)
			return err
		},
		"no pathOf": func() error {
			_, err := Submit(context.Background(), d, testScope, testRepo, []*unstructured.Unstructured{app(t, "a")}, nil, nil, "s", testUser, gitwrite.ViaAPI, "", nil)
			return err
		},
		"delete without path": func() error {
			_, err := submit(context.Background(), d, nil, []Target{{Kind: "Application", Name: "a"}}, "")
			return err
		},
	}
	for name, run := range cases {
		if err := run(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A path outside the scope is refused by the library before any commit.
	_, err := Submit(context.Background(), d, testScope, testRepo, []*unstructured.Unstructured{app(t, "a")}, nil, func(*unstructured.Unstructured) string { return "regions/a.yaml" }, "s", testUser, gitwrite.ViaAPI, "", nil)
	if !errors.Is(err, gitwrite.ErrForbiddenPath) {
		t.Errorf("path outside the scope: %v", err)
	}
}

// racer is a writer that changes a file between Submit's read and its commit.
type racer struct {
	*fake.Writer
	path string
}

func (r racer) Commit(ctx context.Context, c gitwrite.Change) (gitwrite.Result, error) {
	r.Put(testRepo, r.path, []byte("kind: Application\nmetadata:\n  name: other\n"))
	return r.Writer.Commit(ctx, c)
}

// TestSubmitIfMatchRace: the If-Match of the caller also binds the commit
// itself, so a file that another writer changes after Submit has read it
// is a conflict and is not overwritten.
func TestSubmitIfMatchRace(t *testing.T) {
	f, _, d := newTest(t)
	first, err := submit(context.Background(), d, []*unstructured.Unstructured{app(t, "site")}, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	d.Git = racer{Writer: f.Writer, path: "intent/applications/site.yaml"}
	changed := app(t, "site")
	if err := unstructured.SetNestedField(changed.Object, "bp-ghost", "spec", "blueprintRef", "name"); err != nil {
		t.Fatal(err)
	}
	_, err = submit(context.Background(), d, []*unstructured.Unstructured{changed}, nil, first.ETag)
	if !gitwrite.IsConflict(err) {
		t.Fatalf("a file changed after the read: %v", err)
	}
	if n := len(f.Commits()); n != 1 {
		t.Fatalf("%d commits, want 1", n)
	}
}
