package gitwrite

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"github.com/openova-io/openova/core/controllers/pkg/gitea"
)

const maxFiles = 200

var trailerKey = regexp.MustCompile(`^[A-Z][A-Za-z-]*$`)

// fixedTrailers are written by Commit itself; Change.Trailers may not repeat them.
var fixedTrailers = map[string]bool{"Change-Id": true, "Requested-By": true, "Requested-Via": true}

// current is the state of one file as read before a commit.
type current struct {
	exists  bool
	sha     string
	content []byte
}

// Commit applies all files of ch in one commit or none. The order is:
// field checks, path policy, Secret scan, read of the current files,
// no-op detection, IfMatch check, one ChangeFiles call. When Gitea
// refuses the call because a file moved after the read, Commit returns
// *ConflictError if any file has IfMatch, and otherwise starts again
// from the read, at most MaxRetries times. A change in ModePullRequest
// goes to commitPullRequest after the Secret scan.
func (c *Client) Commit(ctx context.Context, ch Change) (Result, error) {
	if err := checkFields(ch); err != nil {
		return Result{}, err
	}
	if err := checkPaths(ch); err != nil {
		return Result{}, err
	}
	if err := checkSecrets(ch); err != nil {
		return Result{}, err
	}
	gc, err := c.gitea(ctx, ch.Scope.Robot)
	if err != nil {
		return Result{}, err
	}
	branch := branchOf(ch.Repo)
	committer := c.committer(ch.Scope)
	author := committer
	requestedBy := committer.Name
	if ch.Author != (Identity{}) {
		author = ch.Author
		requestedBy = ch.Author.Email
	}
	message := commitMessage(ch, requestedBy)
	if ch.Mode == ModePullRequest {
		return commitPullRequest(ctx, gc, ch, message, author, committer)
	}
	withIfMatch := false
	for _, f := range ch.Files {
		if f.IfMatch != "" {
			withIfMatch = true
		}
	}

	for attempt := 0; ; attempt++ {
		cur, err := readFiles(ctx, gc, ch.Repo, branch, ch.Files)
		if err != nil {
			return Result{}, err
		}
		ops, results := planOps(ch.Files, cur)
		if len(ops) == 0 {
			head, err := gc.BranchHead(ctx, ch.Repo.Owner, ch.Repo.Name, branch)
			if err != nil {
				return Result{}, fmt.Errorf("gitwrite: read the head of %s: %w", branch, classify(ctx, err))
			}
			return Result{Commit: head, Committed: false, Files: results}, nil
		}
		if err := checkIfMatch(ch.Files, cur); err != nil {
			return Result{}, err
		}
		resp, err := gc.ChangeFiles(ctx, ch.Repo.Owner, ch.Repo.Name, gitea.ChangeFilesRequest{
			Branch:    branch,
			Message:   message,
			Author:    gitea.CommitIdentity{Name: author.Name, Email: author.Email},
			Committer: gitea.CommitIdentity{Name: committer.Name, Email: committer.Email},
			Files:     ops,
		})
		if err == nil {
			written := map[string]string{}
			for _, f := range resp.Files {
				written[f.Path] = f.SHA
			}
			for i, f := range ch.Files {
				if sha, ok := written[f.Path]; ok && f.Op == OpUpsert {
					results[i].BlobSHA = sha
				}
			}
			return Result{Commit: resp.CommitSHA, Committed: true, Files: results}, nil
		}
		if !mayBeMismatch(err) {
			return Result{}, fmt.Errorf("gitwrite: commit: %w", classify(ctx, err))
		}
		moved, rerr := firstMoved(ctx, gc, ch, branch, cur)
		if rerr != nil {
			return Result{}, rerr
		}
		if moved == nil {
			return Result{}, fmt.Errorf("gitwrite: commit: %w", classify(ctx, err))
		}
		if withIfMatch || attempt >= c.cfg.MaxRetries {
			return Result{}, moved
		}
	}
}

