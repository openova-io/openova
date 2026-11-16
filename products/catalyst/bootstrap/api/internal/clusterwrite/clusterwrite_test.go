package clusterwrite

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/audit"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
)

func TestOwner(t *testing.T) {
	cm := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	obj := func(labels map[string]string, refs ...metav1.OwnerReference) *unstructured.Unstructured {
		o := &unstructured.Unstructured{}
		o.SetNamespace("shop")
		o.SetLabels(labels)
		o.SetOwnerReferences(refs)
		return o
	}
	for _, c := range []struct {
		gvr   schema.GroupVersionResource
		live  *unstructured.Unstructured
		owner string
	}{
		{cm, obj(map[string]string{"kustomize.toolkit.fluxcd.io/name": "shop", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}), "Kustomization flux-system/shop"},
		{cm, obj(map[string]string{"kustomize.toolkit.fluxcd.io/name": "shop"}), "Kustomization shop"},
		{cm, obj(map[string]string{"helm.toolkit.fluxcd.io/name": "wp", "helm.toolkit.fluxcd.io/namespace": "shop"}), "HelmRelease shop/wp"},
		{cm, obj(nil, metav1.OwnerReference{APIVersion: "helm.toolkit.fluxcd.io/v2", Kind: "HelmRelease", Name: "wp"}), "HelmRelease shop/wp"},
		{cm, obj(map[string]string{"app.kubernetes.io/managed-by": "Flux"}), "Flux"},
		{schema.GroupVersionResource{Group: "apps.openova.io", Version: "v1", Resource: "applications"}, obj(nil), "the intent lock (applications.apps.openova.io)"},
		{schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}, nil, "the intent lock (helmreleases.helm.toolkit.fluxcd.io)"},
		{cm, obj(nil, metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "wp"}), ""},
		{cm, obj(map[string]string{"app.kubernetes.io/managed-by": "Helm"}), ""},
		{cm, nil, ""},
	} {
		owner, owned := Owner(c.gvr, c.live)
		if owner != c.owner || owned != (c.owner != "") {
			t.Errorf("Owner(%s, %v) = (%q, %v), want %q", c.gvr.Resource, c.live, owner, owned, c.owner)
		}
	}
}

// The locked kinds are those of the intent lock of the platform chart.
func TestLockedKindsEqualTheIntentLockOfTheChart(t *testing.T) {
	raw, err := os.ReadFile("../../../../chart/values.yaml")
	if err != nil {
		t.Fatalf("read the chart values: %v", err)
	}
	var values struct {
		IntentLock struct {
			Resources map[string][]struct {
				APIGroup  string   `json:"apiGroup"`
				Resources []string `json:"resources"`
			} `json:"resources"`
		} `json:"intentLock"`
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatalf("parse the chart values: %v", err)
	}
	chart := map[schema.GroupResource]bool{}
	for _, rules := range values.IntentLock.Resources {
		for _, rule := range rules {
			for _, resource := range rule.Resources {
				chart[schema.GroupResource{Group: rule.APIGroup, Resource: resource}] = true
			}
		}
	}
	if len(chart) != len(lockedKinds) {
		t.Errorf("the chart locks %d kinds, lockedKinds has %d", len(chart), len(lockedKinds))
	}
	for gr := range chart {
		if !lockedKinds[gr] {
			t.Errorf("%s is in the intent lock of the chart and not in lockedKinds", gr)
		}
	}
}

