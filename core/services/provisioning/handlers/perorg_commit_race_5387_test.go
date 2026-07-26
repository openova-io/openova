package handlers

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	ghclient "github.com/openova-io/openova/core/services/provisioning/github"
	"github.com/openova-io/openova/core/services/provisioning/gitops"
)

// #5387 (hw290) — every funnel Organization that installed an app failed
// provisioning, terminally, at "Deploying <app>":
//
//	git commit to per-Org repo failed: commit to per-Org repo walk-two/catalyst-tenant:
//	POST …/repos/walk-two/catalyst-tenant/contents: 500
//	{"message":"PushOutOfDate Error … ! [rejected] a480a0e7… -> main (non-fast-forward)"}
//
// Two independent defects stacked into that one red step, and the tests below
// pin one each:
//
//  1. THE WRITER RACED ITSELF. The tenant service dispatches a cart as ONE
//     `/provisioning/apps/install` per cart entry, and ApplyAppInstall runs each
//     in its own detached goroutine — every one of which re-renders the WHOLE
//     cart and pushes it to the SAME `<slug>/catalyst-tenant@main` branch. N
//     concurrent Gitea ChangeFiles batches, one branch head, at most one
//     fast-forward. applyTenantChangePerOrg now holds a per-branch gate across
//     the whole read-merge-commit cycle so the siblings queue instead of
//     colliding.
//
//  2. THE RETRY NEVER ENGAGED. Gitea serves this branch-head CAS loss as a bare
//     500 spelled `non-fast-forward`, which isGiteaRefRaceError did not match,
//     so CommitFilesWithPruneAndRebuild treated it as fatal on attempt 1. See
//     github/client_pushoutofdate_5387_test.go for the classifier-level proof;
//     the second test here pins the handler-visible consequence — the funnel
//     step no longer goes red on a single recoverable collision.

// perOrgRaceFake is a fake per-Org Gitea that models the ChangeFiles commit
// the way the real one behaves: the batch POST clones the branch head, applies,
// and pushes — with a real window in between. Two overlapping POSTs therefore
// produce a PushOutOfDate for the loser, exactly as on hw290.
//
// It also records the maximum number of commit CYCLES (first remote read →
// batch POST) that were ever in flight at once, which is the direct measure of
// whether the per-Org gate is doing its job.
type perOrgRaceFake struct {
	t *testing.T

	mu sync.Mutex
	// head advances on every accepted batch — the branch tip.
	head int
	// inFlight/maxInFlight track overlapping commit cycles.
	inFlight    int
	maxInFlight int
	postCount   int
	rejects     int
	// commitWindow is how long the fake holds a batch between reading the
	// head and pushing — the race window a concurrent writer can land in.
	commitWindow time.Duration
}

// enterCycle/leaveCycle bracket one handler-side commit cycle. The cycle opens
// on the branch-ref read (the first remote call applyTenantChangePerOrg makes
// through the commit client) and closes when its batch POST is answered.
func (f *perOrgRaceFake) enterCycle() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inFlight++
	if f.inFlight > f.maxInFlight {
		f.maxInFlight = f.inFlight
	}
}

func (f *perOrgRaceFake) leaveCycle() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.inFlight > 0 {
		f.inFlight--
	}
}

