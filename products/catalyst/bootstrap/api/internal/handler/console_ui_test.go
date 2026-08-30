package handler

// console_ui_test.go — EPIC #6723 lane C: the sidebar mapping layer.
//
// Table-driven, cluster-free tests for the two pure seams
// (validateSidebarOverrides, mergeSidebarEntries + the projections) plus one
// round-trip through the real handlers against a fake dynamic client behind
// a started k8scache.Factory — so the ConfigMap create/update path and the
// merged GET are exercised by the same code the console calls, not by a
// re-derivation of it.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kfake "k8s.io/client-go/kubernetes/fake"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/auth"
	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/k8scache"
)

func intPtr(i int) *int { return &i }

// ── fixtures ─────────────────────────────────────────────────────────────

func blueprintCR(name string, consoleUI map[string]interface{}, endpoints []interface{}) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("catalyst.openova.io/v1")
	u.SetKind("Blueprint")
	u.SetName(name)
	spec := map[string]interface{}{"version": "1.0.0"}
	if consoleUI != nil {
		spec["consoleUI"] = consoleUI
	}
	if endpoints != nil {
		spec["endpoints"] = endpoints
	}
	_ = unstructured.SetNestedMap(u.Object, spec, "spec")
	return u
}

func applicationCR(name, ns, blueprint string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion("apps.openova.io/v1")
	u.SetKind("Application")
	u.SetName(name)
	u.SetNamespace(ns)
	_ = unstructured.SetNestedMap(u.Object, map[string]interface{}{
		"blueprintRef":   map[string]interface{}{"name": blueprint, "version": "1.0.0"},
		"environmentRef": ns + "-prod",
	}, "spec")
	return u
}

var (
	agenityConsoleUI = map[string]interface{}{
		"sidebarEntry": true,
		"sidebarLabel": "Agenity",
		"sidebarRoute": "/apps/bp-agenity/dashboard",
		"sidebarOrder": int64(40),
		"sidebarIcon":  "M3 4h18",
	}
	uiEndpoints  = []interface{}{map[string]interface{}{"name": "console", "launchDefault": true, "ssoEnabled": true}}
	apiEndpoints = []interface{}{map[string]interface{}{"name": "api", "protocol": "https"}}
)

// ── validation ───────────────────────────────────────────────────────────