func TestOwnerTierAndActor(t *testing.T) {
	for claims, want := range map[*auth.Claims]bool{
		nil:               true,
		{Tier: " Owner "}: true,
		{RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-owner"}}}:                true,
		{Tier: "admin", RealmAccess: auth.RealmAccess{Roles: []string{"catalyst-admin"}}}: false,
		{}: false,
	} {
		if OwnerTier(claims) != want {
			t.Errorf("OwnerTier(%+v) != %v", claims, want)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if a := Actor(&auth.Claims{Sub: "u-1"}, r); a != "u-1" {
		t.Errorf("Actor with a subject only = %q", a)
	}
	if a := Actor(nil, r); a != "operator" {
		t.Errorf("Actor without claims or header = %q", a)
	}
	r.Header.Set("X-User-Email", "ops@example.test")
	if a := Actor(nil, r); a != "ops@example.test" {
		t.Errorf("Actor from the header = %q", a)
	}
}

type recorder struct {
	events []audit.Event
	err    error
}

func (p *recorder) Publish(_ context.Context, ev audit.Event) error {
	p.events = append(p.events, ev)
	return p.err
}

func request(tier string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	return r.WithContext(context.WithValue(r.Context(), auth.ClaimsKey, &auth.Claims{Tier: tier, Email: "owner@example.test"}))
}

var write = Write{Cluster: "alpha", Verb: "scale", Resource: "deployments", Namespace: "shop", Name: "wp", Reason: "load test of the shop"}

func TestBreakGlassRefuses(t *testing.T) {
	short := write
	short.Reason = " too short "
	for _, c := range []struct {
		tier  string
		wr    Write
		bus   *audit.Bus
		code  int
		error string
	}{
		{"admin", write, audit.NewBus(audit.BusConfig{Publisher: &recorder{}}), 403, "break-glass-owner-only"},
		{"owner", short, audit.NewBus(audit.BusConfig{Publisher: &recorder{}}), 400, "break-glass-reason-required"},
		{"owner", write, audit.NewBus(audit.BusConfig{}), 503, "audit-unavailable"},
		{"owner", write, audit.NewBus(audit.BusConfig{Publisher: &recorder{err: errors.New("no responders")}}), 503, "audit-unavailable"},
	} {
		rec := httptest.NewRecorder()
		if _, ok := BreakGlass(rec, request(c.tier), c.bus, c.wr); ok || rec.Code != c.code ||
			!strings.Contains(rec.Body.String(), `"error":"`+c.error+`"`) {
			t.Errorf("tier %s: got ok=%v %d %s, want %d %s", c.tier, ok, rec.Code, rec.Body.String(), c.code, c.error)
		}
	}
	rec := httptest.NewRecorder()
	if finish, ok := BreakGlass(rec, request("owner"), nil, write); !ok || rec.Body.Len() != 0 {
		t.Errorf("without a bus the write must go ahead: %d %s", rec.Code, rec.Body.String())
	} else {
		finish(context.Background(), nil)
	}
}

// Both rules record an intent event before the write and a result event
// after it.
func TestBreakGlassAndOperationRecordIntentThenResult(t *testing.T) {
	for _, c := range []struct {
		start      func(*audit.Bus) (Finish, bool)
		auditType  string
		detail     string
		writeError error
	}{
		{func(b *audit.Bus) (Finish, bool) {
			return BreakGlass(httptest.NewRecorder(), request("owner"), b, write)
		},
			"break-glass", "scale deployments shop/wp: load test of the shop", nil},
		{func(b *audit.Bus) (Finish, bool) {
			return Operation(httptest.NewRecorder(), request("admin"), b, Op{Cluster: "alpha", Name: "restart", Target: "deployments shop/wp"})
		}, "operation-restart", "restart deployments shop/wp", errors.New("conflict")},
	} {
		pub := &recorder{}
		finish, ok := c.start(audit.NewBus(audit.BusConfig{Publisher: pub}))
		if !ok || len(pub.events) != 1 {
			t.Fatalf("%s: ok=%v events=%d", c.auditType, ok, len(pub.events))
		}
		intent := pub.events[0]
		if intent.AuditType != c.auditType || intent.Phase != audit.PhaseIntent || intent.Actor != "owner@example.test" ||
			intent.SovereignID != "alpha" || intent.Detail != c.detail || intent.ID == "" {
			t.Fatalf("%s: intent event %+v", c.auditType, intent)
		}
		finish(context.Background(), c.writeError)
		want := map[bool]string{true: "ok", false: "error"}[c.writeError == nil]
		if len(pub.events) != 2 || pub.events[1].Phase != audit.PhaseResult || pub.events[1].Result != want ||
			pub.events[1].ID == intent.ID || !strings.Contains(pub.events[1].Detail, "(intent "+intent.ID+")") {
			t.Fatalf("%s: result event %+v", c.auditType, pub.events)
		}
	}
	rec := httptest.NewRecorder()
	if _, ok := Operation(rec, request("admin"), audit.NewBus(audit.BusConfig{}), Op{Name: "exec"}); ok || rec.Code != 503 {
		t.Fatalf("an operation without a durable record must not run: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAllowRefusesAGitOwnedObjectFirst(t *testing.T) {
	rec := httptest.NewRecorder()
	gvr := schema.GroupVersionResource{Group: "dr.openova.io", Version: "v1", Resource: "continuums"}
	if _, ok := Allow(rec, request("owner"), nil, gvr, nil, write); ok || rec.Code != http.StatusConflict ||
		!strings.Contains(rec.Body.String(), `"error":"managed-by-git"`) ||
		!strings.Contains(rec.Body.String(), `"owner":"the intent lock (continuums.dr.openova.io)"`) {
		t.Fatalf("got ok=%v %d %s", ok, rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	if _, ok := Allow(rec, request("owner"), nil, schema.GroupVersionResource{Version: "v1", Resource: "pods"}, nil, write); !ok {
		t.Fatalf("an object that Git does not own was refused: %d %s", rec.Code, rec.Body.String())
	}
}
