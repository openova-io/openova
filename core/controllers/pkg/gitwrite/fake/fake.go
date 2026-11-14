// Package fake provides an in-memory gitwrite.Writer for tests.
//
// fake.New() runs the real gitwrite client against an in-memory Gitea
// served on a loopback listener of the test process, so its behaviour is
// the library's own: the field checks, the path policy, the Secret
// refusal, compare-and-swap, the no-op and the commit message. Every
// repository and branch exists and starts empty. The committer of a
// commit is the robot of its scope, with the e-mail domain
// "robots.fake.invalid". A Change in gitwrite.ModePullRequest commits to
// its branch and opens a pull request, which the fake never merges.
package fake

import (
	"context"
	"net/http/httptest"

	"github.com/openova-io/openova/core/controllers/pkg/gitwrite"
	"github.com/openova-io/openova/core/controllers/pkg/gitwrite/internal/giteadouble"
)

// RobotDomain is the e-mail domain of the robot identities of the fake.
const RobotDomain = "robots.fake.invalid"

// Commit is one commit the fake made.
type Commit struct {
	Repo      gitwrite.Repo
	SHA       string
	Message   string
	Author    gitwrite.Identity
	Committer gitwrite.Identity
	Paths     []string // the paths the commit changed, in the order of the Change
}

// Writer is the in-memory gitwrite.Writer.
type Writer struct {
	double *giteadouble.Server
	server *httptest.Server
	client *gitwrite.Client
}

var _ gitwrite.Writer = (*Writer)(nil)

// robotTokens gives every robot a token of its own, so that the
// committer is the robot of the scope.
type robotTokens struct{}

func (robotTokens) Token(_ context.Context, robot string) (string, error) {
	return "fake-" + robot, nil
}

// New returns an empty Writer. Call Close when the test ends.
func New() *Writer {
	d := giteadouble.New()
	srv := httptest.NewServer(d)
	c, err := gitwrite.New(gitwrite.Config{BaseURL: srv.URL, Tokens: robotTokens{}, RobotDomain: RobotDomain})
	if err != nil {
		srv.Close()
		panic("gitwrite/fake: " + err.Error())
	}
	return &Writer{double: d, server: srv, client: c}
}

// Close stops the loopback listener.
func (w *Writer) Close() {
	w.server.Close()
}

// Commit applies all files in one commit or none.
func (w *Writer) Commit(ctx context.Context, c gitwrite.Change) (gitwrite.Result, error) {
	return w.client.Commit(ctx, c)
}

// Read returns a file and its blob SHA. A missing file returns gitwrite.ErrNotFound.
func (w *Writer) Read(ctx context.Context, s gitwrite.Scope, r gitwrite.Repo, path string) ([]byte, string, error) {
	return w.client.Read(ctx, s, r, path)
}

// List returns the entries directly under dir. A missing dir returns an empty slice.
func (w *Writer) List(ctx context.Context, s gitwrite.Scope, r gitwrite.Repo, dir string) ([]gitwrite.Entry, error) {
	return w.client.List(ctx, s, r, dir)
}

// Put writes a file without a commit of the library, as test set-up or
// as another writer. It is not listed by Commits.
func (w *Writer) Put(r gitwrite.Repo, path string, content []byte) {
	w.double.Put(r.Owner, r.Name, branchOf(r), path, content)
}

// Commits returns the commits made through Commit, oldest first.
func (w *Writer) Commits() []Commit {
	var out []Commit
	for _, c := range w.double.Commits() {
		out = append(out, Commit{
			Repo:      gitwrite.Repo{Owner: c.Owner, Name: c.Repo, Branch: c.Branch},
			SHA:       c.SHA,
			Message:   c.Message,
			Author:    gitwrite.Identity{Name: c.AuthorName, Email: c.AuthorEmail},
			Committer: gitwrite.Identity{Name: c.CommitterName, Email: c.CommitterEmail},
			Paths:     c.Paths,
		})
	}
	return out
}

func branchOf(r gitwrite.Repo) string {
	if r.Branch == "" {
		return "main"
	}
	return r.Branch
}
