package gitwrite_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite/internal/giteadouble"
)

const fqdn = "t1.example.org"

var repo = gitwrite.Repo{Owner: "openova", Name: "sovereign-regions"}

// robotTokens gives each robot its own token, so the double can see which one was used.
type robotTokens struct{}

func (robotTokens) Token(_ context.Context, robot string) (string, error) {
	return "tok-" + robot, nil
}

func setup(t *testing.T, tokens gitwrite.TokenSource, retries int) (*giteadouble.Server, *gitwrite.Client) {
	t.Helper()
	d := giteadouble.New()
	srv := httptest.NewServer(d)
	t.Cleanup(srv.Close)
	c, err := gitwrite.New(gitwrite.Config{BaseURL: srv.URL, Tokens: tokens, RobotDomain: "robots." + fqdn, MaxRetries: retries})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return d, c
}

func p(name string) string { return "clusters/" + fqdn + "/intent/" + name }

func change(files ...gitwrite.FileChange) gitwrite.Change {
	return gitwrite.Change{
		Scope:    gitwrite.FleetIntent(fqdn),
		Repo:     repo,
		Files:    files,
		Summary:  "intent: test change",
		ChangeID: gitwrite.NewChangeID(),
		Via:      gitwrite.ViaAPI,
	}
}

func upsert(path, content string) gitwrite.FileChange {
	return gitwrite.FileChange{Path: path, Op: gitwrite.OpUpsert, Content: []byte(content)}
}

// TestCommitAllFilesInOneCall: three files, one batch call, one commit,
// and each result carries the blob SHA Read returns.
func TestCommitAllFilesInOneCall(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	ctx := context.Background()
	res, err := c.Commit(ctx, change(upsert(p("a.yaml"), "a: 1\n"), upsert(p("b.yaml"), "b: 1\n"), upsert(p("c/d.yaml"), "d: 1\n")))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if !res.Committed || res.Commit != d.Head("openova", "sovereign-regions", "main") || len(res.Files) != 3 {
		t.Fatalf("result = %+v", res)
	}
	if n := d.Calls("change"); n != 1 {
		t.Fatalf("batch calls = %d, want 1", n)
	}
	if got := d.Commits(); len(got) != 1 || len(got[0].Paths) != 3 {
		t.Fatalf("commits = %+v", got)
	}
	for _, f := range res.Files {
		_, sha, err := c.Read(ctx, gitwrite.FleetIntent(fqdn), repo, f.Path)
		if err != nil || sha == "" || sha != f.BlobSHA {
			t.Errorf("%s: read sha %q, result sha %q, err %v", f.Path, sha, f.BlobSHA, err)
		}
	}
}

// TestCommitAtomic: a file moves on the server between the read and the
// write; Gitea refuses the batch, and the new file of the same batch is
// not applied either.
func TestCommitAtomic(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	d.Put("openova", "sovereign-regions", "main", p("a.yaml"), []byte("a: 1\n"))
	before := giteadouble.BlobSHA([]byte("a: 1\n"))
	d.BeforeChange = func() { d.Put("openova", "sovereign-regions", "main", p("a.yaml"), []byte("a: 2\n")) }
	ch := change(upsert(p("new.yaml"), "n: 1\n"), gitwrite.FileChange{Path: p("a.yaml"), Op: gitwrite.OpUpsert, Content: []byte("a: 3\n"), IfMatch: before})
	_, err := c.Commit(context.Background(), ch)
	var ce *gitwrite.ConflictError
	if !errors.As(err, &ce) || ce.Path != p("a.yaml") || ce.Want != before || ce.Current != giteadouble.BlobSHA([]byte("a: 2\n")) {
		t.Fatalf("want a conflict on a.yaml, got %v", err)
	}
	files := d.Files("openova", "sovereign-regions", "main")
	if _, ok := files[p("new.yaml")]; ok {
		t.Fatal("new.yaml was applied although the batch was refused")
	}
	if string(files[p("a.yaml")]) != "a: 2\n" || len(d.Commits()) != 0 {
		t.Fatalf("files = %v, commits = %d", files, len(d.Commits()))
	}
}

