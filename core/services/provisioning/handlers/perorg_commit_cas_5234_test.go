package handlers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	ghclient "github.com/openova-io/openova/core/services/provisioning/github"
	"github.com/openova-io/openova/core/services/provisioning/gitops"
	"github.com/openova-io/openova/core/services/provisioning/store"
	"github.com/openova-io/openova/core/services/shared/events"
)

// #5234 (hw274) — the funnel's purchased-app commit to the per-Org
// `<slug>/catalyst-tenant` repo lost the ref compare-and-swap to the
// org-controller's per-file PutFile commit bursts on every attempt ("ref-race
// persisted after 5 attempts") and the purchased WordPress never deployed.
// Root defect on the handler side: applyTenantChangePerOrg built its file map
// — INCLUDING the read-modify-write merges of the kustomization indexes —
// ONCE before the retry loop, so a retry re-pushed content computed against a
// superseded head (the #1031 stale-base class, now against the
// org-controller's #5104 merge-writes). The tests here prove:
//
//  1. the commit rebuilds its merge against the NEW head on every retry (the
//     org-controller's mid-race index write survives into the winning batch);
//  2. a terminally-exhausted ref-race error is NOT classified as the parkable
//     #4404 "Gitea not ready" race — the caller must take the FAIL branch;
//  3. the terminal failure paints the in-flight funnel provision red
//     immediately (machine-readable step + provision.failed event), instead
//     of leaving "Deploying <app>" running for the 10-minute pod wait.

// giteaRebuildFake is a fake Gitea contents API whose vcluster/apps tree
// gains an org-controller-authored baseline doc (resourcequota.yaml) right
// after the first (rejected) batch POST — simulating the concurrent
// controller merge-write landing mid-race.
type giteaRebuildFake struct {
	t *testing.T

	mu        sync.Mutex
	phase     int // 1 = pre-race tree, 2 = controller's write landed
	postCount int
	batches   [][]byte
}

func (f *giteaRebuildFake) kustContent() string {
	base := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - networkpolicy.yaml\n"
	if f.phase >= 2 {
		base += "  - resourcequota.yaml\n"
	}
	return base
}

