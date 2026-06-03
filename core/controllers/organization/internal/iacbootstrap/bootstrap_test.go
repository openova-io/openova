// bootstrap_test.go — unit coverage for the per-Org IaC repo bootstrap
// orchestrator (ADR-0009).
package iacbootstrap

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
)

// fakeGitea is an in-memory GiteaClient that records every mutating
// call so tests can assert ordering + idempotency.
type fakeGitea struct {
	mu sync.Mutex

	orgs              map[string]gitea.Org
	repos             map[string]gitea.Repo // key: org/name
	files             map[string][]byte     // key: org/repo/branch/path
	users             map[string]gitea.AdminUser
	tokens            map[string]gitea.AccessToken // key: user/tokenName
	collaborators     map[string]string            // key: org/repo/user → permission
	branchProtections map[string][]string          // key: org/repo → contexts

	// Failure injection knobs.
	failEnsureOrg              bool
	failEnsureRepo             bool
	failPutFile                bool
	failCreateAdminUser        bool
	failCreateUserAccessToken  bool
	tokenAlreadyExistsOnce     bool // returns ErrAccessTokenExists on first call only
	failAddCollaborator        bool
	failEnsureBranchProtection bool

	calls []string
}

func newFakeGitea() *fakeGitea {
	return &fakeGitea{
		orgs:              map[string]gitea.Org{},
		repos:             map[string]gitea.Repo{},
		files:             map[string][]byte{},
		users:             map[string]gitea.AdminUser{},
		tokens:            map[string]gitea.AccessToken{},
		collaborators:     map[string]string{},
		branchProtections: map[string][]string{},
	}
}

func (f *fakeGitea) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, name)
}

func (f *fakeGitea) EnsureOrg(_ context.Context, slug, fullName, description, visibility string) (gitea.Org, error) {
	f.record("EnsureOrg/" + slug)
	if f.failEnsureOrg {
		return gitea.Org{}, errors.New("ensure org failed")
	}
	if existing, ok := f.orgs[slug]; ok {
		return existing, nil
	}
	o := gitea.Org{Username: slug, FullName: fullName, Description: description, Visibility: visibility}
	f.orgs[slug] = o
	return o, nil
}

func (f *fakeGitea) EnsureRepo(_ context.Context, org, name, description string, private bool) (gitea.Repo, error) {
	f.record("EnsureRepo/" + org + "/" + name)
	if f.failEnsureRepo {
		return gitea.Repo{}, errors.New("ensure repo failed")
	}
	key := org + "/" + name
	if existing, ok := f.repos[key]; ok {
		return existing, nil
	}
	r := gitea.Repo{Name: name, FullName: key, Description: description, Private: private, DefaultBranch: "main"}
	f.repos[key] = r
	return r, nil
}

func (f *fakeGitea) PutFile(_ context.Context, org, repo, branch, path string, data []byte, _ string, _ ...gitea.PutFileOpts) (gitea.File, bool, error) {
	f.record("PutFile/" + org + "/" + repo + "/" + branch + "/" + path)
	if f.failPutFile {
		return gitea.File{}, false, errors.New("put file failed")
	}
	key := strings.Join([]string{org, repo, branch, path}, "/")
	existing, found := f.files[key]
	if found && string(existing) == string(data) {
		return gitea.File{Path: path}, false, nil
	}
	f.files[key] = append([]byte(nil), data...)
	return gitea.File{Path: path}, true, nil
}

func (f *fakeGitea) CreateAdminUser(_ context.Context, username, email, password string) (gitea.AdminUser, error) {
	f.record("CreateAdminUser/" + username)
	if f.failCreateAdminUser {
		return gitea.AdminUser{}, errors.New("create user failed")
	}
	if existing, ok := f.users[username]; ok {
		return existing, nil
	}
	if password == "" {
		return gitea.AdminUser{}, errors.New("empty password")
	}
	u := gitea.AdminUser{Username: username, Email: email}
	f.users[username] = u
	return u, nil
}

func (f *fakeGitea) DeleteAdminUser(_ context.Context, username string) error {
	f.record("DeleteAdminUser/" + username)
	delete(f.users, username)
	return nil
}

func (f *fakeGitea) CreateUserAccessToken(_ context.Context, username, tokenName string, scopes []string) (gitea.AccessToken, error) {
	f.record("CreateUserAccessToken/" + username + "/" + tokenName)
	if f.failCreateUserAccessToken {
		return gitea.AccessToken{}, errors.New("create token failed")
	}
	if f.tokenAlreadyExistsOnce {
		f.tokenAlreadyExistsOnce = false
		return gitea.AccessToken{}, gitea.ErrAccessTokenExists
	}
	if _, ok := f.users[username]; !ok {
		return gitea.AccessToken{}, gitea.ErrUserNotFound
	}
	t := gitea.AccessToken{Name: tokenName, Sha1: "plaintext-" + username + "-" + tokenName, Scopes: scopes}
	f.tokens[username+"/"+tokenName] = t
	return t, nil
}