func TestConsoleUI_ValidateSidebarOverrides(t *testing.T) {
	hosts := []string{"hw310.omani.works", "omani.homes"}
	cases := []struct {
		name     string
		ov       SidebarOverrides
		hosts    []string
		wantOK   bool
		wantHint string // substring one of the problems must carry
	}{
		{name: "empty is valid", ov: SidebarOverrides{}, hosts: hosts, wantOK: true},
		{name: "blueprint id enable only", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-agenity", Enabled: true}}}, hosts: hosts, wantOK: true},
		{name: "application id with parent + order + label + console route", ov: SidebarOverrides{Entries: []SidebarOverride{
			{ID: "app:grafana", Enabled: true, Label: "Observability", Route: "/app/grafana", Order: intPtr(10), Parent: "cloud"},
		}}, hosts: hosts, wantOK: true},
		{name: "https on the sovereign fqdn", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "app:grafana", Route: "https://grafana.hw310.omani.works/d/home"}}}, hosts: hosts, wantOK: true},
		{name: "https on an org pool subdomain", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "app:agenity", Route: "https://agenity.acme.omani.homes"}}}, hosts: hosts, wantOK: true},
		{name: "order 0 and 100 are inclusive bounds", ov: SidebarOverrides{Entries: []SidebarOverride{
			{ID: "bp-a", Order: intPtr(0)}, {ID: "bp-b", Order: intPtr(100)},
		}}, hosts: hosts, wantOK: true},
		{name: "label exactly 40 runes", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Label: "0123456789012345678901234567890123456789"}}}, hosts: hosts, wantOK: true},

		{name: "missing id", ov: SidebarOverrides{Entries: []SidebarOverride{{Enabled: true}}}, hosts: hosts, wantHint: ".id: required"},
		{name: "id with uppercase", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "BP-Agenity"}}}, hosts: hosts, wantHint: ".id: must be a Blueprint name"},
		{name: "id with a slash", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "app:acme/grafana"}}}, hosts: hosts, wantHint: ".id: must be a Blueprint name"},
		{name: "duplicate id", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a"}, {ID: "bp-a"}}}, hosts: hosts, wantHint: "duplicate id bp-a"},
		{name: "label 41 runes", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Label: "01234567890123456789012345678901234567890"}}}, hosts: hosts, wantHint: ".label: at most 40 characters"},
		{name: "label with control char", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Label: "bad\x01label"}}}, hosts: hosts, wantHint: ".label: control characters"},
		{name: "route without leading slash", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Route: "apps/x"}}}, hosts: hosts, wantHint: ".route: must start with /"},
		{name: "route protocol-relative", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Route: "//evil.example/x"}}}, hosts: hosts, wantHint: "must not start with //"},
		{name: "route http not https", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Route: "http://grafana.hw310.omani.works"}}}, hosts: hosts, wantHint: ".route: must start with /"},
		{name: "route https off-domain", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Route: "https://evil.example/x"}}}, hosts: hosts, wantHint: "not on one of this Sovereign's parent domains"},
		{name: "route https suffix-spoof is off-domain", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Route: "https://notomani.homes/x"}}}, hosts: hosts, wantHint: "not on one of this Sovereign's parent domains"},
		{name: "route https with no known hosts fails closed", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Route: "https://grafana.hw310.omani.works"}}}, hosts: nil, wantHint: "none is known"},
		{name: "route with whitespace", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Route: "/apps/x y"}}}, hosts: hosts, wantHint: "whitespace"},
		{name: "order 101", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Order: intPtr(101)}}}, hosts: hosts, wantHint: ".order: must be between 0 and 100"},
		{name: "order -1", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Order: intPtr(-1)}}}, hosts: hosts, wantHint: ".order: must be between 0 and 100"},
		{name: "parent settings is refused (no sub-nav children)", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Parent: "settings"}}}, hosts: hosts, wantHint: ".parent: \"settings\" is not a mappable menu item"},
		{name: "parent sovereignty is refused (anchor, not a page)", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Parent: "sovereignty"}}}, hosts: hosts, wantHint: ".parent: \"sovereignty\" is not a mappable"},
		{name: "parent unknown", ov: SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-a", Parent: "nope"}}}, hosts: hosts, wantHint: ".parent: \"nope\" is not a mappable"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			problems := validateSidebarOverrides(c.ov, c.hosts)
			if c.wantOK {
				if len(problems) != 0 {
					t.Fatalf("expected valid, got problems: %v", problems)
				}
				return
			}
			if len(problems) == 0 {
				t.Fatalf("expected a problem containing %q, got none", c.wantHint)
			}
			for _, p := range problems {
				if bytes.Contains([]byte(p), []byte(c.wantHint)) {
					return
				}
			}
			t.Fatalf("no problem contains %q; got %v", c.wantHint, problems)
		})
	}
}

func TestConsoleUI_ValidateSidebarOverrides_CapsEntryCount(t *testing.T) {
	ov := SidebarOverrides{Entries: make([]SidebarOverride, sidebarMaxEntries+1)}
	for i := range ov.Entries {
		ov.Entries[i].ID = "bp-x"
	}
	problems := validateSidebarOverrides(ov, nil)
	if len(problems) != 1 || !bytes.Contains([]byte(problems[0]), []byte("at most 200 overrides")) {
		t.Fatalf("expected the single cap problem, got %v", problems)
	}
}

// ── projections + merge ──────────────────────────────────────────────────

