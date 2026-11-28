package suite

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

// fixtureConsole serves recorded answers of the console API. answers maps
// "METHOD request-URI" to the answers in turn; the last one repeats. A call
// with no recorded answer gets HTTP 599. It records every call and the body
// of every call that had one.
type fixtureConsole struct {
	mu      sync.Mutex
	answers map[string][]string // "status body"
	calls   []string
	bodies  []string
}

func (c *fixtureConsole) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := r.Method + " " + r.URL.RequestURI()
	c.calls = append(c.calls, key)
	if b, _ := io.ReadAll(r.Body); len(b) > 0 {
		c.bodies = append(c.bodies, key+" "+string(b))
	}
	q := c.answers[key]
	if len(q) == 0 {
		w.WriteHeader(599)
		return
	}
	a := q[0]
	if len(q) > 1 {
		c.answers[key] = q[1:]
	}
	status, body, _ := strings.Cut(a, " ")
	code, err := strconv.Atoi(status)
	if err != nil {
		code = 599
	}
	w.WriteHeader(code)
	_, _ = io.WriteString(w, body)
}

// fixtureRun starts the console with answers and runs fn against it with the
// waits of a test. It returns the calls, the bodies and the error of fn.
func fixtureRun(t *testing.T, answers map[string][]string, fn func(ctx context.Context, cc *CheckContext) error) (string, string, error) {
	t.Helper()
	saved := fixtureWaits
	fixtureWaits.org, fixtureWaits.app, fixtureWaits.gone, fixtureWaits.every = 300*time.Millisecond, 300*time.Millisecond, 300*time.Millisecond, time.Millisecond
	defer func() { fixtureWaits = saved }()
	c := &fixtureConsole{answers: answers}
	srv := httptest.NewServer(c)
	defer srv.Close()
	cc := &CheckContext{API: api.New(srv.URL, "s"), Deploy: &DeploymentInfo{ID: "dep-1"}, Regions: []kube.Region{{ID: "r-a"}, {ID: "r-b"}}}
	err := fn(context.Background(), cc)
	c.mu.Lock()
	defer c.mu.Unlock()
	return strings.Join(c.calls, ", "), strings.Join(c.bodies, ", "), err
}

func fixtureWant(t *testing.T, what, got, want string) {
	t.Helper()
	if got != want {
		t.Errorf("%s:\n got %s\nwant %s", what, got, want)
	}
}

func fixtureFails(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("error %v, want one that says %q", err, want)
	}
}

const (
	fixtureList    = "GET /api/v1/organizations"
	fixtureListed  = `200 {"items":[{"org_tenant_id":"id-1","subdomain":"gate-a","state":"done","tenant_namespace":"gate-a"},{"org_tenant_id":"id-0","subdomain":"gate-b","state":"deleted"}]}`
	fixtureEnvs    = "GET /api/v1/refs/organizations/gate-a/environments"
	fixtureInstall = "POST /catalyst/v1/apps/instances"
	fixtureApp     = "GET /api/v1/sovereigns/dep-1/applications/wp-1?namespace=gate-a"
	fixtureApps    = "GET /api/v1/sovereigns/dep-1/applications"
	fixtureBySlug  = "GET /api/v1/organizations/gate-a"
	fixtureGone    = `404 {"error":"org-tenant-not-found"}`
)