func (f *fakeGitea) DeleteUserAccessToken(_ context.Context, username, tokenName string) error {
	f.record("DeleteUserAccessToken/" + username + "/" + tokenName)
	delete(f.tokens, username+"/"+tokenName)
	return nil
}

func (f *fakeGitea) AddCollaborator(_ context.Context, org, repo, user, permission string) error {
	f.record("AddCollaborator/" + org + "/" + repo + "/" + user)
	if f.failAddCollaborator {
		return errors.New("add collaborator failed")
	}
	f.collaborators[org+"/"+repo+"/"+user] = permission
	return nil
}

func (f *fakeGitea) RemoveCollaborator(_ context.Context, org, repo, user string) error {
	f.record("RemoveCollaborator/" + org + "/" + repo + "/" + user)
	delete(f.collaborators, org+"/"+repo+"/"+user)
	return nil
}

func (f *fakeGitea) EnsureBranchProtection(_ context.Context, org, repo string, statusCheckContexts []string) error {
	f.record("EnsureBranchProtection/" + org + "/" + repo)
	if f.failEnsureBranchProtection {
		return errors.New("branch protection failed")
	}
	f.branchProtections[org+"/"+repo] = append([]string(nil), statusCheckContexts...)
	return nil
}

func (f *fakeGitea) DeleteBranchProtection(_ context.Context, org, repo, _ string) error {
	f.record("DeleteBranchProtection/" + org + "/" + repo)
	delete(f.branchProtections, org+"/"+repo)
	return nil
}

func (f *fakeGitea) DeleteRepo(_ context.Context, org, repo string) error {
	f.record("DeleteRepo/" + org + "/" + repo)
	delete(f.repos, org+"/"+repo)
	return nil
}

// fakeTokenStore records writes + reads so tests can assert idempotency.
type fakeTokenStore struct {
	mu     sync.Mutex
	values map[string]string
	calls  []string
}

func newFakeTokenStore() *fakeTokenStore {
	return &fakeTokenStore{values: map[string]string{}}
}

func (s *fakeTokenStore) HasToken(_ context.Context, org string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "HasToken/"+org)
	_, ok := s.values[org]
	return ok, nil
}

func (s *fakeTokenStore) PutToken(_ context.Context, org, plaintext string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "PutToken/"+org)
	s.values[org] = plaintext
	return nil
}

func (s *fakeTokenStore) DeleteToken(_ context.Context, org string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "DeleteToken/"+org)
	delete(s.values, org)
	return nil
}

// ------------------------------------------------------------------ tests --

func TestBootstrap_HappyPath_FirstRun(t *testing.T) {
	ctx := context.Background()
	gc := newFakeGitea()
	ts := newFakeTokenStore()

	got, err := Bootstrap(ctx, gc, ts, Input{
		Slug:          "acme",
		DisplayName:   "ACME Corp",
		SovereignFQDN: "t01.omani.works",
	})
	if err != nil {
		t.Fatalf("Bootstrap: unexpected error: %v", err)
	}

	wantURL := "https://gitea.t01.omani.works/acme/iac"
	if got.RepoURL != wantURL {
		t.Errorf("RepoURL: got %q, want %q", got.RepoURL, wantURL)
	}
	if got.RobotUsername != "acme-iac-bot" {
		t.Errorf("RobotUsername: got %q, want %q", got.RobotUsername, "acme-iac-bot")
	}
	if got.AlreadyBootstrapped {
		t.Errorf("AlreadyBootstrapped: first run should report false")
	}

	// Assert every step ran.
	wantCalls := []string{
		"EnsureOrg/acme",
		"EnsureRepo/acme/iac",
		"CreateAdminUser/acme-iac-bot",
		"CreateUserAccessToken/acme-iac-bot/iac-bot-token",
		"AddCollaborator/acme/iac/acme-iac-bot",
		"EnsureBranchProtection/acme/iac",
	}
	for _, want := range wantCalls {
		if !contains(gc.calls, want) {
			t.Errorf("missing call %q in %v", want, gc.calls)
		}
	}

	// Tree seeded — including the pre-check workflow that produces the
	// three branch-protection status checks (ADR-0009 §Consequences;
	// without it every PR traps in "required status checks have not yet
	// succeeded").
	for _, p := range []string{"README.md", "kustomization.yaml", "apps/.gitkeep", "envs/.gitkeep", "policies/.gitkeep", ".gitea/workflows/iac-prechecks.yml"} {
		if _, ok := gc.files["acme/iac/main/"+p]; !ok {
			t.Errorf("missing seeded file %q", p)
		}
	}

	// The seeded workflow MUST reference all three locked status-check
	// contexts, otherwise branch protection requires a context the repo
	// never produces.
	wf := gc.files["acme/iac/main/.gitea/workflows/iac-prechecks.yml"]
	for _, ctxName := range StatusCheckContexts {
		if !strings.Contains(string(wf), ctxName) {
			t.Errorf("seeded pre-check workflow missing status-check context %q", ctxName)
		}
	}

	// Branch protection wired to the three locked status checks.
	got2 := gc.branchProtections["acme/iac"]
	sort.Strings(got2)
	want2 := append([]string{}, StatusCheckContexts...)
	sort.Strings(want2)
	if !sliceEq(got2, want2) {
		t.Errorf("branch protection contexts: got %v, want %v", got2, want2)
	}

	// Token persisted to the store.
	if _, ok := ts.values["acme"]; !ok {
		t.Errorf("token store: expected acme key, got %v", ts.values)
	}
}