// TestCommitNoOp: every upsert equals the file and every delete targets
// an absent file, so nothing is committed and Commit is the head.
func TestCommitNoOp(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	d.Put("openova", "sovereign-regions", "main", p("a.yaml"), []byte("a: 1\n"))
	head := d.Head("openova", "sovereign-regions", "main")
	res, err := c.Commit(context.Background(), change(upsert(p("a.yaml"), "a: 1\n"), gitwrite.FileChange{Path: p("gone.yaml"), Op: gitwrite.OpDelete}))
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.Committed || res.Commit != head || d.Calls("change") != 0 {
		t.Fatalf("result %+v, batch calls %d", res, d.Calls("change"))
	}
	if res.Files[0].BlobSHA != giteadouble.BlobSHA([]byte("a: 1\n")) || res.Files[1].BlobSHA != "" {
		t.Fatalf("files = %+v", res.Files)
	}
}

// TestCommitSendsOnlyChangedFiles: an unchanged file is not sent; a
// delete of an existing file is.
func TestCommitSendsOnlyChangedFiles(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	d.Put("openova", "sovereign-regions", "main", p("same.yaml"), []byte("s: 1\n"))
	d.Put("openova", "sovereign-regions", "main", p("old.yaml"), []byte("o: 1\n"))
	res, err := c.Commit(context.Background(), change(upsert(p("same.yaml"), "s: 1\n"), upsert(p("new.yaml"), "n: 1\n"), gitwrite.FileChange{Path: p("old.yaml"), Op: gitwrite.OpDelete}))
	if err != nil || !res.Committed {
		t.Fatalf("Commit: %+v %v", res, err)
	}
	got := d.Commits()
	if len(got) != 1 || strings.Join(got[0].Paths, ",") != p("new.yaml")+","+p("old.yaml") {
		t.Fatalf("commits = %+v", got)
	}
	if _, ok := d.Files("openova", "sovereign-regions", "main")[p("old.yaml")]; ok {
		t.Fatal("old.yaml was not deleted")
	}
	if res.Files[0].BlobSHA == "" || res.Files[1].BlobSHA == "" || res.Files[2].BlobSHA != "" {
		t.Fatalf("files = %+v", res.Files)
	}
}

// TestCommitIfMatch: each IfMatch is checked against the file read before
// the call; a mismatch is a *ConflictError and no batch is sent.
func TestCommitIfMatch(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	d.Put("openova", "sovereign-regions", "main", p("a.yaml"), []byte("a: 1\n"))
	sha := giteadouble.BlobSHA([]byte("a: 1\n"))
	cases := []struct {
		name          string
		file          gitwrite.FileChange
		want, current string
	}{
		{"stale sha", gitwrite.FileChange{Path: p("a.yaml"), Op: gitwrite.OpUpsert, Content: []byte("a: 2\n"), IfMatch: "0000"}, "0000", sha},
		{"must be absent", gitwrite.FileChange{Path: p("a.yaml"), Op: gitwrite.OpUpsert, Content: []byte("a: 2\n"), IfMatch: gitwrite.AbsentSHA}, gitwrite.AbsentSHA, sha},
		{"absent file", gitwrite.FileChange{Path: p("b.yaml"), Op: gitwrite.OpUpsert, Content: []byte("b: 1\n"), IfMatch: sha}, sha, ""},
		{"delete stale", gitwrite.FileChange{Path: p("a.yaml"), Op: gitwrite.OpDelete, IfMatch: "0000"}, "0000", sha},
	}
	for _, tc := range cases {
		_, err := c.Commit(context.Background(), change(tc.file))
		var ce *gitwrite.ConflictError
		if !errors.As(err, &ce) || ce.Path != tc.file.Path || ce.Want != tc.want || ce.Current != tc.current || !gitwrite.IsConflict(err) {
			t.Errorf("%s: got %v", tc.name, err)
		}
	}
	if d.Calls("change") != 0 {
		t.Fatalf("a batch was sent: %d", d.Calls("change"))
	}
	res, err := c.Commit(context.Background(), change(gitwrite.FileChange{Path: p("a.yaml"), Op: gitwrite.OpUpsert, Content: []byte("a: 2\n"), IfMatch: sha}))
	if err != nil || !res.Committed {
		t.Fatalf("matching IfMatch: %+v %v", res, err)
	}
	if _, err := c.Commit(context.Background(), change(gitwrite.FileChange{Path: p("c.yaml"), Op: gitwrite.OpUpsert, Content: []byte("c: 1\n"), IfMatch: gitwrite.AbsentSHA})); err != nil {
		t.Fatalf("AbsentSHA on an absent file: %v", err)
	}
}

