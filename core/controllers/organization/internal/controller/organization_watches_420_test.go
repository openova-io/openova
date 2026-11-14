package controller

// CTL-420 — the Organization reconciler watches its vCluster HelmRelease and
// the console Gateway instead of asking again every 30 seconds.
//
// The last test drives the real SetupWithManager through a manager whose cache
// is controller-runtime's fake informer set: an event on a fake informer runs
// through the very handler and predicate SetupWithManager registered, into the
// controller's work queue, and the reconciler's first log line names the
// Organization it was asked to reconcile.

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	toolscache "k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/cache/informertest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/config"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllertest"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// watched builds an unstructured object of kind gvk with the given labels and
// status conditions (type -> [status, observedGeneration]).
func watched(gvk schema.GroupVersionKind, ns, name string, labels map[string]string, observed int64, conds map[string][2]any) *unstructured.Unstructured {
	u := watchObject(gvk)
	u.SetNamespace(ns)
	u.SetName(name)
	u.SetLabels(labels)
	list := []any{}
	for _, t := range watchedConditionTypes {
		if c, ok := conds[t]; ok {
			list = append(list, map[string]any{"type": t, "status": c[0], "observedGeneration": c[1],
				"lastTransitionTime": "2026-11-14T10:00:00Z"})
		}
	}
	status := map[string]any{"conditions": list}
	if observed > 0 {
		status["observedGeneration"] = observed
	}
	u.Object["status"] = status
	return u
}

func vclusterHR(slug, ready string, observed int64) *unstructured.Unstructured {
	return watched(helmReleaseGVK, slug, "vcluster", map[string]string{OrganizationLabel: slug}, observed,
		map[string][2]any{"Ready": {ready, observed}})
}

func watchedConsoleGateway(ns, name string, programmedGen int64) *unstructured.Unstructured {
	return watched(gatewayGVK, ns, name, nil, 0, map[string][2]any{"Programmed": {"True", programmedGen}})
}

func TestCTL420_ReadinessChangedPredicate(t *testing.T) {
	t.Parallel()
	p := readinessChanged()
	annotated := vclusterHR("acme", "False", 1)
	annotated.SetAnnotations(map[string]string{"reconcile.fluxcd.io/requestedAt": "now"})
	movedTime := watchedConsoleGateway("kube-system", "cilium-gateway-console", 3)
	movedTime.Object["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["lastTransitionTime"] = "2026-11-14T11:00:00Z"
	cases := []struct {
		name     string
		old, new client.Object
		want     bool
	}{
		{"HelmRelease Ready False to True passes", vclusterHR("acme", "False", 1), vclusterHR("acme", "True", 1), true},
		{"HelmRelease observedGeneration change passes", vclusterHR("acme", "False", 1), vclusterHR("acme", "False", 2), true},
		{"HelmRelease change outside readiness does not pass", vclusterHR("acme", "False", 1), annotated, false},
		{"Gateway Programmed observedGeneration change passes", watchedConsoleGateway("kube-system", "cilium-gateway-console", 3), watchedConsoleGateway("kube-system", "cilium-gateway-console", 4), true},
		{"Gateway condition time change alone does not pass", watchedConsoleGateway("kube-system", "cilium-gateway-console", 3), movedTime, false},
	}
	for _, c := range cases {
		if got := p.Update(event.UpdateEvent{ObjectOld: c.old, ObjectNew: c.new}); got != c.want {
			t.Errorf("%s: predicate passed=%v, want %v", c.name, got, c.want)
		}
	}
	if !p.Create(event.CreateEvent{Object: vclusterHR("acme", "False", 1)}) {
		t.Errorf("a create must pass")
	}
	if !p.Delete(event.DeleteEvent{Object: vclusterHR("acme", "True", 1)}) {
		t.Errorf("a delete must pass")
	}
	if p.Generic(event.GenericEvent{Object: vclusterHR("acme", "True", 1)}) {
		t.Errorf("a generic event must not pass")
	}
}

func TestCTL420_LabelledObjectMapsToItsOrganization(t *testing.T) {
	t.Parallel()
	got := organizationForLabelledObject(context.Background(), vclusterHR("acme", "True", 1))
	if len(got) != 1 || got[0].Name != "acme" || got[0].Namespace != "" {
		t.Fatalf("labelled HelmRelease mapped to %v, want the cluster-scoped Organization acme", got)
	}
	plain := watched(helmReleaseGVK, "flux-system", "other", nil, 1, nil)
	if got := organizationForLabelledObject(context.Background(), plain); len(got) != 0 {
		t.Fatalf("unlabelled HelmRelease mapped to %v, want nothing", got)
	}
}

func orgNamed(name, slug, parentDomain string) *orgapi.Organization {
	o := sampleOrg()
	o.Name = name
	o.UID = ""
	o.Spec.Slug = slug
	o.Spec.TenantPublic = orgapi.OrganizationTenantPublic{ParentDomain: parentDomain}
	return o
}

func watchScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := orgapi.AddToScheme(s); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	return s
}

