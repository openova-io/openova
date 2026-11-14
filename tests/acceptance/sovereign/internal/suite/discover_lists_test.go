package suite

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
)

// The Environment list comes from the console API only: a 404 is an error
// that names the answer, never a read of the cluster.
func TestListEnvironmentsReadsOnlyTheAPI(t *testing.T) {
	// No kubectl can run here: the list must come from the API alone.
	t.Setenv("PATH", t.TempDir())
	for _, c := range []struct {
		status int
		body   string
		want   string
	}{
		{404, `{"error":"not-found"}`, "GET environments: HTTP 404"},
		{503, `{"error":"cache-not-synced"}`, "GET environments: HTTP 503"},
		{200, `{"items":[{"name":"x-cp","organizationRef":"x","placement":"multi-region","regions":[{},{}]}],"total":1}`, ""},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.URL.Path != "/api/v1/sovereigns/dep/environments" {
				http.NotFound(w, req)
				return
			}
			w.WriteHeader(c.status)
			_, _ = io.WriteString(w, c.body)
		}))
		r := &runner{out: io.Discard, run: &Run{Mode: "run"}, api: api.New(srv.URL, "session"), dep: "dep"}
		envs, src, err := r.listEnvironments(context.Background())
		srv.Close()
		if c.want != "" {
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("HTTP %d: err %v, want one that holds %q", c.status, err, c.want)
			}
			continue
		}
		if err != nil || src != "GET /api/v1/sovereigns/{id}/environments" || len(envs) != 1 ||
			envs[0] != (EnvInfo{Name: "x-cp", Org: "x", Placement: "multi-region", Regions: 2}) {
			t.Errorf("HTTP 200: envs %+v source %q err %v", envs, src, err)
		}
	}
}