func TestConsoleUI_ProjectSidebarEntry(t *testing.T) {
	cases := []struct {
		name    string
		bp      *unstructured.Unstructured
		wantOK  bool
		want    SidebarEntry
		enabled bool
	}{
		{name: "nil", bp: nil},
		{name: "no consoleUI block is not an entry", bp: blueprintCR("bp-plain", nil, uiEndpoints)},
		{
			name:   "opt-in projects enabled with all fields",
			bp:     blueprintCR("bp-agenity", agenityConsoleUI, uiEndpoints),
			wantOK: true,
			want: SidebarEntry{
				ID: "bp-agenity", Label: "Agenity", Route: "/apps/bp-agenity/dashboard", Order: 40, Icon: "M3 4h18",
				Source: "blueprint", Enabled: true,
				DefaultLabel: "Agenity", DefaultRoute: "/apps/bp-agenity/dashboard", DefaultOrder: 40, DefaultEnabled: true,
			},
		},
		{
			name:   "opt-out still projects as a disabled candidate with defaults filled",
			bp:     blueprintCR("bp-shy", map[string]interface{}{"sidebarEntry": false}, nil),
			wantOK: true,
			want: SidebarEntry{
				ID: "bp-shy", Label: "bp-shy", Route: "/apps/bp-shy", Order: 0,
				Source: "blueprint", Enabled: false,
				DefaultLabel: "bp-shy", DefaultRoute: "/apps/bp-shy", DefaultOrder: 0, DefaultEnabled: false,
			},
		},
		{
			name:   "explicit order 0 is honoured (Wave 5.69d)",
			bp:     blueprintCR("bp-top", map[string]interface{}{"sidebarEntry": true, "sidebarOrder": int64(0)}, nil),
			wantOK: true,
			want: SidebarEntry{
				ID: "bp-top", Label: "bp-top", Route: "/apps/bp-top", Order: 0,
				Source: "blueprint", Enabled: true,
				DefaultLabel: "bp-top", DefaultRoute: "/apps/bp-top", DefaultOrder: 0, DefaultEnabled: true,
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := projectSidebarEntry(c.bp)
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v (entry=%+v)", ok, c.wantOK, got)
			}
			if ok && got != c.want {
				t.Fatalf("entry mismatch\n got  %+v\n want %+v", got, c.want)
			}
		})
	}
}

func TestConsoleUI_ProjectApplicationCandidate(t *testing.T) {
	bps := map[string]*unstructured.Unstructured{
		"bp-grafana":  blueprintCR("bp-grafana", nil, uiEndpoints),
		"bp-valkey":   blueprintCR("bp-valkey", nil, apiEndpoints),
		"bp-noeps":    blueprintCR("bp-noeps", nil, nil),
		"bp-uinamed":  blueprintCR("bp-uinamed", nil, []interface{}{map[string]interface{}{"name": "UI"}}),
		"bp-agenity":  blueprintCR("bp-agenity", agenityConsoleUI, uiEndpoints),
		"bp-ssoonly":  blueprintCR("bp-ssoonly", nil, []interface{}{map[string]interface{}{"name": "web", "ssoEnabled": true}}),
		"bp-launcher": blueprintCR("bp-launcher", nil, []interface{}{map[string]interface{}{"name": "web", "launchDefault": true}}),
	}
	cases := []struct {
		name   string
		app    *unstructured.Unstructured
		wantOK bool
		wantID string
		icon   string
	}{
		{name: "nil app", app: nil},
		{name: "unknown blueprint fails closed", app: applicationCR("mystery", "acme", "bp-unknown")},
		{name: "api-only blueprint is not a candidate", app: applicationCR("cache", "acme", "bp-valkey")},
		{name: "blueprint with no endpoints is not a candidate", app: applicationCR("thing", "acme", "bp-noeps")},
		{name: "launchDefault qualifies", app: applicationCR("grafana", "monitoring", "bp-grafana"), wantOK: true, wantID: "app:grafana"},
		{name: "ssoEnabled qualifies", app: applicationCR("portal", "acme", "bp-ssoonly"), wantOK: true, wantID: "app:portal"},
		{name: "launchDefault alone qualifies", app: applicationCR("front", "acme", "bp-launcher"), wantOK: true, wantID: "app:front"},
		{name: "endpoint named ui qualifies case-insensitively", app: applicationCR("shop", "acme", "bp-uinamed"), wantOK: true, wantID: "app:shop"},
		{name: "inherits the blueprint sidebarIcon", app: applicationCR("agenity", "acme", "bp-agenity"), wantOK: true, wantID: "app:agenity", icon: "M3 4h18"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := projectApplicationCandidate(c.app, bps)
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v (%+v)", ok, c.wantOK, got)
			}
			if !ok {
				return
			}
			if got.ID != c.wantID || got.Source != "application" || got.Enabled || got.DefaultEnabled {
				t.Fatalf("candidate shape wrong: %+v", got)
			}
			if got.Route != "/app/"+c.app.GetName() || got.DefaultRoute != got.Route {
				t.Fatalf("candidate route should be the console AppDetail page, got %+v", got)
			}
			if got.Order != 50 || got.DefaultOrder != 50 {
				t.Fatalf("candidate order should default to 50, got %+v", got)
			}
			if got.Icon != c.icon {
				t.Fatalf("icon=%q want %q", got.Icon, c.icon)
			}
		})
	}
}