func (f *giteaRebuildFake) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		phase := f.phase
		f.mu.Unlock()
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/catalog/plans"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"s","slug":"s"}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/catalog/apps"):
			// resolveAppSlugs id→slug lookup.
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"wordpress","slug":"wordpress"}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `[{"object":{"sha":"headsha-%036d"}}]`, phase)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/trees/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tree":[],"truncated":false}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contents/vcluster/apps"):
			// ListDir — the dir listing the #5104 tree-derived merge reads.
			w.Header().Set("Content-Type", "application/json")
			if phase >= 2 {
				_, _ = w.Write([]byte(`[{"name":"networkpolicy.yaml","type":"file"},{"name":"resourcequota.yaml","type":"file"},{"name":"kustomization.yaml","type":"file"}]`))
				return
			}
			_, _ = w.Write([]byte(`[{"name":"networkpolicy.yaml","type":"file"},{"name":"kustomization.yaml","type":"file"}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contents/vcluster/apps/kustomization.yaml"):
			// Serves BOTH the merge's ReadFile and the commit's SHA probe.
			f.mu.Lock()
			content := base64.StdEncoding.EncodeToString([]byte(f.kustContent()))
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"sha":"kustsha-%d","content":"%s","encoding":"base64"}`, phase, content)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			// db-password reads + fresh app-file probes → not there yet.
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			body, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.postCount++
			f.batches = append(f.batches, body)
			first := f.postCount == 1
			if first {
				// Reject the first batch at the ref CAS — and land the
				// concurrent org-controller write while the funnel backs off.
				f.phase = 2
			}
			f.mu.Unlock()
			if first {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"message":"cannot lock ref 'refs/heads/main': is at aaaa but expected bbbb"}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"commit":{"sha":"newcommitsha00000000000000000000000000"}}`))
		default:
			f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

// batchKustContent extracts the base64-decoded kustomization.yaml content
// from a recorded ChangeFiles batch body ("" when the batch didn't carry it).
func batchKustContent(t *testing.T, body []byte) string {
	t.Helper()
	var payload struct {
		Files []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"files"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("batch body not JSON: %v\n%s", err, body)
	}
	for _, f := range payload.Files {
		if f.Path == "vcluster/apps/kustomization.yaml" {
			dec, err := base64.StdEncoding.DecodeString(f.Content)
			if err != nil {
				t.Fatalf("kustomization content not base64: %v", err)
			}
			return string(dec)
		}
	}
	return ""
}

// TestApplyTenantChangePerOrg_RebuildsMergeAgainstNewHeadOnRetry_5234 drives
// the day-2 install while the org-controller lands an index merge-write
// mid-race. The funnel's retry must re-read the head and re-merge: the
// winning batch's kustomization.yaml carries the controller's
// resourcequota.yaml entry the first (stale) batch could not know about. A
// stale-base replay would clobber that entry — the #1031/#5104 regression
// this closes.
func TestApplyTenantChangePerOrg_RebuildsMergeAgainstNewHeadOnRetry_5234(t *testing.T) {
	fake := &giteaRebuildFake{t: t, phase: 1}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	gen := gitops.NewManifestGenerator("clusters/sov/org-tenants")
	gen.ParentDomain = "omani.homes"

	h := &Handler{
		Generator:    gen,
		GitHubClient: ghclient.NewClientWithAPIURL("token", "openova", "openova", srv.URL),
		GitBranch:    "main",
		PerOrgGitops: true,
		CatalogURL:   srv.URL,
	}

	err := h.applyTenantChange(context.Background(), appChangeData{
		TenantSlug: "g274cas",
		AppSlug:    "wordpress",
		Apps:       []string{"wordpress"},
		PlanID:     "s",
	}, "install")
	if err != nil {
		t.Fatalf("applyTenantChange should have retried past the ref CAS and succeeded, got: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.postCount != 2 {
		t.Fatalf("expected 2 batch POSTs (1 CAS loss + 1 win), got %d", fake.postCount)
	}
	firstKust := batchKustContent(t, fake.batches[0])
	winKust := batchKustContent(t, fake.batches[1])
	if firstKust == "" || winKust == "" {
		t.Fatalf("both batches must carry vcluster/apps/kustomization.yaml (first=%q win=%q)", firstKust, winKust)
	}
	if strings.Contains(firstKust, "resourcequota.yaml") {
		t.Errorf("first batch already lists resourcequota.yaml — the fake's mid-race write fired too early:\n%s", firstKust)
	}
	if !strings.Contains(winKust, "resourcequota.yaml") {
		t.Errorf("winning batch does NOT list the org-controller's mid-race resourcequota.yaml — the retry replayed a stale base instead of re-merging against the new head (#5234):\n%s", winKust)
	}
	for _, keep := range []string{"networkpolicy.yaml", "app-wordpress.yaml"} {
		if !strings.Contains(winKust, keep) {
			t.Errorf("winning batch kustomization dropped %q:\n%s", keep, winKust)
		}
	}
}

// TestRefRaceExhaustionIsTerminal_NotParkable_5234 pins the branch selection
// in runInstallJob: a ref-race that persisted through every CAS attempt is a
// TERMINAL failure (fail the Job + paint the provision red), NOT the parkable
// #4404 "per-Org Gitea repo not ready yet" race. If the exhausted error ever
// matched isGiteaNotReadyError, the install would park forever with no
// machine-readable failed state anywhere.
func TestRefRaceExhaustionIsTerminal_NotParkable_5234(t *testing.T) {
	exhausted := errors.New(`commit to per-Org repo acme274/catalyst-tenant: commit: ref-race persisted after 10 attempts: update ref: GitHub API POST http://gitea-http.gitea.svc:3000/api/v1/repos/acme274/catalyst-tenant/contents: 422 {"message":"sha does not match [given: aaaa, expected: bbbb]"}: not a fast forward (gitea contents API)`)
	if isGiteaNotReadyError(exhausted) {
		t.Fatalf("a terminally-exhausted ref-race must NOT be parkable as #4404 Gitea-not-ready — it would wait forever with no red step")
	}
	lockShape := errors.New(`commit to per-Org repo acme274/catalyst-tenant: commit: ref-race persisted after 10 attempts: update ref: GitHub API POST http://gitea/api/v1/repos/acme274/catalyst-tenant/contents: 409 {"message":"cannot lock ref 'refs/heads/main'"}: not a fast forward (gitea contents API)`)
	if isGiteaNotReadyError(lockShape) {
		t.Fatalf("the ref-lock exhaustion shape must NOT be parkable as #4404 Gitea-not-ready")
	}
}

// --- terminal red-step propagation (#5234) ---

// fakeCommitFailStore implements commitFailProvisionStore.
type fakeCommitFailStore struct {
	inFlight *store.Provision
	getErr   error

	updatedID string
	updated   *store.Provision
}

func (f *fakeCommitFailStore) GetInFlightProvisionByTenant(_ context.Context, _ string) (*store.Provision, error) {
	return f.inFlight, f.getErr
}

func (f *fakeCommitFailStore) UpdateProvision(_ context.Context, id string, p *store.Provision) error {
	f.updatedID = id
	f.updated = p
	return nil
}

// capturingPublisher records every published event.
type capturingPublisher struct {
	mu     sync.Mutex
	events []*events.Event
}

func (c *capturingPublisher) Publish(_ context.Context, _ string, e *events.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, e)
	return nil
}

