package sovself

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	k8stesting "k8s.io/client-go/testing"
)

// gated sends one POST through MothershipOnly and returns the status, the
// body and whether the wrapped handler ran.
func gated(t *testing.T, rt *Route) (int, string, bool) {
	t.Helper()
	reached := false
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusAccepted)
	})
	rec := httptest.NewRecorder()
	rt.MothershipOnly(next).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/deployments", nil))
	return rec.Code, rec.Body.String(), reached
}

// TestMothershipOnly: a provisioning route reaches its handler only in mode
// mothership; in mode sovereign it answers 403 not-in-mothership-mode.
func TestMothershipOnly(t *testing.T) {
	sovereign := map[string]string{"SOVEREIGN_FQDN": "alpha.example.org"}
	cases := []struct {
		name    string
		vars    map[string]string // nil: no variables document
		env     map[string]string
		allowed bool
	}{
		{"mode sovereign", map[string]string{"SOVEREIGN_MODE": "sovereign"}, sovereign, false},
		{"a document with only kit keys is mode sovereign", map[string]string{"SOVEREIGN_FQDN": "alpha.example.org"}, sovereign, false},
		{"a value the contract does not allow is mode sovereign", map[string]string{"SOVEREIGN_MODE": "parent"}, sovereign, false},
		{"mode mothership", map[string]string{"SOVEREIGN_MODE": "mothership"}, sovereign, true},
		{"no document and no SOVEREIGN_FQDN is the mothership", nil, map[string]string{}, true},
		{"no document and SOVEREIGN_FQDN is a Sovereign", nil, sovereign, false},
	}
	for _, c := range cases {
		cs := cluster(t)
		if c.vars != nil {
			cs = cluster(t, configMap("flux-system", "sovereign-variables", c.vars))
		}
		code, body, reached := gated(t, route(cs, c.env, true, &clock{time.Unix(0, 0)}))
		if c.allowed {
			if code != http.StatusAccepted || !reached {
				t.Errorf("%s: status %d, handler ran %v; want 202 from the handler", c.name, code, reached)
			}
			continue
		}
		if code != http.StatusForbidden || reached {
			t.Errorf("%s: status %d, handler ran %v; want 403 and the handler not run", c.name, code, reached)
		}
		if body != "{\"error\":\"not-in-mothership-mode\"}\n" {
			t.Errorf("%s: body %q", c.name, body)
		}
	}
}

// TestMothershipOnlyFailsClosed: on a Sovereign whose ConfigMaps cannot be
// read, or that has no cluster client, the route is refused; the mothership
// without a cluster client keeps it.
func TestMothershipOnlyFailsClosed(t *testing.T) {
	cs := cluster(t, configMap("flux-system", "sovereign-variables", map[string]string{"SOVEREIGN_MODE": "mothership"}))
	cs.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("connection refused")
	})
	if code, _, reached := gated(t, route(cs, map[string]string{"SOVEREIGN_FQDN": "alpha.example.org"}, true, &clock{time.Unix(0, 0)})); code != http.StatusForbidden || reached {
		t.Errorf("a read that fails on a Sovereign: status %d, handler ran %v; want 403", code, reached)
	}
	noClient := func() (kubernetes.Interface, error) { return nil, errors.New("not in a cluster") }
	for _, c := range []struct {
		name    string
		env     map[string]string
		allowed bool
	}{
		{"no cluster client on a Sovereign", map[string]string{"SOVEREIGN_FQDN": "alpha.example.org"}, false},
		{"no cluster client on the mothership", map[string]string{}, true},
	} {
		rt := route(nil, c.env, true, &clock{time.Unix(0, 0)})
		rt.newClient = noClient
		code, _, reached := gated(t, rt)
		if reached != c.allowed || (!c.allowed && code != http.StatusForbidden) {
			t.Errorf("%s: status %d, handler ran %v; want the handler run %v", c.name, code, reached, c.allowed)
		}
	}
}