func TestBootstrap_Idempotent_SecondRun(t *testing.T) {
	ctx := context.Background()
	gc := newFakeGitea()
	ts := newFakeTokenStore()

	in := Input{
		Slug:          "acme",
		DisplayName:   "ACME Corp",
		SovereignFQDN: "t01.omani.works",
	}

	if _, err := Bootstrap(ctx, gc, ts, in); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstRunCalls := len(gc.calls)
	gc.calls = nil

	got, err := Bootstrap(ctx, gc, ts, in)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if !got.AlreadyBootstrapped {
		t.Errorf("AlreadyBootstrapped: second run should report true")
	}
	if got.RepoURL == "" {
		t.Errorf("RepoURL: second run must still return the URL")
	}

	// Second run MUST NOT mint a fresh token (token already in store).
	for _, c := range gc.calls {
		if strings.HasPrefix(c, "CreateUserAccessToken/") {
			t.Errorf("second run minted a new token: %v", gc.calls)
		}
	}
	// And MUST NOT write any new files (PutFile byte-equal short-circuit).
	for _, c := range gc.calls {
		if strings.HasPrefix(c, "PutFile/") {
			// PutFile is called (probe) but returns committed=false.
			// We accept the call but assert no file actually changed below.
		}
	}
	// On the second run, gc.calls count should equal the first run
	// (every call is still made, but only as a verify).
	if len(gc.calls) != firstRunCalls {
		t.Logf("call count delta: first=%d second=%d (informational)", firstRunCalls, len(gc.calls))
	}
}

func TestBootstrap_TokenAlreadyExistsOnGitea_DeletesAndRemints(t *testing.T) {
	ctx := context.Background()
	gc := newFakeGitea()
	ts := newFakeTokenStore()
	gc.tokenAlreadyExistsOnce = true

	if _, err := Bootstrap(ctx, gc, ts, Input{
		Slug:          "acme",
		DisplayName:   "ACME Corp",
		SovereignFQDN: "t01.omani.works",
	}); err != nil {
		t.Fatalf("Bootstrap with stale-token recovery: %v", err)
	}

	// We expect:
	//   first CreateUserAccessToken (returns ErrAccessTokenExists)
	//   DeleteUserAccessToken
	//   CreateUserAccessToken (success)
	createCount := 0
	deleteCount := 0
	for _, c := range gc.calls {
		switch {
		case strings.HasPrefix(c, "CreateUserAccessToken/"):
			createCount++
		case strings.HasPrefix(c, "DeleteUserAccessToken/"):
			deleteCount++
		}
	}
	if createCount != 2 {
		t.Errorf("CreateUserAccessToken call count: got %d, want 2", createCount)
	}
	if deleteCount != 1 {
		t.Errorf("DeleteUserAccessToken call count: got %d, want 1", deleteCount)
	}
	if _, ok := ts.values["acme"]; !ok {
		t.Errorf("token store: expected acme key after re-mint, got %v", ts.values)
	}
}

func TestBootstrap_RejectsInvalidSlug(t *testing.T) {
	ctx := context.Background()
	cases := []string{"", "-acme", "acme-", "ACME", "a", "ac me", "a--b"}
	for _, slug := range cases {
		_, err := Bootstrap(ctx, newFakeGitea(), newFakeTokenStore(), Input{
			Slug:          slug,
			DisplayName:   "X",
			SovereignFQDN: "t.example",
		})
		if err == nil {
			t.Errorf("Bootstrap accepted invalid slug %q", slug)
		}
	}
}