func TestConsoleUI_CollectSidebarDefaults_DedupesAndOrders(t *testing.T) {
	bps := []*unstructured.Unstructured{
		blueprintCR("bp-zeta", map[string]interface{}{"sidebarEntry": true, "sidebarOrder": int64(70)}, nil),
		blueprintCR("bp-agenity", agenityConsoleUI, uiEndpoints),
		blueprintCR("bp-grafana", nil, uiEndpoints),
	}
	apps := []*unstructured.Unstructured{
		applicationCR("grafana", "org-b", "bp-grafana"),
		applicationCR("grafana", "org-a", "bp-grafana"), // same name, other Org — collapses
		applicationCR("agenity", "org-a", "bp-agenity"),
	}
	got := collectSidebarDefaults(bps, apps)
	ids := make([]string, 0, len(got))
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	want := []string{"bp-agenity", "bp-zeta", "app:agenity", "app:grafana"}
	if len(ids) != len(want) {
		t.Fatalf("ids=%v want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("ids=%v want %v", ids, want)
		}
	}
}

func TestConsoleUI_MergeSidebarEntries(t *testing.T) {
	defaults := func() []SidebarEntry {
		return collectSidebarDefaults(
			[]*unstructured.Unstructured{
				blueprintCR("bp-agenity", agenityConsoleUI, uiEndpoints),
				blueprintCR("bp-grafana", nil, uiEndpoints),
				blueprintCR("bp-shy", map[string]interface{}{"sidebarEntry": false, "sidebarLabel": "Shy", "sidebarOrder": int64(60)}, nil),
			},
			[]*unstructured.Unstructured{applicationCR("grafana", "monitoring", "bp-grafana")},
		)
	}
	find := func(entries []SidebarEntry, id string) SidebarEntry {
		for _, e := range entries {
			if e.ID == id {
				return e
			}
		}
		t.Fatalf("entry %s missing from %+v", id, entries)
		return SidebarEntry{}
	}

	t.Run("no overrides keeps defaults and sorts by order", func(t *testing.T) {
		got := mergeSidebarEntries(defaults(), SidebarOverrides{})
		if len(got) != 3 {
			t.Fatalf("want 3 entries, got %+v", got)
		}
		if got[0].ID != "bp-agenity" || got[1].ID != "app:grafana" || got[2].ID != "bp-shy" {
			t.Fatalf("order wrong: %s %s %s", got[0].ID, got[1].ID, got[2].ID)
		}
		for _, e := range got {
			if e.Overridden || e.Parent != "" {
				t.Fatalf("untouched entry must not read as overridden: %+v", e)
			}
		}
		if !find(got, "bp-agenity").Enabled || find(got, "app:grafana").Enabled || find(got, "bp-shy").Enabled {
			t.Fatalf("default enabled flags wrong: %+v", got)
		}
	})

	t.Run("override enables, renames, re-routes, re-orders and nests", func(t *testing.T) {
		ov := SidebarOverrides{Entries: []SidebarOverride{
			{ID: "app:grafana", Enabled: true, Label: "Observability", Route: "https://grafana.hw310.omani.works", Order: intPtr(5), Parent: "cloud"},
			{ID: "bp-agenity", Enabled: false},
			{ID: "bp-shy", Enabled: true, Order: intPtr(0)},
		}}
		got := mergeSidebarEntries(defaults(), ov)
		g := find(got, "app:grafana")
		if !g.Enabled || g.Label != "Observability" || g.Route != "https://grafana.hw310.omani.works" || g.Order != 5 || g.Parent != "cloud" || !g.Overridden {
			t.Fatalf("grafana override not applied: %+v", g)
		}
		if g.DefaultLabel != "grafana" || g.DefaultRoute != "/app/grafana" || g.DefaultOrder != 50 || g.DefaultEnabled {
			t.Fatalf("defaults must survive the overlay: %+v", g)
		}
		a := find(got, "bp-agenity")
		if a.Enabled || !a.Overridden || a.Label != "Agenity" || a.Route != "/apps/bp-agenity/dashboard" {
			t.Fatalf("agenity disable must keep its label/route: %+v", a)
		}
		// explicit order 0 pins first; then grafana (5); then agenity (40)
		if got[0].ID != "bp-shy" || got[1].ID != "app:grafana" || got[2].ID != "bp-agenity" {
			t.Fatalf("merged order wrong: %s %s %s", got[0].ID, got[1].ID, got[2].ID)
		}
	})

	t.Run("blank label/route and nil order inherit the default", func(t *testing.T) {
		ov := SidebarOverrides{Entries: []SidebarOverride{{ID: "bp-agenity", Enabled: true, Label: "   ", Route: ""}}}
		a := find(mergeSidebarEntries(defaults(), ov), "bp-agenity")
		if a.Label != "Agenity" || a.Route != "/apps/bp-agenity/dashboard" || a.Order != 40 {
			t.Fatalf("blank override fields must inherit: %+v", a)
		}
	})

	t.Run("unknown ids are ignored, invalid parent is dropped", func(t *testing.T) {
		ov := SidebarOverrides{Entries: []SidebarOverride{
			{ID: "app:uninstalled", Enabled: true},
			{ID: "bp-agenity", Enabled: true, Parent: "settings"},
		}}
		got := mergeSidebarEntries(defaults(), ov)
		if len(got) != 3 {
			t.Fatalf("unknown override must not materialise an entry: %+v", got)
		}
		if p := find(got, "bp-agenity").Parent; p != "" {
			t.Fatalf("settings can never be a parent, got %q", p)
		}
	})
}

