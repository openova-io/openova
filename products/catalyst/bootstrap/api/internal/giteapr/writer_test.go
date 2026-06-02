// writer_test.go — unit coverage for the IaC PR pipeline.
package giteapr

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
)

// fakeIaC is a deterministic in-memory IaC client.
type fakeIaC struct {
	mu       sync.Mutex
	branches map[string]bool
	files    map[string][]byte // org/repo/branch/path → bytes
	prs      map[string]gitea.PullRequest
	prSeq    int64
	merged   map[int64]bool

	failPut     bool
	failDelete  bool
	failPR      bool
	failBranch  bool
	failMerge   bool
}

func newFakeIaC() *fakeIaC {
	return &fakeIaC{
		branches: map[string]bool{},
		files:    map[string][]byte{},
		prs:      map[string]gitea.PullRequest{},
		merged:   map[int64]bool{},
	}
}

func (f *fakeIaC) EnsureBranch(_ context.Context, org, repo, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failBranch {
		return errors.New("branch boom")
	}
	f.branches[org+"/"+repo+"/"+branch] = true
	return nil
}

func (f *fakeIaC) PutFile(_ context.Context, org, repo, branch, path string, data []byte, _ string, _ ...gitea.PutFileOpts) (gitea.File, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPut {
		return gitea.File{}, false, errors.New("put boom")
	}
	f.files[org+"/"+repo+"/"+branch+"/"+path] = data
	return gitea.File{Path: path}, true, nil
}

func (f *fakeIaC) DeleteFile(_ context.Context, org, repo, branch, path, _ string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failDelete {
		return false, errors.New("del boom")
	}
	delete(f.files, org+"/"+repo+"/"+branch+"/"+path)
	return true, nil
}

func (f *fakeIaC) CreatePullRequest(_ context.Context, org, repo, head, base, title, body string) (gitea.PullRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failPR {
		return gitea.PullRequest{}, errors.New("pr boom")
	}
	key := org + "/" + repo + "/" + head + "→" + base
	if pr, ok := f.prs[key]; ok {
		return pr, nil
	}
	f.prSeq++
	pr := gitea.PullRequest{
		Number: f.prSeq,
		URL:    "https://gitea.example/" + org + "/" + repo + "/pulls/" + strFmt(f.prSeq),
		Title:  title,
		Body:   body,
	}
	pr.Head.Ref = head
	pr.Base.Ref = base
	f.prs[key] = pr
	return pr, nil
}

func (f *fakeIaC) MergePullRequest(_ context.Context, _, _ string, number int64, _ gitea.MergePROpts) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failMerge {
		return errors.New("merge boom")
	}
	f.merged[number] = true
	return nil
}

// fakeStatusChecker scripts a sequence of status maps.
type fakeStatusChecker struct {
	mu       sync.Mutex
	script   []map[string]CheckStatus
	calls    int
	finalErr error
}

func (f *fakeStatusChecker) GetStatuses(_ context.Context, _, _, _ string) (map[string]CheckStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.finalErr != nil && f.calls >= len(f.script) {
		return nil, f.finalErr
	}
	i := f.calls
	if i >= len(f.script) {
		i = len(f.script) - 1
	}
	f.calls++
	return f.script[i], nil
}