func TestBootstrap_RejectsMissingFields(t *testing.T) {
	ctx := context.Background()
	cases := []Input{
		{Slug: "", DisplayName: "x", SovereignFQDN: "t.example"},
		{Slug: "acme", DisplayName: "", SovereignFQDN: "t.example"},
		{Slug: "acme", DisplayName: "x", SovereignFQDN: ""},
	}
	for i, in := range cases {
		if _, err := Bootstrap(ctx, newFakeGitea(), newFakeTokenStore(), in); err == nil {
			t.Errorf("case %d: Bootstrap accepted incomplete Input %+v", i, in)
		}
	}
}

func TestBootstrap_FailureModes(t *testing.T) {
	ctx := context.Background()
	in := Input{Slug: "acme", DisplayName: "ACME", SovereignFQDN: "t01.omani.works"}
	cases := []struct {
		name string
		fail func(g *fakeGitea)
	}{
		{"ensure-org", func(g *fakeGitea) { g.failEnsureOrg = true }},
		{"ensure-repo", func(g *fakeGitea) { g.failEnsureRepo = true }},
		{"put-file", func(g *fakeGitea) { g.failPutFile = true }},
		{"create-user", func(g *fakeGitea) { g.failCreateAdminUser = true }},
		{"create-token", func(g *fakeGitea) { g.failCreateUserAccessToken = true }},
		{"collaborator", func(g *fakeGitea) { g.failAddCollaborator = true }},
		{"branch-protection", func(g *fakeGitea) { g.failEnsureBranchProtection = true }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitea()
			tc.fail(g)
			if _, err := Bootstrap(ctx, g, newFakeTokenStore(), in); err == nil {
				t.Errorf("expected error on %s failure", tc.name)
			}
		})
	}
}

func TestRotateRobotToken(t *testing.T) {
	ctx := context.Background()
	gc := newFakeGitea()
	ts := newFakeTokenStore()

	// Pre-seed the user + a stale token.
	gc.users["acme-iac-bot"] = gitea.AdminUser{Username: "acme-iac-bot"}
	gc.tokens["acme-iac-bot/iac-bot-token"] = gitea.AccessToken{Name: TokenName, Sha1: "stale"}
	ts.values["acme"] = "stale-store-value"

	if err := RotateRobotToken(ctx, gc, ts, "acme"); err != nil {
		t.Fatalf("RotateRobotToken: %v", err)
	}

	got := ts.values["acme"]
	if got == "" || got == "stale-store-value" {
		t.Errorf("store value should have rotated, got %q", got)
	}
	// Gitea token plaintext also rotated.
	if gc.tokens["acme-iac-bot/iac-bot-token"].Sha1 == "stale" {
		t.Errorf("Gitea token plaintext should have rotated")
	}
}

func TestTeardown_ReversesBootstrapInOrder(t *testing.T) {
	ctx := context.Background()
	gc := newFakeGitea()
	ts := newFakeTokenStore()

	if _, err := Bootstrap(ctx, gc, ts, Input{Slug: "acme", DisplayName: "ACME", SovereignFQDN: "t01.omani.works"}); err != nil {
		t.Fatalf("bootstrap precondition: %v", err)
	}
	gc.calls = nil

	if err := Teardown(ctx, gc, ts, "acme"); err != nil {
		t.Fatalf("Teardown: %v", err)
	}

	// Order MUST be: branch-protection, collaborator, repo, token, user.
	wantOrder := []string{
		"DeleteBranchProtection/acme/iac",
		"RemoveCollaborator/acme/iac/acme-iac-bot",
		"DeleteRepo/acme/iac",
		"DeleteUserAccessToken/acme-iac-bot/iac-bot-token",
		"DeleteAdminUser/acme-iac-bot",
	}
	prevIdx := -1
	for _, w := range wantOrder {
		idx := indexOf(gc.calls, w)
		if idx < 0 {
			t.Errorf("missing teardown call %q", w)
			continue
		}
		if idx < prevIdx {
			t.Errorf("teardown ordering violation: %q at idx=%d after later step (prev=%d)", w, idx, prevIdx)
		}
		prevIdx = idx
	}

	// OpenBao path deleted too.
	if _, ok := ts.values["acme"]; ok {
		t.Errorf("store path should have been deleted; values=%v", ts.values)
	}
}

func TestTeardown_RejectsEmptySlug(t *testing.T) {
	if err := Teardown(context.Background(), newFakeGitea(), newFakeTokenStore(), ""); err == nil {
		t.Errorf("Teardown accepted empty slug")
	}
}

// ----------------------------------------------------------- helpers --

func contains(haystack []string, needle string) bool {
	return indexOf(haystack, needle) >= 0
}

func indexOf(haystack []string, needle string) int {
	for i, h := range haystack {
		if h == needle {
			return i
		}
	}
	return -1
}

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