func TestConsoleUI_DecodeSidebarOverrides(t *testing.T) {
	if ov, err := decodeSidebarOverrides(""); err != nil || len(ov.Entries) != 0 || ov.Entries == nil {
		t.Fatalf("empty payload must decode to an empty (non-nil) mapping: %+v %v", ov, err)
	}
	if _, err := decodeSidebarOverrides("{not json"); err == nil {
		t.Fatalf("malformed payload must error")
	}
	ov, err := decodeSidebarOverrides(`{"entries":[{"id":"bp-agenity","enabled":true,"order":0}]}`)
	if err != nil || len(ov.Entries) != 1 || ov.Entries[0].Order == nil || *ov.Entries[0].Order != 0 {
		t.Fatalf("order 0 must round-trip as an explicit pointer: %+v %v", ov, err)
	}
}

// ── handler round trip (fake cluster) ────────────────────────────────────

func newConsoleUIRig(t *testing.T, objs ...runtime.Object) (*Handler, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, gvk := range []schema.GroupVersionKind{
		{Version: "v1", Kind: "ConfigMap"}, {Version: "v1", Kind: "ConfigMapList"},
		{Group: "catalyst.openova.io", Version: "v1", Kind: "Blueprint"}, {Group: "catalyst.openova.io", Version: "v1", Kind: "BlueprintList"},
		{Group: "apps.openova.io", Version: "v1", Kind: "Application"}, {Group: "apps.openova.io", Version: "v1", Kind: "ApplicationList"},
	} {
		if len(gvk.Kind) > 4 && gvk.Kind[len(gvk.Kind)-4:] == "List" {
			scheme.AddKnownTypeWithName(gvk, &unstructured.UnstructuredList{})
			continue
		}
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
	}
	listKinds := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "configmaps"}:                               "ConfigMapList",
		{Group: "catalyst.openova.io", Version: "v1", Resource: "blueprints"}: "BlueprintList",
		{Group: "apps.openova.io", Version: "v1", Resource: "applications"}:   "ApplicationList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, listKinds, objs...)

	reg := k8scache.NewRegistry()
	for _, k := range []k8scache.Kind{
		{Name: "configmap", GVR: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, Namespaced: true, Sensitive: true},
		{Name: "blueprint", GVR: schema.GroupVersionResource{Group: "catalyst.openova.io", Version: "v1", Resource: "blueprints"}, Namespaced: false},
		{Name: "application", GVR: schema.GroupVersionResource{Group: "apps.openova.io", Version: "v1", Resource: "applications"}, Namespaced: true},
	} {
		if err := reg.Add(k); err != nil {
			t.Fatalf("registry add %s: %v", k.Name, err)
		}
	}
	f, err := k8scache.NewFactory(k8scache.Config{
		Logger:   quietHandlerLogger(),
		Registry: reg,
		Clusters: []k8scache.ClusterRef{{ID: "alpha", DynamicClient: dyn, CoreClient: kfake.NewSimpleClientset()}},
	})
	if err != nil {
		t.Fatalf("NewFactory: %v", err)
	}
	if err := f.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(f.Stop)

	wantBPs, wantApps := 0, 0
	for _, o := range objs {
		if u, ok := o.(*unstructured.Unstructured); ok {
			switch u.GetKind() {
			case "Blueprint":
				wantBPs++
			case "Application":
				wantApps++
			}
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		bps, _, _ := f.List("alpha", "blueprint", labels.Everything())
		apps, _, _ := f.List("alpha", "application", labels.Everything())
		if len(bps) >= wantBPs && len(apps) >= wantApps {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	h := NewWithPDM(quietHandlerLogger(), &fakePDM{})
	h.SetK8sCache(f, k8scache.NewSARCache(), "X-Forwarded-User")
	return h, dyn
}

func consoleUIRouter(h *Handler) *chi.Mux {
	r := chi.NewRouter()
	r.Get("/api/v1/sovereigns/{id}/console-ui/sidebar-entries", h.HandleConsoleUISidebarEntries)
	r.Get("/api/v1/sovereigns/{id}/console-ui/sidebar-overrides", h.HandleConsoleUISidebarOverridesGet)
	r.Put("/api/v1/sovereigns/{id}/console-ui/sidebar-overrides", h.HandleConsoleUISidebarOverridesPut)
	return r
}

type sidebarEntriesResponse struct {
	Entries []SidebarEntry `json:"entries"`
	Parents []string       `json:"parents"`
}

func TestConsoleUISidebar_RoundTrip_MergedViewReflectsPut(t *testing.T) {
	t.Setenv("SOVEREIGN_FQDN", "hw310.omani.works")
	h, dyn := newConsoleUIRig(t,
		blueprintCR("bp-agenity", agenityConsoleUI, uiEndpoints),
		blueprintCR("bp-grafana", nil, uiEndpoints),
		blueprintCR("bp-valkey", nil, apiEndpoints),
		applicationCR("grafana", "monitoring", "bp-grafana"),
		applicationCR("cache", "acme", "bp-valkey"),
	)
	r := consoleUIRouter(h)

	get := func() sidebarEntriesResponse {
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/console-ui/sidebar-entries", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET sidebar-entries: %d %s", rec.Code, rec.Body.String())
		}
		var resp sidebarEntriesResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}
	find := func(resp sidebarEntriesResponse, id string) *SidebarEntry {
		for i := range resp.Entries {
			if resp.Entries[i].ID == id {
				return &resp.Entries[i]
			}
		}
		return nil
	}

	// 1. No ConfigMap yet → pure defaults: agenity enabled, grafana a
	//    disabled candidate, the api-only cache app absent.
	before := get()
	if len(before.Parents) == 0 || before.Parents[0] != "dashboard" {
		t.Fatalf("parents must be returned for the Settings dropdown: %+v", before.Parents)
	}
	if a := find(before, "bp-agenity"); a == nil || !a.Enabled || a.Source != "blueprint" || a.Overridden {
		t.Fatalf("agenity default wrong: %+v", a)
	}
	if g := find(before, "app:grafana"); g == nil || g.Enabled || g.Source != "application" || g.Route != "/app/grafana" {
		t.Fatalf("grafana candidate wrong: %+v", g)
	}
	if find(before, "app:cache") != nil {
		t.Fatalf("an api-only Application must not be a candidate")
	}

	// 2. GET overrides before any write → empty list, never 404.
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/console-ui/sidebar-overrides", nil))
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"entries":[]`)) {
		t.Fatalf("GET overrides (none stored): %d %s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte(`hw310.omani.works`)) {
		t.Fatalf("allowedHosts must surface the Sovereign FQDN: %s", rec.Body.String())
	}

	// 3. Invalid PUT is refused with the problems listed and writes nothing.
	bad := `{"entries":[{"id":"app:grafana","enabled":true,"route":"https://evil.example/x","order":500,"parent":"settings"}]}`
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/api/v1/sovereigns/alpha/console-ui/sidebar-overrides", bytes.NewBufferString(bad))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid PUT: want 400, got %d %s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"parent domains", "between 0 and 100", `is not a mappable menu item`} {
		if !bytes.Contains(rec.Body.Bytes(), []byte(want)) {
			t.Fatalf("400 body must name %q: %s", want, rec.Body.String())
		}
	}
	if _, err := dyn.Resource(cmGVR).Namespace(sidebarOverridesNamespace).Get(context.Background(), sidebarOverridesConfigMap, metav1.GetOptions{}); err == nil {
		t.Fatalf("a refused PUT must not create the ConfigMap")
	}

	// 4. Valid PUT creates the ConfigMap; the merged view reflects it.
	good := `{"entries":[
	  {"id":"app:grafana","enabled":true,"label":"Observability","route":"https://grafana.hw310.omani.works/","order":5,"parent":"cloud"},
	  {"id":"bp-agenity","enabled":false}
	]}`
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/sovereigns/alpha/console-ui/sidebar-overrides", bytes.NewBufferString(good))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid PUT: %d %s", rec.Code, rec.Body.String())
	}
	cm, err := dyn.Resource(cmGVR).Namespace(sidebarOverridesNamespace).Get(context.Background(), sidebarOverridesConfigMap, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("ConfigMap must exist after PUT: %v", err)
	}
	if raw, _, _ := unstructured.NestedString(cm.Object, "data", sidebarOverridesDataKey); !bytes.Contains([]byte(raw), []byte(`"Observability"`)) {
		t.Fatalf("ConfigMap payload wrong: %s", raw)
	}

	after := get()
	g := find(after, "app:grafana")
	if g == nil || !g.Enabled || g.Label != "Observability" || g.Parent != "cloud" || g.Order != 5 || g.Route != "https://grafana.hw310.omani.works/" || !g.Overridden {
		t.Fatalf("merged grafana wrong: %+v", g)
	}
	if a := find(after, "bp-agenity"); a == nil || a.Enabled || !a.Overridden {
		t.Fatalf("merged agenity wrong: %+v", a)
	}

	// 5. Second PUT takes the UPDATE path and the read-back is the new list.
	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/api/v1/sovereigns/alpha/console-ui/sidebar-overrides", bytes.NewBufferString(`{"entries":[]}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("second PUT: %d %s", rec.Code, rec.Body.String())
	}
	reset := get()
	if a := find(reset, "bp-agenity"); a == nil || !a.Enabled || a.Overridden {
		t.Fatalf("clearing overrides must restore the default: %+v", a)
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/console-ui/sidebar-overrides", nil))
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte(`"entries":[]`)) {
		t.Fatalf("GET overrides after reset: %d %s", rec.Code, rec.Body.String())
	}
}

