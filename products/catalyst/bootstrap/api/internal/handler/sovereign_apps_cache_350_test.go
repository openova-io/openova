// CTL-350 — GET /api/v1/sovereign/apps reads the informer cache.
//
// What this file proves:
//
//  1. With the cache synced on a Sovereign, the handler makes no live LIST
//     and answers with the same bytes as the live path for the same objects.
//  2. A kind whose informer has not synced (its LIST fails, as when the CRD
//     is not installed yet) is listed live, once, and only that kind.
//  3. On the mothership (SOVEREIGN_FQDN unset) the handler never reads the
//     cache, which holds other Sovereigns' clusters there.
//  4. Objects from the cache come in the API server's order, namespace/name.
package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakek8s "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

const appsCacheFQDN = "ctl350.example.test"

var appsCacheKinds = map[string]schema.GroupVersionResource{
	"helmrelease": helmReleaseGVR,
	"httproute":   httpRouteGVR,
	"application": applicationGVR,
}

// appsCacheObjects is one cluster state whose answer does not depend on the
// order of a LIST: three HelmReleases, one HTTPRoute, and one Application
// that adopts the HelmRelease the route belongs to.
func appsCacheObjects() []runtime.Object {
	gitea := makeHR("bp-gitea", "flux-system", "True")
	_ = unstructured.SetNestedField(gitea.Object, "gitea", "spec", "targetNamespace")
	_ = unstructured.SetNestedField(gitea.Object, "gitea", "spec", "releaseName")
	app := makeApplication("ctl350-app", "org-acme", "acme-prod")
	_ = unstructured.SetNestedField(app.Object, "acme", "spec", "organizationRef")
	_ = unstructured.SetNestedField(app.Object, "bp-gitea", "spec", "blueprintRef", "name")
	_ = unstructured.SetNestedField(app.Object, "bp-gitea", "spec", "helmRelease", "name")
	_ = unstructured.SetNestedField(app.Object, "Ready", "status", "phase")
	return []runtime.Object{
		makeHR("bp-cilium", "flux-system", "True"),
		makeHR("bp-keycloak", "flux-system", "False"),
		gitea,
		makeHTTPRoute("gitea", "gitea", []string{"gitea." + appsCacheFQDN}),
		app,
	}
}

func appsCacheDyn(objs []runtime.Object) *dynamicfake.FakeDynamicClient {
	return dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		helmReleaseGVR: "HelmReleaseList",
		httpRouteGVR:   "HTTPRouteList",
		applicationGVR: "ApplicationList",
	}, objs...)
}