// Read returns the file at path and its blob SHA. The scope selects the
// robot whose token is used; the path is not checked against its prefixes.
func (c *Client) Read(ctx context.Context, s Scope, r Repo, path string) ([]byte, string, error) {
	if s.Robot == "" || r.Owner == "" || r.Name == "" || path == "" {
		return nil, "", fmt.Errorf("%w: Read needs a scope robot, a repository and a path", ErrInvalid)
	}
	if !validPath(path) {
		return nil, "", fmt.Errorf("%w: %q", ErrForbiddenPath, path)
	}
	gc, err := c.gitea(ctx, s.Robot)
	if err != nil {
		return nil, "", err
	}
	f, err := gc.GetFile(ctx, r.Owner, r.Name, branchOf(r), path)
	if err != nil {
		if errors.Is(err, gitea.ErrFileNotFound) || errors.Is(err, gitea.ErrRepoNotFound) {
			return nil, "", fmt.Errorf("%w: %s: %w", ErrNotFound, path, err)
		}
		return nil, "", fmt.Errorf("gitwrite: read %s: %w", path, classify(ctx, err))
	}
	content, err := f.Decoded()
	if err != nil {
		return nil, "", fmt.Errorf("gitwrite: decode %s: %w", path, err)
	}
	return content, f.SHA, nil
}

// List returns the entries directly under dir, in the order Gitea gives.
// dir "" is the repository root; one trailing slash is ignored. A missing
// dir returns an empty slice; a missing repository returns ErrNotFound.
func (c *Client) List(ctx context.Context, s Scope, r Repo, dir string) ([]Entry, error) {
	if s.Robot == "" || r.Owner == "" || r.Name == "" {
		return nil, fmt.Errorf("%w: List needs a scope robot and a repository", ErrInvalid)
	}
	dir = strings.TrimSuffix(dir, "/")
	if dir != "" && !validPath(dir) {
		return nil, fmt.Errorf("%w: %q", ErrForbiddenPath, dir)
	}
	gc, err := c.gitea(ctx, s.Robot)
	if err != nil {
		return nil, err
	}
	list, err := gc.ListContents(ctx, r.Owner, r.Name, branchOf(r), dir)
	if err != nil {
		if errors.Is(err, gitea.ErrFileNotFound) {
			return []Entry{}, nil
		}
		if errors.Is(err, gitea.ErrRepoNotFound) {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		return nil, fmt.Errorf("gitwrite: list %s: %w", dir, classify(ctx, err))
	}
	out := make([]Entry, 0, len(list))
	for _, e := range list {
		out = append(out, Entry{Path: e.Path, BlobSHA: e.SHA, IsDir: e.Type == "dir"})
	}
	return out, nil
}

// gitea returns a Gitea client that carries the token of robot.
func (c *Client) gitea(ctx context.Context, robot string) (*gitea.Client, error) {
	token, err := c.cfg.Tokens.Token(ctx, robot)
	if err != nil {
		return nil, fmt.Errorf("gitwrite: token of %s: %w", robot, err)
	}
	if token == "" {
		return nil, fmt.Errorf("gitwrite: the token source gave an empty token for %s", robot)
	}
	return gitea.NewWithHTTP(c.cfg.BaseURL, token, c.hc), nil
}

// committer is the robot identity that commits for scope s.
func (c *Client) committer(s Scope) Identity {
	robot := s.Robot
	if c.shared {
		robot = SharedRobot
	}
	return Identity{Name: robot, Email: robot + "@" + c.cfg.RobotDomain}
}

func branchOf(r Repo) string {
	if r.Branch == "" {
		return "main"
	}
	return r.Branch
}

// checkFields is semantics 6 of the contract: every refusal wraps ErrInvalid.
func checkFields(ch Change) error {
	invalid := func(format string, args ...any) error {
		return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
	}
	if ch.Scope.Name == "" || ch.Scope.Robot == "" {
		return invalid("the scope needs a name and a robot")
	}
	if ch.Repo.Owner == "" || ch.Repo.Name == "" {
		return invalid("the repository needs an owner and a name")
	}
	if len(ch.Files) == 0 || len(ch.Files) > maxFiles {
		return invalid("%d files; a change has 1 to %d", len(ch.Files), maxFiles)
	}
	if n := utf8.RuneCountInString(ch.Summary); n < 1 || n > 72 || strings.ContainsAny(ch.Summary, "\r\n") {
		return invalid("the summary must be one line of 1 to 72 characters")
	}
	if !validChangeID(ch.ChangeID) {
		return invalid("change id %q is not a ULID from NewChangeID", ch.ChangeID)
	}
	switch ch.Via {
	case ViaConsole, ViaAPI, ViaMCP, ViaEvent, ViaController:
	default:
		return invalid("via %q is not one of console, api, mcp, event, controller", ch.Via)
	}
	switch ch.Mode {
	case "", ModeDirect:
	case ModePullRequest:
		if branchOf(ch.Repo) != "main" {
			return invalid("mode pull-request opens pull requests into main only, not into %q", ch.Repo.Branch)
		}
	default:
		return invalid("mode %q is not direct or pull-request", ch.Mode)
	}
	if (ch.Author.Name == "") != (ch.Author.Email == "") {
		return invalid("the author needs both a name and an e-mail, or neither")
	}
	for k, v := range ch.Trailers {
		if !trailerKey.MatchString(k) || fixedTrailers[k] {
			return invalid("trailer key %q", k)
		}
		if v == "" || strings.ContainsAny(v, "\r\n") {
			return invalid("trailer %s needs a value of one line", k)
		}
	}
	seen := map[string]bool{}
	for _, f := range ch.Files {
		if f.Path == "" {
			return invalid("a file has no path")
		}
		if f.Op != OpUpsert && f.Op != OpDelete {
			return invalid("op %q of %s", f.Op, f.Path)
		}
		if seen[f.Path] {
			return invalid("path %s occurs twice", f.Path)
		}
		seen[f.Path] = true
	}
	return nil
}

// validPath refuses a leading or trailing slash and an empty, "." or ".." element.
func validPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	for _, el := range strings.Split(p, "/") {
		if el == "" || el == "." || el == ".." {
			return false
		}
	}
	return true
}

