package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	ghclient "github.com/openova-io/openova/core/services/provisioning/github"
	"github.com/openova-io/openova/core/services/provisioning/gitops"
)

// #4384 — the funnel cart day-2 install committed the wordpress HelmRelease to
// the GLOBAL openova/openova repo (wrong target for a Sovereign's per-Org apps)
// and 404'd on the empty-SHA tree path (`GET .../git/trees/?recursive=1`). The
// fix retargets the commit to the per-Org `<slug>/catalyst-tenant` repo's
// `vcluster/apps/` tree and resolves the branch SHA correctly.
//
// This test stands up a fake Gitea contents API, drives applyTenantChange with
// PerOrgGitops=true, and asserts:
//  1. the ChangeFiles batch commits to /repos/<slug>/catalyst-tenant/contents
//     (NOT /repos/openova/openova/...),
//  2. the batch carries vcluster/apps/app-wordpress.yaml,
//  3. NO request ever hits the empty-SHA tree path (the live 404).

type recordedReq struct {
	Method string
	Path   string
	Body   []byte
}

func fakeGiteaForCart(t *testing.T) (*httptest.Server, *[]recordedReq, *sync.Mutex) {
	t.Helper()
	var (
		mu   sync.Mutex
		reqs []recordedReq
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		reqs = append(reqs, recordedReq{Method: r.Method, Path: r.URL.Path, Body: body})
		mu.Unlock()

		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/catalog/plans"):
			// Catalog plan lookup so resolveTenantPlanSlug is authoritative
			// (reachable=true) without needing a live Org CR.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"s","slug":"s"}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			// Branch ref resolves to a real commit SHA (repo healthy).
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"object":{"sha":"c3f4799deadbeef0000000000000000deadbeef"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/trees/"):
			// The empty-SHA tree path is /git/trees/?recursive=1 (no SHA
			// segment). The #4384 fix passes the COMMIT SHA straight through to
			// Gitea, so the path carries a real SHA. Serve an empty tree
			// (nothing to prune yet) so commitOnceContents proceeds to create.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tree":[],"truncated":false}`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			// kustomization.yaml / db file existence probes → 404 (fresh Org).
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			// Batch ChangeFiles commit succeeds.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"commit":{"sha":"newcommitsha0000000000000000000000000000"}}`))
		default:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs, &mu
}

func TestApplyTenantChangePerOrg_CommitsToPerOrgRepo_4384(t *testing.T) {
	srv, reqs, mu := fakeGiteaForCart(t)

	gen := gitops.NewManifestGenerator("clusters/sov/org-tenants")
	gen.ParentDomain = "omani.homes"

	h := &Handler{
		Generator: gen,
		// Globally configured at openova/openova (the WRONG target for per-Org
		// apps) — the fix must retarget to g6wpwalk/catalyst-tenant.
		GitHubClient: ghclient.NewClientWithAPIURL("token", "openova", "openova", srv.URL),
		GitBranch:    "main",
		PerOrgGitops: true,
		// Authoritative plan resolution via the stub catalog (so the #4293
		// fail-closed gate passes without a live Org CR).
		CatalogURL: srv.URL,
	}

	// Drive the day-2 install path directly (applyTenantChange → per-Org branch).
	err := h.applyTenantChange(context.Background(), appChangeData{
		TenantSlug: "g6wpwalk",
		AppSlug:    "wordpress",
		Apps:       []string{"wordpress"},
		PlanID:     "s",
	}, "install")
	if err != nil {
		t.Fatalf("applyTenantChange (per-Org) returned error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	var sawPerOrgBatch bool
	var batchBody []byte
	for _, rq := range *reqs {
		// The #4384 root-cause failure: a tree GET with NO SHA segment.
		if rq.Method == http.MethodGet && strings.Contains(rq.Path, "/git/trees/?") {
			t.Errorf("regression: empty-SHA tree path hit (%s) — the live 404", rq.Path)
		}
		if rq.Method == http.MethodGet && strings.HasSuffix(rq.Path, "/git/trees/") {
			t.Errorf("regression: empty-SHA tree path hit (%s) — the live 404", rq.Path)
		}
		// Any write to the GLOBAL openova/openova repo is the wrong target.
		if strings.Contains(rq.Path, "/repos/openova/openova/contents") {
			t.Errorf("regression: committed to GLOBAL openova/openova repo (#4384 wrong target): %s", rq.Path)
		}
		if rq.Method == http.MethodPost && strings.HasSuffix(rq.Path, "/contents") {
			if strings.Contains(rq.Path, "/repos/g6wpwalk/catalyst-tenant/contents") {
				sawPerOrgBatch = true
				batchBody = rq.Body
			}
		}
	}

	if !sawPerOrgBatch {
		t.Fatalf("cart install did NOT commit to /repos/g6wpwalk/catalyst-tenant/contents (#4384). requests:\n%s", dumpPaths(*reqs))
	}

	// The batch must carry the wordpress HelmRelease at vcluster/apps/app-wordpress.yaml.
	var payload struct {
		Branch string `json:"branch"`
		Files  []struct {
			Operation string `json:"operation"`
			Path      string `json:"path"`
			Content   string `json:"content"`
		} `json:"files"`
	}
	if err := json.Unmarshal(batchBody, &payload); err != nil {
		t.Fatalf("batch body not JSON: %v\n%s", err, batchBody)
	}
	if payload.Branch != "main" {
		t.Errorf("batch branch = %q, want main", payload.Branch)
	}
	var sawWordpress, sawKust bool
	for _, f := range payload.Files {
		if f.Path == "vcluster/apps/app-wordpress.yaml" {
			sawWordpress = true
			dec, _ := base64.StdEncoding.DecodeString(f.Content)
			if !strings.Contains(string(dec), "wordpress") {
				t.Errorf("app-wordpress.yaml content missing wordpress:\n%s", dec)
			}
		}
		if f.Path == "vcluster/apps/kustomization.yaml" {
			sawKust = true
			dec, _ := base64.StdEncoding.DecodeString(f.Content)
			// The merged kustomization preserves the org-controller NP
			// baseline AND lists the new app.
			for _, want := range []string{"networkpolicy.yaml", "app-wordpress.yaml"} {
				if !strings.Contains(string(dec), want) {
					t.Errorf("merged kustomization missing %q:\n%s", want, dec)
				}
			}
			// #4567: the CNP lives in vcluster/host-apps/, not apps/ — it must
			// NOT be listed in the apps kustomization (else the kustomize build
			// fails on the missing file and the purchased app never deploys).
			if strings.Contains(string(dec), "ciliumnetworkpolicy.yaml") {
				t.Errorf("apps kustomization wrongly lists ciliumnetworkpolicy.yaml (a host-apps/ file) — breaks the build:\n%s", dec)
			}
		}
	}
	if !sawWordpress {
		t.Errorf("batch did NOT carry vcluster/apps/app-wordpress.yaml (#4384 close criteria)")
	}
	if !sawKust {
		t.Errorf("batch did NOT carry the merged vcluster/apps/kustomization.yaml")
	}
}

func dumpPaths(reqs []recordedReq) string {
	var b strings.Builder
	for _, r := range reqs {
		b.WriteString(r.Method + " " + r.Path + "\n")
	}
	return b.String()
}
