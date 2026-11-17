package gitops

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"
)

// useVariables sets the variables document the renders read, for one test.
func useVariables(t *testing.T, data map[string]string) {
	t.Helper()
	clearCutoverFactEnv(t)
	sovereignVariables.set(data)
	t.Cleanup(func() { sovereignVariables.set(nil) })
}

// The lines a HelmRepository declared before SOV-183, without a cutover fact.
const legacySourceLines = "  url: oci://ghcr.io/openova-io\n  secretRef:\n    name: ghcr-pull"

func TestCatalogSourceFromTheDocument(t *testing.T) {
	cases := []struct {
		name, override string
		doc            map[string]string
		want           string
	}{
		{"no document renders as before", "", nil, legacySourceLines},
		{"kit keys only render as before", "", map[string]string{"SOVEREIGN_FQDN": "alpha.example.org"}, legacySourceLines},
		{"the values of today render the same bytes", "", map[string]string{
			"REGISTRY_HOST": "ghcr.io", "REGISTRY_SCHEME": "https", "REGISTRY_PATH_CHARTS": "openova-io", "REGISTRY_PULL_SECRET": "ghcr-pull",
		}, legacySourceLines},
		{"host, path and pull secret come from the document", "", map[string]string{
			"REGISTRY_HOST": "registry.alpha.example.org", "REGISTRY_SCHEME": "https", "REGISTRY_PATH_CHARTS": "charts", "REGISTRY_PULL_SECRET": "alpha-pull",
		}, "  url: oci://registry.alpha.example.org/charts\n  secretRef:\n    name: alpha-pull"},
		{"absent path and pull secret give the contract defaults", "", map[string]string{"REGISTRY_HOST": "registry.alpha.example.org:5000"},
			"  url: oci://registry.alpha.example.org:5000/openova-io\n  secretRef:\n    name: ghcr-pull"},
		{"blank values give the contract defaults", "", map[string]string{
			"REGISTRY_HOST": "registry.alpha.example.org", "REGISTRY_PATH_CHARTS": "  ", "REGISTRY_PULL_SECRET": "",
		}, "  url: oci://registry.alpha.example.org/openova-io\n  secretRef:\n    name: ghcr-pull"},
		{"scheme http adds insecure", "", map[string]string{"REGISTRY_HOST": "parent.example.org:8080", "REGISTRY_SCHEME": "http"},
			"  url: oci://parent.example.org:8080/openova-io\n  insecure: true\n  secretRef:\n    name: ghcr-pull"},
		{"a scheme the contract does not allow is https", "", map[string]string{"REGISTRY_HOST": "registry.alpha.example.org", "REGISTRY_SCHEME": "ftp"},
			"  url: oci://registry.alpha.example.org/openova-io\n  secretRef:\n    name: ghcr-pull"},
		{"a host with a scheme is not a host", "", map[string]string{"REGISTRY_HOST": "https://registry.alpha.example.org", "REGISTRY_PULL_SECRET": "alpha-pull"},
			"  url: oci://ghcr.io/openova-io\n  secretRef:\n    name: alpha-pull"},
		{"a pull secret that is not a Secret name gives the default", "", map[string]string{"REGISTRY_HOST": "registry.alpha.example.org", "REGISTRY_PULL_SECRET": "Alpha Pull"},
			"  url: oci://registry.alpha.example.org/openova-io\n  secretRef:\n    name: ghcr-pull"},
		{"the cutover override still comes first", "oci://registry.beta.example.org/openova-io", map[string]string{
			"REGISTRY_HOST": "ghcr.io", "REGISTRY_SCHEME": "http", "REGISTRY_PULL_SECRET": "alpha-pull",
		}, "  url: oci://registry.beta.example.org/openova-io\n  secretRef:\n    name: alpha-pull"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useVariables(t, tc.doc)
			t.Setenv("CATALYST_LOCAL_REGISTRY_URL", tc.override)
			if got := catalogSourceLines(); got != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

var helmRepositoryDoc = regexp.MustCompile(`(?m)^kind: HelmRepository$`)

// renderedSources renders an Organization's tree with every kind of emitted
// HelmRepository and returns the number of HelmRepository documents and of the
// given url and secretRef lines in it.
func renderedSources(t *testing.T, url, secret string) (repos, urls, secrets, ghcr int) {
	t.Helper()
	g := NewManifestGenerator("clusters/alpha.example.org/org-tenants")
	out := g.GenerateAllWithAppConfigs("acme", "m", []string{"openclaw", "stalwart-mail", "umami"},
		"pw", map[string]map[string]any{"postgres": {
			"active_hot_standby": true,
			"primary_region":     "hz-fsn-rtz-prod",
			"replica_region":     "hz-hel-rtz-prod",
		}})
	for _, content := range out {
		repos += len(helmRepositoryDoc.FindAllString(content, -1))
		urls += strings.Count(content, "\n  url: "+url+"\n")
		secrets += strings.Count(content, "\n  secretRef:\n    name: "+secret+"\n")
		ghcr += strings.Count(content, "ghcr.io/openova-io")
	}
	return repos, urls, secrets, ghcr
}

func TestEveryEmittedSourceFollowsTheDocument(t *testing.T) {
	useVariables(t, map[string]string{
		"REGISTRY_HOST": "registry.alpha.example.org", "REGISTRY_SCHEME": "https",
		"REGISTRY_PATH_CHARTS": "charts", "REGISTRY_PULL_SECRET": "alpha-pull",
	})
	repos, urls, secrets, ghcr := renderedSources(t, "oci://registry.alpha.example.org/charts", "alpha-pull")
	if repos < 4 {
		t.Fatalf("want at least 4 HelmRepository documents (openclaw, stalwart, both sides of the CNPG pair), got %d", repos)
	}
	if urls != repos || secrets != repos {
		t.Fatalf("every HelmRepository must take url and secretRef from the document: %d documents, %d url lines, %d secretRef lines", repos, urls, secrets)
	}
	if ghcr != 0 {
		t.Fatalf("%d lines still name ghcr.io/openova-io", ghcr)
	}
}

// fakeAPI serves the list and the watch of the ConfigMap. Each watch request
// takes the next script; a script is the events of one watch, written as
// lines, and the watch ends after them.
type fakeAPI struct {
	mu      sync.Mutex
	list    []string // the data of the ConfigMap at each list, as JSON
	watches [][]string
	lists   int
	queries []string
	held    chan string // the query of each watch the server holds open
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Path != "/api/v1/namespaces/flux-system/configmaps" || r.Header.Get("Authorization") != "Bearer token-1" {
		http.Error(w, "unexpected request", http.StatusForbidden)
		return
	}
	f.queries = append(f.queries, r.URL.RawQuery)
	if r.URL.Query().Get("fieldSelector") != "metadata.name=sovereign-variables" {
		http.Error(w, "no field selector", http.StatusBadRequest)
		return
	}
	if r.URL.Query().Get("watch") != "true" {
		data := f.list[min(f.lists, len(f.list)-1)]
		f.lists++
		items := "[]"
		if data != "" {
			items = `[{"metadata":{"resourceVersion":"10"},"data":` + data + `}]`
		}
		_, _ = fmt.Fprintf(w, `{"metadata":{"resourceVersion":"%d"},"items":%s}`, 10*f.lists, items)
		return
	}
	if len(f.watches) == 0 {
		// The server holds the watch open until the client goes away.
		if f.held != nil {
			f.held <- r.URL.RawQuery
		}
		f.mu.Unlock()
		<-r.Context().Done()
		f.mu.Lock()
		return
	}
	events := f.watches[0]
	f.watches = f.watches[1:]
	for _, e := range events {
		_, _ = fmt.Fprintln(w, e)
	}
}

func modified(rv, data string) string {
	return `{"type":"MODIFIED","object":{"metadata":{"resourceVersion":"` + rv + `"},"data":` + data + `}}`
}

// startFake runs the watch against f and returns a function that waits until
// the store holds a REGISTRY_HOST of want.
func startFake(t *testing.T, f *fakeAPI) func(want string) {
	t.Helper()
	clearCutoverFactEnv(t)
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	store := &sovereignVariables
	t.Cleanup(func() { store.set(nil) })
	changed := make(chan struct{}, 64)
	w := &variablesWatch{
		api: &kubeAPI{
			base:   srv.URL,
			token:  func() (string, error) { return "token-1", nil },
			client: srv.Client(),
			stream: srv.Client(),
		},
		store:  store,
		onRead: func() { changed <- struct{}{} },
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.run(ctx, "")
	return func(want string) {
		t.Helper()
		deadline := time.After(20 * time.Second)
		for store.get("REGISTRY_HOST") != want {
			select {
			case <-changed:
			case <-deadline:
				t.Fatalf("the store never held REGISTRY_HOST %q; it holds %q", want, store.get("REGISTRY_HOST"))
			}
		}
	}
}

func TestAChangeOfTheConfigMapChangesTheNextRender(t *testing.T) {
	f := &fakeAPI{
		list:    []string{`{"REGISTRY_HOST":"ghcr.io"}`},
		watches: [][]string{{modified("11", `{"REGISTRY_HOST":"registry.alpha.example.org","REGISTRY_PULL_SECRET":"alpha-pull"}`)}},
		held:    make(chan string, 1),
	}
	wait := startFake(t, f)
	wait("ghcr.io")
	wait("registry.alpha.example.org")
	if got, want := catalogSourceLines(), "  url: oci://registry.alpha.example.org/openova-io\n  secretRef:\n    name: alpha-pull"; got != want {
		t.Fatalf("after the change the render gives\n%s\nwant\n%s", got, want)
	}
	var second string
	select {
	case second = <-f.held:
	case <-time.After(20 * time.Second):
		t.Fatal("no second watch was started")
	}
	if !strings.Contains(second, "resourceVersion=11") {
		t.Fatalf("the second watch must start from resource version 11; its query is %s", second)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lists != 1 {
		t.Fatalf("a watch that ends normally goes on from its resource version: want 1 list, got %d", f.lists)
	}
}

func TestAWatchErrorListsAgain(t *testing.T) {
	f := &fakeAPI{
		list:    []string{`{"REGISTRY_HOST":"ghcr.io"}`, `{"REGISTRY_HOST":"registry.beta.example.org"}`},
		watches: [][]string{{`{"type":"ERROR","object":{"kind":"Status","code":410}}`}},
	}
	wait := startFake(t, f)
	wait("registry.beta.example.org")
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lists != 2 {
		t.Fatalf("want a second list after the watch error, got %d lists", f.lists)
	}
}

func TestADeletedConfigMapRendersAsBefore(t *testing.T) {
	f := &fakeAPI{
		list:    []string{`{"REGISTRY_HOST":"registry.alpha.example.org"}`},
		watches: [][]string{{`{"type":"DELETED","object":{"metadata":{"resourceVersion":"12"},"data":{"REGISTRY_HOST":"registry.alpha.example.org"}}}`}},
	}
	wait := startFake(t, f)
	wait("registry.alpha.example.org")
	wait("")
	if got := catalogSourceLines(); got != legacySourceLines {
		t.Fatalf("without the ConfigMap the render must be as before; got\n%s", got)
	}
}

func TestAnAbsentConfigMapRendersAsBefore(t *testing.T) {
	f := &fakeAPI{list: []string{""}}
	clearCutoverFactEnv(t)
	sovereignVariables.set(map[string]string{"REGISTRY_HOST": "registry.stale.example.org"})
	wait := startFake(t, f)
	wait("")
	if got := catalogSourceLines(); got != legacySourceLines {
		t.Fatalf("an absent ConfigMap must render as before; got\n%s", got)
	}
}

func TestOutsideAClusterNothingIsRead(t *testing.T) {
	useVariables(t, nil)
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	t.Setenv("KUBERNETES_SERVICE_PORT", "")
	if _, err := inClusterAPI(); err == nil {
		t.Fatal("inClusterAPI must fail without KUBERNETES_SERVICE_HOST")
	}
	StartVariablesWatch(context.Background())
	if got := catalogSourceLines(); got != legacySourceLines {
		t.Fatalf("outside a cluster the render must be as before; got\n%s", got)
	}
}
