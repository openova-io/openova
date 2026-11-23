package suite

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
)

const j5App, j5Namespace = "accept-1795298030", "hw307-omani-works"

func init() {
	empty := kubeList()
	clean := map[string]string{"* " + appResource: empty, "* " + hrResource: empty, "* " + cnpgResource: empty,
		"* persistentvolumeclaims": empty, "* secrets " + j5Namespace: kubeList(obj{"metadata": kubeMeta(j5Namespace, "ghcr-pull")})}
	fakeAnswers["j5-clean"] = clean
	// What gate run 44952720479 left in region a after a J5 that passed.
	seeds := map[string]string{"a secrets " + j5Namespace: kubeList(obj{"metadata": kubeMeta(j5Namespace, "ghcr-pull")},
		obj{"metadata": kubeMeta(j5Namespace, j5App+"-bp-wordpress-tenant-media-s3")},
		obj{"metadata": kubeMeta(j5Namespace, j5App+"-db-replication-ca-seed")})}
	for k, v := range clean {
		seeds[k] = v
	}
	fakeAnswers["j5-seed-secrets-left"] = seeds
}

// j5Run runs journey J5 against a scenario with a console that deletes the
// Application and then answers 404 for it.
func j5Run(t *testing.T, scenario string) *runner {
	t.Helper()
	t.Setenv(fakeKubectlEnv, scenario)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	regions := fakeRegions(t)
	r := &runner{out: io.Discard, cfg: Config{DeleteTimeout: 200 * time.Millisecond}, api: api.New(srv.URL, "session"),
		run: &Run{Mode: "run"}, dep: "dep-1", A: regions[0], B: regions[1], app: app{Name: j5App, Namespace: j5Namespace, Created: true}}
	r.j5(context.Background())
	return r
}

// J5 removes the Application of J1 through the API and passes only when
// nothing of it is left in any region, the Secrets of its namespace named
// after it included; it names each one that is left with its region.
func TestJ5NamesEveryLeftover(t *testing.T) {
	if r := j5Run(t, "j5-clean"); len(failedSteps(r)) != 0 || len(r.run.Steps) != 2 {
		t.Fatalf("nothing left: want J5.1 and J5.2 PASS, got %d steps failing with %v", len(r.run.Steps), failedSteps(r))
	}
	r := j5Run(t, "j5-seed-secrets-left")
	failed := failedSteps(r)
	want := "map[region-a:[secrets/" + j5Namespace + "/" + j5App + "-bp-wordpress-tenant-media-s3 secrets/" + j5Namespace + "/" + j5App + "-db-replication-ca-seed] region-b:[]]"
	if len(failed) != 1 || !strings.Contains(failed[0], "timed out after 200ms; last observed: "+want) {
		t.Fatalf("seeds left in region a: want J5.2 to fail naming them, got %v", failed)
	}
}