// checkPaths is semantics 4 of the contract.
func checkPaths(ch Change) error {
	for _, f := range ch.Files {
		if !validPath(f.Path) || !underPrefix(f.Path, ch.Scope.Prefixes) {
			return fmt.Errorf("%w: %s (scope %s)", ErrForbiddenPath, f.Path, ch.Scope.Name)
		}
	}
	return nil
}

func underPrefix(p string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}

// checkSecrets is semantics 5 of the contract.
func checkSecrets(ch Change) error {
	for _, f := range ch.Files {
		if f.Op != OpUpsert {
			continue
		}
		lower := strings.ToLower(f.Path)
		if !strings.HasSuffix(lower, ".yaml") && !strings.HasSuffix(lower, ".yml") && !strings.HasSuffix(lower, ".json") {
			continue
		}
		if holdsSecret(f.Content) {
			return fmt.Errorf("%w: %s", ErrSecretContent, f.Path)
		}
	}
	return nil
}

// holdsSecret splits content into YAML documents (JSON is YAML) and
// reports whether one of them has apiVersion v1 and kind Secret. A
// document that does not parse is skipped; it holds no Secret object.
func holdsSecret(content []byte) bool {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(content)))
	for {
		doc, err := reader.Read()
		if err != nil {
			return false
		}
		var obj map[string]any
		if yaml.Unmarshal(doc, &obj) != nil {
			continue
		}
		if obj["apiVersion"] == "v1" && obj["kind"] == "Secret" {
			return true
		}
	}
}

// commitMessage is semantics 7 of the contract. The lines are joined by
// a newline; there is no newline after the last line.
func commitMessage(ch Change, requestedBy string) string {
	lines := []string{
		ch.Summary,
		"",
		"Change-Id: " + ch.ChangeID,
		"Requested-By: " + requestedBy,
		"Requested-Via: " + string(ch.Via),
	}
	keys := make([]string, 0, len(ch.Trailers))
	for k := range ch.Trailers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		lines = append(lines, k+": "+ch.Trailers[k])
	}
	return strings.Join(lines, "\n")
}

// readFiles reads the current state of every file of the change.
func readFiles(ctx context.Context, gc *gitea.Client, r Repo, branch string, files []FileChange) (map[string]current, error) {
	out := make(map[string]current, len(files))
	for _, f := range files {
		st, err := readOne(ctx, gc, r, branch, f.Path)
		if err != nil {
			return nil, err
		}
		out[f.Path] = st
	}
	return out, nil
}