// TestCommitRetriesWithoutIfMatch: with no IfMatch, a server-side
// mismatch is retried from the read, and the retry wins.
func TestCommitRetriesWithoutIfMatch(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	d.Put("openova", "sovereign-regions", "main", p("a.yaml"), []byte("a: 1\n"))
	var once sync.Once
	d.BeforeChange = func() {
		once.Do(func() { d.Put("openova", "sovereign-regions", "main", p("a.yaml"), []byte("a: 2\n")) })
	}
	res, err := c.Commit(context.Background(), change(upsert(p("a.yaml"), "a: 3\n")))
	if err != nil || !res.Committed {
		t.Fatalf("Commit: %+v %v", res, err)
	}
	if d.Calls("change") != 2 || string(d.Files("openova", "sovereign-regions", "main")[p("a.yaml")]) != "a: 3\n" {
		t.Fatalf("batch calls %d, files %v", d.Calls("change"), d.Files("openova", "sovereign-regions", "main"))
	}
}

// TestCommitRetryLimit: MaxRetries retries, then the conflict is returned.
func TestCommitRetryLimit(t *testing.T) {
	d, c := setup(t, robotTokens{}, 2)
	d.Put("openova", "sovereign-regions", "main", p("a.yaml"), []byte("a: 0\n"))
	var mu sync.Mutex
	n := 0
	d.BeforeChange = func() {
		mu.Lock()
		n++
		v := n
		mu.Unlock()
		d.Put("openova", "sovereign-regions", "main", p("a.yaml"), []byte("a: "+strings.Repeat("x", v)+"\n"))
	}
	_, err := c.Commit(context.Background(), change(upsert(p("a.yaml"), "a: 9\n")))
	if !gitwrite.IsConflict(err) {
		t.Fatalf("want a conflict, got %v", err)
	}
	if d.Calls("change") != 3 {
		t.Fatalf("batch calls = %d, want 3 (one call and two retries)", d.Calls("change"))
	}
}

// TestCommitForbiddenPath: a path outside the scope or of a bad shape is
// refused before any network call.
func TestCommitForbiddenPath(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	for _, path := range []string{
		"clusters/other.example.org/intent/a.yaml",
		"regions/r1/a.yaml",
		p("../../regions/a.yaml"),
		"/" + p("a.yaml"),
		p("x//a.yaml"),
		p("./a.yaml"),
		p("dir/"),
	} {
		if _, err := c.Commit(context.Background(), change(upsert(path, "a: 1\n"))); !errors.Is(err, gitwrite.ErrForbiddenPath) {
			t.Errorf("%q: got %v", path, err)
		}
	}
	empty := change(upsert(p("a.yaml"), "a: 1\n"))
	empty.Scope.Prefixes = nil
	if _, err := c.Commit(context.Background(), empty); !errors.Is(err, gitwrite.ErrForbiddenPath) {
		t.Errorf("a scope without prefixes: got %v", err)
	}
	if _, _, err := c.Read(context.Background(), gitwrite.FleetIntent(fqdn), repo, "a/../b"); !errors.Is(err, gitwrite.ErrForbiddenPath) {
		t.Errorf("Read with ..: got %v", err)
	}
	if n := d.Calls(""); n != 0 {
		t.Fatalf("%d network calls, want 0", n)
	}
}

