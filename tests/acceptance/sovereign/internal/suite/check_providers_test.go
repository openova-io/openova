package suite

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/api"
	"github.com/openova-io/openova/tests/acceptance/sovereign/internal/kube"
)

func TestProvidersCheck(t *testing.T) {
	const (
		packs = "/infra/providers/alpha/profile.yaml 0\n/infra/providers/beta/profile.yaml 1\n"
		cms   = `{"items":[{"metadata":{"name":"cloud-installation-site-1"},"data":{"profile.yaml":"kind: CloudProfile\nspec:\n  extends: beta\n"}}]}`
		route = `{"providers":[{"id":"alpha","provider":"alpha","installation":""},{"id":"beta/site-1","provider":"beta","installation":"site-1"}]}`
	)
	for _, c := range []struct {
		name, packs, cms, route, regionB, noAPI, want string // noAPI: the region that has no catalyst-api
	}{
		{"exact", packs, cms, route, "", "", ""},
		{"no installation listed", packs, cms, `{"providers":[{"id":"alpha","provider":"alpha"}]}`, "", "", "the route lists [alpha]; the image and the cluster give [alpha beta/site-1]"},
		{"a pack that needs an installation listed alone", packs, `{"items":[]}`, `{"providers":[{"id":"alpha","provider":"alpha"},{"id":"beta","provider":"beta"}]}`, "", "", "the route lists [alpha beta]; the image and the cluster give [alpha]"},
		{"empty list", packs, cms, `{"providers":[]}`, "", "", "the route lists []"},
		{"id unlike its fields", packs, cms, `{"providers":[{"id":"beta","provider":"beta","installation":"site-1"}]}`, "", "", `the entry with id "beta" has provider "beta" and installation "site-1"`},
		{"no pack in the image", "", cms, route, "", "", `the line "" names no pack`},
		{"a ConfigMap without spec.extends", packs, `{"items":[{"metadata":{"name":"cloud-installation-site-1"},"data":{}}]}`, route, "", "", "has no spec.extends"},
		{"regions disagree", packs, cms, route, `{"items":[]}`, "", "the regions disagree on the installation documents: [region-a] has [beta/site-1], [region-b] has []"},
		{"the second region has no API", packs, cms, route, "", "region-b", ""},
		{"the first region has no API", packs, cms, route, "", "region-a", `[region-a] the packs of the image: Error from server (NotFound): deployments.apps "catalyst-api" not found`},
		{"the second region has no API and other installation documents", packs, cms, route, `{"items":[]}`, "region-b", "the regions disagree on the installation documents: [region-a] has [beta/site-1], [region-b] has []"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/providers" || r.URL.Query().Get("include") != "all" {
					http.NotFound(w, r)
					return
				}
				_, _ = io.WriteString(w, c.route)
			}))
			defer srv.Close()
			kubectl := func(r kube.Region, _ context.Context, _ io.Reader, args ...string) ([]byte, error) {
				switch {
				case args[2] == "exec" && args[len(args)-1] == providersScript && r.ID == c.noAPI:
					return nil, errors.New(`Error from server (NotFound): deployments.apps "catalyst-api" not found`)
				case args[2] == "exec" && args[len(args)-1] == providersScript:
					return []byte(c.packs), nil
				case args[0] == "get" && args[1] == "configmaps" && strings.Join(args[4:6], " ") == "-l catalyst.openova.io/cloud-installation=true":
					if r.ID == "region-b" && c.regionB != "" {
						return []byte(c.regionB), nil
					}
					return []byte(c.cms), nil
				}
				return nil, errors.New("unexpected kubectl " + strings.Join(args, " "))
			}
			cc := &CheckContext{API: api.New(srv.URL, "session"), Regions: []kube.Region{{ID: "region-a"}, {ID: "region-b"}}}
			r := &runner{out: io.Discard, run: &Run{Mode: "run"}}
			r.runChecks(context.Background(), &Manifest{Checks: []ManifestCheck{entry(providersID, StateRequired)}}, []Check{providersCheck{kubectl: kubectl}}, cc, true)
			if res := r.run.Checks[0]; (c.want == "") != (res.Status == Pass) || !strings.Contains(res.Reason, c.want) {
				t.Fatalf("status %s, reason %q; want a reason that holds %q (empty: PASS)", res.Status, res.Reason, c.want)
			}
		})
	}
}