func TestCTL420_ConsoleGatewayMapsToTenantNetworkingOrganizations(t *testing.T) {
	t.Parallel()
	cl := fake.NewClientBuilder().WithScheme(watchScheme(t)).WithObjects(
		orgNamed("zeta", "zeta", "omani.homes"),
		orgNamed("alpha", "alpha", "omani.rest"),
		orgNamed("plain", "plain", ""),
	).Build()
	r := &Reconciler{Client: cl, Log: logr.Discard()}
	ctx := context.Background()

	got := r.organizationsForConsoleGateway(ctx, watchedConsoleGateway("kube-system", "cilium-gateway-console", 1))
	var names []string
	for _, q := range got {
		names = append(names, q.Name)
	}
	if strings.Join(names, ",") != "alpha,zeta" {
		t.Fatalf("console Gateway mapped to %v, want [alpha zeta]: every Organization with a pool parentDomain, sorted", names)
	}
	if got := r.organizationsForConsoleGateway(ctx, watchedConsoleGateway("kube-system", "some-other-gateway", 1)); len(got) != 0 {
		t.Fatalf("another Gateway mapped to %v, want nothing", got)
	}
	if got := r.organizationsForConsoleGateway(ctx, watchedConsoleGateway("default", "cilium-gateway-console", 1)); len(got) != 0 {
		t.Fatalf("a same-named Gateway in another namespace mapped to %v, want nothing", got)
	}
}

// recorder collects the Organizations the reconciler logged "reconcile" for,
// in order, and closes started when the controller logs that its workers run.
type recorder struct {
	mu      sync.Mutex
	orgs    []string
	seen    chan string
	started chan struct{}
	once    sync.Once
}

func (rec *recorder) reconcilerLog(_, args string) {
	if !strings.Contains(args, `"msg"="reconcile"`) {
		return
	}
	i := strings.Index(args, `"organization"="`)
	if i < 0 {
		return
	}
	rest := args[i+len(`"organization"="`):]
	name := rest[:strings.Index(rest, `"`)]
	rec.mu.Lock()
	rec.orgs = append(rec.orgs, name)
	rec.mu.Unlock()
	select {
	case rec.seen <- name:
	default:
	}
}

func (rec *recorder) managerLog(_, args string) {
	if strings.Contains(args, `"msg"="Starting workers"`) {
		rec.once.Do(func() { close(rec.started) })
	}
}

func (rec *recorder) waitFor(t *testing.T, name string) {
	t.Helper()
	deadline := time.After(20 * time.Second)
	for {
		select {
		case got := <-rec.seen:
			if got == name {
				return
			}
		case <-deadline:
			rec.mu.Lock()
			defer rec.mu.Unlock()
			t.Fatalf("no reconcile of Organization %q within 20s; reconciled: %v", name, rec.orgs)
		}
	}
}

func (rec *recorder) reconciled() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.orgs...)
}

func TestCTL420_SetupWithManager_WatchesEnqueueTheOrganization(t *testing.T) {
	scheme := watchScheme(t)
	rec := &recorder{seen: make(chan string, 64), started: make(chan struct{})}
	// One fake informer per watched kind, made before the manager starts: the
	// controller's three sources start at once, and the fake informer set
	// is not safe to fill from several goroutines.
	hrInformer, gwInformer := &controllertest.FakeInformer{}, &controllertest.FakeInformer{}
	informers := &informertest.FakeInformers{Scheme: scheme, InformersByGVK: map[schema.GroupVersionKind]toolscache.SharedIndexInformer{
		orgapi.GroupVersion.WithKind("Organization"): &controllertest.FakeInformer{},
		helmReleaseGVK: hrInformer,
		gatewayGVK:     gwInformer,
	}}
	skip := true
	mgr, err := manager.New(&rest.Config{Host: "http://127.0.0.1:1"}, manager.Options{
		Scheme:     scheme,
		Logger:     funcr.New(rec.managerLog, funcr.Options{}),
		Metrics:    metricsserver.Options{BindAddress: "0"},
		Controller: config.Controller{SkipNameValidation: &skip},
		NewCache: func(*rest.Config, cache.Options) (cache.Cache, error) {
			return informers, nil
		},
	})
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	// "pool-org" engages tenant networking; its slug differs from its name, so
	// its reconcile stops at the slug check and touches nothing else.
	cl := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&orgapi.Organization{}).
		WithObjects(orgNamed("pool-org", "pool-org-mismatch", "omani.homes")).
		Build()
	r := &Reconciler{Client: cl, Log: funcr.New(rec.reconcilerLog, funcr.Options{})}
	if err := r.SetupWithManager(mgr); err != nil {
		t.Fatalf("SetupWithManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- mgr.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	select {
	case <-rec.started:
	case err := <-done:
		t.Fatalf("manager stopped before its controller started: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatalf("the controller did not start within 20s")
	}

	// An update outside readiness must not enqueue: "quiet-org" is sent first,
	// and with one worker its reconcile would come before the one awaited next.
	quietOld, quietNew := vclusterHR("quiet-org", "False", 1), vclusterHR("quiet-org", "False", 1)
	quietNew.SetAnnotations(map[string]string{"reconcile.fluxcd.io/requestedAt": "now"})
	hrInformer.Update(quietOld, quietNew)

	// A Ready change on a labelled HelmRelease enqueues its Organization.
	hrInformer.Update(vclusterHR("hr-org", "False", 1), vclusterHR("hr-org", "True", 1))
	rec.waitFor(t, "hr-org")

	// A Programmed change on the console Gateway enqueues every Organization
	// that engages tenant networking.
	gwInformer.Update(watchedConsoleGateway("kube-system", "cilium-gateway-console", 1),
		watchedConsoleGateway("kube-system", "cilium-gateway-console", 2))
	rec.waitFor(t, "pool-org")

	for _, name := range rec.reconciled() {
		if name == "quiet-org" {
			t.Fatalf("an update outside readiness enqueued its Organization; reconciled: %v", rec.reconciled())
		}
	}
}