// TestCommitRefusesSecrets: a document with apiVersion v1 and kind Secret
// in a YAML or JSON file is refused, whatever else the file holds.
func TestCommitRefusesSecrets(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	refused := map[string]string{
		"multi.yaml":  "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n---\napiVersion: v1\nkind: Secret\nmetadata:\n  name: b\n",
		"after.yml":   "- not a map\n---\nkind: Secret\napiVersion: v1\n",
		"plain.json":  `{"apiVersion": "v1", "kind": "Secret", "metadata": {"name": "c"}}`,
		"upper.YAML":  "apiVersion: v1\nkind: Secret\n",
		"nodata.yaml": "apiVersion: v1\nkind: Secret\nmetadata:\n  name: d\n",
	}
	for name, content := range refused {
		if _, err := c.Commit(context.Background(), change(upsert(p(name), content))); !errors.Is(err, gitwrite.ErrSecretContent) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if d.Calls("") != 0 {
		t.Fatalf("a refused change made %d network calls", d.Calls(""))
	}
	allowed := map[string]string{
		"other-group.yaml": "apiVersion: example.org/v1\nkind: Secret\n",
		"configmap.yaml":   "apiVersion: v1\nkind: ConfigMap\ndata:\n  kind: Secret\n",
		"broken.yaml":      "apiVersion: v1\nkind: Secret\n  bad: [indent\n",
		"notes.txt":        "apiVersion: v1\nkind: Secret\n",
	}
	for name, content := range allowed {
		if _, err := c.Commit(context.Background(), change(upsert(p(name), content))); err != nil {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := c.Commit(context.Background(), change(gitwrite.FileChange{Path: p("plain.json"), Op: gitwrite.OpDelete, Content: []byte(refused["plain.json"])})); err != nil {
		t.Errorf("a delete is not scanned: got %v", err)
	}
}

// TestCommitMessage: the message is exactly the summary, a blank line and
// the trailers, extra trailers sorted by key.
func TestCommitMessage(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	ch := change(upsert(p("a.yaml"), "a: 1\n"))
	ch.ChangeID = "01JD0000000000000000000000"
	ch.Via = gitwrite.ViaConsole
	ch.Author = gitwrite.Identity{Name: "Ann Example", Email: "ann@example.org"}
	ch.Trailers = map[string]string{"Organization": "acme", "Approved-By": "bob@example.org"}
	if _, err := c.Commit(context.Background(), ch); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	want := "intent: test change\n\nChange-Id: 01JD0000000000000000000000\nRequested-By: ann@example.org\nRequested-Via: console\nApproved-By: bob@example.org\nOrganization: acme"
	if got := d.Commits()[0].Message; got != want {
		t.Fatalf("message:\n%q\nwant\n%q", got, want)
	}
	robot := change(upsert(p("b.yaml"), "b: 1\n"))
	robot.ChangeID = "01JD0000000000000000000001"
	robot.Via = gitwrite.ViaController
	if _, err := c.Commit(context.Background(), robot); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	want = "intent: test change\n\nChange-Id: 01JD0000000000000000000001\nRequested-By: catalyst-fleet-bot\nRequested-Via: controller"
	if got := d.Commits()[1].Message; got != want {
		t.Fatalf("message:\n%q\nwant\n%q", got, want)
	}
}

// TestCommitIdentity: the committer is the scope's robot, or SharedRobot
// with SharedTokenSource; the author is the person, or the committer.
func TestCommitIdentity(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	ch := change(upsert(p("a.yaml"), "a: 1\n"))
	ch.Author = gitwrite.Identity{Name: "Ann Example", Email: "ann@example.org"}
	if _, err := c.Commit(context.Background(), ch); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if _, err := c.Commit(context.Background(), gitwrite.Change{
		Scope: gitwrite.OrgIntent("acme"), Repo: gitwrite.Repo{Owner: "acme", Name: "catalyst-tenant"},
		Files: []gitwrite.FileChange{upsert("intent/applications/site.yaml", "a: 1\n")}, Summary: "s",
		ChangeID: gitwrite.NewChangeID(), Via: gitwrite.ViaMCP,
	}); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	got := d.Commits()
	if got[0].CommitterName != "catalyst-fleet-bot" || got[0].CommitterEmail != "catalyst-fleet-bot@robots."+fqdn ||
		got[0].AuthorName != "Ann Example" || got[0].AuthorEmail != "ann@example.org" || got[0].Token != "tok-catalyst-fleet-bot" {
		t.Errorf("fleet commit = %+v", got[0])
	}
	if got[1].CommitterName != "acme-iac-bot" || got[1].AuthorName != "acme-iac-bot" || got[1].AuthorEmail != "acme-iac-bot@robots."+fqdn || got[1].Token != "tok-acme-iac-bot" {
		t.Errorf("organization commit = %+v", got[1])
	}

	sd, sc := setup(t, gitwrite.SharedTokenSource("shared-value"), 0)
	if _, err := sc.Commit(context.Background(), change(upsert(p("a.yaml"), "a: 1\n"))); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	s := sd.Commits()[0]
	if s.CommitterName != gitwrite.SharedRobot || s.CommitterEmail != "catalyst-shared@robots."+fqdn || s.AuthorName != gitwrite.SharedRobot || s.Token != "shared-value" ||
		!strings.Contains(s.Message, "\nRequested-By: catalyst-shared\n") {
		t.Errorf("shared commit = %+v", s)
	}
}

// TestCommitInvalid: every missing or malformed field is ErrInvalid and
// makes no network call.
func TestCommitInvalid(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	many := make([]gitwrite.FileChange, 201)
	for i := range many {
		many[i] = upsert(p("f"+strings.Repeat("x", i)+".yaml"), "a: 1\n")
	}
	cases := map[string]func(*gitwrite.Change){
		"no scope":         func(ch *gitwrite.Change) { ch.Scope = gitwrite.Scope{} },
		"no repo name":     func(ch *gitwrite.Change) { ch.Repo.Name = "" },
		"no files":         func(ch *gitwrite.Change) { ch.Files = nil },
		"201 files":        func(ch *gitwrite.Change) { ch.Files = many },
		"duplicate path":   func(ch *gitwrite.Change) { ch.Files = append(ch.Files, ch.Files[0]) },
		"empty path":       func(ch *gitwrite.Change) { ch.Files[0].Path = "" },
		"bad op":           func(ch *gitwrite.Change) { ch.Files[0].Op = "move" },
		"empty summary":    func(ch *gitwrite.Change) { ch.Summary = "" },
		"long summary":     func(ch *gitwrite.Change) { ch.Summary = strings.Repeat("s", 73) },
		"two-line summary": func(ch *gitwrite.Change) { ch.Summary = "a\nb" },
		"no change id":     func(ch *gitwrite.Change) { ch.ChangeID = "" },
		"bad change id":    func(ch *gitwrite.Change) { ch.ChangeID = "01jd0000000000000000000000" },
		"no via":           func(ch *gitwrite.Change) { ch.Via = "" },
		"unknown via":      func(ch *gitwrite.Change) { ch.Via = "cli" },
		"half author":      func(ch *gitwrite.Change) { ch.Author = gitwrite.Identity{Name: "Ann"} },
		"bad trailer key":  func(ch *gitwrite.Change) { ch.Trailers = map[string]string{"approved-by": "x"} },
		"fixed trailer":    func(ch *gitwrite.Change) { ch.Trailers = map[string]string{"Change-Id": "x"} },
		"two-line trailer": func(ch *gitwrite.Change) { ch.Trailers = map[string]string{"Note": "a\nb"} },
	}
	for name, mutate := range cases {
		ch := change(upsert(p("a.yaml"), "a: 1\n"))
		mutate(&ch)
		if _, err := c.Commit(context.Background(), ch); !errors.Is(err, gitwrite.ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	ok := change(upsert(p("a.yaml"), "a: 1\n"))
	ok.Summary = strings.Repeat("é", 72)
	if _, err := c.Commit(context.Background(), ok); err != nil {
		t.Errorf("72 characters: got %v", err)
	}
	if n := d.Calls("change"); n != 1 {
		t.Fatalf("batch calls = %d, want 1 (the valid change only)", n)
	}
}

// TestCommitErrors: 5xx, a connection error and a time-out are
// ErrUnavailable; 401 and 403 are the Gitea error, not ErrUnavailable.
func TestCommitErrors(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable} {
		d, c := setup(t, robotTokens{}, 0)
		d.Intercept = func(string, string) int { return status }
		if _, err := c.Commit(context.Background(), change(upsert(p("a.yaml"), "a: 1\n"))); !errors.Is(err, gitwrite.ErrUnavailable) {
			t.Errorf("%d: got %v", status, err)
		}
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		d, c := setup(t, robotTokens{}, 0)
		d.Intercept = func(_, kind string) int {
			if kind == "change" {
				return status
			}
			return 0
		}
		_, err := c.Commit(context.Background(), change(upsert(p("a.yaml"), "a: 1\n")))
		var he *gitea.HTTPError
		if errors.Is(err, gitwrite.ErrUnavailable) || !errors.As(err, &he) || he.Status != status {
			t.Errorf("%d: got %v", status, err)
		}
	}
	srv := httptest.NewServer(giteadouble.New())
	base := srv.URL
	srv.Close()
	c, err := gitwrite.New(gitwrite.Config{BaseURL: base, Tokens: robotTokens{}, RobotDomain: "robots." + fqdn})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.Commit(context.Background(), change(upsert(p("a.yaml"), "a: 1\n"))); !errors.Is(err, gitwrite.ErrUnavailable) {
		t.Errorf("connection refused: got %v", err)
	}
	_, live := setup(t, robotTokens{}, 0)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := live.Commit(ctx, change(upsert(p("a.yaml"), "a: 1\n"))); !errors.Is(err, gitwrite.ErrUnavailable) {
		t.Errorf("time-out: got %v", err)
	}
}

// TestReadAndList: Read returns content and SHA, ErrNotFound when absent;
// List returns direct entries, and an empty slice for a missing dir.
func TestReadAndList(t *testing.T) {
	d, c := setup(t, robotTokens{}, 0)
	s := gitwrite.FleetIntent(fqdn)
	d.Put("openova", "sovereign-regions", "main", p("a.yaml"), []byte("a: 1\n"))
	d.Put("openova", "sovereign-regions", "main", p("access/u.yaml"), []byte("u: 1\n"))
	content, sha, err := c.Read(context.Background(), s, repo, p("a.yaml"))
	if err != nil || string(content) != "a: 1\n" || sha != giteadouble.BlobSHA([]byte("a: 1\n")) {
		t.Fatalf("Read = %q %q %v", content, sha, err)
	}
	if _, _, err := c.Read(context.Background(), s, repo, p("none.yaml")); !errors.Is(err, gitwrite.ErrNotFound) {
		t.Fatalf("missing file: got %v", err)
	}
	entries, err := c.List(context.Background(), s, repo, "clusters/"+fqdn+"/intent/")
	if err != nil || len(entries) != 2 {
		t.Fatalf("List = %+v %v", entries, err)
	}
	if entries[0] != (gitwrite.Entry{Path: p("a.yaml"), BlobSHA: sha}) || entries[1].Path != p("access") || !entries[1].IsDir {
		t.Fatalf("entries = %+v", entries)
	}
	empty, err := c.List(context.Background(), s, repo, p("organizations"))
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("missing dir: %+v %v", empty, err)
	}
}

// TestNewChangeID: 26 characters of the ULID alphabet, time first, unique.
func TestNewChangeID(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := gitwrite.NewChangeID()
		if len(id) != 26 || id[0] > '7' || strings.Trim(id, "0123456789ABCDEFGHJKMNPQRSTVWXYZ") != "" || seen[id] {
			t.Fatalf("bad or repeated id %q", id)
		}
		seen[id] = true
	}
}

// TestScopes: the five constructors give the robots and prefixes of the contract.
func TestScopes(t *testing.T) {
	cases := []struct {
		got   gitwrite.Scope
		name  string
		robot string
		pre   string
	}{
		{gitwrite.FleetIntent("a.example.org"), "fleet-intent", "catalyst-fleet-bot", "clusters/a.example.org/intent/"},
		{gitwrite.OrgIntent("acme"), "org-intent", "acme-iac-bot", "intent/"},
		{gitwrite.Catalog(), "catalog", "catalyst-fleet-bot", ""},
		{gitwrite.FleetRendered("a.example.org"), "fleet-rendered", "catalyst-shared", "clusters/a.example.org/regions/"},
		{gitwrite.OrgRendered(), "org-rendered", "catalyst-shared", "regions/,vcluster/,continuum/"},
	}
	for _, tc := range cases {
		if tc.got.Name != tc.name || tc.got.Robot != tc.robot || strings.Join(tc.got.Prefixes, ",") != tc.pre {
			t.Errorf("%s: got %+v", tc.name, tc.got)
		}
	}
}

// TestNewConfig: required fields and negative values are ErrInvalid.
func TestNewConfig(t *testing.T) {
	good := gitwrite.Config{BaseURL: "http://gitea.invalid", Tokens: robotTokens{}, RobotDomain: "robots." + fqdn}
	for name, cfg := range map[string]gitwrite.Config{
		"no base url":      {Tokens: good.Tokens, RobotDomain: good.RobotDomain},
		"no tokens":        {BaseURL: good.BaseURL, RobotDomain: good.RobotDomain},
		"no robot domain":  {BaseURL: good.BaseURL, Tokens: good.Tokens},
		"negative timeout": {BaseURL: good.BaseURL, Tokens: good.Tokens, RobotDomain: good.RobotDomain, HTTPTimeout: -1},
		"negative retries": {BaseURL: good.BaseURL, Tokens: good.Tokens, RobotDomain: good.RobotDomain, MaxRetries: -1},
	} {
		if _, err := gitwrite.New(cfg); !errors.Is(err, gitwrite.ErrInvalid) {
			t.Errorf("%s: got %v", name, err)
		}
	}
	if _, err := gitwrite.New(good); err != nil {
		t.Fatalf("good config: %v", err)
	}
}

// TestConflictError: the message names the path, and IsConflict sees a wrapped one.
func TestConflictError(t *testing.T) {
	err := error(&gitwrite.ConflictError{Path: "intent/a.yaml", Want: "abc", Current: ""})
	if err.Error() != "gitwrite: intent/a.yaml changed: want abc, current absent" {
		t.Fatalf("Error() = %q", err.Error())
	}
	if !gitwrite.IsConflict(errors.Join(errors.New("x"), err)) || gitwrite.IsConflict(gitwrite.ErrInvalid) {
		t.Fatal("IsConflict")
	}
}