func (c *capturingPublisher) Close() {}

func funnelSteps() []store.ProvisionStep {
	return []store.ProvisionStep{
		{Name: "Creating tenant", Status: "completed"},
		{Name: "Committing manifests to Git", Status: "completed"},
		{Name: "Provisioning vCluster", Status: "completed"},
		{Name: "Deploying WordPress", Status: "running"},
		{Name: "Configuring TLS certificates", Status: "pending"},
		{Name: "Running health checks", Status: "pending"},
	}
}

// TestFailActiveProvisionOn_PaintsRedStepAndPublishes_5234 asserts the
// terminal commit failure lands as machine-readable state IMMEDIATELY: the
// matching "Deploying <app>" step goes failed with the commit error, the
// provision goes failed, and provision.failed is published (the event the
// downstream consumer turns into customer status "failed" — the state the
// /launching interstitial and the marketplace re-visit gate read).
func TestFailActiveProvisionOn_PaintsRedStepAndPublishes_5234(t *testing.T) {
	st := &fakeCommitFailStore{
		inFlight: &store.Provision{
			ID:       "prov-1",
			TenantID: "t-1",
			Status:   "provisioning",
			Steps:    funnelSteps(),
		},
	}
	pub := &capturingPublisher{}
	h := &Handler{Producer: pub}

	commitErr := errors.New("commit to per-Org repo acme274/catalyst-tenant: commit: ref-race persisted after 10 attempts: update ref: ...")
	h.failActiveProvisionOn(context.Background(), st, appChangeData{
		TenantID:   "t-1",
		TenantSlug: "acme274",
		AppSlug:    "wordpress",
	}, commitErr)

	if st.updated == nil {
		t.Fatalf("provision was not updated — no machine-readable failed state landed")
	}
	if st.updatedID != "prov-1" {
		t.Errorf("updated provision id = %q, want prov-1", st.updatedID)
	}
	if st.updated.Status != "failed" {
		t.Errorf("provision status = %q, want failed", st.updated.Status)
	}
	depIdx := 3 // "Deploying WordPress"
	step := st.updated.Steps[depIdx]
	if step.Status != "failed" {
		t.Errorf("step %q status = %q, want failed (the red step)", step.Name, step.Status)
	}
	if !strings.Contains(step.Message, "ref-race persisted") {
		t.Errorf("red step message should carry the commit error, got %q", step.Message)
	}
	if step.DoneAt.IsZero() {
		t.Errorf("red step should be timestamped")
	}

	pub.mu.Lock()
	defer pub.mu.Unlock()
	var sawFailed bool
	for _, e := range pub.events {
		if e.Type == "provision.failed" && e.TenantID == "t-1" {
			sawFailed = true
		}
	}
	if !sawFailed {
		t.Errorf("provision.failed was not published — customer status never flips to failed")
	}
}

// TestFailActiveProvisionOn_NoInFlightProvisionIsNoop_5234 — a plain day-2
// install on an already-active Org has no in-flight funnel provision; the
// helper must not fabricate one (the Job + provision.app_failed event carry
// the state there).
func TestFailActiveProvisionOn_NoInFlightProvisionIsNoop_5234(t *testing.T) {
	st := &fakeCommitFailStore{inFlight: nil}
	pub := &capturingPublisher{}
	h := &Handler{Producer: pub}

	h.failActiveProvisionOn(context.Background(), st, appChangeData{
		TenantID: "t-2",
		AppSlug:  "wordpress",
	}, errors.New("boom"))

	if st.updated != nil {
		t.Errorf("no in-flight provision — nothing should have been updated")
	}
	pub.mu.Lock()
	defer pub.mu.Unlock()
	if len(pub.events) != 0 {
		t.Errorf("no in-flight provision — nothing should have been published, got %d events", len(pub.events))
	}
}

// TestCommitFailureStepIndex_5234 pins the red-step selection.
func TestCommitFailureStepIndex_5234(t *testing.T) {
	steps := funnelSteps()
	cases := []struct {
		name    string
		steps   []store.ProvisionStep
		appSlug string
		want    int
	}{
		{"matches the app's own Deploying step", steps, "wordpress", 3},
		{"unknown slug falls back to first Deploying step", steps, "ghost", 3},
		{"empty slug falls back to first Deploying step", steps, "", 3},
		{"no Deploying step falls back to the commit step", steps[:3], "wordpress", 1},
		{"no match at all falls back to step 0", steps[:1], "wordpress", 0},
		{"empty steps default to 0", nil, "wordpress", 0},
	}
	for _, tc := range cases {
		if got := commitFailureStepIndex(tc.steps, tc.appSlug); got != tc.want {
			t.Errorf("%s: commitFailureStepIndex = %d, want %d", tc.name, got, tc.want)
		}
	}
}