func (f *perOrgRaceFake) handler() http.HandlerFunc {
	// refSeen tracks which cycles have opened, so a retry's second ref read
	// inside the same handler call doesn't double-count. Keyed by nothing
	// fancy — we simply pair every ref read with the next POST from the same
	// connection-agnostic sequence, which is sufficient because the client
	// always does ref-read → … → POST.
	return func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/catalog/plans"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"s","slug":"s"}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/catalog/apps"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"wordpress","slug":"wordpress"},{"id":"listmonk","slug":"listmonk"},{"id":"openclaw","slug":"openclaw"}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			f.enterCycle()
			f.mu.Lock()
			head := f.head
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `[{"object":{"sha":"headsha-%032d"}}]`, head)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/trees/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tree":[],"truncated":false}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contents/vcluster/apps"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"name":"networkpolicy.yaml","type":"file"},{"name":"kustomization.yaml","type":"file"}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contents/vcluster/apps/kustomization.yaml"):
			f.mu.Lock()
			head := f.head
			f.mu.Unlock()
			content := base64.StdEncoding.EncodeToString([]byte(
				"apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n  - networkpolicy.yaml\n"))
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"sha":"kustsha-%d","content":"%s","encoding":"base64"}`, head, content)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			// Gitea's ChangeFiles: clone at `base`, apply, push. Anything that
			// moved the head inside the window makes the push non-fast-forward.
			f.mu.Lock()
			f.postCount++
			base := f.head
			f.mu.Unlock()

			time.Sleep(f.commitWindow)

			f.mu.Lock()
			moved := f.head != base
			if !moved {
				f.head++
			} else {
				f.rejects++
			}
			f.mu.Unlock()

			f.leaveCycle()
			w.Header().Set("Content-Type", "application/json")
			if moved {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprintf(w, `{"message":"PushOutOfDate Error: exit status 1: To /data/git/repositories/w5387/catalyst-tenant.git\n ! [rejected]        a480a0e7 -> main (non-fast-forward)\n"}`)
				return
			}
			fmt.Fprintf(w, `{"commit":{"sha":"commitsha-%032d"}}`, base+1)
		default:
			f.t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}
}

func newPerOrgRaceHandler(t *testing.T, srvURL string) *Handler {
	t.Helper()
	gen := gitops.NewManifestGenerator("clusters/sov/org-tenants")
	gen.ParentDomain = "omani.homes"
	return &Handler{
		Generator:    gen,
		GitHubClient: ghclient.NewClientWithAPIURL("token", "openova", "openova", srvURL),
		GitBranch:    "main",
		PerOrgGitops: true,
		CatalogURL:   srvURL,
	}
}

// TestApplyTenantChangePerOrg_SerialisesConcurrentCartInstalls_5387 dispatches
// a 3-app cart the way the funnel does — three concurrent installs for the SAME
// Org — against a Gitea that rejects overlapping pushes exactly like the real
// one.
//
// Without the per-Org gate the three commit cycles interleave on one branch
// head (maxInFlight == 3) and the fake serves PushOutOfDate to the losers: the
// hw290 failure, self-inflicted. With the gate they queue, so the branch never
// sees more than one writer, no push is ever rejected, and all three installs
// commit.
func TestApplyTenantChangePerOrg_SerialisesConcurrentCartInstalls_5387(t *testing.T) {
	fake := &perOrgRaceFake{t: t, commitWindow: 25 * time.Millisecond}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	h := newPerOrgRaceHandler(t, srv.URL)

	cart := []string{"wordpress", "listmonk", "openclaw"}
	errs := make([]error, len(cart))
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, app := range cart {
		wg.Add(1)
		go func(i int, app string) {
			defer wg.Done()
			<-start // release all three at once — the funnel's fan-out
			errs[i] = h.applyTenantChange(context.Background(), appChangeData{
				TenantSlug: "w5387",
				AppSlug:    app,
				Apps:       cart,
				PlanID:     "s",
			}, "install")
		}(i, app)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("cart app %q failed to commit: %v", cart[i], err)
		}
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.maxInFlight != 1 {
		t.Errorf("the per-Org branch saw %d concurrent commit cycles — the funnel's own cart installs are racing each other on one head (#5387); want 1",
			fake.maxInFlight)
	}
	if fake.rejects != 0 {
		t.Errorf("Gitea rejected %d push(es) as PushOutOfDate — serialised commits must never collide with each other (#5387)", fake.rejects)
	}
	if fake.postCount != len(cart) {
		t.Errorf("expected exactly %d batch POSTs (one per cart install, no retries needed), got %d", len(cart), fake.postCount)
	}
	if fake.head != len(cart) {
		t.Errorf("expected the branch head to advance once per install (%d), got %d", len(cart), fake.head)
	}
}

// TestApplyTenantChangePerOrg_RecoversFromGiteaPushOutOfDate_5387 pins the
// handler-visible half of the classifier fix: an out-of-process writer (the
// org-controller, which no gate in this process can hold back) advances the
// head under us once. On hw290 that single collision was terminal — the day-2
// commit returned the raw 500 and "Deploying <app>" went red. It must now be
// absorbed by the ref-race retry and the install must land.
func TestApplyTenantChangePerOrg_RecoversFromGiteaPushOutOfDate_5387(t *testing.T) {
	var (
		mu        sync.Mutex
		postCount int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/catalog/plans"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"s","slug":"s"}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/catalog/apps"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"id":"wordpress","slug":"wordpress"}]`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/git/refs/heads/main"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"object":{"sha":"headsha-00000000000000000000000000000001"}}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/trees/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"tree":[],"truncated":false}`))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/contents/vcluster/apps"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`[{"name":"networkpolicy.yaml","type":"file"}]`))
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			http.NotFound(w, r)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/contents"):
			mu.Lock()
			postCount++
			first := postCount == 1
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			if first {
				// The VERBATIM hw290 shape: 500 + PushOutOfDate.
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"message":"PushOutOfDate Error … ! [rejected] a480a0e7… -> main (non-fast-forward)"}`))
				return
			}
			_, _ = w.Write([]byte(`{"commit":{"sha":"commitsha-0000000000000000000000000002"}}`))
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	h := newPerOrgRaceHandler(t, srv.URL)
	err := h.applyTenantChange(context.Background(), appChangeData{
		TenantSlug: "w5387b",
		AppSlug:    "wordpress",
		Apps:       []string{"wordpress"},
		PlanID:     "s",
	}, "install")
	if err != nil {
		t.Fatalf("a single PushOutOfDate collision must be retried, not fail the install (#5387); got: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if postCount != 2 {
		t.Errorf("expected 2 batch POSTs (1 PushOutOfDate + 1 win), got %d", postCount)
	}
}

// TestPushOutOfDateExhaustionIsTerminalNotParkable_5387 — a PushOutOfDate that
// survives every CAS attempt must still be a TERMINAL failure that paints the
// funnel red, not the parkable #4404 "per-Org Gitea repo not ready" race. The
// 500 body must never be mistaken for the 404 shapes isGiteaNotReadyError
// keys on, or a genuinely wedged branch would sit in the pending-install
// registry for 30 minutes with no red step anywhere.
func TestPushOutOfDateExhaustionIsTerminalNotParkable_5387(t *testing.T) {
	exhausted := fmt.Errorf(`commit to per-Org repo walk-two/catalyst-tenant: commit: ref-race persisted after 10 attempts: update ref: GitHub API POST http://gitea-http.gitea.svc:3000/api/v1/repos/walk-two/catalyst-tenant/contents: 500 {"message":"PushOutOfDate Error … ! [rejected] a480a0e7… -> main (non-fast-forward)"}: not a fast forward (gitea contents API)`)
	if isGiteaNotReadyError(exhausted) {
		t.Fatalf("an exhausted PushOutOfDate ref-race must NOT be parkable as #4404 Gitea-not-ready — it would wait 30 minutes with no red step")
	}
}