func TestFixtureOrgEnsure(t *testing.T) {
	ensure := func(slug string) func(context.Context, *CheckContext) error {
		return func(ctx context.Context, cc *CheckContext) error { return EnsureOrganization(ctx, cc, slug) }
	}
	// Pass: gate-b is listed only as deleted, so it is created, and the wait
	// reads its state until done.
	calls, bodies, err := fixtureRun(t, map[string][]string{fixtureList: {fixtureListed},
		"POST /api/v1/organizations":     {`202 {"org_tenant_id":"id-2","subdomain":"gate-b","state":"pending"}`},
		"GET /api/v1/organizations/id-2": {`200 {"state":"vcluster-created"}`, `200 {"state":"done"}`}}, ensure("gate-b"))
	if err != nil {
		t.Fatal(err)
	}
	fixtureWant(t, "calls", calls, "GET /api/v1/organizations, POST /api/v1/organizations, GET /api/v1/organizations/id-2, GET /api/v1/organizations/id-2")
	fixtureWant(t, "bodies", bodies, `POST /api/v1/organizations {"admin_email":"gate-b@acceptance.invalid","kind":"internal","plan_slug":"m","subdomain":"gate-b"}`)

	// Pass without a call: gate-a exists and is done.
	calls, _, err = fixtureRun(t, map[string][]string{fixtureList: {fixtureListed}}, ensure("gate-a"))
	if err != nil {
		t.Fatal(err)
	}
	fixtureWant(t, "calls of an existing Organization", calls, fixtureList)

	// Failures.
	_, _, err = fixtureRun(t, map[string][]string{fixtureList: {`200 {"items":[]}`}, "POST /api/v1/organizations": {`503 {"error":"org-pool-empty"}`}}, ensure("gate-c"))
	fixtureFails(t, err, "POST /api/v1/organizations gate-c: HTTP 503 (want 202): {\"error\":\"org-pool-empty\"}")
	_, _, err = fixtureRun(t, map[string][]string{fixtureList: {`200 {"items":[{"org_tenant_id":"id-3","subdomain":"gate-c","state":"pending"}]}`},
		"GET /api/v1/organizations/id-3": {`200 {"state":"failed","last_error":"vcluster: boom"}`}}, ensure("gate-c"))
	fixtureFails(t, err, "the Organization gate-c is not done: the Organization gate-c is failed: vcluster: boom")
	calls, _, err = fixtureRun(t, nil, ensure("acme"))
	fixtureFails(t, err, `the slug "acme" is not a slug of the gate`)
	fixtureWant(t, "calls for a slug that is not of the gate", calls, "")
	fixtureFails(t, EnsureOrganization(context.Background(), &CheckContext{}, "gate-a"), "the run has no console session")
}

func TestFixtureOrgInstall(t *testing.T) {
	install := func(cc *CheckContext) error {
		return InstallBlueprint(context.Background(), cc, "gate-a", "bp-wordpress-tenant", "wp-1", map[string]any{"replicas": 1})
	}
	answers := func(post string, app ...string) map[string][]string {
		return map[string][]string{fixtureEnvs: {`200 {"environments":[]}`, `200 {"environments":[{"name":"gate-a-prod"},{"name":"gate-a-dev"}]}`},
			fixtureInstall: {post}, fixtureApp: app}
	}
	notReady := `200 {"name":"wp-1","conditions":[{"type":"Ready","status":"False","reason":"Installing","message":"helm install"}]}`
	calls, bodies, err := fixtureRun(t, answers(`201 {"id":"u-1"}`, notReady, `200 {"name":"wp-1","conditions":[{"type":"Ready","status":"True"}]}`),
		func(_ context.Context, cc *CheckContext) error { return install(cc) })
	if err != nil {
		t.Fatal(err)
	}
	fixtureWant(t, "calls", calls, fixtureEnvs+", "+fixtureEnvs+", "+fixtureInstall+", "+fixtureApp+", "+fixtureApp)
	fixtureWant(t, "bodies", bodies, fixtureInstall+` {"blueprint":"bp-wordpress-tenant","environment":"gate-a-prod","name":"wp-1","org":"gate-a",`+
		`"placement":{"regions":["r-a"]},"topology":"singleton","values":{"replicas":1}}`)

	// Failures.
	_, _, err = fixtureRun(t, answers(`409 {"code":"organization-not-ready"}`), func(_ context.Context, cc *CheckContext) error { return install(cc) })
	fixtureFails(t, err, `POST /catalyst/v1/apps/instances wp-1 of bp-wordpress-tenant in gate-a: HTTP 409 (want 201): {"code":"organization-not-ready"}`)
	_, _, err = fixtureRun(t, answers(`201 {"id":"u-1"}`, notReady), func(_ context.Context, cc *CheckContext) error { return install(cc) })
	fixtureFails(t, err, "the Application wp-1 in gate-a is not Ready: timed out after 300ms; last observed: Ready=False Installing: helm install")
	calls, _, err = fixtureRun(t, nil, func(_ context.Context, cc *CheckContext) error { cc.Deploy = nil; return install(cc) })
	fixtureFails(t, err, "the run names no deployment")
	fixtureWant(t, "calls without a deployment", calls, "")
	calls, _, err = fixtureRun(t, nil, func(ctx context.Context, cc *CheckContext) error {
		return InstallBlueprint(ctx, cc, "acme", "bp-wordpress-tenant", "wp-1", nil)
	})
	fixtureFails(t, err, `the slug "acme" is not a slug of the gate`)
	fixtureWant(t, "calls for a slug that is not of the gate", calls, "")
	fixtureFails(t, install(&CheckContext{Deploy: &DeploymentInfo{ID: "dep-1"}, Regions: []kube.Region{{ID: "r-a"}}}), "the run has no console session")
}