func TestConsoleUISidebar_OverridesGate(t *testing.T) {
	h, _ := newConsoleUIRig(t, blueprintCR("bp-agenity", agenityConsoleUI, uiEndpoints))
	r := consoleUIRouter(h)

	do := func(claims *auth.Claims, method string) int {
		req := httptest.NewRequest(method, "/api/v1/sovereigns/alpha/console-ui/sidebar-overrides", bytes.NewBufferString(`{"entries":[]}`))
		req.Header.Set("Content-Type", "application/json")
		if claims != nil {
			req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, claims))
		}
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec.Code
	}

	orgScoped := &auth.Claims{Email: "owner@acme.omani.homes", Tier: auth.OrgScopedTier, Org: "acme"}
	viewer := &auth.Claims{Email: "viewer@hw310.omani.works", Tier: "viewer"}
	admin := &auth.Claims{Email: "admin@hw310.omani.works", Tier: "admin"}

	for _, c := range []struct {
		name   string
		claims *auth.Claims
		method string
		want   int
	}{
		{"org-scoped GET refused", orgScoped, http.MethodGet, http.StatusForbidden},
		{"org-scoped PUT refused", orgScoped, http.MethodPut, http.StatusForbidden},
		{"viewer PUT refused", viewer, http.MethodPut, http.StatusForbidden},
		{"admin GET allowed", admin, http.MethodGet, http.StatusOK},
		{"admin PUT allowed", admin, http.MethodPut, http.StatusOK},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := do(c.claims, c.method); got != c.want {
				t.Fatalf("want %d got %d", c.want, got)
			}
		})
	}

	// The merged view stays readable for any session — the console renders
	// it on every page load; the gate protects the mapping, not the menu.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sovereigns/alpha/console-ui/sidebar-entries", nil)
	req = req.WithContext(context.WithValue(req.Context(), auth.ClaimsKey, viewer))
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("viewer must still read the merged view: %d", rec.Code)
	}
}