func strFmt(n int64) string {
	// avoid pulling fmt just for this
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func mkMutation() Mutation {
	return Mutation{
		Org:           "acme",
		App:           "wp",
		EndpointName:  "ui",
		ManifestYAML:  []byte("apiVersion: v1\nkind: Endpoint\n"),
		Op:            OpCreate,
		CommitMessage: "test",
		PRTitle:       "endpoint(create): acme/wp/ui",
		PRBody:        FormatPRBody(Mutation{Org: "acme", App: "wp", EndpointName: "ui", Op: OpCreate}, "tester"),
	}
}

func TestOpenAndMerge_HappyPath(t *testing.T) {
	iac := newFakeIaC()
	statuses := &fakeStatusChecker{
		script: []map[string]CheckStatus{
			{"kyverno-admission": CheckPass, "cert-manager-precheck": CheckPass, "dns-conflict-precheck": CheckPass},
		},
	}
	w := NewWriter(iac, statuses, PollConfig{Interval: 1 * time.Millisecond, Budget: 100 * time.Millisecond})

	res, err := w.OpenAndMerge(context.Background(), mkMutation())
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if res.Status != StatusMerged {
		t.Fatalf("expected merged; got %s", res.Status)
	}
	if !iac.merged[res.PRNumber] {
		t.Fatalf("PR %d not merged in fake", res.PRNumber)
	}
	if !strings.HasPrefix(res.Branch, "endpoint/wp/ui/") {
		t.Fatalf("unexpected branch %q", res.Branch)
	}
	if res.Path != "apps/wp/endpoints/ui.yaml" {
		t.Fatalf("unexpected path %q", res.Path)
	}
	for _, n := range LockedStatusCheckNames {
		if res.PerCheck[n] != CheckPass {
			t.Fatalf("check %s expected pass, got %s", n, res.PerCheck[n])
		}
	}
}

func TestOpenAndMerge_FailedPrecheck(t *testing.T) {
	iac := newFakeIaC()
	statuses := &fakeStatusChecker{
		script: []map[string]CheckStatus{
			{"kyverno-admission": CheckPass, "cert-manager-precheck": CheckFail, "dns-conflict-precheck": CheckPass},
		},
	}
	w := NewWriter(iac, statuses, PollConfig{Interval: 1 * time.Millisecond, Budget: 100 * time.Millisecond})

	res, err := w.OpenAndMerge(context.Background(), mkMutation())
	if err != nil {
		t.Fatalf("expected nil err on precheck-fail (PR left open), got %v", err)
	}
	if res.Status != StatusFailedPrecheck {
		t.Fatalf("expected failed-precheck; got %s", res.Status)
	}
	if iac.merged[res.PRNumber] {
		t.Fatal("PR should NOT have merged on failed precheck")
	}
}

func TestOpenAndMerge_PendingAtBudget(t *testing.T) {
	iac := newFakeIaC()
	statuses := &fakeStatusChecker{
		script: []map[string]CheckStatus{
			{"kyverno-admission": CheckPending, "cert-manager-precheck": CheckPending, "dns-conflict-precheck": CheckPending},
		},
	}
	w := NewWriter(iac, statuses, PollConfig{Interval: 1 * time.Millisecond, Budget: 5 * time.Millisecond})

	res, err := w.OpenAndMerge(context.Background(), mkMutation())
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if res.Status != StatusOpen {
		t.Fatalf("expected open; got %s", res.Status)
	}
	if iac.merged[res.PRNumber] {
		t.Fatal("PR should NOT have merged when budget elapsed mid-pending")
	}
}

func TestOpenAndMerge_DeletePath(t *testing.T) {
	iac := newFakeIaC()
	// seed an existing file so DeleteFile path is exercised
	iac.files["acme/iac/main/apps/wp/endpoints/ui.yaml"] = []byte("old")
	statuses := &fakeStatusChecker{
		script: []map[string]CheckStatus{
			{"kyverno-admission": CheckPass, "cert-manager-precheck": CheckPass, "dns-conflict-precheck": CheckPass},
		},
	}
	w := NewWriter(iac, statuses, PollConfig{Interval: 1 * time.Millisecond, Budget: 100 * time.Millisecond})

	m := mkMutation()
	m.Op = OpDelete
	m.ManifestYAML = nil
	res, err := w.OpenAndMerge(context.Background(), m)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if res.Status != StatusMerged {
		t.Fatalf("expected merged; got %s", res.Status)
	}
}

func TestOpenAndMerge_RequiresWiring(t *testing.T) {
	_, err := (&Writer{}).OpenAndMerge(context.Background(), mkMutation())
	if !errors.Is(err, ErrNotWired) {
		t.Fatalf("expected ErrNotWired; got %v", err)
	}
}

func TestOpenAndMerge_ValidatesInput(t *testing.T) {
	iac := newFakeIaC()
	statuses := &fakeStatusChecker{}
	w := NewWriter(iac, statuses, PollConfig{Interval: 1 * time.Millisecond, Budget: 5 * time.Millisecond})

	_, err := w.OpenAndMerge(context.Background(), Mutation{Org: "", App: "wp", EndpointName: "ui", Op: OpCreate, ManifestYAML: []byte("x")})
	if err == nil {
		t.Fatal("expected error for missing Org")
	}

	_, err = w.OpenAndMerge(context.Background(), Mutation{Org: "acme", App: "wp", EndpointName: "ui", Op: OpUnknown})
	if err == nil {
		t.Fatal("expected error for unknown Op")
	}

	_, err = w.OpenAndMerge(context.Background(), Mutation{Org: "acme", App: "wp", EndpointName: "ui", Op: OpCreate, ManifestYAML: nil})
	if err == nil {
		t.Fatal("expected error for empty manifest on create")
	}
}

func TestOpenAndMerge_MergeErrorLeavesOpen(t *testing.T) {
	iac := newFakeIaC()
	iac.failMerge = true
	statuses := &fakeStatusChecker{
		script: []map[string]CheckStatus{
			{"kyverno-admission": CheckPass, "cert-manager-precheck": CheckPass, "dns-conflict-precheck": CheckPass},
		},
	}
	w := NewWriter(iac, statuses, PollConfig{Interval: 1 * time.Millisecond, Budget: 100 * time.Millisecond})

	res, err := w.OpenAndMerge(context.Background(), mkMutation())
	if err == nil {
		t.Fatal("expected error when merge fails")
	}
	if res.Status != StatusOpen {
		t.Fatalf("expected open when merge fails; got %s", res.Status)
	}
}

func TestBranchName_DeterministicPerManifest(t *testing.T) {
	m := mkMutation()
	a := BranchName(m)
	b := BranchName(m)
	if a != b {
		t.Fatalf("expected stable branch name; got %s vs %s", a, b)
	}
	m.ManifestYAML = []byte("apiVersion: v2\n")
	c := BranchName(m)
	if a == c {
		t.Fatal("expected different branch when manifest changes")
	}
}

func TestEndpointManifestPath(t *testing.T) {
	got := EndpointManifestPath("wp", "ui")
	if got != "apps/wp/endpoints/ui.yaml" {
		t.Fatalf("unexpected path: %s", got)
	}
}

func TestFormatPRBody_MentionsCanonicalChecks(t *testing.T) {
	body := FormatPRBody(mkMutation(), "alice@acme")
	for _, n := range LockedStatusCheckNames {
		if !strings.Contains(body, n) {
			t.Fatalf("PR body missing check name %q", n)
		}
	}
	if !strings.Contains(body, "alice@acme") {
		t.Fatal("PR body should mention requester")
	}
	if !strings.Contains(body, "ADR-0009") {
		t.Fatal("PR body should mention ADR-0009 anchor")
	}
}