func TestFixtureOrgRemove(t *testing.T) {
	remove := func(ctx context.Context, cc *CheckContext) error { return RemoveOrganization(ctx, cc, "gate-a") }
	answers := func(del string, bySlug []string, apps ...string) map[string][]string {
		return map[string][]string{fixtureList: {fixtureListed}, "DELETE /api/v1/organizations/id-1": {del}, fixtureBySlug: bySlug, fixtureApps: apps}
	}
	left := `200 {"kind":"ApplicationList","items":[{"name":"wp-1","namespace":"gate-a"},{"name":"wp-9","namespace":"gate-z"}]}`
	none := `200 {"kind":"ApplicationList","items":[{"name":"wp-9","namespace":"gate-z"}]}`
	calls, bodies, err := fixtureRun(t, answers("204 ", []string{`200 {"subdomain":"gate-a","state":"done"}`, fixtureGone}, left, none), remove)
	if err != nil {
		t.Fatal(err)
	}
	fixtureWant(t, "calls", calls, fixtureList+", DELETE /api/v1/organizations/id-1, "+fixtureBySlug+", "+fixtureBySlug+", "+fixtureApps+", "+fixtureBySlug+", "+fixtureApps)
	fixtureWant(t, "bodies", bodies, "")

	// Failures.
	_, _, err = fixtureRun(t, answers(`502 {"error":"organization-cr-delete-failed"}`, nil), remove)
	fixtureFails(t, err, `DELETE /api/v1/organizations/id-1 (gate-a): HTTP 502: {"error":"organization-cr-delete-failed"}`)
	_, _, err = fixtureRun(t, answers("204 ", []string{fixtureGone}, left), remove)
	fixtureFails(t, err, "the Organization gate-a is not gone: timed out after 300ms; last observed: Applications left in gate-a: wp-1")
	_, _, err = fixtureRun(t, answers("204 ", []string{`404 {"code":"route-not-found"}`}, none), remove)
	fixtureFails(t, err, `last observed: GET /api/v1/organizations/gate-a: HTTP 404 {"code":"route-not-found"}`)
	calls, _, err = fixtureRun(t, nil, func(ctx context.Context, cc *CheckContext) error { return RemoveOrganization(ctx, cc, "acme") })
	fixtureFails(t, err, `the slug "acme" is not a slug of the gate`)
	fixtureWant(t, "calls for a slug that is not of the gate", calls, "")
	calls, _, err = fixtureRun(t, answers("204 ", []string{fixtureGone}, none), func(ctx context.Context, cc *CheckContext) error { cc.Deploy = nil; return remove(ctx, cc) })
	fixtureFails(t, err, "the run names no deployment")
	fixtureWant(t, "calls without a deployment", calls, "")
	fixtureFails(t, remove(context.Background(), &CheckContext{Deploy: &DeploymentInfo{ID: "dep-1"}}), "the run has no console session")
}