func readOne(ctx context.Context, gc *gitea.Client, r Repo, branch, path string) (current, error) {
	file, err := gc.GetFile(ctx, r.Owner, r.Name, branch, path)
	if errors.Is(err, gitea.ErrFileNotFound) {
		return current{}, nil
	}
	if errors.Is(err, gitea.ErrRepoNotFound) {
		return current{}, fmt.Errorf("%w: repository %s/%s: %w", ErrNotFound, r.Owner, r.Name, err)
	}
	if err != nil {
		return current{}, fmt.Errorf("gitwrite: read %s: %w", path, classify(ctx, err))
	}
	content, err := file.Decoded()
	if err != nil {
		return current{}, fmt.Errorf("gitwrite: decode %s: %w", path, err)
	}
	return current{exists: true, sha: file.SHA, content: content}, nil
}

// planOps returns the operations of the files whose wanted state differs
// from cur, and a result per file in the order of files. A file that
// already has the wanted state is not sent.
func planOps(files []FileChange, cur map[string]current) ([]gitea.ChangeFile, []FileResult) {
	var ops []gitea.ChangeFile
	results := make([]FileResult, len(files))
	for i, f := range files {
		st := cur[f.Path]
		results[i] = FileResult{Path: f.Path}
		switch f.Op {
		case OpUpsert:
			results[i].BlobSHA = st.sha
			if st.exists && bytes.Equal(st.content, f.Content) {
				continue
			}
			op := gitea.ChangeFile{Operation: "create", Path: f.Path, ContentBase64: base64.StdEncoding.EncodeToString(f.Content)}
			if st.exists {
				op.Operation = "update"
				op.SHA = st.sha
			}
			ops = append(ops, op)
		case OpDelete:
			if st.exists {
				ops = append(ops, gitea.ChangeFile{Operation: "delete", Path: f.Path, SHA: st.sha})
			}
		}
	}
	return ops, results
}

// checkIfMatch is the first half of semantics 3: each IfMatch is compared
// with the blob SHA read before the commit.
func checkIfMatch(files []FileChange, cur map[string]current) error {
	for _, f := range files {
		if f.IfMatch == "" {
			continue
		}
		st := cur[f.Path]
		if f.IfMatch == AbsentSHA {
			if st.exists {
				return &ConflictError{Path: f.Path, Want: AbsentSHA, Current: st.sha}
			}
			continue
		}
		if !st.exists || st.sha != f.IfMatch {
			return &ConflictError{Path: f.Path, Want: f.IfMatch, Current: st.sha}
		}
	}
	return nil
}

// mayBeMismatch reports whether Gitea's refusal can be a file that moved
// after the read: HTTP 404, 409 or 422.
func mayBeMismatch(err error) bool {
	var he *gitea.HTTPError
	if !errors.As(err, &he) {
		return false
	}
	return he.Status == http.StatusNotFound || he.Status == http.StatusConflict || he.Status == http.StatusUnprocessableEntity
}

// firstMoved reads every file again, in the order of their paths, and
// returns a *ConflictError for the first one whose blob SHA is no longer
// the one read before the commit, or nil when none moved. Want is the
// file's IfMatch, or the SHA read before (AbsentSHA when it was absent).
func firstMoved(ctx context.Context, gc *gitea.Client, ch Change, branch string, before map[string]current) (*ConflictError, error) {
	files := append([]FileChange(nil), ch.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	for _, f := range files {
		now, err := readOne(ctx, gc, ch.Repo, branch, f.Path)
		if err != nil {
			return nil, err
		}
		was := before[f.Path]
		if now.exists == was.exists && now.sha == was.sha {
			continue
		}
		want := f.IfMatch
		if want == "" {
			want = was.sha
			if !was.exists {
				want = AbsentSHA
			}
		}
		return &ConflictError{Path: f.Path, Want: want, Current: now.sha}, nil
	}
	return nil, nil
}

// classify is semantics 10 of the contract: HTTP 5xx, a time-out or a
// connection error wraps ErrUnavailable; any other error, HTTP 401 and
// 403 among them, is returned as it is. A cancelled context is not
// unavailability.
func classify(ctx context.Context, err error) error {
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		return err
	}
	var he *gitea.HTTPError
	if errors.As(err, &he) {
		if he.Status >= 500 {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return err
	}
	var ue *url.Error
	if errors.As(err, &ue) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return err
}