// appsCacheRig returns a handler whose live client holds liveObjs, and the
// resources that client was asked to LIST. When cachedKinds is not empty the
// handler also has an informer cache: one cluster, named as a Sovereign
// names its own, that watches a second client holding cacheObjs. A kind
// written with a leading "!" is registered, but its LIST fails in that
// second client, so its informer never syncs.
func appsCacheRig(t *testing.T, liveObjs, cacheObjs []runtime.Object, cachedKinds ...string) (*Handler, *[]string) {
	t.Helper()
	lists := &[]string{}
	live := appsCacheDyn(liveObjs)
	live.PrependReactor("list", "*", func(a clienttesting.Action) (bool, runtime.Object, error) {
		*lists = append(*lists, a.GetResource().Resource)
		return false, nil, nil
	})
	h := NewWithPDM(silentLogger(), &fakePDM{})
	h.SetSovereignDepsFactory(func() (*sovereignDeps, error) {
		return &sovereignDeps{core: fakek8s.NewSimpleClientset(), dyn: live}, nil
	})
	if len(cachedKinds) == 0 {
		return h, lists
	}
	reg := k8scache.NewRegistry()
	watched := appsCacheDyn(cacheObjs)
	var synced []string
	for _, kind := range cachedKinds {
		name := strings.TrimPrefix(kind, "!")
		if err := reg.Add(k8scache.Kind{Name: name, GVR: appsCacheKinds[name], Namespaced: true}); err != nil {
			t.Fatalf("registry add %s: %v", name, err)
		}
		if name == kind {
			synced = append(synced, name)
			continue
		}
		watched.PrependReactor("list", appsCacheKinds[name].Resource, func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, errors.New("the server could not find the requested resource")
		})
	}
	const cluster = "sovereign-" + appsCacheFQDN
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger:   silentLogger(),
		Registry: reg,
		Clusters: []k8scache.ClusterRef{{ID: cluster, DynamicClient: watched, CoreClient: fakek8s.NewSimpleClientset()}},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(f.Stop)
	deadline := time.Now().Add(10 * time.Second)
	for _, kind := range synced {
		for !f.Synced()[cluster][kind] {
			if time.Now().After(deadline) {
				t.Fatalf("the informer of %s did not sync", kind)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	return h, lists
}

func appsCacheGet(t *testing.T, h *Handler) string {
	t.Helper()
	w := httptest.NewRecorder()
	h.HandleSovereignApps(w, httptest.NewRequest(http.MethodGet, "/api/v1/sovereign/apps", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	return w.Body.String()
}

func appsCacheWantLists(t *testing.T, got *[]string, want ...string) {
	t.Helper()
	have := append([]string{}, *got...)
	sort.Strings(have)
	if !reflect.DeepEqual(have, append([]string{}, want...)) {
		t.Errorf("live LIST calls = %v, want %v", have, want)
	}
}

func TestSovereignAppsCache_SyncedCacheMakesNoLiveListAndGivesTheSameAnswer(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", appsCacheFQDN)
	objs := appsCacheObjects()

	live, liveLists := appsCacheRig(t, objs, nil)
	want := appsCacheGet(t, live)
	appsCacheWantLists(t, liveLists, "applications", "helmreleases", "httproutes")
	for _, s := range []string{`"id":"ctl350-app"`, `"org":"acme"`, `"environment":"acme-prod"`, `"externalURL":"https://gitea.` + appsCacheFQDN + `"`} {
		if !strings.Contains(want, s) {
			t.Fatalf("the live answer lacks %s, so the comparison below would prove nothing: %s", s, want)
		}
	}

	cached, cachedLists := appsCacheRig(t, nil, objs, "helmrelease", "httproute", "application")
	for round := 1; round <= 3; round++ {
		if got := appsCacheGet(t, cached); got != want {
			t.Fatalf("round %d: the answer from the cache differs from the live answer\ncache: %s\nlive:  %s", round, got, want)
		}
	}
	appsCacheWantLists(t, cachedLists)
}

func TestSovereignAppsCache_AKindWhoseInformerHasNotSyncedIsListedLiveOnce(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", appsCacheFQDN)
	objs := appsCacheObjects()
	live, _ := appsCacheRig(t, objs, nil)
	want := appsCacheGet(t, live)

	h, lists := appsCacheRig(t, objs, objs, "helmrelease", "httproute", "!application")
	if got := appsCacheGet(t, h); got != want {
		t.Fatalf("the answer differs from the live answer\ngot:  %s\nlive: %s", got, want)
	}
	appsCacheWantLists(t, lists, "applications")
}

func TestSovereignAppsCache_TheMothershipNeverReadsTheCache(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "")
	other := []runtime.Object{makeApplication("other-sovereign-app", "org-other", "other-prod")}
	h, lists := appsCacheRig(t, appsCacheObjects(), other, "helmrelease", "httproute", "application")
	got := appsCacheGet(t, h)
	appsCacheWantLists(t, lists, "applications", "helmreleases", "httproutes")
	if strings.Contains(got, "other-sovereign-app") || !strings.Contains(got, `"id":"ctl350-app"`) {
		t.Fatalf("the answer must come from the live client alone: %s", got)
	}
}

func TestSovereignAppsCache_ObjectsComeInNamespaceSlashNameOrder(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", appsCacheFQDN)
	objs := []runtime.Object{
		makeApplication("three", "b", "e"),
		makeApplication("one", "a", "e"),
		makeApplication("two", "a-b", "e"),
	}
	h, lists := appsCacheRig(t, nil, objs, "helmrelease", "httproute", "application")
	for round := 1; round <= 5; round++ {
		got := appsCacheGet(t, h)
		two, one, three := strings.Index(got, `"id":"two"`), strings.Index(got, `"id":"one"`), strings.Index(got, `"id":"three"`)
		// "a-b/two" sorts before "a/one": the hyphen is below the slash.
		if two < 0 || two >= one || one >= three {
			t.Fatalf("round %d: want the cards in the order two, one, three; got offsets %d, %d, %d in %s", round, two, one, three, got)
		}
	}
	appsCacheWantLists(t, lists)
}