// TestPerOrgCommitGate_ReleasesAndIsCtxCancellable_5387 pins the gate's own
// contract: mutual exclusion per key, independence across keys, a ctx-abortable
// wait (so a tenant-deleted preempt never parks behind another Org), and no map
// growth once every holder has released.
func TestPerOrgCommitGate_ReleasesAndIsCtxCancellable_5387(t *testing.T) {
	var g perOrgCommitGate
	ctx := context.Background()

	relA, err := g.acquire(ctx, "acme/catalyst-tenant@main")
	if err != nil {
		t.Fatalf("first acquire must succeed: %v", err)
	}

	// A DIFFERENT Org must not be blocked by acme's in-flight commit.
	relB, err := g.acquire(ctx, "beta/catalyst-tenant@main")
	if err != nil {
		t.Fatalf("a different Org must not be gated behind acme: %v", err)
	}
	relB()

	// The SAME Org must block — and must give up when its ctx is cancelled.
	cancelCtx, cancel := context.WithCancel(ctx)
	blocked := make(chan error, 1)
	go func() { _, aerr := g.acquire(cancelCtx, "acme/catalyst-tenant@main"); blocked <- aerr }()
	select {
	case aerr := <-blocked:
		t.Fatalf("second acquire on the same key must block while the first holds it, returned %v", aerr)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case aerr := <-blocked:
		if aerr == nil {
			t.Errorf("a cancelled waiter must return ctx.Err(), not acquire the gate")
		}
	case <-time.After(time.Second):
		t.Fatalf("a cancelled waiter must abort promptly instead of parking forever")
	}

	// Once released the key is re-acquirable, and the registry drains.
	relA()
	relA() // release is idempotent
	relC, err := g.acquire(ctx, "acme/catalyst-tenant@main")
	if err != nil {
		t.Fatalf("gate must be re-acquirable after release: %v", err)
	}
	relC()

	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.slots) != 0 {
		t.Errorf("gate leaked %d slot(s) after every holder released: %v", len(g.slots), g.slots)
	}
}
